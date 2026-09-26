package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/startupobs"
)

// Exercise the background entry point, not just Start with a manually injected
// recorder: deferred NAS steps otherwise silently lose all task correlation.
func TestReadinessReconcilerObservesQueuedTaskAndHotOff(t *testing.T) {
	ctx := context.Background()
	pool, tasks, task, _ := quickwinFixture(t)
	runtime, err := tasks.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO dsh_storage_provision(workspace_id,agent_id,spec,intent) VALUES($1,$2,'{}',$3)`, runtime.WorkspaceID, task.AgentID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM dsh_storage_provision WHERE workspace_id=$1 AND agent_id=$2`, runtime.WorkspaceID, task.AgentID)
	})
	client, exporter := taskTraceTestClient(t)
	tasks.Langfuse = client
	enabled := true
	tasks.RuntimeStartRecoveryConfig = func() RuntimeStartRecoveryConfig { return RuntimeStartRecoveryConfig{StartupObservability: enabled} }
	calls := 0
	launcher := &FCE2BLauncher{Pool: pool, Tasks: tasks, ProvisionDSHStorage: func(work context.Context, _ dshhost.Database, key dshhost.Key) (dshhost.Host, error) {
		if key.AgentID != uuid.UUID(task.AgentID.Bytes) {
			t.Error("unexpected agent")
		}
		calls++
		startupobs.Start(work, "nas_step_5")(nil)
		return dshhost.Host{}, dshhost.ErrPending
	}}
	for i, on := range []bool{true, false, true} {
		enabled = on
		launcher.reconcileRuntimeReadiness(ctx)
		want := 1
		if i == 2 {
			want = 2
		}
		spans := exporter.GetSpans()
		if len(spans) != want {
			t.Fatalf("phase %d: spans=%d want=%d", i, len(spans), want)
		}
		for _, span := range spans {
			if span.Name != "runtime_start.nas_step_5" || taskTraceAttr(span, "langfuse.observation.metadata.task_id") != uuid.UUID(task.ID.Bytes).String() {
				t.Fatal("background step lost task correlation")
			}
		}
	}
	if calls != 3 {
		t.Fatalf("Q7 changed reconciliation behavior: calls=%d", calls)
	}
}
