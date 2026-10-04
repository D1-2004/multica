package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	canceled, err := testHandler.Queries.CancelAgentTaskForSteer(ctx, parseUUID(f.taskID))
	old := &canceled
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

func TestSteerIssueHumanAPIQueuesAndReplaysSameCorrection(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	f := createCommentDeliveryFixture(t, "human-steer-api")
	if _, err := testPool.Exec(ctx, `UPDATE issue SET assignee_type='agent',assignee_id=$2 WHERE id=$1`, f.issueID, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now(),dispatched_at=now(),delivered_comment_ids=coalesced_comment_ids||ARRAY[trigger_comment_id] WHERE id=$1`, f.taskID); err != nil {
		t.Fatal(err)
	}
	post := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/issues/"+f.issueID+"/steer", strings.NewReader(`{"content":"correction from the human API"}`))
		req.Header.Set("X-User-ID", testUserID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req.Header.Set("Idempotency-Key", key)
		req = withURLParams(req, "id", f.issueID)
		w := httptest.NewRecorder()
		testHandler.SteerIssue(w, req)
		return w
	}
	if w := post(""); w.Code != http.StatusBadRequest {
		t.Fatalf("missing replay key: %d %s", w.Code, w.Body.String())
	}
	first := post("human-correction")
	if first.Code != http.StatusAccepted {
		t.Fatalf("steer: %d %s", first.Code, first.Body.String())
	}
	var accepted AgentDispatchResponse
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.ControlResult == nil || accepted.ControlResult.PreemptedExternalTaskID != f.taskID {
		t.Fatalf("missing cancellation: %+v", accepted)
	}
	replay := post("human-correction")
	if replay.Code != http.StatusAccepted {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	var duplicate AgentDispatchResponse
	_ = json.Unmarshal(replay.Body.Bytes(), &duplicate)
	if duplicate.TaskID != accepted.TaskID || duplicate.CommentID != accepted.CommentID {
		t.Fatal("replay created another input or run")
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
