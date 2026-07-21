package handler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeAgentDispatchDuplicateQueries struct {
	processedAt pgtype.Timestamptz
	binding     db.ChannelChatSessionBinding
	bindingKey  string
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

func TestBuildDispatchPromptSeparatesDisplayAndRuntime(t *testing.T) {
	c := DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-secret", Title: "项目群"},
			Sender:       DispatchSender{DisplayName: "张三", OpenDingTalkID: "open-secret", StaffID: "staff-secret"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-secret", Text: "请查看告警", Attachments: []DispatchAttachment{{Name: "log.txt", DownloadURL: "https://private.example/log"}}}},
		}},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
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
	if !strings.Contains(p.RuntimePrompt, "untrusted") {
		t.Fatalf("runtime prompt missing private safety instructions: %q", p.RuntimePrompt)
	}
	if strings.Contains(p.RuntimePrompt, "DWS") {
		t.Fatalf("runtime prompt must not carry outbound workflow instructions: %q", p.RuntimePrompt)
	}
	if !strings.Contains(p.WorkflowPrompt, "DWS") {
		t.Fatalf("workflow prompt missing private outbound instructions: %q", p.WorkflowPrompt)
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
		ExternalIdentity: AgentDispatchExternalIdentity{ContextToken: "opaque"},
	}
	if err := c.validate(); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}

	t.Run("robot without context token", func(t *testing.T) {
		command := c
		command.ExternalIdentity.ContextToken = ""
		if err := command.validate(); err != nil {
			t.Fatalf("robot command without context token rejected: %v", err)
		}
	})

	t.Run("digital employee without sender platform identifiers", func(t *testing.T) {
		command := c
		command.Source.Type = "digital_employee"
		command.Surface.Type = "issue"
		command.Outbound.Mode = "dws"
		command.Event.Data.Sender = DispatchSender{DisplayName: "张三"}
		command.ExternalIdentity.ContextToken = ""
		if err := command.validate(); err != nil {
			t.Fatalf("digital employee command without sender ids or context token rejected: %v", err)
		}
	})

	t.Run("source surface combinations are exact", func(t *testing.T) {
		robotWithIssue := c
		robotWithIssue.Surface.Type = "issue"
		if err := robotWithIssue.validate(); err == nil || !strings.Contains(err.Error(), "surface.type must be chat") {
			t.Fatalf("robot issue surface error = %v", err)
		}

		digitalEmployeeWithChat := c
		digitalEmployeeWithChat.Source.Type = "digital_employee"
		digitalEmployeeWithChat.Surface.Type = "chat"
		digitalEmployeeWithChat.Outbound.Mode = "dws"
		if err := digitalEmployeeWithChat.validate(); err == nil || !strings.Contains(err.Error(), "surface.type must be issue") {
			t.Fatalf("digital employee chat surface error = %v", err)
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

func TestDispatchPromptBuilderRoutesRuntimePolicyBySource(t *testing.T) {
	base := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "robot"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-1"},
			Sender:       DispatchSender{DisplayName: "张三", OpenDingTalkID: "open-user-1"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-1", Text: "处理告警"}},
		}},
		Outbound: DispatchOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
	}

	robotPrompt := mustBuildDispatchPrompt(t, base)
	if robotPrompt.WorkflowPrompt != "" {
		t.Fatalf("robot workflow prompt must not own server-side outbound: %q", robotPrompt.WorkflowPrompt)
	}

	digitalEmployee := base
	digitalEmployee.Source.Type = "digital_employee"
	digitalEmployee.Outbound.Mode = "dws"
	digitalPrompt := mustBuildDispatchPrompt(t, digitalEmployee)
	for _, want := range []string{"DWS", "mode=dws", "replyTo=latest_message"} {
		if !strings.Contains(digitalPrompt.WorkflowPrompt, want) {
			t.Errorf("digital employee workflow prompt missing %q: %q", want, digitalPrompt.WorkflowPrompt)
		}
	}

	unsupported := base
	unsupported.Event.Domain = "calendar"
	if _, err := BuildDispatchPrompt(unsupported); err == nil {
		t.Fatal("unregistered prompt strategy was accepted")
	}
}

func TestDigitalEmployeePromptRequiresDWSOutboundLifecycle(t *testing.T) {
	c := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-trusted"},
			Sender:       DispatchSender{DisplayName: "张三", OpenDingTalkID: "open-sender-trusted"},
			Messages: []DispatchMessage{
				{OpenMsgID: "msg-older", Text: "第一条"},
				{OpenMsgID: "msg-latest", Text: "第二条"},
			},
		}},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	}

	workflowPrompt := mustBuildDispatchPrompt(t, c).WorkflowPrompt
	for _, required := range []string{
		`"openConversationId":"cid-trusted"`,
		`"openMsgId":"msg-latest"`,
		`"senderOpenDingTalkId":"open-sender-trusted"`,
		"dws chat message add-emoji",
		`--emoji "收到"`,
		"dws chat message reply",
		"--ref-sender",
		"--format json",
		"before doing the requested work",
		"success, partial success, blocked, or failed",
		"Do not use the robot SDK",
	} {
		if !strings.Contains(workflowPrompt, required) {
			t.Errorf("digital employee workflow prompt missing %q: %q", required, workflowPrompt)
		}
	}
	if strings.Contains(workflowPrompt, "msg-older") {
		t.Fatalf("workflow prompt must target only the latest message: %q", workflowPrompt)
	}
}

