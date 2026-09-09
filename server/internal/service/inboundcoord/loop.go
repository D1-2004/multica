package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/google/uuid"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const maxLoopRounds = 8

const toolRequiredNudge = "You must call a tool. Do not invent facts or use related_tasks as the answer. Scene questions finish from Host scene_memory. If the user named a conversation_id, call assoc_recall with that exact id. Then call finish."

// Completer is the one Chat Completions round the coordinator loop needs.
type Completer interface {
	Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

func (c *Coordinator) runLoop(ctx context.Context, turn Turn) (Decision, error) {
	ensureTurnTraceID(&turn)
	lt := langfuse.TraceFromContext(ctx)
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(buildSystemPrompt(turn)), openai.UserMessage(buildUserPrompt(turn)),
	}
	logCoordinatorLLMRequest(turn, buildUserPrompt(turn))
	var used []string
	var recalls []recallCall
	recalledIssues := map[string]struct{}{}
	continuationIssues := map[string]struct{}{}
	if turn.Loop == LoopTaskFinished && turn.IssueID != "" {
		recalledIssues[turn.IssueID] = struct{}{}
	}
	steps := make([]protocol.ChatCoordinatorStep, 0, maxLoopRounds*2)
	appendStep := func(step protocol.ChatCoordinatorStep) { step.Seq = len(steps) + 1; steps = append(steps, step) }
	fakeWorkHinted := false
	fail := func(err error) (Decision, error) {
		appendStep(protocol.ChatCoordinatorStep{Type: "error", Content: clipRunes(err.Error(), 800), Error: true})
		return Decision{Action: ActionDeferred, Reason: "coordinator_undecided", Steps: steps}, err
	}
	for round := 0; round < maxLoopRounds; round++ {
		recalled := len(recalls) > 0
		messages[0] = openai.SystemMessage(buildSystemPromptForStage(turn, recalled))
		tools := toolsForDisclosure(turn, round, recalled)
		manifest := policyManifestForStage(turn, recalled)
		if lt != nil {
			lt.AddMetadata(map[string]any{"policy_version": manifest.PolicyVersion, "assembly_version": manifest.AssemblyVersion, "prompt_hash": manifest.PromptHash, "modules": manifest.Modules, "active_rule_ids": manifest.ActiveRuleIDs, "history_status": turn.HistoryStatus, "history_before": turn.HistoryBefore, "dingtalk_history_count": len(turn.DingTalkHistory), "allowed_tools": toolParamNames(tools)})
		}
		generation := traceRoundGeneration(lt, round, messages, tools)
		completion, err := c.complete(ctx, messages, tools)
		endRoundGeneration(generation, completion, err)
		if err != nil {
			return fail(err)
		}
		if len(completion.Choices) == 0 {
			return fail(fmt.Errorf("coordinator loop: no choices"))
		}
		msg := completion.Choices[0].Message
		normalizeToolCallTypes(&msg)
		calls := functionToolCalls(msg)
		if len(calls) == 0 {
			messages = append(messages, msg.ToParam(), openai.UserMessage("Call an available tool to finish or obtain missing evidence. Do not invent facts."))
			continue
		}
		messages = append(messages, msg.ToParam())
		// Independent reads may share a response. A terminal plan must be alone:
		// a model cannot reason from a tool result it has not received yet.
		allowed := map[string]bool{}
		for _, name := range toolParamNames(tools) {
			allowed[name] = true
		}
		for _, call := range calls {
			if call.Name == toolAssocRecall {
				call.Arguments = defaultRecallConversationID(call.Arguments, turn.ConversationID)
			}
			appendStep(protocol.ChatCoordinatorStep{Type: "tool_use", Tool: call.Name, Input: clipRunes(call.Arguments, 4000)})
			var result string
			var callErr error
			if !allowed[call.Name] {
				callErr = hintErr("tool is not available in this stage", "Use the advertised read tools, then submit all work through finish.items; do not call write tools.")
			} else if call.Name == toolFinish {
				if len(calls) != 1 {
					callErr = hintErr("finish must be the only call", "Read the tool evidence on the next round before finishing.")
				} else if turn.Loop == LoopTaskFinished {
					decision := parseDecision(call.Arguments, turn)
					if decision.Action != ActionReply && decision.Action != ActionSilence {
						callErr = fmt.Errorf("task_finished may only reply or silence")
					} else {
						decision.Steps = steps
						decision.ToolRounds = round + 1
						decision.ToolsUsed = append(used, call.Name)
						if saveErr := SavePlan(ctx, decision); saveErr != nil {
							return fail(saveErr)
						}
						logCoordinatorLLMFinish(turn, round, call.Arguments, decision)
						traceToolEnd(traceToolStart(lt, round, call), finishToolOutput(decision), nil, "terminal")
						return decision, nil
					}
				} else {
					var decision Decision
					decision, callErr = parseValidatedWindowPlanHinted(call.Arguments, turn, recalls, recalledIssues, fakeWorkHinted)
					if isFakeWorkReplyHint(callErr) {
						fakeWorkHinted = true
					}
					if callErr == nil {
						decision.Steps = steps
						decision.ToolRounds = round + 1
						decision.ToolsUsed = append(used, call.Name)
						if saveErr := SavePlan(ctx, decision); saveErr != nil {
							return fail(saveErr)
						}
						logCoordinatorLLMFinish(turn, round, call.Arguments, decision)
						traceToolEnd(traceToolStart(lt, round, call), finishToolOutput(decision), nil, "terminal")
						return decision, nil
					}
				}
			} else if call.Name == toolContextRead {
				result, callErr = c.readHistoryContext(ctx, &turn, call.Arguments)
			} else {
				if call.Name == toolAssocRecall {
					call.Arguments = defaultRecallConversationID(call.Arguments, turn.ConversationID)
				}
				callErr = requireRecalledIssueForTool(call.Name, call.Arguments, recalledIssues)
				if callErr == nil {
					result, callErr = c.callTool(ctx, turn, call.Name, call.Arguments)
				}
				if callErr == nil && call.Name == toolAssocRecall {
					recalls = append(recalls, parseRecallCall(call.Arguments))
					collectRecalledIssues(recalledIssues, continuationIssues, turn.ConversationID, result)
				}
			}
			used = append(used, call.Name)
			if callErr != nil {
				result = marshalToolFailure(callErr)
			}
			appendStep(protocol.ChatCoordinatorStep{Type: "tool_result", Tool: call.Name, Output: clipRunes(result, 8000), Error: callErr != nil})
			messages = append(messages, openai.ToolMessage(result, call.ID))
			logCoordinatorLLMTool(turn, round, call.Name, call.Arguments, result, callErr != nil, "")
			traceToolEnd(traceToolStart(lt, round, call), result, callErr, "")
		}
	}
	return fail(fmt.Errorf("coordinator loop: evidence or valid plan missing after %d rounds", maxLoopRounds))
}

