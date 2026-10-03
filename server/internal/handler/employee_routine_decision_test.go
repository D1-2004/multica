package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// newEmployeeRoutineDecisionFixture is an employee_decide routine whose agent
// has a real scene worker, a scripted model and a dispatch endpoint.
func newEmployeeRoutineDecisionFixture(t *testing.T, instructions string) (*employeeRoutineHandlerFixture, *employeeWakeTestModel) {
	t.Helper()
	x := newEmployeeRoutineHandlerFixture(t, instructions)
	f, ctx := x.f, context.Background()
	model := &employeeWakeTestModel{respond: func(int) (string, map[string]any) {
		return wakeToolCall("call-quiet", "stay_quiet", map[string]any{})
	}}
	f.h.EmployeeLoopReady = func(context.Context, pgtype.UUID, pgtype.UUID) error { return nil }
	f.h.EmployeeSceneWorker = NewEmployeeSceneWorker(f.h, model)
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return nil }
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dispatch_endpoint(workspace_id,agent_id,actor_user_id,endpoint_id,dispatch_url) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'https://router.example.test/dispatch')`, testWorkspaceID, x.a.ID, testUserID, "routine-decision-"+x.a.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, statement := range []string{
			`DELETE FROM employee_routine_decision WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_host_notice WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_event_consumption WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_scene_job WHERE agent_id=$1::uuid`,
			`DELETE FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`,
		} {
			_, _ = testPool.Exec(bg, statement, x.a.ID)
		}
	})
	decide := contextcap.RoutineEmployeeDecide
	if _, err := f.h.updateSceneRoutine(ctx, x.a, mustRoutine(t, f, x.a, x.routine.ID), routineMember(), sceneRoutinePatch{EmployeeExecution: &decide}); err != nil {
		t.Fatal(err)
	}
	return x, model
}

type routineDecisionState struct {
	State, Reason, QueueID, JobID, TaskID string
	RunStatus                             string
	RunTask                               pgtype.UUID
	RunResult                             []byte
}

func (x *employeeRoutineHandlerFixture) decisionState(t *testing.T, runID pgtype.UUID) routineDecisionState {
	t.Helper()
	var s routineDecisionState
	if err := testPool.QueryRow(context.Background(), `SELECT d.state,d.reason,COALESCE(d.queue_task_id::text,''),d.job_id::text,d.employee_task_id::text,ar.status,ar.task_id,COALESCE(ar.result,'{}'::jsonb)
 FROM employee_routine_decision d JOIN autopilot_run ar ON ar.id=d.autopilot_run_id WHERE d.autopilot_run_id=$1`, runID).
		Scan(&s.State, &s.Reason, &s.QueueID, &s.JobID, &s.TaskID, &s.RunStatus, &s.RunTask, &s.RunResult); err != nil {
		t.Fatal("decision state", err)
	}
	return s
}

func (x *employeeRoutineHandlerFixture) fireDecision(t *testing.T, planned time.Time) *db.AutopilotRun {
	t.Helper()
	run, err := x.f.h.AutopilotService.DispatchAutopilotForPlan(context.Background(), x.ap, x.trigger.ID, "schedule", nil, planned)
	if err != nil || run.Status != "running" || run.TaskID.Valid {
		t.Fatalf("decision occurrence = %+v err=%v", run, err)
	}
	return run
}

func (x *employeeRoutineHandlerFixture) processWake(t *testing.T) {
	t.Helper()
	if worked, err := x.f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("decision wake: worked=%v err=%v", worked, err)
	}
}

// TestEmployeeRoutineDecisionWakeDispatchesFrozenRule: the model sees the
// frozen rule, recent outcomes and only the routine's own actions, and its
// dispatch starts exactly the frozen packet once, with routine notices.
func TestEmployeeRoutineDecisionWakeDispatchesFrozenRule(t *testing.T) {
	x, model := newEmployeeRoutineDecisionFixture(t, "FROZEN_DECISION_V1: remind only when the weekly report is missing.")
	ctx := context.Background()
	model.respond = func(int) (string, map[string]any) {
		return wakeToolCall("call-run", "run_routine", map[string]any{"reason": "the weekly report is missing"})
	}
	run := x.fireDecision(t, time.Now().UTC().Truncate(15*time.Minute))
	// Edited after acceptance: the decision and its run keep the frozen rule.
	changed := "CHANGED_DECISION_V2: do something else."
	if _, err := x.f.h.updateSceneRoutine(ctx, x.a, mustRoutine(t, x.f, x.a, x.routine.ID), routineMember(), sceneRoutinePatch{Instructions: &changed}); err != nil {
		t.Fatal(err)
	}
	x.processWake(t)
	if model.calls != 1 {
		t.Fatal("decision used model calls", model.calls)
	}
	first := string(model.requests[0])
	for _, want := range []string{"Routine decision context from the Host", "FROZEN_DECISION_V1", "run_routine", "wait_for_next_occurrence", "stay_quiet", "ROUTINE DECISION"} {
		if !strings.Contains(first, want) {
			t.Fatalf("model input lacks %q", want)
		}
	}
	for _, forbidden := range []string{"CHANGED_DECISION_V2", `"name":"dispatch_task"`, `"name":"scene_config_get"`} {
		if strings.Contains(first, forbidden) {
			i := strings.Index(first, forbidden)
			t.Fatalf("model input offers %q: …%s…", forbidden, first[max(0, i-200):min(len(first), i+200)])
		}
	}
	state := x.decisionState(t, run.ID)
	if state.State != "dispatched" || state.QueueID == "" || uuidToString(state.RunTask) != state.QueueID || state.Reason != "the weekly report is missing" {
		t.Fatalf("decision = %+v", state)
	}
	if n := x.count(t, `SELECT count(*) FROM response_action WHERE id=$1`, x.noticeID(run.ID, "start")); n != 1 {
		t.Fatal("dispatched decision start notice", n)
	}
	auth := service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{x.runtime.ID}}
	claimed := x.claimExact(t, auth, parseUUID(state.QueueID))
	req := newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, x.daemonID)
	req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityEmployeeDirectV1)
	resp, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, claimed, x.runtime, "", uuidToString(x.runtime.ID), testWorkspaceID)
	if failure != nil || !strings.Contains(resp.DirectTaskPrompt, "FROZEN_DECISION_V1") || strings.Contains(resp.DirectTaskPrompt, "CHANGED_DECISION_V2") {
		t.Fatalf("decided claim failure=%v prompt=%q", failure, resp.DirectTaskPrompt)
	}
	var jobState, kind string
	var attempts int
	if err := testPool.QueryRow(ctx, `SELECT state,kind,model_attempts FROM employee_scene_job WHERE id=$1::uuid`, state.JobID).Scan(&jobState, &kind, &attempts); err != nil || jobState != "completed" || kind != "task_wake" || attempts != 1 {
		t.Fatal("wake job", jobState, kind, attempts, err)
	}
}

// TestEmployeeRoutineDecisionQuietReplyWaitSettleWithoutRun covers the three
// outcomes that end the occurrence without a Run.
func TestEmployeeRoutineDecisionQuietReplyWaitSettleWithoutRun(t *testing.T) {
	cases := map[string]struct {
		call  func(int) (string, map[string]any)
		state string
		run   string
		sends int
	}{
		"quiet": {call: func(int) (string, map[string]any) { return wakeToolCall("call-quiet", "stay_quiet", map[string]any{}) }, state: "quiet", run: "completed"},
		"wait": {call: func(int) (string, map[string]any) {
			return wakeToolCall("call-wait", "wait_for_next_occurrence", map[string]any{"reason": "the report is not due yet"})
		}, state: "waited", run: "completed"},
		"reply": {call: func(int) (string, map[string]any) {
			return wakeToolCall("call-reply", "reply", map[string]any{"reply": "周报还没交，请记得在今天下班前提交。"})
		}, state: "replied", run: "completed", sends: 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			x, model := newEmployeeRoutineDecisionFixture(t, "Remind the group only when the weekly report is missing.")
			model.respond = tc.call
			run := x.fireDecision(t, time.Now().UTC().Truncate(15*time.Minute))
			x.processWake(t)
			state := x.decisionState(t, run.ID)
			var result map[string]map[string]string
			_ = json.Unmarshal(state.RunResult, &result)
			if state.State != tc.state || state.RunStatus != tc.run || state.RunTask.Valid || state.QueueID != "" || result["employee_decision"]["state"] != tc.state {
				var jobState, lastError string
				var outcome []byte
				_ = testPool.QueryRow(context.Background(), `SELECT state,COALESCE(last_error,''),COALESCE(outcome,'{}'::jsonb) FROM employee_scene_job WHERE id=$1::uuid`, state.JobID).Scan(&jobState, &lastError, &outcome)
				t.Fatalf("decision = %+v result=%s job=%s err=%s outcome=%s", state, state.RunResult, jobState, lastError, outcome)
			}
			if n := x.count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid`, x.a.ID); n != 0 {
				t.Fatal("a non-dispatch decision queued work", n)
			}
			if n := x.count(t, `SELECT count(*) FROM employee_task_run WHERE task_id=$1::uuid`, state.TaskID); n != 0 {
				t.Fatal("a non-dispatch decision invented a Run", n)
			}
			if n := x.count(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid`, x.a.ID); n != tc.sends {
				t.Fatal("scene sends", n)
			}
			if tc.sends == 1 {
				if n := x.count(t, `SELECT count(*) FROM response_action WHERE request_id=$1 AND input->>'scene_id'=$2`, "scene-notice:"+state.JobID, ctxcapScene); n != 1 {
					t.Fatal("reply did not go to the routine's scene", n)
				}
			}
			if model.calls != 1 {
				t.Fatal("model calls", model.calls)
			}
		})
	}
}

// TestEmployeeRoutineDecisionBudgetFailureIsPersisted: a model that never
// decides uses at most three requests and the occurrence records a failure.
func TestEmployeeRoutineDecisionBudgetFailureIsPersisted(t *testing.T) {
	x, model := newEmployeeRoutineDecisionFixture(t, "Remind the group only when the weekly report is missing.")
	model.respond = func(call int) (string, map[string]any) {
		return wakeToolCall("call-bad", "dispatch_task", map[string]any{"goal": "escape the routine"})
	}
	run := x.fireDecision(t, time.Now().UTC().Truncate(15*time.Minute))
	x.processWake(t)
	state := x.decisionState(t, run.ID)
	if model.calls > 3 || state.State != "failed" || state.RunStatus != "failed" || state.QueueID != "" {
		t.Fatalf("calls=%d decision=%+v", model.calls, state)
	}
	var attempts int
	if err := testPool.QueryRow(context.Background(), `SELECT model_attempts FROM employee_scene_job WHERE id=$1::uuid`, state.JobID).Scan(&attempts); err != nil || attempts != model.calls {
		t.Fatal("persisted model attempts", attempts, model.calls, err)
	}
	if n := x.count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid`, x.a.ID); n != 0 {
		t.Fatal("unregistered tool escaped the routine", n)
	}
}

