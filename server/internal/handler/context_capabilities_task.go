package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Binding layers recorded on a resolved connector and in the connector call
// audit: why this task may use the connector.
const (
	connectorBindingGlobal = contextcap.LayerGlobal
	connectorBindingOrg    = contextcap.LayerOrg
	connectorBindingScene  = contextcap.LayerScene
	connectorBindingPerson = contextcap.LayerPerson
)

// Credential layers recorded on a resolved connector and in the connector call
// audit: which credential the relay sends upstream.
const (
	connectorCredentialNone      = "none"
	connectorCredentialWorkspace = "workspace"
	connectorCredentialOrg       = contextcap.ScopeOrg
	connectorCredentialScene     = contextcap.ScopeScene
	connectorCredentialPerson    = contextcap.ScopePerson
)

// Why a task gets no org, scene or personal layer (resolveTaskContextScope).
const (
	// taskContextNoDispatch: no DingTalk dispatch context (web comments,
	// autopilot, plain chat) or a replayed one (a manual rerun).
	taskContextNoDispatch = "no_dispatch"
	taskContextA2A        = "a2a"
	taskContextRerun      = "rerun"
	// taskContextNotTenant: the task's org is not one of the agent's tenants.
	taskContextNotTenant = "not_tenant"
	// taskContextEarlierBinding: no recorded dispatch org, and the task is
	// older than the agent's current DingTalk binding.
	taskContextEarlierBinding = "earlier_binding"
	taskContextLookupFailed   = "lookup_failed"
)

// contextCredentialBox returns the internal connector secret box as a
// contextcap.Box, or a nil interface when no key is configured (never a typed
// nil pointer, which would panic inside Seal/Open).
func (h *Handler) contextCredentialBox() contextcap.Box {
	if h.InternalConnectorSecretBox == nil {
		return nil
	}
	return h.InternalConnectorSecretBox
}

// taskContextScope derives the context scope of a task
// (docs/context-capabilities.md §2): its tenant org, group scene and trigger
// person, from its server-written dispatch context. It returns the zero
// Scope, so only the global layer applies, for A2A-origin tasks, manual
// reruns, tasks without a DingTalk dispatch context, tasks whose org is not a
// tenant of the agent, and when a lookup fails. workspaceID must be the
// task's workspace (agent_task_queue has none).
func (h *Handler) taskContextScope(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue) contextcap.Scope {
	scope, _ := h.resolveTaskContextScope(ctx, workspaceID, task)
	return scope
}

// resolveTaskContextScope is taskContextScope that also says why the task
// gets no layer ("" when its scope applies).
//
// The org is the dispatch's recorded agent org (external_identity.dws.orgId),
// else the agent's DingTalk identity org; an agent without either keeps
// org "" (scene and person layers, no org layer), the scope its mobile
// configuration and links write. A staffId or openConversationId only
// means something inside the org it was dispatched in, so the layers apply
// only while that org is one of the agent's tenants (contextcap.AgentTenant):
// a task from a deleted tenant, an org without a tenant or an earlier binding
// never reads another org's bindings or credentials. Retries and Coordinator
// item tasks carry the original dispatch context, so the recorded org is the
// reliable signal; a task without one that was created before the agent was
// (re)bound may come from the earlier binding and gets no layer either.
func (h *Handler) resolveTaskContextScope(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue) (contextcap.Scope, string) {
	if service.IsA2ATaskOrigin(task.Context) {
		return contextcap.Scope{}, taskContextA2A
	}
	scope := contextcap.ScopeFromTaskContext(task.Context)
	if !scope.Dispatched {
		return contextcap.Scope{}, taskContextNoDispatch
	}
	if task.RerunOfTaskID.Valid {
		return contextcap.Scope{}, taskContextRerun
	}
	if h.Queries == nil || h.DB == nil {
		return contextcap.Scope{}, taskContextLookupFailed
	}
	orgID := scope.DispatchOrgID
	if orgID == "" {
		identity, err := h.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: workspaceID, AgentID: task.AgentID})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// An agent without a DingTalk identity (a robot channel) keeps
			// its scene and personal configuration under org "", its
			// implicit tenant (contextCapAgentInOrg, configuration links):
			// the scene and person layers apply, there is no org layer.
			return scope, ""
		case err != nil:
			slog.WarnContext(ctx, "context capabilities: agent DingTalk identity unavailable; skipping org, scene and personal layers",
				"task_id", uuidToString(task.ID), "agent_id", uuidToString(task.AgentID), "error", err)
			return contextcap.Scope{}, taskContextLookupFailed
		case identity.BoundAt.Valid && task.CreatedAt.Valid && identity.BoundAt.Time.After(task.CreatedAt.Time):
			return contextcap.Scope{}, taskContextEarlierBinding
		}
		orgID = strings.TrimSpace(identity.OrgID)
		if orgID == "" {
			// An identity without an org is orgless too.
			return scope, ""
		}
	}
	if _, err := contextcap.AgentTenant(ctx, h.DB, uuidToString(workspaceID), uuidToString(task.AgentID), orgID); err != nil {
		if errors.Is(err, contextcap.ErrNotFound) {
			return contextcap.Scope{}, taskContextNotTenant
		}
		slog.WarnContext(ctx, "context capabilities: agent tenants unavailable; skipping org, scene and personal layers",
			"task_id", uuidToString(task.ID), "agent_id", uuidToString(task.AgentID), "error", err)
		return contextcap.Scope{}, taskContextLookupFailed
	}
	scope.OrgID = orgID
	return scope, ""
}

