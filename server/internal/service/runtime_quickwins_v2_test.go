package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/startupobs"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestReadinessEventBeforeBlockedAndLeaseRelease(t *testing.T) {
	ctx := context.Background()
	pool, svc, task, attempt := quickwinFixture(t)
	rt, err := svc.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	key := dshhost.Key{WorkspaceID: uuid.UUID(rt.WorkspaceID.Bytes), AgentID: uuid.UUID(task.AgentID.Bytes)}
	svc.RuntimeStartRecoveryConfig = func() RuntimeStartRecoveryConfig { return RuntimeStartRecoveryConfig{DSHEventWakeup: true} }
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM runtime_readiness_event WHERE workspace_id=$1`, rt.WorkspaceID) })
	// A different replica publishes while the original launcher still owns its lease.
	leaseStore := newPostgresTaskRuntimeLaunchLeaseStore(svc.Queries)
	lease, ok, err := leaseStore.Acquire(ctx, task.ID, time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire: %v %v", ok, err)
	}
	svc.NotifyDSHReadiness(ctx, pool, key, "provisioning_ready")
	if _, err = svc.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "dsh_host_waiting"); err != nil {
		t.Fatal(err)
	}
	if err = svc.MarkRuntimeStartBlocked(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	check := func(flags db.ListDSHHostWaitingTasksParams, want bool) {
		t.Helper()
		rows, err := svc.Queries.ListDSHHostWaitingTasks(ctx, flags)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			found = found || row.ID == task.ID
		}
		if found != want {
			t.Fatalf("selected=%v want=%v", found, want)
		}
	}
	eventFlags := db.ListDSHHostWaitingTasksParams{DshEventWakeup: true, EventsOnly: true}
	check(eventFlags, false)
	if err = leaseStore.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	check(eventFlags, true)
	check(db.ListDSHHostWaitingTasksParams{}, false)
	// Two replicas observing one durable event still elect only one launcher.
	var wg sync.WaitGroup
	results := make(chan taskRuntimeLaunchLease, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, ok, err := leaseStore.Acquire(ctx, task.ID, time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				results <- lease
			}
		}()
	}
	wg.Wait()
	close(results)
	count := 0
	for lease := range results {
		count++
		_ = leaseStore.Release(ctx, lease)
	}
	if count != 1 {
		t.Fatalf("launch owners=%d", count)
	}
	// An event is consumed by the next attempt, so a fresh pending result cannot spin.
	next, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolHTTPJSONV1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RecordRuntimeStartStage(ctx, next.ID, task.ID, task.RuntimeID, "dsh_host_waiting"); err != nil {
		t.Fatal(err)
	}
	if err = svc.MarkRuntimeStartBlocked(ctx, next); err != nil {
		t.Fatal(err)
	}
	check(eventFlags, false)
}

func TestRuntimeShutdownJoinsReleaseWithoutFailingQueuedTask(t *testing.T) {
	ctx := context.Background()
	_, svc, task, attempt := quickwinFixture(t)
	svc.RuntimeStartRecoveryConfig = func() RuntimeStartRecoveryConfig { return RuntimeStartRecoveryConfig{RecoverAbandonedLaunches: true} }
	launchCtx, finish, ok := svc.trackRuntimeLaunch(ctx)
	if !ok {
		t.Fatal("launch rejected")
	}
	lease, ok, err := svc.runtimeLaunchLeases.Acquire(ctx, task.ID, time.Minute)
	if err != nil || !ok {
		t.Fatal("lease acquisition failed")
	}
	finished := make(chan error, 1)
	go func() {
		<-launchCtx.Done()
		_, err := svc.FailTaskRuntimeStart(launchCtx, task.ID, task.RuntimeID, attempt.ID, ClassifyRuntimeStartError(SandboxBackendAliyunFC, launchCtx.Err()))
		if !errors.Is(err, errRuntimeLaunchShutdown) {
			finished <- errors.New("shutdown was not recognized")
		} else {
			finished <- svc.runtimeLaunchLeases.Release(ctx, lease)
		}
		finish()
	}()
	shutdown, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err = svc.ShutdownRuntimeLaunches(shutdown); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	got, err := svc.Queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "queued" || got.RuntimeLaunchLeaseExpiresAt.Valid {
		t.Fatalf("shutdown changed task or retained lease: %s", got.Status)
	}
	if _, _, ok = svc.trackRuntimeLaunch(ctx); ok {
		t.Fatal("admitted during shutdown")
	}
}

func TestASBCapacityRealEventIsDistinctFromRetryCompletion(t *testing.T) {
	enabled := true
	svc := &TaskService{RuntimeStartRecoveryConfig: func() RuntimeStartRecoveryConfig { return RuntimeStartRecoveryConfig{ASBEventWakeup: enabled} }}
	l := &ASBLauncher{Tasks: svc, CapacityWait: newASBCapacityWaitCoordinator()}
	l.NotifyRuntimeCapacityMayBeAvailable()
	select {
	case <-l.CapacityWait.capacityAvailable:
	default:
		t.Fatal("capacity event not delivered")
	}
	l.CapacityWait.notify()
	select {
	case <-l.CapacityWait.capacityAvailable:
		t.Fatal("retry completion bypassed backoff")
	default:
	}
	<-l.CapacityWait.wakeups
	enabled = false
	l.NotifyRuntimeCapacityMayBeAvailable()
	select {
	case <-l.CapacityWait.wakeups:
	default:
		t.Fatal("off lost legacy notification")
	}
}

func TestStartupObservabilityHotOffStopsSpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	enabled := true
	svc := &TaskService{Langfuse: langfuse.NewWithExporter(langfuse.Config{}, exporter), RuntimeStartRecoveryConfig: func() RuntimeStartRecoveryConfig { return RuntimeStartRecoveryConfig{StartupObservability: enabled} }}
	ctx := svc.withStartupObservability(context.Background(), db.AgentTaskQueue{})
	startupobs.Start(ctx, "nas_step_0")(nil)
	if len(exporter.GetSpans()) != 1 {
		t.Fatal("startup span missing")
	}
	enabled = false
	startupobs.Start(ctx, "fc_command_sandbox_exec")(nil)
	if len(exporter.GetSpans()) != 1 {
		t.Fatal("hot-off emitted a span")
	}
}

type deadlineProbeRunner struct {
	remaining time.Duration
	bounded   bool
}

func (r *deadlineProbeRunner) Run(ctx context.Context, _ string, _ []string, _ []string) (string, error) {
	deadline, _ := ctx.Deadline()
	r.remaining = time.Until(deadline)
	r.bounded, _ = ctx.Value(boundedExecKey{}).(bool)
	return "", nil
}
func TestReadyExecDeadlineOnOff(t *testing.T) {
	r := &deadlineProbeRunner{}
	l := &FCE2BLauncher{Runner: r, Config: FCE2BConfig{SandboxReadyTimeout: time.Minute, QuickWins: RuntimeStartRecoveryConfig{BoundedReadyExec: true}}}
	if err := l.checkSandboxReady(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if r.remaining > 5*time.Second || !r.bounded {
		t.Fatal("short deadline/WaitDelay marker missing")
	}
	l.Config.QuickWins.BoundedReadyExec = false
	_ = l.checkSandboxReady(context.Background(), "test")
	if r.remaining < 50*time.Second || r.bounded {
		t.Fatal("off did not restore legacy timeout")
	}
}

func TestBoundedOSExecReturnsWhenChildKeepsPipesOpen(t *testing.T) {
	script := filepath.Join(t.TempDir(), "hold-pipe")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 3 &\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ctx := context.WithValue(context.Background(), boundedExecKey{}, true)
	_, err := (OSCommandRunner{}).Run(ctx, script, nil, nil)
	if err == nil || time.Since(start) > 2800*time.Millisecond {
		t.Fatalf("pipe wait was not bounded: %v %v", time.Since(start), err)
	}
}

func TestHotProbeReceiptCannotEscapeItsSandbox(t *testing.T) {
	runner := &fakeCommandRunner{out: []string{"root-log-v1", ""}}
	l := &FCE2BLauncher{Runner: runner, Config: FCE2BConfig{SandboxReadyTimeout: time.Minute, QuickWins: RuntimeStartRecoveryConfig{CoalescedHotExec: true}}}
	rt := db.AgentRuntime{Provider: "pi", Metadata: []byte(`{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","template_id":"fixture","runner_protocol":"root-log-v1"}`)}
	rt.Metadata, _ = json.Marshal(map[string]string{"runner": FCE2BRunnerCommandForProvider("pi")})
	ctx := context.WithValue(context.Background(), hotRunnerProbeKey{}, &hotRunnerProbe{})
	if err := l.checkReusedSandboxReady(ctx, "sandbox-a", rt); err != nil {
		t.Fatal(err)
	}
	if _, err := l.detectFCE2BRunnerLaunch(ctx, "sandbox-a", rt); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatal("hot path did not coalesce probes")
	}
	if _, err := l.detectFCE2BRunnerLaunch(ctx, "sandbox-b", rt); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 {
		t.Fatal("receipt reused across sandboxes")
	}
	l.Config.QuickWins.CoalescedHotExec = false
	if err := l.checkReusedSandboxReady(ctx, "sandbox-a", rt); err != nil {
		t.Fatal(err)
	}
	if _, err := l.detectFCE2BRunnerLaunch(ctx, "sandbox-a", rt); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 4 {
		t.Fatal("off did not restore two separate exec probes")
	}

}

