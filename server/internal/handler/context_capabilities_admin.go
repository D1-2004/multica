package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Admin context capability API: the agent's offer catalog and read-only
// summaries of its scene and person scopes. Workspace-scoped agent routes,
// human actor only, same permission as editing the agent's skills.

const (
	contextCapOffersBodyLimit = 64 << 10
	contextCapMaxOfferIDs     = 256
)

type contextCapLibraryConnectorDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	AuthMode string `json:"auth_mode"`
	// CatalogSlug is the official app ("github", "notion", ...) or "" for
	// an Aone FaaS connector; the UI uses it for the brand logo.
	CatalogSlug string `json:"catalog_slug"`
}

type contextCapScopeSummaryDTO struct {
	ScopeKey        string                 `json:"scope_key"`
	ScopeTitle      string                 `json:"scope_title"`
	Bindings        []contextCapBindingDTO `json:"bindings"`
	CredentialCount int                    `json:"credential_count"`
}

type contextCapAdminResponse struct {
	Enabled bool `json:"enabled"`
	Library struct {
		Connectors []contextCapLibraryConnectorDTO `json:"connectors"`
		Skills     []contextCapSkillDTO            `json:"skills"`
	} `json:"library"`
	Offers struct {
		ConnectorIDs []string `json:"connector_ids"`
		SkillIDs     []string `json:"skill_ids"`
	} `json:"offers"`
	Scenes       []contextCapScopeSummaryDTO `json:"scenes"`
	Persons      []contextCapScopeSummaryDTO `json:"persons"`
	ConfigureURL string                      `json:"configure_url"`
}

func newContextCapAdminResponse(enabled bool) contextCapAdminResponse {
	var resp contextCapAdminResponse
	resp.Enabled = enabled
	resp.Library.Connectors = []contextCapLibraryConnectorDTO{}
	resp.Library.Skills = []contextCapSkillDTO{}
	resp.Offers.ConnectorIDs = []string{}
	resp.Offers.SkillIDs = []string{}
	resp.Scenes = []contextCapScopeSummaryDTO{}
	resp.Persons = []contextCapScopeSummaryDTO{}
	return resp
}

// contextCapAdminCaller is the route agent of an admin call and whether the
// caller is a workspace owner/admin (not only the agent's owner).
type contextCapAdminCaller struct {
	agent          db.Agent
	workspaceAdmin bool
}

// contextCapAdminAgent loads the route agent and requires the agent-manage
// permission (workspace owner/admin or agent owner), as SetAgentSkills does.
// Connector offers additionally need a workspace owner/admin: the connector
// library and its per-agent grants are admin-only, and an offered connector
// can run on the workspace credential.
func (h *Handler) contextCapAdminAgent(w http.ResponseWriter, r *http.Request) (contextCapAdminCaller, bool) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return contextCapAdminCaller{}, false
	}
	if !h.canManageAgent(w, r, agent) {
		return contextCapAdminCaller{}, false
	}
	member, err := h.getWorkspaceMember(r.Context(), requestUserID(r), uuidToString(agent.WorkspaceID))
	w.Header().Set("Cache-Control", "no-store")
	return contextCapAdminCaller{agent: agent, workspaceAdmin: err == nil && roleAllowed(member.Role, "owner", "admin")}, true
}

// contextCapConfigureURL is the mobile configuration page for the agent, or
// "" when no browser-facing app origin is configured.
func (h *Handler) contextCapConfigureURL(query string) string {
	origin := strings.TrimRight(firstNonEmpty(h.currentConfig().AppURL, h.currentConfig().FrontendOrigin), "/")
	if origin == "" {
		return ""
	}
	return origin + "/dingtalk/configure?" + query
}

