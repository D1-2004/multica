package connmgr

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// memPeers is an in-memory Peers; tests seed other replicas' loads by hand.
type memPeers struct {
	mu         sync.Mutex
	nodes      map[string]int
	heartbeats int
	leaves     []string
}

func newMemPeers() *memPeers { return &memPeers{nodes: make(map[string]int)} }

func (p *memPeers) Heartbeat(_ context.Context, node string, held int, _ time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes[node] = held
	p.heartbeats++
	return nil
}

func (p *memPeers) Alive(context.Context) ([]PeerLoad, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PeerLoad, 0, len(p.nodes))
	for node, held := range p.nodes {
		out = append(out, PeerLoad{Node: node, Held: held})
	}
	return out, nil
}

func (p *memPeers) Leave(_ context.Context, node string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.nodes, node)
	p.leaves = append(p.leaves, node)
	return nil
}

func (p *memPeers) set(node string, held int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes[node] = held
}

// syncBuffer collects log output from concurrent goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) count(substr string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), substr)
}

func captureLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewJSONHandler(buf, nil)), buf
}

func numberedTargets(prefix string, n int) []Target {
	out := make([]Target, n)
	for i := range out {
		out[i] = Target{Key: fmt.Sprintf("%s-%d", prefix, i), Fingerprint: "fp"}
	}
	return out
}

// drainingGroups lists the groups where this coordinator holds a DRAINING
// member.
func (f *memCoordinator) drainingGroups() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for group, members := range f.members {
		for _, st := range members {
			if st == StateDraining {
				out = append(out, group)
			}
		}
	}
	return out
}

func (f *memCoordinator) drains() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.beginDrains
}

func runsForKey(runs []runRecord, key string) []runRecord {
	var out []runRecord
	for _, r := range runs {
		if r.key == key {
			out = append(out, r)
		}
	}
	return out
}

func TestManagerShedsDownToFairShareAndNeverWinsBack(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	peers := newMemPeers()
	peers.set("peer-x", 0) // another live replica holding nothing
	list := newTargetList(numberedTargets("k", 4)...)
	h := &connHarness{}
	cfg := fastConfig(coord, list, h)
	cfg.Peers = peers
	m, _ := startManager(t, cfg)

	// fair = ceil(4/2) = 2. The first shed starts once this replica holds more.
	if !waitFor(time.Second, func() bool { return coord.drains() == 1 }) {
		t.Fatalf("no shed started; held=%d", m.Held())
	}
	first := coord.drainingGroups()
	if len(first) != 1 {
		t.Fatalf("draining groups = %v, want exactly one shed", first)
	}
	shed := first[0]
	// Until someone takes the key over, the DRAINING connection keeps
	// consuming and no second shed starts.
	time.Sleep(60 * time.Millisecond)
	if active, _ := h.activeNow(); active != 4 {
		t.Fatalf("active connections = %d during an untaken shed, want 4", active)
	}
	if got := coord.drains(); got != 1 {
		t.Fatalf("drains = %d while a shed is in progress, want 1", got)
	}
	if m.Held() != 3 {
		t.Fatalf("Held() = %d with one DRAINING, want 3", m.Held())
	}

	// peer-x's replacement becomes READY: the shed completes.
	coord.with(func(f *memCoordinator) { f.extraReadyGroup[shed] = 1 })
	peers.set("peer-x", 1)
	if !waitFor(time.Second, func() bool {
		runs := runsForKey(h.snapshot(), shed)
		return len(runs) == 1 && runs[0].exited
	}) {
		t.Fatal("shed connection did not stop after the replacement was READY")
	}

	// held 3 > fair 2 and peer-x (1) can still take one: a second shed.
	if !waitFor(time.Second, func() bool { return coord.drains() == 2 }) {
		t.Fatalf("second shed never started; held=%d", m.Held())
	}
	second := coord.drainingGroups()
	if len(second) != 1 || second[0] == shed {
		t.Fatalf("second shed groups = %v", second)
	}
	coord.with(func(f *memCoordinator) { f.extraReadyGroup[second[0]] = 1 })
	peers.set("peer-x", 2)
	if !waitFor(time.Second, func() bool { return m.Held() == 2 && len(coord.drainingGroups()) == 0 }) {
		t.Fatalf("did not settle at fair share: held=%d draining=%v", m.Held(), coord.drainingGroups())
	}

	// Settled at fair: many ticks later nothing moved, and the restarted
	// contenders never won the shed keys back.
	time.Sleep(150 * time.Millisecond)
	if got := coord.drains(); got != 2 || m.Held() != 2 {
		t.Fatalf("not stable at fair share: drains=%d held=%d", got, m.Held())
	}
	for _, key := range []string{shed, second[0]} {
		if runs := runsForKey(h.snapshot(), key); len(runs) != 1 {
			t.Fatalf("shed key %s was dialed %d times; it must not be won back", key, len(runs))
		}
		if coord.memberCount(key) != 0 {
			t.Fatalf("this replica holds a member for shed key %s again", key)
		}
	}
}

