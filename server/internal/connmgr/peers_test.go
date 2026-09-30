package connmgr

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestNewRedisPeersValidatesConfig(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })

	cases := map[string]RedisPeersConfig{
		"no prefix":     {Name: "n"},
		"braced prefix": {KeyPrefix: "a:{b}:", Name: "n"},
		"no name":       {KeyPrefix: testKeyPrefix},
		"braced name":   {KeyPrefix: testKeyPrefix, Name: "{n}"},
	}
	for name, cfg := range cases {
		if _, err := NewRedisPeers(client, cfg); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: error = %v, want ErrConfig", name, err)
		}
	}
	if _, err := NewRedisPeers(nil, RedisPeersConfig{KeyPrefix: testKeyPrefix, Name: "n"}); !errors.Is(err, ErrConfig) {
		t.Errorf("nil client: error = %v, want ErrConfig", err)
	}
	peers, err := NewRedisPeers(client, RedisPeersConfig{KeyPrefix: testKeyPrefix, Name: "dws_event_stream"})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if peers.Key() != testKeyPrefix+"nodes:dws_event_stream" {
		t.Fatalf("key = %q", peers.Key())
	}
	ctx := context.Background()
	if err := peers.Heartbeat(ctx, "", 0, time.Second); !errors.Is(err, ErrConfig) {
		t.Errorf("empty node heartbeat error = %v", err)
	}
	if err := peers.Heartbeat(ctx, "node", -1, time.Second); !errors.Is(err, ErrConfig) {
		t.Errorf("negative held heartbeat error = %v", err)
	}
	if err := peers.Heartbeat(ctx, "node", 0, 0); !errors.Is(err, ErrConfig) {
		t.Errorf("zero TTL heartbeat error = %v", err)
	}
}

func TestRedisPeersHeartbeatAliveExpiryAndLeave(t *testing.T) {
	client := newRedisTestClient(t)
	peers, err := NewRedisPeers(client, RedisPeersConfig{KeyPrefix: testKeyPrefix, Name: "peers_test"})
	if err != nil {
		t.Fatalf("new peers: %v", err)
	}
	ctx := context.Background()
	const ttl = 150 * time.Millisecond

	if err := peers.Heartbeat(ctx, "node-a", 3, ttl); err != nil {
		t.Fatalf("heartbeat a: %v", err)
	}
	if err := peers.Heartbeat(ctx, "node-b", 1, ttl); err != nil {
		t.Fatalf("heartbeat b: %v", err)
	}
	loads := aliveMap(t, peers)
	if len(loads) != 2 || loads["node-a"] != 3 || loads["node-b"] != 1 {
		t.Fatalf("alive = %v, want a=3 b=1", loads)
	}
	if pttl, err := client.PTTL(ctx, peers.Key()).Result(); err != nil || pttl <= 0 {
		t.Fatalf("membership record has no expiry: %s %v", pttl, err)
	}

	time.Sleep(80 * time.Millisecond)
	if err := peers.Heartbeat(ctx, "node-a", 4, ttl); err != nil {
		t.Fatalf("heartbeat a again: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // b is past its TTL, a is not
	loads = aliveMap(t, peers)
	if len(loads) != 1 || loads["node-a"] != 4 {
		t.Fatalf("alive after b expired = %v, want only a=4", loads)
	}
	if n, err := client.HLen(ctx, peers.Key()).Result(); err != nil || n != 1 {
		t.Fatalf("expired node not pruned on read: len=%d err=%v", n, err)
	}

	if err := peers.Leave(ctx, "node-a"); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if loads := aliveMap(t, peers); len(loads) != 0 {
		t.Fatalf("alive after leave = %v", loads)
	}
}

func aliveMap(t *testing.T, peers Peers) map[string]int {
	t.Helper()
	loads, err := peers.Alive(context.Background())
	if err != nil {
		t.Fatalf("alive: %v", err)
	}
	out := make(map[string]int, len(loads))
	for _, p := range loads {
		out[p.Node] = p.Held
	}
	return out
}
