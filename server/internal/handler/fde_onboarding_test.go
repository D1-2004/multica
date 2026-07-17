package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/managedagent"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestGetFDEOnboardingReturnsAllExistingWorkspacesForDisplay(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler database fixture unavailable")
	}

	ctx := context.Background()
	suffix := randomID()[:8]
	var memberWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('FDE Existing Member Workspace', $1, 'FMO')
		RETURNING id
	`, "fde-existing-member-"+suffix).Scan(&memberWorkspaceID); err != nil {
		t.Fatalf("create existing member workspace: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
	`, memberWorkspaceID, testUserID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, memberWorkspaceID)
	})

	w := httptest.NewRecorder()
	testHandler.GetFDEOnboarding(w, newRequest(http.MethodGet, "/api/fde/onboarding", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("get FDE onboarding: got %d: %s", w.Code, w.Body.String())
	}
	var response FDEOnboardingStateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Configured {
		t.Fatal("test handler without managed source must report configured=false")
	}
	if !response.CreateOnly {
		t.Fatal("FDE onboarding response must identify the create-only contract")
	}
	workspaceIDs := make(map[string]bool, len(response.Workspaces))
	for _, workspace := range response.Workspaces {
		workspaceIDs[workspace.ID] = true
	}
	if !workspaceIDs[testWorkspaceID] || !workspaceIDs[memberWorkspaceID] {
		t.Fatalf("FDE workspaces = %#v, want existing workspaces %s and %s", response.Workspaces, testWorkspaceID, memberWorkspaceID)
	}
}

func TestFDEDingTalkInstallAllowsOtherOrganizationMembers(t *testing.T) {
	workspaceID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	agentID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	initiatorID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}

	params := fdeDingTalkInstallParams(workspaceID, agentID, initiatorID)
	if params.WorkspaceID != workspaceID || params.AgentID != agentID || params.InitiatorID != initiatorID {
		t.Fatalf("install identities = %#v, want workspace=%v agent=%v initiator=%v", params, workspaceID, agentID, initiatorID)
	}
	if !params.AllowUnbound {
		t.Fatal("FDE DingTalk installation must allow other organization members by default")
	}
}

func TestCreateFDEWorkspaceAlwaysCreatesAnotherWorkspace(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler database fixture unavailable")
	}

	ctx := context.Background()
	suffix := randomID()[:8]
	var userID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('FDE New User', $1)
		RETURNING id
	`, fmt.Sprintf("fde-new-user-%s@multica.test", suffix)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	var ordinaryWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('Existing Product Workspace', $1, 'EPW')
		RETURNING id
	`, "existing-product-workspace-"+suffix).Scan(&ordinaryWorkspaceID); err != nil {
		t.Fatalf("create existing workspace: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, ordinaryWorkspaceID, userID); err != nil {
		t.Fatalf("own existing workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, ordinaryWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})

	parsedUserID := parseUUID(userID)
	first, err := testHandler.createFDEWorkspace(
		newRequestAs(userID, http.MethodPost, "/api/fde/onboarding", nil),
		parsedUserID,
		FDEOnboardingProvisionRequest{WorkspaceName: "FDE Team"},
	)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, first.ID) })
	if uuidToString(first.ID) == ordinaryWorkspaceID {
		t.Fatal("FDE onboarding reused an ordinary workspace")
	}

	second, err := testHandler.createFDEWorkspace(
		newRequestAs(userID, http.MethodPost, "/api/fde/onboarding", nil),
		parsedUserID,
		FDEOnboardingProvisionRequest{WorkspaceName: "FDE Team"},
	)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, second.ID) })
	if first.ID == second.ID {
		t.Fatalf("second request reused the first workspace: %s", uuidToString(first.ID))
	}
}

func TestCreateFDEWorkspaceRejectsWorkspaceIDOnlyRequests(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler database fixture unavailable")
	}

	var req FDEOnboardingProvisionRequest
	if err := json.Unmarshal([]byte(`{"workspace_id":"`+testWorkspaceID+`"}`), &req); err != nil {
		t.Fatalf("decode old request: %v", err)
	}
	_, err := testHandler.createFDEWorkspace(
		newRequest(http.MethodPost, "/api/fde/onboarding", nil),
		parseUUID(testUserID),
		req,
	)
	if !errors.Is(err, errFDEWorkspaceInput) {
		t.Fatalf("workspace_id-only request error = %v, want workspace name required", err)
	}
}

