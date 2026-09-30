package service

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
)

// DEAP digital employees reach an Agent over A2A with two views of the same
// DingTalk message: the standard DingTalk event extension in
// message.metadata[URI] (event type, conversation, thread, quoted message,
// open DingTalk ids) and DEAP's private request context in
// params.metadata.context.attributes (sender staff id, corp id, user type,
// proactive-group flag, the employee's agentUuid, DEAP session and run).
//
// buildA2ADingTalkInboundPayload folds both into one inbound envelope whose
// field names follow the Router DispatchCommand (openConversationId,
// staffId, openMsgId, referencedMessage ...), so an A2A run and a Router
// dispatch describe the speaker and the 场域 the same way. The standard
// extension wins field by field; the private context fills what it lacks.
// Every value is caller-asserted: it tells the model who spoke and where, it
// never authenticates anyone or grants access.

const (
	a2aDingTalkInboundSchema = "multica.dingtalk_inbound.v1"

	a2aDingTalkFactsExtension = "dingtalk_event_extension"
	a2aDingTalkFactsContext   = "deap_request_context"

	a2aDingTalkMaxIDBytes       = 512
	a2aDingTalkMaxNameRunes     = 128
	a2aDingTalkMaxQuotedRunes   = 1000
	a2aDingTalkMaxAttachments   = 16
	a2aDingTalkEventSingle      = "im.message.single"
	a2aDingTalkEventGroup       = "im.message.group"
	a2aDingTalkEventMention     = "im.message.mention"
	a2aDingTalkConversationDM   = "single"
	a2aDingTalkConversationRoom = "group"
)

type a2aDingTalkInbound struct {
	Schema         string                          `json:"schema"`
	Source         a2aDingTalkInboundSource        `json:"source"`
	Event          a2aDingTalkInboundEvent         `json:"event"`
	Conversation   *a2aDingTalkInboundConversation `json:"conversation,omitempty"`
	Sender         *a2aDingTalkInboundSender       `json:"sender,omitempty"`
	Message        *a2aDingTalkInboundMessage      `json:"message,omitempty"`
	Receiver       *a2aDingTalkInboundReceiver     `json:"receiver,omitempty"`
	DEAP           *a2aDingTalkInboundDEAP         `json:"deap,omitempty"`
	FactsFrom      []string                        `json:"factsFrom"`
	RedactedFields []string                        `json:"redacted_fields,omitempty"`
}

type a2aDingTalkInboundSource struct {
	Platform  string `json:"platform"`
	Type      string `json:"type"`
	Transport string `json:"transport"`
	Provider  string `json:"provider"`
}

type a2aDingTalkInboundEvent struct {
	Type                  string `json:"type"`
	DingTalkEventType     string `json:"dingtalkEventType,omitempty"`
	EventID               string `json:"eventId,omitempty"`
	OccurredAt            string `json:"occurredAt,omitempty"`
	Addressed             *bool  `json:"addressed,omitempty"`
	ProactiveConversation bool   `json:"proactiveConversation,omitempty"`
}

type a2aDingTalkInboundConversation struct {
	OpenConversationID string                    `json:"openConversationId,omitempty"`
	Type               string                    `json:"type,omitempty"`
	Thread             *a2aDingTalkInboundThread `json:"thread,omitempty"`
}

type a2aDingTalkInboundThread struct {
	ID               string `json:"id"`
	RootMessageID    string `json:"rootMessageId,omitempty"`
	OpenConvThreadID string `json:"openConvThreadId,omitempty"`
}

type a2aDingTalkInboundSender struct {
	DisplayName    string `json:"displayName,omitempty"`
	StaffID        string `json:"staffId,omitempty"`
	OpenDingTalkID string `json:"openDingTalkId,omitempty"`
	CorpID         string `json:"corpId,omitempty"`
	UserType       string `json:"userType,omitempty"`
}

type a2aDingTalkInboundMessage struct {
	OpenMsgID         string                         `json:"openMsgId,omitempty"`
	CreateTime        string                         `json:"createTime,omitempty"`
	ReferencedMessage *a2aDingTalkInboundReference   `json:"referencedMessage,omitempty"`
	Attachments       []a2aDingTalkInboundAttachment `json:"attachments,omitempty"`
}

type a2aDingTalkInboundReference struct {
	OpenMsgID            string `json:"openMsgId,omitempty"`
	Text                 string `json:"text,omitempty"`
	SenderName           string `json:"senderName,omitempty"`
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId,omitempty"`
}

type a2aDingTalkInboundAttachment struct {
	Type            string `json:"type,omitempty"`
	ResourceID      string `json:"resourceId,omitempty"`
	ResourceIDType  string `json:"resourceIdType,omitempty"`
	DownloadCommand string `json:"downloadCommand,omitempty"`
	ExpiresAtMillis int64  `json:"expiresAtMillis,omitempty"`
}

