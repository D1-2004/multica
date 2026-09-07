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
	userPrompt := buildUserPrompt(turn)
	logCoordinatorLLMRequest(turn, userPrompt)
	lt := langfuse.TraceFromContext(ctx)
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
		openai.UserMessage(userPrompt),
	}
	var used []string
	var recalls []recallCall
	var bind BindSpec
	recalledIssues := map[string]struct{}{}
	continuationIssues := map[string]struct{}{}
	if turn.Loop == LoopTaskFinished {
		if id := strings.TrimSpace(turn.IssueID); id != "" {
			recalledIssues[id] = struct{}{}
		}
	}
	steps := make([]protocol.ChatCoordinatorStep, 0, maxLoopRounds*2)
	appendStep := func(step protocol.ChatCoordinatorStep) {
		step.Seq = len(steps) + 1
		steps = append(steps, step)
	}
	fail := func(err error) (Decision, error) {
		appendStep(protocol.ChatCoordinatorStep{Type: "error", Content: clipRunes(err.Error(), 800), Error: true})
		return Decision{Steps: append([]protocol.ChatCoordinatorStep{}, steps...)}, err
	}
	reject := func(turn Turn, round int, call functionCall, err error, reason string) {
		out := marshalToolFailure(err)
		appendStep(protocol.ChatCoordinatorStep{Type: "tool_result", Tool: call.Name, Output: clipRunes(out, 1600), Error: true})
		messages = append(messages, openai.ToolMessage(out, call.ID))
		logCoordinatorLLMTool(turn, round, call.Name, call.Arguments, out, true, reason)
		traceToolReject(lt, round, call, out, reason)
	}
	for round := 0; round < maxLoopRounds; round++ {
		tools := toolsForTurn(turn, round)
		generation := traceRoundGeneration(lt, round, messages, tools)
		completion, err := c.complete(ctx, messages, tools)
		endRoundGeneration(generation, completion, err)
		if err != nil {
			return fail(err)
		}
		if len(completion.Choices) == 0 {
			err := fmt.Errorf("coordinator loop: no choices")
			traceLoopFailure(lt, round, err)
			return fail(err)
		}
		msg := completion.Choices[0].Message
		if content := clipRunes(strings.TrimSpace(msg.Content), 800); content != "" {
			appendStep(protocol.ChatCoordinatorStep{Type: "thinking", Content: content})
		}
		normalizeToolCallTypes(&msg)
		calls := functionToolCalls(msg)
		if len(calls) == 0 {
			logCoordinatorLLMNudge(turn, round, msg.Content)
			traceNudge(lt, round, msg.Content)
			if round >= maxLoopRounds-1 {
				err := fmt.Errorf("coordinator loop: no finish")
				traceLoopFailure(lt, round, err)
				return fail(err)
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
					reject(turn, round, call, reqErr, "recall_required")
					continue
				}
				if reqErr := requirePurposeForNewIssue(turn, call.Arguments); reqErr != nil {
					reject(turn, round, call, reqErr, "purpose_required")
					continue
				}
				if reqErr := requireSpokenIssueText(call.Arguments); reqErr != nil {
					reject(turn, round, call, reqErr, "issue_text_required")
					continue
				}
				used = append(used, call.Name)
				decision := parseDecision(call.Arguments, turn)
				applyBindSpec(&decision, bind)
				decision.ToolRounds = round + 1
				decision.ToolsUsed = used
				logCoordinatorLLMFinish(turn, round, call.Arguments, decision)
				traceToolEnd(traceToolStart(lt, round, call), finishToolOutput(decision), nil, "terminal")
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
				reject(turn, round, call, reqErr, "issue_not_recalled")
				continue
			}
			toolObs := traceToolStart(lt, round, call)
			result, callErr := c.callTool(ctx, turn, call.Name, call.Arguments)
			if errors.Is(callErr, ErrIssueBusy) {
				appendStep(protocol.ChatCoordinatorStep{Type: "tool_result", Tool: call.Name, Output: callErr.Error(), Error: true})
				logCoordinatorLLMTool(turn, round, call.Name, call.Arguments, callErr.Error(), true, "issue_busy")
				traceToolEnd(toolObs, callErr.Error(), callErr, "issue_busy")
				if !shouldRetryBusyIssueComment(turn) {
					return Decision{
						Action:     ActionSilence,
						Reason:     "issue_busy_unrelated",
						ToolRounds: round + 1, ToolsUsed: used, Steps: append([]protocol.ChatCoordinatorStep{}, steps...),
					}, nil
				}
				return Decision{
					Action: ActionRetry, IssueID: issueIDFromToolArguments(call.Arguments),
					Reason:     "issue_busy_park",
					ToolRounds: round + 1, ToolsUsed: used, Steps: append([]protocol.ChatCoordinatorStep{}, steps...),
				}, nil
			}
			if callErr != nil {
				result = marshalToolFailure(callErr)
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
						traceToolEnd(toolObs, result, nil, "reply_text_required")
						reject(turn, round, call, hintErr("reply_text is required", hintReplyText), "reply_text_required")
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
					traceToolEnd(toolObs, result, nil, "terminal")
					return decision, nil
				}
			}
			appendStep(protocol.ChatCoordinatorStep{Type: "tool_result", Tool: call.Name, Output: clipRunes(result, 1600), Error: callErr != nil})
			messages = append(messages, openai.ToolMessage(result, call.ID))
			logCoordinatorLLMTool(turn, round, call.Name, call.Arguments, result, callErr != nil, "")
			traceToolEnd(toolObs, result, callErr, "")
		}
	}
	err := fmt.Errorf("coordinator loop: exceeded %d rounds", maxLoopRounds)
	traceLoopFailure(lt, maxLoopRounds-1, err)
	return fail(err)
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

