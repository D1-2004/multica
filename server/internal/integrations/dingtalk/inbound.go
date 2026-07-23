package dingtalk

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// This file holds the translation from a DingTalk bot-message callback
// (the /v1.0/im/bot/messages/get payload, SDK chatbot.BotCallbackDataModel)
// to the engine's normalized channel.InboundMessage.

// botCallbackData is the bot-message callback payload. Field set matches
// the official SDK's BotCallbackDataModel; unknown fields are ignored.
type botCallbackData struct {
	ConversationID string `json:"conversationId"`
	// RobotCode is present on live Stream callbacks even though older SDK
	// models omitted it. It is the authoritative robot identity for APIs that
	// address the receiving bot, including processing-emotion reply/recall.
	RobotCode string `json:"robotCode"`
	AtUsers   []struct {
		DingtalkID string `json:"dingtalkId"`
		StaffID    string `json:"staffId"`
	} `json:"atUsers"`
	ChatbotUserID             string `json:"chatbotUserId"`
	MsgID                     string `json:"msgId"`
	SenderNick                string `json:"senderNick"`
	SenderStaffID             string `json:"senderStaffId"`
	SessionWebhook            string `json:"sessionWebhook"`
	SessionWebhookExpiredTime int64  `json:"sessionWebhookExpiredTime"`
	CreateAt                  int64  `json:"createAt"`
	SenderCorpID              string `json:"senderCorpId"`
	ConversationType          string `json:"conversationType"` // "1" = DM, "2" = group
	SenderID                  string `json:"senderId"`
	ConversationTitle         string `json:"conversationTitle"`
	Msgtype                   string `json:"msgtype"`
	Text                      struct {
		Content string `json:"content"`
	} `json:"text"`
	// Content carries the non-plain-text payloads.
	Content richTextContent `json:"content"`
}

// richTextContent is the content envelope of a richText callback.
type richTextContent struct {
	RichText     []richTextNode    `json:"richText"`
	CardContent  []cardContentNode `json:"cardContent"`
	DownloadCode string            `json:"downloadCode"`
	FileName     string            `json:"fileName"`
}

// cardContentNode is one node of the interactiveCard callback's ordered
// RICHTEXT tree. The live 2950 callback exposes readable runs as TEXT values
// and destinations as LINK values; it does not expose the rendered link label
// or an image resource.
type cardContentNode struct {
	ElementType string            `json:"elementType"`
	Value       string            `json:"value"`
	Children    []cardContentNode `json:"children"`
}

// richTextNode is one node of the richText list: a text run, or a media
// node discriminated by `type` (text runs carry no type).
type richTextNode struct {
	Text         string `json:"text"`
	Type         string `json:"type"`
	DownloadCode string `json:"downloadCode"`
	FileName     string `json:"fileName"`
}

// msgtypeRichText is the callback msgtype for formatted messages. A
// quote-reply, a copy-paste that keeps formatting, or an image+text mix
// all arrive as richText — with text.content EMPTY and the real content
// in content.richText.
const msgtypeRichText = "richText"

// commandView strips leading @-mention tokens when a slash command follows
// them. Messages sent through the API (other bots, CLI tools) carry the
// robot's mention as literal text — "@Multica /new" — which would hide the
// command from the first-token parsers; real-client mentions are stripped
// by DingTalk before delivery and never reach text.content. The stripped
// view is adopted ONLY when the remainder starts with "/": for anything
// else the mentions are kept — they are content ("@张三 请跟进").
func commandView(text string) string {
	lines := strings.SplitN(text, "\n", 2)
	rest := strings.TrimLeft(lines[0], " \t")
	stripped := false
	for strings.HasPrefix(rest, "@") {
		sp := strings.IndexAny(rest, " \t")
		if sp < 0 {
			return text // a lone mention; no command can follow
		}
		rest = strings.TrimLeft(rest[sp:], " \t")
		stripped = true
	}
	if !stripped || !strings.HasPrefix(rest, "/") {
		return text
	}
	if len(lines) == 2 {
		return rest + "\n" + lines[1]
	}
	return rest
}

