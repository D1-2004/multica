package employeeloop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/llm"
)

func feedbackTestConfig() Config {
	stringField := map[string]any{"type": "string"}
	return Config{StreamedFeedback: true, Tools: []Tool{
		{Name: FirstFeedbackToolName, Effect: true, Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": stringField, "text": stringField, "intent": stringField}, "required": []any{"source_ref", "text", "intent"}}},
		{Name: "find_tasks", Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": stringField}, "required": []any{"source_ref"}}},
		{Name: "reply", Effect: true, Terminal: Reply, Schema: map[string]any{"type": "object", "properties": map[string]any{"reply": stringField}, "required": []any{"reply"}}},
	}}
}

func writeStreamChunk(t *testing.T, w http.ResponseWriter, delta map[string]any, finish any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": "stream-1", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
	w.(http.Flusher).Flush()
}

func streamNative(index int, id, name, arguments string) map[string]any {
	return map[string]any{"index": index, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}
}

func TestStreamedFeedbackBeforeProviderFinishAndReadsAfterFullBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	feedbackSeen := make(chan struct{})
	var requests, effects, reads, hostCalls atomic.Int32
	var finished atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["stream"] != true {
			t.Error("provider was not streamed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 2 {
			for _, tool := range body["tools"].([]any) {
				if tool.(map[string]any)["function"].(map[string]any)["name"] == FirstFeedbackToolName {
					t.Error("later request offered first_feedback")
				}
			}
			writeStreamChunk(t, w, map[string]any{"role": "assistant", "content": "已查到你在这个群发起的事项。"}, "stop")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		writeStreamChunk(t, w, map[string]any{"role": "assistant", "content": "不会透传的普通正文", "reasoning_content": "不会透传的推理"}, nil)
		writeStreamChunk(t, w, map[string]any{"tool_calls": []any{streamNative(0, "fb-1", FirstFeedbackToolName, `{"source_ref":"s1","intent":"lookup","text":"我先查一下`)}}, nil)
		if effects.Load() != 0 {
			t.Error("partial arguments escaped")
		}
		writeStreamChunk(t, w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": `你在这个群发起的事项。"}`}}}}, nil)
		select {
		case <-feedbackSeen:
		case <-ctx.Done():
			t.Error("feedback was held until stream end")
			return
		}
		if reads.Load() != 0 || finished.Load() {
			t.Error("business tool executed before full response")
		}
		writeStreamChunk(t, w, map[string]any{"tool_calls": []any{streamNative(1, "find-1", "find_tasks", `{"source_ref":"s1"}`)}}, nil)
		finished.Store(true)
		writeStreamChunk(t, w, map[string]any{}, "tool_calls")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	host := hostFunc(func(_ context.Context, identity Identity, call ToolCall) (ToolResult, error) {
		if identity != testInput().Identity {
			t.Fatal("stream lost trusted identity")
		}
		if call.Name == FirstFeedbackToolName {
			if hostCalls.Add(1) == 1 {
				if finished.Load() || call.Arguments["text"] != "我先查一下你在这个群发起的事项。" {
					t.Fatal("feedback not complete or not early")
				}
				effects.Add(1) // fake durable journal: replay has no second effect
				close(feedbackSeen)
			}
			return ToolResult{Content: `{"accepted":true,"terminal":false}`, Receipt: "first-feedback:job:receipt"}, nil
		}
		if !finished.Load() || call.Name != "find_tasks" {
			t.Fatal("unexpected early/non-read business execution")
		}
		reads.Add(1)
		return ToolResult{Content: `{"tasks":[]}`}, nil
	})
	model := llm.New(llm.Config{BaseURL: srv.URL, APIKey: "fixture", MaxRetries: -1})
	out, err := New(feedbackTestConfig(), model, host).Run(ctx, testInput())
	if err != nil || out.Kind != Reply || out.Reply != "已查到你在这个群发起的事项。" || requests.Load() != 2 || out.ModelCalls != 2 || effects.Load() != 1 || hostCalls.Load() != 2 || reads.Load() != 1 {
		t.Fatalf("out=%+v err=%v requests=%d feedbackEffects=%d feedbackCalls=%d reads=%d", out, err, requests.Load(), effects.Load(), hostCalls.Load(), reads.Load())
	}
}

func TestStreamedIncompleteOrInvalidBusinessBatchExecutesNoBusinessTools(t *testing.T) {
	for _, mode := range []string{"truncated", "length", "malformed", "bad_schema", "terminal_mix", "feedback_only", "no_public_frame"} {
		t.Run(mode, func(t *testing.T) {
			var requests, feedback, business atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if requests.Add(1) > 1 {
					writeStreamChunk(t, w, map[string]any{"role": "assistant", "content": "刚才没有完成查询。"}, "stop")
					_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
					return
				}
				writeStreamChunk(t, w, map[string]any{"role": "assistant", "content": "正文和推理不能作为反馈", "reasoning_content": "内部推理"}, nil)
				if mode != "no_public_frame" {
					writeStreamChunk(t, w, map[string]any{"tool_calls": []any{streamNative(0, "fb-1", FirstFeedbackToolName, `{"source_ref":"s1","intent":"lookup","text":"我先查一下。"}`)}}, nil)
				}
				if mode == "truncated" {
					writeStreamChunk(t, w, map[string]any{"tool_calls": []any{streamNative(1, "read-1", "find_tasks", `{"source_ref":`)}}, nil)
					return
				}
				name, args := "find_tasks", `{"source_ref":"s1"}`
				switch mode {
				case "malformed":
					args = `{"source_ref":`
				case "bad_schema":
					args = `{}`
				case "terminal_mix":
					name, args = "reply", `{"reply":"直接回答"}`
				case "no_public_frame":
					args = `{"source_ref":`
				}
				if mode != "feedback_only" {
					writeStreamChunk(t, w, map[string]any{"tool_calls": []any{streamNative(1, "read-1", name, args)}}, nil)
				}
				finish := "tool_calls"
				if mode == "length" {
					finish = "length"
				}
				writeStreamChunk(t, w, map[string]any{}, finish)
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()
			host := hostFunc(func(_ context.Context, _ Identity, c ToolCall) (ToolResult, error) {
				if c.Name == FirstFeedbackToolName {
					feedback.Add(1)
					return ToolResult{Content: "accepted", Receipt: "first-feedback:fixture"}, nil
				}
				business.Add(1)
				return ToolResult{Content: "unexpected"}, nil
			})
			out, err := New(feedbackTestConfig(), llm.New(llm.Config{BaseURL: srv.URL, APIKey: "fixture", MaxRetries: -1}), host).Run(context.Background(), testInput())
			wantFeedback := int32(1)
			if mode == "no_public_frame" {
				wantFeedback = 0
			}
			if err != nil || out.Kind != Reply || out.ModelCalls != 2 || business.Load() != 0 || feedback.Load() != wantFeedback {
				t.Fatalf("out=%+v err=%v feedback=%d business=%d", out, err, feedback.Load(), business.Load())
			}
		})
	}
}
