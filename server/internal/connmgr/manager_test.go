package connmgr

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// memCoordinator is an in-memory Coordinator with the Redis coordinator's
// capacity and fencing rules (no TTL expiry). Knobs let tests inject renewal
// failures, simulate READY members on other replicas, and observe calls.
type memCoordinator struct {
	mu      sync.Mutex
	target  int
	ttl     time.Duration
	members map[string]map[string]State // group -> token -> state

	// extraReady simulates READY members held by other replicas in every
	// group; it counts toward capacity and toward Snapshot.Ready.
	extraReady int

	claims       int
	claimGroups  []string
	markReadys   int
	beginDrains  int
	releases     int
	events       []string
	claimedAt    []time.Time
	releasedAt   []time.Time
	markReadyErr error
	// markReadyFailures limits markReadyErr to the first N calls (0 = always).
	markReadyFailures int

	renewErr      error
	renewBlock    chan struct{}
	renewCtxErr   error
	drainObserved chan struct{}
}

func newMemCoordinator(target int, ttl time.Duration) *memCoordinator {
	return &memCoordinator{target: target, ttl: ttl, members: make(map[string]map[string]State)}
}

func (f *memCoordinator) snapshotLocked(group string, state State) Snapshot {
	s := Snapshot{TargetReady: f.target, State: state}
	for _, st := range f.members[group] {
		s.Total++
		switch st {
		case StateConnecting:
			s.Connecting++
		case StateReady:
			s.Ready++
		case StateDraining:
			s.Draining++
		}
	}
	s.Total += f.extraReady
	s.Ready += f.extraReady
	now := time.Now()
	s.ServerNow = now
	s.ExpiresAt = now.Add(f.ttl)
	return s
}

func (f *memCoordinator) Claim(_ context.Context, group, token string) (Lease, Snapshot, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	f.claimGroups = append(f.claimGroups, group)
	members := f.members[group]
	if members == nil {
		members = make(map[string]State)
		f.members[group] = members
	}
	if current, ok := members[token]; ok {
		if current != StateConnecting {
			return Lease{}, f.snapshotLocked(group, current), false, &CoordinatorError{Op: "claim", Group: group, Actual: current, Kind: ErrLeaseStateMismatch}
		}
		return Lease{Group: group, Token: token, State: current}, f.snapshotLocked(group, current), true, nil
	}
	s := f.snapshotLocked(group, "")
	nonDraining := s.Connecting + s.Ready
	if s.Draining == 0 {
		if nonDraining >= f.target || s.Total >= f.target {
			return Lease{}, s, false, nil
		}
	} else if nonDraining >= f.target || s.Total >= f.target+1 {
		return Lease{}, s, false, nil
	}
	members[token] = StateConnecting
	f.events = append(f.events, "claim:"+group+":"+token)
	f.claimedAt = append(f.claimedAt, time.Now())
	return Lease{Group: group, Token: token, State: StateConnecting}, f.snapshotLocked(group, StateConnecting), true, nil
}

func (f *memCoordinator) MarkReady(_ context.Context, lease Lease) (Lease, Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markReadys++
	if f.markReadyErr != nil && (f.markReadyFailures == 0 || f.markReadys <= f.markReadyFailures) {
		return Lease{}, Snapshot{}, f.markReadyErr
	}
	current, ok := f.members[lease.Group][lease.Token]
	if !ok {
		return Lease{}, f.snapshotLocked(lease.Group, ""), &CoordinatorError{Op: "mark_ready", Group: lease.Group, Kind: ErrLeaseLost}
	}
	if current != StateConnecting && current != StateReady {
		return Lease{}, f.snapshotLocked(lease.Group, current), &CoordinatorError{Op: "mark_ready", Group: lease.Group, Actual: current, Kind: ErrLeaseStateMismatch}
	}
	f.members[lease.Group][lease.Token] = StateReady
	lease.State = StateReady
	return lease, f.snapshotLocked(lease.Group, StateReady), nil
}

