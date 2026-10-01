package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service/scenememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Admin scene helpers (docs/context-capabilities.md §6 "Scenes"): the scene
// shape (DingTalk group chats and 1:1 chats) and the caller resolution the
// tenant and Context Builder routes (agent_tenants.go) share.
// Workspace-scoped agent routes, human actor only, same permission as
// editing the agent's skills (workspace owner/admin or the agent owner).

const (
	agentScenesDefaultLimit = 50
	agentScenesMaxLimit     = 200
	agentScenesMaxOffset    = 1 << 20
)

type agentSceneDTO struct {
	SceneKey string `json:"scene_key"`
	// Kind is "group" or "dm".
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	OrgID        string `json:"org_id"`
	LastActiveAt string `json:"last_active_at"`
	// InboundSessionID is the newest Coordinator chat session of the
	// conversation, usable with /coordinator-conversations/{sessionId}/messages
	// ("" when there is none). InboundCount counts the sessions that
	// transcript shows (same endpoint namespace and source).
	InboundSessionID string `json:"inbound_session_id"`
	InboundCount     int    `json:"inbound_count"`
	// MemoryID is the scene_memory row id ("" when the scene has none).
	MemoryID  string `json:"memory_id"`
	HasPrompt bool   `json:"has_prompt"`
}

type agentSceneBindingDTO struct {
	ResourceType  string `json:"resource_type"`
	ResourceID    string `json:"resource_id"`
	Enabled       bool   `json:"enabled"`
	UpdatedByName string `json:"updated_by_name"`
	UpdatedAt     string `json:"updated_at"`
}

// agentSceneCredentialDTO is the credential of an offered connector in a
// Context Builder node's scope.
type agentSceneCredentialDTO struct {
	// Connected: a credential (an OAuth account or a pasted token) is
	// stored for the connector in the scope.
	Connected bool `json:"connected"`
	// Account is its hint ("@login", "OAuth" or "••••abcd"); "" when not
	// connected, and for a person's scope unless the caller is a workspace
	// admin or that person.
	Account string `json:"account"`
}

type agentSceneOfferedConnectorDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CatalogSlug string `json:"catalog_slug"`
	AuthMode    string `json:"auth_mode"`
	// AcceptsCredential, AcceptsPAT, OAuthAvailable and InstallURL follow
	// the configure page's offer rules (contextCapOfferedConnectorDTO).
	AcceptsCredential bool                    `json:"accepts_credential"`
	AcceptsPAT        bool                    `json:"accepts_pat"`
	OAuthAvailable    bool                    `json:"oauth_available"`
	InstallURL        string                  `json:"install_url,omitempty"`
	Credential        agentSceneCredentialDTO `json:"credential"`
}

// agentSceneCaller is the route agent of an admin scene call, its workspace
// and agent ids, its DingTalk identity org ("" without an identity) and
// whether the caller is a workspace owner/admin (not only the agent owner).
type agentSceneCaller struct {
	agent          db.Agent
	workspaceID    string
	agentID        string
	orgID          string
	workspaceAdmin bool
}

// agentSceneAdmin loads the route agent for an admin scene call: a human
// caller with the agent-manage permission (contextCapAdminAgent). Agents may
// not read or write scenes, as with scene memory.
func (h *Handler) agentSceneAdmin(w http.ResponseWriter, r *http.Request) (agentSceneCaller, bool) {
	caller, ok := h.contextCapAdminAgent(w, r)
	if !ok {
		return agentSceneCaller{}, false
	}
	agent := caller.agent
	out := agentSceneCaller{agent: agent, workspaceID: uuidToString(agent.WorkspaceID), agentID: uuidToString(agent.ID), workspaceAdmin: caller.workspaceAdmin}
	if actorType, _ := h.resolveActor(r, requestUserID(r), out.workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents may not manage scenes")
		return agentSceneCaller{}, false
	}
	identity, err := h.Queries.GetAgentDingTalkIdentity(r.Context(), db.GetAgentDingTalkIdentityParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
	switch {
	case err == nil:
		out.orgID = strings.TrimSpace(identity.OrgID)
	case errors.Is(err, pgx.ErrNoRows):
	default:
		slog.ErrorContext(r.Context(), "agent scenes: DingTalk identity lookup failed", "agent_id", out.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load agent scenes")
		return agentSceneCaller{}, false
	}
	return out, true
}

// contextCapAgent is the caller's agent in the shape the context capability
// resolver takes.
func (c agentSceneCaller) contextCapAgent() contextCapAgent {
	return contextCapAgent{Agent: c.agent, ID: c.agentID, WorkspaceID: c.workspaceID, OrgID: c.orgID, IdentityOrgID: c.orgID}
}

// agentSceneTitle picks the display title of a scene: the scene memory title
// (or its locating line), the Coordinator conversation title, the 1:1
// sender, then the stored configuration and binding snapshots.
func agentSceneTitle(s contextcap.SceneSummary) string {
	title := firstNonEmpty(scenememory.DisplayTitle(s.MemoryTitle, s.MemoryText), s.ConversationTitle)
	if title == "" && s.Kind == contextcap.SceneKindDM {
		title = strings.TrimSpace(s.SenderName)
	}
	return firstNonEmpty(title, s.ConfigTitle, s.BindingTitle)
}

func agentSceneView(s contextcap.SceneSummary) agentSceneDTO {
	return agentSceneDTO{
		SceneKey: s.SceneKey, Kind: s.Kind, Title: agentSceneTitle(s), OrgID: s.OrgID,
		LastActiveAt: contextCapTime(s.LastActiveAt), InboundSessionID: s.InboundSessionID, InboundCount: s.InboundCount,
		MemoryID: s.MemoryID, HasPrompt: s.HasPrompt,
	}
}

// agentSceneUserNames maps user ids to display names.
func (h *Handler) agentSceneUserNames(ctx context.Context, ids []string) (map[string]string, error) {
	names := map[string]string{}
	seen := map[string]bool{}
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return names, nil
	}
	rows, err := h.DB.Query(ctx, `SELECT id::text, name FROM "user" WHERE id = ANY($1::uuid[])`, unique)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

func agentSceneBindingView(b contextcap.Binding, names map[string]string) agentSceneBindingDTO {
	return agentSceneBindingDTO{
		ResourceType: b.ResourceType, ResourceID: b.ResourceID, Enabled: b.Enabled,
		UpdatedByName: names[b.UpdatedBy], UpdatedAt: contextCapTime(b.UpdatedAt),
	}
}

// agentSceneRedactsMCPConfig reports whether the workspace always redacts
// secrets, which withholds custom MCP servers like the agent's mcp_config.
func (h *Handler) agentSceneRedactsMCPConfig(ctx context.Context, workspaceID string) (bool, error) {
	ws, err := h.Queries.GetWorkspace(ctx, parseUUID(workspaceID))
	if err != nil {
		return false, err
	}
	return workspaceAlwaysRedactSecrets(ws.Settings), nil
}

// agentSceneMCPConfigBodyLimit bounds PUT .../mcp-config: the configuration
// plus its envelope.
const agentSceneMCPConfigBodyLimit = contextcap.MaxScopeMCPConfigBytes + 1<<10