// buildContextCapAdmin assembles the admin view. For a workspace owner/admin
// the library lists every workspace connector regardless of the internal
// connector flag, so saving the catalog never silently drops connector
// offers. An agent owner who is not a workspace admin may keep or remove
// connector offers but not add new ones, so their library lists only the
// connectors already offered (the connector library itself is admin-only).
// Scope bindings are listed only for currently offered resources.
func (h *Handler) buildContextCapAdmin(ctx context.Context, caller contextCapAdminCaller) (contextCapAdminResponse, error) {
	resp := newContextCapAdminResponse(true)
	agent := caller.agent
	ws, agentID := uuidToString(agent.WorkspaceID), uuidToString(agent.ID)

	offers, err := contextcap.ListOffers(ctx, h.DB, ws, agentID)
	if err != nil {
		return resp, err
	}
	rows, err := h.DB.Query(ctx, `SELECT id::text, name, enabled, auth_mode, catalog_slug FROM internal_connector
		WHERE workspace_id = $1::uuid AND ($2::boolean OR id = ANY($3::uuid[])) ORDER BY name, id`,
		ws, caller.workspaceAdmin, offers.ConnectorIDs)
	if err != nil {
		return resp, err
	}
	for rows.Next() {
		var c contextCapLibraryConnectorDTO
		if err := rows.Scan(&c.ID, &c.Name, &c.Enabled, &c.AuthMode, &c.CatalogSlug); err != nil {
			rows.Close()
			return resp, err
		}
		resp.Library.Connectors = append(resp.Library.Connectors, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return resp, err
	}
	if resp.Library.Skills, err = h.contextCapSkills(ctx, `SELECT id::text, name, description FROM skill
		WHERE workspace_id = $1::uuid ORDER BY name, id`, ws); err != nil {
		return resp, err
	}

	resp.Offers.ConnectorIDs = offers.ConnectorIDs
	resp.Offers.SkillIDs = offers.SkillIDs

	summaries, err := contextcap.ListScopeSummaries(ctx, h.DB, ws, agentID)
	if err != nil {
		return resp, err
	}
	for _, summary := range summaries {
		view := contextCapScopeSummaryDTO{
			ScopeKey: summary.ScopeKey, ScopeTitle: summary.ScopeTitle,
			Bindings: contextCapBindingViews(summary.Bindings, offers), CredentialCount: summary.CredentialCount,
		}
		switch summary.ScopeType {
		case contextcap.ScopeScene:
			resp.Scenes = append(resp.Scenes, view)
		case contextcap.ScopePerson:
			resp.Persons = append(resp.Persons, view)
		}
	}
	resp.ConfigureURL = h.contextCapConfigureURL("agent=" + agentID)
	return resp, nil
}

// GetAgentContextCapabilities returns the agent's offer catalog, the
// workspace library to pick from, and scene/person summaries. While the
// context_capabilities flag is off it returns {"enabled": false} with empty
// collections.
func (h *Handler) GetAgentContextCapabilities(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.contextCapAdminAgent(w, r)
	if !ok {
		return
	}
	resp, err := h.buildContextCapAdmin(r.Context(), caller)
	if err != nil {
		slog.ErrorContext(r.Context(), "context capabilities: admin view failed", "agent_id", uuidToString(caller.agent.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load context capabilities")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// PutAgentContextCapabilityOffers replaces the agent's offer catalog in one
// transaction. Both id lists are required so a partial body can never wipe
// the other half of the catalog. Adding a connector offer needs a workspace
// owner/admin, and a skill another agent's source manages cannot be offered
// (the SetAgentSkills rule).
func (h *Handler) PutAgentContextCapabilityOffers(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.contextCapAdminAgent(w, r)
	if !ok {
		return
	}
	agent := caller.agent
	var input struct {
		ConnectorIDs *[]string `json:"connector_ids"`
		SkillIDs     *[]string `json:"skill_ids"`
	}
	if !decodeContextCapBody(w, r, contextCapOffersBodyLimit, &input) {
		return
	}
	if input.ConnectorIDs == nil || input.SkillIDs == nil {
		writeError(w, http.StatusBadRequest, "connector_ids and skill_ids are required")
		return
	}
	if len(*input.ConnectorIDs) > contextCapMaxOfferIDs || len(*input.SkillIDs) > contextCapMaxOfferIDs {
		writeError(w, http.StatusBadRequest, "too many offered items")
		return
	}
	ctx := r.Context()
	ws, agentID := uuidToString(agent.WorkspaceID), uuidToString(agent.ID)
	connectorIDs, okConnectors := canonicalContextCapIDs(*input.ConnectorIDs)
	skillIDs, okSkills := canonicalContextCapIDs(*input.SkillIDs)
	if !okConnectors || !okSkills {
		writeError(w, http.StatusBadRequest, "invalid offer id")
		return
	}
	if ok, err := h.contextCapSkillsOfferable(ctx, agentID, skillIDs); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify skill source ownership")
		return
	} else if !ok {
		writeError(w, http.StatusBadRequest, "source-managed skills cannot be offered by another agent")
		return
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save offers")
		return
	}
	defer tx.Rollback(ctx)
	if !caller.workspaceAdmin {
		// Checked under the offer lock so a concurrent admin removal cannot be
		// undone by a stale agent-owner draft.
		if err := contextcap.LockOffers(ctx, tx, agentID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save offers")
			return
		}
		current, err := contextcap.ListOffers(ctx, tx, ws, agentID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save offers")
			return
		}
		for _, id := range connectorIDs {
			if !current.Contains(contextcap.ResourceConnector, id) {
				writeError(w, http.StatusForbidden, "only workspace owners and admins can offer connectors")
				return
			}
		}
	}
	err = contextcap.ReplaceOffers(ctx, tx, ws, agentID, connectorIDs, skillIDs, requestUserID(r))
	switch {
	case errors.Is(err, contextcap.ErrUnknownResource):
		writeError(w, http.StatusBadRequest, "offers must be connectors or skills of this workspace")
		return
	case errors.Is(err, contextcap.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid offer id")
		return
	case errors.Is(err, contextcap.ErrNotFound):
		writeError(w, http.StatusNotFound, "agent not found")
		return
	case err != nil:
		slog.ErrorContext(ctx, "context capabilities: offer replace failed", "agent_id", agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save offers")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save offers")
		return
	}
	slog.InfoContext(ctx, "context capabilities: offers replaced", "agent_id", agentID, "workspace_id", ws,
		"connectors", len(*input.ConnectorIDs), "skills", len(*input.SkillIDs), "actor_id", requestUserID(r))
	resp, err := h.buildContextCapAdmin(ctx, caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load context capabilities")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// canonicalContextCapIDs parses and canonicalizes UUID strings. It reports
// false for any malformed id.
func canonicalContextCapIDs(raw []string) ([]string, bool) {
	out := make([]string, 0, len(raw))
	for _, id := range raw {
		parsed, err := util.ParseUUID(strings.TrimSpace(id))
		if err != nil {
			return nil, false
		}
		out = append(out, uuidToString(parsed))
	}
	return out, true
}

// contextCapSkillsOfferable reports whether none of skillIDs is a skill that
// another agent's Git source manages. A source-managed skill belongs to its
// agent only (validateAgentSkillIDsInWorkspace applies the same rule to
// agent_skill); offering it elsewhere would load it into other agents' runs
// and leave stale offers when the source removes it.
func (h *Handler) contextCapSkillsOfferable(ctx context.Context, agentID string, skillIDs []string) (bool, error) {
	if len(skillIDs) == 0 {
		return true, nil
	}
	var foreign bool
	err := h.DB.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM agent_source_skill ass
		WHERE ass.skill_id = ANY($1::uuid[])
		  AND NOT EXISTS (SELECT 1 FROM agent_source src WHERE src.id = ass.agent_source_id AND src.agent_id = $2::uuid))`,
		skillIDs, agentID).Scan(&foreign)
	return !foreign, err
}
