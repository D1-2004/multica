package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
)

func withWorkspaceAccessParams(req *http.Request, params ...string) *http.Request {
	rctx := chi.NewRouteContext()
	for i := 0; i+1 < len(params); i += 2 {
		rctx.URLParams.Add(params[i], params[i+1])
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestWorkspaceAccessTokenLifecycleCreatesStableServiceMember(t *testing.T) {
	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	createReq := withWorkspaceAccessParams(newRequest(http.MethodPost, "/access-tokens", map[string]any{
		"name":       "Vendor user A",
		"expires_at": expiresAt,
	}), "id", testWorkspaceID)
	createRec := httptest.NewRecorder()
	testHandler.CreateWorkspaceAccessToken(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	if strings.Contains(createRec.Body.String(), "capabilities") || strings.Contains(createRec.Body.String(), "resource_scope") {
		t.Fatalf("create response exposed removed policy fields: %s", createRec.Body.String())
	}
	var created WorkspaceAccessTokenSecretResponse
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if !strings.HasPrefix(created.Token, "dta_") || created.Version != 1 {
		t.Fatalf("created token = %+v", created)
	}

	var subjectID, memberID, principalType, storedHash, memberRole string
	if err := testPool.QueryRow(context.Background(), `
		SELECT t.subject_user_id::text, m.id::text, u.principal_type, t.token_hash, m.role
		FROM workspace_access_token t
		JOIN "user" u ON u.id = t.subject_user_id
		JOIN member m ON m.workspace_id = t.workspace_id AND m.user_id = t.subject_user_id
		WHERE t.id = $1
	`, created.ID).Scan(&subjectID, &memberID, &principalType, &storedHash, &memberRole); err != nil {
		t.Fatalf("load service member: %v", err)
	}
	if principalType != middleware.WorkspaceAccessActorSource || memberRole != "member" {
		t.Fatalf("principal_type=%q role=%q", principalType, memberRole)
	}
	if storedHash != auth.HashToken(created.Token) || strings.Contains(storedHash, created.Token) {
		t.Fatal("database must store only the token hash")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace_access_token WHERE id = $1`, created.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, subjectID)
	})

	updateReq := withWorkspaceAccessParams(newRequest(http.MethodPatch, "/access-tokens/"+created.ID, map[string]any{
		"name":       "Vendor user A renamed",
		"expires_at": nil,
		"version":    created.Version,
	}), "id", testWorkspaceID, "tokenId", created.ID)
	updateRec := httptest.NewRecorder()
	testHandler.UpdateWorkspaceAccessToken(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updateRec.Code, updateRec.Body.String())
	}
	var updated WorkspaceAccessTokenResponse
	if err := json.NewDecoder(updateRec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updated.Version != 2 || updated.Name != "Vendor user A renamed" {
		t.Fatalf("updated token = %+v", updated)
	}

	newExpiry := time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	regenerateReq := withWorkspaceAccessParams(newRequest(http.MethodPost, "/access-tokens/"+created.ID+"/regenerate", map[string]any{
		"expires_at": newExpiry,
		"version":    updated.Version,
	}), "id", testWorkspaceID, "tokenId", created.ID)
	regenerateRec := httptest.NewRecorder()
	testHandler.RegenerateWorkspaceAccessToken(regenerateRec, regenerateReq)
	if regenerateRec.Code != http.StatusOK {
		t.Fatalf("regenerate status=%d body=%s", regenerateRec.Code, regenerateRec.Body.String())
	}
	var regenerated WorkspaceAccessTokenSecretResponse
	if err := json.NewDecoder(regenerateRec.Body).Decode(&regenerated); err != nil {
		t.Fatalf("decode regenerate: %v", err)
	}
	if regenerated.Token == created.Token || regenerated.ID != created.ID || regenerated.Version != 3 {
		t.Fatalf("regenerated token = %+v", regenerated)
	}
	var subjectAfter, memberRoleAfter string
	if err := testPool.QueryRow(context.Background(), `
		SELECT t.subject_user_id::text, m.role
		FROM workspace_access_token t
		JOIN member m ON m.workspace_id = t.workspace_id AND m.user_id = t.subject_user_id
		WHERE t.id = $1
	`, created.ID).Scan(&subjectAfter, &memberRoleAfter); err != nil {
		t.Fatalf("load service member after regenerate: %v", err)
	}
	if subjectAfter != subjectID || memberRoleAfter != "member" {
		t.Fatalf("regenerate changed service member: subject=%s role=%s", subjectAfter, memberRoleAfter)
	}

	authStatus := func(rawToken string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/workspace-access/self", nil)
		req.Header.Set("Authorization", "Bearer "+rawToken)
		rec := httptest.NewRecorder()
		middleware.Auth(testHandler.Queries, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(rec, req)
		return rec.Code
	}
	if got := authStatus(created.Token); got != http.StatusUnauthorized {
		t.Fatalf("old token status=%d, want 401", got)
	}
	if got := authStatus(regenerated.Token); got != http.StatusNoContent {
		t.Fatalf("new token status=%d, want 204", got)
	}

	activeDeleteReq := withWorkspaceAccessParams(newRequest(http.MethodDelete, "/access-tokens/"+created.ID, nil), "id", testWorkspaceID, "tokenId", created.ID)
	activeDeleteRec := httptest.NewRecorder()
	testHandler.DeleteWorkspaceAccessToken(activeDeleteRec, activeDeleteReq)
	if activeDeleteRec.Code != http.StatusConflict {
		t.Fatalf("active delete status=%d body=%s", activeDeleteRec.Code, activeDeleteRec.Body.String())
	}

	revokeReq := withWorkspaceAccessParams(newRequest(http.MethodPost, "/access-tokens/"+created.ID+"/revoke", nil), "id", testWorkspaceID, "tokenId", created.ID)
	revokeRec := httptest.NewRecorder()
	testHandler.RevokeWorkspaceAccessToken(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revokeRec.Code, revokeRec.Body.String())
	}
	if got := authStatus(regenerated.Token); got != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d, want 401", got)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT role FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, subjectID).Scan(&memberRoleAfter); err != nil || memberRoleAfter != "member" {
		t.Fatalf("revoke must retain service membership: role=%q err=%v", memberRoleAfter, err)
	}

	deleteReq := withWorkspaceAccessParams(newRequest(http.MethodDelete, "/access-tokens/"+created.ID, nil), "id", testWorkspaceID, "tokenId", created.ID)
	deleteRec := httptest.NewRecorder()
	testHandler.DeleteWorkspaceAccessToken(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	var tokenCount int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM workspace_access_token WHERE id = $1`, created.ID).Scan(&tokenCount); err != nil || tokenCount != 0 {
		t.Fatalf("deleted token count=%d err=%v", tokenCount, err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT role FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, subjectID).Scan(&memberRoleAfter); err != nil || memberRoleAfter != "member" {
		t.Fatalf("delete must retain service membership: role=%q err=%v", memberRoleAfter, err)
	}
	var auditTokenID *string
	if err := testPool.QueryRow(context.Background(), `
		SELECT token_id::text
		FROM workspace_access_audit
		WHERE action = 'token.deleted' AND resource_id = $1
		ORDER BY created_at DESC LIMIT 1
	`, created.ID).Scan(&auditTokenID); err != nil || auditTokenID != nil {
		t.Fatalf("delete audit token_id=%v err=%v, want retained audit with null FK", auditTokenID, err)
	}

	removeMemberReq := withWorkspaceAccessParams(newRequest(http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/members/"+memberID, nil), "id", testWorkspaceID, "memberId", memberID)
	removeMemberRec := httptest.NewRecorder()
	testHandler.DeleteMember(removeMemberRec, removeMemberReq)
	if removeMemberRec.Code != http.StatusNoContent {
		t.Fatalf("remove orphan service member status=%d body=%s", removeMemberRec.Code, removeMemberRec.Body.String())
	}
	var memberCount int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM member WHERE id = $1`, memberID).Scan(&memberCount); err != nil || memberCount != 0 {
		t.Fatalf("orphan service member count=%d err=%v", memberCount, err)
	}
}

func TestGetWorkspaceAccessSelfIncludesBoundWorkspaceWithoutCapabilities(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/workspace-access/self", nil)
	req = req.WithContext(middleware.WithWorkspaceAccessPrincipal(req.Context(), middleware.WorkspaceAccessPrincipal{
		TokenID:     "11111111-1111-4111-8111-111111111111",
		UserID:      testUserID,
		WorkspaceID: testWorkspaceID,
		Name:        "Vendor A",
		Version:     1,
	}))
	rec := httptest.NewRecorder()
	testHandler.GetWorkspaceAccessSelf(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "capabilities") {
		t.Fatalf("self response exposed removed capabilities: %s", rec.Body.String())
	}
	var got struct {
		PrincipalType string `json:"principal_type"`
		WorkspaceID   string `json:"workspace_id"`
		Workspace     struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"workspace"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode self: %v", err)
	}
	if got.PrincipalType != middleware.WorkspaceAccessActorSource || got.WorkspaceID != testWorkspaceID || got.Workspace.ID != testWorkspaceID || got.Workspace.Name == "" || got.Workspace.Slug == "" {
		t.Fatalf("self response = %+v", got)
	}
}

func TestWorkspaceAccessTokenRejectsMissingName(t *testing.T) {
	req := withWorkspaceAccessParams(newRequest(http.MethodPost, "/access-tokens", map[string]any{
		"name": " ", "expires_at": nil,
	}), "id", testWorkspaceID)
	rec := httptest.NewRecorder()
	testHandler.CreateWorkspaceAccessToken(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
