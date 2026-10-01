package scenememory

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	workerConcurrency  = 2
	workerPollInterval = 500 * time.Millisecond
)

// Flusher runs one claimed Scene Memory job. Nil means the worker will not
// claim rows — MarkDirty can still queue work for a later binary.
type Flusher interface {
	Flush(context.Context, Memory) error
}

type Worker struct {
	store   *Store
	flusher Flusher
	allow   func() bool
	notify  chan struct{}
	done    chan struct{}
}

func NewWorker(store *Store, flusher Flusher, allow func() bool) *Worker {
	if allow == nil {
		allow = func() bool { return true }
	}
	return &Worker{
		store:   store,
		flusher: flusher,
		allow:   allow,
		notify:  make(chan struct{}, workerConcurrency),
		done:    make(chan struct{}),
	}
}

func (w *Worker) Notify() {
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *Worker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	defer close(w.done)
	var workers sync.WaitGroup
	workers.Add(workerConcurrency)
	for range workerConcurrency {
		go func() {
			defer workers.Done()
			w.runLoop(ctx)
		}()
	}
	workers.Wait()
}

func (w *Worker) WaitWithTimeout(timeout time.Duration) bool {
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

func (w *Worker) runLoop(ctx context.Context) {
	ticker := time.NewTicker(workerPollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("scene memory job failed", "error", err)
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

func (w *Worker) ProcessNext(ctx context.Context) (bool, error) {
	if w == nil || w.store == nil || w.flusher == nil || !w.allow() {
		return false, nil
	}
	row, err := w.store.Claim(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	slog.Info("scene memory claimed",
		"event", "scene_memory_claimed",
		"scene_id", util.UUIDToString(row.SceneID),
		"scene_key", row.ConversationID(),
		"attempt", row.AttemptCount,
	)
	if err := w.flusher.Flush(ctx, row); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return true, err
		}
		code := FlushErrorCode(err)
		var historyDetail any
		var historyErr *dwsclient.HistoryError
		if errors.As(err, &historyErr) {
			historyDetail = historyErr.DiagnosticFields()
		}
		slog.Warn("scene memory flush failed",
			"event", "scene_memory_flush_failed",
			"scene_id", util.UUIDToString(row.SceneID),
			"scene_key", row.ConversationID(),
			"agent_id", util.UUIDToString(row.AgentID),
			"workspace_id", util.UUIDToString(row.WorkspaceID),
			"attempt", row.AttemptCount,
			"error_code", code,
			"history_error", historyDetail,
			"error", err,
		)
		if BlockAfterFailure(code, err, row.AttemptCount) {
			blockErr := w.store.Block(ctx, row, code, err.Error())
			if blockErr != nil {
				if errors.Is(blockErr, ErrLeaseLost) {
					// The lease expired or a newer trigger arrived while this
					// flush ran; nothing was blocked, the row stays claimable.
					return true, err
				}
				return true, blockErr
			}
			// Error level: a blocked scene stops learning until a new
			// trigger from that scene arrives, so operators must see it.
			slog.Error("scene memory blocked",
				"event", "scene_memory_blocked",
				"scene_id", util.UUIDToString(row.SceneID),
				"scene_key", row.ConversationID(),
				"scene_title", row.Title(),
				"agent_id", util.UUIDToString(row.AgentID),
				"workspace_id", util.UUIDToString(row.WorkspaceID),
				"attempt", row.AttemptCount,
				"error_code", code,
				"terminal", TerminalFlushCode(code),
				"history_error", historyDetail,
				"error", err,
			)
			return true, err
		}
		if retryErr := w.store.Retry(ctx, row, RetryDelayFor(code, row.AttemptCount), code, err.Error()); retryErr != nil && !errors.Is(retryErr, ErrLeaseLost) {
			return true, retryErr
		}
		return true, err
	}
	return true, nil
}
