package connmgr

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// These tests run two Managers ("replicas"), each with its own Redis client
// and RedisCoordinator, against one real Redis. They skip without
// REDIS_TEST_URL.

const (
	clusterLeaseTTL      = time.Second
	clusterRenewInterval = 20 * time.Millisecond
)

// crashableCoordinator lets a test simulate a process that dies without
// releasing: once crashed, Release is dropped and the member can only expire.
type crashableCoordinator struct {
	Coordinator
	crashed atomic.Bool
}

func (c *crashableCoordinator) Release(ctx context.Context, l Lease) error {
	if c.crashed.Load() {
		return nil
	}
	return c.Coordinator.Release(ctx, l)
}

type replica struct {
	name   string
	m      *Manager
	coord  *crashableCoordinator
	cancel context.CancelFunc
}

func newClusterReplica(t *testing.T, name string, client *redis.Client, list *targetList, h *connHarness, tweak func(*Config)) *replica {
	t.Helper()
	redisCoord, err := NewRedisCoordinator(client, RedisCoordinatorConfig{
		KeyPrefix:   testKeyPrefix,
		TargetReady: 1,
		LeaseTTL:    clusterLeaseTTL,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coord := &crashableCoordinator{Coordinator: redisCoord}
	cfg := Config{
		Name:              "test_cluster",
		List:              list.List,
		Dial:              h.dial(name),
		Coordinator:       coord,
		PollInterval:      50 * time.Millisecond,
		RenewInterval:     clusterRenewInterval,
		DrainTimeout:      3 * time.Second,
		ShutdownTimeout:   3 * time.Second,
		MinBackoff:        20 * time.Millisecond,
		MaxBackoff:        200 * time.Millisecond,
		ResetBackoffAfter: time.Second,
		ClaimDelayStep:    30 * time.Millisecond,
		ClaimDelayMax:     300 * time.Millisecond,
		Logger:            discardLogger(),
	}
	if tweak != nil {
		tweak(&cfg)
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatalf("new manager %s: %v", name, err)
	}
	return &replica{name: name, m: m, coord: coord}
}

func (r *replica) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.m.Run(ctx)
	t.Cleanup(func() {
		cancel()
		if !r.m.WaitWithTimeout(5 * time.Second) {
			t.Errorf("replica %s did not stop", r.name)
		}
	})
}

// newClusterClients returns n clients on the flushed test DB, one per replica.
func newClusterClients(t *testing.T, n int) []*redis.Client {
	t.Helper()
	first := newRedisTestClient(t) // skips without Redis; flushes the DB
	clients := []*redis.Client{first}
	for i := 1; i < n; i++ {
		c := redis.NewClient(first.Options())
		t.Cleanup(func() { _ = c.Close() })
		clients = append(clients, c)
	}
	return clients
}

func holderOf(t *testing.T, a, b *replica) (holder, other *replica) {
	t.Helper()
	switch {
	case a.m.Held() == 1 && b.m.Held() == 0:
		return a, b
	case b.m.Held() == 1 && a.m.Held() == 0:
		return b, a
	default:
		t.Fatalf("no single holder: %s=%d %s=%d", a.name, a.m.Held(), b.name, b.m.Held())
		return nil, nil
	}
}

func lastRun(runs []runRecord, replicaName, key string) (runRecord, bool) {
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].replica == replicaName && runs[i].key == key {
			return runs[i], true
		}
	}
	return runRecord{}, false
}

func TestClusterExactlyOneReadyAndGracefulHandoff(t *testing.T) {
	clients := newClusterClients(t, 2)
	const key = "dws:user-handoff"
	list := newTargetList(Target{Key: key, Fingerprint: "fp", Value: "token-never-logged"})
	h := &connHarness{}
	a := newClusterReplica(t, "A", clients[0], list, h, nil)
	b := newClusterReplica(t, "B", clients[1], list, h, nil)
	a.start(t)
	b.start(t)

	if !waitFor(3*time.Second, func() bool { return a.m.Held()+b.m.Held() == 1 }) {
		t.Fatalf("no READY connection: A=%d B=%d", a.m.Held(), b.m.Held())
	}
	// Steady state: exactly one READY connection cluster-wide, while the other
	// replica keeps contending.
	for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if held := a.m.Held() + b.m.Held(); held != 1 {
			t.Fatalf("steady state READY count = %d, want 1", held)
		}
		if active, _ := h.activeNow(); active != 1 {
			t.Fatalf("steady state active connections = %d, want 1", active)
		}
	}
	holder, other := holderOf(t, a, b)

	holder.m.BeginShutdown()
	if !holder.m.WaitForHandoffs(3 * time.Second) {
		t.Fatal("graceful handoff did not complete")
	}
	if holder.m.Held() != 0 || other.m.Held() != 1 {
		t.Fatalf("after handoff: holder=%d other=%d, want 0/1", holder.m.Held(), other.m.Held())
	}
	runs := h.snapshot()
	oldRun, ok := lastRun(runs, holder.name, key)
	if !ok || !oldRun.exited {
		t.Fatalf("old connection still running after handoff: %+v", runs)
	}
	newRun, ok := lastRun(runs, other.name, key)
	if !ok || newRun.readyAt.IsZero() || newRun.exited {
		t.Fatalf("replacement not running: %+v", runs)
	}
	// Overlap: the DRAINING connection kept consuming until the replacement's
	// link was up and its READY transition was committed, so there was never
	// a gap without a connected consumer. (The old member stops as soon as
	// its renew observes the committed READY, which can be a round trip before
	// the replacement's ready callback returns; frames arriving meanwhile
	// buffer in the replacement's already-open link.)
	if !newRun.readyCalledAt.Before(oldRun.exitAt) {
		t.Fatalf("no overlap: replacement link up at %s, old exited at %s", newRun.readyCalledAt.Format(time.StampMicro), oldRun.exitAt.Format(time.StampMicro))
	}
	if _, maxActive := h.activeNow(); maxActive != 2 {
		t.Fatalf("max concurrent connections = %d, want 2 (one DRAINING + one READY)", maxActive)
	}
	// The key never went unconsumed after the handoff settled.
	time.Sleep(100 * time.Millisecond)
	if active, _ := h.activeNow(); active != 1 || other.m.Held() != 1 {
		t.Fatalf("after handoff: active=%d other held=%d", active, other.m.Held())
	}
}

