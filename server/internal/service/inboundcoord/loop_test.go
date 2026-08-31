package inboundcoord

import (
	"context"
	"fmt"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"

	"github.com/multica-ai/multica/server/pkg/llm"
)

type scriptedCompleter struct {
	rounds []openai.ChatCompletion
	calls  int
	params []openai.ChatCompletionNewParams
}

func (s *scriptedCompleter) Chat(_ context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	s.params = append(s.params, params)
	if s.calls >= len(s.rounds) {
		return nil, fmt.Errorf("unexpected extra round %d", s.calls)
	}
	out := s.rounds[s.calls]
	s.calls++
	return &out, nil
}

type stubTools struct {
	calls  []string
	recall string
}

func (s *stubTools) Call(_ context.Context, _ Turn, name, arguments string) (string, error) {
	s.calls = append(s.calls, name+" "+arguments)
	switch name {
	case toolAssocRecall:
		if s.recall != "" {
			return s.recall, nil
		}
		return `{"items":[]}`, nil
	case toolAssocBind:
		return `{"linked":true}`, nil
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func assistantJSON(content string) openai.ChatCompletion {
	return openai.ChatCompletion{Choices: []openai.ChatCompletionChoice{{
		Message: openai.ChatCompletionMessage{Content: content, Role: "assistant"},
	}}}
}

func assistantTool(id, name, args string) openai.ChatCompletion {
	return openai.ChatCompletion{Choices: []openai.ChatCompletionChoice{{
		Message: openai.ChatCompletionMessage{
			Role: "assistant",
			ToolCalls: []openai.ChatCompletionMessageToolCallUnion{{
				ID:   id,
				Type: "function",
				Function: openai.ChatCompletionMessageFunctionToolCallFunction{
					Name:      name,
					Arguments: args,
				},
			}},
		},
	}}}
}

func TestLoopRecallThenFinish(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("call-1", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("call-2", toolFinish, `{"action":"issue","text":"我先去问冬翔今天想吃什么","look_into":"向冬翔确认今天吃什么","reason":"要向同事确认"}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue":"issue-eat","purpose":"向冬翔确认今天吃什么","status":"waiting"}]}`}
	c := &Coordinator{Chat: chat, Tools: tools}
	got, err := c.runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Message:        "问一下冬翔，今天想吃什么",
		ConversationID: "cid-dongxiang",
		PersonID:       "uid-dx",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue {
		t.Fatalf("action=%s", got.Action)
	}
	if got.LookInto != "向冬翔确认今天吃什么" {
		t.Fatalf("look_into=%q", got.LookInto)
	}
	if got.ToolRounds != 2 {
		t.Fatalf("tool_rounds=%d", got.ToolRounds)
	}
	if strings.Join(got.ToolsUsed, ",") != "assoc_recall,finish" {
		t.Fatalf("tools=%v", got.ToolsUsed)
	}
	if len(tools.calls) != 1 || !strings.HasPrefix(tools.calls[0], toolAssocRecall) {
		t.Fatalf("tool calls=%v", tools.calls)
	}
	if len(chat.params) != 2 {
		t.Fatalf("rounds=%d", len(chat.params))
	}
	if names := toolDefNames(chat.params[0]); strings.Join(names, ",") != "assoc_recall,assoc_bind,finish" {
		t.Fatalf("round0 tools=%v", names)
	}
}

func TestLoopContentJSONWithoutToolCalls(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantJSON(`{"action":"reply","text":"在，有事直接说。","look_into":"","reason":"打招呼"}`),
	}}
	c := &Coordinator{Chat: chat}
	got, err := c.runLoop(context.Background(), Turn{Source: SourceWeb, Message: "你好"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionReply || got.UserText != "在，有事直接说。" {
		t.Fatalf("got %#v", got)
	}
	if got.ToolRounds != 0 || len(got.ToolsUsed) != 0 {
		t.Fatalf("unexpected tools %#v", got)
	}
}

func TestLoopLastRoundOnlyFinish(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("c1", toolAssocRecall, `{}`),
		assistantTool("c2", toolAssocRecall, `{}`),
		assistantTool("c3", toolFinish, `{"action":"issue","text":"我先去核对报名表","look_into":"报名表截止时间","reason":"要查资料"}`),
	}}
	c := &Coordinator{Chat: chat, Tools: &stubTools{}}
	got, err := c.runLoop(context.Background(), Turn{Source: SourceRobot, Message: "查一下截止时间"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue {
		t.Fatalf("action=%s", got.Action)
	}
	if len(chat.params) != 3 {
		t.Fatalf("rounds=%d", len(chat.params))
	}
	last := toolDefNames(chat.params[2])
	if strings.Join(last, ",") != toolFinish {
		t.Fatalf("last-round tools=%v, want only finish", last)
	}
}

func TestLoopExceedsRounds(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("c1", toolAssocRecall, `{}`),
		assistantTool("c2", toolAssocRecall, `{}`),
		assistantTool("c3", toolAssocRecall, `{}`),
	}}
	c := &Coordinator{Chat: chat, Tools: &stubTools{}}
	_, err := c.runLoop(context.Background(), Turn{Source: SourceWeb, Message: "查一下"})
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("err=%v", err)
	}
}

func TestDecideDoesNotCallChatLoop(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantJSON(`{"action":"reply","text":"在。","reason":"打招呼"}`),
	}}
	c := &Coordinator{
		LLM:  llm.New(llm.Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"}),
		Chat: chat,
	}
	got := c.Decide(context.Background(), Turn{Source: SourceWeb, Addressed: true, Message: "你好"})
	if got.Action != ActionContinue {
		t.Fatalf("unreachable LLM must fail open, action=%s", got.Action)
	}
	if chat.calls != 0 {
		t.Fatalf("Decide must not run the leftover tool loop, chat.calls=%d", chat.calls)
	}
}

func TestCoordinatorToolDefsIncludeAssocAndFinish(t *testing.T) {
	t.Parallel()
	names := toolDefNamesFromDefs(coordinatorToolDefs())
	if strings.Join(names, ",") != "assoc_recall,assoc_bind,finish" {
		t.Fatalf("tools=%v", names)
	}
}

func toolDefNames(params openai.ChatCompletionNewParams) []string {
	return toolDefNamesFromDefs(params.Tools)
}

func toolDefNamesFromDefs(tools []openai.ChatCompletionToolUnionParam) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		fn := tool.GetFunction()
		if fn == nil {
			continue
		}
		names = append(names, fn.Name)
	}
	return names
}

func TestIdentityNoteDigitalEmployeeComplete(t *testing.T) {
	t.Parallel()
	got := IdentityNote(SourceDigitalEmployee, "cid", "uid")
	if !strings.Contains(got, "complete") {
		t.Fatalf("note=%q", got)
	}
}

func TestBuildUserPromptIncludesConversationID(t *testing.T) {
	t.Parallel()
	prompt := buildUserPrompt(Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ConversationID: "cid-dongxiang",
		PersonID:       "123456",
		IdentityNote:   IdentityNote(SourceDigitalEmployee, "cid-dongxiang", "123456"),
		Message:        "问一下冬翔，今天想吃什么",
	})
	for _, want := range []string{
		"conversation_id: cid-dongxiang",
		"person_id: 123456",
		"identity_note: digital-employee inbound: conversation_id and uid are complete",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

var _ Completer = (*scriptedCompleter)(nil)
var _ Tools = (*stubTools)(nil)
