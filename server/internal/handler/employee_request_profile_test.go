package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/modelregistry"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
)

func employeeProfilePlan(t *testing.T, model, profile string) modelregistry.CoordinatorPlan {
	t.Helper()
	fields := map[string]any{"version": 1, "revision": 37, "candidate_refs": []modelregistry.Ref{{Provider: "test", Model: model}}}
	if profile != "" {
		fields["request_profile"] = profile
	}
	raw, _ := json.Marshal(fields)
	var plan modelregistry.CoordinatorPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestEmployeeRequestProfileMatchesHTTPJournalAndTrace(t *testing.T) {
	for _, tc := range []struct{ model, profile, mode string }{
		{"deepseek-v4-flash", "employee-fast-v1", "text"},
		{"qwen3.7-plus", "employee-fast-v1", "text"},
		{"qwen3.8-max", "employee-fast-v1", "tool"},
		{"deepseek-v4-flash", "employee-fast-v1", "quiet"},
		{"deepseek-v4-flash", "employee-fast-v1", "fallback"},
		{"qwen3.8-max", "", "tool"},
	} {
		t.Run(tc.model+"/"+tc.profile+"/"+tc.mode, func(t *testing.T) {
			host, config, input, _ := employeeDeadlineFixture(t)
			plan := employeeProfilePlan(t, tc.model, tc.profile)
			if tc.mode == "fallback" {
				plan.Candidates = append(plan.Candidates, modelregistry.Ref{Provider: "fallback", Model: "qwen3.7-plus"})
			}
			var mu sync.Mutex
			var bodies []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				mu.Lock()
				bodies = append(bodies, body)
				n := len(bodies)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if tc.mode == "fallback" && n == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":{"message":"provider busy","type":"server_error"}}`))
					return
				}
				message := map[string]any{"role": "assistant", "content": "DONE"}
				finish := "stop"
				if n == 1 && tc.mode != "text" {
					name := "probe"
					if tc.mode == "quiet" {
						name = "stay_quiet"
					}
					message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "profile-tool", "type": "function", "function": map[string]any{"name": name, "arguments": "{}"}}}}
					finish = "tool_calls"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"model": body["model"], "choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": message}}})
			}))
			defer server.Close()
			routes := &employeeTestRoutes{plan: plan, url: server.URL}
			var executionHost employeeloop.Host = host
			if tc.mode == "tool" {
				config.Tools = []employeeloop.Tool{{Name: "probe", Schema: map[string]any{"type": "object"}}}
				executionHost = employeeProbeHost{}
			}
			client, exporter := employeeTraceClient(t)
			trace := employeeTraceStart(context.Background(), client, host.job)
			defer trace.End(langfuse.EndOptions{})
			ctx := langfuse.ContextWithTrace(context.Background(), trace)
			model := &employeeJournalModel{store: host.worker.store, job: host.job, routes: routes, routePlan: &plan}
			out, err := employeeloop.New(config, model, executionHost).Run(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.mode == "tool" || tc.mode == "fallback" {
				want = 2
			}
			mu.Lock()
			captured := append([]map[string]any(nil), bodies...)
			mu.Unlock()
			if len(captured) != want || out.ModelCalls != want {
				t.Fatal("unexpected provider budget", len(captured), out.ModelCalls)
			}
			if tc.mode == "quiet" && out.Kind != employeeloop.Quiet {
				t.Fatal("quiet behavior changed", out)
			}
			var journal []employeeentry.ModelTurn
			if err := testPool.QueryRow(ctx, `SELECT model_journal FROM employee_scene_job WHERE id=$1::uuid`, host.job.ID).Scan(&journal); err != nil {
				t.Fatal(err)
			}
			generations := employeeTraceKind(exporter, "generation")
			if len(journal) != want || len(generations) != want {
				t.Fatal("journal/trace does not match actual HTTP", len(journal), len(generations))
			}
			for i, body := range captured {
				wantModel := tc.model
				if tc.mode == "fallback" && i > 0 {
					wantModel = "qwen3.7-plus"
				}
				if body["model"] != wantModel || body["tool_choice"] == "required" {
					t.Error("frozen model or optional direct reply changed", body)
				}
				if tc.profile != "" {
					if body["max_completion_tokens"] != float64(4096) || body["enable_thinking"] != false {
						t.Errorf("missing bounded no-thinking profile: %+v", body)
					}
					if strings.Contains(wantModel, "deepseek") {
						thinking, _ := body["thinking"].(map[string]any)
						if thinking["type"] != "disabled" || body["reasoning_effort"] != nil {
							t.Error("DeepSeek native thinking switch missing or conflicting effort", body)
						}
					} else if body["reasoning_effort"] != "none" || body["thinking"] != nil {
						t.Error("Qwen profile changed", body)
					}
				} else {
					for _, key := range []string{"max_completion_tokens", "enable_thinking", "thinking", "reasoning_effort"} {
						if _, present := body[key]; present {
							t.Errorf("legacy request gained %s", key)
						}
					}
				}
				var saved, traced map[string]any
				_ = json.Unmarshal(journal[i].Request, &saved)
				_ = json.Unmarshal([]byte(employeeTraceAttr(generations[i], "langfuse.observation.input")), &traced)
				if !reflect.DeepEqual(body, saved) || !reflect.DeepEqual(body, traced) {
					t.Error("provider body differs from pre-I/O journal or Langfuse input")
				}
				if employeeTraceAttr(generations[i], "langfuse.observation.metadata.request_profile") != tc.profile {
					t.Error("trace lost frozen request profile")
				}
			}
			// A frozen legacy plan stays legacy even after the current plan enables
			// the new profile; cached model turns must not prepare or send again.
			routes.plan = employeeProfilePlan(t, tc.model, "employee-fast-v1")
			replay := &employeeJournalModel{store: host.worker.store, job: host.job, routes: routes, routePlan: &plan}
			if _, err := employeeloop.New(config, replay, executionHost).Run(ctx, input); err != nil {
				t.Fatal("frozen-profile replay changed request identity", err)
			}
			mu.Lock()
			after := len(bodies)
			mu.Unlock()
			if after != want || len(employeeTraceKind(exporter, "generation")) != want || len(routes.prepared) != want {
				t.Fatal("replay performed provider work", after, len(routes.prepared))
			}
		})
	}
}

