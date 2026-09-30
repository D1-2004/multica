package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	e2b "github.com/aliyun-fc/e2b-go-sdk"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestFCE2BTaskStopArgsRunAsRootThroughEitherTransport(t *testing.T) {
	runtimeID, taskID := uuid.New().String(), uuid.New().String()
	args := fcE2BTaskStopArgs("sbx_123", runtimeID, 29567, taskID)
	requireFCE2BSDKAccepts(args)
	operation, err := parseFCE2BOperation(args)
	if err != nil || operation.exec == nil || operation.exec.Background {
		t.Fatalf("stop command = %#v, %v", operation, err)
	}
	request := operation.exec
	if request.SandboxID != "sbx_123" || request.User != "root" || request.Env["LD_PRELOAD"] != "" || len(request.Env) != 6 {
		t.Fatalf("stop command identity = %#v", request)
	}
	want := fcE2BShellCommand([]string{"bash", "-c", fcE2BTaskStopScript, "fc-e2b-task-stop", runtimeID, "29567", taskID})
	if request.Command != want || !strings.HasSuffix(request.Command, " fc-e2b-task-stop "+runtimeID+" 29567 "+taskID) {
		t.Fatalf("stop command text = %.120q", request.Command)
	}
}

func TestParseFCE2BTaskStopReceipt(t *testing.T) {
	receipt, err := parseFCE2BTaskStopReceipt("login banner\n" + `{"version":3,"runners":1,"marked":2,"unreadable":1,"found":3,"terminated":2,"killed":1,"remaining":0}` + "\n")
	if err != nil || receipt != (fcE2BTaskStopReceipt{Version: 3, Runners: 1, Marked: 2, Unreadable: 1, Found: 3, Terminated: 2, Killed: 1}) {
		t.Fatalf("receipt = %#v, %v", receipt, err)
	}
	if _, err := parseFCE2BTaskStopReceipt(`{"version":3,"error":"invalid arguments"}`); err == nil {
		t.Fatal("a refused stop was accepted")
	}
	// A version 2 receipt came from the start-time sweep that ended other
	// tasks' orphans (PRI-61).
	for _, out := range []string{"", "done", `{"found":1}`, `{"version":2,"runners":1,"orphans":1,"found":1}`} {
		if _, err := parseFCE2BTaskStopReceipt(out); err == nil {
			t.Fatalf("receipt accepted from %q", out)
		}
	}
}

type stopRecordingRunner struct {
	mu     sync.Mutex
	calls  [][]string
	scopes []FCE2BScope
	out    string
	err    error
}

func (r *stopRecordingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *stopRecordingRunner) Run(ctx context.Context, _ string, args []string, _ []string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), args...))
	r.scopes = append(r.scopes, fcE2BScopeFrom(ctx))
	return r.out, r.err
}