// finishToolOutput is the Langfuse view of a finish call: the parsed verdict
// rather than the raw arguments, so a reviewer sees what the server acted on.
func finishToolOutput(decision Decision) string {
	raw, err := json.Marshal(map[string]any{
		"action":    string(decision.Action),
		"issue_id":  strings.TrimSpace(decision.IssueID),
		"text":      clipRunes(strings.TrimSpace(decision.UserText), traceOutputTextBudget),
		"look_into": clipRunes(strings.TrimSpace(decision.LookInto), llmLogFieldBudget),
		"purpose":   clipRunes(strings.TrimSpace(decision.Purpose), llmLogFieldBudget),
		"intent":    strings.TrimSpace(decision.Intent),
		"reason":    clipRunes(strings.TrimSpace(decision.Reason), llmLogFieldBudget),
	})
	if err != nil {
		return ""
	}
	return string(raw)
}

func (c *Coordinator) complete(ctx context.Context, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) (*openai.ChatCompletion, error) {
	params := openai.ChatCompletionNewParams{
		Messages:            messages,
		Model:               shared.ChatModel(coordinatorModel),
		Tools:               tools,
		ReasoningEffort:     shared.ReasoningEffortNone,
		MaxCompletionTokens: openai.Int(maxCompletionTokens),
	}
	params.SetExtraFields(map[string]any{
		"enable_thinking": false,
		"tool_choice":     "required",
	})
	if temperature > 0 {
		params.Temperature = openai.Float(temperature)
	}
	if c != nil && c.Chat != nil {
		return c.Chat.Chat(ctx, params)
	}
	if c == nil || c.LLM == nil {
		return nil, fmt.Errorf("coordinator loop: llm is not configured")
	}
	return c.LLM.Chat(ctx, params)
}

func (c *Coordinator) callTool(ctx context.Context, turn Turn, name, arguments string) (string, error) {
	if c == nil || c.Tools == nil {
		return "", fmt.Errorf("coordinator tools are not configured")
	}
	return c.Tools.Call(ctx, turn, name, arguments)
}

