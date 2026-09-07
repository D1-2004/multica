package dingtalkresponse

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type fakeProvider struct {
	sends   atomic.Int32
	queries atomic.Int32
	send    func(context.Context, ActionInput, string) (dwsclient.SendResult, error)
	query   func(context.Context, ActionInput, string) (dwsclient.SendStatus, error)
}

func (p *fakeProvider) Send(ctx context.Context, in ActionInput, key string) (dwsclient.SendResult, error) {
	p.sends.Add(1)
	if p.send != nil {
		return p.send(ctx, in, key)
	}
	return dwsclient.SendResult{OpenTaskID: "provider-task"}, nil
}
func (p *fakeProvider) Query(ctx context.Context, in ActionInput, id string) (dwsclient.SendStatus, error) {
	p.queries.Add(1)
	if p.query != nil {
		return p.query(ctx, in, id)
	}
	return dwsclient.SendStatus{State: "delivered", OpenConversationID: in.ConversationID, OpenMessageID: "sent-message"}, nil
}

type fakeReceipts struct {
	mu   sync.Mutex
	rows []protocol.DingTalkResponseReceipt
	fail bool
}

func (r *fakeReceipts) SendResponseReceipt(_ context.Context, _, _ string, receipt protocol.DingTalkResponseReceipt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, receipt)
	if r.fail {
		return errors.New("retry receipt")
	}
	return nil
}

func responsePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("database unreachable: %v", err)
	}
	schema := "response_test_" + uuid.NewString()
	schemaName := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schemaName+" CASCADE"); admin.Close() })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, name := range []string{"9144_response_action", "9145_response_action_id", "9146_response_action_due", "9147_response_action_scope", "9148_response_route_callback", "9153_sandbox_send_receipt", "9154_sandbox_send_receipt_id", "9155_sandbox_send_receipt_task", "9156_sandbox_send_receipt_due"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", name+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), string(raw)); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}
	return pool
}

func inputFixture() ActionInput {
	return ActionInput{WorkspaceID: uuid.NewString(), AgentID: uuid.NewString(), RequestID: "request", DWSUID: "123", DWSOrgID: "org", ConversationID: "cid", SenderOpenDingTalkID: "sender", IsGroup: true, CallbackURL: "https://router.example/api/v1/dispatch-tasks/task/response-receipt", CallbackTarget: "router-v1", Text: "hello"}
}

