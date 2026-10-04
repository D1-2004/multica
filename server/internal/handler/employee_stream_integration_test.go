package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/pkg/llm"
)

type feedbackSignalHost struct {
	inner     *employeeSceneHost
	seen      chan struct{}
	signalled bool
}

func (h *feedbackSignalHost) Execute(ctx context.Context, id employeeloop.Identity, call employeeloop.ToolCall) (employeeloop.ToolResult, error) {
	out, err := h.inner.Execute(ctx, id, call)
	if call.Name == "first_feedback" && err == nil && !h.signalled {
		h.signalled = true
		close(h.seen)
	}
	return out, err
}

func TestEmployeeStreamIntegrationPersistsBeforeEndAndReplaysWithoutHTTP(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "succeeded", "查一下我之前发起的工作")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_first_feedback WHERE agent_id=$1`, f.agentID)
	})
	seen := make(chan struct{})
	var requests atomic.Int32
	chunk := func(w http.ResponseWriter, delta map[string]any, finish any) {
		body, _ := json.Marshal(map[string]any{"id": "integrated-stream", "object": "chat.completion.chunk", "model": "fixture-model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
		w.(http.Flusher).Flush()
	}
	call := func(index int, nativeID, name string, args map[string]any) map[string]any {
		argBytes, _ := json.Marshal(args)
		return map[string]any{"index": index, "id": nativeID, "type": "function", "function": map[string]any{"name": name, "arguments": string(argBytes)}}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 2 {
			chunk(w, map[string]any{"role": "assistant", "content": "这项工作已完成。"}, "stop")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		chunk(w, map[string]any{"role": "assistant", "tool_calls": []any{call(0, "feedback-stream", "first_feedback", map[string]any{"source_ref": source.SourceRef, "intent": "lookup", "text": "我先查一下这项工作的最新状态。"})}}, nil)
		select {
		case <-seen:
		case <-ctx.Done():
			t.Error("Host did not accept feedback while first response pending")
			return
		}
		var feedbackRows, discoveryRows int
		var responsePending bool
		if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_first_feedback WHERE job_id=j.id),(SELECT count(*) FROM jsonb_each(j.tool_journal) e WHERE e.value->'input'->>'name'='find_tasks'),model_journal->0->'response' IS NULL FROM employee_scene_job j WHERE id=$1`, host.job.ID).Scan(&feedbackRows, &discoveryRows, &responsePending); err != nil || feedbackRows != 1 || discoveryRows != 0 || !responsePending {
			t.Error("early transaction evidence wrong", feedbackRows, discoveryRows, responsePending, err)
		}
		chunk(w, map[string]any{"tool_calls": []any{call(1, "lookup-stream", "find_tasks", map[string]any{"source_ref": source.SourceRef})}}, nil)
		chunk(w, map[string]any{}, "tool_calls")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	cfg := employeeloop.Config{Model: "fixture-model", Tools: employeeDiscoveryTools(employeeSceneTools())}
	messageSources := employeeSourceMessages(host.job.Items[0], host.envelopes[0])
	employeeConfigureFirstFeedback(&cfg, host.job, messageSources, host.envelopes)
	if !cfg.StreamedFeedback {
		t.Fatal("new eligible input did not freeze streamed feedback")
	}
	input := employeeloop.Input{Identity: id, CurrentWindow: source.Message.Text}
	client := llm.New(llm.Config{APIKey: "fixture", BaseURL: srv.URL, MaxRetries: -1})
	jm := &employeeJournalModel{store: host.worker.store, job: host.job, delegate: client}
	out, err := employeeloop.New(cfg, jm, &feedbackSignalHost{inner: host, seen: seen}).Run(ctx, input)
	if err != nil || out.Reply != "这项工作已完成。" || requests.Load() != 2 || out.ModelCalls != 2 {
		t.Fatal(out, requests.Load(), err)
	}
	var journal []employeeentry.ModelTurn
	if err = testPool.QueryRow(ctx, `SELECT model_journal FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&journal); err != nil || len(journal) != 2 {
		t.Fatal(journal, err)
	}
	var request map[string]any
	_ = json.Unmarshal(journal[0].Request, &request)
	if request["stream"] != true {
		t.Fatal("stream flag absent from durable request identity")
	}
	replayed, err := employeeloop.New(cfg, &employeeJournalModel{store: host.worker.store, job: host.job, delegate: client}, host).Run(ctx, input)
	if err != nil || replayed.Reply != out.Reply || requests.Load() != 2 {
		t.Fatal("recovery repeated provider request or broke native replay", replayed, requests.Load(), err)
	}
	participationComplete(t, host, out)
	var feedbackState string
	if err := testPool.QueryRow(ctx, `SELECT a.state FROM response_action a JOIN employee_first_feedback f ON f.action_id=a.id WHERE f.job_id=$1`, host.job.ID).Scan(&feedbackState); err != nil || feedbackState != "cancelled" {
		t.Fatal("final did not supersede unsubmitted feedback", feedbackState, err)
	}
}

func TestEmployeeStreamIntegrationNewInputGate(t *testing.T) {
	_, host, _, _ := employeeCurrentTaskHost(t, "succeeded", "查一下我之前发起的工作")
	messages := employeeSourceMessages(host.job.Items[0], host.envelopes[0])
	for _, mode := range []string{"legacy", "new", "task_wake", "multi_source"} {
		t.Run(mode, func(t *testing.T) {
			job, sources := host.job, messages
			cfg := employeeloop.Config{Tools: employeeSceneTools()}
			if mode != "legacy" {
				cfg.Tools = employeeDiscoveryTools(cfg.Tools)
			}
			if mode == "task_wake" {
				job.Kind = employeeentry.KindTaskWake
			}
			if mode == "multi_source" {
				sources = append(append([]employeeSourceMessage{}, messages...), messages[0])
			}
			employeeConfigureFirstFeedback(&cfg, job, sources, host.envelopes)
			feedbackTools := 0
			for _, tool := range cfg.Tools {
				if tool.Name == "first_feedback" {
					feedbackTools++
				}
			}
			if mode == "new" {
				if !cfg.StreamedFeedback || feedbackTools != 1 {
					t.Fatal("eligible new input was not frozen", cfg)
				}
			} else if cfg.StreamedFeedback || feedbackTools != 0 {
				t.Fatal("feedback leaked into legacy/automation/ambiguous input", cfg)
			}
		})
	}
}
