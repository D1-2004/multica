package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type fakeAgentDispatchDuplicateQueries struct {
	processedAt pgtype.Timestamptz
	binding     db.ChannelChatSessionBinding
	bindingKey  string
	taskID      pgtype.UUID
}

func (f *fakeAgentDispatchDuplicateQueries) GetChannelInboundDedupStatus(
	context.Context,
	db.GetChannelInboundDedupStatusParams,
) (pgtype.Timestamptz, error) {
	return f.processedAt, nil
}

func (f *fakeAgentDispatchDuplicateQueries) GetChannelChatSessionBinding(
	_ context.Context,
	params db.GetChannelChatSessionBindingParams,
) (db.ChannelChatSessionBinding, error) {
	f.bindingKey = params.ChannelChatID
	return f.binding, nil
}

func (f *fakeAgentDispatchDuplicateQueries) GetAgentDispatchTaskIDByMessage(
	context.Context,
	db.GetAgentDispatchTaskIDByMessageParams,
) (pgtype.UUID, error) {
	return f.taskID, nil
}

func TestBuildDispatchPromptSeparatesDisplayAndContextInstruction(t *testing.T) {
	c := DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-secret", Title: "项目群"},
			Sender:       DispatchSender{DisplayName: "张三", OpenDingTalkID: "open-secret", StaffID: "staff-secret"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-secret", Text: "请查看告警", Attachments: []DispatchAttachment{{Name: "log.txt", DownloadURL: "https://private.example/log"}}}},
		}},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: "ROUTER CONTEXT",
	}
	p := mustBuildDispatchPrompt(t, c)
	if !strings.Contains(p.DisplayContent, "请查看告警") || !strings.Contains(p.DisplayContent, "log.txt") {
		t.Fatalf("display content missing message: %q", p.DisplayContent)
	}
	for _, secret := range []string{"cid-secret", "open-secret", "staff-secret", "msg-secret", "https://private.example"} {
		if strings.Contains(p.DisplayContent, secret) {
			t.Fatalf("display content leaked %q: %q", secret, p.DisplayContent)
		}
	}
	if instruction := buildDispatchInstructionForTest(nil, c.Surface.Type, nil, c.ContextPrompt); instruction != "ROUTER CONTEXT" {
		t.Fatalf("instruction = %q, want Router context only without configured prompts", instruction)
	}
}

func TestCalendarStartedDispatchUsesIssueWithoutOutboundReply(t *testing.T) {
	start := int64(1784217600000)
	c := DispatchCommand{
		SchemaVersion: "2.0", AgentID: "agent",
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "calendar", Type: "calendar.started", Data: DispatchEventData{
			CalendarID: "calendar-1", Subject: "项目评审会", StartTime: &start,
			Attendees:         []DispatchCalendarAttendee{{UID: "uid-secret"}},
			AIReadableContent: "日程「项目评审会」已经开始。\n请检查设计方案并推进待办。",
		}},
		Surface:          DispatchSurface{Type: "issue"},
		Outbound:         DispatchOutbound{Mode: "none"},
		ContextPrompt:    "CALENDAR CONTEXT",
		ExternalIdentity: AgentDispatchExternalIdentity{ContextToken: "context-token", ExpiresAt: 4102444800000},
	}
	if err := c.validate(); err != nil {
		t.Fatalf("valid calendar dispatch rejected: %v", err)
	}
	prompt := mustBuildDispatchPrompt(t, c)
	if !strings.Contains(prompt.DisplayContent, "项目评审会") || strings.Contains(prompt.DisplayContent, "uid-secret") {
		t.Fatalf("calendar display content = %q", prompt.DisplayContent)
	}
	if instruction := buildDispatchInstructionForTest(nil, c.Surface.Type, nil, c.ContextPrompt); instruction != "CALENDAR CONTEXT" {
		t.Fatalf("calendar instruction = %q", instruction)
	}
	if got := dispatchWindowIdempotencyKey(c); got != "calendar:calendar-1:1784217600000" {
		t.Fatalf("calendar idempotency key = %q", got)
	}
	if got := dispatchIssueTitle(c, dispatchWindowIdempotencyKey(c)); got != "【钉钉·日程】项目评审会｜2026-07-16 16:00 · UZMQZZ4C" {
		t.Fatalf("calendar issue title = %q", got)
	}

	missingToken := c
	missingToken.ExternalIdentity.ContextToken = ""
	if err := missingToken.validate(); err == nil || !strings.Contains(err.Error(), "contextToken") {
		t.Fatalf("calendar dispatch without context token error = %v", err)
	}
}

func TestCalendarStartedDispatchWithDWSOutboundUsesRouterContextInstruction(t *testing.T) {
	start := int64(1784217600000)
	c := DispatchCommand{
		SchemaVersion: "2.0", AgentID: "agent",
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "calendar", Type: "calendar.started", Data: DispatchEventData{
			CalendarID: "calendar-1", Subject: "项目评审会", StartTime: &start,
			Attendees:         []DispatchCalendarAttendee{{UID: "uid-secret"}},
			AIReadableContent: "日程「项目评审会」已经开始。\n请检查设计方案并推进待办。",
			Conversation:      DispatchConversation{OpenConversationID: "cid-calendar"},
			Sender:            DispatchSender{DisplayName: "日程助手", OpenDingTalkID: "open-sender"},
			Messages:          []DispatchMessage{{OpenMsgID: "msg-calendar", Text: "日程通知"}},
		}},
		Surface:       DispatchSurface{Type: "issue"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: "CALENDAR DWS CONTEXT",
		ExternalIdentity: AgentDispatchExternalIdentity{
			DWS: &AgentDispatchDWSIdentity{UID: "123", OrgID: "456"},
		},
	}
	if err := c.validate(); err != nil {
		t.Fatalf("valid calendar dispatch with dws outbound rejected: %v", err)
	}
	if instruction := buildDispatchInstructionForTest(nil, c.Surface.Type, nil, c.ContextPrompt); instruction != "CALENDAR DWS CONTEXT" {
		t.Fatalf("calendar instruction = %q", instruction)
	}
}

func TestDispatchCommandValidateSourceOutboundAndIdentity(t *testing.T) {
	c := DispatchCommand{
		SchemaVersion: "2.0", AgentID: "agent",
		Source: DispatchSource{Platform: "dingtalk", Type: "robot"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid"},
			Sender:       DispatchSender{StaffID: "staff"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg", Text: "hello"}},
		}},
		Surface: DispatchSurface{Type: "chat"}, Outbound: DispatchOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
		ExternalIdentity: AgentDispatchExternalIdentity{
			ContextToken: "opaque",
			ExpiresAt:    4102444800000,
		},
	}
	if err := c.validate(); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}

	t.Run("robot without context token", func(t *testing.T) {
		command := c
		command.ExternalIdentity.ContextToken = ""
		command.ExternalIdentity.ExpiresAt = 0
		if err := command.validate(); err != nil {
			t.Fatalf("robot command without context token rejected: %v", err)
		}
	})

	t.Run("digital employee without sender platform identifiers", func(t *testing.T) {
		command := c
		command.Source.Type = "digital_employee"
		command.CompletionCallback = &DispatchCompletionCallback{
			URL: "/api/v1/dispatch-tasks/test-validation-no-sender/execution-result",
		}
		command.Surface.Type = "issue"
		command.Outbound.Mode = "dws"
		command.Event.Data.Sender = DispatchSender{DisplayName: "张三"}
		command.ExternalIdentity.ContextToken = ""
		command.ExternalIdentity.ExpiresAt = 0
		if err := command.validate(); err != nil {
			t.Fatalf("digital employee command without sender ids or context token rejected: %v", err)
		}
	})

	t.Run("surface outbound and callback are independent from source type", func(t *testing.T) {
		robotWithIssue := c
		robotWithIssue.Surface.Type = "issue"
		robotWithIssue.Outbound.Mode = "dws"
		robotWithIssue.CompletionCallback = &DispatchCompletionCallback{
			URL: "/api/v1/dispatch-tasks/test-validation-robot-issue/execution-result",
		}
		if err := robotWithIssue.validate(); err != nil {
			t.Fatalf("robot issue+dws+callback rejected: %v", err)
		}

		digitalEmployeeWithChat := c
		digitalEmployeeWithChat.Source.Type = "digital_employee"
		digitalEmployeeWithChat.CompletionCallback = &DispatchCompletionCallback{
			URL: "/api/v1/dispatch-tasks/test-validation-chat/execution-result",
		}
		digitalEmployeeWithChat.Surface.Type = "chat"
		digitalEmployeeWithChat.Outbound.Mode = "robot_sdk"
		if err := digitalEmployeeWithChat.validate(); err != nil {
			t.Fatalf("digital employee chat+robot_sdk rejected: %v", err)
		}

		digitalEmployeeWithAuto := c
		digitalEmployeeWithAuto.Source.Type = "digital_employee"
		digitalEmployeeWithAuto.CompletionCallback = &DispatchCompletionCallback{
			URL: "/api/v1/dispatch-tasks/test-validation-auto/execution-result",
		}
		digitalEmployeeWithAuto.Surface.Type = "auto"
		digitalEmployeeWithAuto.Outbound.Mode = "dws"
		if err := digitalEmployeeWithAuto.validate(); err != nil {
			t.Fatalf("digital employee auto+dws rejected: %v", err)
		}
	})

	t.Run("surface and outbound values remain closed", func(t *testing.T) {
		invalidSurface := c
		invalidSurface.Surface.Type = "ticket"
		if err := invalidSurface.validate(); err == nil || !strings.Contains(err.Error(), "surface.type") {
			t.Fatalf("invalid surface error = %v", err)
		}

		invalidOutbound := c
		invalidOutbound.Outbound.Mode = "webhook"
		if err := invalidOutbound.validate(); err == nil || !strings.Contains(err.Error(), "outbound.mode") {
			t.Fatalf("invalid outbound error = %v", err)
		}
	})

	t.Run("robot still requires stable sender identity", func(t *testing.T) {
		command := c
		command.Event.Data.Sender = DispatchSender{DisplayName: "张三"}
		if err := command.validate(); err == nil || !strings.Contains(err.Error(), "sender identity") {
			t.Fatalf("robot command without stable sender identity error = %v", err)
		}
	})

	t.Run("present context token is strictly validated", func(t *testing.T) {
		for _, invalid := range []string{" token-with-spaces ", "token\nwith-newline", strings.Repeat("x", 8193)} {
			command := c
			command.ExternalIdentity.ContextToken = invalid
			if err := command.validate(); err == nil || !strings.Contains(err.Error(), "externalIdentity.contextToken is invalid") {
				t.Fatalf("invalid context token error = %v", err)
			}
		}
	})

	t.Run("context token and expiry must be present together", func(t *testing.T) {
		missingExpiry := c
		missingExpiry.ExternalIdentity.ExpiresAt = 0
		if err := missingExpiry.validate(); err == nil || !strings.Contains(err.Error(), "externalIdentity.expiresAt") {
			t.Fatalf("missing expiry error = %v", err)
		}

		missingToken := c
		missingToken.ExternalIdentity.ContextToken = ""
		if err := missingToken.validate(); err == nil || !strings.Contains(err.Error(), "externalIdentity.contextToken") {
			t.Fatalf("missing token error = %v", err)
		}
	})
}

