package connmgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisTestDB is dedicated to this package so it never collides with other
// suites sharing REDIS_TEST_URL (the robot coordinator uses DB 15).
const redisTestDB = 13

const testKeyPrefix = "multica:connmgr-test:v1:"

// newRedisTestClient returns a client on the flushed test DB, or skips when
// REDIS_TEST_URL is unset or unreachable.
func newRedisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("REDIS_TEST_URL")
	if rawURL == "" {
		t.Skip("REDIS_TEST_URL not set")
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("parse REDIS_TEST_URL: %v", err)
	}
	opts.DB = redisTestDB
	client := redis.NewClient(opts)
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		t.Skipf("REDIS_TEST_URL unreachable: %v", err)
	}
	if err := client.FlushDB(ctx).Err(); err != nil {
		client.Close()
		t.Fatalf("flush Redis test DB: %v", err)
	}
	t.Cleanup(func() {
		_ = client.FlushDB(context.Background()).Err()
		_ = client.Close()
	})
	return client
}

func newTestRedisCoordinator(t *testing.T, target int, ttl time.Duration) (*RedisCoordinator, *redis.Client) {
	t.Helper()
	client := newRedisTestClient(t)
	coordinator, err := NewRedisCoordinator(client, RedisCoordinatorConfig{
		KeyPrefix:   testKeyPrefix,
		TargetReady: target,
		LeaseTTL:    ttl,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	return coordinator, client
}

func claimLease(t *testing.T, coordinator Coordinator, group, token string) (Lease, Snapshot) {
	t.Helper()
	lease, snapshot, acquired, err := coordinator.Claim(context.Background(), group, token)
	if err != nil {
		t.Fatalf("claim %s: %v", token, err)
	}
	if !acquired {
		t.Fatalf("claim %s: capacity unexpectedly unavailable: %+v", token, snapshot)
	}
	return lease, snapshot
}

func markReady(t *testing.T, coordinator Coordinator, lease Lease) (Lease, Snapshot) {
	t.Helper()
	readyLease, snapshot, err := coordinator.MarkReady(context.Background(), lease)
	if err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	return readyLease, snapshot
}

func TestNewRedisCoordinatorValidatesConfig(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })

	for _, target := range []int{-1, 0, 3} {
		_, err := NewRedisCoordinator(client, RedisCoordinatorConfig{KeyPrefix: testKeyPrefix, TargetReady: target})
		if !errors.Is(err, ErrConfig) {
			t.Errorf("target %d: error = %v, want ErrConfig", target, err)
		}
	}
	if _, err := NewRedisCoordinator(nil, RedisCoordinatorConfig{KeyPrefix: testKeyPrefix, TargetReady: 2}); !errors.Is(err, ErrConfig) {
		t.Errorf("nil Redis: error = %v, want ErrConfig", err)
	}
	if _, err := NewRedisCoordinator(client, RedisCoordinatorConfig{KeyPrefix: testKeyPrefix, TargetReady: 2, LeaseTTL: time.Nanosecond}); !errors.Is(err, ErrConfig) {
		t.Errorf("sub-millisecond TTL: error = %v, want ErrConfig", err)
	}
	if _, err := NewRedisCoordinator(client, RedisCoordinatorConfig{TargetReady: 1}); !errors.Is(err, ErrConfig) {
		t.Errorf("empty key prefix: error = %v, want ErrConfig", err)
	}
	if _, err := NewRedisCoordinator(client, RedisCoordinatorConfig{KeyPrefix: "bad:{tag}:", TargetReady: 1}); !errors.Is(err, ErrConfig) {
		t.Errorf("braced key prefix: error = %v, want ErrConfig", err)
	}

	coordinator, err := NewRedisCoordinator(client, RedisCoordinatorConfig{KeyPrefix: testKeyPrefix, TargetReady: 2})
	if err != nil {
		t.Fatalf("default config: %v", err)
	}
	if coordinator.TargetReady() != 2 || coordinator.LeaseTTL() != DefaultLeaseTTL {
		t.Fatalf("defaults = target %d TTL %s", coordinator.TargetReady(), coordinator.LeaseTTL())
	}
	keys := coordinator.keys("dws:user-1")
	if keys[0] != testKeyPrefix+"{dws:user-1}:members" || keys[1] != testKeyPrefix+"{dws:user-1}:expiry" {
		t.Fatalf("keys = %v, want hash-tagged members/expiry pair", keys)
	}
}

