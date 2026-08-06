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
	if instruction := buildDispatchInstruction(nil, c.Surface.Type, c.ContextPrompt); instruction != "ROUTER CONTEXT" {
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
	if instruction := buildDispatchInstruction(nil, c.Surface.Type, c.ContextPrompt); instruction != "CALENDAR CONTEXT" {
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
		Surface:          DispatchSurface{Type: "issue"},
		Outbound:         DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt:    "CALENDAR DWS CONTEXT",
		ExternalIdentity: AgentDispatchExternalIdentity{
			DWS: &AgentDispatchDWSIdentity{UID: "123", OrgID: "456"},
		},
	}
	if err := c.validate(); err != nil {
		t.Fatalf("valid calendar dispatch with dws outbound rejected: %v", err)
	}
	if instruction := buildDispatchInstruction(nil, c.Surface.Type, c.ContextPrompt); instruction != "CALENDAR DWS CONTEXT" {
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

	instruction := buildDispatchInstruction(flags, command.Surface.Type, command.ContextPrompt)
	if want := "COMMON POLICY\n\nAUTO POLICY\n\nROUTER CONTEXT"; instruction != want {
		t.Fatalf("instruction = %q, want %q", instruction, want)
	}
	if _, _, err := provider.ApplyJSON([]byte(`{
	  "common":{"prompt":"UPDATED COMMON POLICY"},
	  "auto":{"prompt":"UPDATED AUTO POLICY"}
	}`)); err != nil {
		t.Fatalf("update Diamond prompt: %v", err)
	}
	instruction = buildDispatchInstruction(flags, command.Surface.Type, command.ContextPrompt)
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
		instruction := buildDispatchInstruction(flags, command.Surface.Type, command.ContextPrompt)
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

	instruction := buildDispatchInstruction(nil, command.Surface.Type, command.ContextPrompt)
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
	instruction := buildDispatchInstruction(nil, command.Surface.Type, command.ContextPrompt)
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
			if tc.response.Instruction != "ROUTER CONTEXT" {
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
	if response.Instruction != "ROUTER CONTEXT" {
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
	if response.Instruction != "ROUTER CONTEXT" {
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
	if response.Instruction != "ROUTER CONTEXT" {
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
	if response.Instruction != "ROUTER CONTEXT" {
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
	if response.Instruction != "ROUTER CONTEXT" {
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
	if response.Instruction != "ROUTER CONTEXT" {
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
	if response.Instruction != "CALENDAR DWS CONTEXT" {
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
	if instruction := buildDispatchInstruction(nil, c.Surface.Type, c.ContextPrompt); instruction != "APPROVAL CONTEXT" {
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
		Surface:          DispatchSurface{Type: "issue"},
		Outbound:         DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ContextPrompt:    "APPROVAL DWS CONTEXT",
		ExternalIdentity: AgentDispatchExternalIdentity{
			DWS: &AgentDispatchDWSIdentity{UID: "123", OrgID: "456"},
		},
	}
	if err := c.validate(); err != nil {
		t.Fatalf("valid approval dispatch with dws outbound rejected: %v", err)
	}
	if instruction := buildDispatchInstruction(nil, c.Surface.Type, c.ContextPrompt); instruction != "APPROVAL DWS CONTEXT" {
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
	if response.Instruction != "APPROVAL DWS CONTEXT" {
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

	instruction := buildDispatchInstruction(nil, c.Surface.Type, c.ContextPrompt)
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
