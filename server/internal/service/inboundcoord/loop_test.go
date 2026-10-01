package inboundcoord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	openai "github.com/openai/openai-go/v3"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// The speculative conversation render runs beside the routing request, so this
// fixture is now reached from two goroutines and has to serialize its ledger.
type scriptedCompleter struct {
	mu                 sync.Mutex
	rounds             []openai.ChatCompletion
	calls              int
	params             []openai.ChatCompletionNewParams
	checkRounds        []openai.ChatCompletion
	checkCalls         int
	checkParams        []openai.ChatCompletionNewParams
	checkError         error
	conversationRounds []openai.ChatCompletion
	conversationCalls  int
	conversationParams []openai.ChatCompletionNewParams
}

func TestNewIssueRequiresCurrentSceneRecall(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, SceneID: testSceneID("cid-current"), ConversationID: "cid-current", Message: "帮我沉淀这条决策", SenderName: "冬翔"}
	raw := `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我来整理这条决策","purpose":"整理当前决策及判断依据","intent":"other"}]}`
	for _, recalls := range [][]recallCall{nil, {{ConversationID: "cid-other"}}} {
		if _, err := parseValidatedWindowPlan(raw, turn, recalls, nil); err == nil {
			t.Fatal("new work must check the current scene, not another scene")
		}
	}
	got, err := parseValidatedWindowPlan(raw, turn, []recallCall{{ConversationID: "cid-current"}}, nil)
	if err != nil || len(got.Items) != 1 || got.Items[0].IssueID != "" {
		t.Fatalf("plan=%#v err=%v", got, err)
	}
	if _, err := parseValidatedWindowPlan(`{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"在，你说。"}]}`, Turn{SceneID: testSceneID("cid-current"), ConversationID: "cid-current", Message: "你说话"}, nil, nil); err != nil {
		t.Fatalf("presence needs no work lookup: %v", err)
	}
}

func (s *scriptedCompleter) conversationCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conversationCalls
}

func (s *scriptedCompleter) Chat(_ context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if names := toolParamNames(params.Tools); len(names) == 1 && names[0] == toolConversationReplies {
		s.conversationParams = append(s.conversationParams, params)
		if s.conversationCalls >= len(s.conversationRounds) {
			return nil, fmt.Errorf("unexpected conversation rendering call %d", s.conversationCalls)
		}
		out := s.conversationRounds[s.conversationCalls]
		s.conversationCalls++
		return &out, nil
	}

	if names := toolParamNames(params.Tools); len(names) == 1 && names[0] == "finish_check" {
		s.checkParams = append(s.checkParams, params)
		s.checkCalls++
		if s.checkError != nil {
			return nil, s.checkError
		}
		if s.checkRounds != nil {
			if s.checkCalls > len(s.checkRounds) {
				return nil, fmt.Errorf("unexpected extra finish check %d", s.checkCalls)
			}
			out := withScriptedFinishRequest(s.checkRounds[s.checkCalls-1], params)
			return &out, nil
		}
		// Existing scripted tests exercise the Host plan protocol, not model
		// judgment. Dedicated finish-check tests supply explicit verdicts.
		out := withScriptedFinishRequest(scriptedFinishVerdict("allow", "Scripted Host-protocol fixture accepts this candidate."), params)
		return &out, nil
	}
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

func TestLoopPlansBusyContinuationWithoutWriting(t *testing.T) {
	t.Parallel()
	for _, busy := range []bool{false, true} {
		chat := &scriptedCompleter{rounds: []openai.ChatCompletion{

			assistantTool("obsolete-write", toolIssueCommentAdd, `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","content":"伪造的正文","reply_text":"已送达"}`),
			assistantTool("plan", toolFinish, `{"actions":[{"kind":"continue_work","source_refs":["u1"],"reply":"我把番茄这个答复带过去","purpose":"向须莫确认番茄还是菠萝的偏好","intent":"confirm","issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","basis":"answer"}]}`),
		}}
		tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"向须莫确认番茄还是菠萝","on_this_scene":true}]}`, errors: map[string]error{toolIssueCommentAdd: ErrIssueBusy}}
		got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "p2p", Message: "番茄", SenderName: "须莫", SceneID: testSceneID("cid-v6"), ConversationID: "cid-v6", Busy: busy, HistoryStatus: "loaded", DingTalkHistory: []HistoryLine{{Role: "员工", Content: "你更喜欢番茄还是菠萝？"}}})
		if err != nil {
			t.Fatal(err)
		}
		if got.Action != ActionIssue || len(got.Items) != 1 || got.Items[0].IssueID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" || got.IssueComment != nil {
			t.Fatalf("busy=%v plan=%#v", busy, got)
		}
		if got.Items[0].Delegator != "须莫" || !strings.Contains(got.Items[0].Content, "须莫 在钉钉会话中的消息：\n\n番茄") || strings.Contains(got.Items[0].Content, "伪造") {
			t.Fatalf("trusted source lost: %#v", got.Items[0])
		}
		if len(tools.calls) != 1 || !strings.HasPrefix(tools.calls[0], toolAssocRecall) {
			t.Fatalf("the loop must not start a busy comment task: %v", tools.calls)
		}
		rejected := false
		for _, step := range got.Steps {
			if step.Tool == toolIssueCommentAdd && step.Error {
				rejected = true
			}
		}
		if !rejected {
			t.Fatal("the unadvertised write must be visibly rejected")
		}
	}
}

func TestLoopBusyFloodRemainsConversationWithoutWriting(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("reply", toolFinish, `{"actions":[{"kind":"ignore","source_refs":["u1"],"reason":"没有请求"}]}`)}}
	tools := &stubTools{errors: map[string]error{toolIssueCommentAdd: ErrIssueBusy}}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "group", Message: "灌水11：食堂窗口11今天供应番茄炒蛋，与工作无关。", SceneID: testSceneID("cid-v6"), ConversationID: "cid-v6", Busy: true})
	if err != nil || got.Action != ActionSilence || len(got.Items) != 0 || len(tools.calls) != 1 || !strings.HasPrefix(tools.calls[0], toolAssocRecall) || chat.calls != 1 {
		t.Fatalf("busy chatter cannot write or enter a retry storm: %#v calls=%v err=%v", got, tools.calls, err)
	}
}

func TestLoopBusyProgressDoesNotRequestAnotherTask(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{

		assistantTool("reply", toolFinish, `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r1"],"reply":"这件事还在排队，完成后我告诉你。"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"向须莫确认口味","status":"queued","on_this_scene":true}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, Addressed: true, Message: "怎么还没好", SceneID: testSceneID("cid-v6"), ConversationID: "cid-v6", Busy: true})
	if err != nil || got.Action != ActionReply || len(got.Items) != 0 || len(tools.calls) != 1 {
		t.Fatalf("progress must remain read-only at capacity: %#v calls=%v err=%v", got, tools.calls, err)
	}
}

