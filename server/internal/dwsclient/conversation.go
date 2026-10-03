package dwsclient

import (
	"context"
	"errors"
	"strings"
)

// ConversationTitle returns a conversation's title as id sees it, on id's
// shared SDK client (get_conversation_info; when DingTalk refuses an
// internal group, the identity's group list). "" means DingTalk named none.
// A native IM event carries no conversation title, so its group scene is
// named this way.
func (s Shared) ConversationTitle(ctx context.Context, id Identity, base IdentityMint, conversationID string) (string, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", errors.New("DWS conversation title needs a conversation id")
	}
	client, err := s.Client(ctx, id, base)
	if err != nil {
		return "", err
	}
	group, err := client.Groups.Info(ctx, conversationID)
	if err != nil {
		return "", err
	}
	if group.ConversationID != "" && group.ConversationID != conversationID {
		return "", errors.New("DWS conversation info names another conversation")
	}
	return strings.TrimSpace(group.Title), nil
}
