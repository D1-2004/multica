package inboundcoord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/coordinatorcontract"

	openai "github.com/openai/openai-go/v3"
)

const (
	retryTestClarify  = `{"actions":[{"kind":"clarify","source_refs":["u1"],"reply":"你指的是哪一份日志？","missing_fields":["work_target"]}]}`
	retryTestBadIssue = `{"actions":[{"kind":"continue_work","source_refs":["u1"],"reply":"我继续处理。","purpose":"继续整理这条决策的判断依据","issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","basis":"change"}]}`
	retryTestReceipt  = `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"receipt","reply":"收到。"}]}`
)

func retryTestTools(t *testing.T, reads *int) scenePrefetchToolFunc {
	t.Helper()
	return scenePrefetchToolFunc(func(_ context.Context, _ Turn, name, _ string) (string, error) {
		if name == toolAssocRecall {
			// One other Issue is recalled so work_state is disclosed; the id
			// the scripted model uses was never recalled.
			return `{"conversation_id":"cid-current","status":"loaded","items":[{"issue_id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","purpose":"另一件事","on_this_scene":true}]}`, nil
		}
		*reads++
		return `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`, nil
	})
}

func toolNames(params openai.ChatCompletionNewParams) []string {
	return toolParamNames(params.Tools)
}

