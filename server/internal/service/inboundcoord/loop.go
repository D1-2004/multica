package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const maxLoopRounds = 8

const toolRequiredNudge = "You must call a tool. Do not answer from memory or related_tasks. If the user named a conversation_id, call assoc_recall with that exact id. Then call finish."

// Completer is the one Chat Completions round the coordinator loop needs.
type Completer interface {
	Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

func (c *Coordinator) runLoop(ctx context.Context, turn Turn) (Decision, error) {
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
		openai.UserMessage(buildUserPrompt(turn)),
	}
	var used []string
	var recalls []recallCall
	for round := 0; round < maxLoopRounds; round++ {
		completion, err := c.complete(ctx, messages, toolsForRound(round))
		if err != nil {
			return Decision{}, err
		}
		if len(completion.Choices) == 0 {
			return Decision{}, fmt.Errorf("coordinator loop: no choices")
		}
		msg := completion.Choices[0].Message
		normalizeToolCallTypes(&msg)
		calls := functionToolCalls(msg)
		if len(calls) == 0 {
			if round >= maxLoopRounds-1 {
				return Decision{}, fmt.Errorf("coordinator loop: no finish")
			}
			messages = append(messages, msg.ToParam())
			messages = append(messages, openai.UserMessage(toolRequiredNudge))
			continue
		}
		messages = append(messages, msg.ToParam())
		for _, call := range calls {
			if call.Name == toolFinish {
				if reqErr := requireRecallBeforeFinish(turn, recalls, call.Arguments); reqErr != nil {
					messages = append(messages, openai.ToolMessage(`{"error":`+jsonQuote(reqErr.Error())+`}`, call.ID))
					slog.Info("inbound coordinator tool",
						"event", "inbound_coordinator_tool",
						"tool", call.Name,
						"round", round,
						"error", true,
						"reason", "recall_required",
					)
					continue
				}
				used = append(used, call.Name)
				decision := parseDecision(call.Arguments, turn)
				decision.ToolRounds = round + 1
				decision.ToolsUsed = used
				return decision, nil
			}
			used = append(used, call.Name)
			if call.Name == toolAssocRecall {
				recalls = append(recalls, parseRecallCall(call.Arguments))
			}
			result, callErr := c.callTool(ctx, turn, call.Name, call.Arguments)
			if callErr != nil {
				result = `{"error":` + jsonQuote(callErr.Error()) + `}`
			}
			messages = append(messages, openai.ToolMessage(result, call.ID))
			slog.Info("inbound coordinator tool",
				"event", "inbound_coordinator_tool",
				"tool", call.Name,
				"round", round,
				"error", callErr != nil,
			)
		}
	}
	return Decision{}, fmt.Errorf("coordinator loop: exceeded %d rounds", maxLoopRounds)
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
			Description: openai.String("Recall Issue/Task matters and inbound/outbound events on the scene graph. A new inbound on a previously outbound DM is the same conversation_id. Pass the user's openConversationId when they name one. Omit conversation_id to use this inbound scene. Pass q without conversation_id to list this agent's matters in the window. since defaults to 48h."),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"since":           map[string]any{"type": "string", "description": "24h, 48h, 7d, or RFC3339. Defaults to 48h."},
					"conversation_id": map[string]any{"type": "string", "description": "DingTalk openConversationId. If omitted and q/issue are empty, defaults to this inbound conversation_id."},
					"person_id":       map[string]any{"type": "string", "description": "DingTalk uid. Optional rank signal; do not invent."},
					"issue":           map[string]any{"type": "string", "description": "Issue UUID if already known."},
					"q":               map[string]any{"type": "string", "description": "Keyword filter on purpose. Omit conversation_id to search across this agent's matters."},
					"limit":           map[string]any{"type": "integer", "description": "Max items, default 20, max 50."},
				},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        toolAssocBind,
			Description: openai.String("Bind a DingTalk conversation_id to an Issue so later replies in that scene recall it. issue_id from assoc_recall links the scene to a matter; omit issue_id to tag the inbound event only."),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"conversation_id": map[string]any{"type": "string"},
					"issue_id":        map[string]any{"type": "string", "description": "Issue UUID from assoc_recall. Required to link the scene to a matter."},
					"evidence_id":     map[string]any{"type": "string"},
					"person_id":       map[string]any{"type": "string"},
					"purpose":         map[string]any{"type": "string", "description": "Deliverable phrase such as 向冬翔确认今天吃什么. Needed when creating the Issue task node."},
					"kind":            map[string]any{"type": "string"},
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

func requireRecallBeforeFinish(turn Turn, recalls []recallCall, finishRaw string) error {
	var parsed struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(finishRaw)), &parsed)
	if Action(strings.TrimSpace(parsed.Action)) == ActionSilence {
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