func TestDispatchCommandValidatesAndPersistsExternalDWSIdentity(t *testing.T) {
	valid := []byte(`{
		"schemaVersion":"2.0",
		"agentId":"agent",
		"source":{"platform":"dingtalk","type":"digital_employee"},
		"event":{"domain":"channel","type":"message.created","data":{
			"conversation":{"openConversationId":"cid"},
			"sender":{"displayName":"sender"},
			"messages":[{"openMsgId":"msg","text":"hello"}]
		}},
		"surface":{"type":"chat"},
		"outbound":{"mode":"dws","replyTo":"latest_message"},
		"externalIdentity":{"dws":{"uid":"24710833","orgId":"439446171"}}
	}`)
	var request AgentDispatchV2Request
	if err := json.Unmarshal(valid, &request); err != nil {
		t.Fatal(err)
	}
	command := request.DispatchCommand()
	if err := command.validate(); err != nil {
		t.Fatalf("DWS-only external identity rejected: %v", err)
	}
	contextJSON := string(dispatchRuntimeContext(command, "dispatch-dws-only"))
	for _, want := range []string{`"external_identity"`, `"dws"`, `"uid":"24710833"`, `"orgId":"439446171"`} {
		if !strings.Contains(contextJSON, want) {
			t.Fatalf("private dispatch context missing %s: %s", want, contextJSON)
		}
	}
	if strings.Contains(contextJSON, "contextToken") {
		t.Fatalf("private dispatch context used the wire token shape: %s", contextJSON)
	}

	for _, tc := range []struct {
		name string
		dws  string
	}{
		{name: "uid without org", dws: `{"uid":"24710833"}`},
		{name: "org without uid", dws: `{"orgId":"439446171"}`},
		{name: "non decimal uid", dws: `{"uid":"uid-1","orgId":"439446171"}`},
		{name: "non decimal org", dws: `{"uid":"24710833","orgId":"org-1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(strings.ReplaceAll(string(valid), `{"uid":"24710833","orgId":"439446171"}`, tc.dws))
			var invalid AgentDispatchV2Request
			if err := json.Unmarshal(raw, &invalid); err != nil {
				t.Fatal(err)
			}
			if err := invalid.DispatchCommand().validate(); err == nil || !strings.Contains(err.Error(), "externalIdentity.dws") {
				t.Fatalf("invalid externalIdentity.dws error = %v", err)
			}
		})
	}
}

func TestDispatchRuntimeContextCarriesIdentityExpiryWithoutDuplicatingToken(t *testing.T) {
	contextJSON := dispatchRuntimeContext(DispatchCommand{
		SchemaVersion: "2.0",
		ExternalIdentity: AgentDispatchExternalIdentity{
			ContextToken: "secret-context-token",
			ExpiresAt:    4102444800000,
		},
	}, "dispatch-key")
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload[protocol.AgentIdentityContextTokenJSONKey]; present {
		t.Fatalf("dispatch context duplicated ContextToken: %s", contextJSON)
	}
	var expiresAt int64
	if err := json.Unmarshal(payload[protocol.AgentIdentityContextTokenExpiresAtJSONKey], &expiresAt); err != nil {
		t.Fatalf("decode ContextToken expiry: %v", err)
	}
	if expiresAt != 4102444800000 {
		t.Fatalf("ContextToken expiry = %d", expiresAt)
	}
	var source string
	if err := json.Unmarshal(payload[protocol.AgentIdentityContextTokenSourceJSONKey], &source); err != nil {
		t.Fatalf("decode ContextToken source: %v", err)
	}
	if source != "external" {
		t.Fatalf("ContextToken source = %q", source)
	}
}

func TestBuildDispatchPromptRetainsAllMessagesAndSafeAttachmentDisplay(t *testing.T) {
	c := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "robot"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Sender: DispatchSender{DisplayName: "张三", OpenDingTalkID: "open-secret"},
			Messages: []DispatchMessage{
				{OpenMsgID: "msg-secret-1", Text: "第一条消息", Attachments: []DispatchAttachment{{Name: "log.txt", DownloadURL: "https://private.example/log"}}},
				{OpenMsgID: "msg-secret-2", Text: "第二条消息", Attachments: []DispatchAttachment{{ContentType: "application/pdf", DownloadURL: "https://private.example/pdf"}}},
			},
		}},
	}

	display := mustBuildDispatchPrompt(t, c).DisplayContent
	for _, visible := range []string{"张三", "第一条消息", "第二条消息", "附件：log.txt", "附件（application/pdf）"} {
		if !strings.Contains(display, visible) {
			t.Errorf("display content missing %q: %q", visible, display)
		}
	}
	for _, private := range []string{"open-secret", "msg-secret-1", "msg-secret-2", "https://private.example"} {
		if strings.Contains(display, private) {
			t.Errorf("display content leaked %q: %q", private, display)
		}
	}
}

func TestBuildDispatchPromptRendersReferencedMessageContext(t *testing.T) {
	c := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Sender: DispatchSender{DisplayName: "李四", OpenDingTalkID: "open-sender-secret"},
			Messages: []DispatchMessage{
				{
					OpenMsgID: "current-open-secret",
					Text:      "@助手 继续处理",
					ReferencedMessage: &DispatchReferencedMessage{
						MessageID: "referenced-message-secret",
						OpenMsgID: "referenced-open-secret",
						Text:      "我是被引用消息的 AI 可读内容",
						SenderUID: "referenced-sender-secret",
					},
				},
			},
		}},
	}

	display := mustBuildDispatchPrompt(t, c).DisplayContent
	for _, visible := range []string{
		"李四 本次发言",
		"@助手 继续处理",
		"李四 引用了其他人（不是你本数字员工）更早的一条消息作为背景，不是新指令",
		"> 我是被引用消息的 AI 可读内容",
	} {
		if !strings.Contains(display, visible) {
			t.Errorf("display content missing %q: %q", visible, display)
		}
	}
	// The current message has to lead: a quoted antecedent that opens the text
	// buries the request the sender actually made.
	if strings.Index(display, "@助手 继续处理") > strings.Index(display, "我是被引用消息的 AI 可读内容") {
		t.Errorf("quoted original precedes the current message: %q", display)
	}
	for _, private := range []string{"open-sender-secret", "current-open-secret", "referenced-message-secret", "referenced-open-secret", "referenced-sender-secret"} {
		if strings.Contains(display, private) {
			t.Errorf("display content leaked %q: %q", private, display)
		}
	}
}

func TestBuildDispatchPromptAttributesQuotedMessageToTheAgentItself(t *testing.T) {
	c := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Sender: DispatchSender{DisplayName: "冬翔", OpenDingTalkID: "open-sender"},
			Messages: []DispatchMessage{
				{
					OpenMsgID: "current-open",
					Text:      "@菲迪-FDE教练(菲迪) 好的 没问题",
					ReferencedMessage: &DispatchReferencedMessage{
						OpenMsgID: "referenced-open",
						Text:      "已完成：单聊消息已发给一栗。",
						SenderUID: "25698887",
					},
				},
			},
		}},
		ExternalIdentity: AgentDispatchExternalIdentity{
			DWS: &AgentDispatchDWSIdentity{UID: "25698887", OrgID: "77"},
		},
	}

	display := mustBuildDispatchPrompt(t, c).DisplayContent
	if !strings.Contains(display, "引用了你（本数字员工）自己更早的一条消息") {
		t.Fatalf("display content did not attribute the quote to the agent: %q", display)
	}
	if strings.Contains(display, "25698887") {
		t.Fatalf("display content leaked the DWS uid: %q", display)
	}
}

func TestBuildDispatchPromptAttributesQuotedMessageToTheCurrentSender(t *testing.T) {
	c := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Sender: DispatchSender{DisplayName: "冬翔", OpenDingTalkID: "open-sender"},
			Messages: []DispatchMessage{
				{
					OpenMsgID: "current-open",
					Text:      "补充一点",
					ReferencedMessage: &DispatchReferencedMessage{
						OpenMsgID: "referenced-open",
						Text:      "我刚才提的需求",
						SenderUID: "open-sender",
					},
				},
			},
		}},
	}

	display := mustBuildDispatchPrompt(t, c).DisplayContent
	if !strings.Contains(display, "引用了冬翔 自己更早的一条消息") {
		t.Fatalf("display content did not attribute the quote to the sender: %q", display)
	}
}

func TestBuildDispatchPromptBoundsQuotedOriginalAndReportsItsLength(t *testing.T) {
	original := strings.Repeat("原", dispatchQuotedDisplayMaxRunes+37)
	c := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Sender: DispatchSender{DisplayName: "冬翔"},
			Messages: []DispatchMessage{
				{
					OpenMsgID: "current-open",
					Text:      "按这个继续",
					ReferencedMessage: &DispatchReferencedMessage{
						OpenMsgID: "referenced-open",
						Text:      original,
						SenderUID: "someone-else",
					},
				},
			},
		}},
	}

	display := mustBuildDispatchPrompt(t, c).DisplayContent
	if strings.Contains(display, original) {
		t.Fatalf("display content inlined the untruncated original: %q", display)
	}
	if !strings.Contains(display, fmt.Sprintf("；原文共 %d 字，以下为前 %d 字", dispatchQuotedDisplayMaxRunes+37, dispatchQuotedDisplayMaxRunes)) {
		t.Fatalf("display content did not report the truncation: %q", display)
	}
	if !strings.Contains(display, "按这个继续") {
		t.Fatalf("display content dropped the current message: %q", display)
	}
	if !strings.Contains(display, "（已截断，关键细节请回读原文，不要凭摘要推测。）") {
		t.Fatalf("display content did not point at the read-back path: %q", display)
	}
}

func TestQuotedMessageInstructionCarriesRereadLocatorAndSelfAttribution(t *testing.T) {
	stored := persistedDispatchContext{
		Source:   DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Domain:   "channel",
		Type:     "message.created",
		Surface:  DispatchSurface{Type: "auto"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		EventData: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-trusted"},
			Sender:       DispatchSender{DisplayName: "冬翔", OpenDingTalkID: "open-sender"},
			Messages: []DispatchMessage{
				{
					OpenMsgID: "current-open",
					Text:      "好的 没问题",
					ReferencedMessage: &DispatchReferencedMessage{
						OpenMsgID: "referenced-open",
						Text:      strings.Repeat("长", dispatchQuotedDisplayMaxRunes+1),
						SenderUID: "25698887",
					},
				},
			},
		},
		ExternalIdentity: &persistedDispatchExternalIdentity{
			DWS: &AgentDispatchDWSIdentity{UID: "25698887", OrgID: "77"},
		},
	}

	segments := composeDispatchInstructionSegments(dispatchInstructionInputs{
		Stored:          stored,
		Present:         true,
		DingTalkContext: true,
	})
	var quoted *DispatchPromptSegment
	for i := range segments {
		if segments[i].ID == DispatchSegmentQuotedMessage {
			quoted = &segments[i]
		}
	}
	if quoted == nil {
		t.Fatal("quoted_message segment was not composed")
	}
	if !quoted.Included || quoted.Customizable {
		t.Fatalf("quoted_message segment = %+v, want included and non-customizable", *quoted)
	}
	// The read-back command is printed with real ids so it runs as written, and
	// the whole hint stays no longer than the excerpt it points past.
	want := "- referenced-open (501 chars, TRUNCATED, by you): " +
		"`dws chat message list-by-ids --msg-ids referenced-open --format json`"
	if !strings.Contains(quoted.EffectiveText, want) {
		t.Errorf("quoted_message instruction missing %q: %q", want, quoted.EffectiveText)
	}
	if !strings.Contains(quoted.EffectiveText, "--conversation-ids cid-trusted") {
		t.Errorf("quoted_message instruction missing the conversation fallback: %q", quoted.EffectiveText)
	}
	if runes := utf8.RuneCountInString(quoted.EffectiveText); runes > dispatchQuotedDisplayMaxRunes+len(want) {
		t.Errorf("quoted_message instruction is %d runes, longer than the excerpt it replaces", runes)
	}
	if !strings.Contains(instructionFromSegments(segments), "## Quoted DingTalk Message") {
		t.Fatal("composed instruction dropped the quoted-message segment")
	}
}

func TestQuotedMessageInstructionFallsBackToConversationListingWithoutLocator(t *testing.T) {
	stored := persistedDispatchContext{
		Source:   DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Domain:   "channel",
		Type:     "message.created",
		Surface:  DispatchSurface{Type: "auto"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		EventData: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-trusted"},
			Sender:       DispatchSender{DisplayName: "冬翔"},
			Messages: []DispatchMessage{
				{
					OpenMsgID: "current-open",
					Text:      "按这个继续",
					ReferencedMessage: &DispatchReferencedMessage{
						Text:      "被引用的原文",
						SenderUID: "someone-else",
					},
				},
			},
		},
	}

	instruction := buildDispatchQuotedMessageInstruction(stored)
	for _, want := range []string{
		"- quoted message id not supplied (6 chars, full, by uid someone-else)",
		"If that fails: `dws chat message search-advanced --conversation-ids cid-trusted --limit 50 --format json`",
	} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("quoted_message instruction missing %q: %q", want, instruction)
		}
	}
}

func TestQuotedMessageSegmentExcludedWithoutQuotedMessage(t *testing.T) {
	segments := composeDispatchInstructionSegments(dispatchInstructionInputs{
		Stored: persistedDispatchContext{
			Source:   DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
			Domain:   "channel",
			Type:     "message.created",
			Surface:  DispatchSurface{Type: "auto"},
			Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
			EventData: DispatchEventData{
				Messages: []DispatchMessage{{OpenMsgID: "current-open", Text: "普通消息"}},
			},
		},
		Present:         true,
		DingTalkContext: true,
	})
	for _, segment := range segments {
		if segment.ID != DispatchSegmentQuotedMessage {
			continue
		}
		if segment.Included || segment.ExcludedReason != "no_quoted_message" {
			t.Fatalf("quoted_message segment = %+v, want excluded as no_quoted_message", segment)
		}
		return
	}
	t.Fatal("quoted_message segment was not composed")
}

func TestDispatchMessageReferencedMessageAcceptsRouterAliases(t *testing.T) {
	raw := []byte(`{
		"schemaVersion": "2.0",
		"agentId": "agent",
		"source": {"platform": "dingtalk", "type": "digital_employee"},
		"event": {
			"domain": "channel",
			"type": "message.created",
			"data": {
				"conversation": {"openConversationId": "cid", "type": "group"},
				"sender": {"openDingTalkId": "open-sender", "displayName": "李四"},
				"messages": [{
					"openMsgId": "current-open",
					"occurredAt": 1787600000000,
					"text": "@助手 继续",
					"referencedMessage": {
						"messageId": "referenced-message",
						"msgId": "referenced-open",
						"content": "上一轮回复内容",
						"senderUid": "referenced-sender"
					}
				}]
			}
		},
		"surface": {"type": "issue"},
		"outbound": {"mode": "dws", "replyTo": "latest_message"}
	}`)
	var c DispatchCommand
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("referencedMessage aliases rejected at unmarshal: %v", err)
	}
	referenced := c.Event.Data.Messages[0].ReferencedMessage
	if referenced == nil {
		t.Fatal("referencedMessage was not decoded")
	}
	if referenced.MessageID != "referenced-message" ||
		referenced.OpenMsgID != "referenced-open" ||
		referenced.Text != "上一轮回复内容" ||
		referenced.SenderUID != "referenced-sender" {
		t.Fatalf("referencedMessage = %+v", referenced)
	}

	display := mustBuildDispatchPrompt(t, c).DisplayContent
	if !strings.Contains(display, "上一轮回复内容") || !strings.Contains(display, "@助手 继续") {
		t.Fatalf("display content missing referenced context: %q", display)
	}
	context := dispatchTaskContextForTest(t, c)
	for _, visible := range []string{`"referencedMessage"`, `"messageId":"referenced-message"`, `"openMsgId":"referenced-open"`, `"text":"上一轮回复内容"`, `"senderUid":"referenced-sender"`} {
		if !strings.Contains(string(context), visible) {
			t.Fatalf("dispatch context missing %s: %s", visible, string(context))
		}
	}
}

func TestDispatchPromptBuilderComposesCommonModeAndContext(t *testing.T) {
	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{
	  "common":{"prompt":"COMMON POLICY"},
	  "auto":{"prompt":"AUTO POLICY"}
	}`)); err != nil {
		t.Fatalf("seed Diamond prompt: %v", err)
	}
	flags := featureflag.NewService(provider)
	command := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "robot"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-1"},
			Sender:       DispatchSender{DisplayName: "张三", OpenDingTalkID: "open-user-1"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-1", Text: "处理告警"}},
		}},
		Surface:       DispatchSurface{Type: "auto"},
		Outbound:      DispatchOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
		ContextPrompt: "ROUTER CONTEXT",
	}

	instruction := buildDispatchInstructionForTest(flags, command.Surface.Type, nil, command.ContextPrompt)
	if want := "COMMON POLICY\n\nAUTO POLICY\n\nROUTER CONTEXT"; instruction != want {
		t.Fatalf("instruction = %q, want %q", instruction, want)
	}
	if _, _, err := provider.ApplyJSON([]byte(`{
	  "common":{"prompt":"UPDATED COMMON POLICY"},
	  "auto":{"prompt":"UPDATED AUTO POLICY"}
	}`)); err != nil {
		t.Fatalf("update Diamond prompt: %v", err)
	}
	instruction = buildDispatchInstructionForTest(flags, command.Surface.Type, nil, command.ContextPrompt)
	if want := "UPDATED COMMON POLICY\n\nUPDATED AUTO POLICY\n\nROUTER CONTEXT"; instruction != want {
		t.Fatalf("instruction after update = %q, want %q", instruction, want)
	}
}

