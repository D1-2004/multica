package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// importPackageConfiguration preserves omitted fields while applying
// explicit empty/false/null values, within the source publication transaction.
func (h *Handler) importPackageConfiguration(ctx context.Context, tx pgx.Tx, q *db.Queries, agent db.Agent, value map[string]json.RawMessage, actorID pgtype.UUID) error {
	raw := value
	encoded, err := json.Marshal(value)
	if err != nil { return err }
	var request CreateAgentPackageRequest
	var c UpdateAgentRequest
	if err := json.Unmarshal(encoded, &request); err != nil { return err }
	if err := json.Unmarshal(encoded, &c); err != nil { return err }
	// Preserve the existing rule: disabling inbound wins a conflicting package;
	// enabling events otherwise enables inbound in the same transaction.
	if c.EventTriggerEnabled != nil && c.InboundCoordinator != nil && !*c.InboundCoordinator { disabled := false; c.EventTriggerEnabled = &disabled }
	if c.EventTriggerEnabled != nil && *c.EventTriggerEnabled {
		if h.EventTriggers == nil { return sourceRequestError(http.StatusServiceUnavailable,"event triggers are unavailable") }
		enabled := true; c.InboundCoordinator = &enabled
	}
	definition := packageConfiguration{Configuration:c}
	if !agent.RuntimeID.Valid { return sourceRequestError(http.StatusConflict, "select a runtime before publishing configuration") }
	runtime, err := q.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID:agent.RuntimeID, WorkspaceID:agent.WorkspaceID})
	if err != nil { return err }
	if err := definition.validateRuntime(runtime); err != nil { return err }
	if !agentpkg.IsKnownThinkingValue(runtime.Provider, request.ThinkingLevel) || !agentpkg.IsKnownServiceTier(runtime.Provider, request.ServiceTier) { return sourceRequestError(http.StatusUnprocessableEntity, "manifest execution settings are incompatible with the selected runtime") }
	if len(request.ComposioToolkitAllowlist) > 0 && !h.composioMCPAppsEnabled(ctx) { return sourceRequestError(http.StatusUnprocessableEntity, "Composio apps are unavailable in this workspace") }
	params := db.UpdateAgentParams{ID:agent.ID, RuntimeConfig:raw["runtime_config"], CustomEnv:raw["custom_env"], CustomArgs:raw["custom_args"], McpConfig:raw["mcp_config"]}
	if c.AvatarURL != nil { params.AvatarUrl = pgtype.Text{String:*c.AvatarURL, Valid:true} }
	if c.Model != nil { params.Model = pgtype.Text{String:*c.Model, Valid:true} }
	if c.ThinkingLevel != nil { params.ThinkingLevel = pgtype.Text{String:*c.ThinkingLevel, Valid:true} }
	if c.ServiceTier != nil { params.ServiceTier = pgtype.Text{String:*c.ServiceTier, Valid:true} }
	if c.MaxConcurrentTasks != nil { params.MaxConcurrentTasks = pgtype.Int4{Int32:*c.MaxConcurrentTasks, Valid:true} }
	if c.ComposioToolkitAllowlist != nil { params.ComposioToolkitAllowlist = normaliseComposioToolkitAllowlist(*c.ComposioToolkitAllowlist) }
	if _, err := q.UpdateAgent(ctx, params); err != nil { return err }
	if string(raw["mcp_config"]) == "null" { if _, err := q.ClearAgentMcpConfig(ctx, agent.ID); err != nil { return err } }
	if string(raw["composio_toolkit_allowlist"]) == "null" { if _, err := q.ClearAgentComposioToolkitAllowlist(ctx, agent.ID); err != nil { return err } }
	if err := definition.importConfiguration(ctx, q, agent, actorID); err != nil { return err }
	if c.EventTriggerEnabled != nil { return service.SetEventTriggerEnabledAndInboundInTx(ctx,tx,agent,actorID,*c.EventTriggerEnabled,c.InboundCoordinator) }
	return nil
}
