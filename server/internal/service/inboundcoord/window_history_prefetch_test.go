package inboundcoord

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// sequencedHistory answers each Load with the next scripted error, then
// succeeds; it records every call.
type sequencedHistory struct {
	mu      sync.Mutex
	calls   int
	turns   []Turn
	errs    []error
	history []HistoryLine
	block   chan struct{}
}

func (s *sequencedHistory) Load(ctx context.Context, turn Turn) ([]HistoryLine, error) {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.turns = append(s.turns, turn)
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	return append([]HistoryLine(nil), s.history...), nil
}

func (s *sequencedHistory) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func windowTurn(cutoff time.Time) Turn {
	return Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ChatType:       "p2p",
		AgentID:        testAgentID(),
		ConversationID: "cid-real",
		DWSUID:         "24710833",
		DWSOrgID:       "439446171",
		EvidenceID:     "msg-2",
		Utterances: []WindowUtterance{
			{Text: "6 点", EvidenceID: "msg-2", Timestamp: cutoff},
		},
		HistoryBefore:    cutoff,
		MessageTimestamp: cutoff,
	}
}

func windowCoordinator(t *testing.T, loader DingTalkHistoryLoader, allowed *atomic.Bool) (*Coordinator, *atomic.Int32, *string) {
	t.Helper()
	var calls atomic.Int32
	prompt := new(string)
	c := &Coordinator{LLM: decisionLLM(t, &calls, prompt, false), DWSHistory: loader, windowHistory: newWindowHistoryReads()}
	c.HistoryPrefetchAgentProvider = func(pgtype.UUID) bool { return allowed.Load() }
	return c, &calls, prompt
}

