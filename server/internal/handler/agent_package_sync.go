package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// applyPackageSourceConfiguration preserves omitted fields while applying
// explicit empty/false/null values, within the source publication transaction.
func (h *Handler) applyPackageSourceConfiguration(ctx context.Context, q *db.Queries, agent db.Agent, source preparedAgentSource, secrets map[string]string, deferred []string, pluginBindings map[string]string, actorID pgtype.UUID) error {
	if source.bundle.Definition == nil {
		return nil
	}
	request := CreateGitHubAgentRequest{Secrets: secrets, DeferredBindings: deferred, DshPluginBindings: pluginBindings}
	raw := map[string]json.RawMessage{}
	definition, err := preparePackageConfiguration(&request, raw, source.bundle)
	if err != nil {
		return err
	}
	if !agent.RuntimeID.Valid {
		return sourceRequestError(http.StatusConflict, "select a runtime before publishing configuration")
	}
	runtime, err := q.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID: agent.RuntimeID, WorkspaceID: agent.WorkspaceID})
	if err != nil {
		return err
	}
	if err := definition.validateRuntime(runtime); err != nil {
		return err
	}
	if !agentpkg.IsKnownThinkingValue(runtime.Provider, request.ThinkingLevel) || !agentpkg.IsKnownServiceTier(runtime.Provider, request.ServiceTier) {
		return sourceRequestError(http.StatusUnprocessableEntity, "manifest execution settings are incompatible with the selected runtime")
	}
	if len(request.ComposioToolkitAllowlist) > 0 && !h.composioMCPAppsEnabled(ctx) {
		return sourceRequestError(http.StatusUnprocessableEntity, "Composio apps are unavailable in this workspace")
	}
	c := definition.Configuration
	params := db.UpdateAgentParams{ID: agent.ID, RuntimeConfig: raw["runtime_config"], CustomEnv: raw["custom_env"], CustomArgs: raw["custom_args"], McpConfig: raw["mcp_config"]}
	if c.AvatarURL != nil {
		params.AvatarUrl = pgtype.Text{String: *c.AvatarURL, Valid: true}
	}
	if c.Model != nil {
		params.Model = pgtype.Text{String: *c.Model, Valid: true}
	}
	if c.ThinkingLevel != nil {
		params.ThinkingLevel = pgtype.Text{String: *c.ThinkingLevel, Valid: true}
	}
	if c.ServiceTier != nil {
		params.ServiceTier = pgtype.Text{String: *c.ServiceTier, Valid: true}
	}
	if c.MaxConcurrentTasks != nil {
		params.MaxConcurrentTasks = pgtype.Int4{Int32: *c.MaxConcurrentTasks, Valid: true}
	}
	if c.ComposioToolkitAllowlist != nil {
		params.ComposioToolkitAllowlist = normaliseComposioToolkitAllowlist(*c.ComposioToolkitAllowlist)
	}
	if request.PermissionMode != nil {
		permission, _, err := parsePermissionInput(agent.WorkspaceID, request.PermissionMode, request.InvocationTargets, true, true, nil)
		if err != nil {
			return sourceRequestError(http.StatusUnprocessableEntity, err.Error())
		}
		params.PermissionMode = pgtype.Text{String: permission.mode, Valid: true}
		params.Visibility = pgtype.Text{String: permission.legacyVisibility(), Valid: true}
		if err := replaceInvocationTargetsWithQueries(ctx, q, agent.ID, agent.OwnerID, permission.targets); err != nil {
			return err
		}
	}
	if _, err := q.UpdateAgent(ctx, params); err != nil {
		return err
	}
	if string(raw["mcp_config"]) == "null" {
		if _, err := q.ClearAgentMcpConfig(ctx, agent.ID); err != nil {
			return err
		}
	}
	if string(raw["composio_toolkit_allowlist"]) == "null" {
		if _, err := q.ClearAgentComposioToolkitAllowlist(ctx, agent.ID); err != nil {
			return err
		}
	}
	return definition.apply(ctx, q, agent, actorID)
}
