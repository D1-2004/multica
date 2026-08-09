package a2aintegration

import (
	"encoding/json"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestBuildAgentCardJSON(t *testing.T) {
	t.Parallel()

	card, err := BuildAgentCard(CardConfig{
		BaseURL:       "https://multica.example.com/",
		PublicAgentID: "agt_public_123",
		Name:          "Coding Agent",
		Description:   "Builds local projects.",
		Version:       "2026.08.09",
		Skills: []a2a.AgentSkill{
			{
				ID:          "coding",
				Name:        "Coding",
				Description: "Implements software tasks.",
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildAgentCard() error = %v", err)
	}

	encoded, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	want := `{"supportedInterfaces":[{"url":"https://multica.example.com/api/a2a/agents/agt_public_123/v1","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"description":"Builds local projects.","name":"Coding Agent","securityRequirements":[{"schemes":{"bearerAuth":[]}}],"securitySchemes":{"bearerAuth":{"httpAuthSecurityScheme":{"bearerFormat":"opaque","description":"Opaque Multica A2A access token.","scheme":"bearer"}}},"skills":[{"description":"Implements software tasks.","id":"coding","name":"Coding","tags":[]}],"version":"2026.08.09"}`
	if string(encoded) != want {
		t.Fatalf("Agent Card JSON mismatch\n got: %s\nwant: %s", encoded, want)
	}
}

func TestBuildAgentCardNormalizesNilCollections(t *testing.T) {
	t.Parallel()

	card, err := BuildAgentCard(CardConfig{
		BaseURL:       "http://127.0.0.1:8080/base",
		PublicAgentID: "agent_1",
		Name:          "Agent",
		Version:       "1",
	})
	if err != nil {
		t.Fatalf("BuildAgentCard() error = %v", err)
	}
	if card.Skills == nil {
		t.Fatal("BuildAgentCard() Skills = nil, want non-nil empty slice")
	}
	if len(card.Skills) != 0 {
		t.Fatalf("BuildAgentCard() Skills length = %d, want 0", len(card.Skills))
	}
	if got := card.SupportedInterfaces[0].Tenant; got != "" {
		t.Fatalf("BuildAgentCard() interface tenant = %q, want empty", got)
	}
	if got, want := card.SupportedInterfaces[0].URL, "http://127.0.0.1:8080/base/api/a2a/agents/agent_1/v1"; got != want {
		t.Fatalf("BuildAgentCard() interface URL = %q, want %q", got, want)
	}
	if card.Capabilities.Streaming || card.Capabilities.PushNotifications || card.Capabilities.ExtendedAgentCard {
		t.Fatalf("BuildAgentCard() capabilities = %+v, want all optional capabilities disabled", card.Capabilities)
	}
}

func TestAgentURLsRejectUnsafePublicAgentID(t *testing.T) {
	t.Parallel()

	got, err := AgentCardURL("https://multica.example.com/", "agent_1")
	if err != nil {
		t.Fatalf("AgentCardURL() error = %v", err)
	}
	if want := "https://multica.example.com/api/a2a/agents/agent_1/.well-known/agent-card.json"; got != want {
		t.Fatalf("AgentCardURL() = %q, want %q", got, want)
	}

	if _, err := AgentCardURL("https://multica.example.com", "../other-agent"); err == nil {
		t.Fatal("AgentCardURL() error = nil, want unsafe path error")
	}
}
