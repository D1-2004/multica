package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

func TestAgentIdentityGitHubBaseURLUsesControlURL(t *testing.T) {
	cfg := Config{
		AgentIdentityControlBaseURL: " https://pre-agent-identity.dingtalk.com/ ",
		FCE2B: service.FCE2BConfig{
			AgentIdentityBaseURL: "https://agent-identity.dingtalk.com",
		},
	}

	if got := agentIdentityGitHubBaseURL(cfg); got != "https://pre-agent-identity.dingtalk.com" {
		t.Fatalf("agentIdentityGitHubBaseURL = %q, want pre control URL", got)
	}
}

func TestAgentIdentityGitHubBaseURLUsesFCE2BControlURL(t *testing.T) {
	cfg := Config{
		FCE2B: service.FCE2BConfig{
			AgentIdentityControlBaseURL: " https://pre-agent-identity.dingtalk.com/ ",
			AgentIdentityBaseURL:        "https://agent-identity.dingtalk.com",
		},
	}

	if got := agentIdentityGitHubBaseURL(cfg); got != "https://pre-agent-identity.dingtalk.com" {
		t.Fatalf("agentIdentityGitHubBaseURL = %q, want FCE2B control URL", got)
	}
}

func TestAgentIdentityGitHubBaseURLFallsBackToSandboxURL(t *testing.T) {
	cfg := Config{
		FCE2B: service.FCE2BConfig{
			AgentIdentityBaseURL: " https://agent-identity.dingtalk.com/ ",
		},
	}

	if got := agentIdentityGitHubBaseURL(cfg); got != "https://agent-identity.dingtalk.com" {
		t.Fatalf("agentIdentityGitHubBaseURL = %q, want sandbox URL fallback", got)
	}
}
