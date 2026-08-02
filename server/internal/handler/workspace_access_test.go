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

func TestWorkspaceAccessGrantLifecycle(t *testing.T) {
	createReq := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/access-grants", map[string]any{
		"name":           "Vendor A",
		"capabilities":   []string{"deployment.manage", "trace.read"},
		"resource_scope": "own_agents",
	})
	createReq = withWorkspaceAccessParams(createReq, "id", testWorkspaceID)
	createRec := httptest.NewRecorder()
	testHandler.CreateWorkspaceAccessGrant(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("CreateWorkspaceAccessGrant status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var grant WorkspaceAccessGrantResponse
	if err := json.NewDecoder(createRec.Body).Decode(&grant); err != nil {
		t.Fatalf("decode grant: %v", err)
	}
	if grant.ResourceScope != "own_agents" || grant.Version != 1 {
		t.Fatalf("unexpected grant: %+v", grant)
	}

	var subjectID, principalType string
	if err := testPool.QueryRow(context.Background(), `
		SELECT g.subject_user_id::text, u.principal_type
		FROM workspace_access_grant g
		JOIN "user" u ON u.id = g.subject_user_id
		WHERE g.id = $1
	`, grant.ID).Scan(&subjectID, &principalType); err != nil {
		t.Fatalf("load grant subject: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace_access_grant WHERE id = $1`, grant.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, subjectID)
	})
	if principalType != "workspace_access_grant" {
		t.Fatalf("principal_type = %q", principalType)
	}
	var memberCount int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM member WHERE user_id = $1`, subjectID).Scan(&memberCount); err != nil {
		t.Fatalf("count subject memberships: %v", err)
	}
	if memberCount != 0 {
		t.Fatalf("grant subject has %d member rows, want 0", memberCount)
	}

	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	tokenReq := newRequest(http.MethodPost, "/tokens", map[string]any{
		"name":       "DTA production",
		"expires_at": expiresAt,
	})
	tokenReq = withWorkspaceAccessParams(tokenReq, "id", testWorkspaceID, "grantId", grant.ID)
	tokenRec := httptest.NewRecorder()
	testHandler.CreateWorkspaceAccessToken(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusCreated {
		t.Fatalf("CreateWorkspaceAccessToken status = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
	}
	var createdToken CreateWorkspaceAccessTokenResponse
	if err := json.NewDecoder(tokenRec.Body).Decode(&createdToken); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if !strings.HasPrefix(createdToken.Token, "dta_") {
		t.Fatalf("token = %q, want dta_ prefix", createdToken.Token)
	}
	var storedHash string
	if err := testPool.QueryRow(context.Background(), `SELECT token_hash FROM workspace_access_token WHERE id = $1`, createdToken.ID).Scan(&storedHash); err != nil {
		t.Fatalf("load token hash: %v", err)
	}
	if storedHash != auth.HashToken(createdToken.Token) || strings.Contains(storedHash, createdToken.Token) {
		t.Fatal("database must store only the token hash")
	}

	listReq := newRequest(http.MethodGet, "/tokens", nil)
	listReq = withWorkspaceAccessParams(listReq, "id", testWorkspaceID, "grantId", grant.ID)
	listRec := httptest.NewRecorder()
	testHandler.ListWorkspaceAccessTokens(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("ListWorkspaceAccessTokens status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	if strings.Contains(listRec.Body.String(), createdToken.Token) || strings.Contains(listRec.Body.String(), storedHash) {
		t.Fatal("token list leaked plaintext or hash")
	}

	updateReq := newRequest(http.MethodPatch, "/grant", map[string]any{
		"name":           "Vendor A",
		"capabilities":   []string{"trace.read"},
		"resource_scope": "workspace",
		"version":        grant.Version,
	})
	updateReq = withWorkspaceAccessParams(updateReq, "id", testWorkspaceID, "grantId", grant.ID)
	updateRec := httptest.NewRecorder()
	testHandler.UpdateWorkspaceAccessGrant(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("UpdateWorkspaceAccessGrant status = %d, body = %s", updateRec.Code, updateRec.Body.String())
	}
	var updated WorkspaceAccessGrantResponse
	if err := json.NewDecoder(updateRec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated grant: %v", err)
	}
	if updated.ResourceScope != "workspace" || updated.Version != 2 || len(updated.Capabilities) != 1 || updated.Capabilities[0] != "trace.read" {
		t.Fatalf("unexpected updated grant: %+v", updated)
	}

	disableReq := newRequest(http.MethodPost, "/disable", nil)
	disableReq = withWorkspaceAccessParams(disableReq, "id", testWorkspaceID, "grantId", grant.ID)
	disableRec := httptest.NewRecorder()
	testHandler.DisableWorkspaceAccessGrant(disableRec, disableReq)
	if disableRec.Code != http.StatusOK {
		t.Fatalf("DisableWorkspaceAccessGrant status = %d, body = %s", disableRec.Code, disableRec.Body.String())
	}

	revokeReq := newRequest(http.MethodDelete, "/token", nil)
	revokeReq = withWorkspaceAccessParams(revokeReq, "id", testWorkspaceID, "grantId", grant.ID, "tokenId", createdToken.ID)
	revokeRec := httptest.NewRecorder()
	testHandler.RevokeWorkspaceAccessToken(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusNoContent {
		t.Fatalf("RevokeWorkspaceAccessToken status = %d, body = %s", revokeRec.Code, revokeRec.Body.String())
	}
}

func TestWorkspaceAccessGrantRejectsInvalidPolicy(t *testing.T) {
	req := newRequest(http.MethodPost, "/access-grants", map[string]any{
		"name":           "Vendor",
		"capabilities":   []string{"members.manage"},
		"resource_scope": "workspace",
	})
	req = withWorkspaceAccessParams(req, "id", testWorkspaceID)
	rec := httptest.NewRecorder()
	testHandler.CreateWorkspaceAccessGrant(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
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
			ctx := middleware.WithWorkspaceAccessPrincipal(context.Background(), middleware.WorkspaceAccessPrincipal{
				UserID:        subjectID,
				WorkspaceID:   workspaceID,
				ResourceScope: tt.scope,
			})
			allowed, isGrant := workspaceAccessCanUseAgent(ctx, tt.agent)
			if !isGrant || allowed != tt.allowed {
				t.Fatalf("got allowed=%v isGrant=%v, want allowed=%v isGrant=true", allowed, isGrant, tt.allowed)
			}
		})
	}
}

func TestWorkspaceAccessTraceScopeAndPagination(t *testing.T) {
	ctx := context.Background()
	var subjectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email, principal_type)
		VALUES ('Trace Grant', 'trace-grant-' || gen_random_uuid()::text || '@internal.multica.invalid', 'workspace_access_grant')
		RETURNING id
	`).Scan(&subjectID); err != nil {
		t.Fatalf("create trace subject: %v", err)
	}

	createAgent := func(name, ownerID string) string {
		t.Helper()
		var agentID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent (
				workspace_id, name, description, runtime_mode, runtime_config,
				runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id
			)
			VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 'private', 1, $4)
			RETURNING id
		`, testWorkspaceID, name, testRuntimeID, ownerID).Scan(&agentID); err != nil {
			t.Fatalf("create trace agent: %v", err)
		}
		return agentID
	}
	ownedAgentID := createAgent("trace-grant-owned-"+subjectID, subjectID)
	otherAgentID := createAgent("trace-grant-other-"+subjectID, testUserID)

	createTask := func(agentID string) string {
		t.Helper()
		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, context)
			VALUES ($1, $2, 'completed', 0, jsonb_build_object(
				'type', 'quick_create', 'workspace_id', $3::text,
				'requester_id', $4::text, 'prompt', 'diagnose'
			))
			RETURNING id
		`, agentID, testRuntimeID, testWorkspaceID, testUserID).Scan(&taskID); err != nil {
			t.Fatalf("create trace task: %v", err)
		}
		return taskID
	}
	ownedTaskID := createTask(ownedAgentID)
	otherTaskID := createTask(otherAgentID)

	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_message (task_id, seq, type, tool, content, input, output)
		VALUES
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
		principal := middleware.WorkspaceAccessPrincipal{
			UserID: subjectID, WorkspaceID: testWorkspaceID, ResourceScope: scope,
			Capabilities: []string{"trace.read"},
		}
		requestCtx := middleware.WithWorkspaceAccessPrincipal(req.Context(), principal)
		requestCtx = middleware.SetMemberContext(requestCtx, testWorkspaceID, db.Member{
			WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(subjectID), Role: middleware.WorkspaceAccessActorSource,
		})
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
	if deniedRec.Code != http.StatusForbidden || !strings.Contains(deniedRec.Body.String(), "grant_resource_not_allowed") {
		t.Fatalf("own scope foreign trace status = %d, body = %s", deniedRec.Code, deniedRec.Body.String())
	}

	workspaceRec := httptest.NewRecorder()
	testHandler.ListTaskMessagesByUser(workspaceRec, requestFor(otherTaskID, "workspace", ""))
	if workspaceRec.Code != http.StatusOK {
		t.Fatalf("workspace scope trace status = %d, body = %s", workspaceRec.Code, workspaceRec.Body.String())
	}
}
