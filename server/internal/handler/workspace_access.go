package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var workspaceAccessCapabilities = map[string]struct{}{
	"deployment.manage": {},
	"deployment.retire": {},
	"trace.read":        {},
}

type WorkspaceAccessTokenResponse struct {
	ID           string   `json:"id"`
	WorkspaceID  string   `json:"workspace_id"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
	Version      int32    `json:"version"`
	Prefix       string   `json:"token_prefix"`
	ExpiresAt    *string  `json:"expires_at"`
	LastUsedAt   *string  `json:"last_used_at"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	RevokedAt    *string  `json:"revoked_at"`
}

type WorkspaceAccessTokenSecretResponse struct {
	WorkspaceAccessTokenResponse
	Token string `json:"token"`
}

func workspaceAccessTokenToResponse(token db.WorkspaceAccessToken) WorkspaceAccessTokenResponse {
	return WorkspaceAccessTokenResponse{
		ID:           uuidToString(token.ID),
		WorkspaceID:  uuidToString(token.WorkspaceID),
		Name:         token.Name,
		Capabilities: token.Capabilities,
		Version:      token.Version,
		Prefix:       token.TokenPrefix,
		ExpiresAt:    timestampToPtr(token.ExpiresAt),
		LastUsedAt:   timestampToPtr(token.LastUsedAt),
		CreatedAt:    timestampToString(token.CreatedAt),
		UpdatedAt:    timestampToString(token.UpdatedAt),
		RevokedAt:    timestampToPtr(token.RevokedAt),
	}
}

func validateWorkspaceAccessPolicy(name string, capabilities []string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	if len(capabilities) == 0 {
		return errors.New("at least one capability is required")
	}
	seen := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if _, ok := workspaceAccessCapabilities[capability]; !ok {
			return errors.New("unsupported capability")
		}
		if _, duplicate := seen[capability]; duplicate {
			return errors.New("capabilities must not contain duplicates")
		}
		seen[capability] = struct{}{}
	}
	return nil
}

type createWorkspaceAccessTokenRequest struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
	ExpiresAt    *string  `json:"expires_at"`
}

func (h *Handler) CreateWorkspaceAccessToken(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req createWorkspaceAccessTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateWorkspaceAccessPolicy(req.Name, req.Capabilities); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	expiresAt, ok := parseWorkspaceAccessExpiry(w, req.ExpiresAt)
	if !ok {
		return
	}
	rawToken, err := auth.GenerateWorkspaceAccessToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate access token")
		return
	}

	var token db.WorkspaceAccessToken
	err = h.runWorkspaceAccessTransaction(r.Context(), func(qtx *db.Queries) error {
		subject, createErr := qtx.CreateWorkspaceAccessTokenSubject(r.Context(), "DTA access: "+strings.TrimSpace(req.Name))
		if createErr != nil {
			return createErr
		}
		token, createErr = qtx.CreateWorkspaceAccessToken(r.Context(), db.CreateWorkspaceAccessTokenParams{
			WorkspaceID:   parseUUID(workspaceID),
			SubjectUserID: subject.ID,
			Name:          strings.TrimSpace(req.Name),
			TokenHash:     auth.HashToken(rawToken),
			TokenPrefix:   workspaceAccessTokenPrefix(rawToken),
			Capabilities:  req.Capabilities,
			ExpiresAt:     expiresAt,
			ActorUserID:   parseUUID(actorID),
		})
		if createErr != nil {
			return createErr
		}
		return createWorkspaceAccessAudit(r, qtx, token.WorkspaceID, token.ID, parseUUID(actorID), "token.created", "token", token.ID)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create access token")
		return
	}
	writeJSON(w, http.StatusCreated, WorkspaceAccessTokenSecretResponse{
		WorkspaceAccessTokenResponse: workspaceAccessTokenToResponse(token),
		Token:                        rawToken,
	})
}

