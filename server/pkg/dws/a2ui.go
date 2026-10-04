package dws

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
)

// A2UISend is one interactive card (im/create_and_send_a2ui_card). Messages
// are the A2UI JSON texts, each one message. BizCardID is the caller's
// business id; the gateway returns its own id in A2UIReceipt.BizID.
type A2UISend struct {
	Target
	Messages  []string
	Summary   string
	BizCardID string
	RequestID string
}

// A2UIReceipt is the gateway's card identity after a confirmed send.
// MessageID is the card message's openMessageId, the same id a dws text
// send returns as Sent.MessageID. TaskID is only used to resolve MessageID
// when the create response omits it.
type A2UIReceipt struct {
	BizID          string `json:"bizId"`
	CardInstanceID int64  `json:"cardInstanceId"`
	MessageID      string `json:"messageId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	TaskID         string `json:"taskId,omitempty"`
}

// A2UISendArguments is the im/create_and_send_a2ui_card argument map.
func A2UISendArguments(req A2UISend) (map[string]any, error) {
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	req.UserOpenDingTalkID = strings.TrimSpace(req.UserOpenDingTalkID)
	req.BizCardID = strings.TrimSpace(req.BizCardID)
	req.RequestID = strings.TrimSpace(req.RequestID)
	if err := oneA2UITarget(req.Target); err != nil {
		return nil, err
	}
	if len(req.Messages) == 0 {
		return nil, invalid("a2ui send needs messages")
	}
	for _, message := range req.Messages {
		if strings.TrimSpace(message) == "" {
			return nil, invalid("a2ui send needs messages")
		}
	}
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		return nil, invalid("a2ui send needs a summary")
	}
	args := map[string]any{
		"requestId":       orUUID(req.RequestID),
		"bizCardId":       orUUID(req.BizCardID),
		"a2uiMessages":    req.Messages,
		"summary":         summary,
		"protocolVersion": "1.0",
		"flowStatus":      "PROCESSING",
	}
	if err := req.Target.apply(args); err != nil {
		return nil, err
	}
	return args, nil
}

// SendA2UI delivers an interactive card as the Client's identity.
func (s *MessageService) SendA2UI(ctx context.Context, req A2UISend) (A2UIReceipt, error) {
	if s == nil || s.c == nil {
		return A2UIReceipt{}, invalid("a2ui send needs a client")
	}
	args, err := A2UISendArguments(req)
	if err != nil {
		return A2UIReceipt{}, err
	}
	raw, err := s.c.Call(ctx, ServerIM, "create_and_send_a2ui_card", args)
	if err != nil {
		return A2UIReceipt{}, err
	}
	receipt, err := readA2UIReceipt(raw)
	if err != nil {
		return A2UIReceipt{}, err
	}
	if receipt.MessageID == "" && receipt.TaskID != "" {
		status, statusErr := s.c.Call(ctx, ServerIM, "query_message_send_status", map[string]any{"openTaskId": receipt.TaskID})
		if statusErr == nil {
			if id := a2uiMessageID(status); id != "" {
				receipt.MessageID = id
			}
		}
	}
	return receipt, nil
}

type a2uiReceiptWire struct {
	BizID              string          `json:"bizId"`
	CardInstanceID     int64           `json:"cardInstanceId"`
	OpenMessageID      string          `json:"openMessageId"`
	MessageID          string          `json:"messageId"`
	MsgID              string          `json:"msgId"`
	OpenConversationID string          `json:"openConversationId"`
	ConversationID     string          `json:"conversationId"`
	OpenTaskID         string          `json:"openTaskId"`
	TaskID             string          `json:"taskId"`
	Result             json.RawMessage `json:"result"`
}

func readA2UIReceipt(raw []byte) (A2UIReceipt, error) {
	wire, err := unmarshalA2UIReceipt(raw)
	if err != nil {
		return A2UIReceipt{}, invalid("a2ui send was not confirmed")
	}
	if strings.TrimSpace(wire.BizID) == "" && len(wire.Result) > 0 && wire.Result[0] == '{' {
		if inner, innerErr := unmarshalA2UIReceipt(wire.Result); innerErr == nil && strings.TrimSpace(inner.BizID) != "" {
			wire = inner
		}
	}
	receipt := A2UIReceipt{
		BizID:          strings.TrimSpace(wire.BizID),
		CardInstanceID: wire.CardInstanceID,
		MessageID:      firstA2UIID(wire.OpenMessageID, wire.MessageID, wire.MsgID),
		ConversationID: firstA2UIID(wire.OpenConversationID, wire.ConversationID),
		TaskID:         firstA2UIID(wire.OpenTaskID, wire.TaskID),
	}
	if receipt.BizID == "" || receipt.CardInstanceID == 0 {
		return A2UIReceipt{}, invalid("a2ui send was not confirmed")
	}
	return receipt, nil
}

func unmarshalA2UIReceipt(raw []byte) (a2uiReceiptWire, error) {
	var wire a2uiReceiptWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return a2uiReceiptWire{}, err
	}
	return wire, nil
}

func a2uiMessageID(raw []byte) string {
	wire, err := unmarshalA2UIReceipt(raw)
	if err != nil {
		return ""
	}
	if id := firstA2UIID(wire.OpenMessageID, wire.MessageID, wire.MsgID); id != "" {
		return id
	}
	if len(wire.Result) > 0 && wire.Result[0] == '{' {
		inner, err := unmarshalA2UIReceipt(wire.Result)
		if err == nil {
			return firstA2UIID(inner.OpenMessageID, inner.MessageID, inner.MsgID)
		}
	}
	return ""
}

func firstA2UIID(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

// A2UIFinishArguments is the im/update_a2ui_card argument map that marks a
// card finished. requestID empty mints a trace id; it is not an idempotency key.
func A2UIFinishArguments(bizID, surfaceID, requestID string) (map[string]any, error) {
	bizID, err := normalizeA2UIBizID(bizID)
	if err != nil {
		return nil, err
	}
	surfaceID = strings.TrimSpace(surfaceID)
	if surfaceID == "" || strings.ContainsAny(surfaceID, " \t\r\n") {
		return nil, invalid("a2ui finish needs a surface id")
	}
	body, err := json.Marshal(map[string]any{
		"version": "v1.0",
		"updateDataModel": map[string]any{
			"surfaceId": surfaceID,
			"path":      "/status",
			"value":     "finished",
		},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"requestId":       orUUID(requestID),
		"bizId":           bizID,
		"flowStatus":      "FINISH",
		"a2uiMessages":    []string{string(body)},
		"a2uiAnnotations": []any{},
	}, nil
}

// FinishA2UI marks a card finished. The stored interaction is the source of
// truth; a caller that already saved the answer treats a finish error as
// best-effort.
func (s *MessageService) FinishA2UI(ctx context.Context, bizID, surfaceID string) error {
	if s == nil || s.c == nil {
		return invalid("a2ui finish needs a client")
	}
	args, err := A2UIFinishArguments(bizID, surfaceID, "")
	if err != nil {
		return err
	}
	_, err = s.c.Call(ctx, ServerIM, "update_a2ui_card", args)
	return err
}

func oneA2UITarget(t Target) error {
	n := 0
	if strings.TrimSpace(t.ConversationID) != "" {
		n++
	}
	if strings.TrimSpace(t.UserOpenDingTalkID) != "" {
		n++
	}
	if n != 1 {
		return invalid("a2ui send needs exactly one of conversationId or userOpenDingTalkId")
	}
	return nil
}

func normalizeA2UIBizID(raw string) (string, error) {
	bizID := strings.TrimSpace(raw)
	if bizID == "" {
		return "", invalid("a2ui finish needs a biz id")
	}
	for _, r := range bizID {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", invalid("a2ui biz id must be a single opaque id")
		}
	}
	switch strings.ToLower(bizID) {
	case "bizid", "biz-id", "your-biz-id", "your_biz_id",
		"<bizid>", "<biz-id>", "<your-biz-id>",
		"{bizid}", "{biz-id}", "${bizid}", "${biz-id}":
		return "", invalid("a2ui biz id is still a placeholder")
	}
	return bizID, nil
}