// flattenRichText renders a richText callback's node list to plain text.
// Text runs are concatenated verbatim (DingTalk encodes line breaks
// inside the runs); media nodes retain a readable placeholder while their
// download credentials are imported separately.
func flattenRichText(data botCallbackData) string {
	var b strings.Builder
	for _, node := range data.Content.RichText {
		switch {
		case node.Text != "":
			b.WriteString(node.Text)
		case node.Type == "picture":
			b.WriteString("[Image]")
		}
	}
	return strings.TrimSpace(b.String())
}

type dingtalkRawAttachment struct {
	Type         channel.MsgType `json:"type"`
	DownloadCode string          `json:"download_code"`
	FileName     string          `json:"file_name,omitempty"`
}

func callbackAttachments(data botCallbackData) []dingtalkRawAttachment {
	attachments := make([]dingtalkRawAttachment, 0)
	if data.Msgtype == msgtypeRichText {
		for _, node := range data.Content.RichText {
			code := strings.TrimSpace(node.DownloadCode)
			if code == "" {
				continue
			}
			switch node.Type {
			case "picture":
				attachments = append(attachments, dingtalkRawAttachment{Type: channel.MsgTypeImage, DownloadCode: code})
			case "file":
				attachments = append(attachments, dingtalkRawAttachment{Type: channel.MsgTypeFile, DownloadCode: code, FileName: strings.TrimSpace(node.FileName)})
			}
		}
		return attachments
	}

	code := strings.TrimSpace(data.Content.DownloadCode)
	if code == "" {
		return attachments
	}
	switch data.Msgtype {
	case "picture":
		attachments = append(attachments, dingtalkRawAttachment{Type: channel.MsgTypeImage, DownloadCode: code})
	case "file":
		attachments = append(attachments, dingtalkRawAttachment{Type: channel.MsgTypeFile, DownloadCode: code, FileName: strings.TrimSpace(data.Content.FileName)})
	}
	return attachments
}

// flattenInteractiveCard preserves the callback tree's display order while
// retaining only the two value-bearing node kinds observed in a 2950 card.
func flattenInteractiveCard(data botCallbackData) string {
	parts := make([]string, 0)
	var visit func(cardContentNode)
	visit = func(node cardContentNode) {
		switch node.ElementType {
		case "TEXT", "LINK":
			if value := strings.TrimSpace(node.Value); value != "" {
				parts = append(parts, value)
			}
		}
		for _, child := range node.Children {
			visit(child)
		}
	}
	for _, root := range data.Content.CardContent {
		visit(root)
	}
	return strings.Join(parts, "\n")
}

