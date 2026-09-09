package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/langfuse"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const toolFinishCheck = "finish_check"
const finishCheckTimeout = 12 * time.Second

type finishWorkCheck struct {
	ActionRef    string `json:"action_ref"`
	Deliverables string `json:"deliverables"`
}

type finishCheckResult struct {
	RequestQuoteRef   string            `json:"request_quote_ref"`
	CandidateQuoteRef string            `json:"candidate_quote_ref"`
	WorkChecks        []finishWorkCheck `json:"work_checks"`
	ConstraintQuote   string            `json:"constraint_quote,omitempty"`
	RequestQuote      string            `json:"request_quote"`
	CandidateQuote    string            `json:"candidate_quote"`
	Verdict           string            `json:"verdict"`
	Reason            string            `json:"reason"`
	MissingSourceRefs []string          `json:"missing_source_refs"`
}

func finishCheckTool(action Action, quotes finishQuoteOptions, workRefs ...string) openai.ChatCompletionToolUnionParam {
	description := "Review a proposed reply or silence. Compare the actual request with the quoted candidate and supplied evidence. Reject unsupported business answers, unhandled work or violated restrictions; status replies need no report formatting. Do not answer the business question."
	if action == ActionIssue {
		description = "Authorize or reject STARTING the proposed work plan. Host will execute each work action.purpose after allow. Compare its planned actions with the actual request and full authorization limits. A lookup plan is valid before the answer exists; do not require research results or a final business reply now."
	}
	actionRefSchema := map[string]any{"type": "string", "description": "Copy a Host work action_ref. Non-work actions have no work check."}
	if len(workRefs) > 0 {
		actionRefSchema["enum"] = workRefs
	}
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        toolFinishCheck,
		Description: openai.String(description),
		Parameters: shared.FunctionParameters{"type": "object", "additionalProperties": false,
			"required": []string{"request_quote_ref", "candidate_quote_ref", "verdict", "reason", "missing_source_refs", "work_checks"},
			"properties": map[string]any{
				"work_checks":         map[string]any{"type": "array", "maxItems": WindowPlanMaxItems, "description": "Exactly one entry per candidate start_work/continue_work, no entries for other kinds. Classify independent deliverables within EACH action, not number of source_refs. Empty for non-work-only candidates.", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action_ref", "deliverables"}, "properties": map[string]any{"action_ref": actionRefSchema, "deliverables": map[string]any{"type": "string", "enum": []string{"single", "multiple", "none"}, "description": "single: one output, possibly several steps; multiple: unrelated independently executable goals bundled in one action; none: no executable deliverable."}}}},
				"constraint_quote":    map[string]any{"type": "string", "maxLength": 200, "description": "Optional verbatim evidence of an applicable authorization/scope/privacy boundary (only used as repair feedback on revise): quote at most 200 characters verbatim from job_policy or current_window. Host validates this before showing the missing boundary to the Coordinator; no paraphrase or invented rule."},
				"request_quote_ref":   map[string]any{"type": "string", "enum": finishQuoteRefs(quotes.Requests), "description": "Select a Host request quote option qN. The option is evidence only; read the entire current_window for all intents and constraints. Do not transcribe text."},
				"candidate_quote_ref": map[string]any{"type": "string", "enum": finishQuoteRefs(quotes.Candidates), "description": "Select a Host candidate quote option cN for the action you compared. Host binds its exact original text; do not transcribe or escape it."},
				"verdict":             map[string]any{"type": "string", "enum": []string{"allow", "revise"}},
				"reason":              map[string]any{"type": "string", "maxLength": 160, "description": "One short clause naming the material defect or reason to allow. At most 160 characters. No report, rule recital, business answer or formatting advice."},
				"missing_source_refs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Current uN refs whose requested work or required clarification is not covered; empty for allow."},
			}},
	})
}

// finishReadEvidence excludes candidate answers and prior review hints: neither
// is independent evidence. Full working policy is supplied once, separately.
func finishReadEvidence(turn Turn) []map[string]any {
	out := make([]map[string]any, 0, len(turn.CoordinationReads))
	for _, read := range turn.CoordinationReads {
		entry := map[string]any{"read_ref": read.ReadRef, "tool": read.Tool, "result": read.Result}
		if read.Failed {
			entry["failed"] = true
		}
		out = append(out, entry)
	}
	return out
}

func needsFinishCheck(turn Turn, decision Decision) bool {
	return decision.Action == ActionReply || decision.Action == ActionSilence || decision.Action == ActionIssue
}

