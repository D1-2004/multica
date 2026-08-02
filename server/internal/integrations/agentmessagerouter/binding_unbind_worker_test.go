package agentmessagerouter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeBindingUnbindStore struct {
	intent      db.DingtalkBindingUnbindOutbox
	claimErr    error
	completed   int
	retried     int
	retryParams db.RetryDingTalkBindingUnbindParams
}

func (f *fakeBindingUnbindStore) ClaimDingTalkBindingUnbind(_ context.Context, _ string) (db.DingtalkBindingUnbindOutbox, error) {
	return f.intent, f.claimErr
}

func (f *fakeBindingUnbindStore) CompleteDingTalkBindingUnbind(_ context.Context, _ db.CompleteDingTalkBindingUnbindParams) (db.CompleteDingTalkBindingUnbindRow, error) {
	f.completed++
	return db.CompleteDingTalkBindingUnbindRow{}, nil
}

func (f *fakeBindingUnbindStore) RetryDingTalkBindingUnbind(_ context.Context, params db.RetryDingTalkBindingUnbindParams) (db.DingtalkBindingUnbindOutbox, error) {
	f.retried++
	f.retryParams = params
	return f.intent, nil
}

type fakeBindingUnbindRouter struct {
	requests []DigitalEmployeeBindingKey
	result   DigitalEmployeeBindingUnbindResult
	err      error
}

func (f *fakeBindingUnbindRouter) UnbindDigitalEmployeeBinding(_ context.Context, key DigitalEmployeeBindingKey) (DigitalEmployeeBindingUnbindResult, error) {
	f.requests = append(f.requests, key)
	return f.result, f.err
}

func TestBindingUnbindWorkerConditionallyUnbindsPersistedAccountKey(t *testing.T) {
	for _, status := range []string{"unbound", "ownership_changed"} {
		t.Run(status, func(t *testing.T) {
			store := &fakeBindingUnbindStore{intent: bindingUnbindIntentForTest(t)}
			router := &fakeBindingUnbindRouter{result: DigitalEmployeeBindingUnbindResult{Status: status}}
			worker := NewBindingUnbindWorker(store, router, store.intent.TargetIdentity)

			worked, err := worker.ProcessNext(context.Background())
			if err != nil || !worked {
				t.Fatalf("ProcessNext worked=%v error=%v", worked, err)
			}
			if store.completed != 1 || store.retried != 0 || len(router.requests) != 1 {
				t.Fatalf("completed=%d retried=%d requests=%#v", store.completed, store.retried, router.requests)
			}
			request := router.requests[0]
			if request.AgentID != uuidStringForTest(store.intent.AgentID) || request.Platform != "dingtalk" ||
				request.TenantID != "corp-a" || request.AccountID != "employee-a" || len(request.ExpectedDomains) != 0 {
				t.Fatalf("request = %#v", request)
			}
		})
	}
}

func TestBindingUnbindWorkerRetriesInconsistentAndUnavailableWithoutSensitiveError(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 30, 0, 0, time.UTC)
	for _, tt := range []struct {
		name     string
		result   DigitalEmployeeBindingUnbindResult
		routerErr error
		wantCode string
	}{
		{name: "inconsistent", result: DigitalEmployeeBindingUnbindResult{Status: "inconsistent"}, wantCode: "inconsistent"},
		{name: "unavailable", routerErr: errors.New("upstream rejected Bearer secret-value"), wantCode: "router_unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeBindingUnbindStore{intent: bindingUnbindIntentForTest(t)}
			store.intent.AttemptCount = 2
			router := &fakeBindingUnbindRouter{result: tt.result, err: tt.routerErr}
			worker := NewBindingUnbindWorker(store, router, store.intent.TargetIdentity)
			worker.now = func() time.Time { return now }

			worked, err := worker.ProcessNext(context.Background())
			if err != nil || !worked {
				t.Fatalf("ProcessNext worked=%v error=%v", worked, err)
			}
			if store.completed != 0 || store.retried != 1 || store.retryParams.LastErrorCode.String != tt.wantCode ||
				!store.retryParams.AvailableAt.Time.Equal(now.Add(4*time.Second)) {
				t.Fatalf("completed=%d retried=%d retry=%#v", store.completed, store.retried, store.retryParams)
			}
			if tt.routerErr != nil && store.retryParams.LastErrorCode.String == tt.routerErr.Error() {
				t.Fatal("raw Router error was persisted")
			}
		})
	}
}

