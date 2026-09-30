package dws

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// clock is a settable Config.Now.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func sessionClient(t *testing.T, clk *clock, refresh func(body map[string]string) (int, string), accept func(token string) bool) (*fakeGateway, *fakeOAuth, *Client) {
	t.Helper()
	gw, gwURL := startGateway(t, func(tool string, _ map[string]any, token string) (int, string) {
		if !accept(token) {
			return 400, `{"error":"Missing service_id or access_key"}`
		}
		return ok(`{}`)
	})
	oauth, authURL := startOAuth(t, func(_ string, body map[string]string) (int, string) { return refresh(body) })
	c, err := NewWithToken(context.Background(), Config{GatewayURL: gwURL, AuthURL: authURL, SkipVerify: true, Now: clk.now},
		Token{AccessToken: "uat-1", RefreshToken: "rt-1", ClientID: "client-1", ExpiresAt: clk.now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return gw, oauth, c
}

func TestSessionRefreshesNearExpiryAndRotatesTheRefreshToken(t *testing.T) {
	clk := &clock{t: time.Now()}
	gen := 1
	gw, oauth, c := sessionClient(t, clk, func(body map[string]string) (int, string) {
		gen++
		return 200, `{"accessToken":"uat-` + string(rune('0'+gen)) + `","refreshToken":"rt-` + string(rune('0'+gen)) + `","expiresIn":7200}`
	}, func(string) bool { return true })

	call := func() string {
		if _, err := c.Call(context.Background(), ServerIM, "x", nil); err != nil {
			t.Fatal(err)
		}
		return gw.calls[len(gw.calls)-1].Header.Get("x-user-access-token")
	}
	if tok := call(); tok != "uat-1" || len(oauth.requests) != 0 {
		t.Fatalf("fresh token: %s, refreshes %d", tok, len(oauth.requests))
	}
	clk.add(56 * time.Minute) // inside the 5 minute skew
	if tok := call(); tok != "uat-2" {
		t.Fatalf("after refresh: %s", tok)
	}
	if oauth.paths[0] != "/oauth2/refreshToken" || oauth.requests[0]["refreshToken"] != "rt-1" || oauth.requests[0]["clientId"] != "client-1" {
		t.Fatalf("refresh request %s %v", oauth.paths[0], oauth.requests[0])
	}
	clk.add(2 * time.Hour)
	call()
	if oauth.requests[1]["refreshToken"] != "rt-2" {
		t.Fatal("the rotated refresh token must be used next time")
	}
}

func TestSessionExpiresWhenTheRefreshTokenIsRejected(t *testing.T) {
	clk := &clock{t: time.Now()}
	_, _, c := sessionClient(t, clk, func(map[string]string) (int, string) {
		// Measured on the staging endpoint with a bad refresh token.
		return 200, `{"errorCode":"UNAUTHORIZED","errorMsg":"Token 解密失败","success":false}`
	}, func(string) bool { return true })
	clk.add(2 * time.Hour)
	_, err := c.Call(context.Background(), ServerIM, "x", nil)
	if !errors.Is(err, ErrSessionExpired) || c.Alive() {
		t.Fatalf("err=%v alive=%v", err, c.Alive())
	}
	if _, err := c.Call(context.Background(), ServerIM, "x", nil); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("an expired session stays expired: %v", err)
	}
}

func TestSessionKeepsAValidTokenWhenRefreshIsDown(t *testing.T) {
	clk := &clock{t: time.Now()}
	gw, _, c := sessionClient(t, clk, func(map[string]string) (int, string) { return 503, "busy" }, func(string) bool { return true })
	clk.add(57 * time.Minute) // near expiry, still valid
	if _, err := c.Call(context.Background(), ServerIM, "x", nil); err != nil || !c.Alive() {
		t.Fatalf("err=%v alive=%v", err, c.Alive())
	}
	if gw.calls[0].Header.Get("x-user-access-token") != "uat-1" {
		t.Fatal("the still-valid token should be used")
	}
}

func TestAnUnreadableRefreshAnswerIsTransient(t *testing.T) {
	clk := &clock{t: time.Now()}
	_, _, c := sessionClient(t, clk, func(map[string]string) (int, string) { return 200, "" }, func(string) bool { return true })
	clk.add(57 * time.Minute) // near expiry, still valid
	if _, err := c.Call(context.Background(), ServerIM, "x", nil); err != nil || !c.Alive() {
		t.Fatalf("an empty 200 must not end the session: err=%v alive=%v", err, c.Alive())
	}
}

