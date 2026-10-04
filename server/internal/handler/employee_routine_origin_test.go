package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// employeeRoutineHandlerFixture is a scene routine of an employee-mode agent
// whose runtime is a local daemon that advertises Employee Direct.
type employeeRoutineHandlerFixture struct {
	f        *ctxcapFixture
	a        contextCapAgent
	runtime  db.AgentRuntime
	daemonID string
	ap       db.Autopilot
	trigger  db.AutopilotTrigger
	routine  sceneRoutineView
}

func newEmployeeRoutineHandlerFixture(t *testing.T, instructions string) *employeeRoutineHandlerFixture {
	t.Helper()
	f, a := routineFixture(t)
	ctx := context.Background()
	x := &employeeRoutineHandlerFixture{f: f, a: a, daemonID: "routine-daemon-" + uuid.NewString()}
	runtimeID := uuid.NewString()
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_runtime(id,workspace_id,daemon_id,name,runtime_mode,provider,status,owner_id,metadata,last_seen_at) VALUES($1::uuid,$2::uuid,$3,'Routine claim runtime','local','codex','online',$4::uuid,'{"client_capabilities":["employee-direct-v1"]}',now())`, runtimeID, testWorkspaceID, x.daemonID, testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, statement := range []string{
			`DELETE FROM employee_routine_occurrence WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_learning_consumption WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_learning WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_run_notice WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_task_entry WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_task_run WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_task WHERE agent_id=$1::uuid`,
			`DELETE FROM scene_event_receipt WHERE agent_id=$1::uuid`,
			`DELETE FROM task_usage WHERE task_id IN (SELECT id FROM agent_task_queue WHERE agent_id=$1::uuid)`,
			`DELETE FROM task_message WHERE task_id IN (SELECT id FROM agent_task_queue WHERE agent_id=$1::uuid)`,
			`DELETE FROM agent_task_queue WHERE agent_id=$1::uuid`,
			`UPDATE agent SET runtime_id=$2::uuid WHERE id=$1::uuid`,
		} {
			args := []any{a.ID}
			if strings.Contains(statement, "$2") {
				args = append(args, testRuntimeID)
			}
			if _, err := testPool.Exec(bg, statement, args...); err != nil {
				t.Error(statement, err)
			}
		}
		_, _ = testPool.Exec(bg, `DELETE FROM agent_runtime WHERE id=$1::uuid`, runtimeID)
	})
	if _, err := testPool.Exec(ctx, `UPDATE agent SET runtime_id=$2::uuid,runtime_mode='local',coordination_mode='employee' WHERE id=$1::uuid`, a.ID, runtimeID); err != nil {
		t.Fatal(err)
	}
	var err error
	if x.runtime, err = f.h.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID)); err != nil {
		t.Fatal(err)
	}
	// Every live replica advertises the reader in this fixture.
	f.h.EmployeeSceneWorker = &EmployeeSceneWorker{ReplicaReady: func(context.Context) error { return nil }}
	created := createGroupRoutine(t, f, a, sceneRoutineInput{Title: "Daily digest", Instructions: instructions, Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "*/15 * * * *"}})
	x.routine = created.Routine
	if x.ap, err = f.h.Queries.GetAutopilot(ctx, parseUUID(created.Routine.AutopilotID)); err != nil {
		t.Fatal(err)
	}
	triggers, err := f.h.Queries.ListAutopilotTriggers(ctx, x.ap.ID)
	if err != nil || len(triggers) != 1 {
		t.Fatal(triggers, err)
	}
	x.trigger = triggers[0]
	return x
}

func (x *employeeRoutineHandlerFixture) noticeID(runID pgtype.UUID, phase string) string {
	return dingtalkresponse.StableActionID(testWorkspaceID, x.a.ID, dingtalkresponse.RoutineNoticeRequestID(uuidToString(runID), phase), "message.send")
}

type routineQueueState struct {
	Status, Runtime, Agent string
	FireAt, DispatchedAt   pgtype.Timestamptz
	AgentActive            int
	RuntimeQueued          int
}

// queueState reports the queue row and the competing work of its agent and
// runtime, so an isolation failure names the state that leaked into it.
func (x *employeeRoutineHandlerFixture) queueState(t *testing.T, id pgtype.UUID) routineQueueState {
	t.Helper()
	var s routineQueueState
	if err := testPool.QueryRow(context.Background(), `SELECT q.status,q.runtime_id::text,q.agent_id::text,q.fire_at,q.dispatched_at,
 (SELECT count(*) FROM agent_task_queue a WHERE a.agent_id=q.agent_id AND a.id<>q.id AND a.status IN ('queued','dispatched','running','deferred')),
 (SELECT count(*) FROM agent_task_queue r WHERE r.runtime_id=q.runtime_id AND r.id<>q.id AND r.status IN ('queued','dispatched'))
 FROM agent_task_queue q WHERE q.id=$1`, id).Scan(&s.Status, &s.Runtime, &s.Agent, &s.FireAt, &s.DispatchedAt, &s.AgentActive, &s.RuntimeQueued); err != nil {
		t.Fatal(err)
	}
	return s
}

// claimExact claims this fixture's own execution through the product claim
// path. The runtime and agent belong to this fixture only; a miss reports the
// row and any competing work instead of a bare nil.
func (x *employeeRoutineHandlerFixture) claimExact(t *testing.T, auth service.TaskClaimAuthorization, id pgtype.UUID) *db.AgentTaskQueue {
	t.Helper()
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(context.Background(), x.runtime.ID, auth)
	if err != nil || claimed == nil || claimed.ID != id {
		t.Fatalf("routine execution not claimable on its own runtime: claimed=%v err=%v state=%+v", claimed, err, x.queueState(t, id))
	}
	return claimed
}

func (x *employeeRoutineHandlerFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(sql, err)
	}
	return n
}

// TestEmployeeRoutineClaimRunsFrozenPacketWithSingleNoticeOwner drives one
// scheduled occurrence through the real producer, claim, model-input build,
// completion and every background reader.
func TestEmployeeRoutineClaimRunsFrozenPacketWithSingleNoticeOwner(t *testing.T) {
	x := newEmployeeRoutineHandlerFixture(t, "FROZEN_ROUTINE_V1: list yesterday's open questions.")
	f, ctx := x.f, context.Background()
	previousResponses := testHandler.DingTalkResponses
	testHandler.DingTalkResponses = f.h.DingTalkResponses
	t.Cleanup(func() { testHandler.DingTalkResponses = previousResponses })

	planned := time.Now().UTC().Truncate(15 * time.Minute)
	run, err := f.h.AutopilotService.DispatchAutopilotForPlan(ctx, x.ap, x.trigger.ID, "schedule", nil, planned)
	if err != nil || run.Status != "running" || !run.TaskID.Valid {
		t.Fatalf("run = %+v err=%v", run, err)
	}
	var startText string
	if err := testPool.QueryRow(ctx, `SELECT input->>'text' FROM response_action WHERE id=$1`, x.noticeID(run.ID, dingtalkresponse.RoutineNoticeStart)).Scan(&startText); err != nil || !strings.Contains(startText, "Daily digest") || strings.Contains(startText, "FROZEN_ROUTINE_V1") {
		t.Fatalf("start notice %q err=%v", startText, err)
	}

	// Edit the routine after acceptance: the accepted occurrence must not move.
	changed := "CHANGED_ROUTINE_V2: do something else."
	if _, err := f.h.updateSceneRoutine(ctx, x.a, mustRoutine(t, f, x.a, x.routine.ID), routineMember(), sceneRoutinePatch{Instructions: &changed}); err != nil {
		t.Fatal(err)
	}

	auth := service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{x.runtime.ID}}
	claimed := x.claimExact(t, auth, run.TaskID)
	old := newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, x.daemonID)
	if _, _, _, _, failure := testHandler.buildClaimedTaskResponse(old, claimed, x.runtime, "", uuidToString(x.runtime.ID), testWorkspaceID); failure == nil {
		t.Fatal("a daemon without Employee Direct received the routine packet")
	}
	if state := x.queueState(t, run.TaskID); state.Status != "deferred" {
		t.Fatalf("old daemon claim did not defer the routine execution: %+v", state)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET fire_at=now()-interval '1 second' WHERE id=$1 AND status='deferred'`, run.TaskID); err != nil {
		t.Fatal(err)
	}
	claimed = x.claimExact(t, auth, run.TaskID)
	req := newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, x.daemonID)
	req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityEmployeeDirectV1)
	resp, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, claimed, x.runtime, "", uuidToString(x.runtime.ID), testWorkspaceID)
	if failure != nil {
		t.Fatal(failure.message)
	}
	if !strings.Contains(resp.DirectTaskPrompt, "FROZEN_ROUTINE_V1") || strings.Contains(resp.DirectTaskPrompt, "CHANGED_ROUTINE_V2") ||
		!strings.Contains(resp.DirectTaskPrompt, employeeRoutineOutputInstruction) || strings.Contains(resp.DirectTaskPrompt, employeeDirectOutputInstruction) ||
		resp.AutopilotDescription != "" || resp.AutopilotRunID != uuidToString(run.ID) || resp.WorkspaceID != testWorkspaceID {
		t.Fatalf("claim prompt=%q autopilot=%q run=%q", resp.DirectTaskPrompt, resp.AutopilotDescription, resp.AutopilotRunID)
	}
	// The model input the daemon builds from this wire payload.
	wire, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var task daemon.Task
	if err := json.Unmarshal(wire, &task); err != nil {
		t.Fatal(err)
	}
	modelInput := daemon.BuildPrompt(task, "codex")
	if !strings.HasPrefix(modelInput, "Work packet:") || !strings.Contains(modelInput, "FROZEN_ROUTINE_V1") ||
		strings.Contains(modelInput, "CHANGED_ROUTINE_V2") || strings.Contains(modelInput, "Autopilot instructions") {
		t.Fatalf("model input = %s", modelInput)
	}

	queueID := uuidToString(claimed.ID)
	w := httptest.NewRecorder()
	testHandler.StartTask(w, withURLParam(newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, x.daemonID), "taskId", queueID))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	testHandler.CompleteTask(w, withURLParam(newDaemonTokenRequest(http.MethodPost, "/", map[string]any{"output": "ROUTINE_OUTPUT_OK", "session_id": "routine-session"}, testWorkspaceID, x.daemonID), "taskId", queueID))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var runState, result, taskState string
	if err := testPool.QueryRow(ctx, `SELECT r.state,r.result,t.state FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id WHERE r.queue_task_id=$1::uuid`, queueID).Scan(&runState, &result, &taskState); err != nil || runState != "succeeded" || taskState != "succeeded" || result != "ROUTINE_OUTPUT_OK" {
		t.Fatal(runState, taskState, result, err)
	}
	var endText string
	if err := testPool.QueryRow(ctx, `SELECT input->>'text' FROM response_action WHERE id=$1`, x.noticeID(run.ID, dingtalkresponse.RoutineNoticeEnd)).Scan(&endText); err != nil || !strings.Contains(endText, "ROUTINE_OUTPUT_OK") {
		t.Fatalf("end notice %q err=%v", endText, err)
	}

	// The task event was not delivered (no listener here): the durable
	// reconciler settles the AutopilotRun without a second end notice.
	settled, err := f.h.ReconcileEmployeeRoutineRuns(ctx, 100)
	if err != nil || settled < 1 {
		t.Fatal(settled, err)
	}
	if again, err := f.h.ReconcileEmployeeRoutineRuns(ctx, 100); err != nil || again != 0 {
		t.Fatal("settled twice", again, err)
	}
	apRun, err := f.h.Queries.GetAutopilotRun(ctx, run.ID)
	if err != nil || apRun.Status != "completed" {
		t.Fatal(apRun.Status, err)
	}

	// The message Run notice, the execution fact and requester-private
	// learning all leave the routine origin alone.
	if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	f.h.EmployeeMemory = employeememory.NewStore(testPool)
	for range 20 {
		n, err := f.h.ReconcileEmployeeLearnings(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	runID := ""
	if err := testPool.QueryRow(ctx, `SELECT id::text FROM employee_task_run WHERE queue_task_id=$1::uuid`, queueID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input->>'routine_run_id'=$2`, x.a.ID, uuidToString(run.ID)); n != 2 {
		t.Fatal("routine notices are not exactly start and end", n)
	}
	if n := x.count(t, `SELECT count(*) FROM employee_run_notice WHERE run_id=$1::uuid`, runID); n != 0 {
		t.Fatal("message Run notice also sent the routine result", n)
	}
	if n := x.count(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input ? 'employee_run_notice_id'`, x.a.ID); n != 0 {
		t.Fatal("a second sender enqueued a notice", n)
	}
	if n := x.count(t, `SELECT count(*) FROM scene_event_receipt WHERE agent_id=$1::uuid AND source='employee.execution' AND source_event_id=$2`, x.a.ID, runID); n != 0 {
		t.Fatal("routine run recorded as a message execution fact", n)
	}
	if n := x.count(t, `SELECT count(*) FROM agent_task_queue WHERE id=$1::uuid AND context ? 'employee_execution_event_skip'`, queueID); n != 0 {
		t.Fatal("execution event reader marked the routine run", n)
	}
	var reason string
	if err := testPool.QueryRow(ctx, `SELECT reason FROM employee_learning_consumption WHERE run_id=$1::uuid`, runID).Scan(&reason); err != nil || reason != "automation_origin" {
		t.Fatal("learning", reason, err)
	}
	if n := x.count(t, `SELECT count(*) FROM employee_learning WHERE agent_id=$1::uuid`, x.a.ID); n != 0 {
		t.Fatal("automation produced a private learning", n)
	}

	// The next occurrence uses the edited instructions; a B3 decision wake can
	// read the deterministic outcome history.
	next, err := f.h.AutopilotService.DispatchAutopilotForPlan(ctx, x.ap, x.trigger.ID, "schedule", nil, planned.Add(15*time.Minute))
	if err != nil || next.Status != "running" {
		t.Fatal(next, err)
	}
	nextQueue, err := f.h.Queries.GetAgentTask(ctx, next.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	nextDirect, _ := service.ParseDirectTaskContext(nextQueue)
	if !strings.Contains(nextDirect.Prompt, "CHANGED_ROUTINE_V2") {
		t.Fatal("next occurrence kept the old instructions")
	}
	if _, err := testPool.Exec(ctx, `UPDATE response_action SET state='delivered' WHERE id=$1`, x.noticeID(run.ID, dingtalkresponse.RoutineNoticeEnd)); err != nil {
		t.Fatal(err)
	}
	outcomes, err := service.ListRoutineOccurrenceOutcomes(ctx, testPool, testWorkspaceID, x.routine.ID, 10)
	if err != nil || len(outcomes) != 2 || !outcomes[1].Sent || outcomes[1].NoticeSHA256 == "" || outcomes[1].ResultSHA256 == "" || outcomes[0].Sent {
		t.Fatalf("outcomes = %+v err=%v", outcomes, err)
	}
}

// TestEmployeeRoutineReplicaGateKeepsAutopilotPath: while a live replica
// lacks the reader marker, the occurrence keeps the existing run_only shape.
func TestEmployeeRoutineReplicaGateKeepsAutopilotPath(t *testing.T) {
	x := newEmployeeRoutineHandlerFixture(t, "GATED_ROUTINE: say hello.")
	f, ctx := x.f, context.Background()
	f.h.EmployeeSceneWorker = &EmployeeSceneWorker{ReplicaReady: func(context.Context) error { return errEmployeeRoutineTestGate }}
	run, err := f.h.AutopilotService.DispatchAutopilotForPlan(ctx, x.ap, x.trigger.ID, "schedule", nil, time.Now().UTC().Truncate(15*time.Minute))
	if err != nil || run.Status != "running" || !run.TaskID.Valid {
		t.Fatal(run, err)
	}
	queue, err := f.h.Queries.GetAgentTask(ctx, run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if service.IsEmployeeDirectTask(queue) || !service.IsSceneRoutineContext(queue.Context) {
		t.Fatal("gate closed but the new queue shape was produced")
	}
	if n := x.count(t, `SELECT count(*) FROM employee_routine_occurrence WHERE agent_id=$1::uuid`, x.a.ID); n != 0 {
		t.Fatal("gate closed but a routine receipt was written", n)
	}
}

var errEmployeeRoutineTestGate = &sceneRoutineError{Code: "gate", Message: "live replicas lack the employee-loop marker"}