func TestBindingUnbindWorkerLeavesQueueEmpty(t *testing.T) {
	store := &fakeBindingUnbindStore{claimErr: pgx.ErrNoRows}
	worker := NewBindingUnbindWorker(store, &fakeBindingUnbindRouter{}, "router-target:v1:sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	worked, err := worker.ProcessNext(context.Background())
	if err != nil || worked {
		t.Fatalf("ProcessNext worked=%v error=%v", worked, err)
	}
}

func TestBindingUnbindWorkerRecoversExpiredLeaseAndRetriesAfterRestartWithoutAgentRow(t *testing.T) {
	pool := taskCompletionTestPool(t)
	ctx := context.Background()
	targetIdentity := "router-target:v1:sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	installationID := mustUUIDForTest("11111111-2222-3333-4444-555555555555")
	workspaceID := mustUUIDForTest("22222222-3333-4444-5555-666666666666")
	agentID := mustUUIDForTest("33333333-4444-5555-6666-777777777777")
	var intentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO dingtalk_binding_unbind_outbox (
			installation_id, workspace_id, agent_id,
			platform, tenant_id, account_id, target_identity
		) VALUES ($1, $2, $3, 'dingtalk', 'tenant-restart', 'account-restart', $4)
		RETURNING id
	`, installationID, workspaceID, agentID, targetIdentity).Scan(&intentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM dingtalk_binding_unbind_outbox WHERE id = $1`, intentID)
	})
	queries := db.New(pool)

	firstClaim, err := queries.ClaimDingTalkBindingUnbind(ctx, targetIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.ClaimDingTalkBindingUnbind(ctx, targetIdentity); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second live-lease claim error = %v, want pgx.ErrNoRows", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE dingtalk_binding_unbind_outbox
		SET lease_expires_at = now() - interval '1 second'
		WHERE id = $1
	`, intentID); err != nil {
		t.Fatal(err)
	}
	secondClaim, err := queries.ClaimDingTalkBindingUnbind(ctx, targetIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if !secondClaim.LeaseToken.Valid || secondClaim.LeaseToken == firstClaim.LeaseToken {
		t.Fatalf("expired lease was not replaced: first=%v second=%v", firstClaim.LeaseToken, secondClaim.LeaseToken)
	}
	if _, err := queries.RetryDingTalkBindingUnbind(ctx, db.RetryDingTalkBindingUnbindParams{
		AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true},
		LastErrorCode: pgtype.Text{String: "router_unavailable", Valid: true},
		ID: intentID, LeaseToken: secondClaim.LeaseToken,
	}); err != nil {
		t.Fatal(err)
	}

	router := &fakeBindingUnbindRouter{result: DigitalEmployeeBindingUnbindResult{Status: "unbound"}}
	restartedWorker := NewBindingUnbindWorker(queries, router, targetIdentity)
	worked, err := restartedWorker.ProcessNext(ctx)
	if err != nil || !worked {
		t.Fatalf("restarted worker worked=%v error=%v", worked, err)
	}
	if len(router.requests) != 1 || router.requests[0].AgentID != uuidStringForTest(agentID) ||
		router.requests[0].TenantID != "tenant-restart" || router.requests[0].AccountID != "account-restart" {
		t.Fatalf("restarted request = %#v", router.requests)
	}
	var status string
	var attempts int
	if err := pool.QueryRow(ctx, `
		SELECT status, attempt_count FROM dingtalk_binding_unbind_outbox WHERE id = $1
	`, intentID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" || attempts != 2 {
		t.Fatalf("restart result status=%q attempts=%d", status, attempts)
	}
}

func bindingUnbindIntentForTest(t *testing.T) db.DingtalkBindingUnbindOutbox {
	t.Helper()
	return db.DingtalkBindingUnbindOutbox{
		ID:             mustUUIDForTest("11111111-1111-1111-1111-111111111111"),
		InstallationID: mustUUIDForTest("22222222-2222-2222-2222-222222222222"),
		WorkspaceID:    mustUUIDForTest("33333333-3333-3333-3333-333333333333"),
		AgentID:        mustUUIDForTest("44444444-4444-4444-4444-444444444444"),
		Platform:       "dingtalk",
		TenantID:       "corp-a",
		AccountID:      "employee-a",
		TargetIdentity: "router-target:v1:sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status:         "queued",
		LeaseToken:     pgtype.UUID{Bytes: [16]byte{5}, Valid: true},
	}
}
