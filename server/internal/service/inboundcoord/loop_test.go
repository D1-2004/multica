package inboundcoord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	errors map[string]error
}

func (s *stubTools) Call(_ context.Context, _ Turn, name, arguments string) (string, error) {
	s.calls = append(s.calls, name+" "+arguments)
	if err := s.errors[name]; err != nil {
		return "", err
	}
	switch name {
	case toolAssocRecall:
		if s.recall != "" {
			return s.recall, nil
		}
		return `{"items":[]}`, nil
	case toolAssocBind:
		var args bindArgs
		_ = json.Unmarshal([]byte(arguments), &args)
		payload := map[string]any{
			"purpose": strings.TrimSpace(args.Purpose),
			"intent":  strings.TrimSpace(args.Intent),
			"pending": strings.TrimSpace(args.IssueID) == "",
			"linked":  strings.TrimSpace(args.IssueID) != "",
		}
		if args.IssueID != "" {
			payload["issue_id"] = args.IssueID
		}
		raw, _ := json.Marshal(payload)
		return string(raw), nil
	case toolIssueGet:
		return `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","title":"向须莫v6确认今晚几点打球","status":"in_review"}`, nil
	case toolIssueCommentList:
		return `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","comments":[]}`, nil
	case toolIssueCommentAdd:
		return `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","issue_identifier":"WS-13","comment_id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","task_id":"cccccccc-cccc-cccc-cccc-cccccccccccc"}`, nil
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func TestLoopIssueCommentBusyRequestsDispatchRetry(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("comment", toolIssueCommentAdd, `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","content":"须莫v6 在钉钉会话中的消息：\n\n番茄","reply_text":"我把番茄这个答复带回去了。"}`),
	}}
	tools := &stubTools{
		recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"向须莫v6确认喜欢番茄还是菠萝","status":"waiting","on_this_scene":true,"why":"本会话事项"}]}`,
		errors: map[string]error{toolIssueCommentAdd: ErrIssueBusy},
	}
	decision, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source: SourceDigitalEmployee, Addressed: true, ChatType: "p2p", Message: "番茄", ConversationID: "cid-v6",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionRetry || decision.IssueID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Fatalf("decision=%#v", decision)
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

func TestConversationNamePrefersTitleThenSender(t *testing.T) {
	t.Parallel()
	if got := conversationName(Turn{ConversationTitle: "项目群", SenderName: "冬翔"}); got != "项目群" {
		t.Fatalf("title=%q", got)
	}
	if got := conversationName(Turn{SenderName: "冬翔"}); got != "冬翔" {
		t.Fatalf("sender=%q", got)
	}
}

func TestLoopLogsLLMRequestAndFinish(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("call-1", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("call-2", toolFinish, `{"action":"issue","text":"我先去问冬翔今天想吃什么","look_into":"向冬翔确认今天吃什么","delegator":"冬翔","purpose":"向冬翔确认今天吃什么","intent":"ask","reason":"要向同事确认"}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"issue-eat","purpose":"向冬翔确认今天吃什么","status":"waiting","on_this_scene":true,"why":"本会话事项"}]}`}
	c := &Coordinator{Chat: chat, Tools: tools}
	if _, err := c.runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Message:        "问一下冬翔，今天想吃什么",
		ConversationID: "cid-dongxiang",
		SenderName:     "冬翔",
		AgentName:      "预发测试智能体",
		WorkspaceID:    "ws-1",
		PersonID:       "uid-dx",
		Persona:        "靠谱同事",
	}); err != nil {
		t.Fatal(err)
	}
	logs := buf.String()
	for _, want := range []string{
		`"event":"inbound_coordinator_llm_request"`,
		`"conversation_id":"cid-dongxiang"`,
		`"conversation_name":"冬翔"`,
		`"sender_name":"冬翔"`,
		`"agent_name":"预发测试智能体"`,
		`"coord_trace_id":"`,
		"问一下冬翔，今天想吃什么",
		`"event":"inbound_coordinator_llm"`,
		`"tool":"assoc_recall"`,
		`"event":"inbound_coordinator_llm_finish"`,
		`"action":"issue"`,
		"向冬翔确认今天吃什么",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %s\n%s", want, logs)
		}
	}
}

