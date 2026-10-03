package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	webhookWorkerPollInterval = time.Second
	webhookWorkerMaxAttempts  = 5
	webhookWorkerConcurrency  = 4
)

// WebhookDeliveryWorker owns queued webhook delivery dispatch. Its queue and
// lease both live in Postgres, so a process restart or replica failover simply
// reclaims expired rows; the in-memory notification is only a latency hint.
type WebhookDeliveryWorker struct {
	h      *Handler
	notify chan struct{}
	done   chan struct{}
}

func NewWebhookDeliveryWorker(h *Handler) *WebhookDeliveryWorker {
	return &WebhookDeliveryWorker{
		h:      h,
		notify: make(chan struct{}, webhookWorkerConcurrency),
		done:   make(chan struct{}),
	}
}

func (w *WebhookDeliveryWorker) Notify() {
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// Run owns a bounded per-process worker pool. SKIP LOCKED distributes queued
// deliveries across both these workers and other server replicas. ProcessNext
// is public to the package so handler tests can drive the durable queue
// synchronously without starting a goroutine.
func (w *WebhookDeliveryWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	defer close(w.done)
	if w.h == nil || w.h.Queries == nil {
		return
	}

	var workers sync.WaitGroup
	workers.Add(webhookWorkerConcurrency)
	for range webhookWorkerConcurrency {
		go func() {
			defer workers.Done()
			w.runLoop(ctx)
		}()
	}
	workers.Wait()
}

// runLoop polls as a recovery sweeper and also wakes immediately after local
// ingress. Each loop has its own ticker so an idle pool can fan back out even
// when burst notifications coalesce.
func (w *WebhookDeliveryWorker) runLoop(ctx context.Context) {
	ticker := time.NewTicker(webhookWorkerPollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("webhook worker: process delivery", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-w.notify:
		case <-ticker.C:
		}
	}
}

func (w *WebhookDeliveryWorker) WaitWithTimeout(timeout time.Duration) bool {
	if w == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return true
	case <-timer.C:
		return false
	}
}

