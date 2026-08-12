package a2aintegration

import (
	"encoding/json"
	"strings"
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

func TestBuildAgentCardDeclaresBaseTaskSkillWhenNoSkillsAreConfigured(t *testing.T) {
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
	if len(card.Skills) != 1 {
		t.Fatalf("BuildAgentCard() Skills length = %d, want the base task skill", len(card.Skills))
	}
	if got, want := card.Skills[0].ID, "multica-agent-task"; got != want {
		t.Fatalf("BuildAgentCard() base skill ID = %q, want %q", got, want)
	}
	if card.Skills[0].Name == "" || card.Skills[0].Description == "" || card.Skills[0].Tags == nil {
		t.Fatalf("BuildAgentCard() base skill is incomplete: %#v", card.Skills[0])
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

func TestMergeConfiguredAgentSkillsPublishesStableOpaqueCapabilities(t *testing.T) {
	t.Parallel()

	declared := []a2a.AgentSkill{{
		ID:          "custom-review",
		Name:        "Custom review",
		Description: "Reviews a submitted change.",
	}}
	configured := []ConfiguredAgentSkill{
		{
			ID:          "9dacba10-0cc7-4761-96e1-a8c87a763a16",
			Name:        "agent-message-router-observability",
			Description: "Queries message routing observations.",
		},
		{
			ID:   "c58b7310-4e81-4f2b-82f8-ab4fa0690969",
			Name: "dws",
		},
	}

	got, err := MergeConfiguredAgentSkills(declared, configured)
	if err != nil {
		t.Fatalf("MergeConfiguredAgentSkills() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("MergeConfiguredAgentSkills() length = %d, want 3", len(got))
	}
	if got[0].ID != declared[0].ID {
		t.Fatalf("declared skill changed: %#v", got[0])
	}
	for index, skill := range got[1:] {
		if !strings.HasPrefix(skill.ID, "multica-skill-") {
			t.Fatalf("configured skill %d ID = %q, want opaque Multica ID", index, skill.ID)
		}
		if strings.Contains(skill.ID, configured[index].ID) {
			t.Fatalf("configured skill %d leaked its database ID: %q", index, skill.ID)
		}
		if skill.Name != configured[index].Name || skill.Description == "" || skill.Tags == nil {
			t.Fatalf("configured skill %d is incomplete: %#v", index, skill)
		}
	}

	again, err := MergeConfiguredAgentSkills(nil, configured[:1])
	if err != nil {
		t.Fatalf("second MergeConfiguredAgentSkills() error = %v", err)
	}
	if again[0].ID != got[1].ID {
		t.Fatalf("configured skill ID is not stable: first=%q second=%q", got[1].ID, again[0].ID)
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

	mcpURL, err := AgentMCPURL("https://multica.example.com/base", "agent_1")
	if err != nil {
		t.Fatalf("AgentMCPURL() error = %v", err)
	}
	if want := "https://multica.example.com/base/api/mcp/agents/agent_1"; mcpURL != want {
		t.Fatalf("AgentMCPURL() = %q, want %q", mcpURL, want)
	}
}