func (h *Handler) ListWorkspaceAccessTokens(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	tokens, err := h.Queries.ListWorkspaceAccessTokens(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list access tokens")
		return
	}
	response := make([]WorkspaceAccessTokenResponse, len(tokens))
	for i, token := range tokens {
		response[i] = workspaceAccessTokenToResponse(token)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) GetWorkspaceAccessToken(w http.ResponseWriter, r *http.Request) {
	token, ok := h.loadWorkspaceAccessToken(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, workspaceAccessTokenToResponse(token))
}

type updateWorkspaceAccessTokenRequest struct {
	Name         string          `json:"name"`
	Capabilities []string        `json:"capabilities"`
	ExpiresAt    json.RawMessage `json:"expires_at"`
	Version      int32           `json:"version"`
}

func (h *Handler) UpdateWorkspaceAccessToken(w http.ResponseWriter, r *http.Request) {
	current, ok := h.loadWorkspaceAccessToken(w, r)
	if !ok {
		return
	}
	if current.RevokedAt.Valid {
		writeError(w, http.StatusConflict, "access token is revoked")
		return
	}
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req updateWorkspaceAccessTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateWorkspaceAccessPolicy(req.Name, req.Capabilities); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	expiresAt, ok := parseRequiredWorkspaceAccessExpiry(w, req.ExpiresAt)
	if !ok {
		return
	}
	var updated db.WorkspaceAccessToken
	err := h.runWorkspaceAccessTransaction(r.Context(), func(qtx *db.Queries) error {
		var updateErr error
		updated, updateErr = qtx.UpdateWorkspaceAccessToken(r.Context(), db.UpdateWorkspaceAccessTokenParams{
			Name:         strings.TrimSpace(req.Name),
			Capabilities: req.Capabilities,
			ExpiresAt:    expiresAt,
			ActorUserID:  parseUUID(actorID),
			ID:           current.ID,
			WorkspaceID:  current.WorkspaceID,
			Version:      req.Version,
		})
		if updateErr != nil {
			return updateErr
		}
		return createWorkspaceAccessAudit(r, qtx, current.WorkspaceID, current.ID, parseUUID(actorID), "token.updated", "token", current.ID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "access token was modified by another request")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update access token")
		return
	}
	writeJSON(w, http.StatusOK, workspaceAccessTokenToResponse(updated))
}

type regenerateWorkspaceAccessTokenRequest struct {
	ExpiresAt json.RawMessage `json:"expires_at"`
	Version   int32           `json:"version"`
}

func (h *Handler) RegenerateWorkspaceAccessToken(w http.ResponseWriter, r *http.Request) {
	current, ok := h.loadWorkspaceAccessToken(w, r)
	if !ok {
		return
	}
	if current.RevokedAt.Valid {
		writeError(w, http.StatusConflict, "access token is revoked")
		return
	}
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req regenerateWorkspaceAccessTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	expiresAt, ok := parseRequiredWorkspaceAccessExpiry(w, req.ExpiresAt)
	if !ok {
		return
	}
	rawToken, err := auth.GenerateWorkspaceAccessToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate access token")
		return
	}
	var updated db.WorkspaceAccessToken
	err = h.runWorkspaceAccessTransaction(r.Context(), func(qtx *db.Queries) error {
		var updateErr error
		updated, updateErr = qtx.RegenerateWorkspaceAccessToken(r.Context(), db.RegenerateWorkspaceAccessTokenParams{
			TokenHash:   auth.HashToken(rawToken),
			TokenPrefix: workspaceAccessTokenPrefix(rawToken),
			ExpiresAt:   expiresAt,
			ActorUserID: parseUUID(actorID),
			ID:          current.ID,
			WorkspaceID: current.WorkspaceID,
			Version:     req.Version,
		})
		if updateErr != nil {
			return updateErr
		}
		return createWorkspaceAccessAudit(r, qtx, current.WorkspaceID, current.ID, parseUUID(actorID), "token.regenerated", "token", current.ID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "access token was modified by another request")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to regenerate access token")
		return
	}
	writeJSON(w, http.StatusOK, WorkspaceAccessTokenSecretResponse{
		WorkspaceAccessTokenResponse: workspaceAccessTokenToResponse(updated),
		Token:                        rawToken,
	})
}

func (h *Handler) RevokeWorkspaceAccessToken(w http.ResponseWriter, r *http.Request) {
	current, ok := h.loadWorkspaceAccessToken(w, r)
	if !ok {
		return
	}
	if current.RevokedAt.Valid {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	err := h.runWorkspaceAccessTransaction(r.Context(), func(qtx *db.Queries) error {
		token, revokeErr := qtx.RevokeWorkspaceAccessToken(r.Context(), db.RevokeWorkspaceAccessTokenParams{
			ActorUserID: parseUUID(actorID),
			ID:          current.ID,
			WorkspaceID: current.WorkspaceID,
		})
		if errors.Is(revokeErr, pgx.ErrNoRows) {
			return nil
		}
		if revokeErr != nil {
			return revokeErr
		}
		return createWorkspaceAccessAudit(r, qtx, current.WorkspaceID, current.ID, parseUUID(actorID), "token.revoked", "token", token.ID)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke access token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) GetWorkspaceAccessSelf(w http.ResponseWriter, r *http.Request) {
	principal, ok := middleware.WorkspaceAccessPrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusForbidden, "workspace_access_operation_not_allowed")
		return
	}
	workspace, err := h.Queries.GetWorkspace(r.Context(), parseUUID(principal.WorkspaceID))
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"principal_type": "workspace_access_token",
		"token_id":       principal.TokenID,
		"name":           principal.Name,
		"workspace_id":   principal.WorkspaceID,
		"workspace": map[string]any{
			"id":           uuidToString(workspace.ID),
			"name":         workspace.Name,
			"slug":         workspace.Slug,
			"issue_prefix": workspace.IssuePrefix,
		},
		"capabilities": principal.Capabilities,
		"version":      principal.Version,
	})
}

func (h *Handler) loadWorkspaceAccessToken(w http.ResponseWriter, r *http.Request) (db.WorkspaceAccessToken, bool) {
	workspaceID := workspaceIDFromURL(r, "id")
	tokenID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "tokenId"), "token id")
	if !ok {
		return db.WorkspaceAccessToken{}, false
	}
	token, err := h.Queries.GetWorkspaceAccessToken(r.Context(), db.GetWorkspaceAccessTokenParams{
		ID: tokenID, WorkspaceID: parseUUID(workspaceID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "access token not found")
		return db.WorkspaceAccessToken{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load access token")
		return db.WorkspaceAccessToken{}, false
	}
	return token, true
}

func workspaceAccessTokenPrefix(rawToken string) string {
	if len(rawToken) > 12 {
		return rawToken[:12]
	}
	return rawToken
}

func parseRequiredWorkspaceAccessExpiry(w http.ResponseWriter, raw json.RawMessage) (pgtype.Timestamptz, bool) {
	if len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "expires_at is required")
		return pgtype.Timestamptz{}, false
	}
	if string(raw) == "null" {
		return pgtype.Timestamptz{}, true
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		writeError(w, http.StatusBadRequest, "expires_at must be an RFC3339 timestamp or null")
		return pgtype.Timestamptz{}, false
	}
	return parseWorkspaceAccessExpiry(w, &value)
}