func dueNow(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE response_action SET next_attempt_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}
func process(t *testing.T, s *Service) {
	t.Helper()
	worked, err := s.processOne(context.Background())
	if err != nil || !worked {
		t.Fatalf("process: worked=%v err=%v", worked, err)
	}
}
func stateOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var state string
	if err := pool.QueryRow(context.Background(), `SELECT state FROM response_action WHERE id=$1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestActionIsAtomicAndImmutable(t *testing.T) {
	pool := responsePool(t)
	s := NewService(pool, nil, nil)
	ctx := context.Background()
	in := inputFixture()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Enqueue(ctx, tx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM response_action`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback count=%d err=%v", count, err)
	}
	id2, err := s.Submit(ctx, in)
	if err != nil || id2 != id {
		t.Fatalf("stable id=%s/%s err=%v", id, id2, err)
	}
	if _, err := s.Submit(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Text = "different"
	if _, err := s.Submit(ctx, in); err == nil {
		t.Fatal("overwrote stable action")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM response_action`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestFrozenRouteIsImmutableAndSupportsUpdateCallback(t *testing.T) {
	pool := responsePool(t)
	s := NewService(pool, nil, nil)
	ctx := context.Background()
	in := inputFixture()
	in.Text = ""
	route := Route{CallbackURL: "/api/v1/dispatch-tasks/task/execution-result", Input: in}
	if err := s.RegisterRoute(ctx, pool, route); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterRoute(ctx, pool, route); err != nil {
		t.Fatal(err)
	}
	route.CallbackURL = "/api/v1/dispatch-tasks/task/execution-update"
	if err := s.RegisterRoute(ctx, pool, route); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindRoute(ctx, route.CallbackURL)
	if err != nil || got == nil || got.Input.CallbackURL != in.CallbackURL {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	route.Input.ShowAITag = true
	if err := s.RegisterRoute(ctx, pool, route); err == nil {
		t.Fatal("changed frozen route")
	}
	if got, err := s.FindRoute(ctx, "https://router.example/legacy"); got != nil || err != nil {
		t.Fatalf("legacy=%+v err=%v", got, err)
	}
}

func TestAcceptedIsNotDeliveredAndOnlyQueryCompletes(t *testing.T) {
	pool := responsePool(t)
	p := &fakeProvider{}
	r := &fakeReceipts{}
	s := NewService(pool, p, r)
	id, err := s.Submit(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	process(t, s)
	if state := stateOf(t, pool, id); state != "provider_accepted" {
		t.Fatal(state)
	}
	if len(r.rows) != 0 {
		t.Fatal("accepted send emitted delivery receipt")
	}
	dueNow(t, pool, id)
	process(t, s)
	if state := stateOf(t, pool, id); state != "delivered" {
		t.Fatal(state)
	}
	if p.sends.Load() != 1 || len(r.rows) != 1 || r.rows[0].OpenMessageID != "sent-message" {
		t.Fatalf("sends=%d receipts=%+v", p.sends.Load(), r.rows)
	}
	if worked, err := s.processOne(context.Background()); worked || err != nil {
		t.Fatalf("terminal reprocessed: %v %v", worked, err)
	}
}

func TestUnknownSendNeverResubmitsAcrossRestart(t *testing.T) {
	pool := responsePool(t)
	p := &fakeProvider{send: func(context.Context, ActionInput, string) (dwsclient.SendResult, error) {
		return dwsclient.SendResult{}, context.DeadlineExceeded
	}}
	r := &fakeReceipts{}
	s := NewService(pool, p, r)
	id, err := s.Submit(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	process(t, s)
	if state := stateOf(t, pool, id); state != "unknown" {
		t.Fatal(state)
	}
	dueNow(t, pool, id)
	process(t, NewService(pool, p, r))
	if p.sends.Load() != 1 || p.queries.Load() != 0 || len(r.rows) != 1 || r.rows[0].State != "unknown" {
		t.Fatalf("sends=%d queries=%d receipts=%+v", p.sends.Load(), p.queries.Load(), r.rows)
	}
}

func TestConcurrentWorkersCannotSendSameAction(t *testing.T) {
	pool := responsePool(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	p := &fakeProvider{send: func(context.Context, ActionInput, string) (dwsclient.SendResult, error) {
		close(entered)
		<-release
		return dwsclient.SendResult{OpenTaskID: "provider-task"}, nil
	}}
	s := NewService(pool, p, &fakeReceipts{})
	if _, err := s.Submit(context.Background(), inputFixture()); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := s.processOne(context.Background()); finished <- err }()
	<-entered
	worked, err := NewService(pool, p, &fakeReceipts{}).processOne(context.Background())
	close(release)
	if worked || err != nil {
		t.Fatalf("second replica claimed in-flight action: %v %v", worked, err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if p.sends.Load() != 1 {
		t.Fatalf("sent %d times", p.sends.Load())
	}
}

func TestExpiredUnknownLeaseCleansWithoutSending(t *testing.T) {
	pool := responsePool(t)
	p := &fakeProvider{}
	r := &fakeReceipts{}
	s := NewService(pool, p, r)
	id, err := s.Submit(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE response_action SET state='unknown',error_code='submission_interrupted',lease_token=$2,lease_until=now()-interval '1 second' WHERE id=$1`, id, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	process(t, s)
	if p.sends.Load() != 0 || len(r.rows) != 1 || r.rows[0].State != "unknown" {
		t.Fatalf("sends=%d receipts=%+v", p.sends.Load(), r.rows)
	}
}

func TestReceiptRetryDoesNotRepeatSend(t *testing.T) {
	pool := responsePool(t)
	p := &fakeProvider{}
	r := &fakeReceipts{fail: true}
	s := NewService(pool, p, r)
	id, err := s.Submit(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	process(t, s)
	dueNow(t, pool, id)
	process(t, s)
	r.fail = false
	dueNow(t, pool, id)
	process(t, NewService(pool, p, r))
	if p.sends.Load() != 1 || p.queries.Load() != 1 || len(r.rows) != 2 || r.rows[0].ActionID != r.rows[1].ActionID {
		t.Fatalf("sends=%d queries=%d receipts=%+v", p.sends.Load(), p.queries.Load(), r.rows)
	}
}

func TestUnknownWithProviderTaskCanReconcileToDelivered(t *testing.T) {
	pool := responsePool(t)
	p := &fakeProvider{query: func(context.Context, ActionInput, string) (dwsclient.SendStatus, error) {
		return dwsclient.SendStatus{State: "provider_accepted"}, nil
	}}
	r := &fakeReceipts{}
	s := NewService(pool, p, r)
	id, err := s.Submit(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	process(t, s)
	if _, err := pool.Exec(context.Background(), `UPDATE response_action SET first_attempt_at=now()-interval '16 minutes',next_attempt_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	process(t, s)
	if stateOf(t, pool, id) != "unknown" || len(r.rows) != 1 || r.rows[0].State != "unknown" {
		t.Fatalf("receipts=%+v", r.rows)
	}
	p.query = nil
	dueNow(t, pool, id)
	process(t, s)
	if stateOf(t, pool, id) != "delivered" || p.sends.Load() != 1 || len(r.rows) != 2 || r.rows[1].State != "delivered" {
		t.Fatalf("receipts=%+v", r.rows)
	}
}

func TestCloseStatesNeverCallProvider(t *testing.T) {
	pool := responsePool(t)
	p := &fakeProvider{}
	r := &fakeReceipts{}
	s := NewService(pool, p, r)
	for _, state := range []string{"silent", "cancelled", "failed", "unknown"} {
		in := inputFixture()
		in.RequestID = state
		in.Text = ""
		in.CloseState = state
		if _, err := s.Submit(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		process(t, s)
	}
	if p.sends.Load() != 0 || p.queries.Load() != 0 || len(r.rows) != 4 {
		t.Fatalf("receipts=%+v", r.rows)
	}
}

func TestRetryOnlyWhenSendDefinitelyNotSubmitted(t *testing.T) {
	pool := responsePool(t)
	var keys []string
	p := &fakeProvider{send: func(_ context.Context, _ ActionInput, key string) (dwsclient.SendResult, error) {
		keys = append(keys, key)
		return dwsclient.SendResult{}, &NotSubmittedError{Err: errors.New("auth failed")}
	}}
	s := NewService(pool, p, &fakeReceipts{})
	id, err := s.Submit(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	process(t, s)
	if stateOf(t, pool, id) != "pending" {
		t.Fatal("unsubmitted auth failure not retried")
	}
	dueNow(t, pool, id)
	process(t, s)
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("keys=%v", keys)
	}
}

func TestResponseRouteOnlyAcceptsTrustedCallbackShape(t *testing.T) {
	in := inputFixture()
	in.CallbackURL = "/api/v1/dispatch-tasks/task/response-receipt"
	for _, callback := range []string{
		"/api/v1/dispatch-tasks/task/execution-result",
		"/api/v1/dispatch-tasks/task/execution-update",
	} {
		if err := validateRoute(Route{CallbackURL: callback, Input: in}); err != nil {
			t.Fatal(err)
		}
	}
	for _, callback := range []string{
		"/api/v1/dispatch-tasks/other/execution-result",
		"/api/v1/dispatch-tasks/task/execution-result?token=x",
		"/api/v1/dispatch-tasks/task/execution-result?",
		"/api/v1/dispatch-tasks/task/execution-result#frag",
		"/api/v1/dispatch-tasks/%74ask/execution-result",
		"//other.example/api/v1/dispatch-tasks/task/execution-result",
		"https://user@router.example/api/v1/dispatch-tasks/task/execution-result",
		"/api/v1/dispatch-tasks/task/response-receipt",
	} {
		if err := validateRoute(Route{CallbackURL: callback, Input: in}); err == nil {
			t.Fatalf("accepted callback %q", callback)
		}
	}
	in.CallbackURL = "https://other.example/api/v1/dispatch-tasks/task/response-receipt"
	if err := validateRoute(Route{CallbackURL: "https://router.example/api/v1/dispatch-tasks/task/execution-result", Input: in}); err == nil {
		t.Fatal("accepted cross-origin receipt")
	}
}

func TestStaleLeaseCannotOverwriteNewOwnerState(t *testing.T) {
	pool := responsePool(t)
	s := NewService(pool, &fakeProvider{}, &fakeReceipts{})
	id, err := s.Submit(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.claim(context.Background())
	if err != nil || old == nil {
		t.Fatalf("claim = %+v, %v", old, err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE response_action SET lease_until=now()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	current, err := s.claim(context.Background())
	if err != nil || current == nil {
		t.Fatalf("claim = %+v, %v", current, err)
	}
	if err := s.saveState(context.Background(), current, "unknown", "provider-task", "", "", "recovered"); err != nil {
		t.Fatal(err)
	}
	if err := s.saveState(context.Background(), old, "failed", "", "", "", "stale"); err == nil {
		t.Fatal("stale lease overwrote new owner")
	}
	if stateOf(t, pool, id) != "unknown" {
		t.Fatal("stale state persisted")
	}
}

func TestQueryCannotConfirmDifferentConversation(t *testing.T) {
	pool := responsePool(t)
	p := &fakeProvider{query: func(context.Context, ActionInput, string) (dwsclient.SendStatus, error) {
		return dwsclient.SendStatus{State: "delivered", OpenConversationID: "different-cid", OpenMessageID: "mid"}, nil
	}}
	r := &fakeReceipts{}
	s := NewService(pool, p, r)
	id, err := s.Submit(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	process(t, s)
	dueNow(t, pool, id)
	process(t, s)
	if stateOf(t, pool, id) != "unknown" || len(r.rows) != 1 || r.rows[0].State != "unknown" || r.rows[0].OpenMessageID != "" {
		t.Fatalf("receipts = %+v", r.rows)
	}
}
