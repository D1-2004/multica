package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Mobile context capability API (docs/context-capabilities.md §5-§6). Every
// route is wrapped with RequireDingTalkHumanActor in the router and answers
// 404 while the context_capabilities flag is off. Authority never comes from
// workspace membership: the caller needs a live context_config_grant for the
// exact (agent, org, scope) it reads or writes.

const contextCapBodyLimit = 8 << 10

// contextCapAgent is an agent resolved for a context capability call. OrgID
// is the agent's current DingTalk org (agent_dingtalk_identity.org_id, "" when
// it has none); grants and bindings only apply under that org.
type contextCapAgent struct {
	Agent       db.Agent
	ID          string
	WorkspaceID string
	OrgID       string
}

type contextCapAgentDTO struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	AvatarURL   *string `json:"avatar_url"`
	WorkspaceID string  `json:"workspace_id"`
}

type contextCapBindingDTO struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Enabled      bool   `json:"enabled"`
	// ShareInGroups is present only on personal connector bindings: the
	// 「在群聊中由我触发时也可用」 opt-in (stored only this round).
	ShareInGroups *bool `json:"share_in_groups,omitempty"`
}

type contextCapCredentialDTO struct {
	ConnectorID string `json:"connector_id"`
	Hint        string `json:"hint"`
	UpdatedAt   string `json:"updated_at"`
	// Kind is "oauth" for an account connected through OAuth and "bearer"
	// for a pasted token (including a GitHub Personal Access Token).
	Kind string `json:"kind"`
}

type contextCapGrantDTO struct {
	ScopeType  string `json:"scope_type"`
	ScopeKey   string `json:"scope_key"`
	ScopeTitle string `json:"scope_title"`
	Source     string `json:"source"`
	ExpiresAt  string `json:"expires_at"`
}

type contextCapSceneDTO struct {
	ScopeKey   string `json:"scope_key"`
	ScopeTitle string `json:"scope_title"`
	Source     string `json:"source"`
	ExpiresAt  string `json:"expires_at"`
	// Kind is "group" for a group chat and "dm" for a 1:1 chat.
	Kind string `json:"kind"`
}

type contextCapPersonDTO struct {
	ScopeKey    string                    `json:"scope_key"`
	ScopeTitle  string                    `json:"scope_title"`
	Source      string                    `json:"source"`
	ExpiresAt   string                    `json:"expires_at"`
	Bindings    []contextCapBindingDTO    `json:"bindings"`
	Credentials []contextCapCredentialDTO `json:"credentials"`
}

type contextCapSkillDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type contextCapConnectorRefDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// CatalogSlug names the official app ("" for custom connectors).
	CatalogSlug string `json:"catalog_slug"`
}

type contextCapOfferedConnectorDTO struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Tools []string `json:"tools"`
	// AcceptsCredential reports that a pasted token is accepted (Bearer
	// connectors, and official apps that allow a Personal Access Token).
	AcceptsCredential bool `json:"accepts_credential"`
	// CredentialRequired: the connector uses credentials (Bearer or OAuth)
	// and has no workspace credential, so a group or person must connect or
	// paste one before it is mounted.
	CredentialRequired bool `json:"credential_required"`
	// CatalogSlug names the official app ("" for custom connectors).
	CatalogSlug string `json:"catalog_slug"`
	// AuthMode is "none", "bearer" or "oauth".
	AuthMode string `json:"auth_mode"`
	// AcceptsPAT: an OAuth official app that also accepts a Personal Access
	// Token (GitHub).
	AcceptsPAT bool `json:"accepts_pat"`
	// OAuthAvailable: an official app whose OAuth connect this deployment can
	// run (GitHub needs GITHUB_APP_CLIENT_ID and GITHUB_APP_CLIENT_SECRET);
	// the page hides "connect" when false and offers the PAT form instead.
	OAuthAvailable bool `json:"oauth_available"`
	// InstallURL is where people grant the app access to their resources
	// (GitHub App installation); omitted when there is none.
	InstallURL string `json:"install_url,omitempty"`
}

type contextCapAgentDetailResponse struct {
	Agent  contextCapAgentDTO `json:"agent"`
	Global struct {
		Connectors []contextCapConnectorRefDTO `json:"connectors"`
		Skills     []contextCapSkillDTO        `json:"skills"`
	} `json:"global"`
	Offers struct {
		Connectors []contextCapOfferedConnectorDTO `json:"connectors"`
		Skills     []contextCapSkillDTO            `json:"skills"`
	} `json:"offers"`
	Person         *contextCapPersonDTO `json:"person"`
	Scenes         []contextCapSceneDTO `json:"scenes"`
	JSAPIAvailable bool                 `json:"jsapi_available"`
}

func contextCapTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func contextCapAgentView(a contextCapAgent) contextCapAgentDTO {
	return contextCapAgentDTO{ID: a.ID, Name: a.Agent.Name, AvatarURL: textToPtr(a.Agent.AvatarUrl), WorkspaceID: a.WorkspaceID}
}

