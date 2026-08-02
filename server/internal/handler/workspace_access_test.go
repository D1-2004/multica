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
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func withWorkspaceAccessParams(req *http.Request, params ...string) *http.Request {
	rctx := chi.NewRouteContext()
	for i := 0; i+1 < len(params); i += 2 {
		rctx.URLParams.Add(params[i], params[i+1])
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestWorkspaceAccessTokenLifecycleAndRegeneration(t *testing.T) {
	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	createReq := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/access-tokens", map[string]any{
		"name":           "Vendor user A",
		"capabilities":   []string{"deployment.manage", "trace.read"},
		"resource_scope": "own_agents",
		"expires_at":     expiresAt,
	})
	createReq = withWorkspaceAccessParams(createReq, "id", testWorkspaceID)
	createRec := httptest.NewRecorder()
	testHandler.CreateWorkspaceAccessToken(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("CreateWorkspaceAccessToken status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created WorkspaceAccessTokenSecretResponse
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if !strings.HasPrefix(created.Token, "dta_") || created.Version != 1 || created.ResourceScope != "own_agents" {
		t.Fatalf("unexpected created token: %+v", created)
	}

	var subjectID, principalType, storedHash string
	if err := testPool.QueryRow(context.Background(), `
		SELECT t.subject_user_id::text, u.principal_type, t.token_hash
		FROM workspace_access_token t
		JOIN "user" u ON u.id = t.subject_user_id
		WHERE t.id = $1
	`, created.ID).Scan(&subjectID, &principalType, &storedHash); err != nil {
		t.Fatalf("load token subject: %v", err)
	}
	if principalType != "workspace_access_token" {
		t.Fatalf("principal_type = %q", principalType)
	}
	if storedHash != auth.HashToken(created.Token) || strings.Contains(storedHash, created.Token) {
		t.Fatal("database must store only the token hash")
	}
	var memberCount int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM member WHERE user_id = $1`, subjectID).Scan(&memberCount); err != nil {
		t.Fatalf("count subject memberships: %v", err)
	}
	if memberCount != 0 {
		t.Fatalf("token subject has %d member rows, want 0", memberCount)
	}

	var ownedAgentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 'private', 1, $4)
		RETURNING id
	`, testWorkspaceID, "regenerate-owner-"+created.ID, testRuntimeID, subjectID).Scan(&ownedAgentID); err != nil {
		t.Fatalf("create owned agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, ownedAgentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace_access_token WHERE id = $1`, created.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, subjectID)
	})

	listReq := withWorkspaceAccessParams(newRequest(http.MethodGet, "/access-tokens", nil), "id", testWorkspaceID)
	listRec := httptest.NewRecorder()
	testHandler.ListWorkspaceAccessTokens(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("ListWorkspaceAccessTokens status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	if strings.Contains(listRec.Body.String(), created.Token) || strings.Contains(listRec.Body.String(), storedHash) {
		t.Fatal("token list leaked plaintext or hash")
	}

	updateReq := newRequest(http.MethodPatch, "/access-tokens/"+created.ID, map[string]any{
		"name":           "Vendor user A",
		"capabilities":   []string{"trace.read"},
		"resource_scope": "workspace",
		"expires_at":     nil,
		"version":        created.Version,
	})
	updateReq = withWorkspaceAccessParams(updateReq, "id", testWorkspaceID, "tokenId", created.ID)
	updateRec := httptest.NewRecorder()
	testHandler.UpdateWorkspaceAccessToken(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("UpdateWorkspaceAccessToken status = %d, body = %s", updateRec.Code, updateRec.Body.String())
	}
	var updated WorkspaceAccessTokenResponse
	if err := json.NewDecoder(updateRec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated token: %v", err)
	}
	if updated.Version != 2 || updated.ResourceScope != "workspace" || len(updated.Capabilities) != 1 || updated.Capabilities[0] != "trace.read" {
		t.Fatalf("unexpected updated token: %+v", updated)
	}

	newExpiry := time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	regenerateReq := newRequest(http.MethodPost, "/access-tokens/"+created.ID+"/regenerate", map[string]any{
		"expires_at": newExpiry,
		"version":    updated.Version,
	})
	regenerateReq = withWorkspaceAccessParams(regenerateReq, "id", testWorkspaceID, "tokenId", created.ID)
	regenerateRec := httptest.NewRecorder()
	testHandler.RegenerateWorkspaceAccessToken(regenerateRec, regenerateReq)
	if regenerateRec.Code != http.StatusOK {
		t.Fatalf("RegenerateWorkspaceAccessToken status = %d, body = %s", regenerateRec.Code, regenerateRec.Body.String())
	}
	var regenerated WorkspaceAccessTokenSecretResponse
	if err := json.NewDecoder(regenerateRec.Body).Decode(&regenerated); err != nil {
		t.Fatalf("decode regenerated token: %v", err)
	}
	if regenerated.Token == created.Token || regenerated.ID != created.ID || regenerated.Version != 3 {
		t.Fatalf("unexpected regenerated token: %+v", regenerated)
	}
	var subjectAfter, agentOwnerAfter string
	if err := testPool.QueryRow(context.Background(), `SELECT subject_user_id::text FROM workspace_access_token WHERE id = $1`, created.ID).Scan(&subjectAfter); err != nil {
		t.Fatalf("load subject after regenerate: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT owner_id::text FROM agent WHERE id = $1`, ownedAgentID).Scan(&agentOwnerAfter); err != nil {
		t.Fatalf("load agent owner after regenerate: %v", err)
	}
	if subjectAfter != subjectID || agentOwnerAfter != subjectID {
		t.Fatalf("regenerate changed ownership: subject=%s agent_owner=%s want=%s", subjectAfter, agentOwnerAfter, subjectID)
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
		t.Fatalf("old token after regenerate status=%d, want 401", got)
	}
	if got := authStatus(regenerated.Token); got != http.StatusNoContent {
		t.Fatalf("new token after regenerate status=%d, want 204", got)
	}

	staleReq := newRequest(http.MethodPost, "/access-tokens/"+created.ID+"/regenerate", map[string]any{
		"expires_at": newExpiry,
		"version":    updated.Version,
	})
	staleReq = withWorkspaceAccessParams(staleReq, "id", testWorkspaceID, "tokenId", created.ID)
	staleRec := httptest.NewRecorder()
	testHandler.RegenerateWorkspaceAccessToken(staleRec, staleReq)
	if staleRec.Code != http.StatusConflict {
		t.Fatalf("stale regenerate status=%d body=%s", staleRec.Code, staleRec.Body.String())
	}

	revokeReq := withWorkspaceAccessParams(newRequest(http.MethodDelete, "/access-tokens/"+created.ID, nil), "id", testWorkspaceID, "tokenId", created.ID)
	revokeRec := httptest.NewRecorder()
	testHandler.RevokeWorkspaceAccessToken(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusNoContent {
		t.Fatalf("RevokeWorkspaceAccessToken status = %d, body = %s", revokeRec.Code, revokeRec.Body.String())
	}
	if got := authStatus(regenerated.Token); got != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d, want 401", got)
	}
	revokedRegenerateReq := newRequest(http.MethodPost, "/access-tokens/"+created.ID+"/regenerate", map[string]any{
		"expires_at": newExpiry,
		"version":    regenerated.Version,
	})
	revokedRegenerateReq = withWorkspaceAccessParams(revokedRegenerateReq, "id", testWorkspaceID, "tokenId", created.ID)
	revokedRegenerateRec := httptest.NewRecorder()
	testHandler.RegenerateWorkspaceAccessToken(revokedRegenerateRec, revokedRegenerateReq)
	if revokedRegenerateRec.Code != http.StatusConflict {
		t.Fatalf("revoked regenerate status=%d body=%s", revokedRegenerateRec.Code, revokedRegenerateRec.Body.String())
	}
}

func TestGetWorkspaceAccessSelfIncludesBoundWorkspace(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/workspace-access/self", nil)
	req = req.WithContext(middleware.WithWorkspaceAccessPrincipal(req.Context(), middleware.WorkspaceAccessPrincipal{
		TokenID:       "11111111-1111-1111-1111-111111111111",
		UserID:        testUserID,
		WorkspaceID:   testWorkspaceID,
		Name:          "Vendor A",
		Capabilities:  []string{"deployment.manage"},
		ResourceScope: "own_agents",
		Version:       1,
	}))
	rec := httptest.NewRecorder()
	testHandler.GetWorkspaceAccessSelf(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
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
	if got.PrincipalType != "workspace_access_token" || got.WorkspaceID != testWorkspaceID || got.Workspace.ID != testWorkspaceID || got.Workspace.Name == "" || got.Workspace.Slug == "" {
		t.Fatalf("self response = %+v", got)
	}
}

func TestWorkspaceAccessTokenRejectsInvalidPolicy(t *testing.T) {
	req := newRequest(http.MethodPost, "/access-tokens", map[string]any{
		"name": "Vendor", "capabilities": []string{"members.manage"}, "resource_scope": "workspace",
	})
	req = withWorkspaceAccessParams(req, "id", testWorkspaceID)
	rec := httptest.NewRecorder()
	testHandler.CreateWorkspaceAccessToken(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceAccessTokensHaveIndependentDynamicPolicies(t *testing.T) {
	create := func(name string, capabilities []string) WorkspaceAccessTokenSecretResponse {
		t.Helper()
		req := newRequest(http.MethodPost, "/access-tokens", map[string]any{
			"name": name, "capabilities": capabilities, "resource_scope": "own_agents", "expires_at": nil,
		})
		req = withWorkspaceAccessParams(req, "id", testWorkspaceID)
		rec := httptest.NewRecorder()
		testHandler.CreateWorkspaceAccessToken(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s status=%d body=%s", name, rec.Code, rec.Body.String())
		}
		var token WorkspaceAccessTokenSecretResponse
		if err := json.NewDecoder(rec.Body).Decode(&token); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		return token
	}
	traceToken := create("Trace operator", []string{"trace.read"})
	manageToken := create("Deployment operator", []string{"deployment.manage"})
	var traceSubject, manageSubject string
	if err := testPool.QueryRow(context.Background(), `SELECT subject_user_id::text FROM workspace_access_token WHERE id = $1`, traceToken.ID).Scan(&traceSubject); err != nil {
		t.Fatalf("load trace subject: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT subject_user_id::text FROM workspace_access_token WHERE id = $1`, manageToken.ID).Scan(&manageSubject); err != nil {
		t.Fatalf("load manage subject: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace_access_token WHERE id = ANY($1::uuid[])`, []string{traceToken.ID, manageToken.ID})
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = ANY($1::uuid[])`, []string{traceSubject, manageSubject})
	})
	if traceSubject == manageSubject {
		t.Fatal("different external operators must have different internal subjects")
	}

	requestStatus := func(rawToken string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
		req.Header.Set("Authorization", "Bearer "+rawToken)
		rec := httptest.NewRecorder()
		middleware.Auth(testHandler.Queries, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(rec, req)
		return rec.Code
	}
	if got := requestStatus(traceToken.Token); got != http.StatusForbidden {
		t.Fatalf("trace-only token GET /api/agents status=%d, want 403", got)
	}
	if got := requestStatus(manageToken.Token); got != http.StatusNoContent {
		t.Fatalf("manage token GET /api/agents status=%d, want 204", got)
	}

	updateReq := newRequest(http.MethodPatch, "/access-tokens/"+traceToken.ID, map[string]any{
		"name": "Trace operator", "capabilities": []string{"deployment.manage", "trace.read"},
		"resource_scope": "own_agents", "expires_at": nil, "version": traceToken.Version,
	})
	updateReq = withWorkspaceAccessParams(updateReq, "id", testWorkspaceID, "tokenId", traceToken.ID)
	updateRec := httptest.NewRecorder()
	testHandler.UpdateWorkspaceAccessToken(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update trace token status=%d body=%s", updateRec.Code, updateRec.Body.String())
	}
	if got := requestStatus(traceToken.Token); got != http.StatusNoContent {
		t.Fatalf("updated token GET /api/agents status=%d, want 204", got)
	}
}

func TestWorkspaceAccessAgentScopeMatrix(t *testing.T) {
	subjectID := "11111111-1111-4111-8111-111111111111"
	workspaceID := "22222222-2222-4222-8222-222222222222"
	otherWorkspaceID := "33333333-3333-4333-8333-333333333333"
	otherOwnerID := "44444444-4444-4444-8444-444444444444"
	tests := []struct {
		name    string
		scope   string
		agent   db.Agent
		allowed bool
	}{
		{"own agent", "own_agents", db.Agent{WorkspaceID: parseUUID(workspaceID), OwnerID: parseUUID(subjectID)}, true},
		{"same workspace other owner", "own_agents", db.Agent{WorkspaceID: parseUUID(workspaceID), OwnerID: parseUUID(otherOwnerID)}, false},
		{"workspace scope other owner", "workspace", db.Agent{WorkspaceID: parseUUID(workspaceID), OwnerID: parseUUID(otherOwnerID)}, true},
		{"workspace scope cannot cross workspace", "workspace", db.Agent{WorkspaceID: parseUUID(otherWorkspaceID), OwnerID: parseUUID(subjectID)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := middleware.WithWorkspaceAccessPrincipal(context.Background(), middleware.WorkspaceAccessPrincipal{UserID: subjectID, WorkspaceID: workspaceID, ResourceScope: tt.scope})
			allowed, isToken := workspaceAccessCanUseAgent(ctx, tt.agent)
			if !isToken || allowed != tt.allowed {
				t.Fatalf("got allowed=%v isToken=%v, want allowed=%v isToken=true", allowed, isToken, tt.allowed)
			}
		})
	}
}

func TestWorkspaceAccessTraceScopeAndPagination(t *testing.T) {
	ctx := context.Background()
	var subjectID string
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email, principal_type) VALUES ('Trace Token', 'trace-token-' || gen_random_uuid()::text || '@internal.multica.invalid', 'workspace_access_token') RETURNING id`).Scan(&subjectID); err != nil {
		t.Fatalf("create trace subject: %v", err)
	}
	createAgent := func(name, ownerID string) string {
		t.Helper()
		var agentID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id)
			VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 'private', 1, $4) RETURNING id
		`, testWorkspaceID, name, testRuntimeID, ownerID).Scan(&agentID); err != nil {
			t.Fatalf("create trace agent: %v", err)
		}
		return agentID
	}
	ownedAgentID := createAgent("trace-token-owned-"+subjectID, subjectID)
	otherAgentID := createAgent("trace-token-other-"+subjectID, testUserID)
	createTask := func(agentID string) string {
		t.Helper()
		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, context)
			VALUES ($1, $2, 'completed', 0, jsonb_build_object('type', 'quick_create', 'workspace_id', $3::text, 'requester_id', $4::text, 'prompt', 'diagnose')) RETURNING id
		`, agentID, testRuntimeID, testWorkspaceID, testUserID).Scan(&taskID); err != nil {
			t.Fatalf("create trace task: %v", err)
		}
		return taskID
	}
	ownedTaskID := createTask(ownedAgentID)
	otherTaskID := createTask(otherAgentID)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_message (task_id, seq, type, tool, content, input, output) VALUES
		($1, 1, 'thinking', NULL, 'considering', NULL, NULL),
		($1, 2, 'tool_use', 'Search', NULL, '{"query":"full fidelity"}'::jsonb, NULL),
		($1, 3, 'tool_result', 'Search', NULL, NULL, 'raw tool result')
	`, ownedTaskID); err != nil {
		t.Fatalf("create trace messages: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = ANY($1::uuid[])`, []string{ownedTaskID, otherTaskID})
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = ANY($1::uuid[])`, []string{ownedAgentID, otherAgentID})
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, subjectID)
	})
	requestFor := func(taskID, scope, query string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskID+"/messages"+query, nil)
		req = withWorkspaceAccessParams(req, "taskId", taskID)
		principal := middleware.WorkspaceAccessPrincipal{UserID: subjectID, WorkspaceID: testWorkspaceID, ResourceScope: scope, Capabilities: []string{"trace.read"}}
		requestCtx := middleware.WithWorkspaceAccessPrincipal(req.Context(), principal)
		requestCtx = middleware.SetMemberContext(requestCtx, testWorkspaceID, db.Member{WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(subjectID), Role: middleware.WorkspaceAccessActorSource})
		return req.WithContext(requestCtx)
	}
	pageRec := httptest.NewRecorder()
	testHandler.ListTaskMessagesByUser(pageRec, requestFor(ownedTaskID, "own_agents", "?since=1&limit=2"))
	if pageRec.Code != http.StatusOK {
		t.Fatalf("owned trace status = %d, body = %s", pageRec.Code, pageRec.Body.String())
	}
	var page []protocol.TaskMessagePayload
	if err := json.NewDecoder(pageRec.Body).Decode(&page); err != nil {
		t.Fatalf("decode trace page: %v", err)
	}
	if len(page) != 2 || page[0].Seq != 2 || page[0].Input["query"] != "full fidelity" || page[1].Output != "raw tool result" {
		t.Fatalf("unexpected trace page: %#v", page)
	}
	deniedRec := httptest.NewRecorder()
	testHandler.ListTaskMessagesByUser(deniedRec, requestFor(otherTaskID, "own_agents", ""))
	if deniedRec.Code != http.StatusForbidden || !strings.Contains(deniedRec.Body.String(), "workspace_access_resource_not_allowed") {
		t.Fatalf("own scope foreign trace status = %d, body = %s", deniedRec.Code, deniedRec.Body.String())
	}
	workspaceRec := httptest.NewRecorder()
	testHandler.ListTaskMessagesByUser(workspaceRec, requestFor(otherTaskID, "workspace", ""))
	if workspaceRec.Code != http.StatusOK {
		t.Fatalf("workspace scope trace status = %d, body = %s", workspaceRec.Code, workspaceRec.Body.String())
	}
}
