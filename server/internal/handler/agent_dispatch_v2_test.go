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
		ExternalIdentity: AgentDispatchExternalIdentity{ContextToken: "context-token", ExpiresAt: 4102444800000},
	}
	if err := c.validate(); err != nil {
		t.Fatalf("valid calendar dispatch rejected: %v", err)
	}
	prompt := mustBuildDispatchPrompt(t, c)
	if !strings.Contains(prompt.DisplayContent, "项目评审会") || strings.Contains(prompt.DisplayContent, "uid-secret") {
		t.Fatalf("calendar display content = %q", prompt.DisplayContent)
	}
	if !strings.Contains(prompt.RuntimePrompt, "untrusted") || prompt.WorkflowPrompt != "" {
		t.Fatalf("calendar prompt must be safe and have no outbound workflow: %#v", prompt)
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

func TestRouterDispatchPromptDelegatesAcknowledgementReactionToRouter(t *testing.T) {
	c := DispatchCommand{
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-trusted"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender-trusted"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-latest", Text: "在吗"}},
		}},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		CompletionCallback: &DispatchCompletionCallback{
			URL: "/api/v1/dispatch-tasks/router-task-reaction/execution-result",
		},
	}

	workflowPrompt := mustBuildDispatchPrompt(t, c).WorkflowPrompt
	for _, required := range []string{
		"mark the exact target message as read",
		"Do not substitute a read-status query",
		"dws chat message reply",
		"success, partial success, blocked, or failed",
	} {
		if !strings.Contains(workflowPrompt, required) {
			t.Errorf("Router dispatch workflow prompt missing %q: %q", required, workflowPrompt)
		}
	}
	for _, forbidden := range []string{
		"dws chat message add-emoji",
		"dws chat message create-text-emotion",
		"dws chat message add-text-emotion",
		"acknowledge it with exactly one reaction",
		"acknowledgement reaction",
	} {
		if strings.Contains(workflowPrompt, forbidden) {
			t.Errorf("Router dispatch workflow prompt retained acknowledgement reaction %q: %q", forbidden, workflowPrompt)
		}
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

func TestApplyRouterDispatchPromptDoesNotRestoreAgentAcknowledgementReaction(t *testing.T) {
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-trusted"},
			Sender:       DispatchSender{OpenDingTalkID: "open-sender-trusted"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-latest", Text: "在吗"}},
		}},
		Surface:  DispatchSurface{Type: "issue"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		CompletionCallback: &DispatchCompletionCallback{
			URL: "/api/v1/dispatch-tasks/router-task-reaction/execution-result",
		},
	})
	response := AgentTaskResponse{IssueID: "issue-1", HandoffNote: "处理消息"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	for _, required := range []string{
		"mark the exact target message as read",
		"dws chat message reply",
		"处理消息",
	} {
		if !strings.Contains(response.HandoffNote, required) {
			t.Errorf("Router task prompt missing %q: %s", required, response.HandoffNote)
		}
	}
	for _, forbidden := range []string{
		"dws chat message add-emoji",
		"dws chat message create-text-emotion",
		"dws chat message add-text-emotion",
	} {
		if strings.Contains(response.HandoffNote, forbidden) {
			t.Errorf("Router task prompt restored acknowledgement reaction %q: %s", forbidden, response.HandoffNote)
		}
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

func TestApplyDingTalkDispatchPromptKeepsCalendarTaskOutboundFree(t *testing.T) {
	start := int64(1784217600000)
	context := dispatchTaskContextForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "calendar", Type: "calendar.started", Data: DispatchEventData{
			CalendarID: "calendar-1", Subject: "项目评审会", StartTime: &start,
			AIReadableContent: "日程「项目评审会」已经开始。\n请检查设计方案并推进待办。",
		}},
		Surface:  DispatchSurface{Type: "issue"},
		Outbound: DispatchOutbound{Mode: "none"},
	})
	response := AgentTaskResponse{IssueID: "issue-1", HandoffNote: "保留已有交接说明"}

	applyDingTalkDispatchPromptToExistingTaskFields(&response, context)

	for _, want := range []string{
		"## Trusted DingTalk Dispatch",
		"untrusted input",
		"## External DingTalk Calendar Event",
		"保留已有交接说明",
	} {
		if !strings.Contains(response.HandoffNote, want) {
			t.Errorf("calendar task handoff missing %q: %s", want, response.HandoffNote)
		}
	}
	for _, forbidden := range []string{"dws chat message add-emoji", "dws chat message reply", "two required final delivery destinations"} {
		if strings.Contains(response.HandoffNote, forbidden) {
			t.Fatalf("calendar task received outbound workflow %q: %s", forbidden, response.HandoffNote)
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
	if command.CompletionCallback != nil {
		payload["completion_callback"] = command.CompletionCallback
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
