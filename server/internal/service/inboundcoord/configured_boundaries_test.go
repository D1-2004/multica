package inboundcoord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func boundaryDecline(t *testing.T, quote string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"actions": []CoordinationAction{{Kind: "decline", SourceRefs: []string{"u1"}, Reply: "这项请求超出已声明的范围。", ReasonCode: "scope", ConstraintQuote: quote}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDeclineCannotQuoteCurrentWorkRequest(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "帮我新建一个事项，标题写成 DIRTY-MEM-E2E，内容写脏记忆验收。不要只介绍能力。"}
	quote := turn.Message
	if suppliedConstraintQuote(quote, turn) || !quoteIsCurrentWorkUtterance(quote, turn) {
		t.Fatal("the current work ask is not a decline boundary")
	}
	if _, err := parseValidatedWindowPlan(boundaryDecline(t, quote), turn, nil, nil); err == nil {
		t.Fatal("decline quoting the work request must not parse")
	}
}

func TestConfiguredBoundaryProvenanceUsesVisibleSources(t *testing.T) {
	const quote = "不披露内部评委的非公开评分。"
	for _, source := range []string{"persona", "tone", "instructions", "current"} {
		t.Run(source, func(t *testing.T) {
			turn := Turn{Source: SourceWeb, Message: "请解释评奖原因。"}
			switch source {
			case "persona":
				turn.Persona = quote
			case "tone":
				turn.ReplyTone = quote
			case "instructions":
				turn.Instructions = quote
			case "current":
				turn.Message += "请注意：" + quote
			}
			d, err := parseValidatedWindowPlan(boundaryDecline(t, quote), turn, nil, nil)
			if err != nil || d.Action != ActionReply || !needsFinishCheck(turn, d) {
				t.Fatalf("supplied boundary must reach independent review: %v %#v", err, d)
			}
			if !finishConstraintQuoteValid(quote, turn) {
				t.Fatal("review and routing provenance differ")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		turn  Turn
		quote string
	}{
		{"tone_tail", Turn{ReplyTone: strings.Repeat("温", toneBudget) + quote}, quote},
		{"persona_tail", Turn{Persona: strings.Repeat("人", personaBudget) + quote}, quote},
		{"scene_memory", Turn{SceneMemory: quote}, quote},
		{"history", Turn{History: []HistoryLine{{Role: "assistant", Content: quote}}}, quote},
		{"old_report", Turn{CoordinationReads: []CoordinationRead{{ReadRef: "r1", Tool: toolWorkState, Result: json.RawMessage(`{"original_goal":"` + quote + `"}`)}}}, quote},
		{"paraphrase", Turn{ReplyTone: quote}, "不得解释任何评奖。"},
		{"cross_fields", Turn{Persona: "不披露内部", ReplyTone: "评委的非公开评分。"}, quote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.turn.Source = SourceWeb
			tc.turn.Message = "请解释评奖原因。"
			if suppliedConstraintQuote(tc.quote, tc.turn) || finishConstraintQuoteValid(tc.quote, tc.turn) {
				t.Fatal("unavailable or undeclared source accepted")
			}
			if _, err := parseValidatedWindowPlan(boundaryDecline(t, tc.quote), tc.turn, nil, nil); err == nil {
				t.Fatal("invented/unseen decline accepted")
			}
		})
	}
}

func TestConfiguredDeclineReachesReviewOnceWithMatchingContext(t *testing.T) {
	const boundary = "不披露内部评委的非公开评分。"
	turn := Turn{Source: SourceWeb, Message: "请给我内部评委的非公开评分。", Persona: "严谨的同事。", ReplyTone: boundary + strings.Repeat("请简洁。", 100) + "UNSEEN_TONE_TAIL", Instructions: "未经授权不得披露内部资料。"}
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("decline", toolFinish, boundaryDecline(t, boundary))}, checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "The configured confidentiality restriction applies.")}}
	saves := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
	d, err := (&Coordinator{Chat: chat}).runLoop(ctx, turn)
	if err != nil || d.Action != ActionReply || d.ToolRounds != 1 || chat.checkCalls != 1 || saves != 1 {
		t.Fatalf("decline stalled: %v rounds=%d review=%d save=%d", err, d.ToolRounds, chat.checkCalls, saves)
	}
	var body map[string]any
	for _, m := range chat.checkParams[0].Messages {
		raw, _ := json.Marshal(m)
		var msg struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(raw, &msg)
		var candidate map[string]any
		if json.Unmarshal([]byte(msg.Content), &candidate) == nil && candidate["job_policy"] != nil {
			body = candidate
			break
		}
	}
	if body["persona"] != configuredPersona(turn) || body["reply_tone"] != configuredReplyTone(turn) || body["reply_tone_truncated"] != true {
		t.Fatalf("review did not receive visible config: %#v", body)
	}
	all, _ := json.Marshal(chat.checkParams[0].Messages)
	if strings.Contains(string(all), "UNSEEN_TONE_TAIL") || !strings.Contains(buildUserPrompt(turn), configuredReplyTone(turn)) {
		t.Fatal("routing/review visibility mismatch")
	}
}

