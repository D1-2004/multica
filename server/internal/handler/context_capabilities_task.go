package handler

import (
	"context"
	"errors"
	"log/slog"
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
	connectorBindingGlobal = "global"
	connectorBindingScene  = contextcap.ScopeScene
	connectorBindingPerson = contextcap.ScopePerson
)

// Credential layers recorded on a resolved connector and in the connector call
// audit: which credential the relay sends upstream.
const (
	connectorCredentialNone      = "none"
	connectorCredentialWorkspace = "workspace"
	connectorCredentialScene     = contextcap.ScopeScene
	connectorCredentialPerson    = contextcap.ScopePerson
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

// taskContextScope derives the scene and trigger person of a task from its
// server-written dispatch context and fills OrgID from the agent's DingTalk
// identity ("" when the agent has none). It returns the zero Scope when the
// context_capabilities flag is off, for A2A-origin tasks, for manual reruns
// (a member re-triggered a copy of someone else's dispatch context), and when
// the identity cannot be read, so no scene or personal layer applies.
// workspaceID must be the task's workspace (agent_task_queue has none).
func (h *Handler) taskContextScope(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue) contextcap.Scope {
	if service.IsA2ATaskOrigin(task.Context) || task.RerunOfTaskID.Valid {
		return contextcap.Scope{}
	}
	scope := contextcap.ScopeFromTaskContext(task.Context)
	if !scope.HasScene() && !scope.HasPerson() {
		return scope
	}
	if h.Queries == nil {
		return contextcap.Scope{}
	}
	identity, err := h.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: workspaceID, AgentID: task.AgentID})
	switch {
	case err == nil:
		scope.OrgID = strings.TrimSpace(identity.OrgID)
	case errors.Is(err, pgx.ErrNoRows):
		scope.OrgID = ""
	default:
		slog.WarnContext(ctx, "context capabilities: agent DingTalk identity unavailable; skipping scene and personal layers",
			"task_id", uuidToString(task.ID), "agent_id", uuidToString(task.AgentID), "error", err)
		return contextcap.Scope{}
	}
	if dispatchedUnderOtherBinding(scope, identity, err == nil, task) {
		slog.WarnContext(ctx, "context capabilities: task was dispatched under an earlier DingTalk binding; skipping scene and personal layers",
			"task_id", uuidToString(task.ID), "agent_id", uuidToString(task.AgentID))
		return contextcap.Scope{}
	}
	return scope
}