func TestLoopBusyAnswersAndNoiseHaveDistinctPlans(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		message    string
		busy, work bool
	}{
		{"番茄", false, true}, {"可以，三点没问题", false, true}, {"番茄", true, true},
		{"灌水：食堂今天供应番茄炒蛋", false, false}, {"灌水：食堂今天供应番茄炒蛋", true, false},
	} {
		rounds := []openai.ChatCompletion{assistantTool("quiet", toolFinish, `{"actions":[{"kind":"ignore","source_refs":["u1"],"reason":"当前消息无需回应且没有工作请求。"}]}`)}
		if tc.work {
			rounds = []openai.ChatCompletion{

				assistantTool("plan", toolFinish, `{"actions":[{"kind":"continue_work","source_refs":["u1"],"reply":"我把这个答复带过去","purpose":"将同事对当前安排的答复转告委托人","intent":"confirm","issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","basis":"answer"}]}`),
			}
		}
		tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"确认同事对当前安排的选择","on_this_scene":true}]}`}
		got, err := (&Coordinator{Chat: &scriptedCompleter{rounds: rounds}, Tools: tools}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "group", Message: tc.message, SenderName: "须莫", SceneID: testSceneID("cid-current"), ConversationID: "cid-current", Busy: tc.busy, HistoryStatus: "loaded", DingTalkHistory: []HistoryLine{{Role: "员工", Content: "这个安排可以吗？"}}})
		if err != nil {
			t.Fatal(err)
		}
		if tc.work && (got.Action != ActionIssue || len(got.Items) != 1 || got.Items[0].IssueID == "") {
			t.Fatalf("busy=%v answer=%q lost its continuation: %#v", tc.busy, tc.message, got)
		}
		if !tc.work && (got.Action != ActionSilence || len(got.Items) != 0) {
			t.Fatalf("noise became retry work: %#v", got)
		}
		for _, call := range tools.calls {
			if !strings.HasPrefix(call, toolAssocRecall) {
				t.Fatalf("loop executed work before Host admission: %v", tools.calls)
			}
		}
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

		assistantTool("call-2", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我先去问冬翔今天想吃什么","purpose":"向冬翔确认今天吃什么","intent":"ask","context":"向冬翔确认今天吃什么"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"issue-meeting","purpose":"向冬翔确认明天开会时间","status":"waiting","on_this_scene":true,"why":"本会话事项"}]}`}
	c := &Coordinator{Chat: chat, Tools: tools}
	if _, err := c.runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Message:        "问一下冬翔，今天想吃什么",
		SceneID:        testSceneID("cid-dongxiang"),
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
		`"event":"inbound_coordinator_scene_prefetch"`,
		`"origin":"host_prefetch"`,
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

		assistantTool("call-2", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我先去问冬翔今天想吃什么","purpose":"向冬翔确认今天吃什么","intent":"ask","context":"向冬翔确认今天吃什么"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"issue-meeting","purpose":"向冬翔确认明天开会时间","status":"waiting","on_this_scene":true,"why":"本会话事项"}]}`}
	c := &Coordinator{Chat: chat, Tools: tools}
	got, err := c.runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Message:        "问一下冬翔，今天想吃什么",
		SceneID:        testSceneID("cid-dongxiang"),
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
	if !strings.Contains(got.LookInto, "向冬翔确认今天吃什么") || !strings.Contains(got.LookInto, "scene_cid=cid-dongxiang") {
		t.Fatalf("look_into=%q", got.LookInto)
	}
	if got.Purpose != "冬翔委托：向冬翔确认今天吃什么" || got.Intent != "ask" {
		t.Fatalf("new issue purpose=%q intent=%q", got.Purpose, got.Intent)
	}
	if got.ToolRounds != 1 {
		t.Fatalf("tool_rounds=%d", got.ToolRounds)
	}
	if strings.Join(got.ToolsUsed, ",") != "assoc_recall,finish" {
		t.Fatalf("tools=%v", got.ToolsUsed)
	}
	if len(tools.calls) != 1 {
		t.Fatalf("tool calls=%v", tools.calls)
	}
	if len(chat.params) != 1 {
		t.Fatalf("rounds=%d", len(chat.params))
	}
	wantStepTypes := []string{"tool_use", "tool_result", "tool_use"}
	if len(got.Steps) != len(wantStepTypes) {
		t.Fatalf("steps=%#v", got.Steps)
	}
	for i, wantType := range wantStepTypes {
		if got.Steps[i].Seq != i+1 || got.Steps[i].Type != wantType {
			t.Fatalf("step[%d]=%#v, want seq=%d type=%s", i, got.Steps[i], i+1, wantType)
		}
	}
	if !strings.Contains(got.Steps[0].Content, "Host prefetch") || got.Steps[0].Tool != toolAssocRecall || got.Steps[1].Tool != toolAssocRecall ||
		got.Steps[2].Tool != toolFinish {
		t.Fatalf("tool timeline=%#v", got.Steps)
	}
	if len(got.CoordinationActions) != 1 || got.CoordinationActions[0].Kind != "start_work" || got.UserText == "" || got.PlanVersion == "" {
		t.Fatalf("decision timeline=%#v", got.Steps)
	}
	if names := toolDefNames(chat.params[0]); strings.Join(names, ",") != "assoc_recall,work_state,context_read,finish" {
		t.Fatalf("round0 tools=%v", names)
	}
}

func TestLoopRecallFillsInboundConversationID(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"q":"辰驷"}`),
		assistantTool("finish", toolFinish, `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r2"],"reply":"当前会话没有打球这件事。"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[]}`}
	inbound := "cid+bEFv7ngm9n79Q1vL9HYJw=="
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "和辰驷确认一下，明天几点有空去打球。",
		SceneID:        testSceneID(inbound),
		ConversationID: inbound,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionReply {
		t.Fatalf("action=%s", got.Action)
	}
	if len(tools.calls) != 2 || !strings.Contains(tools.calls[0], inbound) || !strings.Contains(tools.calls[1], inbound) || !strings.Contains(tools.calls[1], "辰驷") {
		t.Fatalf("recall must keep inbound cid with q, calls=%v", tools.calls)
	}
	if len(got.Steps) == 0 || !strings.Contains(got.Steps[0].Input, inbound) {
		t.Fatalf("timeline must show inbound cid, steps=%#v", got.Steps)
	}
}

