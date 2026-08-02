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

type WorkspaceAccessGrantResponse struct {
	ID            string   `json:"id"`
	WorkspaceID   string   `json:"workspace_id"`
	Name          string   `json:"name"`
	Capabilities  []string `json:"capabilities"`
	ResourceScope string   `json:"resource_scope"`
	Status        string   `json:"status"`
	Version       int32    `json:"version"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	DisabledAt    *string  `json:"disabled_at"`
}

type WorkspaceAccessTokenResponse struct {
	ID         string  `json:"id"`
	GrantID    string  `json:"grant_id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"token_prefix"`
	ExpiresAt  *string `json:"expires_at"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
	RevokedAt  *string `json:"revoked_at"`
}

type CreateWorkspaceAccessTokenResponse struct {
	WorkspaceAccessTokenResponse
	Token string `json:"token"`
}

func workspaceAccessGrantToResponse(grant db.WorkspaceAccessGrant) WorkspaceAccessGrantResponse {
	return WorkspaceAccessGrantResponse{
		ID:            uuidToString(grant.ID),
		WorkspaceID:   uuidToString(grant.WorkspaceID),
		Name:          grant.Name,
		Capabilities:  grant.Capabilities,
		ResourceScope: grant.ResourceScope,
		Status:        grant.Status,
		Version:       grant.Version,
		CreatedAt:     timestampToString(grant.CreatedAt),
		UpdatedAt:     timestampToString(grant.UpdatedAt),
		DisabledAt:    timestampToPtr(grant.DisabledAt),
	}
}

func workspaceAccessTokenToResponse(token db.WorkspaceAccessToken) WorkspaceAccessTokenResponse {
	return WorkspaceAccessTokenResponse{
		ID:         uuidToString(token.ID),
		GrantID:    uuidToString(token.GrantID),
		Name:       token.Name,
		Prefix:     token.TokenPrefix,
		ExpiresAt:  timestampToPtr(token.ExpiresAt),
		LastUsedAt: timestampToPtr(token.LastUsedAt),
		CreatedAt:  timestampToString(token.CreatedAt),
		RevokedAt:  timestampToPtr(token.RevokedAt),
	}
}

func validateWorkspaceAccessPolicy(name string, capabilities []string, resourceScope string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	if resourceScope != "own_agents" && resourceScope != "workspace" {
		return errors.New("resource_scope must be own_agents or workspace")
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

type createWorkspaceAccessGrantRequest struct {
	Name          string   `json:"name"`
	Capabilities  []string `json:"capabilities"`
	ResourceScope string   `json:"resource_scope"`
}

func (h *Handler) CreateWorkspaceAccessGrant(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req createWorkspaceAccessGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateWorkspaceAccessPolicy(req.Name, req.Capabilities, req.ResourceScope); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.TxStarter == nil {
		writeError(w, http.StatusInternalServerError, "database transactions unavailable")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create access grant")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := db.New(tx)
	subject, err := qtx.CreateWorkspaceAccessGrantSubject(r.Context(), "DTA access: "+strings.TrimSpace(req.Name))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create access grant subject")
		return
	}
	grant, err := qtx.CreateWorkspaceAccessGrant(r.Context(), db.CreateWorkspaceAccessGrantParams{
		WorkspaceID:   parseUUID(workspaceID),
		SubjectUserID: subject.ID,
		Name:          strings.TrimSpace(req.Name),
		Capabilities:  req.Capabilities,
		ResourceScope: req.ResourceScope,
		ActorUserID:   parseUUID(actorID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create access grant")
		return
	}
	if err := createWorkspaceAccessAudit(r, qtx, workspaceID, grant.ID, pgtype.UUID{}, actorID, "grant.created", "grant", grant.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to audit access grant")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create access grant")
		return
	}
	writeJSON(w, http.StatusCreated, workspaceAccessGrantToResponse(grant))
}

func (h *Handler) ListWorkspaceAccessGrants(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	grants, err := h.Queries.ListWorkspaceAccessGrants(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list access grants")
		return
	}
	response := make([]WorkspaceAccessGrantResponse, len(grants))
	for i, grant := range grants {
		response[i] = workspaceAccessGrantToResponse(grant)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) GetWorkspaceAccessGrant(w http.ResponseWriter, r *http.Request) {
	grant, ok := h.loadWorkspaceAccessGrant(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, workspaceAccessGrantToResponse(grant))
}

type updateWorkspaceAccessGrantRequest struct {
	Name          string   `json:"name"`
	Capabilities  []string `json:"capabilities"`
	ResourceScope string   `json:"resource_scope"`
	Version       int32    `json:"version"`
}

func (h *Handler) UpdateWorkspaceAccessGrant(w http.ResponseWriter, r *http.Request) {
	current, ok := h.loadWorkspaceAccessGrant(w, r)
	if !ok {
		return
	}
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req updateWorkspaceAccessGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateWorkspaceAccessPolicy(req.Name, req.Capabilities, req.ResourceScope); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var updated db.WorkspaceAccessGrant
	err := h.runWorkspaceAccessTransaction(r.Context(), func(qtx *db.Queries) error {
		var updateErr error
		updated, updateErr = qtx.UpdateWorkspaceAccessGrant(r.Context(), db.UpdateWorkspaceAccessGrantParams{
			Name:          strings.TrimSpace(req.Name),
			Capabilities:  req.Capabilities,
			ResourceScope: req.ResourceScope,
			ActorUserID:   parseUUID(actorID),
			ID:            current.ID,
			WorkspaceID:   current.WorkspaceID,
			Version:       req.Version,
		})
		if updateErr != nil {
			return updateErr
		}
		return createWorkspaceAccessAudit(r, qtx, uuidToString(current.WorkspaceID), current.ID, pgtype.UUID{}, actorID, "grant.updated", "grant", current.ID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "access grant was modified by another request")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update access grant")
		return
	}
	writeJSON(w, http.StatusOK, workspaceAccessGrantToResponse(updated))
}

func (h *Handler) DisableWorkspaceAccessGrant(w http.ResponseWriter, r *http.Request) {
	h.setWorkspaceAccessGrantStatus(w, r, "disabled")
}

func (h *Handler) EnableWorkspaceAccessGrant(w http.ResponseWriter, r *http.Request) {
	h.setWorkspaceAccessGrantStatus(w, r, "active")
}

func (h *Handler) setWorkspaceAccessGrantStatus(w http.ResponseWriter, r *http.Request, status string) {
	current, ok := h.loadWorkspaceAccessGrant(w, r)
	if !ok {
		return
	}
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	action := "grant.enabled"
	if status == "disabled" {
		action = "grant.disabled"
	}
	var updated db.WorkspaceAccessGrant
	err := h.runWorkspaceAccessTransaction(r.Context(), func(qtx *db.Queries) error {
		var updateErr error
		updated, updateErr = qtx.SetWorkspaceAccessGrantStatus(r.Context(), db.SetWorkspaceAccessGrantStatusParams{
			Status: status, ActorUserID: parseUUID(actorID), ID: current.ID, WorkspaceID: current.WorkspaceID,
		})
		if updateErr != nil {
			return updateErr
		}
		return createWorkspaceAccessAudit(r, qtx, uuidToString(current.WorkspaceID), current.ID, pgtype.UUID{}, actorID, action, "grant", current.ID)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update access grant status")
		return
	}
	writeJSON(w, http.StatusOK, workspaceAccessGrantToResponse(updated))
}

type createWorkspaceAccessTokenRequest struct {
	Name      string  `json:"name"`
	ExpiresAt *string `json:"expires_at"`
}

func (h *Handler) CreateWorkspaceAccessToken(w http.ResponseWriter, r *http.Request) {
	grant, ok := h.loadWorkspaceAccessGrant(w, r)
	if !ok {
		return
	}
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req createWorkspaceAccessTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
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
	prefix := rawToken
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	var token db.WorkspaceAccessToken
	err = h.runWorkspaceAccessTransaction(r.Context(), func(qtx *db.Queries) error {
		var createErr error
		token, createErr = qtx.CreateWorkspaceAccessToken(r.Context(), db.CreateWorkspaceAccessTokenParams{
			GrantID: grant.ID, Name: strings.TrimSpace(req.Name), TokenHash: auth.HashToken(rawToken),
			TokenPrefix: prefix, ExpiresAt: expiresAt, ActorUserID: parseUUID(actorID),
		})
		if createErr != nil {
			return createErr
		}
		return createWorkspaceAccessAudit(r, qtx, uuidToString(grant.WorkspaceID), grant.ID, token.ID, actorID, "token.created", "token", token.ID)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create access token")
		return
	}
	writeJSON(w, http.StatusCreated, CreateWorkspaceAccessTokenResponse{
		WorkspaceAccessTokenResponse: workspaceAccessTokenToResponse(token),
		Token:                        rawToken,
	})
}

func (h *Handler) ListWorkspaceAccessTokens(w http.ResponseWriter, r *http.Request) {
	grant, ok := h.loadWorkspaceAccessGrant(w, r)
	if !ok {
		return
	}
	tokens, err := h.Queries.ListWorkspaceAccessTokens(r.Context(), db.ListWorkspaceAccessTokensParams{
		GrantID:     grant.ID,
		WorkspaceID: grant.WorkspaceID,
	})
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

func (h *Handler) UpdateWorkspaceAccessToken(w http.ResponseWriter, r *http.Request) {
	grant, ok := h.loadWorkspaceAccessGrant(w, r)
	if !ok {
		return
	}
	tokenID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "tokenId"), "token id")
	if !ok {
		return
	}
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.ExpiresAt) == 0 {
		writeError(w, http.StatusBadRequest, "expires_at is required")
		return
	}
	var expiryValue *string
	if string(req.ExpiresAt) != "null" {
		var value string
		if err := json.Unmarshal(req.ExpiresAt, &value); err != nil {
			writeError(w, http.StatusBadRequest, "expires_at must be an RFC3339 timestamp or null")
			return
		}
		expiryValue = &value
	}
	expiresAt, ok := parseWorkspaceAccessExpiry(w, expiryValue)
	if !ok {
		return
	}
	var token db.WorkspaceAccessToken
	err := h.runWorkspaceAccessTransaction(r.Context(), func(qtx *db.Queries) error {
		var updateErr error
		token, updateErr = qtx.UpdateWorkspaceAccessTokenExpiry(r.Context(), db.UpdateWorkspaceAccessTokenExpiryParams{
			ExpiresAt: expiresAt, ID: tokenID, GrantID: grant.ID, WorkspaceID: grant.WorkspaceID,
		})
		if updateErr != nil {
			return updateErr
		}
		return createWorkspaceAccessAudit(r, qtx, uuidToString(grant.WorkspaceID), grant.ID, token.ID, actorID, "token.expiry_updated", "token", token.ID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "access token not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update access token")
		return
	}
	writeJSON(w, http.StatusOK, workspaceAccessTokenToResponse(token))
}

func (h *Handler) RevokeWorkspaceAccessToken(w http.ResponseWriter, r *http.Request) {
	grant, ok := h.loadWorkspaceAccessGrant(w, r)
	if !ok {
		return
	}
	tokenID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "tokenId"), "token id")
	if !ok {
		return
	}
	actorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	err := h.runWorkspaceAccessTransaction(r.Context(), func(qtx *db.Queries) error {
		token, revokeErr := qtx.RevokeWorkspaceAccessToken(r.Context(), db.RevokeWorkspaceAccessTokenParams{
			ActorUserID: parseUUID(actorID), ID: tokenID, GrantID: grant.ID, WorkspaceID: grant.WorkspaceID,
		})
		if errors.Is(revokeErr, pgx.ErrNoRows) {
			return nil
		}
		if revokeErr != nil {
			return revokeErr
		}
		return createWorkspaceAccessAudit(r, qtx, uuidToString(grant.WorkspaceID), grant.ID, token.ID, actorID, "token.revoked", "token", token.ID)
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
		writeError(w, http.StatusForbidden, "grant_operation_not_allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"principal_type": "workspace_access_grant",
		"grant_id":       principal.GrantID,
		"name":           principal.Name,
		"workspace_id":   principal.WorkspaceID,
		"capabilities":   principal.Capabilities,
		"resource_scope": principal.ResourceScope,
		"status":         "active",
		"version":        principal.Version,
	})
}

func (h *Handler) loadWorkspaceAccessGrant(w http.ResponseWriter, r *http.Request) (db.WorkspaceAccessGrant, bool) {
	workspaceID := workspaceIDFromURL(r, "id")
	grantID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "grantId"), "grant id")
	if !ok {
		return db.WorkspaceAccessGrant{}, false
	}
	grant, err := h.Queries.GetWorkspaceAccessGrant(r.Context(), db.GetWorkspaceAccessGrantParams{
		ID:          grantID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "access grant not found")
		return db.WorkspaceAccessGrant{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load access grant")
		return db.WorkspaceAccessGrant{}, false
	}
	return grant, true
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

func createWorkspaceAccessAudit(r *http.Request, queries workspaceAccessAuditCreator, workspaceID string, grantID, tokenID pgtype.UUID, actorUserID, action, resourceType string, resourceID pgtype.UUID) error {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	_, err := queries.CreateWorkspaceAccessAudit(r.Context(), db.CreateWorkspaceAccessAuditParams{
		WorkspaceID:  parseUUID(workspaceID),
		GrantID:      grantID,
		TokenID:      tokenID,
		ActorUserID:  parseUUID(actorUserID),
		Action:       action,
		ResourceType: pgtype.Text{String: resourceType, Valid: resourceType != ""},
		ResourceID:   resourceID,
		Result:       "success",
		RequestID:    pgtype.Text{String: requestID, Valid: requestID != ""},
	})
	return err
}