// dingtalkRawEvent carries the DingTalk-specific fields the cross-platform
// envelope does not — read back only inside the dingtalk resolvers/replier
// (the core never reads Raw).
type dingtalkRawEvent struct {
	// ClientID is the installation routing key: the app the connection
	// belongs to. Stamped by the channel (which knows its own identity)
	// rather than read from the payload, mirroring how each Slack
	// connection only ever delivers its own app's events.
	ClientID string `json:"client_id"`
	// RobotCode comes from the authenticated Stream callback payload. Keep it
	// with the message because historical Stream installations predate
	// robot_code persistence and client_id is a different identifier.
	RobotCode string `json:"robot_code,omitempty"`
	// InstallationID is the immutable admission-time routing fence. Durable
	// inbox callbacks must resolve this exact row instead of following an app_id
	// that may have been reclaimed by another workspace before processing.
	InstallationID string `json:"installation_id,omitempty"`
	// SessionWebhook is the per-message reply webhook (valid ~90 min);
	// the OutboundReplier posts verdict replies through it with no
	// access token or API permission needed.
	SessionWebhook            string `json:"session_webhook,omitempty"`
	SessionWebhookExpiredTime int64  `json:"session_webhook_expired_time,omitempty"`
	SenderStaffID             string `json:"sender_staff_id,omitempty"`
	// SenderUID and SenderOrgID are trusted Gateway projections used by the
	// HTTP Callback transport. Stream callbacks leave them empty and resolve
	// the same identity from senderCorpId + senderStaffId through OrgEmp HSF.
	SenderUID   string `json:"sender_uid,omitempty"`
	SenderOrgID string `json:"sender_org_id,omitempty"`
	// AgentIdentityContextToken is a short-lived opaque credential supplied by
	// the authenticated Agent Message Router callback. Stream callbacks leave
	// it empty and use the local sender resolution path instead.
	AgentIdentityContextToken string          `json:"agent_identity_context_token,omitempty"`
	DispatchContext           json.RawMessage `json:"dispatch_context,omitempty"`
	SenderCorpID              string          `json:"sender_corp_id,omitempty"`
	SenderNick                string          `json:"sender_nick,omitempty"`
	ConversationTitle         string          `json:"conversation_title,omitempty"`
	Msgtype                   string          `json:"msgtype,omitempty"`
	// MessageAttachments carry short-lived credentials only to the DingTalk
	// attachment importer. They must never be logged or persisted as URLs.
	MessageAttachments []dingtalkRawAttachment `json:"message_attachments,omitempty"`
	// CreateAt is the callback's epoch-millisecond send time; the typing
	// indicator uses it to skip stale redeliveries after a reconnect.
	CreateAt int64 `json:"create_at,omitempty"`
	// StreamSource is Multica transport metadata, not part of DingTalk's
	// callback. The durable inbox stamps it after decrypting the callback so it
	// can follow the task without exposing the callback payload.
	StreamSource *protocol.DingTalkStreamSource `json:"stream_source,omitempty"`
}

// AgentDispatchMessage is trusted DingTalk context projected by Agent Message
// Router. It intentionally excludes transport credentials: robot callbacks add
// their installation routing separately, while digital-employee dispatch uses
// the authenticated endpoint namespace selected by the channel engine caller.
type AgentDispatchMessage struct {
	ConversationID    string
	ConversationType  string
	ConversationTitle string
	MessageID         string
	CreatedAt         int64
	// SenderID is the platform sender identifier used only for chat routing
	// and per-sender session isolation. SenderUID/SenderOrgID are reserved for
	// the trusted numeric DWS identity pair.
	SenderID             string
	SenderUID            string
	SenderOrgID          string
	SenderStaffID        string
	SenderName           string
	Text                 string
	IdentityContextToken string
	DispatchContext      json.RawMessage
}

// HTTPCallbackMessage keeps the robot callback transport explicit at call
// sites. Its installation is validated before normalization.
type HTTPCallbackMessage AgentDispatchMessage

var errAgentDispatchMessageIDRequired = errors.New("dingtalk: agent dispatch message id is required")

type agentDispatchContextEncodingError struct {
	cause error
}

func (e *agentDispatchContextEncodingError) Error() string {
	return fmt.Sprintf("dingtalk: encode agent dispatch context: %v", e.cause)
}

func (e *agentDispatchContextEncodingError) Unwrap() error {
	return e.cause
}

// InboundFromAgentDispatch normalizes trusted non-robot dispatch context. The
// authenticated caller supplies the durable endpoint namespace separately via
// engine.HandleOptions.InstallationOverride.
func InboundFromAgentDispatch(in AgentDispatchMessage) (channel.InboundMessage, error) {
	return inboundFromAgentDispatch(in, "", "")
}

// InboundFromHTTPCallback adapts a Router callback into the same channel
// message consumed by DingTalk Stream. installationID is resolved from the
// authenticated Agent + robot account before this function is called.
func InboundFromHTTPCallback(in HTTPCallbackMessage, clientID, installationID string) (channel.InboundMessage, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(installationID) == "" {
		return channel.InboundMessage{}, errors.New("dingtalk: HTTP callback installation is required")
	}
	message, err := inboundFromAgentDispatch(AgentDispatchMessage(in), clientID, installationID)
	if err == nil {
		return message, nil
	}
	if errors.Is(err, errAgentDispatchMessageIDRequired) {
		return channel.InboundMessage{}, errors.New("dingtalk: HTTP callback message id is required")
	}
	var encodingErr *agentDispatchContextEncodingError
	if errors.As(err, &encodingErr) {
		return channel.InboundMessage{}, fmt.Errorf("dingtalk: encode HTTP callback context: %w", encodingErr.cause)
	}
	return channel.InboundMessage{}, err
}