func TestLoopNewDeliverableFinishSkipsForcedComment(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{

		assistantTool("finish", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我去问须莫v6今天晚饭想吃什么","purpose":"向须莫v6确认今天晚饭吃什么","intent":"ask","context":"向须莫v6确认今天晚饭吃什么"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"须莫🥥委托：看看联系人里有没有须莫v6","status":"waiting","on_this_scene":true,"why":"本会话事项"}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "你去问下须莫v6，今天晚饭想吃什么",
		SceneID:        testSceneID("cid-v6"),
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

		assistantTool("finish", toolFinish, `{"actions":[{"kind":"continue_work","source_refs":["u1"],"reply":"我把七点这个答复带过去","purpose":"向须莫确认今晚几点可以打球","intent":"confirm","issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","basis":"answer"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"向须莫确认今晚几点打球","on_this_scene":true}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, Message: "7点", SenderName: "须莫", SceneID: testSceneID("cid-v6"), ConversationID: "cid-v6", HistoryStatus: "loaded", DingTalkHistory: []HistoryLine{{Role: "员工", Content: "今晚几点方便打球？"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue || len(got.Items) != 1 || got.Items[0].IssueID != issueID || got.IssueComment != nil || len(tools.calls) != 1 {
		t.Fatalf("continuation must be an uncommitted target-preserving plan: %#v calls=%v", got, tools.calls)
	}
	if got.Items[0].Delegator != "须莫" || !strings.Contains(got.Items[0].Content, "7点") {
		t.Fatalf("source attribution lost: %#v", got.Items[0])
	}
}

func TestLoopOneSceneCardAllowsNewIssueWhenPurposeDiffers(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{

		assistantTool("finish", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我去问辰驷明天几点有空打球。","purpose":"向辰驷确认明天几点有空去打球","intent":"ask","context":"冬翔委托：向辰驷确认明天几点有空去打球"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"冬翔委托：向辰驷确认明天洗脚时间","status":"waiting","on_this_scene":true,"why":"本会话事项","waiting_on":"cid-inbound"}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "和辰驷确认一下，明天几点有空去打球。",
		SceneID:        testSceneID("cid-inbound"),
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
		assistantTool("finish", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我去问辰驷明天几点有空打球。","purpose":"向辰驷确认明天几点有空去打球","intent":"ask","context":"向辰驷确认明天几点有空去打球"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"向辰驷确认明天什么时候去洗脚","status":"waiting","on_this_scene":false,"why":"关键词命中，不是本会话"}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "和辰驷确认一下，明天几点有空去打球。",
		SceneID:        testSceneID("cid-inbound"),
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

		assistantTool("bind", toolAssocBind, `{"conversation_id":"cid-v6","delegator":"须莫🥥","intent":"ask","purpose":"向须莫v6询问明早有没有会议"}`),
		assistantTool("finish", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我去问须莫v6明早有没有会议","purpose":"向须莫v6询问明早有没有会议","intent":"ask"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"须莫🥥委托：向须莫v6询问晚上有没有会议","status":"waiting","on_this_scene":true,"why":"本会话事项"}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "你再问一下须莫v6明早有没有会议",
		SceneID:        testSceneID("cid-v6"),
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
			if !strings.Contains(step.Output, `"hint"`) || !strings.Contains(step.Output, "finish.actions") {
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

		assistantTool("finish-bad", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我去问","purpose":"","intent":"ask"}]}`),
		assistantTool("finish-ok", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我去问须莫v6明早有没有会议","purpose":"向须莫v6询问明早有没有会议","intent":"ask"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		Message:        "问一下须莫v6明早有没有会议",
		SceneID:        testSceneID("cid-v6"),
		ConversationID: "cid-v6",
		SenderName:     "须莫🥥",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue || got.Purpose != "须莫🥥委托：向须莫v6询问明早有没有会议" {
		t.Fatalf("decision=%#v", got)
	}
	sawRejection := false
	for _, step := range got.Steps {
		if step.Tool == toolFinish && step.Error && strings.Contains(step.Output, `"error"`) {
			sawRejection = true
		}
	}
	if !sawRejection || len(tools.calls) != 1 || got.IssueComment != nil {
		t.Fatalf("empty purpose must be rejected before any write, steps=%#v calls=%v", got.Steps, tools.calls)
	}
}

func TestLoopContentJSONWithoutToolCallsNudgeThenFinish(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantJSON(`{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r1"],"reply":"这个单聊在跟高铁还是开车"}]}`),
		assistantTool("f1", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"在，有事直接说。"}]}`),
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
	turn := Turn{Source: SourceDigitalEmployee, SceneID: testSceneID("cid-current"), ConversationID: "cid-current", HistoryStatus: "not_loaded"}
	for _, round := range []int{maxLoopRounds - 2, maxLoopRounds - 1} {
		names := toolDefNamesFromDefs(toolsForDisclosure(turn, round, true))
		if strings.Join(names, ",") != toolFinish {
			t.Fatalf("repair/finish budget must not start another read: %v", names)
		}
	}
	first := toolDefNamesFromDefs(toolsForDisclosure(turn, 0, false))
	if strings.Join(first, ",") != "assoc_recall,context_read,finish" {
		t.Fatalf("initial tools=%v", first)
	}
	turn.recalledIssueIDs = []string{"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}
	later := toolDefNamesFromDefs(toolsForDisclosure(turn, 1, true))
	if strings.Join(later, ",") != "assoc_recall,work_state,context_read,finish" {
		t.Fatalf("recalled tools=%v", later)
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
	if err == nil || decision.Action != ActionDeferred || !strings.Contains(err.Error(), "8 rounds") {
		t.Fatalf("err=%v", err)
	}
	if len(decision.Steps) == 0 || decision.Steps[len(decision.Steps)-1].Type != "error" {
		t.Fatalf("failed loop lost timeline: %#v", decision.Steps)
	}
}

func TestDecideRunsToolLoop(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("f1", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"在，有事直接说。"}]}`),
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
		assistantTool("f0", toolFinish, `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r1"],"reply":"这个单聊在跟高铁还是开车"}]}`),
		assistantTool("r1", toolAssocRecall, `{"conversation_id":"cid+bEFv7ngm9n79Q1vL9HYJ1w==","since":"48h"}`),
		assistantTool("f1", toolFinish, `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r2"],"reply":"这个会话在图上没有记录。"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[]}`}
	c := &Coordinator{Chat: chat, Tools: tools}
	got, err := c.runLoop(context.Background(), Turn{
		Source:         SourceRobot,
		Addressed:      true,
		Message:        fake + " 里面聊了什么",
		SceneID:        testSceneID("cid-robot"),
		ConversationID: "cid-robot",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionReply || !strings.Contains(got.UserText, "没有记录") {
		t.Fatalf("got %#v", got)
	}
	if got.ToolRounds != 3 || len(got.Steps) < 4 || got.Steps[3].Tool != toolFinish || !got.Steps[3].Error {
		t.Fatalf("unsupported answer must be rejected before the named scene is read: %#v", got)
	}
	if len(tools.calls) != 2 || !strings.Contains(tools.calls[0], "cid-robot") || !strings.Contains(tools.calls[1], fake) {
		t.Fatalf("must recall the fake cid, calls=%v", tools.calls)
	}
}

func TestExtractConversationIDs(t *testing.T) {
	t.Parallel()
	short := "cid+bEFv7ngm9n79Q1vL9HYJ1w=="
	long := "cidSYyaWxOBeot0PXEyA6BNd1/SxKmRz0LQxkl8L2MbF4U="
	for _, tt := range []struct {
		name    string
		message string
		want    []string
	}{
		{"short", short + " 里面聊了什么", []string{short}},
		{"long and punctuation", "看看（" + long + "）和 `" + short + "`", []string{long, short}},
		{"field and duplicate", "conversation_id=" + long + "，再查" + long, []string{long}},
		{"url-safe opaque id", "cidAbCdEfgh_123-xyz 在跟什么", []string{"cidAbCdEfgh_123-xyz"}},
		{"report query", "https://landray.dingtalkapps.com/alid/app/report/viewReport_new.html?id=report&cid=75953554200&cname=team", nil},
		{"encoded report link", "dingtalk://dingtalkclient/action/openapp?redirect_url=https%3A%2F%2Fexample.com%2Freport%3Fcid%3D75953554200", nil},
		{"opaque id inside URL", "https://example.com/report?openConversationId=" + long, []string{long}},
		{"opaque id inside DingTalk link", "dingtalk://dingtalkclient/action/openapp?openConversationId=" + short, []string{short}},
		{"link and explicit scene", "[报告](https://example.com/report?cid=75953554200)\n" + short + " 里面聊了什么", []string{short}},
		{"link and adjacent explicit scene", "https://example.com/report?cid=75953554200，另查" + long, []string{long}},
		{"ordinary text", "cid=75953554200 acidabcdefghij lucidabcdefghij cid-short", nil},
		{"malformed padding", short + "= " + short + "extra", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := extractConversationIDs(tt.message)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestLoopReportLinkDoesNotRequireNamedConversationRecall(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("f1", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"receipt","reply":"PC 官网迭代这一段似乎没有写完。"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[]}`}
	c := &Coordinator{Chat: chat, Tools: tools}
	got, err := c.runLoop(context.Background(), Turn{
		Source:         SourceDigitalEmployee,
		SceneID:        testSceneID("cid58bvFJhm51zIMVj1WGa/vMyRqIqidBhxeJFn667zqMY="),
		ConversationID: "cid58bvFJhm51zIMVj1WGa/vMyRqIqidBhxeJFn667zqMY=",
		Message: "今日最重要进展：PC 官网迭代。围绕\n" +
			"[日志](https://landray.dingtalkapps.com/alid/app/report/viewReport_new.html?id=report&cid=75953554200&cname=team)\n" +
			"dingtalk://dingtalkclient/action/openapp?redirect_url=https%3A%2F%2Fexample.com%2Freport%3Fcid%3D75953554200",
	})
	if err != nil || got.Action != ActionReply || got.ToolRounds != 1 || len(tools.calls) != 1 || !strings.Contains(tools.calls[0], "cid58bvFJhm51zIMVj1WGa/vMyRqIqidBhxeJFn667zqMY=") || strings.Contains(tools.calls[0], "75953554200") {
		t.Fatalf("report metadata must not create another scene lookup: decision=%#v err=%v calls=%v", got, err, tools.calls)
	}
}

func TestNamedCurrentConversationStillRequiresRecall(t *testing.T) {
	t.Parallel()
	cid := "cid+bEFv7ngm9n79Q1vL9HYJ1w=="
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: cid, Message: cid + " 里面聊了什么", CoordinationReads: []CoordinationRead{{ReadRef: "r1", Tool: toolAssocRecall}}}
	raw := `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r1"],"reply":"本次范围内未找到事项记录。"}]}`
	_, err := parseValidatedWindowPlan(raw, turn, nil, nil)
	if err == nil || !strings.Contains(marshalToolFailure(err), cid) {
		t.Fatalf("missing recall must identify the exact named scene: %v", err)
	}
	got, err := parseValidatedWindowPlan(raw, turn, []recallCall{{ConversationID: cid}}, nil)
	if err != nil || got.Action != ActionReply {
		t.Fatalf("current named scene is answerable after recall: %#v err=%v", got, err)
	}
}

func TestLinkedConversationStillRequiresRecall(t *testing.T) {
	t.Parallel()
	cid := "cid+bEFv7ngm9n79Q1vL9HYJ1w=="
	turn := Turn{
		Source:            SourceDigitalEmployee,
		SceneID:           testSceneID("cid-current"),
		ConversationID:    "cid-current",
		Message:           "这个会话在跟什么事 dingtalk://dingtalkclient/action/openapp?openConversationId=" + cid,
		CoordinationReads: []CoordinationRead{{ReadRef: "r1", Tool: toolAssocRecall}},
	}
	raw := `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r1"],"reply":"本次范围内未找到事项记录。"}]}`
	if _, err := parseValidatedWindowPlan(raw, turn, []recallCall{{ConversationID: turn.ConversationID}}, nil); err == nil {
		t.Fatal("reading the current scene does not cover the genuine linked scene")
	}
	got, err := parseValidatedWindowPlan(raw, turn, []recallCall{{ConversationID: cid}}, nil)
	if err != nil || got.Action != ActionReply {
		t.Fatalf("linked named scene is answerable after recall: %#v err=%v", got, err)
	}
}

func TestDecideNamedConversationRecallThenReply(t *testing.T) {
	t.Parallel()
	named := "cid+bEFv7ngm9n79Q1vL9HYJw=="
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("call-1", toolAssocRecall, `{"conversation_id":"cid+bEFv7ngm9n79Q1vL9HYJw==","since":"48h"}`),
		assistantTool("call-2", toolFinish, `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r2"],"reply":"这个单聊最近在跟「向冬翔确认明天去上海是坐高铁还是开车」。"}]}`),
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
		SceneID:        testSceneID("cid-robot"),
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
	if strings.Join(got.ToolsUsed, ",") != "assoc_recall,assoc_recall,finish" {
		t.Fatalf("tools=%v", got.ToolsUsed)
	}
	if len(tools.calls) != 2 || !strings.Contains(tools.calls[0], "cid-robot") || !strings.Contains(tools.calls[1], named) {
		t.Fatalf("recall must use the named cid, calls=%v", tools.calls)
	}
}

func TestCoordinatorToolDefsIncludeAssocAndFinish(t *testing.T) {
	t.Parallel()
	for _, recalled := range []bool{false, true} {
		turn := Turn{Source: SourceDigitalEmployee, HistoryStatus: "not_loaded"}
		if recalled {
			turn.recalledIssueIDs = []string{"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}
		}
		names := toolDefNamesFromDefs(toolsForDisclosure(turn, 0, recalled))
		if !containsString(names, toolAssocRecall) || !containsString(names, toolFinish) || !containsString(names, toolContextRead) {
			t.Fatalf("missing decision/read path: %v", names)
		}
		if containsString(names, toolAssocBind) || containsString(names, toolIssueCommentAdd) {
			t.Fatalf("reasoning phase advertised a write: %v", names)
		}
		if containsString(names, toolWorkState) != recalled || containsString(names, toolIssueGet) || containsString(names, toolIssueCommentList) {
			t.Fatalf("issue target not disclosed yet: %v", names)
		}
	}
}

func TestWindowPlanPreservesActualDingTalkSpeaker(t *testing.T) {
	t.Parallel()
	const issue = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	for _, source := range []Source{SourceDigitalEmployee, SourceRobot} {
		turn := Turn{Source: source, SenderName: "被联系的同事", SceneID: testSceneID("cid-current"), ConversationID: "cid-current", Message: "周五三点可以", HistoryStatus: "loaded"}
		got, err := parseValidatedWindowPlan(`{"actions":[{"kind":"continue_work","source_refs":["u1"],"reply":"我把这个答复带过去","issue_id":"`+issue+`","purpose":"确认同事周五三点是否可以开会","intent":"confirm","basis":"answer"}]}`, turn, []recallCall{{ConversationID: turn.ConversationID}}, map[string]struct{}{issue: {}})
		if err != nil || len(got.Items) != 1 {
			t.Fatalf("source=%s plan=%#v err=%v", source, got, err)
		}
		if got.Items[0].Delegator != turn.SenderName || got.Items[0].Content != "被联系的同事 在钉钉会话中的消息：\n\n周五三点可以" {
			t.Fatalf("comment author must not replace actual speaker: %#v", got.Items[0])
		}
	}
}

func TestWindowPlanCannotIntroduceUnrecalledTarget(t *testing.T) {
	t.Parallel()
	turn := Turn{Source: SourceDigitalEmployee, SceneID: testSceneID("cid-current"), ConversationID: "cid-current", Message: "改成线上会议", SenderName: "冬翔"}
	raw := `{"actions":[{"kind":"continue_work","source_refs":["u1"],"reply":"我把会议方式改成线上","purpose":"将这次会议的地点改成线上会议","intent":"confirm","issue_id":"unrecalled-issue","basis":"change"}]}`
	if _, err := parseValidatedWindowPlan(raw, turn, []recallCall{{ConversationID: turn.ConversationID}}, nil); err == nil {
		t.Fatal("a continuation cannot introduce a target absent from recall")
	}
	fn := windowPlanTool(true).GetFunction()
	props := fn.Parameters["properties"].(map[string]any)
	if _, ok := props["issue_id"]; ok {
		t.Fatal("a top-level target can bypass per-item source attribution")
	}
	items := props["actions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	for _, required := range []string{"source_refs", "purpose", "intent", "basis"} {
		if _, ok := items[required]; !ok {
			t.Fatalf("missing per-item obligation %s", required)
		}
	}
}

func TestWindowPlanWorkReceiptDoesNotRequireModelText(t *testing.T) {
	t.Parallel()
	turn := Turn{Source: SourceDigitalEmployee, SceneID: testSceneID("cid-env"), ConversationID: "cid-env", SenderName: "106201", Message: "看下你的环境变量和dws身份 mcp和skills有什么"}
	recalls := []recallCall{{ConversationID: turn.ConversationID}}
	empty := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"向本群汇报当前运行环境的关键配置（环境变量/DWS身份/MCP/Skills）简略状态","intent":"lookup"}]}`
	d, err := parseValidatedWindowPlan(empty, turn, recalls, nil)
	if err != nil || d.Action != ActionIssue || len(d.Items) != 1 || d.UserText != hostWorkReceipt("start_work", "zh") {
		t.Fatalf("optional model reply blocked valid work: %+v err=%v", d, err)
	}
	complete := strings.Replace(empty, `"kind":"start_work"`, `"kind":"start_work","reply":"我去查配置与身份并汇报。"`, 1)
	supplied, err := parseValidatedWindowPlan(complete, turn, recalls, nil)
	if err != nil || supplied.UserText != d.UserText || supplied.Items[0].Purpose != d.Items[0].Purpose {
		t.Fatalf("legacy work reply changed execution or Host receipt: %+v err=%v", supplied, err)
	}
}

func TestWindowPlanAcceptsTechnicalSubject(t *testing.T) {
	t.Parallel()
	turn := Turn{Source: SourceDigitalEmployee, SceneID: testSceneID("cid-tech"), ConversationID: "cid-tech", SenderName: "乔宁", Message: "请排查 dws chat data-auth 授权失败"}
	d, err := parseValidatedWindowPlan(`{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我来排查授权失败的原因。","purpose":"排查 dws chat data-auth 授权失败的原因和修复方法","intent":"lookup"}]}`, turn, []recallCall{{ConversationID: turn.ConversationID}}, nil)
	if err != nil || d.Action != ActionIssue || len(d.Items) != 1 {
		t.Fatalf("technical subject must reach semantic review: d=%+v err=%v", d, err)
	}
}

func TestFinishMissingWorkReplyDoesNotSpendRepairRound(t *testing.T) {
	t.Parallel()
	empty := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"向本群汇报当前运行环境的关键配置（环境变量/DWS身份/MCP/Skills）简略状态","intent":"lookup"}]}`
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("r0", toolAssocRecall, `{"conversation_id":"cid-env"}`), assistantTool("f0", toolFinish, empty)}}
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, SceneID: testSceneID("cid-env"), ConversationID: "cid-env", SenderName: "106201", Message: "看下你的环境变量和dws身份 mcp和skills有什么"})
	if err != nil || d.Action != ActionIssue || chat.calls != 2 || chat.checkCalls != 1 || d.UserText != hostWorkReceipt("start_work", "zh") {
		t.Fatalf("unnecessary reply repair: %+v calls=%d err=%v", d, chat.calls, err)
	}
	for _, step := range d.Steps {
		if step.Error {
			t.Fatalf("optional work reply created a repair error: %+v", step)
		}
	}
}

func TestMarshalToolFailureIncludesHint(t *testing.T) {
	t.Parallel()
	_, err := parseValidatedWindowPlan(`{"actions":[{"kind":"continue_work","source_refs":["u1"],"reply":"我把这个答复带过去","purpose":"确认须莫周五三点是否方便开会","intent":"confirm","issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","basis":"answer"}]}`, Turn{Source: SourceDigitalEmployee, SenderName: "须莫", Message: "可以", HistoryStatus: "not_loaded"}, nil, map[string]struct{}{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa": {}})
	if err == nil {
		t.Fatal("missing original question must reject an answer plan")
	}
	raw := marshalToolFailure(err)
	if !strings.Contains(raw, `"error"`) || !strings.Contains(raw, `"hint"`) || !strings.Contains(raw, "context_read") {
		t.Fatalf("failure=%s", raw)
	}
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
	for _, ready := range []bool{false, true} {
		fn := windowPlanTool(ready).GetFunction()
		if fn == nil || !fn.Description.Valid() {
			t.Fatal("finish description missing")
		}
		props := fn.Parameters["properties"].(map[string]any)
		operationProps := props["actions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		actions := stringSlice(operationProps["kind"].(map[string]any)["enum"])
		if containsString(actions, "reply") || !containsString(actions, "acknowledge") || containsString(actions, "start_work") != ready || containsString(actions, "continue_work") != ready {
			t.Fatalf("work must wait for evidence: %v", actions)
		}
		if _, ok := operationProps["purpose"]; ok != ready {
			t.Fatalf("work schema must appear only after evidence unlocks submission: ready=%v items=%v", ready, ok)
		}
	}
	manifest := policyManifest(Turn{Source: SourceDigitalEmployee})
	if !containsString(manifest.ActiveRuleIDs, "COORD.F04") {
		t.Fatal("capability boundary must be visible before direct answer versus work decision")
	}
}

func TestLoopFinishTopLevelIssueIDIsRejectedThenScopedPlan(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{

		assistantTool("bad", toolFinish, `{"action":"issue","issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`),
		assistantTool("fixed", toolFinish, `{"actions":[{"kind":"continue_work","source_refs":["u1"],"reply":"我把七点这个答复带过去","purpose":"向须莫确认今晚几点可以打球","intent":"confirm","issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","basis":"answer"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"向须莫确认今晚几点打球","on_this_scene":true}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, Message: "7点", SenderName: "须莫", SceneID: testSceneID("cid-v6"), ConversationID: "cid-v6", HistoryStatus: "loaded", DingTalkHistory: []HistoryLine{{Role: "员工", Content: "今晚几点打球？"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue || len(got.Items) != 1 || got.Items[0].IssueID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" || got.IssueComment != nil || len(tools.calls) != 1 {
		t.Fatalf("repair must not execute an extra comment: %#v calls=%v", got, tools.calls)
	}
	sawHint := false
	for _, step := range got.Steps {
		if step.Tool == toolFinish && step.Error && strings.Contains(step.Output, "actions") {
			sawHint = true
		}
	}
	if !sawHint {
		t.Fatal("invalid top-level target needs a repair hint pointing at a sourced work item")
	}
}

func TestLoopFinishIssueMissingModelReplyGetsHostReceipt(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("finish", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"向冬翔确认今天吃什么","intent":"ask"}]}`)}}
	got, err := (&Coordinator{Chat: chat, Tools: &stubTools{recall: `{"items":[]}`}}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, Message: "问一下冬翔，今天想吃什么", SceneID: testSceneID("cid-dongxiang"), ConversationID: "cid-dongxiang", SenderName: "冬翔"})
	if err != nil || got.Action != ActionIssue || got.UserText != hostWorkReceipt("start_work", "zh") || got.IssueID != "" || chat.calls != 1 {
		t.Fatalf("missing work text caused a repair: %+v err=%v", got, err)
	}
	for _, step := range got.Steps {
		if step.Error {
			t.Fatalf("missing model ACK must not fail: %+v", step)
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
		"job_policy_status: host_held",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Always search the web first.") {
		t.Fatal("routing prompt must not eagerly include full execution instructions")
	}
	if strings.Contains(prompt, "agent_skills:") {
		t.Fatalf("empty skills must be omitted:\n%s", prompt)
	}
}