func (w *WebhookDeliveryWorker) ProcessNext(ctx context.Context) (bool, error) {
	delivery, err := w.h.Queries.ClaimQueuedWebhookDelivery(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim queued delivery: %w", err)
	}

	if w.h.WebhookRateLimiter != nil {
		key := uuidToString(delivery.TriggerID)
		if !w.h.WebhookRateLimiter.Allow(ctx, key) {
			w.h.Metrics.RecordWebhookRateLimited("worker_trigger")
			retryAfter := webhookLimiterRetryAfter(ctx, w.h.WebhookRateLimiter, key)
			if retryAfter <= 0 {
				retryAfter = time.Second
			}
			_, err := w.h.Queries.DeferClaimedWebhookDelivery(ctx, db.DeferClaimedWebhookDeliveryParams{
				ID:          delivery.ID,
				LeaseToken:  delivery.LeaseToken,
				AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(retryAfter), Valid: true},
			})
			_, err = handleWebhookLeaseMutation("defer", delivery, err)
			return true, err
		}
	}

	trigger, err := w.h.Queries.GetAutopilotTrigger(ctx, delivery.TriggerID)
	if err != nil {
		return true, w.retryOrFail(ctx, delivery, fmt.Errorf("load trigger: %w", err))
	}
	autopilot, err := w.h.Queries.GetAutopilot(ctx, delivery.AutopilotID)
	if err != nil {
		return true, w.retryOrFail(ctx, delivery, fmt.Errorf("load autopilot: %w", err))
	}
	if uuidToString(trigger.AutopilotID) != uuidToString(delivery.AutopilotID) ||
		uuidToString(autopilot.WorkspaceID) != uuidToString(delivery.WorkspaceID) {
		return true, w.complete(ctx, delivery, deliveryStatusFailed, pgtype.UUID{}, "delivery ownership mismatch")
	}

	// Once ingress has synchronously admitted a run and returned accepted or
	// skipped, that decision is durable: the run keeps the payload and route
	// it was admitted with. Re-check mutable trigger/autopilot state only for
	// deliveries recovered from the pre-admission crash window; otherwise a
	// pause immediately after the response could strand the run.
	admitted, admissionErr := w.h.Queries.GetAutopilotRunByWebhookDelivery(ctx, delivery.ID)
	hasAdmittedRun := admissionErr == nil
	if admissionErr != nil && !errors.Is(admissionErr, pgx.ErrNoRows) {
		return true, w.retryOrFail(ctx, delivery, fmt.Errorf("load admitted run: %w", admissionErr))
	}
	payload := admitted.TriggerPayload
	if !hasAdmittedRun {
		// Rebuild the accepted input from the delivery row: the first
		// receivedAt and the stored body, proven by the frozen digest.
		source, err := w.h.loadWebhookFrozenSource(ctx, delivery)
		switch {
		case errors.Is(err, errWebhookSourceDrift):
			slog.Warn("webhook worker: stored delivery no longer rebuilds its accepted input",
				"delivery_id", uuidToString(delivery.ID),
				"trigger_id", uuidToString(delivery.TriggerID),
			)
			return true, w.complete(ctx, delivery, deliveryStatusFailed, pgtype.UUID{}, webhookSourceDigestMismatch)
		case errors.Is(err, errWebhookStoredBodyInvalid):
			return true, w.complete(ctx, delivery, deliveryStatusFailed, pgtype.UUID{}, err.Error())
		case err != nil:
			return true, w.retryOrFail(ctx, delivery, err)
		}
		payload = source.Payload
		switch {
		case !trigger.Enabled:
			return true, w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, "trigger_disabled")
		case autopilot.Status == "archived":
			return true, w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, "autopilot_archived")
		case autopilot.Status != "active":
			return true, w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, "autopilot_paused")
		case !webhookEventAllowedByTriggerScope(trigger.EventFilters, source.Envelope):
			return true, w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, "event_filtered")
		}
		current, problem, err := w.h.resolveWebhookEndpointBinding(ctx, autopilot, trigger.ID, delivery.Provider, trigger.SigningSecret.String)
		if err != nil {
			return true, w.retryOrFail(ctx, delivery, err)
		}
		if problem != "" {
			return true, w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, problem)
		}
		if source.Binding.Version > 0 && current.routeKey() != source.Binding.routeKey() {
			slog.Warn("webhook worker: endpoint route changed before admission",
				"delivery_id", uuidToString(delivery.ID),
				"trigger_id", uuidToString(delivery.TriggerID),
				"accepted_scene_id", source.Binding.SceneID,
				"current_scene_id", current.SceneID,
			)
			return true, w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, webhookBindingChanged)
		}
	} else {
		// An admitted run that has not reached its issue or task yet is
		// dispatched with the autopilot snapshot loaded above. It may only run
		// on the target it was accepted for: if the assignee, mode, routine
		// scene or tenant changed since, or the routine's scene is no longer
		// usable, the run is settled as skipped instead of being re-routed.
		pending, err := w.h.admittedWebhookRunAwaitsDispatch(ctx, admitted)
		if err != nil {
			return true, w.retryOrFail(ctx, delivery, err)
		}
		if pending {
			refusal, err := w.h.admittedWebhookRunRefusal(ctx, delivery, autopilot, trigger)
			if err != nil {
				return true, w.retryOrFail(ctx, delivery, err)
			}
			if refusal != "" {
				return true, w.refuseAdmittedRun(ctx, delivery, admitted, refusal)
			}
		}
	}

	// A delivery whose frozen binding selected the Employee path runs only
	// there; it never switches producer across retries.
	employee, err := w.h.frozenWebhookEmployeeDispatch(ctx, delivery.ID)
	if err != nil {
		return true, w.retryOrFail(ctx, delivery, err)
	}
	if employee {
		return true, w.dispatchEmployeeWebhook(ctx, delivery, autopilot, trigger, admitted, hasAdmittedRun)
	}

	run, dispatchErr := w.h.AutopilotService.DispatchAutopilotForWebhookDelivery(
		ctx,
		autopilot,
		trigger.ID,
		payload,
		delivery.ID,
	)
	if dispatchErr != nil {
		if run != nil {
			return true, w.complete(ctx, delivery, deliveryStatusFailed, run.ID, dispatchErr.Error())
		}
		return true, w.retryOrFail(ctx, delivery, dispatchErr)
	}
	if run.Status == "failed" {
		reason := "autopilot run failed"
		if run.FailureReason.Valid {
			reason = run.FailureReason.String
		}
		return true, w.complete(ctx, delivery, deliveryStatusFailed, run.ID, reason)
	}

	if err := w.h.Queries.TouchAutopilotTriggerFiredAt(ctx, trigger.ID); err != nil {
		slog.Warn("webhook worker: touch last_fired_at",
			"delivery_id", uuidToString(delivery.ID),
			"trigger_id", uuidToString(trigger.ID),
			"error", err,
		)
	}
	return true, w.complete(ctx, delivery, deliveryStatusDispatched, run.ID, "")
}