func shouldRetryBusyIssueComment(turn Turn) bool {
	if AllWindowAck(turn) {
		return false
	}
	msg := strings.TrimSpace(turn.Message)
	if strings.Contains(msg, "灌水") || strings.Contains(strings.ToUpper(msg), "FLOOD") {
		return false
	}
	return true
}

func toolsForTurn(turn Turn, round int) []openai.ChatCompletionToolUnionParam {
	if round >= maxLoopRounds-1 {
		if turn.Loop == LoopTaskFinished {
			return []openai.ChatCompletionToolUnionParam{coordinatorTaskFinishedFinishTool()}
		}
		return []openai.ChatCompletionToolUnionParam{coordinatorFinishTool()}
	}
	if turn.Loop == LoopTaskFinished {
		return taskFinishedToolDefs()
	}
	return coordinatorToolDefs()
}

func toolsForRound(round int) []openai.ChatCompletionToolUnionParam {
	return toolsForTurn(Turn{}, round)
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

func coordinatorFinishTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        toolFinish,
		Description: openai.String("End the coordinator loop with the user-facing verdict for this scene window. Use reply when the window is a greeting or does not advance a recalled purpose. Use issue when sandbox work is needed. items holds 1-2 deliverables in this window; each delegator copies that line's sender. Same deliverable → one item. Two different deliverables → two items. Never more than two. Continue an existing Issue with issue_comment_add, never with finish issue_id. text is one IM sentence for the whole window, required for reply and issue."),
		Parameters: shared.FunctionParameters{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"action"},
			"properties": map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{"reply", "issue", "silence"}},
				"text":      map[string]any{"type": "string", "description": "Required for reply and issue. One IM sentence for the whole window. Name the work in ordinary language."},
				"look_into": map[string]any{"type": "string"},
				"delegator": map[string]any{"type": "string", "minLength": 1, "description": "Required when action=issue and items is omitted. Copy the utterance sender. Never use another person."},
				"purpose":   map[string]any{"type": "string", "minLength": 8, "description": "Required when action=issue and items is omitted. Structured brief of 委托人, 事件, 目的."},
				"intent":    map[string]any{"type": "string", "enum": []string{"ask", "confirm", "notify", "lookup", "wait", "other"}, "description": "Required when action=issue and items is omitted."},
				"items": map[string]any{
					"type":        "array",
					"maxItems":    SceneWindowMaxItems,
					"description": "0-2 work items for this window. Omit for reply/silence. One item per distinct deliverable. delegator must be that line's sender.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"required":             []string{"delegator", "purpose", "intent"},
						"properties": map[string]any{
							"delegator": map[string]any{"type": "string", "minLength": 1},
							"purpose":   map[string]any{"type": "string", "minLength": 8},
							"intent":    map[string]any{"type": "string", "enum": []string{"ask", "confirm", "notify", "lookup", "wait", "other"}},
							"look_into": map[string]any{"type": "string"},
							"place":     map[string]any{"type": "string"},
						},
					},
				},
				"reason": map[string]any{"type": "string"},
			},
		},
	})
}

