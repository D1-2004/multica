package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeeplan"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type employeePlanFixture struct {
	f       *dingTalkResponseFixture
	model   *employeeWakeTestModel
	scope   employeetask.Scope
	taskID  string
	planID  string
	receipt string
}

// employeePlanDatabase dispatches a real explicit-goal Task with a plan from
// one admitted message: step 1 is the dispatch prompt, the rest follow.
func employeePlanDatabase(t *testing.T, steps []map[string]any) employeePlanFixture {
	t.Helper()
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback, f.command.ResponsePolicy = nil, nil
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1::uuid,$2::uuid,'123','456',$3::uuid)`, f.agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	ep, err := f.h.Queries.EnsureAgentDispatchEndpoint(ctx, db.EnsureAgentDispatchEndpointParams{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID), ActorUserID: parseUUID(testUserID), EndpointID: "plan-" + uuid.NewString(), DispatchUrl: "https://test.invalid/dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	dc.EndpointID, dc.EndpointNamespaceID = ep.EndpointID, ep.ID
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_follow_up", "employee_task_plan", "employee_task_wait", "employee_task_link", "employee_host_notice", "employee_run_notice", "agent_dispatch_endpoint", "agent_dingtalk_identity"} {
			_, _ = testPool.Exec(ctx, `DELETE FROM `+table+` WHERE agent_id=$1::uuid`, f.agentID)
		}
	})
	if w := employeeHTTP(t, f, dc, uuid.NewString()); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	var receipt string
	if err = testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1::uuid`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	followUps := make([]any, 0, len(steps))
	for _, step := range steps {
		followUps = append(followUps, step)
	}
	model := &employeeWakeTestModel{respond: func(int) (string, map[string]any) {
		return wakeToolCall("call-plan", "dispatch_task", map[string]any{"source_ref": receipt + "/message-1", "goal": "Two-step report", "prompt": "Step one: collect the numbers", "reply": "我按计划来处理。", "follow_up_steps": followUps})
	}}
	f.h.EmployeeSceneWorker.model = model
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var taskID, sceneID, planID string
	if err = testPool.QueryRow(ctx, `SELECT t.id::text,t.scene_id::text,p.id::text FROM employee_task t JOIN employee_task_plan p ON p.task_id=t.id WHERE t.agent_id=$1::uuid`, f.agentID).Scan(&taskID, &sceneID, &planID); err != nil {
		t.Fatalf("plan dispatch: %v", err)
	}
	scope := employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "456", Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: sceneID}}
	return employeePlanFixture{f: f, model: model, scope: scope, taskID: taskID, planID: planID, receipt: receipt}
}

func (p employeePlanFixture) runs(t *testing.T) []employeetask.Run {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT id::text,queue_task_id::text,state FROM employee_task_run WHERE task_id=$1::uuid ORDER BY created_at,id`, p.taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []employeetask.Run
	for rows.Next() {
		var r employeetask.Run
		if err = rows.Scan(&r.ID, &r.QueueTaskID, &r.State); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (p employeePlanFixture) finish(t *testing.T, run employeetask.Run, state string) {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1::uuid`, run.QueueTaskID); err != nil {
		t.Fatal(err)
	}
	var err error
	switch state {
	case "succeeded":
		_, err = p.f.h.TaskService.CompleteTask(ctx, parseUUID(run.QueueTaskID), []byte(`{"output":"step output `+run.ID+`"}`), "", "", false, "")
	case "failed":
		_, err = p.f.h.TaskService.FailTask(ctx, parseUUID(run.QueueTaskID), "step failure", "", "", "agent_error", false, "")
	}
	if err != nil {
		t.Fatal(err)
	}
}

