package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	openai "github.com/openai/openai-go/v3"
)

const scriptedRequestQuoteRef = "[scripted_request_quote_ref]"
const scriptedCandidateQuoteRef = "[scripted_candidate_quote_ref]"
const scriptedWorkChecks = "[scripted_work_checks]"

func scriptedFinishVerdict(verdict, reason string, refs ...string) openai.ChatCompletion {
	if refs == nil {
		refs = []string{}
	}
	raw, _ := json.Marshal(map[string]any{"verdict": verdict, "reason": reason, "missing_source_refs": refs, "request_quote_ref": scriptedRequestQuoteRef, "candidate_quote_ref": scriptedCandidateQuoteRef, "work_checks": scriptedWorkChecks})
	return assistantTool("review", "finish_check", string(raw))
}

// Only synthetic markers are filled. Missing, blank or fabricated quotes in
// explicit malformed responses remain unchanged so Host tests cannot mask them.
func scriptedReferenceEnums(parameters map[string]any) (string, string) {
	props, _ := parameters["properties"].(map[string]any)
	first := func(name string) string {
		field, _ := props[name].(map[string]any)
		values := stringSlice(field["enum"])
		if len(values) > 0 {
			return values[0]
		}
		return ""
	}
	return first("request_quote_ref"), first("candidate_quote_ref")
}

func withScriptedFinishReferences(response openai.ChatCompletion, proposal, requestRef, candidateRef string) openai.ChatCompletion {
	if len(response.Choices) != 1 {
		return response
	}
	calls := functionToolCalls(response.Choices[0].Message)
	if len(calls) != 1 || calls[0].Name != toolFinishCheck {
		return response
	}
	var result map[string]any
	if json.Unmarshal([]byte(calls[0].Arguments), &result) != nil {
		return response
	}
	var input struct {
		Candidate struct {
			Actions []struct {
				CoordinationAction
				ActionRef string `json:"action_ref"`
			} `json:"actions"`
		} `json:"candidate"`
	}
	if json.Unmarshal([]byte(proposal), &input) != nil {
		return response
	}
	if result["work_checks"] == scriptedWorkChecks {
		checks := []map[string]string{}
		for _, action := range input.Candidate.Actions {
			if action.Kind == "start_work" || action.Kind == "continue_work" {
				checks = append(checks, map[string]string{"action_ref": action.ActionRef, "deliverables": "single"})
			}
		}
		result["work_checks"] = checks
	}
	if result["request_quote_ref"] == scriptedRequestQuoteRef {
		result["request_quote_ref"] = requestRef
	}
	if result["candidate_quote_ref"] == scriptedCandidateQuoteRef {
		result["candidate_quote_ref"] = candidateRef
	}
	raw, _ := json.Marshal(result)
	return assistantTool(calls[0].ID, toolFinishCheck, string(raw))
}

func withScriptedFinishRequest(response openai.ChatCompletion, params openai.ChatCompletionNewParams) openai.ChatCompletion {
	if len(params.Messages) == 0 {
		return response
	}
	var last struct{ Content string }
	for i := len(params.Messages) - 1; i >= 0; i-- {
		raw, _ := json.Marshal(params.Messages[i])
		_ = json.Unmarshal(raw, &last)
		var proposal map[string]json.RawMessage
		if json.Unmarshal([]byte(last.Content), &proposal) == nil && proposal["candidate"] != nil {
			break
		}
	}
	if len(params.Tools) == 0 || params.Tools[0].GetFunction() == nil {
		return response
	}
	requestRef, candidateRef := scriptedReferenceEnums(params.Tools[0].GetFunction().Parameters)
	return withScriptedFinishReferences(response, last.Content, requestRef, candidateRef)
}

func TestFinishCheckRejectsUnsupportedAnswerBeforeSavingAndRepairsToWork(t *testing.T) {
	chat := &scriptedCompleter{
		rounds: []openai.ChatCompletion{
			assistantTool("bad", toolFinish, `{"actions":[{"kind":"describe_capabilities","source_refs":["u1"],"reply":"只生成一份，系统会自动合并。"}]}`),
			assistantTool("recall", toolAssocRecall, `{}`),
			assistantTool("work", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我来查证听记生成规则。","purpose":"查证主持人与参会人同时开启听记的生成规则","intent":"other"}]}`),
		},
		checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("revise", "No product evidence supports the conclusion; this request requires a lookup task.", "u1"), scriptedFinishVerdict("allow", "The product lookup task respects the working restrictions.")},
	}
	var saved []Decision
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(d Decision) error { saved = append(saved, d); return nil })
	decision, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(ctx, Turn{
		Source: SourceDigitalEmployee, Addressed: true, ConversationID: "cid-current", Message: "主持人和参会人都开启听记会生成几份？", Instructions: "Product answers require checking the current official product evidence.",
	})
	if err != nil || decision.Action != ActionIssue || len(decision.Items) != 1 || decision.Items[0].IssueID != "" {
		t.Fatalf("must repair to a new lookup plan: action=%s items=%d err=%v", decision.Action, len(decision.Items), err)
	}
	if len(saved) != 1 || saved[0].Action != ActionIssue || chat.checkCalls != 2 {
		t.Fatalf("unsupported reply must never be durable: saves=%d checks=%d", len(saved), chat.checkCalls)
	}
	for _, params := range chat.checkParams {
		if strings.Join(toolParamNames(params.Tools), ",") != "finish_check" {
			t.Fatal("semantic reviewer must have no routing or business tools")
		}
	}
}

