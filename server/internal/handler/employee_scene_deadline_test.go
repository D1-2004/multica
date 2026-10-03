package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/pkg/llm"
	openai "github.com/openai/openai-go/v3"
)

func employeeDeadlineFixture(t *testing.T) (*employeeSceneHost, employeeloop.Config, employeeloop.Input, employeeSourceMessage) {
	t.Helper()
	f, dc := employeeMemoryFixture(t)
	host, id, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "deadline-source", Text: "Analyze the supplied data", SenderOpenDingTalkID: "requester-open-id"}})
	return host, employeeloop.Config{Model: "deadline-test", Tools: employeeSceneTools()}, employeeloop.Input{Identity: id, CurrentWindow: source.Message.Text}, source
}

func TestEmployeeProviderDeadlineHTTPRetryAndReplay(t *testing.T) {
	for _, dispatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "reply", true: "accepted_effect"}[dispatch], func(t *testing.T) {
			host, config, input, source := employeeDeadlineFixture(t)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if requests.Add(1) == 1 {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(3 * time.Second):
						t.Error("provider request never cancelled")
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				message := map[string]any{"role": "assistant", "content": "RECOVERED"}
				finish := "stop"
				if dispatch {
					args, _ := json.Marshal(map[string]any{"source_ref": source.SourceRef, "goal": "Analyze supplied data", "prompt": "Analyze the supplied data and return evidence", "reply": "Accepted once"})
					message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "accepted-after-timeout", "type": "function", "function": map[string]any{"name": "dispatch_task", "arguments": string(args)}}}}
					finish = "tool_calls"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": message}}, "usage": map[string]int{"prompt_tokens": 17, "completion_tokens": 3}})
			}))
			defer server.Close()
			client := llm.New(llm.Config{BaseURL: server.URL, APIKey: "test", DefaultModel: "deadline-test", MaxRetries: -1})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			traceClient, exporter := employeeTraceClient(t)
			trace := employeeTraceStart(ctx, traceClient, host.job)
			ctx = langfuse.ContextWithTrace(ctx, trace)
			defer trace.End(langfuse.EndOptions{})
			model := &employeeJournalModel{store: host.worker.store, job: host.job, delegate: client, requestTimeout: 80 * time.Millisecond}
			out, err := employeeloop.New(config, model, host).Run(ctx, input)
			if err != nil || ctx.Err() != nil || requests.Load() != 2 {
				t.Fatalf("first timeout consumed parent budget: outcome=%+v requests=%d err=%v parent=%v", out.Decision, requests.Load(), err, ctx.Err())
			}
			if !dispatch && out.Reply != "RECOVERED" {
				t.Fatal("retry reply missing")
			}
			if dispatch && (out.Kind != employeeloop.Dispatched || len(out.Receipts) != 1) {
				t.Fatal("accepted effect missing")
			}
			var attempts int
			var journal []employeeentry.ModelTurn
			if err = testPool.QueryRow(ctx, `SELECT model_attempts,model_journal FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&attempts, &journal); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 || len(journal) != 2 || journal[0].Failure == "" || len(journal[1].Response) == 0 {
				t.Fatal("child timeout failure was not journaled with live parent context")
			}
			gens := employeeTraceKind(exporter, "generation")
			if len(gens) != 2 || employeeTraceAttr(gens[0], "langfuse.observation.level") != "ERROR" {
				t.Fatal("real failed/successful attempts not traced")
			}
			elapsed := gens[0].EndTime.Sub(gens[0].StartTime)
			if elapsed < 40*time.Millisecond || elapsed > time.Second {
				t.Fatalf("generation did not measure the bounded provider call: %v", elapsed)
			}
			replay := &employeeJournalModel{store: host.worker.store, job: host.job, delegate: client, requestTimeout: time.Millisecond}
			again, err := employeeloop.New(config, replay, host).Run(ctx, input)
			if err != nil || requests.Load() != 2 || again.Kind != out.Kind || len(employeeTraceKind(exporter, "generation")) != 2 {
				t.Fatalf("journal replay reissued provider I/O: requests=%d err=%v", requests.Load(), err)
			}
			if dispatch {
				var runs int
				if err = testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE agent_id=$1`, host.job.Scope.AgentID).Scan(&runs); err != nil || runs != 1 {
					t.Fatalf("accepted effect repeated: runs=%d err=%v", runs, err)
				}
			}
		})
	}
}