func TestManagerStalledShedKeepsConsuming(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	peers := newMemPeers()
	peers.set("peer-x", 0) // alive but never takes anything over
	list := newTargetList(numberedTargets("k", 4)...)
	h := &connHarness{}
	logger, logs := captureLogger()
	cfg := fastConfig(coord, list, h)
	cfg.Peers = peers
	cfg.DrainTimeout = 60 * time.Millisecond
	cfg.Logger = logger
	m, _ := startManager(t, cfg)

	if !waitFor(time.Second, func() bool { return coord.drains() == 1 }) {
		t.Fatal("no shed started")
	}
	shed := coord.drainingGroups()[0]
	if !waitFor(time.Second, func() bool { return logs.count(`"event":"test_conn_shed_stalled"`) == 1 }) {
		t.Fatal("stalled shed was not reported")
	}
	time.Sleep(100 * time.Millisecond) // many ticks after the drain budget
	if got := coord.drains(); got != 1 {
		t.Fatalf("drains = %d; a stalled shed must block further sheds", got)
	}
	if got := logs.count(`"event":"test_conn_shed_stalled"`); got != 1 {
		t.Fatalf("stalled shed reported %d times, want once", got)
	}
	runs := runsForKey(h.snapshot(), shed)
	if len(runs) != 1 || runs[0].exited {
		t.Fatalf("stalled shed dropped its stream: %+v", runs)
	}
	if active, _ := h.activeNow(); active != 4 || m.Held() != 3 {
		t.Fatalf("active=%d held=%d, want every stream consumed (3 READY + 1 DRAINING)", active, m.Held())
	}

	// A late replacement still completes the handoff.
	coord.with(func(f *memCoordinator) { f.extraReadyGroup[shed] = 1 })
	peers.set("peer-x", 1)
	if !waitFor(time.Second, func() bool { return runsForKey(h.snapshot(), shed)[0].exited }) {
		t.Fatal("stalled shed did not finish once a replacement was READY")
	}
}

func TestManagerDoesNotShedWithoutAPeerBelowFair(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	peers := newMemPeers()
	// Two other replicas report fair loads; a third that vanished still counts
	// as alive but also reports a fair load, so nobody can usefully receive.
	peers.set("peer-x", 2)
	peers.set("peer-y", 2)
	list := newTargetList(numberedTargets("k", 6)...)
	h := &connHarness{}
	cfg := fastConfig(coord, list, h)
	cfg.Peers = peers
	m, _ := startManager(t, cfg)

	// This replica holds all 6 (the peers are fakes); fair = ceil(6/3) = 2 and
	// both peers already hold 2, so there is no receiver.
	if !waitFor(time.Second, func() bool { return m.Held() == 6 }) {
		t.Fatalf("held=%d", m.Held())
	}
	time.Sleep(80 * time.Millisecond)
	if got := coord.drains(); got != 0 {
		t.Fatalf("shed %d times with no peer below fair", got)
	}
}

func TestManagerWithoutPeersNeverSheds(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	list := newTargetList(numberedTargets("k", 4)...)
	h := &connHarness{}
	m, _ := startManager(t, fastConfig(coord, list, h))
	if !waitFor(time.Second, func() bool { return m.Held() == 4 }) {
		t.Fatalf("held=%d", m.Held())
	}
	time.Sleep(60 * time.Millisecond)
	if got := coord.drains(); got != 0 {
		t.Fatalf("shed %d times without Peers", got)
	}
}

func TestManagerHeartbeatsAndLeavesPeersOnShutdown(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	peers := newMemPeers()
	list := newTargetList(Target{Key: "k", Fingerprint: "fp"})
	h := &connHarness{}
	cfg := fastConfig(coord, list, h)
	cfg.Peers = peers
	m, cancel := startManager(t, cfg)

	if !waitFor(time.Second, func() bool {
		peers.mu.Lock()
		defer peers.mu.Unlock()
		return peers.nodes[m.NodeID()] == 1
	}) {
		t.Fatal("heartbeat never reported the READY connection")
	}
	m.BeginShutdown()
	if !waitFor(time.Second, func() bool {
		peers.mu.Lock()
		defer peers.mu.Unlock()
		_, alive := peers.nodes[m.NodeID()]
		return !alive && len(peers.leaves) == 1
	}) {
		t.Fatal("BeginShutdown did not leave the membership")
	}
	peers.mu.Lock()
	beats := peers.heartbeats
	peers.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	peers.mu.Lock()
	_, rejoined := peers.nodes[m.NodeID()]
	after := peers.heartbeats
	peers.mu.Unlock()
	if rejoined || after != beats {
		t.Fatalf("a shutting-down replica kept heartbeating (%d -> %d)", beats, after)
	}
	cancel()
	if !m.WaitWithTimeout(time.Second) {
		t.Fatal("manager did not stop")
	}
	peers.mu.Lock()
	defer peers.mu.Unlock()
	if len(peers.leaves) != 1 {
		t.Fatalf("left %d times, want once", len(peers.leaves))
	}
}