func TestFinishCheckRepeatedRejectedCandidateNeverFailsOpen(t *testing.T) {
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("revise", "Current work has not been assigned.", "u1")}}
	for i := 0; i < maxLoopRounds; i++ {
		chat.rounds = append(chat.rounds, assistantTool("same", toolFinish, `{"actions":[{"kind":"describe_capabilities","source_refs":["u1"],"reply":"只生成一份。"}]}`))
	}
	saves := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
	d, err := (&Coordinator{Chat: chat}).runLoop(ctx, Turn{Source: SourceDigitalEmployee, Message: "请查证听记规则"})
	if err == nil || d.Action != ActionDeferred || saves != 0 {
		t.Fatalf("repeated rejection must not be saved or accepted: action=%s saves=%d err=%v", d.Action, saves, err)
	}
	if chat.checkCalls != 1 {
		t.Fatalf("same candidate with unchanged evidence should reuse the rejection: checks=%d", chat.checkCalls)
	}
}

func TestFinishCheckFailureDefersWithoutSavingOrRetryingMainModel(t *testing.T) {
	cases := map[string]struct {
		response openai.ChatCompletion
		err      error
	}{
		"transport":               {err: errors.New("review unavailable")},
		"no tool":                 {response: openai.ChatCompletion{}},
		"wrong tool":              {response: assistantTool("bad", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"receipt","reply":"ok"}]}`)},
		"unknown verdict":         {response: assistantTool("bad", "finish_check", `{"verdict":"maybe","reason":"unknown","missing_source_refs":[]}`)},
		"allow with omitted work": {response: scriptedFinishVerdict("allow", "uncovered", "u1")},
		"revise without reason":   {response: scriptedFinishVerdict("revise", "", "u1")},
		"unknown source":          {response: scriptedFinishVerdict("revise", "missing", "u99")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("reply", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"你好。"}]}`)}, checkRounds: []openai.ChatCompletion{tc.response, tc.response}, checkError: tc.err}
			saves := 0
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
			d, err := (&Coordinator{Chat: chat}).runLoop(ctx, Turn{Source: SourceDigitalEmployee, Message: "你好"})
			wantChecks := 2
			if tc.err != nil {
				wantChecks = 1
			}
			if err == nil || d.Action != ActionDeferred || saves != 0 || chat.calls != 1 || chat.checkCalls != wantChecks {
				t.Fatalf("review failure must defer without rerouting: action=%s saves=%d routing_calls=%d checks=%d err=%v", d.Action, saves, chat.calls, chat.checkCalls, err)
			}
		})
	}
}

func TestFinishCheckAllowsConversationAndChecksSilence(t *testing.T) {
	for _, action := range []string{"reply", "silence"} {
		t.Run(action, func(t *testing.T) {
			raw := `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"你好。"}]}`
			if action == "silence" {
				raw = `{"actions":[{"kind":"ignore","source_refs":["u1"],"reason":"当前群聊无需员工回应。"}]}`
			}
			chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("finish", toolFinish, raw)}, checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "No unhandled work or unsupported result.")}}
			saves := 0
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
			d, err := (&Coordinator{Chat: chat}).runLoop(ctx, Turn{Source: SourceDigitalEmployee, ChatType: "group", Message: "你好"})
			if err != nil || string(d.Action) != action || saves != 1 || chat.checkCalls != 1 {
				t.Fatalf("conversation check: action=%s saves=%d checks=%d err=%v", d.Action, saves, chat.checkCalls, err)
			}
		})
	}
}

func TestFinishCheckTaskFinishedReviewsCurrentResult(t *testing.T) {
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("finish", toolFinish, completionFinishJSON(Turn{TaskResult: "查证完成", TaskDeliveryContext: "This result has not been delivered."}, "查询完成，规则链接在结果中。"))}}
	d, err := (&Coordinator{Chat: chat}).runLoop(context.Background(), Turn{Loop: LoopTaskFinished, TaskResult: "查证完成", TaskDeliveryContext: "This result has not been delivered."})
	if err != nil || d.Action != ActionReply || chat.checkCalls != 1 {
		t.Fatalf("task completion must receive its current-result review: action=%s checks=%d err=%v", d.Action, chat.checkCalls, err)
	}
}

