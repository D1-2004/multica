//go:build employeeintegration

package employeeloop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/pkg/llm"
)

// TestEmployeeModelSmoke uses a real model only with both the employeeintegration
// build tag and explicit opt-in. The Host is fake: no real task or external effect
// is created. Both cases share a hard cap of three outgoing HTTP requests total.
func TestEmployeeModelSmoke(t *testing.T) {
	if os.Getenv("MULTICA_RUN_EMPLOYEE_MODEL_SMOKE") != "1" {
		t.Skip("set MULTICA_RUN_EMPLOYEE_MODEL_SMOKE=1 to enable the real-model smoke")
	}
	required := func(name string) string {
		t.Helper()
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			t.Fatalf("real-model smoke requires %s", name)
		}
		return value
	}
	baseURL := required("EMPLOYEE_MODEL_BASE_URL")
	apiKey := required("EMPLOYEE_MODEL_API_KEY")
	modelName := required("EMPLOYEE_MODEL_MODEL")
	transport := &smokeBudgetTransport{base: http.DefaultTransport, maxRequests: 3}
	client := llm.New(llm.Config{BaseURL: baseURL, APIKey: apiKey, DefaultModel: modelName, MaxRetries: -1, HTTPClient: &http.Client{Transport: transport, Timeout: 45 * time.Second}})

	config := Config{
		Model:   modelName,
		Persona: Persona{Name: "菲迪", Personality: "Help colleagues coordinate engineering work.", Tone: "用自然、简洁的中文交流。"},
		Tools: []Tool{{Name: "dispatch_task", Description: "Accept substantial work for later execution. The Host returns acceptance and a terminal reply immediately. source_ref must identify a supplied utterance.", Effect: true, Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source_ref": map[string]any{"type": "string"},
				"goal":       map[string]any{"type": "string"},
				"prompt":     map[string]any{"type": "string"},
				"reply":      map[string]any{"type": "string", "description": "A concise Chinese acknowledgement that the work was accepted, without claiming completion."},
			},
			"required":             []string{"source_ref", "goal", "prompt", "reply"},
			"additionalProperties": false,
		}}},
	}
	input := Input{Identity: Identity{WorkspaceID: "smoke-workspace", AgentID: "smoke-agent", TenantOrgID: "smoke-org", Scene: scene.Ref{SceneID: "smoke-scene"}, ReceiptID: "smoke-inbound-receipt"}}
	cases := []struct {
		name, window   string
		want           Disposition
		wantDispatches int
	}{
		{name: "greeting_first_call_reply", window: "[source_ref: smoke-message-1]\n说话人：同事\n消息：你好", want: Reply},
		{name: "task_first_call_dispatch", window: "[source_ref: smoke-message-1]\n说话人：同事\n消息：请使用 dispatch_task，把‘检查新版本登录流程并提交一份测试报告’交给后台任务执行。source_ref 是 smoke-message-1，目标和产物已明确，无需再确认；现在只回复已经受理，不要声称检查完成。", want: Dispatched, wantDispatches: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatches := 0
			host := hostFunc(func(ctx context.Context, identity Identity, call ToolCall) (ToolResult, error) {
				if err := ctx.Err(); err != nil {
					return ToolResult{}, err
				}
				if identity != input.Identity || call.Name != "dispatch_task" || call.Arguments["source_ref"] != "smoke-message-1" {
					return ToolResult{}, errors.New("smoke Host rejected identity, tool or source reference")
				}
				for _, field := range []string{"goal", "prompt", "reply"} {
					value, ok := call.Arguments[field].(string)
					if !ok || strings.TrimSpace(value) == "" {
						return ToolResult{}, errors.New("smoke Host rejected an incomplete dispatch")
					}
				}
				dispatches++
				return ToolResult{Content: `{"accepted":true,"fake_host":true}`, Receipt: "smoke-accepted-" + call.NativeToolCallID, Terminal: &Decision{Kind: Dispatched, Reply: call.Arguments["reply"].(string)}}, nil
			})
			current := input
			current.CurrentWindow = tc.window
			before := transport.requests.Load()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			outcome, err := New(config, client, host).Run(ctx, current)
			httpCalls := transport.requests.Load() - before
			// Do not print SDK errors, raw completion text or headers: upstream error
			// payloads may echo credentials. Only non-sensitive counters are reported.
			if err != nil {
				t.Fatalf("real-model smoke failed; model_calls=%d http_calls=%d (provider details withheld)", outcome.ModelCalls, httpCalls)
			}
			if outcome.Kind != tc.want || strings.TrimSpace(outcome.Reply) == "" || outcome.ModelCalls != 1 || httpCalls != 1 || dispatches != tc.wantDispatches {
				t.Fatalf("unexpected smoke outcome: disposition=%q model_calls=%d http_calls=%d dispatches=%d", outcome.Kind, outcome.ModelCalls, httpCalls, dispatches)
			}
			if tc.want == Dispatched && (len(outcome.Receipts) != 1 || len(outcome.ToolOutcomes) != 1) {
				t.Fatal("accepted dispatch lost its fake Host receipt")
			}
			t.Logf("disposition=%s model_calls=%d http_calls=%d dispatches=%d", outcome.Kind, outcome.ModelCalls, httpCalls, dispatches)
		})
	}
	if transport.requests.Load() > 3 {
		t.Fatal("smoke exceeded its total HTTP request budget")
	}
}

