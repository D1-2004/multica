package dwsclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// ResolveMessageSender reads the trusted source message using the sending
// identity. Ingress open IDs may belong to a different application/org scope.
// Names are never used as identity, and a different conversation cannot qualify.
func (c CLI) ResolveMessageSender(ctx context.Context, dir, conversationID, messageID string) (string, error) {
	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(messageID) == "" || strings.Contains(messageID, ",") {
		return "", errors.New("DWS source message reference is incomplete")
	}
	raw, err := c.messageOp(ctx, dir, []string{"chat", "message", "list-by-ids", "--msg-ids", messageID, "--format", "json"},
		func(client *dws.Client) ([]byte, error) { return messagesByIDsSDK(ctx, client, messageID) })
	if err != nil {
		return "", err
	}
	return parseMessageSender(raw, conversationID, messageID)
}

func parseExactMessage(raw []byte, conversationID, messageID string) (json.RawMessage, error) {
	invalid := errors.New("DWS source message identity could not be verified")
	if len(raw) > MaxResponseBytes {
		return nil, invalid
	}
	var envelope struct {
		OK       *bool             `json:"ok"`
		Success  *bool             `json:"success"`
		Data     json.RawMessage   `json:"data"`
		Result   json.RawMessage   `json:"result"`
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, invalid
	}
	if envelope.OK != nil {
		if !*envelope.OK || len(envelope.Data) == 0 {
			return nil, invalid
		}
		return parseExactMessage(envelope.Data, conversationID, messageID)
	}
	if envelope.Success == nil || !*envelope.Success {
		return nil, invalid
	}
	messages := envelope.Messages
	if len(envelope.Result) > 0 {
		var result struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if json.Unmarshal(envelope.Result, &result) != nil {
			return nil, invalid
		}
		if len(result.Messages) > 0 {
			messages = result.Messages
		}
	}
	if len(messages) != 1 {
		return nil, invalid
	}
	var message struct {
		MessageID            string `json:"messageId"`
		OpenMessageID        string `json:"openMessageId"`
		ConversationID       string `json:"conversationId"`
		OpenConversationID   string `json:"openConversationId"`
		SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
		SenderID             string `json:"senderId"`
	}
	if json.Unmarshal(messages[0], &message) != nil {
		return nil, invalid
	}
	match := func(a, b, want string) bool {
		return (a != "" || b != "") && (a == "" || a == want) && (b == "" || b == want)
	}
	if !match(message.MessageID, message.OpenMessageID, messageID) || !match(message.ConversationID, message.OpenConversationID, conversationID) {
		return nil, invalid
	}
	return messages[0], nil
}

func parseMessageSender(raw []byte, conversationID, messageID string) (string, error) {
	messageRaw, err := parseExactMessage(raw, conversationID, messageID)
	if err != nil {
		return "", err
	}
	var message struct {
		SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
		SenderID             string `json:"senderId"`
	}
	if json.Unmarshal(messageRaw, &message) != nil {
		return "", errors.New("DWS source message identity could not be verified")
	}
	invalid := errors.New("DWS source message identity could not be verified")
	sender := strings.TrimSpace(message.SenderOpenDingTalkID)
	if sender == "" || strings.ContainsAny(sender, " ,<>\t\n\r") || message.SenderID != "" && message.SenderID != sender {
		return "", invalid
	}
	return sender, nil
}
