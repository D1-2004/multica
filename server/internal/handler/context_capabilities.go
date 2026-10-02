package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Mobile context capability API (docs/context-capabilities.md §5-§6). Every
// route is wrapped with RequireDingTalkHumanActor in the router. Reading a
// scope takes a live context_config_grant for the exact (agent, org, scope),
// or, for the agent's scenes and org (enterprise) scopes, managing the agent
// (workspace owner/admin of its workspace, or the agent owner: the
// agent-manage permission of the admin routes). What the caller may change
// there is decided by one policy, contextCapScopeRights. A request works in
// one tenant org of the agent (its optional org_id, else the identity org).
// Plain workspace membership grants nothing, and a manager is never the
// person of a personal scope.

const contextCapBodyLimit = 8 << 10

// contextCapAgent is an agent resolved for a context capability call. OrgID
// is the org the call works in: the agent's current DingTalk org
// (agent_dingtalk_identity.org_id, "" when it has none) unless the request
// named another tenant of the agent (contextCapAgentInOrg). Grants,
// bindings and scenes are read and written under OrgID only.
// IdentityOrgID is always the agent's DingTalk identity org: Coordinator
// jobs that recorded no org belong to it.
type contextCapAgent struct {
	Agent         db.Agent
	ID            string
	WorkspaceID   string
	OrgID         string
	IdentityOrgID string
	// OrgName is the tenant name of OrgID when contextCapAgentInOrg resolved
	// it ("" otherwise).
	OrgName string
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
	// OrgID is the tenant org the scope lives in.
	OrgID string `json:"org_id"`
}

type contextCapSceneDTO struct {
	ScopeKey   string `json:"scope_key"`
	ScopeTitle string `json:"scope_title"`
	Source     string `json:"source"`
	ExpiresAt  string `json:"expires_at"`
	// Kind is "group" for a group chat and "dm" for a 1:1 chat.
	Kind string `json:"kind"`
	// OrgID is the tenant org the scene lives in.
	OrgID string `json:"org_id"`
}