func (f *memCoordinator) Renew(ctx context.Context, lease Lease) (Snapshot, error) {
	f.mu.Lock()
	block := f.renewBlock
	renewErr := f.renewErr
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			f.mu.Lock()
			f.renewCtxErr = ctx.Err()
			f.mu.Unlock()
			return Snapshot{}, ctx.Err()
		}
	}
	if renewErr != nil {
		return Snapshot{}, renewErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.members[lease.Group][lease.Token]
	if !ok {
		return f.snapshotLocked(lease.Group, ""), &CoordinatorError{Op: "renew", Group: lease.Group, Kind: ErrLeaseLost}
	}
	if current != lease.State {
		return f.snapshotLocked(lease.Group, current), &CoordinatorError{Op: "renew", Group: lease.Group, Expected: lease.State, Actual: current, Kind: ErrLeaseStateMismatch}
	}
	return f.snapshotLocked(lease.Group, current), nil
}

func (f *memCoordinator) BeginDrain(_ context.Context, lease Lease) (Lease, Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beginDrains++
	current, ok := f.members[lease.Group][lease.Token]
	if !ok {
		return Lease{}, f.snapshotLocked(lease.Group, ""), &CoordinatorError{Op: "begin_drain", Group: lease.Group, Kind: ErrLeaseLost}
	}
	if current == StateReady {
		for token, st := range f.members[lease.Group] {
			if token != lease.Token && st == StateDraining {
				return Lease{}, f.snapshotLocked(lease.Group, current), &CoordinatorError{Op: "begin_drain", Group: lease.Group, Kind: ErrDrainInProgress}
			}
		}
		f.members[lease.Group][lease.Token] = StateDraining
	} else if current != StateDraining {
		return Lease{}, f.snapshotLocked(lease.Group, current), &CoordinatorError{Op: "begin_drain", Group: lease.Group, Expected: StateReady, Actual: current, Kind: ErrLeaseStateMismatch}
	}
	if f.drainObserved != nil && f.beginDrains == 1 {
		close(f.drainObserved)
	}
	lease.State = StateDraining
	return lease, f.snapshotLocked(lease.Group, StateDraining), nil
}

func (f *memCoordinator) Release(_ context.Context, lease Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	f.events = append(f.events, "release:"+lease.Group+":"+lease.Token)
	f.releasedAt = append(f.releasedAt, time.Now())
	delete(f.members[lease.Group], lease.Token)
	return nil
}

func (f *memCoordinator) TargetReady() int        { return f.target }
func (f *memCoordinator) LeaseTTL() time.Duration { return f.ttl }

func (f *memCoordinator) with(fn func(f *memCoordinator)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *memCoordinator) memberCount(group string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.members[group])
}

// runRecord is one Conn.Run observed by the harness.
type runRecord struct {
	replica     string
	key         string
	fingerprint string
	dialedAt    time.Time
	// readyCalledAt is when the link was up and Run called ready; readyAt is
	// when ready returned successfully (member READY).
	readyCalledAt time.Time
	readyAt       time.Time
	exitAt        time.Time
	readyErr      error
	exitErr       error
	exited        bool
}

// connHarness builds fake Conns and records their lifecycle across replicas.
type connHarness struct {
	mu        sync.Mutex
	runs      []*runRecord
	active    int // Runs with an established link (ready called) that have not exited
	maxActive int
	dialCalls int

	// dialErrs fails the first N Dial calls.
	dialErrs int
	// behavior, when set, decides what a READY run does; nil blocks until ctx ends.
	behavior func(dialIndex int, target Target) func(ctx context.Context) error
	// ignoreReadyErr makes Runs keep going after ready fails (a misbehaving Conn).
	ignoreReadyErr bool
}

func (h *connHarness) dial(replica string) func(Target) (Conn, error) {
	return func(target Target) (Conn, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.dialCalls++
		if h.dialCalls <= h.dialErrs {
			return nil, errors.New("dial refused")
		}
		index := len(h.runs)
		rec := &runRecord{replica: replica, key: target.Key, fingerprint: target.Fingerprint, dialedAt: time.Now()}
		h.runs = append(h.runs, rec)
		var behavior func(context.Context) error
		if h.behavior != nil {
			behavior = h.behavior(index, target)
		}
		return &fakeConn{h: h, rec: rec, behavior: behavior}, nil
	}
}

func (h *connHarness) snapshot() []runRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]runRecord, len(h.runs))
	for i, rec := range h.runs {
		out[i] = *rec
	}
	return out
}

func (h *connHarness) activeNow() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active, h.maxActive
}