// dispatchedUnderOtherBinding reports whether a task's dispatch may belong to
// a different DingTalk binding than the agent's current one. A staffId or
// openConversationId is only meaningful inside the org it was dispatched in,
// so reading an old task under a rebound agent's new org could pick up
// another person's credentials. The dispatch's recorded agent org
// (external_identity.dws.orgId) must equal the current org; without it, the
// agent must not have been (re)bound after the task was created. Retries and
// Coordinator item tasks carry the original dispatch context, so the recorded
// org is the reliable signal and the timestamp only a fallback. An agent
// without a DingTalk identity keeps the org "" scope on both sides (bindings
// and tasks), so there is no binding to drift from.
func dispatchedUnderOtherBinding(scope contextcap.Scope, identity db.AgentDingtalkIdentity, bound bool, task db.AgentTaskQueue) bool {
	if !bound {
		return false
	}
	if scope.DispatchOrgID != "" {
		return scope.DispatchOrgID != scope.OrgID
	}
	return identity.BoundAt.Valid && task.CreatedAt.Valid && identity.BoundAt.Time.After(task.CreatedAt.Time)
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

// taskConnectorContext is the scene/person part of one task's connector
// resolution: connectors bound to the task's scope (with their binding
// layer), the scope's credentials, and the agent's enabled connector offers.
type taskConnectorContext struct {
	bindingLayer map[string]string
	bound        []internalConnector
	credentials  []contextcap.Credential
	offered      map[string]bool
}

// loadTaskConnectorContext reads the scene/person connector layers of scope.
// Connectors already in global keep their global binding layer.
func (h *Handler) loadTaskConnectorContext(ctx context.Context, ws, agent string, scope contextcap.Scope, global map[string]bool) (taskConnectorContext, error) {
	out := taskConnectorContext{bindingLayer: map[string]string{}, offered: map[string]bool{}}
	bindings, err := contextcap.TaskBindings(ctx, h.DB, ws, agent, scope)
	if err != nil {
		return out, err
	}
	var boundIDs []string
	for _, binding := range bindings {
		if binding.ResourceType != contextcap.ResourceConnector || global[binding.ResourceID] {
			continue
		}
		layer, seen := out.bindingLayer[binding.ResourceID]
		switch {
		case !seen:
			out.bindingLayer[binding.ResourceID] = binding.ScopeType
			boundIDs = append(boundIDs, binding.ResourceID)
		case layer == connectorBindingScene && binding.ScopeType == contextcap.ScopePerson:
			out.bindingLayer[binding.ResourceID] = connectorBindingPerson
		}
	}
	if len(boundIDs) > 0 {
		if out.bound, err = h.queryInternalConnectors(ctx, `SELECT `+internalConnectorSelect+`
			FROM internal_connector c
			WHERE c.workspace_id=$1::uuid AND c.id = ANY($2::uuid[]) AND c.enabled
			ORDER BY c.name, c.id`, ws, boundIDs); err != nil {
			return out, err
		}
	}
	if out.credentials, err = contextcap.TaskCredentials(ctx, h.DB, ws, agent, scope); err != nil {
		return out, err
	}
	if scope.HasScene() {
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
// global grants (authorizedConnectors semantics) plus connectors bound to the
// task's scene or trigger person that are still offered and enabled in the
// workspace library, deduplicated by id. Each returned connector carries its
// binding layer and a resolved credential chosen person > scene > workspace;
// a Bearer connector without a usable credential in any applicable layer is
// omitted. auth_mode='none' needs no credential. Claim-time injection and
// every relay call run this same resolver, so a toggle or revoke applies to
// the next tool call. When the scene/person layers cannot be read, the task
// keeps its global connectors with workspace credentials rather than losing
// them. workspaceID must be the task's workspace.
func (h *Handler) authorizedTaskConnectors(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue) ([]internalConnector, error) {
	ws, agent := uuidToString(workspaceID), uuidToString(task.AgentID)
	global, err := h.grantedConnectors(ctx, ws, agent)
	if err != nil {
		return nil, err
	}
	scope := h.taskContextScope(ctx, workspaceID, task)

	globalIDs := make(map[string]bool, len(global))
	for _, c := range global {
		globalIDs[c.ID] = true
	}
	layers := taskConnectorContext{}
	if scope.HasScene() || scope.HasPerson() {
		if layers, err = h.loadTaskConnectorContext(ctx, ws, agent, scope, globalIDs); err != nil {
			slog.WarnContext(ctx, "context capabilities: scene and personal connector layers unavailable; using global grants only",
				"task_id", uuidToString(task.ID), "agent_id", agent, "error", err)
			layers, scope = taskConnectorContext{}, contextcap.Scope{}
		}
	}

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
		if c.CatalogSlug != "" && len(c.AllowedTools) == 0 {
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
// c and reports whether any applicable layer provides one. A person credential
// applies to any connector the task may use; a scene credential only to a
// connector in the agent's enabled offer catalog (offered), because it serves
// every member's run in the group and the admin opts a connector into group
// configuration by offering it. A scene or person credential that fails to
// open is treated as absent (and logged without any secret material), and
// so is an expired OAuth credential without a refresh token. A usable OAuth
// credential keeps its OAuth part and row location, so the relay can refresh
// it (under a row lock) right before calling upstream.
func (h *Handler) resolveTaskConnectorCredential(ctx context.Context, c *internalConnector, task db.AgentTaskQueue, scope contextcap.Scope, credentials []contextcap.Credential, offered bool) bool {
	if c.AuthMode == "none" {
		c.setResolvedCredential("", connectorCredentialNone)
		return true
	}
	now := time.Now()
	for _, layer := range []struct {
		scopeType string
		key       string
	}{{contextcap.ScopePerson, scope.PersonKey}, {contextcap.ScopeScene, scope.SceneKey}} {
		if layer.key == "" || (layer.scopeType == contextcap.ScopeScene && !offered) {
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

// taskContextSkillIDs returns the skill ids bound to the task's scene or
// trigger person that are still in the agent's offer catalog. It returns nil
// when the flag is off, for A2A tasks, tasks without dispatch context, and on
// lookup errors (the task then runs with its global skills only).
func (h *Handler) taskContextSkillIDs(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue) []pgtype.UUID {
	scope := h.taskContextScope(ctx, workspaceID, task)
	if !scope.HasScene() && !scope.HasPerson() {
		return nil
	}
	bindings, err := contextcap.TaskBindings(ctx, h.DB, uuidToString(workspaceID), uuidToString(task.AgentID), scope)
	if err != nil {
		slog.WarnContext(ctx, "context capabilities: skill binding lookup failed; using global skills only",
			"task_id", uuidToString(task.ID), "agent_id", uuidToString(task.AgentID), "error", err)
		return nil
	}
	return contextSkillIDs(bindings, nil)
}

// resolvableContextSkillIDs is the extra skill set ResolveTaskSkillBundles
// accepts beyond agent skills: the enabled offered skills among requestedIDs
// (the refs the daemon asks for). Every context skill of a task is an offered
// skill, and accepting any offered one means a scene toggle between claim and
// bundle resolution cannot turn a claimed ref into a 404. Tasks without a
// scene or personal scope never carry context skill refs and get nil, so the
// common resolve path loads no extra skills.
func (h *Handler) resolvableContextSkillIDs(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue, requestedIDs []string) []pgtype.UUID {
	if len(requestedIDs) == 0 {
		return nil
	}
	scope := h.taskContextScope(ctx, workspaceID, task)
	if !scope.HasScene() && !scope.HasPerson() {
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