// contextCapTenantRefDTO names one tenant of an agent on the configure page.
type contextCapTenantRefDTO struct {
	OrgID  string `json:"org_id"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// contextCapOrgLayerDTO is the enterprise (org) layer of the configure
// page's org: its bindings (offered resources only), credentials, prompt
// components and custom MCP servers. Every caller who may open the detail
// gets it; only agent managers have rights there (contextCapScopeRights).
// Credential hints are blank, and the MCP document withheld, for a caller
// without them.
type contextCapOrgLayerDTO struct {
	ScopeKey    string                    `json:"scope_key"`
	ScopeTitle  string                    `json:"scope_title"`
	Bindings    []contextCapBindingDTO    `json:"bindings"`
	Credentials []contextCapCredentialDTO `json:"credentials"`
	// Rights is what the caller may change in the org scope.
	Rights contextCapRights `json:"rights"`
	// CanEdit is Rights.Toggle, kept for older clients.
	CanEdit bool `json:"can_edit"`
	contextCapScopeComponentsDTO
}

type contextCapPersonDTO struct {
	ScopeKey    string                    `json:"scope_key"`
	ScopeTitle  string                    `json:"scope_title"`
	Source      string                    `json:"source"`
	ExpiresAt   string                    `json:"expires_at"`
	Bindings    []contextCapBindingDTO    `json:"bindings"`
	Credentials []contextCapCredentialDTO `json:"credentials"`
	// Rights is what the caller (the person) may change in the scope.
	Rights contextCapRights `json:"rights"`
	contextCapScopeComponentsDTO
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

// contextCapCatalogAppDTO is one official app of the connector catalog: the
// configure page lists every app it supports, including the ones not yet
// opened for this agent.
type contextCapCatalogAppDTO struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Setup is how its sign-in gets ready (contextConfigAppSetup):
	// "automatic", "oauth_app" or "unsupported".
	Setup string `json:"setup"`
	// Ready: an OAuth connect can start on this deployment now.
	Ready bool `json:"ready"`
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
	Person *contextCapPersonDTO `json:"person"`
	Scenes []contextCapSceneDTO `json:"scenes"`
	// Access is "manager" when the caller manages the agent (Scenes then
	// lists every scene of the agent), else "grant".
	Access         string `json:"access"`
	JSAPIAvailable bool   `json:"jsapi_available"`
	// Tenant is the org this detail describes (the request's org_id, else
	// the agent's identity org); null for an agent without a DingTalk
	// identity when none was named.
	Tenant *contextCapTenantRefDTO `json:"tenant"`
	// Tenants are the orgs the caller may open: every tenant for a manager,
	// else the tenants the caller holds a live grant under.
	Tenants []contextCapTenantRefDTO `json:"tenants"`
	// Org is the enterprise layer of Tenant (read-only unless the caller
	// manages the agent); null when there is no tenant.
	Org *contextCapOrgLayerDTO `json:"org"`
	// Apps is the connector catalog in its order; an app becomes usable once
	// the agent's manager adds it (Global or Offers then lists it).
	Apps []contextCapCatalogAppDTO `json:"apps"`
	// CanConfigureApps: the caller may save an app's OAuth application here
	// (the agent's managers).
	CanConfigureApps bool `json:"can_configure_apps"`
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
	return contextCapGrantDTO{ScopeType: g.ScopeType, ScopeKey: g.ScopeKey, ScopeTitle: g.ScopeTitle, Source: g.Source, ExpiresAt: contextCapTime(g.ExpiresAt), OrgID: g.OrgID}
}

func contextCapSceneView(g contextcap.Grant, kind string) contextCapSceneDTO {
	if kind != contextcap.SceneKindDM {
		kind = contextcap.SceneKindGroup
	}
	return contextCapSceneDTO{ScopeKey: g.ScopeKey, ScopeTitle: g.ScopeTitle, Source: g.Source, ExpiresAt: contextCapTime(g.ExpiresAt), Kind: kind, OrgID: g.OrgID}
}

func contextCapBindingView(b contextcap.Binding) contextCapBindingDTO {
	view := contextCapBindingDTO{ResourceType: b.ResourceType, ResourceID: b.ResourceID, Enabled: b.Enabled}
	if b.ScopeType == contextcap.ScopePerson && b.ResourceType == contextcap.ResourceConnector {
		share := b.ShareInGroups
		view.ShareInGroups = &share
	}
	return view
}

// contextCapSceneKinds returns the kind (group or dm) of each scene_id of
// agent a from its scene directory. Ids the directory does not know default
// to group.
func (h *Handler) contextCapSceneKinds(ctx context.Context, a contextCapAgent, ids []string) (map[string]string, error) {
	kinds := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return kinds, nil
	}
	scenes, _, err := contextcap.ListAgentScenes(ctx, h.DB, contextcap.SceneListQuery{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, OrgID: a.OrgID, IDs: ids,
	})
	if err != nil {
		return nil, err
	}
	for _, scene := range scenes {
		kinds[scene.SceneID] = scene.Kind
	}
	return kinds, nil
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

// contextCapMobileUser gates a mobile endpoint: 401 without a valid user. It
// returns the canonical user id and marks the response uncacheable.
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
	out.IdentityOrgID = out.OrgID
	return out, nil
}

// errContextCapUnknownTenant: a request named an org that is not a tenant
// of the agent.
var errContextCapUnknownTenant = errors.New("not a tenant of the agent")

// contextCapAgentInOrg returns a working in orgID, a tenant of the agent
// (contextcap.AgentTenants), with OrgName set. An empty orgID selects the
// agent's identity org. errContextCapUnknownTenant when orgID is not a
// tenant.
func (h *Handler) contextCapAgentInOrg(ctx context.Context, a contextCapAgent, orgID string) (contextCapAgent, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		orgID = a.IdentityOrgID
	}
	if orgID == "" {
		// An agent without a DingTalk identity keeps its org "" scope.
		a.OrgID, a.OrgName = "", ""
		return a, nil
	}
	if !contextcap.ValidOrgID(orgID) {
		return a, errContextCapUnknownTenant
	}
	tenant, err := contextcap.AgentTenant(ctx, h.DB, a.WorkspaceID, a.ID, orgID)
	if errors.Is(err, contextcap.ErrNotFound) {
		return a, errContextCapUnknownTenant
	}
	if err != nil {
		return a, err
	}
	a.OrgID, a.OrgName = tenant.OrgID, tenant.Name
	return a, nil
}

// Refusals of a mobile request by a caller without any access to the agent.
const (
	contextCapForbiddenAgent = "you are not allowed to configure this agent"
	contextCapForbiddenScope = "you are not allowed to configure this scope"
)

// contextCapRequestOrg applies the optional org_id of a mobile scope request
// (contextCapRequestOrgFor, refusing with contextCapForbiddenScope).
func (h *Handler) contextCapRequestOrg(w http.ResponseWriter, r *http.Request, a contextCapAgent, userID, orgID string) (contextCapAgent, bool) {
	return h.contextCapRequestOrgFor(w, r, a, userID, orgID, contextCapForbiddenScope)
}

// contextCapRequestOrgFor applies the optional org_id of a mobile request
// (contextCapAgentInOrg) and writes the error: 404 tenant_not_found for an
// org that is not a tenant of the agent, but only to a caller who manages
// the agent or holds a live grant for it. Anyone else gets the 403
// (forbidden) a tenant org would give them, so the routes do not tell which
// org ids are tenants.
func (h *Handler) contextCapRequestOrgFor(w http.ResponseWriter, r *http.Request, a contextCapAgent, userID, orgID, forbidden string) (contextCapAgent, bool) {
	out, err := h.contextCapAgentInOrg(r.Context(), a, orgID)
	if errors.Is(err, errContextCapUnknownTenant) {
		var access bool
		if access, err = h.contextCapHasAccess(r.Context(), a, userID); err == nil {
			if !access {
				writeError(w, http.StatusForbidden, forbidden)
				return contextCapAgent{}, false
			}
			err = errContextCapUnknownTenant
		}
	}
	switch {
	case err == nil:
		return out, true
	case errors.Is(err, errContextCapUnknownTenant):
		writeErrorCode(w, http.StatusNotFound, contextCapErrTenantNotFound, "this organization is not a tenant of the agent")
	default:
		slog.ErrorContext(r.Context(), "context capabilities: tenant lookup failed", "agent_id", a.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "tenant lookup failed")
	}
	return contextCapAgent{}, false
}

// contextCapHasAccess reports whether userID manages agent a or holds a
// live grant for it in any org.
func (h *Handler) contextCapHasAccess(ctx context.Context, a contextCapAgent, userID string) (bool, error) {
	manages, err := h.contextCapManages(ctx, a, userID)
	if err != nil || manages {
		return manages, err
	}
	grants, err := contextcap.ListLiveGrantsForUser(ctx, h.DB, userID, a.ID)
	if err != nil {
		return false, err
	}
	for _, grant := range grants {
		if grant.WorkspaceID == a.WorkspaceID {
			return true, nil
		}
	}
	return false, nil
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

// contextCapSourceManager is the source of a scene the caller configures as
// a manager of the agent rather than through a grant; it has no expiry.
const contextCapSourceManager = "manager"

// Access of the caller to an agent on the configure page.
const (
	contextCapAccessGrant   = "grant"
	contextCapAccessManager = "manager"
)

// contextCapScope is what one scene or person request resolves to
// (contextCapResolveScope).
//
// The embedded Grant is the EFFECTIVE scope the request reads and writes
// (ScopeType, ScopeKey, ScopeTitle), with the source and expiry of the
// caller's authority: the caller's live grant, or, for a scene of an agent
// the caller manages, a grant-shaped stand-in (source "manager", no expiry).
// A scene, group or 1:1 chat alike, is the agent's work scene keyed by its
// scene_id (docs/agent-scene.md); a 1:1 chat is never mapped to its
// counterpart person, whose personal scope is a separate person request.
type contextCapScope struct {
	contextcap.Grant
	// Manager: the caller acts through managing the agent, not a grant.
	Manager bool
	// Kind is the scene kind (group or dm) of a scene request; "" for a
	// person request.
	Kind string
	// Scene describes the scene a scene request named (key, title, and the
	// source and expiry of the caller's access to it); zero for a person
	// request. It equals Grant.
	Scene contextcap.Grant
	// Self: the caller is the person of the effective person scope (their
	// live person grant).
	Self bool
	// Rights is what the caller may change in the effective scope
	// (contextCapScopeRights).
	Rights contextCapRights
}

// contextCapNeed is what a scope request intends to do with the scope.
type contextCapNeed int

const (
	// contextCapNeedRead reads the scope.
	contextCapNeedRead contextCapNeed = iota
	// contextCapNeedToggle switches an offered connector or skill
	// (bindings): contextCapRights.Toggle.
	contextCapNeedToggle
	// contextCapNeedPrompts replaces the prompt components:
	// contextCapRights.EditPrompts.
	contextCapNeedPrompts
	// contextCapNeedMCP replaces the custom MCP servers:
	// contextCapRights.EditMCP.
	contextCapNeedMCP
	// contextCapNeedCredential stores, removes or connects a credential:
	// contextCapRights.Connect.
	contextCapNeedCredential
	// contextCapNeedRevoke revokes the configure-page grants of a group or
	// person scope. It is a manager action of the admin routes (which
	// already require managing the agent), not one of the rights; it only
	// needs the scope to be known.
	contextCapNeedRevoke
	// contextCapNeedRoutines creates, edits, runs or deletes the routines of
	// a scene: contextCapRights.EditRoutines.
	contextCapNeedRoutines
)

// contextCapRights is what one caller may change in one org, scene or person
// scope of the Context Builder, on the configure page and on the admin
// routes alike (contextCapScopeRights).
type contextCapRights struct {
	// Toggle: switch offered connectors and skills on or off (bindings,
	// share_in_groups).
	Toggle bool `json:"toggle"`
	// Connect: store, remove or connect accounts and tokens (credentials and
	// OAuth connects).
	Connect bool `json:"connect"`
	// EditPrompts: add, edit, delete and switch prompt components.
	EditPrompts bool `json:"edit_prompts"`
	// EditMCP: add, edit, delete and switch custom MCP servers.
	EditMCP bool `json:"edit_mcp"`
	// EditRoutines: create, edit, run and delete the scene's routines
	// (例行任务). Only group and 1:1 chat scenes have routines.
	EditRoutines bool `json:"edit_routines"`
}

// contextCapAllRights may change everything in an org or person scope.
var contextCapAllRights = contextCapRights{Toggle: true, Connect: true, EditPrompts: true, EditMCP: true}

// contextCapSceneRights may change everything in a scene, its routines too.
var contextCapSceneRights = contextCapRights{Toggle: true, Connect: true, EditPrompts: true, EditMCP: true, EditRoutines: true}

// contextCapScopeRights is the one edit-rights policy of the Context Builder
// (docs/context-capabilities.md §5 "Who may change what"). Every write path
// decides through it: the configure page's bindings, credentials, connects,
// prompts and custom MCP servers, the admin Context Builder's node writes,
// and the OAuth connect at start and at the callback
// (authorizeConnectorOAuthScope). scopeType is the scope's type (a 1:1 chat
// is a scene like a group); manages is the agent-manage permission
// (workspace owner/admin or the agent owner, contextCapManages); self is
// whether the caller is the person of a person scope (contextCapScope.Self).
//
//	scope         agent manager      the person   anyone else (link holders)
//	org           everything         -            nothing (shown read-only)
//	scene         everything         -            everything
//	person        nothing (view)     everything   nothing
//
// A scene (a group or a 1:1 chat) is changed by whoever may open it: a
// holder of its link like a manager, the same rule as changing it from the
// conversation (冬翔, 2026-10-02: keep it simple until people use it). A
// caller who is both a manager and the person edits the person scope as the
// person.
func contextCapScopeRights(scopeType string, manages, self bool) contextCapRights {
	switch scopeType {
	case contextcap.ScopeOrg:
		if manages {
			return contextCapAllRights
		}
	case contextcap.ScopeScene:
		return contextCapSceneRights
	case contextcap.ScopePerson:
		if self {
			return contextCapAllRights
		}
	}
	return contextCapRights{}
}

// contextCapScopeRef is the {type, key, title} of an effective scope in API
// responses.
type contextCapScopeRef struct {
	Type  string `json:"type"`
	Key   string `json:"key"`
	Title string `json:"title"`
}

// ref returns the effective scope as an API reference.
func (s contextCapScope) ref() *contextCapScopeRef {
	return &contextCapScopeRef{Type: s.ScopeType, Key: s.ScopeKey, Title: s.ScopeTitle}
}

// contextCapManages reports whether userID holds the agent-manage permission
// for agent a (memberManagesAgent, the rule canManageAgent applies on the
// admin routes): a member of the agent's workspace who is a workspace
// owner/admin or the agent's owner.
func (h *Handler) contextCapManages(ctx context.Context, a contextCapAgent, userID string) (bool, error) {
	member, err := h.getWorkspaceMember(ctx, userID, a.WorkspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return memberManagesAgent(a.Agent, member), nil
}

// contextCapWorkspaceAdmin reports whether userID is an owner or admin of
// agent a's workspace.
func (h *Handler) contextCapWorkspaceAdmin(ctx context.Context, a contextCapAgent, userID string) (bool, error) {
	member, err := h.getWorkspaceMember(ctx, userID, a.WorkspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return roleAllowed(member.Role, "owner", "admin"), nil
}

// contextCapManagedScope is the scope stand-in of a scene of agent a for a
// manager of the agent.
func contextCapManagedScope(a contextCapAgent, userID string, scene contextcap.SceneSummary) contextCapScope {
	grant := contextCapManagedSceneGrant(a, userID, scene.SceneID, agentSceneTitle(scene))
	return contextCapScope{Grant: grant, Manager: true, Kind: scene.Kind, Scene: grant}
}

// contextCapManagedSceneGrant is the grant-shaped stand-in (source
// "manager", no expiry) of a scene a manager of agent a configures.
func contextCapManagedSceneGrant(a contextCapAgent, userID, sceneID, title string) contextcap.Grant {
	return contextcap.Grant{
		UserID: userID, WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: contextcap.ScopeScene, OrgID: a.OrgID,
		ScopeKey: sceneID, ScopeTitle: title, Source: contextCapSourceManager,
	}
}

// Errors of contextCapResolveScope besides contextcap.ErrInvalidInput (a
// malformed scope) and contextcap.ErrNotFound (a manager's unknown scene).
// Any other error is a failed lookup; errContextCapSceneLookup marks the
// scene lookup among those.
var (
	errContextCapForbidden   = errors.New("not allowed to configure this scope")
	errContextCapSceneLookup = errors.New("scene lookup failed")
)

// contextCapResolveOptions carries what an admin route already knows, so
// the resolver does not look it up again.
type contextCapResolveOptions struct {
	// Scene is the requested scene when the caller already loaded it.
	Scene *contextcap.SceneSummary
	// Manages is the caller's agent-manage permission when already known.
	Manages *bool
	// ManagerPersons lets a manager read a person scope without that
	// person's grant (view only, contextCapScopeRights), as for a 1:1 chat.
	// The admin Context Builder sets it; the mobile routes never do.
	ManagerPersons bool
}

// contextCapResolveScope is the single place that maps an org, scene or
// person request of userID on agent a (under a.OrgID, a tenant org of the
// agent) to the effective scope and the caller's access to it
// (contextCapScope). The mobile routes (contextCapRequireScope), the admin
// Context Builder routes and the OAuth connect (authorizeConnectorOAuthScope,
// at start and at the callback) all use it. Who may read is decided here;
// what the caller may change is contextCapScopeRights, which it fills into
// Rights from the effective scope type, the caller's agent-manage
// permission and Self.
//
//   - Org request (scope key = a.OrgID): managing the agent, or any live
//     grant of the caller under that org (no rights; the configure page
//     shows the org layer read-only to everyone).
//   - Person request: the caller's live person grant for exactly that
//     staffId (Self; a manager is not the person). With ManagerPersons
//     (admin routes only), a manager reads it too.
//   - Scene request (scope key = a scene_id, a group or a 1:1 chat): the
//     caller's live scene grant, or managing the agent (contextCapManages)
//     and the scene being one of the agent's work scenes in a.OrgID
//     (agent_scene; 404 otherwise).
//
// The kind is the scene directory's.
func (h *Handler) contextCapResolveScope(ctx context.Context, a contextCapAgent, userID, scopeType, scopeKey string) (contextCapScope, error) {
	return h.contextCapResolveScopeWith(ctx, a, userID, scopeType, scopeKey, contextCapResolveOptions{})
}

func (h *Handler) contextCapResolveScopeWith(ctx context.Context, a contextCapAgent, userID, scopeType, scopeKey string, opts contextCapResolveOptions) (contextCapScope, error) {
	if !contextcap.ValidScopeKey(scopeType, scopeKey) {
		return contextCapScope{}, contextcap.ErrInvalidInput
	}
	liveGrant := func(scopeType, key string) (contextcap.Grant, bool, error) {
		grant, err := contextcap.GetLiveGrant(ctx, h.DB, userID, a.ID, scopeType, a.OrgID, key)
		switch {
		case err == nil:
			return grant, grant.WorkspaceID == a.WorkspaceID, nil
		case errors.Is(err, contextcap.ErrNotFound):
			return contextcap.Grant{}, false, nil
		default:
			return contextcap.Grant{}, false, fmt.Errorf("grant lookup: %w", err)
		}
	}
	managesKnown, managesValue := false, false
	manages := func() (bool, error) {
		if opts.Manages != nil {
			return *opts.Manages, nil
		}
		if !managesKnown {
			value, err := h.contextCapManages(ctx, a, userID)
			if err != nil {
				return false, fmt.Errorf("manager lookup: %w", err)
			}
			managesKnown, managesValue = true, value
		}
		return managesValue, nil
	}
	// withRights fills in what the caller may change in the resolved scope,
	// through the one policy (contextCapScopeRights).
	withRights := func(scope contextCapScope) (contextCapScope, error) {
		isManager, err := manages()
		if err != nil {
			return contextCapScope{}, err
		}
		scope.Rights = contextCapScopeRights(scope.ScopeType, isManager, scope.Self)
		return scope, nil
	}

	if scopeType == contextcap.ScopeOrg {
		// The org (enterprise) scope of the org the call works in: agent
		// managers configure it; people holding any live grant under that
		// org resolve it without rights (so their writes answer
		// manager_only); the configure page shows it to them read-only.
		if scopeKey != a.OrgID {
			return contextCapScope{}, contextcap.ErrInvalidInput
		}
		scope := contextCapScope{Grant: contextcap.Grant{
			UserID: userID, WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: contextcap.ScopeOrg, OrgID: a.OrgID,
			ScopeKey: a.OrgID, ScopeTitle: firstNonEmpty(a.OrgName, a.OrgID), Source: contextCapSourceManager,
		}}
		isManager, err := manages()
		if err != nil {
			return contextCapScope{}, err
		}
		if isManager {
			scope.Manager = true
			return withRights(scope)
		}
		grants, err := contextcap.ListLiveGrantsForUser(ctx, h.DB, userID, a.ID)
		if err != nil {
			return contextCapScope{}, fmt.Errorf("grant lookup: %w", err)
		}
		for _, grant := range grants {
			if grant.WorkspaceID == a.WorkspaceID && grant.OrgID == a.OrgID {
				scope.Source, scope.ExpiresAt = grant.Source, grant.ExpiresAt
				return withRights(scope)
			}
		}
		return contextCapScope{}, errContextCapForbidden
	}

	if scopeType == contextcap.ScopePerson {
		grant, ok, err := liveGrant(contextcap.ScopePerson, scopeKey)
		if err != nil {
			return contextCapScope{}, err
		}
		if ok {
			return withRights(contextCapScope{Grant: grant, Self: true})
		}
		if opts.ManagerPersons {
			isManager, err := manages()
			if err != nil {
				return contextCapScope{}, err
			}
			if isManager {
				return withRights(contextCapScope{Grant: contextcap.Grant{
					UserID: userID, WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: contextcap.ScopePerson, OrgID: a.OrgID,
					ScopeKey: scopeKey, Source: contextCapSourceManager,
				}, Manager: true})
			}
		}
		return contextCapScope{}, errContextCapForbidden
	}

	// A scene request: one of the agent's work scenes in a.OrgID, a group or
	// a 1:1 chat alike (docs/agent-scene.md).
	var scene contextcap.SceneSummary
	found := false
	if opts.Scene != nil {
		scene, found = *opts.Scene, true
	} else {
		loaded, err := contextcap.GetScene(ctx, h.DB, a.WorkspaceID, a.ID, a.OrgID, scopeKey)
		switch {
		case err == nil:
			scene, found = loaded, true
		case !errors.Is(err, contextcap.ErrNotFound):
			return contextCapScope{}, fmt.Errorf("%w: %w", errContextCapSceneLookup, err)
		}
	}
	sceneGrant, hasSceneGrant, err := liveGrant(contextcap.ScopeScene, scopeKey)
	if err != nil {
		return contextCapScope{}, err
	}
	if hasSceneGrant {
		kind := contextcap.SceneKindGroup
		if found {
			kind = scene.Kind
			sceneGrant.ScopeTitle = firstNonEmpty(agentSceneTitle(scene), sceneGrant.ScopeTitle)
		}
		return withRights(contextCapScope{Grant: sceneGrant, Kind: kind, Scene: sceneGrant})
	}
	isManager, err := manages()
	if err != nil {
		return contextCapScope{}, err
	}
	if !isManager {
		return contextCapScope{}, errContextCapForbidden
	}
	if !found {
		return contextCapScope{}, contextcap.ErrNotFound
	}
	return withRights(contextCapManagedScope(a, userID, scene))
}

// contextCapRequireScope authorizes a mobile call on exactly (agent, agent
// org, scopeType, scopeKey) for need (contextCapResolveScope, then
// contextCapScopeAllows) and writes the error response otherwise: 400 for a
// malformed scope, 403 without access or without the right need asks for
// (codes manager_only and person_only), 404 for a manager's unknown scene.
func (h *Handler) contextCapRequireScope(w http.ResponseWriter, r *http.Request, a contextCapAgent, userID, scopeType, scopeKey string, need contextCapNeed) (contextCapScope, bool) {
	return h.contextCapRequireScopeWith(w, r, a, userID, scopeType, scopeKey, need, contextCapResolveOptions{})
}

func (h *Handler) contextCapRequireScopeWith(w http.ResponseWriter, r *http.Request, a contextCapAgent, userID, scopeType, scopeKey string, need contextCapNeed, opts contextCapResolveOptions) (contextCapScope, bool) {
	scope, err := h.contextCapResolveScopeWith(r.Context(), a, userID, scopeType, scopeKey, opts)
	switch {
	case err == nil:
		return scope, contextCapScopeAllows(w, scope, need)
	case errors.Is(err, contextcap.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid scope")
	case errors.Is(err, errContextCapForbidden):
		writeError(w, http.StatusForbidden, contextCapForbiddenScope)
	case errors.Is(err, contextcap.ErrNotFound):
		writeError(w, http.StatusNotFound, "scene not found")
	case errors.Is(err, errContextCapSceneLookup):
		slog.ErrorContext(r.Context(), "context capabilities: scene lookup failed", "agent_id", a.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "scene lookup failed")
	default:
		slog.ErrorContext(r.Context(), "context capabilities: grant lookup failed", "agent_id", a.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "grant lookup failed")
	}
	return contextCapScope{}, false
}

// contextCapScopeAllows checks need against a resolved scope's Rights
// (contextCapScopeRights) and writes the refusal: 403 person_only when a
// person scope's right is missing (only the person changes it), else 403
// manager_only (only agent managers change org and scene scopes).
func contextCapScopeAllows(w http.ResponseWriter, scope contextCapScope, need contextCapNeed) bool {
	if need == contextCapNeedRead {
		return true
	}
	allowed := false
	switch need {
	case contextCapNeedToggle:
		allowed = scope.Rights.Toggle
	case contextCapNeedPrompts:
		allowed = scope.Rights.EditPrompts
	case contextCapNeedMCP:
		allowed = scope.Rights.EditMCP
	case contextCapNeedCredential:
		allowed = scope.Rights.Connect
	case contextCapNeedRevoke:
		allowed = true
	case contextCapNeedRoutines:
		allowed = scope.Rights.EditRoutines
	}
	if allowed {
		return true
	}
	switch scope.ScopeType {
	case contextcap.ScopePerson:
		writeErrorCode(w, http.StatusForbidden, contextCapErrPersonOnly,
			"only the person can change their own configuration")
	case contextcap.ScopeOrg:
		writeErrorCode(w, http.StatusForbidden, contextCapErrManagerOnly,
			"only an agent manager can change the enterprise configuration")
	default:
		writeErrorCode(w, http.StatusForbidden, contextCapErrManagerOnly,
			"only an agent manager can change the scene configuration")
	}
	return false
}

// Error codes of scope requests.
const (
	// contextCapErrPersonOnly: a write to a person's scope by someone who is
	// not that person, a manager included.
	contextCapErrPersonOnly = "person_only"
	// contextCapErrManagerOnly: a write to an org or group scope by someone
	// who does not manage the agent (a configure-link holder).
	contextCapErrManagerOnly = "manager_only"
	// contextCapErrTenantNotFound: the request named an org that is not a
	// tenant of the agent.
	contextCapErrTenantNotFound = "tenant_not_found"
)

// contextCapScopeOrg is the org a mobile scope request works in: its org_id,
// or, for an org scope without one, the org the scope key names.
func contextCapScopeOrg(scopeType, scopeKey, orgID string) string {
	if strings.TrimSpace(orgID) == "" && scopeType == contextcap.ScopeOrg {
		return scopeKey
	}
	return orgID
}

// contextCapGrantOrgs returns the orgs a grant of agent a counts under: the
// agent's tenants (contextcap.AgentTenants), plus "" for an agent without a
// DingTalk identity (its scopes live under the org "").
func (h *Handler) contextCapGrantOrgs(ctx context.Context, a contextCapAgent) (map[string]contextcap.Tenant, error) {
	tenants, err := contextcap.AgentTenants(ctx, h.DB, a.WorkspaceID, a.ID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]contextcap.Tenant, len(tenants)+1)
	for _, tenant := range tenants {
		out[tenant.OrgID] = tenant
	}
	if a.IdentityOrgID == "" {
		out[""] = contextcap.Tenant{Source: contextcap.TenantSourceIdentity}
	}
	return out, nil
}

// contextCapLiveGrants returns the caller's live grants for agent a under the
// org the call works in (a.OrgID), newest first.
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
// grant for the caller. Scene links (a group's or a 1:1 chat's) stay
// reusable until they expire; a stored person link is consumed atomically by
// the first redemption. The link and the grant commit together.
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
	if link.ScopeType == contextcap.ScopePerson && link.ExtraSceneID != "" {
		// A personal link minted in a 1:1 chat before 1:1 chats got scene
		// links (2026-10-02; no run mints one now) also grants that 1:1
		// chat's scene (its scene_id, registered when the chat's message
		// arrived), for as long as the person grant: nobody but the person
		// takes part in it.
		if _, err := contextcap.UpsertGrant(ctx, tx, contextcap.Grant{
			UserID: userID, WorkspaceID: link.WorkspaceID, AgentID: link.AgentID, ScopeType: contextcap.ScopeScene,
			OrgID: link.OrgID, ScopeKey: link.ExtraSceneID, ScopeTitle: link.ScopeTitle, Source: contextcap.GrantSourceAgentLink,
		}, contextcap.GrantTTLPerson); err != nil {
			slog.ErrorContext(ctx, "context capabilities: DM scene grant failed during redeem", "agent_id", link.AgentID, "error", err)
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
		"org_id":       grant.OrgID,
	})
}

// ListContextConfigAgents lists the agents the caller holds a live grant for,
// with those grants (access "grant"), then the other agents the caller
// manages (access "manager", no scopes): non-archived user agents of
// workspaces where the caller is owner/admin, and agents the caller owns in
// a workspace they belong to. An agent both granted and managed reports
// "manager". Grants of archived agents or of a previous DingTalk org of the
// agent are omitted.
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
	managed, err := h.contextCapManagedAgents(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "context capabilities: managed agent lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "agent lookup failed")
		return
	}
	type agentEntry struct {
		contextCapAgentDTO
		// Access is "grant" (configuration links only) or "manager" (the
		// caller manages the agent and may configure all of its scenes).
		Access string               `json:"access"`
		Scopes []contextCapGrantDTO `json:"scopes"`
	}
	agents := []*agentEntry{}
	byID := map[string]*agentEntry{}
	loaded := map[string]*contextCapAgent{}
	orgs := map[string]map[string]contextcap.Tenant{}
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
				if orgs[a.ID], err = h.contextCapGrantOrgs(ctx, a); err != nil {
					writeError(w, http.StatusInternalServerError, "tenant lookup failed")
					return
				}
			}
			loaded[grant.AgentID] = agent
		}
		if agent == nil || grant.WorkspaceID != agent.WorkspaceID {
			continue
		}
		if _, tenant := orgs[agent.ID][grant.OrgID]; !tenant {
			continue
		}
		entry, ok := byID[agent.ID]
		if !ok {
			entry = &agentEntry{contextCapAgentDTO: contextCapAgentView(*agent), Access: contextCapAccessGrant, Scopes: []contextCapGrantDTO{}}
			byID[agent.ID] = entry
			agents = append(agents, entry)
		}
		entry.Scopes = append(entry.Scopes, contextCapGrantView(grant))
	}
	for _, agent := range managed {
		if entry, ok := byID[agent.ID]; ok {
			entry.Access = contextCapAccessManager
			continue
		}
		entry := &agentEntry{contextCapAgentDTO: agent, Access: contextCapAccessManager, Scopes: []contextCapGrantDTO{}}
		byID[agent.ID] = entry
		agents = append(agents, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

// contextCapManagedAgents lists the non-archived user agents userID manages
// (contextCapManages), ordered by name.
func (h *Handler) contextCapManagedAgents(ctx context.Context, userID string) ([]contextCapAgentDTO, error) {
	// The WHERE clause is the SQL form of memberManagesAgent: a workspace
	// owner/admin, or the agent owner (a NULL owner_id never matches).
	rows, err := h.DB.Query(ctx, `SELECT a.id::text, a.name, a.avatar_url, a.workspace_id::text
		FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id AND m.user_id = $1::uuid
		WHERE a.archived_at IS NULL AND a.kind = 'user' AND (m.role IN ('owner', 'admin') OR a.owner_id = $1::uuid)
		ORDER BY a.name, a.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contextCapAgentDTO{}
	for rows.Next() {
		var agent contextCapAgentDTO
		if err := rows.Scan(&agent.ID, &agent.Name, &agent.AvatarURL, &agent.WorkspaceID); err != nil {
			return nil, err
		}
		out = append(out, agent)
	}
	return out, rows.Err()
}