// runtime.fc_e2b_sdk_rollout gates the stop by the task's scope; a selected
// stop takes the SDK transport like every other command of that scope.
func TestFCE2BLauncherStopsTaskProcessesOnlyWhenTheRolloutSelectsTheTask(t *testing.T) {
	workspace, agent, runtimeID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	task := db.AgentTaskQueue{
		ID:        pgtype.UUID{Bytes: taskID, Valid: true},
		AgentID:   pgtype.UUID{Bytes: agent, Valid: true},
		RuntimeID: pgtype.UUID{Bytes: runtimeID, Valid: true},
		Status:    "cancelled",
	}
	rt := db.AgentRuntime{ID: task.RuntimeID, WorkspaceID: pgtype.UUID{Bytes: workspace, Valid: true}}
	other := uuid.New().String()
	for _, tc := range []struct {
		name     string
		rollout  FCE2BSDKRollout
		selected bool
	}{
		{"agent listed", FCE2BSDKRollout{Enabled: true, AgentIDs: []string{agent.String()}}, true},
		{"workspace listed", FCE2BSDKRollout{Enabled: true, WorkspaceIDs: []string{workspace.String()}}, true},
		{"runtime listed", FCE2BSDKRollout{Enabled: true, RuntimeIDs: []string{runtimeID.String()}}, true},
		{"everything", FCE2BSDKRollout{Enabled: true, Percent: 100}, true},
		{"absent", FCE2BSDKRollout{}, false},
		{"master switch off", FCE2BSDKRollout{AgentIDs: []string{agent.String()}, Percent: 100}, false},
		{"other agent", FCE2BSDKRollout{Enabled: true, AgentIDs: []string{other}, WorkspaceIDs: []string{other}, RuntimeIDs: []string{other}}, false},
	} {
		cli := &stopRecordingRunner{out: `{"version":3,"runners":1,"marked":2,"found":2,"terminated":2,"killed":0,"remaining":0}`}
		sdk := &stopRecordingRunner{out: cli.out}
		// The live document says the opposite of the frozen snapshot; the
		// snapshot decides.
		live := FCE2BSDKRollout{Enabled: !tc.selected, Percent: 100}
		l := &FCE2BLauncher{
			Config: FCE2BConfig{SDKRollout: tc.rollout},
			Runner: FCE2BRolloutRunner{CLI: cli, SDK: sdk, Rollout: func() FCE2BSDKRollout { return live }},
		}
		receipt, err := l.stopTaskProcessesInSandbox(context.Background(), task, rt, "sbx_123", 1)
		if !tc.selected {
			if err != nil || receipt != (fcE2BTaskStopReceipt{}) || len(cli.calls)+len(sdk.calls) != 0 {
				t.Fatalf("%s: stop ran: receipt = %#v, %v; cli=%d sdk=%d", tc.name, receipt, err, len(cli.calls), len(sdk.calls))
			}
			continue
		}
		if err != nil || receipt.Found != 2 || receipt.Terminated != 2 {
			t.Fatalf("%s: receipt = %#v, %v", tc.name, receipt, err)
		}
		if len(sdk.calls) != 1 || len(cli.calls) != 0 {
			t.Fatalf("%s: routed cli=%d sdk=%d", tc.name, len(cli.calls), len(sdk.calls))
		}
		wantArgs := fcE2BTaskStopArgs("sbx_123", runtimeID.String(), fcE2BHealthPortForTask(task.ID), taskID.String())
		if !reflect.DeepEqual(sdk.calls[0], wantArgs) {
			t.Fatalf("%s: stop args = %q", tc.name, sdk.calls[0])
		}
		if sdk.scopes[0] != (FCE2BScope{WorkspaceID: workspace, AgentID: agent, RuntimeID: runtimeID}) {
			t.Fatalf("%s: stop scope = %#v", tc.name, sdk.scopes[0])
		}
	}

	on := FCE2BConfig{SDKRollout: FCE2BSDKRollout{Enabled: true, Percent: 100}}
	gone := &stopRecordingRunner{err: fcE2BSDKFailure(&e2b.NotFoundError{Message: `404: sandbox "sbx_123" not found`}, "", "")}
	if _, err := (&FCE2BLauncher{Config: on, Runner: gone}).stopTaskProcessesInSandbox(context.Background(), task, rt, "sbx_123", 1); err != nil {
		t.Fatalf("a removed sandbox has nothing left to stop: %v", err)
	}
	survivors := &stopRecordingRunner{out: `{"version":3,"runners":1,"found":1,"terminated":0,"killed":0,"remaining":1}`}
	if receipt, err := (&FCE2BLauncher{Config: on, Runner: survivors}).stopTaskProcessesInSandbox(context.Background(), task, rt, "sbx_123", 1); err != nil || receipt.Remaining != 1 {
		t.Fatalf("survivors = %#v, %v", receipt, err)
	}
	if _, err := (&FCE2BLauncher{Config: on, Runner: survivors}).stopTaskProcessesInSandbox(context.Background(), task, rt, "sbx;reboot", 1); err == nil {
		t.Fatal("an invalid sandbox id reached the transport")
	}
}

