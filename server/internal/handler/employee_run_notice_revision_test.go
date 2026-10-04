package handler

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service"
)

func employeeNoticeTask(t *testing.T, f employeeNoticeFixture) employeetask.Task {
	t.Helper()
	var scope employeetask.Scope
	var taskID string
	err := testPool.QueryRow(context.Background(), `SELECT t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scene_id::text,t.id::text FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE r.id=$1::uuid`, f.runID).Scan(&scope.WorkspaceID, &scope.AgentID, &scope.TenantOrgID, &scope.Scene.SceneID, &taskID)
	if err != nil {
		t.Fatal(err)
	}
	scope.Kind = employeetask.ScopeScene
	task, err := employeetask.NewStore(testPool).Get(context.Background(), scope, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func correctEmployeeNoticeGoal(t *testing.T, f employeeNoticeFixture) {
	t.Helper()
	task := employeeNoticeTask(t, f)
	_, _, err := employeetask.NewStore(testPool).AppendInput(context.Background(), task.Scope, task.ID, employeetask.InputParams{Source: employeetask.Source{Namespace: "notice-correction", Key: uuid.NewString()}, ActorRef: "member:" + testUserID, Body: "将目标改为新版本", Correction: &employeetask.Definition{Goal: "New corrected goal"}, ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
}

func assertEmployeeNoticeOldResultRetained(t *testing.T, f employeeNoticeFixture) {
	t.Helper()
	var result, state string
	var revision, current int64
	err := testPool.QueryRow(context.Background(), `SELECT r.result,r.state,r.goal_revision,t.goal_revision FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id WHERE r.id=$1::uuid`, f.runID).Scan(&result, &state, &revision, &current)
	if err != nil || state != "succeeded" || result != "真实已存结果" || revision != 1 || current != 2 {
		t.Fatal("old Run ledger was rewritten", result, state, revision, current, err)
	}
	if f.model.calls != 1 {
		t.Fatal("notification recomputed the task", f.model.calls)
	}
}

func TestEmployeeRunNoticeLateOldRevisionResultIsSuppressed(t *testing.T) {
	f := employeeNoticeDatabase(t, "running", false, false)
	correctEmployeeNoticeGoal(t, f)
	if _, err := f.h.TaskService.CompleteTask(context.Background(), parseUUID(f.queueID), []byte(`{"output":"真实已存结果"}`), "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	var hasAction bool
	if err := testPool.QueryRow(context.Background(), `SELECT state,reason,action_id IS NOT NULL FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&state, &reason, &hasAction); err != nil {
		t.Fatal(err)
	}
	if state != "suppressed" || reason != "stale_goal_revision" || hasAction {
		t.Fatal("old revision was sent as the corrected result", state, reason, hasAction)
	}
	assertEmployeeNoticeOldResultRetained(t, f)
}

func TestEmployeeRunNoticeGoalCorrectionAfterEnqueuePreventsSend(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	correctEmployeeNoticeGoal(t, f)
	provider := &employeeNoticeProvider{}
	actionID := startEmployeeNoticeDelivery(t, f, provider)
	waitEmployeeNoticeAction(t, actionID, "cancelled")
	if provider.sends.Load() != 0 {
		t.Fatal("queued old result reached requester", provider.sends.Load())
	}
	var state, reason string
	if err := testPool.QueryRow(context.Background(), `SELECT state,reason FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "suppressed" || reason != "stale_goal_revision" {
		t.Fatal(state, reason)
	}
	assertEmployeeNoticeOldResultRetained(t, f)
}

func TestEmployeeRunNoticeSameRevisionFollowupKeepsResultScopedToItsRun(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	task := employeeNoticeTask(t, f)
	resumed, _, err := employeetask.NewStore(testPool).Resume(context.Background(), task.Scope, task.ID, employeetask.ResumeParams{Source: employeetask.Source{Namespace: "notice-resume", Key: uuid.NewString()}, ActorRef: "member:" + testUserID, Body: "继续核对这一目标", ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := f.h.Queries.GetAgentTask(context.Background(), parseUUID(f.queueID))
	if err != nil {
		t.Fatal(err)
	}
	newRun, err := f.h.TaskService.EnqueueDirectTask(context.Background(), service.DirectTaskRequest{Task: resumed, Source: employeetask.Source{Namespace: "notice-resume", Key: uuid.NewString()}, Prompt: "Continue checking this goal", PrincipalID: parseUUID(testUserID), Context: queue.Context})
	if err != nil {
		t.Fatal(err)
	}
	var body string
	if err = testPool.QueryRow(context.Background(), `SELECT body FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "真实已存结果" {
		t.Fatal("notice changed the frozen run output", body)
	}
	provider := &employeeNoticeProvider{}
	actionID := startEmployeeNoticeDelivery(t, f, provider)
	waitEmployeeNoticeAction(t, actionID, "delivered")
	current := employeeNoticeTask(t, f)
	if current.State != employeetask.StateRunning || current.ActiveRunID != newRun.Run.ID || current.GoalRevision != task.GoalRevision {
		t.Fatal("old notice settled a newer Run", current.State, current.ActiveRunID)
	}
}