// smokeBudgetTransport counts actual HTTP attempts, including any SDK retry or
// redirect. It refuses to call the network transport for a fourth request.
type smokeBudgetTransport struct {
	base        http.RoundTripper
	maxRequests int32
	requests    atomic.Int32
}

func (s *smokeBudgetTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	for {
		used := s.requests.Load()
		if used >= s.maxRequests {
			return nil, errors.New("employee model smoke HTTP budget exhausted")
		}
		if s.requests.CompareAndSwap(used, used+1) {
			break
		}
	}
	return s.base.RoundTrip(request)
}

func TestEmployeeModelSmokeHTTPBudget(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	transport := &smokeBudgetTransport{base: http.DefaultTransport, maxRequests: 3}
	client := &http.Client{Transport: transport}
	for i := 0; i < 4; i++ {
		response, err := client.Get(server.URL)
		if i < 3 {
			if err != nil {
				t.Fatal("allowed local request failed")
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			_ = response.Body.Close()
			t.Fatal("fourth request reached the server")
		}
	}
	if requests.Load() != 3 || transport.requests.Load() != 3 {
		t.Fatalf("wire calls=%d counted=%d", requests.Load(), transport.requests.Load())
	}
}

// This fixture exercises both smoke assertions without any provider account.
func TestEmployeeModelSmokeWithLocalProvider(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"你好！"}}]}`
		if requests.Add(1) == 2 {
			response = `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"smoke-dispatch-1","type":"function","function":{"name":"dispatch_task","arguments":"{\"source_ref\":\"smoke-message-1\",\"goal\":\"检查登录流程\",\"prompt\":\"检查登录流程并提交测试报告\",\"reply\":\"已受理，完成后告诉你。\"}"}}]}}]}`
		}
		if _, err := w.Write([]byte(response)); err != nil {
			t.Error("could not write local model fixture")
		}
	}))
	defer server.Close()
	t.Setenv("MULTICA_RUN_EMPLOYEE_MODEL_SMOKE", "1")
	t.Setenv("EMPLOYEE_MODEL_BASE_URL", server.URL)
	t.Setenv("EMPLOYEE_MODEL_API_KEY", "local-smoke-fixture")
	t.Setenv("EMPLOYEE_MODEL_MODEL", "local-smoke-fixture")
	TestEmployeeModelSmoke(t)
	if requests.Load() != 2 {
		t.Fatalf("fixture used %d HTTP requests, want 2", requests.Load())
	}
}