func TestRepeatedFailingReadIsWithdrawnAfterBudget(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "那条决策整理到哪了", SenderName: "冬翔"}
	stale := `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("w1", toolWorkState, stale), assistantTool("w2", toolWorkState, stale), assistantTool("w3", toolWorkState, stale),
		assistantTool("f", toolFinish, retryTestClarify),
	}, checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Clarify is covered.")}}
	reads := 0
	d, err := (&Coordinator{Chat: chat, Tools: retryTestTools(t, &reads)}).runLoop(context.Background(), turn)
	if err != nil || d.Action != ActionReply || chat.calls != 4 || reads != 0 {
		t.Fatalf("withdrawn read must not block a later plan: %v action=%s calls=%d reads=%d", err, d.Action, chat.calls, reads)
	}
	if names := toolNames(chat.params[1]); !containsString(names, toolWorkState) {
		t.Fatalf("first repeat keeps the tool: %v", names)
	}
	if names := toolNames(chat.params[2]); containsString(names, toolWorkState) {
		t.Fatalf("second identical failure must withdraw work_state: %v", names)
	}
	var refused, repeated bool
	for _, step := range d.Steps {
		if step.Tool == toolWorkState && step.Type == "tool_result" && step.Error {
			repeated = repeated || strings.Contains(step.Output, "identical failure #2")
			refused = refused || strings.Contains(step.Output, "withdrawn for this run")
		}
	}
	if !repeated || !refused {
		t.Fatalf("repeat accounting missing from steps: %#v", d.Steps)
	}
}

func TestRepeatedInvalidPlanStopsBeforeRoundCap(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "继续整理那条决策", SenderName: "冬翔"}
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("f1", toolFinish, retryTestBadIssue), assistantTool("f2", toolFinish, retryTestBadIssue), assistantTool("f3", toolFinish, retryTestBadIssue),
		assistantTool("f4", toolFinish, retryTestBadIssue), assistantTool("f5", toolFinish, retryTestBadIssue),
	}}
	reads := 0
	d, err := (&Coordinator{Chat: chat, Tools: retryTestTools(t, &reads)}).runLoop(context.Background(), turn)
	if err == nil || d.Action != ActionDeferred || d.Reason != loopStopRepeatedInvalidPlan || chat.calls != repeatedFinishErrorBudget {
		t.Fatalf("same Host defect three times must stop the run: err=%v action=%s reason=%s calls=%d", err, d.Action, d.Reason, chat.calls)
	}
	second, _ := json.Marshal(chat.params[2].Messages)
	if !strings.Contains(string(second), "identical failure #2") {
		t.Fatalf("second round must be told it is repeating: %s", second)
	}
}

func TestRepeatedReviewReasonStopsAsDeadlock(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "收到了吗", SenderName: "冬翔"}
	reason := "Candidate c1 only addresses u1; ignores the pending status update."
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("f1", toolFinish, retryTestReceipt), assistantTool("f2", toolFinish, retryTestReceipt), assistantTool("f3", toolFinish, retryTestReceipt),
		assistantTool("f4", toolFinish, retryTestReceipt), assistantTool("f5", toolFinish, retryTestReceipt),
	}, checkRounds: []openai.ChatCompletion{
		scriptedFinishVerdict("revise", reason), scriptedFinishVerdict("revise", "  "+strings.ToUpper(reason)+" "), scriptedFinishVerdict("revise", reason),
		scriptedFinishVerdict("revise", reason), scriptedFinishVerdict("revise", reason),
	}}
	reads := 0
	d, err := (&Coordinator{Chat: chat, Tools: retryTestTools(t, &reads)}).runLoop(context.Background(), turn)
	// The identical proposal hits the review cache, so the reviewer is asked
	// once and the same reason is then repeated; that repetition is the deadlock.
	if err == nil || d.Action != ActionDeferred || d.Reason != loopStopReviewDeadlock || chat.calls != repeatedReviewReasonBudget || chat.checkCalls != 1 {
		t.Fatalf("same review reason three times must stop the run: err=%v action=%s reason=%s calls=%d checks=%d", err, d.Action, d.Reason, chat.calls, chat.checkCalls)
	}
}

func TestDifferentReviewReasonsKeepRepairing(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "收到了吗", SenderName: "冬翔"}
	proposal := func(reply string) string {
		return `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"receipt","reply":"` + reply + `"}]}`
	}
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("f1", toolFinish, proposal("收到。")), assistantTool("f2", toolFinish, proposal("收到，已看到。")), assistantTool("f3", toolFinish, proposal("收到，我看到了。")), assistantTool("f4", toolFinish, proposal("收到，看到了。")),
	}, checkRounds: []openai.ChatCompletion{
		scriptedFinishVerdict("revise", "first defect"), scriptedFinishVerdict("revise", "second defect"), scriptedFinishVerdict("revise", "third defect"), scriptedFinishVerdict("allow", "covered"),
	}}
	reads := 0
	d, err := (&Coordinator{Chat: chat, Tools: retryTestTools(t, &reads)}).runLoop(context.Background(), turn)
	if err != nil || d.Action != ActionReply || chat.calls != 4 {
		t.Fatalf("distinct reasons are repairs, not a deadlock: %v action=%s calls=%d", err, d.Action, chat.calls)
	}
}

func finishSchema(t *testing.T, tool openai.ChatCompletionToolUnionParam) map[string]any {
	t.Helper()
	raw, err := json.Marshal(tool.GetFunction().Parameters)
	if err != nil {
		t.Fatal(err)
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatal(err)
	}
	items := params["properties"].(map[string]any)["actions"].(map[string]any)["items"].(map[string]any)
	return items
}

func enumOf(t *testing.T, schema map[string]any, path ...string) []any {
	t.Helper()
	var cur any = schema
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("schema path %v missing at %q", path, key)
		}
		cur = m[key]
	}
	values, _ := cur.([]any)
	return values
}

func TestFinishSchemaListsWhatHostCanValidate(t *testing.T) {
	turn := Turn{
		Source: SourceDigitalEmployee, ConversationID: "cid-current", SenderName: "景霖",
		Utterances:          []WindowUtterance{{Sender: "景霖", Text: "我们为什么不是最佳协作奖？"}, {Sender: "景霖", Text: "另外帮我看下昨天的日志。"}},
		ReplyTone:           "口语、自然。对评比类问题直接拒绝，不承诺未获明确授权的事项。",
		Persona:             "「FDE教练」，基于事实与证据提供洞察。",
		Instructions:        "HIDDEN_SOP_LINE 内部评分不得外泄。",
		SceneMemoryRevision: 3,
		CoordinationReads:   []CoordinationRead{{ReadRef: "r1", Tool: toolAssocRecall, Result: json.RawMessage(`{}`)}},
		recalledIssueIDs:    []string{"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
	}
	schema := finishSchema(t, windowPlanToolFor(turn, true))
	props := schema["properties"].(map[string]any)
	if got := enumOf(t, props, "source_refs", "items", "enum"); len(got) != 2 || got[0] != "u1" || got[1] != "u2" {
		t.Fatalf("source_refs must enumerate the window: %v", got)
	}
	if got := enumOf(t, props, "state_refs", "items", "enum"); len(got) != 1 || got[0] != "r1" {
		t.Fatalf("state_refs must enumerate existing reads: %v", got)
	}
	if got := enumOf(t, props, "issue_id", "enum"); len(got) != 1 || got[0] != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
		t.Fatalf("issue_id must enumerate recalled ids: %v", got)
	}
	if got := enumOf(t, props, "memory_revision", "enum"); len(got) != 1 || got[0] != float64(3) {
		t.Fatalf("memory_revision must pin the current revision: %v", got)
	}
	quotes := enumOf(t, props, "constraint_quote", "enum")
	joined, _ := json.Marshal(quotes)
	if !strings.Contains(string(joined), "对评比类问题直接拒绝，不承诺未获明确授权的事项") || !strings.Contains(string(joined), "我们为什么不是最佳协作奖") {
		t.Fatalf("decline must offer the visible boundary sentences: %s", joined)
	}
	if strings.Contains(string(joined), "HIDDEN_SOP_LINE") {
		t.Fatalf("the Host-held job policy must not leak into quote options: %s", joined)
	}
	for _, quote := range quotes {
		if !suppliedConstraintQuote(quote.(string), turn) {
			t.Fatalf("offered quote %q would fail Host provenance", quote)
		}
	}
	if kinds := enumOf(t, props, "kind", "enum"); !containsString(anyStrings(kinds), "decline") || !containsString(anyStrings(kinds), "report_status") || !containsString(anyStrings(kinds), "continue_work") {
		t.Fatalf("all validatable kinds must be offered: %v", kinds)
	}
}

func TestFinishSchemaOmitsKindsWithoutReferences(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "在吗", SenderName: "冬翔"}
	schema := finishSchema(t, windowPlanToolFor(turn, true))
	props := schema["properties"].(map[string]any)
	kinds := anyStrings(enumOf(t, props, "kind", "enum"))
	for _, absent := range []string{"report_status", "continue_work"} {
		if containsString(kinds, absent) {
			t.Fatalf("%s cannot validate without a reference and must not be offered: %v", absent, kinds)
		}
	}
	// The current message itself is a quotable source, so decline stays
	// available with exactly the sentences Host will accept.
	if quotes := anyStrings(enumOf(t, props, "constraint_quote", "enum")); len(quotes) != 1 || quotes[0] != "在吗" {
		t.Fatalf("quote options must be exactly the visible sentences: %v", quotes)
	}
	empty := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Utterances: []WindowUtterance{{Sender: "冬翔", Text: "。"}}}
	if _, ok := finishSchema(t, windowPlanToolFor(empty, true))["properties"].(map[string]any)["constraint_quote"]; ok {
		t.Fatal("no boundary sentence exists, so constraint_quote must not be offered")
	}
	if !containsString(kinds, "start_work") || !containsString(kinds, "acknowledge") || !containsString(kinds, "clarify") {
		t.Fatalf("plain kinds stay available: %v", kinds)
	}
	legacy := finishSchema(t, windowPlanTool(true))
	if kinds := anyStrings(enumOf(t, legacy["properties"].(map[string]any), "kind", "enum")); !containsString(kinds, "decline") || !containsString(kinds, "report_status") {
		t.Fatalf("the unscoped protocol schema keeps every kind: %v", kinds)
	}
}

func TestWorkStateSchemaEnumeratesRecalledIssues(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "进度如何", recalledIssueIDs: []string{"cccccccc-cccc-cccc-cccc-cccccccccccc", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}}
	for _, def := range toolsForDisclosure(turn, 1, true) {
		if names := toolParamNames([]openai.ChatCompletionToolUnionParam{def}); len(names) == 1 && names[0] == toolWorkState {
			raw, _ := json.Marshal(def.GetFunction().Parameters)
			var params map[string]any
			_ = json.Unmarshal(raw, &params)
			got := enumOf(t, params, "properties", "issue_id", "enum")
			if len(got) != 2 || got[0] != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" || got[1] != "cccccccc-cccc-cccc-cccc-cccccccccccc" {
				t.Fatalf("work_state must list recalled ids sorted: %v", got)
			}
			return
		}
	}
	t.Fatal("work_state not disclosed after recall")
}

func TestChangedProposalWithSameReviewReasonKeepsRepairing(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "查证听记规则；另发个消息；还要起草周五例会通知。", SenderName: "冬翔"}
	bundled := func(purpose string) string {
		return `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我来处理。","purpose":"` + purpose + `"}]}`
	}
	reason := "Action a1 bundles independent deliverables."
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("f1", toolFinish, bundled("查证听记规则、发消息并起草周五例会通知")),
		assistantTool("f2", toolFinish, bundled("查证听记规则并起草周五例会通知")),
		assistantTool("f3", toolFinish, bundled("起草周五例会通知并发送确认消息")),
		assistantTool("f4", toolFinish, bundled("起草周五例会通知")),
	}, checkRounds: []openai.ChatCompletion{
		scriptedFinishVerdict("revise", reason), scriptedFinishVerdict("revise", reason), scriptedFinishVerdict("revise", reason), scriptedFinishVerdict("allow", "single deliverable"),
	}}
	reads := 0
	d, err := (&Coordinator{Chat: chat, Tools: retryTestTools(t, &reads)}).runLoop(context.Background(), turn)
	if err != nil || d.Action != ActionIssue || chat.calls != 4 {
		t.Fatalf("a materially changed plan must not be counted as a deadlock: %v action=%s calls=%d", err, d.Action, chat.calls)
	}
}