// TestRedisCoordinatorValidatesGroupAndToken runs without Redis: invalid
// inputs are rejected before any script executes.
func TestRedisCoordinatorValidatesGroupAndToken(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	coordinator, err := NewRedisCoordinator(client, RedisCoordinatorConfig{KeyPrefix: testKeyPrefix, TargetReady: 1})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()
	for name, group := range map[string]string{
		"empty":     "",
		"open":      "a{b",
		"close":     "a}b",
		"too_long":  strings.Repeat("k", maxGroupLen+1),
		"hash_tag":  "{x}",
		"only_open": "{",
	} {
		if _, _, _, err := coordinator.Claim(ctx, group, "token"); !errors.Is(err, ErrConfig) {
			t.Errorf("%s group claim error = %v, want ErrConfig", name, err)
		}
	}
	if err := validateGroup(strings.Repeat("k", maxGroupLen)); err != nil {
		t.Errorf("group of exactly %d bytes rejected: %v", maxGroupLen, err)
	}
	for name, token := range map[string]string{"empty": "", "too_long": strings.Repeat("t", maxTokenLen+1)} {
		if _, _, _, err := coordinator.Claim(ctx, "group", token); !errors.Is(err, ErrConfig) {
			t.Errorf("%s token claim error = %v, want ErrConfig", name, err)
		}
	}
	if _, err := coordinator.Renew(ctx, Lease{Group: "group", Token: "token", State: "BOGUS"}); !errors.Is(err, ErrConfig) {
		t.Errorf("unknown state renew error = %v, want ErrConfig", err)
	}
}

func TestRedisCoordinatorTargetTwoRollingHandoffCapsAtThree(t *testing.T) {
	coordinator, _ := newTestRedisCoordinator(t, 2, DefaultLeaseTTL)
	ctx := context.Background()
	const group = "group-target-two"

	leaseA, _ := claimLease(t, coordinator, group, "node-a-g1")
	leaseA, _ = markReady(t, coordinator, leaseA)
	leaseB, _ := claimLease(t, coordinator, group, "node-b-g1")
	leaseB, steady := markReady(t, coordinator, leaseB)
	if steady.Total != 2 || steady.Ready != 2 || steady.Connecting != 0 || steady.Draining != 0 {
		t.Fatalf("steady snapshot = %+v, want two READY", steady)
	}
	if ttl := steady.RemainingTTL(); ttl <= 0 || ttl > DefaultLeaseTTL {
		t.Fatalf("remaining TTL = %s, want (0, %s]", ttl, DefaultLeaseTTL)
	}

	if _, snapshot, acquired, err := coordinator.Claim(ctx, group, "node-c-g1"); err != nil || acquired {
		t.Fatalf("third steady claim: acquired=%v err=%v snapshot=%+v", acquired, err, snapshot)
	}

	leaseA, draining, err := coordinator.BeginDrain(ctx, leaseA)
	if err != nil {
		t.Fatalf("begin drain A: %v", err)
	}
	if draining.Total != 2 || draining.Ready != 1 || draining.Draining != 1 {
		t.Fatalf("draining snapshot = %+v", draining)
	}
	if _, _, err := coordinator.BeginDrain(ctx, leaseB); !errors.Is(err, ErrDrainInProgress) {
		t.Fatalf("second drain error = %v, want ErrDrainInProgress", err)
	}

	leaseC, connecting := claimLease(t, coordinator, group, "node-c-g1")
	if connecting.Total != 3 || connecting.Ready != 1 || connecting.Connecting != 1 || connecting.Draining != 1 {
		t.Fatalf("handoff connecting snapshot = %+v", connecting)
	}
	if _, snapshot, acquired, err := coordinator.Claim(ctx, group, "node-d-g1"); err != nil || acquired || snapshot.Total != 3 {
		t.Fatalf("fourth claim: acquired=%v err=%v snapshot=%+v", acquired, err, snapshot)
	}

	leaseC, replacementReady := markReady(t, coordinator, leaseC)
	if replacementReady.Total != 3 || replacementReady.Ready != 2 || replacementReady.Draining != 1 || !replacementReady.ReadyTargetMet() {
		t.Fatalf("replacement READY snapshot = %+v", replacementReady)
	}
	if err := coordinator.Release(ctx, leaseA); err != nil {
		t.Fatalf("release draining A: %v", err)
	}
	final, err := coordinator.Renew(ctx, leaseC)
	if err != nil {
		t.Fatalf("renew C: %v", err)
	}
	if final.Total != 2 || final.Ready != 2 || final.Draining != 0 {
		t.Fatalf("final snapshot = %+v, want two READY", final)
	}
}

