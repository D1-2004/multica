package middleware

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http/httptest"
	"testing"
)

func TestWorkspaceAccessPermissionContract(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"POST", "/api/dsh-plugins/upload", true},
		{"GET", "/api/agents/a/dsh-plugins", true},
		{"PUT", "/api/agents/a/dsh-plugins", true},
		{"GET", "/api/agents/a/dsh-profile", true},
		{"POST", "/api/agents/a/dsh-profile", true},
		{"GET", "/api/workspace-access/self", true},
		{"DELETE", "/api/agents/a/dsh-plugins", false},
		{"PUT", "/api/agents/a", false},
		{"POST", "/api/agents/a/dsh-native/access", false},
		{"POST", "/api/agents/a/dsh-profile/retry", false},
		{"POST", "/api/tasks", false},
		{"GET", "/api/workspaces/w/access-tokens", false},
		{"POST", "/api/dsh-plugins/upload/extra", false},
		{"POST", "/api/agents/a/dsh-profile/../tasks", false},
		{"GET", "/api/me", false},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if got := WorkspaceAccessRequestAllowed(WorkspaceAccessDSHConfig, r); got != tc.allowed {
			t.Errorf("%s %s: %v", tc.method, tc.path, got)
		}
		if !WorkspaceAccessRequestAllowed(WorkspaceAccessAll, r) {
			t.Errorf("all denied %s", tc.path)
		}
		if WorkspaceAccessRequestAllowed("unknown", r) {
			t.Fatal("unknown permission accepted")
		}
	}
}

func TestWorkspaceAccessMemberCannotElevateAnotherSubjectOrWorkspace(t *testing.T) {
	ws, user := uuid.New(), uuid.New()
	member := db.Member{WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, UserID: pgtype.UUID{Bytes: user, Valid: true}, Role: "member"}
	for _, permission := range []string{WorkspaceAccessAll, WorkspaceAccessDSHConfig} {
		p := WorkspaceAccessPrincipal{WorkspaceID: ws.String(), UserID: user.String(), Permission: permission}
		if WorkspaceAccessMember(WithWorkspaceAccessPrincipal(context.Background(), p), member).Role != "owner" {
			t.Fatal("authorized subject has no authority")
		}
		p.WorkspaceID = uuid.NewString()
		if WorkspaceAccessMember(WithWorkspaceAccessPrincipal(context.Background(), p), member).Role != "member" {
			t.Fatal("cross-workspace escalation")
		}
		p.WorkspaceID = ws.String()
		p.UserID = uuid.NewString()
		if WorkspaceAccessMember(WithWorkspaceAccessPrincipal(context.Background(), p), member).Role != "member" {
			t.Fatal("another subject elevated")
		}
	}
	if WorkspaceAccessMember(context.Background(), member).Role != "member" {
		t.Fatal("human membership changed")
	}
}

func TestWorkspaceAccessDeniesFilesystemForDTATokens(t *testing.T) {
	for _, path := range []string{"/api/filesystem/roots", "/api/filesystem/entries", "/api/filesystem/grants"} {
		r := httptest.NewRequest("GET", path, nil)
		if WorkspaceAccessRequestAllowed(WorkspaceAccessAll, r) || WorkspaceAccessRequestAllowed(WorkspaceAccessDSHConfig, r) {
			t.Fatalf("DTA token allowed %s", path)
		}
	}
}
