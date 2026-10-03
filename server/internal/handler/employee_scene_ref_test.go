package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DS-12: a model copied a source_ref with a trailing quote. The Host accepts
// it only as the exact same reference, journals the canonical form, and every
// later provenance check (execution fact, Run notice) still matches.
func TestEmployeeSceneQuotedSourceRefKeepsDispatchProvenance(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback, f.command.ResponsePolicy = nil, nil
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1::uuid,$2::uuid,'123','456',$3::uuid)`, f.agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	ep, err := f.h.Queries.EnsureAgentDispatchEndpoint(ctx, db.EnsureAgentDispatchEndpointParams{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID), ActorUserID: parseUUID(testUserID), EndpointID: "ref-" + uuid.NewString(), DispatchUrl: "https://test.invalid/dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	dc.EndpointID, dc.EndpointNamespaceID = ep.EndpointID, ep.ID
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM employee_run_notice WHERE agent_id=$1::uuid`, f.agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, f.agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`, f.agentID)
	})
	if w := employeeHTTP(t, f, dc, uuid.NewString()); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	var receipt string
	if err = testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1::uuid`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	canonical := receipt + "/message-1"
	model.dispatch, model.sourceRef = true, canonical+`"`
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var journal []byte
	var runID, queueID string
	if err = testPool.QueryRow(ctx, `SELECT tool_journal FROM employee_scene_job WHERE agent_id=$1::uuid`, f.agentID).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 || !strings.Contains(string(journal), `"source_ref": "`+canonical+`"`) && !strings.Contains(string(journal), `"source_ref":"`+canonical+`"`) || strings.Contains(string(journal), canonical+`\"`) {
		t.Fatalf("quoted ref not canonicalized: calls=%d journal=%s", model.calls, journal)
	}
	if err = testPool.QueryRow(ctx, `SELECT id::text,queue_task_id::text FROM employee_task_run WHERE agent_id=$1::uuid`, f.agentID).Scan(&runID, &queueID); err != nil {
		t.Fatalf("quoted ref did not dispatch: %v", err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1::uuid`, queueID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.TaskService.CompleteTask(ctx, parseUUID(queueID), []byte(`{"output":"已完成"}`), "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var factState, factReason string
	if err = testPool.QueryRow(ctx, `SELECT c.state,c.reason FROM scene_event_receipt r JOIN employee_event_consumption c ON c.receipt_id=r.id WHERE r.agent_id=$1::uuid AND r.source='employee.execution' AND r.source_event_id=$2`, f.agentID, runID).Scan(&factState, &factReason); err != nil || factState != "completed" || factReason != "current_goal_revision" {
		t.Fatalf("execution fact lost provenance: %s %s %v", factState, factReason, err)
	}
	if _, err = f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var noticeState, noticeReason string
	if err = testPool.QueryRow(ctx, `SELECT state,reason FROM employee_run_notice WHERE run_id=$1::uuid`, runID).Scan(&noticeState, &noticeReason); err != nil || noticeState != "enqueued" {
		t.Fatalf("run notice held: %s %s %v", noticeState, noticeReason, err)
	}
}

// A reference that is not exactly a frozen source is refused before any
// effect; the refusal lists the valid references and the model corrects the
// call within the same three-request budget instead of ending the turn.
func TestEmployeeSceneRefusedSourceRefRetriesWithinBudget(t *testing.T) {
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback, f.command.ResponsePolicy = nil, nil
	if w := employeeHTTP(t, f, dc, uuid.NewString()); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1::uuid`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	dispatch := func(id, ref string) (string, map[string]any) {
		return wakeToolCall(id, "dispatch_task", map[string]any{"source_ref": ref, "goal": "Analyze feedback", "prompt": "Analyze feedback and report", "reply": "我来分析这些反馈。"})
	}
	model := &employeeWakeTestModel{respond: func(call int) (string, map[string]any) {
		if call == 1 {
			// One character off is a different reference, never fuzzy-matched.
			return dispatch("call-wrong", receipt+"/message-2")
		}
		return dispatch("call-right", receipt+"/message-1")
	}}
	f.h.EmployeeSceneWorker.model = model
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var outcome []byte
	var tasks int
	if err := testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE agent_id=$1::uuid`, f.agentID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task WHERE agent_id=$1::uuid`, f.agentID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	var saved employeeSavedOutcome
	if err := json.Unmarshal(outcome, &saved); err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 || tasks != 1 || saved.Failure != "" || saved.Outcome.Kind != "dispatched" || saved.Outcome.Reply != "我来分析这些反馈。" {
		t.Fatalf("refusal ended the turn: calls=%d tasks=%d outcome=%s", model.calls, tasks, outcome)
	}
	if !strings.Contains(string(model.requests[1]), receipt+"/message-1") || !strings.Contains(string(model.requests[1]), "valid source_ref values") {
		t.Fatalf("refusal did not tell the model the valid references: %s", model.requests[1])
	}
	var reply string
	if err := testPool.QueryRow(ctx, `SELECT input->>'text' FROM response_action WHERE agent_id=$1::uuid`, f.agentID).Scan(&reply); err != nil || reply != "我来分析这些反馈。" {
		t.Fatalf("acceptance reply: %q %v", reply, err)
	}
}