type functionCall struct {
	ID, Name, Arguments string
}

func functionToolCalls(msg openai.ChatCompletionMessage) []functionCall {
	out := make([]functionCall, 0, len(msg.ToolCalls))
	for _, call := range msg.ToolCalls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			continue
		}
		id := strings.TrimSpace(call.ID)
		if id == "" {
			id = name
		}
		out = append(out, functionCall{ID: id, Name: name, Arguments: call.Function.Arguments})
	}
	return out
}

func taskFinishedToolDefs() []openai.ChatCompletionToolUnionParam {
	return []openai.ChatCompletionToolUnionParam{
		coordinatorIssueGetTool(),
		coordinatorIssueCommentListTool(),
		coordinatorTaskFinishedFinishTool(),
	}
}

func coordinatorTaskFinishedFinishTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        toolFinish,
		Description: openai.String("End the task-finished loop. Default action=silence when the sandbox already told this conversation. action=reply is one short colleague line only if this chat still lacks the outcome. Never 已发到群里 or 查收一下. Do not open a new Issue."),
		Parameters: shared.FunctionParameters{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"action"},
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"reply", "silence"}},
				"text":   map[string]any{"type": "string", "description": "Required for reply. One short colleague line. Forbidden: 已发到群里, 查收一下, inventing 私信你."},
				"reason": map[string]any{"type": "string"},
			},
		},
	})
}

func coordinatorToolDefs() []openai.ChatCompletionToolUnionParam {
	return []openai.ChatCompletionToolUnionParam{
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        toolAssocRecall,
			Description: openai.String("Read candidate matters on this scene. Default conversation_id is this inbound scene; another scene requires its exact user-named ID. First recall without q; q only filters the same scene. Use 7d/30d for explicitly older work. Empty means no recorded matters in this range, not no external data or capability."),
			Parameters: shared.FunctionParameters{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"conversation_id": map[string]any{"type": "string"},
				"since":           map[string]any{"type": "string", "description": "Defaults 48h; 24h, 48h, 7d, 30d, or RFC3339."},
				"q":               map[string]any{"type": "string", "description": "Optional extra keyword filter within this scene."},
				"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			}},
		}), coordinatorIssueGetTool(), coordinatorIssueCommentListTool(),
	}
}

func coordinatorIssueGetTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        toolIssueGet,
		Description: openai.String("Read one Issue this agent owns. Copy issue_id from assoc_recall, or from issue_id when loop=task_finished. Returns title, status, and a clipped description for rerank. Does not start a sandbox."),
		Parameters: shared.FunctionParameters{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"issue_id"},
			"properties": map[string]any{
				"issue_id": recalledIssueIDSchema("Issue UUID copied exactly from assoc_recall items[].issue_id, or from this turn's issue_id when loop=task_finished."),
			},
		},
	})
}

func coordinatorIssueCommentListTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        toolIssueCommentList,
		Description: openai.String("List recent comments on an Issue this agent owns. Copy issue_id from assoc_recall, or from issue_id when loop=task_finished. Use to understand this task, not a 300-person thread."),
		Parameters: shared.FunctionParameters{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"issue_id"},
			"properties": map[string]any{
				"issue_id": recalledIssueIDSchema("Issue UUID copied exactly from assoc_recall items[].issue_id, or from this turn's issue_id when loop=task_finished."),
				"tail":     map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Newest comments to return, default 20, max 50."},
			},
		},
	})
}

func normalizeToolCallTypes(msg *openai.ChatCompletionMessage) {
	if msg == nil {
		return
	}
	for i := range msg.ToolCalls {
		if strings.TrimSpace(msg.ToolCalls[i].Type) == "" && strings.TrimSpace(msg.ToolCalls[i].Function.Name) != "" {
			msg.ToolCalls[i].Type = "function"
		}
	}
}

func jsonQuote(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `"tool error"`
	}
	return string(raw)
}

func recalledIssueIDSchema(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"minLength":   8,
		"description": description,
	}
}