func contextCapGrantView(g contextcap.Grant) contextCapGrantDTO {
	return contextCapGrantDTO{ScopeType: g.ScopeType, ScopeKey: g.ScopeKey, ScopeTitle: g.ScopeTitle, Source: g.Source, ExpiresAt: contextCapTime(g.ExpiresAt)}
}

func contextCapSceneView(g contextcap.Grant, kind string) contextCapSceneDTO {
	if kind != contextcap.SceneKindDM {
		kind = contextcap.SceneKindGroup
	}
	return contextCapSceneDTO{ScopeKey: g.ScopeKey, ScopeTitle: g.ScopeTitle, Source: g.Source, ExpiresAt: contextCapTime(g.ExpiresAt), Kind: kind}
}

func contextCapBindingView(b contextcap.Binding) contextCapBindingDTO {
	view := contextCapBindingDTO{ResourceType: b.ResourceType, ResourceID: b.ResourceID, Enabled: b.Enabled}
	if b.ScopeType == contextcap.ScopePerson && b.ResourceType == contextcap.ResourceConnector {
		share := b.ShareInGroups
		view.ShareInGroups = &share
	}
	return view
}

// contextCapSceneKinds returns the kind (group or dm) of each scene key of
// agent a. Keys the agent's scene records do not know default to group.
func (h *Handler) contextCapSceneKinds(ctx context.Context, a contextCapAgent, keys []string) (map[string]string, error) {
	return contextcap.SceneKinds(ctx, h.DB, a.WorkspaceID, a.ID, a.OrgID, keys)
}

// contextCapBindingViews keeps only bindings whose resource is in offers, so
// every "bindings" list describes what can currently take effect. Rows of a
// removed offer stay stored and reappear when the resource is offered again.
func contextCapBindingViews(bindings []contextcap.Binding, offers contextcap.Offers) []contextCapBindingDTO {
	out := make([]contextCapBindingDTO, 0, len(bindings))
	for _, binding := range bindings {
		if offers.Contains(binding.ResourceType, binding.ResourceID) {
			out = append(out, contextCapBindingView(binding))
		}
	}
	return out
}

func contextCapCredentialView(c contextcap.Credential) contextCapCredentialDTO {
	return contextCapCredentialDTO{ConnectorID: c.ConnectorID, Hint: c.Hint, UpdatedAt: contextCapTime(c.UpdatedAt), Kind: contextcap.CredentialKindFromHint(c.Hint)}
}

func contextCapCredentialViews(credentials []contextcap.Credential) []contextCapCredentialDTO {
	out := make([]contextCapCredentialDTO, 0, len(credentials))
	for _, credential := range credentials {
		out = append(out, contextCapCredentialView(credential))
	}
	return out
}

// contextCapMobileUser gates a mobile endpoint: 404 while the feature flag is
// off, 401 without a valid user. It returns the canonical user id and marks
// the response uncacheable.
func (h *Handler) contextCapMobileUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	raw, ok := requireUserID(w, r)
	if !ok {
		return "", false
	}
	userID, err := util.ParseUUID(raw)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "user not authenticated")
		return "", false
	}
	return uuidToString(userID), true
}

// loadContextCapAgent loads an active user agent and its DingTalk org. It
// returns contextcap.ErrNotFound for a malformed id and for a missing,
// archived or system agent.
func (h *Handler) loadContextCapAgent(ctx context.Context, rawID string) (contextCapAgent, error) {
	agentID, err := util.ParseUUID(strings.TrimSpace(rawID))
	if err != nil {
		return contextCapAgent{}, contextcap.ErrNotFound
	}
	agent, err := h.Queries.GetAgent(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return contextCapAgent{}, contextcap.ErrNotFound
	}
	if err != nil {
		return contextCapAgent{}, err
	}
	if agent.ArchivedAt.Valid || agent.Kind != "user" {
		return contextCapAgent{}, contextcap.ErrNotFound
	}
	out := contextCapAgent{Agent: agent, ID: uuidToString(agent.ID), WorkspaceID: uuidToString(agent.WorkspaceID)}
	identity, err := h.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
	switch {
	case err == nil:
		out.OrgID = strings.TrimSpace(identity.OrgID)
	case errors.Is(err, pgx.ErrNoRows):
		out.OrgID = ""
	default:
		return contextCapAgent{}, err
	}
	return out, nil
}

// contextCapAgentOr404 loads the agent named by the {agentId} route param.
func (h *Handler) contextCapAgentOr404(w http.ResponseWriter, r *http.Request) (contextCapAgent, bool) {
	agent, err := h.loadContextCapAgent(r.Context(), chi.URLParam(r, "agentId"))
	if errors.Is(err, contextcap.ErrNotFound) {
		writeError(w, http.StatusNotFound, "agent not found")
		return contextCapAgent{}, false
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "context capabilities: agent lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "agent lookup failed")
		return contextCapAgent{}, false
	}
	return agent, true
}

