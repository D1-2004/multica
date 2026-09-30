package dws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// poolWorld is a gateway and an OAuth endpoint where every minted code
// exchanges for a distinct token; tokens listed in dead are rejected.
type poolWorld struct {
	mu     sync.Mutex
	mints  map[string]int
	dead   map[string]bool
	cfg    Config
	failOn string
}

func newPoolWorld(t *testing.T) *poolWorld {
	w := &poolWorld{mints: map[string]int{}, dead: map[string]bool{}}
	_, gwURL := startGateway(t, func(tool string, _ map[string]any, token string) (int, string) {
		w.mu.Lock()
		dead := w.dead[token]
		w.mu.Unlock()
		if dead {
			return 401, "expired"
		}
		if tool == "get_current_user_profile" {
			return ok(meResult)
		}
		return ok(`{}`)
	})
	_, authURL := startOAuth(t, func(path string, body map[string]string) (int, string) {
		if path == "/oauth2/refreshToken" {
			return 200, `{"errorCode":"UNAUTHORIZED","success":false}`
		}
		return 200, `{"accessToken":"uat-` + body["authCode"] + `","refreshToken":"rt","expiresIn":7200}`
	})
	w.cfg = Config{GatewayURL: gwURL, AuthURL: authURL}
	return w
}

func (w *poolWorld) mint(_ context.Context, key string) (AuthCode, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if key == w.failOn {
		return AuthCode{}, errors.New("hsf unavailable")
	}
	w.mints[key]++
	time.Sleep(5 * time.Millisecond) // widen the window for concurrent callers
	return AuthCode{Code: fmt.Sprintf("%s-%d", key, w.mints[key]), ClientID: "client"}, nil
}

func TestPoolInitializesOncePerIdentity(t *testing.T) {
	w := newPoolWorld(t)
	p := &Pool{Config: w.cfg, Mint: w.mint}
	var wg sync.WaitGroup
	clients := make([]*Client, 20)
	for i := range clients {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := p.Client(context.Background(), "agent-a")
			if err != nil {
				t.Error(err)
			}
			clients[i] = c
		}(i)
	}
	wg.Wait()
	if w.mints["agent-a"] != 1 {
		t.Fatalf("mints = %d, want 1", w.mints["agent-a"])
	}
	for _, c := range clients {
		if c != clients[0] {
			t.Fatal("every caller must get the same client")
		}
	}
}

func TestPoolReinitializesAfterTheSessionExpires(t *testing.T) {
	w := newPoolWorld(t)
	p := &Pool{Config: w.cfg, Mint: w.mint}
	ctx := context.Background()
	first, _ := p.Client(ctx, "agent-a")
	w.mu.Lock()
	w.dead["uat-agent-a-1"] = true
	w.mu.Unlock()
	// The rejected token's refresh is refused too: the session is over. The
	// pool never replays the caller's work; the next Client() rebuilds.
	if _, err := first.Call(ctx, ServerIM, "x", nil); !errors.Is(err, ErrSessionExpired) || first.Alive() {
		t.Fatalf("err=%v alive=%v", err, first.Alive())
	}
	second, err := p.Client(ctx, "agent-a")
	if err != nil || second == first || !second.Alive() || w.mints["agent-a"] != 2 {
		t.Fatalf("err=%v same=%v mints=%d", err, second == first, w.mints["agent-a"])
	}
	if _, err := second.Call(ctx, ServerIM, "x", nil); err != nil {
		t.Fatal(err)
	}
}

func TestPoolDoesNotCacheInitFailures(t *testing.T) {
	w := newPoolWorld(t)
	w.failOn = "agent-a"
	p := &Pool{Config: w.cfg, Mint: w.mint}
	_, err := p.Client(context.Background(), "agent-a")
	var ie *InitError
	if !errors.As(err, &ie) || ie.Stage != StageMint || !errors.Is(err, ErrMintFailed) {
		t.Fatalf("err = %v", err)
	}
	w.failOn = ""
	if _, err := p.Client(context.Background(), "agent-a"); err != nil {
		t.Fatalf("the next call must mint again: %v", err)
	}
}

func TestPoolNeverEvictsAnInitializingEntry(t *testing.T) {
	w := newPoolWorld(t)
	release := make(chan struct{})
	started := make(chan struct{})
	p := &Pool{Config: w.cfg, MaxClients: 1, Mint: func(ctx context.Context, key string) (AuthCode, error) {
		if key == "slow" {
			close(started)
			<-release
		}
		return w.mint(ctx, key)
	}}
	done := make(chan *Client)
	go func() {
		c, _ := p.Client(context.Background(), "slow")
		done <- c
	}()
	<-started
	if _, err := p.Client(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	close(release)
	slow := <-done
	if again, _ := p.Client(context.Background(), "slow"); again != slow {
		t.Fatal("an initializing entry was evicted; its identity would be minted twice")
	}
}

func TestAgentIdentityRedeemsAnAuthCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/agent-identity/v1/credentials/redeem" || r.Header.Get("Authorization") != "Bearer ctx" ||
			r.Header.Get("X-Multica-Sandbox-Relay-Token") != "relay" || string(body) != `{"identityKey":"dws","credentialType":"DWS_AUTH_CODE"}` {
			t.Errorf("redeem request %s %v %s", r.URL.Path, r.Header, body)
		}
		_, _ = io.WriteString(w, `{"ok":true,"identity":{"key":"dws","type":"DWS_UID","uid":"42","clientId":"client-1"},"credential":{"type":"DWS_AUTH_CODE","authCode":"code-1"}}`)
	}))
	defer srv.Close()
	code, err := AgentIdentity{BaseURL: srv.URL, Header: http.Header{"X-Multica-Sandbox-Relay-Token": {"relay"}}}.Redeem(context.Background(), "ctx")
	if err != nil || code.Code != "code-1" || code.ClientID != "client-1" {
		t.Fatalf("code=%+v err=%v", code, err)
	}
}

func TestAgentIdentityRejections(t *testing.T) {
	for name, body := range map[string]string{
		"not ok":          `{"ok":false,"errorCode":"CONTEXT_EXPIRED"}`,
		"wrong identity":  `{"ok":true,"identity":{"key":"github","type":"DWS_UID","uid":"1","clientId":"c"},"credential":{"type":"DWS_AUTH_CODE","authCode":"x"}}`,
		"missing code":    `{"ok":true,"identity":{"key":"dws","type":"DWS_UID","uid":"1","clientId":"c"},"credential":{"type":"DWS_AUTH_CODE"}}`,
		"non-numeric uid": `{"ok":true,"identity":{"key":"dws","type":"DWS_UID","uid":"abc","clientId":"c"},"credential":{"type":"DWS_AUTH_CODE","authCode":"x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
			defer srv.Close()
			code, err := AgentIdentity{BaseURL: srv.URL}.Redeem(context.Background(), "ctx")
			if err == nil || strings.Contains(err.Error(), "x\"") || code.Code != "" {
				t.Fatalf("code=%+v err=%v", code, err)
			}
		})
	}
}