type fakeConn struct {
	h        *connHarness
	rec      *runRecord
	behavior func(context.Context) error
}

func (c *fakeConn) Run(ctx context.Context, ready func(context.Context) error) error {
	h := c.h
	// The link is established: from here on the connection receives (frames
	// buffer until ready returns), so it counts as active.
	h.mu.Lock()
	c.rec.readyCalledAt = time.Now()
	h.active++
	if h.active > h.maxActive {
		h.maxActive = h.active
	}
	h.mu.Unlock()

	readyErr := ready(ctx)
	h.mu.Lock()
	c.rec.readyErr = readyErr
	ignore := h.ignoreReadyErr
	if readyErr == nil {
		c.rec.readyAt = time.Now()
	}
	h.mu.Unlock()

	var err error
	switch {
	case readyErr != nil && !ignore:
		err = readyErr
	case readyErr == nil && c.behavior != nil:
		err = c.behavior(ctx)
	default:
		<-ctx.Done()
	}

	h.mu.Lock()
	h.active--
	c.rec.exitAt = time.Now()
	c.rec.exitErr = err
	c.rec.exited = true
	h.mu.Unlock()
	return err
}

// targetList is a mutable List source.
type targetList struct {
	mu      sync.Mutex
	targets []Target
	err     error
	calls   int
}

func newTargetList(targets ...Target) *targetList { return &targetList{targets: targets} }

func (l *targetList) List(context.Context) ([]Target, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.err != nil {
		return nil, l.err
	}
	return append([]Target(nil), l.targets...), nil
}

func (l *targetList) set(targets ...Target) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.targets = targets
}

