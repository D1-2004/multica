package handler

// Dispatch Command 2.0 is deliberately kept as a structured event at the
// Multica boundary. The router owns routing/window state; Multica owns the
// projection into issue/comment display text and private runtime instructions.

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"golang.org/x/text/unicode/norm"
)

type DispatchSource struct {
	Platform string `json:"platform"`
	Type     string `json:"type"`
}

type DispatchEvent struct {
	Domain string            `json:"domain"`
	Type   string            `json:"type"`
	Data   DispatchEventData `json:"data"`
}

type DispatchConversation struct {
	OpenConversationID string `json:"openConversationId"`
	Type               string `json:"type,omitempty"`
	Title              string `json:"title,omitempty"`
}

type DispatchSender struct {
	DisplayName          string `json:"displayName,omitempty"`
	OpenDingTalkID       string `json:"openDingTalkId,omitempty"`
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId,omitempty"`
	StaffID              string `json:"staffId,omitempty"`
}

type DispatchAttachment struct {
	Type        string `json:"type,omitempty"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	DownloadURL string `json:"downloadUrl,omitempty"`
	ExpiresAt   *int64 `json:"expiresAt,omitempty"`
}

type DispatchMessage struct {
	OpenMsgID   string               `json:"openMsgId"`
	OccurredAt  int64                `json:"occurredAt"`
	Text        string               `json:"text,omitempty"`
	Attachments []DispatchAttachment `json:"attachments,omitempty"`
}

type DispatchCalendarAttendee struct {
	UID            string `json:"uid"`
	ResponseStatus *int   `json:"responseStatus,omitempty"`
	Optional       *bool  `json:"optional,omitempty"`
}

// ApprovalEventData carries approval-specific routing locators. They remain
// opaque to display prompts; only AIReadableContent is rendered to the agent.
type ApprovalEventData struct {
	FormCode          string   `json:"formCode"`
	OriginatorUid     string   `json:"originatorUid"`
	ApproverUids      []string `json:"approverUids"`
	CcUids            []string `json:"ccUids,omitempty"`
	NodeType          string   `json:"nodeType"`
	Status            string   `json:"status"`
	ConversationID    string   `json:"conversationId,omitempty"`
	AIReadableContent string   `json:"aiReadableContent"`
}

// DispatchEventData keeps all platform routing locators in domain data. The
// optional fields are intentionally opaque to PromptBuilder and are only used
// by outbound strategies; they are never rendered into Issue/Comment content.
type DispatchEventData struct {
	Conversation       DispatchConversation       `json:"conversation"`
	Sender             DispatchSender             `json:"sender"`
	Messages           []DispatchMessage          `json:"messages"`
	CalendarID         string                     `json:"calendarId,omitempty"`
	Subject            string                     `json:"subject,omitempty"`
	Comment            string                     `json:"comment,omitempty"`
	StartTime          *int64                     `json:"startTime,omitempty"`
	EndTime            *int64                     `json:"endTime,omitempty"`
	Timezone           string                     `json:"timezone,omitempty"`
	AllDayEvent        bool                       `json:"allDayEvent,omitempty"`
	BelongOrgID        string                     `json:"belongOrgId,omitempty"`
	Organizers         []string                   `json:"organizers,omitempty"`
	Attendees          []DispatchCalendarAttendee `json:"attendees,omitempty"`
	Location           string                     `json:"location,omitempty"`
	DetailURL          string                     `json:"detailUrl,omitempty"`
	VideoConferenceURL string                     `json:"videoConferenceUrl,omitempty"`
	AIReadableContent  string                     `json:"aiReadableContent,omitempty"`
	Approval           *ApprovalEventData         `json:"approval,omitempty"`
	Reply              json.RawMessage            `json:"reply,omitempty"`
	Reference          json.RawMessage            `json:"reference,omitempty"`
	Reaction           json.RawMessage            `json:"reaction,omitempty"`
}

type DispatchSurface struct {
	Type string `json:"type"`
}

type DispatchOutbound struct {
	Mode    string `json:"mode"`
	ReplyTo string `json:"replyTo,omitempty"`
}

type DispatchCompletionCallback struct {
	URL       string `json:"url"`
	UpdateURL string `json:"updateUrl,omitempty"`
	Target    string `json:"-"`
}

type DispatchCommand struct {
	SchemaVersion      string                        `json:"schemaVersion"`
	AgentID            string                        `json:"agentId,omitempty"`
	Continuation       *AgentDispatchContinuation    `json:"continuation"`
	Source             DispatchSource                `json:"source"`
	Event              DispatchEvent                 `json:"event"`
	Surface            DispatchSurface               `json:"surface"`
	Outbound           DispatchOutbound              `json:"outbound"`
	ContextPrompt      string                        `json:"contextPrompt,omitempty"`
	ExternalIdentity   AgentDispatchExternalIdentity `json:"externalIdentity"`
	CompletionCallback *DispatchCompletionCallback   `json:"completionCallback,omitempty"`
	DispatchEndpointID string                        `json:"-"`
}

type DispatchPrompt struct {
	DisplayContent string
}

type dispatchPromptBuilderKey struct {
	Domain     string
	EventType  string
	SourceType string
}

type dispatchPromptStrategy func(DispatchCommand) DispatchPrompt

// DispatchPromptBuilder is the single structured-event projection boundary.
// Adding a domain, event type, or source requires an explicit strategy
// registration instead of prompt assembly in an HTTP handler.
type DispatchPromptBuilder struct {
	strategies map[dispatchPromptBuilderKey]dispatchPromptStrategy
}

func NewDispatchPromptBuilder() *DispatchPromptBuilder {
	builder := &DispatchPromptBuilder{strategies: make(map[dispatchPromptBuilderKey]dispatchPromptStrategy)}
	builder.register("channel", "message.created", "robot", buildDingTalkRobotPrompt)
	builder.register("channel", "message.created", "digital_employee", buildDingTalkDigitalEmployeePrompt)
	builder.register("calendar", "calendar.started", "digital_employee", buildDingTalkCalendarStartedPrompt)
	builder.register("approval", "approval.status_changed", "digital_employee", buildApprovalStatusChangedPrompt)
	return builder
}

func (b *DispatchPromptBuilder) register(domain, eventType, sourceType string, strategy dispatchPromptStrategy) {
	b.strategies[dispatchPromptBuilderKey{Domain: domain, EventType: eventType, SourceType: sourceType}] = strategy
}

func (b *DispatchPromptBuilder) Build(c DispatchCommand) (DispatchPrompt, error) {
	return b.build(c)
}

func (b *DispatchPromptBuilder) build(c DispatchCommand) (DispatchPrompt, error) {
	if b == nil {
		return DispatchPrompt{}, errors.New("dispatch prompt builder is not configured")
	}
	key := dispatchPromptBuilderKey{Domain: c.Event.Domain, EventType: c.Event.Type, SourceType: c.Source.Type}
	strategy, ok := b.strategies[key]
	if !ok {
		return DispatchPrompt{}, fmt.Errorf("unsupported dispatch prompt strategy: %s/%s/%s", key.Domain, key.EventType, key.SourceType)
	}
	return strategy(c), nil
}

func buildDispatchInstruction(flags *featureflag.Service, surfaceType, contextPrompt string) string {
	return joinDispatchPromptSections(
		resolveDispatchRuntimePrompt(flags, featureflag.DispatchCommonRuntimePromptFlagKey),
		resolveSurfaceRuntimePrompt(flags, surfaceType),
		contextPrompt,
	)
}

func resolveDispatchRuntimePrompt(flags *featureflag.Service, flagKey string) string {
	configured := strings.TrimSpace(flags.Variant(context.Background(), flagKey, ""))
	if configured == "" || configured == "off" {
		return ""
	}
	return configured
}

func resolveSurfaceRuntimePrompt(flags *featureflag.Service, surfaceType string) string {
	var flagKey string
	switch surfaceType {
	case protocol.DispatchSurfaceTypeIssue:
		flagKey = featureflag.DispatchIssueRuntimePromptFlagKey
	case protocol.DispatchSurfaceTypeChat:
		flagKey = featureflag.DispatchChatRuntimePromptFlagKey
	case protocol.DispatchSurfaceTypeAuto:
		flagKey = featureflag.DispatchAutoRuntimePromptFlagKey
	default:
		return ""
	}
	return resolveDispatchRuntimePrompt(flags, flagKey)
}

var defaultDispatchPromptBuilder = NewDispatchPromptBuilder()
var routerCompletionCallbackPattern = regexp.MustCompile(`^/api/v1/dispatch-tasks/([A-Za-z0-9_-]{1,128})/execution-result$`)
var routerExecutionUpdateCallbackPattern = regexp.MustCompile(`^/api/v1/dispatch-tasks/([A-Za-z0-9_-]{1,128})/execution-update$`)
var routerCompletionTargetPattern = regexp.MustCompile(`^router-target:v1:sha256:[a-f0-9]{64}$`)

func (c DispatchCommand) validate() error {
	if c.SchemaVersion != "2.0" {
		return errors.New("schemaVersion must be 2.0")
	}
	if c.Source.Platform != "dingtalk" || (c.Source.Type != "robot" && c.Source.Type != "digital_employee") {
		return errors.New("source must be dingtalk robot or digital_employee")
	}
	if c.Event.Domain == "calendar" && c.Event.Type == "calendar.started" {
		if err := c.validateCalendarStarted(); err != nil {
			return err
		}
	} else if c.Event.Domain == "approval" && c.Event.Type == "approval.status_changed" {
		if err := c.validateApprovalStatusChanged(); err != nil {
			return err
		}
	} else if err := c.validateChannelMessageCreated(); err != nil {
		return err
	}
	if !validDispatchContextToken(c.ExternalIdentity.ContextToken) {
		return errors.New("externalIdentity.contextToken is invalid")
	}
	if c.ExternalIdentity.ContextToken == "" {
		if c.ExternalIdentity.ExpiresAt != 0 {
			return errors.New("externalIdentity.contextToken is required when expiresAt is present")
		}
	} else if c.ExternalIdentity.ExpiresAt <= 0 {
		return errors.New("externalIdentity.expiresAt is invalid")
	}
	if c.ExternalIdentity.DWS != nil &&
		(!validDispatchDWSIdentifier(c.ExternalIdentity.DWS.UID) ||
			!validDispatchDWSIdentifier(c.ExternalIdentity.DWS.OrgID)) {
		return errors.New("externalIdentity.dws uid and orgId must be decimal identifiers")
	}
	// Callback presence alone selects durable terminal delivery. An absent
	// callback keeps the direct Streaming and rolling legacy behavior; source
	// type and outbound mode do not select completion semantics.
	if c.CompletionCallback != nil {
		if !routerCompletionCallbackPattern.MatchString(c.CompletionCallback.URL) {
			return errors.New("completionCallback.url is invalid")
		}
		if c.CompletionCallback.UpdateURL != "" &&
			!routerExecutionUpdateCallbackPattern.MatchString(c.CompletionCallback.UpdateURL) {
			return errors.New("completionCallback.updateUrl is invalid")
		}
		if c.CompletionCallback.UpdateURL != "" {
			resultMatch := routerCompletionCallbackPattern.FindStringSubmatch(c.CompletionCallback.URL)
			updateMatch := routerExecutionUpdateCallbackPattern.FindStringSubmatch(c.CompletionCallback.UpdateURL)
			if len(resultMatch) != 2 || len(updateMatch) != 2 || resultMatch[1] != updateMatch[1] {
				return errors.New("completionCallback urls must reference the same dispatch task")
			}
		}
	}
	if c.Continuation == nil && strings.TrimSpace(c.AgentID) == "" {
		return errors.New("agentId is required for first dispatch")
	}
	if c.Continuation != nil && strings.TrimSpace(c.AgentID) != "" {
		return errors.New("agentId and continuation are mutually exclusive")
	}
	return nil
}

func (c DispatchCommand) validateChannelMessageCreated() error {
	if c.Event.Domain != "channel" || c.Event.Type != "message.created" {
		return errors.New("event must be channel/message.created, calendar/calendar.started or approval/approval.status_changed")
	}
	if strings.TrimSpace(c.Event.Data.Conversation.OpenConversationID) == "" || len(c.Event.Data.Messages) == 0 {
		return errors.New("event.data conversation and messages are required")
	}
	if c.Source.Type == "robot" && strings.TrimSpace(c.Event.Data.Sender.OpenDingTalkID) == "" && strings.TrimSpace(c.Event.Data.Sender.SenderOpenDingTalkID) == "" && strings.TrimSpace(c.Event.Data.Sender.StaffID) == "" {
		return errors.New("event.data.sender identity is required")
	}
	for _, m := range c.Event.Data.Messages {
		if strings.TrimSpace(m.OpenMsgID) == "" || strings.TrimSpace(m.Text) == "" && len(m.Attachments) == 0 {
			return errors.New("each message needs openMsgId and text or attachment")
		}
	}
	if c.Surface.Type != protocol.DispatchSurfaceTypeIssue &&
		c.Surface.Type != protocol.DispatchSurfaceTypeChat &&
		c.Surface.Type != protocol.DispatchSurfaceTypeAuto {
		return errors.New("surface.type must be issue, chat, or auto")
	}
	if c.Outbound.Mode != protocol.DispatchOutboundModeDWS && c.Outbound.Mode != protocol.DispatchOutboundModeRobotSDK {
		return errors.New("outbound.mode must be dws or robot_sdk")
	}
	if c.Outbound.ReplyTo != protocol.DispatchReplyToLatestMessage {
		return errors.New("outbound.replyTo must be latest_message")
	}
	return nil
}

func (c DispatchCommand) validateCalendarStarted() error {
	if c.Source.Type != "digital_employee" {
		return errors.New("calendar.started source must be digital_employee")
	}
	if strings.TrimSpace(c.Event.Data.CalendarID) == "" || strings.TrimSpace(c.Event.Data.Subject) == "" || c.Event.Data.StartTime == nil || strings.TrimSpace(c.Event.Data.AIReadableContent) == "" {
		return errors.New("calendar.started requires calendarId, subject, startTime and aiReadableContent")
	}
	if c.Surface.Type != protocol.DispatchSurfaceTypeIssue {
		return errors.New("calendar.started surface.type must be issue")
	}
	if c.Outbound.Mode != protocol.DispatchOutboundModeNone &&
		c.Outbound.Mode != protocol.DispatchOutboundModeDWS &&
		c.Outbound.Mode != protocol.DispatchOutboundModeRobotSDK {
		return errors.New("calendar.started outbound.mode must be none, dws or robot_sdk")
	}
	if c.Outbound.Mode == protocol.DispatchOutboundModeNone {
		if strings.TrimSpace(c.Outbound.ReplyTo) != "" {
			return errors.New("calendar.started outbound.replyTo must be empty when mode is none")
		}
	} else if c.Outbound.ReplyTo != protocol.DispatchReplyToLatestMessage {
		return errors.New("calendar.started outbound.replyTo must be latest_message when mode is dws or robot_sdk")
	}
	if strings.TrimSpace(c.ExternalIdentity.ContextToken) == "" && c.ExternalIdentity.DWS == nil {
		return errors.New("calendar.started externalIdentity.contextToken or externalIdentity.dws is required")
	}
	return nil
}

func (c DispatchCommand) validateApprovalStatusChanged() error {
	if c.Source.Type != "digital_employee" {
		return errors.New("approval source must be digital_employee")
	}
	if c.Event.Data.Approval == nil ||
		strings.TrimSpace(c.Event.Data.Approval.FormCode) == "" ||
		len(c.Event.Data.Approval.ApproverUids) == 0 ||
		strings.TrimSpace(c.Event.Data.Approval.AIReadableContent) == "" {
		return errors.New("approval requires formCode, approverUids and aiReadableContent")
	}
	if c.Surface.Type != protocol.DispatchSurfaceTypeIssue {
		return errors.New("approval surface.type must be issue")
	}
	if c.Outbound.Mode != protocol.DispatchOutboundModeNone &&
		c.Outbound.Mode != protocol.DispatchOutboundModeDWS &&
		c.Outbound.Mode != protocol.DispatchOutboundModeRobotSDK {
		return errors.New("approval outbound.mode must be none, dws or robot_sdk")
	}
	if c.Outbound.Mode == protocol.DispatchOutboundModeNone {
		if strings.TrimSpace(c.Outbound.ReplyTo) != "" {
			return errors.New("approval outbound.replyTo must be empty when mode is none")
		}
	} else if c.Outbound.ReplyTo != protocol.DispatchReplyToLatestMessage {
		return errors.New("approval outbound.replyTo must be latest_message when mode is dws or robot_sdk")
	}
	if strings.TrimSpace(c.ExternalIdentity.ContextToken) == "" && c.ExternalIdentity.DWS == nil {
		return errors.New("approval externalIdentity.contextToken or externalIdentity.dws is required")
	}
	return nil
}

// shouldSkipApprovalDispatch returns true when the DingTalk approval engine
// handles the node automatically (auto_approve). In that case Multica does not
// create an issue or dispatch to the agent — DingTalk itself passes the node
// and advances the approval flow. Only non-auto_approve nodes (e.g., a human
// approver in a previous node, or a node that requires agent judgment) reach
// the agent.
func shouldSkipApprovalDispatch(command DispatchCommand) bool {
	return command.Event.Domain == "approval" &&
		command.Event.Type == "approval.status_changed" &&
		command.Event.Data.Approval != nil &&
		strings.TrimSpace(command.Event.Data.Approval.NodeType) == "auto_approve"
}

// extractIssueIdentifierFromApprovalContent scans the AIReadableContent of an
// approval event for an issue identifier matching the workspace's issue prefix
// (e.g. "WS-50"). The identifier is written by the agent into the form field
// named 关联Issue when it creates the approval instance. When the approval
// status changes, the Router includes form values in AIReadableContent, and
// this function recovers the identifier so Mutica can link the approval event
// back to the original issue (creating a continuation/comment instead of a
// new issue).
func extractIssueIdentifierFromApprovalContent(content, issuePrefix string) string {
	issuePrefix = strings.TrimSpace(issuePrefix)
	if issuePrefix == "" || strings.TrimSpace(content) == "" {
		return ""
	}
	pattern := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(issuePrefix) + `-(\d+)\b`)
	match := pattern.FindString(strings.TrimSpace(content))
	if match == "" {
		return ""
	}
	return strings.ToUpper(match)
}
func validDispatchContextToken(token string) bool {
	if token == "" {
		return true
	}
	if strings.TrimSpace(token) != token || len(token) > 8192 {
		return false
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validDispatchDWSIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// BuildDispatchPrompt has a strict visibility split. IDs, reply locators,
// security text and DWS instructions never enter DisplayContent.
func BuildDispatchPrompt(c DispatchCommand) (DispatchPrompt, error) {
	return defaultDispatchPromptBuilder.Build(c)
}

func buildDingTalkRobotPrompt(c DispatchCommand) DispatchPrompt {
	return buildDingTalkPrompt(c)
}

func buildDingTalkDigitalEmployeePrompt(c DispatchCommand) DispatchPrompt {
	return buildDingTalkPrompt(c)
}

func buildDingTalkCalendarStartedPrompt(c DispatchCommand) DispatchPrompt {
	return DispatchPrompt{
		DisplayContent: strings.TrimSpace(c.Event.Data.AIReadableContent) + "\n",
	}
}

func buildApprovalStatusChangedPrompt(c DispatchCommand) DispatchPrompt {
	a := c.Event.Data.Approval
	return DispatchPrompt{
		DisplayContent: strings.TrimSpace(a.AIReadableContent) + "\n",
	}
}

func buildDingTalkPrompt(c DispatchCommand) DispatchPrompt {
	return DispatchPrompt{
		DisplayContent: buildDingTalkChannelDisplay(c),
	}
}

type persistedDispatchContext struct {
	Source        DispatchSource   `json:"dispatch_source"`
	Domain        string           `json:"dispatch_domain"`
	Type          string           `json:"dispatch_type"`
	Surface       DispatchSurface  `json:"dispatch_surface"`
	Outbound      DispatchOutbound `json:"dispatch_outbound"`
	ContextPrompt string           `json:"dispatch_context_prompt"`
}

// applyDingTalkDispatchPromptToExistingTaskFields rebuilds the claim-scoped
// instruction from current Diamond configuration and the persisted Router
// context. User-authored issue, comment, and chat fields remain untouched.
func applyDingTalkDispatchPromptToExistingTaskFields(response *AgentTaskResponse, rawContext []byte) {
	applyDingTalkDispatchPromptToExistingTaskFieldsWithFeatureFlags(response, rawContext, nil)
}

func applyDingTalkDispatchPromptToExistingTaskFieldsWithFeatureFlags(
	response *AgentTaskResponse,
	rawContext []byte,
	flags *featureflag.Service,
) {
	if response == nil || len(rawContext) == 0 {
		return
	}
	var stored persistedDispatchContext
	if err := json.Unmarshal(rawContext, &stored); err != nil {
		return
	}
	if stored.Source.Platform != "dingtalk" {
		return
	}
	channelMessage := stored.Domain == "channel" &&
		stored.Type == "message.created" &&
		stored.Outbound.ReplyTo == protocol.DispatchReplyToLatestMessage &&
		(stored.Surface.Type == protocol.DispatchSurfaceTypeIssue ||
			stored.Surface.Type == protocol.DispatchSurfaceTypeChat ||
			stored.Surface.Type == protocol.DispatchSurfaceTypeAuto) &&
		(stored.Outbound.Mode == protocol.DispatchOutboundModeDWS ||
			stored.Outbound.Mode == protocol.DispatchOutboundModeRobotSDK)
	calendarIssue := stored.Source.Type == "digital_employee" &&
		stored.Domain == "calendar" && stored.Type == "calendar.started" &&
		stored.Surface.Type == protocol.DispatchSurfaceTypeIssue &&
		(stored.Outbound.Mode == protocol.DispatchOutboundModeNone ||
			stored.Outbound.Mode == protocol.DispatchOutboundModeDWS ||
			stored.Outbound.Mode == protocol.DispatchOutboundModeRobotSDK)
	approvalIssue := stored.Source.Type == "digital_employee" &&
		stored.Domain == "approval" && stored.Type == "approval.status_changed" &&
		stored.Surface.Type == protocol.DispatchSurfaceTypeIssue &&
		(stored.Outbound.Mode == protocol.DispatchOutboundModeDWS ||
			stored.Outbound.Mode == protocol.DispatchOutboundModeRobotSDK)
	if !channelMessage && !calendarIssue && !approvalIssue {
		return
	}
	response.Instruction = buildDispatchInstruction(flags, stored.Surface.Type, stored.ContextPrompt)
}

func joinDispatchPromptSections(sections ...string) string {
	nonEmpty := make([]string, 0, len(sections))
	for _, section := range sections {
		if trimmed := strings.TrimSpace(section); trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}

func buildDingTalkChannelDisplay(c DispatchCommand) string {
	var b strings.Builder
	if name := strings.TrimSpace(c.Event.Data.Sender.DisplayName); name != "" {
		b.WriteString(name)
		b.WriteString(" 在钉钉会话中的消息：\n\n")
	} else {
		b.WriteString("钉钉会话消息：\n\n")
	}
	for i, m := range c.Event.Data.Messages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		text := strings.TrimSpace(m.Text)
		hasMessageContent := false
		if text != "" {
			b.WriteString(text)
			hasMessageContent = true
		}
		for _, a := range m.Attachments {
			if hasMessageContent {
				b.WriteString("\n")
			}
			b.WriteString(dispatchAttachmentDisplay(a))
			hasMessageContent = true
		}
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func dispatchAttachmentDisplay(a DispatchAttachment) string {
	if name := strings.TrimSpace(a.Name); name != "" {
		return "附件：" + name
	}
	if contentType := strings.TrimSpace(a.ContentType); contentType != "" {
		return "附件（" + contentType + "）"
	}
	if attachmentType := strings.TrimSpace(a.Type); attachmentType != "" {
		return "附件（" + attachmentType + "）"
	}
	return "附件"
}

func dispatchWindowIdempotencyKey(c DispatchCommand) string {
	if c.Event.Domain == "calendar" && c.Event.Type == "calendar.started" {
		startTime := int64(0)
		if c.Event.Data.StartTime != nil {
			startTime = *c.Event.Data.StartTime
		}
		return fmt.Sprintf("calendar:%s:%d", strings.TrimSpace(c.Event.Data.CalendarID), startTime)
	}
	if c.Event.Domain == "approval" && c.Event.Type == "approval.status_changed" && c.Event.Data.Approval != nil {
		return fmt.Sprintf("approval:%s:%s", strings.TrimSpace(c.Event.Data.Approval.FormCode), strings.TrimSpace(c.Event.Data.Approval.Status))
	}
	// The router intentionally keeps window IDs internal. Stable message IDs
	// provide the same key across transport retries without leaking IDs into
	// the visible issue/comment text.
	b, _ := json.Marshal(c.Event.Data.Messages)
	h := sha256.Sum256(b)
	return "dispatch-window:" + hex.EncodeToString(h[:])
}

func dispatchIssueTitle(c DispatchCommand, idempotencyKey string) string {
	shortCode := dispatchEventShortCode(idempotencyKey)
	if c.Event.Domain == "calendar" && c.Event.Type == "calendar.started" {
		subject := normalizeDispatchTitleFragment(c.Event.Data.Subject)
		if subject == "" {
			subject = "钉钉日程"
		}
		return truncateDispatchTitle(
			"【钉钉·日程】"+subject+"｜"+dispatchCalendarTitleTime(c.Event.Data),
			shortCode,
		)
	}
	if c.Event.Domain == "approval" && c.Event.Type == "approval.status_changed" && c.Event.Data.Approval != nil {
		formCode := normalizeDispatchTitleFragment(c.Event.Data.Approval.FormCode)
		if formCode == "" {
			formCode = "钉钉审批"
		}
		return truncateDispatchTitle("审批单："+formCode, shortCode)
	}

	summary := ""
	for _, message := range c.Event.Data.Messages {
		if text := normalizeDispatchTitleFragment(message.Text); text != "" {
			summary = text
			break
		}
	}
	if summary == "" {
		summary = firstDispatchAttachmentTitleFragment(c.Event.Data.Messages, func(a DispatchAttachment) string {
			return a.Name
		})
	}
	if summary == "" {
		summary = firstDispatchAttachmentTitleFragment(c.Event.Data.Messages, func(a DispatchAttachment) string {
			return a.ContentType
		})
	}
	if summary == "" {
		summary = firstDispatchAttachmentTitleFragment(c.Event.Data.Messages, func(a DispatchAttachment) string {
			return a.Type
		})
	}
	if summary == "" {
		summary = "钉钉消息"
	}

	sender := normalizeDispatchTitleFragment(c.Event.Data.Sender.DisplayName)
	if sender == "" {
		sender = "钉钉用户"
	}

	var title string
	switch strings.ToLower(normalizeDispatchTitleFragment(c.Event.Data.Conversation.Type)) {
	case "single", "p2p", "private", "direct":
		title = "【钉钉·私聊】" + sender + "：" + summary
	case "group":
		conversation := normalizeDispatchTitleFragment(c.Event.Data.Conversation.Title)
		if conversation == "" {
			conversation = "钉钉群聊"
		}
		title = "【钉钉·群聊】" + conversation + "｜" + sender + "：" + summary
	default:
		conversation := normalizeDispatchTitleFragment(c.Event.Data.Conversation.Title)
		if conversation != "" {
			conversation += "｜"
		}
		title = "【钉钉消息】" + conversation + sender + "：" + summary
	}
	return truncateDispatchTitle(title, shortCode)
}

func dispatchEventShortCode(idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:])[:8]
}

func firstDispatchAttachmentTitleFragment(
	messages []DispatchMessage,
	value func(DispatchAttachment) string,
) string {
	for _, message := range messages {
		for _, attachment := range message.Attachments {
			if fragment := normalizeDispatchTitleFragment(value(attachment)); fragment != "" {
				return fragment
			}
		}
	}
	return ""
}

func normalizeDispatchTitleFragment(value string) string {
	value = norm.NFKC.String(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	return strings.TrimFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("|｜:：·•・/\\,，;；", r)
	})
}

func dispatchCalendarTitleTime(data DispatchEventData) string {
	if data.StartTime == nil {
		return "时间待定"
	}
	location := time.UTC
	if name := strings.TrimSpace(data.Timezone); name != "" {
		if loaded, err := time.LoadLocation(name); err == nil {
			location = loaded
		}
	}
	start := time.UnixMilli(*data.StartTime).In(location)
	if data.AllDayEvent {
		return start.Format("2006-01-02")
	}
	return start.Format("2006-01-02 15:04")
}

func truncateDispatchTitle(title, shortCode string) string {
	const maxRunes = 160
	suffix := " · " + shortCode
	available := maxRunes - len([]rune(suffix))
	titleRunes := []rune(title)
	if len(titleRunes) > available {
		title = string(titleRunes[:available])
		title = strings.TrimRightFunc(title, func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune("|｜:：·•・/\\,，;；", r)
		})
	}
	return title + suffix
}