// employeeWebhookReadinessDelay is how long a delivery frozen for the
// Employee path waits while not every replica reads it (a rollback window).
const employeeWebhookReadinessDelay = 30 * time.Second

// dispatchEmployeeWebhook runs a delivery whose binding froze the Employee
// path: its digest-checked envelope, the frozen payload allowlist and route
// go to service.DispatchEmployeeWebhookRoutine, which admits one EmployeeTask
// Direct execution for the delivery (or replays the one it admitted).
func (w *WebhookDeliveryWorker) dispatchEmployeeWebhook(ctx context.Context, delivery db.WebhookDelivery, autopilot db.Autopilot, trigger db.AutopilotTrigger, admitted db.AutopilotRun, hasAdmittedRun bool) error {
	if err := w.h.EmployeeRoutineReady(ctx); err != nil {
		slog.Warn("webhook worker: employee path not ready; delivery waits",
			"delivery_id", uuidToString(delivery.ID), "reason", err.Error())
		_, deferErr := w.h.Queries.DeferClaimedWebhookDelivery(ctx, db.DeferClaimedWebhookDeliveryParams{
			ID: delivery.ID, LeaseToken: delivery.LeaseToken,
			AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(employeeWebhookReadinessDelay), Valid: true},
		})
		_, deferErr = handleWebhookLeaseMutation("defer", delivery, deferErr)
		return deferErr
	}
	settleFailed := func(reason string) error {
		if hasAdmittedRun && admitted.Status == "running" && !admitted.TaskID.Valid {
			if _, err := w.h.Queries.UpdateAutopilotRunFailed(ctx, db.UpdateAutopilotRunFailedParams{ID: admitted.ID, FailureReason: pgtype.Text{String: "webhook " + reason, Valid: true}}); err != nil {
				return w.retryOrFail(ctx, delivery, fmt.Errorf("settle run: %w", err))
			}
			return w.complete(ctx, delivery, deliveryStatusFailed, admitted.ID, reason)
		}
		return w.complete(ctx, delivery, deliveryStatusFailed, pgtype.UUID{}, reason)
	}
	source, err := w.h.loadWebhookFrozenSource(ctx, delivery)
	switch {
	case errors.Is(err, errWebhookSourceDrift):
		slog.Warn("webhook worker: stored delivery no longer rebuilds its accepted input",
			"delivery_id", uuidToString(delivery.ID), "trigger_id", uuidToString(delivery.TriggerID))
		return settleFailed(webhookSourceDigestMismatch)
	case errors.Is(err, errWebhookStoredBodyInvalid):
		return settleFailed(err.Error())
	case err != nil:
		return w.retryOrFail(ctx, delivery, err)
	}
	selected, problem, err := selectWebhookPayload(source.Envelope, source.Binding.PayloadFields)
	if err != nil {
		return w.retryOrFail(ctx, delivery, err)
	}
	run, err := w.h.AutopilotService.DispatchEmployeeWebhookRoutine(ctx, autopilot, service.WebhookRoutineDelivery{
		DeliveryID: delivery.ID, TriggerID: trigger.ID, IdentityPolicy: source.IdentityPolicy, ProviderEventID: source.EventID,
		ReceivedAt: source.ReceivedAt, SourceDigest: source.Digest, Event: source.Envelope.Event, Envelope: source.Payload,
		PayloadFields: source.Binding.PayloadFields, Payload: selected, SelectionError: problem,
		RoutineID: source.Binding.RoutineID, SceneID: source.Binding.SceneID, TenantOrgID: source.Binding.TenantOrgID,
	})
	if errors.Is(err, service.ErrWebhookRoutineGone) {
		slog.Warn("webhook worker: employee routine binding changed after acceptance",
			"delivery_id", uuidToString(delivery.ID), "trigger_id", uuidToString(delivery.TriggerID), "routine_id", source.Binding.RoutineID)
		if hasAdmittedRun && admitted.Status == "running" && !admitted.TaskID.Valid {
			return w.refuseAdmittedRun(ctx, delivery, admitted, webhookBindingChanged)
		}
		return w.complete(ctx, delivery, deliveryStatusIgnored, pgtype.UUID{}, webhookBindingChanged)
	}
	if err != nil {
		return w.retryOrFail(ctx, delivery, err)
	}
	if run.Status == "failed" {
		reason := "employee webhook occurrence failed"
		if run.FailureReason.Valid {
			reason = run.FailureReason.String
		}
		return w.complete(ctx, delivery, deliveryStatusFailed, run.ID, reason)
	}
	if err := w.h.Queries.TouchAutopilotTriggerFiredAt(ctx, trigger.ID); err != nil {
		slog.Warn("webhook worker: touch last_fired_at", "delivery_id", uuidToString(delivery.ID), "error", err)
	}
	return w.complete(ctx, delivery, deliveryStatusDispatched, run.ID, "")
}