// contextCapRequireGrant requires the caller's live grant for exactly
// (agent, agent org, scopeType, scopeKey): 400 for a malformed scope, 403
// without a grant.
func (h *Handler) contextCapRequireGrant(w http.ResponseWriter, r *http.Request, a contextCapAgent, userID, scopeType, scopeKey string) (contextcap.Grant, bool) {
	if !contextcap.ValidScopeKey(scopeType, scopeKey) {
		writeError(w, http.StatusBadRequest, "invalid scope")
		return contextcap.Grant{}, false
	}
	grant, err := contextcap.GetLiveGrant(r.Context(), h.DB, userID, a.ID, scopeType, a.OrgID, scopeKey)
	if errors.Is(err, contextcap.ErrNotFound) || (err == nil && grant.WorkspaceID != a.WorkspaceID) {
		writeError(w, http.StatusForbidden, "you are not allowed to configure this scope")
		return contextcap.Grant{}, false
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "context capabilities: grant lookup failed", "agent_id", a.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "grant lookup failed")
		return contextcap.Grant{}, false
	}
	return grant, true
}

// contextCapLiveGrants returns the caller's live grants for agent a under its
// current org, newest first.
func (h *Handler) contextCapLiveGrants(ctx context.Context, a contextCapAgent, userID string) ([]contextcap.Grant, error) {
	grants, err := contextcap.ListLiveGrantsForUser(ctx, h.DB, userID, a.ID)
	if err != nil {
		return nil, err
	}
	out := make([]contextcap.Grant, 0, len(grants))
	for _, grant := range grants {
		if grant.WorkspaceID == a.WorkspaceID && grant.OrgID == a.OrgID {
			out = append(out, grant)
		}
	}
	return out, nil
}

// contextCapVisibleOffers returns the agent's enabled offer catalog as the
// mobile page may use it. Connector offers are hidden while internal MCP
// connectors are off, because they could never be mounted.
func (h *Handler) contextCapVisibleOffers(ctx context.Context, a contextCapAgent) (contextcap.Offers, error) {
	offers, err := contextcap.ListOffers(ctx, h.DB, a.WorkspaceID, a.ID)
	if err != nil {
		return offers, err
	}
	return offers, nil
}

// decodeContextCapBody decodes one bounded JSON object with no unknown
// fields and no trailing data.
func decodeContextCapBody(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// contextCapPathParam returns a route param decoded once. chi matches on
// r.URL.RawPath whenever the request path carried escapes (for example the
// "%3D" padding of an openConversationId), which leaves params escaped.
func contextCapPathParam(r *http.Request, name string) (string, bool) {
	value := chi.URLParam(r, name)
	if r.URL.RawPath == "" {
		return value, true
	}
	decoded, err := url.PathUnescape(value)
	return decoded, err == nil
}

// RedeemContextConfigLink redeems an agent-issued configuration link into a
// grant for the caller. Scene links stay reusable until they expire; person
// links are consumed atomically by the first redemption. The link and the
// grant commit together.
func (h *Handler) RedeemContextConfigLink(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if !decodeContextCapBody(w, r, contextCapBodyLimit, &input) {
		return
	}
	token := strings.TrimSpace(input.Token)
	if !contextcap.ValidLinkTokenFormat(token) {
		writeError(w, http.StatusGone, "this configuration link is invalid or has expired")
		return
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "link store unavailable")
		return
	}
	defer tx.Rollback(ctx)
	link, err := contextcap.RedeemLink(ctx, tx, contextcap.HashLinkToken(token), userID)
	if errors.Is(err, contextcap.ErrNotFound) {
		writeError(w, http.StatusGone, "this configuration link is invalid or has expired")
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "context capabilities: link redeem failed", "error", err)
		writeError(w, http.StatusInternalServerError, "link redeem failed")
		return
	}
	agent, err := h.loadContextCapAgent(ctx, link.AgentID)
	if errors.Is(err, contextcap.ErrNotFound) || (err == nil && agent.WorkspaceID != link.WorkspaceID) {
		// Rolling back keeps a person link unconsumed; it is useless anyway.
		writeError(w, http.StatusGone, "this configuration link is invalid or has expired")
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "context capabilities: agent lookup failed during redeem", "error", err)
		writeError(w, http.StatusInternalServerError, "link redeem failed")
		return
	}
	if link.ScopeType == contextcap.ScopePerson {
		held, err := contextcap.PersonHeldByOther(ctx, tx, userID, link.AgentID, link.OrgID, link.ScopeKey)
		if err != nil {
			slog.ErrorContext(ctx, "context capabilities: person holder check failed during redeem", "agent_id", link.AgentID, "error", err)
			writeError(w, http.StatusInternalServerError, "link redeem failed")
			return
		}
		if held {
			// Rolling back leaves the link unconsumed; it still cannot take over.
			slog.WarnContext(ctx, "context capabilities: personal link redeemed by a different account than the scope holder",
				"agent_id", link.AgentID, "user_id", userID)
			writeError(w, http.StatusConflict, "this personal configuration already belongs to another DingTalk account")
			return
		}
	}
	grant, err := contextcap.UpsertGrant(ctx, tx, contextcap.Grant{
		UserID: userID, WorkspaceID: link.WorkspaceID, AgentID: link.AgentID, ScopeType: link.ScopeType,
		OrgID: link.OrgID, ScopeKey: link.ScopeKey, ScopeTitle: link.ScopeTitle, Source: contextcap.GrantSourceAgentLink,
	}, contextcap.GrantTTL(link.ScopeType))
	if err != nil {
		slog.ErrorContext(ctx, "context capabilities: grant upsert failed during redeem", "agent_id", link.AgentID, "error", err)
		writeError(w, http.StatusInternalServerError, "link redeem failed")
		return
	}
	if link.ScopeType == contextcap.ScopePerson && link.ExtraSceneKey != "" {
		// A personal link minted in a 1:1 chat also grants that 1:1
		// conversation as a scene (a DM is a scene), for as long as the
		// person grant: nobody but the person takes part in it. The DM is
		// registered so it is listed before anything is configured for it.
		if _, err := contextcap.UpsertGrant(ctx, tx, contextcap.Grant{
			UserID: userID, WorkspaceID: link.WorkspaceID, AgentID: link.AgentID, ScopeType: contextcap.ScopeScene,
			OrgID: link.OrgID, ScopeKey: link.ExtraSceneKey, ScopeTitle: link.ScopeTitle, Source: contextcap.GrantSourceAgentLink,
		}, contextcap.GrantTTLPerson); err != nil {
			slog.ErrorContext(ctx, "context capabilities: DM scene grant failed during redeem", "agent_id", link.AgentID, "error", err)
			writeError(w, http.StatusInternalServerError, "link redeem failed")
			return
		}
		if err := contextcap.RegisterDirectScene(ctx, tx, link.WorkspaceID, link.AgentID, link.OrgID, link.ExtraSceneKey, link.ScopeTitle); err != nil {
			slog.ErrorContext(ctx, "context capabilities: DM scene registration failed during redeem", "agent_id", link.AgentID, "error", err)
			writeError(w, http.StatusInternalServerError, "link redeem failed")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "link redeem failed")
		return
	}
	slog.InfoContext(ctx, "context capabilities: configuration link redeemed",
		"agent_id", link.AgentID, "workspace_id", link.WorkspaceID, "scope_type", link.ScopeType, "user_id", userID)
	writeJSON(w, http.StatusOK, map[string]string{
		"agent_id":     grant.AgentID,
		"workspace_id": grant.WorkspaceID,
		"scope_type":   grant.ScopeType,
		"scope_key":    grant.ScopeKey,
		"scope_title":  grant.ScopeTitle,
	})
}

