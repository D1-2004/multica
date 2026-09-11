package dingtalkresponse

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func enqueueWaitFixture(t *testing.T, s *Service, pool *pgxpool.Pool, in ActionInput, jobID string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	id, err := s.EnqueueCoordinatorWait(ctx, tx, in, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}
func createWaitJobFixture(t *testing.T, pool *pgxpool.Pool, in ActionInput, jobID, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE inbound_coordinator_job(id uuid,workspace_id uuid,agent_id uuid,status text,command jsonb)`); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"_coordinator_wait": map[string]any{"status": "waiting", "response_action_id": StableActionID(in.WorkspaceID, in.AgentID, "coordinator-wait:"+jobID, "message.send")}, "_coordinator_plan": map[string]any{"PlanVersion": "window-plan-v1", "Items": []map[string]string{{"action_key": "item-1"}}, "CompletedActionKeys": []string{}}})
	if _, err := pool.Exec(ctx, `INSERT INTO inbound_coordinator_job VALUES($1,$2,$3,$4,$5)`, jobID, in.WorkspaceID, in.AgentID, status, raw); err != nil {
		t.Fatal(err)
	}
}
func TestCoordinatorWaitIsHostOnlyAndDoesNotCloseReceipt(t *testing.T) {
	pool := responsePool(t)
	provider := &fakeProvider{}
	receipts := &fakeReceipts{}
	s := NewService(pool, provider, receipts)
	in := inputFixture()
	jobID := uuid.NewString()
	forged := in
	forged.CoordinatorWaitJobID = jobID
	if _, err := s.Submit(context.Background(), forged); err == nil {
		t.Fatal("ordinary send accepted a caller-supplied progress flag")
	}
	createWaitJobFixture(t, pool, in, jobID, "pending")
	id := enqueueWaitFixture(t, s, pool, in, jobID)
	if again := enqueueWaitFixture(t, s, pool, in, jobID); again != id {
		t.Fatal("progress key changed")
	}
	var frozen ActionInput
	var raw []byte
	if err := pool.QueryRow(context.Background(), `SELECT input FROM response_action WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &frozen) != nil || frozen.CallbackURL != "" || frozen.TaskID != "" || frozen.CoordinatorWaitJobID != jobID {
		t.Fatal("waiting notice masqueraded as execution/completion")
	}
	process(t, s)
	dueNow(t, pool, id)
	process(t, s)
	if stateOf(t, pool, id) != "delivered" || provider.sends.Load() != 1 || len(receipts.rows) != 0 {
		t.Fatalf("non-terminal progress: sends=%d receipt_count=%d state=%s", provider.sends.Load(), len(receipts.rows), stateOf(t, pool, id))
	}
}
func TestCoordinatorWaitResolvedBeforeSendDoesNotSend(t *testing.T) {
	pool := responsePool(t)
	provider := &fakeProvider{}
	receipts := &fakeReceipts{}
	s := NewService(pool, provider, receipts)
	in := inputFixture()
	jobID := uuid.NewString()
	createWaitJobFixture(t, pool, in, jobID, "completed")
	id := enqueueWaitFixture(t, s, pool, in, jobID)
	process(t, s)
	if stateOf(t, pool, id) != "cancelled" || provider.sends.Load() != 0 || len(receipts.rows) != 0 {
		t.Fatal("resolved wait sent stale progress or a closing receipt")
	}
}
func TestCoordinatorWaitEnqueueRollsBack(t *testing.T) {
	pool := responsePool(t)
	s := NewService(pool, nil, nil)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.EnqueueCoordinatorWait(context.Background(), tx, inputFixture(), uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(context.Background(), `SELECT count(*) FROM response_action`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback left progress action: count=%d err=%v", count, err)
	}
}

func TestCoordinatorWaitAllItemsCommittedBeforeSendDoesNotSend(t *testing.T) {
	pool := responsePool(t)
	provider := &fakeProvider{}
	receipts := &fakeReceipts{}
	s := NewService(pool, provider, receipts)
	in := inputFixture()
	jobID := uuid.NewString()
	createWaitJobFixture(t, pool, in, jobID, "running")
	id := enqueueWaitFixture(t, s, pool, in, jobID)
	if _, err := pool.Exec(context.Background(), `UPDATE inbound_coordinator_job SET command=jsonb_set(command,'{_coordinator_plan,CompletedActionKeys}','["item-1"]') WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	process(t, s)
	if stateOf(t, pool, id) != "cancelled" || provider.sends.Load() != 0 || len(receipts.rows) != 0 {
		t.Fatal("already-committed work received stale waiting progress")
	}
}
