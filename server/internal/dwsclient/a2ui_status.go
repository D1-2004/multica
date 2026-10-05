package dwsclient

import (
	"encoding/json"
	"errors"
	"strings"
)

// Real message identity is separate from delivery. A send-status response can
// omit the conversation id; keep it absent for the Host's exact quote checks.
type a2uiStatusWire struct {
	Success            *bool           `json:"success"`
	Result             *a2uiStatusWire `json:"result"`
	MessageRef         *a2uiStatusWire `json:"messageRef"`
	OpenMessageID      string          `json:"openMessageId"`
	MessageID          string          `json:"messageId"`
	MsgID              string          `json:"msgId"`
	OpenConversationID string          `json:"openConversationId"`
	ConversationID     string          `json:"conversationId"`
	OpenCID            string          `json:"openCid"`
	SendStatus         json.RawMessage `json:"sendStatus"`
	Status             json.RawMessage `json:"status"`
}

func a2uiStatusIdentity(raw []byte) (string, string, string) {
	var wire a2uiStatusWire
	if json.Unmarshal(raw, &wire) != nil || wire.Success == nil || !*wire.Success {
		return "", "", "schema_unrecognized"
	}
	if wire.Result != nil {
		if wire.Result.Success != nil && !*wire.Result.Success {
			return "", "", "failed"
		}
		wire = *wire.Result
	}
	statusJSON := wire.SendStatus
	if len(statusJSON) == 0 {
		statusJSON = wire.Status
	}
	var state string
	if len(statusJSON) != 0 && json.Unmarshal(statusJSON, &state) != nil {
		return "", "", "schema_unrecognized"
	}
	switch strings.ToUpper(state) {
	case "FAIL", "FAILED", "FAILURE", "CANCELLED", "CANCELED":
		return "", "", "failed"
	case "PENDING", "PROCESSING", "SENDING", "INIT", "QUEUED":
		return "", "", "pending"
	case "", "SUCCESS", "SUCCEEDED", "DELIVERED", "SEND_SUCCESS":
	default:
		return "", "", "schema_unrecognized"
	}
	message := firstA2UIStatusID(wire.OpenMessageID, wire.MessageID, wire.MsgID)
	conversation := firstA2UIStatusID(wire.OpenConversationID, wire.ConversationID, wire.OpenCID)
	if ref := wire.MessageRef; ref != nil {
		refMessage := firstA2UIStatusID(ref.OpenMessageID, ref.MessageID, ref.MsgID)
		refConversation := firstA2UIStatusID(ref.OpenConversationID, ref.ConversationID, ref.OpenCID)
		if (message != "" && refMessage != "" && message != refMessage) || (conversation != "" && refConversation != "" && conversation != refConversation) {
			return "", "", "schema_unrecognized"
		}
		message = firstA2UIStatusID(message, refMessage)
		conversation = firstA2UIStatusID(conversation, refConversation)
	}
	if message == "" {
		return "", "", "pending"
	}
	if state == "" {
		return message, conversation, "identity_unconfirmed"
	}
	return message, conversation, "identity"
}

func firstA2UIStatusID(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// Retry only an exact structured, temporarily invisible task diagnostic.
// Token, permission and other parameter failures must not be retried.
func a2uiReceiptNotVisible(err error) bool {
	var operation *MessageOperationError
	return errors.As(err, &operation) && operation.diagnostics.ServerErrorCode() == "PARAM_ERROR" && operation.providerMessage == "消息不存在"
}