func waitCalls(t *testing.T, loader *sequencedHistory, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for loader.count() < want {
		if time.Now().After(deadline) {
			t.Fatalf("history loads = %d, want %d", loader.count(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWindowHistoryReadServesTheClaimedDecision(t *testing.T) {
	// The first read is the early one; any later read (the shadow) differs.
	loader := &scriptedHistory{answers: [][]HistoryLine{
		{{Role: "菲迪", Content: "晚上几点出发？"}},
		{{Role: "菲迪", Content: "认领时的读取"}},
	}}
	var allowed atomic.Bool
	allowed.Store(true)
	var calls atomic.Int32
	prompt := new(string)
	c := &Coordinator{LLM: decisionLLM(t, &calls, prompt, false), DWSHistory: loader, windowHistory: newWindowHistoryReads()}
	c.HistoryPrefetchAgentProvider = func(pgtype.UUID) bool { return allowed.Load() }
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)

	if !c.PrefetchWindowHistory(windowTurn(cutoff)) {
		t.Fatal("an allowed, readable window must start its history read")
	}
	// A repeated notification for the same window does not read twice.
	if !c.PrefetchWindowHistory(windowTurn(cutoff)) {
		t.Fatal("the running read must be reported")
	}
	deadline := time.Now().Add(2 * time.Second)
	for loader.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	turn := windowTurn(cutoff)
	turn.Message = "6 点"
	got := c.Decide(context.Background(), turn)
	if got.Action != ActionReply || calls.Load() != 1 {
		t.Fatalf("decision=%+v llm_calls=%d", got, calls.Load())
	}
	if !strings.Contains(*prompt, `"status":"loaded"`) || !strings.Contains(*prompt, "晚上几点出发") || strings.Contains(*prompt, "认领时的读取") {
		t.Fatalf("the first prompt must carry the window read: %q", *prompt)
	}
	// Consumed once: the entry is gone after the decision took it.
	if c.takeWindowHistory(windowTurn(cutoff)) != nil {
		t.Fatal("a window read must be consumed by one decision")
	}
}

func TestWindowHistoryReadNeedsIdenticalInputs(t *testing.T) {
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	changes := map[string]func(*Turn){
		"later cutoff": func(t *Turn) {
			t.HistoryBefore = cutoff.Add(3 * time.Second)
			t.MessageTimestamp = t.HistoryBefore
		},
		"another window message": func(t *Turn) {
			t.Utterances = append(t.Utterances, WindowUtterance{Text: "改 7 点", EvidenceID: "msg-3", Timestamp: cutoff})
		},
		"another conversation": func(t *Turn) { t.ConversationID = "cid-other" },
		"another reader":       func(t *Turn) { t.DWSUID = "other-uid" },
		"another agent":        func(t *Turn) { t.AgentID = pgtype.UUID{Bytes: [16]byte{9}, Valid: true} },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			loader := &sequencedHistory{history: []HistoryLine{{Role: "菲迪", Content: "晚上几点出发？"}}}
			var allowed atomic.Bool
			allowed.Store(true)
			c, _, _ := windowCoordinator(t, loader, &allowed)
			c.PrefetchWindowHistory(windowTurn(cutoff))
			waitCalls(t, loader, 1)
			turn := windowTurn(cutoff)
			change(&turn)
			turn.Message = "6 点"
			c.Decide(context.Background(), turn)
			if n := loader.count(); n != 2 {
				t.Fatalf("a read with different inputs must not be reused, loads=%d", n)
			}
			last := loader.turns[len(loader.turns)-1]
			if windowHistoryKey(last) != windowHistoryKey(turn) {
				t.Fatal("the claim-time read must use the decision's own inputs")
			}
		})
	}
}

func TestFailedWindowHistoryReadFallsBackToClaimRead(t *testing.T) {
	loader := &sequencedHistory{errs: []error{errors.New("dws unavailable")}, history: []HistoryLine{{Role: "菲迪", Content: "晚上几点出发？"}}}
	var allowed atomic.Bool
	allowed.Store(true)
	c, _, prompt := windowCoordinator(t, loader, &allowed)
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	c.PrefetchWindowHistory(windowTurn(cutoff))
	waitCalls(t, loader, 1)
	turn := windowTurn(cutoff)
	turn.Message = "6 点"
	c.Decide(context.Background(), turn)
	if n := loader.count(); n != 2 {
		t.Fatalf("a failed window read must be retried at claim, loads=%d", n)
	}
	if !strings.Contains(*prompt, `"status":"loaded"`) {
		t.Fatalf("the claim-time read must supply history: %q", *prompt)
	}
}

func TestInFlightWindowHistoryReadIsAwaitedNotRepeated(t *testing.T) {
	loader := &sequencedHistory{history: []HistoryLine{{Role: "菲迪", Content: "晚上几点出发？"}}, block: make(chan struct{})}
	var allowed atomic.Bool
	allowed.Store(true)
	c, _, prompt := windowCoordinator(t, loader, &allowed)
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	c.PrefetchWindowHistory(windowTurn(cutoff))
	// Only the early read is unblocked before the decision needs history; a
	// claim-time re-read would block past the decision and fail this test.
	release := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		loader.block <- struct{}{}
		close(release)
	}()
	turn := windowTurn(cutoff)
	turn.Message = "6 点"
	started := time.Now()
	c.Decide(context.Background(), turn)
	<-release
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("a running window read must be awaited, not repeated: took %s", elapsed)
	}
	if !strings.Contains(*prompt, "晚上几点出发") {
		t.Fatalf("awaited window history missing: %q", *prompt)
	}
	close(loader.block)
}

func TestWindowHistoryReadFollowsTheAgentSwitch(t *testing.T) {
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)

	// Off at enqueue: no early read, the claim reads as before.
	loader := &sequencedHistory{history: []HistoryLine{{Role: "菲迪", Content: "晚上几点出发？"}}}
	var allowed atomic.Bool
	c, _, _ := windowCoordinator(t, loader, &allowed)
	if c.PrefetchWindowHistory(windowTurn(cutoff)) {
		t.Fatal("a disabled agent must not read during the window")
	}
	turn := windowTurn(cutoff)
	turn.Message = "6 点"
	c.Decide(context.Background(), turn)
	if n := loader.count(); n != 1 {
		t.Fatalf("disabled agent must read once at claim, loads=%d", n)
	}

	// Switched off between enqueue and claim: the early read is not used.
	loader = &sequencedHistory{history: []HistoryLine{{Role: "菲迪", Content: "晚上几点出发？"}}}
	allowed.Store(true)
	c, _, _ = windowCoordinator(t, loader, &allowed)
	c.PrefetchWindowHistory(windowTurn(cutoff))
	waitCalls(t, loader, 1)
	allowed.Store(false)
	c.Decide(context.Background(), turn)
	if n := loader.count(); n != 2 {
		t.Fatalf("switch-off must restore the claim-time read, loads=%d", n)
	}

	// A Coordinator without the provider or the cache never reads early.
	plain := &Coordinator{DWSHistory: loader}
	if plain.PrefetchWindowHistory(windowTurn(cutoff)) {
		t.Fatal("no provider means no window read")
	}
}