func inboundFromAgentDispatch(in AgentDispatchMessage, clientID, installationID string) (channel.InboundMessage, error) {
	conversationType := "2"
	if strings.EqualFold(strings.TrimSpace(in.ConversationType), "single") {
		conversationType = "1"
	}
	routeSenderID := strings.TrimSpace(in.SenderID)
	if routeSenderID == "" {
		routeSenderID = strings.TrimSpace(in.SenderUID)
	}
	msg, ok := inboundFromBotCallbackForInstallation(botCallbackData{
		ConversationID:    strings.TrimSpace(in.ConversationID),
		MsgID:             strings.TrimSpace(in.MessageID),
		SenderNick:        strings.TrimSpace(in.SenderName),
		SenderStaffID:     strings.TrimSpace(in.SenderStaffID),
		CreateAt:          in.CreatedAt,
		ConversationType:  conversationType,
		SenderID:          routeSenderID,
		ConversationTitle: strings.TrimSpace(in.ConversationTitle),
		Msgtype:           "text",
		Text: struct {
			Content string `json:"content"`
		}{Content: in.Text},
	}, strings.TrimSpace(clientID), strings.TrimSpace(installationID), protocol.DingTalkStreamSource{})
	if !ok {
		return channel.InboundMessage{}, errAgentDispatchMessageIDRequired
	}
	raw, err := decodeDingTalkRaw(msg)
	if err != nil {
		return channel.InboundMessage{}, err
	}
	raw.SenderUID = strings.TrimSpace(in.SenderUID)
	raw.SenderOrgID = strings.TrimSpace(in.SenderOrgID)
	raw.AgentIdentityContextToken = strings.TrimSpace(in.IdentityContextToken)
	raw.DispatchContext = append(json.RawMessage(nil), in.DispatchContext...)
	msg.Raw, err = json.Marshal(raw)
	if err != nil {
		return channel.InboundMessage{}, &agentDispatchContextEncodingError{cause: err}
	}
	return msg, nil
}

// inboundFromBotCallback normalizes one bot-message callback. ok=false
// drops payloads that must not reach the core (no message id — nothing
// to dedup on).
func inboundFromBotCallback(data botCallbackData, clientID string) (channel.InboundMessage, bool) {
	return inboundFromBotCallbackForInstallation(data, clientID, "", protocol.DingTalkStreamSource{})
}

// inboundFromBotCallbackForInstallation keeps the transport-neutral callback
// entry point used by the HTTP callback adapter. Stream ingestion calls the
// explicit WithSource variant below because it also has the decrypted original
// callback available for credential stripping and Agent handoff.
func inboundFromBotCallbackForInstallation(data botCallbackData, clientID, installationID string, streamSource protocol.DingTalkStreamSource) (channel.InboundMessage, bool) {
	return inboundFromBotCallbackForInstallationWithSource(data, clientID, installationID, streamSource, nil)
}

