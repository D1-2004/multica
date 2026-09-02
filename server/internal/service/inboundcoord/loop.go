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
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const maxLoopRounds = 8

const toolRequiredNudge = "You must call a tool. Do not answer from memory or related_tasks. If the user named a conversation_id, call assoc_recall with that exact id. Then call finish."

// Completer is the one Chat Completions round the coordinator loop needs.
type Completer interface {
	Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

func (c *Coordinator) runLoop(ctx context.Context, turn Turn) (Decision, error) {
	ensureTurnTraceID(&turn)
	userPrompt := buildUserPrompt(turn)
	logCoordinatorLLMRequest(turn, userPrompt)
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
		openai.UserMessage(userPrompt),
	}
	var used []string
	var recalls []recallCall
	var bind BindSpec
	recalledIssues := map[string]struct{}{}
	continuationIssues := map[string]struct{}{}
	steps := make([]protocol.ChatCoordinatorStep, 0, maxLoopRounds*2)
	appendStep := func(step protocol.ChatCoordinatorStep) {
		step.Seq = len(steps) + 1
		steps = append(steps, step)
	}
	fail := func(err error) (Decision, error) {
		appendStep(protocol.ChatCoordinatorStep{Type: "error", Content: clipRunes(err.Error(), 800), Error: true})
		return Decision{Steps: append([]protocol.ChatCoordinatorStep{}, steps...)}, err
	}
	for round := 0; round < maxLoopRounds; round++ {
		completion, err := c.complete(ctx, messages, toolsForRound(round))
		if err != nil {
			return fail(err)
		}
		if len(completion.Choices) == 0 {
			return fail(fmt.Errorf("coordinator loop: no choices"))
		}
		msg := completion.Choices[0].Message
		if content := clipRunes(strings.TrimSpace(msg.Content), 800); content != "" {
			appendStep(protocol.ChatCoordinatorStep{Type: "thinking", Content: content})
		}
		normalizeToolCallTypes(&msg)
		calls := functionToolCalls(msg)
		if len(calls) == 0 {
			logCoordinatorLLMNudge(turn, round, msg.Content)
			if round >= maxLoopRounds-1 {
				return fail(fmt.Errorf("coordinator loop: no finish"))
			}
			messages = append(messages, msg.ToParam())
			messages = append(messages, openai.UserMessage(toolRequiredNudge))
			continue
		}
		messages = append(messages, msg.ToParam())
		for _, call := range calls {
			if call.Name == toolAssocRecall {
				call.Arguments = defaultRecallConversationID(call.Arguments, turn.ConversationID)
			}
			appendStep(protocol.ChatCoordinatorStep{Type: "tool_use", Tool: call.Name, Input: clipRunes(call.Arguments, 1200)})
			if call.Name == toolFinish {
				if reqErr := requireRecallBeforeFinish(turn, recalls, recalledIssues, continuationIssues, bind, call.Arguments); reqErr != nil {
					appendStep(protocol.ChatCoordinatorStep{Type: "tool_result", Tool: call.Name, Output: reqErr.Error(), Error: true})
					messages = append(messages, openai.ToolMessage(`{"error":`+jsonQuote(reqErr.Error())+`}`, call.ID))
					logCoordinatorLLMTool(turn, round, call.Name, call.Arguments, reqErr.Error(), true, "recall_required")
					continue
				}
				if reqErr := requirePurposeForNewIssue(turn, call.Arguments); reqErr != nil {
					appendStep(protocol.ChatCoordinatorStep{Type: "tool_result", Tool: call.Name, Output: reqErr.Error(), Error: true})
					messages = append(messages, openai.ToolMessage(`{"error":`+jsonQuote(reqErr.Error())+`}`, call.ID))
					logCoordinatorLLMTool(turn, round, call.Name, call.Arguments, reqErr.Error(), true, "purpose_required")
					continue
				}
				used = append(used, call.Name)
				decision := parseDecision(call.Arguments, turn)
				applyBindSpec(&decision, bind)
				decision.ToolRounds = round + 1
				decision.ToolsUsed = used
				logCoordinatorLLMFinish(turn, round, call.Arguments, decision)
				if reason := clipRunes(strings.TrimSpace(decision.Reason), 800); reason != "" {
					appendStep(protocol.ChatCoordinatorStep{Type: "thinking", Content: reason})
				}
				if text := strings.TrimSpace(decision.UserText); text != "" {
					appendStep(protocol.ChatCoordinatorStep{Type: "text", Content: text})
				}
				decision.Steps = append([]protocol.ChatCoordinatorStep{}, steps...)
				return decision, nil
			}
			used = append(used, call.Name)
			if call.Name == toolAssocRecall {
				recalls = append(recalls, parseRecallCall(call.Arguments))
			}
			if reqErr := requireRecalledIssueForTool(call.Name, call.Arguments, recalledIssues); reqErr != nil {
				appendStep(protocol.ChatCoordinatorStep{Type: "tool_result", Tool: call.Name, Output: reqErr.Error(), Error: true})
				messages = append(messages, openai.ToolMessage(`{"error":`+jsonQuote(reqErr.Error())+`}`, call.ID))
				logCoordinatorLLMTool(turn, round, call.Name, call.Arguments, reqErr.Error(), true, "issue_not_recalled")
				continue
			}
			result, callErr := c.callTool(ctx, turn, call.Name, call.Arguments)
			if errors.Is(callErr, ErrIssueBusy) {
				appendStep(protocol.ChatCoordinatorStep{Type: "tool_result", Tool: call.Name, Output: callErr.Error(), Error: true})
				logCoordinatorLLMTool(turn, round, call.Name, call.Arguments, callErr.Error(), true, "issue_busy")
				return Decision{
					Action: ActionRetry, IssueID: issueIDFromToolArguments(call.Arguments),
					ToolRounds: round + 1, ToolsUsed: used, Steps: append([]protocol.ChatCoordinatorStep{}, steps...),
				}, nil
			}
			if callErr != nil {
				result = `{"error":` + jsonQuote(callErr.Error()) + `}`
			} else if call.Name == toolAssocRecall {
				collectRecalledIssues(recalledIssues, continuationIssues, turn.ConversationID, result)
			} else if call.Name == toolAssocBind {
				if spec, ok := parseBindSpec(result); ok {
					bind = spec
				}
			} else if call.Name == toolIssueCommentAdd {
				if effect, ok := parseIssueCommentEffect(result); ok {
					appendStep(protocol.ChatCoordinatorStep{Type: "tool_result", Tool: call.Name, Output: clipRunes(result, 1600)})
					replyText := issueCommentReplyText(call.Arguments)
					if replyText == "" {
						appendStep(protocol.ChatCoordinatorStep{Type: "tool_result", Tool: call.Name, Output: "reply_text is required", Error: true})
						messages = append(messages, openai.ToolMessage(`{"error":"reply_text is required"}`, call.ID))
						logCoordinatorLLMTool(turn, round, call.Name, call.Arguments, "reply_text is required", true, "reply_text_required")
						continue
					}
					decision := Decision{
						Action:       ActionReply,
						UserText:     replyText,
						IssueID:      effect.IssueID,
						Reason:       "issue_comment_added",
						ToolRounds:   round + 1,
						ToolsUsed:    used,
						IssueComment: &effect,
						Steps: append(append([]protocol.ChatCoordinatorStep{}, steps...), protocol.ChatCoordinatorStep{
							Seq: len(steps) + 1, Type: "text", Content: replyText,
						}),
					}
					applyBindSpec(&decision, bind)
					logCoordinatorLLMTool(turn, round, call.Name, call.Arguments, result, false, "terminal")
					logCoordinatorLLMFinish(turn, round, call.Arguments, decision)
					return decision, nil
				}
			}
			appendStep(protocol.ChatCoordinatorStep{Type: "tool_result", Tool: call.Name, Output: clipRunes(result, 1600), Error: callErr != nil})
			messages = append(messages, openai.ToolMessage(result, call.ID))
			logCoordinatorLLMTool(turn, round, call.Name, call.Arguments, result, callErr != nil, "")
		}
	}
	return fail(fmt.Errorf("coordinator loop: exceeded %d rounds", maxLoopRounds))
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

func toolsForRound(round int) []openai.ChatCompletionToolUnionParam {
	if round >= maxLoopRounds-1 {
		return []openai.ChatCompletionToolUnionParam{coordinatorFinishTool()}
	}
	return coordinatorToolDefs()
}

func coordinatorFinishTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        toolFinish,
		Description: openai.String("End the coordinator loop with the user-facing verdict. Use reply only for a complete answer available now. Use issue for contacts, DWS, search, files, external data, writes, actions, or any capability unavailable in this loop. Never use reply to say you cannot complete the request."),
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{"reply", "issue", "silence"}},
				"text":      map[string]any{"type": "string"},
				"look_into": map[string]any{"type": "string"},
				"issue_id":  map[string]any{"type": "string", "description": "Existing Issue UUID copied exactly from assoc_recall when this message continues recalled work. Omit to create a new Issue."},
				"delegator": map[string]any{"type": "string", "description": "Required for a new Issue. Who asked this agent to act. Copy the inbound sender name."},
				"purpose":   map[string]any{"type": "string", "description": "Required for a new Issue. Event and goal, such as 向辰驷确认明天几点打球. No DWS or auth."},
				"intent":    map[string]any{"type": "string", "enum": []string{"ask", "confirm", "notify", "lookup", "wait", "other"}},
				"reason":    map[string]any{"type": "string"},
			},
			"required": []string{"action"},
		},
	})
}

