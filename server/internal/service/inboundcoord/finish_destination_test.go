package inboundcoord

import (
	"context"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func TestFinishCheckWorkWindowCannotSettleOnSpeakOnly(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-work", Message: "@VOC决策助理 帮我给岚调新建一个aone，内容是支持semantica的能力"}
	candidate := Decision{Action: ActionReply, UserText: "收到，我记一下这个能力。", CoordinationActions: []CoordinationAction{{Kind: "acknowledge", SourceRefs: []string{"u1"}, AckKind: "correction", Reply: "收到，我记一下这个能力。"}}}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Correction is acknowledged.")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil)
	if err != nil || result.Verdict != "revise" || !strings.Contains(result.Reason, "speaking kinds cannot settle a work request") {
		t.Fatalf("work window must not finish as acknowledge: verdict=%s reason=%s err=%v", result.Verdict, result.Reason, err)
	}
}

func TestFinishCheckGreetingMayStaySpeakOnly(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "你好"}
	candidate := Decision{Action: ActionReply, UserText: "你好。", CoordinationActions: []CoordinationAction{{Kind: "acknowledge", SourceRefs: []string{"u1"}, AckKind: "greeting", Reply: "你好。"}}}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Greeting is covered.")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil)
	if err != nil || result.Verdict != "allow" {
		t.Fatalf("greeting must remain speak-only: verdict=%s reason=%s err=%v", result.Verdict, result.Reason, err)
	}
}

func TestFinishCheckCapabilityAskMayDescribe(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "你能做什么？"}
	candidate := Decision{Action: ActionReply, UserText: "我可以介绍当前已展示的能力。", CoordinationActions: []CoordinationAction{{Kind: "describe_capabilities", SourceRefs: []string{"u1"}, Reply: "我可以介绍当前已展示的能力。"}}}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Bounded capability overview is accurate.")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil)
	if err != nil || result.Verdict != "allow" {
		t.Fatalf("capability ask may describe_capabilities: verdict=%s reason=%s err=%v", result.Verdict, result.Reason, err)
	}
}

func TestFinishCheckInventedAuthorizationClarifyIsSpeakFunnel(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "用我新给你加的 mcp 来帮我给岚调新建一个 aone"}
	candidate := Decision{Action: ActionReply, UserText: "我没有权限，你自己去建。", CoordinationActions: []CoordinationAction{{Kind: "clarify", SourceRefs: []string{"u1"}, MissingFields: []string{"authorization"}, Reply: "我没有权限，你自己去建。"}}}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Need the user to authorize the tool.")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil)
	if err != nil || result.Verdict != "revise" || !strings.Contains(result.Reason, "start_work") {
		t.Fatalf("authorization clarify on a work window must return to work: verdict=%s reason=%s err=%v", result.Verdict, result.Reason, err)
	}
}

func TestPromptProjectsThreeEvidenceCards(t *testing.T) {
	prompt := buildUserPrompt(Turn{
		Source: SourceDigitalEmployee, Message: "帮我查一下进度",
		SceneMemory: "偏好简洁", SceneMemoryRevision: 3, SceneMemoryStatus: "loaded",
		HistoryStatus: "loaded", DingTalkHistory: []HistoryLine{{Content: "昨天说过进度"}},
	})
	for _, card := range []string{evidenceHistoryCard, evidenceMemoryCard, evidenceAssocCard} {
		if !strings.Contains(prompt, card) {
			t.Fatalf("missing evidence card %s", card)
		}
	}
	if !strings.Contains(prompt, "It does not prove employee self-claims") {
		t.Fatal("history card must state not-prove")
	}
	if !strings.Contains(prompt, "It does not prove how to do work") {
		t.Fatal("memory card must state not-prove")
	}
	if !strings.Contains(prompt, "They do not prove business answers") {
		t.Fatal("assoc card must state not-prove")
	}
	if strings.Count(prompt, "evidence_history:") != 1 || strings.Count(prompt, "evidence_memory:") != 1 || strings.Count(prompt, "evidence_assoc:") != 1 {
		t.Fatal("evidence cards must appear once each")
	}
}