func TestFinishCheckHasFullPolicyTailAndRefreshesAfterEvidenceChanges(t *testing.T) {
	constraint := "Only draft. Never send until the user explicitly approves."
	turn := Turn{Source: SourceDigitalEmployee, Message: "给小林写一段通知", Instructions: strings.Repeat("Background context. ", 1000) + constraint}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("revise", "No result evidence.", "u1"), scriptedFinishVerdict("allow", "Host loaded the requested draft.")}}
	coordinator := &Coordinator{Chat: chat}
	cache := map[string]finishCheckResult{}
	candidate := Decision{Action: ActionReply, UserText: "通知内容已准备好。", CoordinationActions: []CoordinationAction{{Kind: "report_status", SourceRefs: []string{"u1"}, StateRefs: []string{"r1"}, Reply: "通知内容已准备好。"}}}
	messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(buildSystemPrompt(turn)), openai.UserMessage(buildUserPrompt(turn))}
	first, err := coordinator.checkFinish(context.Background(), turn, candidate, messages, 0, cache)
	if err != nil || first.Verdict != "revise" {
		t.Fatalf("first check: %#v err=%v", first, err)
	}
	if _, err = coordinator.checkFinish(context.Background(), turn, candidate, messages, 1, cache); err != nil || chat.checkCalls != 1 {
		t.Fatalf("unchanged context cache: calls=%d err=%v", chat.checkCalls, err)
	}
	serialized, _ := json.Marshal(chat.checkParams[0].Messages)
	if !strings.Contains(string(serialized), constraint) {
		t.Fatal("review omitted trailing job authorization restriction")
	}
	turn.CoordinationReads = []CoordinationRead{{ReadRef: "r1", Tool: toolWorkState, Result: json.RawMessage(`{"status":"done","status_source":"issue_database","original_goal":"起草周五三点开会通知"}`)}}
	last, err := coordinator.checkFinish(context.Background(), turn, candidate, messages, 2, cache)
	if err != nil || last.Verdict != "allow" || chat.checkCalls != 2 {
		t.Fatalf("changed evidence must be reviewed again: %#v calls=%d err=%v", last, chat.checkCalls, err)
	}
}

func TestFinishCheckRestrictsWorkPlanBeforeSaveAndAcceptsAuthorizedDraft(t *testing.T) {
	chat := &scriptedCompleter{
		rounds: []openai.ChatCompletion{
			assistantTool("recall", toolAssocRecall, `{}`),
			assistantTool("overreach", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我现在把通知发给同事。","purpose":"向同事发送周五开会通知","intent":"other"}]}`),
			assistantTool("draft", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我来起草通知，等你确认后再发送。","purpose":"只起草周五开会通知，未获批准不能发送","intent":"other"}]}`),
		},
		checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("revise", "The policy and current request authorize drafting only, not sending.", "u1"), scriptedFinishVerdict("allow", "The corrected task is restricted to drafting.")},
	}
	var saved []Decision
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(d Decision) error { saved = append(saved, d); return nil })
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(ctx, Turn{
		Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "帮我给同事起草周五开会通知。",
		Instructions: strings.Repeat("Background information. ", 1000) + "Only draft. Never send until the user explicitly approves.",
	})
	if err != nil || d.Action != ActionIssue || len(saved) != 1 || len(saved[0].Items) != 1 || chat.checkCalls != 2 {
		t.Fatalf("only the reviewed draft plan may be saved: action=%s saves=%d checks=%d err=%v", d.Action, len(saved), chat.checkCalls, err)
	}
	if saved[0].Items[0].Purpose != "用户委托：只起草周五开会通知，未获批准不能发送" || saved[0].UserText != "我来起草通知，等你确认后再发送。" {
		t.Fatal("the unauthorized sending plan was saved")
	}
}