func TestProvisioningWaitReasonOnOff(t *testing.T) {
	l := &FCE2BLauncher{ProvisionDSHStorage: func(context.Context, dshhost.Database, dshhost.Key) (dshhost.Host, error) {
		return dshhost.Host{}, dshhost.ErrPending
	}}
	for _, enabled := range []bool{false, true, false} {
		l.Config.QuickWins.BoundDSHHostWait = enabled
		err := l.prepareDSHTaskFilesystem(context.Background(), nil, dshhost.Key{})
		if !errors.Is(err, errDSHHostWaiting) {
			t.Fatal("pending lost retry classification")
		}
		want := "dsh_host_waiting"
		if enabled {
			want = "provisioning_pending"
		}
		if got := dshWaitReason(err); got != want {
			t.Fatalf("reason=%s want=%s", got, want)
		}
	}
}

func TestFCSandboxCreateIncludesTaskInstrumentation(t *testing.T) {
	runner := &fakeCommandRunner{out: []string{"Sandbox created with ID sbx_observed using template fixture"}}
	launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{CLIPath: "e2b-test", SandboxReadyTimeout: time.Minute}, runner)
	var stages []string
	ctx := startupobs.WithRecorder(context.Background(), func(_ context.Context, name string, _ time.Time, err error) {
		if err != nil {
			t.Error(err)
		}
		stages = append(stages, name)
	})
	if _, err := launcher.createSandbox(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	if len(stages) != 2 || stages[0] != "fc_command_sandbox_create" || stages[1] != "fc_create" {
		t.Fatalf("create bypassed startup instrumentation: %v", stages)
	}
}
