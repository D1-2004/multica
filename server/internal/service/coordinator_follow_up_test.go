package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestProactiveFollowUpDurableBusyBatchAndReceipts(t *testing.T) {
	f := newIssueFollowUpFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM coordinator_issue_follow_up WHERE issue_id=$1`, f.params.Issue.ID)
	})
	first, err := f.svc.CreateExternalFollowUp(ctx, f.params, IssueCommentCreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	p := f.params
	p.IdempotencyKey = "proactive-2"
	p.Content = "补充：请统计收到和发出的消息。"
	p.AgentIdentityContextToken = ""
	p.DispatchContext = []byte(`{"proactive_conversation":true,"external_identity":{"dws":{"uid":"u1","orgId":"o1"}},"dispatch_event_data":{"conversation":{"openConversationId":"cid-test"},"messages":[{"openMsgId":"m2","text":"补充"}]}}`)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.svc.QueueCoordinatorFollowUp(ctx, p); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	second, err := f.svc.QueueCoordinatorFollowUp(ctx, p)
	if err != nil || second.Task.ID.Valid {
		t.Fatalf("busy follow-up not queued: %v", err)
	}
	p.IdempotencyKey = "proactive-3"
	p.Content = "补充：只统计今天。"
	third, err := f.svc.QueueCoordinatorFollowUp(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	f.assertCounts(t, 3, 1)
	if worked, err := f.svc.ProcessCoordinatorFollowUp(ctx); err != nil || worked {
		t.Fatalf("started while previous task active: %v %v", worked, err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='deferred' WHERE id=$1`, first.Task.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.svc.ProcessCoordinatorFollowUp(ctx); err != nil || worked {
		t.Fatalf("jumped ahead of deferred retry: %v %v", worked, err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, first.Task.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.svc.ProcessCoordinatorFollowUp(ctx); err != nil || !worked {
		t.Fatalf("pending dispatch: %v %v", worked, err)
	}
	f.assertCounts(t, 3, 2)
	var taskID pgtype.UUID
	var raw []byte
	var coalesced []pgtype.UUID
	if err = f.pool.QueryRow(ctx, `SELECT task_id FROM coordinator_issue_follow_up WHERE comment_id=$1`, second.Comment.ID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT context,coalesced_comment_ids FROM agent_task_queue WHERE id=$1`, taskID).Scan(&raw, &coalesced); err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(coalesced) != 1 || len(data["coordinator_follow_up_comment_ids"]) == 0 || len(data["agent_identity_context_token"]) > 0 {
		t.Fatal("lost batch coverage or reused task-scoped token")
	}
	first.Task.Context = p.DispatchContext
	// The finished first task can describe the additions as pending execution, never covered.
	outstanding, err := f.svc.OutstandingCoordinatorFollowUps(ctx, first.Task, "cid-test")
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Items []any `json:"items"`
	}
	if json.Unmarshal([]byte(outstanding), &view) != nil || len(view.Items) != 2 {
		t.Fatalf("outstanding=%s", outstanding)
	}
	task, err := f.svc.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	task.Status = "completed"
	task.DeliveredCommentIds = []pgtype.UUID{third.Comment.ID}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='completed',delivered_comment_ids=$2 WHERE id=$1`, taskID, task.DeliveredCommentIds); err != nil {
		t.Fatal(err)
	}
	owned, err := f.svc.ReconcileCoordinatorFollowUpReceipts(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if !owned[util.UUIDToString(second.Comment.ID)] {
		t.Fatal("generic reconciliation could steal queued follow-up")
	}
	var pending int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM coordinator_issue_follow_up WHERE issue_id=$1 AND task_id IS NULL`, f.params.Issue.ID).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("omitted comment not retained: %d %v", pending, err)
	}
}

func TestProactiveFollowUpSkipsLockedOldestIssue(t *testing.T) {
	a := newIssueFollowUpFixture(t)
	b := newIssueFollowUpFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, f := range []issueFollowUpFixture{a, b} {
		p := f.params
		p.DispatchContext = []byte(`{"external_identity":{"dws":{"uid":"u1","orgId":"org"}},"dispatch_event_data":{"conversation":{"openConversationId":"cid"}}}`)
		if _, err := f.svc.QueueCoordinatorFollowUp(ctx, p); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = f.pool.Exec(context.Background(), `DELETE FROM coordinator_issue_follow_up WHERE issue_id=$1`, f.params.Issue.ID)
		})
	}
	if _, err := a.pool.Exec(ctx, `UPDATE issue SET updated_at='2001-01-01' WHERE id=$1`, a.params.Issue.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.pool.Exec(ctx, `UPDATE issue SET updated_at='2002-01-01' WHERE id=$1`, b.params.Issue.ID); err != nil {
		t.Fatal(err)
	}
	lock, err := a.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err := lock.Exec(ctx, `SELECT id FROM issue WHERE id=$1 FOR UPDATE`, a.params.Issue.ID); err != nil {
		t.Fatal(err)
	}
	worked, err := b.svc.ProcessCoordinatorFollowUp(ctx)
	if err != nil || !worked {
		t.Fatalf("locked oldest Issue prevented independent ready follow-up: worked=%v err=%v", worked, err)
	}
	var count int
	if err := b.pool.QueryRow(ctx, `SELECT count(*) FROM coordinator_issue_follow_up WHERE issue_id=$1 AND task_id IS NOT NULL`, b.params.Issue.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unlocked Issue was not dispatched: count=%d err=%v", count, err)
	}
}

// One conversation's follow-ups batch together whatever title each delivery
// carried; another conversation or type does not.
func TestFollowUpConversationScopeIgnoresTitle(t *testing.T) {
	a := followUpConversationScope(json.RawMessage(`{"openConversationId":"cidA","type":"group","title":"项目群"}`))
	b := followUpConversationScope(json.RawMessage(`{"openConversationId":"cidA","type":"group"}`))
	if a != b {
		t.Fatalf("title split the scope: %q vs %q", a, b)
	}
	for _, other := range []string{`{"openConversationId":"cidB","type":"group"}`, `{"openConversationId":"cidA","type":"single"}`} {
		if followUpConversationScope(json.RawMessage(other)) == a {
			t.Fatalf("%s shares cidA's scope", other)
		}
	}
	if got := followUpConversationScope(json.RawMessage(`[1]`)); got != `[1]` {
		t.Fatalf("undecodable conversation = %q", got)
	}
}