// reconcile runs the Host consumers in the worker's order: terminal facts,
// Run notices, then plan follow-ups.
func (p employeePlanFixture) reconcile(t *testing.T) int {
	t.Helper()
	ctx := context.Background()
	if _, err := p.f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := p.f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	n, err := p.f.h.ReconcileEmployeeTaskFollowUps(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (p employeePlanFixture) followUps(t *testing.T) []employeeplan.FollowUp {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT run_id::text,step_index,decision,reason,COALESCE(next_run_id::text,''),COALESCE(wake_job_id::text,''),COALESCE(note_action_id,'') FROM employee_task_follow_up WHERE task_id=$1::uuid ORDER BY created_at`, p.taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []employeeplan.FollowUp
	for rows.Next() {
		var f employeeplan.FollowUp
		if err = rows.Scan(&f.RunID, &f.StepIndex, &f.Decision, &f.Reason, &f.NextRunID, &f.WakeJobID, &f.NoteActionID); err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

func (p employeePlanFixture) taskState(t *testing.T) (string, string) {
	t.Helper()
	var task, plan string
	if err := testPool.QueryRow(context.Background(), `SELECT t.state,p.state FROM employee_task t JOIN employee_task_plan p ON p.task_id=t.id WHERE t.id=$1::uuid`, p.taskID).Scan(&task, &plan); err != nil {
		t.Fatal(err)
	}
	return task, plan
}

func TestEmployeeTaskPlanAdvancesWithoutModelAndCompletesGoal(t *testing.T) {
	p := employeePlanDatabase(t, []map[string]any{{"prompt": "Step two: write the report from step one"}})
	ctx := context.Background()
	var lifecycle int
	if err := testPool.QueryRow(ctx, `SELECT lifecycle_version FROM employee_task WHERE id=$1::uuid`, p.taskID).Scan(&lifecycle); err != nil || lifecycle != 2 {
		t.Fatalf("plan Task lifecycle=%d %v", lifecycle, err)
	}
	runs := p.runs(t)
	if len(runs) != 1 {
		t.Fatalf("runs=%d", len(runs))
	}
	var firstPrompt string
	if err := testPool.QueryRow(ctx, `SELECT context->>'direct_task_prompt' FROM agent_task_queue WHERE id=$1::uuid`, runs[0].QueueTaskID).Scan(&firstPrompt); err != nil || !strings.Contains(firstPrompt, "PLAN STEP 1 OF 2") {
		t.Fatalf("step 1 prompt: %v %q", err, firstPrompt)
	}
	// No terminal fact yet: nothing advances.
	if n := p.reconcile(t); n != 0 || len(p.runs(t)) != 1 {
		t.Fatalf("advanced before a terminal fact: %d", n)
	}
	p.finish(t, runs[0], "succeeded")
	if n := p.reconcile(t); n != 1 {
		t.Fatalf("follow-ups=%d", n)
	}
	runs = p.runs(t)
	followUps := p.followUps(t)
	if len(runs) != 2 || len(followUps) != 1 || followUps[0].Decision != employeeplan.DecisionDispatched || followUps[0].NextRunID != runs[1].ID || followUps[0].StepIndex != 1 {
		t.Fatalf("step 2 not dispatched: runs=%+v follow-ups=%+v", runs, followUps)
	}
	var stepContext []byte
	if err := testPool.QueryRow(ctx, `SELECT context FROM agent_task_queue WHERE id=$1::uuid`, runs[1].QueueTaskID).Scan(&stepContext); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stepContext), "PLAN STEP 2 OF 2") || !strings.Contains(string(stepContext), "Step two: write the report") || !strings.Contains(string(stepContext), "step output "+runs[0].ID) {
		t.Fatalf("step 2 packet lost the plan or step 1 report: %s", stepContext)
	}
	// Replays never dispatch twice.
	if n := p.reconcile(t); n != 0 || len(p.runs(t)) != 2 {
		t.Fatalf("replay advanced again: %d", n)
	}
	p.finish(t, runs[1], "succeeded")
	if n := p.reconcile(t); n != 1 {
		t.Fatalf("final follow-ups=%d", n)
	}
	// The Host-dispatched step keeps provenance: its terminal fact and notice.
	var factState, factReason, noticeState string
	if err := testPool.QueryRow(ctx, `SELECT c.state,c.reason FROM scene_event_receipt r JOIN employee_event_consumption c ON c.receipt_id=r.id WHERE r.source='employee.execution' AND r.source_event_id=$1`, runs[1].ID).Scan(&factState, &factReason); err != nil || factState != "completed" || factReason != "current_goal_revision" {
		t.Fatalf("plan step fact: %s %s %v", factState, factReason, err)
	}
	for _, run := range runs {
		if err := testPool.QueryRow(ctx, `SELECT state FROM employee_run_notice WHERE run_id=$1::uuid`, run.ID).Scan(&noticeState); err != nil || noticeState != "enqueued" {
			t.Fatalf("run %s notice: %s %v", run.ID, noticeState, err)
		}
	}
	taskState, planState := p.taskState(t)
	if taskState != "succeeded" || planState != "completed" {
		t.Fatalf("goal not completed: task=%s plan=%s", taskState, planState)
	}
	var rounds int
	if err := testPool.QueryRow(ctx, `SELECT autonomous_rounds FROM employee_task WHERE id=$1::uuid`, p.taskID).Scan(&rounds); err != nil || rounds != 1 {
		t.Fatalf("autonomous rounds=%d %v", rounds, err)
	}
	if p.model.calls != 1 {
		t.Fatalf("plan advance called the model: %d", p.model.calls)
	}
}

func TestEmployeeTaskPlanFailurePausesForHumanWithNote(t *testing.T) {
	p := employeePlanDatabase(t, []map[string]any{{"prompt": "Step two"}})
	ctx := context.Background()
	runs := p.runs(t)
	p.finish(t, runs[0], "failed")
	p.reconcile(t)
	followUps := p.followUps(t)
	if len(followUps) != 1 || followUps[0].Decision != employeeplan.DecisionPaused || followUps[0].Reason != "step_failed" || followUps[0].NoteActionID == "" || len(p.runs(t)) != 1 {
		t.Fatalf("failure did not pause: %+v runs=%d", followUps, len(p.runs(t)))
	}
	taskState, planState := p.taskState(t)
	if taskState != "waiting" || planState != "paused" {
		t.Fatalf("task=%s plan=%s", taskState, planState)
	}
	var text, kind string
	if err := testPool.QueryRow(ctx, `SELECT a.input->>'text',h.source_kind FROM response_action a JOIN employee_host_notice h ON h.action_id=a.id WHERE a.id=$1`, followUps[0].NoteActionID).Scan(&text, &kind); err != nil || !strings.Contains(text, "第 1 步没有成功") || kind != "task_plan" {
		t.Fatalf("pause note: %q %s %v", text, kind, err)
	}
	if p.model.calls != 1 {
		t.Fatalf("pause called the model: %d", p.model.calls)
	}
}

func TestEmployeeTaskPlanStopRevisionBudgetAndGovernor(t *testing.T) {
	for _, tc := range []struct {
		name, decision, reason, plan string
		steps                        int
		before                       func(t *testing.T, p employeePlanFixture)
		note                         bool
	}{
		{name: "stopped", decision: "stopped", reason: "task_stopped", plan: "stopped", steps: 1, before: func(t *testing.T, p employeePlanFixture) {
			if _, err := testPool.Exec(context.Background(), `UPDATE employee_task SET state='cancelled' WHERE id=$1::uuid`, p.taskID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "goal_revised", decision: "stale", reason: "goal_revised", plan: "superseded", steps: 1, before: func(t *testing.T, p employeePlanFixture) {
			if _, err := testPool.Exec(context.Background(), `UPDATE employee_task SET goal_revision=goal_revision+1 WHERE id=$1::uuid`, p.taskID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "budget", decision: "paused", reason: "step_budget_exhausted", plan: "paused", steps: 2, note: true, before: func(t *testing.T, p employeePlanFixture) {
			if _, err := testPool.Exec(context.Background(), `INSERT INTO employee_task_follow_up(workspace_id,agent_id,tenant_org_id,scene_id,task_id,plan_id,plan_revision,run_id,step_index,decision,wake_job_id,next_step_index) SELECT workspace_id,agent_id,tenant_org_id,scene_id,task_id,id,1,gen_random_uuid(),1,'woken',gen_random_uuid(),2 FROM employee_task_plan WHERE id=$1::uuid`, p.planID); err != nil {
				t.Fatal(err)
			}
			if _, err := testPool.Exec(context.Background(), `UPDATE employee_task_plan SET step_budget=1 WHERE id=$1::uuid`, p.planID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "governor", decision: "paused", reason: "autonomous_round_limit", plan: "paused", steps: 1, note: true, before: func(t *testing.T, p employeePlanFixture) {
			if _, err := testPool.Exec(context.Background(), `UPDATE employee_task SET autonomous_rounds=12 WHERE id=$1::uuid`, p.taskID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []map[string]any{}
			for i := 0; i < tc.steps; i++ {
				steps = append(steps, map[string]any{"prompt": "later step"})
			}
			p := employeePlanDatabase(t, steps)
			runs := p.runs(t)
			p.finish(t, runs[0], "succeeded")
			tc.before(t, p)
			p.reconcile(t)
			var found *employeeplan.FollowUp
			for _, f := range p.followUps(t) {
				if f.RunID == runs[0].ID {
					found = &f
				}
			}
			if found == nil || string(found.Decision) != tc.decision || found.Reason != tc.reason || found.NextRunID != "" || (found.NoteActionID != "") != tc.note {
				t.Fatalf("decision: %+v", found)
			}
			if _, plan := p.taskState(t); plan != tc.plan {
				t.Fatalf("plan state=%s", plan)
			}
			if len(p.runs(t)) != 1 || p.model.calls != 1 {
				t.Fatalf("runs=%d model=%d", len(p.runs(t)), p.model.calls)
			}
		})
	}
}

func TestEmployeeTaskPlanReviewWakeDecides(t *testing.T) {
	for _, tc := range []struct {
		name    string
		respond func(int) (string, map[string]any)
	}{
		{"continue", func(int) (string, map[string]any) {
			return wakeToolCall("call-continue", "continue_plan", map[string]any{"reply": "第一步的结果没问题，继续第二步。"})
		}},
		{"pause", func(int) (string, map[string]any) {
			return wakeToolCall("call-pause", "reply", map[string]any{"reply": "第一步结果里缺了一项，先确认再继续。"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := employeePlanDatabase(t, []map[string]any{{"prompt": "Step two after review", "review_first": true}})
			ctx := context.Background()
			runs := p.runs(t)
			p.finish(t, runs[0], "succeeded")
			p.reconcile(t)
			followUps := p.followUps(t)
			if len(followUps) != 1 || followUps[0].Decision != employeeplan.DecisionWoken || followUps[0].WakeJobID == "" || len(p.runs(t)) != 1 {
				t.Fatalf("review step did not wake: %+v", followUps)
			}
			wakeModel := &employeeWakeTestModel{respond: tc.respond}
			p.f.h.EmployeeSceneWorker.model = wakeModel
			if worked, err := p.f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
				t.Fatal(worked, err)
			}
			if wakeModel.calls != 1 || !strings.Contains(string(wakeModel.requests[0]), "continue_plan") || !strings.Contains(string(wakeModel.requests[0]), "Step two after review") {
				t.Fatalf("decision wake: calls=%d", wakeModel.calls)
			}
			var kind, state string
			if err := testPool.QueryRow(ctx, `SELECT j.kind,c.state FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id WHERE j.id=$1::uuid`, followUps[0].WakeJobID).Scan(&kind, &state); err != nil || kind != employeeentry.KindTaskWake || state != "completed" {
				t.Fatalf("wake job: %s %s %v", kind, state, err)
			}
			runs = p.runs(t)
			after := p.followUps(t)[0]
			taskState, planState := p.taskState(t)
			if tc.name == "continue" {
				if len(runs) != 2 || after.NextRunID != runs[1].ID || planState != "active" {
					t.Fatalf("continue_plan: runs=%d next=%s plan=%s", len(runs), after.NextRunID, planState)
				}
				p.finish(t, runs[1], "succeeded")
				p.reconcile(t)
				if taskState, planState = p.taskState(t); taskState != "succeeded" || planState != "completed" {
					t.Fatalf("reviewed plan did not complete: task=%s plan=%s", taskState, planState)
				}
				return
			}
			if len(runs) != 1 || planState != "paused" || taskState != "waiting" {
				t.Fatalf("declined review: runs=%d plan=%s task=%s", len(runs), planState, taskState)
			}
		})
	}
}

func TestEmployeeTaskWakeGovernorWaitsForHuman(t *testing.T) {
	f := employeeWakeDatabase(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE employee_task SET autonomous_rounds=12 WHERE id=$1::uuid`, f.task.ID); err != nil {
		t.Fatal(err)
	}
	wake := f.admit(t, "governor-1")
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var outcome []byte
	var rounds int
	if err := testPool.QueryRow(ctx, `SELECT j.outcome,t.autonomous_rounds FROM employee_scene_job j, employee_task t WHERE j.id=$1::uuid AND t.id=$2::uuid`, wake.JobID, f.task.ID).Scan(&outcome, &rounds); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := testPool.QueryRow(ctx, `SELECT input->>'text' FROM response_action WHERE agent_id=$1::uuid AND input->>'scene_notice_id'=$2`, f.agentID, wake.JobID).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if f.wake.calls != 0 || rounds != 13 || !strings.Contains(string(outcome), "autonomous_round_limit") || text != employeeAutonomousLimitNote {
		t.Fatalf("governor: calls=%d rounds=%d outcome=%s text=%q", f.wake.calls, rounds, outcome, text)
	}
}

func TestEmployeeUpstreamTerminalReleasesBlockedGoal(t *testing.T) {
	p := employeePlanDatabase(t, []map[string]any{{"prompt": "later"}})
	ctx := context.Background()
	store := employeetask.NewStore(testPool)
	// An independent v1 upstream in the same scope and requester.
	upstream, err := store.Create(ctx, employeetask.CreateParams{Scope: p.scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: "dingtalk:456:open_id:requester-open-id", Definition: employeetask.Definition{Goal: "upstream"}, Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}})
	if err != nil {
		t.Fatal(err)
	}
	var requester string
	if err = testPool.QueryRow(ctx, `SELECT requester_ref FROM employee_task WHERE id=$1::uuid`, p.taskID).Scan(&requester); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE employee_task SET requester_ref=$2 WHERE id=$1::uuid`, upstream.ID, requester); err != nil {
		t.Fatal(err)
	}
	dependent, err := store.Create(ctx, employeetask.CreateParams{Scope: p.scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, Lifecycle: employeetask.LifecycleV2, CompletionMode: employeetask.CompletionExplicitGoal, RequesterRef: requester, Definition: employeetask.Definition{Goal: "dependent"}, Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = store.BlockOnTask(ctx, p.scope, dependent.ID, employeetask.BlockParams{Source: employeetask.Source{Namespace: "test", Key: "block"}, UpstreamTaskID: upstream.ID, AuthorityRef: "test"}); err != nil {
		t.Fatal(err)
	}
	state := func(id string) string {
		var s string
		if err := testPool.QueryRow(ctx, `SELECT state FROM employee_task WHERE id=$1::uuid`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if state(dependent.ID) != "waiting" {
		t.Fatalf("dependent not waiting: %s", state(dependent.ID))
	}
	// The upstream's terminal fact is recorded without the inline release (an
	// older replica); the Host release reconciler applies it.
	queue := uuid.NewString()
	run, err := store.StartRun(ctx, p.scope, upstream.ID, employeetask.StartRunParams{Source: employeetask.Source{Namespace: "test", Key: "run"}, QueueTaskID: queue, ExpectedVersion: upstream.Version})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.RecordResult(ctx, p.scope, upstream.ID, employeetask.ResultParams{Source: employeetask.Source{Namespace: "queue_terminal", Key: queue}, RunID: run.ID, State: employeetask.StateSucceeded, Result: "done"}); err != nil {
		t.Fatal(err)
	}
	if state(dependent.ID) != "waiting" {
		t.Fatal("released without the Host")
	}
	if n, err := p.f.h.ReconcileEmployeeUpstreamReleases(ctx, 100); err != nil || n != 1 {
		t.Fatalf("release reconciler: %d %v", n, err)
	}
	if state(dependent.ID) != "ready" {
		t.Fatalf("dependent after release: %s", state(dependent.ID))
	}
	if n, err := p.f.h.ReconcileEmployeeUpstreamReleases(ctx, 100); err != nil || n != 0 {
		t.Fatalf("release replay: %d %v", n, err)
	}
	var wakes int
	if err = testPool.QueryRow(ctx, `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='task_wake'`, p.f.agentID).Scan(&wakes); err != nil || wakes != 0 {
		t.Fatalf("release woke a goal its plan did not authorize: %d %v", wakes, err)
	}
	// The real terminal path releases inline: the plan Task's own first Run
	// ends, and a goal blocked on it is released in that transaction.
	second, err := store.Create(ctx, employeetask.CreateParams{Scope: p.scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, Lifecycle: employeetask.LifecycleV2, CompletionMode: employeetask.CompletionExplicitGoal, RequesterRef: requester, Definition: employeetask.Definition{Goal: "second dependent"}, Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}})
	if err != nil {
		t.Fatal(err)
	}
	inlineUpstream, err := store.Create(ctx, employeetask.CreateParams{Scope: p.scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: requester, Definition: employeetask.Definition{Goal: "inline upstream"}, Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = store.BlockOnTask(ctx, p.scope, second.ID, employeetask.BlockParams{Source: employeetask.Source{Namespace: "test", Key: "block-inline"}, UpstreamTaskID: inlineUpstream.ID, AuthorityRef: "test"}); err != nil {
		t.Fatal(err)
	}
	prepared, err := p.f.h.TaskService.EnqueueDirectTask(ctx, directTaskRequestForTest(t, p, inlineUpstream))
	if err != nil {
		t.Fatal(err)
	}
	p.finish(t, employeetask.Run{ID: prepared.Run.ID, QueueTaskID: prepared.Run.QueueTaskID}, "succeeded")
	if state(inlineUpstream.ID) != "succeeded" || state(second.ID) != "ready" {
		t.Fatalf("inline release: upstream=%s dependent=%s", state(inlineUpstream.ID), state(second.ID))
	}
}

func directTaskRequestForTest(t *testing.T, p employeePlanFixture, task employeetask.Task) service.DirectTaskRequest {
	t.Helper()
	return service.DirectTaskRequest{Task: task, Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, Prompt: "inline upstream work", PrincipalID: parseUUID(testUserID), Context: json.RawMessage(`{}`)}
}