func TestConcurrentRejectionsShareOneRefresh(t *testing.T) {
	clk := &clock{t: time.Now()}
	var mu sync.Mutex
	refreshes := 0
	_, _, c := sessionClient(t, clk, func(map[string]string) (int, string) {
		mu.Lock()
		refreshes++
		n := refreshes
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		if n > 1 {
			return 503, "busy" // a second, needless refresh would fail the caller
		}
		return 200, `{"accessToken":"uat-2","refreshToken":"rt-2","expiresIn":7200}`
	}, func(token string) bool { return token == "uat-2" })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Call(context.Background(), ServerIM, "x", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes)
	}
}

func TestGatewayRejectionForcesOneRefreshAndRetry(t *testing.T) {
	clk := &clock{t: time.Now()}
	gw, oauth, c := sessionClient(t, clk, func(map[string]string) (int, string) {
		return 200, `{"accessToken":"uat-2","refreshToken":"rt-2","expiresIn":7200}`
	}, func(token string) bool { return token == "uat-2" })
	if _, err := c.Call(context.Background(), ServerIM, "x", nil); err != nil {
		t.Fatal(err)
	}
	if len(gw.calls) != 2 || len(oauth.requests) != 1 || gw.calls[1].Header.Get("x-user-access-token") != "uat-2" {
		t.Fatalf("calls=%d refreshes=%d", len(gw.calls), len(oauth.requests))
	}
}

func TestConcurrentCallsShareOneRefresh(t *testing.T) {
	clk := &clock{t: time.Now()}
	var mu sync.Mutex
	refreshes := 0
	_, _, c := sessionClient(t, clk, func(map[string]string) (int, string) {
		mu.Lock()
		refreshes++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return 200, `{"accessToken":"uat-2","refreshToken":"rt-2","expiresIn":7200}`
	}, func(string) bool { return true })
	clk.add(58 * time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Call(context.Background(), ServerIM, "x", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if refreshes != 1 {
		t.Fatalf("refreshes = %d; a rotating refresh token must be used once", refreshes)
	}
}

func TestATokenWithoutRefreshEndsWithTheSession(t *testing.T) {
	clk := &clock{t: time.Now()}
	_, gwURL := startGateway(t, func(string, map[string]any, string) (int, string) { return ok(`{}`) })
	c, _ := NewWithToken(context.Background(), Config{GatewayURL: gwURL, SkipVerify: true, Now: clk.now},
		Token{AccessToken: "t", ExpiresAt: clk.now().Add(time.Hour)})
	clk.add(59 * time.Minute)
	if _, err := c.Call(context.Background(), ServerIM, "x", nil); err != nil {
		t.Fatalf("still valid: %v", err)
	}
	clk.add(2 * time.Minute)
	if _, err := c.Call(context.Background(), ServerIM, "x", nil); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expired: %v", err)
	}
}

// Token follows the rotation, so a persisted session never keeps a spent
// refresh token, and keeps the organization the exchange reported.
func TestTokenFollowsTheRotation(t *testing.T) {
	clk := &clock{t: time.Now()}
	_, _, c := sessionClient(t, clk, func(map[string]string) (int, string) {
		return 200, `{"accessToken":"uat-2","refreshToken":"rt-2","expiresIn":7200}`
	}, func(string) bool { return true })
	c.token.CorpID = "corp-1"
	c.current.Store(&c.token)
	if got := c.Token(); got.AccessToken != "uat-1" || got.RefreshToken != "rt-1" {
		t.Fatalf("initial token: %v", got)
	}
	clk.add(56 * time.Minute)
	if _, err := c.Call(context.Background(), ServerIM, "x", nil); err != nil {
		t.Fatal(err)
	}
	if got := c.Token(); got.AccessToken != "uat-2" || got.RefreshToken != "rt-2" || got.CorpID != "corp-1" {
		t.Fatalf("after refresh: %v", got)
	}
}

// A token the payload says was refused is refreshed and the call retried
// once; the retried call's business failure comes back as its payload.
func TestCallRawRetriesARefusedTokenOnce(t *testing.T) {
	_, gwURL := startGateway(t, func(_ string, _ map[string]any, token string) (int, string) {
		if token == "uat-1" {
			return 200, toolText(`{"success":false,"errorCode":"USER_TOKEN_ILLEGAL"}`)
		}
		return 200, toolText(`{"success":false,"errorCode":"130003"}`)
	})
	_, authURL := startOAuth(t, func(string, map[string]string) (int, string) {
		return 200, `{"accessToken":"uat-2","refreshToken":"rt-2","expiresIn":7200}`
	})
	c, err := NewWithToken(context.Background(), Config{GatewayURL: gwURL, AuthURL: authURL, SkipVerify: true},
		Token{AccessToken: "uat-1", RefreshToken: "rt-1", ClientID: "client-1", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.CallRaw(context.Background(), ServerIM, "x", nil)
	if err != nil || string(raw) != `{"success":false,"errorCode":"130003"}` || c.Token().AccessToken != "uat-2" {
		t.Fatalf("raw = %s, %v, token %v", raw, err, c.Token())
	}
}
