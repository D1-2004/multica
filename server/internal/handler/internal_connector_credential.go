package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// PutInternalConnectorCredential accepts a new Bearer only from a human
// workspace owner/admin. The route has both guards; the value is write-only.
func (h *Handler) PutInternalConnectorCredential(w http.ResponseWriter, r *http.Request) {
	if !h.internalConnectorsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	if h.InternalConnectorSecretBox == nil {
		writeError(w, http.StatusServiceUnavailable, "connector credential storage is not configured")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	ws, id := chi.URLParam(r, "id"), chi.URLParam(r, "connectorId")
	wsUUID, ok := parseUUIDOrBadRequest(w, ws, "workspace id")
	if !ok {
		return
	}
	idUUID, ok := parseUUIDOrBadRequest(w, id, "connector id")
	if !ok {
		return
	}
	ws, id = uuidToString(wsUUID), uuidToString(idUUID)
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var input struct {
		BearerToken string `json:"bearer_token"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || len(input.BearerToken) == 0 || len(input.BearerToken) > 4096 ||
		strings.TrimSpace(input.BearerToken) != input.BearerToken || strings.ContainsAny(input.BearerToken, "\r\n\x00") {
		writeError(w, 400, "invalid connector credential")
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		writeError(w, 400, "invalid connector credential")
		return
	}
	payload, err := json.Marshal(connectorSealedCredential{WorkspaceID: ws, ConnectorID: id, Bearer: input.BearerToken})
	if err != nil {
		writeError(w, 500, "connector credential could not be saved")
		return
	}
	sealed, err := h.InternalConnectorSecretBox.Seal(payload)
	if err != nil {
		writeError(w, 500, "connector credential could not be saved")
		return
	}
	tag, err := h.DB.Exec(r.Context(), `UPDATE internal_connector SET credential_ciphertext=$3,updated_at=now()
		WHERE id=$1::uuid AND workspace_id=$2::uuid`, id, ws, sealed)
	if err != nil {
		writeError(w, 500, "connector credential could not be saved")
		return
	}
	if tag.RowsAffected() != 1 {
		writeError(w, 404, "connector not found")
		return
	}
	slog.InfoContext(r.Context(), "internal connector credential rotated", "connector_id", id, "workspace_id", ws, "actor_id", requestUserID(r))
	writeJSON(w, 200, map[string]any{"credential_ready": true, "credential_source": "workspace"})
}

// TestInternalConnector checks connectivity without invoking any upstream tool.
// The response contains only allowlisted tool names and a generic failure.
func (h *Handler) TestInternalConnector(w http.ResponseWriter, r *http.Request) {
	if !h.internalConnectorsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	ws, id := chi.URLParam(r, "id"), chi.URLParam(r, "connectorId")
	wsUUID, ok := parseUUIDOrBadRequest(w, ws, "workspace id")
	if !ok {
		return
	}
	idUUID, ok := parseUUIDOrBadRequest(w, id, "connector id")
	if !ok {
		return
	}
	ws, id = uuidToString(wsUUID), uuidToString(idUUID)
	var c internalConnector
	var raw []byte
	err := h.DB.QueryRow(r.Context(), `SELECT id::text,workspace_id::text,name,upstream_url,credential_ref,allowed_tools,enabled,credential_ciphertext
		FROM internal_connector WHERE id=$1::uuid AND workspace_id=$2::uuid`, id, ws).
		Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.UpstreamURL, &c.CredentialRef, &raw, &c.Enabled, &c.CredentialCiphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "connector not found")
		return
	}
	if err != nil || json.Unmarshal(raw, &c.AllowedTools) != nil {
		writeError(w, 500, "connector configuration unavailable")
		return
	}
	if err := validateConnectorInput(connectorInput{Name: c.Name, UpstreamURL: c.UpstreamURL, AllowedTools: c.AllowedTools}); err != nil {
		writeError(w, 503, "connector configuration unavailable")
		return
	}
	if !h.connectorCredentialReady(c) {
		writeJSON(w, 200, map[string]any{"reachable": false, "message": "credential is not configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	started := time.Now()
	cursor := ""
	seenCursors := map[string]bool{}
	tools := map[string]bool{}
	more := false
	for page := 0; page < 8; page++ {
		result, callErr := h.callInternalConnectorUpstream(ctx, c, "tools/list", connectorRPCParams{Cursor: cursor})
		if callErr != nil {
			slog.InfoContext(r.Context(), "internal connector test failed", "connector_id", id, "workspace_id", ws, "duration_ms", time.Since(started).Milliseconds())
			writeJSON(w, 200, map[string]any{"reachable": false, "message": "upstream unavailable, invalid, or rejected the credential"})
			return
		}
		list, ok := result.(map[string]any)
		if !ok {
			writeJSON(w, 200, map[string]any{"reachable": false, "message": "invalid upstream tool list"})
			return
		}
		entries, ok := list["tools"].([]map[string]any)
		if !ok {
			writeJSON(w, 200, map[string]any{"reachable": false, "message": "invalid upstream tool list"})
			return
		}
		for _, item := range entries {
			if name, ok := item["name"].(string); ok {
				tools[name] = true
			}
		}
		next, _ := list["nextCursor"].(string)
		if next == "" {
			more = false
			break
		}
		if seenCursors[next] {
			writeJSON(w, 200, map[string]any{"reachable": false, "message": "invalid upstream pagination"})
			return
		}
		seenCursors[next] = true
		cursor, more = next, true
	}
	found := []string{}
	for _, name := range c.AllowedTools {
		if tools[name] {
			found = append(found, name)
		}
	}
	slog.InfoContext(r.Context(), "internal connector test completed", "connector_id", id, "workspace_id", ws, "duration_ms", time.Since(started).Milliseconds(), "found_tools", len(found))
	writeJSON(w, 200, map[string]any{"reachable": true, "tools": found, "has_more": more, "duration_ms": time.Since(started).Milliseconds()})
}