func TestBuildUserPromptIncludesSkillSnapshots(t *testing.T) {
	t.Parallel()
	prompt := buildUserPrompt(Turn{
		Source:    SourceDigitalEmployee,
		Addressed: true,
		Skills: []SkillSnapshot{
			{Name: "dingtalk-minutes", Description: "查询听记并整理行动项"},
			{Name: "  ", Description: "skip empty name"},
			{Name: "dingtalk-calendar", Description: "约会议、查日程"},
		},
		Message: "把今天下午那场会的听记整理成待办",
	})
	for _, want := range []string{
		"agent_skills:",
		"- dingtalk-minutes: 查询听记并整理行动项",
		"- dingtalk-calendar: 约会议、查日程",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "skip empty name") {
		t.Fatalf("empty skill names must be omitted:\n%s", prompt)
	}
}

func TestFormatSkillSnapshotsClipsAndCaps(t *testing.T) {
	t.Parallel()
	longDesc := strings.Repeat("听记", skillSnapshotDescBudget)
	got := formatSkillSnapshots([]SkillSnapshot{{Name: "minutes", Description: longDesc + "多余"}})
	wantDesc := clipRunes(longDesc, skillSnapshotDescBudget)
	if !strings.Contains(got, "- minutes: "+wantDesc) {
		t.Fatalf("clipped snapshot=%q", got)
	}
	if strings.Contains(got, "多余") {
		t.Fatalf("description must clip: %q", got)
	}

	many := make([]SkillSnapshot, skillSnapshotLimit+4)
	for i := range many {
		many[i] = SkillSnapshot{Name: fmt.Sprintf("s%02d", i), Description: "can"}
	}
	got = formatSkillSnapshots(many)
	if strings.Contains(got, fmt.Sprintf("s%02d", skillSnapshotLimit)) {
		t.Fatalf("must cap at %d skills:\n%s", skillSnapshotLimit, got)
	}
	if !strings.Contains(got, "s00: can") || !strings.Contains(got, fmt.Sprintf("s%02d: can", skillSnapshotLimit-1)) {
		t.Fatalf("expected first %d skills:\n%s", skillSnapshotLimit, got)
	}

	heavy := make([]SkillSnapshot, 20)
	desc := strings.Repeat("能", skillSnapshotDescBudget)
	for i := range heavy {
		heavy[i] = SkillSnapshot{Name: fmt.Sprintf("skill-%02d", i), Description: desc}
	}
	got = formatSkillSnapshots(heavy)
	if n := utf8.RuneCountInString(got); n > skillSnapshotsBudget {
		t.Fatalf("snapshot budget %d, got %d", skillSnapshotsBudget, n)
	}
	if !strings.Contains(got, "skill-00") || strings.Contains(got, "skill-19") {
		t.Fatalf("budget should keep early skills and drop later ones:\n%s", got)
	}
}