func coordinatorToolDefs() []openai.ChatCompletionToolUnionParam {
	return []openai.ChatCompletionToolUnionParam{
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        toolAssocRecall,
			Description: openai.String("Recall Issue/Task matters and inbound/outbound events on the scene graph. Always include this inbound conversation_id; the server fills it if omitted. q filters purpose on that scene and must not drop the cid. Pass a different openConversationId only when the user named one. since defaults to 48h."),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"since":           map[string]any{"type": "string", "description": "24h, 48h, 7d, or RFC3339. Defaults to 48h."},
					"conversation_id": map[string]any{"type": "string", "description": "DingTalk openConversationId of the scene. Defaults to this inbound conversation_id. Do not omit it when q is set."},
					"person_id":       map[string]any{"type": "string", "description": "DingTalk uid. Optional rank signal; do not invent."},
					"issue":           map[string]any{"type": "string", "description": "Issue UUID if already known."},
					"q":               map[string]any{"type": "string", "description": "Keyword filter on purpose in the recalled scene. Does not replace conversation_id."},
					"limit":           map[string]any{"type": "integer", "description": "Max items, default 20, max 50."},
				},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        toolAssocBind,
			Description: openai.String("Attach this conversation to an existing Issue from assoc_recall. issue_id is required. Rewrite purpose as {委托人}委托：{事件与目的}. Do not call this for a new matter — finish action=issue with purpose instead, which creates the Issue then binds it."),
			Parameters: shared.FunctionParameters{
				"type":     "object",
				"required": []string{"issue_id", "purpose", "intent", "delegator"},
				"properties": map[string]any{
					"conversation_id": map[string]any{"type": "string", "description": "DingTalk openConversationId. Defaults to this inbound scene."},
					"issue_id":        map[string]any{"type": "string", "description": "Existing Issue UUID copied exactly from assoc_recall. Required."},
					"delegator":       map[string]any{"type": "string", "description": "Who asked this agent to act, such as 冬翔. Copy the inbound sender name; do not invent."},
					"place":           map[string]any{"type": "string", "description": "Optional. Where the event happens. Omit when unknown."},
					"purpose":         map[string]any{"type": "string", "description": "Event and goal, such as 向辰驷确认明天几点打球. Never paste the raw inbound envelope."},
					"intent":          map[string]any{"type": "string", "enum": []string{"ask", "confirm", "notify", "lookup", "wait", "other"}, "description": "ask=向某人询问; confirm=确认时间或选择; notify=通知原发起人; lookup=查找人或记录; wait=等待回复; other=其他."},
					"waiting_on":      map[string]any{"type": "string", "description": "openConversationId this matter is waiting on, if different from conversation_id."},
					"display_name":    map[string]any{"type": "string", "description": "Human name of the person in this scene, such as 须莫. Do not invent."},
					"person_id":       map[string]any{"type": "string"},
					"evidence_id":     map[string]any{"type": "string"},
					"kind":            map[string]any{"type": "string", "description": "dm or group. Copy from the inbound scene."},
				},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        toolIssueGet,
			Description: openai.String("Read one Issue this agent owns. Copy issue_id from assoc_recall. Returns title, status, and a clipped description for rerank. Does not start a sandbox."),
			Parameters: shared.FunctionParameters{
				"type":     "object",
				"required": []string{"issue_id"},
				"properties": map[string]any{
					"issue_id": map[string]any{"type": "string", "description": "Issue UUID copied exactly from assoc_recall."},
				},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        toolIssueCommentList,
			Description: openai.String("List recent comments on an Issue this agent owns. Copy issue_id from assoc_recall. Use before deciding whether the inbound turn continues that matter."),
			Parameters: shared.FunctionParameters{
				"type":     "object",
				"required": []string{"issue_id"},
				"properties": map[string]any{
					"issue_id": map[string]any{"type": "string", "description": "Issue UUID copied exactly from assoc_recall."},
					"tail":     map[string]any{"type": "integer", "description": "Newest comments to return, default 20, max 50."},
				},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        toolIssueCommentAdd,
			Description: openai.String("Add the trusted inbound DingTalk message as a member comment on an Issue this agent owns. The current DingTalk event sender is the actual speaker. The stored Multica comment author is only the workspace principal executing this Issue tool and is not evidence of the delegator, speaker, or recipient. This identity rule applies to both digital-employee and robot messages; a robot sender uid may be missing and must not be invented. The normal Issue comment path starts its next task. This tool is terminal on success: reply_text closes the current IM turn, so do not call finish afterward."),
			Parameters: shared.FunctionParameters{
				"type":     "object",
				"required": []string{"issue_id", "content", "reply_text"},
				"properties": map[string]any{
					"issue_id":   map[string]any{"type": "string", "description": "Issue UUID copied exactly from assoc_recall."},
					"content":    map[string]any{"type": "string", "description": "Exact inbound words prefixed with the actual sender from the current DingTalk event. Never derive that speaker or the original delegator from the Multica comment author."},
					"reply_text": map[string]any{"type": "string", "description": "Short user-facing acknowledgement sent to the current IM speaker after the comment is committed."},
					"parent":     map[string]any{"type": "string", "description": "Optional parent comment UUID."},
				},
			},
		}),
		coordinatorFinishTool(),
	}
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

func conversationName(turn Turn) string {
	if name := strings.TrimSpace(turn.ConversationTitle); name != "" {
		return clipRunes(name, llmLogNameBudget)
	}
	return clipRunes(strings.TrimSpace(turn.SenderName), llmLogNameBudget)
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
			"dingtalk_history_count", len(turn.DingTalkHistory),
			"multica_history_count", len(turn.History),
			"system_prompt_runes", len([]rune(systemPrompt)),
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
			return fmt.Errorf("issue_id is required")
		}
		if _, ok := recalled[issueID]; !ok {
			return fmt.Errorf("issue_id must be copied exactly from assoc_recall")
		}
	case toolIssueCommentAdd:
		var args issueIDArgs
		if json.Unmarshal([]byte(strings.TrimSpace(raw)), &args) != nil {
			return fmt.Errorf("invalid issue_comment_add arguments")
		}
		issueID := strings.TrimSpace(args.IssueID)
		if issueID == "" {
			return fmt.Errorf("issue_id is required")
		}
		if _, ok := recalled[issueID]; !ok {
			return fmt.Errorf("issue_id must be copied exactly from assoc_recall")
		}
		if strings.TrimSpace(args.ReplyText) == "" {
			return fmt.Errorf("reply_text is required")
		}
	case toolAssocBind:
		var args bindArgs
		_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &args)
		issueID := strings.TrimSpace(args.IssueID)
		if issueID == "" {
			return fmt.Errorf("issue_id is required; assoc_bind must attach an existing Issue from assoc_recall")
		}
		if _, ok := recalled[issueID]; !ok {
			return fmt.Errorf("issue_id must be copied exactly from assoc_recall")
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

var conversationIDPattern = regexp.MustCompile(`cid[+A-Za-z0-9_/=-]{8,}`)

func extractConversationIDs(message string) []string {
	found := conversationIDPattern.FindAllString(message, -1)
	if len(found) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, id := range found {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
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

type BindSpec struct {
	Pending        bool
	Linked         bool
	Purpose        string
	Intent         string
	IssueID        string
	ConversationID string
	WaitingOn      string
	DisplayName    string
	Kind           string
}

func parseBindSpec(raw string) (BindSpec, bool) {
	var parsed struct {
		Pending        bool   `json:"pending"`
		Linked         bool   `json:"linked"`
		Purpose        string `json:"purpose"`
		Intent         string `json:"intent"`
		IssueID        string `json:"issue_id"`
		ConversationID string `json:"conversation_id"`
		WaitingOn      string `json:"waiting_on"`
		DisplayName    string `json:"display_name"`
		Kind           string `json:"kind"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &parsed) != nil {
		return BindSpec{}, false
	}
	if strings.TrimSpace(parsed.Purpose) == "" || strings.TrimSpace(parsed.Intent) == "" {
		return BindSpec{}, false
	}
	return BindSpec{
		Pending:        parsed.Pending,
		Linked:         parsed.Linked,
		Purpose:        strings.TrimSpace(parsed.Purpose),
		Intent:         strings.TrimSpace(parsed.Intent),
		IssueID:        strings.TrimSpace(parsed.IssueID),
		ConversationID: strings.TrimSpace(parsed.ConversationID),
		WaitingOn:      strings.TrimSpace(parsed.WaitingOn),
		DisplayName:    strings.TrimSpace(parsed.DisplayName),
		Kind:           strings.TrimSpace(parsed.Kind),
	}, true
}

func applyBindSpec(decision *Decision, bind BindSpec) {
	if decision == nil {
		return
	}
	if bind.Purpose != "" {
		decision.Purpose = bind.Purpose
	}
	if bind.Intent != "" {
		decision.Intent = bind.Intent
	}
}

func requirePurposeForNewIssue(turn Turn, finishRaw string) error {
	var parsed struct {
		Action    string `json:"action"`
		IssueID   string `json:"issue_id"`
		Purpose   string `json:"purpose"`
		Delegator string `json:"delegator"`
		Intent    string `json:"intent"`
		Place     string `json:"place"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(finishRaw)), &parsed)
	if Action(strings.TrimSpace(parsed.Action)) != ActionIssue {
		return nil
	}
	if strings.TrimSpace(parsed.IssueID) != "" {
		return nil
	}
	delegator := firstNonEmpty(parsed.Delegator, turn.SenderName)
	if _, err := assoc.ComposeCoordinatorPurpose(delegator, parsed.Place, parsed.Purpose); err != nil {
		return fmt.Errorf("new Issue needs delegator, purpose, and intent on finish, such as 冬翔委托：向辰驷确认明天几点打球: %w", err)
	}
	if _, ok := assoc.CoordinatorIntent(parsed.Intent); !ok {
		return fmt.Errorf("new Issue needs intent on finish: ask, confirm, notify, lookup, wait, or other")
	}
	return nil
}

func requireRecallBeforeFinish(
	turn Turn,
	recalls []recallCall,
	recalledIssues map[string]struct{},
	continuationIssues map[string]struct{},
	bind BindSpec,
	finishRaw string,
) error {
	var parsed struct {
		Action  string `json:"action"`
		IssueID string `json:"issue_id"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(finishRaw)), &parsed)
	action := Action(strings.TrimSpace(parsed.Action))
	issueID := strings.TrimSpace(parsed.IssueID)
	if issueID != "" {
		if action != ActionIssue {
			return fmt.Errorf("issue_id is only valid with action=issue")
		}
		if _, ok := recalledIssues[issueID]; !ok {
			return fmt.Errorf("issue_id must be copied exactly from assoc_recall")
		}
	}
	if action == ActionSilence {
		return nil
	}
	ids := extractConversationIDs(turn.Message)
	if len(ids) == 0 && !asksSceneQuestion(turn.Message) {
		return nil
	}
	if len(recalls) == 0 {
		return fmt.Errorf("call assoc_recall before finish; do not answer from memory")
	}
	for _, id := range ids {
		covered := false
		for _, recall := range recalls {
			got := recall.ConversationID
			if got == "" {
				got = strings.TrimSpace(turn.ConversationID)
			}
			if got == id {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("assoc_recall must pass conversation_id %s exactly; empty items means unknown", id)
		}
	}
	return nil
}
