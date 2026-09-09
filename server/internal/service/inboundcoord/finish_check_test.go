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

const scriptedRequestQuote = "[scripted_request_quote]"
const scriptedCandidateQuote = "[scripted_candidate_quote]"

func scriptedFinishVerdict(verdict, reason string, refs ...string) openai.ChatCompletion {
	if refs == nil {
		refs = []string{}
	}
	raw, _ := json.Marshal(map[string]any{"verdict": verdict, "reason": reason, "missing_source_refs": refs, "request_quote": scriptedRequestQuote, "candidate_quote": scriptedCandidateQuote})
	return assistantTool("review", "finish_check", string(raw))
}

// Only synthetic markers are filled. Missing, blank or fabricated quotes in
// explicit malformed responses remain unchanged so Host tests cannot mask them.
func scriptedExactQuote(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return string(runes)
}

func withScriptedFinishQuotes(response openai.ChatCompletion, proposal string) openai.ChatCompletion {
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
		Window    []struct{ Text string } `json:"current_window"`
		Candidate struct {
			Action, Text string
			Items        []struct{ Purpose string }
		} `json:"candidate"`
	}
	if json.Unmarshal([]byte(proposal), &input) != nil {
		return response
	}
	requestQuote := "[empty_window]"
	for _, utterance := range input.Window {
		if text := strings.TrimSpace(utterance.Text); text != "" {
			requestQuote = scriptedExactQuote(text)
			break
		}
	}
	candidateQuote := scriptedExactQuote(input.Candidate.Text)
	if candidateQuote == "" {
		for _, item := range input.Candidate.Items {
			if purpose := strings.TrimSpace(item.Purpose); purpose != "" {
				candidateQuote = scriptedExactQuote(purpose)
				break
			}
		}
	}
	if candidateQuote == "" && input.Candidate.Action == "silence" {
		candidateQuote = "[silence]"
	}
	if result["request_quote"] == scriptedRequestQuote {
		result["request_quote"] = requestQuote
	}
	if result["candidate_quote"] == scriptedCandidateQuote {
		result["candidate_quote"] = candidateQuote
	}
	raw, _ := json.Marshal(result)
	return assistantTool(calls[0].ID, toolFinishCheck, string(raw))
}

func withScriptedFinishRequest(response openai.ChatCompletion, params openai.ChatCompletionNewParams) openai.ChatCompletion {
	if len(params.Messages) == 0 {
		return response
	}
	raw, _ := json.Marshal(params.Messages[len(params.Messages)-1])
	var last struct{ Content string }
	_ = json.Unmarshal(raw, &last)
	return withScriptedFinishQuotes(response, last.Content)
}

func TestFinishCheckRejectsUnsupportedAnswerBeforeSavingAndRepairsToWork(t *testing.T) {
	chat := &scriptedCompleter{
		rounds: []openai.ChatCompletion{
			assistantTool("bad", toolFinish, `{"action":"reply","text":"只生成一份，系统会自动合并。"}`),
			assistantTool("recall", toolAssocRecall, `{}`),
			assistantTool("work", toolFinish, `{"action":"issue","text":"我来查证听记生成规则。","items":[{"source_refs":["u1"],"purpose":"查证主持人与参会人同时开启听记的生成规则","intent":"other","basis":"new_request"}]}`),
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
		chat.rounds = append(chat.rounds, assistantTool("same", toolFinish, `{"action":"reply","text":"只生成一份。"}`))
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
		"wrong tool":              {response: assistantTool("bad", toolFinish, `{"action":"reply","text":"ok"}`)},
		"unknown verdict":         {response: assistantTool("bad", "finish_check", `{"verdict":"maybe","reason":"unknown","missing_source_refs":[]}`)},
		"allow with omitted work": {response: scriptedFinishVerdict("allow", "uncovered", "u1")},
		"revise without reason":   {response: scriptedFinishVerdict("revise", "", "u1")},
		"unknown source":          {response: scriptedFinishVerdict("revise", "missing", "u99")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("reply", toolFinish, `{"action":"reply","text":"你好。"}`)}, checkRounds: []openai.ChatCompletion{tc.response}, checkError: tc.err}
			saves := 0
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
			d, err := (&Coordinator{Chat: chat}).runLoop(ctx, Turn{Source: SourceDigitalEmployee, Message: "你好"})
			if err == nil || d.Action != ActionDeferred || saves != 0 || chat.calls != 1 || chat.checkCalls != 1 {
				t.Fatalf("review failure must defer immediately: action=%s saves=%d routing_calls=%d checks=%d err=%v", d.Action, saves, chat.calls, chat.checkCalls, err)
			}
		})
	}
}

func TestFinishCheckAllowsConversationAndChecksSilence(t *testing.T) {
	for _, action := range []string{"reply", "silence"} {
		t.Run(action, func(t *testing.T) {
			chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("finish", toolFinish, `{"action":"`+action+`","text":"你好。"}`)}, checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "No unhandled work or unsupported result.")}}
			saves := 0
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
			d, err := (&Coordinator{Chat: chat}).runLoop(ctx, Turn{Source: SourceDigitalEmployee, ChatType: "group", Message: "你好"})
			if err != nil || string(d.Action) != action || saves != 1 || chat.checkCalls != 1 {
				t.Fatalf("conversation check: action=%s saves=%d checks=%d err=%v", d.Action, saves, chat.checkCalls, err)
			}
		})
	}
}