func TestWindowHistoryReadSkipsUnreadableWindows(t *testing.T) {
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	cases := map[string]func(*Turn){
		"no window message":   func(t *Turn) { t.Utterances = nil },
		"no cutoff":           func(t *Turn) { t.HistoryBefore = time.Time{} },
		"no DWS identity":     func(t *Turn) { t.DWSUID = "" },
		"unaddressed group":   func(t *Turn) { t.ProactiveConversation, t.Addressed, t.ChatType = true, false, "group" },
		"history already set": func(t *Turn) { t.DingTalkHistory = []HistoryLine{{Content: "x"}} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			loader := &sequencedHistory{}
			var allowed atomic.Bool
			allowed.Store(true)
			c, _, _ := windowCoordinator(t, loader, &allowed)
			turn := windowTurn(cutoff)
			change(&turn)
			if c.PrefetchWindowHistory(turn) {
				t.Fatal("window read must follow the claim-time prefetch rules")
			}
		})
	}
}

func TestExpiredWindowHistoryReadIsDropped(t *testing.T) {
	loader := &sequencedHistory{history: []HistoryLine{{Role: "菲迪", Content: "晚上几点出发？"}}}
	var allowed atomic.Bool
	allowed.Store(true)
	c, _, _ := windowCoordinator(t, loader, &allowed)
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	c.PrefetchWindowHistory(windowTurn(cutoff))
	waitCalls(t, loader, 1)
	c.windowHistory.mu.Lock()
	reads := make([]*windowHistoryRead, 0, len(c.windowHistory.entries))
	for _, read := range c.windowHistory.entries {
		reads = append(reads, read)
	}
	c.windowHistory.mu.Unlock()
	for _, read := range reads {
		<-read.done
		c.windowHistory.mu.Lock()
		read.started = read.started.Add(-2 * windowHistoryTTL)
		c.windowHistory.mu.Unlock()
	}
	if c.takeWindowHistory(windowTurn(cutoff)) != nil {
		t.Fatal("an expired window read must not be used")
	}
}

// scriptedHistory answers the n-th Load with the n-th history and can hold
// every call until its context ends.
type scriptedHistory struct {
	mu      sync.Mutex
	calls   int
	answers [][]HistoryLine
	hold    bool
}