func TestFinishCheckBoundsSkillCatalogAndPersonaWithoutImplyingCompleteness(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "你能做什么？", SkillsStatus: "loaded", Persona: strings.Repeat("Reliable colleague. ", 1000) + "UNEXPOSED_PERSONA_TAIL"}
	for i := 0; i < 60; i++ {
		turn.Skills = append(turn.Skills, SkillSnapshot{Name: fmt.Sprintf("skill-%02d", i), Description: strings.Repeat("Long business capability details. ", 1000) + "UNEXPOSED_SKILL_TAIL"})
	}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Bounded capability overview is accurate.")}}
	_, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, Decision{Action: ActionReply, UserText: "我可以介绍当前已展示的能力。", CoordinationActions: []CoordinationAction{{Kind: "describe_capabilities", SourceRefs: []string{"u1"}, Reply: "我可以介绍当前已展示的能力。"}}}, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(chat.checkParams[0].Messages[1])
	if err != nil {
		t.Fatal(err)
	}
	var message struct{ Content string }
	if err := json.Unmarshal(encoded, &message); err != nil {
		t.Fatal(err)
	}
	var input struct {
		Persona          string `json:"persona"`
		PersonaTruncated bool   `json:"persona_truncated"`
		Skills           struct {
			Status, Snapshot, Scope, Descriptions string
			Shown, Supplied                       int
			CatalogComplete                       bool `json:"catalog_complete"`
		} `json:"skills"`
	}
	if err := json.Unmarshal([]byte(message.Content), &input); err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(input.Persona) > personaBudget || !input.PersonaTruncated || strings.Contains(input.Persona, "UNEXPOSED_PERSONA_TAIL") {
		t.Fatal("finish check leaked unbounded persona text")
	}
	if utf8.RuneCountInString(input.Skills.Snapshot) > skillSnapshotsBudget || input.Skills.Shown <= 0 || input.Skills.Shown > skillSnapshotLimit || input.Skills.Supplied != len(turn.Skills) || input.Skills.CatalogComplete {
		t.Fatalf("bounded catalog completeness is inaccurate: shown=%d supplied=%d complete=%t", input.Skills.Shown, input.Skills.Supplied, input.Skills.CatalogComplete)
	}
	if input.Skills.Status != "loaded" || input.Skills.Scope != "installed_catalog_snapshot" || input.Skills.Descriptions == "" || strings.Contains(input.Skills.Snapshot, "UNEXPOSED_SKILL_TAIL") || strings.Contains(input.Skills.Snapshot, "skill-59") {
		t.Fatal("review must receive a declared bounded catalog, not full instructions or an apparently complete inventory")
	}
}

type finishCheckInspectCompleter func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)

func (f finishCheckInspectCompleter) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	return f(ctx, params)
}

func TestFinishCheckUsesDeterministicBoundedModelRequest(t *testing.T) {
	for _, parentLimit := range []time.Duration{time.Minute, 5 * time.Second} {
		t.Run(parentLimit.String(), func(t *testing.T) {
			var observed bool
			observer := finishCheckInspectCompleter(func(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				observed = true
				body, err := json.Marshal(params)
				if err != nil {
					t.Fatal(err)
				}
				var request map[string]json.RawMessage
				if err = json.Unmarshal(body, &request); err != nil {
					t.Fatal(err)
				}
				if string(request["temperature"]) != "0" {
					t.Fatalf("review temperature must be explicitly zero, got %s", request["temperature"])
				}
				if string(request["max_completion_tokens"]) != "768" {
					t.Fatalf("review output cap must be 768, got %s", request["max_completion_tokens"])
				}
				if names := toolParamNames(params.Tools); len(names) != 1 || names[0] != toolFinishCheck {
					t.Fatal("review request exposed another tool")
				}
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("review has no request deadline")
				}
				wanted := 12 * time.Second
				if parentLimit < wanted {
					wanted = parentLimit
				}
				if remaining := time.Until(deadline); remaining <= wanted-time.Second || remaining > wanted {
					t.Fatalf("actual review deadline exceeds its 12s cap or caller budget: remaining=%s wanted=%s", remaining, wanted)
				}
				response := withScriptedFinishRequest(scriptedFinishVerdict("allow", "The candidate is an accurate conversational response."), params)
				return &response, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), parentLimit)
			defer cancel()
			result, err := (&Coordinator{Chat: observer}).checkFinish(ctx, Turn{Source: SourceDigitalEmployee, Message: "你好"}, Decision{Action: ActionReply, UserText: "你好", CoordinationActions: []CoordinationAction{{Kind: "acknowledge", SourceRefs: []string{"u1"}, AckKind: "greeting", Reply: "你好"}}}, nil, 0, nil)
			if err != nil || result.Verdict != "allow" || !observed {
				t.Fatalf("review request was not completed: verdict=%s observed=%t err=%v", result.Verdict, observed, err)
			}
		})
	}
}

func TestFinishCheckRejectsUnknownQuoteReferencesBeforeSaving(t *testing.T) {
	requestText := "帮我给同事起草周五开会通知。"
	cases := []struct{ name, requestQuote, candidateQuote string }{
		{"unknown request", "q999", scriptedCandidateQuoteRef},
		{"empty request", "", scriptedCandidateQuoteRef},
		{"unknown candidate", scriptedRequestQuoteRef, "c999"},
		{"empty candidate", scriptedRequestQuoteRef, ""},
		{"wrong reference domains", "c1", "q1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"verdict": "allow", "reason": "Scripted quotation validation fixture.", "missing_source_refs": []string{}, "request_quote_ref": tc.requestQuote, "candidate_quote_ref": tc.candidateQuote, "work_checks": scriptedWorkChecks})
			chat := &scriptedCompleter{
				rounds: []openai.ChatCompletion{
					assistantTool("recall", toolAssocRecall, `{}`),
					assistantTool("send", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我现在把通知发给同事。","purpose":"向同事发送周五开会通知","intent":"other"}]}`),
				},
				checkRounds: []openai.ChatCompletion{assistantTool("untrusted-review", toolFinishCheck, string(raw)), assistantTool("untrusted-review-again", toolFinishCheck, string(raw))},
			}
			saves := 0
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
			d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(ctx, Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: requestText, Instructions: "Only draft. Do not send without approval."})
			if err == nil || d.Action != ActionDeferred || saves != 0 || chat.checkCalls != 2 {
				t.Fatalf("ungrounded quotation must not allow any durable plan: action=%s saves=%d checks=%d err=%v", d.Action, saves, chat.checkCalls, err)
			}
		})
	}
}

