package main

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/handler"
)

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

// Native subscriptions use the gateway of the Agent Identity that issues
// their credentials; production keeps its existing Redis namespace.
func TestNativeSubscriptionsFollowTheIdentityEnvironment(t *testing.T) {
	for base, want := range map[string]string{
		"https://pre-agent-identity.dingtalk.com": "staging",
		"https://PRE-agent-identity.dingtalk.com": "staging",
		"https://agent-identity.dingtalk.com":     "production",
		"":                                        "production",
	} {
		if got := handler.NativeDWSEnvironmentFor(base); got != want {
			t.Errorf("NativeDWSEnvironmentFor(%q) = %q, want %q", base, got, want)
		}
	}
	if got := nativeSourceNamespace("production"); got != "native-v2:" {
		t.Errorf("production namespace = %q", got)
	}
	if got := nativeSourceNamespace("staging"); got != "native-v2-staging:" {
		t.Errorf("staging namespace = %q", got)
	}
}
