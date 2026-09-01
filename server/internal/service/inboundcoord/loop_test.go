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

func TestLoopContentJSONWithoutToolCallsNudgeThenFinish(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantJSON(`{"action":"reply","text":"这个单聊在跟高铁还是开车","look_into":"","reason":"猜的"}`),
		assistantTool("f1", toolFinish, `{"action":"reply","text":"在，有事直接说。","look_into":"","reason":"打招呼"}`),
	}}
	c := &Coordinator{Chat: chat}
	got, err := c.runLoop(context.Background(), Turn{Source: SourceWeb, Message: "你好"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionReply || got.UserText != "在，有事直接说。" {
		t.Fatalf("got %#v", got)
	}
	if strings.Join(got.ToolsUsed, ",") != toolFinish {
		t.Fatalf("tools=%v", got.ToolsUsed)
	}
	if chat.calls != 2 {
		t.Fatalf("content JSON must not finish the loop, calls=%d", chat.calls)
	}
}

func TestLoopLastRoundOnlyFinish(t *testing.T) {
	t.Parallel()
	last := toolDefNamesFromDefs(toolsForRound(maxLoopRounds - 1))
	if strings.Join(last, ",") != toolFinish {
		t.Fatalf("last-round tools=%v, want only finish", last)
	}
	first := toolDefNamesFromDefs(toolsForRound(0))
	if strings.Join(first, ",") != "assoc_recall,assoc_bind,finish" {
		t.Fatalf("round0 tools=%v", first)
	}
}

func TestLoopExceedsRounds(t *testing.T) {
	t.Parallel()
	rounds := make([]openai.ChatCompletion, maxLoopRounds)
	for i := range rounds {
		rounds[i] = assistantTool(fmt.Sprintf("c%d", i), toolAssocRecall, `{}`)
	}
	chat := &scriptedCompleter{rounds: rounds}
	c := &Coordinator{Chat: chat, Tools: &stubTools{}}
	_, err := c.runLoop(context.Background(), Turn{Source: SourceWeb, Message: "查一下"})
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("err=%v", err)
	}
}

func TestDecideRunsToolLoop(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("f1", toolFinish, `{"action":"reply","text":"在，有事直接说。","reason":"打招呼"}`),
	}}
	c := &Coordinator{
		LLM:  llm.New(llm.Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"}),
		Chat: chat,
	}
	got := c.Decide(context.Background(), Turn{Source: SourceWeb, Addressed: true, Message: "你好"})
	if got.Action != ActionReply || got.UserText != "在，有事直接说。" {
		t.Fatalf("got %#v", got)
	}
	if chat.calls != 1 {
		t.Fatalf("Decide must run the tool loop, chat.calls=%d", chat.calls)
	}
}