func TestBuildUserPromptIncludesConversationID(t *testing.T) {
	t.Parallel()
	prompt := buildUserPrompt(Turn{
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		SceneID:        testSceneID("cid-dongxiang"),
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

func TestLoopIssueBusyAckSilences(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("done", toolFinish, `{"actions":[{"kind":"ignore","source_refs":["u1"],"reason":"已结束且没有新请求"}]}`)}}
	tools := &stubTools{}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "group", Message: "谢谢", SceneID: testSceneID("cid-v6"), ConversationID: "cid-v6", Busy: true})
	if err != nil || got.Action != ActionSilence || len(got.Items) != 0 || len(tools.calls) != 1 || !strings.HasPrefix(tools.calls[0], toolAssocRecall) {
		t.Fatalf("closure ACK is no work even at capacity: %#v %v", got, err)
	}
}

func TestLoopTaskFinishedAllowsIssueGetWithoutRecall(t *testing.T) {
	t.Parallel()
	issueID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("get", toolIssueGet, `{"issue_id":"`+issueID+`"}`),
		assistantTool("finish", toolFinish, completionFinishJSON(Turn{IssueID: issueID, TaskResult: "须莫说周五三点可以开会。"}, "已经问过须莫，周五三点可以。")),
	}}
	tools := &stubTools{}
	decision, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{
		Loop:       LoopTaskFinished,
		Source:     SourceDigitalEmployee,
		Addressed:  true,
		ChatType:   "p2p",
		Message:    "任务已完成，请向委托人汇报。",
		IssueID:    issueID,
		TaskResult: "须莫说周五三点可以开会。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionReply {
		t.Fatalf("decision=%#v", decision)
	}
	if len(tools.calls) == 0 || !strings.HasPrefix(tools.calls[0], toolIssueGet) {
		t.Fatalf("expected issue_get, calls=%v", tools.calls)
	}
}

