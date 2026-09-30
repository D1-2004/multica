package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type internalConnector struct {
	ID               string   `json:"id"`
	WorkspaceID      string   `json:"workspace_id"`
	Name             string   `json:"name"`
	UpstreamURL      string   `json:"upstream_url"`
	CredentialRef    string   `json:"credential_ref"`
	AuthMode         string   `json:"auth_mode"`
	AllowedTools     []string `json:"allowed_tools"`
	AgentIDs         []string `json:"agent_ids"`
	Enabled          bool     `json:"enabled"`
	CredentialReady  bool     `json:"credential_ready"`
	CredentialSource string   `json:"credential_source"`
	// CredentialOptional reports that the connector may be enabled without a
	// workspace credential because groups and people can bring their own
	// (context capabilities). Until one does, it is not mounted.
	CredentialOptional   bool   `json:"credential_optional"`
	CredentialCiphertext []byte `json:"-"`
	// Official app (catalog) connectors. CatalogSlug is "" for custom
	// connectors; allowed_tools of a catalog connector is server-managed
	// (pinnedCatalogTools over DiscoveredTools and WriteEnabled).
	CatalogSlug         string                    `json:"catalog_slug"`
	WriteEnabled        bool                      `json:"write_enabled"`
	DiscoveredToolCount int                       `json:"discovered_tool_count"`
	CredentialAccount   string                    `json:"credential_account"`
	DiscoveredTools     []discoveredConnectorTool `json:"-"`

	// Task-scoped resolution (authorizedTaskConnectors). Never serialized and
	// never logged: resolvedBearer and resolvedOAuth are upstream secrets.
	bindingLayer    string
	credentialLayer string
	resolvedBearer  string
	resolvedOAuth   *contextcap.OAuthToken
	bearerResolved  bool
	// credentialKey locates a scene or person credential for OAuth refresh.
	credentialKey contextcap.CredentialBinding
}

// setResolvedCredential pins the credential the relay sends upstream for this
// task, so connectorBearer does not fall back to the workspace credential.
func (c *internalConnector) setResolvedCredential(bearer, layer string) {
	c.setResolvedSecret(contextcap.Secret{Bearer: bearer}, layer, contextcap.CredentialBinding{})
}

// setResolvedSecret pins a credential together with its OAuth part and, for
// a scene or person credential, the row that holds it (OAuth refresh
// rewrites that row).
func (c *internalConnector) setResolvedSecret(secret contextcap.Secret, layer string, key contextcap.CredentialBinding) {
	c.resolvedBearer = secret.Bearer
	c.resolvedOAuth = secret.OAuth
	c.credentialLayer = layer
	c.credentialKey = key
	c.bearerResolved = true
}

type connectorInput struct {
	Name         string   `json:"name"`
	UpstreamURL  string   `json:"upstream_url"`
	AuthMode     string   `json:"auth_mode"`
	BearerToken  string   `json:"bearer_token"`
	AutoDiscover bool     `json:"auto_discover"`
	AllowedTools []string `json:"allowed_tools"`
	AgentIDs     []string `json:"agent_ids"`
	Enabled      bool     `json:"enabled"`
	// WriteEnabled toggles write tools of an official app connector on
	// update (nil = unchanged). Rejected for custom connectors.
	WriteEnabled *bool `json:"write_enabled,omitempty"`

	// catalogSlug is set by the server from the stored row, never decoded.
	// A catalog connector must use exactly its catalog MCP URL, may use
	// auth_mode 'oauth' and may have no tools until an account is connected.
	catalogSlug string
}

// internalConnectorSelect is the internal_connector column list read by
// scanInternalConnector, qualified by the alias c.
const internalConnectorSelect = `c.id::text, c.workspace_id::text, c.name, c.upstream_url, c.credential_ref, c.auth_mode, c.allowed_tools, c.enabled,
	c.credential_ciphertext, c.catalog_slug, c.write_enabled, c.discovered_tools`

// scanInternalConnector scans internalConnectorSelect followed by extra
// destinations.
func scanInternalConnector(row pgx.Row, extra ...any) (internalConnector, error) {
	var c internalConnector
	var allowed, discovered []byte
	dest := append([]any{&c.ID, &c.WorkspaceID, &c.Name, &c.UpstreamURL, &c.CredentialRef, &c.AuthMode, &allowed, &c.Enabled,
		&c.CredentialCiphertext, &c.CatalogSlug, &c.WriteEnabled, &discovered}, extra...)
	if err := row.Scan(dest...); err != nil {
		return c, err
	}
	if err := json.Unmarshal(allowed, &c.AllowedTools); err != nil {
		return c, err
	}
	if c.AllowedTools == nil {
		c.AllowedTools = []string{}
	}
	if len(discovered) > 0 && json.Unmarshal(discovered, &c.DiscoveredTools) != nil {
		// A malformed discovery snapshot only hides discovered tools; the
		// pinned allowed_tools stay authoritative.
		c.DiscoveredTools = nil
	}
	c.DiscoveredToolCount = len(c.DiscoveredTools)
	return c, nil
}

