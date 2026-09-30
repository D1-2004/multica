package redisstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/pkg/dws"
	"github.com/multica-ai/multica/server/pkg/dws/events"
)

// Runs against a real Redis: DWS_RPC_TEST_REDIS_URL=redis://localhost:6379/15
func TestStoreAgainstRedis(t *testing.T) {
	url := os.Getenv("DWS_RPC_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set DWS_RPC_TEST_REDIS_URL to run against Redis")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{Redis: redis.NewClient(opts), Prefix: fmt.Sprintf("dws-for-tag-test:%d:", time.Now().UnixNano())}
	ctx := context.Background()

	if ok, _ := s.Lease(ctx, "a", "r1", time.Second); !ok {
		t.Fatal("free lease")
	}
	if ok, _ := s.Lease(ctx, "a", "r2", time.Second); ok {
		t.Fatal("a held lease must not move")
	}
	if ok, _ := s.Lease(ctx, "a", "r1", time.Second); !ok {
		t.Fatal("the holder renews")
	}
	_ = s.Release(ctx, "a", "r2") // not the holder: no effect
	if ok, _ := s.Lease(ctx, "a", "r2", time.Second); ok {
		t.Fatal("release by a non-holder must not free the lease")
	}
	_ = s.Release(ctx, "a", "r1")
	if ok, _ := s.Lease(ctx, "a", "r2", time.Second); !ok {
		t.Fatal("released lease")
	}

	if seen, _ := s.Handled(ctx, "a", "e1"); seen {
		t.Fatal("not handled yet")
	}
	_ = s.MarkHandled(ctx, "a", "e1", time.Minute)
	if seen, _ := s.Handled(ctx, "a", "e1"); !seen {
		t.Fatal("handled")
	}

	_ = s.SetSubscriptions(ctx, "a", []dws.Subscription{{ID: "s1", EventKey: dws.EventIMGroup}})
	if ids, _ := s.SubscribedIdentities(ctx); len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("ids = %v", ids)
	}
	_ = s.SetSubscriptions(ctx, "a", nil)
	if ids, _ := s.SubscribedIdentities(ctx); len(ids) != 0 {
		t.Fatalf("ids = %v", ids)
	}
	_ = s.SetStatus(ctx, events.Status{Identity: "a", State: events.StateConnected})
	if sts, _ := s.Statuses(ctx); len(sts) != 1 || sts[0].State != events.StateConnected {
		t.Fatalf("statuses = %+v", sts)
	}
}