func TestRedisCoordinatorTargetOneAllowsOneHandoffMember(t *testing.T) {
	coordinator, _ := newTestRedisCoordinator(t, 1, DefaultLeaseTTL)
	ctx := context.Background()
	const group = "group-target-one"

	leaseA, _ := claimLease(t, coordinator, group, "node-a-g1")
	leaseA, steady := markReady(t, coordinator, leaseA)
	if steady.Total != 1 || steady.Ready != 1 {
		t.Fatalf("target-one steady snapshot = %+v", steady)
	}
	if _, _, acquired, err := coordinator.Claim(ctx, group, "node-b-g1"); err != nil || acquired {
		t.Fatalf("second steady claim: acquired=%v err=%v", acquired, err)
	}

	leaseA, _, err := coordinator.BeginDrain(ctx, leaseA)
	if err != nil {
		t.Fatalf("begin drain A: %v", err)
	}
	leaseB, handoff := claimLease(t, coordinator, group, "node-b-g1")
	if handoff.Total != 2 || handoff.Draining != 1 || handoff.Connecting != 1 {
		t.Fatalf("target-one handoff snapshot = %+v", handoff)
	}
	_, replacement := markReady(t, coordinator, leaseB)
	if replacement.Total != 2 || replacement.Ready != 1 || !replacement.ReadyTargetMet() {
		t.Fatalf("target-one replacement snapshot = %+v", replacement)
	}
	if _, _, acquired, err := coordinator.Claim(ctx, group, "node-c-g1"); err != nil || acquired {
		t.Fatalf("third target-one claim: acquired=%v err=%v", acquired, err)
	}
}

func TestRedisCoordinatorGroupsAreIndependent(t *testing.T) {
	coordinator, _ := newTestRedisCoordinator(t, 1, DefaultLeaseTTL)
	claimLease(t, coordinator, "group-one", "node-a-g1")
	if _, snapshot := claimLease(t, coordinator, "group-two", "node-a-g2"); snapshot.Total != 1 {
		t.Fatalf("second group snapshot = %+v, want its own single member", snapshot)
	}
}

func TestRedisCoordinatorTokenAndStateFencing(t *testing.T) {
	coordinator, _ := newTestRedisCoordinator(t, 1, DefaultLeaseTTL)
	ctx := context.Background()
	const group = "group-fencing"

	lease, _ := claimLease(t, coordinator, group, "node-a-sensitive-token")
	lease, _ = markReady(t, coordinator, lease)
	forged := Lease{Group: group, Token: "forged-token", State: StateReady}
	if err := coordinator.Release(ctx, forged); err != nil {
		t.Fatalf("forged release should be a fenced no-op: %v", err)
	}
	if _, err := coordinator.Renew(ctx, lease); err != nil {
		t.Fatalf("real owner lost lease after forged release: %v", err)
	}

	wrongState := lease
	wrongState.State = StateConnecting
	if _, err := coordinator.Renew(ctx, wrongState); !errors.Is(err, ErrLeaseStateMismatch) {
		t.Fatalf("wrong-state renew error = %v, want ErrLeaseStateMismatch", err)
	} else if strings.Contains(err.Error(), lease.Token) {
		t.Fatalf("structured error leaked lease token: %v", err)
	} else {
		var coordErr *CoordinatorError
		if !errors.As(err, &coordErr) || coordErr.Group != group || coordErr.Actual != StateReady || coordErr.Expected != StateConnecting {
			t.Fatalf("structured error = %#v, want group/expected/actual populated", err)
		}
	}

	if err := coordinator.Release(ctx, lease); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if _, err := coordinator.Renew(ctx, lease); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("renew after release error = %v, want ErrLeaseLost", err)
	}
}

func TestRedisCoordinatorExpiredMemberIsPrunedUsingRedisTime(t *testing.T) {
	const ttl = 80 * time.Millisecond
	coordinator, _ := newTestRedisCoordinator(t, 1, ttl)
	ctx := context.Background()
	const group = "group-expiry"

	leaseA, snapshot := claimLease(t, coordinator, group, "node-a-g1")
	if remaining := snapshot.RemainingTTL(); remaining <= 0 || remaining > ttl {
		t.Fatalf("claim remaining TTL = %s, want (0, %s]", remaining, ttl)
	}
	time.Sleep(2 * ttl)

	leaseB, afterExpiry := claimLease(t, coordinator, group, "node-b-g1")
	if afterExpiry.Total != 1 || afterExpiry.Connecting != 1 {
		t.Fatalf("post-expiry snapshot = %+v", afterExpiry)
	}
	if _, err := coordinator.Renew(ctx, leaseA); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired A renew error = %v, want ErrLeaseLost", err)
	}
	if err := coordinator.Release(ctx, leaseB); err != nil {
		t.Fatalf("release B: %v", err)
	}
}