func TestFinishCheckAcceptsCanonicalEmptyWindowAndSilenceQuotes(t *testing.T) {
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "No input or response is pending.")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), Turn{Source: SourceDigitalEmployee}, Decision{Action: ActionSilence, CoordinationActions: []CoordinationAction{{Kind: "ignore", Reason: "No input is pending."}}}, nil, 0, nil)
	if err != nil || result.Verdict != "allow" {
		t.Fatalf("canonical empty input/silence quotation: verdict=%s err=%v", result.Verdict, err)
	}
}

func TestFinishCheckPassesCandidateActionToPolicyAssembly(t *testing.T) {
	for _, action := range []Action{ActionReply, ActionSilence, ActionIssue} {
		t.Run(string(action), func(t *testing.T) {
			chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Scripted policy selection fixture.")}}
			candidate := Decision{Action: action, UserText: "准备通知草稿", CoordinationActions: []CoordinationAction{{Kind: "acknowledge", SourceRefs: []string{"u1"}, AckKind: "receipt", Reply: "准备通知草稿"}}}
			if action == ActionIssue {
				candidate.CoordinationActions[0].Kind = "start_work"
				candidate.CoordinationActions[0].AckKind = ""
				candidate.CoordinationActions[0].Purpose = "起草周五下午三点会议通知正文"
				candidate.CoordinationActions[0].Intent = "other"
				candidate.Items = []WindowItem{{Purpose: "起草周五下午三点会议通知正文", Intent: "other", SourceRefs: []string{"u1"}}}
			}
			if action == ActionSilence {
				candidate.UserText = ""
				candidate.CoordinationActions = []CoordinationAction{{Kind: "ignore", SourceRefs: []string{"u1"}, Reason: "Policy-selection fixture."}}
			}
			_, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), Turn{Source: SourceDigitalEmployee, Message: "准备通知草稿", Instructions: "Only draft until approved."}, candidate, nil, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(chat.checkParams) != 1 {
				t.Fatalf("expected one review request, got %d", len(chat.checkParams))
			}
			raw, _ := json.Marshal(chat.checkParams[0].Messages[0])
			var system struct{ Content string }
			_ = json.Unmarshal(raw, &system)
			wanted, forbidden := "finish_check", "finish_check_work"
			if action == ActionIssue {
				wanted, forbidden = forbidden, wanted
			}
			if !strings.Contains(system.Content, "[policy:"+wanted+"@") || strings.Contains(system.Content, "[policy:"+forbidden+"@") {
				t.Fatalf("actual %s review request received the wrong policy module", action)
			}
		})
	}
}

func TestFinishCheckRejectsFabricatedConstraintQuotes(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "先起草通知，不要发送。", Instructions: "Only draft. Never send until approved."}
	candidate := Decision{Action: ActionIssue, UserText: "我现在发送通知。"}
	for _, tc := range []struct {
		quote, verdict string
		valid          bool
	}{
		{"Never send until approved.", "revise", true},
		{"不要发送", "revise", true},
		{"The user cannot use any tool.", "revise", false},
		{"Never send until approved.", "allow", true},
		{strings.Repeat("x", 201), "revise", false},
	} {
		result := finishCheckResult{RequestQuote: "先起草通知", CandidateQuote: candidate.UserText, ConstraintQuote: tc.quote, Verdict: tc.verdict}
		err := validateFinishQuotes(result, turn, candidate)
		if (err == nil) != tc.valid {
			t.Fatalf("quote=%q verdict=%s valid=%t err=%v", tc.quote, tc.verdict, tc.valid, err)
		}
	}
}

