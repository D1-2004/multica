package handler

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func TestDispatchClaimComposesInstructionWithoutChangingUserContent(t *testing.T) {
	provider := featureflag.NewStaticProvider()
	provider.LoadRules(map[string]featureflag.Rule{
		featureflag.DispatchCommonRuntimePromptFlagKey: {Default: true, Variant: "COMMON POLICY"},
		featureflag.DispatchIssueRuntimePromptFlagKey:  {Default: true, Variant: "ISSUE POLICY"},
		featureflag.DispatchChatRuntimePromptFlagKey:   {Default: true, Variant: "CHAT POLICY"},
	})
	flags := featureflag.NewService(provider)
	issueContext := dispatchTaskContextWithPromptForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-1"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-1", Text: "用户消息"}},
		}},
		Surface:       DispatchSurface{Type: "issue"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	}, "ROUTER CONTEXT")
	chatContext := dispatchTaskContextWithPromptForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-1"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-1", Text: "用户消息"}},
		}},
		Surface:       DispatchSurface{Type: "chat"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	}, "ROUTER CONTEXT")
	commentID := "comment-1"
	tests := []struct {
		name         string
		context      []byte
		response     AgentTaskResponse
		assertStable func(*testing.T, AgentTaskResponse)
		want         string
	}{
		{
			name:     "initial issue keeps handoff note",
			context:  issueContext,
			response: AgentTaskResponse{IssueID: "issue-1", HandoffNote: "原始交接内容"},
			assertStable: func(t *testing.T, response AgentTaskResponse) {
				if response.HandoffNote != "原始交接内容" {
					t.Fatalf("handoff note changed: %q", response.HandoffNote)
				}
			},
			want: "COMMON POLICY\n\nISSUE POLICY\n\nROUTER CONTEXT",
		},
		{
			name:    "issue comment keeps trigger content",
			context: issueContext,
			response: AgentTaskResponse{
				IssueID:               "issue-1",
				TriggerCommentID:      &commentID,
				TriggerCommentContent: "原始评论内容",
			},
			assertStable: func(t *testing.T, response AgentTaskResponse) {
				if response.TriggerCommentContent != "原始评论内容" {
					t.Fatalf("trigger comment content changed: %q", response.TriggerCommentContent)
				}
			},
			want: "COMMON POLICY\n\nISSUE POLICY\n\nROUTER CONTEXT",
		},
		{
			name:     "chat keeps chat message",
			context:  chatContext,
			response: AgentTaskResponse{ChatSessionID: "chat-1", ChatMessage: "原始聊天内容"},
			assertStable: func(t *testing.T, response AgentTaskResponse) {
				if response.ChatMessage != "原始聊天内容" {
					t.Fatalf("chat message changed: %q", response.ChatMessage)
				}
			},
			want: "COMMON POLICY\n\nCHAT POLICY\n\nROUTER CONTEXT",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			applyDingTalkDispatchPromptForClaimWithFeatureFlags(&tc.response, tc.context, flags, true)
			tc.assertStable(t, tc.response)
			encoded, err := json.Marshal(tc.response)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatal(err)
			}
			var instruction string
			if raw, ok := payload["instruction"]; ok {
				if err := json.Unmarshal(raw, &instruction); err != nil {
					t.Fatal(err)
				}
			}
			if instruction != tc.want {
				t.Fatalf("instruction = %q, want %q", instruction, tc.want)
			}
		})
	}
}

