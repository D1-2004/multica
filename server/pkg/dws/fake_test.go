package dws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeGateway answers tools/call by tool name and records every call.
type fakeGateway struct {
	t       *testing.T
	mu      sync.Mutex
	calls   []fakeCall
	handler func(tool string, args map[string]any, token string) (status int, body string)
}

type fakeCall struct {
	Server string
	Tool   string
	Args   map[string]any
	Header http.Header
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.JSONRPC != "2.0" || req.Method != "tools/call" {
		g.t.Errorf("bad JSON-RPC request: %s", raw)
	}
	server := ""
	for name, id := range serverIDs {
		if r.URL.Path == "/server/"+id {
			server = string(name)
		}
	}
	g.mu.Lock()
	g.calls = append(g.calls, fakeCall{Server: server, Tool: req.Params.Name, Args: req.Params.Arguments, Header: r.Header.Clone()})
	g.mu.Unlock()
	status, body := g.handler(req.Params.Name, req.Params.Arguments, r.Header.Get("x-user-access-token"))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (g *fakeGateway) tools() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.calls))
	for i, c := range g.calls {
		out[i] = c.Tool
	}
	return out
}

func (g *fakeGateway) call(tool string) fakeCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.calls {
		if c.Tool == tool {
			return c
		}
	}
	g.t.Fatalf("tool %s was not called; calls: %v", tool, g.calls)
	return fakeCall{}
}

func startGateway(t *testing.T, handler func(tool string, args map[string]any, token string) (int, string)) (*fakeGateway, string) {
	t.Helper()
	g := &fakeGateway{t: t, handler: handler}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return g, srv.URL
}

// newTestClient is a Client on a fake gateway with a token in hand.
func newTestClient(t *testing.T, handler func(tool string, args map[string]any) (int, string)) (*fakeGateway, *Client) {
	t.Helper()
	g, url := startGateway(t, func(tool string, args map[string]any, _ string) (int, string) { return handler(tool, args) })
	c, err := NewWithToken(context.Background(), Config{GatewayURL: url, SkipVerify: true}, Token{AccessToken: "tok-1"})
	if err != nil {
		t.Fatal(err)
	}
	return g, c
}

// toolText wraps a tool payload the way the gateway does.
func toolText(payload string) string {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{"content": []map[string]string{{"type": "text", "text": payload}}},
	})
	return string(raw)
}

func ok(result string) (int, string) {
	return 200, toolText(`{"success":true,"errorCode":null,"errorMsg":null,"result":` + result + `}`)
}

const meResult = `[{"orgEmployeeModel":{"userId":"42","orgUserName":"数字员工","corpId":"ding1","orgName":"钉钉"}}]`

// fakeOAuth serves the DWS-hosted /oauth2/getToken and /oauth2/refreshToken
// and DingTalk's secret-based token endpoint at /token.
type fakeOAuth struct {
	mu       sync.Mutex
	requests []map[string]string
	paths    []string
	respond  func(path string, body map[string]string) (int, string)
}

func (o *fakeOAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	o.mu.Lock()
	o.requests = append(o.requests, body)
	o.paths = append(o.paths, r.URL.Path)
	o.mu.Unlock()
	status, out := o.respond(r.URL.Path, body)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, out)
}

func startOAuth(t *testing.T, respond func(path string, body map[string]string) (int, string)) (*fakeOAuth, string) {
	t.Helper()
	o := &fakeOAuth{respond: respond}
	srv := httptest.NewServer(o)
	t.Cleanup(srv.Close)
	return o, srv.URL
}

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %q", s)
	}
	return m
}
