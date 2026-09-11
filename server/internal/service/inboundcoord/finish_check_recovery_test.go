package inboundcoord

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func badMixedWorkCheck() openai.ChatCompletion {
	raw, _ := json.Marshal(map[string]any{"verdict": "allow", "reason": "All requests covered", "missing_source_refs": []string{}, "request_quote_ref": scriptedRequestQuoteRef, "candidate_quote_ref": scriptedCandidateQuoteRef, "work_checks": []map[string]string{{"action_ref": "a1", "deliverables": "single"}, {"action_ref": "a2", "deliverables": "single"}, {"action_ref": "a3", "deliverables": "none"}}})
	return assistantTool("bad-check", toolFinishCheck, string(raw))
}

func TestFinishCheckProtocolRepairKeepsPlanAndOneMainRound(t *testing.T) {
	candidate := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"查证平板端审批数据导出的支持情况","intent":"lookup","reply":"我来查证支持情况。"},{"kind":"start_work","source_refs":["u1"],"purpose":"起草周五下午三点例会通知，不发送","intent":"other","reply":"我来起草通知。"},{"kind":"clarify","source_refs":["u1"],"missing_fields":["recipient","message_body"],"reply":"还需要给谁发什么内容？"}]}`
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("plan", toolFinish, candidate)}, checkRounds: []openai.ChatCompletion{badMixedWorkCheck(), scriptedFinishVerdict("allow", "Two work items and a necessary clarification are covered.")}}
	saves := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(d Decision) error {
		saves++
		if len(d.Items) != 2 {
			t.Errorf("lost work item")
		}
		return nil
	})
	d, err := (&Coordinator{Chat: chat}).runLoop(ctx, Turn{Source: SourceWeb, Message: "请查证平板端审批导出；另起草周五三点例会通知，先不发送；最后帮我发个消息。"})
	if err != nil || d.Action != ActionIssue || saves != 1 || chat.calls != 1 || chat.checkCalls != 2 {
		t.Fatalf("protocol recovery did not preserve the valid plan: action=%s saves=%d main=%d review=%d err=%v", d.Action, saves, chat.calls, chat.checkCalls, err)
	}
	if !reflect.DeepEqual(chat.checkParams[0].Messages, chat.checkParams[1].Messages[:len(chat.checkParams[0].Messages)]) {
		t.Fatal("protocol retry changed original review inputs")
	}
	props := chat.checkParams[1].Tools[0].GetFunction().Parameters["properties"].(map[string]any)
	checkItems := props["work_checks"].(map[string]any)["items"].(map[string]any)
	refs := checkItems["properties"].(map[string]any)["action_ref"].(map[string]any)["enum"]
	if !reflect.DeepEqual(refs, []string{"a1", "a2"}) {
		t.Fatalf("non-work action leaked into review reference schema: %#v", refs)
	}
	raw, _ := json.Marshal(chat.checkParams[1].Messages[len(chat.checkParams[1].Messages)-1])
	if !strings.Contains(string(raw), "required_work_action_refs") {
		t.Fatal("protocol repair omitted concrete allowed work refs")
	}
}

func TestFinishCheckProtocolRepairDoesNotOverrideRevise(t *testing.T) {
	malformed := assistantTool("bad", toolFinishCheck, `{"verdict":"allow"}`)
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{malformed, scriptedFinishVerdict("revise", "Only drafting is authorized; sending is not authorized.", "u1")}}
	turn := Turn{Source: SourceWeb, Message: "只起草通知，不要发送。"}
	candidate := Decision{Action: ActionIssue, UserText: "我来发送通知。", CoordinationActions: []CoordinationAction{{Kind: "start_work", SourceRefs: []string{"u1"}, Purpose: "发送通知给全体同事", Intent: "notify", Reply: "我来发送通知。"}}, Items: []WindowItem{{SourceRefs: []string{"u1"}, Purpose: "发送通知给全体同事", Intent: "notify"}}}
	cache := map[string]finishCheckResult{}
	c := &Coordinator{Chat: chat}
	got, err := c.checkFinish(context.Background(), turn, candidate, nil, 0, cache)
	if err != nil || got.Verdict != "revise" || chat.checkCalls != 2 {
		t.Fatalf("repair replaced semantic rejection: %#v calls=%d err=%v", got, chat.checkCalls, err)
	}
	got, err = c.checkFinish(context.Background(), turn, candidate, nil, 1, cache)
	if err != nil || got.Verdict != "revise" || chat.checkCalls != 2 {
		t.Fatal("valid rejection was not cached")
	}
}

func TestRejectedNonWorkProposalDoesNotAbortOnInventedWorkReference(t *testing.T) {
	result := finishCheckResult{Verdict: "revise", Reason: "The request requires execution rather than a claim of inability.", WorkChecks: []finishWorkCheck{{ActionRef: "a1", Deliverables: "single", TargetMatch: "new_work"}}}
	candidate := Decision{Action: ActionReply, CoordinationActions: []CoordinationAction{{Kind: "decline", SourceRefs: []string{"u1"}, Reply: "I cannot execute commands."}}}
	if err := validateFinishWorkChecks(&result, candidate); err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "revise" || len(result.WorkChecks) != 0 {
		t.Fatalf("rejection changed or invented evidence retained: %+v", result)
	}
	result.Verdict = "allow"
	result.WorkChecks = []finishWorkCheck{{ActionRef: "a1", Deliverables: "single", TargetMatch: "new_work"}}
	if err := validateFinishWorkChecks(&result, candidate); err == nil {
		t.Fatal("invented work reference authorized non-work candidate")
	}
}

func TestRejectedProposalRepairsInMainLoopWithoutGrantingWork(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"verdict": "revise", "reason": "The scope is already explicit; route the requested calculation to execution.", "missing_source_refs": []string{}, "request_quote_ref": scriptedRequestQuoteRef, "candidate_quote_ref": scriptedCandidateQuoteRef, "work_checks": []finishWorkCheck{{ActionRef: "a1", Deliverables: "single", TargetMatch: "new_work"}}})
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("clarify", toolFinish, `{"actions":[{"kind":"clarify","source_refs":["u1"],"missing_fields":["scope"],"reply":"需要计算什么？"}]}`),
		assistantTool("work", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"实际执行 Python 计算 1 到 100 的平方和","reply":"我来运行 Python 计算。"}]}`),
	}, checkRounds: []openai.ChatCompletion{assistantTool("review", toolFinishCheck, string(raw)), scriptedFinishVerdict("allow", "The explicit calculation is authorized.")}}
	saves := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(d Decision) error { saves++; return nil })
	d, err := (&Coordinator{Chat: chat}).runLoop(ctx, Turn{Source: SourceWeb, Message: "请实际运行 Python 计算 1 到 100 的平方和。"})
	if err != nil || d.Action != ActionIssue || saves != 1 || chat.calls != 2 || chat.checkCalls != 2 {
		t.Fatalf("action=%s saves=%d main=%d review=%d err=%v", d.Action, saves, chat.calls, chat.checkCalls, err)
	}
	props := chat.checkParams[0].Tools[0].GetFunction().Parameters["properties"].(map[string]any)
	if props["work_checks"].(map[string]any)["maxItems"] != 0 {
		t.Fatal("non-work schema permits invented work checks")
	}
}