// taskEffectiveContext is the effective context of one task
// (docs/context-capabilities.md §1.3, §3): its tenant-gated scope, the stored
// layers of that scope and their merge over a global layer
// (mergeTaskContext). Claim injection (skills, prompt components, custom MCP
// servers) and the connector resolver of the claim and of every relay call
// read it; the admin 生效预览 merges with the same mergeTaskContext.
type taskEffectiveContext struct {
	Scope contextcap.Scope
	// Skipped says why no org, scene or personal layer applies ("" when
	// Scope has layers).
	Skipped string
	// Layers are the org, scene and person layers of Scope, outermost first
	// (contextcap.LoadLayers).
	Layers []contextcap.ContextLayer
	// Effective is mergeTaskContext(global, Layers...).
	Effective contextcap.EffectiveContext
	// ReservedMCPServers are the custom MCP servers of the scope layers the
	// merge left out for their reserved name, as layer:name.
	ReservedMCPServers []string
	// UnmountedMCPServers are applied custom MCP servers of the scope layers
	// the claim left out because the runtime cannot mount MCP servers
	// (withoutScopeMCPServers), as layer:name.
	UnmountedMCPServers []string
}

// taskEffectiveContext builds the effective context of task over global, the
// agent's global layer as the caller resolved it (Layer must be
// contextcap.LayerGlobal). When the scope layers cannot be read it returns the
// global-only context, with a zero Scope, together with the error, so the
// caller can log it and continue with the global layer.
func (h *Handler) taskEffectiveContext(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue, global contextcap.ContextLayer) (taskEffectiveContext, error) {
	scope, skipped := h.resolveTaskContextScope(ctx, workspaceID, task)
	out := taskEffectiveContext{Scope: scope, Skipped: skipped}
	if scope.HasLayers() {
		layers, err := contextcap.LoadLayers(ctx, h.DB, uuidToString(workspaceID), uuidToString(task.AgentID), scope.Selection())
		if err != nil {
			out.Scope, out.Skipped = contextcap.Scope{}, taskContextLookupFailed
			out.Effective, _ = mergeTaskContext(global)
			return out, err
		}
		out.Layers = layers
	}
	out.Effective, out.ReservedMCPServers = mergeTaskContext(append([]contextcap.ContextLayer{global}, out.Layers...)...)
	return out, nil
}

// connectorServerNamePattern matches the server names the claim gives the
// task's internal connectors (connectorServerName).
var connectorServerNamePattern = regexp.MustCompile(`^c[0-9a-f]{16}$`)

// reservedMCPServerName reports a server name the claim's managed MCP
// servers use: the multica server and the connector servers. A custom server
// of that name would make the managed merge (injectRunnerMCP) fail the claim.
func reservedMCPServerName(name string) bool {
	return name == "multica" || connectorServerNamePattern.MatchString(name)
}

// mergeTaskContext is the one merge of an effective context, used by the
// claim, the connector resolver and the admin 生效预览:
// contextcap.MergeContext, minus the custom MCP servers of the org, scene and
// person layers whose name is reserved for a managed server
// (reservedMCPServerName), which a run could never mount. It also returns the
// servers it left out, as layer:name.
func mergeTaskContext(layers ...contextcap.ContextLayer) (contextcap.EffectiveContext, []string) {
	effective := contextcap.MergeContext(layers...)
	servers := make([]contextcap.EffectiveMCPServer, 0, len(effective.MCPServers))
	var reserved []string
	for _, server := range effective.MCPServers {
		if server.Layer != contextcap.LayerGlobal && reservedMCPServerName(server.Name) {
			reserved = append(reserved, server.Layer+":"+server.Name)
			continue
		}
		servers = append(servers, server)
	}
	effective.MCPServers = servers
	return effective, reserved
}