// a2aDingTalkInboundReceiver is the digital employee that received the
// message. DWSUID/DWSOrgID come only from the operator binding on this Agent,
// never from the caller.
type a2aDingTalkInboundReceiver struct {
	DEAPAgentUUID string `json:"deapAgentUuid,omitempty"`
	Name          string `json:"name,omitempty"`
	DWSUID        string `json:"dwsUid,omitempty"`
	DWSOrgID      string `json:"dwsOrgId,omitempty"`
}

type a2aDingTalkInboundDEAP struct {
	SessionID        string `json:"sessionId,omitempty"`
	RunID            string `json:"runId,omitempty"`
	InvocationSource string `json:"invocationSource,omitempty"`
}

// a2aDingTalkBoundIdentity is the operator-bound employee identity for this
// Agent, or empty when the Agent has none.
type a2aDingTalkBoundIdentity struct {
	UID           string
	OrgID         string
	DEAPAgentUUID string
}

// buildA2ADingTalkInboundPayload returns the JSON object stored as the input
// chat message's source payload, or nil when the request carries no DingTalk
// facts (an ordinary A2A client).
func buildA2ADingTalkInboundPayload(
	requestMetadata map[string]any,
	messageMetadata map[string]any,
	bound a2aDingTalkBoundIdentity,
) []byte {
	inbound, ok := normalizeA2ADingTalkInbound(requestMetadata, messageMetadata, bound)
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(inbound)
	if err != nil {
		return nil
	}
	return encoded
}

func normalizeA2ADingTalkInbound(
	requestMetadata map[string]any,
	messageMetadata map[string]any,
	bound a2aDingTalkBoundIdentity,
) (a2aDingTalkInbound, bool) {
	event, hasEvent := a2aDingTalkEventFromMetadata(messageMetadata)
	attributes := a2aDEAPContextAttributes(requestMetadata)
	if !hasEvent && attributes == nil {
		return a2aDingTalkInbound{}, false
	}

	inbound := a2aDingTalkInbound{
		Schema: a2aDingTalkInboundSchema,
		Source: a2aDingTalkInboundSource{
			Platform:  "dingtalk",
			Type:      "digital_employee",
			Transport: "a2a",
			Provider:  "deap",
		},
		Event:     a2aDingTalkInboundEvent{Type: "message.created"},
		FactsFrom: []string{},
	}
	conversation := a2aDingTalkInboundConversation{}
	sender := a2aDingTalkInboundSender{}
	message := a2aDingTalkInboundMessage{}
	receiver := a2aDingTalkInboundReceiver{}
	deap := a2aDingTalkInboundDEAP{}

	if hasEvent {
		inbound.FactsFrom = append(inbound.FactsFrom, a2aDingTalkFactsExtension)
		inbound.Event.DingTalkEventType = event.eventType
		inbound.Event.EventID = event.eventID
		inbound.Event.OccurredAt = event.occurredAt
		addressed := event.eventType != a2aDingTalkEventGroup
		inbound.Event.Addressed = &addressed
		inbound.Event.ProactiveConversation = event.eventType == a2aDingTalkEventGroup
		conversation = event.conversation
		sender.DisplayName = event.senderName
		sender.OpenDingTalkID = event.senderOpenDingTalkID
		message.OpenMsgID = event.openMessageID
		message.CreateTime = event.createTime
		message.ReferencedMessage = event.quoted
		message.Attachments = event.attachments
		if event.redactedURLs {
			inbound.RedactedFields = append(inbound.RedactedFields, "message.attachments[].url")
		}
	}

	if attributes != nil {
		inbound.FactsFrom = append(inbound.FactsFrom, a2aDingTalkFactsContext)
		userInfo, _ := attributes["userInfo"].(map[string]any)
		sessionInfo, _ := attributes["sessionInfo"].(map[string]any)
		invocationInfo, _ := attributes["invocationInfo"].(map[string]any)
		agentInfo, _ := attributes["agentInfo"].(map[string]any)

		if conversation.OpenConversationID == "" {
			conversation.OpenConversationID = a2aDingTalkID(metadataStringAt(sessionInfo, "openConversationId"))
		}
		if conversation.Type == "" {
			switch metadataStringAt(invocationInfo, "dialogType") {
			case a2aDingTalkConversationDM:
				conversation.Type = a2aDingTalkConversationDM
			case a2aDingTalkConversationRoom:
				conversation.Type = a2aDingTalkConversationRoom
			}
		}
		proactive, _ := invocationInfo["isGroupProactiveResponse"].(bool)
		if inbound.Event.Addressed == nil && conversation.Type != "" {
			// DEAP forwards a group message only when the employee is @-ed or
			// the employee answers proactively; the flag tells the two apart.
			addressed := !(conversation.Type == a2aDingTalkConversationRoom && proactive)
			inbound.Event.Addressed = &addressed
			inbound.Event.ProactiveConversation = conversation.Type == a2aDingTalkConversationRoom && proactive
		}
		if sender.DisplayName == "" {
			sender.DisplayName = a2aDingTalkName(metadataStringAt(userInfo, "userName"))
		}
		sender.StaffID = a2aDingTalkID(metadataStringAt(userInfo, "userId"))
		sender.CorpID = a2aDingTalkID(metadataStringAt(userInfo, "corpId"))
		sender.UserType = a2aDingTalkID(metadataStringAt(userInfo, "userType"))

		receiver.DEAPAgentUUID = a2aDingTalkID(metadataStringAt(agentInfo, "agentCode"))
		receiver.Name = a2aDingTalkName(metadataStringAt(agentInfo, "agentName"))

		deap.SessionID = a2aDingTalkID(metadataStringAt(sessionInfo, "sessionId"))
		deap.RunID = a2aDingTalkID(metadataStringAt(sessionInfo, "runId"))
		deap.InvocationSource = a2aDingTalkID(metadataStringAt(invocationInfo, "invocationSource"))
	}

	if bound.UID != "" && bound.OrgID != "" {
		receiver.DWSUID = bound.UID
		receiver.DWSOrgID = bound.OrgID
		if receiver.DEAPAgentUUID == "" {
			receiver.DEAPAgentUUID = bound.DEAPAgentUUID
		}
	}

	if conversation != (a2aDingTalkInboundConversation{}) {
		inbound.Conversation = &conversation
	}
	if sender != (a2aDingTalkInboundSender{}) {
		inbound.Sender = &sender
	}
	if message.OpenMsgID != "" || message.CreateTime != "" || message.ReferencedMessage != nil || len(message.Attachments) > 0 {
		inbound.Message = &message
	}
	if receiver != (a2aDingTalkInboundReceiver{}) {
		inbound.Receiver = &receiver
	}
	if deap != (a2aDingTalkInboundDEAP{}) {
		inbound.DEAP = &deap
	}
	return inbound, true
}

