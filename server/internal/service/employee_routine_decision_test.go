package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newEmployeeRoutineDecisionFixture(t *testing.T) *employeeRoutineFixture {
	t.Helper()
	f := newEmployeeRoutineFixture(t)
	if err := contextcap.SetRoutineEmployeeExecution(context.Background(), f.pool, f.routine.ID, contextcap.RoutineEmployeeDecide); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM employee_routine_decision WHERE workspace_id=$1::uuid`, f.ws)
	})
	return f
}

type routineDecisionRow struct {
	OccurrenceID, JobID, State, Reason, TaskID, QueueID string
}

func (f *employeeRoutineFixture) decision(t *testing.T, runID pgtype.UUID) routineDecisionRow {
	t.Helper()
	var d routineDecisionRow
	if err := f.pool.QueryRow(context.Background(), `SELECT occurrence_id::text,job_id::text,state,reason,employee_task_id::text,COALESCE(queue_task_id::text,'') FROM employee_routine_decision WHERE autopilot_run_id=$1`, runID).
		Scan(&d.OccurrenceID, &d.JobID, &d.State, &d.Reason, &d.TaskID, &d.QueueID); err != nil {
		t.Fatal("decision", err)
	}
	return d
}

func (f *employeeRoutineFixture) inTx(t *testing.T, fn func(tx pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestEmployeeRoutineDecisionAdmitsWakeInsteadOfRun(t *testing.T) {
	f := newEmployeeRoutineDecisionFixture(t)
	ctx := context.Background()
	run := f.fire(t, f.slot(0))
	if run.Status != "running" || run.TaskID.Valid {
		t.Fatalf("decision run = %+v", run)
	}
	o := f.occurrence(t, run.ID)
	d := f.decision(t, run.ID)
	if o.State != "decision" || o.DispatchMode != "employee_decide" || o.TaskID == "" || o.RunID != "" || o.QueueID != "" || o.PromptSHA == "" {
		t.Fatalf("receipt = %+v", o)
	}
	if d.State != "pending" || d.OccurrenceID != o.ID || d.TaskID != o.TaskID || d.QueueID != "" {
		t.Fatalf("decision = %+v", d)
	}
	if len(f.host.wakes) != 1 || f.host.wakeNotified != 1 || len(f.host.notices) != 0 {
		t.Fatalf("wakes=%d notified=%d start notices=%d", len(f.host.wakes), f.host.wakeNotified, len(f.host.notices))
	}
	wake := f.host.wakes[0]
	if wake.Source != RoutineDecisionWakeSource || wake.EventID != o.ID || !wake.OccurredAt.Equal(o.OccurredAt.Time) || wake.Wake.Kind != employeeentry.TaskWakeRoutineDecision ||
		wake.Wake.TaskID != o.TaskID || wake.Scope.SceneID != f.sceneID || wake.Wake.Validate() != nil {
		t.Fatalf("wake = %+v", wake)
	}
	if n := f.count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid`, f.agent); n != 0 {
		t.Fatal("decision occurrence queued work before deciding", n)
	}
	scope := employeetask.Scope{WorkspaceID: f.ws, AgentID: f.agent, TenantOrgID: f.org, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: f.sceneID}}
	task, err := employeetask.NewStore(f.pool).Get(ctx, scope, o.TaskID)
	if err != nil || task.State != employeetask.StateReady || task.ActiveRunID != "" {
		t.Fatal(task, err)
	}
	origin, err := LoadAutomationTaskOrigin(ctx, f.pool, scope, o.TaskID)
	if err != nil || origin.Origin.QueueTaskID() != "" || origin.HistoryPolicy.Kind != AutomationHistorySceneEndpoint || origin.Creator.ID != f.user {
		t.Fatalf("decision task origin = %+v err=%v", origin, err)
	}
	// A pending decision overlaps the next occurrence.
	next := f.fire(t, f.slot(1))
	if next.Status != "skipped" || f.occurrence(t, next.ID).Overlap != o.ID {
		t.Fatalf("pending decision did not block the next occurrence: %+v", next)
	}
	// Replays read the frozen decision occurrence.
	if again := f.fire(t, f.slot(0)); again.ID != run.ID || len(f.host.wakes) != 1 {
		t.Fatal("decision replay admitted another wake")
	}
}