func TestLoopRecallThenFinish(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("call-1", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("call-2", toolFinish, `{"action":"issue","text":"我先去问冬翔今天想吃什么","look_into":"向冬翔确认今天吃什么","delegator":"冬翔","purpose":"向冬翔确认今天吃什么","intent":"ask","reason":"要向同事确认"}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"issue-eat","purpose":"向冬翔确认今天吃什么","status":"waiting","on_this_scene":true,"why":"本会话事项"}]}`}
	c := &Coordinator{Chat: chat, Tools: tools}
	got, err := c.runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Message:        "问一下冬翔，今天想吃什么",
		ConversationID: "cid-dongxiang",
		PersonID:       "uid-dx",
		SenderName:     "冬翔",
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
	if got.Purpose != "冬翔委托：向冬翔确认今天吃什么" || got.Intent != "ask" {
		t.Fatalf("new issue purpose=%q intent=%q", got.Purpose, got.Intent)
	}
	if got.ToolRounds != 2 {
		t.Fatalf("tool_rounds=%d", got.ToolRounds)
	}
	if strings.Join(got.ToolsUsed, ",") != "assoc_recall,finish" {
		t.Fatalf("tools=%v", got.ToolsUsed)
	}
	if len(tools.calls) != 1 {
		t.Fatalf("tool calls=%v", tools.calls)
	}
	if len(chat.params) != 2 {
		t.Fatalf("rounds=%d", len(chat.params))
	}
	wantStepTypes := []string{"tool_use", "tool_result", "tool_use", "thinking", "text"}
	if len(got.Steps) != len(wantStepTypes) {
		t.Fatalf("steps=%#v", got.Steps)
	}
	for i, wantType := range wantStepTypes {
		if got.Steps[i].Seq != i+1 || got.Steps[i].Type != wantType {
			t.Fatalf("step[%d]=%#v, want seq=%d type=%s", i, got.Steps[i], i+1, wantType)
		}
	}
	if got.Steps[0].Tool != toolAssocRecall || got.Steps[1].Tool != toolAssocRecall ||
		got.Steps[2].Tool != toolFinish {
		t.Fatalf("tool timeline=%#v", got.Steps)
	}
	if got.Steps[3].Content != "要向同事确认" || got.Steps[4].Content != got.UserText {
		t.Fatalf("decision timeline=%#v", got.Steps)
	}
	if names := toolDefNames(chat.params[0]); strings.Join(names, ",") != "assoc_recall,assoc_bind,issue_get,issue_comment_list,issue_comment_add,finish" {
		t.Fatalf("round0 tools=%v", names)
	}
}

func TestLoopRecallFillsInboundConversationID(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"q":"辰驷"}`),
		assistantTool("finish", toolFinish, `{"action":"reply","text":"当前会话没有打球这件事。","reason":"场域无匹配"}`),
	}}
	tools := &stubTools{recall: `{"items":[]}`}
	inbound := "cid+bEFv7ngm9n79Q1vL9HYJw=="
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "和辰驷确认一下，明天几点有空去打球。",
		ConversationID: inbound,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionReply {
		t.Fatalf("action=%s", got.Action)
	}
	if len(tools.calls) == 0 || !strings.Contains(tools.calls[0], inbound) || !strings.Contains(tools.calls[0], "辰驷") {
		t.Fatalf("recall must keep inbound cid with q, calls=%v", tools.calls)
	}
	if len(got.Steps) == 0 || !strings.Contains(got.Steps[0].Input, inbound) {
		t.Fatalf("timeline must show inbound cid, steps=%#v", got.Steps)
	}
}

func TestLoopNewDeliverableFinishSkipsForcedComment(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("finish", toolFinish, `{"action":"issue","text":"我去问须莫v6今天晚饭想吃什么","look_into":"向须莫v6确认今天晚饭吃什么","delegator":"冬翔","purpose":"向须莫v6确认今天晚饭吃什么","intent":"ask","reason":"新的询问"}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"须莫🥥委托：看看联系人里有没有须莫v6","status":"waiting","on_this_scene":true,"why":"本会话事项"}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "你去问下须莫v6，今天晚饭想吃什么",
		ConversationID: "cid-v6",
		SenderName:     "冬翔",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue || got.IssueID != "" {
		t.Fatalf("decision=%#v", got)
	}
	if got.Purpose != "冬翔委托：向须莫v6确认今天晚饭吃什么" || got.Intent != "ask" {
		t.Fatalf("purpose=%q intent=%q", got.Purpose, got.Intent)
	}
}

