package redisstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// xorSealer stands in for an AEAD; it only has to make plaintext unreadable.
type xorSealer struct{ key byte }

func (x xorSealer) Seal(p []byte) ([]byte, error) {
	out := append([]byte("sealed:"), p...)
	for i := 7; i < len(out); i++ {
		out[i] ^= x.key
	}
	return out, nil
}

func (x xorSealer) Open(s []byte) ([]byte, error) {
	if !bytes.HasPrefix(s, []byte("sealed:")) {
		return nil, errors.New("not sealed")
	}
	out := append([]byte(nil), s[7:]...)
	for i := range out {
		out[i] ^= x.key
	}
	return out, nil
}

func testTokenStore(t *testing.T) (*TokenStore, *redis.Client) {
	url := os.Getenv("DWS_RPC_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set DWS_RPC_TEST_REDIS_URL to run against Redis")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	return &TokenStore{Redis: rdb, Sealer: xorSealer{key: 0x5a}, Prefix: fmt.Sprintf("dws-for-tag-test:%d:", time.Now().UnixNano()), LockPoll: 10 * time.Millisecond}, rdb
}

func TestTokenStoreKeepsTokensSealed(t *testing.T) {
	s, rdb := testTokenStore(t)
	ctx := context.Background()
	if _, ok, err := s.Load(ctx, "agent-1"); ok || err != nil {
		t.Fatalf("empty store: %v %v", ok, err)
	}
	tok := dws.Token{AccessToken: "uat-secret", RefreshToken: "rt-secret", ClientID: "client-1",
		ExpiresAt: time.Unix(1_800_000_000, 0).UTC(), CorpID: "corp-1"}
	if err := s.Save(ctx, "agent-1", tok); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Load(ctx, "agent-1")
	if !ok || err != nil || got != tok {
		t.Fatalf("load = %v %v %v", got, ok, err)
	}
	keys, _ := rdb.Keys(ctx, s.Prefix+"*").Result()
	for _, k := range keys {
		raw, _ := rdb.Get(ctx, k).Result()
		if strings.Contains(raw, "uat-secret") || strings.Contains(raw, "rt-secret") || strings.Contains(k, "agent-1") {
			t.Fatalf("plaintext in Redis: %s = %q", k, raw)
		}
	}
	if ttl, _ := rdb.PTTL(ctx, keys[0]).Result(); ttl <= 0 {
		t.Fatalf("token without TTL: %v", ttl)
	}
	other := &TokenStore{Redis: rdb, Sealer: xorSealer{key: 0x11}, Prefix: s.Prefix}
	if _, ok, _ := other.Load(ctx, "agent-1"); ok {
		t.Fatal("a token sealed with another key was read")
	}
	if err := s.Delete(ctx, "agent-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Load(ctx, "agent-1"); ok {
		t.Fatal("deleted token still loads")
	}
	if err := (&TokenStore{Redis: rdb}).Save(ctx, "agent-1", tok); err == nil {
		t.Fatal("a store without a Sealer wrote a token")
	}
}

func TestTokenStoreLockIsExclusiveAndFenced(t *testing.T) {
	s, _ := testTokenStore(t)
	ctx := context.Background()
	var inside, max atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := s.Lock(ctx, "agent-1", 5*time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			if n := inside.Add(1); n > max.Load() {
				max.Store(n)
			}
			time.Sleep(20 * time.Millisecond)
			inside.Add(-1)
			unlock()
		}()
	}
	wg.Wait()
	if max.Load() != 1 {
		t.Fatalf("%d holders at once", max.Load())
	}
	// An expired holder's unlock does not free the next holder's lock.
	stale, _ := s.Lock(ctx, "agent-2", 30*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	current, err := s.Lock(ctx, "agent-2", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stale()
	waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := s.Lock(waitCtx, "agent-2", time.Second); err == nil {
		t.Fatal("a stale unlock released the current holder")
	}
	current()
}

// Two "replicas" share one token lineage through Redis.
func TestPoolsShareATokenThroughRedis(t *testing.T) {
	s, _ := testTokenStore(t)
	ctx := context.Background()
	if err := s.Save(ctx, "agent-1", dws.Token{AccessToken: "uat-1", RefreshToken: "rt-1", ClientID: "client-1",
		ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	minted := false
	p := &dws.Pool{Config: dws.Config{SkipVerify: true}, Store: s,
		Mint: func(context.Context, string) (dws.AuthCode, error) {
			minted = true
			return dws.AuthCode{}, errors.New("no")
		}}
	c, err := p.Client(ctx, "agent-1")
	if err != nil || minted || c.Token().AccessToken != "uat-1" {
		t.Fatalf("client = %v, %v, minted=%v", c, err, minted)
	}
}
