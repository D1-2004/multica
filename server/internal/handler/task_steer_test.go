package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCancelAckReconcilesDeferredCommentsAndRequiresProcessExit(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	f := createCommentDeliveryFixture(t, "cancel-ack-reconcile")
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET daemon_id='cancel-ack-daemon' WHERE id=$1`, f.runtimeID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE issue SET assignee_type='agent',assignee_id=$2 WHERE id=$1`, f.issueID, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',dispatched_at=now(),started_at=now(),created_at=now()-interval '10 minutes',delivered_comment_ids=ARRAY[trigger_comment_id] WHERE id=$1`, f.taskID); err != nil {
		t.Fatal(err)
	}
	old, err := testHandler.TaskService.CancelTask(ctx, parseUUID(f.taskID))
	if err != nil {
		t.Fatal(err)
	}
	testHandler.reconcileCommentsOnCompletion(ctx, old)
	if n := queuedTaskCountForAgentIssue(t, f.issueID, f.agentID); n != 1 {
		t.Fatalf("queued successors=%d", n)
	}
	ack := func(body map[string]any) *httptest.ResponseRecorder {
		runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(f.runtimeID))
		if err != nil {
			t.Fatal(err)
		}
		req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+f.taskID+"/cancel-ack", body, testWorkspaceID, runtime.DaemonID.String)
		req = withURLParams(req, "taskId", f.taskID)
		w := httptest.NewRecorder()
		testHandler.AckTaskCancelled(w, req)
		return w
	}
	if w := ack(map[string]any{}); w.Code != http.StatusOK {
		t.Fatalf("legacy ack: %d %s", w.Code, w.Body.String())
	}
	_, err = testHandler.Queries.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{AgentID: parseUUID(f.agentID), PrepareLeaseSecs: 60})
	if err == nil {
		t.Fatal("legacy acknowledgement opened process barrier")
	}
	// A comment arriving during termination must join the same successor.
	if _, err = testPool.Exec(ctx, `INSERT INTO comment(issue_id,workspace_id,author_type,author_id,content,type) VALUES($1,$2,'member',$3,'late correction','comment')`, f.issueID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	if w := ack(map[string]any{"process_group_stopped": true}); w.Code != http.StatusOK {
		t.Fatalf("positive ack: %d %s", w.Code, w.Body.String())
	}
	if n := queuedTaskCountForAgentIssue(t, f.issueID, f.agentID); n != 1 {
		t.Fatalf("late correction duplicated successor: %d", n)
	}
	next, err := testHandler.Queries.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{AgentID: parseUUID(f.agentID), PrepareLeaseSecs: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.CoalescedCommentIds) < 2 {
		t.Fatalf("deferred inputs were lost: %v", next.CoalescedCommentIds)
	}
}

func TestDispatchIssueSteerControlValidation(t *testing.T) {
	c := DispatchCommand{Event: DispatchEvent{Domain: "channel", Type: "message.created"}, Surface: DispatchSurface{Type: "issue"}, Continuation: &AgentDispatchContinuation{Kind: "issue", IssueID: "existing-issue"}, Control: &DispatchControl{Action: "dispatch", SessionMode: "continue", QueueMode: "steer"}}
	if err := c.validateControl(); err != nil {
		t.Fatal(err)
	}
	c.Control.SessionMode = "fresh"
	if err := c.validateControl(); err == nil {
		t.Fatal("fresh steer accepted")
	}
}
