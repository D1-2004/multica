package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

// PutInternalConnectorCredential accepts a new Bearer only from a human
// workspace owner/admin. The route has both guards; the value is write-only.
func (h *Handler) PutInternalConnectorCredential(w http.ResponseWriter, r *http.Request) {
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
	var authMode, catalogSlug string
	if err := h.DB.QueryRow(r.Context(), `SELECT auth_mode, catalog_slug FROM internal_connector WHERE id=$1::uuid AND workspace_id=$2::uuid`, id, ws).Scan(&authMode, &catalogSlug); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "connector not found")
		return
	} else if err != nil {
		writeError(w, 500, "connector configuration unavailable")
		return
	}
	// Bearer connectors, and official apps that accept a Personal Access
	// Token (GitHub) as the workspace's shared account.
	if !connectorAcceptsBearer(authMode, catalogSlug) {
		writeError(w, 400, "connector does not use a Bearer credential")
		return
	}
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
	response := map[string]any{"credential_ready": true, "credential_source": "workspace"}
	if catalogSlug != "" {
		// First credential of an official app: pin its tools now. Failure is
		// not fatal; the admin can refresh tools later.
		result, _, err := h.discoverCatalogToolsIfEmpty(r.Context(), ws, id, contextcap.Secret{Bearer: input.BearerToken}, connectorCredentialWorkspace, contextcap.CredentialBinding{})
		if err != nil {
			slog.WarnContext(r.Context(), "official app first tool discovery failed", "connector_id", id, "error", err)
		}
		response["discovered_tool_count"] = result.Discovered
	}
	writeJSON(w, 200, response)
}

// DeleteInternalConnectorCredential removes the workspace's stored credential
// of a connector: for an official app it disconnects the shared account
// (所有人共用). Human workspace owner/admin only (same guards as PUT). It is
// idempotent and answers 204; 404 for an unknown connector. The provider
// grant of an OAuth account is not revoked, and an operator-managed
// environment credential (MULTICA_INTERNAL_MCP_BEARER_<id>) is not affected.
// An OAuth refresh holds the row lock while it reseals, so a disconnect
// either waits for it or makes it answer "reconnect required"; it cannot be
// undone by a concurrent refresh.
func (h *Handler) DeleteInternalConnectorCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ws, ok := catalogWorkspaceID(w, r)
	if !ok {
		return
	}
	id, ok := catalogConnectorID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tag, err := h.DB.Exec(ctx, `UPDATE internal_connector SET credential_ciphertext = NULL, updated_at = now()
		WHERE id = $1::uuid AND workspace_id = $2::uuid AND credential_ciphertext IS NOT NULL`, id, ws)
	if err != nil {
		slog.ErrorContext(ctx, "internal connector credential removal failed", "connector_id", id, "workspace_id", ws, "error", err)
		writeError(w, http.StatusInternalServerError, "connector credential could not be removed")
		return
	}
	if tag.RowsAffected() == 0 {
		// Nothing was stored, or the connector does not exist.
		if _, err := h.internalConnectorCatalogSlug(ctx, ws, id); errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "connector not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "connector configuration unavailable")
			return
		}
	} else {
		slog.InfoContext(ctx, "internal connector credential removed", "connector_id", id, "workspace_id", ws, "actor_id", requestUserID(r))
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestInternalConnector checks connectivity without invoking any upstream tool.
// The response contains discovered tool names and a sanitized failure category.
func (h *Handler) TestInternalConnector(w http.ResponseWriter, r *http.Request) {
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
	c, err := h.loadInternalConnector(r.Context(), ws, id)
	if errors.Is(err, errConnectorNotFound) {
		writeError(w, 404, "connector not found")
		return
	}
	if err != nil {
		writeError(w, 500, "connector configuration unavailable")
		return
	}
	if err := validateConnectorInput(connectorInput{Name: c.Name, UpstreamURL: c.UpstreamURL, AuthMode: c.AuthMode, AllowedTools: c.AllowedTools, catalogSlug: c.CatalogSlug}); err != nil {
		writeError(w, 503, "connector configuration unavailable")
		return
	}
	if !h.connectorCredentialReady(c) {
		writeJSON(w, 200, map[string]any{"reachable": false, "message": "credential is not configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	started := time.Now()
	cursor := ""
	seenCursors := map[string]bool{}
	tools := map[string]bool{}
	more := false
	attempts := 0
	for page := 0; page < 8; page++ {
		result, pageAttempts, callErr := h.connectorToolListWithRetry(ctx, c, cursor)
		attempts += pageAttempts
		if callErr != nil {
			slog.InfoContext(r.Context(), "internal connector test failed", "connector_id", id, "workspace_id", ws, "duration_ms", time.Since(started).Milliseconds(), "attempts", attempts, "failure_class", connectorTestFailureMessage(callErr))
			writeJSON(w, 200, map[string]any{"reachable": false, "ready": false, "message": connectorTestFailureMessage(callErr)})
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
	found := make([]string, 0, len(tools))
	for name := range tools {
		found = append(found, name)
	}
	sort.Strings(found)
	missing := []string{}
	ready := true
	slog.InfoContext(r.Context(), "internal connector test completed", "connector_id", id, "workspace_id", ws, "duration_ms", time.Since(started).Milliseconds(), "attempts", attempts, "found_tools", len(found), "missing_tools", len(missing))
	writeJSON(w, 200, map[string]any{"reachable": true, "ready": ready, "tools": found, "missing_tools": missing, "has_more": more, "duration_ms": time.Since(started).Milliseconds()})
}

func connectorTestFailureMessage(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "upstream request timed out"
	}
	var status connectorUpstreamStatusError
	if errors.As(err, &status) {
		if status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden {
			return fmt.Sprintf("upstream rejected credential (HTTP %d)", status.Code)
		}
		return fmt.Sprintf("upstream returned HTTP %d", status.Code)
	}
	var protocol connectorUpstreamProtocolError
	if errors.As(err, &protocol) {
		return "upstream returned an invalid MCP response"
	}
	return "upstream network connection failed"
}
