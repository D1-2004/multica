package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/modelregistry"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/pkg/llm"
	openai "github.com/openai/openai-go/v3"
)

type employeeTestRoutes struct {
	plan       modelregistry.CoordinatorPlan
	url        string
	prepared   []modelregistry.Ref
	planErr    error
	prepareErr error
}

func (r *employeeTestRoutes) CoordinatorPlan(context.Context) (modelregistry.CoordinatorPlan, error) {
	return r.plan, r.planErr
}
func (r *employeeTestRoutes) PrepareCoordinatorAttempt(_ context.Context, p modelregistry.CoordinatorPlan, i int) (modelregistry.CoordinatorAttempt, error) {
	if r.prepareErr != nil {
		return nil, r.prepareErr
	}
	if i < 0 || i >= len(p.Candidates) {
		return nil, fmt.Errorf("invalid candidate")
	}
	ref := p.Candidates[i]
	r.prepared = append(r.prepared, ref)
	return &employeeTestAttempt{ref: ref, client: llm.New(llm.Config{BaseURL: r.url + "/" + ref.Provider, APIKey: "test-key", DefaultModel: ref.Model, MaxRetries: -1})}, nil
}

type employeeTestAttempt struct {
	ref    modelregistry.Ref
	client *llm.Client
}

func (a *employeeTestAttempt) Ref() modelregistry.Ref       { return a.ref }
func (a *employeeTestAttempt) ConfigurationRevision() int64 { return 99 }
func (a *employeeTestAttempt) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	return a.client.Chat(ctx, p)
}

type employeeProbeHost struct{}

func (employeeProbeHost) Execute(context.Context, employeeloop.Identity, employeeloop.ToolCall) (employeeloop.ToolResult, error) {
	return employeeloop.ToolResult{Content: "probe complete"}, nil
}

