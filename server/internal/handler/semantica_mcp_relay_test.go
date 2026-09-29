package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/redis/go-redis/v9"
)

const semanticaTestAgent = "11111111-1111-4111-8111-111111111111"
const semanticaTestTask = "22222222-2222-4222-8222-222222222222"
const semanticaTestWorkspace = "33333333-3333-4333-8333-333333333333"

type semanticaTestTasks struct{ task db.AgentTaskQueue }

func (s semanticaTestTasks) GetAgentTaskInWorkspace(_ context.Context, p db.GetAgentTaskInWorkspaceParams) (db.AgentTaskQueue, error) {
	ws, _ := util.ParseUUID(semanticaTestWorkspace)
	if p.WorkspaceID != ws {
		return db.AgentTaskQueue{}, errors.New("wrong workspace")
	}
	return s.task, nil
}

type semanticaTestRates struct {
	keys []string
	n    int64
	err  error
}

func (s *semanticaTestRates) Eval(ctx context.Context, script string, keys []string, _ ...interface{}) *redis.Cmd {
	s.keys = append(s.keys, keys...)
	cmd := redis.NewCmd(ctx)
	if !strings.Contains(script, "PEXPIRE") {
		cmd.SetErr(errors.New("missing atomic TTL"))
	} else if s.err != nil {
		cmd.SetErr(s.err)
	} else {
		cmd.SetVal(s.n)
	}
	return cmd
}

func semanticaFixture(t *testing.T, upstream http.HandlerFunc) (*Handler, *semanticaTestRates) {
	t.Helper()
	t.Setenv("AONE_ENV_TYPE", "pre")
	server := httptest.NewTLSServer(upstream)
	t.Cleanup(server.Close)
	taskID, _ := util.ParseUUID(semanticaTestTask)
	agentID, _ := util.ParseUUID(semanticaTestAgent)
	rates := &semanticaTestRates{n: 1}
	h := &Handler{SemanticaMCPRelay: &SemanticaMCPRelay{targetAgentID: semanticaTestAgent, endpoint: server.URL, bearer: "upstream-secret", client: server.Client(), redis: rates, environment: "pre", tasks: semanticaTestTasks{db.AgentTaskQueue{ID: taskID, AgentID: agentID, Status: "running"}}}}
	return h, rates
}
func semanticaRequest() *http.Request {
	r := httptest.NewRequest("POST", "/api/mcp", nil)
	r.Header.Set("X-Actor-Source", "task_token")
	r.Header.Set("X-Agent-ID", semanticaTestAgent)
	r.Header.Set("X-Task-ID", semanticaTestTask)
	r.Header.Set("X-Workspace-ID", semanticaTestWorkspace)
	r.Header.Set("Authorization", "Bearer mat_private")
	r.Header.Set("X-Caller-Secret", "private")
	return r
}

func TestSemanticaTaskBoundary(t *testing.T) {
	cases := []string{"wrong-agent", "personal-token", "finished-task", "wrong-workspace", "unlisted-tool", "caller-url", "redis-down", "rate-limit", "disabled-prod"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			h, rates := semanticaFixture(t, func(http.ResponseWriter, *http.Request) { called = true })
			r := semanticaRequest()
			args := `{"method":"tools/call","tool_name":"search_knowledge","arguments":{}}`
			switch name {
			case "wrong-agent":
				r.Header.Set("X-Agent-ID", semanticaTestTask)
			case "personal-token":
				r.Header.Set("X-Actor-Source", "")
			case "finished-task":
				s := h.SemanticaMCPRelay.tasks.(semanticaTestTasks)
				s.task.Status = "completed"
				h.SemanticaMCPRelay.tasks = s
			case "wrong-workspace":
				r.Header.Set("X-Workspace-ID", semanticaTestTask)
			case "unlisted-tool":
				args = `{"method":"tools/call","tool_name":"delete_knowledge","arguments":{}}`
			case "caller-url":
				args = `{"method":"tools/list","url":"https://evil.example"}`
			case "redis-down":
				rates.err = errors.New("unavailable")
			case "rate-limit":
				rates.n = 31
			case "disabled-prod":
				t.Setenv("AONE_ENV_TYPE", "prod")
			}
			w := httptest.NewRecorder()
			h.handleSemanticaRelayCall(w, r, json.RawMessage(`1`), json.RawMessage(args))
			if called || !strings.Contains(w.Body.String(), `"isError":true`) {
				t.Fatalf("boundary failed: called=%v body=%s", called, w.Body.String())
			}
		})
	}
}

