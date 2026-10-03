package dwsclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// VerifyMessageFile reads the provider's exact delivered message. Resource text
// projections and quoted-message resources cannot prove an outgoing file.
func (c CLI) VerifyMessageFile(ctx context.Context, dir, conversationID, messageID string) (bool, error) {
	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(messageID) == "" || strings.Contains(messageID, ",") {
		return false, errors.New("DWS delivered message reference is incomplete")
	}
	raw, err := c.messageOp(ctx, dir, []string{"chat", "message", "list-by-ids", "--msg-ids", messageID, "--format", "json"}, func(client *dws.Client) ([]byte, error) {
		// Do not use MessagesByIDsOutput: its resourceRefs also include links parsed
		// from text and quoted messages, which are useful reads but not file proof.
		raw, err := client.CallRaw(ctx, dws.ServerIM, "list_messages_by_ids", map[string]any{"openMsgIds": []string{messageID}})
		if err != nil {
			return nil, err
		}
		return sdkBounded(raw)
	})
	if err != nil {
		return false, err
	}
	return parseMessageFile(raw, conversationID, messageID)
}
func parseMessageFile(raw []byte, conversationID, messageID string) (bool, error) {
	message, err := parseExactMessage(raw, conversationID, messageID)
	if err != nil {
		return false, err
	}
	var body struct {
		Resources []struct {
			ID     string `json:"resourceId"`
			IDType string `json:"resourceIdType"`
			Type   string `json:"resourceType"`
		} `json:"resources"`
	}
	if json.Unmarshal(message, &body) != nil {
		return false, errors.New("DWS file resources could not be verified")
	}
	for _, resource := range body.Resources {
		if resource.Type == "file" && resource.IDType == "fileId" && strings.TrimSpace(resource.ID) != "" {
			return true, nil
		}
	}
	return false, nil
}
