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
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) internalConnectorsEnabled(ctx context.Context) bool {
	return featureflags.InternalMCPConnectorsEnabled(ctx, h.FeatureFlags)
}

type internalConnector struct {
	ID                   string   `json:"id"`
	WorkspaceID          string   `json:"workspace_id"`
	Name                 string   `json:"name"`
	UpstreamURL          string   `json:"upstream_url"`
	CredentialRef        string   `json:"credential_ref"`
	AuthMode             string   `json:"auth_mode"`
	AllowedTools         []string `json:"allowed_tools"`
	AgentIDs             []string `json:"agent_ids"`
	Enabled              bool     `json:"enabled"`
	CredentialReady      bool     `json:"credential_ready"`
	CredentialSource     string   `json:"credential_source"`
	CredentialCiphertext []byte   `json:"-"`
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
}

func connectorCredentialRef(id string) string {
	return "MULTICA_INTERNAL_MCP_BEARER_" + strings.ToUpper(strings.ReplaceAll(id, "-", ""))
}

func validateConnectorInput(in connectorInput) error {
	if in.AuthMode != "" && in.AuthMode != "none" && in.AuthMode != "bearer" {
		return errors.New("invalid connector auth mode")
	}
	if len(strings.TrimSpace(in.Name)) < 1 || len(in.Name) > 120 {
		return errors.New("name must contain 1-120 characters")
	}
	if err := validateConnectorURL(in.UpstreamURL); err != nil {
		return err
	}
	if len(in.AllowedTools) == 0 || len(in.AllowedTools) > 64 {
		return errors.New("allowed_tools must contain 1-64 tools")
	}
	if in.Enabled && len(in.AgentIDs) == 0 {
		return errors.New("enabled connector requires an Agent grant")
	}
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
	if !h.internalConnectorsEnabled(r.Context()) {
		writeJSON(w, http.StatusOK, []internalConnector{})
		return
	}
	ws := chi.URLParam(r, "id")
	rows, err := h.DB.Query(r.Context(), `SELECT id::text, workspace_id::text, name, upstream_url, credential_ref, auth_mode, allowed_tools, enabled, credential_ciphertext
		FROM internal_connector WHERE workspace_id = $1::uuid ORDER BY updated_at DESC`, ws)
	if err != nil {
		writeError(w, 500, "failed to list connectors")
		return
	}
	defer rows.Close()
	items := []internalConnector{}
	for rows.Next() {
		var c internalConnector
		var raw []byte
		if rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.UpstreamURL, &c.CredentialRef, &c.AuthMode, &raw, &c.Enabled, &c.CredentialCiphertext) != nil {
			writeError(w, 500, "failed to scan connectors")
			return
		}
		if err := json.Unmarshal(raw, &c.AllowedTools); err != nil {
			writeError(w, 500, "invalid connector tool configuration")
			return
		}
		c.CredentialReady = h.connectorCredentialReady(c)
		c.CredentialSource = h.connectorCredentialSource(c)
		c.AgentIDs = []string{}
		agentRows, e := h.DB.Query(r.Context(), `SELECT agent_id::text FROM internal_connector_agent WHERE connector_id=$1::uuid AND workspace_id=$2::uuid`, c.ID, ws)
		if e != nil {
			writeError(w, 500, "failed to list connector grants")
			return
		}
		for agentRows.Next() {
			var id string
			if err := agentRows.Scan(&id); err != nil {
				agentRows.Close()
				writeError(w, 500, "failed to scan connector grants")
				return
			}
			c.AgentIDs = append(c.AgentIDs, id)
		}
		if err := agentRows.Err(); err != nil {
			agentRows.Close()
			writeError(w, 500, "failed to list connector grants")
			return
		}
		agentRows.Close()
		items = append(items, c)
	}
	if rows.Err() != nil {
		writeError(w, 500, "failed to list connectors")
		return
	}
	writeJSON(w, 200, items)
}

func (h *Handler) CreateInternalConnector(w http.ResponseWriter, r *http.Request) {
	if !h.internalConnectorsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	h.saveInternalConnector(w, r, true)
}
func (h *Handler) UpdateInternalConnector(w http.ResponseWriter, r *http.Request) {
	if !h.internalConnectorsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
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
		var ciphertext []byte
		e := tx.QueryRow(r.Context(), `SELECT upstream_url, auth_mode, credential_ciphertext FROM internal_connector WHERE id=$1::uuid AND workspace_id=$2::uuid FOR UPDATE`, id, ws).Scan(&existingURL, &existingMode, &ciphertext)
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
		if in.Enabled && !h.connectorCredentialReady(internalConnector{ID: id, WorkspaceID: ws, CredentialRef: connectorCredentialRef(id), AuthMode: existingMode, CredentialCiphertext: ciphertext}) {
			writeError(w, 400, "connector credential is not configured")
			return
		}
		tag, e := tx.Exec(r.Context(), `UPDATE internal_connector SET name=$3,allowed_tools=$4,enabled=$5,updated_at=now()
			WHERE id=$1::uuid AND workspace_id=$2::uuid`, id, ws, strings.TrimSpace(in.Name), raw, in.Enabled)
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

func (h *Handler) authorizedConnectors(ctx context.Context, workspaceID, agentID string) ([]internalConnector, error) {
	if !h.internalConnectorsEnabled(ctx) {
		return nil, nil
	}
	rows, err := h.DB.Query(ctx, `SELECT c.id::text,c.workspace_id::text,c.name,c.upstream_url,c.credential_ref,c.auth_mode,c.allowed_tools,c.enabled,c.credential_ciphertext
		FROM internal_connector c JOIN internal_connector_agent g ON g.connector_id=c.id AND g.workspace_id=c.workspace_id
		WHERE c.workspace_id=$1::uuid AND g.agent_id=$2::uuid AND c.enabled`, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []internalConnector{}
	for rows.Next() {
		var c internalConnector
		var raw []byte
		if err = rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.UpstreamURL, &c.CredentialRef, &c.AuthMode, &raw, &c.Enabled, &c.CredentialCiphertext); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &c.AllowedTools); err != nil {
			return nil, err
		}
		if h.connectorCredentialReady(c) {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// ListAvailableInternalConnectors exposes only usable connector/Agent pairs to
// members. Upstream addresses and credential references remain admin-only.
func (h *Handler) ListAvailableInternalConnectors(w http.ResponseWriter, r *http.Request) {
	items := []map[string]any{}
	if !h.internalConnectorsEnabled(r.Context()) {
		writeJSON(w, http.StatusOK, items)
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace_id")
	if !ok {
		return
	}
	ws := uuidToString(workspaceID)
	rows, err := h.DB.Query(r.Context(), `SELECT c.id::text,c.name,c.upstream_url,c.credential_ref,c.auth_mode,c.allowed_tools,g.agent_id::text,c.credential_ciphertext
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
		var c internalConnector
		var agentID string
		var raw []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.UpstreamURL, &c.CredentialRef, &c.AuthMode, &raw, &agentID, &c.CredentialCiphertext); err != nil {
			writeError(w, 500, "connector list unavailable")
			return
		}
		c.WorkspaceID = ws
		if json.Unmarshal(raw, &c.AllowedTools) != nil || !h.connectorCredentialReady(c) {
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
		items = append(items, map[string]any{"id": c.ID, "name": c.Name, "server_name": connectorServerName(c.ID), "agent_id": agentID, "agent_name": agent.Name, "tools": c.AllowedTools})
	}
	if rows.Err() != nil {
		writeError(w, 500, "connector list unavailable")
		return
	}
	writeJSON(w, http.StatusOK, items)
}