// claimTaskContext builds the effective context of a claimed task over the
// agent's mcp_config as the claim resolved it. A failure to read the layers
// is logged and the task runs with the global layer only.
func (h *Handler) claimTaskContext(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue, mcpConfig json.RawMessage) taskEffectiveContext {
	out, err := h.taskEffectiveContext(ctx, workspaceID, task, contextcap.ContextLayer{Layer: contextcap.LayerGlobal, MCPConfig: mcpConfig})
	if err != nil {
		slog.WarnContext(ctx, "context builder: org, scene and personal layers unavailable; the task runs with the global layer only",
			"task_id", uuidToString(task.ID), "agent_id", uuidToString(task.AgentID), "error", err)
	}
	return out
}

// contextSkillIDs returns the skills the scope layers switch on beyond the
// global layer (nil when none), for LoadTaskSkillBundles and
// LoadTaskExecutionSkills, which add them to the agent's own skills.
func (t taskEffectiveContext) contextSkillIDs() []pgtype.UUID {
	bindings := make([]contextcap.Binding, 0, len(t.Effective.Skills))
	for _, skill := range t.Effective.Skills {
		if skill.Layer != contextcap.LayerGlobal {
			bindings = append(bindings, contextcap.Binding{ResourceType: contextcap.ResourceSkill, ResourceID: skill.ID})
		}
	}
	return contextSkillIDs(bindings, nil)
}

// scopeMCPServerNames returns the names of the applied custom MCP servers of
// the scope layers: the servers mcpConfig layers onto the agent's.
func (t taskEffectiveContext) scopeMCPServerNames() []string {
	var names []string
	for _, server := range t.Effective.AppliedMCPServers() {
		if server.Layer != contextcap.LayerGlobal {
			names = append(names, server.Name)
		}
	}
	return names
}

// withoutScopeMCPServers returns t without the custom MCP servers of the
// scope layers, recorded in UnmountedMCPServers, for a runtime that cannot
// mount MCP servers: the task then runs without them instead of being
// cancelled for them. The agent's own servers stay.
func (t taskEffectiveContext) withoutScopeMCPServers() taskEffectiveContext {
	kept := make([]contextcap.EffectiveMCPServer, 0, len(t.Effective.MCPServers))
	var unmounted []string
	for _, server := range t.Effective.MCPServers {
		if server.Layer != contextcap.LayerGlobal {
			if server.OverriddenBy == "" {
				unmounted = append(unmounted, server.Layer+":"+server.Name)
			}
			continue
		}
		// No scope server is left to replace the agent's own.
		server.OverriddenBy = ""
		kept = append(kept, server)
	}
	t.Effective.MCPServers = kept
	t.UnmountedMCPServers = append(t.UnmountedMCPServers, unmounted...)
	return t
}

// mcpConfig layers the applied custom MCP servers of the scope layers onto
// base, the agent's mcp_config as the claim resolved it
// (contextcap.MergeMCPConfig: by server name, nearest layer wins, so a scope
// server replaces an agent server of the same name). Without a scope server
// base comes back unchanged, byte for byte. A malformed base is kept as it is
// (the scope servers are then not applied) and logged.
func (t taskEffectiveContext) mcpConfig(ctx context.Context, taskID pgtype.UUID, base json.RawMessage) json.RawMessage {
	merged, err := contextcap.MergeMCPConfig(base, t.Effective)
	if err != nil {
		slog.WarnContext(ctx, "context builder: agent mcp_config is malformed; custom MCP servers of the context layers are not applied",
			"task_id", uuidToString(taskID), "error", err)
		return base
	}
	return merged
}

// instructions appends the applied prompt components of the scope layers to
// base, the agent instructions as the claim composed them, as one block
// (EffectiveContext.PromptBlock, headed 「## 场域上下文」). base comes back
// unchanged when no component applies.
func (t taskEffectiveContext) instructions(base string) string {
	block := t.Effective.PromptBlock()
	switch {
	case block == "":
		return base
	case strings.TrimSpace(base) == "":
		return block
	default:
		return base + "\n\n" + block
	}
}