func parseWorkspaceAccessExpiry(w http.ResponseWriter, value *string) (pgtype.Timestamptz, bool) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return pgtype.Timestamptz{}, true
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "expires_at must be an RFC3339 timestamp or null")
		return pgtype.Timestamptz{}, false
	}
	if !parsed.After(time.Now()) {
		writeError(w, http.StatusBadRequest, "expires_at must be in the future")
		return pgtype.Timestamptz{}, false
	}
	return pgtype.Timestamptz{Time: parsed, Valid: true}, true
}

type workspaceAccessAuditCreator interface {
	CreateWorkspaceAccessAudit(context.Context, db.CreateWorkspaceAccessAuditParams) (db.WorkspaceAccessAudit, error)
}

func (h *Handler) runWorkspaceAccessTransaction(ctx context.Context, fn func(*db.Queries) error) error {
	if h.TxStarter == nil {
		return errors.New("database transactions unavailable")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(h.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func createWorkspaceAccessAudit(r *http.Request, queries workspaceAccessAuditCreator, workspaceID, tokenID, actorUserID pgtype.UUID, action, resourceType string, resourceID pgtype.UUID) error {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	_, err := queries.CreateWorkspaceAccessAudit(r.Context(), db.CreateWorkspaceAccessAuditParams{
		WorkspaceID:  workspaceID,
		TokenID:      tokenID,
		ActorUserID:  actorUserID,
		Action:       action,
		ResourceType: pgtype.Text{String: resourceType, Valid: resourceType != ""},
		ResourceID:   resourceID,
		Result:       "success",
		RequestID:    pgtype.Text{String: requestID, Valid: requestID != ""},
	})
	return err
}
