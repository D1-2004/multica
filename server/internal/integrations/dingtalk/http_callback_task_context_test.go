package dingtalk

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestHTTPCallbackTaskContextCarriesDispatchPolicyAndExternalIdentity(t *testing.T) {
	dispatchContext := json.RawMessage(`{
		"dispatch_schema_version":"2.0",
		"dispatch_source":{"platform":"dingtalk","type":"robot"},
		"dispatch_surface":{"type":"chat"},
		"dispatch_outbound":{"mode":"dws","replyTo":"latest_message"},
		"dispatch_workflow_prompt":"trusted DWS workflow"
	}`)
	input := HTTPCallbackMessage{
		ConversationID:       "conversation-1",
		ConversationType:     "single",
		MessageID:            "message-1",
		SenderID:             "sender-1",
		SenderStaffID:        "staff-1",
		Text:                 "hello",
		IdentityContextToken: "sealed-router-context",
		DispatchContext:      dispatchContext,
	}
	message, err := InboundFromHTTPCallback(input, "client-1", "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("InboundFromHTTPCallback: %v", err)
	}

	contextJSON, err := (&robotTaskContextResolver{}).ResolveTaskContext(
		context.Background(), engine.ResolvedInstallation{}, message,
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatal(err)
	}
	var token string
	if err := json.Unmarshal(payload[protocol.AgentIdentityContextTokenJSONKey], &token); err != nil {
		t.Fatal(err)
	}
	if token != "sealed-router-context" {
		t.Fatalf("context token = %q", token)
	}
	for _, key := range []string{
		protocol.DispatchSurfaceJSONKey,
		protocol.DispatchOutboundJSONKey,
		protocol.DispatchWorkflowPromptJSONKey,
	} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("task context missing %q: %s", key, contextJSON)
		}
	}
}

func TestAgentDispatchNormalizationDoesNotRequireRobotInstallation(t *testing.T) {
	dispatchContext := json.RawMessage(`{
		"dispatch_schema_version":"2.0",
		"dispatch_source":{"platform":"dingtalk","type":"digital_employee"},
		"dispatch_surface":{"type":"chat"},
		"dispatch_outbound":{"mode":"dws","replyTo":"latest_message"}
	}`)
	message, err := InboundFromAgentDispatch(AgentDispatchMessage{
		ConversationID:       "conversation-digital-employee",
		ConversationType:     "group",
		ConversationTitle:    "数字员工群",
		MessageID:            "message-digital-employee",
		SenderID:             "sender-digital-employee",
		SenderName:           "张三",
		Text:                 "hello",
		IdentityContextToken: "sealed-digital-employee-context",
		DispatchContext:      dispatchContext,
	})
	if err != nil {
		t.Fatalf("InboundFromAgentDispatch: %v", err)
	}
	raw, err := decodeDingTalkRaw(message)
	if err != nil {
		t.Fatal(err)
	}
	if raw.ClientID != "" || raw.InstallationID != "" {
		t.Fatalf("agent dispatch fabricated robot routing: client=%q installation=%q", raw.ClientID, raw.InstallationID)
	}
	if raw.AgentIdentityContextToken != "sealed-digital-employee-context" {
		t.Fatalf("identity context token = %q", raw.AgentIdentityContextToken)
	}
	var gotContext, wantContext any
	if err := json.Unmarshal(raw.DispatchContext, &gotContext); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(dispatchContext, &wantContext); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotContext, wantContext) {
		t.Fatalf("dispatch context = %s, want %s", raw.DispatchContext, dispatchContext)
	}
	if message.Source.ChatID != "conversation-digital-employee" ||
		message.Source.SenderID != "sender-digital-employee" {
		t.Fatalf("normalized source = %+v", message.Source)
	}
}

func TestAgentDispatchNormalizationErrorsUseDispatchTerms(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     AgentDispatchMessage
		wantError string
	}{
		{
			name: "missing message id",
			input: AgentDispatchMessage{
				ConversationID: "conversation-digital-employee",
				Text:           "hello",
			},
			wantError: "dingtalk: agent dispatch message id is required",
		},
		{
			name: "invalid dispatch context",
			input: AgentDispatchMessage{
				ConversationID:  "conversation-digital-employee",
				MessageID:       "message-digital-employee",
				Text:            "hello",
				DispatchContext: json.RawMessage(`{`),
			},
			wantError: "dingtalk: encode agent dispatch context",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := InboundFromAgentDispatch(tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("agent dispatch error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestHTTPCallbackNormalizationRetainsCallbackErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     HTTPCallbackMessage
		wantError string
	}{
		{
			name: "missing message id",
			input: HTTPCallbackMessage{
				ConversationID: "conversation-robot",
				Text:           "hello",
			},
			wantError: "dingtalk: HTTP callback message id is required",
		},
		{
			name: "invalid dispatch context",
			input: HTTPCallbackMessage{
				ConversationID:  "conversation-robot",
				MessageID:       "message-robot",
				Text:            "hello",
				DispatchContext: json.RawMessage(`{`),
			},
			wantError: "dingtalk: encode HTTP callback context",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := InboundFromHTTPCallback(
				tc.input,
				"client-robot",
				"11111111-1111-1111-1111-111111111111",
			)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("HTTP callback error = %v, want %q", err, tc.wantError)
			}
		})
	}
}