func inboundFromBotCallbackForInstallationWithSource(data botCallbackData, clientID, installationID string, streamSource protocol.DingTalkStreamSource, sourcePayload json.RawMessage) (channel.InboundMessage, bool) {
	if data.MsgID == "" {
		return channel.InboundMessage{}, false
	}
	chatType := channel.ChatTypeGroup
	if data.ConversationType == "1" {
		chatType = channel.ChatTypeP2P
	}
	// SenderStaffID is the org-scoped stable user id; SenderID (the
	// encrypted dingtalkId) is the fallback for senders outside the
	// app's org, where staffId is absent.
	senderID := data.SenderStaffID
	if senderID == "" {
		senderID = data.SenderID
	}
	var rawStreamSource *protocol.DingTalkStreamSource
	if streamSource.Hostname != "" || streamSource.NodeID != "" || streamSource.ConnectionID != "" {
		copied := streamSource
		rawStreamSource = &copied
	}
	raw, _ := json.Marshal(dingtalkRawEvent{
		ClientID:                  clientID,
		RobotCode:                 strings.TrimSpace(data.RobotCode),
		InstallationID:            installationID,
		SessionWebhook:            data.SessionWebhook,
		SessionWebhookExpiredTime: data.SessionWebhookExpiredTime,
		SenderStaffID:             data.SenderStaffID,
		SenderCorpID:              data.SenderCorpID,
		SenderNick:                data.SenderNick,
		ConversationTitle:         data.ConversationTitle,
		Msgtype:                   data.Msgtype,
		MessageAttachments:        callbackAttachments(data),
		CreateAt:                  data.CreateAt,
		StreamSource:              rawStreamSource,
	})
	msgType := channel.MsgTypeText
	text := strings.TrimSpace(data.Text.Content)
	switch {
	case data.Msgtype == "text" || data.Msgtype == "":
		// Plain text — already extracted above.
		if text == "" {
			text = "[文本消息]"
		}
	case data.Msgtype == msgtypeRichText:
		// Formatted messages carry their content in content.richText and
		// leave text.content empty; flatten so they don't ingest as empty
		// messages (which read to the agent as "your message is blank").
		text = flattenRichText(data)
		if text == "" {
			text = "[富文本消息]"
			msgType = channel.MsgTypeUnknown
		}
	case data.Msgtype == "interactiveCard":
		text = flattenInteractiveCard(data)
		if text == "" {
			text = "[互动卡片]"
			msgType = channel.MsgTypeUnknown
		}
	case data.Msgtype == "picture":
		text = "[图片]"
		msgType = channel.MsgTypeImage
	case data.Msgtype == "file":
		text = "[文件]"
		if name := strings.TrimSpace(data.Content.FileName); name != "" {
			text += " " + name
		}
		msgType = channel.MsgTypeFile
	case data.Msgtype == "audio":
		text = "[语音消息]"
		msgType = channel.MsgTypeAudio
	case data.Msgtype == "video":
		text = "[视频消息]"
		msgType = channel.MsgTypeVideo
	default:
		text = "[消息类型: " + data.Msgtype + "]"
		msgType = channel.MsgTypeUnknown
	}
	// Leading @-mentions hide a slash command from the first-token parsers
	// (/new, /reset, /issue, /unbind); adopt the stripped view when — and
	// only when — a command follows.
	text = commandView(text)
	// /new (or /reset) on the first non-empty line forces a fresh agent
	// session for this dispatch (mirrors the Lark enricher): the directive
	// is stripped and the remainder is the prompt.
	forceFresh := false
	if stripped, ok := parseFreshSessionCommand(text); ok {
		text = stripped
		forceFresh = true
	}
	return channel.InboundMessage{
		EventID:    data.MsgID,
		MessageID:  data.MsgID,
		Type:       msgType,
		Text:       text,
		ForceFresh: forceFresh,
		// DingTalk only delivers group messages that @-mention the robot,
		// so every callback is, by construction, addressed to the bot.
		AddressedToBot: true,
		SourcePayload:  sourcePayload,
		Source: channel.Source{
			ChannelType: TypeDingtalk,
			ChatID:      data.ConversationID,
			ChatType:    chatType,
			SenderID:    senderID,
		},
		Raw: raw,
	}, true
}

func decodeDingTalkRaw(msg channel.InboundMessage) (dingtalkRawEvent, error) {
	var raw dingtalkRawEvent
	if len(msg.Raw) == 0 {
		return dingtalkRawEvent{}, errEmptyRaw
	}
	if err := json.Unmarshal(msg.Raw, &raw); err != nil {
		return dingtalkRawEvent{}, err
	}
	return raw, nil
}