// ListContextConfigAgents lists the agents the caller holds a live grant for,
// with those grants. Grants of archived agents or of a previous DingTalk org
// of the agent are omitted.
func (h *Handler) ListContextConfigAgents(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	grants, err := contextcap.ListLiveGrantsForUser(ctx, h.DB, userID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "grant lookup failed")
		return
	}
	type agentEntry struct {
		contextCapAgentDTO
		Scopes []contextCapGrantDTO `json:"scopes"`
	}
	agents := []*agentEntry{}
	byID := map[string]*agentEntry{}
	loaded := map[string]*contextCapAgent{}
	for _, grant := range grants {
		agent, seen := loaded[grant.AgentID]
		if !seen {
			a, err := h.loadContextCapAgent(ctx, grant.AgentID)
			if err != nil && !errors.Is(err, contextcap.ErrNotFound) {
				writeError(w, http.StatusInternalServerError, "agent lookup failed")
				return
			}
			if err == nil {
				agent = &a
			}
			loaded[grant.AgentID] = agent
		}
		if agent == nil || grant.WorkspaceID != agent.WorkspaceID || grant.OrgID != agent.OrgID {
			continue
		}
		entry, ok := byID[agent.ID]
		if !ok {
			entry = &agentEntry{contextCapAgentDTO: contextCapAgentView(*agent), Scopes: []contextCapGrantDTO{}}
			byID[agent.ID] = entry
			agents = append(agents, entry)
		}
		entry.Scopes = append(entry.Scopes, contextCapGrantView(grant))
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

// GetContextConfigAgent returns what the caller may see and configure for one
// agent: global items (read-only), the offer catalog, the caller's person
// scope and granted scenes. It needs at least one live grant for the agent.
// Upstream URLs, credential references and workspace credential material are
// never returned.
func (h *Handler) GetContextConfigAgent(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	grants, err := h.contextCapLiveGrants(ctx, a, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "grant lookup failed")
		return
	}
	if len(grants) == 0 {
		writeError(w, http.StatusForbidden, "you are not allowed to configure this agent")
		return
	}
	offers, err := h.contextCapVisibleOffers(ctx, a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offer lookup failed")
		return
	}

	var resp contextCapAgentDetailResponse
	resp.Agent = contextCapAgentView(a)
	resp.Global.Connectors = []contextCapConnectorRefDTO{}
	resp.Global.Skills = []contextCapSkillDTO{}
	resp.Offers.Connectors = []contextCapOfferedConnectorDTO{}
	resp.Offers.Skills = []contextCapSkillDTO{}
	resp.Scenes = []contextCapSceneDTO{}

	global, err := h.authorizedConnectors(ctx, a.WorkspaceID, a.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "connector lookup failed")
		return
	}
	for _, c := range global {
		resp.Global.Connectors = append(resp.Global.Connectors, contextCapConnectorRefDTO{ID: c.ID, Name: c.Name, CatalogSlug: c.CatalogSlug})
	}
	if resp.Global.Skills, err = h.contextCapSkills(ctx, `SELECT s.id::text, s.name, s.description
		FROM skill s JOIN agent_skill ask ON ask.skill_id = s.id
		WHERE ask.agent_id = $1::uuid AND ask.enabled AND s.workspace_id = $2::uuid
		ORDER BY s.name, s.id`, a.ID, a.WorkspaceID); err != nil {
		writeError(w, http.StatusInternalServerError, "skill lookup failed")
		return
	}
	if len(offers.ConnectorIDs) > 0 {
		offered, err := h.queryInternalConnectors(ctx, `SELECT `+internalConnectorSelect+`
			FROM internal_connector c
			WHERE c.workspace_id=$1::uuid AND c.id = ANY($2::uuid[]) AND c.enabled
			ORDER BY c.name, c.id`, a.WorkspaceID, offers.ConnectorIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "connector lookup failed")
			return
		}
		githubOAuthConfigured := githubUserAuthorizationConfigured()
		for _, c := range offered {
			accepts := connectorAcceptsBearer(c.AuthMode, c.CatalogSlug)
			usesCredential := c.AuthMode == "bearer" || c.AuthMode == "oauth"
			tools := c.AllowedTools
			if tools == nil {
				tools = []string{}
			}
			item := contextCapOfferedConnectorDTO{
				ID: c.ID, Name: c.Name, Tools: tools, AcceptsCredential: accepts,
				CredentialRequired: usesCredential && !h.connectorCredentialReady(c),
				CatalogSlug:        c.CatalogSlug, AuthMode: c.AuthMode,
				AcceptsPAT: c.AuthMode == "oauth" && accepts,
			}
			if app, ok := catalogApp(c.CatalogSlug); ok {
				item.InstallURL = catalogAppInstallURL(app)
				item.OAuthAvailable = c.AuthMode == "oauth" && app.OAuthAvailable(githubOAuthConfigured)
			}
			resp.Offers.Connectors = append(resp.Offers.Connectors, item)
		}
	}
	if len(offers.SkillIDs) > 0 {
		if resp.Offers.Skills, err = h.contextCapSkills(ctx, `SELECT s.id::text, s.name, s.description
			FROM skill s WHERE s.workspace_id = $1::uuid AND s.id = ANY($2::uuid[])
			ORDER BY s.name, s.id`, a.WorkspaceID, offers.SkillIDs); err != nil {
			writeError(w, http.StatusInternalServerError, "skill lookup failed")
			return
		}
	}

	var person *contextcap.Grant
	sceneKeys := []string{}
	for i := range grants {
		if grants[i].ScopeType == contextcap.ScopeScene {
			sceneKeys = append(sceneKeys, grants[i].ScopeKey)
		}
	}
	kinds, err := h.contextCapSceneKinds(ctx, a, sceneKeys)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "scene lookup failed")
		return
	}
	for i := range grants {
		switch grants[i].ScopeType {
		case contextcap.ScopePerson:
			if person == nil {
				person = &grants[i]
			}
		case contextcap.ScopeScene:
			resp.Scenes = append(resp.Scenes, contextCapSceneView(grants[i], kinds[grants[i].ScopeKey]))
		}
	}
	if person != nil {
		bindings, err := contextcap.ListScopeBindings(ctx, h.DB, a.WorkspaceID, a.ID, contextcap.ScopePerson, a.OrgID, person.ScopeKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "binding lookup failed")
			return
		}
		credentials, err := contextcap.ListScopeCredentials(ctx, h.DB, a.WorkspaceID, a.ID, contextcap.ScopePerson, a.OrgID, person.ScopeKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "credential lookup failed")
			return
		}
		resp.Person = &contextCapPersonDTO{
			ScopeKey: person.ScopeKey, ScopeTitle: person.ScopeTitle, Source: person.Source, ExpiresAt: contextCapTime(person.ExpiresAt),
			Bindings: contextCapBindingViews(bindings, offers), Credentials: contextCapCredentialViews(credentials),
		}
	}
	// The JSAPI group picker needs H5 signing and a verified person grant
	// (ResolveContextConfigScene refuses without one).
	resp.JSAPIAvailable = person != nil && h.dingTalkJSAPIReady()
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) contextCapSkills(ctx context.Context, query string, args ...any) ([]contextCapSkillDTO, error) {
	rows, err := h.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contextCapSkillDTO{}
	for rows.Next() {
		var skill contextCapSkillDTO
		if err := rows.Scan(&skill.ID, &skill.Name, &skill.Description); err != nil {
			return nil, err
		}
		out = append(out, skill)
	}
	return out, rows.Err()
}