// logClaim writes the one context line of a claim: the task's tenant org and
// layers (or why none applies), the applied prompt components and custom MCP
// servers as layer:name, the ones a nearer layer overrides as
// layer:name>nearer, the custom MCP servers left out for a reserved name or
// a runtime without MCP, and how many skills and connectors the scope layers
// switch on (connectors are mounted only with a usable credential, see
// authorizedTaskConnectors). Names only: never prompt text, server
// configuration or credentials. Tasks without a DingTalk dispatch context
// and A2A tasks log nothing.
func (t taskEffectiveContext) logClaim(ctx context.Context, task db.AgentTaskQueue) {
	if t.Skipped == taskContextNoDispatch || t.Skipped == taskContextA2A {
		return
	}
	layers := make([]string, 0, len(t.Layers))
	for _, layer := range t.Layers {
		layers = append(layers, layer.Layer)
	}
	prompts, promptsOverridden := []string{}, []string{}
	for _, prompt := range t.Effective.Prompts {
		if prompt.OverriddenBy != "" {
			promptsOverridden = append(promptsOverridden, prompt.Layer+":"+prompt.Name+">"+prompt.OverriddenBy)
		} else {
			prompts = append(prompts, prompt.Layer+":"+prompt.Name)
		}
	}
	servers, serversOverridden := []string{}, []string{}
	for _, server := range t.Effective.MCPServers {
		switch {
		case server.OverriddenBy != "":
			serversOverridden = append(serversOverridden, server.Layer+":"+server.Name+">"+server.OverriddenBy)
		case server.Layer != contextcap.LayerGlobal:
			servers = append(servers, server.Layer+":"+server.Name)
		}
	}
	skills, connectors := 0, 0
	for _, skill := range t.Effective.Skills {
		if skill.Layer != contextcap.LayerGlobal {
			skills++
		}
	}
	for _, connector := range t.Effective.Connectors {
		if connector.Layer != contextcap.LayerGlobal {
			connectors++
		}
	}
	slog.InfoContext(ctx, "context builder: claim context",
		"task_id", uuidToString(task.ID), "agent_id", uuidToString(task.AgentID),
		"org_id", t.Scope.OrgID, "skipped", t.Skipped, "layers", strings.Join(layers, ","),
		"prompts", prompts, "prompts_overridden", promptsOverridden,
		"mcp_servers", servers, "mcp_servers_overridden", serversOverridden,
		"mcp_servers_reserved", t.ReservedMCPServers, "mcp_servers_unmounted", t.UnmountedMCPServers,
		"invalid_mcp_layers", t.Effective.InvalidMCPLayers,
		"skills", skills, "connectors", connectors)
}

// grantedConnectors returns every enabled connector the agent is globally
// granted, regardless of credential readiness.
func (h *Handler) grantedConnectors(ctx context.Context, workspaceID, agentID string) ([]internalConnector, error) {
	return h.queryInternalConnectors(ctx, `SELECT `+internalConnectorSelect+`
		FROM internal_connector c JOIN internal_connector_agent g ON g.connector_id=c.id AND g.workspace_id=c.workspace_id
		WHERE c.workspace_id=$1::uuid AND g.agent_id=$2::uuid AND c.enabled
		ORDER BY c.name, c.id`, workspaceID, agentID)
}

