package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"

	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/util"
)

// Langfuse instrumentation for the short loop. One inbound turn is one
// Langfuse trace whose id is the coordinator trace id (coord_trace_id in SLS),
// so an id copied from the logs opens the same turn in Langfuse. Each model
// round is a generation, each tool call a tool observation, and every lookup
// key the SLS index exposes is copied as first-level, filterable metadata onto
// every span.
const (
	coordinatorTraceName   = "inbound_coordinator"
	coordinatorTraceTag    = "inbound_coordinator"
	traceOutputTextBudget  = 4000
	traceToolPayloadBudget = 16000
)

func (c *Coordinator) startTurnTrace(ctx context.Context, turn Turn, started time.Time) *langfuse.Trace {
	if c == nil || c.Langfuse == nil {
		return nil
	}
	trace := c.Langfuse.StartTrace(ctx, coordinatorTraceOptions(turn, started))
	trace.Index(coordinatorIndexKeys(turn))
	return trace
}

func coordinatorTraceOptions(turn Turn, started time.Time) langfuse.TraceOptions {
	kind := strings.TrimSpace(turn.Kind)
	if kind == "" {
		kind = strings.TrimSpace(turn.ChatType)
	}
	metadata := map[string]any{
		"loop":              coordinatorTraceName,
		"coord_trace_id":    strings.TrimSpace(turn.TraceID),
		"decision_id":       strings.TrimSpace(turn.UserDecisionRequestID),
		"conversation_id":   strings.TrimSpace(turn.ConversationID),
		"conversation_name": conversationName(turn),
		"conversation_kind": kind,
		"chat_type":         strings.TrimSpace(turn.ChatType),
		"sender_name":       clipRunes(strings.TrimSpace(turn.SenderName), llmLogNameBudget),
		"person_id":         strings.TrimSpace(turn.PersonID),
		"dws_uid":           strings.TrimSpace(turn.DWSUID),
		"dws_org_id":        strings.TrimSpace(turn.DWSOrgID),
		"agent_id":          util.UUIDToString(turn.AgentID),
		"agent_name":        strings.TrimSpace(turn.AgentName),
		"workspace_id":      strings.TrimSpace(turn.WorkspaceID),
		"user_id":           util.UUIDToString(turn.UserID),
		"chat_session_id":   strings.TrimSpace(turn.ChatSessionID),
		"evidence_id":       strings.TrimSpace(turn.EvidenceID),
		"source":            string(turn.Source),
		"model":             turn.modelName(),
		"addressed":         turn.Addressed,
		"busy":              turn.Busy,
	}
	for key, value := range coordinatorContractMetadata(turn) {
		metadata[key] = value
	}
	if turn.SceneMemoryRevision > 0 {
		metadata["scene_memory_revision"] = turn.SceneMemoryRevision
	}
	tags := coordinatorTraceTags(turn)
	// The root input is what the person said; the prompt context the loop saw
	// (history sizes, persona, scene memory) is root metadata so the trace
	// reads as "message in, verdict out" in the Langfuse UI.
	rootMetadata := map[string]any{
		"conversation_title":     clipRunes(strings.TrimSpace(turn.ConversationTitle), llmLogNameBudget),
		"dingtalk_history_count": len(turn.DingTalkHistory),
		"multica_history_count":  len(turn.History),
		"related_tasks":          clipRunes(strings.TrimSpace(turn.RelatedTasks), llmLogFieldBudget),
		"persona":                clipRunes(strings.TrimSpace(turn.Persona), personaBudget),
		"reply_tone":             clipRunes(strings.TrimSpace(turn.ReplyTone), toneBudget),
		"skill_count":            len(turn.Skills),
	}
	if turn.SceneMemoryRevision > 0 {
		rootMetadata["scene_memory"] = clipRunes(strings.TrimSpace(turn.SceneMemory), traceOutputTextBudget)
	}
	return langfuse.TraceOptions{
		TraceID:      turn.TraceID,
		Name:         coordinatorTraceName,
		Type:         langfuse.TypeAgent,
		UserID:       coordinatorTraceUserID(turn),
		SessionID:    coordinatorTraceSessionID(turn),
		Tags:         tags,
		Metadata:     metadata,
		RootMetadata: rootMetadata,
		Input:        clipRunes(strings.TrimSpace(turn.Message), traceOutputTextBudget),
		StartTime:    started,
	}
}

