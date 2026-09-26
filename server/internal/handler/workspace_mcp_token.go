package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
)

type createWorkspaceMCPTokenRequest struct {
	Name          string   `json:"name"`
	SubjectUserID string   `json:"subject_user_id"`
	ServiceName   string   `json:"service_name"`
	ServiceRole   string   `json:"service_role"`
	Scopes        []string `json:"scopes"`
	ExpiresAt     string   `json:"expires_at"`
}

// RequireWorkspaceMCPHumanIssuer is stricter than the general human gate:
// DTA service credentials may manage DTA configuration, but must never mint
// a workspace MCP identity using their special owner elevation.
func RequireWorkspaceMCPHumanIssuer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Actor-Source") != "" {
			writeError(w, http.StatusForbidden, "workspace MCP credentials require a human issuer")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) CreateWorkspaceMCPToken(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req createWorkspaceMCPTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 120 {
		writeError(w, http.StatusBadRequest, "name must be 1-120 characters")
		return
	}
	if req.ServiceName != "" && req.SubjectUserID != "" {
		writeError(w, http.StatusBadRequest, "choose subject_user_id or service_name")
		return
	}
	if req.ServiceRole != "" && req.ServiceRole != "member" && req.ServiceRole != "admin" {
		writeError(w, http.StatusBadRequest, "service_role must be member or admin")
		return
	}
	if req.ServiceRole == "admin" {
		member, found := middleware.MemberFromContext(r.Context())
		if !found || member.Role != "owner" {
			writeError(w, http.StatusForbidden, "only workspace owners can issue admin service credentials")
			return
		}
	}
	if req.SubjectUserID == "" && req.ServiceName == "" {
		req.SubjectUserID = actorID
	}
	if req.ServiceName == "" && req.SubjectUserID != actorID {
		writeError(w, http.StatusForbidden, "subject_user_id must be the issuing user; use service_name for an external client")
		return
	}
	var subjectID any
	if req.ServiceName == "" {
		if req.ServiceRole != "" {
			writeError(w, http.StatusBadRequest, "service_role requires service_name")
			return
		}
		parsed, valid := parseUUIDOrBadRequest(w, req.SubjectUserID, "subject_user_id")
		if !valid {
			return
		}
		subjectID = parsed
	}
	scopes := make([]string, 0, len(req.Scopes))
	seen := map[string]bool{}
	for _, scope := range req.Scopes {
		if scope != "read" && scope != "write" && scope != "manage" {
			writeError(w, http.StatusBadRequest, "scopes must contain read, write, or manage")
			return
		}
		if !seen[scope] {
			scopes = append(scopes, scope)
			seen[scope] = true
		}
	}
	if len(scopes) == 0 {
		writeError(w, http.StatusBadRequest, "at least one scope is required")
		return
	}
	expires := time.Now().Add(7 * 24 * time.Hour)
	if req.ExpiresAt != "" {
		var err error
		expires, err = time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid expires_at")
			return
		}
	}
	if !expires.After(time.Now()) || expires.After(time.Now().Add(30*24*time.Hour)) {
		writeError(w, http.StatusBadRequest, "expires_at must be within 30 days")
		return
	}
	if req.ServiceName == "" {
		var memberExists bool
		if err := h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM member WHERE workspace_id=$1 AND user_id=$2)`,
			parseUUID(workspaceID), subjectID).Scan(&memberExists); err != nil || !memberExists {
			writeError(w, http.StatusBadRequest, "subject is not a workspace member")
			return
		}
	} else if len(strings.TrimSpace(req.ServiceName)) == 0 || len(req.ServiceName) > 120 {
		writeError(w, http.StatusBadRequest, "service_name must be 1-120 characters")
		return
	}
	baseURL, err := normalizeAgentA2APublicBaseURL(h.currentConfig().PublicURL)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "public URL unavailable")
		return
	}
	secret, err := auth.GenerateWorkspaceMCPToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	var id string
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token creation failed")
		return
	}
	defer tx.Rollback(r.Context())
	if req.ServiceName != "" {
		if req.ServiceRole == "" {
			req.ServiceRole = "member"
		}
		var serviceID string
		err = tx.QueryRow(r.Context(), `INSERT INTO "user" (name,email,principal_type)
			VALUES ($1,'workspace-mcp+' || gen_random_uuid()::text || '@internal.multica.invalid','workspace_mcp_service')
			RETURNING id::text`, strings.TrimSpace(req.ServiceName)).Scan(&serviceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "service member creation failed")
			return
		}
		subjectID = parseUUID(serviceID)
		req.SubjectUserID = serviceID
		_, err = tx.Exec(r.Context(), `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,$3)`,
			parseUUID(workspaceID), subjectID, req.ServiceRole)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "service membership creation failed")
			return
		}
	}
	err = tx.QueryRow(r.Context(), `INSERT INTO workspace_mcp_token
		(workspace_id, subject_user_id, created_by, name, token_hash, token_prefix, scopes, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text`, parseUUID(workspaceID), subjectID,
		parseUUID(actorID), req.Name, auth.HashToken(secret), secret[:13], scopes, expires).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token creation failed")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO workspace_mcp_audit
		(workspace_id,token_id,subject_user_id,actor_user_id,tool_name,result)
		VALUES ($1,$2,$3,$4,'token.created','success')`, parseUUID(workspaceID), parseUUID(id), subjectID, parseUUID(actorID))
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "token creation audit failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "workspace_id": workspaceID, "subject_user_id": req.SubjectUserID,
		"name": req.Name, "scopes": scopes, "expires_at": expires.Format(time.RFC3339),
		"token": secret,
		"url":   baseURL + "/api/mcp/workspaces/" + workspaceID + "/connect/" + secret,
	})
}

func (h *Handler) ListWorkspaceMCPTokens(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	rows, err := h.DB.Query(r.Context(), `SELECT id::text, subject_user_id::text, name, token_prefix,
		scopes, expires_at, revoked_at, last_used_at, created_at FROM workspace_mcp_token
		WHERE workspace_id=$1 ORDER BY created_at DESC`, parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tokens")
		return
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, subject, name, prefix string
		var scopes []string
		var expires, created time.Time
		var revoked, lastUsed *time.Time
		if err := rows.Scan(&id, &subject, &name, &prefix, &scopes, &expires, &revoked, &lastUsed, &created); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list tokens")
			return
		}
		out = append(out, map[string]any{"id": id, "subject_user_id": subject, "name": name,
			"token_prefix": prefix, "scopes": scopes, "expires_at": expires, "revoked_at": revoked,
			"last_used_at": lastUsed, "created_at": created})
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tokens")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) RevokeWorkspaceMCPToken(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	tokenID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "tokenId"), "token_id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke token")
		return
	}
	defer tx.Rollback(r.Context())
	var subjectID string
	err = tx.QueryRow(r.Context(), `UPDATE workspace_mcp_token SET revoked_at=now()
		WHERE id=$1 AND workspace_id=$2 AND revoked_at IS NULL RETURNING subject_user_id::text`, tokenID, parseUUID(workspaceID)).Scan(&subjectID)
	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "token not found or already revoked")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke token")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO workspace_mcp_audit
		(workspace_id,token_id,subject_user_id,actor_user_id,tool_name,result)
		VALUES ($1,$2,$3,$4,'token.revoked','success')`, parseUUID(workspaceID), tokenID, parseUUID(subjectID), parseUUID(actorID))
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "failed to audit token revocation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