func TestClusterCrashedHolderIsReplacedAfterLeaseTTL(t *testing.T) {
	clients := newClusterClients(t, 2)
	const key = "dws:user-crash"
	list := newTargetList(Target{Key: key, Fingerprint: "fp"})
	h := &connHarness{}
	a := newClusterReplica(t, "A", clients[0], list, h, nil)
	b := newClusterReplica(t, "B", clients[1], list, h, nil)
	a.start(t)
	b.start(t)

	if !waitFor(3*time.Second, func() bool { return a.m.Held()+b.m.Held() == 1 }) {
		t.Fatalf("no READY connection: A=%d B=%d", a.m.Held(), b.m.Held())
	}
	holder, other := holderOf(t, a, b)

	// Crash: the process stops consuming (ctx cancelled, no drain) and never
	// reaches Redis again, so its member must expire on its own.
	holder.coord.crashed.Store(true)
	crashedAt := time.Now()
	holder.cancel()
	if !holder.m.WaitWithTimeout(2 * time.Second) {
		t.Fatal("crashed holder did not stop")
	}
	if !waitFor(3*time.Second, func() bool { return other.m.Held() == 1 }) {
		t.Fatal("surviving replica never took over after the lease TTL")
	}
	takeover := time.Since(crashedAt)
	// The last renewal happened at most one RenewInterval before the crash.
	if minimum := clusterLeaseTTL - clusterRenewInterval - 100*time.Millisecond; takeover < minimum {
		t.Fatalf("takeover after %s, before the crashed member's lease could expire (>= %s)", takeover, minimum)
	}
	if takeover > clusterLeaseTTL+1500*time.Millisecond {
		t.Fatalf("takeover after %s, want about one lease TTL (%s)", takeover, clusterLeaseTTL)
	}
	runs := h.snapshot()
	oldRun, _ := lastRun(runs, holder.name, key)
	if !oldRun.exited || oldRun.exitAt.Sub(crashedAt) > 200*time.Millisecond {
		t.Fatalf("crashed holder kept consuming: %+v", oldRun)
	}
}

func TestClusterCancelledHolderReleasesForPromptTakeover(t *testing.T) {
	clients := newClusterClients(t, 2)
	const key = "dws:user-cancel"
	list := newTargetList(Target{Key: key, Fingerprint: "fp"})
	h := &connHarness{}
	a := newClusterReplica(t, "A", clients[0], list, h, nil)
	b := newClusterReplica(t, "B", clients[1], list, h, nil)
	a.start(t)
	b.start(t)

	if !waitFor(3*time.Second, func() bool { return a.m.Held()+b.m.Held() == 1 }) {
		t.Fatalf("no READY connection: A=%d B=%d", a.m.Held(), b.m.Held())
	}
	holder, other := holderOf(t, a, b)
	cancelledAt := time.Now()
	holder.cancel() // hard stop without drain; Release still runs
	if !waitFor(3*time.Second, func() bool { return other.m.Held() == 1 }) {
		t.Fatal("surviving replica never took over")
	}
	if takeover := time.Since(cancelledAt); takeover >= clusterLeaseTTL/2 {
		t.Fatalf("takeover after %s; a released member should not wait for its TTL", takeover)
	}
}

func TestClusterBalancesKeysAcrossReplicas(t *testing.T) {
	clients := newClusterClients(t, 2)
	var targets []Target
	for i := 0; i < 6; i++ {
		targets = append(targets, Target{Key: fmt.Sprintf("dws:user-%d", i), Fingerprint: "fp"})
	}
	list := newTargetList(targets...)
	h := &connHarness{}
	a := newClusterReplica(t, "A", clients[0], list, h, nil)
	b := newClusterReplica(t, "B", clients[1], list, h, nil)
	a.start(t)
	b.start(t)

	if !waitFor(5*time.Second, func() bool { return a.m.Held()+b.m.Held() == 6 }) {
		t.Fatalf("keys not all held: A=%d B=%d", a.m.Held(), b.m.Held())
	}
	time.Sleep(200 * time.Millisecond) // settle
	heldA, heldB := a.m.Held(), b.m.Held()
	if heldA+heldB != 6 {
		t.Fatalf("held drifted after settle: A=%d B=%d", heldA, heldB)
	}
	if active, _ := h.activeNow(); active != 6 {
		t.Fatalf("active connections = %d, want exactly one per key", active)
	}
	if heldA < 2 || heldB < 2 {
		t.Fatalf("unbalanced: A=%d B=%d, want both >= 2", heldA, heldB)
	}
	t.Logf("balanced split A=%d B=%d", heldA, heldB)
}