// coordinatorIndexKeys are the ids a reader may hold when looking for this
// turn; each becomes an "idx.<key>.<value>" event (see langfuse.Trace.Index).
// coordinatorIndexKeys are the turn's ids that nothing else makes searchable:
// the trace id is the coord_trace_id, the session is the conversation (or the
// chat session when there is no conversation), and the tags carry the agent,
// the workspace and one user id.
func coordinatorIndexKeys(turn Turn) map[string]string {
	keys := map[string]string{
		"evidence_id": strings.TrimSpace(turn.EvidenceID),
	}
	if strings.TrimSpace(turn.ConversationID) != "" {
		keys["chat_session_id"] = strings.TrimSpace(turn.ChatSessionID)
	}
	tagged := coordinatorTraceUserID(turn)
	for key, value := range map[string]string{
		"person_id": strings.TrimSpace(turn.PersonID),
		"dws_uid":   strings.TrimSpace(turn.DWSUID),
		"user_id":   util.UUIDToString(turn.UserID),
	} {
		if value != "" && value != tagged {
			keys[key] = value
		}
	}
	return keys
}

// coordinatorTraceTags are the static Langfuse tags of a turn; they must be
// known before the loop starts and are repeated by any task joining the trace.
func coordinatorTraceTags(turn Turn) []string {
	kind := strings.TrimSpace(turn.Kind)
	if kind == "" {
		kind = strings.TrimSpace(turn.ChatType)
	}
	tags := []string{coordinatorTraceTag, langfuse.Tag("source", string(turn.Source)), langfuse.Tag("kind", kind)}
	// Ids the Langfuse API can only filter through tags on this deployment.
	tags = append(tags,
		langfuse.Tag("agent", util.UUIDToString(turn.AgentID)),
		langfuse.Tag("agent_name", strings.TrimSpace(turn.AgentName)),
		langfuse.Tag("workspace", turn.WorkspaceID),
		langfuse.Tag("user", coordinatorTraceUserID(turn)),
	)
	out := tags[:0]
	for _, tag := range tags {
		if tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// coordinatorTraceUserID is the Langfuse user: the DingTalk person when the
// turn came from a channel, otherwise the Multica member.
func coordinatorTraceUserID(turn Turn) string {
	for _, candidate := range []string{turn.PersonID, turn.DWSUID, util.UUIDToString(turn.UserID), turn.SenderName} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

// coordinatorTraceSessionID groups turns of one conversation: the DingTalk
// openConversationId, or the web chat session.
func coordinatorTraceSessionID(turn Turn) string {
	if cid := strings.TrimSpace(turn.ConversationID); cid != "" {
		return cid
	}
	return strings.TrimSpace(turn.ChatSessionID)
}

func finishCoordinatorTrace(t *langfuse.Trace, decision Decision, loopErr error) {
	if t == nil {
		return
	}
	// The verdict is metadata, not a tag: Langfuse freezes tags when the
	// first span of the trace arrives, long before the decision exists.
	action := string(decision.Action)
	t.AddMetadata(map[string]any{
		"action":             action,
		"coordination_kinds": decision.CoordinationKinds(),
		"issue_id":           strings.TrimSpace(decision.IssueID),
		"tool_rounds":        decision.ToolRounds,
		"fail_open":          decision.Action == ActionContinue && loopErr != nil,
		"deferred":           decision.Action == ActionDeferred,
	})
	output := map[string]any{
		"action":             action,
		"coordination_kinds": decision.CoordinationKinds(),
		"issue_id":           strings.TrimSpace(decision.IssueID),
		"user_text":          clipRunes(strings.TrimSpace(redactConfigLink(decision.UserText, decision.configLinkURL)), traceOutputTextBudget),
		"look_into":          clipRunes(strings.TrimSpace(decision.LookInto), llmLogFieldBudget),
		"purpose":            clipRunes(strings.TrimSpace(decision.Purpose), llmLogFieldBudget),
		"intent":             strings.TrimSpace(decision.Intent),
		"reason":             clipRunes(strings.TrimSpace(decision.Reason), llmLogFieldBudget),
		"tool_rounds":        decision.ToolRounds,
		"tools_used":         decision.ToolsUsed,
		"elapsed_ms":         decision.ElapsedMs,
	}
	output["coordination_actions"] = redactConfigLinkActions(decision.CoordinationActions, decision.configLinkURL)
	if decision.IssueComment != nil {
		output["issue_comment"] = decision.IssueComment
	}
	t.End(langfuse.EndOptions{Output: output, Err: loopErr})
}

func traceRoundGeneration(t *langfuse.Trace, round int, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam, model string) *langfuse.Observation {
	if t == nil {
		return nil
	}
	params := map[string]any{
		"temperature":           temperature,
		"max_completion_tokens": maxCompletionTokens,
		"reasoning_effort":      "none",
		"enable_thinking":       false,
		"tool_choice":           "required",
		"tools":                 toolParamNames(tools),
	}
	return t.StartObservation(langfuse.ObservationOptions{
		Type:            langfuse.TypeGeneration,
		Name:            fmt.Sprintf("coordinator.round.%d", round+1),
		Model:           model,
		ModelParameters: params,
		Input:           messages,
		Metadata:        map[string]any{"round": round + 1},
	})
}

func endRoundGeneration(gen *langfuse.Observation, completion *openai.ChatCompletion, err error) {
	if gen == nil {
		return
	}
	end := langfuse.EndOptions{Err: err}
	if completion != nil {
		end.Usage = completionUsage(completion)
		if len(completion.Choices) > 0 {
			choice := completion.Choices[0]
			end.Output = completionMessagePayload(choice.Message)
			end.Metadata = map[string]any{"finish_reason": choice.FinishReason}
		}
		if model := strings.TrimSpace(completion.Model); model != "" {
			if end.Metadata == nil {
				end.Metadata = map[string]any{}
			}
			end.Metadata["response_model"] = model
		}
	}
	gen.End(end)
}

func completionUsage(completion *openai.ChatCompletion) *langfuse.Usage {
	if completion == nil {
		return nil
	}
	usage := &langfuse.Usage{
		Input:     completion.Usage.PromptTokens,
		Output:    completion.Usage.CompletionTokens,
		Total:     completion.Usage.TotalTokens,
		CacheRead: completion.Usage.PromptTokensDetails.CachedTokens,
		Reasoning: completion.Usage.CompletionTokensDetails.ReasoningTokens,
	}
	if usage.Input == 0 && usage.Output == 0 && usage.Total == 0 {
		return nil
	}
	return usage
}

// completionMessagePayload prefers the byte-exact upstream JSON and falls
// back to the SDK struct for synthetic completions built in tests.
func completionMessagePayload(msg openai.ChatCompletionMessage) any {
	if raw := strings.TrimSpace(msg.RawJSON()); raw != "" {
		return json.RawMessage(raw)
	}
	return msg
}

func toolParamNames(tools []openai.ChatCompletionToolUnionParam) []string {
	raw, err := json.Marshal(tools)
	if err != nil {
		return nil
	}
	var decoded []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &decoded) != nil {
		return nil
	}
	names := make([]string, 0, len(decoded))
	for _, tool := range decoded {
		if name := strings.TrimSpace(tool.Function.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func traceToolStart(t *langfuse.Trace, round int, call functionCall) *langfuse.Observation {
	if t == nil {
		return nil
	}
	return t.StartObservation(langfuse.ObservationOptions{
		Type:     langfuse.TypeTool,
		Name:     call.Name,
		Input:    toolPayload(call.Arguments),
		Metadata: map[string]any{"round": round + 1, "tool_call_id": call.ID},
	})
}

func traceToolEnd(obs *langfuse.Observation, result string, err error, reason string) {
	if obs == nil {
		return
	}
	end := langfuse.EndOptions{Output: toolPayload(result), Err: err}
	if reason != "" {
		end.Metadata = map[string]any{"reason": reason}
		if err == nil && reason != "terminal" {
			end.StatusMessage = reason
		}
	}
	obs.End(end)
}

// traceToolReject records a tool call the server refused before execution.
func traceToolReject(t *langfuse.Trace, round int, call functionCall, output, reason string) {
	if t == nil {
		return
	}
	t.Event(langfuse.ObservationOptions{
		Type:     langfuse.TypeTool,
		Name:     call.Name,
		Input:    toolPayload(call.Arguments),
		Metadata: map[string]any{"round": round + 1, "tool_call_id": call.ID, "reason": reason},
	}, langfuse.EndOptions{
		Output:        toolPayload(output),
		Level:         langfuse.LevelWarning,
		StatusMessage: reason,
	})
}

func traceNudge(t *langfuse.Trace, round int, content string) {
	if t == nil {
		return
	}
	t.Event(langfuse.ObservationOptions{
		Type:     langfuse.TypeEvent,
		Name:     "coordinator.nudge",
		Input:    clipRunes(strings.TrimSpace(content), llmLogFieldBudget),
		Metadata: map[string]any{"round": round + 1},
	}, langfuse.EndOptions{
		Output:        toolRequiredNudge,
		Level:         langfuse.LevelWarning,
		StatusMessage: "assistant answered without a tool call",
	})
}

func traceLoopFailure(t *langfuse.Trace, round int, err error) {
	if t == nil || err == nil {
		return
	}
	t.Event(langfuse.ObservationOptions{
		Type:     langfuse.TypeEvent,
		Name:     "coordinator.loop_error",
		Metadata: map[string]any{"round": round + 1},
	}, langfuse.EndOptions{Err: err})
}

// toolPayload keeps JSON arguments/results structured in Langfuse and clips
// anything else as text.
func toolPayload(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if json.Valid([]byte(raw)) && len(raw) <= traceToolPayloadBudget {
		return json.RawMessage(raw)
	}
	return clipRunes(raw, traceToolPayloadBudget)
}

// traceHistoryPreflight records the DWS history read that precedes the first
// model round for channel turns.
func traceHistoryStart(t *langfuse.Trace, turn Turn) *langfuse.Observation {
	if t == nil {
		return nil
	}
	return t.StartObservation(langfuse.ObservationOptions{
		Type:  langfuse.TypeRetriever,
		Name:  "dws_chat_history",
		Input: map[string]any{"conversation_id": strings.TrimSpace(turn.ConversationID), "limit": dingtalkHistoryLimit},
	})
}

func traceHistoryEnd(obs *langfuse.Observation, history []HistoryLine, err error) {
	if obs == nil {
		return
	}
	end := langfuse.EndOptions{Err: err}
	if err == nil {
		lines := make([]map[string]string, 0, len(history))
		for _, line := range history {
			lines = append(lines, map[string]string{
				"role":    clipRunes(strings.TrimSpace(line.Role), llmLogNameBudget),
				"content": clipRunes(strings.TrimSpace(line.Content), llmLogFieldBudget),
			})
		}
		end.Output = map[string]any{"message_count": len(history), "messages": lines}
	}
	obs.End(end)
}
