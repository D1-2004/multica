// Package userdecision owns durable human decisions, independently of card delivery.
package userdecision

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const Version = "coordinator-user-decision-v1"

type Event struct {
	ValidationError string
	Protocol        string
	ID              string
	CorpID          string
	ConversationID  string
	CardID          string
	OperatorID      string
	RequestID       string
	Version         string
	Selected        []string
	Custom          string
	At              time.Time
	Raw             json.RawMessage
}

// ParseEvent accepts the DWS Stream envelope or its decoded payload. The
// action context contains the sender identity, never the human operator.
func ParseEvent(raw []byte) (Event, error) {
	e, err := ParseAuditEvent(raw)
	if err == nil && e.ValidationError != "" {
		return Event{}, errors.New(e.ValidationError)
	}
	return e, err
}

// ParseAuditEvent preserves malformed answers when the trusted envelope can be
// matched, allowing rejection records without accepting a choice.
func ParseAuditEvent(raw []byte) (Event, error) { return parseEvent(raw, 0) }

func parseEvent(raw []byte, depth int) (Event, error) {
	var envelope struct {
		Data         json.RawMessage `json:"data"`
		EventID      string          `json:"eventId"`
		EventIDSnake string          `json:"event_id"`
		EventKey     string          `json:"eventKey"`
		Type         string          `json:"type"`
		Payload      json.RawMessage `json:"payload"`
	}
	invalid := errors.New("invalid coordinator card event")
	if depth > 4 || len(raw) > 1024*1024 || json.Unmarshal(raw, &envelope) != nil {
		return Event{}, invalid
	}
	if len(envelope.Data) > 0 {
		var encoded string
		if json.Unmarshal(envelope.Data, &encoded) == nil {
			return parseEvent([]byte(encoded), depth+1)
		}
		return parseEvent(envelope.Data, depth+1)
	}
	if envelope.EventKey != "" && envelope.EventKey != "user_card_action_triggered" {
		return Event{}, invalid
	}
	if envelope.EventKey == "" && envelope.Type != "user_card_action_triggered" {
		return Event{}, invalid
	}
	var payload struct {
		CorpID string `json:"corpid"`
		Body   struct {
			Biz struct {
				ID string `json:"bizId"`
			} `json:"bizInfoDTO"`
			Conversation struct {
				ID string `json:"openConversationId"`
			} `json:"conversationContextDTO"`
			Operator struct {
				ID string `json:"openDingTalkId"`
			} `json:"operatorDTO"`
			A2UI struct {
				Action json.RawMessage `json:"action"`
			} `json:"a2uiEvent"`
			Legacy    json.RawMessage `json:"actionData"`
			Timestamp int64           `json:"triggerTimestamp"`
		} `json:"body"`
	}
	if json.Unmarshal(envelope.Payload, &payload) != nil {
		return Event{}, invalid
	}
	action := payload.Body.A2UI.Action
	legacy := len(action) == 0
	protocol := "a2uiEvent.action"
	if legacy {
		protocol = "actionData.context"
		action = payload.Body.Legacy
	}
	var a struct {
		Name    string `json:"name"`
		Context struct {
			ID      string `json:"sourceTurnId"`
			Version string `json:"sourceProjectionVersion"`
			Outcome string `json:"outcome"`
			Answers map[string]struct {
				Selected []string `json:"selected"`
				Custom   string   `json:"custom"`
			} `json:"answers"`
		} `json:"context"`
	}
	if json.Unmarshal(action, &a) != nil || (a.Name != "runtime.clarification.submit" && !(legacy && a.Name == "")) || a.Context.Outcome != "answered" {
		return Event{}, invalid
	}
	answer, ok := a.Context.Answers["q0"]
	answerError := ""
	if !ok || len(a.Context.Answers) != 1 || (len(answer.Selected) == 1 && strings.TrimSpace(answer.Selected[0]) == "") || len(answer.Selected) > 1 || (len(answer.Selected) == 0 && strings.TrimSpace(answer.Custom) == "") || len([]rune(answer.Custom)) > 8000 {
		answerError = "invalid_answer"
	}
	id := envelope.EventID
	if id == "" {
		id = envelope.EventIDSnake
	}
	e := Event{ValidationError: answerError, Protocol: protocol, ID: id, CorpID: payload.CorpID, ConversationID: payload.Body.Conversation.ID, CardID: payload.Body.Biz.ID, OperatorID: payload.Body.Operator.ID, RequestID: a.Context.ID, Version: a.Context.Version, Selected: answer.Selected, Custom: answer.Custom, At: time.UnixMilli(payload.Body.Timestamp), Raw: append(json.RawMessage(nil), raw...)}
	if e.ID == "" || e.CorpID == "" || e.ConversationID == "" || e.CardID == "" || e.OperatorID == "" || e.RequestID == "" || e.Version == "" {
		return Event{}, invalid
	}
	return e, nil
}

type Identity struct{ Environment, CorpID, ConversationID, CardID, InitiatorID, RequestID, Version string }

func (i Identity) Validate(e Event, environment string, now, expires time.Time) error {
	if environment != i.Environment || e.CorpID != i.CorpID || e.ConversationID != i.ConversationID || e.CardID != i.CardID || e.OperatorID != i.InitiatorID || e.RequestID != i.RequestID || e.Version != i.Version {
		return errors.New("identity_or_version_mismatch")
	}
	if expires.IsZero() || !now.Before(expires) {
		return errors.New("expired")
	}
	return nil
}
