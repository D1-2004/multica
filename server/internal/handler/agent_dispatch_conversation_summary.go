package handler

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Retained only to validate and quietly receipt already queued Router digests.
const dispatchEventTypeConversationSummary = "conversation.summary"

// ConversationSummaryEventData describes retired transport input, never new work.
type ConversationSummaryEventData struct {
	SummaryID                   string          `json:"summaryId,omitempty"`
	SourceID                    string          `json:"sourceId,omitempty"`
	AgentID                     string          `json:"agentId,omitempty"`
	ConversationCid             string          `json:"conversationCid,omitempty"`
	TimeBucket                  string          `json:"timeBucket,omitempty"`
	BucketStart                 string          `json:"bucketStart,omitempty"`
	BucketEnd                   string          `json:"bucketEnd,omitempty"`
	MessageCount                int             `json:"messageCount,omitempty"`
	MentionCount                int             `json:"mentionCount,omitempty"`
	FollowedSenderMessageCount  int             `json:"followedSenderMessageCount,omitempty"`
	TopConversationMessageCount int             `json:"topConversationMessageCount,omitempty"`
	Attributes                  json.RawMessage `json:"attributes,omitempty"`
	FirstMessageAt              string          `json:"firstMessageAt,omitempty"`
	LastMessageAt               string          `json:"lastMessageAt,omitempty"`
}

// Validate the historical wire envelope before returning its quiet receipt.
func (c DispatchCommand) validateConversationSummary() error {
	if c.Source.Type != "digital_employee" {
		return errors.New("conversation.summary source must be digital_employee")
	}
	summary := c.Event.Data.ConversationSummary
	if summary == nil ||
		strings.TrimSpace(summary.SummaryID) == "" ||
		strings.TrimSpace(c.Event.Data.Conversation.OpenConversationID) == "" {
		return errors.New("conversation.summary requires conversationSummary.summaryId and conversation.openConversationId")
	}
	if c.Surface.Type != protocol.DispatchSurfaceTypeIssue &&
		c.Surface.Type != protocol.DispatchSurfaceTypeChat &&
		c.Surface.Type != protocol.DispatchSurfaceTypeAuto {
		return errors.New("conversation.summary surface.type must be issue, chat, or auto")
	}
	if c.Outbound.Mode != protocol.DispatchOutboundModeNone &&
		c.Outbound.Mode != protocol.DispatchOutboundModeDWS &&
		c.Outbound.Mode != protocol.DispatchOutboundModeRobotSDK {
		return errors.New("conversation.summary outbound.mode must be none, dws or robot_sdk")
	}
	if strings.TrimSpace(c.ExternalIdentity.ContextToken) == "" && c.ExternalIdentity.DWS == nil {
		return errors.New("conversation.summary externalIdentity.contextToken or externalIdentity.dws is required")
	}
	return nil
}
