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
		waiting, err := queries.ListDSHHostWaitingTasks(ctx)
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