func connectorCredentialRef(id string) string {
	return "MULTICA_INTERNAL_MCP_BEARER_" + strings.ToUpper(strings.ReplaceAll(id, "-", ""))
}

func validateConnectorInput(in connectorInput) error {
	if in.AuthMode != "" && in.AuthMode != "none" && in.AuthMode != "bearer" && (in.AuthMode != "oauth" || in.catalogSlug == "") {
		return errors.New("invalid connector auth mode")
	}
	if len(strings.TrimSpace(in.Name)) < 1 || len(in.Name) > 120 {
		return errors.New("name must contain 1-120 characters")
	}
	if in.catalogSlug != "" {
		// An official app connector is pinned to its catalog template URL
		// and has no tools until an account is connected and discovered.
		app, ok := catalogApp(in.catalogSlug)
		if !ok || in.UpstreamURL != app.MCPURL {
			return errors.New("official app connector URL does not match the catalog")
		}
		if len(in.AllowedTools) > maxPinnedConnectorTools {
			return errors.New("allowed_tools must contain at most 64 tools")
		}
	} else {
		if err := validateConnectorURL(in.UpstreamURL); err != nil {
			return err
		}
		if len(in.AllowedTools) == 0 || len(in.AllowedTools) > maxPinnedConnectorTools {
			return errors.New("allowed_tools must contain 1-64 tools")
		}
	}
	// An enabled connector may have no global Agent grant: it can be offered
	// to agents' scene and personal layers only (context capabilities).
	seen := map[string]bool{}
	presentedSeen := map[string]bool{}
	for _, tool := range in.AllowedTools {
		if len(tool) == 0 || len(tool) > 128 || strings.ContainsAny(tool, ",\r\n") || seen[tool] {
			return errors.New("invalid or duplicate allowed tool")
		}
		seen[tool] = true
		presented := connectorPresentedToolName(tool)
		if presentedSeen[presented] {
			return errors.New("allowed tools collide after MCP name shortening")
		}
		presentedSeen[presented] = true
	}
	agentSeen := map[string]bool{}
	for _, id := range in.AgentIDs {
		if _, err := uuid.Parse(id); err != nil {
			return errors.New("invalid agent_id")
		}
		if agentSeen[id] {
			return errors.New("duplicate agent_id")
		}
		agentSeen[id] = true
	}
	return nil
}

func validateConnectorURL(raw string) error {
	// An official app's exact template URL bypasses the deployment host
	// allowlist. Only the server writes it (createCatalogConnector); the
	// custom connector create path refuses it, and upstream_url never
	// changes after creation.
	if connectorURLIsCatalogTemplate(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || (u.Port() != "" && u.Port() != "443") || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return errors.New("upstream_url must be a fixed HTTPS URL")
	}
	host := strings.ToLower(u.Hostname())
	allowed := false
	for _, suffix := range strings.Split(os.Getenv("MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES"), ",") {
		suffix = strings.TrimSpace(strings.ToLower(suffix))
		if suffix != "" && (host == suffix || strings.HasSuffix(host, "."+suffix)) {
			allowed = true
		}
	}
	if !allowed {
		return errors.New("upstream host is not in the deployment allowlist")
	}
	return nil
}

func (h *Handler) ListInternalConnectors(w http.ResponseWriter, r *http.Request) {
	ws := chi.URLParam(r, "id")
	items, err := h.queryInternalConnectors(r.Context(), `SELECT `+internalConnectorSelect+`
		FROM internal_connector c WHERE c.workspace_id = $1::uuid ORDER BY c.updated_at DESC`, ws)
	if err != nil {
		writeError(w, 500, "failed to list connectors")
		return
	}
	for i := range items {
		if err := h.decorateInternalConnectorView(r.Context(), &items[i]); err != nil {
			writeError(w, 500, "failed to list connector grants")
			return
		}
	}
	writeJSON(w, 200, items)
}

