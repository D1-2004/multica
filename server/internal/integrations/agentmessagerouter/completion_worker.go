package agentmessagerouter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const (
	completionWorkerConcurrency       = 2
	completionWorkerPollInterval      = time.Second
	completionWorkerReconcileInterval = 15 * time.Second
	completionWorkerReconcileBatch    = 100
)

type CompletionReconciler interface {
	ReconcileTaskCompletions(context.Context, string, int32) (int, error)
}

// ResponseActionInterceptor durably hands managed replies to their owner
// before acknowledging the execution callback. It must be idempotent.
type ResponseActionInterceptor interface {
	PrepareExecutionResult(context.Context, string, ExecutionResultRequest) (bool, error)
	PrepareExecutionUpdate(context.Context, string, ExecutionUpdateRequest) (bool, error)
}

type CompletionWorker struct {
	queries         *db.Queries
	dwsSender       DWSReplySender
	client          ExecutionCallbackClient
	targetIdentity  string
	reconciler      CompletionReconciler
	ResponseActions ResponseActionInterceptor
	notify          chan struct{}
	done            chan struct{}
}

func NewCompletionWorker(
	queries *db.Queries,
	client ExecutionCallbackClient,
	reconciler CompletionReconciler,
) *CompletionWorker {
	targetIdentity := ""
	if client != nil {
		targetIdentity = client.TargetIdentity()
	}
	return &CompletionWorker{
		queries:        queries,
		client:         client,
		targetIdentity: targetIdentity,
		reconciler:     reconciler,
		notify:         make(chan struct{}, completionWorkerConcurrency),
		done:           make(chan struct{}),
	}
}

func (w *CompletionWorker) SetDWSReplySender(sender DWSReplySender) {
	w.dwsSender = sender
}

func (w *CompletionWorker) TargetIdentity() string {
	if w == nil {
		return ""
	}
	return w.targetIdentity
}

func (w *CompletionWorker) NotifyTaskCompletion() {
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *CompletionWorker) NotifyTaskExecutionUpdate() {
	w.NotifyTaskCompletion()
}

func (w *CompletionWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	defer close(w.done)
	if w.queries == nil || w.client == nil {
		return
	}

	var workers sync.WaitGroup
	workers.Add(completionWorkerConcurrency)
	for range completionWorkerConcurrency {
		go func() {
			defer workers.Done()
			w.runDeliveryLoop(ctx)
		}()
	}
	if w.reconciler != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			w.runReconcileLoop(ctx)
		}()
	}
	workers.Wait()
}

