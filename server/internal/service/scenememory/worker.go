package scenememory

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	workerConcurrency  = 2
	workerPollInterval = 500 * time.Millisecond
)

// Flusher runs one claimed Scene Memory job. Nil means the worker will not
// claim rows — MarkDirty can still queue work for a later binary.
type Flusher interface {
	Flush(context.Context, db.SceneMemory) error
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
		"scene_memory_id", util.UUIDToString(row.ID),
		"scene_key", row.SceneKey,
		"attempt", row.AttemptCount,
	)
	if err := w.flusher.Flush(ctx, row); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return true, err
		}
		code := FlushErrorCode(err)
		if TerminalFlushCode(code) {
			if blockErr := w.store.Block(ctx, row, code, err.Error()); blockErr != nil && !errors.Is(blockErr, ErrLeaseLost) {
				return true, blockErr
			}
			return true, err
		}
		if retryErr := w.store.Retry(ctx, row, RetryDelayFor(code, row.AttemptCount), code, err.Error()); retryErr != nil && !errors.Is(retryErr, ErrLeaseLost) {
			return true, retryErr
		}
		return true, err
	}
	return true, nil
}
