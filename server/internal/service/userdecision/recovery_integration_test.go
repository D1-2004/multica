package userdecision

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
	"time"
)

func preproductionTestDB(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("MULTICA_USER_DECISION_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires explicitly selected preproduction database")
	}
	if strings.Contains(dsn, "localhost") || strings.Contains(dsn, "127.0.0.1") {
		t.Fatal("runtime database verification belongs in preproduction")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure preproduction database")
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal("could not connect to preproduction database")
	}
	return ctx, pool
}

func integrationRequest(environment string) Request {
	return Request{ID: uuid.NewString(), WorkspaceID: uuid.NewString(), AgentID: uuid.NewString(), JobID: uuid.NewString(), Environment: environment, CorpID: "test-corp", ConversationID: "test-cid", InitiatorID: "initiator", SenderUID: "sender", SenderOrgID: "org", Snapshot: json.RawMessage(`{}`), Proposal: Proposal{Question: "fixture", Options: []Option{{ID: "new", Label: "new", Kind: "start_work", Plan: json.RawMessage(`{}`)}, {ID: "reply", Label: "reply", Kind: "reply", Plan: json.RawMessage(`{}`)}}}}
}
func integrationEvent(r Request) Event {
	return Event{ID: uuid.NewString(), CorpID: r.CorpID, ConversationID: r.ConversationID, CardID: "fixture-" + r.ID, OperatorID: r.InitiatorID, RequestID: r.ID, Version: Version, Selected: []string{"reply"}, Raw: json.RawMessage(`{"fixture":true}`)}
}