func TestFinishCheckDiscardsUnusableOptionalBoundaryWithoutDiscardingVerdict(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "先起草通知，不要发送。", Instructions: "Only draft. Never send until approved. " + strings.Repeat("x", 201)}
	for _, tc := range []struct {
		quote, verdict string
		keep           bool
	}{
		{"Never send until approved.", "allow", true},
		{"不要发送", "revise", true},
		{"FORGED_POLICY_SENTINEL", "revise", false},
		{strings.Repeat("x", 201), "revise", false},
	} {
		raw, _ := json.Marshal(map[string]any{"request_quote_ref": scriptedRequestQuoteRef, "candidate_quote_ref": scriptedCandidateQuoteRef, "constraint_quote": tc.quote, "verdict": tc.verdict, "reason": "Only an authorized draft may proceed.", "missing_source_refs": []string{}, "work_checks": scriptedWorkChecks})
		chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{assistantTool("check", toolFinishCheck, string(raw))}}
		result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, Decision{Action: ActionIssue, UserText: "我来起草通知。", CoordinationActions: []CoordinationAction{{Kind: "start_work", SourceRefs: []string{"u1"}, Purpose: "起草周五下午三点会议通知正文", Intent: "other", Reply: "我来起草通知。"}}, Items: []WindowItem{{SourceRefs: []string{"u1"}, Purpose: "起草周五下午三点会议通知正文", Intent: "other"}}}, nil, 0, nil)
		if err != nil || result.Verdict != tc.verdict || (result.ConstraintQuote != "") != tc.keep {
			t.Fatalf("optional evidence handling: verdict=%s quote=%q err=%v", result.Verdict, result.ConstraintQuote, err)
		}
	}
}

func TestFinishCheckRepairSeesRejectedProposalAndSpecificReasonWithoutForgedBoundary(t *testing.T) {
	const bad = `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"起草周五下午三点全员例会通知正文","intent":"other","reply":"已经发给大家了。"}]}`
	const fixed = `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"起草周五下午三点全员例会通知正文","intent":"other","reply":"我来起草周五例会通知。"}]}`
	const reason = "第1项reply虚报发送；保持起草purpose，只将reply改成起草承诺。"
	revise, _ := json.Marshal(map[string]any{"request_quote_ref": scriptedRequestQuoteRef, "candidate_quote_ref": scriptedCandidateQuoteRef, "constraint_quote": "FORGED_POLICY_SENTINEL", "verdict": "revise", "reason": reason, "missing_source_refs": []string{}, "work_checks": scriptedWorkChecks})
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("bad", toolFinish, bad), assistantTool("fixed", toolFinish, fixed)}, checkRounds: []openai.ChatCompletion{assistantTool("review", toolFinishCheck, string(revise)), scriptedFinishVerdict("allow", "The drafting plan and its acknowledgement stay within scope.")}}
	d, err := (&Coordinator{Chat: chat}).runLoop(context.Background(), Turn{Source: SourceWeb, Message: "帮我起草周五三点全员例会通知，先不发送。"})
	if err != nil || d.Action != ActionIssue || chat.calls != 2 || chat.checkCalls != 2 {
		t.Fatalf("repair did not complete: %#v calls=%d checks=%d err=%v", d, chat.calls, chat.checkCalls, err)
	}
	next, _ := json.Marshal(chat.params[1].Messages)
	if !strings.Contains(string(next), reason) || !strings.Contains(string(next), "已经发给大家了。") || strings.Contains(string(next), "FORGED_POLICY_SENTINEL") {
		t.Fatalf("repair lost the real defect or exposed a forged restriction: %s", next)
	}
}