// GetContextConfigScene returns one granted scene's bindings and credential
// hints.
func (h *Handler) GetContextConfigScene(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return
	}
	sceneKey, ok := contextCapPathParam(r, "sceneKey")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scope")
		return
	}
	grant, ok := h.contextCapRequireGrant(w, r, a, userID, contextcap.ScopeScene, sceneKey)
	if !ok {
		return
	}
	ctx := r.Context()
	offers, err := h.contextCapVisibleOffers(ctx, a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offer lookup failed")
		return
	}
	bindings, err := contextcap.ListScopeBindings(ctx, h.DB, a.WorkspaceID, a.ID, contextcap.ScopeScene, a.OrgID, grant.ScopeKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "binding lookup failed")
		return
	}
	credentials, err := contextcap.ListScopeCredentials(ctx, h.DB, a.WorkspaceID, a.ID, contextcap.ScopeScene, a.OrgID, grant.ScopeKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credential lookup failed")
		return
	}
	kinds, err := h.contextCapSceneKinds(ctx, a, []string{grant.ScopeKey})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "scene lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scene":       contextCapSceneView(grant, kinds[grant.ScopeKey]),
		"bindings":    contextCapBindingViews(bindings, offers),
		"credentials": contextCapCredentialViews(credentials),
	})
}

