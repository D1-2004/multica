package connmgr

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Rebalancing scenarios: two or more Managers with RedisPeers against one real
// Redis. Timings are scaled down (poll 100ms, renew 20ms, lease 1s); in
// production every duration is ~100x longer.

const rebalancePoll = 100 * time.Millisecond

func withRedisPeers(t *testing.T, client *redis.Client, extra func(*Config)) func(*Config) {
	t.Helper()
	peers, err := NewRedisPeers(client, RedisPeersConfig{KeyPrefix: testKeyPrefix, Name: "test_cluster"})
	if err != nil {
		t.Fatalf("new peers: %v", err)
	}
	return func(c *Config) {
		c.Peers = peers
		c.PollInterval = rebalancePoll
		if extra != nil {
			extra(c)
		}
	}
}

func keyNames(targets []Target) []string {
	out := make([]string, len(targets))
	for i, target := range targets {
		out[i] = target.Key
	}
	return out
}

// activeByKey counts, per key, connections whose link is up and not exited.
func (h *connHarness) activeByKey() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]int)
	for _, r := range h.runs {
		if !r.readyCalledAt.IsZero() && !r.exited {
			out[r.key]++
		}
	}
	return out
}

// invariantWatch samples, every couple of milliseconds, the coordinator record
// of every key (never more than one live READY member) and, once armed, the
// consumers (every key has one or two connected consumers: never a gap, and
// at most the DRAINING + replacement overlap).
type invariantWatch struct {
	client *redis.Client
	h      *connHarness
	keys   []string
	armed  atomic.Bool
	stop   chan struct{}
	done   chan struct{}

	mu         sync.Mutex
	samples    int
	readySeen  int // key samples with exactly one live READY member
	violations []string
}

func watchInvariants(t *testing.T, client *redis.Client, h *connHarness, keys []string) *invariantWatch {
	t.Helper()
	w := &invariantWatch{client: client, h: h, keys: keys, stop: make(chan struct{}), done: make(chan struct{})}
	go w.loop()
	t.Cleanup(w.halt)
	return w
}

func (w *invariantWatch) halt() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	<-w.done
}

func (w *invariantWatch) loop() {
	defer close(w.done)
	ctx := context.Background()
	for {
		select {
		case <-w.stop:
			return
		case <-time.After(2 * time.Millisecond):
		}
		now, err := w.client.Time(ctx).Result()
		if err != nil {
			continue
		}
		var problems []string
		readySeen := 0
		for _, key := range w.keys {
			base := testKeyPrefix + "{" + key + "}"
			members, err := w.client.HGetAll(ctx, base+":members").Result()
			if err != nil {
				continue
			}
			ready := 0
			for token, state := range members {
				if state != string(StateReady) {
					continue
				}
				score, err := w.client.ZScore(ctx, base+":expiry", token).Result()
				if err == nil && int64(score) > now.UnixMilli() {
					ready++
				}
			}
			if ready > 1 {
				problems = append(problems, fmt.Sprintf("%s has %d READY members", key, ready))
			}
			if ready == 1 {
				readySeen++
			}
		}
		if w.armed.Load() {
			active := w.h.activeByKey()
			for _, key := range w.keys {
				if n := active[key]; n < 1 || n > 2 {
					problems = append(problems, fmt.Sprintf("%s has %d connected consumers", key, n))
				}
			}
		}
		w.mu.Lock()
		w.samples++
		w.readySeen += readySeen
		if len(w.violations) < 20 {
			w.violations = append(w.violations, problems...)
		}
		w.mu.Unlock()
	}
}

func (w *invariantWatch) check(t *testing.T) {
	t.Helper()
	w.halt()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.violations) > 0 {
		t.Fatalf("invariant violations over %d samples: %s", w.samples, strings.Join(w.violations, "; "))
	}
	if w.samples < 10 || w.readySeen == 0 {
		t.Fatalf("invariant watcher is vacuous: %d samples, %d READY observations", w.samples, w.readySeen)
	}
	t.Logf("invariants held over %d samples (%d key observations with one READY member)", w.samples, w.readySeen)
}

