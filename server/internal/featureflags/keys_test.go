package featureflags

import (
	"context"
	"testing"
)

func TestReleaseFlagsDefaultToOff(t *testing.T) {
	ctx := context.Background()
	if AgentBuilderEnabled(ctx, nil) {
		t.Fatal("agent builder release flag must default to off")
	}
	if ResourceLabelsEnabled(ctx, nil) {
		t.Fatal("resource labels release flag must default to off")
	}
	if WorkspaceAccessTokensEnabled(ctx, nil) {
		t.Fatal("workspace access tokens release flag must default to off")
	}
	if MulticaMCPChatSendEnabled(ctx, nil) {
		t.Fatal("Multica MCP Chat send release flag must default to off")
	}
	if AgentA2AInboundEnabled(ctx, nil) {
		t.Fatal("agent A2A inbound release flag must default to off")
	}
}

func TestAgentA2AInboundIsPublishedToFrontend(t *testing.T) {
	flags := EvaluateFrontendPublicFlags(context.Background(), nil)
	value, ok := flags[AgentA2AInbound]
	if !ok {
		t.Fatal("agent A2A inbound release flag must be published to the frontend")
	}
	if value {
		t.Fatal("agent A2A inbound release flag must default to off")
	}
}

func TestAgentSkillTogglesCompatDecisionStaysEnabled(t *testing.T) {
	flags := EvaluateFrontendPublicFlags(context.Background(), nil)
	if !flags[agentSkillTogglesCompat] {
		t.Fatal("agent skill toggles must stay enabled for installed v0.4.0 clients")
	}
}