func TestSemanticaForwardingAndDiscovery(t *testing.T) {
	h, rates := semanticaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("X-Task-ID") != "" || r.Header.Get("X-Caller-Secret") != "" {
			t.Error("credential boundary violated")
		}
		var req struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "tools/list" {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"search_knowledge","inputSchema":{"type":"object"}},{"name":"delete_knowledge"}]}}`)
		} else {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"real upstream fixture"}]}}`)
		}
	})
	for _, args := range []string{`{"method":"tools/list"}`, `{"method":"tools/call","tool_name":"search_knowledge","arguments":{"query":"MCP"}}`} {
		w := httptest.NewRecorder()
		h.handleSemanticaRelayCall(w, semanticaRequest(), json.RawMessage(`9`), json.RawMessage(args))
		if strings.Contains(w.Body.String(), `"isError":true`) || strings.Contains(w.Body.String(), "delete_knowledge") || !strings.Contains(w.Body.String(), `"id":9`) {
			t.Fatalf("bad result: %s", w.Body.String())
		}
	}
	if len(rates.keys) != 6 {
		t.Fatalf("wrong rate scopes: %v", rates.keys)
	}
	for _, key := range rates.keys {
		if !strings.HasPrefix(key, "mcpconn:pre:semantica:rate:") {
			t.Fatal(key)
		}
	}
}

func TestSemanticaSSEStopsAtResponse(t *testing.T) {
	reader, writer := io.Pipe()
	release := make(chan struct{})
	defer close(release)
	go func() {
		defer writer.Close()
		fmt.Fprint(writer, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":2}\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
		// The stream stays open until the reader has returned.
		<-release
	}()
	done := make(chan []byte, 1)
	go func() { b, _ := semanticaReadRPC(reader, "text/event-stream"); done <- b }()
	select {
	case data := <-done:
		if !strings.Contains(string(data), `"id":1`) {
			t.Fatalf("wrong response %s", data)
		}
	case <-time.After(time.Second):
		t.Error("waited for SSE EOF")
	}
	reader.Close()
}

func TestSemanticaResponseBoundsAndRPC(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"wrong-id", `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"x"}]}}`, 200},
		{"upstream-error", `{"jsonrpc":"2.0","id":1,"error":{"message":"secret"}}`, 200},
		{"too-large", strings.Repeat("x", semanticaMaxResponse+1), 200},
		{"redirect", "", 302},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := semanticaFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })
			_, err := h.SemanticaMCPRelay.call(context.Background(), semanticaRelayArguments{Method: "tools/list"})
			if err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

// The public sandbox relay waits only 30 seconds for response headers, while
// Semantica may need 45 seconds. Scaled timings reproduce that real boundary.
func TestSemanticaReturnsHeadersBeforeSlowUpstream(t *testing.T) {
	h, _ := semanticaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.handleSemanticaRelayCall(w, r, json.RawMessage(`1`), json.RawMessage(`{"method":"tools/list"}`))
	}))
	defer server.Close()
	transport := &http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	req, _ := http.NewRequest(http.MethodPost, server.URL, nil)
	req.Header = semanticaRequest().Header
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("outer relay timed out before MCP result: %v", err)
	}
	defer resp.Body.Close()
	var result multicaMCPResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || result.Result == nil || result.Error != nil {
		t.Fatalf("invalid completed result: %+v", result)
	}
}