func waitBalanced(t *testing.T, timeout time.Duration, want int, replicas ...*replica) time.Duration {
	t.Helper()
	started := time.Now()
	if !waitFor(timeout, func() bool {
		for _, r := range replicas {
			if r.m.Held() != want {
				return false
			}
		}
		return true
	}) {
		t.Fatalf("not balanced at %d each after %s: %s", want, timeout, heldSummary(replicas...))
	}
	return time.Since(started)
}

func heldSummary(replicas ...*replica) string {
	parts := make([]string, 0, len(replicas))
	for _, r := range replicas {
		parts = append(parts, fmt.Sprintf("%s=%d", r.name, r.m.Held()))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// assertStable checks that held counts and the set of connections stay put
// for several poll intervals (no flapping).
func assertStable(t *testing.T, h *connHarness, polls int, want int, replicas ...*replica) {
	t.Helper()
	dials := len(h.snapshot())
	for i := 0; i < polls; i++ {
		time.Sleep(rebalancePoll)
		for _, r := range replicas {
			if r.m.Held() != want {
				t.Fatalf("flapping after %d polls: %s", i+1, heldSummary(replicas...))
			}
		}
	}
	if now := len(h.snapshot()); now != dials {
		t.Fatalf("connections moved while balanced: %d new dials over %d polls", now-dials, polls)
	}
}

func polls(d time.Duration) string {
	return fmt.Sprintf("%s (%.1f poll intervals)", d.Round(time.Millisecond), float64(d)/float64(rebalancePoll))
}

func TestClusterStaggeredStartRebalances(t *testing.T) {
	clients := newClusterClients(t, 2)
	targets := numberedTargets("dws:stagger", 6)
	list := newTargetList(targets...)
	h := &connHarness{}

	a := newClusterReplica(t, "A", clients[0], list, h, withRedisPeers(t, clients[0], nil))
	a.start(t)
	if !waitFor(3*time.Second, func() bool { return a.m.Held() == 6 }) {
		t.Fatalf("A alone held %d", a.m.Held())
	}
	watch := watchInvariants(t, clients[0], h, keyNames(targets))
	watch.armed.Store(true)

	b := newClusterReplica(t, "B", clients[1], list, h, withRedisPeers(t, clients[1], nil))
	b.start(t)
	converged := waitBalanced(t, 5*time.Second, 3, a, b)
	t.Logf("staggered start: B joined after A held 6; 3/3 after %s", polls(converged))

	assertStable(t, h, 8, 3, a, b)
	watch.check(t)
}

func TestClusterRollingDeployEndsBalanced(t *testing.T) {
	clients := newClusterClients(t, 4)
	targets := numberedTargets("dws:roll", 6)
	list := newTargetList(targets...)
	h := &connHarness{}
	newReplica := func(name string, client *redis.Client) *replica {
		r := newClusterReplica(t, name, client, list, h, withRedisPeers(t, client, nil))
		r.start(t)
		return r
	}

	a := newReplica("A", clients[0])
	b := newReplica("B", clients[1])
	initial := waitBalanced(t, 5*time.Second, 3, a, b)
	t.Logf("A and B started together: 3/3 after %s", polls(initial))
	watch := watchInvariants(t, clients[0], h, keyNames(targets))
	watch.armed.Store(true)

	stop := func(r *replica) {
		r.m.BeginShutdown()
		if !r.m.WaitForHandoffs(5 * time.Second) {
			t.Fatalf("%s: rolling handoff did not complete", r.name)
		}
		r.cancel()
		if !r.m.WaitWithTimeout(3 * time.Second) {
			t.Fatalf("%s did not stop", r.name)
		}
	}

	// Roll A: its connections hand over to B, then A2 starts.
	stop(a)
	if b.m.Held() != 6 {
		t.Fatalf("after A drained B holds %d, want 6", b.m.Held())
	}
	a2 := newReplica("A2", clients[2])
	afterA := waitBalanced(t, 5*time.Second, 3, a2, b)
	t.Logf("A drained -> A2 started: 3/3 after %s", polls(afterA))

	// Roll B the same way.
	stop(b)
	if a2.m.Held() != 6 {
		t.Fatalf("after B drained A2 holds %d, want 6", a2.m.Held())
	}
	b2 := newReplica("B2", clients[3])
	afterB := waitBalanced(t, 5*time.Second, 3, a2, b2)
	t.Logf("B drained -> B2 started: 3/3 after %s", polls(afterB))

	assertStable(t, h, 8, 3, a2, b2)
	watch.check(t)
}

func TestClusterShedWithoutAvailablePeerKeepsStream(t *testing.T) {
	clients := newClusterClients(t, 2)
	targets := numberedTargets("dws:stall", 4)
	list := newTargetList(targets...)
	h := &connHarness{}

	// A "ghost" replica heartbeats (counts as alive, holds nothing) but never
	// claims anything, so a shed toward it cannot be taken over.
	ghost, err := NewRedisPeers(clients[0], RedisPeersConfig{KeyPrefix: testKeyPrefix, Name: "test_cluster"})
	if err != nil {
		t.Fatalf("new peers: %v", err)
	}
	if err := ghost.Heartbeat(context.Background(), "ghost-node", 0, time.Minute); err != nil {
		t.Fatalf("ghost heartbeat: %v", err)
	}

	logger, logs := captureLogger()
	const drainBudget = 500 * time.Millisecond
	a := newClusterReplica(t, "A", clients[0], list, h, withRedisPeers(t, clients[0], func(c *Config) {
		c.DrainTimeout = drainBudget
		c.Logger = logger
	}))
	a.start(t)

	// fair = ceil(4/2) = 2: A sheds one key, which nobody takes.
	if !waitFor(3*time.Second, func() bool { return logs.count(`"event":"test_cluster_shed"`) == 1 }) {
		t.Fatalf("A never shed; held=%d", a.m.Held())
	}
	watch := watchInvariants(t, clients[0], h, keyNames(targets))
	if !waitFor(3*time.Second, func() bool { return logs.count(`"event":"test_cluster_shed_stalled"`) == 1 }) {
		t.Fatal("stalled shed was not reported after the drain budget")
	}
	watch.armed.Store(true)
	time.Sleep(3 * rebalancePoll)
	if got := logs.count(`"event":"test_cluster_shed"`); got != 1 {
		t.Fatalf("A started %d sheds while one was stalled, want 1", got)
	}
	if active, _ := h.activeNow(); active != 4 || a.m.Held() != 3 {
		t.Fatalf("stalled shed lost a stream: active=%d held=%d", active, a.m.Held())
	}
	for key, n := range h.activeByKey() {
		if n != 1 {
			t.Fatalf("%s has %d consumers, want 1", key, n)
		}
	}

	// A real replica joins: the stalled shed completes and balancing
	// continues. Three live nodes (A, B, ghost) make fair = ceil(4/3) = 2.
	b := newClusterReplica(t, "B", clients[1], list, h, withRedisPeers(t, clients[1], nil))
	b.start(t)
	joined := waitBalanced(t, 5*time.Second, 2, a, b)
	t.Logf("stalled shed resolved and 2/2 reached %s after B joined", polls(joined))
	if !waitFor(time.Second, func() bool { return logs.count(`"event":"test_cluster_shed_finished"`) >= 2 }) {
		t.Fatalf("shed_finished events = %d, want the stalled shed and the next one", logs.count(`"event":"test_cluster_shed_finished"`))
	}
	assertStable(t, h, 5, 2, a, b)
	watch.check(t)
}
