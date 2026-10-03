package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	openai "github.com/openai/openai-go/v3"
)

type employeeSteerModel struct {
	calls     int
	sourceRef string
}

func (m *employeeSteerModel) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	m.calls++
	args, _ := json.Marshal(map[string]any{"source_ref": m.sourceRef, "correction": "不对，只统计已签约客户", "reply": "收到，已按新要求调整正在进行的任务。"})
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-steer", "type": "function", "function": map[string]any{"name": "steer_task", "arguments": string(args)}}}}}}})
	var result openai.ChatCompletion
	err := json.Unmarshal(raw, &result)
	return &result, err
}

// A follow-up message from the same requester steers the running Direct task
// through EmployeeLoop: one model call, no new EmployeeTask, the old run is
// exit-fenced and the successor answers the correction message.
func TestEmployeeLoopSteerTaskToolInterruptsRequesterTask(t *testing.T) {
	f := employeeNoticeDatabase(t, "running", false, false)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET status='online' WHERE id=(SELECT runtime_id FROM agent_task_queue WHERE id=$1::uuid)`, f.queueID); err != nil {
		t.Fatal(err)
	}
	var endpointID string
	var namespaceID pgtype.UUID
	if err := testPool.QueryRow(ctx, `SELECT endpoint_id,id FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, f.agentID).Scan(&endpointID, &namespaceID); err != nil {
		t.Fatal(err)
	}
	dc := agentDispatchContext{EndpointID: endpointID, EndpointNamespaceID: namespaceID, UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	model := &employeeSteerModel{}
	f.h.EmployeeSceneWorker = NewEmployeeSceneWorker(f.h, model)
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return nil }
	f.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "message-2", Text: "不对，只统计已签约客户"}}
	if w := employeeHTTP(t, f.dingTalkResponseFixture, dc, uuid.NewString()); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	var jobID string
	var items []employeeentry.Item
	if err := testPool.QueryRow(ctx, `SELECT id::text,items FROM employee_scene_job WHERE agent_id=$1::uuid AND id<>$2::uuid`, f.agentID, f.jobID).Scan(&jobID, &items); err != nil {
		t.Fatal(err)
	}
	model.sourceRef = items[0].ReceiptID + "/message-2"
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls=%d", model.calls)
	}
	old, err := f.h.Queries.GetAgentTask(ctx, parseUUID(f.queueID))
	if err != nil || old.Status != "cancelled" {
		t.Fatalf("original run: %+v %v", old, err)
	}
	var tasks, runs int
	var successorContext []byte
	if err = testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task WHERE agent_id=$1::uuid),(SELECT count(*) FROM employee_task_run WHERE agent_id=$1::uuid),(SELECT q.context FROM agent_task_queue q WHERE q.context->>'steer_predecessor_task_id'=$2)`, f.agentID, f.queueID).Scan(&tasks, &runs, &successorContext); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || runs != 2 {
		t.Fatalf("steer created new work: tasks=%d runs=%d", tasks, runs)
	}
	var private map[string]any
	if err = json.Unmarshal(successorContext, &private); err != nil {
		t.Fatal(err)
	}
	if private["employee_job_id"] != f.jobID || !strings.Contains(private["direct_task_prompt"].(string), "不对，只统计已签约客户") {
		t.Fatalf("successor must keep the original delivery binding and carry the correction: %s", successorContext)
	}
	var outcome []byte
	if err = testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE id=$1::uuid`, jobID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(outcome), "收到，已按新要求调整") {
		t.Fatalf("acknowledgement not recorded: %s", outcome)
	}
	// The requester receives the corrected result: the replaced run is silent
	// and the successor's notice is enqueued for the original request.
	var successorQueue, successorRun string
	if err = testPool.QueryRow(ctx, `SELECT q.id::text,r.id::text FROM agent_task_queue q JOIN employee_task_run r ON r.queue_task_id=q.id WHERE q.context->>'steer_predecessor_task_id'=$1`, f.queueID).Scan(&successorQueue, &successorRun); err != nil {
		t.Fatal(err)
	}
	if err = f.h.TaskService.AcknowledgeTaskProcessStopped(ctx, parseUUID(f.queueID)); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',dispatched_at=now(),started_at=now() WHERE id=$1::uuid`, successorQueue); err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.TaskService.CompleteTask(ctx, parseUUID(successorQueue), []byte(`{"output":"已签约客户统计完成"}`), "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = reconcileEmployeeNotice(t, f.h); err != nil {
		t.Fatal(err)
	}
	notices := map[string][2]string{}
	rows, err := testPool.Query(ctx, `SELECT run_id::text,state,COALESCE(reason,'') FROM employee_run_notice WHERE agent_id=$1::uuid`, f.agentID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var run, state, reason string
		if err = rows.Scan(&run, &state, &reason); err != nil {
			t.Fatal(err)
		}
		notices[run] = [2]string{state, reason}
	}
	rows.Close()
	if notices[f.runID] != [2]string{"suppressed", "steered"} || notices[successorRun][0] != "enqueued" {
		t.Fatalf("notices: replaced=%v successor=%v", notices[f.runID], notices[successorRun])
	}
}

