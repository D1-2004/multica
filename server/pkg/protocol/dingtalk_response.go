package protocol

const (
	DingTalkResponsePolicyVersion   = 1
	DingTalkResponseModeCoordinator = "multica_coordinator"
	DingTalkResponseModeLegacy      = "legacy"
	DWSMessagePolicyCapability      = "dws_message_policy_v1"
)

// DingTalkResponsePolicy is frozen at ingress and never inferred from surface.
type DingTalkResponsePolicy struct {
	Version   int    `json:"version"`
	Mode      string `json:"mode"`
	Revision  int64  `json:"revision"`
	ShowAITag bool   `json:"showAiTag"`
}

func (p *DingTalkResponsePolicy) Valid() bool {
	return p != nil && p.Version == DingTalkResponsePolicyVersion && p.Revision > 0 &&
		(p.Mode == DingTalkResponseModeCoordinator || p.Mode == DingTalkResponseModeLegacy)
}

func (p *DingTalkResponsePolicy) Managed() bool {
	return p.Valid() && p.Mode == DingTalkResponseModeCoordinator
}

const (
	// DingTalkOriginOpenMsgIDMetadataKey is frozen on Issue create and never
	// overwritten by later comments. Host outbound uses it when the current
	// task did not stamp a more specific inbound message.
	DingTalkOriginOpenMsgIDMetadataKey = "dingtalk.origin_open_msg_id"
	// DingTalkReplyToOpenMsgIDContextKey is the inbound openMsgId this task
	// should quote. IndependentIssueTaskContext preserves unknown keys.
	DingTalkReplyToOpenMsgIDContextKey = "dingtalk_reply_to_open_msg_id"
)

// DingTalkMessagePolicy is trusted per-task state, not agent custom_env.
type DingTalkMessagePolicy struct {
	ShowAITag                bool   `json:"show_ai_tag"`
	PlatformManagedLifecycle bool   `json:"platform_managed_lifecycle"`
	ReplyToOpenMsgID         string `json:"reply_to_open_msg_id,omitempty"`
	ReplyConversationID      string `json:"reply_conversation_id,omitempty"`
	// ReplyToSenderOpenDingTalkID is who wrote ReplyToOpenMsgID. A quote reply
	// is auto-addressed to them, so the rewrite drops a duplicate placeholder.
	ReplyToSenderOpenDingTalkID string `json:"reply_to_sender_open_dingtalk_id,omitempty"`
}

// DingTalkResponseReceipt reports delivery separately from task execution.
type DingTalkResponseReceipt struct {
	RequestID          string `json:"requestId"`
	AgentID            string `json:"agentId"`
	ActionID           string `json:"actionId"`
	State              string `json:"state"`
	OccurredAt         int64  `json:"occurredAt"`
	OpenTaskID         string `json:"openTaskId,omitempty"`
	OpenConversationID string `json:"openConversationId,omitempty"`
	OpenMessageID      string `json:"openMessageId,omitempty"`
	ErrorCode          string `json:"errorCode,omitempty"`
}

// DingTalkSendReceipt is task-local evidence from the managed DWS wrapper.
// Pending is persisted before invoking DWS; accepted never proves delivery.
type DingTalkSendReceipt struct {
	ClientActionID          string `json:"clientActionId"`
	State                   string `json:"state"`
	OpenTaskID              string `json:"openTaskId,omitempty"`
	OpenConversationID      string `json:"openConversationId,omitempty"`
	OpenMessageID           string `json:"openMessageId,omitempty"`
	IdempotencyKey          string `json:"idempotencyKey,omitempty"`
	RecipientOpenDingTalkID string `json:"recipientOpenDingTalkId,omitempty"`
	PayloadHash             string `json:"payloadHash"`
	ErrorCode               string `json:"errorCode,omitempty"`
}