func TestDispatchPromptBuilderUsesDiamondPromptForEverySurface(t *testing.T) {
	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{
	  "common":{"prompt":"COMMON POLICY"},
	  "issue":{"prompt":"ISSUE MODE POLICY"},
  "chat":{"prompt":"CHAT MODE POLICY"},
  "auto":{"prompt":"AUTO MODE POLICY"}
}`)); err != nil {
		t.Fatalf("seed Diamond prompts: %v", err)
	}
	flags := featureflag.NewService(provider)
	command := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "robot"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-1"},
			Sender:       DispatchSender{DisplayName: "张三", OpenDingTalkID: "open-user-1"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-1", Text: "处理告警"}},
		}},
		Outbound: DispatchOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
	}

	for surface, want := range map[string]string{
		"issue": "COMMON POLICY\n\nISSUE MODE POLICY\n\nROUTER CONTEXT",
		"chat":  "COMMON POLICY\n\nCHAT MODE POLICY\n\nROUTER CONTEXT",
		"auto":  "COMMON POLICY\n\nAUTO MODE POLICY\n\nROUTER CONTEXT",
	} {
		command.Surface.Type = surface
		command.ContextPrompt = "ROUTER CONTEXT"
		instruction := buildDispatchInstructionForTest(flags, command.Surface.Type, nil, command.ContextPrompt)
		if instruction != want {
			t.Errorf("surface %s instruction = %q, want %q", surface, instruction, want)
		}
	}
}

func TestDispatchPromptBuilderHasNoEmbeddedSurfacePromptFallback(t *testing.T) {
	command := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "robot"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-1"},
			Sender:       DispatchSender{DisplayName: "张三", OpenDingTalkID: "open-user-1"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-1", Text: "处理告警"}},
		}},
		Surface:       DispatchSurface{Type: "auto"},
		Outbound:      DispatchOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
		ContextPrompt: "ROUTER CONTEXT",
	}

	instruction := buildDispatchInstructionForTest(nil, command.Surface.Type, nil, command.ContextPrompt)
	if instruction != "ROUTER CONTEXT" {
		t.Fatalf("instruction without configured prompts = %q", instruction)
	}
}

func TestDispatchPromptBuilderDoesNotGenerateDWSInstructionFromStructuredData(t *testing.T) {
	command := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-trusted"},
			Sender:       DispatchSender{DisplayName: "张三", OpenDingTalkID: "open-sender-trusted"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-latest", Text: "处理任务"}},
		}},
		Surface:  DispatchSurface{Type: "chat"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	}
	instruction := buildDispatchInstructionForTest(nil, command.Surface.Type, nil, command.ContextPrompt)
	if instruction != "" {
		t.Fatalf("structured dispatch generated hard-coded instruction: %q", instruction)
	}
	unsupported := command
	unsupported.Event.Domain = "calendar"
	if _, err := BuildDispatchPrompt(unsupported); err == nil {
		t.Fatal("unregistered prompt strategy was accepted")
	}
}

func TestApplyDingTalkDispatchPromptReusesExistingTaskFields(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-trusted"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender-trusted"},
			Messages: []DispatchMessage{
				{OpenMsgID: "msg-older", Text: "第一条"},
				{OpenMsgID: "msg-latest", Text: "起来打球"},
			},
		}},
		Surface:       DispatchSurface{Type: "issue"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: "ROUTER CONTEXT",
	})
	commentID := "comment-1"

	for _, tc := range []struct {
		name     string
		response AgentTaskResponse
		content  func(AgentTaskResponse) string
		original string
	}{
		{
			name:     "initial issue assignment uses handoff note",
			response: AgentTaskResponse{IssueID: "issue-1", HandoffNote: "保留已有交接说明"},
			content:  func(response AgentTaskResponse) string { return response.HandoffNote },
			original: "保留已有交接说明",
		},
		{
			name: "issue continuation uses trigger comment content",
			response: AgentTaskResponse{
				IssueID:               "issue-1",
				TriggerCommentID:      &commentID,
				TriggerCommentContent: "起来打球",
			},
			content:  func(response AgentTaskResponse) string { return response.TriggerCommentContent },
			original: "起来打球",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applyDingTalkDispatchPromptToExistingTaskFields(&tc.response, context)
			content := tc.content(tc.response)
			if content != tc.original {
				t.Fatalf("user-visible task field changed: %q", content)
			}
			if !strings.HasPrefix(tc.response.Instruction, "ROUTER CONTEXT") {
				t.Fatalf("instruction = %q", tc.response.Instruction)
			}
			encoded, err := json.Marshal(tc.response)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{
				"dispatch_runtime_prompt",
				"dispatch_workflow_prompt",
				"dispatch_surface_type",
				"dispatch_outbound_mode",
			} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("claim response introduced dispatch wire field %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestApplyRouterDispatchPromptKeepsHandoffSeparateFromInstruction(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-trusted"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender-trusted"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-latest", Text: "在吗"}},
		}},
		Surface:       DispatchSurface{Type: "issue"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: "ROUTER CONTEXT",
		CompletionCallback: &DispatchCompletionCallback{
			URL: "/api/v1/dispatch-tasks/router-task-reaction/execution-result",
		},
	})
	response := AgentTaskResponse{IssueID: "issue-1", HandoffNote: "处理消息"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	if response.HandoffNote != "处理消息" {
		t.Fatalf("handoff note changed: %q", response.HandoffNote)
	}
	if !strings.HasPrefix(response.Instruction, "ROUTER CONTEXT") {
		t.Fatalf("instruction = %q", response.Instruction)
	}
}

func TestApplyDingTalkDispatchPromptKeepsRobotSDKChatMessageSeparate(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "robot"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-robot"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-robot", Text: "机器人消息"}},
		}},
		Surface:       DispatchSurface{Type: "chat"},
		Outbound:      DispatchOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
		ContextPrompt: "ROUTER CONTEXT",
	})
	response := AgentTaskResponse{ChatSessionID: "chat-1", ChatMessage: "机器人消息"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	if response.ChatMessage != "机器人消息" {
		t.Fatalf("chat message changed: %q", response.ChatMessage)
	}
	if !strings.HasPrefix(response.Instruction, "ROUTER CONTEXT") {
		t.Fatalf("instruction = %q", response.Instruction)
	}
}

func TestApplyDingTalkDispatchPromptKeepsDWSChatMessageSeparate(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-chat"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-chat", Text: "创建会话"}},
		}},
		Surface:       DispatchSurface{Type: "chat"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: "ROUTER CONTEXT",
	})
	response := AgentTaskResponse{ChatSessionID: "chat-1", ChatMessage: "创建会话"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	if response.ChatMessage != "创建会话" {
		t.Fatalf("chat message changed: %q", response.ChatMessage)
	}
	if !strings.HasPrefix(response.Instruction, "ROUTER CONTEXT") {
		t.Fatalf("instruction = %q", response.Instruction)
	}
}

func TestApplyDingTalkDispatchPromptKeepsAutoChatMessageSeparate(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-auto"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-auto", Text: "处理复杂任务"}},
		}},
		Surface:       DispatchSurface{Type: "auto"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: "ROUTER CONTEXT",
	})
	response := AgentTaskResponse{ChatSessionID: "chat-1", ChatMessage: "处理复杂任务"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	if response.ChatMessage != "处理复杂任务" {
		t.Fatalf("chat message changed: %q", response.ChatMessage)
	}
	if !strings.HasPrefix(response.Instruction, "ROUTER CONTEXT") {
		t.Fatalf("instruction = %q", response.Instruction)
	}
}

func TestApplyDingTalkDispatchPromptKeepsRobotIssueHandoffSeparate(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "robot"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-issue"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-issue", Text: "创建问题"}},
		}},
		Surface:       DispatchSurface{Type: "issue"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: "ROUTER CONTEXT",
	})
	response := AgentTaskResponse{IssueID: "issue-1", HandoffNote: "已有交接"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	if response.HandoffNote != "已有交接" {
		t.Fatalf("handoff note changed: %q", response.HandoffNote)
	}
	if !strings.HasPrefix(response.Instruction, "ROUTER CONTEXT") {
		t.Fatalf("instruction = %q", response.Instruction)
	}
}

func TestApplyDingTalkDispatchPromptKeepsCalendarHandoffSeparate(t *testing.T) {
	start := int64(1784217600000)
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "calendar", Type: "calendar.started", Data: DispatchEventData{
			CalendarID: "calendar-1", Subject: "项目评审会", StartTime: &start,
			AIReadableContent: "日程「项目评审会」已经开始。\n请检查设计方案并推进待办。",
		}},
		Surface:       DispatchSurface{Type: "issue"},
		Outbound:      DispatchOutbound{Mode: "none"},
		ContextPrompt: "ROUTER CONTEXT",
	})
	response := AgentTaskResponse{IssueID: "issue-1", HandoffNote: "保留已有交接说明"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	if response.HandoffNote != "保留已有交接说明" {
		t.Fatalf("handoff note changed: %q", response.HandoffNote)
	}
	if !strings.HasPrefix(response.Instruction, "ROUTER CONTEXT") {
		t.Fatalf("instruction = %q", response.Instruction)
	}
}

func TestApplyDingTalkDispatchPromptAppliesCalendarDWSContext(t *testing.T) {
	start := int64(1784217600000)
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "calendar", Type: "calendar.started", Data: DispatchEventData{
			CalendarID: "calendar-1", Subject: "项目评审会", StartTime: &start,
			AIReadableContent: "日程「项目评审会」已经开始。\n请检查设计方案并推进待办。",
			Conversation:      DispatchConversation{OpenConversationID: "cid-calendar"},
			Sender:            DispatchSender{DisplayName: "日程助手", OpenDingTalkID: "open-sender"},
			Messages:          []DispatchMessage{{OpenMsgID: "msg-calendar", Text: "日程通知"}},
		}},
		Surface:       DispatchSurface{Type: "issue"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: "CALENDAR DWS CONTEXT",
	})
	response := AgentTaskResponse{IssueID: "issue-1", HandoffNote: "保留已有交接说明"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	if response.HandoffNote != "保留已有交接说明" {
		t.Fatalf("calendar task handoff changed: %q", response.HandoffNote)
	}
	if !strings.HasPrefix(response.Instruction, "CALENDAR DWS CONTEXT") {
		t.Fatalf("calendar instruction = %q", response.Instruction)
	}
}

func TestApprovalStatusChangedDispatchUsesIssueWithoutOutboundReply(t *testing.T) {
	c := DispatchCommand{
		SchemaVersion: "2.0", AgentID: "agent",
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "approval", Type: "approval.status_changed", Data: DispatchEventData{
			Approval: &ApprovalEventData{
				FormCode:          "FORM-2026-001",
				OriginatorUid:     "originator-uid-secret",
				ApproverUids:      []string{"approver-uid-secret-1", "approver-uid-secret-2"},
				CcUids:            []string{"cc-uid-secret"},
				NodeType:          "auto_approve",
				Status:            "approving",
				ConversationID:    "cid-secret",
				AIReadableContent: "审批单「FORM-2026-001」待你审批，节点类型：自动通过",
			},
		}},
		Surface:          DispatchSurface{Type: "issue"},
		Outbound:         DispatchOutbound{Mode: "none"},
		ContextPrompt:    "APPROVAL CONTEXT",
		ExternalIdentity: AgentDispatchExternalIdentity{ContextToken: "context-token", ExpiresAt: 4102444800000},
	}
	if err := c.validate(); err != nil {
		t.Fatalf("valid approval dispatch rejected: %v", err)
	}
	prompt := mustBuildDispatchPrompt(t, c)
	if !strings.Contains(prompt.DisplayContent, "审批单「FORM-2026-001」") {
		t.Fatalf("approval display content missing aiReadableContent: %q", prompt.DisplayContent)
	}
	for _, secret := range []string{"approver-uid-secret-1", "approver-uid-secret-2", "originator-uid-secret", "cc-uid-secret", "cid-secret"} {
		if strings.Contains(prompt.DisplayContent, secret) {
			t.Fatalf("approval display content leaked %q: %q", secret, prompt.DisplayContent)
		}
	}
	if instruction := buildDispatchInstructionForTest(nil, c.Surface.Type, nil, c.ContextPrompt); instruction != "APPROVAL CONTEXT" {
		t.Fatalf("approval instruction = %q", instruction)
	}
	if got := dispatchWindowIdempotencyKey(c); got != "approval:FORM-2026-001:approving" {
		t.Fatalf("approval idempotency key = %q", got)
	}
	if got := dispatchIssueTitle(c, dispatchWindowIdempotencyKey(c)); !strings.HasPrefix(got, "审批单：FORM-2026-001 · ") {
		t.Fatalf("approval issue title = %q", got)
	}

	clone := func() DispatchCommand {
		cloned := c
		approval := *c.Event.Data.Approval
		cloned.Event.Data.Approval = &approval
		return cloned
	}

	missingToken := clone()
	missingToken.ExternalIdentity.ContextToken = ""
	missingToken.ExternalIdentity.ExpiresAt = 0
	if err := missingToken.validate(); err == nil || !strings.Contains(err.Error(), "contextToken") {
		t.Fatalf("approval dispatch without context token error = %v", err)
	}

	dwsIdentityOnly := clone()
	dwsIdentityOnly.ExternalIdentity = AgentDispatchExternalIdentity{
		DWS: &AgentDispatchDWSIdentity{UID: "123", OrgID: "456"},
	}
	if err := dwsIdentityOnly.validate(); err != nil {
		t.Fatalf("approval dispatch with DWS-only external identity rejected: %v", err)
	}

	missingFormCode := clone()
	missingFormCode.Event.Data.Approval.FormCode = ""
	if err := missingFormCode.validate(); err == nil || !strings.Contains(err.Error(), "formCode") {
		t.Fatalf("approval dispatch without formCode error = %v", err)
	}

	missingApproverUids := clone()
	missingApproverUids.Event.Data.Approval.ApproverUids = nil
	if err := missingApproverUids.validate(); err == nil || !strings.Contains(err.Error(), "approverUids") {
		t.Fatalf("approval dispatch without approverUids error = %v", err)
	}

	missingAIReadableContent := clone()
	missingAIReadableContent.Event.Data.Approval.AIReadableContent = ""
	if err := missingAIReadableContent.validate(); err == nil || !strings.Contains(err.Error(), "aiReadableContent") {
		t.Fatalf("approval dispatch without aiReadableContent error = %v", err)
	}

	wrongSurface := clone()
	wrongSurface.Surface.Type = "chat"
	if err := wrongSurface.validate(); err == nil || !strings.Contains(err.Error(), "surface") {
		t.Fatalf("approval dispatch with wrong surface error = %v", err)
	}

	wrongOutbound := clone()
	wrongOutbound.Outbound.Mode = "invalid"
	if err := wrongOutbound.validate(); err == nil || !strings.Contains(err.Error(), "outbound") {
		t.Fatalf("approval dispatch with wrong outbound error = %v", err)
	}

	dwsWithoutReplyTo := clone()
	dwsWithoutReplyTo.Outbound.Mode = "dws"
	dwsWithoutReplyTo.Outbound.ReplyTo = ""
	if err := dwsWithoutReplyTo.validate(); err == nil || !strings.Contains(err.Error(), "replyTo") {
		t.Fatalf("approval dispatch with dws outbound but missing replyTo error = %v", err)
	}

	wrongSource := clone()
	wrongSource.Source.Type = "robot"
	if err := wrongSource.validate(); err == nil || !strings.Contains(err.Error(), "digital_employee") {
		t.Fatalf("approval dispatch with robot source error = %v", err)
	}

	nilApproval := clone()
	nilApproval.Event.Data.Approval = nil
	if err := nilApproval.validate(); err == nil || !strings.Contains(err.Error(), "approval requires") {
		t.Fatalf("approval dispatch with nil approval data error = %v", err)
	}
}

func TestShouldSkipApprovalDispatch(t *testing.T) {
	tests := []struct {
		name    string
		command DispatchCommand
		want    bool
	}{
		{
			name: "auto_approve approval is skipped",
			command: DispatchCommand{
				Event: DispatchEvent{Domain: "approval", Type: "approval.status_changed", Data: DispatchEventData{
					Approval: &ApprovalEventData{NodeType: "auto_approve", FormCode: "FORM-001"},
				}},
			},
			want: true,
		},
		{
			name: "non-auto_approve approval is not skipped",
			command: DispatchCommand{
				Event: DispatchEvent{Domain: "approval", Type: "approval.status_changed", Data: DispatchEventData{
					Approval: &ApprovalEventData{NodeType: "normal", FormCode: "FORM-001"},
				}},
			},
			want: false,
		},
		{
			name: "channel event is not skipped",
			command: DispatchCommand{
				Event: DispatchEvent{Domain: "channel", Type: "message.created"},
			},
			want: false,
		},
		{
			name: "calendar event is not skipped",
			command: DispatchCommand{
				Event: DispatchEvent{Domain: "calendar", Type: "calendar.started"},
			},
			want: false,
		},
		{
			name: "approval with whitespace nodeType is not skipped",
			command: DispatchCommand{
				Event: DispatchEvent{Domain: "approval", Type: "approval.status_changed", Data: DispatchEventData{
					Approval: &ApprovalEventData{NodeType: "  auto_approve  ", FormCode: "FORM-001"},
				}},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSkipApprovalDispatch(tt.command); got != tt.want {
				t.Fatalf("shouldSkipApprovalDispatch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractIssueIdentifierFromApprovalContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		prefix  string
		want    string
	}{
		{
			name:    "identifier in form field",
			content: "审批单「FORM-001」已通过\n关联Issue：MUL-123\n事由：天气查询",
			prefix:  "MUL",
			want:    "MUL-123",
		},
		{
			name:    "identifier embedded in text",
			content: "审批标题：MUL-456 的天气查询申请已通过",
			prefix:  "MUL",
			want:    "MUL-456",
		},
		{
			name:    "case insensitive match",
			content: "关联issue: mul-789",
			prefix:  "MUL",
			want:    "MUL-789",
		},
		{
			name:    "no identifier in content",
			content: "审批单「FORM-001」已通过",
			prefix:  "MUL",
			want:    "",
		},
		{
			name:    "empty content",
			content: "",
			prefix:  "MUL",
			want:    "",
		},
		{
			name:    "empty prefix",
			content: "MUL-123",
			prefix:  "",
			want:    "",
		},
		{
			name:    "different prefix in content does not match",
			content: "关联Issue: ABC-123",
			prefix:  "MUL",
			want:    "",
		},
		{
			name:    "prefix with special chars",
			content: "关联Issue: FDE-42",
			prefix:  "FDE",
			want:    "FDE-42",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractIssueIdentifierFromApprovalContent(tt.content, tt.prefix)
			if got != tt.want {
				t.Fatalf("extractIssueIdentifierFromApprovalContent(%q, %q) = %q, want %q",
					tt.content, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestApplyDingTalkDispatchPromptKeepsApprovalTaskOutboundFree(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "approval", Type: "approval.status_changed", Data: DispatchEventData{
			Approval: &ApprovalEventData{
				FormCode:          "FORM-2026-001",
				OriginatorUid:     "originator-uid",
				ApproverUids:      []string{"approver-uid-1"},
				NodeType:          "auto_approve",
				Status:            "approving",
				AIReadableContent: "审批单「FORM-2026-001」待你审批，节点类型：自动通过",
			},
		}},
		Surface:  DispatchSurface{Type: "issue"},
		Outbound: DispatchOutbound{Mode: "none"},
	})
	response := AgentTaskResponse{IssueID: "issue-1", HandoffNote: "保留已有交接说明"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	// Approval dispatches with outbound=none do not inject any prompt into
	// existing task fields. The handoff note must remain untouched.
	if response.HandoffNote != "保留已有交接说明" {
		t.Fatalf("approval task handoff must not be modified; got: %s", response.HandoffNote)
	}
}

func TestApprovalStatusChangedDispatchWithDWSOutboundUsesRouterContextInstruction(t *testing.T) {
	c := DispatchCommand{
		SchemaVersion: "2.0", AgentID: "agent",
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "approval", Type: "approval.status_changed", Data: DispatchEventData{
			Approval: &ApprovalEventData{
				FormCode:          "FORM-2026-002",
				OriginatorUid:     "originator-uid",
				ApproverUids:      []string{"approver-uid-1"},
				NodeType:          "normal",
				Status:            "approving",
				AIReadableContent: "审批单「FORM-2026-002」待你审批，请审核报销明细。",
			},
			Conversation: DispatchConversation{OpenConversationID: "cid-approval"},
			Sender:       DispatchSender{DisplayName: "审批助手", OpenDingTalkID: "open-sender"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-approval", Text: "审批通知"}},
		}},
		Surface:       DispatchSurface{Type: "issue"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: "APPROVAL DWS CONTEXT",
		ExternalIdentity: AgentDispatchExternalIdentity{
			DWS: &AgentDispatchDWSIdentity{UID: "123", OrgID: "456"},
		},
	}
	if err := c.validate(); err != nil {
		t.Fatalf("valid approval dispatch with dws outbound rejected: %v", err)
	}
	if instruction := buildDispatchInstructionForTest(nil, c.Surface.Type, nil, c.ContextPrompt); instruction != "APPROVAL DWS CONTEXT" {
		t.Fatalf("approval instruction = %q", instruction)
	}
}

func TestApplyDingTalkDispatchPromptAppliesApprovalDWSContext(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "approval", Type: "approval.status_changed", Data: DispatchEventData{
			Approval: &ApprovalEventData{
				FormCode:          "FORM-2026-002",
				OriginatorUid:     "originator-uid",
				ApproverUids:      []string{"approver-uid-1"},
				NodeType:          "normal",
				Status:            "approving",
				AIReadableContent: "审批单「FORM-2026-002」待你审批，请审核报销明细。",
			},
			Conversation: DispatchConversation{OpenConversationID: "cid-approval"},
			Sender:       DispatchSender{DisplayName: "审批助手", OpenDingTalkID: "open-sender"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-approval", Text: "审批通知"}},
		}},
		Surface:       DispatchSurface{Type: "issue"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: "APPROVAL DWS CONTEXT",
	})
	response := AgentTaskResponse{IssueID: "issue-1", HandoffNote: "保留已有交接说明"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	if response.HandoffNote != "保留已有交接说明" {
		t.Fatalf("approval task handoff changed: %q", response.HandoffNote)
	}
	if !strings.HasPrefix(response.Instruction, "APPROVAL DWS CONTEXT") {
		t.Fatalf("approval instruction = %q", response.Instruction)
	}
}
func TestDispatchRuntimeContextStoresStructuredDataWithoutGeneratedPromptFields(t *testing.T) {
	command := DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-structured"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender-structured"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-structured", Text: "处理一下"}},
		}},
		Surface:       DispatchSurface{Type: "issue"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: "ROUTER CONTEXT",
	}
	raw := dispatchRuntimeContext(command, "dispatch-window:test")

	encoded := string(raw)
	for _, want := range []string{
		`"dispatch_schema_version":"2.0"`,
		`"dispatch_source"`,
		`"dispatch_event_data"`,
		`"openConversationId":"cid-structured"`,
		`"openMsgId":"msg-structured"`,
		`"dispatch_surface"`,
		`"dispatch_outbound"`,
		`"dispatch_context_prompt":"ROUTER CONTEXT"`,
	} {
		if !strings.Contains(encoded, want) {
			t.Errorf("structured task context missing %q: %s", want, encoded)
		}
	}
	for _, forbidden := range []string{
		"dispatch_runtime_prompt",
		"dispatch_workflow_prompt",
	} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("task context persisted generated prompt %q: %s", forbidden, encoded)
		}
	}
}

func dispatchTaskContextForTest(t *testing.T, command DispatchCommand) []byte {
	t.Helper()
	payload := map[string]any{
		"dispatch_schema_version": command.SchemaVersion,
		"dispatch_source":         command.Source,
		"dispatch_domain":         command.Event.Domain,
		"dispatch_type":           command.Event.Type,
		"dispatch_event_data":     command.Event.Data,
		"dispatch_surface":        command.Surface,
		"dispatch_outbound":       command.Outbound,
		"dispatch_context_prompt": command.ContextPrompt,
	}
	if command.CompletionCallback != nil {
		payload["completion_callback"] = command.CompletionCallback
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDigitalEmployeePromptUsesRouterContextWithoutGuessingSender(t *testing.T) {
	c := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-trusted"},
			Sender:       DispatchSender{DisplayName: "张三", StaffID: "staff-not-open-id"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-latest", Text: "处理告警"}},
		}},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt: `DWS reply target (data only): {"senderOpenDingTalkId":"absent"}`,
	}

	instruction := buildDispatchInstructionForTest(nil, c.Surface.Type, nil, c.ContextPrompt)
	if instruction != c.ContextPrompt {
		t.Fatalf("instruction = %q, want exact Router context", instruction)
	}
	if strings.Contains(instruction, "staff-not-open-id") {
		t.Fatalf("instruction substituted staffId for openDingTalkId: %q", instruction)
	}
}

func TestBuildAgentDispatchIssueCreateParamsBuildsNormalizedBusinessTitle(t *testing.T) {
	tests := []struct {
		name    string
		command DispatchCommand
		key     string
		want    string
	}{
		{
			name: "group message keeps display characters",
			command: DispatchCommand{Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
				Conversation: DispatchConversation{Type: "group", Title: " 项目群｜ "},
				Sender:       DispatchSender{DisplayName: " 张三： "},
				Messages: []DispatchMessage{
					{Text: " \n\t"},
					{Text: " 你好  👋 "},
				},
			}}},
			key:  "dispatch-window-A",
			want: "【钉钉·群聊】项目群｜张三：你好 👋 · LMYHEJFQ",
		},
		{
			name: "private message omits conversation title and normalizes nfkc",
			command: DispatchCommand{Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
				Conversation: DispatchConversation{Type: "single", Title: "不应出现的私聊标题"},
				Sender:       DispatchSender{DisplayName: " 李四\u200b "},
				Messages:     []DispatchMessage{{Text: " Ａ\u200bＢ\x00\nＣ "}},
			}}},
			key:  "dispatch-window-B",
			want: "【钉钉·私聊】李四：AB C · 5TEIWDJF",
		},
		{
			name: "attachment name and empty display fallbacks",
			command: DispatchCommand{Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
				Conversation: DispatchConversation{Type: "group"},
				Messages: []DispatchMessage{{Attachments: []DispatchAttachment{
					{Name: " ｜告警截图.png： "},
				}}},
			}}},
			key:  "dispatch-window-A",
			want: "【钉钉·群聊】钉钉群聊｜钉钉用户：告警截图.png · LMYHEJFQ",
		},
		{
			name: "unknown conversation and attachment content type",
			command: DispatchCommand{Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
				Messages: []DispatchMessage{{Attachments: []DispatchAttachment{
					{ContentType: " application/pdf "},
				}}},
			}}},
			key:  "dispatch-window-A",
			want: "【钉钉消息】钉钉用户：application/pdf · LMYHEJFQ",
		},
		{
			name: "attachment type fallback",
			command: DispatchCommand{Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
				Conversation: DispatchConversation{Type: "direct"},
				Sender:       DispatchSender{DisplayName: "王五"},
				Messages: []DispatchMessage{{Attachments: []DispatchAttachment{
					{Type: "image"},
				}}},
			}}},
			key:  "dispatch-window-A",
			want: "【钉钉·私聊】王五：image · LMYHEJFQ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := buildAgentDispatchIssueCreateParams(
				tt.command,
				DispatchPrompt{},
				agentDispatchContext{},
				db.Agent{},
				tt.key,
				agentDispatchIssueCreateOverrides{},
			)
			if params.Title != tt.want {
				t.Fatalf("title = %q, want %q", params.Title, tt.want)
			}
		})
	}
}

func TestBuildAgentDispatchIssueCreateParamsUsesAcceptanceInsteadOfTitleDeduplication(t *testing.T) {
	command := DispatchCommand{CompletionCallback: &DispatchCompletionCallback{
		URL: "/api/v1/dispatch-tasks/title-dedup-test/execution-result",
	}}
	withAcceptance := buildAgentDispatchIssueCreateParams(
		command,
		DispatchPrompt{},
		agentDispatchContext{},
		db.Agent{},
		"dispatch-window-A",
		agentDispatchIssueCreateOverrides{},
	)
	if !withAcceptance.AllowDuplicate {
		t.Fatal("dispatch with durable acceptance still uses business title as its idempotency guard")
	}

	command.CompletionCallback = nil
	withoutAcceptance := buildAgentDispatchIssueCreateParams(
		command,
		DispatchPrompt{},
		agentDispatchContext{},
		db.Agent{},
		"dispatch-window-A",
		agentDispatchIssueCreateOverrides{},
	)
	if withoutAcceptance.AllowDuplicate {
		t.Fatal("dispatch without durable acceptance unexpectedly bypasses the title duplicate guard")
	}
}

func TestDispatchEventShortCodeIsStableBase32(t *testing.T) {
	for _, key := range []string{"dispatch-window-A", "含中文的 key", "key/with:safe-input"} {
		first := dispatchEventShortCode(key)
		second := dispatchEventShortCode(key)
		if first != second || len(first) != 8 {
			t.Fatalf("short code for %q is not stable eight-character output: %q / %q", key, first, second)
		}
		for _, r := range first {
			if (r < 'A' || r > 'Z') && (r < '2' || r > '7') {
				t.Fatalf("short code for %q contains unsafe rune %q: %q", key, r, first)
			}
		}
	}
	if dispatchEventShortCode("dispatch-window-A") == dispatchEventShortCode("dispatch-window-B") {
		t.Fatal("different dispatch windows produced the same short code")
	}
}

func TestBuildAgentDispatchIssueCreateParamsKeepsShortCodeWithinRuneLimit(t *testing.T) {
	command := DispatchCommand{Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
		Conversation: DispatchConversation{Type: "group", Title: strings.Repeat("会", 100)},
		Sender:       DispatchSender{DisplayName: strings.Repeat("发", 100)},
		Messages:     []DispatchMessage{{Text: strings.Repeat("界", 200)}},
	}}}
	params := buildAgentDispatchIssueCreateParams(
		command,
		DispatchPrompt{},
		agentDispatchContext{},
		db.Agent{},
		"dispatch-window-A",
		agentDispatchIssueCreateOverrides{},
	)

	if got := utf8.RuneCountInString(params.Title); got > 160 {
		t.Fatalf("title rune count = %d, want <= 160", got)
	}
	if !strings.HasSuffix(params.Title, " · LMYHEJFQ") {
		t.Fatalf("title lost stable short code: %q", params.Title)
	}
}

func TestBuildAgentDispatchIssueCreateParamsFormatsCalendarTitle(t *testing.T) {
	start := int64(1784217600000)
	command := DispatchCommand{Event: DispatchEvent{Domain: "calendar", Type: "calendar.started", Data: DispatchEventData{
		Subject: " ｜项目评审会： ", StartTime: &start, Timezone: "Asia/Shanghai",
	}}}
	params := buildAgentDispatchIssueCreateParams(
		command,
		DispatchPrompt{},
		agentDispatchContext{},
		db.Agent{},
		"calendar-key",
		agentDispatchIssueCreateOverrides{},
	)
	if params.Title != "【钉钉·日程】项目评审会｜2026-07-17 00:00 · 5CYY7PQC" {
		t.Fatalf("calendar title = %q", params.Title)
	}
}

func TestRecoverDuplicateAgentChatDispatchReturnsExistingContinuation(t *testing.T) {
	chatSessionID := parseUUID("11111111-1111-1111-1111-111111111111")
	taskID := parseUUID("33333333-3333-3333-3333-333333333333")
	queries := &fakeAgentDispatchDuplicateQueries{
		processedAt: pgtype.Timestamptz{Valid: true},
		taskID:      taskID,
		binding: db.ChannelChatSessionBinding{
			ChatSessionID: chatSessionID,
		},
	}
	message := channel.InboundMessage{Source: channel.Source{
		ChannelType: "dingtalk",
		ChatID:      "conversation-1",
		ChatType:    channel.ChatTypeP2P,
		SenderID:    "sender-1",
	}}

	response, err := recoverDuplicateAgentChatDispatch(
		context.Background(),
		queries,
		parseUUID("22222222-2222-2222-2222-222222222222"),
		"message-1",
		message,
	)
	if err != nil {
		t.Fatalf("recover duplicate dispatch: %v", err)
	}
	if response.Continuation.Kind != "chat" || response.Continuation.ChatSessionID != uuidToString(chatSessionID) {
		t.Fatalf("continuation = %+v", response.Continuation)
	}
	if response.TaskID != uuidToString(taskID) {
		t.Fatalf("taskId = %q, want %s", response.TaskID, uuidToString(taskID))
	}
	if queries.bindingKey != "conversation-1" {
		t.Fatalf("binding key = %q", queries.bindingKey)
	}
}

func TestRecoverDuplicateAgentChatDispatchWaitsForCommittedResult(t *testing.T) {
	queries := &fakeAgentDispatchDuplicateQueries{}
	message := channel.InboundMessage{Source: channel.Source{
		ChannelType: "dingtalk",
		ChatID:      "conversation-1",
		ChatType:    channel.ChatTypeP2P,
		SenderID:    "sender-1",
	}}

	_, err := recoverDuplicateAgentChatDispatch(
		context.Background(),
		queries,
		parseUUID("22222222-2222-2222-2222-222222222222"),
		"message-1",
		message,
	)
	if !errors.Is(err, errAgentDispatchDuplicateNotReady) {
		t.Fatalf("error = %v", err)
	}
}

func mustBuildDispatchPrompt(t *testing.T, command DispatchCommand) DispatchPrompt {
	t.Helper()
	prompt, err := BuildDispatchPrompt(command)
	if err != nil {
		t.Fatalf("BuildDispatchPrompt: %v", err)
	}
	return prompt
}

func emotionReplyDispatchCommand() DispatchCommand {
	return DispatchCommand{
		SchemaVersion: "2.0", AgentID: "agent",
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "emotionReply", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-group", Type: "group", Title: "项目群"},
			Sender:       DispatchSender{DisplayName: "张三", OpenDingTalkID: "open-sender"},
			Messages: []DispatchMessage{
				{
					OpenMsgID:  "msg-reacted",
					OccurredAt: 1784500000000,
					Text:       "本周发布计划已同步",
					Reaction: &DispatchMessageReaction{
						EmotionName:    "赞",
						EmotionTypeV2:  1,
						EmotionVersion: 3,
						Action:         "add",
						OperateTime:    1784500001000,
					},
				},
			},
		}},
		Surface:          DispatchSurface{Type: "issue"},
		Outbound:         DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt:    "ROUTER CONTEXT",
		ExternalIdentity: AgentDispatchExternalIdentity{ContextToken: "context-token", ExpiresAt: 4102444800000},
	}
}

// Router 契约中 emotionTypeV2/emotionVersion 是 JSON number（gateway 原始事件与
// Router MessageReaction 模型均为整型）；struct 字段若声明为 string 会在
// json.Unmarshal 阶段整体失败，回归防护见本条。
func TestEmotionReplyDispatchParsesNumericEmotionFields(t *testing.T) {
	raw := []byte(`{
		"schemaVersion": "2.0",
		"source": {"platform": "dingtalk", "sourceType": "digital_employee", "sourceId": "source-1"},
		"event": {
			"domain": "channel",
			"type": "emotionReply",
			"data": {
				"conversation": {"openConversationId": "cid-group", "type": "group", "title": "发布同步群"},
				"sender": {"openSenderId": "open-sender", "displayName": "张三"},
				"messages": [{
					"openMsgId": "msg-reacted",
					"occurredAt": 1784500000000,
					"text": "本周发布计划已同步",
					"reaction": {"emotionName": "赞", "emotionTypeV2": 1, "emotionVersion": 3, "action": "add", "operateTime": 1784500001000}
				}]
			}
		},
		"surface": {"type": "issue"},
		"outbound": {"mode": "dws", "replyTo": "latest_message"},
		"externalIdentity": {"contextToken": "context-token", "expiresAt": 4102444800000}
	}`)
	var c DispatchCommand
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("numeric emotion fields rejected at unmarshal: %v", err)
	}
	reaction := c.Event.Data.Messages[0].Reaction
	if reaction == nil || reaction.EmotionTypeV2 != 1 || reaction.EmotionVersion != 3 {
		t.Fatalf("reaction = %+v", reaction)
	}
}

func TestEmotionReplyDispatchValidatesAndRendersReactionEntries(t *testing.T) {
	c := emotionReplyDispatchCommand()
	if err := c.validate(); err != nil {
		t.Fatalf("valid emotionReply dispatch rejected: %v", err)
	}

	prompt := mustBuildDispatchPrompt(t, c)
	if !strings.Contains(prompt.DisplayContent, "张三") ||
		!strings.Contains(prompt.DisplayContent, "对消息「本周发布计划已同步」贴上了表情 赞") {
		t.Fatalf("display content = %q", prompt.DisplayContent)
	}
	for _, secret := range []string{"cid-group", "open-sender", "msg-reacted"} {
		if strings.Contains(prompt.DisplayContent, secret) {
			t.Fatalf("display content leaked %q: %q", secret, prompt.DisplayContent)
		}
	}

	title := dispatchIssueTitle(c, dispatchWindowIdempotencyKey(c))
	if !strings.Contains(title, "表情回复") || !strings.Contains(title, "赞") {
		t.Fatalf("issue title = %q", title)
	}

	// add 与 remove 是独立事件：幂等键必须不同（remove 不抵消 add）。
	removed := emotionReplyDispatchCommand()
	removed.Event.Data.Messages[0].Reaction.Action = "remove"
	if dispatchWindowIdempotencyKey(c) == dispatchWindowIdempotencyKey(removed) {
		t.Fatal("add/remove reactions share the same idempotency key")
	}
}

func TestEmotionReplyDispatchRendersEmptyTextAndRemoveAction(t *testing.T) {
	c := emotionReplyDispatchCommand()
	c.Event.Data.Messages[0].Text = ""
	c.Event.Data.Messages[0].Reaction.Action = "remove"
	if err := c.validate(); err != nil {
		t.Fatalf("reaction on attachment-only message rejected: %v", err)
	}
	prompt := mustBuildDispatchPrompt(t, c)
	if !strings.Contains(prompt.DisplayContent, "对消息「一条消息」移除了表情 赞") {
		t.Fatalf("display content = %q", prompt.DisplayContent)
	}
}

func TestEmotionReplyDispatchRejectsInvalidReactionEntries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*DispatchCommand)
	}{
		{"robot source", func(c *DispatchCommand) { c.Source.Type = "robot" }},
		{"invalid action", func(c *DispatchCommand) { c.Event.Data.Messages[0].Reaction.Action = "peek" }},
		{"empty emotion name", func(c *DispatchCommand) { c.Event.Data.Messages[0].Reaction.EmotionName = "" }},
		{"missing openMsgId", func(c *DispatchCommand) { c.Event.Data.Messages[0].OpenMsgID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := emotionReplyDispatchCommand()
			tc.mutate(&c)
			if err := c.validate(); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestChannelMixedWindowRendersReactionAlongsideMessages(t *testing.T) {
	// 混合窗口：顶层 type 仍是 message.created，条目级 reaction 独立分流渲染。
	c := emotionReplyDispatchCommand()
	c.Event.Type = "message.created"
	c.Event.Data.Messages = append([]DispatchMessage{
		{OpenMsgID: "msg-user", OccurredAt: 1784499999000, Text: "这个方案看一下"},
	}, c.Event.Data.Messages...)
	if err := c.validate(); err != nil {
		t.Fatalf("mixed window rejected: %v", err)
	}
	prompt := mustBuildDispatchPrompt(t, c)
	if !strings.Contains(prompt.DisplayContent, "这个方案看一下") ||
		!strings.Contains(prompt.DisplayContent, "对消息「本周发布计划已同步」贴上了表情 赞") {
		t.Fatalf("mixed display content = %q", prompt.DisplayContent)
	}
}

func TestApplyDingTalkDispatchPromptRebuildsInstructionForEmotionReply(t *testing.T) {
	context := dispatchTaskContextForTest(t, emotionReplyDispatchCommand())
	response := AgentTaskResponse{IssueID: "issue-1", HandoffNote: "保留交接"}
	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)
	if !strings.HasPrefix(response.Instruction, "ROUTER CONTEXT") {
		t.Fatalf("emotionReply claim instruction = %q", response.Instruction)
	}
	if response.HandoffNote != "保留交接" {
		t.Fatalf("user-visible handoff changed: %q", response.HandoffNote)
	}
}

func TestAgentDispatchPromptReplacesEveryDiamondSection(t *testing.T) {
	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{
	  "common":{"prompt":"COMMON POLICY"},
	  "issue":{"prompt":"ISSUE MODE POLICY"},
	  "chat":{"prompt":"CHAT MODE POLICY"},
	  "auto":{"prompt":"AUTO MODE POLICY"}
	}`)); err != nil {
		t.Fatalf("seed Diamond prompts: %v", err)
	}
	flags := featureflag.NewService(provider)

	for _, surface := range []string{"issue", "chat", "auto"} {
		instruction := buildDispatchInstructionForTest(flags, surface, map[string]string{DispatchSegmentPolicy: "AGENT AUTHORED POLICY"}, "ROUTER CONTEXT")
		if want := "AGENT AUTHORED POLICY\n\nROUTER CONTEXT"; instruction != want {
			t.Errorf("surface %s instruction = %q, want %q", surface, instruction, want)
		}
		if strings.Contains(instruction, "COMMON POLICY") {
			t.Errorf("surface %s instruction still carries the Diamond common section: %q", surface, instruction)
		}
	}
}