func (w *CompletionWorker) runDeliveryLoop(ctx context.Context) {
	ticker := time.NewTicker(completionWorkerPollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("task completion worker failed", "error", err)
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

func (w *CompletionWorker) runReconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(completionWorkerReconcileInterval)
	defer ticker.Stop()
	for {
		count, err := w.reconciler.ReconcileTaskCompletions(
			ctx,
			w.targetIdentity,
			completionWorkerReconcileBatch,
		)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("task completion reconcile failed", "error", err)
		}
		if count > 0 {
			w.NotifyTaskCompletion()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *CompletionWorker) WaitWithTimeout(timeout time.Duration) bool {
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

func (w *CompletionWorker) ProcessNext(ctx context.Context) (bool, error) {
	if w == nil || w.queries == nil || w.client == nil || w.targetIdentity == "" {
		return false, nil
	}
	worked, err := w.processNextExecutionUpdate(ctx)
	if worked || err != nil {
		return worked, err
	}
	return w.processNextCompletion(ctx)
}

func (w *CompletionWorker) processNextExecutionUpdate(ctx context.Context) (bool, error) {
	executionUpdate, err := w.queries.ClaimTaskExecutionUpdate(ctx, w.targetIdentity)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	update := ExecutionUpdateRequest{
		RequestID:      executionUpdate.RequestID,
		AgentID:        util.UUIDToString(executionUpdate.AgentID),
		ExternalTaskID: util.UUIDToString(executionUpdate.RootTaskID),
		UpdateType:     executionUpdate.UpdateType,
		OccurredAt:     executionUpdate.OccurredAt.Time.UnixMilli(),
		ResultMessage:  redact.Text(util.UnescapeBackslashEscapes(executionUpdate.ResultMessage.String)),
		Extension: ExecutionUpdateExtension{
			IssueID:         util.UUIDToString(executionUpdate.IssueID),
			IssueIdentifier: executionUpdate.IssueIdentifier,
			TargetTaskID:    util.UUIDToString(executionUpdate.TargetTaskID),
			TargetAgentID:   util.UUIDToString(executionUpdate.TargetAgentID),
		},
	}
	saveDelivery := func(raw []byte) error {
		_, err := w.queries.SaveTaskExecutionUpdateDWSDelivery(ctx, db.SaveTaskExecutionUpdateDWSDeliveryParams{
			ID: executionUpdate.ID, LeaseToken: executionUpdate.LeaseToken, DwsDelivery: raw,
		})
		return err
	}
	deliveryState := executionUpdate.DwsDelivery
	if len(deliveryState) == 0 {
		if w.ResponseActions != nil {
			_, err = w.ResponseActions.PrepareExecutionUpdate(ctx, executionUpdate.CallbackUrl, update)
		}
		var delivery *DWSDelivery
		if err == nil {
			delivery, err = w.client.SubmitExecutionUpdate(ctx, executionUpdate.CallbackUrl, update)
		}
		if err == nil && delivery != nil {
			deliveryState, err = freezeDWSDelivery(delivery, update.AgentID)
			if err == nil {
				err = saveDelivery(deliveryState)
			}
		}
	}
	if err == nil && len(deliveryState) > 0 {
		err = w.resumeDWSDelivery(ctx, deliveryState, saveDelivery)
	}
	if err == nil {
		_, completeErr := w.queries.CompleteTaskExecutionUpdate(ctx, db.CompleteTaskExecutionUpdateParams{
			ID:         executionUpdate.ID,
			LeaseToken: executionUpdate.LeaseToken,
		})
		if errors.Is(completeErr, pgx.ErrNoRows) {
			return true, nil
		}
		return true, completeErr
	}

	var deliveryErr *ExecutionResultDeliveryError
	var dwsPermanent *dwsDeliveryPermanentError
	if errors.As(err, &dwsPermanent) || errors.As(err, &deliveryErr) && !executionUpdateDeliveryRetryable(deliveryErr) {
		_, deadLetterErr := w.queries.DeadLetterTaskExecutionUpdate(ctx, db.DeadLetterTaskExecutionUpdateParams{
			ID:         executionUpdate.ID,
			LeaseToken: executionUpdate.LeaseToken,
			LastError:  pgtype.Text{String: err.Error(), Valid: true},
		})
		if errors.Is(deadLetterErr, pgx.ErrNoRows) {
			return true, nil
		}
		if deadLetterErr == nil {
			slog.Error("task execution update moved to dead letter",
				"execution_update_id", util.UUIDToString(executionUpdate.ID),
				"request_id", executionUpdate.RequestID,
				"error", err,
			)
		}
		return true, deadLetterErr
	}

	backoff := time.Second * time.Duration(1<<min(executionUpdate.AttemptCount, 8))
	_, retryErr := w.queries.RetryTaskExecutionUpdate(ctx, db.RetryTaskExecutionUpdateParams{
		ID:          executionUpdate.ID,
		LeaseToken:  executionUpdate.LeaseToken,
		AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(backoff), Valid: true},
		LastError:   pgtype.Text{String: err.Error(), Valid: true},
	})
	if errors.Is(retryErr, pgx.ErrNoRows) {
		return true, nil
	}
	if retryErr == nil {
		slog.Warn("task execution update delivery deferred",
			"execution_update_id", util.UUIDToString(executionUpdate.ID),
			"request_id", executionUpdate.RequestID,
			"attempt", executionUpdate.AttemptCount,
			"backoff", backoff,
			"error", err,
		)
	}
	return true, retryErr
}

func executionUpdateDeliveryRetryable(deliveryErr *ExecutionResultDeliveryError) bool {
	if deliveryErr == nil {
		return false
	}
	// updateUrl is only emitted by a Router version that implements this
	// endpoint, but a callback can still land on an older pod during a rolling
	// deployment. Keep the durable handoff queued until that pod drains.
	return deliveryErr.status == http.StatusNotFound || deliveryErr.Retryable()
}

func (w *CompletionWorker) processNextCompletion(ctx context.Context) (bool, error) {
	completion, err := w.queries.ClaimTaskCompletion(ctx, w.targetIdentity)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var executionSummary map[string]any
	if len(completion.ExecutionSummary) > 0 {
		if err := json.Unmarshal(completion.ExecutionSummary, &executionSummary); err != nil {
			return true, err
		}
	}
	var shouldReply *bool
	replyReason := ""
	if completion.ExecutionStatus == "completed" {
		if frozen, ok := executionSummary[protocol.TaskReplyDecisionSummaryKey].(map[string]any); ok {
			if value, exists := frozen["shouldReply"].(bool); exists {
				shouldReply = &value
				if reason, reasonOK := frozen["reason"].(string); reasonOK {
					replyReason = reason
				}
			}
		}
	}
	delete(executionSummary, protocol.TaskReplyDecisionSummaryKey)

	result := ExecutionResultRequest{
		RequestID:         completion.RequestID,
		AgentID:           util.UUIDToString(completion.AgentID),
		ExternalTaskID:    util.UUIDToString(completion.RootTaskID),
		ExternalRunID:     util.UUIDToString(completion.TerminalTaskID),
		ExternalSessionID: completion.ExternalSessionID.String,
		ExecutionStatus:   completion.ExecutionStatus,
		ResultMessage:     redact.Text(util.UnescapeBackslashEscapes(completion.ResultMessage)),
		ExecutionSummary:  executionSummary,
		ShouldReply:       shouldReply,
		ReplyReason:       replyReason,
		ExecutionResult: map[string]any{
			"terminalTaskId": util.UUIDToString(completion.TerminalTaskID),
			"error":          completion.Error.String,
			"failureReason":  completion.FailureReason.String,
		},
	}
	if completion.ExecutionStatus == "failed" && completion.FailureReason.String == "coordinator_job_failed" && strings.TrimSpace(completion.ResultMessage) != "" {
		shouldReply := true
		result.ShouldReply = &shouldReply
		result.ReplyReason = "coordinator_failure_reply"
	}

	saveDelivery := func(raw []byte) error {
		_, err := w.queries.SaveTaskCompletionDWSDelivery(ctx, db.SaveTaskCompletionDWSDeliveryParams{
			ID: completion.ID, LeaseToken: completion.LeaseToken, DwsDelivery: raw,
		})
		return err
	}
	deliveryState := completion.DwsDelivery
	if len(deliveryState) == 0 {
		if w.ResponseActions != nil {
			var managed bool
			managed, err = w.ResponseActions.PrepareExecutionResult(ctx, completion.CallbackUrl, result)
			if managed {
				shouldReply := false
				result.ShouldReply = &shouldReply
				result.ReplyReason = "multica_managed_response"
			}
		}
		var delivery *DWSDelivery
		if err == nil {
			delivery, err = w.client.SubmitExecutionResult(ctx, completion.CallbackUrl, result)
		}
		if err == nil && delivery != nil {
			deliveryState, err = freezeDWSDelivery(delivery, result.AgentID)
			if err == nil {
				err = saveDelivery(deliveryState)
			}
		}
	}
	if err == nil && len(deliveryState) > 0 {
		err = w.resumeDWSDelivery(ctx, deliveryState, saveDelivery)
	}
	if err == nil {
		_, completeErr := w.queries.CompleteTaskCompletion(ctx, db.CompleteTaskCompletionParams{
			ID:         completion.ID,
			LeaseToken: completion.LeaseToken,
		})
		if errors.Is(completeErr, pgx.ErrNoRows) {
			return true, nil
		}
		return true, completeErr
	}

	var deliveryErr *ExecutionResultDeliveryError
	var dwsPermanent *dwsDeliveryPermanentError
	dropAfterFirstFailure := len(deliveryState) == 0 && strings.HasPrefix(completion.RequestID, "multica-comment-terminal:")
	if errors.As(err, &dwsPermanent) || dropAfterFirstFailure || errors.As(err, &deliveryErr) && !deliveryErr.Retryable() {
		_, deadLetterErr := w.queries.DeadLetterTaskCompletion(ctx, db.DeadLetterTaskCompletionParams{
			ID:         completion.ID,
			LeaseToken: completion.LeaseToken,
			LastError:  pgtype.Text{String: err.Error(), Valid: true},
		})
		if errors.Is(deadLetterErr, pgx.ErrNoRows) {
			return true, nil
		}
		if deadLetterErr == nil {
			slog.Error("task completion moved to dead letter",
				"completion_id", util.UUIDToString(completion.ID),
				"request_id", completion.RequestID,
				"error", err,
			)
		}
		return true, deadLetterErr
	}

	backoff := time.Second * time.Duration(1<<min(completion.AttemptCount, 8))
	_, retryErr := w.queries.RetryTaskCompletion(ctx, db.RetryTaskCompletionParams{
		ID:          completion.ID,
		LeaseToken:  completion.LeaseToken,
		AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(backoff), Valid: true},
		LastError:   pgtype.Text{String: err.Error(), Valid: true},
	})
	if errors.Is(retryErr, pgx.ErrNoRows) {
		return true, nil
	}
	if retryErr == nil {
		slog.Warn("task completion delivery deferred",
			"completion_id", util.UUIDToString(completion.ID),
			"request_id", completion.RequestID,
			"attempt", completion.AttemptCount+1,
			"backoff", backoff,
			"error", err,
		)
	}
	return true, retryErr
}
