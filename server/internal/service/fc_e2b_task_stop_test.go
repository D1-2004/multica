package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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

func (r *stopRecordingRunner) Run(ctx context.Context, _ string, args []string, _ []string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), args...))
	r.scopes = append(r.scopes, fcE2BScopeFrom(ctx))
	return r.out, r.err
}

// The stop runs through the rollout like every FC/E2B command, so the SDK
// and the CLI transport both end a cancelled task's processes.
func TestFCE2BLauncherStopsTaskProcessesThroughTheRollout(t *testing.T) {
	workspace, agent, runtimeID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	task := db.AgentTaskQueue{
		ID:        pgtype.UUID{Bytes: taskID, Valid: true},
		AgentID:   pgtype.UUID{Bytes: agent, Valid: true},
		RuntimeID: pgtype.UUID{Bytes: runtimeID, Valid: true},
		Status:    "cancelled",
	}
	rt := db.AgentRuntime{ID: task.RuntimeID, WorkspaceID: pgtype.UUID{Bytes: workspace, Valid: true}}
	for _, enabled := range []bool{true, false} {
		cli := &stopRecordingRunner{out: `{"version":3,"runners":1,"marked":2,"found":2,"terminated":2,"killed":0,"remaining":0}`}
		sdk := &stopRecordingRunner{out: cli.out}
		rollout := FCE2BSDKRollout{Enabled: enabled, AgentIDs: []string{agent.String()}}
		l := &FCE2BLauncher{Runner: FCE2BRolloutRunner{CLI: cli, SDK: sdk, Rollout: func() (FCE2BSDKRollout, string) { return rollout, fcE2BRolloutSourceDiamond }}}
		receipt, err := l.stopTaskProcessesInSandbox(context.Background(), task, rt, "sbx_123", 1)
		if err != nil || receipt.Found != 2 || receipt.Terminated != 2 {
			t.Fatalf("sdk=%v receipt = %#v, %v", enabled, receipt, err)
		}
		used, idle := cli, sdk
		if enabled {
			used, idle = sdk, cli
		}
		if len(used.calls) != 1 || len(idle.calls) != 0 {
			t.Fatalf("sdk=%v routed cli=%d sdk=%d", enabled, len(cli.calls), len(sdk.calls))
		}
		wantArgs := fcE2BTaskStopArgs("sbx_123", runtimeID.String(), fcE2BHealthPortForTask(task.ID), taskID.String())
		if !reflect.DeepEqual(used.calls[0], wantArgs) {
			t.Fatalf("stop args = %q", used.calls[0])
		}
		if used.scopes[0] != (FCE2BScope{WorkspaceID: workspace, AgentID: agent, RuntimeID: runtimeID}) {
			t.Fatalf("stop scope = %#v", used.scopes[0])
		}
	}

	gone := &stopRecordingRunner{err: errors.New(`command failed: 404: sandbox "sbx_123" not found: `)}
	if _, err := (&FCE2BLauncher{Runner: gone}).stopTaskProcessesInSandbox(context.Background(), task, rt, "sbx_123", 1); err != nil {
		t.Fatalf("a removed sandbox has nothing left to stop: %v", err)
	}
	survivors := &stopRecordingRunner{out: `{"version":3,"runners":1,"found":1,"terminated":0,"killed":0,"remaining":1}`}
	if receipt, err := (&FCE2BLauncher{Runner: survivors}).stopTaskProcessesInSandbox(context.Background(), task, rt, "sbx_123", 1); err != nil || receipt.Remaining != 1 {
		t.Fatalf("survivors = %#v, %v", receipt, err)
	}
	if _, err := (&FCE2BLauncher{Runner: survivors}).stopTaskProcessesInSandbox(context.Background(), task, rt, "sbx;reboot", 1); err == nil {
		t.Fatal("an invalid sandbox id reached the transport")
	}
}

func TestCancelledTaskStopIsScheduledOncePerCancelledTask(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://fc-e2b-stop-test@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var mu sync.Mutex
	scheduled := 0
	release := make(chan struct{})
	l := &FCE2BLauncher{Pool: pool, sleep: func(ctx context.Context, d time.Duration) error {
		// The first pass starts at once; stop there.
		if d == 0 {
			mu.Lock()
			scheduled++
			mu.Unlock()
			<-release
		}
		return context.Canceled
	}}
	newTask := func(status string) db.AgentTaskQueue {
		return db.AgentTaskQueue{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, RuntimeID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, Status: status}
	}
	cancelled := newTask("cancelled")
	l.scheduleCancelledTaskStop(cancelled)
	// The event and the metrics path both notify for one terminal write.
	l.scheduleCancelledTaskStop(cancelled)
	l.scheduleCancelledTaskStop(newTask("completed"))
	l.scheduleCancelledTaskStop(newTask("failed"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		started := scheduled
		mu.Unlock()
		if started >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Notifications while the stop is pending start nothing new.
	l.scheduleCancelledTaskStop(cancelled)
	close(release)
	for {
		if _, pending := fcE2BTaskStopPending.Load(util.UUIDToString(cancelled.ID)); !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stop never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if scheduled != 1 {
		t.Fatalf("stops scheduled = %d, want one for the cancelled task only", scheduled)
	}
}