func (c *Coordinator) checkFinish(ctx context.Context, turn Turn, decision Decision, messages []openai.ChatCompletionMessageParamUnion, round int, cache map[string]finishCheckResult) (finishCheckResult, error) {
	if !needsFinishCheck(turn, decision) {
		return finishCheckResult{Verdict: "allow"}, nil
	}
	record := func(result finishCheckResult, cached bool, err error) {
		logFinishCheck(turn, round, result, cached, err)
		if lt := langfuse.TraceFromContext(ctx); lt != nil {
			lt.AddMetadata(map[string]any{"finish_check_verdict": result.Verdict, "finish_check_candidate_action": decision.Action, "finish_check_error": err != nil})
			end := langfuse.EndOptions{Output: result, Err: err}
			if err == nil && result.Verdict == "revise" {
				end.Level = langfuse.LevelWarning
			}
			lt.Event(langfuse.ObservationOptions{Type: langfuse.TypeTool, Name: toolFinishCheck, Metadata: map[string]any{"routing_round": round + 1, "cache_hit": cached, "candidate_action": decision.Action}}, end)
		}
	}
	if turn.InstructionsUnavailable {
		err := fmt.Errorf("finish check requires the complete Agent instruction snapshot")
		record(finishCheckResult{}, false, err)
		return finishCheckResult{}, err
	}
	policy := coordinatorFinishPolicy(turn)
	skills := formatSkillSnapshots(turn.Skills)
	shownSkills := 0
	if skills != "" {
		shownSkills = len(strings.Split(skills, "\n"))
	}
	skillsStatus := promptContextState(turn.SkillsStatus, skills != "", false)
	input := map[string]any{
		"proactive_conversation": turn.ProactiveConversation,
		"source":                 turn.Source, "chat_type": turn.ChatType, "conversation_id": turn.ConversationID, "addressed": turn.Addressed,
		"employee_account_name": turn.EmployeeAccountName, "employee_uid": turn.DWSUID,
		"agent_name": firstNonEmpty(turn.EmployeeAccountName, turn.AgentName), "agent_config_label": turn.AgentName, "persona": clipRunes(strings.TrimSpace(turn.Persona), personaBudget),
		"persona_truncated":    utf8.RuneCountInString(strings.TrimSpace(turn.Persona)) > personaBudget,
		"skills":               map[string]any{"status": skillsStatus, "snapshot": skills, "scope": "installed_catalog_snapshot", "shown": shownSkills, "supplied": len(turn.Skills), "catalog_complete": shownSkills == len(turn.Skills) && skillsStatus == "loaded", "descriptions": "bounded, not full skill instructions"},
		"job_policy":           policy,
		"coordinator_contract": coordinatorContractMetadata(turn),
		"history_status":       turn.HistoryStatus, "history_before": turn.HistoryBefore,
		"scene_memory_status": turn.SceneMemoryStatus, "scene_memory_revision": turn.SceneMemoryRevision, "scene_memory": turn.SceneMemory,
		"read_evidence":             finishReadEvidence(turn),
		"reply_delivery_guarantees": "Work replies are delivered only after ALL work items are committed and tasks queued. Acceptance/queued acknowledgements are then true. This does not prove execution completed, business results, or external delivery. A clarify question handles its request for this window; the user answers in a later window.",
	}
	if turn.ProactiveConversation {
		input["reply_delivery_guarantees"] = "Work acceptance is sent only after every work item is durably stored: new execution is queued; additions to a busy Issue wait in its durable follow-up queue. Neither state proves running, completion or external delivery. Unmentioned requests may be handled within the employee role. Avoid acknowledging every line."
	}
	if turn.Loop == LoopTaskFinished {
		input["outstanding_follow_ups"] = turn.OutstandingFollowUps
		input["current_task_result"] = turn.TaskResult
		input["current_result_ref"] = currentResultRef(turn)
		input["task_delivery_context"] = turn.TaskDeliveryContext
	}

	// Explicit refs are Host-owned positions in this frozen window, not ids the
	// reviewing model can invent or import from old conversation history.
	refs := make([]string, len(windowUtterances(turn)))
	for i := range refs {
		refs[i] = fmt.Sprintf("u%d", i+1)
	}
	body, err := json.Marshal(input)
	if err != nil {
		return finishCheckResult{}, fmt.Errorf("encode finish check: %w", err)
	}
	// Show one model-facing representation. The old internal work/non-work
	// projection is only message-granular and contradicts mixed intents in u1.
	type reviewAction struct {
		CoordinationAction
		ActionRef string `json:"action_ref"`
	}
	actions := make([]reviewAction, len(decision.CoordinationActions))
	for i, a := range decision.CoordinationActions {
		actions[i] = reviewAction{CoordinationAction: a, ActionRef: fmt.Sprintf("a%d", i+1)}
	}
	if len(actions) == 0 {
		return finishCheckResult{}, fmt.Errorf("finish review requires explicit coordination actions")
	}

	itemIndex := 0
	for i := range actions {
		if actions[i].Kind != "start_work" && actions[i].Kind != "continue_work" {
			continue
		}
		if itemIndex >= len(decision.Items) {
			return finishCheckResult{}, fmt.Errorf("work action has no Host work item")
		}
		item := decision.Items[itemIndex]
		actions[i].Purpose, actions[i].Context = item.Purpose, item.LookInto
		itemIndex++
	}
	if itemIndex != len(decision.Items) {
		return finishCheckResult{}, fmt.Errorf("Host work item has no coordination action")
	}

	window := make([]map[string]any, 0, len(refs))
	for i, utterance := range windowUtterances(turn) {
		window = append(window, map[string]any{"source_ref": refs[i], "text": utterance.Text,
			"sender": utterance.Sender, "sender_id": utterance.SenderID, "evidence_id": utterance.EvidenceID,
			"timestamp": utterance.Timestamp, "mentions": utterance.Mentions, "mention_relation": mentionRelation(turn, utterance), "reply_to_sender_id": utterance.ReplyToSenderID, "reply_to_evidence_id": utterance.ReplyToEvidenceID, "quoted_context": utterance.ReplyToContent})
	}
	workRefs := finishWorkActionRefs(decision)
	mixedActions := len(workRefs) > 0 && len(workRefs) < len(decision.CoordinationActions)
	mode := "conversation_result"
	if decision.Action == ActionIssue {
		mode = "work_plan_authorization"
	}
	if mixedActions {
		mode = "mixed_coordination_actions"
	}
	// Put the exact proposal after the long background policy. Use the same
	// lower-case work fields the routing schema exposes, rather than Go names.
	quotes := finishQuotes(turn, decision)
	proposal, err := json.Marshal(map[string]any{
		"review_mode": mode, "current_window": window, "source_refs": refs,
		"candidate": map[string]any{"actions": actions}, "quote_options": quotes,
	})
	if err != nil {
		return finishCheckResult{}, fmt.Errorf("encode finish proposal: %w", err)
	}
	reviewTurn := Turn{Loop: LoopFinishCheck, FinishCheckAction: decision.Action, FinishCheckMixedActions: mixedActions}
	if turn.Loop != LoopTaskFinished {
		reviewTurn.Source, reviewTurn.ChatType = turn.Source, turn.ChatType
	}
	system := buildSystemPrompt(reviewTurn)
	key := policyHash(system + "\n" + string(body) + "\n" + string(proposal))
	if cached, ok := cache[key]; ok {
		record(cached, true, nil)
		return cached, nil
	}
	checkMessages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system), openai.UserMessage(string(body)), openai.UserMessage(string(proposal))}
	checkCtx, cancel := context.WithTimeout(ctx, finishCheckTimeout)
	defer cancel()
	lt := langfuse.TraceFromContext(ctx)
	manifest := policyManifest(reviewTurn)
	reviewTool := finishCheckTool(decision.Action, quotes, workRefs...)
	if mixedActions {
		reviewTool.OfFunction.Function.Description = openai.String("Review every candidate action using its corresponding policy: apply finish_check to non-work responses and finish_check_work to planned work. Require both evidence-backed responses and authorized work scope; future work outputs need not exist yet. Return one verdict for the full mixed proposal.")
	}
	// A malformed review is a repairable model protocol error. Retry it once
	// in the same isolated context and existing deadline, without rerouting or
	// changing the proposal. Transport failures still fail closed immediately.
	for attempt := 0; attempt < 2; attempt++ {
		var generation *langfuse.Observation
		if lt != nil {
			name := fmt.Sprintf("coordinator.finish_check.%d", round+1)
			if attempt > 0 {
				name += ".repair"
			}
			generation = lt.StartObservation(langfuse.ObservationOptions{
				Type: langfuse.TypeGeneration, Name: name, Model: coordinatorModel,
				Input: checkMessages, ModelParameters: map[string]any{"max_completion_tokens": 768, "temperature": 0, "tool_choice": "required", "timeout_ms": finishCheckTimeout.Milliseconds()},
				Metadata: map[string]any{"finish_check_policy_version": manifest.PolicyVersion, "finish_check_prompt_hash": manifest.PromptHash, "finish_check_modules": manifest.Modules, "job_policy_sha256": policy["sha256"], "job_policy_kind": policy["kind"], "coordinator_contract": coordinatorContractMetadata(turn), "protocol_attempt": attempt + 1},
			})
		}
		completion, callErr := c.completeWithLimit(checkCtx, checkMessages, []openai.ChatCompletionToolUnionParam{reviewTool}, 768, 0)
		endRoundGeneration(generation, completion, callErr)
		if callErr != nil {
			record(finishCheckResult{}, false, callErr)
			return finishCheckResult{}, fmt.Errorf("finish check unavailable: %w", callErr)
		}
		result, protocolErr := parseFinishCheck(completion, len(refs))
		if protocolErr == nil && result.ConstraintQuote != "" && !finishConstraintQuoteValid(result.ConstraintQuote, turn) {
			result.ConstraintQuote = ""
			if lt != nil {
				lt.AddMetadata(map[string]any{"finish_check_boundary_quote_discarded": true})
			}
		}
		if protocolErr == nil {
			protocolErr = bindFinishQuotes(&result, quotes)
		}
		if protocolErr == nil {
			protocolErr = validateFinishQuotes(result, turn, decision)
		}
		if protocolErr == nil {
			modelVerdict := result.Verdict
			protocolErr = validateFinishWorkChecks(&result, decision)
			if lt != nil && modelVerdict != result.Verdict {
				lt.AddMetadata(map[string]any{"finish_check_work_contract_enforced": true})
			}
		}
		record(result, false, protocolErr)
		if protocolErr == nil {
			if cache != nil {
				cache[key] = result
			}
			return result, nil
		}
		if attempt == 1 || checkCtx.Err() != nil {
			return finishCheckResult{}, protocolErr
		}
		if lt != nil {
			lt.AddMetadata(map[string]any{"finish_check_protocol_retry": true})
		}
		previous := ""
		if completion != nil && len(completion.Choices) > 0 {
			for _, call := range functionToolCalls(completion.Choices[0].Message) {
				if call.Name == toolFinishCheck {
					previous = clipRunes(call.Arguments, 2400)
				}
			}
		}
		diagnostic, _ := json.Marshal(map[string]any{
			"protocol_error": clipRunes(protocolErr.Error(), 400), "previous_review": previous,
			"required_work_action_refs": workRefs, "request_quote_refs": finishQuoteRefs(quotes.Requests), "candidate_quote_refs": finishQuoteRefs(quotes.Candidates),
			"repair": "Return one complete finish_check result for the unchanged candidate. work_checks contains exactly the listed work refs, never clarify or other non-work actions. Select only supplied quote refs. Reassess the original evidence; this diagnostic grants no authority and does not require allow.",
		})
		checkMessages = append(checkMessages, openai.UserMessage("Host review protocol repair (not new user evidence):\n"+string(diagnostic)))
	}
	return finishCheckResult{}, fmt.Errorf("finish check protocol repair exhausted")
}