func TestUpsertFDERuntimeAlignsOwnerOnRetry(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler database fixture unavailable")
	}

	ctx := context.Background()
	suffix := randomID()[:8]
	var firstOwnerID, secondOwnerID, workspaceID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('FDE Runtime Owner One', $1)
		RETURNING id
	`, fmt.Sprintf("fde-runtime-owner-one-%s@multica.test", suffix)).Scan(&firstOwnerID); err != nil {
		t.Fatalf("create first owner: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('FDE Runtime Owner Two', $1)
		RETURNING id
	`, fmt.Sprintf("fde-runtime-owner-two-%s@multica.test", suffix)).Scan(&secondOwnerID); err != nil {
		t.Fatalf("create second owner: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('FDE Runtime Ownership', $1, 'FRO')
		RETURNING id
	`, "fde-runtime-ownership-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id IN ($1, $2)`, firstOwnerID, secondOwnerID)
	})

	h := *testHandler
	h.cfg.FCE2B = service.FCE2BConfig{Template: "fde-runtime-test", TimeoutSeconds: 900}
	req := newRequestAs(uuidToString(secondOwnerID), http.MethodPost, "/api/fde/onboarding", nil)
	if _, err := h.upsertFDERuntime(req, workspaceID, firstOwnerID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	runtime, err := h.upsertFDERuntime(req, workspaceID, secondOwnerID)
	if err != nil {
		t.Fatalf("realign runtime owner: %v", err)
	}
	if runtime.OwnerID != secondOwnerID {
		t.Fatalf("runtime owner = %s, want scanner/Agent owner %s", uuidToString(runtime.OwnerID), uuidToString(secondOwnerID))
	}
}

func TestManagedAgentProvisionReassignsExistingAgentToScanner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler database fixture unavailable")
	}

	ctx := context.Background()
	suffix := randomID()[:8]
	sourceKey := "fde-agent-test-" + suffix
	var firstOwnerID, scannerID, workspaceID, runtimeID, agentID pgtype.UUID
	for _, row := range []struct {
		name  string
		email string
		id    *pgtype.UUID
	}{
		{name: "FDE Existing Owner", email: fmt.Sprintf("fde-existing-owner-%s@multica.test", suffix), id: &firstOwnerID},
		{name: "FDE Scanner", email: fmt.Sprintf("fde-scanner-%s@multica.test", suffix), id: &scannerID},
	} {
		if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`, row.name, row.email).Scan(row.id); err != nil {
			t.Fatalf("create %s: %v", row.name, err)
		}
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('FDE Existing Agent', $1, 'FEA') RETURNING id
	`, "fde-existing-agent-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM managed_agent_source_snapshot WHERE source_key = $1`, sourceKey)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id IN ($1, $2)`, firstOwnerID, scannerID)
	})
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, visibility
		) VALUES ($1, $2, 'FDE Runtime', 'cloud', 'fc-e2b', 'online', 'test', '{}', $3, 'private')
		RETURNING id
	`, workspaceID, "fc-e2b:fde:"+uuidToString(workspaceID), firstOwnerID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, runtime_mode, runtime_config, visibility, status,
			max_concurrent_tasks, owner_id, description, runtime_id, instructions,
			custom_env, custom_args, permission_mode
		) VALUES ($1, 'FDE Existing Agent', 'cloud', '{}', 'private', 'offline', 6, $2, '', $3, '', '{}', '[]', 'private')
		RETURNING id
	`, workspaceID, firstOwnerID, runtimeID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO managed_agent_source_snapshot (source_key, repository_url, ref)
		VALUES ($1, $2, 'master')
	`, sourceKey, managedagent.DefaultRepositoryURL); err != nil {
		t.Fatalf("create managed snapshot ledger: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_source (
			agent_id, workspace_id, source_type, managed_source_key, repo_owner,
			repo_name, ref, manifest_path, synced_commit_sha, sync_status, created_by
		) VALUES ($1, $2, 'managed_git', $4, 'keeperqaq', 'fde-agent',
			'master', 'multica-agent.yaml', 'test-sha', 'ready', $3)
	`, agentID, workspaceID, firstOwnerID, sourceKey); err != nil {
		t.Fatalf("create managed source: %v", err)
	}

	managed, err := managedagent.New(testHandler.Queries, testPool, managedagent.Config{
		SourceKey: sourceKey, RepositoryURL: managedagent.DefaultRepositoryURL,
		Ref: managedagent.DefaultRef, SyncInterval: 30 * time.Minute, BatchSize: 50,
	}, nil)
	if err != nil {
		t.Fatalf("create managed service: %v", err)
	}
	agent, created, err := managed.Provision(ctx, workspaceID, scannerID, runtimeID, "cloud", "hermes", "")
	if err != nil {
		t.Fatalf("reuse managed Agent: %v", err)
	}
	if created {
		t.Fatal("existing managed Agent must be reused")
	}
	if agent.OwnerID != scannerID {
		t.Fatalf("Agent owner = %s, want scanner %s", uuidToString(agent.OwnerID), uuidToString(scannerID))
	}
}