func (s *scriptedHistory) Load(ctx context.Context, _ Turn) ([]HistoryLine, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	hold := s.hold
	s.mu.Unlock()
	if hold {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if n-1 < len(s.answers) {
		return append([]HistoryLine(nil), s.answers[n-1]...), nil
	}
	return append([]HistoryLine(nil), s.answers[len(s.answers)-1]...), nil
}

func (s *scriptedHistory) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestInFlightWindowHistoryReadStaysWithinTheClaimBudget(t *testing.T) {
	loader := &scriptedHistory{hold: true}
	var allowed atomic.Bool
	allowed.Store(true)
	var calls atomic.Int32
	prompt := new(string)
	c := &Coordinator{LLM: decisionLLM(t, &calls, prompt, false), DWSHistory: loader, windowHistory: newWindowHistoryReads()}
	c.HistoryPrefetchAgentProvider = func(pgtype.UUID) bool { return allowed.Load() }
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	c.PrefetchWindowHistory(windowTurn(cutoff))
	// The early read has run 1 s when the job is claimed; it fails at its
	// own 2.5 s deadline and the fallback must not get a fresh 2.5 s.
	time.Sleep(time.Second)
	turn := windowTurn(cutoff)
	turn.Message = "6 点"
	started := time.Now()
	c.Decide(context.Background(), turn)
	if elapsed := time.Since(started); elapsed > historyPrefetchTimeout+700*time.Millisecond {
		t.Fatalf("history wait %s exceeded the claim-time budget %s", elapsed, historyPrefetchTimeout)
	}
	if !strings.Contains(*prompt, "6 点") {
		t.Fatalf("the decision must still run after a history timeout: %q", *prompt)
	}
}

func TestParkedJobDoesNotReuseAnOldWindowRead(t *testing.T) {
	loader := &scriptedHistory{answers: [][]HistoryLine{
		{{Role: "菲迪", Content: "早读看到的旧上下文"}},
		{{Role: "菲迪", Content: "认领时的新上下文"}},
	}}
	var allowed atomic.Bool
	allowed.Store(true)
	var calls atomic.Int32
	prompt := new(string)
	c := &Coordinator{LLM: decisionLLM(t, &calls, prompt, false), DWSHistory: loader, windowHistory: newWindowHistoryReads()}
	c.HistoryPrefetchAgentProvider = func(pgtype.UUID) bool { return allowed.Load() }
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	c.PrefetchWindowHistory(windowTurn(cutoff))
	c.windowHistory.mu.Lock()
	var read *windowHistoryRead
	for _, r := range c.windowHistory.entries {
		read = r
	}
	c.windowHistory.mu.Unlock()
	<-read.done
	c.windowHistory.mu.Lock()
	read.started = read.started.Add(-windowHistoryReuseAge - time.Second)
	c.windowHistory.mu.Unlock()

	turn := windowTurn(cutoff)
	turn.Message = "6 点"
	c.Decide(context.Background(), turn)
	if n := loader.count(); n != 2 {
		t.Fatalf("an early read older than the reuse age must be read again, loads=%d", n)
	}
	if !strings.Contains(*prompt, "认领时的新上下文") || strings.Contains(*prompt, "早读看到的旧上下文") {
		t.Fatalf("the claim-time history must be used: %q", *prompt)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestReusedWindowReadIsShadowCompared(t *testing.T) {
	logs := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	defer slog.SetDefault(previous)

	loader := &scriptedHistory{answers: [][]HistoryLine{
		{{Role: "菲迪", Content: "早读看到的旧上下文", EvidenceID: "m1"}},
		{{Role: "菲迪", Content: "早读看到的旧上下文", EvidenceID: "m1"}, {Role: "须莫", Content: "迟到可见的消息", EvidenceID: "m0"}},
	}}
	var allowed atomic.Bool
	allowed.Store(true)
	var calls atomic.Int32
	prompt := new(string)
	c := &Coordinator{LLM: decisionLLM(t, &calls, prompt, false), DWSHistory: loader, windowHistory: newWindowHistoryReads()}
	c.HistoryPrefetchAgentProvider = func(pgtype.UUID) bool { return allowed.Load() }
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	c.PrefetchWindowHistory(windowTurn(cutoff))
	deadline := time.Now().Add(2 * time.Second)
	for loader.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	turn := windowTurn(cutoff)
	turn.Message = "6 点"
	c.Decide(context.Background(), turn)
	for !strings.Contains(logs.String(), "inbound_coordinator_window_history_shadow") && time.Now().Before(deadline.Add(2*time.Second)) {
		time.Sleep(5 * time.Millisecond)
	}
	if out := logs.String(); !strings.Contains(out, "outcome=mismatch") || !strings.Contains(out, "early_count=1") || !strings.Contains(out, "claim_count=2") {
		t.Fatalf("a claim-time read that differs must be reported: %s", out)
	}
	if compareHistory([]HistoryLine{{EvidenceID: "a", Content: "x"}}, []HistoryLine{{EvidenceID: "a", Content: "x"}}) != "match" {
		t.Fatal("identical reads must match")
	}
}

func TestWindowHistoryReadsAreBoundedPerReplica(t *testing.T) {
	loader := &scriptedHistory{hold: true}
	var allowed atomic.Bool
	allowed.Store(true)
	c, _, _ := windowCoordinator(t, loader, &allowed)
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	for i := 0; i < windowHistoryMaxActive; i++ {
		turn := windowTurn(cutoff)
		turn.ConversationID = "cid-" + string(rune('a'+i))
		if !c.PrefetchWindowHistory(turn) {
			t.Fatalf("read %d must start below the active limit", i)
		}
	}
	extra := windowTurn(cutoff)
	extra.ConversationID = "cid-over-limit"
	if c.PrefetchWindowHistory(extra) {
		t.Fatal("a replica at its active-read limit must leave the read to claim time")
	}
}

func TestWindowHistoryReadSkipsOwnerSwitchOff(t *testing.T) {
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	for _, on := range []bool{false, true} {
		loader := &sequencedHistory{history: []HistoryLine{{Role: "菲迪", Content: "晚上几点出发？"}}}
		var allowed atomic.Bool
		allowed.Store(true)
		c, _, _ := windowCoordinator(t, loader, &allowed)
		c.Queries = &coordQueriesStub{inbound: on}
		if got := c.PrefetchWindowHistory(windowTurn(cutoff)); got != on {
			t.Fatalf("owner switch on=%v: early read started=%v", on, got)
		}
		if !on {
			time.Sleep(50 * time.Millisecond)
			if n := loader.count(); n != 0 {
				t.Fatalf("switched-off Coordinator read history %d times", n)
			}
		}
	}
}