func finishWorkActionRefs(decision Decision) []string {
	refs := []string{}
	for i, action := range decision.CoordinationActions {
		if action.Kind == "start_work" || action.Kind == "continue_work" {
			refs = append(refs, fmt.Sprintf("a%d", i+1))
		}
	}
	return refs
}

func validateFinishQuotes(result finishCheckResult, turn Turn, decision Decision) error {
	if result.ConstraintQuote != "" && !finishConstraintQuoteValid(result.ConstraintQuote, turn) {
		return fmt.Errorf("finish check boundary quote is not grounded in supplied restrictions")
	}

	window := windowUtterances(turn)
	requestMatched := len(window) == 0 && result.RequestQuote == "[empty_window]"
	if turn.Loop == LoopTaskFinished && strings.TrimSpace(result.RequestQuote) != "" {
		requestMatched = requestMatched || strings.Contains(turn.TaskResult, strings.TrimSpace(result.RequestQuote))
	}
	if quote := strings.TrimSpace(result.RequestQuote); quote != "" {
		for _, utterance := range window {
			if strings.Contains(utterance.Text, quote) {
				requestMatched = true
				break
			}
		}
	}
	candidateMatched := decision.Action == ActionSilence && decision.UserText == "" && result.CandidateQuote == "[silence]"
	if quote := strings.TrimSpace(result.CandidateQuote); quote != "" {
		candidateMatched = candidateMatched || strings.Contains(decision.UserText, quote)
		for _, item := range decision.Items {
			if strings.Contains(item.Purpose, quote) {
				candidateMatched = true
				break
			}
		}
	}
	if !requestMatched || !candidateMatched {
		return fmt.Errorf("finish check did not ground its comparison in the actual request and candidate")
	}
	return nil
}