func TestEmployeeRequestProfileOwnsThinkingAndBudgetBeforeJournal(t *testing.T) {
	for _, name := range []string{"deepseek-v4-flash", "qwen3.8-max"} {
		plan := employeeProfilePlan(t, name, "employee-fast-v1")
		model := employeeJournalModel{routes: &employeeTestRoutes{}, routePlan: &plan}
		request := openai.ChatCompletionNewParams{Model: "unselected", ReasoningEffort: "high"}
		extra := map[string]any{"model": "unselected-extra", "enable_thinking": true, "thinking": map[string]any{"type": "enabled"}, "reasoning_effort": "high", "max_completion_tokens": 99999, "tool_choice": "auto"}
		request.SetExtraFields(extra)
		if _, err := model.routeSelection(&request); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(request)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if body["model"] != name || body["max_completion_tokens"] != float64(4096) || body["enable_thinking"] != false || body["tool_choice"] != "auto" {
			t.Fatal("extra fields overrode frozen fast profile", string(raw))
		}
		if strings.Contains(name, "deepseek") {
			thinking, _ := body["thinking"].(map[string]any)
			if body["reasoning_effort"] != nil || thinking["type"] != "disabled" {
				t.Fatal("DeepSeek override survived", string(raw))
			}
		} else if body["reasoning_effort"] != "none" || body["thinking"] != nil {
			t.Fatal("Qwen override survived", string(raw))
		}
		if extra["enable_thinking"] != true || extra["model"] != "unselected-extra" {
			t.Fatal("normalization mutated shared extra fields")
		}
	}
}

func TestEmployeeRequestProfileRejectsUnknownWithoutChangingLegacy(t *testing.T) {
	legacy := `{"version":1,"revision":37,"candidate_refs":[{"provider":"test","model":"qwen3.8-max"}]}`
	var plan modelregistry.CoordinatorPlan
	if err := json.Unmarshal([]byte(legacy), &plan); err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(plan); string(raw) != legacy {
		t.Fatal("old plan bytes changed", string(raw))
	}
	unknown := employeeProfilePlan(t, "qwen3.8-max", "employee-fast-future")
	model := &employeeJournalModel{routes: &employeeTestRoutes{}, routePlan: &unknown}
	request := openai.ChatCompletionNewParams{Model: "unselected"}
	if _, err := model.routeSelection(&request); err == nil {
		t.Fatalf("unknown request profile accepted: %+v", unknown)
	}
}