func TestLoopTaskFinishedSilencesRedundantWrapup(t *testing.T) {
	t.Parallel()
	delivery := TaskDeliveryContext{Status: "loaded", TaskID: "current-run", Scope: "current_task", Deliveries: []TaskDeliveryEvidence{{ConversationID: "cid-current", MessageID: "msg-result", SentText: "劳动合同法第三条的解释如下。", TextComplete: true}}}
	result := "劳动合同法第三条的解释如下。"
	chat := &scriptedCompleter{}
	got := (&Coordinator{Chat: chat}).Decide(context.Background(), Turn{Loop: LoopTaskFinished, Source: SourceDigitalEmployee, Addressed: true, ChatType: "group", Message: "任务已完成", SceneID: testSceneID("cid-current"), ConversationID: "cid-current", TaskResult: result, AlreadyToldScene: TaskFinishedResultAlreadyDelivered(result, "cid-current", delivery)})
	if got.Action != ActionSilence || got.UserText != "" || chat.calls != 0 {
		t.Fatalf("this exact result already reached this scene: %#v calls=%d", got, chat.calls)
	}
	if TaskFinishedResultAlreadyDelivered("新结论：这条不适用。", "cid-current", delivery) {
		t.Fatal("an old delivery cannot suppress a new result")
	}
}

