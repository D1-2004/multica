package engine

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
)

const streamCoordinatorRedisTestDB = 15

func newStreamCoordinatorRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("REDIS_TEST_URL")
	if rawURL == "" {
		t.Skip("REDIS_TEST_URL not set")
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("parse REDIS_TEST_URL: %v", err)
	}
	opts.DB = streamCoordinatorRedisTestDB
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

func newTestStreamCoordinator(t *testing.T, target int, ttl time.Duration) (*RedisStreamCoordinator, *redis.Client) {
	t.Helper()
	client := newStreamCoordinatorRedisClient(t)
	coordinator, err := NewRedisStreamCoordinator(client, RedisStreamCoordinatorConfig{
		TargetReady: target,
		LeaseTTL:    ttl,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	return coordinator, client
}

func streamCoordinatorUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("scan UUID %q: %v", value, err)
	}
	return id
}

func claimStreamLease(t *testing.T, coordinator StreamCoordinator, installationID pgtype.UUID, token string) (StreamLease, StreamLeaseSnapshot) {
	t.Helper()
	lease, snapshot, acquired, err := coordinator.Claim(context.Background(), installationID, token)
	if err != nil {
		t.Fatalf("claim %s: %v", token, err)
	}
	if !acquired {
		t.Fatalf("claim %s: capacity unexpectedly unavailable: %+v", token, snapshot)
	}
	return lease, snapshot
}

func markStreamReady(t *testing.T, coordinator StreamCoordinator, lease StreamLease) (StreamLease, StreamLeaseSnapshot) {
	t.Helper()
	readyLease, snapshot, err := coordinator.MarkReady(context.Background(), lease)
	if err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	return readyLease, snapshot
}

func TestNewRedisStreamCoordinatorValidatesConfig(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })

	for _, target := range []int{-1, 0, 3} {
		_, err := NewRedisStreamCoordinator(client, RedisStreamCoordinatorConfig{TargetReady: target})
		if !errors.Is(err, ErrStreamCoordinatorConfig) {
			t.Errorf("target %d: error = %v, want ErrStreamCoordinatorConfig", target, err)
		}
	}
	if _, err := NewRedisStreamCoordinator(nil, RedisStreamCoordinatorConfig{TargetReady: 2}); !errors.Is(err, ErrStreamCoordinatorConfig) {
		t.Errorf("nil Redis: error = %v, want ErrStreamCoordinatorConfig", err)
	}
	if _, err := NewRedisStreamCoordinator(client, RedisStreamCoordinatorConfig{TargetReady: 2, LeaseTTL: time.Nanosecond}); !errors.Is(err, ErrStreamCoordinatorConfig) {
		t.Errorf("sub-millisecond TTL: error = %v, want ErrStreamCoordinatorConfig", err)
	}

	coordinator, err := NewRedisStreamCoordinator(client, RedisStreamCoordinatorConfig{TargetReady: 2})
	if err != nil {
		t.Fatalf("default config: %v", err)
	}
	if coordinator.TargetReady() != 2 || coordinator.LeaseTTL() != DefaultRedisStreamLeaseTTL {
		t.Fatalf("defaults = target %d TTL %s", coordinator.TargetReady(), coordinator.LeaseTTL())
	}
}