// refuseAdmittedRun settles an admitted run that can no longer run on the
// target it was accepted for: the run is skipped with the reason and the
// delivery is ignored with the run linked, so nothing runs elsewhere.
func (w *WebhookDeliveryWorker) refuseAdmittedRun(ctx context.Context, delivery db.WebhookDelivery, run db.AutopilotRun, reason string) error {
	slog.Warn("webhook worker: admitted run refused, its accepted target changed",
		"delivery_id", uuidToString(delivery.ID),
		"trigger_id", uuidToString(delivery.TriggerID),
		"run_id", uuidToString(run.ID),
		"reason", reason,
	)
	if _, err := w.h.Queries.UpdateAutopilotRunSkipped(ctx, db.UpdateAutopilotRunSkippedParams{
		ID:            run.ID,
		FailureReason: pgtype.Text{String: "webhook " + reason + ": the accepted target changed before dispatch", Valid: true},
	}); err != nil {
		return w.retryOrFail(ctx, delivery, fmt.Errorf("settle refused run: %w", err))
	}
	return w.complete(ctx, delivery, deliveryStatusIgnored, run.ID, reason)
}

func (w *WebhookDeliveryWorker) complete(
	ctx context.Context,
	delivery db.WebhookDelivery,
	status string,
	runID pgtype.UUID,
	reason string,
) error {
	params := db.CompleteClaimedWebhookDeliveryParams{
		ID:             delivery.ID,
		LeaseToken:     delivery.LeaseToken,
		Status:         status,
		AutopilotRunID: runID,
	}
	if reason != "" {
		params.Error = pgtype.Text{String: reason, Valid: true}
	}
	_, err := w.h.Queries.CompleteClaimedWebhookDelivery(ctx, params)
	lost, err := handleWebhookLeaseMutation("complete", delivery, err)
	if err != nil {
		return err
	}
	if lost {
		return nil
	}
	w.h.Metrics.RecordWebhookDelivery(delivery.Provider, status)
	return nil
}

func (w *WebhookDeliveryWorker) retryOrFail(ctx context.Context, delivery db.WebhookDelivery, cause error) error {
	if delivery.DispatchAttempts+1 >= webhookWorkerMaxAttempts {
		return w.complete(ctx, delivery, deliveryStatusFailed, pgtype.UUID{}, cause.Error())
	}
	backoff := time.Second * time.Duration(1<<min(delivery.DispatchAttempts, 6))
	_, err := w.h.Queries.RetryClaimedWebhookDelivery(ctx, db.RetryClaimedWebhookDeliveryParams{
		ID:          delivery.ID,
		LeaseToken:  delivery.LeaseToken,
		AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(backoff), Valid: true},
		Error:       pgtype.Text{String: cause.Error(), Valid: true},
	})
	lost, mutationErr := handleWebhookLeaseMutation("retry", delivery, err)
	if mutationErr != nil {
		return fmt.Errorf("retry claimed delivery after %v: %w", cause, mutationErr)
	}
	if lost {
		return nil
	}
	slog.Warn("webhook worker: delivery deferred",
		"delivery_id", uuidToString(delivery.ID),
		"attempt", delivery.DispatchAttempts+1,
		"backoff", backoff,
		"error", cause,
	)
	return nil
}

// handleWebhookLeaseMutation normalizes the expected race where a slow worker
// outlives its lease and a new owner has already claimed or completed the row.
// The new owner is responsible for the terminal transition, so the stale
// worker must neither emit an error nor record completion metrics.
func handleWebhookLeaseMutation(operation string, delivery db.WebhookDelivery, err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Debug("webhook worker: lease ownership changed",
			"operation", operation,
			"delivery_id", uuidToString(delivery.ID),
		)
		return true, nil
	}
	return false, fmt.Errorf("%s claimed delivery: %w", operation, err)
}