func parseFinishCheck(completion *openai.ChatCompletion, sourceCount int) (finishCheckResult, error) {
	var result finishCheckResult
	if completion == nil || len(completion.Choices) != 1 {
		return result, fmt.Errorf("finish check needs one result")
	}
	if completion.Choices[0].FinishReason == "length" {
		return result, fmt.Errorf("finish check output was truncated")
	}
	calls := functionToolCalls(completion.Choices[0].Message)
	if len(calls) != 1 || calls[0].Name != toolFinishCheck {
		return result, fmt.Errorf("finish check did not call its validation tool")
	}
	var wireFields map[string]json.RawMessage
	if json.Unmarshal([]byte(calls[0].Arguments), &wireFields) == nil {
		if _, ok := wireFields["request_quote"]; ok {
			return result, fmt.Errorf("select request_quote_ref instead of transcribing request_quote")
		}
		if _, ok := wireFields["candidate_quote"]; ok {
			return result, fmt.Errorf("select candidate_quote_ref instead of transcribing candidate_quote")
		}
	}
	decoder := json.NewDecoder(strings.NewReader(calls[0].Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("invalid finish check: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return result, fmt.Errorf("finish check must contain one JSON object")
	}
	if (result.Verdict != "allow" && result.Verdict != "revise") || strings.TrimSpace(result.Reason) == "" || result.MissingSourceRefs == nil || result.WorkChecks == nil || result.RequestQuoteRef == "" || result.CandidateQuoteRef == "" {
		return result, fmt.Errorf("finish check needs a verdict and reason")
	}
	for _, ref := range result.MissingSourceRefs {
		found := false
		for i := 1; i <= sourceCount; i++ {
			if ref == fmt.Sprintf("u%d", i) {
				found = true
				break
			}
		}
		if !found {
			return result, fmt.Errorf("finish check referenced unknown source %q", ref)
		}
	}
	if result.Verdict == "allow" && len(result.MissingSourceRefs) != 0 {
		return result, fmt.Errorf("finish check allowed uncovered work")
	}
	return result, nil
}

func logFinishCheck(turn Turn, round int, result finishCheckResult, cached bool, err error) {
	slog.Info("inbound coordinator finish checked", append(coordinatorLogIndex(turn),
		"event", "inbound_coordinator_finish_check", "round", round+1, "verdict", result.Verdict,
		"reason", clipRunes(result.Reason, 800), "missing_source_refs", result.MissingSourceRefs, "work_checks", result.WorkChecks,
		"cache_hit", cached, "error", err != nil)...)
}

func finishConstraintQuoteValid(quote string, turn Turn) bool {
	if strings.TrimSpace(quote) == "" || utf8.RuneCountInString(quote) > 200 {
		return false
	}
	if strings.Contains(coordinationConstraintText(turn), quote) {
		return true
	}
	for _, u := range windowUtterances(turn) {
		if strings.Contains(u.Text, quote) {
			return true
		}
	}
	return false
}

// The model assesses semantic independence explicitly; Host enforces the
// one-deliverable action contract rather than interpreting prose in reason.
func validateFinishWorkChecks(result *finishCheckResult, decision Decision) error {
	expected := map[string]bool{}
	for i, a := range decision.CoordinationActions {
		if a.Kind == "start_work" || a.Kind == "continue_work" {
			expected[fmt.Sprintf("a%d", i+1)] = true
		}
	}
	seen := map[string]bool{}
	for _, check := range result.WorkChecks {
		if !expected[check.ActionRef] || seen[check.ActionRef] || !oneOf(check.Deliverables, "single", "multiple", "none") {
			return fmt.Errorf("finish check has invalid or duplicate work action reference")
		}
		seen[check.ActionRef] = true
		if result.Verdict == "allow" && check.Deliverables != "single" {
			result.Verdict = "revise"
			if check.Deliverables == "multiple" {
				result.Reason = "Work action " + check.ActionRef + " bundles independent deliverables; split them into separate work actions while retaining the other valid actions."
			} else {
				result.Reason = "Work action " + check.ActionRef + " has no executable deliverable; repair its scope or use the appropriate non-work action."
			}
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("finish check did not assess every work action")
	}
	return nil
}
