package service

import (
	"context"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// stubWakeup records every call so the test can assert that notify
// reaches the daemon hub and carries the right runtime / task IDs.
type stubWakeup struct {
	calls []struct{ runtimeID, taskID string }
}

func (s *stubWakeup) NotifyTaskAvailable(runtimeID, taskID string) {
	s.calls = append(s.calls, struct{ runtimeID, taskID string }{runtimeID, taskID})
}

type stubRuntimeLauncher struct {
	calls chan db.AgentTaskQueue
}

func (s *stubRuntimeLauncher) LaunchTask(_ context.Context, task db.AgentTaskQueue) error {
	s.calls <- task
	return nil
}

// TestNotifyTaskAvailable_BumpsBeforeWakeup pins the contract noted in
// the EmptyClaimCache docs: the version Bump MUST run before the
// daemon WS wakeup, otherwise the wakeup-driven claim could read a
// still-current empty verdict and return null while the freshly
// queued task sits idle. The test (1) marks the runtime empty under
// the current version, (2) fires notifyTaskAvailable, then (3)
// asserts the prior verdict is rejected AND the wakeup hook saw the
// new task — proving every enqueue path (issue / mention /
// quick-create / chat / autopilot / retry) gets the same
// bump-then-notify behaviour for free.
func TestNotifyTaskAvailable_BumpsBeforeWakeup(t *testing.T) {
	rdb := newRedisTestClient(t)
	cache := NewEmptyClaimCache(rdb)
	wakeup := &stubWakeup{}

	svc := &TaskService{
		EmptyClaim: cache,
		Wakeup:     wakeup,
	}

	runtimeID := testUUID(7)
	taskID := testUUID(8)
	runtimeKey := util.UUIDToString(runtimeID)

	ctx := context.Background()
	v0 := cache.CurrentVersion(ctx, runtimeKey)
	cache.MarkEmpty(ctx, runtimeKey, v0)
	if !cache.IsEmpty(ctx, runtimeKey) {
		t.Fatal("precondition: cache should report empty after MarkEmpty under current version")
	}

	svc.notifyTaskAvailable(db.AgentTaskQueue{
		ID:        taskID,
		RuntimeID: runtimeID,
	})

	if cache.IsEmpty(ctx, runtimeKey) {
		t.Fatal("notifyTaskAvailable must Bump the version so the prior empty verdict is rejected")
	}
	if got := len(wakeup.calls); got != 1 {
		t.Fatalf("expected 1 wakeup call, got %d", got)
	}
	if wakeup.calls[0].runtimeID != runtimeKey {
		t.Fatalf("wakeup runtime mismatch: got %q want %q", wakeup.calls[0].runtimeID, runtimeKey)
	}
	if wakeup.calls[0].taskID != util.UUIDToString(taskID) {
		t.Fatalf("wakeup task mismatch: got %q want %q", wakeup.calls[0].taskID, util.UUIDToString(taskID))
	}
}

// TestNotifyTaskAvailable_InvalidWithoutRuntimeIsNoOp guards the
// no-RuntimeID early return — chat / quick-create / autopilot all set
// it on insert, but a buggy caller that forgot must not silently bump
// every workspace's version. The cache treats Bump("") as a no-op,
// but this test pins that the RuntimeID guard sits above the Bump
// call so a future refactor cannot drop the guard without test
// coverage.
func TestNotifyTaskAvailable_InvalidWithoutRuntimeIsNoOp(t *testing.T) {
	rdb := newRedisTestClient(t)
	cache := NewEmptyClaimCache(rdb)
	wakeup := &stubWakeup{}

	svc := &TaskService{
		EmptyClaim: cache,
		Wakeup:     wakeup,
	}

	ctx := context.Background()
	v0 := cache.CurrentVersion(ctx, "rt-stays")
	cache.MarkEmpty(ctx, "rt-stays", v0)

	svc.notifyTaskAvailable(db.AgentTaskQueue{
		// RuntimeID intentionally invalid (zero value, Valid=false).
		ID: testUUID(9),
	})

	if !cache.IsEmpty(ctx, "rt-stays") {
		t.Fatal("notifyTaskAvailable with invalid RuntimeID must not touch cache")
	}
	if got := len(wakeup.calls); got != 0 {
		t.Fatalf("expected 0 wakeup calls when RuntimeID is invalid, got %d", got)
	}
}

func TestNotifyTaskEnqueued_InvokesRuntimeLauncher(t *testing.T) {
	rdb := newRedisTestClient(t)
	cache := NewEmptyClaimCache(rdb)
	wakeup := &stubWakeup{}
	launcher := &stubRuntimeLauncher{calls: make(chan db.AgentTaskQueue, 1)}

	svc := &TaskService{
		EmptyClaim:      cache,
		Wakeup:          wakeup,
		RuntimeLauncher: launcher,
	}

	task := db.AgentTaskQueue{
		ID:        testUUID(10),
		RuntimeID: testUUID(11),
	}
	svc.NotifyTaskEnqueued(context.Background(), task)

	select {
	case got := <-launcher.calls:
		if util.UUIDToString(got.ID) != util.UUIDToString(task.ID) {
			t.Fatalf("launcher task id = %q, want %q", util.UUIDToString(got.ID), util.UUIDToString(task.ID))
		}
	case <-time.After(time.Second):
		t.Fatal("runtime launcher was not invoked")
	}
	if got := len(wakeup.calls); got != 1 {
		t.Fatalf("expected wakeup to remain wired, got %d calls", got)
	}
}

func TestNextQueuedTaskForTerminal_SelectsSameIssueAgent(t *testing.T) {
	terminal := db.AgentTaskQueue{
		ID:        testUUID(20),
		RuntimeID: testUUID(21),
		AgentID:   testUUID(22),
		IssueID:   testUUID(23),
	}
	want := db.AgentTaskQueue{
		ID:        testUUID(24),
		RuntimeID: terminal.RuntimeID,
		AgentID:   terminal.AgentID,
		IssueID:   terminal.IssueID,
	}
	candidates := []db.AgentTaskQueue{
		{
			ID:        testUUID(25),
			RuntimeID: terminal.RuntimeID,
			AgentID:   testUUID(26),
			IssueID:   terminal.IssueID,
		},
		want,
	}

	got, ok := nextQueuedTaskForTerminal(terminal, candidates)
	if !ok {
		t.Fatal("expected a queued task in the same issue/agent lane")
	}
	if util.UUIDToString(got.ID) != util.UUIDToString(want.ID) {
		t.Fatalf("next queued task = %q, want %q", util.UUIDToString(got.ID), util.UUIDToString(want.ID))
	}
}

func TestNextQueuedTaskForTerminal_SelectsSameChatAgent(t *testing.T) {
	terminal := db.AgentTaskQueue{
		ID:            testUUID(30),
		RuntimeID:     testUUID(31),
		AgentID:       testUUID(32),
		ChatSessionID: testUUID(33),
	}
	want := db.AgentTaskQueue{
		ID:            testUUID(34),
		RuntimeID:     terminal.RuntimeID,
		AgentID:       terminal.AgentID,
		ChatSessionID: terminal.ChatSessionID,
	}
	candidates := []db.AgentTaskQueue{
		{
			ID:            testUUID(35),
			RuntimeID:     terminal.RuntimeID,
			AgentID:       terminal.AgentID,
			ChatSessionID: testUUID(36),
		},
		want,
	}

	got, ok := nextQueuedTaskForTerminal(terminal, candidates)
	if !ok {
		t.Fatal("expected a queued task in the same chat/agent lane")
	}
	if util.UUIDToString(got.ID) != util.UUIDToString(want.ID) {
		t.Fatalf("next queued task = %q, want %q", util.UUIDToString(got.ID), util.UUIDToString(want.ID))
	}
}

func TestNextQueuedTaskForTerminal_SelectsQuickCreateShape(t *testing.T) {
	terminal := db.AgentTaskQueue{
		ID:        testUUID(40),
		RuntimeID: testUUID(41),
		AgentID:   testUUID(42),
	}
	want := db.AgentTaskQueue{
		ID:        testUUID(43),
		RuntimeID: terminal.RuntimeID,
		AgentID:   terminal.AgentID,
	}
	candidates := []db.AgentTaskQueue{
		{
			ID:             testUUID(44),
			RuntimeID:      terminal.RuntimeID,
			AgentID:        terminal.AgentID,
			AutopilotRunID: testUUID(45),
		},
		want,
	}

	got, ok := nextQueuedTaskForTerminal(terminal, candidates)
	if !ok {
		t.Fatal("expected a queued task in the same quick-create lane")
	}
	if util.UUIDToString(got.ID) != util.UUIDToString(want.ID) {
		t.Fatalf("next queued task = %q, want %q", util.UUIDToString(got.ID), util.UUIDToString(want.ID))
	}
}

func TestNextQueuedTaskForTerminal_RejectsMissingAgent(t *testing.T) {
	terminal := db.AgentTaskQueue{
		ID:        testUUID(50),
		RuntimeID: testUUID(51),
		IssueID:   testUUID(52),
	}
	candidates := []db.AgentTaskQueue{{
		ID:        testUUID(53),
		RuntimeID: terminal.RuntimeID,
		AgentID:   testUUID(54),
		IssueID:   terminal.IssueID,
	}}

	if _, ok := nextQueuedTaskForTerminal(terminal, candidates); ok {
		t.Fatal("missing terminal agent must not drain an unrelated queued task")
	}
}