// queryInternalConnectors runs a query selecting internalConnectorSelect.
func (h *Handler) queryInternalConnectors(ctx context.Context, query string, args ...any) ([]internalConnector, error) {
	rows, err := h.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []internalConnector{}
	for rows.Next() {
		c, err := scanInternalConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// taskConnectorContext is the org/scene/person part of one task's connector
// resolution: connectors the task's scope layers switch on (with their
// binding layer), the scope's credentials, and the agent's enabled connector
// offers.
type taskConnectorContext struct {
	bindingLayer map[string]string
	bound        []internalConnector
	credentials  []contextcap.Credential
	offered      map[string]bool
}

// loadTaskConnectorContext reads the connector side of a task's effective
// context. A connector the global layer has keeps its global binding layer;
// any other takes the nearest layer that switches it on (MergeContext).
func (h *Handler) loadTaskConnectorContext(ctx context.Context, ws, agent string, task taskEffectiveContext) (taskConnectorContext, error) {
	out := taskConnectorContext{bindingLayer: map[string]string{}, offered: map[string]bool{}}
	var boundIDs []string
	for _, connector := range task.Effective.Connectors {
		if connector.Layer == contextcap.LayerGlobal {
			continue
		}
		out.bindingLayer[connector.ID] = connector.Layer
		boundIDs = append(boundIDs, connector.ID)
	}
	var err error
	if len(boundIDs) > 0 {
		if out.bound, err = h.queryInternalConnectors(ctx, `SELECT `+internalConnectorSelect+`
			FROM internal_connector c
			WHERE c.workspace_id=$1::uuid AND c.id = ANY($2::uuid[]) AND c.enabled
			ORDER BY c.name, c.id`, ws, boundIDs); err != nil {
			return out, err
		}
	}
	if out.credentials, err = contextcap.LayerCredentials(ctx, h.DB, ws, agent, task.Scope.Selection()); err != nil {
		return out, err
	}
	if task.Scope.HasOrg() || task.Scope.HasScene() {
		offers, err := contextcap.ListOffers(ctx, h.DB, ws, agent)
		if err != nil {
			return out, err
		}
		for _, id := range offers.ConnectorIDs {
			out.offered[id] = true
		}
	}
	return out, nil
}

// authorizedTaskConnectors resolves the connectors one claimed task may use:
// global grants (authorizedConnectors semantics) plus connectors the task's
// org, scene or trigger person layers switch on that are still offered and
// enabled in the workspace library, deduplicated by id (the task's effective
// context, taskEffectiveContext). Each returned connector carries its binding
// layer and a resolved credential chosen person > scene > org > workspace; a
// Bearer connector without a usable credential in any applicable layer is
// omitted. auth_mode='none' needs no credential. Claim-time injection and
// every relay call run this same resolver, so a toggle or revoke applies to
// the next tool call. When the scope layers cannot be read, the task keeps
// its global connectors with workspace credentials rather than losing them.
// workspaceID must be the task's workspace.
func (h *Handler) authorizedTaskConnectors(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue) ([]internalConnector, error) {
	ws, agent := uuidToString(workspaceID), uuidToString(task.AgentID)
	global, err := h.grantedConnectors(ctx, ws, agent)
	if err != nil {
		return nil, err
	}
	globalLayer := contextcap.ContextLayer{Layer: contextcap.LayerGlobal, ConnectorIDs: make([]string, 0, len(global))}
	globalIDs := make(map[string]bool, len(global))
	for _, c := range global {
		globalIDs[c.ID] = true
		globalLayer.ConnectorIDs = append(globalLayer.ConnectorIDs, c.ID)
	}
	effective, err := h.taskEffectiveContext(ctx, workspaceID, task, globalLayer)
	layers := taskConnectorContext{}
	if err == nil && effective.Scope.HasLayers() {
		layers, err = h.loadTaskConnectorContext(ctx, ws, agent, effective)
	}
	if err != nil {
		slog.WarnContext(ctx, "context capabilities: org, scene and personal connector layers unavailable; using global grants only",
			"task_id", uuidToString(task.ID), "agent_id", agent, "error", err)
		layers, effective.Scope = taskConnectorContext{}, contextcap.Scope{}
	}
	scope := effective.Scope

	candidates := append(append(make([]internalConnector, 0, len(global)+len(layers.bound)), global...), layers.bound...)
	out := make([]internalConnector, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		// An official app connector is not mounted until its tools are
		// known (an account was connected and discovery pinned them).
		if len(c.AllowedTools) == 0 {
			continue
		}
		c.bindingLayer = connectorBindingGlobal
		if !globalIDs[c.ID] {
			c.bindingLayer = layers.bindingLayer[c.ID]
		}
		if !h.resolveTaskConnectorCredential(ctx, &c, task, scope, layers.credentials, layers.offered[c.ID]) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// resolveTaskConnectorCredential fixes the credential the relay will send for
// c and reports whether any applicable layer provides one, first match wins:
// person, scene, org, workspace. A person credential applies to any connector
// the task may use; a scene or org credential only to a connector in the
// agent's enabled offer catalog (offered), because it serves every member's
// run in the group or the tenant and the admin opts a connector into that
// configuration by offering it. A scoped credential that fails to open is
// treated as absent (and logged without any secret material), and so is an
// expired OAuth credential without a refresh token. A usable OAuth credential
// keeps its OAuth part and row location, so the relay can refresh it (under a
// row lock) right before calling upstream.
func (h *Handler) resolveTaskConnectorCredential(ctx context.Context, c *internalConnector, task db.AgentTaskQueue, scope contextcap.Scope, credentials []contextcap.Credential, offered bool) bool {
	if c.AuthMode == "none" {
		c.setResolvedCredential("", connectorCredentialNone)
		return true
	}
	now := time.Now()
	orgKey := ""
	if scope.HasOrg() {
		orgKey = scope.OrgID
	}
	for _, layer := range []struct {
		scopeType string
		key       string
	}{{contextcap.ScopePerson, scope.PersonKey}, {contextcap.ScopeScene, scope.SceneKey}, {contextcap.ScopeOrg, orgKey}} {
		if layer.key == "" || (layer.scopeType != contextcap.ScopePerson && !offered) {
			continue
		}
		for _, credential := range credentials {
			if credential.ConnectorID != c.ID || credential.ScopeType != layer.scopeType || credential.ScopeKey != layer.key || credential.OrgID != scope.OrgID {
				continue
			}
			key := contextcap.CredentialBinding{
				WorkspaceID: c.WorkspaceID, AgentID: uuidToString(task.AgentID), ConnectorID: c.ID,
				ScopeType: layer.scopeType, OrgID: scope.OrgID, ScopeKey: layer.key,
			}
			secret, err := contextcap.OpenCredentialSecret(h.contextCredentialBox(), key, credential.Ciphertext)
			if err != nil {
				slog.WarnContext(ctx, "context capabilities: scoped connector credential unusable; trying the next layer",
					"connector_id", c.ID, "task_id", uuidToString(task.ID), "credential_layer", layer.scopeType, "error", err)
				continue
			}
			if !secret.Usable(now) {
				slog.InfoContext(ctx, "context capabilities: scoped OAuth credential expired without a refresh token; trying the next layer",
					"connector_id", c.ID, "task_id", uuidToString(task.ID), "credential_layer", layer.scopeType)
				continue
			}
			c.setResolvedSecret(secret, layer.scopeType, key)
			return true
		}
	}
	secret, err := h.connectorSecret(*c)
	if err != nil || !secret.Usable(now) {
		return false
	}
	c.setResolvedSecret(secret, connectorCredentialWorkspace, contextcap.CredentialBinding{})
	return true
}

// resolvableContextSkillIDs is the extra skill set ResolveTaskSkillBundles
// accepts beyond agent skills: the enabled offered skills among requestedIDs
// (the refs the daemon asks for). Every context skill of a task is an offered
// skill, and accepting any offered one means a scope toggle between claim and
// bundle resolution cannot turn a claimed ref into a 404. Tasks without an
// org, scene or personal scope never carry context skill refs and get nil, so
// the common resolve path loads no extra skills.
func (h *Handler) resolvableContextSkillIDs(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue, requestedIDs []string) []pgtype.UUID {
	if len(requestedIDs) == 0 {
		return nil
	}
	scope := h.taskContextScope(ctx, workspaceID, task)
	if !scope.HasLayers() {
		return nil
	}
	offers, err := contextcap.ListOffers(ctx, h.DB, uuidToString(workspaceID), uuidToString(task.AgentID))
	if err != nil {
		slog.WarnContext(ctx, "context capabilities: offer lookup failed during skill bundle resolution",
			"task_id", uuidToString(task.ID), "agent_id", uuidToString(task.AgentID), "error", err)
		return nil
	}
	requested := make(map[string]bool, len(requestedIDs))
	for _, id := range requestedIDs {
		requested[strings.ToLower(strings.TrimSpace(id))] = true
	}
	extra := make([]contextcap.Binding, 0, len(requestedIDs))
	for _, id := range offers.SkillIDs {
		if requested[id] {
			extra = append(extra, contextcap.Binding{ResourceType: contextcap.ResourceSkill, ResourceID: id})
		}
	}
	return contextSkillIDs(extra, nil)
}

// contextSkillIDs appends the skill ids of bindings to existing, skipping
// duplicates and malformed ids.
func contextSkillIDs(bindings []contextcap.Binding, existing []pgtype.UUID) []pgtype.UUID {
	seen := make(map[string]bool, len(existing)+len(bindings))
	out := append([]pgtype.UUID(nil), existing...)
	for _, id := range existing {
		seen[uuidToString(id)] = true
	}
	for _, binding := range bindings {
		if binding.ResourceType != contextcap.ResourceSkill || seen[binding.ResourceID] {
			continue
		}
		var id pgtype.UUID
		if err := id.Scan(binding.ResourceID); err != nil || !id.Valid {
			continue
		}
		seen[binding.ResourceID] = true
		out = append(out, id)
	}
	return out
}