const (
	hintBindNeedsIssue  = "assoc_bind only attaches an existing Issue. Copy issue_id from assoc_recall items[].issue_id. If this is a new matter, do not bind; call finish action=issue with delegator, purpose, intent, and omit issue_id."
	hintCopyIssueID     = "Call assoc_recall first, then copy items[].issue_id byte-for-byte. Do not invent an id. A new matter uses finish action=issue without issue_id. Continuing an existing Issue uses issue_comment_add."
	hintNewIssueFinish  = "finish action=issue without issue_id creates the Issue. Set delegator (inbound sender), purpose as {委托人}委托：{事件与目的} with no DWS/auth, intent ask|confirm|notify|lookup|wait|other, and text naming the work. Do not write 记录事项."
	hintContinueComment = "finish cannot take issue_id. Continuing an existing Issue uses issue_comment_add with that issue_id, content naming the current sender and exact inbound words, and reply_text. If current_message does not advance a recalled purpose, finish action=reply."
	hintIssueText       = "finish action=issue needs text spoken to the user, naming the work in ordinary language, such as 我去问冬翔晚上打不打球. Do not omit text."
	hintPurpose         = "Rewrite purpose as {委托人}委托：{事件与目的}, e.g. 须莫🥥委托：向须莫v6询问明早有没有会议. Drop dws, data-auth, openConversationId, and 记录事项."
	hintIntent          = "intent must be one of ask, confirm, notify, lookup, wait, other."
	hintConversation    = "Pass conversation_id as the DingTalk openConversationId (cid…). The server fills the inbound cid if omitted."
	hintReplyText       = "issue_comment_add is terminal. Set reply_text to the short IM acknowledgement for the current speaker."
	hintRecallFirst     = "Call assoc_recall with the named conversation_id before finish. Do not answer from memory."
	hintNewDeliverable  = "This inbound is a different deliverable from that Issue. finish action=issue without issue_id. Do not issue_comment_add."
	hintIssueSpokenText = "Set finish.text to a short spoken line for this window, such as 我去查环境并汇报简略结果. Complete items without text cannot submit."
	hintIssueWorkItems  = "Use finish.items with source_refs, purpose, intent, basis; all requests must have a disposition."
	hintIssueItemLimit  = "Keep at most 8 planned items; never drop later requests."
	hintPurposeTooling  = "Purpose may name the requested object, including DWS身份 / MCP / Skills. Do not paste CLI commands, data-auth, or openConversationId. Rewrite as {委托人}委托：{事件与目的}."
	hintPurposeRepair   = "Rewrite purpose as {委托人}委托：{事件与目的}, naming the concrete event and deliverable. Do not paste the inbound envelope."
)

type toolHintError struct {
	msg  string
	hint string
	err  error
}

func (e *toolHintError) Error() string {
	if e == nil {
		return "tool error"
	}
	if e.err != nil {
		if e.msg == "" {
			return e.err.Error()
		}
		return e.msg + ": " + e.err.Error()
	}
	return e.msg
}

func (e *toolHintError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *toolHintError) Hint() string {
	if e == nil {
		return ""
	}
	return e.hint
}

func hintErr(msg, hint string) error {
	return &toolHintError{msg: msg, hint: hint}
}

func hintWrap(msg, hint string, err error) error {
	return &toolHintError{msg: msg, hint: hint, err: err}
}

type hinter interface {
	Hint() string
}

func marshalToolFailure(err error) string {
	if err == nil {
		return `{"error":"tool error"}`
	}
	payload := map[string]string{"error": err.Error()}
	var h hinter
	if errors.As(err, &h) {
		if hint := strings.TrimSpace(h.Hint()); hint != "" {
			payload["hint"] = hint
		}
	}
	raw, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return `{"error":` + jsonQuote(err.Error()) + `}`
	}
	return string(raw)
}

const (
	llmLogPromptBudget = 8000
	llmLogToolBudget   = 4000
	llmLogFieldBudget  = 400
	llmLogNameBudget   = 80
)

func ensureTurnTraceID(turn *Turn) {
	if turn == nil {
		return
	}
	if strings.TrimSpace(turn.TraceID) == "" {
		turn.TraceID = uuid.NewString()
	}
}

// conversationName is the human-readable scene name used by the SLS index and
// the Langfuse metadata: the channel title, else the Scene Memory title, else
// the sender (which channel adapters may pass as a bare id).
func conversationName(turn Turn) string {
	for _, candidate := range []string{turn.ConversationTitle, turn.SceneTitle, turn.SenderName} {
		if name := strings.TrimSpace(candidate); name != "" {
			return clipRunes(name, llmLogNameBudget)
		}
	}
	return ""
}

