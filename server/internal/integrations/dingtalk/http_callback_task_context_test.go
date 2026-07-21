package dingtalk

import (
	"context"
	"encoding/json"
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