func TestLoopInboundOutreachReplyContinuesRecalledIssue(t *testing.T) {
	t.Parallel()
	issueID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("comment", toolIssueCommentAdd, `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","content":"须莫v6 在钉钉会话中的消息：\n\n7点","reply_text":"我把7点这个答复带回去了。"}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"向须莫v6确认今晚几点打球","status":"waiting","on_this_scene":true,"why":"本会话事项","waiting_on":"cid-v6"}]}`}
	c := &Coordinator{Chat: chat, Tools: tools}

	got, err := c.runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Message:        "7点",
		ConversationID: "cid-v6",
		PersonID:       "uid-v6",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionReply || got.IssueID != issueID || got.IssueComment == nil {
		t.Fatalf("decision = %#v", got)
	}
	if got.IssueComment.CommentID != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
		t.Fatalf("issue comment = %#v", got.IssueComment)
	}
}

func TestLoopOneSceneCardAllowsNewIssueWhenPurposeDiffers(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("finish", toolFinish, `{"action":"issue","text":"我去问辰驷明天几点有空打球。","look_into":"冬翔委托：向辰驷确认明天几点有空去打球","delegator":"冬翔","purpose":"向辰驷确认明天几点有空去打球","intent":"ask","reason":"交付物不同"}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"冬翔委托：向辰驷确认明天洗脚时间","status":"waiting","on_this_scene":true,"why":"本会话事项","waiting_on":"cid-inbound"}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "和辰驷确认一下，明天几点有空去打球。",
		ConversationID: "cid-inbound",
		SenderName:     "冬翔",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue || got.IssueComment != nil {
		t.Fatalf("one scene card must not force comment-add, decision=%#v", got)
	}
	if got.Purpose != "冬翔委托：向辰驷确认明天几点有空去打球" {
		t.Fatalf("purpose=%q", got.Purpose)
	}
}

func TestLoopWindowKeywordHitDoesNotForceComment(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"q":"辰驷"}`),
		assistantTool("finish", toolFinish, `{"action":"issue","text":"我去问辰驷明天几点有空打球。","look_into":"向辰驷确认明天几点有空去打球","delegator":"冬翔","purpose":"向辰驷确认明天几点有空去打球","intent":"ask","reason":"新的询问"}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"向辰驷确认明天什么时候去洗脚","status":"waiting","on_this_scene":false,"why":"关键词命中，不是本会话"}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "和辰驷确认一下，明天几点有空去打球。",
		ConversationID: "cid-inbound",
		SenderName:     "冬翔",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue || got.IssueComment != nil {
		t.Fatalf("window hit must not force comment-add, decision=%#v", got)
	}
	if got.Purpose != "冬翔委托：向辰驷确认明天几点有空去打球" {
		t.Fatalf("purpose=%q", got.Purpose)
	}
}

func TestLoopBindWithoutIssueIDIsRejected(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("bind", toolAssocBind, `{"conversation_id":"cid-v6","delegator":"须莫🥥","intent":"ask","purpose":"向须莫v6询问明早有没有会议"}`),
		assistantTool("finish", toolFinish, `{"action":"issue","text":"我去问须莫v6明早有没有会议","delegator":"须莫🥥","purpose":"向须莫v6询问明早有没有会议","intent":"ask","reason":"新事项必须落到 Issue"}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"须莫🥥委托：向须莫v6询问晚上有没有会议","status":"waiting","on_this_scene":true,"why":"本会话事项"}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "你再问一下须莫v6明早有没有会议",
		ConversationID: "cid-v6",
		SenderName:     "须莫🥥",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue || got.IssueID != "" {
		t.Fatalf("decision=%#v", got)
	}
	if got.Purpose != "须莫🥥委托：向须莫v6询问明早有没有会议" {
		t.Fatalf("purpose=%q", got.Purpose)
	}
	if len(tools.calls) != 1 || !strings.HasPrefix(tools.calls[0], toolAssocRecall) {
		t.Fatalf("assoc_bind without issue_id must not run, calls=%v", tools.calls)
	}
	sawBindError := false
	for _, step := range got.Steps {
		if step.Tool == toolAssocBind && step.Error {
			sawBindError = true
			if !strings.Contains(step.Output, `"hint"`) || !strings.Contains(step.Output, "finish action=issue") {
				t.Fatalf("bind error must include recovery hint, output=%s", step.Output)
			}
		}
	}
	if !sawBindError {
		t.Fatalf("expected assoc_bind error step, steps=%#v", got.Steps)
	}
}