// GetContextConfigAgent returns what the caller may see and configure for one
// agent in one of its tenant orgs (?org_id=, else the identity org, or, for
// a caller without a grant there who does not manage the agent, the org of
// the caller's newest grant): global items (read-only), the offer catalog,
// the org (enterprise) layer (rights for agent managers only), the caller's person
// scope and scenes: the granted ones, plus, for a manager of the agent,
// every scene the agent has seen in that org (source "manager"). The org
// layer and the person scope carry their rights (contextCapScopeRights),
// prompt components and custom MCP servers. It needs at least one live
// grant for the agent in that org or the agent-manage permission. Upstream
// URLs, credential references and workspace credential material are never
// returned.
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
	manages, err := h.contextCapManages(ctx, a, userID)
	if err != nil {
		slog.ErrorContext(ctx, "context capabilities: manager lookup failed", "agent_id", a.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "grant lookup failed")
		return
	}
	tenantOrgs, err := h.contextCapGrantOrgs(ctx, a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tenant lookup failed")
		return
	}
	allGrants, err := contextcap.ListLiveGrantsForUser(ctx, h.DB, userID, a.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "grant lookup failed")
		return
	}
	grantOrgs := map[string]bool{}
	newestGrantOrg := ""
	for _, grant := range allGrants {
		if _, tenant := tenantOrgs[grant.OrgID]; grant.WorkspaceID != a.WorkspaceID || !tenant {
			continue
		}
		if len(grantOrgs) == 0 {
			newestGrantOrg = grant.OrgID
		}
		grantOrgs[grant.OrgID] = true
	}
	requestedOrg := strings.TrimSpace(r.URL.Query().Get("org_id"))
	if requestedOrg == "" && !manages && !grantOrgs[a.IdentityOrgID] && len(grantOrgs) > 0 {
		requestedOrg = newestGrantOrg
	}
	if a, ok = h.contextCapRequestOrgFor(w, r, a, userID, requestedOrg, contextCapForbiddenAgent); !ok {
		return
	}
	grants, err := h.contextCapLiveGrants(ctx, a, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "grant lookup failed")
		return
	}
	if len(grants) == 0 && !manages {
		writeError(w, http.StatusForbidden, contextCapForbiddenAgent)
		return
	}
	offers, err := h.contextCapVisibleOffers(ctx, a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offer lookup failed")
		return
	}

	var resp contextCapAgentDetailResponse
	resp.Agent = contextCapAgentView(a)
	resp.Access = contextCapAccessGrant
	if manages {
		resp.Access = contextCapAccessManager
	}
	resp.Global.Connectors = []contextCapConnectorRefDTO{}
	resp.Global.Skills = []contextCapSkillDTO{}
	resp.Offers.Connectors = []contextCapOfferedConnectorDTO{}
	resp.Offers.Skills = []contextCapSkillDTO{}
	resp.Scenes = []contextCapSceneDTO{}
	resp.Tenants = []contextCapTenantRefDTO{}
	resp.Apps = []contextCapCatalogAppDTO{}
	for _, app := range connectorCatalog.Apps() {
		resp.Apps = append(resp.Apps, contextCapCatalogAppDTO{
			Slug: app.Slug, Name: app.Name, Setup: contextConfigAppSetup(app.Slug),
			Ready: h.catalogOAuthAvailableFor(ctx, a.WorkspaceID, app),
		})
	}
	resp.CanConfigureApps = manages
	tenants, err := contextcap.AgentTenants(ctx, h.DB, a.WorkspaceID, a.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tenant lookup failed")
		return
	}
	for _, tenant := range tenants {
		ref := contextCapTenantRefDTO{OrgID: tenant.OrgID, Name: tenant.Name, Source: tenant.Source}
		if tenant.OrgID == a.OrgID {
			current := ref
			resp.Tenant = &current
		}
		if manages || grantOrgs[tenant.OrgID] {
			resp.Tenants = append(resp.Tenants, ref)
		}
	}
	// The enterprise layer: every caller who may open the detail sees it,
	// since its items apply in their scopes too (switches add up across
	// levels; credentials resolve person > scene > org > workspace). Only
	// agent managers have rights there (contextCapScopeRights), and the
	// configure page shows it read-only to everyone: managers change it in
	// the admin Context Builder. Without rights, credential hints are blank
	// and the MCP document is withheld (contextCapScopeComponents).
	if resp.Tenant != nil {
		orgScope, err := h.contextCapResolveScopeWith(ctx, a, userID, contextcap.ScopeOrg, a.OrgID, contextCapResolveOptions{Manages: &manages})
		if err != nil {
			slog.ErrorContext(ctx, "context capabilities: org scope lookup failed", "agent_id", a.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "grant lookup failed")
			return
		}
		bindings, err := contextcap.ListScopeBindings(ctx, h.DB, a.WorkspaceID, a.ID, contextcap.ScopeOrg, a.OrgID, a.OrgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "binding lookup failed")
			return
		}
		credentials, err := contextcap.ListScopeCredentials(ctx, h.DB, a.WorkspaceID, a.ID, contextcap.ScopeOrg, a.OrgID, a.OrgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "credential lookup failed")
			return
		}
		components, err := h.contextCapScopeComponents(ctx, a, contextcap.ScopeOrg, a.OrgID, orgScope.Rights)
		if err != nil {
			slog.ErrorContext(ctx, "context capabilities: org components lookup failed", "agent_id", a.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "component lookup failed")
			return
		}
		layer := &contextCapOrgLayerDTO{
			ScopeKey: a.OrgID, ScopeTitle: orgScope.ScopeTitle, Bindings: contextCapBindingViews(bindings, offers),
			Credentials: contextCapCredentialViews(credentials), Rights: orgScope.Rights, CanEdit: orgScope.Rights.Toggle,
			contextCapScopeComponentsDTO: components,
		}
		if !orgScope.Rights.Connect {
			for i := range layer.Credentials {
				layer.Credentials[i].Hint = ""
			}
		}
		// A switched-off component is a draft: only those who may edit the
		// enterprise prompts see it.
		if !orgScope.Rights.EditPrompts {
			enabled := make([]contextCapPromptDTO, 0, len(layer.Prompts))
			for _, prompt := range layer.Prompts {
				if prompt.Enabled {
					enabled = append(enabled, prompt)
				}
			}
			layer.Prompts = enabled
		}
		resp.Org = layer
	}

	// Every granted connector is a 通用能力, including one that waits for an
	// account: a scope may connect its own.
	global, err := h.grantedConnectors(ctx, a.WorkspaceID, a.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "connector lookup failed")
		return
	}
	grantedIDs := make([]string, 0, len(global))
	for _, c := range global {
		resp.Global.Connectors = append(resp.Global.Connectors, contextCapConnectorRefDTO{ID: c.ID, Name: c.Name, CatalogSlug: c.CatalogSlug})
		grantedIDs = append(grantedIDs, c.ID)
	}
	// Without connect rights, the enterprise accounts listed are those of
	// connectors this page can show (offered or the agent's own), as the
	// bindings are.
	if resp.Org != nil && !resp.Org.Rights.Connect {
		shown := make([]contextCapCredentialDTO, 0, len(resp.Org.Credentials))
		for _, credential := range resp.Org.Credentials {
			if offers.Contains(contextcap.ResourceConnector, credential.ConnectorID) || slices.Contains(grantedIDs, credential.ConnectorID) {
				shown = append(shown, credential)
			}
		}
		resp.Org.Credentials = shown
	}
	if resp.Global.Skills, err = h.contextCapSkills(ctx, `SELECT s.id::text, s.name, s.description
		FROM skill s JOIN agent_skill ask ON ask.skill_id = s.id
		WHERE ask.agent_id = $1::uuid AND ask.enabled AND s.workspace_id = $2::uuid
		ORDER BY s.name, s.id`, a.ID, a.WorkspaceID); err != nil {
		writeError(w, http.StatusInternalServerError, "skill lookup failed")
		return
	}
	if scopeIDs := scopeConnectorIDs(offers.ConnectorIDs, grantedIDs); len(scopeIDs) > 0 {
		listed, err := h.queryInternalConnectors(ctx, `SELECT `+internalConnectorSelect+`
			FROM internal_connector c
			WHERE c.workspace_id=$1::uuid AND c.id = ANY($2::uuid[]) AND c.enabled
			ORDER BY c.name, c.id`, a.WorkspaceID, scopeIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "connector lookup failed")
			return
		}
		for _, c := range listed {
			if !offers.Contains(contextcap.ResourceConnector, c.ID) && !takesScopeAccount(c) {
				continue
			}
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
				item.OAuthAvailable = c.AuthMode == "oauth" && h.catalogOAuthAvailableFor(ctx, a.WorkspaceID, app)
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

	// A manager sees every scene of the agent (newest activity first, from
	// one statement). The scene records know the kind better than the
	// configuration alone (a Coordinator conversation's type is positive
	// evidence), so only granted scenes outside that list look their kind up
	// in the configuration.
	var managed []contextcap.SceneSummary
	managedKinds := map[string]string{}
	if manages {
		if managed, _, err = contextcap.ListAllAgentScenes(ctx, h.DB, contextcap.SceneListQuery{
			WorkspaceID: a.WorkspaceID, AgentID: a.ID, OrgID: a.OrgID,
		}, contextcap.MaxAllScenes); err != nil {
			slog.ErrorContext(ctx, "context capabilities: scene list failed", "agent_id", a.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "scene lookup failed")
			return
		}
		for _, scene := range managed {
			managedKinds[scene.SceneID] = scene.Kind
		}
	}
	var person *contextcap.Grant
	sceneKeys := []string{}
	for i := range grants {
		if _, known := managedKinds[grants[i].ScopeKey]; grants[i].ScopeType == contextcap.ScopeScene && !known {
			sceneKeys = append(sceneKeys, grants[i].ScopeKey)
		}
	}
	kinds, err := h.contextCapSceneKinds(ctx, a, sceneKeys)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "scene lookup failed")
		return
	}
	for key, kind := range managedKinds {
		kinds[key] = kind
	}
	listed := map[string]bool{}
	for i := range grants {
		switch grants[i].ScopeType {
		case contextcap.ScopePerson:
			if person == nil {
				person = &grants[i]
			}
		case contextcap.ScopeScene:
			listed[grants[i].ScopeKey] = true
			resp.Scenes = append(resp.Scenes, contextCapSceneView(grants[i], kinds[grants[i].ScopeKey]))
		}
	}
	for _, scene := range managed {
		if listed[scene.SceneID] {
			continue
		}
		listed[scene.SceneID] = true
		scope := contextCapManagedScope(a, userID, scene)
		resp.Scenes = append(resp.Scenes, contextCapSceneView(scope.Grant, scope.Kind))
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
		components, err := h.contextCapScopeComponents(ctx, a, contextcap.ScopePerson, person.ScopeKey, contextCapScopeRights(contextcap.ScopePerson, manages, true))
		if err != nil {
			slog.ErrorContext(ctx, "context capabilities: person components lookup failed", "agent_id", a.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "component lookup failed")
			return
		}
		resp.Person = &contextCapPersonDTO{
			ScopeKey: person.ScopeKey, ScopeTitle: person.ScopeTitle, Source: person.Source, ExpiresAt: contextCapTime(person.ExpiresAt),
			Bindings: contextCapBindingViews(bindings, offers), Credentials: contextCapCredentialViews(credentials),
			// The caller's own person grant: the caller is that person.
			Rights:                       contextCapScopeRights(contextcap.ScopePerson, manages, true),
			contextCapScopeComponentsDTO: components,
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

// GetContextConfigScene returns one scene (a scene_id; a group or a 1:1 chat)
// the caller holds a grant for or, as a manager of the agent, one of the
// agent's work scenes, with the bindings, credential hints, prompt
// components and custom MCP servers of that scene. rights says what the
// caller may change there (contextCapScopeRights); can_connect is
// rights.connect.
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
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, r.URL.Query().Get("org_id")); !ok {
		return
	}
	scope, ok := h.contextCapRequireScope(w, r, a, userID, contextcap.ScopeScene, sceneKey, contextCapNeedRead)
	if !ok {
		return
	}
	ctx := r.Context()
	bindingViews := []contextCapBindingDTO{}
	credentialViews := []contextCapCredentialDTO{}
	components := contextCapScopeComponentsDTO{Prompts: []contextCapPromptDTO{}}
	{
		offers, err := h.contextCapVisibleOffers(ctx, a)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "offer lookup failed")
			return
		}
		bindings, err := contextcap.ListScopeBindings(ctx, h.DB, a.WorkspaceID, a.ID, scope.ScopeType, a.OrgID, scope.ScopeKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "binding lookup failed")
			return
		}
		credentials, err := contextcap.ListScopeCredentials(ctx, h.DB, a.WorkspaceID, a.ID, scope.ScopeType, a.OrgID, scope.ScopeKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "credential lookup failed")
			return
		}
		bindingViews, credentialViews = contextCapBindingViews(bindings, offers), contextCapCredentialViews(credentials)
		if components, err = h.contextCapScopeComponents(ctx, a, scope.ScopeType, scope.ScopeKey, scope.Rights); err != nil {
			slog.ErrorContext(ctx, "context capabilities: scene components lookup failed", "agent_id", a.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "component lookup failed")
			return
		}
		// An account hint (a provider login, or the tail of a token) reaches
		// whoever may connect in this scope, and for a person also workspace
		// admins, as on the admin scene page and the connected-apps page. A
		// group link holder or a manager reading a 1:1 chat's person keeps the
		// connected state without the hint.
		if !scope.Rights.Connect && len(credentialViews) > 0 {
			admin := false
			if scope.ScopeType == contextcap.ScopePerson {
				if admin, err = h.contextCapWorkspaceAdmin(ctx, a, userID); err != nil {
					writeError(w, http.StatusInternalServerError, "member lookup failed")
					return
				}
			}
			if !admin {
				for i := range credentialViews {
					credentialViews[i].Hint = ""
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scene":               contextCapSceneView(scope.Scene, scope.Kind),
		"scope":               scope.ref(),
		"bindings":            bindingViews,
		"credentials":         credentialViews,
		"rights":              scope.Rights,
		"can_connect":         scope.Rights.Connect,
		"prompts":             components.Prompts,
		"mcp_config":          components.MCPConfig,
		"mcp_config_redacted": components.MCPConfigRedacted,
	})
}

