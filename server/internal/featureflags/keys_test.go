package featureflags

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/pkg/featureflag"
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
}

func TestWorkspaceMCPDefaultsOnInEveryEnvironment(t *testing.T) {
	for _, env := range []string{"production", "prepub", "development", ""} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("AONE_ENV_TYPE", env)
			t.Setenv("APP_ENV", "production")
			ctx := context.Background()
			public := EvaluateFrontendPublicFlags(ctx, nil)
			if !WorkspaceMCPEndpointEnabled(ctx, nil) || !public[WorkspaceMCPEndpoint] {
				t.Fatal("workspace endpoint and settings UI must default on")
			}
			if WorkspaceMCPReplaceAgentLinksEnabled(ctx, nil) || public[WorkspaceMCPReplaceAgentLinks] {
				t.Fatal("workspace MCP must not replace the agent MCP entry")
			}
		})
	}
}

func TestWorkspaceMCPExplicitOffStillDisablesAPIAndUI(t *testing.T) {
	t.Setenv("AONE_ENV_TYPE", "production")
	t.Setenv("FF_WORKSPACE_MCP_ENDPOINT_ENABLED", "false")
	flags := featureflag.NewService(featureflag.NewEnvProvider("FF_"))
	ctx := context.Background()
	if WorkspaceMCPEndpointEnabled(ctx, flags) || EvaluateFrontendPublicFlags(ctx, flags)[WorkspaceMCPEndpoint] {
		t.Fatal("explicit operator override must disable the endpoint and UI together")
	}
}

// MUL-5345: hang stack capture is gone from this build, but v0.4.13–v0.4.18 are
// installed and still hold a debugger channel open on every renderer whenever
// this key arrives as `true`. Those clients are fail-closed on absence, so NOT
// publishing the key is what disarms them — re-adding it would put a flag flip
// back within reach of a fleet that can no longer produce a usable stack.
func TestDesktopHangStackCaptureIsNotPublished(t *testing.T) {
	flags := EvaluateFrontendPublicFlags(context.Background(), nil)
	if _, published := flags["desktop_hang_stack_capture"]; published {
		t.Fatal("hang stack capture must stay unpublished so installed clients keep their debugger channels closed")
	}
}

func TestAgentSkillTogglesCompatDecisionStaysEnabled(t *testing.T) {
	flags := EvaluateFrontendPublicFlags(context.Background(), nil)
	if !flags[agentSkillTogglesCompat] {
		t.Fatal("agent skill toggles must stay enabled for installed v0.4.0 clients")
	}
}