// A sandbox counts as gone only on the SDK's typed not-found, a lifetime 404,
// or CLI text that says so; a "404" elsewhere in command output does not.
func TestFCE2BSandboxMissing(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"sdk not found", fcE2BSDKFailure(&e2b.NotFoundError{Message: "404: sandbox not found"}, "", ""), true},
		{"sdk sandbox not found", fcE2BSDKFailure(&e2b.SandboxNotFoundError{Message: "gone"}, "", ""), true},
		{"lifetime 404", fmt.Errorf("renew: %w", errFCE2BSandboxGone), true},
		{"cli 404", errors.New(`command failed: 404: sandbox "sbx_123" not found: `), true},
		{"cli sandbox not found", errors.New("Sandbox not found"), true},
		{"sdk output mentions 404", fcE2BSDKFailure(&fcE2BCommandExitError{ExitCode: 1}, "GET /x 404 not found", ""), false},
		{"cli 404 without not found", errors.New("command failed: exit status 1: took 404ms"), false},
		{"cli other error", errors.New("command failed: exit status 1: permission denied"), false},
	} {
		if got := fcE2BSandboxMissing(tc.err); got != tc.want {
			t.Errorf("%s: fcE2BSandboxMissing = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// abortedStopLauncher selects every task, so scheduling depends only on the
// task and the pending stops.
func abortedStopLauncher(t *testing.T) *FCE2BLauncher {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://fc-e2b-stop-test@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &FCE2BLauncher{Pool: pool, Config: FCE2BConfig{Enabled: true, SDKRollout: FCE2BSDKRollout{Enabled: true, Percent: 100}}}
}

func newStopTask(status string) db.AgentTaskQueue {
	return db.AgentTaskQueue{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, AgentID: pgtype.UUID{Bytes: uuid.New(), Valid: true},
		RuntimeID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, Status: status}
}

func waitTaskStopDone(t *testing.T, tasks ...db.AgentTaskQueue) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, task := range tasks {
		for {
			if _, pending := fcE2BTaskStopPending.Load(util.UUIDToString(task.ID)); !pending {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the stop never finished")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestAbortedTaskStopIsScheduledOncePerAbortedTask(t *testing.T) {
	l := abortedStopLauncher(t)
	var mu sync.Mutex
	scheduled := 0
	release := make(chan struct{})
	l.sleep = func(ctx context.Context, d time.Duration) error {
		// The first pass starts at once; stop there.
		if d == 0 {
			mu.Lock()
			scheduled++
			mu.Unlock()
			<-release
		}
		return context.Canceled
	}
	cancelled, failed := newStopTask("cancelled"), newStopTask("failed")
	if !l.scheduleAbortedTaskStop(cancelled) {
		t.Fatal("a cancelled task was not stopped")
	}
	// The event and the metrics path both notify for one terminal write.
	if l.scheduleAbortedTaskStop(cancelled) {
		t.Fatal("a second stop was scheduled for one cancelled task")
	}
	if !l.scheduleAbortedTaskStop(failed) {
		t.Fatal("a failed task was not stopped")
	}
	// A completed task's processes are left to the sandbox release.
	if l.scheduleAbortedTaskStop(newStopTask("completed")) || l.scheduleAbortedTaskStop(newStopTask("running")) {
		t.Fatal("a task that did not abort was stopped")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		started := scheduled
		mu.Unlock()
		if started >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Notifications while the stop is pending start nothing new.
	if l.scheduleAbortedTaskStop(cancelled) {
		t.Fatal("a stop was scheduled while one is pending")
	}
	close(release)
	waitTaskStopDone(t, cancelled, failed)
	mu.Lock()
	defer mu.Unlock()
	if scheduled != 2 {
		t.Fatalf("stops scheduled = %d, want one per aborted task", scheduled)
	}
}

// A stop the frozen rollout cannot select starts no goroutine. The workspace
// is unknown until the runtime is read, so a workspace list keeps the stop.
func TestAbortedTaskStopIsScheduledOnlyWhenTheRolloutMaySelectTheTask(t *testing.T) {
	task := newStopTask("cancelled")
	agent := uuid.UUID(task.AgentID.Bytes).String()
	other := uuid.New().String()
	for _, tc := range []struct {
		name     string
		config   FCE2BConfig
		schedule bool
	}{
		{"agent listed", FCE2BConfig{Enabled: true, SDKRollout: FCE2BSDKRollout{Enabled: true, AgentIDs: []string{agent}}}, true},
		{"workspace list", FCE2BConfig{Enabled: true, SDKRollout: FCE2BSDKRollout{Enabled: true, WorkspaceIDs: []string{other}}}, true},
		{"everything", FCE2BConfig{Enabled: true, SDKRollout: FCE2BSDKRollout{Enabled: true, Percent: 100}}, true},
		{"rollout absent", FCE2BConfig{Enabled: true}, false},
		{"master switch off", FCE2BConfig{Enabled: true, SDKRollout: FCE2BSDKRollout{AgentIDs: []string{agent}, Percent: 100}}, false},
		{"other agent", FCE2BConfig{Enabled: true, SDKRollout: FCE2BSDKRollout{Enabled: true, AgentIDs: []string{other}, RuntimeIDs: []string{other}}}, false},
		{"FC/E2B disabled", FCE2BConfig{SDKRollout: FCE2BSDKRollout{Enabled: true, Percent: 100}}, false},
	} {
		l := abortedStopLauncher(t)
		l.Config = tc.config
		l.sleep = func(context.Context, time.Duration) error { return context.Canceled }
		if got := l.scheduleAbortedTaskStop(task); got != tc.schedule {
			t.Fatalf("%s: scheduled = %v, want %v", tc.name, got, tc.schedule)
		}
		waitTaskStopDone(t, task)
	}
}

func TestAbortedTaskStopBacklogIsBounded(t *testing.T) {
	l := abortedStopLauncher(t)
	l.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	fcE2BTaskStopPendingCount.Add(fcE2BTaskStopMaxPending)
	defer fcE2BTaskStopPendingCount.Add(-fcE2BTaskStopMaxPending)
	task := newStopTask("cancelled")
	if l.scheduleAbortedTaskStop(task) {
		t.Fatal("a stop was scheduled past the backlog")
	}
	if _, pending := fcE2BTaskStopPending.Load(util.UUIDToString(task.ID)); pending {
		t.Fatal("a refused stop stayed pending and would block the next notification")
	}
}

// A pass that panics gives its slot back; a pass that finds nothing to stop
// ends the stop without a second pass, which otherwise follows 10 seconds
// after the task ended.
func TestAbortedTaskStopPasses(t *testing.T) {
	l := abortedStopLauncher(t)
	var mu sync.Mutex
	var waits []time.Duration
	l.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return nil
	}
	passes := func(stopPass func(pass int) bool) []int {
		t.Helper()
		var ran []int
		mu.Lock()
		waits = nil
		mu.Unlock()
		l.stopPass = func(_ context.Context, _ *FCE2BLauncher, _ pgtype.UUID, pass int) bool {
			mu.Lock()
			ran = append(ran, pass)
			mu.Unlock()
			return stopPass(pass)
		}
		task := newStopTask("cancelled")
		if !l.scheduleAbortedTaskStop(task) {
			t.Fatal("the stop was not scheduled")
		}
		waitTaskStopDone(t, task)
		if len(fcE2BTaskStopSlots) != 0 {
			t.Fatalf("stop slots held after the stop: %d", len(fcE2BTaskStopSlots))
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), ran...)
	}
	if got := passes(func(int) bool { return true }); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("passes = %v, want both", got)
	}
	mu.Lock()
	if len(waits) != 2 || waits[0] != 0 || waits[1] <= 0 || waits[1] > fcE2BTaskStopSecondPass {
		t.Fatalf("waits = %v, want the first at once and the second within %v of the end", waits, fcE2BTaskStopSecondPass)
	}
	mu.Unlock()
	if got := passes(func(int) bool { return false }); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("passes = %v, want the first only", got)
	}
	if got := passes(func(int) bool { panic("stop pass") }); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("passes = %v, want the first only", got)
	}
}

// One aborted-task stop runs both passes under the snapshot in force when it
// was scheduled. Switching runtime.fc_e2b_sdk_rollout between the passes
// neither skips the second pass of a selected stop nor starts a stop that was
// not selected; stops scheduled after the switch follow the new value.
func TestAbortedTaskStopKeepsItsSnapshotAcrossPasses(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://fc-e2b-stop-test@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	workspace, agent, runtimeID := uuid.New(), uuid.New(), uuid.New()
	rt := db.AgentRuntime{ID: pgtype.UUID{Bytes: runtimeID, Valid: true}, WorkspaceID: pgtype.UUID{Bytes: workspace, Valid: true}}
	selected := FCE2BSDKRollout{Enabled: true, AgentIDs: []string{agent.String()}}
	receipt := `{"version":3,"runners":1,"found":1,"terminated":1,"killed":0,"remaining":0}`
	// stop schedules one cancelled task while live is published, publishes
	// next between its passes, and returns the transport of each pass: "sdk",
	// "cli", or "" when the pass sent nothing; nil when no stop started.
	stop := func(live, next FCE2BSDKRollout) []string {
		t.Helper()
		var mu sync.Mutex
		current := live
		cli := &stopRecordingRunner{out: receipt}
		sdk := &stopRecordingRunner{out: receipt}
		var sent []string
		done := make(chan struct{})
		task := db.AgentTaskQueue{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, AgentID: pgtype.UUID{Bytes: agent, Valid: true},
			RuntimeID: rt.ID, Status: "cancelled"}
		l := &FCE2BLauncher{
			Pool:   pool,
			Runner: FCE2BRolloutRunner{CLI: cli, SDK: sdk, Rollout: func() FCE2BSDKRollout { mu.Lock(); defer mu.Unlock(); return current }},
			ConfigProvider: func() FCE2BConfig {
				mu.Lock()
				defer mu.Unlock()
				return FCE2BConfig{Enabled: true, SDKRollout: current}
			},
			sleep: func(ctx context.Context, d time.Duration) error {
				if d > 0 {
					mu.Lock()
					current = next
					mu.Unlock()
				}
				return nil
			},
			stopPass: func(ctx context.Context, frozen *FCE2BLauncher, _ pgtype.UUID, pass int) bool {
				cliBefore, sdkBefore := cli.count(), sdk.count()
				if _, err := frozen.stopTaskProcessesInSandbox(ctx, task, rt, "sbx_123", pass); err != nil {
					t.Errorf("pass %d: %v", pass, err)
				}
				transport := ""
				switch {
				case sdk.count() > sdkBefore:
					transport = "sdk"
				case cli.count() > cliBefore:
					transport = "cli"
				}
				mu.Lock()
				sent = append(sent, transport)
				complete := len(sent) == 2
				mu.Unlock()
				if complete {
					close(done)
				}
				return true
			},
		}
		if !l.scheduleAbortedTaskStop(task) {
			return nil
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the stop did not run both passes")
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sent...)
	}
	for _, tc := range []struct {
		name       string
		live, next FCE2BSDKRollout
		want       []string
	}{
		{"switched off between the passes", selected, FCE2BSDKRollout{}, []string{"sdk", "sdk"}},
		{"master switch off between the passes", selected, FCE2BSDKRollout{Enabled: false, AgentIDs: selected.AgentIDs}, []string{"sdk", "sdk"}},
		{"switched on between the passes", FCE2BSDKRollout{}, selected, nil},
		{"unchanged on", selected, selected, []string{"sdk", "sdk"}},
		{"unchanged off", FCE2BSDKRollout{}, FCE2BSDKRollout{}, nil},
	} {
		if got := stop(tc.live, tc.next); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: passes sent %q, want %q", tc.name, got, tc.want)
		}
		// A stop scheduled after the switch takes the value now in force.
		var want []string
		if fcE2BRolloutSelects(tc.next, FCE2BScope{WorkspaceID: workspace, AgentID: agent, RuntimeID: runtimeID}) {
			want = []string{"sdk", "sdk"}
		}
		if got := stop(tc.next, tc.next); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: a later stop sent %q, want %q", tc.name, got, want)
		}
	}
}