func TestEmployeeModelRouteFallbackSticksAndReplaysWithoutRequestsOrEffects(t *testing.T) {
	for _, effect := range []bool{false, true} {
		t.Run(fmt.Sprint(effect), func(t *testing.T) {
			host, config, input, source := employeeDeadlineFixture(t)
			plan := modelregistry.CoordinatorPlan{Version: 1, Revision: 21, Candidates: []modelregistry.Ref{{Provider: "primary", Model: "chosen-a"}, {Provider: "fallback", Model: "chosen-b"}, {Provider: "last", Model: "chosen-c"}}}
			var mu sync.Mutex
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				mu.Lock()
				calls = append(calls, r.URL.Path+":"+fmt.Sprint(request["model"]))
				n := len(calls)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if n == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":{"message":"temporary","type":"server_error"}}`))
					return
				}
				message := map[string]any{"role": "assistant", "content": "DONE"}
				finish := "stop"
				if n == 2 {
					name, args := "probe", map[string]any{}
					if effect {
						name = "dispatch_task"
						args = map[string]any{"source_ref": source.SourceRef, "goal": "Run once", "prompt": "Produce one result", "reply": "Accepted once"}
					}
					raw, _ := json.Marshal(args)
					message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "route-tool-once", "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
					finish = "tool_calls"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": message}}, "usage": map[string]int{"prompt_tokens": 17, "completion_tokens": 3}})
			}))
			defer server.Close()
			routes := &employeeTestRoutes{plan: plan, url: server.URL}
			var executionHost employeeloop.Host = host
			if !effect {
				config.Tools = []employeeloop.Tool{{Name: "probe", Schema: map[string]any{"type": "object"}}}
				executionHost = employeeProbeHost{}
			}
			client, exporter := employeeTraceClient(t)
			trace := employeeTraceStart(context.Background(), client, host.job)
			defer trace.End(langfuse.EndOptions{})
			ctx := langfuse.ContextWithTrace(context.Background(), trace)
			legacy := employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				return nil, fmt.Errorf("routed wake reached legacy signup model")
			})
			model := &employeeJournalModel{store: host.worker.store, job: host.job, delegate: legacy, routes: routes, routePlan: &plan}
			out, err := employeeloop.New(config, model, executionHost).Run(ctx, input)
			want := 3
			if effect {
				want = 2
			}
			mu.Lock()
			gotCalls := append([]string(nil), calls...)
			mu.Unlock()
			if err != nil || len(gotCalls) != want {
				t.Fatalf("fallback requests=%v err=%v", gotCalls, err)
			}
			if gotCalls[0] != "/primary/chat/completions:chosen-a" || gotCalls[1] != "/fallback/chat/completions:chosen-b" || (!effect && gotCalls[2] != "/fallback/chat/completions:chosen-b") {
				t.Fatal("fallback did not stick", gotCalls)
			}
			gens := employeeTraceKind(exporter, "generation")
			if len(gens) != want {
				t.Fatal("nested or missing generation", len(gens))
			}
			for i, g := range gens {
				provider, index := "fallback", "1"
				if i == 0 {
					provider, index = "primary", "0"
				}
				if employeeTraceAttr(g, "langfuse.observation.metadata.provider") != provider || employeeTraceAttr(g, "langfuse.observation.metadata.candidate_index") != index || employeeTraceAttr(g, "langfuse.observation.metadata.configuration_revision") != "21" {
					t.Fatal("fallback trace attributed to primary", g.Name, g.Attributes)
				}
			}
			var attempts int
			var journal []employeeentry.ModelTurn
			if err := testPool.QueryRow(ctx, `SELECT model_attempts,model_journal FROM employee_scene_job WHERE id=$1::uuid`, host.job.ID).Scan(&attempts, &journal); err != nil || attempts != want || journal[0].Route == nil || journal[0].Route.NextCandidate != 1 {
				t.Fatal("missing durable selection", attempts, journal, err)
			}
			// A current chain edit must not change this already frozen wake or its
			// recorded failure's next candidate. No credentials are kept in the plan.
			routes.plan = modelregistry.CoordinatorPlan{Version: 1, Revision: 22, Candidates: []modelregistry.Ref{{Provider: "new", Model: "unrelated"}}}
			replay := &employeeJournalModel{store: host.worker.store, job: host.job, delegate: legacy, routes: routes, routePlan: &plan}
			again, err := employeeloop.New(config, replay, executionHost).Run(ctx, input)
			mu.Lock()
			after := len(calls)
			mu.Unlock()
			if err != nil || after != want || again.Kind != out.Kind || len(employeeTraceKind(exporter, "generation")) != want {
				t.Fatal("replay caused new I/O", after, err)
			}
			if effect {
				var runs int
				if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE agent_id=$1::uuid`, host.job.Scope.AgentID).Scan(&runs); err != nil || runs != 1 {
					t.Fatal("replay duplicated effect", runs, err)
				}
			}
			raw, _ := json.Marshal(plan)
			if strings.Contains(string(raw), "test-key") || strings.Contains(string(raw), server.URL) {
				t.Fatal("plan persisted credentials or URL")
			}
		})
	}
}

func TestEmployeeModelRouteWorkerRecoversWithoutCurrentRegistry(t *testing.T) {
	for _, savedOutcome := range []bool{false, true} {
		t.Run(fmt.Sprint(savedOutcome), func(t *testing.T) {
			ctx := context.Background()
			f, dc := employeeMemoryFixture(t)
			configureEmployeeReadyDependencies(f)
			worker := f.h.EmployeeSceneWorker
			worker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				t.Error("new wake used signup model")
				return nil, errors.New("legacy forbidden")
			})
			plan := modelregistry.CoordinatorPlan{Version: 1, Revision: 21, Candidates: []modelregistry.Ref{{Provider: "configured", Model: "primary-model"}}}
			calls := 0
			var source string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request["model"] != "primary-model" {
					t.Error("configured primary not selected", request["model"])
				}
				result := employeeReplyCompletion(t, employeeReplyCall(t, "dispatch-once", "dispatch_task", map[string]any{"source_ref": source, "goal": "one task", "prompt": "Run only once", "reply": "Accepted"}))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(result.RawJSON()))
			}))
			defer server.Close()
			routes := &employeeTestRoutes{plan: plan, url: server.URL}
			worker.ModelRoutes = routes
			f.h.EmployeeLoopReady = worker.Ready
			worker.RecoveryReady = worker.ReadyForRecovery
			f.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "model-routing", Text: "Do this once", SenderOpenDingTalkID: "requester-open-id"}}
			if response := employeeHTTP(t, f, dc, "model-routing"); response.Code != http.StatusAccepted {
				t.Fatal(response.Body.String())
			}
			var jobID, receipt string
			if err := testPool.QueryRow(ctx, `SELECT job_id::text,receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&jobID, &receipt); err != nil {
				t.Fatal(err)
			}
			source = receipt + "/model-routing"
			if worked, err := worker.ProcessNext(ctx); !worked || err != nil {
				t.Fatal(worked, err)
			}
			var rawSnapshot, rawJournal []byte
			if err := testPool.QueryRow(ctx, `SELECT input_snapshot,model_journal FROM employee_scene_job WHERE id=$1`, jobID).Scan(&rawSnapshot, &rawJournal); err != nil {
				t.Fatal(err)
			}
			var snapshot employeeSavedInput
			if err := json.Unmarshal(rawSnapshot, &snapshot); err != nil || snapshot.ModelRoute == nil || snapshot.ModelRoute.Revision != 21 || snapshot.Config.Model != "primary-model" {
				t.Fatal("missing frozen selection", snapshot, err)
			}
			// Recreate the crash boundary after response/effect commit, or after outcome
			// checkpoint. Current config unavailability must not invalidate saved work.
			if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',available_at=now(),outcome=CASE WHEN $2 THEN outcome ELSE NULL END WHERE id=$1`, jobID, savedOutcome); err != nil {
				t.Fatal(err)
			}
			routes.planErr = errors.New("current registry is unavailable")
			routes.prepareErr = routes.planErr
			if err := worker.Ready(ctx, dc.WorkspaceID, dc.AgentID); !errors.Is(err, routes.planErr) {
				t.Fatal("new admission must still fail", err)
			}
			if worked, err := worker.ProcessNext(ctx); !worked || err != nil {
				t.Fatal(worked, err)
			}
			var state string
			var sameSnapshot, sameJournal bool
			if err := testPool.QueryRow(ctx, `SELECT state,input_snapshot=$2::jsonb,model_journal=$3::jsonb FROM employee_scene_job WHERE id=$1`, jobID, rawSnapshot, rawJournal).Scan(&state, &sameSnapshot, &sameJournal); err != nil {
				t.Fatal(err)
			}
			var runs, actions int
			if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task_run WHERE agent_id=$1),(SELECT count(*) FROM response_action WHERE agent_id=$1)`, f.agentID).Scan(&runs, &actions); err != nil {
				t.Fatal(err)
			}
			if state != "completed" || !sameSnapshot || !sameJournal || calls != 1 || runs != 1 || actions != 1 {
				t.Fatalf("recovery state=%s stable=%v/%v HTTP=%d runs=%d actions=%d", state, sameSnapshot, sameJournal, calls, runs, actions)
			}
		})
	}
}

func TestEmployeeModelRouteBoundsUnavailableAndFailedCandidates(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			host, config, input, _ := employeeDeadlineFixture(t)
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
			}))
			defer server.Close()
			plan := modelregistry.CoordinatorPlan{Version: 1, Revision: 21, Candidates: []modelregistry.Ref{{Provider: "one", Model: "same"}, {Provider: "two", Model: "same"}, {Provider: "three", Model: "same"}, {Provider: "four", Model: "same"}}}
			routes := &employeeTestRoutes{plan: plan, url: server.URL}
			if unavailable {
				routes.prepareErr = modelregistry.ErrCandidateUnavailable
			}
			client, exporter := employeeTraceClient(t)
			trace := employeeTraceStart(context.Background(), client, host.job)
			defer trace.End(langfuse.EndOptions{})
			ctx := langfuse.ContextWithTrace(context.Background(), trace)
			model := &employeeJournalModel{store: host.worker.store, job: host.job, routes: routes, routePlan: &plan}
			out, err := employeeloop.New(config, model, host).Run(ctx, input)
			wantHTTP := 3
			if unavailable {
				wantHTTP = 0
			}
			if !errors.Is(err, employeeloop.ErrModelBudget) || out.ModelCalls != 3 || requests != wantHTTP || len(employeeTraceKind(exporter, "generation")) != wantHTTP {
				t.Fatalf("budget calls=%d HTTP=%d err=%v", out.ModelCalls, requests, err)
			}
			var attempts int
			var journal []employeeentry.ModelTurn
			if err := testPool.QueryRow(ctx, `SELECT model_attempts,model_journal FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&attempts, &journal); err != nil {
				t.Fatal(err)
			}
			if attempts != 3 || len(journal) != 3 {
				t.Fatal("unbounded durable reservations", attempts, journal)
			}
			for i, turn := range journal {
				if turn.Route == nil || turn.Route.Candidate != i || turn.Route.NextCandidate != i+1 || turn.Failure == "" {
					t.Fatal("failure cursor missing", turn)
				}
			}
		})
	}
}