// decorateInternalConnectorView fills the admin-facing derived fields of one
// connector: credential readiness, source and account hint, and its global
// agent grants. It never exposes credential material.
func (h *Handler) decorateInternalConnectorView(ctx context.Context, c *internalConnector) error {
	c.CredentialReady = h.connectorCredentialReady(*c)
	c.CredentialSource = h.connectorCredentialSource(*c)
	c.CredentialOptional = c.AuthMode == "bearer" || c.AuthMode == "oauth"
	c.CredentialAccount = h.connectorCredentialAccount(*c)
	c.DiscoveredToolCount = len(c.DiscoveredTools)
	c.AgentIDs = []string{}
	rows, err := h.DB.Query(ctx, `SELECT agent_id::text FROM internal_connector_agent WHERE connector_id=$1::uuid AND workspace_id=$2::uuid ORDER BY agent_id`, c.ID, c.WorkspaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		c.AgentIDs = append(c.AgentIDs, id)
	}
	return rows.Err()
}

func (h *Handler) CreateInternalConnector(w http.ResponseWriter, r *http.Request) {
	h.saveInternalConnector(w, r, true)
}
func (h *Handler) UpdateInternalConnector(w http.ResponseWriter, r *http.Request) {
	h.saveInternalConnector(w, r, false)
}