func TestFinishCheckHasOneCanonicalActionProjectionWithHostWorkScope(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, SenderName: "冬翔", Message: "请查证听记生成规则；另外帮我发个消息。"}
	raw := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"查证主持人与参会人听记生成规则","intent":"lookup","context":"只查官方资料。","reply":"我来查证听记规则。"},{"kind":"clarify","source_refs":["u1"],"missing_fields":["recipient","message_body"],"reply":"消息发给谁，内容是什么？"}]}`
	d, err := parseValidatedWindowPlan(raw, turn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Authorized work and a necessary clarification cover the whole message.")}}
	if _, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, d, nil, 0, nil); err != nil {
		t.Fatal(err)
	}
	last, _ := json.Marshal(chat.checkParams[0].Messages[len(chat.checkParams[0].Messages)-1])
	var message struct{ Content string }
	if err := json.Unmarshal(last, &message); err != nil {
		t.Fatal(err)
	}
	var proposal struct {
		Candidate map[string]json.RawMessage `json:"candidate"`
	}
	if err := json.Unmarshal([]byte(message.Content), &proposal); err != nil {
		t.Fatal(err)
	}
	if len(proposal.Candidate) != 1 || proposal.Candidate["actions"] == nil {
		t.Fatalf("duplicate internal representations leaked into model review: %s", message.Content)
	}
	var actions []CoordinationAction
	if err := json.Unmarshal(proposal.Candidate["actions"], &actions); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0].Purpose != d.Items[0].Purpose || actions[0].Context != d.Items[0].LookInto || actions[0].Reply != d.CoordinationActions[0].Reply || actions[1].Kind != "clarify" {
		t.Fatalf("Host work scope or same-source clarification lost: %#v", actions)
	}
	if !strings.HasPrefix(actions[0].Purpose, "冬翔委托：") {
		t.Fatal("review must see the actual Host-bound work purpose")
	}
}

func TestFinishCheckRequiresOneExplicitAtomicityJudgmentPerWorkAction(t *testing.T) {
	turn := Turn{Source: SourceWeb, Message: "请查证听记规则；另发个消息；还要起草周五例会通知。"}
	raw := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"查证主持人与参会人听记生成规则","intent":"lookup","reply":"我来查证听记。"},{"kind":"clarify","source_refs":["u1"],"missing_fields":["recipient","message_body"],"reply":"消息发给谁，内容是什么？"},{"kind":"start_work","source_refs":["u1"],"purpose":"起草周五下午三点全员例会通知正文","intent":"other","reply":"我来起草通知。"}]}`
	candidate, err := parseValidatedWindowPlan(raw, turn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, checks, want string
		wantError          bool
	}{
		{"single per work", `[{"action_ref":"a1","deliverables":"single"},{"action_ref":"a3","deliverables":"single"}]`, "allow", false},
		{"multiple cannot allow", `[{"action_ref":"a1","deliverables":"multiple"},{"action_ref":"a3","deliverables":"single"}]`, "revise", false},
		{"no deliverable cannot allow", `[{"action_ref":"a1","deliverables":"none"},{"action_ref":"a3","deliverables":"single"}]`, "revise", false},
		{"missing second work", `[{"action_ref":"a1","deliverables":"single"}]`, "", true},
		{"empty work checks", `[]`, "", true},
		{"null work checks", `null`, "", true},
		{"nonwork masquerades", `[{"action_ref":"a1","deliverables":"single"},{"action_ref":"a2","deliverables":"single"}]`, "", true},
		{"unknown reference", `[{"action_ref":"a1","deliverables":"single"},{"action_ref":"a99","deliverables":"single"}]`, "", true},
		{"duplicate reference", `[{"action_ref":"a1","deliverables":"single"},{"action_ref":"a1","deliverables":"single"}]`, "", true},
		{"invalid count kind", `[{"action_ref":"a1","deliverables":"maybe"},{"action_ref":"a3","deliverables":"single"}]`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := `{"verdict":"allow","reason":"The requested plan is covered.","request_quote_ref":"q1","candidate_quote_ref":"c1","missing_source_refs":[],"work_checks":` + tc.checks + `}`
			chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{assistantTool("review", toolFinishCheck, reply)}}
			result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil)
			if (err != nil) != tc.wantError || (!tc.wantError && result.Verdict != tc.want) {
				t.Fatalf("verdict=%s reason=%s error=%v", result.Verdict, result.Reason, err)
			}
			if tc.want == "revise" && !strings.Contains(result.Reason, "a1") {
				t.Fatalf("atomicity repair must target its work action: %s", result.Reason)
			}
		})
	}
}

func TestFinishCheckBindsHostQuoteReferencesWithoutRewritingEscapes(t *testing.T) {
	text := "我能处理产品需求：\\n收集与分析。\n也能排查问题。"
	turn := Turn{Source: SourceDigitalEmployee, Message: "你好\\n你能做什么？\n请介绍能力。"}
	candidate := Decision{Action: ActionReply, UserText: text, CoordinationActions: []CoordinationAction{{Kind: "describe_capabilities", SourceRefs: []string{"u1"}, Reply: text}}}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "The capability summary is within scope.")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil)
	if err != nil || result.RequestQuote != turn.Message || result.CandidateQuote != text || result.RequestQuoteRef == "" || result.CandidateQuoteRef == "" {
		t.Fatalf("Host changed literal/newline text during reference binding: %#v err=%v", result, err)
	}
	props := chat.checkParams[0].Tools[0].GetFunction().Parameters["properties"].(map[string]any)
	if props["request_quote"] != nil || props["candidate_quote"] != nil {
		t.Fatal("free-form quotation must not remain in the reviewer schema")
	}
	q, c := scriptedReferenceEnums(chat.checkParams[0].Tools[0].GetFunction().Parameters)
	if result.RequestQuoteRef != q || result.CandidateQuoteRef != c {
		t.Fatal("review did not use exactly the offered reference domains")
	}
}

func TestFinishQuoteOptionsAreBoundedExactSubstringsAndRejectUnknownRefs(t *testing.T) {
	text := strings.Repeat("原文\\n\n", 100)
	turn := Turn{Source: SourceDigitalEmployee, Message: text}
	candidate := Decision{Action: ActionReply, UserText: text, CoordinationActions: []CoordinationAction{{Kind: "describe_capabilities", SourceRefs: []string{"u1"}, Reply: text}}}
	options := finishQuotes(turn, candidate)
	for _, choices := range [][]finishQuoteOption{options.Requests, options.Candidates} {
		if len(choices) == 0 {
			t.Fatal("empty reference domain")
		}
		for _, choice := range choices {
			if utf8.RuneCountInString(choice.Text) > 80 || !strings.Contains(text, choice.Text) {
				t.Fatalf("quote must be an exact bounded source span: %#v", choice)
			}
		}
	}
	for _, refs := range [][2]string{{"q999", options.Candidates[0].Ref}, {options.Requests[0].Ref, "c999"}, {"c1", "q1"}} {
		if err := bindFinishQuotes(&finishCheckResult{RequestQuoteRef: refs[0], CandidateQuoteRef: refs[1]}, options); err == nil {
			t.Fatalf("unknown/cross-domain refs accepted: %v", refs)
		}
	}
}