func TestEmployeeRoutineDecisionGateClosedNeverRunsUnconditionally(t *testing.T) {
	for name, setup := range map[string]func(f *employeeRoutineFixture){
		"decision_reader_missing": func(f *employeeRoutineFixture) {
			f.host.decisionReady = errors.New("a live replica lacks the decision reader")
		},
		"admission_fails": func(f *employeeRoutineFixture) { f.host.admitErr = errors.New("wake admission failed") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newEmployeeRoutineDecisionFixture(t)
			setup(f)
			run, err := f.svc.DispatchAutopilotForPlan(context.Background(), f.ap, f.trigger.ID, "schedule", nil, f.slot(0))
			if name == "admission_fails" {
				if err == nil {
					t.Fatal("failed wake admission committed")
				}
				if r, o, tk, q := f.slotRows(t, f.slot(0)); r+o+tk+q != 0 {
					t.Fatalf("partial decision commit run=%d receipt=%d task=%d queue=%d", r, o, tk, q)
				}
				return
			}
			if err != nil || run.Status != "skipped" || !strings.Contains(run.FailureReason.String, "decision") {
				t.Fatalf("gate closed run = %+v err=%v", run, err)
			}
			if n := f.count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid`, f.agent); n != 0 {
				t.Fatal("decision routine ran unconditionally", n)
			}
		})
	}
}

func TestEmployeeRoutineDecisionDispatchRunsFrozenPacketOnce(t *testing.T) {
	f := newEmployeeRoutineDecisionFixture(t)
	ctx := context.Background()
	run := f.fire(t, f.slot(0))
	d := f.decision(t, run.ID)
	// The routine changes after the occurrence was accepted.
	if _, err := f.q.UpdateAutopilot(ctx, db.UpdateAutopilotParams{ID: f.ap.ID, Description: pgtype.Text{String: "CHANGED_AFTER_DECISION_ADMISSION", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	// A different job cannot carry out this decision.
	if err := f.inTx(t, func(tx pgx.Tx) error {
		_, err := f.svc.DispatchRoutineDecisionTx(ctx, tx, d.OccurrenceID, uuid.NewString())
		return err
	}); !errors.Is(err, ErrAutomationOriginInvalid) {
		t.Fatal("foreign job dispatched the decision", err)
	}
	var dispatched RoutineDecisionDispatch
	if err := f.inTx(t, func(tx pgx.Tx) error {
		var err error
		dispatched, err = f.svc.DispatchRoutineDecisionTx(ctx, tx, d.OccurrenceID, d.JobID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after := f.decision(t, run.ID)
	queue := f.queue(t, after.QueueID)
	if after.State != "dispatched" || dispatched.Queue.ID != queue.ID || queue.AutopilotRunID != run.ID {
		t.Fatalf("decision after dispatch = %+v", after)
	}
	direct, ok := ParseDirectTaskContext(queue)
	if !ok || !strings.Contains(direct.Prompt, "FROZEN_INSTRUCTIONS_V1") || strings.Contains(direct.Prompt, "CHANGED_AFTER") {
		t.Fatal("decided run did not use the frozen packet", direct.Prompt)
	}
	origin, err := LoadAutomationOrigin(ctx, f.pool, queue)
	if err != nil || origin.ReceiptID() != d.OccurrenceID || origin.RunID() == "" {
		t.Fatal("decided run origin", origin, err)
	}
	apRun, err := f.q.GetAutopilotRun(ctx, run.ID)
	if err != nil || apRun.TaskID != queue.ID || len(f.host.notices) != 1 {
		t.Fatalf("run mapping %+v notices=%d err=%v", apRun, len(f.host.notices), err)
	}
	// A replay returns the same execution; settling afterwards changes nothing.
	if err := f.inTx(t, func(tx pgx.Tx) error {
		again, err := f.svc.DispatchRoutineDecisionTx(ctx, tx, d.OccurrenceID, d.JobID)
		if err == nil && (!again.Replay || again.Queue.ID != queue.ID) {
			return errors.New("dispatch replay created another execution")
		}
		if err != nil {
			return err
		}
		settled, err := f.svc.SettleRoutineDecisionTx(ctx, tx, d.OccurrenceID, d.JobID, RoutineDecisionOutcome{State: RoutineDecisionQuiet})
		if settled {
			return errors.New("a dispatched decision was settled quiet")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid`, f.agent); n != 1 {
		t.Fatal("queue rows", n)
	}
	f.finish(t, after.QueueID, "completed")
	if apRun, _ = f.q.GetAutopilotRun(ctx, run.ID); apRun.Status != "completed" {
		t.Fatal("decided run did not settle the AutopilotRun", apRun.Status)
	}
	// After the dispatched Run ended, the next occurrence is not overlapped.
	if next := f.fire(t, f.slot(1)); next.Status != "running" || f.occurrence(t, next.ID).State != "decision" {
		t.Fatal("next decision occurrence", next)
	}
}

func TestEmployeeRoutineDecisionQuietWaitFailedSettleWithoutRun(t *testing.T) {
	for _, state := range []string{RoutineDecisionQuiet, RoutineDecisionWaited, RoutineDecisionReplied, RoutineDecisionFailed} {
		t.Run(state, func(t *testing.T) {
			f := newEmployeeRoutineDecisionFixture(t)
			ctx := context.Background()
			run := f.fire(t, f.slot(0))
			d := f.decision(t, run.ID)
			if err := f.inTx(t, func(tx pgx.Tx) error {
				ok, err := f.svc.SettleRoutineDecisionTx(ctx, tx, d.OccurrenceID, d.JobID, RoutineDecisionOutcome{State: state, Reason: "condition " + state})
				if !ok && err == nil {
					return errors.New("not settled")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			apRun, err := f.q.GetAutopilotRun(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantRun := "completed"
			if state == RoutineDecisionFailed {
				wantRun = "failed"
			}
			var result map[string]map[string]string
			_ = json.Unmarshal(apRun.Result, &result)
			if apRun.Status != wantRun || apRun.TaskID.Valid || result["employee_decision"]["state"] != state {
				t.Fatalf("run = %s task=%v result=%s", apRun.Status, apRun.TaskID, apRun.Result)
			}
			after := f.decision(t, run.ID)
			if after.State != state || after.QueueID != "" {
				t.Fatalf("decision = %+v", after)
			}
			scope := employeetask.Scope{WorkspaceID: f.ws, AgentID: f.agent, TenantOrgID: f.org, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: f.sceneID}}
			task, err := employeetask.NewStore(f.pool).Get(ctx, scope, d.TaskID)
			if err != nil || task.State != employeetask.StateCancelled {
				t.Fatal("decision task not closed", task.State, err)
			}
			if n := f.count(t, `SELECT count(*) FROM employee_task_run WHERE task_id=$1::uuid`, d.TaskID); n != 0 {
				t.Fatal("settled decision invented a Run", n)
			}
			// Idempotent; a later dispatch of the same decision is refused.
			if err := f.inTx(t, func(tx pgx.Tx) error {
				if ok, err := f.svc.SettleRoutineDecisionTx(ctx, tx, d.OccurrenceID, d.JobID, RoutineDecisionOutcome{State: state}); ok || err != nil {
					return errors.New("settled twice")
				}
				_, err := f.svc.DispatchRoutineDecisionTx(ctx, tx, d.OccurrenceID, d.JobID)
				if !errors.Is(err, ErrRoutineDecisionSettled) {
					return errors.New("dispatch after settle was not refused")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			outcomes, err := ListRoutineOccurrenceOutcomes(ctx, f.pool, f.ws, f.routine.ID, 3)
			if err != nil || len(outcomes) != 1 || outcomes[0].Decision != state || outcomes[0].DecisionReason != "condition "+state || outcomes[0].ExecutionState != "" {
				t.Fatalf("outcomes = %+v %v", outcomes, err)
			}
		})
	}
}

func TestEmployeeRoutineDecisionDispatchRechecksAuthority(t *testing.T) {
	f := newEmployeeRoutineDecisionFixture(t)
	ctx := context.Background()
	run := f.fire(t, f.slot(0))
	d := f.decision(t, run.ID)
	f.exec(t, `DELETE FROM member WHERE workspace_id=$1::uuid AND user_id=$2::uuid`, f.ws, f.user)
	err := f.inTx(t, func(tx pgx.Tx) error {
		_, err := f.svc.DispatchRoutineDecisionTx(ctx, tx, d.OccurrenceID, d.JobID)
		return err
	})
	if !errors.Is(err, ErrRoutineDecisionRefused) {
		t.Fatal("revoked creator still dispatched", err)
	}
	if after := f.decision(t, run.ID); after.State != "pending" || f.count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid`, f.agent) != 0 {
		t.Fatal("refused dispatch wrote state", after)
	}
	scope := employeetask.Scope{WorkspaceID: f.ws, AgentID: f.agent, TenantOrgID: f.org, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: f.sceneID}}
	origin, err := LoadAutomationTaskOrigin(ctx, f.pool, scope, d.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if reason, err := VerifyRoutineOriginAuthority(ctx, f.q, origin); err != nil || reason != "routine_principal_revoked" {
		t.Fatal("authority check", reason, err)
	}
}

func TestEmployeeRoutineDecisionReconcilesHeldJob(t *testing.T) {
	f := newEmployeeRoutineDecisionFixture(t)
	ctx := context.Background()
	run := f.fire(t, f.slot(0))
	d := f.decision(t, run.ID)
	// The wake job ended without settling the decision (held by the worker).
	f.exec(t, `INSERT INTO employee_scene_job(id,workspace_id,agent_id,tenant_org_id,scene_id,principal_id,items,message_count,kind,state,outcome,updated_at)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,'[]'::jsonb,0,'task_wake','completed','{"held_reason":"task_wake_origin_unsupported"}'::jsonb,now()-interval '1 minute')`,
		d.JobID, f.ws, f.agent, f.org, f.sceneID, f.user)
	settled, err := f.svc.ReconcileRoutineDecisions(ctx, 100)
	if err != nil || settled != 1 {
		t.Fatal(settled, err)
	}
	after := f.decision(t, run.ID)
	apRun, _ := f.q.GetAutopilotRun(ctx, run.ID)
	if after.State != "failed" || !strings.Contains(after.Reason, "task_wake_origin_unsupported") || apRun.Status != "failed" {
		t.Fatalf("held decision = %+v run=%s", after, apRun.Status)
	}
	if again, err := f.svc.ReconcileRoutineDecisions(ctx, 100); err != nil || again != 0 {
		t.Fatal("reconciled twice", again, err)
	}
	if next := f.fire(t, f.slot(1)); next.Status != "running" {
		t.Fatal("failed decision kept overlapping", next.Status)
	}
}
