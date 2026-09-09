package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

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
