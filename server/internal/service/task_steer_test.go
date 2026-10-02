package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Separate transactions compete for the same Issue/agent while its old writer
// is canceling. Every correction must survive in exactly one unclaimed run.
func TestSteerIssueConcurrentCorrectionsWaitForProcessExit(t *testing.T) {
	f := newIssueFollowUpFixture(t)
	ctx := context.Background()
	initial := f.params
	initial.IdempotencyKey = "initial"
	first, err := f.svc.CreateExternalFollowUp(ctx, initial, IssueCommentCreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running', started_at=now(), dispatched_at=now(), session_id='old-provider-session', work_dir='/same/workdir' WHERE id=$1`, first.Task.ID); err != nil {
		t.Fatal(err)
	}
	const count = 6
	start := make(chan struct{})
	errs := make(chan error, count)
	results := make(chan IssueCommentCreateResult, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			p := f.params
			p.QueueMode = "steer"
			p.Content = fmt.Sprintf("correction %d", i)
			p.IdempotencyKey = fmt.Sprintf("correction-%d", i)
			r, err := f.svc.CreateExternalFollowUp(ctx, p, IssueCommentCreateOpts{})
			if err != nil {
				errs <- err
				return
			}
			results <- r
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Fatal(err)
	}
	var next db.AgentTaskQueue
	for r := range results {
		if next.ID.Valid && next.ID != r.Task.ID {
			t.Fatalf("duplicate successor: %v vs %v", next.ID, r.Task.ID)
		}
		next = r.Task
	}
	f.assertCounts(t, count+1, 2)
	next, err = f.svc.Queries.GetAgentTask(ctx, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.CoalescedCommentIds) != count-1 {
		t.Fatalf("coalesced=%d, want %d", len(next.CoalescedCommentIds), count-1)
	}
	old, err := f.svc.Queries.GetAgentTask(ctx, first.Task.ID)
	if err != nil || !taskProcessStopPending(old) {
		t.Fatalf("missing stop barrier: task=%+v err=%v", old, err)
	}
	if !fcE2BTaskHasActiveBlocker(next, []db.AgentTaskQueue{old}) {
		t.Fatal("cloud launcher ignored stop barrier")
	}
	_, err = f.svc.Queries.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{AgentID: next.AgentID, PrepareLeaseSecs: 60})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("agent claim before exit: %v", err)
	}
	_, err = f.svc.Queries.ClaimAgentTaskByID(ctx, db.ClaimAgentTaskByIDParams{ID: next.ID, RuntimeID: next.RuntimeID, PrepareLeaseSecs: 60})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("run-once claim before exit: %v", err)
	}
	if err = f.svc.TaskService.AcknowledgeTaskProcessStopped(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.TaskService.AcknowledgeTaskProcessStopped(ctx, old.ID); err != nil {
		t.Fatal("repeat ack:", err)
	}
	claimed, err := f.svc.Queries.ClaimAgentTaskByID(ctx, db.ClaimAgentTaskByIDParams{ID: next.ID, RuntimeID: next.RuntimeID, PrepareLeaseSecs: 60})
	if err != nil || claimed.ID != next.ID {
		t.Fatalf("claim after exit: %v", err)
	}
	// A replay after claim must return its original comment and run, and must
	// never cancel the successor that already consumed the corrections.
	for i := range count {
		p := f.params
		p.QueueMode = "steer"
		p.Content = fmt.Sprintf("correction %d", i)
		p.IdempotencyKey = fmt.Sprintf("correction-%d", i)
		r, err := f.svc.CreateExternalFollowUp(ctx, p, IssueCommentCreateOpts{})
		if err != nil || r.Task.ID != next.ID {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
	current, err := f.svc.Queries.GetAgentTask(ctx, next.ID)
	if err != nil || current.Status != "dispatched" {
		t.Fatalf("replay canceled successor: %s %v", current.Status, err)
	}
}

func TestSteerIssueMergedCallbacksAllReceiveOneExecutionResult(t *testing.T) {
	f := newIssueFollowUpFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		f.pool.Exec(ctx, `DELETE FROM task_completion_outbox WHERE agent_id=$1`, f.params.Issue.AssigneeID)
	})
	first, err := f.svc.CreateExternalFollowUp(ctx, f.params, IssueCommentCreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now(),dispatched_at=now() WHERE id=$1`, first.Task.ID); err != nil {
		t.Fatal(err)
	}
	var next db.AgentTaskQueue
	for i := range 3 {
		p := f.params
		p.QueueMode = "steer"
		p.IdempotencyKey = fmt.Sprintf("callback-%d", i)
		p.Content = fmt.Sprintf("correction %d", i)
		p.DispatchContext, _ = json.Marshal(map[string]any{"completion_callback": map[string]string{"url": fmt.Sprintf("/api/v1/dispatch-tasks/steer-input-%d/execution-result", i), "target": taskCompletionTestTarget}})
		r, err := f.svc.CreateExternalFollowUp(ctx, p, IssueCommentCreateOpts{})
		if err != nil {
			t.Fatal(err)
		}
		next = r.Task
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now(),dispatched_at=now() WHERE id=$1`, next.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.TaskService.CompleteTask(ctx, next.ID, []byte(`{"output":"corrected result"}`), "session", "/workdir", false, ""); err != nil {
		t.Fatal(err)
	}
	var callbacks int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM task_completion_outbox WHERE agent_id=$1 AND execution_status='completed' AND result_message='corrected result'`, next.AgentID).Scan(&callbacks); err != nil || callbacks != 3 {
		t.Fatalf("callbacks=%d want 3, err=%v", callbacks, err)
	}
}

func TestSteerChatCoalescesInputsDuringCancellation(t *testing.T) {
	f := newIssueFollowUpFixture(t)
	ctx := context.Background()
	agent, err := f.svc.Queries.GetAgent(ctx, f.params.Issue.AssigneeID)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := f.svc.Queries.CreateChatSession(ctx, db.CreateChatSessionParams{WorkspaceID: f.params.Issue.WorkspaceID, AgentID: agent.ID, CreatorID: f.params.AuthorID, Title: "steer test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.pool.Exec(ctx, `DELETE FROM chat_message WHERE chat_session_id=$1`, chat.ID)
		f.pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id=$1`, chat.ID)
		f.pool.Exec(ctx, `DELETE FROM chat_session WHERE id=$1`, chat.ID)
	})
	create := func(status, content string) db.AgentTaskQueue {
		var id string
		if err := f.pool.QueryRow(ctx, `INSERT INTO agent_task_queue(agent_id,runtime_id,chat_session_id,status,priority,context) VALUES($1,$2,$3,$4,2,'{}') RETURNING id::text`, agent.ID, agent.RuntimeID, chat.ID, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		task, err := f.svc.Queries.GetAgentTask(ctx, util.MustParseUUID(id))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET chat_input_task_id=id WHERE id=$1`, task.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.pool.Exec(ctx, `INSERT INTO chat_message(chat_session_id,role,content,task_id) VALUES($1,'user',$2,$3)`, chat.ID, content, task.ID); err != nil {
			t.Fatal(err)
		}
		return task
	}
	old := create("running", "original")
	first := create("deferred", "correction 1")
	next, stopped, err := f.svc.TaskService.SteerAgentDispatchChatTask(ctx, first.ID, chat.ID, agent.ID)
	if err != nil || stopped == nil || stopped.ID != old.ID {
		t.Fatalf("first steer: %v", err)
	}
	second := create("deferred", "correction 2")
	merged, stopped, err := f.svc.TaskService.SteerAgentDispatchChatTask(ctx, second.ID, chat.ID, agent.ID)
	if err != nil || stopped != nil || merged.ID != next.ID {
		t.Fatalf("second steer duplicated successor: %v", err)
	}
	var inputs int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM chat_message WHERE task_id=$1 AND role='user'`, next.ID).Scan(&inputs); err != nil || inputs != 2 {
		t.Fatalf("inputs=%d err=%v", inputs, err)
	}
}
