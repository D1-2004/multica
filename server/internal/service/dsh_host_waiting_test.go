package service

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDSHHostWaitingRecoveryUsesLatestAttempt(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
	id := util.MustParseUUID(taskID)
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='queued',dispatched_at=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	task, err := queries.GetAgentTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"dsh_host_waiting", "sandbox_resolving"} {
		attempt, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolHTTPJSONV1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, stage); err != nil {
			t.Fatal(err)
		}
		if err := svc.MarkRuntimeStartBlocked(ctx, attempt); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE agent_task_runtime_start_attempt SET finished_at=now()-interval '1 minute' WHERE id=$1`, attempt.ID); err != nil {
			t.Fatal(err)
		}
		waiting, err := queries.ListDSHHostWaitingTasks(ctx, db.ListDSHHostWaitingTasksParams{})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, candidate := range waiting {
			found = found || candidate.ID == task.ID
		}
		if found != (stage == "dsh_host_waiting") {
			t.Fatalf("latest stage %s: selected=%t", stage, found)
		}
	}
}

func TestDSHHostWaitingRecoversAbandonedStartupWithoutStealingLiveLease(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, _, workspaceID := dispatchedCommentTaskFixture(t, ctx, pool)
	id := util.MustParseUUID(taskID)
	task, err := queries.GetAgentTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_runtime_start_attempt WHERE task_id=$1`, id)
		pool.Exec(context.Background(), `DELETE FROM dsh_employee_session WHERE agent_id=$1`, task.AgentID)
	})
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='queued',dispatched_at=NULL,created_at=now()-interval '10 minutes' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO dsh_employee_session (workspace_id,agent_id,scope_kind,scope_id,session_id) VALUES ($1,$2,'issue',$3,$4)`, workspaceID, task.AgentID, task.IssueID, "session-"+taskID); err != nil {
		t.Fatal(err)
	}
	task, _ = queries.GetAgentTask(ctx, id)
	attempt, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolHTTPJSONV1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_runtime_start_attempt SET last_stage='sandbox_resolving',updated_at=now()-interval '3 minutes' WHERE id=$1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		rows, err := queries.ListDSHHostWaitingTasks(ctx, db.ListDSHHostWaitingTasksParams{})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			found = found || row.ID == id
		}
		if found != want {
			t.Fatalf("recovery selected=%t want=%t", found, want)
		}
	}
	check(true)
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET runtime_launch_lease_expires_at=now()+interval '1 minute' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET runtime_launch_lease_expires_at=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_runtime_start_attempt SET last_stage='runner_exec_submitted' WHERE id=$1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := pool.Exec(ctx, `UPDATE agent_task_runtime_start_attempt SET last_stage='sandbox_resolving',runner_started_at=now() WHERE id=$1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	check(false)
}