// PutContextConfigBinding enables or disables one offered connector or skill
// for a granted scene or person scope. The optional share_in_groups sets the
// personal connector opt-in 「在群聊中由我触发时也可用」 (person scope and
// connectors only; omitted keeps the stored value).
func (h *Handler) PutContextConfigBinding(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return
	}
	var input struct {
		ScopeType     string `json:"scope_type"`
		ScopeKey      string `json:"scope_key"`
		ResourceType  string `json:"resource_type"`
		ResourceID    string `json:"resource_id"`
		Enabled       *bool  `json:"enabled"`
		ShareInGroups *bool  `json:"share_in_groups"`
	}
	if !decodeContextCapBody(w, r, contextCapBodyLimit, &input) {
		return
	}
	if input.Enabled == nil || (input.ResourceType != contextcap.ResourceConnector && input.ResourceType != contextcap.ResourceSkill) {
		writeError(w, http.StatusBadRequest, "resource_type and enabled are required")
		return
	}
	if input.ShareInGroups != nil && (input.ScopeType != contextcap.ScopePerson || input.ResourceType != contextcap.ResourceConnector) {
		writeError(w, http.StatusBadRequest, "share_in_groups applies only to personal connectors")
		return
	}
	resourceID, err := util.ParseUUID(strings.TrimSpace(input.ResourceID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource_id")
		return
	}
	grant, ok := h.contextCapRequireGrant(w, r, a, userID, input.ScopeType, input.ScopeKey)
	if !ok {
		return
	}
	ctx := r.Context()
	offers, err := h.contextCapVisibleOffers(ctx, a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offer lookup failed")
		return
	}
	if !offers.Contains(input.ResourceType, uuidToString(resourceID)) {
		writeError(w, http.StatusForbidden, "this item is not offered by the agent")
		return
	}
	binding, err := contextcap.UpsertBinding(ctx, h.DB, contextcap.BindingWrite{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: grant.ScopeType, OrgID: a.OrgID, ScopeKey: grant.ScopeKey,
		ScopeTitle: grant.ScopeTitle, ResourceType: input.ResourceType, ResourceID: uuidToString(resourceID),
		Enabled: *input.Enabled, ShareInGroups: input.ShareInGroups, ActorID: userID,
	})
	switch {
	case errors.Is(err, contextcap.ErrNotOffered):
		// The offer was removed between the check and the write.
		writeError(w, http.StatusForbidden, "this item is not offered by the agent")
		return
	case errors.Is(err, contextcap.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid binding")
		return
	case err != nil:
		slog.ErrorContext(ctx, "context capabilities: binding write failed", "agent_id", a.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "binding write failed")
		return
	}
	slog.InfoContext(ctx, "context capabilities: binding updated", "agent_id", a.ID, "scope_type", grant.ScopeType,
		"resource_type", binding.ResourceType, "resource_id", binding.ResourceID, "enabled", binding.Enabled,
		"share_in_groups", binding.ShareInGroups, "user_id", userID)
	writeJSON(w, http.StatusOK, map[string]any{"binding": contextCapBindingView(binding)})
}

// contextCapCredentialConnector checks that a scene or person credential may
// be stored for connectorID: an enabled connector of the agent's workspace
// that accepts a pasted token (a Bearer connector, or an official app that
// allows a Personal Access Token) and is offered to the agent or, for a
// person credential only, globally granted to it. A scene credential serves
// every member's run in the group, so it needs the admin's opt-in by offering
// the connector; bringing a personal token for a globally granted connector
// only affects the caller's own runs. It returns the connector's catalog slug
// ("" for custom connectors), or writes the error response and returns false.
func (h *Handler) contextCapCredentialConnector(w http.ResponseWriter, r *http.Request, a contextCapAgent, scopeType, connectorID string) (string, bool) {
	ctx := r.Context()
	var authMode, catalogSlug string
	var enabled, granted bool
	err := h.DB.QueryRow(ctx, `SELECT c.auth_mode, c.catalog_slug, c.enabled, EXISTS(
			SELECT 1 FROM internal_connector_agent g
			WHERE g.connector_id = c.id AND g.workspace_id = c.workspace_id AND g.agent_id = $3::uuid)
		FROM internal_connector c WHERE c.id = $1::uuid AND c.workspace_id = $2::uuid`,
		connectorID, a.WorkspaceID, a.ID).Scan(&authMode, &catalogSlug, &enabled, &granted)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "this connector is not available for the agent")
		return "", false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "connector lookup failed")
		return "", false
	}
	offered, err := contextcap.IsOffered(ctx, h.DB, a.WorkspaceID, a.ID, contextcap.ResourceConnector, connectorID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offer lookup failed")
		return "", false
	}
	usable := offered || (granted && scopeType == contextcap.ScopePerson)
	if !enabled || !usable {
		writeError(w, http.StatusForbidden, "this connector is not available for the agent")
		return "", false
	}
	if !connectorAcceptsBearer(authMode, catalogSlug) {
		writeError(w, http.StatusBadRequest, "this connector does not accept a Bearer credential")
		return "", false
	}
	return catalogSlug, true
}