func TestDigitalEmployeePromptResolvesMissingReplySenderWithoutGuessing(t *testing.T) {
	c := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-trusted"},
			Sender:       DispatchSender{DisplayName: "张三", StaffID: "staff-not-open-id"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-latest", Text: "处理告警"}},
		}},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	}

	workflowPrompt := mustBuildDispatchPrompt(t, c).WorkflowPrompt
	for _, required := range []string{
		"dws chat message list-by-ids",
		"sender openDingTalkId",
		"Do not infer or invent",
	} {
		if !strings.Contains(workflowPrompt, required) {
			t.Errorf("missing-sender workflow prompt missing %q: %q", required, workflowPrompt)
		}
	}
	if strings.Contains(workflowPrompt, "staff-not-open-id") {
		t.Fatalf("workflow prompt must not substitute staffId for openDingTalkId: %q", workflowPrompt)
	}
}

func TestDispatchIssueTitleUsesFirstUserMessage(t *testing.T) {
	longMessage := strings.Repeat("界", 170)
	c := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{Messages: []DispatchMessage{
		{Text: "  \n\t"},
		{Text: longMessage},
	}}}}

	title := dispatchIssueTitle(c)
	if utf8.RuneCountInString(title) != 160 {
		t.Fatalf("title rune count = %d, want 160", utf8.RuneCountInString(title))
	}
	if title != strings.Repeat("界", 160) {
		t.Fatalf("title = %q", title)
	}
}

func TestDispatchIssueTitleFallsBackToAttachmentThenGeneric(t *testing.T) {
	withAttachment := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{Messages: []DispatchMessage{{
		Attachments: []DispatchAttachment{{Name: "告警截图.png"}},
	}}}}}
	if got := dispatchIssueTitle(withAttachment); got != "附件：告警截图.png" {
		t.Fatalf("attachment title = %q", got)
	}

	if got := dispatchIssueTitle(DispatchCommand{}); got != "钉钉消息" {
		t.Fatalf("generic title = %q", got)
	}
}

func TestRecoverDuplicateAgentChatDispatchReturnsExistingContinuation(t *testing.T) {
	chatSessionID := parseUUID("11111111-1111-1111-1111-111111111111")
	queries := &fakeAgentDispatchDuplicateQueries{
		processedAt: pgtype.Timestamptz{Valid: true},
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
