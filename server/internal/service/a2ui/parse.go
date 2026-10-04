package a2ui

import (
	"encoding/json"
	"strings"
)

// click is one user_card_action_triggered aimed at a card this loop opened.
type click struct {
	EventID        string
	CorpID         string
	ConversationID string
	Operator       string
	PublicID       string
	Version        string
	Outcome        string
	Selected       []string
	Custom         string
}

type clickMeta struct {
	EventID string
	CorpID  string
}

// parseClick reads a dwsclient.EventLine, or the inner event it wraps.
// A coordinator card or any other ref is errForeign. Bytes that will not
// become more readable on redelivery are errMalformed.
func parseClick(raw []byte) (click, error) {
	return parseClickDepth(raw, 0, clickMeta{})
}

func parseClickDepth(raw []byte, depth int, meta clickMeta) (click, error) {
	if depth > 4 || len(raw) == 0 || len(raw) > 1024*1024 {
		return click{}, errMalformed
	}
	var envelope struct {
		Data         json.RawMessage `json:"data"`
		EventID      string          `json:"eventId"`
		EventIDSnake string          `json:"event_id"`
		EventKey     string          `json:"eventKey"`
		EventType    string          `json:"event_type"`
		Type         string          `json:"type"`
		CorpID       string          `json:"event_corp_id"`
		Payload      json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return click{}, errMalformed
	}
	keep(&meta.EventID, envelope.EventID)
	keep(&meta.EventID, envelope.EventIDSnake)
	keep(&meta.CorpID, envelope.CorpID)
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		var encoded string
		if json.Unmarshal(envelope.Data, &encoded) == nil {
			return parseClickDepth([]byte(encoded), depth+1, meta)
		}
		return parseClickDepth(envelope.Data, depth+1, meta)
	}
	key := first(envelope.EventKey, envelope.EventType, envelope.Type)
	if key != "" && key != "user_card_action_triggered" && key != "event" {
		return click{}, errForeign
	}
	if len(envelope.Payload) == 0 || string(envelope.Payload) == "null" {
		return click{}, errMalformed
	}
	var payload struct {
		CorpID string `json:"corpid"`
		Body   struct {
			Biz struct {
				ID string `json:"bizId"`
			} `json:"bizInfoDTO"`
			Conversation struct {
				CID  string `json:"cid"`
				Open string `json:"openConversationId"`
			} `json:"conversationContextDTO"`
			Operator struct {
				UID  json.RawMessage `json:"uid"`
				Open string          `json:"openDingTalkId"`
			} `json:"operatorDTO"`
			A2UI struct {
				Action json.RawMessage `json:"action"`
			} `json:"a2uiEvent"`
			ActionData json.RawMessage `json:"actionData"`
		} `json:"body"`
	}
	if json.Unmarshal(envelope.Payload, &payload) != nil {
		return click{}, errMalformed
	}
	keep(&meta.CorpID, payload.CorpID)
	ctx, fromAction, ok := readAction(payload.Body.ActionData, payload.Body.A2UI.Action)
	if !ok {
		return click{}, errForeign
	}
	ref, parsed := ParseRef(ctx.SourceTurnID)
	if !parsed || (ctx.Version != "" && ctx.Version != Version) {
		return click{}, errForeign
	}
	if fromAction && ctx.Name != "" && ctx.Name != submitEvent {
		return click{}, errForeign
	}
	if meta.EventID == "" {
		return click{}, errMalformed
	}
	selected, custom, err := answerOf(ctx)
	if err != nil {
		return click{}, err
	}
	conversation := payload.Body.Conversation.CID
	if conversation == "" {
		conversation = payload.Body.Conversation.Open
	}
	return click{
		EventID: meta.EventID, CorpID: meta.CorpID, ConversationID: conversation,
		Operator: operatorOf(payload.Body.Operator.UID, payload.Body.Operator.Open),
		PublicID: ref.PublicID(), Version: ctx.Version, Outcome: ctx.Outcome,
		Selected: selected, Custom: custom,
	}, nil
}

type actionContext struct {
	Name         string `json:"name"`
	SourceTurnID string `json:"sourceTurnId"`
	Version      string `json:"sourceProjectionVersion"`
	Outcome      string `json:"outcome"`
	Answers      map[string]struct {
		Selected json.RawMessage `json:"selected"`
		Custom   string          `json:"custom"`
	} `json:"answers"`
}

// readAction prefers payload.body.actionData.context. a2uiEvent.action is
// the older shape and must name the clarification submit event.
func readAction(actionData, a2uiAction json.RawMessage) (actionContext, bool, bool) {
	if ctx, ok := contextOf(actionData); ok {
		return ctx, false, true
	}
	ctx, ok := contextOf(a2uiAction)
	if !ok {
		return actionContext{}, false, false
	}
	if ctx.Name != submitEvent {
		return actionContext{}, true, false
	}
	return ctx, true, true
}

func contextOf(raw json.RawMessage) (actionContext, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return actionContext{}, false
	}
	var wrapped struct {
		Name    string        `json:"name"`
		Context actionContext `json:"context"`
	}
	if json.Unmarshal(raw, &wrapped) != nil {
		return actionContext{}, false
	}
	if wrapped.Context.SourceTurnID != "" || len(wrapped.Context.Answers) > 0 {
		wrapped.Context.Name = wrapped.Name
		return wrapped.Context, true
	}
	var flat actionContext
	if json.Unmarshal(raw, &flat) != nil || flat.SourceTurnID == "" {
		return actionContext{}, false
	}
	return flat, true
}

func answerOf(ctx actionContext) ([]string, string, error) {
	if len(ctx.Answers) > 0 {
		if _, ok := ctx.Answers["q0"]; !ok {
			return nil, "", errMalformed
		}
	}
	answer := ctx.Answers["q0"]
	selected, err := parseSelected(answer.Selected)
	if err != nil {
		return nil, "", err
	}
	custom := strings.TrimSpace(answer.Custom)
	if len([]rune(custom)) > maxCustom {
		custom = string([]rune(custom)[:maxCustom])
	}
	return selected, custom, nil
}

func parseSelected(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []string{}, nil
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return cleanIDs(many), nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return cleanIDs([]string{one}), nil
	}
	var objects []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &objects) == nil {
		ids := make([]string, 0, len(objects))
		for _, object := range objects {
			ids = append(ids, object.ID)
		}
		return cleanIDs(ids), nil
	}
	return nil, errMalformed
}

func cleanIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func operatorOf(uid json.RawMessage, openID string) string {
	if len(uid) > 0 && string(uid) != "null" {
		var number json.Number
		if json.Unmarshal(uid, &number) == nil {
			text := number.String()
			if text != "" && text != "0" {
				return text
			}
		}
		var text string
		if json.Unmarshal(uid, &text) == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return strings.TrimSpace(openID)
}

func keep(dst *string, value string) {
	if *dst == "" {
		*dst = strings.TrimSpace(value)
	}
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