func TestEmployeeProviderDeadlineNeverExceedsThreeHTTPRequests(t *testing.T) {
	host, config, input, _ := employeeDeadlineFixture(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			t.Error("provider request not cancelled")
		}
	}))
	defer server.Close()
	client := llm.New(llm.Config{BaseURL: server.URL, APIKey: "test", DefaultModel: "deadline-test", MaxRetries: -1})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	model := &employeeJournalModel{store: host.worker.store, job: host.job, delegate: client, requestTimeout: 60 * time.Millisecond}
	out, err := employeeloop.New(config, model, host).Run(ctx, input)
	if !errors.Is(err, employeeloop.ErrModelBudget) || requests.Load() != 3 || out.ModelCalls != 3 || ctx.Err() != nil {
		t.Fatalf("request limit/retry budget wrong: requests=%d models=%d err=%v parent=%v", requests.Load(), out.ModelCalls, err, ctx.Err())
	}
	var attempts int
	var failures int
	if err = testPool.QueryRow(ctx, `SELECT model_attempts,(SELECT count(*) FROM jsonb_array_elements(model_journal) e WHERE e->>'failure' IS NOT NULL) FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&attempts, &failures); err != nil || attempts != 3 || failures != 3 {
		t.Fatalf("durable request accounting=%d failures=%d err=%v", attempts, failures, err)
	}
}

func TestEmployeeProviderDeadlineRejectsLateToolCompletion(t *testing.T) {
	host, config, input, source := employeeDeadlineFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	model := &employeeJournalModel{store: host.worker.store, job: host.job, requestTimeout: 20 * time.Millisecond, delegate: employeeReplyModelFunc(func(ctx context.Context, _ openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		<-ctx.Done()
		return employeeReplyCompletion(t, employeeReplyCall(t, "late-tool", "dispatch_task", map[string]any{"source_ref": source.SourceRef, "goal": "late work", "prompt": "late work", "reply": "should not happen"})), nil
	})}
	out, err := employeeloop.New(config, model, host).Run(ctx, input)
	if !errors.Is(err, employeeloop.ErrModelBudget) || calls != 3 || len(out.Receipts) != 0 {
		t.Fatalf("late completion accepted: calls=%d receipts=%d err=%v", calls, len(out.Receipts), err)
	}
	var runs int
	if err = testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_task_run WHERE agent_id=$1`, host.job.Scope.AgentID).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("late tool created effects: runs=%d err=%v", runs, err)
	}
}

func TestEmployeeProviderDeadlineUsesEarlierParentAndCapsOverrides(t *testing.T) {
	for _, shortParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "default_cap", true: "earlier_parent"}[shortParent], func(t *testing.T) {
			host, _, _, _ := employeeDeadlineFixture(t)
			duration := 45 * time.Second
			if shortParent {
				duration = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()
			parentDeadline, _ := ctx.Deadline()
			model := &employeeJournalModel{store: host.worker.store, job: host.job, requestTimeout: time.Minute, delegate: employeeReplyModelFunc(func(callCtx context.Context, _ openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				deadline, ok := callCtx.Deadline()
				if !ok || deadline.After(parentDeadline) || time.Until(deadline) > 20*time.Second {
					t.Fatalf("provider deadline exceeds cap or parent: %v", deadline)
				}
				if shortParent && !deadline.Equal(parentDeadline) {
					t.Fatal("earlier parent deadline changed")
				}
				return employeeMemoryAnswer(t), nil
			})}
			if _, err := model.Chat(ctx, openai.ChatCompletionNewParams{Model: "deadline-test", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEmployeeProviderDeadlineParentExpiryKeepsOutcomeCheckpoint(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	f.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "parent-expiry", Text: "Analyze input", SenderOpenDingTalkID: "requester-open-id"}}
	if response := employeeHTTP(t, f, dc, "parent-expiry"); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	worker := f.h.EmployeeSceneWorker
	calls := 0
	worker.model = employeeReplyModelFunc(func(ctx context.Context, _ openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if worked, err := worker.ProcessNext(ctx); !worked || err == nil {
		t.Fatalf("expected actual parent expiry: worked=%v err=%v", worked, err)
	}
	var raw []byte
	var jobID string
	if err := testPool.QueryRow(context.Background(), `SELECT id::text,outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&jobID, &raw); err != nil {
		t.Fatal(err)
	}
	var saved employeeSavedOutcome
	if err := json.Unmarshal(raw, &saved); err != nil || saved.Failure == "" {
		t.Fatalf("parent deadline lost detached failure checkpoint: %s %v", raw, err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_scene_job SET lease_until=now()-interval '1 second' WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("outcome recovery failed: %v %v", worked, err)
	}
	var state string
	if err := testPool.QueryRow(context.Background(), `SELECT state FROM employee_scene_job WHERE id=$1`, jobID).Scan(&state); err != nil || state != "completed" || calls != 1 {
		t.Fatalf("checkpoint replay requested provider: state=%s calls=%d err=%v", state, calls, err)
	}
}