func TestClaimDelayIsRelativeToLeastLoadedPeer(t *testing.T) {
	m := &Manager{cfg: Config{ClaimDelayStep: 250 * time.Millisecond, ClaimDelayMax: 5 * time.Second}}
	m.ready.Store(20)
	m.minPeerHeld.Store(20)
	if got := m.claimDelay(); got != 0 {
		t.Fatalf("delay with an equally loaded peer = %s, want 0 (takeover not slowed)", got)
	}
	m.minPeerHeld.Store(18)
	if got := m.claimDelay(); got != 500*time.Millisecond {
		t.Fatalf("delay two above the least-loaded peer = %s, want 500ms", got)
	}
	m.minPeerHeld.Store(25)
	if got := m.claimDelay(); got != 0 {
		t.Fatalf("delay below the least-loaded peer = %s, want 0", got)
	}
}

// A shed that meets another member's handoff keeps its READY stream and
// hands over once that handoff has finished, instead of closing.
func TestManagerShedDefersBehindAnotherHandoff(t *testing.T) {
	coord := newMemCoordinator(1, 200*time.Millisecond)
	targets := numberedTargets("k", 4)
	// Every key still has a DRAINING member of a replica that is stopping.
	coord.with(func(f *memCoordinator) {
		for _, target := range targets {
			f.members[target.Key] = map[string]State{"old-" + target.Key: StateDraining}
		}
	})
	peers := newMemPeers()
	peers.set("peer-x", 0)
	list := newTargetList(targets...)
	h := &connHarness{}
	logger, logs := captureLogger()
	cfg := fastConfig(coord, list, h)
	cfg.Peers = peers
	cfg.Logger = logger
	m, _ := startManager(t, cfg)

	if !waitFor(time.Second, func() bool { return logs.count(`"event":"test_conn_shed_deferred"`) == 1 }) {
		t.Fatal("the shed was not deferred behind the other handoff")
	}
	time.Sleep(60 * time.Millisecond) // many renewals
	if got := logs.count(`"event":"test_conn_parallel_drain"`); got != 0 {
		t.Fatalf("a shed closed its READY stream %d times", got)
	}
	if got := logs.count(`"event":"test_conn_shed_deferred"`); got != 1 {
		t.Fatalf("deferred shed logged %d times, want once", got)
	}
	for _, run := range h.snapshot() {
		if run.exited {
			t.Fatalf("a stream ended while its shed was deferred: %+v", run)
		}
	}
	if m.Held() != 4 {
		t.Fatalf("held = %d, want all 4 READY", m.Held())
	}

	// The other handoff finishes: the deferred shed begins its own.
	coord.with(func(f *memCoordinator) {
		for _, target := range targets {
			delete(f.members[target.Key], "old-"+target.Key)
		}
	})
	if !waitFor(time.Second, func() bool { return len(coord.drainingGroups()) == 1 }) {
		t.Fatal("the deferred shed never began its handoff")
	}
}

// A shed stream that drops before anyone took it over is contended for
// again at once, not at the next PollInterval.
func TestManagerRestartsAShedStreamThatDropped(t *testing.T) {
	coord := newMemCoordinator(1, 2*time.Second)
	targets := numberedTargets("k", 2)
	var mu sync.Mutex
	drop := map[string]chan struct{}{}
	for _, target := range targets {
		drop[target.Key] = make(chan struct{})
	}
	peers := newMemPeers()
	peers.set("peer-x", 0)
	list := newTargetList(targets...)
	h := &connHarness{behavior: func(index int, target Target) func(context.Context) error {
		mu.Lock()
		ch := drop[target.Key]
		mu.Unlock()
		return func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return nil
			case <-ch:
				return fmt.Errorf("dropped")
			}
		}
	}}
	cfg := fastConfig(coord, list, h)
	cfg.Peers = peers
	cfg.PollInterval = 400 * time.Millisecond
	startManager(t, cfg)

	if !waitFor(2*time.Second, func() bool { return len(coord.drainingGroups()) == 1 }) {
		t.Fatal("no shed started")
	}
	shed := coord.drainingGroups()[0]
	mu.Lock()
	close(drop[shed])
	drop[shed] = make(chan struct{})
	mu.Unlock()
	dropped := time.Now()
	if !waitFor(time.Second, func() bool { return len(runsForKey(h.snapshot(), shed)) == 2 }) {
		t.Fatal("the dropped shed stream was never restarted")
	}
	if waited := runsForKey(h.snapshot(), shed)[1].dialedAt.Sub(dropped); waited > 150*time.Millisecond {
		t.Fatalf("restarted after %s; want a kick, not the next %s sweep", waited, cfg.PollInterval)
	}
}