func TestFinishCheckTaskFinishedKeepsExistingCompletionPath(t *testing.T) {
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("finish", toolFinish, `{"action":"reply","text":"查询完成，规则链接在结果中。"}`)}, checkError: errors.New("must not be called")}
	d, err := (&Coordinator{Chat: chat}).runLoop(context.Background(), Turn{Loop: LoopTaskFinished, TaskResult: "查证完成", TaskDeliveryContext: "This result has not been delivered."})
	if err != nil || d.Action != ActionReply || chat.checkCalls != 0 {
		t.Fatalf("task completion must retain its separate path: action=%s checks=%d err=%v", d.Action, chat.checkCalls, err)
	}
}

func TestFinishCheckHasFullPolicyTailAndRefreshesAfterEvidenceChanges(t *testing.T) {
	constraint := "Only draft. Never send until the user explicitly approves."
	turn := Turn{Source: SourceDigitalEmployee, Message: "给小林写一段通知", Instructions: strings.Repeat("Background context. ", 1000) + constraint}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("revise", "No result evidence.", "u1"), scriptedFinishVerdict("allow", "Host loaded the requested draft.")}}
	coordinator := &Coordinator{Chat: chat}
	cache := map[string]finishCheckResult{}
	candidate := Decision{Action: ActionReply, UserText: "通知内容已准备好。"}
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
	read := assistantTool("new-host-read", toolIssueGet, `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`)
	messages = append(messages, read.Choices[0].Message.ToParam(), openai.ToolMessage(`{"draft":"周五三点开会","status":"loaded"}`, "new-host-read"))
	last, err := coordinator.checkFinish(context.Background(), turn, candidate, messages, 2, cache)
	if err != nil || last.Verdict != "allow" || chat.checkCalls != 2 {
		t.Fatalf("changed evidence must be reviewed again: %#v calls=%d err=%v", last, chat.checkCalls, err)
	}
}

func TestFinishCheckRestrictsWorkPlanBeforeSaveAndAcceptsAuthorizedDraft(t *testing.T) {
	chat := &scriptedCompleter{
		rounds: []openai.ChatCompletion{
			assistantTool("recall", toolAssocRecall, `{}`),
			assistantTool("overreach", toolFinish, `{"action":"issue","text":"我现在把通知发给同事。","items":[{"source_refs":["u1"],"purpose":"向同事发送周五开会通知","intent":"other","basis":"new_request"}]}`),
			assistantTool("draft", toolFinish, `{"action":"issue","text":"我来起草通知，等你确认后再发送。","items":[{"source_refs":["u1"],"purpose":"只起草周五开会通知，未获批准不能发送","intent":"other","basis":"new_request"}]}`),
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
	_, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, Decision{Action: ActionReply, UserText: "我可以介绍当前已展示的能力。"}, nil, 0, nil)
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
				if string(request["max_completion_tokens"]) != "512" {
					t.Fatalf("review output cap must be 512, got %s", request["max_completion_tokens"])
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
			result, err := (&Coordinator{Chat: observer}).checkFinish(ctx, Turn{Source: SourceDigitalEmployee, Message: "你好"}, Decision{Action: ActionReply, UserText: "你好"}, nil, 0, nil)
			if err != nil || result.Verdict != "allow" || !observed {
				t.Fatalf("review request was not completed: verdict=%s observed=%t err=%v", result.Verdict, observed, err)
			}
		})
	}
}

func TestFinishCheckRejectsUngroundedQuotesBeforeSaving(t *testing.T) {
	requestText := "帮我给同事起草周五开会通知。"
	candidateText := "我现在把通知发给同事。"
	cases := []struct{ name, requestQuote, candidateQuote string }{
		{"fabricated request", "请立刻发送并删除历史记录", candidateText},
		{"empty request", "", candidateText},
		{"fabricated candidate", requestText, "已成功生成一份PDF"},
		{"empty candidate", requestText, ""},
		{"source mistaken for candidate", requestText, requestText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"verdict": "allow", "reason": "Scripted quotation validation fixture.", "missing_source_refs": []string{}, "request_quote": tc.requestQuote, "candidate_quote": tc.candidateQuote})
			chat := &scriptedCompleter{
				rounds: []openai.ChatCompletion{
					assistantTool("recall", toolAssocRecall, `{}`),
					assistantTool("send", toolFinish, `{"action":"issue","text":"我现在把通知发给同事。","items":[{"source_refs":["u1"],"purpose":"向同事发送周五开会通知","intent":"other","basis":"new_request"}]}`),
				},
				checkRounds: []openai.ChatCompletion{assistantTool("untrusted-review", toolFinishCheck, string(raw))},
			}
			saves := 0
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
			d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(ctx, Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: requestText, Instructions: "Only draft. Do not send without approval."})
			if err == nil || d.Action != ActionDeferred || saves != 0 || chat.checkCalls != 1 {
				t.Fatalf("ungrounded quotation must not allow any durable plan: action=%s saves=%d checks=%d err=%v", d.Action, saves, chat.checkCalls, err)
			}
		})
	}
}

func TestFinishCheckAcceptsCanonicalEmptyWindowAndSilenceQuotes(t *testing.T) {
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "No input or response is pending.")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), Turn{Source: SourceDigitalEmployee}, Decision{Action: ActionSilence}, nil, 0, nil)
	if err != nil || result.Verdict != "allow" {
		t.Fatalf("canonical empty input/silence quotation: verdict=%s err=%v", result.Verdict, err)
	}
}

func TestFinishCheckPassesCandidateActionToPolicyAssembly(t *testing.T) {
	for _, action := range []Action{ActionReply, ActionSilence, ActionIssue} {
		t.Run(string(action), func(t *testing.T) {
			chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Scripted policy selection fixture.")}}
			_, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), Turn{Source: SourceDigitalEmployee, Message: "准备通知草稿", Instructions: "Only draft until approved."}, Decision{Action: action, UserText: "准备通知草稿"}, nil, 0, nil)
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