// The outer transaction always rolls back: jobs are never visible to live workers.
func TestPostgresExpiryCancellationAndReceiptLease(t *testing.T) {
	ctx, pool := preproductionTestDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	})
	store := &Store{DB: tx, Environment: "integration-" + uuid.NewString()}
	r := integrationRequest(store.Environment)
	// Read an active identity without modifying an existing agent or job.
	if err = tx.QueryRow(ctx, `SELECT id::text,workspace_id::text FROM agent WHERE archived_at IS NULL AND status NOT IN ('disabled','paused') LIMIT 1`).Scan(&r.AgentID, &r.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Prepare(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO inbound_coordinator_job(id,acceptance_id,workspace_id,agent_id,user_id,endpoint_namespace_id,idempotency_key,command,chat_session_id,user_message_id,status,available_at,last_error) VALUES($1,$1,$2,$3,$1,$1,$1::text,'{}',$1,$1,'pending','infinity','awaiting_user_decision')`, r.JobID, r.WorkspaceID, r.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE coordinator_user_decision SET state='sending' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.ConfirmSent(ctx, r.ID, integrationEvent(r).CardID); err != nil {
		t.Fatal(err)
	}
	var seconds float64
	if err = tx.QueryRow(ctx, `SELECT extract(epoch FROM expires_at-sent_at) FROM coordinator_user_decision WHERE id=$1`, r.ID).Scan(&seconds); err != nil || seconds != 86400 {
		t.Fatalf("expiry interval %v: %v", seconds, err)
	}
	// Age only this invisible fixture against the database clock.
	if _, err = tx.Exec(ctx, `UPDATE coordinator_user_decision SET expires_at=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if outcome, err := store.Accept(ctx, integrationEvent(r)); err != nil || outcome != "expired" {
		t.Fatalf("expired accept %s: %v", outcome, err)
	}
	if err = store.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	var state, action, reason string
	var noSelection bool
	if err = tx.QueryRow(ctx, `SELECT state,submission IS NULL FROM coordinator_user_decision WHERE id=$1`, r.ID).Scan(&state, &noSelection); err != nil || state != "expired" || !noSelection {
		t.Fatalf("expiry synthesized a choice: %s %v %v", state, noSelection, err)
	}
	if err = tx.QueryRow(ctx, `SELECT command->'_coordinator_plan'->>'Action',command->'_coordinator_plan'->>'Reason' FROM inbound_coordinator_job WHERE id=$1`, r.JobID).Scan(&action, &reason); err != nil || action != "reply" || reason != "user_decision_expired" {
		t.Fatalf("invalid expiry dispatch %s %s %v", action, reason, err)
	}
	next := integrationRequest(store.Environment)
	if _, err = store.Prepare(ctx, next); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE coordinator_user_decision SET state='sending' WHERE id=$1`, next.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.ConfirmSent(ctx, next.ID, integrationEvent(next).CardID); err != nil {
		t.Fatal(err)
	}
	if outcome, err := store.Accept(ctx, integrationEvent(next)); err != nil || outcome != "accepted" {
		t.Fatalf("accept %s %v", outcome, err)
	}
	lease := uuid.NewString()
	if _, err = tx.Exec(ctx, `UPDATE coordinator_user_decision SET state='resuming',lease_token=$2,lease_expires_at=now()+interval '75 seconds' WHERE id=$1`, next.ID, lease); err != nil {
		t.Fatal(err)
	}
	if err = store.ConfirmSent(ctx, next.ID, integrationEvent(next).CardID); err != nil {
		t.Fatal(err)
	}
	var actualLease string
	if err = tx.QueryRow(ctx, `SELECT state,lease_token::text FROM coordinator_user_decision WHERE id=$1`, next.ID).Scan(&state, &actualLease); err != nil || state != "resuming" || actualLease != lease {
		t.Fatalf("receipt stole resolution lease: %s %s %v", state, actualLease, err)
	}
	// The fixture's non-existent agent simulates deletion of its identity.
	if err = store.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `SELECT state FROM coordinator_user_decision WHERE id=$1`, next.ID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("deleted agent remained executable: %s %v", state, err)
	}
	if outcome, err := store.Accept(ctx, integrationEvent(next)); err != nil || outcome != "already_resolved" {
		t.Fatalf("cancelled request resumed: %s %v", outcome, err)
	}
}

type retryFixtureSession struct {
	updates int
	fail    bool
}

func (*retryFixtureSession) Verify(context.Context, string, string) (string, string, error) {
	return "", "", errors.New("unexpected verification")
}
func (*retryFixtureSession) Send(context.Context, Request) (string, error) {
	return "", errors.New("unexpected resend")
}
func (s *retryFixtureSession) Update(context.Context, Request, string, string) error {
	s.updates++
	if s.fail {
		return errors.New("injected transport failure")
	}
	return nil
}
func (*retryFixtureSession) Consume(context.Context, func(), func([]byte) error) error {
	return errors.New("unexpected consumer")
}
func (*retryFixtureSession) Close() {}

func TestPostgresCardUpdateRetryDoesNotRedispatch(t *testing.T) {
	ctx, pool := preproductionTestDB(t)
	store := &Store{DB: pool, Environment: "integration-" + uuid.NewString()}
	r := integrationRequest(store.Environment)
	if _, err := store.Prepare(ctx, r); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanup, `DELETE FROM coordinator_user_decision WHERE id=$1 AND environment=$2`, r.ID, store.Environment); err != nil {
			t.Error(err)
		}
	})
	// No real job exists; live consumers cannot select the isolated environment.
	if _, err := pool.Exec(ctx, `UPDATE coordinator_user_decision SET state='dispatched',sent_at=now(),card_update_pending=true,execution_result='{"state":"completed","tasks":[]}' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	resolves := 0
	svc := &Service{Store: store, Pool: pool, Resolve: func(context.Context, Request) (json.RawMessage, json.RawMessage, error) {
		resolves++
		return nil, nil, errors.New("unexpected resolution")
	}}
	session := &retryFixtureSession{fail: true}
	if err := svc.processIdentity(ctx, r, session); err == nil {
		t.Fatal("transport failure was hidden")
	}
	var pending, delayed bool
	if err := pool.QueryRow(ctx, `SELECT card_update_pending,available_at>now() FROM coordinator_user_decision WHERE id=$1`, r.ID).Scan(&pending, &delayed); err != nil || !pending || !delayed {
		t.Fatalf("retry lost: %v %v %v", pending, delayed, err)
	}
	// A new service instance must recover the durable retry after restart.
	if _, err := pool.Exec(ctx, `UPDATE coordinator_user_decision SET available_at=now() WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	restarted := &Service{Store: store, Pool: pool, Resolve: svc.Resolve}
	session.fail = false
	if err := restarted.processIdentity(ctx, r, session); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state,card_update_pending FROM coordinator_user_decision WHERE id=$1`, r.ID).Scan(&state, &pending); err != nil || pending || state != "dispatched" || resolves != 0 || session.updates != 2 {
		t.Fatalf("retry duplicated execution: %s pending=%v resolves=%d updates=%d err=%v", state, pending, resolves, session.updates, err)
	}
	if err := restarted.processIdentity(ctx, r, session); err != nil {
		t.Fatal(err)
	}
	if session.updates != 2 {
		t.Fatal("completed update repeated")
	}
	if _, err := pool.Exec(ctx, `UPDATE coordinator_user_decision SET state='sending',lease_token=gen_random_uuid(),lease_expires_at=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimSend(ctx, r.SenderUID, r.SenderOrgID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("uncertain send was reissued: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM coordinator_user_decision WHERE id=$1`, r.ID).Scan(&state); err != nil || state != "send_unknown" {
		t.Fatalf("unknown send lost: %s %v", state, err)
	}
}