// PutContextConfigBinding enables or disables one offered connector or skill
// in an org, scene or person scope the caller may toggle
// (contextCapRights.Toggle). The optional share_in_groups sets the personal
// connector opt-in 「在群聊中由我触发时也可用」 (person scope and connectors
// only; omitted keeps the stored value).
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
		OrgID         string `json:"org_id"`
		ResourceType  string `json:"resource_type"`
		ResourceID    string `json:"resource_id"`
		Enabled       *bool  `json:"enabled"`
		ShareInGroups *bool  `json:"share_in_groups"`
	}
	if !decodeContextCapBody(w, r, contextCapBodyLimit, &input) {
		return
	}
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, contextCapScopeOrg(input.ScopeType, input.ScopeKey, input.OrgID)); !ok {
		return
	}
	if input.Enabled == nil || (input.ResourceType != contextcap.ResourceConnector && input.ResourceType != contextcap.ResourceSkill) {
		writeError(w, http.StatusBadRequest, "resource_type and enabled are required")
		return
	}
	if input.ShareInGroups != nil && input.ResourceType != contextcap.ResourceConnector {
		writeError(w, http.StatusBadRequest, "share_in_groups applies only to personal connectors")
		return
	}
	resourceID, err := util.ParseUUID(strings.TrimSpace(input.ResourceID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource_id")
		return
	}
	grant, ok := h.contextCapRequireScope(w, r, a, userID, input.ScopeType, input.ScopeKey, contextCapNeedToggle)
	if !ok {
		return
	}
	// share_in_groups is the person's own opt-in: it applies to a person
	// scope only, which only the person toggles (a 1:1 chat is a scene).
	if input.ShareInGroups != nil && grant.ScopeType != contextcap.ScopePerson {
		writeError(w, http.StatusBadRequest, "share_in_groups applies only to personal connectors")
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

// contextCapCredentialConnector checks that an org, scene or person
// credential may be stored for connectorID: an enabled connector of the
// agent's workspace that accepts a pasted token (a Bearer connector, or an
// official app that allows a Personal Access Token) and is offered to the
// agent or globally granted to it. A granted connector is a 通用能力, on in
// every scope, and each scope may bring its own account for it. It returns
// the connector's catalog slug ("" for custom connectors), or writes the
// error response and returns false.
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
	if !enabled || !(offered || granted) {
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
// connector in an org, scene or person scope the caller may connect in
// (contextCapRights.Connect). The response carries only the hint and update
// time.
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
		OrgID       string `json:"org_id"`
		ConnectorID string `json:"connector_id"`
		Bearer      string `json:"bearer"`
	}
	if !decodeContextCapBody(w, r, contextCapBodyLimit, &input) {
		return
	}
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, contextCapScopeOrg(input.ScopeType, input.ScopeKey, input.OrgID)); !ok {
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
	grant, ok := h.contextCapRequireScope(w, r, a, userID, input.ScopeType, input.ScopeKey, contextCapNeedCredential)
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
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, contextCapScopeOrg(query.Get("scope_type"), query.Get("scope_key"), query.Get("org_id"))); !ok {
		return
	}
	grant, ok := h.contextCapRequireScope(w, r, a, userID, query.Get("scope_type"), query.Get("scope_key"), contextCapNeedCredential)
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
// DingTalk JSAPI group picker, in the tenant the optional org_id query names
// (default: the agent's identity org). It requires a live person grant for
// the agent in that tenant (a verified DingTalk identity), converts chat_id
// with the corp app, and
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
	// The tenant the page works in (org_id query, default the agent's
	// identity org): the caller's person grant must be in it and the scene
	// grant is stored under it.
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, r.URL.Query().Get("org_id")); !ok {
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
	groupScene, found, err := h.contextCapGroupScene(ctx, a.WorkspaceID, a.ID, a.OrgID, cid)
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
		OrgID: a.OrgID, ScopeKey: groupScene.SceneID, ScopeTitle: agentSceneTitle(groupScene), Source: contextcap.GrantSourceJSAPI,
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

// contextCapGroupScene returns the agent's registered group scene of the
// DingTalk conversation cid in orgID (agent_scene, docs/agent-scene.md);
// found is false when the agent has not served that group there.
func (h *Handler) contextCapGroupScene(ctx context.Context, workspaceID, agentID, orgID, cid string) (contextcap.SceneSummary, bool, error) {
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		return contextcap.SceneSummary{}, false, nil
	}
	agent, err := util.ParseUUID(agentID)
	if err != nil {
		return contextcap.SceneSummary{}, false, nil
	}
	registered, err := scene.Lookup(ctx, h.Queries, scene.Owner{WorkspaceID: ws, AgentID: agent}, scene.DingTalkConversation(orgID, scene.KindGroup, cid))
	switch {
	case errors.Is(err, scene.ErrNotFound), errors.Is(err, scene.ErrKindConflict),
		errors.Is(err, scene.ErrUnresolved), errors.Is(err, scene.ErrInvalidLocator):
		return contextcap.SceneSummary{}, false, nil
	case err != nil:
		return contextcap.SceneSummary{}, false, err
	}
	summary, err := contextcap.GetScene(ctx, h.DB, workspaceID, agentID, orgID, uuidToString(registered.ID))
	if errors.Is(err, contextcap.ErrNotFound) {
		return contextcap.SceneSummary{}, false, nil
	}
	if err != nil {
		return contextcap.SceneSummary{}, false, err
	}
	return summary, true, nil
}