func coordinatorLogIndex(turn Turn) []any {
	kind := strings.TrimSpace(turn.Kind)
	if kind == "" {
		kind = strings.TrimSpace(turn.ChatType)
	}
	return []any{
		"coord_trace_id", strings.TrimSpace(turn.TraceID),
		"conversation_id", strings.TrimSpace(turn.ConversationID),
		"conversation_name", conversationName(turn),
		"conversation_kind", kind,
		"sender_name", clipRunes(strings.TrimSpace(turn.SenderName), llmLogNameBudget),
		"person_id", strings.TrimSpace(turn.PersonID),
		"agent_id", util.UUIDToString(turn.AgentID),
		"agent_name", strings.TrimSpace(turn.AgentName),
		"workspace_id", strings.TrimSpace(turn.WorkspaceID),
		"evidence_id", strings.TrimSpace(turn.EvidenceID),
		"source", string(turn.Source),
		"loop", string(turn.Loop),
		"current_message", clipRunes(strings.TrimSpace(turn.Message), llmLogFieldBudget),
	}
}

func logCoordinatorLLMRequest(turn Turn, userPrompt string) {
	slog.Info("inbound coordinator llm request",
		append(coordinatorLogIndex(turn),
			"event", "inbound_coordinator_llm_request",
			"model", coordinatorModel,
			"addressed", turn.Addressed,
			"busy", turn.Busy,
			"persona", clipRunes(strings.TrimSpace(turn.Persona), personaBudget),
			"reply_tone", clipRunes(strings.TrimSpace(turn.ReplyTone), toneBudget),
			"skill_count", len(turn.Skills),
			"dingtalk_history_count", len(turn.DingTalkHistory),
			"multica_history_count", len(turn.History),
			"scene_memory_revision", turn.SceneMemoryRevision,
			"system_prompt_runes", len([]rune(buildSystemPrompt(turn))),
			"user_prompt", clipRunes(userPrompt, llmLogPromptBudget),
			"user_prompt_runes", len([]rune(userPrompt)),
		)...)
}

func logCoordinatorLLMTool(turn Turn, round int, name, arguments, result string, failed bool, reason string) {
	attrs := append(coordinatorLogIndex(turn),
		"event", "inbound_coordinator_llm",
		"tool", name,
		"round", round,
		"arguments", clipRunes(strings.TrimSpace(arguments), llmLogToolBudget),
		"result", clipRunes(strings.TrimSpace(result), llmLogToolBudget),
		"error", failed,
	)
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	slog.Info("inbound coordinator llm", attrs...)
}

func logCoordinatorLLMFinish(turn Turn, round int, arguments string, decision Decision) {
	slog.Info("inbound coordinator llm finish",
		append(coordinatorLogIndex(turn),
			"event", "inbound_coordinator_llm_finish",
			"round", round,
			"arguments", clipRunes(strings.TrimSpace(arguments), llmLogToolBudget),
			"action", string(decision.Action),
			"issue_id", strings.TrimSpace(decision.IssueID),
			"text", clipRunes(strings.TrimSpace(decision.UserText), llmLogFieldBudget),
			"look_into", clipRunes(strings.TrimSpace(decision.LookInto), llmLogFieldBudget),
			"reason", clipRunes(strings.TrimSpace(decision.Reason), llmLogFieldBudget),
		)...)
}

func logCoordinatorLLMNudge(turn Turn, round int, content string) {
	slog.Info("inbound coordinator llm nudge",
		append(coordinatorLogIndex(turn),
			"event", "inbound_coordinator_llm_nudge",
			"round", round,
			"assistant_text", clipRunes(strings.TrimSpace(content), llmLogFieldBudget),
		)...)
}

type recallCall struct {
	ConversationID string
	Issue          string
	Q              string
}

func parseRecallCall(raw string) recallCall {
	var args recallArgs
	_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &args)
	return recallCall{
		ConversationID: strings.TrimSpace(args.ConversationID),
		Issue:          strings.TrimSpace(args.Issue),
		Q:              strings.TrimSpace(args.Q),
	}
}

func issueIDFromToolArguments(raw string) string {
	var args issueIDArgs
	_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &args)
	return strings.TrimSpace(args.IssueID)
}