func TestLoopNamedCIDFinishWithoutRecallIsRejected(t *testing.T) {
	t.Parallel()
	fake := "cid+bEFv7ngm9n79Q1vL9HYJ1w=="
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("f0", toolFinish, `{"action":"reply","text":"这个单聊在跟高铁还是开车","reason":"猜的"}`),
		assistantTool("r1", toolAssocRecall, `{"conversation_id":"cid+bEFv7ngm9n79Q1vL9HYJ1w==","since":"48h"}`),
		assistantTool("f1", toolFinish, `{"action":"reply","text":"这个会话在图上没有记录。","reason":"recall 为空"}`),
	}}
	tools := &stubTools{recall: `{"items":[]}`}
	c := &Coordinator{Chat: chat, Tools: tools}
	got, err := c.runLoop(context.Background(), Turn{
		Source:         SourceRobot,
		Addressed:      true,
		Message:        fake + " 里面聊了什么",
		ConversationID: "cid-robot",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionReply || !strings.Contains(got.UserText, "没有记录") {
		t.Fatalf("got %#v", got)
	}
	if strings.Join(got.ToolsUsed, ",") != "assoc_recall,finish" {
		t.Fatalf("tools=%v", got.ToolsUsed)
	}
	if len(tools.calls) != 1 || !strings.Contains(tools.calls[0], fake) {
		t.Fatalf("must recall the fake cid, calls=%v", tools.calls)
	}
}

func TestExtractConversationIDs(t *testing.T) {
	t.Parallel()
	got := extractConversationIDs("cid+bEFv7ngm9n79Q1vL9HYJ1w== 里面聊了什么")
	if len(got) != 1 || got[0] != "cid+bEFv7ngm9n79Q1vL9HYJ1w==" {
		t.Fatalf("got=%v", got)
	}
}

func TestDecideNamedConversationRecallThenReply(t *testing.T) {
	t.Parallel()
	named := "cid+bEFv7ngm9n79Q1vL9HYJw=="
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("call-1", toolAssocRecall, `{"conversation_id":"cid+bEFv7ngm9n79Q1vL9HYJw==","since":"48h"}`),
		assistantTool("call-2", toolFinish, `{"action":"reply","text":"这个单聊最近在跟「向冬翔确认明天去上海是坐高铁还是开车」。","look_into":"","reason":"图上已有这件事"}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue":"WS-9","purpose":"向冬翔确认明天去上海是坐高铁还是开车","status":"waiting"}]}`}
	c := &Coordinator{
		LLM:        llm.New(llm.Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"}),
		Chat:       chat,
		Tools:      tools,
		DWSHistory: &dwsHistoryStub{},
	}
	got := c.Decide(context.Background(), Turn{
		Source:         SourceRobot,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "cid+bEFv7ngm9n79Q1vL9HYJw== 里面聊了什么",
		ConversationID: "cid-robot",
		DWSUID:         "24710833",
		DWSOrgID:       "439446171",
	})
	if got.Action != ActionReply {
		t.Fatalf("action=%s", got.Action)
	}
	if !strings.Contains(got.UserText, "高铁") {
		t.Fatalf("text=%q", got.UserText)
	}
	if strings.Join(got.ToolsUsed, ",") != "assoc_recall,finish" {
		t.Fatalf("tools=%v", got.ToolsUsed)
	}
	if len(tools.calls) != 1 || !strings.Contains(tools.calls[0], named) {
		t.Fatalf("recall must use the named cid, calls=%v", tools.calls)
	}
}

func TestCoordinatorToolDefsIncludeAssocAndFinish(t *testing.T) {
	t.Parallel()
	names := toolDefNamesFromDefs(coordinatorToolDefs())
	if strings.Join(names, ",") != "assoc_recall,assoc_bind,finish" {
		t.Fatalf("tools=%v", names)
	}
}

func TestFinishToolRoutesUnavailableCapabilitiesToIssue(t *testing.T) {
	t.Parallel()
	fn := coordinatorFinishTool().GetFunction()
	if fn == nil || !fn.Description.Valid() {
		t.Fatal("finish tool description is missing")
	}
	description := fn.Description.Value
	for _, rule := range []string{
		"Use reply only for a complete answer available now",
		"Use issue for contacts, DWS, search, files",
		"Never use reply to say you cannot complete the request",
	} {
		if !strings.Contains(description, rule) {
			t.Fatalf("finish tool description missing %q: %q", rule, description)
		}
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

func TestBuildUserPromptIncludesPersonaAndTone(t *testing.T) {
	t.Parallel()
	prompt := buildUserPrompt(Turn{
		Source:       SourceRobot,
		Addressed:    true,
		AgentName:    "菲迪",
		Persona:      "你是靠谱的同事，先把事实说清楚。",
		ReplyTone:    "短句、不客套。",
		Instructions: "Always search the web first.",
		Message:      "你好",
	})
	for _, want := range []string{
		"agent_persona: 你是靠谱的同事，先把事实说清楚。",
		"agent_reply_tone: 短句、不客套。",
		"agent_instructions:",
		"Always search the web first.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
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
