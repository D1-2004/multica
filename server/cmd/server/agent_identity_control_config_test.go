package main

import "testing"

func TestAgentIdentityControlBaseURLFromEnv(t *testing.T) {
	t.Setenv("MULTICA_AGENT_IDENTITY_CONTROL_BASE_URL", " https://pre-agent-identity.dingtalk.com/ ")

	if got := agentIdentityControlBaseURLFromEnv(); got != "https://pre-agent-identity.dingtalk.com" {
		t.Fatalf("control base url = %q, want trimmed prepub URL", got)
	}
}

func TestAgentIdentityControlBaseURLFromEnvDefaultsToPrepubInPrePublish(t *testing.T) {
	t.Setenv("AONE_ENV_TYPE", "prepub")
	t.Setenv("MULTICA_AGENT_IDENTITY_CONTROL_BASE_URL", "")

	if got := agentIdentityControlBaseURLFromEnv(); got != "https://pre-agent-identity.dingtalk.com" {
		t.Fatalf("control base url = %q, want prepub URL", got)
	}
}