func TestLoopNewIssueFinishRequiresPurpose(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("finish-bad", toolFinish, `{"action":"issue","text":"我去问","look_into":"会议","reason":"新事项"}`),
		assistantTool("finish-ok", toolFinish, `{"action":"issue","text":"我去问须莫v6明早有没有会议","delegator":"须莫🥥","purpose":"向须莫v6询问明早有没有会议","intent":"ask","reason":"新事项"}`),
	}}
	tools := &stubTools{recall: `{"items":[]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		Message:        "问一下须莫v6明早有没有会议",
		ConversationID: "cid-v6",
		SenderName:     "须莫🥥",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue || got.Purpose != "须莫🥥委托：向须莫v6询问明早有没有会议" {
		t.Fatalf("decision=%#v", got)
	}
	sawHint := false
	for _, step := range got.Steps {
		if step.Tool == toolFinish && step.Error && strings.Contains(step.Output, `"hint"`) && strings.Contains(step.Output, "delegator") {
			sawHint = true
		}
	}
	if !sawHint {
		t.Fatalf("finish error must include recovery hint, steps=%#v", got.Steps)
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
	if strings.Join(first, ",") != "assoc_recall,assoc_bind,issue_get,issue_comment_list,issue_comment_add,finish" {
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
	decision, err := c.runLoop(context.Background(), Turn{Source: SourceWeb, Message: "查一下"})
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("err=%v", err)
	}
	if len(decision.Steps) == 0 || decision.Steps[len(decision.Steps)-1].Type != "error" {
		t.Fatalf("failed loop lost timeline: %#v", decision.Steps)
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
	if strings.Join(names, ",") != "assoc_recall,assoc_bind,issue_get,issue_comment_list,issue_comment_add,finish" {
		t.Fatalf("tools=%v", names)
	}
}

func TestIssueCommentToolSeparatesDingTalkSpeakerFromToolExecutor(t *testing.T) {
	t.Parallel()
	var description string
	for _, tool := range coordinatorToolDefs() {
		fn := tool.GetFunction()
		if fn != nil && fn.Name == toolIssueCommentAdd && fn.Description.Valid() {
			description = fn.Description.Value
			break
		}
	}
	for _, want := range []string{
		"current DingTalk event sender is the actual speaker",
		"comment author is only the workspace principal executing this Issue tool",
		"not evidence of the delegator, speaker, or recipient",
		"both digital-employee and robot messages",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("issue_comment_add description missing %q: %q", want, description)
		}
	}
}

func TestAssocBindSchemaRequiresIssueID(t *testing.T) {
	t.Parallel()
	params := coordinatorFunctionParams(toolAssocBind)
	if params == nil {
		t.Fatal("assoc_bind missing")
	}
	if params["additionalProperties"] != false {
		t.Fatalf("assoc_bind must reject extra properties, additionalProperties=%v", params["additionalProperties"])
	}
	required := stringSlice(params["required"])
	for _, want := range []string{"issue_id", "purpose", "intent", "delegator"} {
		if !containsString(required, want) {
			t.Fatalf("assoc_bind required missing %s: %v", want, required)
		}
	}
	props, _ := params["properties"].(map[string]any)
	issue, _ := props["issue_id"].(map[string]any)
	if issue["minLength"] != 8 {
		t.Fatalf("issue_id schema=%v", issue)
	}
	if !strings.Contains(fmt.Sprint(issue["description"]), "Required") {
		t.Fatalf("issue_id description=%v", issue["description"])
	}
}

func TestMarshalToolFailureIncludesHint(t *testing.T) {
	t.Parallel()
	raw := marshalToolFailure(hintErr("issue_id is required", hintBindNeedsIssue))
	if !strings.Contains(raw, `"error":"issue_id is required"`) || !strings.Contains(raw, `"hint"`) || !strings.Contains(raw, "finish action=issue") {
		t.Fatalf("failure=%s", raw)
	}
}

func coordinatorFunctionParams(name string) map[string]any {
	for _, tool := range coordinatorToolDefs() {
		fn := tool.GetFunction()
		if fn != nil && fn.Name == name {
			return fn.Parameters
		}
	}
	return nil
}

func stringSlice(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, _ := item.(string)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestFinishToolRoutesUnavailableCapabilitiesToIssue(t *testing.T) {
	t.Parallel()
	fn := coordinatorFinishTool().GetFunction()
	if fn == nil || !fn.Description.Valid() {
		t.Fatal("finish tool description is missing")
	}
	description := fn.Description.Value
	for _, rule := range []string{
		"Use reply when current_message is a greeting or does not advance a recalled purpose",
		"Use issue for contacts, DWS, search, files",
		"Never use reply to say you cannot complete the request",
		"issue_comment_add",
		"never with finish issue_id",
	} {
		if !strings.Contains(description, rule) {
			t.Fatalf("finish tool description missing %q: %q", rule, description)
		}
	}
	params := coordinatorFunctionParams(toolFinish)
	if params == nil {
		t.Fatal("finish missing")
	}
	props, _ := params["properties"].(map[string]any)
	if _, ok := props["issue_id"]; ok {
		t.Fatal("finish must not advertise issue_id")
	}
}

func TestLoopFinishIssueIDIsRejectedThenCommentAdd(t *testing.T) {
	t.Parallel()
	issueID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("finish-id", toolFinish, `{"action":"issue","issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`),
		assistantTool("comment", toolIssueCommentAdd, `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","content":"须莫v6 在钉钉会话中的消息：\n\n7点","reply_text":"我把7点这个答复带回去了。"}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"向须莫v6确认今晚几点打球","status":"waiting","on_this_scene":true,"why":"本会话事项"}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Message:        "7点",
		ConversationID: "cid-v6",
		PersonID:       "uid-v6",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionReply || got.IssueID != issueID || got.IssueComment == nil {
		t.Fatalf("decision=%#v", got)
	}
	sawHint := false
	for _, step := range got.Steps {
		if step.Tool == toolFinish && step.Error && strings.Contains(step.Output, "issue_comment_add") {
			sawHint = true
		}
	}
	if !sawHint {
		t.Fatalf("finish issue_id must be rejected with comment hint, steps=%#v", got.Steps)
	}
}

func TestLoopFinishIssueEmptyTextIsRejectedThenSpoken(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("finish-empty", toolFinish, `{"action":"issue","delegator":"冬翔","purpose":"向冬翔确认今天吃什么","intent":"ask"}`),
		assistantTool("finish-ok", toolFinish, `{"action":"issue","text":"我去问冬翔今天想吃什么","delegator":"冬翔","purpose":"向冬翔确认今天吃什么","intent":"ask","reason":"新事项"}`),
	}}
	tools := &stubTools{recall: `{"items":[]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Message:        "问一下冬翔，今天想吃什么",
		ConversationID: "cid-dongxiang",
		SenderName:     "冬翔",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue || got.UserText != "我去问冬翔今天想吃什么" || got.IssueID != "" {
		t.Fatalf("decision=%#v", got)
	}
	if strings.Contains(got.UserText, "核对") {
		t.Fatalf("synthesized ack: %q", got.UserText)
	}
	sawHint := false
	for _, step := range got.Steps {
		if step.Tool == toolFinish && step.Error && strings.Contains(step.Output, "needs text") {
			sawHint = true
		}
	}
	if !sawHint {
		t.Fatalf("empty issue text must be rejected, steps=%#v", got.Steps)
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