func TestLoopTaskFinishedKeepsUntoldProgressForDelegator(t *testing.T) {
	t.Parallel()
	delivery := `{"status":"loaded","task_id":"current-run","scope":"current_task","complete":false,"deliveries":[{"conversation_id":"cid-dxxh","message_id":"msg-ask","sent_text":"明天几点开会？","text_complete":true}]}`
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("finish", toolFinish, completionFinishJSON(Turn{SceneID: testSceneID("cid-delegator"), ConversationID: "cid-delegator", TaskResult: "已向 dxxh 询问明天开会时间，还没有对方回答。", TaskDeliveryContext: delivery}, "已问 dxxh 明天开会时间，等他回。"))}}
	got, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), Turn{Loop: LoopTaskFinished, Source: SourceDigitalEmployee, Addressed: true, ChatType: "group", SceneID: testSceneID("cid-delegator"), ConversationID: "cid-delegator", Message: "任务已完成", TaskResult: "已向 dxxh 询问明天开会时间，还没有对方回答。", TaskDeliveryContext: delivery})
	if err != nil || got.Action != ActionReply || got.UserText == "" {
		t.Fatalf("a useful outcome not yet told to this scene must not be blacklisted: %#v err=%v", got, err)
	}
	input := fmt.Sprint(chat.params[0].Messages)
	raw, _ := json.Marshal(chat.params[0].Messages)
	if !strings.Contains(string(raw), "msg-ask") || strings.Contains(input, "AlreadyToldScene:true") {
		t.Fatalf("completion must receive actual recipient evidence: %s", raw)
	}
}