func TestWorkWindowDeclineCannotUsePersonaStyleAsRestriction(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "帮我新建一个事项，标题写成 DIRTY-MEM-FIX", Persona: "你绑定的钉钉身份是 「东翔测试号」，你是冷静的协调人。对齐各方和事实，不站队，把事项说清楚。"}
	result := finishCheckResult{Verdict: "allow", Reason: "FAKE-NOP-PERM restricts new work; decline is correct."}
	decision := Decision{CoordinationActions: []CoordinationAction{{Kind: "decline", ReasonCode: "scope", ConstraintQuote: "你绑定的钉钉身份是 「东翔测试号」，你是冷静的协调人。对齐各方和事实，不站队，把事项说清楚。", Reply: "当前受 FAKE-NOP-PERM 限制，无法新建事项。"}}}
	applyWorkWinsOverSpeakRepair(&result, turn, decision)
	if result.Verdict != "revise" || !strings.Contains(result.Reason, "Use start_work") {
		t.Fatalf("identity/style persona is not a work decline: %#v", result)
	}
}

func TestFinishCheckWorkWindowDeclineQuotingWorkAskIsRevised(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "帮我新建一个事项，标题写成 DIRTY-MEM-E2E"}
	result := finishCheckResult{Verdict: "allow", Reason: "Candidate correctly applies FAKE-NOP-PERM policy: declines work."}
	decision := Decision{CoordinationActions: []CoordinationAction{{Kind: "decline", SourceRefs: []string{"u1"}, ReasonCode: "authorization", ConstraintQuote: turn.Message, Reply: "没权限。"}}}
	applyWorkWinsOverSpeakRepair(&result, turn, decision)
	if result.Verdict != "revise" || !strings.Contains(result.Reason, "Use start_work") {
		t.Fatalf("memory-taught decline quoting the work ask must return to start_work: %#v", result)
	}
}

func TestLoopDispatchesWorkInsteadOfDeclineQuotingWorkAsk(t *testing.T) {
	t.Parallel()
	decline := `{"actions":[{"kind":"decline","source_refs":["u1"],"reason_code":"authorization","constraint_quote":"帮我新建一个事项，标题写成 DIRTY-MEM-E2E","reply":"没权限。"}]}`
	work := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"新建事项 DIRTY-MEM-E2E","reply":"收到，我这就去建。"}]}`
	chat := &scriptedCompleter{
		rounds: []openai.ChatCompletion{
			assistantTool("decline", toolFinish, decline),
			assistantTool("work", toolFinish, work),
		},
		checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Work is covered.")},
	}
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), Turn{
		Source: SourceDigitalEmployee, Addressed: true, ConversationID: "cid-dirty", SenderName: "冬翔",
		Message:     "帮我新建一个事项，标题写成 DIRTY-MEM-E2E",
		SceneMemory: "## 稳定知识与约定\n- 对外口径 FAKE-NOP-PERM：没有新建事项的工具权限，目录里没有 MCP。",
	})
	if err != nil || d.Action != ActionIssue || chat.calls != 2 {
		t.Fatalf("decline quoting the work ask must repair to start_work: action=%s calls=%d err=%v", d.Action, chat.calls, err)
	}
}

func TestLoopDispatchesWorkInsteadOfSpeakOnlyAck(t *testing.T) {
	t.Parallel()
	ack := `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"correction","reply":"收到，我记一下这个能力。"}]}`
	work := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"为岚调新建一个Aone工单","reply":"收到，我这就去建单。"}]}`
	chat := &scriptedCompleter{
		rounds: []openai.ChatCompletion{
			assistantTool("speak", toolFinish, ack),
			assistantTool("work", toolFinish, work),
		},
		checkRounds: []openai.ChatCompletion{
			scriptedFinishVerdict("allow", "Correction is acknowledged."),
			scriptedFinishVerdict("allow", "Work is covered."),
		},
	}
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), Turn{
		Source: SourceDigitalEmployee, Addressed: true, ConversationID: "cid-work", SenderName: "冬翔",
		Message: "帮我给岚调新建一个aone，内容是支持semantica的能力",
	})
	if err != nil || d.Action != ActionIssue || chat.calls != 2 {
		t.Fatalf("speak-only ack must be repaired to start_work: action=%s calls=%d checks=%d err=%v", d.Action, chat.calls, chat.checkCalls, err)
	}
}