func (h *Handler) saveInternalConnector(w http.ResponseWriter, r *http.Request, create bool) {
	ws := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, ws, "workspace id")
	if !ok {
		return
	}
	ws = uuidToString(wsUUID)
	var in connectorInput
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil {
		writeError(w, 400, "invalid connector request")
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		writeError(w, 400, "invalid connector request")
		return
	}
	id := chi.URLParam(r, "connectorId")
	var sealed []byte
	if create {
		id = uuid.NewString()
		var err error
		sealed, err = h.prepareInternalConnectorCreate(r.Context(), &in, id, ws)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
	} else {
		if in.AutoDiscover || in.BearerToken != "" {
			writeError(w, 400, "tool discovery and credentials cannot be changed by connector update")
			return
		}
		idUUID, ok := parseUUIDOrBadRequest(w, id, "connector id")
		if !ok {
			return
		}
		id = uuidToString(idUUID)
		slug, err := h.internalConnectorCatalogSlug(r.Context(), ws, id)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "connector not found")
			return
		}
		if err != nil {
			writeError(w, 500, "failed to load connector")
			return
		}
		in.catalogSlug = slug
		if slug == "" && in.WriteEnabled != nil {
			writeError(w, 400, "write_enabled applies to official app connectors only")
			return
		}
		if slug != "" {
			// Server-managed: recomputed from the discovered tools below.
			in.AllowedTools = nil
		}
	}
	if create && in.WriteEnabled != nil {
		writeError(w, 400, "write_enabled applies to official app connectors only")
		return
	}
	if err := validateConnectorInput(in); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	for _, agent := range in.AgentIDs {
		var exists bool
		if err := h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent WHERE id=$1::uuid AND workspace_id=$2::uuid)`, agent, ws).Scan(&exists); err != nil || !exists {
			writeError(w, 400, "agent is not in workspace")
			return
		}
	}
	raw, _ := json.Marshal(in.AllowedTools)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "connector store unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	if create {
		if in.Enabled {
			writeError(w, 400, "create the connector before enabling it")
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO internal_connector (id,workspace_id,name,upstream_url,credential_ref,auth_mode,allowed_tools,enabled,credential_ciphertext)
			VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9)`, id, ws, strings.TrimSpace(in.Name), in.UpstreamURL, connectorCredentialRef(id), in.AuthMode, raw, in.Enabled, sealed)
	} else {
		var existingURL string
		var existingMode string
		var existingSlug string
		var existingWrite bool
		var ciphertext, discoveredRaw []byte
		e := tx.QueryRow(r.Context(), `SELECT upstream_url, auth_mode, credential_ciphertext, catalog_slug, write_enabled, discovered_tools
			FROM internal_connector WHERE id=$1::uuid AND workspace_id=$2::uuid FOR UPDATE`, id, ws).
			Scan(&existingURL, &existingMode, &ciphertext, &existingSlug, &existingWrite, &discoveredRaw)
		if errors.Is(e, pgx.ErrNoRows) {
			writeError(w, 404, "connector not found")
			return
		}
		if e != nil {
			writeError(w, 500, "failed to load connector")
			return
		}
		if in.UpstreamURL != existingURL {
			writeError(w, 400, "upstream_url cannot be changed after creation")
			return
		}
		if in.AuthMode != "" && in.AuthMode != existingMode {
			writeError(w, 400, "connector auth mode cannot be changed after creation")
			return
		}
		if existingSlug != in.catalogSlug {
			writeError(w, 409, "connector changed concurrently; reload and try again")
			return
		}
		writeEnabled := existingWrite
		if existingSlug != "" {
			if in.WriteEnabled != nil {
				writeEnabled = *in.WriteEnabled
			}
			raw, _ = json.Marshal(pinnedCatalogTools(decodeDiscoveredTools(discoveredRaw), writeEnabled))
		}
		// A Bearer connector may be enabled without a shared workspace
		// credential: groups and people then bring their own token
		// (docs/context-capabilities.md), and until a layer supplies one the
		// connector is not mounted (authorizedConnectors and
		// authorizedTaskConnectors both require a usable credential).
		tag, e := tx.Exec(r.Context(), `UPDATE internal_connector SET name=$3,allowed_tools=$4,enabled=$5,write_enabled=$6,updated_at=now()
			WHERE id=$1::uuid AND workspace_id=$2::uuid`, id, ws, strings.TrimSpace(in.Name), raw, in.Enabled, writeEnabled)
		err = e
		if err == nil && tag.RowsAffected() != 1 {
			err = errors.New("connector update affected no rows")
		}
	}
	if err != nil {
		writeError(w, 500, "failed to save connector")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM internal_connector_agent WHERE connector_id=$1::uuid AND workspace_id=$2::uuid`, id, ws); err != nil {
		writeError(w, 500, "failed to update grants")
		return
	}
	for _, agent := range in.AgentIDs {
		if _, err = tx.Exec(r.Context(), `INSERT INTO internal_connector_agent (connector_id,workspace_id,agent_id) VALUES ($1::uuid,$2::uuid,$3::uuid)`, id, ws, agent); err != nil {
			writeError(w, 500, "failed to update grants")
			return
		}
	}
	if tx.Commit(r.Context()) != nil {
		writeError(w, 500, "failed to save connector")
		return
	}
	writeJSON(w, 200, map[string]string{"id": id, "credential_ref": connectorCredentialRef(id)})
}

// authorizedConnectors returns the agent's globally granted, enabled
// connectors whose workspace credential is ready. It knows nothing about a
// task's scene or trigger person; task execution paths (claim injection and
// the relay) use authorizedTaskConnectors instead.
func (h *Handler) authorizedConnectors(ctx context.Context, workspaceID, agentID string) ([]internalConnector, error) {
	granted, err := h.grantedConnectors(ctx, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	out := []internalConnector{}
	for _, c := range granted {
		// Official app connectors count only once their tools are known.
		if len(c.AllowedTools) > 0 && h.connectorCredentialReady(c) {
			out = append(out, c)
		}
	}
	return out, nil
}

// ListAvailableInternalConnectors exposes only usable connector/Agent pairs to
// members. Upstream addresses and credential references remain admin-only.
// catalog_slug names the official app ("" for Aone FaaS connectors).
func (h *Handler) ListAvailableInternalConnectors(w http.ResponseWriter, r *http.Request) {
	items := []map[string]any{}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace_id")
	if !ok {
		return
	}
	ws := uuidToString(workspaceID)
	rows, err := h.DB.Query(r.Context(), `SELECT `+internalConnectorSelect+`, g.agent_id::text
		FROM internal_connector c JOIN internal_connector_agent g ON g.connector_id=c.id AND g.workspace_id=c.workspace_id
		WHERE c.workspace_id=$1::uuid AND c.enabled ORDER BY c.name,g.agent_id`, ws)
	if err != nil {
		writeError(w, 500, "connector list unavailable")
		return
	}
	defer rows.Close()
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, ws)
	for rows.Next() {
		var agentID string
		c, err := scanInternalConnector(rows, &agentID)
		if err != nil {
			writeError(w, 500, "connector list unavailable")
			return
		}
		if !h.connectorCredentialReady(c) {
			continue
		}
		if validateConnectorInput(connectorInput{Name: c.Name, UpstreamURL: c.UpstreamURL, AllowedTools: c.AllowedTools, AgentIDs: []string{agentID}, Enabled: true}) != nil {
			continue
		}
		parsedAgent, err := util.ParseUUID(agentID)
		if err != nil {
			continue
		}
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: parsedAgent, WorkspaceID: workspaceID})
		if err != nil || agent.ArchivedAt.Valid || !agent.RuntimeID.Valid {
			continue
		}
		if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, ws) || !h.canInvokeAgent(r.Context(), agent, actorType, actorID, userID, ws) {
			continue
		}
		items = append(items, map[string]any{"id": c.ID, "name": c.Name, "server_name": connectorServerName(c.ID), "agent_id": agentID, "agent_name": agent.Name, "tools": c.AllowedTools,
			"catalog_slug": c.CatalogSlug})
	}
	if rows.Err() != nil {
		writeError(w, 500, "connector list unavailable")
		return
	}
	writeJSON(w, http.StatusOK, items)
}