func TestRedisStreamCoordinatorTargetTwoRollingHandoffCapsAtThree(t *testing.T) {
	coordinator, _ := newTestStreamCoordinator(t, 2, DefaultRedisStreamLeaseTTL)
	ctx := context.Background()
	installationID := streamCoordinatorUUID(t, "11111111-2222-3333-4444-555555555555")

	leaseA, _ := claimStreamLease(t, coordinator, installationID, "node-a-g1")
	leaseA, _ = markStreamReady(t, coordinator, leaseA)
	leaseB, _ := claimStreamLease(t, coordinator, installationID, "node-b-g1")
	leaseB, steady := markStreamReady(t, coordinator, leaseB)
	if steady.Total != 2 || steady.Ready != 2 || steady.Connecting != 0 || steady.Draining != 0 {
		t.Fatalf("steady snapshot = %+v, want two READY", steady)
	}
	if ttl := steady.RemainingTTL(); ttl <= 0 || ttl > DefaultRedisStreamLeaseTTL {
		t.Fatalf("remaining TTL = %s, want (0, %s]", ttl, DefaultRedisStreamLeaseTTL)
	}

	if _, snapshot, acquired, err := coordinator.Claim(ctx, installationID, "node-c-g1"); err != nil || acquired {
		t.Fatalf("third steady claim: acquired=%v err=%v snapshot=%+v", acquired, err, snapshot)
	}

	leaseA, draining, err := coordinator.BeginDrain(ctx, leaseA)
	if err != nil {
		t.Fatalf("begin drain A: %v", err)
	}
	if draining.Total != 2 || draining.Ready != 1 || draining.Draining != 1 {
		t.Fatalf("draining snapshot = %+v", draining)
	}
	if _, _, err := coordinator.BeginDrain(ctx, leaseB); !errors.Is(err, ErrStreamDrainInProgress) {
		t.Fatalf("second drain error = %v, want ErrStreamDrainInProgress", err)
	}

	leaseC, connecting := claimStreamLease(t, coordinator, installationID, "node-c-g1")
	if connecting.Total != 3 || connecting.Ready != 1 || connecting.Connecting != 1 || connecting.Draining != 1 {
		t.Fatalf("handoff connecting snapshot = %+v", connecting)
	}
	if _, snapshot, acquired, err := coordinator.Claim(ctx, installationID, "node-d-g1"); err != nil || acquired || snapshot.Total != 3 {
		t.Fatalf("fourth claim: acquired=%v err=%v snapshot=%+v", acquired, err, snapshot)
	}

	leaseC, replacementReady := markStreamReady(t, coordinator, leaseC)
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

func TestRedisStreamCoordinatorTargetOneAllowsOneHandoffMember(t *testing.T) {
	coordinator, _ := newTestStreamCoordinator(t, 1, DefaultRedisStreamLeaseTTL)
	ctx := context.Background()
	installationID := streamCoordinatorUUID(t, "21111111-2222-3333-4444-555555555555")

	leaseA, _ := claimStreamLease(t, coordinator, installationID, "node-a-g1")
	leaseA, steady := markStreamReady(t, coordinator, leaseA)
	if steady.Total != 1 || steady.Ready != 1 {
		t.Fatalf("target-one steady snapshot = %+v", steady)
	}
	if _, _, acquired, err := coordinator.Claim(ctx, installationID, "node-b-g1"); err != nil || acquired {
		t.Fatalf("second steady claim: acquired=%v err=%v", acquired, err)
	}

	leaseA, _, err := coordinator.BeginDrain(ctx, leaseA)
	if err != nil {
		t.Fatalf("begin drain A: %v", err)
	}
	leaseB, handoff := claimStreamLease(t, coordinator, installationID, "node-b-g1")
	if handoff.Total != 2 || handoff.Draining != 1 || handoff.Connecting != 1 {
		t.Fatalf("target-one handoff snapshot = %+v", handoff)
	}
	_, replacement := markStreamReady(t, coordinator, leaseB)
	if replacement.Total != 2 || replacement.Ready != 1 || !replacement.ReadyTargetMet() {
		t.Fatalf("target-one replacement snapshot = %+v", replacement)
	}
	if _, _, acquired, err := coordinator.Claim(ctx, installationID, "node-c-g1"); err != nil || acquired {
		t.Fatalf("third target-one claim: acquired=%v err=%v", acquired, err)
	}
}

func TestRedisStreamCoordinatorTokenAndStateFencing(t *testing.T) {
	coordinator, _ := newTestStreamCoordinator(t, 1, DefaultRedisStreamLeaseTTL)
	ctx := context.Background()
	installationID := streamCoordinatorUUID(t, "31111111-2222-3333-4444-555555555555")

	lease, _ := claimStreamLease(t, coordinator, installationID, "node-a-sensitive-token")
	lease, _ = markStreamReady(t, coordinator, lease)
	forged := StreamLease{InstallationID: installationID, Token: "forged-token", State: StreamLeaseReady}
	if err := coordinator.Release(ctx, forged); err != nil {
		t.Fatalf("forged release should be a fenced no-op: %v", err)
	}
	if _, err := coordinator.Renew(ctx, lease); err != nil {
		t.Fatalf("real owner lost lease after forged release: %v", err)
	}

	wrongState := lease
	wrongState.State = StreamLeaseConnecting
	if _, err := coordinator.Renew(ctx, wrongState); !errors.Is(err, ErrStreamLeaseStateMismatch) {
		t.Fatalf("wrong-state renew error = %v, want ErrStreamLeaseStateMismatch", err)
	} else if strings.Contains(err.Error(), lease.Token) {
		t.Fatalf("structured error leaked lease token: %v", err)
	}

	if err := coordinator.Release(ctx, lease); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if _, err := coordinator.Renew(ctx, lease); !errors.Is(err, ErrStreamLeaseLost) {
		t.Fatalf("renew after release error = %v, want ErrStreamLeaseLost", err)
	}
}

func TestRedisStreamCoordinatorExpiredMemberIsPrunedUsingRedisTime(t *testing.T) {
	const ttl = 80 * time.Millisecond
	coordinator, _ := newTestStreamCoordinator(t, 1, ttl)
	ctx := context.Background()
	installationID := streamCoordinatorUUID(t, "41111111-2222-3333-4444-555555555555")

	leaseA, snapshot := claimStreamLease(t, coordinator, installationID, "node-a-g1")
	if remaining := snapshot.RemainingTTL(); remaining <= 0 || remaining > ttl {
		t.Fatalf("claim remaining TTL = %s, want (0, %s]", remaining, ttl)
	}
	time.Sleep(2 * ttl)

	leaseB, afterExpiry := claimStreamLease(t, coordinator, installationID, "node-b-g1")
	if afterExpiry.Total != 1 || afterExpiry.Connecting != 1 {
		t.Fatalf("post-expiry snapshot = %+v", afterExpiry)
	}
	if _, err := coordinator.Renew(ctx, leaseA); !errors.Is(err, ErrStreamLeaseLost) {
		t.Fatalf("expired A renew error = %v, want ErrStreamLeaseLost", err)
	}
	if err := coordinator.Release(ctx, leaseB); err != nil {
		t.Fatalf("release B: %v", err)
	}
}

func TestRedisStreamCoordinatorConcurrentClaimsAreAtomic(t *testing.T) {
	coordinator, _ := newTestStreamCoordinator(t, 2, DefaultRedisStreamLeaseTTL)
	installationID := streamCoordinatorUUID(t, "51111111-2222-3333-4444-555555555555")

	const contenders = 24
	type result struct {
		lease    StreamLease
		acquired bool
		err      error
	}
	results := make(chan result, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			lease, _, acquired, err := coordinator.Claim(context.Background(), installationID, "contender-"+time.Unix(int64(index), 0).Format("150405"))
			results <- result{lease: lease, acquired: acquired, err: err}
		}(i)
	}
	wg.Wait()
	close(results)

	var acquired []StreamLease
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

func TestRedisStreamCoordinatorConcurrentBeginDrainAllowsOne(t *testing.T) {
	coordinator, _ := newTestStreamCoordinator(t, 2, DefaultRedisStreamLeaseTTL)
	installationID := streamCoordinatorUUID(t, "61111111-2222-3333-4444-555555555555")
	leaseA, _ := claimStreamLease(t, coordinator, installationID, "node-a-g1")
	leaseA, _ = markStreamReady(t, coordinator, leaseA)
	leaseB, _ := claimStreamLease(t, coordinator, installationID, "node-b-g1")
	leaseB, _ = markStreamReady(t, coordinator, leaseB)

	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	for _, lease := range []StreamLease{leaseA, leaseB} {
		go func(lease StreamLease) {
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
		case errors.Is(err, ErrStreamDrainInProgress):
			drainConflicts++
		default:
			t.Fatalf("begin drain error = %v", err)
		}
	}
	if successes != 1 || drainConflicts != 1 {
		t.Fatalf("begin drain results: successes=%d conflicts=%d", successes, drainConflicts)
	}
}

func TestRedisStreamCoordinatorRejectsCorruptSharedState(t *testing.T) {
	coordinator, client := newTestStreamCoordinator(t, 2, DefaultRedisStreamLeaseTTL)
	ctx := context.Background()
	installationID := streamCoordinatorUUID(t, "71111111-2222-3333-4444-555555555555")
	id, err := validateStreamLeaseIdentity(installationID, "probe")
	if err != nil {
		t.Fatalf("validate ID: %v", err)
	}
	keys := coordinator.keys(id)
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

	if _, _, _, err := coordinator.Claim(ctx, installationID, "probe"); !errors.Is(err, ErrStreamCoordinatorCorruptState) {
		t.Fatalf("corrupt-state claim error = %v, want ErrStreamCoordinatorCorruptState", err)
	}
}