func TestAgentDispatchPromptKeepsRouterContextUnreplaceable(t *testing.T) {
	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{"common":{"prompt":"COMMON POLICY"}}`)); err != nil {
		t.Fatalf("seed Diamond prompt: %v", err)
	}
	flags := featureflag.NewService(provider)

	// contextPrompt carries this run's resolved delivery locators. An authored
	// prompt replaces policy, never the facts the Router supplied.
	instruction := buildDispatchInstructionForTest(flags, "auto", map[string]string{DispatchSegmentPolicy: "AGENT AUTHORED POLICY"}, "ROUTER CONTEXT")
	if !strings.HasSuffix(instruction, "ROUTER CONTEXT") {
		t.Fatalf("instruction dropped the Router context: %q", instruction)
	}
}

func TestBlankAgentDispatchPromptFallsBackToDiamondComposition(t *testing.T) {
	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{
	  "common":{"prompt":"COMMON POLICY"},
	  "auto":{"prompt":"AUTO MODE POLICY"}
	}`)); err != nil {
		t.Fatalf("seed Diamond prompts: %v", err)
	}
	flags := featureflag.NewService(provider)

	// Whitespace-only is the same as unset: an owner who cleared the editor
	// must get the Diamond baseline back, not an empty instruction.
	for _, authored := range []string{"", "   ", "\n\t\n"} {
		instruction := buildDispatchInstructionForTest(flags, "auto", map[string]string{DispatchSegmentPolicy: authored}, "ROUTER CONTEXT")
		if want := "COMMON POLICY\n\nAUTO MODE POLICY\n\nROUTER CONTEXT"; instruction != want {
			t.Errorf("authored %q: instruction = %q, want %q", authored, instruction, want)
		}
	}
}