func TestFinishCheckRejectsFreeQuotationFieldsOnTheWire(t *testing.T) {
	raw := `{"request_quote_ref":"q1","candidate_quote_ref":"c1","request_quote":"fabricated request","candidate_quote":"fabricated result","verdict":"allow","reason":"invalid old wire fields","missing_source_refs":[],"work_checks":[]}`
	response := assistantTool("review", toolFinishCheck, raw)
	if _, err := parseFinishCheck(&response, 1); err == nil {
		t.Fatal("old free-form quotation fields must not be accepted beside finite references")
	}
}

func TestValidateFinishQuotesStillRejectsFabricatedSourceText(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "帮我起草周五会议通知，不要发送。"}
	candidate := Decision{Action: ActionReply, UserText: "我来起草会议通知。", CoordinationActions: []CoordinationAction{{Kind: "acknowledge", Reply: "我来起草会议通知。"}}}
	for _, tc := range []struct {
		request, candidate string
		valid              bool
	}{
		{"帮我起草周五会议通知", "我来起草会议通知。", true},
		{"请立即发送所有资料", "我来起草会议通知。", false},
		{"帮我起草周五会议通知", "已经发给所有人", false},
		{"帮我起草周五会议通知", "帮我起草周五会议通知，不要发送。", false},
		{"", "我来起草会议通知。", false},
	} {
		err := validateFinishQuotes(finishCheckResult{RequestQuote: tc.request, CandidateQuote: tc.candidate, Verdict: "allow"}, turn, candidate)
		if (err == nil) != tc.valid {
			t.Fatalf("original-source validation changed: %#v err=%v", tc, err)
		}
	}
}

func TestFinishCheckMixedActionsReviewBothScopesOnce(t *testing.T) {
	const instructions = "MIXED_REVIEW_POLICY_SENTINEL: Only draft until approved."
	for _, workKind := range []string{"start_work", "continue_work"} {
		t.Run(workKind, func(t *testing.T) {
			turn := Turn{Source: SourceDigitalEmployee, Message: "之前的查询完成了吗？另外起草周五例会通知。", Instructions: instructions}
			candidate := Decision{Action: ActionIssue, UserText: "查询任务仍在进行。\n我来起草通知。", CoordinationActions: []CoordinationAction{
				{Kind: "report_status", SourceRefs: []string{"u1"}, StateRefs: []string{"r1"}, Reply: "查询任务仍在进行。"},
				{Kind: workKind, SourceRefs: []string{"u1"}, Purpose: "起草周五下午三点会议通知正文", Intent: "other", Reply: "我来起草通知。"},
			}, Items: []WindowItem{{Purpose: "起草周五下午三点会议通知正文", Intent: "other", SourceRefs: []string{"u1"}}}}
			chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Both the progress reply and authorized draft are reviewed.")}}
			if _, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil); err != nil {
				t.Fatal(err)
			}
			if chat.checkCalls != 1 || len(chat.checkParams) != 1 {
				t.Fatalf("mixed actions must share one independent review: calls=%d", chat.checkCalls)
			}
			tool := chat.checkParams[0].Tools[0].GetFunction()
			if tool == nil || !strings.Contains(tool.Description.Value, "finish_check to non-work") || !strings.Contains(tool.Description.Value, "finish_check_work to planned work") {
				t.Fatal("mixed review retained a work-only tool contract")
			}
			raw, _ := json.Marshal(chat.checkParams[0].Messages)
			var messages []struct{ Role, Content string }
			if err := json.Unmarshal(raw, &messages); err != nil {
				t.Fatal(err)
			}
			if len(messages) != 3 {
				t.Fatalf("mixed review must retain one system, one background and one proposal: messages=%d", len(messages))
			}
			for _, module := range []string{"core", "finish_check", "finish_check_work"} {
				if strings.Count(messages[0].Content, "[policy:"+module+"@") != 1 {
					t.Fatalf("actual mixed review lacks exactly one %s module", module)
				}
			}
			if strings.Count(string(raw), "MIXED_REVIEW_POLICY_SENTINEL") != 1 {
				t.Fatal("mixed review duplicated or omitted the full working policy")
			}
			var proposal struct {
				Mode      string                                 `json:"review_mode"`
				Candidate struct{ Actions []CoordinationAction } `json:"candidate"`
			}
			if err := json.Unmarshal([]byte(messages[2].Content), &proposal); err != nil {
				t.Fatal(err)
			}
			if proposal.Mode != "mixed_coordination_actions" || len(proposal.Candidate.Actions) != 2 {
				t.Fatal("mixed review lost an action or retained a work-only mode")
			}
		})
	}
}