// With a running task and a recently finished one, the Host never guesses:
// it returns both candidates instead of interrupting the running task.
func TestEmployeeSteerTargetRequiresSingleCandidate(t *testing.T) {
	f := employeeNoticeDatabase(t, "running", false, false)
	ctx := context.Background()
	task := employeeNoticeTask(t, f)
	host := &employeeSceneHost{worker: f.h.EmployeeSceneWorker, job: employeeentry.Job{Scope: employeeentry.Scope{WorkspaceID: task.Scope.WorkspaceID, AgentID: task.Scope.AgentID, TenantOrgID: task.Scope.TenantOrgID, SceneID: task.Scope.Scene.SceneID}}}
	only, candidates, err := host.steerTarget(ctx, task.RequesterRef, "")
	if err != nil || only.ID != task.ID || candidates != nil {
		t.Fatalf("single running task: %+v %v %v", only, candidates, err)
	}
	other, err := employeetask.NewStore(testPool).Create(ctx, employeetask.CreateParams{Scope: task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: task.RequesterRef, Definition: employeetask.Definition{Goal: "Earlier report"}, Source: employeetask.Source{Namespace: "steer_target_test", Key: uuid.NewString()}, Input: "Earlier report"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE employee_task SET state='succeeded', updated_at=now() WHERE id=$1::uuid`, other.ID); err != nil {
		t.Fatal(err)
	}
	none, candidates, err := host.steerTarget(ctx, task.RequesterRef, "")
	if err != nil || none.ID != "" || len(candidates) != 2 {
		t.Fatalf("ambiguous targets must be listed: %+v %+v %v", none, candidates, err)
	}
	if _, _, err = host.steerTarget(ctx, "dingtalk:other:uid:someone", task.ID); err == nil {
		t.Fatal("explicit task_id of another requester accepted")
	}
	if _, err = testPool.Exec(ctx, `UPDATE employee_task SET state='cancelled' WHERE id=$1::uuid`, other.ID); err != nil {
		t.Fatal(err)
	}
	if only, candidates, err = host.steerTarget(ctx, task.RequesterRef, ""); err != nil || only.ID != task.ID || candidates != nil {
		t.Fatalf("a stopped task is never an implicit target: %+v %v %v", only, candidates, err)
	}
}

// A lifecycle v2 goal waiting on its dependencies is active work: it stays an
// implicit steer candidate however long the wait lasts, like a running task.
func TestEmployeeSteerTargetIncludesWaitingGoal(t *testing.T) {
	f := employeeNoticeDatabase(t, "running", false, false)
	ctx := context.Background()
	task := employeeNoticeTask(t, f)
	host := &employeeSceneHost{worker: f.h.EmployeeSceneWorker, job: employeeentry.Job{Scope: employeeentry.Scope{WorkspaceID: task.Scope.WorkspaceID, AgentID: task.Scope.AgentID, TenantOrgID: task.Scope.TenantOrgID, SceneID: task.Scope.Scene.SceneID}}}
	if _, err := testPool.Exec(ctx, `UPDATE employee_task SET state='cancelled', active_run_id=NULL WHERE id=$1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	store := employeetask.NewStore(testPool)
	goal, err := store.Create(ctx, employeetask.CreateParams{Scope: task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: task.RequesterRef, Definition: employeetask.Definition{Goal: "Collect three answers"}, Source: employeetask.Source{Namespace: "steer_target_test", Key: uuid.NewString()}, Input: "Collect three answers", Lifecycle: employeetask.LifecycleV2, CompletionMode: employeetask.CompletionExplicitGoal})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_task_wait WHERE task_id=$1::uuid`, goal.ID)
	})
	goal, _, err = store.WaitTask(ctx, goal.Scope, goal.ID, employeetask.WaitParams{Source: employeetask.Source{Namespace: "collection", Key: "c1/open"}, Kind: employeetask.WaitCollection, RefID: "c1", Mandatory: true, AuthorityRef: "employee_task_entry:" + goal.ID + "/1"})
	if err != nil || goal.State != employeetask.StateWaiting {
		t.Fatalf("waiting goal: %+v %v", goal, err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE employee_task SET updated_at=now()-interval '2 days' WHERE id=$1::uuid`, goal.ID); err != nil {
		t.Fatal(err)
	}
	only, candidates, err := host.steerTarget(ctx, task.RequesterRef, "")
	if err != nil || only.ID != goal.ID || candidates != nil {
		t.Fatalf("waiting goal is not an active candidate: %+v %+v %v", only, candidates, err)
	}
}
