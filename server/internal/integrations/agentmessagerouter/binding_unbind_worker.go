package agentmessagerouter

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	bindingUnbindWorkerConcurrency  = 2
	bindingUnbindWorkerPollInterval = time.Second
)

type BindingUnbindStore interface {
	ClaimDingTalkBindingUnbind(context.Context, string) (db.DingtalkBindingUnbindOutbox, error)
	CompleteDingTalkBindingUnbind(context.Context, db.CompleteDingTalkBindingUnbindParams) (db.CompleteDingTalkBindingUnbindRow, error)
	RetryDingTalkBindingUnbind(context.Context, db.RetryDingTalkBindingUnbindParams) (db.DingtalkBindingUnbindOutbox, error)
}

type BindingUnbindRouter interface {
	UnbindDigitalEmployeeBinding(context.Context, DigitalEmployeeBindingKey) (DigitalEmployeeBindingUnbindResult, error)
}

type BindingUnbindWorker struct {
	store          BindingUnbindStore
	router         BindingUnbindRouter
	targetIdentity string
	now            func() time.Time
	notify         chan struct{}
	done           chan struct{}
}

func NewBindingUnbindWorker(store BindingUnbindStore, router BindingUnbindRouter, targetIdentity string) *BindingUnbindWorker {
	return &BindingUnbindWorker{
		store: store, router: router, targetIdentity: strings.TrimSpace(targetIdentity), now: time.Now,
		notify: make(chan struct{}, bindingUnbindWorkerConcurrency), done: make(chan struct{}),
	}
}

func (w *BindingUnbindWorker) Notify() {
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *BindingUnbindWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	defer close(w.done)
	if w.store == nil || w.router == nil || w.targetIdentity == "" {
		return
	}
	var workers sync.WaitGroup
	workers.Add(bindingUnbindWorkerConcurrency)
	for range bindingUnbindWorkerConcurrency {
		go func() {
			defer workers.Done()
			w.runLoop(ctx)
		}()
	}
	workers.Wait()
}

func (w *BindingUnbindWorker) runLoop(ctx context.Context) {
	ticker := time.NewTicker(bindingUnbindWorkerPollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("dingtalk binding unbind worker failed", "error", err)
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

func (w *BindingUnbindWorker) WaitWithTimeout(timeout time.Duration) bool {
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

func (w *BindingUnbindWorker) ProcessNext(ctx context.Context) (bool, error) {
	if w == nil || w.store == nil || w.router == nil || w.targetIdentity == "" {
		return false, nil
	}
	intent, err := w.store.ClaimDingTalkBindingUnbind(ctx, w.targetIdentity)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := w.router.UnbindDigitalEmployeeBinding(ctx, DigitalEmployeeBindingKey{
		AgentID: util.UUIDToString(intent.AgentID), Platform: intent.Platform,
		TenantID: intent.TenantID, AccountID: intent.AccountID,
	})
	if err != nil {
		return true, w.retry(ctx, intent, bindingUnbindRetryCode(err))
	}
	switch result.Status {
	case "unbound", "ownership_changed":
		return true, w.complete(ctx, intent)
	case "inconsistent":
		return true, w.retry(ctx, intent, "inconsistent")
	default:
		return true, w.retry(ctx, intent, "invalid_router_response")
	}
}

func (w *BindingUnbindWorker) complete(ctx context.Context, intent db.DingtalkBindingUnbindOutbox) error {
	_, err := w.store.CompleteDingTalkBindingUnbind(ctx, db.CompleteDingTalkBindingUnbindParams{
		ID: intent.ID, LeaseToken: intent.LeaseToken,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func (w *BindingUnbindWorker) retry(ctx context.Context, intent db.DingtalkBindingUnbindOutbox, code string) error {
	backoff := time.Second * time.Duration(1<<min(intent.AttemptCount, 8))
	_, err := w.store.RetryDingTalkBindingUnbind(ctx, db.RetryDingTalkBindingUnbindParams{
		AvailableAt: pgtype.Timestamptz{Time: w.now().Add(backoff), Valid: true},
		LastErrorCode: pgtype.Text{String: safeRouterErrorCode(code), Valid: true},
		ID: intent.ID, LeaseToken: intent.LeaseToken,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func bindingUnbindRetryCode(err error) string {
	var apiError *RouterAPIError
	if errors.As(err, &apiError) && apiError.Code != "" {
		return apiError.Code
	}
	return "router_unavailable"
}