func TestEmployeeModelRouteRestartContinuesFrozenFallback(t *testing.T) {
	host, _, _, _ := employeeDeadlineFixture(t)
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/a/") {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
			return
		}
		_, _ = w.Write([]byte(employeeMemoryAnswer(t).RawJSON()))
	}))
	defer server.Close()
	plan := modelregistry.CoordinatorPlan{Version: 1, Revision: 21, Candidates: []modelregistry.Ref{{Provider: "a", Model: "same"}, {Provider: "b", Model: "same"}}}
	routes := &employeeTestRoutes{plan: plan, url: server.URL}
	request := openai.ChatCompletionNewParams{Model: "same", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")}}
	first := &employeeJournalModel{store: host.worker.store, job: host.job, routes: routes, routePlan: &plan}
	if _, err := first.Chat(context.Background(), request); err == nil {
		t.Fatal("missing primary failure")
	}
	routes.plan = modelregistry.CoordinatorPlan{Version: 1, Revision: 22, Candidates: []modelregistry.Ref{{Provider: "c", Model: "new"}}}
	recovered := &employeeJournalModel{store: host.worker.store, job: host.job, routes: routes, routePlan: &plan}
	if _, err := recovered.Chat(context.Background(), request); err == nil {
		t.Fatal("missing cached failure")
	}
	if _, err := recovered.Chat(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if strings.Join(requests, ",") != "/a/chat/completions,/b/chat/completions" || len(routes.prepared) != 2 || routes.prepared[1].Provider != "b" {
		t.Fatal("restart followed changed chain", requests, routes.prepared)
	}
}

func TestEmployeeModelRouteLegacySnapshotRetainsTransportAndRequest(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	configureEmployeeReadyDependencies(f)
	host, _, _ := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "legacy-route", Text: "hello", SenderOpenDingTalkID: "requester-open-id"}})
	worker := host.worker
	ctx := context.Background()
	input, err := worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	input.Config.Model = ""
	input.Input.RecentConversation = ""
	raw, _ := json.Marshal(input)
	if strings.Contains(string(raw), "model_route") || strings.Contains(string(raw), "recent_conversation") {
		t.Fatal("legacy snapshot gained serialization")
	}
	if _, err = worker.store.SaveInput(ctx, host.job, raw); err != nil {
		t.Fatal(err)
	}
	calls := 0
	worker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		if p.Model != "" {
			t.Error("legacy request model changed", p.Model)
		}
		return employeeMemoryAnswer(t), nil
	})
	worker.ModelRoutes = &employeeTestRoutes{planErr: errors.New("new chain unavailable"), prepareErr: errors.New("must not prepare legacy")}
	worker.RecoveryReady = worker.ReadyForRecovery
	f.h.EmployeeLoopReady = worker.Ready
	for i := 0; i < 2; i++ {
		if _, err = testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',available_at=now(),outcome=NULL,lease_token=NULL,lease_until=NULL WHERE id=$1`, host.job.ID); err != nil {
			t.Fatal(err)
		}
		if worked, err := worker.ProcessNext(ctx); !worked || err != nil {
			t.Fatal(worked, err)
		}
	}
	var same bool
	var journal []employeeentry.ModelTurn
	var state string
	if err = testPool.QueryRow(ctx, `SELECT state,input_snapshot=$2::jsonb,model_journal FROM employee_scene_job WHERE id=$1`, host.job.ID, raw).Scan(&state, &same, &journal); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || !same || calls != 1 || len(journal) != 1 || journal[0].Route != nil {
		t.Fatalf("legacy restoration changed: state=%s same=%v calls=%d journal=%+v", state, same, calls, journal)
	}
	var request map[string]any
	if err = json.Unmarshal(journal[0].Request, &request); err != nil || (request["model"] != nil && request["model"] != "") {
		t.Fatal("legacy raw request changed", request["model"], err)
	}
}

func TestEmployeeModelRouteRecoveryKeepsNonModelFences(t *testing.T) {
	f, _, dc := employeeFixture(t)
	configureEmployeeReadyDependencies(f)
	worker := f.h.EmployeeSceneWorker
	worker.ModelRoutes = &employeeTestRoutes{planErr: errors.New("registry unavailable")}
	oldReplica := errors.New("replica not compatible")
	worker.ReplicaReady = func(context.Context) error { return oldReplica }
	if err := worker.ReadyForRecovery(context.Background(), dc.WorkspaceID, dc.AgentID); !errors.Is(err, oldReplica) {
		t.Fatal("recovery bypassed replica fence", err)
	}
	worker.ReplicaReady = func(context.Context) error { return nil }
	f.h.DingTalkResponses.BeforeSend = nil
	if err := worker.ReadyForRecovery(context.Background(), dc.WorkspaceID, dc.AgentID); err == nil {
		t.Fatal("recovery bypassed send fence")
	}
	configureEmployeeReadyDependencies(f)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runtime SET metadata='{}' WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if err := worker.ReadyForRecovery(context.Background(), dc.WorkspaceID, dc.AgentID); err == nil {
		t.Fatal("recovery bypassed runtime capability")
	}
}
