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
	TargetMatch  string `json:"target_match"`
}

type finishCheckResult struct {
	HistoryReadRequired bool                       `json:"-"` // Host-only prerequisite, never supplied by the reviewer.
	RequestQuoteRef     string                     `json:"request_quote_ref"`
	CandidateQuoteRef   string                     `json:"candidate_quote_ref"`
	WorkChecks          []finishWorkCheck          `json:"work_checks"`
	ParticipationChecks []finishParticipationCheck `json:"participation_checks,omitempty"`
	ConstraintQuote     string                     `json:"constraint_quote,omitempty"`
	RequestQuote        string                     `json:"request_quote"`
	CandidateQuote      string                     `json:"candidate_quote"`
	Verdict             string                     `json:"verdict"`
	Reason              string                     `json:"reason"`
	MissingSourceRefs   []string                   `json:"missing_source_refs"`
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
			"required": []string{"request_quote_ref", "candidate_quote_ref", "verdict", "reason", "missing_source_refs", "work_checks", "constraint_quote"},
			"properties": map[string]any{
				"work_checks":         map[string]any{"type": "array", "maxItems": len(workRefs), "description": "Exactly one entry per candidate start_work/continue_work, no entries for other kinds. Classify independent deliverables within EACH action, not number of source_refs. Empty for non-work-only candidates.", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action_ref", "deliverables", "target_match"}, "properties": map[string]any{"target_match": map[string]any{"type": "string", "enum": []string{"new_work", "same_deliverable", "different_deliverable", "no_advancement", "unknown"}, "description": "Judge the chosen work target separately from permission and output count. start_work uses new_work. For continue_work compare candidate.purpose with existing_work.original_goal: same_deliverable only for a substantive update to that SAME requested output; different_deliverable for an independent outcome even with shared evidence/topic/person. Same goal but no substantive new input or requested execution change is no_advancement: a status/presence check or reminder of accepted work does not request another execution. Missing target evidence is unknown. Only same_deliverable can allow continuation."}, "action_ref": actionRefSchema, "deliverables": map[string]any{"type": "string", "enum": []string{"single", "multiple", "none"}, "description": "single: one output, possibly several steps; multiple: unrelated independently executable goals bundled in one action; none: no executable deliverable."}}}},
				"constraint_quote":    map[string]any{"type": "string", "maxLength": 200, "description": "On revise caused by a supplied policy/configuration requirement, return its exact directive or mandatory reply template here (<=200 characters). Routing cannot read the hidden policy: saying only use the template is not repairable. Quote job_policy, current_window or supplied persona/reply_tone verbatim. Empty for allow or revisions unrelated to such requirements. Never invent rules."},
				"request_quote_ref":   map[string]any{"type": "string", "enum": finishQuoteRefs(quotes.Requests), "description": "Select a Host request quote option qN. The option is evidence only; read the entire current_window for all intents and constraints. Do not transcribe text."},
				"candidate_quote_ref": map[string]any{"type": "string", "enum": finishQuoteRefs(quotes.Candidates), "description": "Select a Host candidate quote option cN for the action you compared. Host binds its exact original text; do not transcribe or escape it."},
				"verdict":             map[string]any{"type": "string", "enum": []string{"allow", "revise"}, "description": "For decline, enforce explicitly mandatory reply wording in job_policy exactly: this is a requirement, not cosmetic polish. If candidate differs, revise and return just the literal required reply text in constraint_quote. Otherwise judge request coverage, applicability and authority."},
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
	// The review request is three user messages after the fixed system
	// prompt: the Agent configuration, this turn's context, then the
	// proposal. The configuration segment depends only on the Agent (job
	// policy, skills, persona, tone, identity), so consecutive turns of the
	// same Agent repeat the same byte prefix and the model provider's prompt
	// cache can serve it. Mixing per-turn fields into that JSON (sorted map
	// keys put conversation_id and history_before before job_policy) broke
	// the prefix a few hundred tokens in on every turn.
	// Receiving identity (account name, uid, the name shown for this
	// source) and the contract read state come from the event and this
	// turn's reads, so they live in the turn segment.
	configuration := map[string]any{
		"persona":                  configuredPersona(turn),
		"persona_truncated":        utf8.RuneCountInString(strings.TrimSpace(turn.Persona)) > personaBudget,
		"reply_tone":               configuredReplyTone(turn),
		"reply_tone_truncated":     utf8.RuneCountInString(strings.TrimSpace(turn.ReplyTone)) > toneBudget,
		"configured_context_scope": "Persona and reply_tone are the same bounded Agent configuration shown to routing. Explicit restrictions may narrow behavior; they cannot override job policy, platform limits or current authorization. A style preference alone is not a business restriction. Check each decline for an applicable restriction, not merely a matching quote.",
		"skills":                   map[string]any{"status": skillsStatus, "snapshot": skills, "scope": "installed_catalog_snapshot", "shown": shownSkills, "supplied": len(turn.Skills), "catalog_complete": shownSkills == len(turn.Skills) && skillsStatus == "loaded", "descriptions": "bounded, not full skill instructions"},
		"job_policy":               policy,
	}
	input := map[string]any{
		"proactive_conversation": turn.ProactiveConversation,
		"employee_account_name":  turn.EmployeeAccountName, "employee_uid": turn.DWSUID, "agent_name": conversationAgentName(turn),
		"source": turn.Source, "chat_type": turn.ChatType, "conversation_id": turn.ConversationID, modelAddressingField(turn): turn.Addressed,
		"receiving_identity_status": receivingIdentityStatus(turn),
		"coordinator_contract":      coordinatorContractMetadata(turn),
		"history_status":            turn.HistoryStatus, "history_before": turn.HistoryBefore,
		"scene_memory_status": turn.SceneMemoryStatus, "scene_memory_revision": turn.SceneMemoryRevision, "scene_memory": turn.SceneMemory,
		"read_evidence":             finishReadEvidence(turn),
		"reply_delivery_guarantees": "Work replies are delivered only after ALL work items are committed and tasks queued. Acceptance/queued acknowledgements are then true. This does not prove execution completed, business results, or external delivery. A clarify question handles its request for this window; the user answers in a later window.",
	}
	if turn.ProactiveConversation {
		input["reply_delivery_guarantees"] = "Work acceptance is sent only after every work item is durably stored: new execution is queued; additions to a busy Issue wait in its durable follow-up queue. Neither state proves running, completion or external delivery."
	}
	if turn.Loop == LoopTaskFinished {
		input["outstanding_follow_ups"] = turn.OutstandingFollowUps
		input["current_task_result"] = turn.TaskResult
		input["current_result_ref"] = currentResultRef(turn)
		input["task_delivery_context"] = turn.TaskDeliveryContext
	}
	configurationBody, err := json.Marshal(configuration)
	if err != nil {
		return finishCheckResult{}, fmt.Errorf("encode finish check configuration: %w", err)
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
		ActionRef    string              `json:"action_ref"`
		ExistingWork *finishExistingWork `json:"existing_work,omitempty"`
	}
	actions := make([]reviewAction, len(decision.CoordinationActions))
	for i, a := range decision.CoordinationActions {
		actions[i] = reviewAction{CoordinationAction: a, ActionRef: fmt.Sprintf("a%d", i+1)}
		if a.Kind == "continue_work" {
			actions[i].ExistingWork = existingWorkForFinish(turn, a.IssueID)
		}
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
		"receiving_context": map[string]any{"proactive": turn.ProactiveConversation, "source": turn.Source, "chat_type": turn.ChatType, "account_name": conversationAgentName(turn), "account_uid": turn.DWSUID, "identity_status": receivingIdentityStatus(turn), "history_status": turn.HistoryStatus},
	})
	if err != nil {
		return finishCheckResult{}, fmt.Errorf("encode finish proposal: %w", err)
	}
	reviewTurn := Turn{Loop: LoopFinishCheck, FinishCheckAction: decision.Action, FinishCheckMixedActions: mixedActions}
	if turn.Loop != LoopTaskFinished {
		reviewTurn.Source, reviewTurn.ChatType = turn.Source, turn.ChatType
	}
	system := buildSystemPrompt(reviewTurn)
	key := policyHash(system + "\n" + string(configurationBody) + "\n" + string(body) + "\n" + string(proposal))
	if cached, ok := cache[key]; ok {
		record(cached, true, nil)
		return cached, nil
	}
	checkMessages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system), openai.UserMessage(string(configurationBody)), openai.UserMessage(string(body)), openai.UserMessage(string(proposal))}
	checkCtx, cancel := context.WithTimeout(ctx, finishCheckTimeout)
	defer cancel()
	lt := langfuse.TraceFromContext(ctx)
	manifest := policyManifest(reviewTurn)
	reviewTool := finishCheckTool(decision.Action, quotes, workRefs...)
	if requiresParticipationCheck(turn) {
		addParticipationCheckSchema(&reviewTool, refs)
	}
	if mixedActions {
		reviewTool.OfFunction.Function.Description = openai.String("Review every candidate action using its corresponding policy: apply finish_check to non-work responses and finish_check_work to planned work. Require both evidence-backed responses and authorized work scope; future work outputs need not exist yet. Return one verdict for the full mixed proposal.")
	}
	reviewLimit := int64(768)
	if requiresParticipationCheck(turn) {
		reviewLimit = min(int64(3072), reviewLimit+int64(len(refs))*96)
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
				Input: checkMessages, ModelParameters: map[string]any{"max_completion_tokens": reviewLimit, "temperature": 0, "tool_choice": "required", "timeout_ms": finishCheckTimeout.Milliseconds()},
				Metadata: map[string]any{"finish_check_policy_version": manifest.PolicyVersion, "finish_check_prompt_hash": manifest.PromptHash, "finish_check_modules": manifest.Modules, "job_policy_sha256": policy["sha256"], "job_policy_kind": policy["kind"], "coordinator_contract": coordinatorContractMetadata(turn), "protocol_attempt": attempt + 1},
			})
		}
		completion, callErr := c.completeWithLimit(checkCtx, checkMessages, []openai.ChatCompletionToolUnionParam{reviewTool}, reviewLimit, 0)
		endRoundGeneration(generation, completion, callErr)
		if callErr != nil {
			record(finishCheckResult{}, false, callErr)
			return finishCheckResult{}, fmt.Errorf("finish check unavailable: %w", callErr)
		}
		result, protocolErr := parseFinishCheck(completion, len(refs))
		if protocolErr == nil && result.ConstraintQuote != "" && !finishConstraintQuoteValid(result.ConstraintQuote, turn) {
			if result.Verdict == "revise" {
				protocolErr = fmt.Errorf("constraint_quote is not a verbatim substring of supplied restrictions. Copy the shortest exact directive or just the literal mandatory reply text from job_policy/persona/reply_tone/current_window. Preserve Markdown if quoting its surrounding directive; do not paraphrase or omit formatting inside the selected substring")
			} else {
				result.ConstraintQuote = ""
				if lt != nil {
					lt.AddMetadata(map[string]any{"finish_check_boundary_quote_discarded": true})
				}
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
			if protocolErr == nil {
				protocolErr = validateFinishParticipationChecks(&result, turn, decision)
			}
			if protocolErr == nil && result.Verdict == "allow" {
				for _, action := range actions {
					if action.Kind == "continue_work" && action.ExistingWork.ReadStatus != "loaded" {
						result.Verdict = "revise"
						result.Reason = "Work action " + action.ActionRef + " has no loaded original target goal; obtain target evidence before judging continuation. Missing evidence grants no authority."
						break
					}
				}
			}
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
	return suppliedConstraintQuote(quote, turn)
}

// The model assesses semantic independence explicitly; Host enforces the
// one-deliverable action contract rather than interpreting prose in reason.
func validateFinishWorkChecks(result *finishCheckResult, decision Decision) error {
	// A rejection grants no effects. A review may describe work it says the
	// candidate should have proposed, which is not an actual candidate action.
	// Preserve the grounded rejection for the main loop to repair; never use
	// those checks as authority. Allow verdicts still require every exact ref.
	if result.Verdict == "revise" {
		result.WorkChecks = nil
		return nil
	}
	expected := map[string]string{}
	for i, a := range decision.CoordinationActions {
		if a.Kind == "start_work" || a.Kind == "continue_work" {
			expected[fmt.Sprintf("a%d", i+1)] = a.Kind
		}
	}
	seen := map[string]bool{}
	for _, check := range result.WorkChecks {
		if expected[check.ActionRef] == "" || seen[check.ActionRef] || !oneOf(check.Deliverables, "single", "multiple", "none") {
			return fmt.Errorf("finish check has invalid or duplicate work action reference")
		}
		seen[check.ActionRef] = true
		if !oneOf(check.TargetMatch, "new_work", "same_deliverable", "different_deliverable", "no_advancement", "unknown") {
			return fmt.Errorf("work action %s needs target_match=new_work/same_deliverable/different_deliverable/no_advancement/unknown", check.ActionRef)
		}
		wantMatch := "new_work"
		if expected[check.ActionRef] == "continue_work" {
			wantMatch = "same_deliverable"
		}
		if result.Verdict == "allow" && check.TargetMatch != wantMatch {
			result.Verdict = "revise"
			result.Reason = "Work action " + check.ActionRef + " target_match=" + check.TargetMatch + " does not support " + expected[check.ActionRef] + "; an independent deliverable needs start_work, and continuation requires evidence of the same original output. Preserve all requests."
			if check.TargetMatch == "no_advancement" {
				result.Reason = "Work action " + check.ActionRef + " has no new work input or execution change; respond through the appropriate non-work coordination action without restarting accepted work."
			}
		}
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