func TestAgentDispatchPromptReplacesDiamondSectionOnLegacyDaemonPath(t *testing.T) {
	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{
	  "common":{"prompt":"COMMON POLICY"},
	  "auto":{"prompt":"AUTO MODE POLICY"}
	}`)); err != nil {
		t.Fatalf("seed Diamond prompts: %v", err)
	}
	flags := featureflag.NewService(provider)
	stored := persistedDispatchContext{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Domain:        "channel",
		Type:          "message.created",
		Surface:       DispatchSurface{Type: "auto"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	}

	instruction := buildLegacyDispatchInstruction(stored, flags, "AGENT AUTHORED POLICY")
	if !strings.Contains(instruction, "AGENT AUTHORED POLICY") {
		t.Fatalf("legacy instruction is missing the authored prompt: %q", instruction)
	}
	if strings.Contains(instruction, "AUTO MODE POLICY") {
		t.Fatalf("legacy instruction still carries the Diamond surface section: %q", instruction)
	}
	if strings.Contains(instruction, "dws chat message reply") {
		t.Fatalf("legacy instruction still requires the removed DWS reply command: %q", instruction)
	}
	if !strings.Contains(instruction, "ordinary final assistant reply") {
		t.Fatalf("legacy instruction is missing the provider final-output contract: %q", instruction)
	}
}

func TestLegacyDWSWorkflowWithCompletionCallbackUsesRouterFinalOutput(t *testing.T) {
	prompt := buildLegacyDingTalkDWSWorkflowPrompt(DispatchCommand{
		CompletionCallback: &DispatchCompletionCallback{
			URL: "/api/v1/dispatch-tasks/router-task-1/execution-result",
		},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	})

	if strings.Contains(prompt, "dws chat message reply") {
		t.Fatalf("durable Router delivery still requires the legacy DWS reply command: %q", prompt)
	}
	if !strings.Contains(prompt, "ordinary final assistant reply") {
		t.Fatalf("durable Router delivery is missing the provider final-output instruction: %q", prompt)
	}
}

func TestAgentDispatchPromptReplacesDiamondCompositionAtClaim(t *testing.T) {
	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{
	  "common":{"prompt":"COMMON POLICY"},
	  "chat":{"prompt":"CHAT MODE POLICY"}
	}`)); err != nil {
		t.Fatalf("seed Diamond prompts: %v", err)
	}
	flags := featureflag.NewService(provider)
	context := []byte(`{
		"dispatch_schema_version":"2.0",
		"dispatch_source":{"platform":"dingtalk","type":"digital_employee"},
		"dispatch_domain":"channel",
		"dispatch_type":"message.created",
		"dispatch_surface":{"type":"chat"},
		"dispatch_outbound":{"mode":"robot_sdk","replyTo":"latest_message"},
		"dispatch_context_prompt":"ROUTER CONTEXT"
	}`)

	var authored AgentTaskResponse
	applyTaskInstructionForClaim(&authored, context, flags, map[string]string{DispatchSegmentPolicy: "AGENT AUTHORED POLICY"}, "")
	if want := "AGENT AUTHORED POLICY\n\nROUTER CONTEXT"; !strings.HasPrefix(authored.Instruction, want) {
		t.Fatalf("claim instruction = %q, want it to start with %q", authored.Instruction, want)
	}

	var baseline AgentTaskResponse
	applyTaskInstructionForClaim(&baseline, context, flags, nil, "")
	if want := "COMMON POLICY\n\nCHAT MODE POLICY\n\nROUTER CONTEXT"; !strings.HasPrefix(baseline.Instruction, want) {
		t.Fatalf("baseline claim instruction = %q, want it to start with %q", baseline.Instruction, want)
	}
}