func requireRecalledIssueForTool(name, raw string, recalled map[string]struct{}) error {
	switch name {
	case toolIssueGet, toolIssueCommentList:
		issueID := issueIDFromToolArguments(raw)
		if issueID == "" {
			return hintErr("issue_id is required", hintCopyIssueID)
		}
		if _, ok := recalled[issueID]; !ok {
			return hintErr("issue_id must be copied exactly from assoc_recall", hintCopyIssueID)
		}
	case toolIssueCommentAdd:
		var args issueIDArgs
		if json.Unmarshal([]byte(strings.TrimSpace(raw)), &args) != nil {
			return hintErr("invalid issue_comment_add arguments", hintCopyIssueID)
		}
		issueID := strings.TrimSpace(args.IssueID)
		if issueID == "" {
			return hintErr("issue_id is required", hintCopyIssueID)
		}
		if _, ok := recalled[issueID]; !ok {
			return hintErr("issue_id must be copied exactly from assoc_recall", hintCopyIssueID)
		}
		if strings.TrimSpace(args.ReplyText) == "" {
			return hintErr("reply_text is required", hintReplyText)
		}
	case toolAssocBind:
		var args bindArgs
		_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &args)
		issueID := strings.TrimSpace(args.IssueID)
		if issueID == "" {
			return hintErr("issue_id is required; assoc_bind must attach an existing Issue from assoc_recall", hintBindNeedsIssue)
		}
		if _, ok := recalled[issueID]; !ok {
			return hintErr("issue_id must be copied exactly from assoc_recall", hintCopyIssueID)
		}
	}
	return nil
}

func issueCommentReplyText(raw string) string {
	var args issueIDArgs
	_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &args)
	return strings.TrimSpace(args.ReplyText)
}

func parseIssueCommentEffect(raw string) (IssueCommentEffect, bool) {
	var effect IssueCommentEffect
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &effect) != nil ||
		strings.TrimSpace(effect.IssueID) == "" || strings.TrimSpace(effect.CommentID) == "" {
		return IssueCommentEffect{}, false
	}
	effect.IssueID = strings.TrimSpace(effect.IssueID)
	return effect, true
}

var conversationIDPattern = regexp.MustCompile(`cid[+A-Za-z0-9_/-]{8,}={0,2}`)

func extractConversationIDs(message string) []string {
	// A numeric `cid=` report query parameter is not an openConversationId.
	// Keep genuine opaque IDs, including openConversationId values in links.
	found := conversationIDPattern.FindAllStringIndex(message, -1)
	if len(found) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, span := range found {
		if span[0] > 0 && isConversationIDCharacter(message[span[0]-1]) ||
			span[1] < len(message) && (isConversationIDCharacter(message[span[1]]) || message[span[1]] == '=') {
			continue
		}
		id := message[span[0]:span[1]]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func isConversationIDCharacter(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' ||
		b == '+' || b == '_' || b == '/' || b == '-'
}

func asksSceneQuestion(message string) bool {
	s := strings.ToLower(message)
	for _, needle := range []string{
		"聊了什么", "有哪些事", "在跟什么", "跟什么事", "会话", "事情",
		"conversation", "what happened", "what's going on",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func collectRecalledIssues(issues, continuations map[string]struct{}, conversationID, raw string) {
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &payload) != nil {
		return
	}
	cid := assoc.NormalizeConversationID(conversationID)
	for _, rawItem := range payload.Items {
		var item struct {
			Issue       string          `json:"issue"`
			IssueID     string          `json:"issue_id"`
			Status      string          `json:"status"`
			MatchedVia  string          `json:"matched_via"`
			OnThisScene *bool           `json:"on_this_scene"`
			Why         string          `json:"why"`
			WhyListed   string          `json:"why_listed"`
			WaitingOn   json.RawMessage `json:"waiting_on"`
		}
		if json.Unmarshal(rawItem, &item) != nil {
			continue
		}
		issueID := firstNonEmpty(item.IssueID, item.Issue)
		if issueID == "" {
			continue
		}
		issues[issueID] = struct{}{}
		why := firstNonEmpty(item.Why, item.WhyListed)
		if strings.TrimSpace(item.MatchedVia) == "window" || why == "关键词命中，不是本会话" {
			continue
		}
		if item.OnThisScene != nil && !*item.OnThisScene {
			continue
		}
		if recalledItemOnThisScene(item.OnThisScene, item.WaitingOn, cid) {
			continuations[issueID] = struct{}{}
		}
	}
}

func recalledItemOnThisScene(onThisScene *bool, waitingOn json.RawMessage, cid string) bool {
	if onThisScene != nil {
		return *onThisScene
	}
	if cid == "" || len(waitingOn) == 0 {
		return false
	}
	var asString string
	if json.Unmarshal(waitingOn, &asString) == nil {
		return assoc.NormalizeConversationID(asString) == cid
	}
	var asList []struct {
		ConversationID string `json:"conversation_id"`
	}
	if json.Unmarshal(waitingOn, &asList) != nil {
		return false
	}
	for _, waiting := range asList {
		if assoc.NormalizeConversationID(waiting.ConversationID) == cid {
			return true
		}
	}
	return false
}
