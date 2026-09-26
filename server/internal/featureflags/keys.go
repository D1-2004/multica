package featureflags

import (
	"context"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

const (
	// ComposioMCPApps gates the Composio app management UI and — together with
	// the MUL-3963 permission_mode / invocation_targets access model it depends
	// on — the aligned Private / Public-to picker in the agent create flow.
	// The access model exists to gate Composio sharing, so the two ship on the
	// same switch.
	ComposioMCPApps = "composio_mcp_apps"
	// AgentBuilder controls writes of system builder agents in the internal
	// rolling-release path.
	AgentBuilder = "agents_agent_builder"
	// ResourceLabels controls the agent- and skill-scoped label namespaces.
	ResourceLabels = "settings_resource_labels"
	// WorkspaceAccessTokens gates issuance and use of workspace-bound DTA
	// credentials during the additive-schema / rolling-server rollout.
	WorkspaceAccessTokens         = "workspace_access_tokens"
	WorkspaceMCPEndpoint          = "workspace_mcp_endpoint_enabled"
	WorkspaceMCPReplaceAgentLinks = "workspace_mcp_replace_agent_links"
	// agentSkillTogglesCompat is no longer a release flag. Keep publishing the
	// key as enabled so installed v0.4.0 desktop clients, which still gate the
	// switch on this config decision, receive the permanently enabled behavior.
	agentSkillTogglesCompat = "agents_skill_toggles"
)

var frontendPublicFlags = []string{
	ComposioMCPApps,
	AgentBuilder,
	ResourceLabels,
	WorkspaceAccessTokens,
	WorkspaceMCPEndpoint,
	WorkspaceMCPReplaceAgentLinks,
}

func ComposioMCPAppsEnabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, ComposioMCPApps, false)
}

func AgentBuilderEnabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, AgentBuilder, false)
}

func ResourceLabelsEnabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, ResourceLabels, false)
}

func WorkspaceAccessTokensEnabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, WorkspaceAccessTokens, false)
}

func workspaceMCPPreReleaseDefault() bool {
	for _, name := range []string{"AONE_ENV_TYPE", "ENV_TYPE", "APP_ENV", "GO_ENV"} {
		value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
		if value == "" {
			continue
		}
		return value == "prepub" || value == "pre" || value == "prepublish" || value == "staging" || value == "stage"
	}
	return false
}

func WorkspaceMCPEndpointEnabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, WorkspaceMCPEndpoint, workspaceMCPPreReleaseDefault())
}

func WorkspaceMCPReplaceAgentLinksEnabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, WorkspaceMCPReplaceAgentLinks, workspaceMCPPreReleaseDefault())
}

func EvaluateFrontendPublicFlags(ctx context.Context, flags *featureflag.Service) map[string]bool {
	out := make(map[string]bool, len(frontendPublicFlags)+1)
	for _, key := range frontendPublicFlags {
		defaultValue := false
		if key == WorkspaceMCPEndpoint || key == WorkspaceMCPReplaceAgentLinks {
			defaultValue = workspaceMCPPreReleaseDefault()
		}
		out[key] = flags.IsEnabled(ctx, key, defaultValue)
	}
	out[agentSkillTogglesCompat] = true
	return out
}
