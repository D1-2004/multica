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

type finishCheckResult struct {
	RequestQuote      string   `json:"request_quote"`
	CandidateQuote    string   `json:"candidate_quote"`
	Verdict           string   `json:"verdict"`
	Reason            string   `json:"reason"`
	MissingSourceRefs []string `json:"missing_source_refs"`
}

func finishCheckTool(action Action) openai.ChatCompletionToolUnionParam {
	description := "Review a proposed reply or silence. Compare the actual request with the quoted candidate and supplied evidence. Reject unsupported business answers, unhandled work or violated restrictions; status replies need no report formatting. Do not answer the business question."
	if action == ActionIssue {
		description = "Authorize or reject STARTING the proposed work plan. Host will execute item.purpose after allow. Compare its planned actions with the actual request and full authorization limits. A lookup plan is valid before the answer exists; do not require research results or a final business reply now."
	}
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        toolFinishCheck,
		Description: openai.String(description),
		Parameters: shared.FunctionParameters{"type": "object", "additionalProperties": false,
			"required": []string{"request_quote", "candidate_quote", "verdict", "reason", "missing_source_refs"},
			"properties": map[string]any{
				"request_quote":       map[string]any{"type": "string", "description": "First quote the operative request from current_window text verbatim; [empty_window] only for no input."},
				"candidate_quote":     map[string]any{"type": "string", "description": "Then quote the actual commitment/action from candidate.text or an item.purpose verbatim, NOT from original_text. [silence] only for empty silence. Compare this to the request before deciding."},
				"verdict":             map[string]any{"type": "string", "enum": []string{"allow", "revise"}},
				"reason":              map[string]any{"type": "string", "maxLength": 160, "description": "One short clause naming the material defect or reason to allow. At most 160 characters. No report, rule recital, business answer or formatting advice."},
				"missing_source_refs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Current uN refs whose requested work or required clarification is not covered; empty for allow."},
			}},
	})
}

// finishReadEvidence excludes candidate answers and prior review hints: neither
// is independent evidence. Full working policy is supplied once, separately.
func finishReadEvidence(messages []openai.ChatCompletionMessageParamUnion) []map[string]any {
	raw, _ := json.Marshal(messages)
	var decoded []struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	_ = json.Unmarshal(raw, &decoded)
	types := map[string]string{}
	out := []map[string]any{}
	for _, message := range decoded {
		for _, call := range message.ToolCalls {
			switch call.Function.Name {
			case toolAssocRecall, toolIssueGet, toolIssueCommentList:
				types[call.ID] = call.Function.Name
			case toolContextRead:
				var args struct {
					Kind string `json:"kind"`
				}
				if json.Unmarshal([]byte(call.Function.Arguments), &args) == nil && args.Kind == "history" {
					types[call.ID] = call.Function.Name
				}
			}
		}
		if name := types[message.ToolCallID]; message.Role == "tool" && name != "" {
			var text string
			var result any = message.Content
			if json.Unmarshal(message.Content, &text) == nil {
				result = text
				var structured any
				if json.Unmarshal([]byte(text), &structured) == nil {
					result = structured
				}
			}
			out = append(out, map[string]any{"tool": name, "result": result})
		}
	}
	return out
}

