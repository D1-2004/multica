package scheduler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// schedulerRoutineHost is a minimal Host adapter: every replica is ready and
// the routine's scene accepts notices.
type schedulerRoutineHost struct{}

func (schedulerRoutineHost) RoutineRuntimeContext(context.Context, db.Autopilot) ([]byte, error) {
	return nil, nil
}
func (schedulerRoutineHost) RoutineTaskQueued(context.Context, db.Autopilot, db.AutopilotRun, db.AgentTaskQueue) {
}
func (schedulerRoutineHost) RoutineTaskFinished(context.Context, pgx.Tx, db.AgentTaskQueue, string, []byte, string) error {
	return nil
}
func (schedulerRoutineHost) RoutineTaskSettled(context.Context, db.AgentTaskQueue) {}
func (schedulerRoutineHost) EmployeeRoutineReady(context.Context) error            { return nil }
func (schedulerRoutineHost) CheckRoutineDeliveryTx(context.Context, pgx.Tx, contextcap.Routine) error {
	return nil
}
func (schedulerRoutineHost) EnqueueRoutineStartNoticeTx(context.Context, pgx.Tx, service.RoutineStartNotice) error {
	return nil
}
func (schedulerRoutineHost) NotifyRoutineNotices() {}

// TestEmployeeRoutineSchedulerPauseResumeAndConfigError drives the real
// autopilot schedule handler: a paused routine fires nothing and leaves the
// accepted occurrence running, the next slot after resume runs, and a
// configuration error is a failed run that keeps the cadence (no retry error).
func TestEmployeeRoutineSchedulerPauseResumeAndConfigError(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)
	ws, user, agent, runtime, org := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), "sched-routine-"+uuid.NewString()[:8]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(sql, err)
		}
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, statement := range []string{
			`DELETE FROM employee_routine_occurrence WHERE workspace_id=$1::uuid`,
			`DELETE FROM employee_task_entry WHERE workspace_id=$1::uuid`,
			`DELETE FROM employee_task_run WHERE workspace_id=$1::uuid`,
			`DELETE FROM employee_task WHERE workspace_id=$1::uuid`,
			`DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id=$1::uuid)`,
			`DELETE FROM autopilot_run WHERE autopilot_id IN (SELECT id FROM autopilot WHERE workspace_id=$1::uuid)`,
			`DELETE FROM autopilot_trigger WHERE autopilot_id IN (SELECT id FROM autopilot WHERE workspace_id=$1::uuid)`,
			`DELETE FROM autopilot_rule_version WHERE workspace_id=$1::uuid`,
			`DELETE FROM autopilot WHERE workspace_id=$1::uuid`,
			`DELETE FROM context_scope_routine WHERE workspace_id=$1::uuid`,
			`DELETE FROM agent_scene WHERE workspace_id=$1::uuid`,
			`DELETE FROM agent_dingtalk_identity WHERE workspace_id=$1::uuid`,
			`DELETE FROM agent WHERE workspace_id=$1::uuid`,
			`DELETE FROM agent_runtime WHERE workspace_id=$1::uuid`,
			`DELETE FROM member WHERE workspace_id=$1::uuid`,
			`DELETE FROM workspace WHERE id=$1::uuid`,
		} {
			if _, err := pool.Exec(bg, statement, ws); err != nil {
				t.Error(statement, err)
			}
		}
		_, _ = pool.Exec(bg, `DELETE FROM "user" WHERE id=$1::uuid`, user)
	})
	exec(`INSERT INTO "user"(id,name,email) VALUES($1::uuid,'Scheduler routine',$2)`, user, user+"@test.invalid")
	exec(`INSERT INTO workspace(id,name,slug) VALUES($1::uuid,'Scheduler routine',$2)`, ws, "sched-routine-"+ws)
	exec(`INSERT INTO member(workspace_id,user_id,role) VALUES($1::uuid,$2::uuid,'owner')`, ws, user)
	exec(`INSERT INTO agent_runtime(id,workspace_id,daemon_id,name,runtime_mode,provider,status,owner_id,metadata) VALUES($1::uuid,$2::uuid,$3,'Scheduler routine','local','codex','online',$4::uuid,'{"client_capabilities":["employee-direct-v1"]}')`, runtime, ws, "sched-"+runtime, user)
	exec(`INSERT INTO agent(id,workspace_id,name,runtime_mode,runtime_id,owner_id,permission_mode,coordination_mode) VALUES($1::uuid,$2::uuid,'Scheduler routine','local',$3::uuid,$4::uuid,'private','employee')`, agent, ws, runtime, user)
	exec(`INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid)`, agent, ws, "dws-"+agent, org, user)
	wsID, _ := util.ParseUUID(ws)
	agentID, _ := util.ParseUUID(agent)
	userID, _ := util.ParseUUID(user)
	sc, err := scene.Resolve(ctx, q, scene.Owner{WorkspaceID: wsID, AgentID: agentID}, scene.DingTalkConversation(org, scene.KindGroup, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	ap, err := q.CreateAutopilot(ctx, db.CreateAutopilotParams{WorkspaceID: wsID, Title: "Quarter-hour check", Description: pgtype.Text{String: "Check the queue.", Valid: true},
		AssigneeType: "agent", AssigneeID: agentID, Status: "active", ExecutionMode: "run_only", CreatedByType: "member", CreatedByID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecordAutopilotRuleVersion(ctx, q, ap, "member", userID); err != nil {
		t.Fatal(err)
	}
	trigger, err := q.CreateAutopilotTrigger(ctx, db.CreateAutopilotTriggerParams{AutopilotID: ap.ID, Kind: "schedule", Enabled: true,
		CronExpression: pgtype.Text{String: "*/15 * * * *", Valid: true}, Timezone: pgtype.Text{String: "Asia/Shanghai", Valid: true},
		PublishedByType: pgtype.Text{String: "member", Valid: true}, PublishedByID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.InsertRoutine(ctx, pool, contextcap.Routine{WorkspaceID: ws, AgentID: agent, SceneID: util.UUIDToString(sc.ID), TenantOrgID: org, SceneKind: "group",
		AutopilotID: util.UUIDToString(ap.ID), DedupeKey: "check|schedule", CreatedByType: "member", CreatedByID: user}); err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	taskSvc := &service.TaskService{Queries: q, TxStarter: pool, Bus: bus}
	svc := &service.AutopilotService{Queries: q, TxStarter: pool, Bus: bus, TaskSvc: taskSvc, SceneRoutines: schedulerRoutineHost{}}
	handler := autopilotHandler(q, svc)
	base := time.Date(2026, 10, 3, 4, 0, 0, 0, time.UTC)
	fire := func(n int) HandlerResult {
		t.Helper()
		res, err := handler(ctx, HandlerInput{Scope: Scope{Kind: ScopeKindAutopilotTrigger, ID: util.UUIDToString(trigger.ID)}, PlanTime: base.Add(time.Duration(n) * 15 * time.Minute), Attempt: 1})
		if err != nil {
			t.Fatalf("slot %d: %v", n, err)
		}
		return res
	}
	occurrences := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM employee_routine_occurrence WHERE workspace_id=$1::uuid`, ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	first := fire(0)
	if first.Result["run_status"] != "running" || occurrences() != 1 {
		t.Fatalf("first = %+v", first.Result)
	}
	var queueID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT queue_task_id FROM employee_routine_occurrence WHERE workspace_id=$1::uuid`, ws).Scan(&queueID); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE autopilot SET status='paused' WHERE id=$1`, ap.ID)
	if paused := fire(1); paused.Result["skipped_reason"] != "autopilot_inactive" || occurrences() != 1 {
		t.Fatalf("paused slot = %+v", paused.Result)
	}
	if queue, err := q.GetAgentTask(ctx, queueID); err != nil || queue.Status != "queued" {
		t.Fatal("pause touched accepted work", queue.Status, err)
	}
	// The accepted occurrence ends on its own while paused.
	if _, err := taskSvc.CancelTask(ctx, queueID); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE autopilot SET status='active' WHERE id=$1`, ap.ID)
	if resumed := fire(2); resumed.Result["run_status"] != "running" || occurrences() != 2 {
		t.Fatalf("resumed slot = %+v", resumed.Result)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='cancelled',completed_at=now() WHERE agent_id=$1::uuid AND status='queued'`, agent); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE employee_task_run SET state='cancelled',finished_at=now() WHERE workspace_id=$1::uuid AND state='running'`, ws)
	// Configuration error: a failed run, no handler error, so the slot is
	// recorded and the next slot still fires.
	exec(`UPDATE autopilot SET description='' WHERE id=$1`, ap.ID)
	if failed := fire(3); failed.Result["run_status"] != "failed" || occurrences() != 3 {
		t.Fatalf("config error slot = %+v", failed.Result)
	}
	exec(`UPDATE autopilot SET description='Check the queue again.' WHERE id=$1`, ap.ID)
	if next := fire(4); next.Result["run_status"] != "running" {
		t.Fatalf("cadence after config error = %+v", next.Result)
	}
}