// a2aDingTalkChatType maps a stored inbound envelope to the claim chat_type
// the daemon uses for its Audience line. Unknown shapes return "".
func a2aDingTalkChatType(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var envelope struct {
		Schema       string `json:"schema"`
		Conversation *struct {
			Type string `json:"type"`
		} `json:"conversation"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Schema != a2aDingTalkInboundSchema || envelope.Conversation == nil {
		return ""
	}
	switch envelope.Conversation.Type {
	case a2aDingTalkConversationRoom:
		return "group"
	case a2aDingTalkConversationDM:
		return "p2p"
	default:
		return ""
	}
}

// A2ADingTalkChatType is the exported form used by the daemon claim handler.
func A2ADingTalkChatType(payload []byte) string {
	return a2aDingTalkChatType(payload)
}

type a2aDingTalkEvent struct {
	eventID              string
	eventType            string
	occurredAt           string
	conversation         a2aDingTalkInboundConversation
	senderName           string
	senderOpenDingTalkID string
	openMessageID        string
	createTime           string
	quoted               *a2aDingTalkInboundReference
	attachments          []a2aDingTalkInboundAttachment
	redactedURLs         bool
}

// a2aDingTalkEventFromMetadata validates the standard extension payload the
// way the DEAP spec §9.2 asks a receiver to: a known event type, a
// conversation type that agrees with it, a conversation id, and a thread only
// on group conversations. An invalid event is ignored as a whole rather than
// half-trusted.
func a2aDingTalkEventFromMetadata(metadata map[string]any) (a2aDingTalkEvent, bool) {
	raw, ok := metadata[a2aintegration.DingTalkEventExtensionURI].(map[string]any)
	if !ok {
		return a2aDingTalkEvent{}, false
	}
	data, _ := raw["data"].(map[string]any)
	conversationRaw, _ := data["conversation"].(map[string]any)
	event := a2aDingTalkEvent{
		eventID:    a2aDingTalkID(metadataStringAt(raw, "eventId")),
		eventType:  metadataStringAt(raw, "eventType"),
		occurredAt: a2aDingTalkID(metadataStringAt(raw, "occurredAt")),
	}
	conversationType := metadataStringAt(conversationRaw, "type")
	switch event.eventType {
	case a2aDingTalkEventSingle:
		if conversationType != a2aDingTalkConversationDM {
			return a2aDingTalkEvent{}, false
		}
	case a2aDingTalkEventGroup, a2aDingTalkEventMention:
		if conversationType != a2aDingTalkConversationRoom {
			return a2aDingTalkEvent{}, false
		}
	default:
		return a2aDingTalkEvent{}, false
	}
	conversationID := a2aDingTalkID(metadataStringAt(conversationRaw, "openConversationId"))
	if conversationID == "" {
		return a2aDingTalkEvent{}, false
	}
	event.conversation = a2aDingTalkInboundConversation{
		OpenConversationID: conversationID,
		Type:               conversationType,
	}
	if threadRaw, present := conversationRaw["thread"].(map[string]any); present {
		if conversationType != a2aDingTalkConversationRoom {
			return a2aDingTalkEvent{}, false
		}
		thread := a2aDingTalkInboundThread{
			ID:               a2aDingTalkID(metadataStringAt(threadRaw, "id")),
			RootMessageID:    a2aDingTalkID(metadataStringAt(threadRaw, "rootMessageId")),
			OpenConvThreadID: a2aDingTalkID(metadataStringAt(threadRaw, "openConvThreadId")),
		}
		if thread.ID != "" {
			event.conversation.Thread = &thread
		}
	}
	event.senderName = a2aDingTalkName(metadataStringAt(data, "sender"))
	event.senderOpenDingTalkID = a2aDingTalkID(metadataStringAt(data, "senderOpenDingTalkId"))
	event.openMessageID = a2aDingTalkID(metadataStringAt(data, "openMessageId"))
	if event.openMessageID == "" {
		event.openMessageID = event.eventID
	}
	event.createTime = a2aDingTalkID(metadataStringAt(data, "createTime"))
	if quotedRaw, present := data["quotedMessage"].(map[string]any); present {
		quoted := a2aDingTalkInboundReference{
			OpenMsgID:            a2aDingTalkID(metadataStringAt(quotedRaw, "openMessageId")),
			Text:                 a2aDingTalkTruncate(metadataStringAt(quotedRaw, "content"), a2aDingTalkMaxQuotedRunes),
			SenderName:           a2aDingTalkName(metadataStringAt(quotedRaw, "sender")),
			SenderOpenDingTalkID: a2aDingTalkID(metadataStringAt(quotedRaw, "senderOpenDingTalkId")),
		}
		if quoted != (a2aDingTalkInboundReference{}) {
			event.quoted = &quoted
		}
	}
	if resources, present := data["resources"].([]any); present {
		for _, item := range resources {
			if len(event.attachments) >= a2aDingTalkMaxAttachments {
				break
			}
			resource, ok := item.(map[string]any)
			if !ok {
				continue
			}
			attachment := a2aDingTalkInboundAttachment{
				Type:            a2aDingTalkID(metadataStringAt(resource, "resourceType")),
				ResourceID:      a2aDingTalkID(metadataStringAt(resource, "resourceId")),
				ResourceIDType:  a2aDingTalkID(metadataStringAt(resource, "resourceIdType")),
				DownloadCommand: a2aDingTalkTruncate(metadataStringAt(resource, "downloadCommand"), 200),
			}
			if expires, ok := resource["expireTimeMillis"].(float64); ok && expires > 0 {
				attachment.ExpiresAtMillis = int64(expires)
			}
			// The signed download URL is a short-lived bearer credential.
			if metadataStringAt(resource, "url") != "" {
				event.redactedURLs = true
			}
			if attachment != (a2aDingTalkInboundAttachment{}) {
				event.attachments = append(event.attachments, attachment)
			}
		}
	}
	return event, true
}

// a2aDEAPContextAttributes locates DEAP's private request context. DEAP sends
// it under params.metadata.context.attributes; the shallower shapes are the
// same tolerances openConversationIDFromMetadata accepts.
func a2aDEAPContextAttributes(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	if contextRaw, ok := metadata["context"].(map[string]any); ok {
		if attributes, ok := contextRaw["attributes"].(map[string]any); ok && a2aDEAPAttributesLookValid(attributes) {
			return attributes
		}
	}
	if attributes, ok := metadata["attributes"].(map[string]any); ok && a2aDEAPAttributesLookValid(attributes) {
		return attributes
	}
	return nil
}

func a2aDEAPAttributesLookValid(attributes map[string]any) bool {
	for _, key := range []string{"userInfo", "sessionInfo", "invocationInfo", "agentInfo"} {
		if _, ok := attributes[key].(map[string]any); ok {
			return true
		}
	}
	return false
}

// a2aDingTalkID keeps an opaque identifier only when it fits the DEAP spec's
// 512-byte bound; an oversized value is dropped rather than truncated so a
// clipped id can never be mistaken for a real one.
func a2aDingTalkID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > a2aDingTalkMaxIDBytes || !utf8.ValidString(value) {
		return ""
	}
	return value
}

func a2aDingTalkName(value string) string {
	return a2aDingTalkTruncate(value, a2aDingTalkMaxNameRunes)
}

func a2aDingTalkTruncate(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) {
		return ""
	}
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes]) + "…"
}
