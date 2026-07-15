package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestGetFDEOnboardingListsOnlyAdminWorkspaces(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler database fixture unavailable")
	}

	ctx := context.Background()
	suffix := randomID()[:8]
	var memberWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('FDE Member Only', $1, 'FMO')
		RETURNING id
	`, "fde-member-only-"+suffix).Scan(&memberWorkspaceID); err != nil {
		t.Fatalf("create member-only workspace: %v", err)
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
	if len(response.Workspaces) != 1 || response.Workspaces[0].ID != testWorkspaceID {
		t.Fatalf("admin workspaces = %#v, want only %s", response.Workspaces, testWorkspaceID)
	}
}

func TestResolveOrCreateFDEWorkspaceIsIdempotentPerUser(t *testing.T) {
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
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID) })

	parsedUserID := parseUUID(userID)
	first, err := testHandler.resolveOrCreateFDEWorkspace(
		newRequestAs(userID, http.MethodPost, "/api/fde/onboarding", nil),
		parsedUserID,
		FDEOnboardingProvisionRequest{WorkspaceName: "FDE Team"},
	)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, first.ID) })

	second, err := testHandler.resolveOrCreateFDEWorkspace(
		newRequestAs(userID, http.MethodPost, "/api/fde/onboarding", nil),
		parsedUserID,
		FDEOnboardingProvisionRequest{WorkspaceName: "A Different Name"},
	)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("second request created another workspace: first=%s second=%s", uuidToString(first.ID), uuidToString(second.ID))
	}
}

func TestResolveFDEWorkspaceRejectsOrdinaryMember(t *testing.T) {
	suffix := randomID()[:8]
	memberID := pgtype.UUID{}
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO "user" (name, email)
		VALUES ('FDE Member', $1)
		RETURNING id
	`, fmt.Sprintf("fde-member-%s@multica.test", suffix)).Scan(&memberID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, memberID) })
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
	`, testWorkspaceID, memberID); err != nil {
		t.Fatalf("create member: %v", err)
	}

	_, err := testHandler.resolveOrCreateFDEWorkspace(
		newRequestAs(uuidToString(memberID), http.MethodPost, "/api/fde/onboarding", nil),
		memberID,
		FDEOnboardingProvisionRequest{WorkspaceID: testWorkspaceID},
	)
	if !errors.Is(err, errFDEWorkspaceForbidden) {
		t.Fatalf("member workspace resolve error = %v, want forbidden", err)
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
