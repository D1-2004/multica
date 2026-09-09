package service

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func lifecycleRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	address := os.Getenv("REDIS_TEST_URL")
	if address == "" {
		t.Skip("REDIS_TEST_URL not set")
	}
	options, err := redis.ParseURL(address)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal("test Redis is unavailable")
	}
	return client
}

func TestFCE2BCheckLeaseCoordinatesReplicas(t *testing.T) {
	client := lifecycleRedisClient(t)
	coordinator := newFCE2BCheckCoordinator(client, uuid.NewString(), time.Second, time.Second)
	ctx := context.Background()
	t.Cleanup(func() { client.Del(ctx, coordinator.keys...).Err() })
	var wg sync.WaitGroup
	winners := make(chan string, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, acquired, err := coordinator.claim(ctx)
			if err != nil {
				t.Error(err)
			} else if acquired {
				winners <- token
			}
		}()
	}
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("concurrent claim winners = %d, want 1", len(winners))
	}
	winner := <-winners
	if err := coordinator.finish(ctx, winner); err != nil {
		t.Fatal(err)
	}
	if _, acquired, err := coordinator.claim(ctx); err != nil || acquired {
		t.Fatalf("completed round ran again immediately: acquired=%v err=%v", acquired, err)
	}
	// Advancing only the scheduling key makes the next round due.
	if err := client.Set(ctx, coordinator.keys[1], 0, time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if _, acquired, err := coordinator.claim(ctx); err != nil || !acquired {
		t.Fatalf("next round was not claimable: acquired=%v err=%v", acquired, err)
	}
}

func TestFCE2BCheckLeaseRejectsExpiredOwner(t *testing.T) {
	client := lifecycleRedisClient(t)
	coordinator := newFCE2BCheckCoordinator(client, uuid.NewString(), time.Second, time.Second)
	ctx := context.Background()
	t.Cleanup(func() { client.Del(ctx, coordinator.keys...).Err() })
	oldToken, acquired, err := coordinator.claim(ctx)
	if err != nil || !acquired {
		t.Fatal("initial claim failed", err)
	}
	if err := client.PExpire(ctx, coordinator.keys[0], time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	newToken, acquired, err := coordinator.claim(ctx)
	if err != nil || !acquired {
		t.Fatal("expired lease was not taken over", err)
	}
	if err := coordinator.renew(ctx, oldToken); err == nil {
		t.Fatal("expired owner renewed replacement lease")
	}
	if err := coordinator.finish(ctx, oldToken); err == nil {
		t.Fatal("expired owner finished replacement round")
	}
	if err := coordinator.renew(ctx, newToken); err != nil {
		t.Fatal("replacement owner lost its lease", err)
	}
}
