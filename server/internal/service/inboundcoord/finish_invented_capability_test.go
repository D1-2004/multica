package inboundcoord

import (
	"context"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func TestValidateFinishWorkChecksRewritesContinueWorkDifferentDeliverable(t *testing.T) {
	result := finishCheckResult{
		Verdict: "revise", Reason: "continue_work target is different_deliverable: original goal was drafting copy, new purpose is executing tool call.",
		WorkChecks: []finishWorkCheck{{ActionRef: "a1", Deliverables: "single", TargetMatch: "different_deliverable"}},
	}
	decision := Decision{CoordinationActions: []CoordinationAction{{Kind: "continue_work"}}}
	if err := validateFinishWorkChecks(&result, decision); err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "revise" || !strings.Contains(result.Reason, "Use start_work") || strings.Contains(result.Reason, "drafting copy") {
		t.Fatalf("Host must name start_work instead of keeping the canned deliverable reason: %#v", result)
	}
	if !finishRevisionRequiresKindChange(result.Reason) {
		t.Fatal("different_deliverable repair must count as a kind change")
	}
}

func TestFinishRevisionHintNamesKindChangeForDifferentDeliverable(t *testing.T) {
	hint := finishRevisionHint(finishCheckResult{Reason: differentDeliverableRepairReason("a1"), MissingSourceRefs: []string{}})
	if !strings.Contains(hint, "Change the action kind") || strings.Contains(hint, "Repair the diagnosed action/field") {
		t.Fatalf("independent deliverable repair must not tell the model to reword continue_work: %s", hint)
	}
}

func TestFinishCheckAllowsStartWorkWhenReviewerInventsCatalogLimit(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-aone", Message: "@VOC决策助理(金龙) 帮我给岚调新建一个aone，内容是支持semantica的能力"}
	raw := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"为岚调新建 Aone 工单，内容为支持 semantica 的能力","reply":"收到，我这就用 Aone MCP 给岚调新建工单。"}]}`
	candidate, err := parseValidatedWindowPlan(raw, turn, []recallCall{{ConversationID: turn.ConversationID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("revise", "无Aone MCP工具权限，无法执行新建工单操作。")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil)
	if err != nil || result.Verdict != "allow" || !strings.Contains(result.Reason, "Access checks belong to the executor") {
		t.Fatalf("invented catalog limit must not block start_work: verdict=%s reason=%s err=%v", result.Verdict, result.Reason, err)
	}
}

func TestFinishCheckRevisesClarifyThatInventsCatalogLimit(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "帮我给岚调新建一个aone"}
	candidate := Decision{Action: ActionReply, UserText: "我这边没有直接调用 Aone MCP 的权限。你直接去 Aone 创建？", CoordinationActions: []CoordinationAction{{Kind: "clarify", SourceRefs: []string{"u1"}, MissingFields: []string{"authorization"}, Reply: "我这边没有直接调用 Aone MCP 的权限。你直接去 Aone 创建？"}}}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Candidate correctly identifies lack of Aone tool permission and seeks clarification.")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil)
	if err != nil || result.Verdict != "revise" || !strings.Contains(result.Reason, "Use start_work") {
		t.Fatalf("invented authorization clarify must return to start_work: verdict=%s reason=%s err=%v", result.Verdict, result.Reason, err)
	}
}

func TestFinishCheckRevisesSceneMemoryUsedAsToolAuthorization(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "这个mcp里面不是有 create_workitem 方法吗？", SceneMemory: "具备 AoneCoopMCP 工具能力"}
	candidate := Decision{Action: ActionReply, UserText: "我这边没有直接调用 AoneCoopMCP 建单的权限。", CoordinationActions: []CoordinationAction{{Kind: "clarify", SourceRefs: []string{"u1"}, MissingFields: []string{"authorization"}, Reply: "我这边没有直接调用 AoneCoopMCP 建单的权限。"}}}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("revise", "Candidate claims lack of permission despite scene memory confirming AoneCoopMCP capability; fails to use available tool.")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil)
	if err != nil || result.Verdict != "revise" || !strings.Contains(result.Reason, "scene memory") || strings.Contains(result.Reason, "fails to use available tool") {
		t.Fatalf("scene memory must not be treated as a tool inventory: verdict=%s reason=%s err=%v", result.Verdict, result.Reason, err)
	}
}

func TestFinishCheckKeepsQuotedJobPolicyRestriction(t *testing.T) {
	turn := Turn{Source: SourceWeb, Message: "把这份草稿发给全员。", Instructions: "Only draft. Never send until the user explicitly approves."}
	raw := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"把周五开会通知发给全员","reply":"我现在把通知发出去。"}]}`
	candidate, err := parseValidatedWindowPlan(raw, turn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reply := `{"verdict":"revise","reason":"The policy and current request authorize drafting only, not sending.","request_quote_ref":"q1","candidate_quote_ref":"c1","missing_source_refs":[],"constraint_quote":"Never send until the user explicitly approves.","work_checks":[{"action_ref":"a1","deliverables":"single","target_match":"new_work"}]}`
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{assistantTool("review", toolFinishCheck, reply)}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil)
	if err != nil || result.Verdict != "revise" || result.ConstraintQuote == "" || strings.Contains(result.Reason, "executor checks access") {
		t.Fatalf("quoted send restriction must survive: verdict=%s reason=%s quote=%q err=%v", result.Verdict, result.Reason, result.ConstraintQuote, err)
	}
}

func TestFinishCheckAllowsSameDeliverableMethodChangeDespiteCatalogReason(t *testing.T) {
	result := finishCheckResult{
		Verdict: "revise", Reason: "Candidate proposes executing AoneCoopMCP create_workitem, which is not in the installed skills catalog; capability gap prevents authorization.",
		WorkChecks: []finishWorkCheck{{ActionRef: "a1", Deliverables: "single", TargetMatch: "same_deliverable"}},
	}
	decision := Decision{CoordinationActions: []CoordinationAction{{Kind: "continue_work", Basis: "change"}}}
	if err := validateFinishWorkChecks(&result, decision); err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "allow" || !strings.Contains(result.Reason, "Access checks belong to the executor") {
		t.Fatalf("same deliverable method change must not be blocked by catalog absence: %#v", result)
	}
}

func TestLoopSubmitsStartWorkWhenReviewerInventsCatalogLimit(t *testing.T) {
	t.Parallel()
	finish := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"为岚调新建一个Aone工单，内容为支持semantica的能力","reply":"收到，我这就去建单。"}]}`
	chat := &scriptedCompleter{
		rounds:      []openai.ChatCompletion{assistantTool("plan", toolFinish, finish)},
		checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("revise", "无对应工具支持新建Aone工单")},
	}
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), Turn{
		Source: SourceDigitalEmployee, Addressed: true, ConversationID: "cid-aone", SenderName: "璟琦",
		Message: "@VOC决策助理(金龙) 帮我给岚调新建一个aone，内容是支持semantica的能力",
	})
	if err != nil || d.Action != ActionIssue || chat.checkCalls != 1 || chat.calls != 1 {
		t.Fatalf("catalog invention must not open a repair loop: action=%s calls=%d checks=%d err=%v", d.Action, chat.calls, chat.checkCalls, err)
	}
}