func (l *targetList) setErr(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.err = err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

func fastConfig(coord Coordinator, list *targetList, h *connHarness) Config {
	return Config{
		Name:              "test_conn",
		List:              list.List,
		Dial:              h.dial("A"),
		Coordinator:       coord,
		PollInterval:      10 * time.Millisecond,
		RenewInterval:     5 * time.Millisecond,
		DrainTimeout:      500 * time.Millisecond,
		ShutdownTimeout:   2 * time.Second,
		MinBackoff:        5 * time.Millisecond,
		MaxBackoff:        50 * time.Millisecond,
		ResetBackoffAfter: time.Second,
		ClaimDelayStep:    time.Millisecond,
		ClaimDelayMax:     5 * time.Millisecond,
		Logger:            discardLogger(),
	}
}

func startManager(t *testing.T, cfg Config) (*Manager, context.CancelFunc) {
	t.Helper()
	m, err := New(cfg)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go m.Run(ctx)
	t.Cleanup(func() {
		cancel()
		if !m.WaitWithTimeout(5 * time.Second) {
			t.Errorf("manager %s did not stop", m.NodeID())
		}
	})
	return m, cancel
}

// ---------------------------------------------------------------------------
// Coordinated lifecycle (ported from the robot supervisor tests)
// ---------------------------------------------------------------------------

func TestManagerMarksReadyAndHandsOff(t *testing.T) {
	coord := newMemCoordinator(2, 100*time.Millisecond)
	coord.drainObserved = make(chan struct{})
	list := newTargetList(Target{Key: "dws:user-1", Fingerprint: "fp", Value: "secret-value"})
	h := &connHarness{}
	m, cancel := startManager(t, fastConfig(coord, list, h))

	if !waitFor(300*time.Millisecond, func() bool { return m.Held() == 1 }) {
		t.Fatal("connection did not become READY")
	}

	m.BeginShutdown()
	select {
	case <-coord.drainObserved:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("connection never entered DRAINING")
	}
	if !waitFor(100*time.Millisecond, func() bool { return m.Held() == 0 }) {
		t.Fatalf("Held() = %d while DRAINING, want 0", m.Held())
	}
	// Without replacement READY capacity the DRAINING connection keeps
	// consuming across several renewals.
	time.Sleep(40 * time.Millisecond)
	if active, _ := h.activeNow(); active != 1 {
		t.Fatalf("DRAINING connection stopped before a replacement was READY (active=%d)", active)
	}
	coord.with(func(f *memCoordinator) { f.extraReady = 2 }) // replacements are READY elsewhere
	if !m.WaitForHandoffs(500 * time.Millisecond) {
		t.Fatal("graceful handoff did not finish after replacement became READY")
	}
	cancel()
	if !m.WaitWithTimeout(time.Second) {
		t.Fatal("manager did not stop")
	}
	coord.mu.Lock()
	defer coord.mu.Unlock()
	if len(coord.claimGroups) == 0 || coord.claimGroups[0] != "dws:user-1" {
		t.Fatalf("claim groups = %v, want the target key", coord.claimGroups)
	}
	if coord.markReadys != 1 || coord.beginDrains != 1 || coord.releases != 1 {
		t.Fatalf("coordination transitions mark=%d drain=%d release=%d", coord.markReadys, coord.beginDrains, coord.releases)
	}
}

func TestManagerRenewFailureFailsClosedAtLocalDeadline(t *testing.T) {
	coord := newMemCoordinator(1, 30*time.Millisecond)
	coord.renewErr = errors.New("redis unavailable")
	list := newTargetList(Target{Key: "k", Fingerprint: "fp"})
	h := &connHarness{}
	startManager(t, fastConfig(coord, list, h))

	if !waitFor(500*time.Millisecond, func() bool {
		coord.mu.Lock()
		defer coord.mu.Unlock()
		return coord.releases >= 1
	}) {
		t.Fatal("connection was not closed and released after the lease deadline")
	}
	runs := h.snapshot()
	if len(runs) == 0 || !runs[0].exited {
		t.Fatal("first connection still running after fail-closed release")
	}
	if lived := runs[0].exitAt.Sub(runs[0].readyAt); lived > 150*time.Millisecond {
		t.Fatalf("connection lived %s past READY without renewal, want about one TTL", lived)
	}
}

func TestManagerBlockedRenewCannotCrossLeaseDeadline(t *testing.T) {
	coord := newMemCoordinator(1, 40*time.Millisecond)
	coord.renewBlock = make(chan struct{})
	list := newTargetList(Target{Key: "k", Fingerprint: "fp"})
	h := &connHarness{}
	started := time.Now()
	startManager(t, fastConfig(coord, list, h))

	if !waitFor(300*time.Millisecond, func() bool {
		coord.mu.Lock()
		defer coord.mu.Unlock()
		return coord.releases >= 1
	}) {
		t.Fatal("blocked Renew kept the connection past its lease deadline")
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("fail-closed elapsed %s, want <= 200ms", elapsed)
	}
	coord.mu.Lock()
	renewCtxErr := coord.renewCtxErr
	coord.mu.Unlock()
	if renewCtxErr == nil {
		t.Fatal("blocked Renew did not receive a lease-bounded context cancellation")
	}
}

func TestManagerWaitForHandoffsTimesOutWithoutReplacement(t *testing.T) {
	coord := newMemCoordinator(1, 100*time.Millisecond)
	list := newTargetList(Target{Key: "k", Fingerprint: "fp"})
	h := &connHarness{}
	m, _ := startManager(t, fastConfig(coord, list, h))

	if !waitFor(300*time.Millisecond, func() bool { return m.Held() == 1 }) {
		t.Fatal("connection did not become READY")
	}
	m.BeginShutdown()
	m.BeginShutdown() // idempotent
	if m.WaitForHandoffs(60 * time.Millisecond) {
		t.Fatal("WaitForHandoffs reported success while no replacement was READY")
	}
	if active, _ := h.activeNow(); active != 1 {
		t.Fatalf("DRAINING connection was dropped before handoff (active=%d)", active)
	}
	coord.with(func(f *memCoordinator) { f.extraReady = 1 })
	if !m.WaitForHandoffs(500 * time.Millisecond) {
		t.Fatal("handoff did not finish after a replacement became READY")
	}
}

func TestManagerWaitForHandoffsWithNothingHeld(t *testing.T) {
	coord := newMemCoordinator(1, 100*time.Millisecond)
	coord.extraReady = 1 // another replica holds the only slot
	list := newTargetList(Target{Key: "k", Fingerprint: "fp"})
	h := &connHarness{}
	m, _ := startManager(t, fastConfig(coord, list, h))

	if !waitFor(300*time.Millisecond, func() bool {
		coord.mu.Lock()
		defer coord.mu.Unlock()
		return coord.claims >= 2
	}) {
		t.Fatal("contender never tried to claim")
	}
	m.BeginShutdown()
	if !m.WaitForHandoffs(100 * time.Millisecond) {
		t.Fatal("a contender holding nothing must stop immediately on BeginShutdown")
	}
	if runs := h.snapshot(); len(runs) != 0 {
		t.Fatalf("dialed %d connections without capacity", len(runs))
	}
}

func TestManagerShutdownBeforeReadyStopsImmediately(t *testing.T) {
	coord := newMemCoordinator(1, 100*time.Millisecond)
	coord.markReadyErr = errors.New("redis down") // never becomes READY
	list := newTargetList(Target{Key: "k", Fingerprint: "fp"})
	h := &connHarness{behavior: nil}
	m, _ := startManager(t, fastConfig(coord, list, h))

	if !waitFor(300*time.Millisecond, func() bool { return len(h.snapshot()) >= 1 }) {
		t.Fatal("never dialed")
	}
	m.BeginShutdown()
	if !m.WaitForHandoffs(200 * time.Millisecond) {
		t.Fatal("a member that never became READY must not wait for a handoff")
	}
	if coord.memberCount("k") != 0 {
		t.Fatal("member not released after shutdown before READY")
	}
}

func TestTargetUnmetLimiterIsDelayedAndRateLimited(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	snapshot := Snapshot{TargetReady: 2, Ready: 1, Total: 1}
	var limiter targetUnmetLimiter

	if limiter.shouldLog(base, snapshot) {
		t.Fatal("first unmet observation must start the grace window, not log")
	}
	if limiter.shouldLog(base.Add(targetUnmetLogInterval-time.Millisecond), snapshot) {
		t.Fatal("unmet target logged before grace interval")
	}
	if !limiter.shouldLog(base.Add(targetUnmetLogInterval), snapshot) {
		t.Fatal("persistent unmet target did not log after grace interval")
	}
	if limiter.shouldLog(base.Add(targetUnmetLogInterval+time.Second), snapshot) {
		t.Fatal("persistent unmet target log was not rate limited")
	}
	if !limiter.shouldLog(base.Add(2*targetUnmetLogInterval), snapshot) {
		t.Fatal("persistent unmet target did not log again after rate-limit interval")
	}

	met := snapshot
	met.Ready = met.TargetReady
	if limiter.shouldLog(base.Add(2*targetUnmetLogInterval+time.Second), met) {
		t.Fatal("met target must not log")
	}
	if limiter.shouldLog(base.Add(3*targetUnmetLogInterval), snapshot) {
		t.Fatal("a later degradation must start a fresh grace window")
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func TestNewValidatesConfigAndAppliesDefaults(t *testing.T) {
	coord := newMemCoordinator(1, DefaultLeaseTTL)
	list := newTargetList()
	dial := (&connHarness{}).dial("A")
	valid := Config{Name: "dws_events", List: list.List, Dial: dial, Coordinator: coord}

	m, err := New(valid)
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	cfg := m.cfg
	if cfg.PollInterval != defaultPollInterval || cfg.RenewInterval != defaultRenewInterval ||
		cfg.DrainTimeout != defaultDrainTimeout || cfg.ShutdownTimeout != defaultShutdownTimeout ||
		cfg.MinBackoff != defaultMinBackoff || cfg.MaxBackoff != defaultMaxBackoff ||
		cfg.ResetBackoffAfter != defaultResetBackoffAfter ||
		cfg.ClaimDelayStep != defaultClaimDelayStep || cfg.ClaimDelayMax != defaultClaimDelayMax {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.Now == nil || cfg.Logger == nil {
		t.Fatal("Now and Logger must be defaulted")
	}
	if m.NodeID() == "" || m.DrainTimeout() != defaultDrainTimeout || m.ShutdownTimeout() != defaultShutdownTimeout {
		t.Fatalf("accessors: node=%q drain=%s shutdown=%s", m.NodeID(), m.DrainTimeout(), m.ShutdownTimeout())
	}

	cases := map[string]func(c *Config){
		"no name":             func(c *Config) { c.Name = " " },
		"no list":             func(c *Config) { c.List = nil },
		"no dial":             func(c *Config) { c.Dial = nil },
		"no coordinator":      func(c *Config) { c.Coordinator = nil },
		"negative poll":       func(c *Config) { c.PollInterval = -time.Second },
		"negative claim step": func(c *Config) { c.ClaimDelayStep = -time.Millisecond },
		"renew near ttl":      func(c *Config) { c.RenewInterval = DefaultLeaseTTL / 2 },
		"backoff inverted":    func(c *Config) { c.MinBackoff = time.Minute; c.MaxBackoff = time.Second },
		"zero target":         func(c *Config) { c.Coordinator = newMemCoordinator(0, DefaultLeaseTTL) },
		"zero ttl":            func(c *Config) { c.Coordinator = newMemCoordinator(1, 0) },
	}
	for name, mutate := range cases {
		cfg := valid
		mutate(&cfg)
		if _, err := New(cfg); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: error = %v, want ErrConfig", name, err)
		}
	}
}

func TestClaimDelayScalesWithHeldMembersAndCaps(t *testing.T) {
	m := &Manager{cfg: Config{ClaimDelayStep: 250 * time.Millisecond, ClaimDelayMax: 5 * time.Second}}
	if got := m.claimDelay(); got != 0 {
		t.Fatalf("empty replica delay = %s, want 0", got)
	}
	m.connecting.Store(1)
	m.ready.Store(2)
	if got := m.claimDelay(); got != 750*time.Millisecond {
		t.Fatalf("3 held delay = %s, want 750ms", got)
	}
	m.ready.Store(100)
	if got := m.claimDelay(); got != 5*time.Second {
		t.Fatalf("101 held delay = %s, want cap 5s", got)
	}
}

func TestWaitClaimTurnReevaluatesHeldDuringWait(t *testing.T) {
	m := &Manager{cfg: Config{ClaimDelayStep: 20 * time.Millisecond, ClaimDelayMax: time.Second}}
	m.ready.Store(1) // first wait is at least 20ms
	done := make(chan time.Duration, 1)
	started := time.Now()
	go func() {
		m.waitClaimTurn(context.Background(), make(chan struct{}))
		done <- time.Since(started)
	}()
	time.Sleep(5 * time.Millisecond)
	m.ready.Store(4) // a sibling won meanwhile: the turn moves to >= 80ms
	if elapsed := <-done; elapsed < 75*time.Millisecond {
		t.Fatalf("claim turn after %s ignored members won during the wait", elapsed)
	}

	drain := make(chan struct{})
	close(drain)
	m.ready.Store(100)
	if !m.waitClaimTurn(context.Background(), drain) {
		t.Fatal("a drained contender must stop waiting for its claim turn")
	}
}

// ---------------------------------------------------------------------------
// Desired-set reconciliation and reconnects
// ---------------------------------------------------------------------------

func TestManagerKickSweepsImmediately(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	list := newTargetList()
	h := &connHarness{}
	cfg := fastConfig(coord, list, h)
	cfg.PollInterval = time.Hour // only the boot sweep and explicit kicks
	m, _ := startManager(t, cfg)

	if !waitFor(200*time.Millisecond, func() bool {
		list.mu.Lock()
		defer list.mu.Unlock()
		return list.calls >= 1
	}) {
		t.Fatal("boot sweep never listed")
	}
	list.set(Target{Key: "k", Fingerprint: "fp"})
	m.Kick()
	if !waitFor(500*time.Millisecond, func() bool { return m.Held() == 1 }) {
		t.Fatal("kick did not trigger an immediate sweep")
	}
	// Kick with no pending work must be harmless and never block.
	m.Kick()
	m.Kick()
	m.Kick()
}

func TestManagerFingerprintChangeRestartsAndRemovalStops(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	list := newTargetList(Target{Key: "k", Fingerprint: "fp-one"})
	h := &connHarness{}
	m, _ := startManager(t, fastConfig(coord, list, h))

	if !waitFor(300*time.Millisecond, func() bool { return m.Held() == 1 }) {
		t.Fatal("connection did not become READY")
	}
	// An unchanged row across many sweeps never restarts.
	time.Sleep(60 * time.Millisecond)
	if runs := h.snapshot(); len(runs) != 1 {
		t.Fatalf("unchanged target dialed %d times, want 1", len(runs))
	}

	list.set(Target{Key: "k", Fingerprint: "fp-two"})
	if !waitFor(500*time.Millisecond, func() bool {
		runs := h.snapshot()
		return len(runs) == 2 && !runs[1].readyAt.IsZero() && m.Held() == 1
	}) {
		t.Fatalf("fingerprint change did not restart the connection: %+v", h.snapshot())
	}
	runs := h.snapshot()
	if !runs[0].exited || runs[0].fingerprint != "fp-one" || runs[1].fingerprint != "fp-two" {
		t.Fatalf("restart records = %+v", runs)
	}
	coord.mu.Lock()
	var claimTokens []string
	for _, ev := range coord.events {
		if strings.HasPrefix(ev, "claim:") {
			claimTokens = append(claimTokens, ev)
		}
	}
	members := len(coord.members["k"])
	coord.mu.Unlock()
	if len(claimTokens) != 2 || claimTokens[0] == claimTokens[1] {
		t.Fatalf("restart must use a fresh lease token: %v", claimTokens)
	}
	if members != 1 {
		t.Fatalf("members after restart = %d, want the successor only", members)
	}

	list.set()
	if !waitFor(500*time.Millisecond, func() bool {
		runs := h.snapshot()
		return runs[1].exited && m.Held() == 0 && coord.memberCount("k") == 0
	}) {
		t.Fatal("removed target was not stopped and released")
	}
	coord.mu.Lock()
	claimsAfterRemoval := coord.claims
	coord.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	coord.mu.Lock()
	defer coord.mu.Unlock()
	if coord.claims != claimsAfterRemoval {
		t.Fatalf("removed target is still contending: claims %d -> %d", claimsAfterRemoval, coord.claims)
	}
}

func TestManagerDroppedLinkReleasesAndReclaimsWithBackoff(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	list := newTargetList(Target{Key: "k", Fingerprint: "fp"})
	h := &connHarness{behavior: func(index int, _ Target) func(context.Context) error {
		if index > 0 {
			return nil
		}
		return func(ctx context.Context) error {
			select {
			case <-time.After(20 * time.Millisecond):
				return errors.New("link dropped")
			case <-ctx.Done():
				return nil
			}
		}
	}}
	cfg := fastConfig(coord, list, h)
	cfg.MinBackoff = 40 * time.Millisecond
	cfg.MaxBackoff = 200 * time.Millisecond
	m, _ := startManager(t, cfg)

	if !waitFor(time.Second, func() bool {
		runs := h.snapshot()
		return len(runs) == 2 && !runs[1].readyAt.IsZero()
	}) {
		t.Fatalf("dropped link was not reconnected: %+v", h.snapshot())
	}
	runs := h.snapshot()
	if runs[0].exitErr == nil || !strings.Contains(runs[0].exitErr.Error(), "link dropped") {
		t.Fatalf("first run exit error = %v", runs[0].exitErr)
	}
	coord.mu.Lock()
	events := append([]string(nil), coord.events...)
	releasedAt := append([]time.Time(nil), coord.releasedAt...)
	claimedAt := append([]time.Time(nil), coord.claimedAt...)
	coord.mu.Unlock()
	if len(events) < 3 || !strings.HasPrefix(events[0], "claim:") || !strings.HasPrefix(events[1], "release:") || !strings.HasPrefix(events[2], "claim:") {
		t.Fatalf("events = %v, want claim, release, claim", events)
	}
	// jitter(40ms) is at least 20ms.
	if gap := claimedAt[1].Sub(releasedAt[0]); gap < 20*time.Millisecond {
		t.Fatalf("reclaimed %s after release, want backoff >= 20ms", gap)
	}
	if m.Held() != 1 {
		t.Fatalf("Held() = %d after reconnect, want 1", m.Held())
	}
}

func TestManagerDialErrorBacksOff(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	list := newTargetList(Target{Key: "k", Fingerprint: "fp"})
	h := &connHarness{dialErrs: 2}
	cfg := fastConfig(coord, list, h)
	cfg.MinBackoff = 20 * time.Millisecond
	cfg.MaxBackoff = 100 * time.Millisecond
	m, _ := startManager(t, cfg)

	if !waitFor(time.Second, func() bool { return m.Held() == 1 }) {
		t.Fatal("never connected after transient dial errors")
	}
	coord.mu.Lock()
	defer coord.mu.Unlock()
	if coord.releases != 2 {
		t.Fatalf("releases = %d, want one per failed dial", coord.releases)
	}
	// jitter(20ms) >= 10ms, then jitter(40ms) >= 20ms.
	if gap := coord.claimedAt[1].Sub(coord.releasedAt[0]); gap < 10*time.Millisecond {
		t.Fatalf("first dial retry after %s, want backoff", gap)
	}
	if gap := coord.claimedAt[2].Sub(coord.releasedAt[1]); gap < 20*time.Millisecond {
		t.Fatalf("second dial retry after %s, want doubled backoff", gap)
	}
}

func TestManagerListErrorKeepsConnections(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	list := newTargetList(Target{Key: "k", Fingerprint: "fp"})
	h := &connHarness{}
	m, _ := startManager(t, fastConfig(coord, list, h))

	if !waitFor(300*time.Millisecond, func() bool { return m.Held() == 1 }) {
		t.Fatal("connection did not become READY")
	}
	list.setErr(errors.New("store unavailable"))
	callsBefore := func() int { list.mu.Lock(); defer list.mu.Unlock(); return list.calls }()
	time.Sleep(100 * time.Millisecond) // ~10 failing sweeps
	if calls := func() int { list.mu.Lock(); defer list.mu.Unlock(); return list.calls }(); calls <= callsBefore {
		t.Fatal("sweeps did not run while List failed")
	}
	runs := h.snapshot()
	if len(runs) != 1 || runs[0].exited || m.Held() != 1 {
		t.Fatalf("List error disturbed the connection: runs=%+v held=%d", runs, m.Held())
	}
	coord.mu.Lock()
	releases := coord.releases
	coord.mu.Unlock()
	if releases != 0 {
		t.Fatalf("List error released %d members", releases)
	}

	list.setErr(nil)
	time.Sleep(40 * time.Millisecond)
	if runs := h.snapshot(); len(runs) != 1 || runs[0].exited {
		t.Fatalf("recovery from List error restarted the connection: %+v", runs)
	}
}

func TestManagerReadyFailureEndsAttemptEvenIfConnIgnoresIt(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	coord.markReadyErr = errors.New("redis blip")
	coord.markReadyFailures = 1
	list := newTargetList(Target{Key: "k", Fingerprint: "fp"})
	h := &connHarness{ignoreReadyErr: true}
	m, _ := startManager(t, fastConfig(coord, list, h))

	if !waitFor(time.Second, func() bool { return m.Held() == 1 }) {
		t.Fatal("never became READY after the transient MarkReady failure")
	}
	runs := h.snapshot()
	if len(runs) != 2 || runs[0].readyErr == nil || !runs[0].exited {
		t.Fatalf("a Conn that ignored the ready error kept running: %+v", runs)
	}
}

func TestManagerSkipsInvalidAndDuplicateTargets(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	list := newTargetList(
		Target{Key: "", Fingerprint: "a"},
		Target{Key: "bad{tag}", Fingerprint: "b"},
		Target{Key: strings.Repeat("x", maxGroupLen+1), Fingerprint: "c"},
		Target{Key: "ok", Fingerprint: "first"},
		Target{Key: "ok", Fingerprint: "second"},
	)
	h := &connHarness{}
	m, _ := startManager(t, fastConfig(coord, list, h))

	if !waitFor(300*time.Millisecond, func() bool { return m.Held() == 1 }) {
		t.Fatal("valid target did not connect")
	}
	time.Sleep(50 * time.Millisecond) // several sweeps
	runs := h.snapshot()
	if len(runs) != 1 || runs[0].key != "ok" || runs[0].fingerprint != "first" {
		t.Fatalf("runs = %+v, want only the first valid target", runs)
	}
}

func TestManagerRunTwiceIsRejected(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	list := newTargetList()
	h := &connHarness{}
	m, _ := startManager(t, fastConfig(coord, list, h))
	// Make sure the first Run owns the manager before the second one starts.
	if !waitFor(time.Second, func() bool {
		list.mu.Lock()
		defer list.mu.Unlock()
		return list.calls >= 1
	}) {
		t.Fatal("first Run never swept")
	}
	returned := make(chan struct{})
	go func() {
		m.Run(context.Background())
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("second Run did not return immediately")
	}
}
