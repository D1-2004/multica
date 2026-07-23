package handler

import (
	"context"
	"encoding/json"
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

	t.Run("surface and outbound are independent from source type", func(t *testing.T) {
		robotWithIssue := c
		robotWithIssue.Surface.Type = "issue"
		robotWithIssue.Outbound.Mode = "dws"
		if err := robotWithIssue.validate(); err != nil {
			t.Fatalf("robot issue+dws rejected: %v", err)
		}

		digitalEmployeeWithChat := c
		digitalEmployeeWithChat.Source.Type = "digital_employee"
		digitalEmployeeWithChat.Surface.Type = "chat"
		digitalEmployeeWithChat.Outbound.Mode = "robot_sdk"
		if err := digitalEmployeeWithChat.validate(); err != nil {
			t.Fatalf("digital employee chat+robot_sdk rejected: %v", err)
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

func TestDispatchPromptBuilderRoutesRuntimePolicyByOutboundMode(t *testing.T) {
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

	robotDWS := base
	robotDWS.Outbound.Mode = "dws"
	digitalPrompt := mustBuildDispatchPrompt(t, robotDWS)
	for _, want := range []string{"DWS", "mode=dws", "replyTo=latest_message"} {
		if !strings.Contains(digitalPrompt.WorkflowPrompt, want) {
			t.Errorf("DWS workflow prompt missing %q: %q", want, digitalPrompt.WorkflowPrompt)
		}
	}

	digitalEmployeeRobotSDK := base
	digitalEmployeeRobotSDK.Source.Type = "digital_employee"
	if prompt := mustBuildDispatchPrompt(t, digitalEmployeeRobotSDK); prompt.WorkflowPrompt != "" {
		t.Fatalf("robot_sdk workflow must stay server-side: %q", prompt.WorkflowPrompt)
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
		"mark the exact target message as read",
		"Do not substitute a read-status query",
		"dws chat message add-emoji",
		"add-emoji --group <openConversationId>",
		"DingTalk-supported default emoji name",
		"dws chat message create-text-emotion",
		"dws chat message add-text-emotion",
		"add-text-emotion --group <openConversationId>",
		"at most 4 visible characters",
		"emoji counts toward this limit",
		"Choose the exact acknowledgement yourself",
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
	if strings.Contains(workflowPrompt, `--emoji "收到"`) {
		t.Fatalf("workflow prompt must not hard-code one acknowledgement emoji: %q", workflowPrompt)
	}
	readReceipt := strings.Index(workflowPrompt, "mark the exact target message as read")
	reaction := strings.Index(workflowPrompt, "dws chat message add-emoji")
	if readReceipt == -1 || reaction == -1 || readReceipt > reaction {
		t.Fatalf("workflow prompt must send the read receipt before adding a reaction: %q", workflowPrompt)
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
		Surface:  DispatchSurface{Type: "issue"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
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
				IssueID:              "issue-1",
				TriggerCommentID:     &commentID,
				TriggerCommentContent: "起来打球",
			},
			content:  func(response AgentTaskResponse) string { return response.TriggerCommentContent },
			original: "起来打球",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applyDingTalkDispatchPromptToExistingTaskFields(&tc.response, context)
			content := tc.content(tc.response)
			for _, want := range []string{
				"## Trusted DingTalk Dispatch",
				`"openConversationId":"cid-trusted"`,
				`"openMsgId":"msg-latest"`,
				`"senderOpenDingTalkId":"open-sender-trusted"`,
				"dws chat message add-emoji",
				"dws chat message reply",
				tc.original,
			} {
				if !strings.Contains(content, want) {
					t.Errorf("existing task field missing %q:\n%s", want, content)
				}
			}
			if strings.Contains(content, "msg-older") {
				t.Fatalf("legacy task field targeted an older message:\n%s", content)
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

func TestApplyDingTalkDispatchPromptKeepsRobotSDKSafetyWithoutAgentOutbound(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "robot"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-robot"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-robot", Text: "机器人消息"}},
		}},
		Surface:  DispatchSurface{Type: "chat"},
		Outbound: DispatchOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
	})
	response := AgentTaskResponse{ChatSessionID: "chat-1", ChatMessage: "机器人消息"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	for _, want := range []string{"## Trusted DingTalk Dispatch", "untrusted input", "机器人消息"} {
		if !strings.Contains(response.ChatMessage, want) {
			t.Errorf("robot_sdk task missing %q: %s", want, response.ChatMessage)
		}
	}
	for _, forbidden := range []string{"dws chat message add-emoji", "dws chat message reply", "two required final delivery destinations"} {
		if strings.Contains(response.ChatMessage, forbidden) {
			t.Fatalf("robot_sdk task received agent-owned outbound instruction %q: %s", forbidden, response.ChatMessage)
		}
	}
}

func TestApplyDingTalkDispatchPromptSupportsDWSChatSurface(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-chat"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-chat", Text: "创建会话"}},
		}},
		Surface:  DispatchSurface{Type: "chat"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	})
	response := AgentTaskResponse{ChatSessionID: "chat-1", ChatMessage: "创建会话"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	for _, want := range []string{"## Trusted DingTalk Dispatch", "dws chat message reply", "创建会话"} {
		if !strings.Contains(response.ChatMessage, want) {
			t.Errorf("DWS chat task missing %q: %s", want, response.ChatMessage)
		}
	}
	if strings.Contains(response.ChatMessage, "two required final delivery destinations") {
		t.Fatalf("chat task received issue-only dual-delivery instruction: %s", response.ChatMessage)
	}
}

func TestApplyDingTalkDispatchPromptSupportsRobotIssueThroughDWS(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "robot"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-issue"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-issue", Text: "创建问题"}},
		}},
		Surface:  DispatchSurface{Type: "issue"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	})
	response := AgentTaskResponse{IssueID: "issue-1", HandoffNote: "已有交接"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	for _, want := range []string{"## Trusted DingTalk Dispatch", "two required final delivery destinations", "dws chat message reply", "已有交接"} {
		if !strings.Contains(response.HandoffNote, want) {
			t.Errorf("robot issue+DWS task missing %q: %s", want, response.HandoffNote)
		}
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
		Surface:  DispatchSurface{Type: "issue"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
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
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
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