func needsFinishCheck(turn Turn, decision Decision) bool {
	return turn.Loop != LoopTaskFinished && (decision.Action == ActionReply || decision.Action == ActionSilence || (decision.Action == ActionIssue && strings.TrimSpace(turn.Instructions) != ""))
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
	policy := strings.TrimSpace(turn.Instructions)
	skills := formatSkillSnapshots(turn.Skills)
	shownSkills := 0
	if skills != "" {
		shownSkills = len(strings.Split(skills, "\n"))
	}
	skillsStatus := promptContextState(turn.SkillsStatus, skills != "", false)
	input := map[string]any{
		"source": turn.Source, "chat_type": turn.ChatType, "conversation_id": turn.ConversationID, "addressed": turn.Addressed,
		"agent_name": turn.AgentName, "persona": clipRunes(strings.TrimSpace(turn.Persona), personaBudget),
		"persona_truncated": utf8.RuneCountInString(strings.TrimSpace(turn.Persona)) > personaBudget,
		"skills":            map[string]any{"status": skillsStatus, "snapshot": skills, "scope": "installed_catalog_snapshot", "shown": shownSkills, "supplied": len(turn.Skills), "catalog_complete": shownSkills == len(turn.Skills) && skillsStatus == "loaded", "descriptions": "bounded, not full skill instructions"},
		"job_policy":        map[string]any{"text": policy, "complete": true, "sha256": policyHash(policy)},
		"history_status":    turn.HistoryStatus, "history_before": turn.HistoryBefore,
		"history": turn.History, "dingtalk_history": turn.DingTalkHistory,
		"scene_memory_status": turn.SceneMemoryStatus, "scene_memory_revision": turn.SceneMemoryRevision, "scene_memory": turn.SceneMemory,
		"read_evidence": finishReadEvidence(messages),
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
	items := make([]map[string]any, 0, len(decision.Items))
	for _, item := range decision.Items {
		items = append(items, map[string]any{
			"source_refs": item.SourceRefs, "purpose": item.Purpose, "intent": item.Intent,
			"basis": item.Basis, "issue_id": item.IssueID, "original_text": item.Content,
			"delegator": item.Delegator, "look_into": item.LookInto,
		})
	}
	window := make([]map[string]any, 0, len(refs))
	for i, utterance := range windowUtterances(turn) {
		window = append(window, map[string]any{"source_ref": refs[i], "text": utterance.Text,
			"sender": utterance.Sender, "sender_id": utterance.SenderID, "evidence_id": utterance.EvidenceID,
			"timestamp": utterance.Timestamp, "quoted_context": utterance.ReplyToContent})
	}
	mode := "conversation_result"
	if decision.Action == ActionIssue {
		mode = "work_plan_authorization"
	}
	// Put the exact proposal after the long background policy. Use the same
	// lower-case work fields the routing schema exposes, rather than Go names.
	proposal, err := json.Marshal(map[string]any{
		"review_mode": mode, "current_window": window, "source_refs": refs,
		"candidate": map[string]any{"action": decision.Action, "text": decision.UserText, "items": items, "non_work_refs": decision.NonWorkRefs},
	})
	if err != nil {
		return finishCheckResult{}, fmt.Errorf("encode finish proposal: %w", err)
	}
	reviewTurn := Turn{Loop: LoopFinishCheck, FinishCheckAction: decision.Action}
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
	var generation *langfuse.Observation
	if lt != nil {
		generation = lt.StartObservation(langfuse.ObservationOptions{
			Type: langfuse.TypeGeneration, Name: fmt.Sprintf("coordinator.finish_check.%d", round+1), Model: coordinatorModel,
			Input: checkMessages, ModelParameters: map[string]any{"max_completion_tokens": 512, "temperature": 0, "tool_choice": "required", "timeout_ms": finishCheckTimeout.Milliseconds()},
			Metadata: map[string]any{"finish_check_policy_version": manifest.PolicyVersion, "finish_check_prompt_hash": manifest.PromptHash, "finish_check_modules": manifest.Modules, "job_policy_sha256": policyHash(policy)},
		})
	}
	completion, err := c.completeWithLimit(checkCtx, checkMessages, []openai.ChatCompletionToolUnionParam{finishCheckTool(decision.Action)}, 512, 0)
	endRoundGeneration(generation, completion, err)
	if err != nil {
		record(finishCheckResult{}, false, err)
		return finishCheckResult{}, fmt.Errorf("finish check unavailable: %w", err)
	}
	result, err := parseFinishCheck(completion, len(refs))
	if err == nil {
		err = validateFinishQuotes(result, turn, decision)
	}
	record(result, false, err)
	if err != nil {
		return finishCheckResult{}, err
	}
	if cache != nil {
		cache[key] = result
	}
	return result, nil
}

func validateFinishQuotes(result finishCheckResult, turn Turn, decision Decision) error {
	window := windowUtterances(turn)
	requestMatched := len(window) == 0 && result.RequestQuote == "[empty_window]"
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
	decoder := json.NewDecoder(strings.NewReader(calls[0].Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("invalid finish check: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return result, fmt.Errorf("finish check must contain one JSON object")
	}
	if (result.Verdict != "allow" && result.Verdict != "revise") || strings.TrimSpace(result.Reason) == "" || result.MissingSourceRefs == nil {
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
		"reason", clipRunes(result.Reason, 800), "missing_source_refs", result.MissingSourceRefs,
		"cache_hit", cached, "error", err != nil)...)
}