func TestToolsForTurnTaskFinishedOmitsAssocRecall(t *testing.T) {
	t.Parallel()
	defs := toolsForDisclosure(Turn{Loop: LoopTaskFinished}, 0, false)
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		fn := def.GetFunction()
		if fn == nil {
			t.Fatal("tool missing function")
		}
		names = append(names, fn.Name)
	}
	joined := strings.Join(names, ",")
	if strings.Contains(joined, toolAssocRecall) || strings.Contains(joined, toolIssueCommentAdd) {
		t.Fatalf("task-finished tools must not include scene-wide recall or comment add: %v", names)
	}
	if !strings.Contains(joined, toolIssueGet) || !strings.Contains(joined, toolFinish) {
		t.Fatalf("task-finished tools=%v", names)
	}
	defs = toolsForDisclosure(Turn{Loop: LoopTaskFinished}, 0, false)
	for _, def := range defs {
		fn := def.GetFunction()
		if fn == nil || fn.Name != toolFinish {
			continue
		}
		props := fn.Parameters["properties"].(map[string]any)
		operationProps := props["actions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		actions := stringSlice(operationProps["kind"].(map[string]any)["enum"])
		if strings.Join(actions, ",") != "report_result,ignore" {
			t.Fatalf("completion cannot create or continue work: %v", actions)
		}
	}
}

func TestBuildUserPromptIncludesTaskFinishedWindow(t *testing.T) {
	t.Parallel()
	prompt := buildUserPrompt(Turn{
		Loop:           LoopTaskFinished,
		Source:         SourceDigitalEmployee,
		Addressed:      true,
		SceneID:        testSceneID("cid-dongxiang"),
		ConversationID: "cid-dongxiang",
		IssueID:        "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		TaskResult:     "须莫说周五三点可以。",
		Message:        "任务已完成，请向委托人汇报。",
	})
	for _, want := range []string{
		"loop: task_finished",
		"issue_id: aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"须莫说周五三点可以。",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

var _ Completer = (*scriptedCompleter)(nil)
var _ Tools = (*stubTools)(nil)

func TestLoopShortAnswerRequiresOriginalQuestionBeforePlan(t *testing.T) {
	t.Parallel()
	const plan = `{"actions":[{"kind":"continue_work","source_refs":["u1"],"reply":"我把三点可以这个答复带过去","purpose":"确认须莫周五三点是否方便开会","intent":"confirm","issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","basis":"answer"}]}`
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{

		assistantTool("premature", toolFinish, plan),
		assistantTool("question", toolContextRead, `{"kind":"history"}`),
		assistantTool("complete", toolFinish, plan),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"确认须莫周五三点是否方便开会","on_this_scene":true}]}`}
	history := &dwsHistoryStub{history: []HistoryLine{{Role: "员工", Content: "周五三点方便开会吗？", EvidenceID: "question-before-answer"}}}
	got, err := (&Coordinator{Chat: chat, Tools: tools, DWSHistory: history}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, Addressed: true, Message: "三点可以", SenderName: "须莫", SceneID: testSceneID("cid-current"), ConversationID: "cid-current", HistoryStatus: "not_loaded"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionIssue || len(got.Items) != 1 || got.Items[0].Basis != "answer" || got.IssueComment != nil || history.calls != 1 || len(tools.calls) != 1 || chat.calls != 3 {
		t.Fatalf("answer must obtain evidence without writing during reasoning: %#v history=%d calls=%v err=%v", got, history.calls, tools.calls, err)
	}
	rejected := false
	for _, step := range got.Steps {
		if step.Tool == toolFinish && step.Error {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("loaded question evidence must be required before accepting an answer plan")
	}
}

func TestLoopFailedRecallCannotUnlockNewWork(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("failed-read", toolAssocRecall, `{}`),
		assistantTool("premature", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我来整理当前决策","purpose":"整理当前决策及判断依据","intent":"other"}]}`),
		assistantTool("clarify", toolFinish, `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r2"],"reply":"暂时没读到之前的处理记录，我还不能确认是否已接过这件事。"}]}`),
	}}
	tools := &stubTools{errors: map[string]error{toolAssocRecall: fmt.Errorf("temporary read failure")}}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, Addressed: true, SceneID: testSceneID("cid-current"), ConversationID: "cid-current", Message: "整理这次决策", SenderName: "冬翔"})
	if err != nil || got.Action != ActionReply || len(got.Items) != 0 || len(tools.calls) != 2 || chat.calls != 3 {
		t.Fatalf("failed read cannot authorize duplicate execution: %#v calls=%v err=%v", got, tools.calls, err)
	}
	if containsString(toolDefNames(chat.params[1]), toolWorkState) {
		t.Fatal("failed recall must not unlock issue inspection or work submission")
	}
}

func TestLoopRepeatedAcceptedRequestRepliesWithoutWork(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{

		assistantTool("reply", toolFinish, `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r1"],"reply":"这件事已经排上了，整理好我发你。"}]}`),
	}}
	tools := &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"整理今天会议的决策和行动项","status":"queued","on_this_scene":true}]}`}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{Source: SourceDigitalEmployee, Addressed: true, Message: "帮我整理今天会议的决策和行动项", SceneID: testSceneID("cid-current"), ConversationID: "cid-current", Busy: true})
	if err != nil || got.Action != ActionReply || len(got.Items) != 0 || got.IssueComment != nil || len(tools.calls) != 1 {
		t.Fatalf("repeating an accepted request is not a second execution: %#v calls=%v err=%v", got, tools.calls, err)
	}
}

func TestLoopRejectsUnknownOrTrailingFinishJSON(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"action":"reply","text":"不能忽略附带字段","look_into":"隐藏的另一条工作"}`,
		`{"action":"reply","text":"第一个对象"}{"action":"issue","text":"第二个对象不能被忽略"}`,
	} {
		chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
			assistantTool("invalid", toolFinish, raw),
			assistantTool("repaired", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"在，你说。"}]}`),
		}}
		tools := &stubTools{}
		got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), Turn{Source: SourceWeb, Addressed: true, Message: "你好"})
		if err != nil || got.Action != ActionReply || got.UserText != "在，你说。" || chat.calls != 2 || len(tools.calls) != 0 || len(got.Items) != 0 {
			t.Fatalf("invalid input must be repaired, never partially accepted: %#v err=%v", got, err)
		}
		if len(got.Steps) < 2 || !got.Steps[1].Error || got.Steps[1].Tool != toolFinish {
			t.Fatalf("invalid finish was not recorded as rejected: %#v", got.Steps)
		}
	}
}