func TestRedisCoordinatorConcurrentClaimsAreAtomic(t *testing.T) {
	coordinator, _ := newTestRedisCoordinator(t, 2, DefaultLeaseTTL)
	const group = "group-concurrent-claims"

	const contenders = 24
	type result struct {
		lease    Lease
		acquired bool
		err      error
	}
	results := make(chan result, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			lease, _, acquired, err := coordinator.Claim(context.Background(), group, fmt.Sprintf("contender-%02d", index))
			results <- result{lease: lease, acquired: acquired, err: err}
		}(i)
	}
	wg.Wait()
	close(results)

	var acquired []Lease
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent claim: %v", result.err)
		}
		if result.acquired {
			acquired = append(acquired, result.lease)
		}
	}
	if len(acquired) != 2 {
		t.Fatalf("acquired count = %d, want exactly 2", len(acquired))
	}
	for _, lease := range acquired {
		if _, _, err := coordinator.MarkReady(context.Background(), lease); err != nil {
			t.Fatalf("mark acquired lease ready: %v", err)
		}
	}
}

func TestRedisCoordinatorConcurrentBeginDrainAllowsOne(t *testing.T) {
	coordinator, _ := newTestRedisCoordinator(t, 2, DefaultLeaseTTL)
	const group = "group-concurrent-drain"
	leaseA, _ := claimLease(t, coordinator, group, "node-a-g1")
	leaseA, _ = markReady(t, coordinator, leaseA)
	leaseB, _ := claimLease(t, coordinator, group, "node-b-g1")
	leaseB, _ = markReady(t, coordinator, leaseB)

	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	for _, lease := range []Lease{leaseA, leaseB} {
		go func(lease Lease) {
			<-start
			_, _, err := coordinator.BeginDrain(context.Background(), lease)
			errorsCh <- err
		}(lease)
	}
	close(start)

	var successes, drainConflicts int
	for i := 0; i < 2; i++ {
		err := <-errorsCh
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrDrainInProgress):
			drainConflicts++
		default:
			t.Fatalf("begin drain error = %v", err)
		}
	}
	if successes != 1 || drainConflicts != 1 {
		t.Fatalf("begin drain results: successes=%d conflicts=%d", successes, drainConflicts)
	}
}

func TestRedisCoordinatorRejectsCorruptSharedState(t *testing.T) {
	coordinator, client := newTestRedisCoordinator(t, 2, DefaultLeaseTTL)
	ctx := context.Background()
	const group = "group-corrupt"
	keys := coordinator.keys(group)
	redisNow, err := client.Time(ctx).Result()
	if err != nil {
		t.Fatalf("Redis TIME: %v", err)
	}
	if err := client.HSet(ctx, keys[0], "bad-member", "UNKNOWN").Err(); err != nil {
		t.Fatalf("seed bad HASH state: %v", err)
	}
	if err := client.ZAdd(ctx, keys[1], redis.Z{Score: float64(redisNow.Add(time.Minute).UnixMilli()), Member: "bad-member"}).Err(); err != nil {
		t.Fatalf("seed bad ZSET state: %v", err)
	}

	if _, _, _, err := coordinator.Claim(ctx, group, "probe"); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("corrupt-state claim error = %v, want ErrCorruptState", err)
	}
}

func TestSnapshotHelpers(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if (Snapshot{TargetReady: 1, Ready: 1}).ReadyTargetMet() != true {
		t.Fatal("ready target should be met")
	}
	if (Snapshot{TargetReady: 0, Ready: 1}).ReadyTargetMet() {
		t.Fatal("zero target must never be met")
	}
	if got := (Snapshot{ServerNow: now, ExpiresAt: now.Add(3 * time.Second)}).RemainingTTL(); got != 3*time.Second {
		t.Fatalf("remaining TTL = %s", got)
	}
	if got := (Snapshot{ServerNow: now, ExpiresAt: now.Add(-time.Second)}).RemainingTTL(); got != 0 {
		t.Fatalf("expired remaining TTL = %s", got)
	}
	if got := (Snapshot{}).RemainingTTL(); got != 0 {
		t.Fatalf("zero snapshot remaining TTL = %s", got)
	}
}