func TestDispatchClaimFallsBackToLegacyTaskFieldsWithoutInstructionCapability(t *testing.T) {
	provider := featureflag.NewStaticProvider()
	provider.LoadRules(map[string]featureflag.Rule{
		featureflag.DispatchCommonRuntimePromptFlagKey: {Default: true, Variant: "COMMON POLICY"},
		featureflag.DispatchIssueRuntimePromptFlagKey:  {Default: true, Variant: "ISSUE POLICY"},
		featureflag.DispatchChatRuntimePromptFlagKey:   {Default: true, Variant: "CHAT POLICY"},
	})
	flags := featureflag.NewService(provider)
	issueContext := dispatchTaskContextWithPromptForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event:         DispatchEvent{Domain: "channel", Type: "message.created"},
		Surface:       DispatchSurface{Type: "issue"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	}, "ROUTER CONTEXT")
	chatContext := dispatchTaskContextWithPromptForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event:         DispatchEvent{Domain: "channel", Type: "message.created"},
		Surface:       DispatchSurface{Type: "chat"},
		Outbound:      DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	}, "ROUTER CONTEXT")
	commentID := "comment-1"
	tests := []struct {
		name     string
		context  []byte
		response AgentTaskResponse
		want     AgentTaskResponse
	}{
		{
			name:     "initial issue uses handoff note",
			context:  issueContext,
			response: AgentTaskResponse{IssueID: "issue-1", HandoffNote: "原始交接内容"},
			want: AgentTaskResponse{
				IssueID: "issue-1",
				HandoffNote: "COMMON POLICY\n\nISSUE POLICY\n\nROUTER CONTEXT" +
					"\n\n---\n\n## External DingTalk Message\n\n原始交接内容",
			},
		},
		{
			name:    "issue comment uses trigger content",
			context: issueContext,
			response: AgentTaskResponse{
				IssueID:               "issue-1",
				TriggerCommentID:      &commentID,
				TriggerCommentContent: "原始评论内容",
			},
			want: AgentTaskResponse{
				IssueID:          "issue-1",
				TriggerCommentID: &commentID,
				TriggerCommentContent: "COMMON POLICY\n\nISSUE POLICY\n\nROUTER CONTEXT" +
					"\n\n---\n\n## External DingTalk Message\n\n原始评论内容",
			},
		},
		{
			name:     "chat uses chat message",
			context:  chatContext,
			response: AgentTaskResponse{ChatSessionID: "chat-1", ChatMessage: "原始聊天内容"},
			want: AgentTaskResponse{
				ChatSessionID: "chat-1",
				ChatMessage: "COMMON POLICY\n\nCHAT POLICY\n\nROUTER CONTEXT" +
					"\n\n---\n\n## External DingTalk Message\n\n原始聊天内容",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			applyDingTalkDispatchPromptForClaimWithFeatureFlags(&tc.response, tc.context, flags, false)
			if tc.response.Instruction != "" {
				t.Fatalf("legacy claim instruction = %q, want empty", tc.response.Instruction)
			}
			if tc.response.HandoffNote != tc.want.HandoffNote {
				t.Fatalf("handoff note = %q, want %q", tc.response.HandoffNote, tc.want.HandoffNote)
			}
			if tc.response.TriggerCommentContent != tc.want.TriggerCommentContent {
				t.Fatalf("trigger content = %q, want %q", tc.response.TriggerCommentContent, tc.want.TriggerCommentContent)
			}
			if tc.response.ChatMessage != tc.want.ChatMessage {
				t.Fatalf("chat message = %q, want %q", tc.response.ChatMessage, tc.want.ChatMessage)
			}
		})
	}
}

func TestDispatchRuntimeContextPersistsContextPrompt(t *testing.T) {
	var command DispatchCommand
	if err := json.Unmarshal([]byte(`{
		"schemaVersion":"2.0",
		"contextPrompt":"ROUTER CONTEXT"
	}`), &command); err != nil {
		t.Fatal(err)
	}
	raw := dispatchRuntimeContext(command, "dispatch-key")
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	var contextPrompt string
	if encoded, ok := payload["dispatch_context_prompt"]; ok {
		if err := json.Unmarshal(encoded, &contextPrompt); err != nil {
			t.Fatal(err)
		}
	}
	if contextPrompt != "ROUTER CONTEXT" {
		t.Fatalf("persisted context prompt = %q", contextPrompt)
	}

	var emptyPayload map[string]json.RawMessage
	if err := json.Unmarshal(dispatchRuntimeContext(DispatchCommand{}, "dispatch-key"), &emptyPayload); err != nil {
		t.Fatal(err)
	}
	if _, ok := emptyPayload["dispatch_context_prompt"]; ok {
		t.Fatalf("empty context prompt was persisted: %s", emptyPayload["dispatch_context_prompt"])
	}
}

func dispatchTaskContextWithPromptForTest(t *testing.T, command DispatchCommand, contextPrompt string) []byte {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(dispatchTaskContextForTest(t, command), &payload); err != nil {
		t.Fatal(err)
	}
	payload["dispatch_context_prompt"] = contextPrompt
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