// PutContextConfigCredential stores a write-only Bearer credential for one
// connector in a granted scene or person scope. The response carries only the
// hint and update time.
func (h *Handler) PutContextConfigCredential(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return
	}
	var input struct {
		ScopeType   string `json:"scope_type"`
		ScopeKey    string `json:"scope_key"`
		ConnectorID string `json:"connector_id"`
		Bearer      string `json:"bearer"`
	}
	if !decodeContextCapBody(w, r, contextCapBodyLimit, &input) {
		return
	}
	connectorUUID, err := util.ParseUUID(strings.TrimSpace(input.ConnectorID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid connector_id")
		return
	}
	if !contextcap.ValidBearer(input.Bearer) {
		writeError(w, http.StatusBadRequest, "invalid Bearer credential")
		return
	}
	grant, ok := h.contextCapRequireGrant(w, r, a, userID, input.ScopeType, input.ScopeKey)
	if !ok {
		return
	}
	connectorID := uuidToString(connectorUUID)
	catalogSlug, ok := h.contextCapCredentialConnector(w, r, a, grant.ScopeType, connectorID)
	if !ok {
		return
	}
	box := h.contextCredentialBox()
	if box == nil {
		writeError(w, http.StatusServiceUnavailable, "connector credential storage is not configured")
		return
	}
	key := contextcap.CredentialBinding{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, ConnectorID: connectorID,
		ScopeType: grant.ScopeType, OrgID: a.OrgID, ScopeKey: grant.ScopeKey,
	}
	sealed, err := contextcap.SealCredential(box, key, input.Bearer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credential could not be saved")
		return
	}
	stored, err := contextcap.UpsertCredential(r.Context(), h.DB, key, sealed, contextcap.Hint(input.Bearer), userID)
	if err != nil {
		slog.ErrorContext(r.Context(), "context capabilities: credential write failed", "agent_id", a.ID, "connector_id", connectorID, "error", err)
		writeError(w, http.StatusInternalServerError, "credential could not be saved")
		return
	}
	slog.InfoContext(r.Context(), "context capabilities: credential stored", "agent_id", a.ID, "connector_id", connectorID,
		"scope_type", grant.ScopeType, "user_id", userID)
	if catalogSlug != "" {
		// First credential of an official app (a Personal Access Token): pin
		// its tools now. Failure is not fatal; the admin can refresh tools.
		if _, _, err := h.discoverCatalogToolsIfEmpty(r.Context(), a.WorkspaceID, connectorID, contextcap.Secret{Bearer: input.Bearer}, grant.ScopeType, key); err != nil {
			slog.WarnContext(r.Context(), "official app first tool discovery failed", "agent_id", a.ID, "connector_id", connectorID, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential": contextCapCredentialView(stored)})
}

// DeleteContextConfigCredential removes a scene or person credential. It is
// idempotent and allowed even after the connector stopped being offered.
func (h *Handler) DeleteContextConfigCredential(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	connectorUUID, err := util.ParseUUID(strings.TrimSpace(query.Get("connector_id")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid connector_id")
		return
	}
	grant, ok := h.contextCapRequireGrant(w, r, a, userID, query.Get("scope_type"), query.Get("scope_key"))
	if !ok {
		return
	}
	connectorID := uuidToString(connectorUUID)
	if _, err := contextcap.DeleteCredential(r.Context(), h.DB, contextcap.CredentialBinding{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, ConnectorID: connectorID,
		ScopeType: grant.ScopeType, OrgID: a.OrgID, ScopeKey: grant.ScopeKey,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "credential could not be removed")
		return
	}
	slog.InfoContext(r.Context(), "context capabilities: credential removed", "agent_id", a.ID, "connector_id", connectorID,
		"scope_type", grant.ScopeType, "user_id", userID)
	w.WriteHeader(http.StatusNoContent)
}

// ResolveContextConfigScene grants the caller a scene picked with the
// DingTalk JSAPI group picker. It requires a live person grant for the agent
// (a verified DingTalk identity), converts chat_id with the corp app, and
// accepts only a known group scene of this agent (scene_memory, kind group).
// DingTalk offers no general membership check, so the server trusts that only
// group members can obtain a group's chatId from the picker. chat_id is
// required: an openConversationId is not a secret (every scene-grant holder
// sees it, and it outlives membership), so a bare one is never proof of
// membership. open_conversation_id is only cross-checked against the
// converted chat_id.
func (h *Handler) ResolveContextConfigScene(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return
	}
	var input struct {
		ChatID             string `json:"chat_id"`
		OpenConversationID string `json:"open_conversation_id"`
	}
	if !decodeContextCapBody(w, r, contextCapBodyLimit, &input) {
		return
	}
	chatID, cid := strings.TrimSpace(input.ChatID), strings.TrimSpace(input.OpenConversationID)
	if chatID == "" {
		writeError(w, http.StatusBadRequest, "chat_id is required")
		return
	}
	if len(chatID) > 256 {
		writeError(w, http.StatusBadRequest, "invalid chat_id")
		return
	}
	ctx := r.Context()
	grants, err := h.contextCapLiveGrants(ctx, a, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "grant lookup failed")
		return
	}
	hasPerson := false
	for _, grant := range grants {
		if grant.ScopeType == contextcap.ScopePerson {
			hasPerson = true
			break
		}
	}
	if !hasPerson {
		writeError(w, http.StatusForbidden, "message the agent privately and open its personal configuration link first")
		return
	}
	client, ok := h.dingTalkJSAPIClient()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "DingTalk group lookup is not available")
		return
	}
	converted, err := client.ConvertChatIDToOpenConversationID(ctx, chatID)
	if errors.Is(err, dingtalk.ErrUnsupported) {
		writeError(w, http.StatusServiceUnavailable, "DingTalk group lookup is not available")
		return
	}
	if err != nil {
		slog.WarnContext(ctx, "context capabilities: chatId conversion failed", "agent_id", a.ID, "error", dingtalk.RedactURLError(err))
		writeError(w, http.StatusBadGateway, "DingTalk group lookup failed")
		return
	}
	if cid != "" && cid != converted {
		writeError(w, http.StatusBadRequest, "chat_id and open_conversation_id do not match")
		return
	}
	cid = converted
	if !contextcap.ValidOpenConversationID(cid) {
		writeError(w, http.StatusBadRequest, "invalid open_conversation_id")
		return
	}
	title, found, err := h.contextCapGroupSceneTitle(ctx, a.WorkspaceID, a.ID, a.OrgID, cid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "scene lookup failed")
		return
	}
	if !found {
		writeError(w, http.StatusForbidden, "the agent has not served this group yet")
		return
	}
	grant, err := contextcap.UpsertGrant(ctx, h.DB, contextcap.Grant{
		UserID: userID, WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: contextcap.ScopeScene,
		OrgID: a.OrgID, ScopeKey: cid, ScopeTitle: title, Source: contextcap.GrantSourceJSAPI,
	}, contextcap.GrantTTLScene)
	if err != nil {
		slog.ErrorContext(ctx, "context capabilities: scene grant failed", "agent_id", a.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "scene grant failed")
		return
	}
	slog.InfoContext(ctx, "context capabilities: scene granted via JSAPI", "agent_id", a.ID, "user_id", userID)
	// The JSAPI picker only ever grants a known group scene.
	writeJSON(w, http.StatusOK, map[string]any{"scene": contextCapSceneView(grant, contextcap.SceneKindGroup)})
}

// contextCapGroupSceneTitle reports whether cid is a known DingTalk group
// scene of the agent under orgID (scene_memory) and returns its title. An
// agent without agent_dingtalk_identity has orgID "" here, while scene memory
// falls back to the dispatch's DWS org for it, so "" matches the agent's
// scene rows of any org (they are still this agent's own groups).
func (h *Handler) contextCapGroupSceneTitle(ctx context.Context, workspaceID, agentID, orgID, cid string) (string, bool, error) {
	var title string
	err := h.DB.QueryRow(ctx, `SELECT scene_title FROM scene_memory
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND platform = 'dingtalk'
		  AND ($3::text = '' OR org_id = $3::text) AND scene_key = $4 AND scene_kind = 'group'
		ORDER BY (scene_title <> '') DESC, updated_at DESC
		LIMIT 1`, workspaceID, agentID, orgID, cid).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(title), true, nil
}
