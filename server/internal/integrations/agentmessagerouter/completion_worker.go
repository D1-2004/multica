package agentmessagerouter

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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

type CompletionWorker struct {
	queries    *db.Queries
	client     *Client
	targetIdentity string
	reconciler CompletionReconciler
	notify     chan struct{}
	done       chan struct{}
}

func NewCompletionWorker(
	queries *db.Queries,
	client *Client,
	reconciler CompletionReconciler,
) *CompletionWorker {
	return &CompletionWorker{
		queries:    queries,
		client:     client,
		targetIdentity: client.TargetIdentity(),
		reconciler: reconciler,
		notify:     make(chan struct{}, completionWorkerConcurrency),
		done:       make(chan struct{}),
	}
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
	completion, err := w.queries.ClaimTaskCompletion(ctx, w.targetIdentity)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	result := ExecutionResultRequest{
		RequestID:         completion.RequestID,
		AgentID:           util.UUIDToString(completion.AgentID),
		ExternalTaskID:    util.UUIDToString(completion.RootTaskID),
		ExternalRunID:     util.UUIDToString(completion.TerminalTaskID),
		ExternalSessionID: completion.ExternalSessionID.String,
		ExecutionStatus:   completion.ExecutionStatus,
		ResultMessage:     redact.Text(util.UnescapeBackslashEscapes(completion.ResultMessage)),
		ExecutionResult: map[string]any{
			"terminalTaskId": util.UUIDToString(completion.TerminalTaskID),
			"error":          completion.Error.String,
			"failureReason":  completion.FailureReason.String,
		},
	}
	err = w.client.SubmitExecutionResult(ctx, completion.CallbackUrl, result)
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
	if errors.As(err, &deliveryErr) && !deliveryErr.Retryable() {
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
