package dws

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sharedFixture is one DingTalk gateway and OAuth server seen by several
// "replicas" (Pools) that share a token store.
type sharedFixture struct {
	clk       *clock
	gwURL     string
	oauthURL  string
	mints     atomic.Int32
	refreshes atomic.Int32
	rejectRT  atomic.Bool
	store     *MemoryTokenStore
	mu        sync.Mutex
	live      map[string]bool // access tokens the gateway accepts
	nextGen   int
}

func newSharedFixture(t *testing.T) *sharedFixture {
	f := &sharedFixture{clk: &clock{t: time.Now()}, store: &MemoryTokenStore{}, live: map[string]bool{}}
	_, f.gwURL = startGateway(t, func(_ string, _ map[string]any, token string) (int, string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.live[token] {
			return 401, `{"error":"token rejected"}`
		}
		return ok(`{}`)
	})
	_, f.oauthURL = startOAuth(t, func(path string, body map[string]string) (int, string) {
		if body["refreshToken"] != "" {
			f.refreshes.Add(1)
			if f.rejectRT.Load() {
				return 400, `{"errorCode":"invalid_grant"}`
			}
		}
		return 200, f.issue()
	})
	return f
}

func (f *sharedFixture) issue() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextGen++
	at := fmt.Sprintf("uat-%d", f.nextGen)
	f.live[at] = true
	return fmt.Sprintf(`{"accessToken":%q,"refreshToken":"rt-%d","expiresIn":3600}`, at, f.nextGen)
}

func (f *sharedFixture) replica() *Pool {
	return &Pool{
		Config: Config{GatewayURL: f.gwURL, AuthURL: f.oauthURL, SkipVerify: true, Now: f.clk.now},
		Mint: func(context.Context, string) (AuthCode, error) {
			f.mints.Add(1)
			return AuthCode{Code: "code", ClientID: "client-1"}, nil
		},
		Store: f.store,
	}
}

func callOK(t *testing.T, p *Pool) *Client {
	t.Helper()
	c, err := p.Client(context.Background(), "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(context.Background(), ServerIM, "x", nil); err != nil {
		t.Fatal(err)
	}
	return c
}

// A second replica uses the identity's stored token instead of minting.
func TestReplicasShareOneToken(t *testing.T) {
	f := newSharedFixture(t)
	a, b := f.replica(), f.replica()
	ca, cb := callOK(t, a), callOK(t, b)
	if f.mints.Load() != 1 || ca.Token().AccessToken != cb.Token().AccessToken {
		t.Fatalf("mints=%d tokens %s / %s", f.mints.Load(), ca.Token().AccessToken, cb.Token().AccessToken)
	}
}

// Only one replica refreshes; the other adopts the result instead of
// spending the rotated refresh token again.
func TestOneReplicaRefreshesForAll(t *testing.T) {
	f := newSharedFixture(t)
	a, b := f.replica(), f.replica()
	callOK(t, a)
	callOK(t, b)
	f.clk.add(56 * time.Minute) // inside the refresh skew
	ca := callOK(t, a)
	cb := callOK(t, b)
	if f.refreshes.Load() != 1 || ca.Token().AccessToken != cb.Token().AccessToken || f.mints.Load() != 1 {
		t.Fatalf("refreshes=%d mints=%d tokens %s / %s", f.refreshes.Load(), f.mints.Load(), ca.Token().AccessToken, cb.Token().AccessToken)
	}
}

// Replicas refreshing at once still refresh once.
func TestConcurrentReplicasRefreshOnce(t *testing.T) {
	f := newSharedFixture(t)
	pools := []*Pool{f.replica(), f.replica(), f.replica(), f.replica()}
	for _, p := range pools {
		callOK(t, p)
	}
	f.clk.add(56 * time.Minute)
	var wg sync.WaitGroup
	for _, p := range pools {
		wg.Add(1)
		go func(p *Pool) {
			defer wg.Done()
			c, _ := p.Client(context.Background(), "agent-1")
			_, _ = c.Call(context.Background(), ServerIM, "x", nil)
		}(p)
	}
	wg.Wait()
	if f.refreshes.Load() != 1 {
		t.Fatalf("refreshes=%d", f.refreshes.Load())
	}
}

// A refused refresh token clears the stored token, so the next use mints.
func TestARefusedRefreshMintsAgain(t *testing.T) {
	f := newSharedFixture(t)
	a := f.replica()
	callOK(t, a)
	f.rejectRT.Store(true)
	f.clk.add(61 * time.Minute) // past expiry
	c, _ := a.Client(context.Background(), "agent-1")
	if _, err := c.Call(context.Background(), ServerIM, "x", nil); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expired session: %v", err)
	}
	if _, ok, _ := f.store.Load(context.Background(), "agent-1"); ok {
		t.Fatal("a dead token stayed shared")
	}
	f.rejectRT.Store(false)
	callOK(t, f.replica())
	if f.mints.Load() != 2 {
		t.Fatalf("mints=%d", f.mints.Load())
	}
}

// ClientWith mints with the caller's function, and only when needed.
func TestClientWithMintsOnlyWithoutAStoredToken(t *testing.T) {
	f := newSharedFixture(t)
	callOK(t, f.replica())
	used := false
	_, err := f.replica().ClientWith(context.Background(), "agent-1", func(context.Context) (AuthCode, error) {
		used = true
		return AuthCode{}, errors.New("unused")
	})
	if err != nil || used {
		t.Fatalf("stored token ignored: %v, minted=%v", err, used)
	}
}