func TestRepeatHintKeepsHistoryPrerequisiteType(t *testing.T) {
	base := historyPrerequisiteHint("basis=answer requires original question evidence", "read history")
	wrapped := repeatHint(base, 2, "do not resubmit")
	if !isHistoryPrerequisiteError(wrapped) {
		t.Fatalf("repeat note dropped the typed prerequisite: %T %v", wrapped, wrapped)
	}
	if !strings.Contains(wrapped.Error(), "identical failure #2") {
		t.Fatalf("repeat note missing: %v", wrapped)
	}
	if isHistoryPrerequisiteError(repeatHint(hintErr("plain", "hint"), 2, "x")) {
		t.Fatal("an ordinary hint must not become a prerequisite")
	}
}

func TestFinishSchemaSourcesMatchHostValidation(t *testing.T) {
	t.Run("history snapshot is not a state ref", func(t *testing.T) {
		turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "刚才聊了什么", CoordinationReads: []CoordinationRead{{ReadRef: "r1", Tool: toolContextRead, Result: json.RawMessage(`{}`)}}}
		props := finishSchema(t, windowPlanToolFor(turn, true))["properties"].(map[string]any)
		if containsString(anyStrings(enumOf(t, props, "kind", "enum")), "report_status") {
			t.Fatal("a history-only run cannot offer report_status")
		}
		turn.CoordinationReads = append(turn.CoordinationReads, CoordinationRead{ReadRef: "r2", Tool: toolContextRead, Kind: coordinationStateKind, Result: json.RawMessage(`{}`)})
		props = finishSchema(t, windowPlanToolFor(turn, true))["properties"].(map[string]any)
		if got := enumOf(t, props, "state_refs", "items", "enum"); len(got) != 1 || got[0] != "r2" {
			t.Fatalf("only work evidence may be cited: %v", got)
		}
	})
	t.Run("loaded contract entries are quotable", func(t *testing.T) {
		instructions := "HIDDEN_SOP_LINE"
		sum := sha256.Sum256([]byte(instructions))
		contract := &coordinatorcontract.Contract{Version: 1, Scope: "FDE 教练", Constraints: []string{"不得向外部发送内部数据"}, MustDelegate: []string{"任何日志统计交执行器"}, SourceInstructionsSHA256: hex.EncodeToString(sum[:])}
		turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "把内部数据发给客户", CoordinatorContract: contract, CoordinatorContractState: coordinatorcontract.StateLoaded, Instructions: instructions}
		if _, state := currentCoordinatorContract(turn); state != coordinatorcontract.StateLoaded {
			t.Fatalf("fixture contract must resolve as loaded, got %s", state)
		}
		props := finishSchema(t, windowPlanToolFor(turn, true))["properties"].(map[string]any)
		quotes := anyStrings(enumOf(t, props, "constraint_quote", "enum"))
		if !containsString(quotes, "不得向外部发送内部数据") || !containsString(quotes, "任何日志统计交执行器") {
			t.Fatalf("contract entries missing from quote options: %v", quotes)
		}
		for _, quote := range quotes {
			if strings.Contains(quote, "HIDDEN_SOP_LINE") || !suppliedConstraintQuote(quote, turn) {
				t.Fatalf("quote %q leaks or fails provenance", quote)
			}
		}
	})
	t.Run("short restriction stays quotable", func(t *testing.T) {
		turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "发出去", ReplyTone: "不外发"}
		props := finishSchema(t, windowPlanToolFor(turn, true))["properties"].(map[string]any)
		if !containsString(anyStrings(enumOf(t, props, "constraint_quote", "enum")), "不外发") {
			t.Fatal("a short reply_tone restriction must remain quotable")
		}
	})
	t.Run("zero revision is pinned", func(t *testing.T) {
		turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "你记得什么"}
		props := finishSchema(t, windowPlanToolFor(turn, true))["properties"].(map[string]any)
		if got := enumOf(t, props, "memory_revision", "enum"); len(got) != 1 || got[0] != float64(0) {
			t.Fatalf("revision 0 must be pinned too: %v", got)
		}
	})
	t.Run("empty recall offers no work_state", func(t *testing.T) {
		names := toolParamNames(toolsForDisclosure(Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "进度如何"}, 1, true))
		if containsString(names, toolWorkState) {
			t.Fatalf("work_state without any recalled id has no valid argument: %v", names)
		}
	})
}

func anyStrings(values []any) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