func coordinatorToolDefs() []openai.ChatCompletionToolUnionParam {
	return []openai.ChatCompletionToolUnionParam{
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        toolAssocRecall,
			Description: openai.String("Recall Issue/Task matters and inbound/outbound events on the scene graph. Always include this inbound conversation_id; the server fills it if omitted. q filters purpose on that scene and must not drop the cid. Pass a different openConversationId only when the user named one. since defaults to 48h; use 7d or 30d for older work the user still names."),
			Parameters: shared.FunctionParameters{
				"type":                 "object",
				"additionalProperties": false,
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
			Description: openai.String("Attach this conversation to an existing Issue. issue_id is required by schema and must be copied from assoc_recall items[].issue_id. purpose is the ordinary-language deliverable; the server prefixes 委托人委托. Never omit issue_id. Never invent issue_id. For a new matter do not call this tool — finish action=issue with delegator, purpose, intent, and omit issue_id."),
			Parameters: shared.FunctionParameters{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"issue_id", "purpose", "intent", "delegator"},
				"properties": map[string]any{
					"conversation_id": map[string]any{"type": "string", "description": "DingTalk openConversationId. Defaults to this inbound scene."},
					"issue_id":        recalledIssueIDSchema("Existing Issue UUID copied exactly from assoc_recall items[].issue_id. Required. Never omit. Never invent. For a new matter use finish action=issue instead."),
					"delegator":       map[string]any{"type": "string", "minLength": 1, "description": "Who asked this agent to act, such as 冬翔. Copy the inbound sender name; do not invent."},
					"place":           map[string]any{"type": "string", "description": "Optional. Where the event happens. Omit when unknown."},
					"purpose":         map[string]any{"type": "string", "minLength": 8, "description": "Event and goal, such as 向辰驷确认明天几点打球. Never paste the inbound envelope. No DWS, data-auth, or openConversationId."},
					"intent":          map[string]any{"type": "string", "enum": []string{"ask", "confirm", "notify", "lookup", "wait", "other"}, "description": "ask=向某人询问; confirm=确认时间或选择; notify=通知原发起人; lookup=查找人或记录; wait=等待回复; other=其他."},
					"waiting_on":      map[string]any{"type": "string", "description": "openConversationId this matter is waiting on, if different from conversation_id."},
					"display_name":    map[string]any{"type": "string", "description": "Human name of the person in this scene, such as 须莫. Do not invent."},
					"person_id":       map[string]any{"type": "string"},
					"evidence_id":     map[string]any{"type": "string"},
					"kind":            map[string]any{"type": "string", "enum": []string{"dm", "group", "single"}, "description": "dm or group. Copy from the inbound scene."},
				},
			},
		}),
		coordinatorIssueGetTool(),
		coordinatorIssueCommentListTool(),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        toolIssueCommentAdd,
			Description: openai.String("Add the trusted inbound DingTalk message as a member comment on an Issue this agent owns. The current DingTalk event sender is the actual speaker. The stored Multica comment author is only the workspace principal executing this Issue tool and is not evidence of the delegator, speaker, or recipient. This identity rule applies to both digital-employee and robot messages; a robot sender uid may be missing and must not be invented. The normal Issue comment path starts its next task. This tool is terminal on success: reply_text closes the current IM turn, so do not call finish afterward."),
			Parameters: shared.FunctionParameters{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"issue_id", "content", "reply_text"},
				"properties": map[string]any{
					"issue_id":   recalledIssueIDSchema("Issue UUID copied exactly from assoc_recall items[].issue_id."),
					"content":    map[string]any{"type": "string", "minLength": 1, "description": "Exact inbound words prefixed with the actual sender from the current DingTalk event. Never derive that speaker or the original delegator from the Multica comment author."},
					"reply_text": map[string]any{"type": "string", "minLength": 1, "description": "Short user-facing acknowledgement sent to the current IM speaker after the comment is committed."},
					"parent":     map[string]any{"type": "string", "description": "Optional parent comment UUID."},
				},
			},
		}),
		coordinatorFinishTool(),
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
			"dingtalk_history_count", len(turn.DingTalkHistory),
			"multica_history_count", len(turn.History),
			"scene_memory_revision", turn.SceneMemoryRevision,
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
		Items     []struct {
			Delegator string `json:"delegator"`
			Purpose   string `json:"purpose"`
			Intent    string `json:"intent"`
			Place     string `json:"place"`
		} `json:"items"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(finishRaw)), &parsed)
	if Action(strings.TrimSpace(parsed.Action)) != ActionIssue && len(parsed.Items) == 0 {
		return nil
	}
	if strings.TrimSpace(parsed.IssueID) != "" {
		return nil
	}
	if len(parsed.Items) > 0 {
		if len(parsed.Items) > SceneWindowMaxItems {
			return hintErr("finish items is at most 2", hintNewIssueFinish)
		}
		for _, item := range parsed.Items {
			delegator := firstNonEmpty(item.Delegator, turn.SenderName)
			if !validWindowDelegator(turn, delegator) {
				return hintErr("item.delegator must copy the utterance sender", hintNewIssueFinish)
			}
			if _, err := assoc.ComposeCoordinatorPurpose(delegator, item.Place, item.Purpose); err != nil {
				return hintWrap("each item needs delegator, purpose, and intent", hintNewIssueFinish, err)
			}
			if _, ok := assoc.CoordinatorIntent(item.Intent); !ok {
				return hintErr("each item needs intent: ask, confirm, notify, lookup, wait, or other", hintNewIssueFinish)
			}
		}
		return nil
	}
	delegator := firstNonEmpty(parsed.Delegator, turn.SenderName)
	if !validWindowDelegator(turn, delegator) {
		return hintErr("delegator must copy the utterance sender", hintNewIssueFinish)
	}
	if _, err := assoc.ComposeCoordinatorPurpose(delegator, parsed.Place, parsed.Purpose); err != nil {
		return hintWrap("new Issue needs delegator, purpose, and intent on finish", hintNewIssueFinish, err)
	}
	if _, ok := assoc.CoordinatorIntent(parsed.Intent); !ok {
		return hintErr("new Issue needs intent on finish: ask, confirm, notify, lookup, wait, or other", hintNewIssueFinish)
	}
	return nil
}

func requireSpokenIssueText(finishRaw string) error {
	var parsed struct {
		Action string `json:"action"`
		Text   string `json:"text"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(finishRaw)), &parsed)
	if Action(strings.TrimSpace(parsed.Action)) != ActionIssue {
		return nil
	}
	if strings.TrimSpace(parsed.Text) == "" {
		return hintErr("finish action=issue needs text spoken to the user", hintIssueText)
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
	if turn.Loop == LoopTaskFinished {
		return nil
	}
	var parsed struct {
		Action  string `json:"action"`
		IssueID string `json:"issue_id"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(finishRaw)), &parsed)
	action := Action(strings.TrimSpace(parsed.Action))
	issueID := strings.TrimSpace(parsed.IssueID)
	if issueID != "" {
		return hintErr("finish cannot take issue_id", hintContinueComment)
	}
	if action == ActionSilence {
		return nil
	}
	ids := extractConversationIDs(turn.Message)
	if len(ids) == 0 && !asksSceneQuestion(turn.Message) {
		return nil
	}
	if len(recalls) == 0 {
		return hintErr("call assoc_recall before finish; do not answer from memory", hintRecallFirst)
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
			return hintErr(
				fmt.Sprintf("assoc_recall must pass conversation_id %s exactly; empty items means unknown", id),
				hintRecallFirst,
			)
		}
	}
	return nil
}