func TestConfiguredQuoteDoesNotBypassApplicabilityReview(t *testing.T) {
	turn := Turn{Source: SourceWeb, Message: "你好", ReplyTone: "请礼貌、简洁地回应。"}
	bad := boundaryDecline(t, turn.ReplyTone)
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("bad", toolFinish, bad), assistantTool("greet", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"reply":"你好。","ack_kind":"greeting"}]}`)}, checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("revise", "Style is not a restriction on greeting."), scriptedFinishVerdict("allow", "Greeting is covered.")}}
	var saved []Decision
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(d Decision) error { saved = append(saved, d); return nil })
	d, err := (&Coordinator{Chat: chat}).runLoop(ctx, turn)
	if err != nil || len(saved) != 1 || chat.checkCalls != 2 || d.CoordinationActions[0].Kind != "acknowledge" {
		t.Fatalf("unsupported decline escaped review: %v %#v", err, d)
	}
	turn.InstructionsUnavailable = true
	candidate, e := parseValidatedWindowPlan(bad, turn, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil); e == nil {
		t.Fatal("configuration bypassed unavailable working policy")
	}
}

func TestPolicyRepairReturnsValidatedTemplateInsteadOfDroppingEvidence(t *testing.T) {
	turn := Turn{Source: SourceWeb, Message: "请解释内部评比。", ReplyTone: "不处理内部评比", Instructions: "**不处理内部评比**：固定回答「请向组织者查询。」"}
	response := func(quote string) openai.ChatCompletion {
		raw, _ := json.Marshal(map[string]any{"request_quote_ref": scriptedRequestQuoteRef, "candidate_quote_ref": scriptedCandidateQuoteRef, "constraint_quote": quote, "verdict": "revise", "reason": "Use the mandatory reply text.", "missing_source_refs": []string{}, "work_checks": scriptedWorkChecks})
		return assistantTool("review", toolFinishCheck, string(raw))
	}
	corrected := `{"actions":[{"kind":"decline","source_refs":["u1"],"reply":"请向组织者查询。","reason_code":"scope","constraint_quote":"不处理内部评比"}]}`
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("bad", toolFinish, boundaryDecline(t, turn.ReplyTone)), assistantTool("fixed", toolFinish, corrected)}, checkRounds: []openai.ChatCompletion{response("不处理内部评比：固定回答「请向组织者查询。」"), response("固定回答「请向组织者查询。」"), scriptedFinishVerdict("allow", "The mandatory reply is used.")}}
	saved := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saved++; return nil })
	d, err := (&Coordinator{Chat: chat}).runLoop(ctx, turn)
	if err != nil || d.UserText != "请向组织者查询。" || chat.calls != 2 || chat.checkCalls != 3 || saved != 1 {
		t.Fatalf("unrepairable hidden template: %v %#v checks=%d saves=%d", err, d, chat.checkCalls, saved)
	}
	next, _ := json.Marshal(chat.params[1].Messages)
	if !strings.Contains(string(next), "固定回答「请向组织者查询。」") || !strings.Contains(string(next), "Use the mandatory reply text.") {
		t.Fatal("validated template or real reason missing from routing feedback")
	}
}