// contextCapSceneTitle returns the display title of the agent's scene
// sceneID in orgID; found is false for an unknown scene.
func (h *Handler) contextCapSceneTitle(ctx context.Context, workspaceID, agentID, orgID, sceneID string) (string, bool, error) {
	summary, err := contextcap.GetScene(ctx, h.DB, workspaceID, agentID, orgID, sceneID)
	if errors.Is(err, contextcap.ErrNotFound) || errors.Is(err, contextcap.ErrInvalidInput) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return agentSceneTitle(summary), true, nil
}

// scopeConnectorIDs is the connectors an org, scene or person scope
// configures: the offered ones (公开给场域), which the scope switches on
// itself, and the agent's granted ones (通用能力), on in every scope, which a
// scope may only give its own account. Callers drop a granted connector that
// is not offered and takes no account (takesScopeAccount) once it is loaded.
func scopeConnectorIDs(offered, granted []string) []string {
	ids := make([]string, 0, len(offered)+len(granted))
	seen := make(map[string]bool, len(offered)+len(granted))
	for _, list := range [][]string{offered, granted} {
		for _, id := range list {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// takesScopeAccount reports whether a scope can bring its own account for c.
func takesScopeAccount(c internalConnector) bool {
	return c.AuthMode == "bearer" || c.AuthMode == "oauth"
}
