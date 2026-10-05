package employeeloop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func TestReplyProtocolRejectsAccidentWithoutExecutingText(t *testing.T) {
	const leaked = `<reply>明白了，我先查那个 Webhook 例行任务到底建了没。
<find_tasks source_ref="bf9effda-9714-4a61-b229-62001dac27b1/msg-example=="></find_tasks></reply>`
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "budget_exhausted", true: "native_repair"}[repair], func(t *testing.T) {
			cfg := testConfig()
			cfg.Tools = append(cfg.Tools, Tool{Name: "find_tasks", Schema: map[string]any{}})
			calls, reads := 0, 0
			model := modelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				calls++
				if calls == 1 || !repair {
					return completion(t, leaked, "stop"), nil
				}
				for _, m := range p.Messages {
					if m.OfAssistant != nil && strings.Contains(m.OfAssistant.Content.OfString.Value, "<find_tasks") {
						t.Fatal("invalid reply entered assistant history")
					}
				}
				if calls == 2 {
					return completion(t, "", "tool_calls", nativeCall("real-read", "find_tasks", `{}`)), nil
				}
				return completion(t, "没有找到此前的创建任务。", "stop"), nil
			})
			host := hostFunc(func(_ context.Context, _ Identity, call ToolCall) (ToolResult, error) {
				if call.Name != "find_tasks" || call.NativeToolCallID != "real-read" {
					t.Fatal("text produced an effect", call)
				}
				reads++
				return ToolResult{Content: `{"candidates":[]}`}, nil
			})
			out, err := New(cfg, model, host).Run(context.Background(), testInput())
			if calls != MaxModelCalls || strings.Contains(out.Reply, "<") || len(out.Receipts) != 0 {
				t.Fatal(out, calls, err)
			}
			if repair && (err != nil || reads != 1 || out.Kind != Reply) {
				t.Fatal(out, reads, err)
			}
			if !repair && (!errors.Is(err, ErrModelBudget) || reads != 0 || out.Kind != "") {
				t.Fatal(out, reads, err)
			}
		})
	}
}

func TestReplyProtocolChecksNativePublicFieldsBeforeEffects(t *testing.T) {
	for _, name := range []string{"reply", "describe_capabilities", "dispatch_task"} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			cfg.Tools = append(cfg.Tools, Tool{Name: name, Schema: map[string]any{}})
			hostCalls, modelCalls := 0, 0
			model := modelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				modelCalls++
				if modelCalls == 1 {
					return completion(t, "", "tool_calls", nativeCall("bad-public", name, `{"reply":"<dispatch_task>已安排创建</dispatch_task>"}`)), nil
				}
				return completion(t, "缺少触发后要执行的内容，请补充。", "stop"), nil
			})
			host := hostFunc(func(context.Context, Identity, ToolCall) (ToolResult, error) {
				hostCalls++
				return ToolResult{}, nil
			})
			out, err := New(cfg, model, host).Run(context.Background(), testInput())
			if err != nil || hostCalls != 0 || modelCalls != 2 || out.Kind != Reply {
				t.Fatal(out, hostCalls, modelCalls, err)
			}
		})
	}
}

func TestReplyProtocolReservesOnlyInternalSyntax(t *testing.T) {
	tools := []Tool{{Name: "reply"}, {Name: "find_tasks"}}
	for _, text := range []string{
		"<find_tasks></find_tasks>", "<FIND_TASKS\nsource_ref='opaque' />", "<reply>查一下</reply>",
		`{"source_ref":"bf9effda-9714-4a61-b229-62001dac27b1/msg-example=="}`,
		"bf9effda-9714-4a61-b229-62001dac27b1/msgzf3X+YTw0AEZn53II6887g==", "<find_tasks",
	} {
		if ValidateReplyProtocol(text, tools) == nil {
			t.Fatal("protocol accepted", text)
		}
	}
	for _, text := range []string{"<div>业务内容</div>", "3 < 5，5 > 3", `{"source_ref":"business-id","url":"https://example.com"}`, `{"path":"bf9effda-9714-4a61-b229-62001dac27b1/msgdocs/report.json"}`, "我来处理创建请求，完成后告诉你实际结果。"} {
		if err := ValidateReplyProtocol(text, tools); err != nil {
			t.Fatal("business text rejected", text, err)
		}
	}
}

func TestReplyProtocolRejectsStreamedFeedbackBeforePublicEffect(t *testing.T) {
	hostCalls := 0
	loop := New(feedbackTestConfig(), nil, hostFunc(func(context.Context, Identity, ToolCall) (ToolResult, error) {
		hostCalls++
		return ToolResult{Receipt: "feedback"}, nil
	}))
	loop.state.ModelCalls = 1
	args, _ := json.Marshal(map[string]string{"source_ref": "s1", "intent": "lookup", "text": "<find_tasks>查询</find_tasks>"})
	raw, _ := json.Marshal(map[string]any{"id": "stream-1", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{streamNative(0, "first", FirstFeedbackToolName, string(args))}}}}})
	var chunk openai.ChatCompletionChunk
	if err := json.Unmarshal(raw, &chunk); err != nil {
		t.Fatal(err)
	}
	if err := loop.feedbackObserver(context.Background())(chunk); err == nil || hostCalls != 0 {
		t.Fatal("protocol feedback escaped before complete batch", hostCalls, err)
	}
}