// TestEmployeeRoutineDecisionModeSwitchIsGated: employee_decide can only be
// chosen while every replica runs the decision reader; the view and the scene
// MCP tools expose the choice.
func TestEmployeeRoutineDecisionModeSwitchIsGated(t *testing.T) {
	x := newEmployeeRoutineHandlerFixture(t, "Gated mode switch.")
	f, ctx := x.f, context.Background()
	decide := contextcap.RoutineEmployeeDecide
	f.h.EmployeeSceneWorker = &EmployeeSceneWorker{ReplicaReady: func(context.Context) error { return errors.New("a replica lacks the marker") }}
	if _, err := f.h.updateSceneRoutine(ctx, x.a, mustRoutine(t, f, x.a, x.routine.ID), routineMember(), sceneRoutinePatch{EmployeeExecution: &decide}); routineCode(err) != "routine_decision_unavailable" {
		t.Fatal("gate closed but employee_decide accepted", err)
	}
	bad := "sometimes"
	if _, err := f.h.updateSceneRoutine(ctx, x.a, mustRoutine(t, f, x.a, x.routine.ID), routineMember(), sceneRoutinePatch{EmployeeExecution: &bad}); routineCode(err) != "invalid_routine" {
		t.Fatal("unknown execution accepted", err)
	}
	f.h.EmployeeSceneWorker = &EmployeeSceneWorker{ReplicaReady: func(context.Context) error { return nil }}
	result, err := f.h.updateSceneRoutine(ctx, x.a, mustRoutine(t, f, x.a, x.routine.ID), routineMember(), sceneRoutinePatch{EmployeeExecution: &decide})
	if err != nil || result.Routine.EmployeeExecution != decide {
		t.Fatal(result.Routine, err)
	}
	if routine := mustRoutine(t, f, x.a, x.routine.ID); routine.EmployeeExecution != decide {
		t.Fatal("choice not stored", routine.EmployeeExecution)
	}
	// A webhook delivery always runs: employee_decide needs a schedule.
	if _, err := normalizeRoutineInput(sceneRoutineInput{Title: "Hook", Instructions: "Summarize the delivery.", EmployeeExecution: decide,
		Trigger: sceneRoutineTrigger{Kind: sceneRoutineTriggerHook}}); routineCode(err) != "invalid_routine" {
		t.Fatal("webhook routine accepted employee_decide", err)
	}
	if _, err := normalizeRoutineInput(sceneRoutineInput{Title: "Hook", Instructions: "Summarize the delivery.", EmployeeExecution: contextcap.RoutineRunOnly,
		Trigger: sceneRoutineTrigger{Kind: sceneRoutineTriggerHook}}); err != nil {
		t.Fatal("webhook routine refused run_only", err)
	}
	for _, def := range sceneConfigToolDefinitions("group") {
		tool := def.(map[string]any)
		name := tool["name"].(string)
		if name != sceneConfigToolRoutineCreate && name != sceneConfigToolRoutineUpdate {
			continue
		}
		props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if _, ok := props["employee_execution"]; !ok {
			t.Fatalf("%s lacks employee_execution", name)
		}
	}
}
