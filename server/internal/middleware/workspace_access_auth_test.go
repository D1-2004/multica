package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	internalflags "github.com/multica-ai/multica/server/internal/featureflags"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

type workspaceAccessAuthFixture struct {
	rawToken    string
	tokenID     string
	workspaceID string
	userID      string
}

func setupWorkspaceAccessAuthFixture(t *testing.T) (*db.Queries, workspaceAccessAuthFixture) {
	t.Helper()
	pool := openPool(t)
	stamp := time.Now().UnixNano()
	var creatorID, subjectID, workspaceID, tokenID string
	if err := pool.QueryRow(context.Background(), `INSERT INTO "user" (name, email) VALUES ('Token creator', $1) RETURNING id`, fmt.Sprintf("token-creator-%d@multica.test", stamp)).Scan(&creatorID); err != nil {
		t.Fatalf("create token creator: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `INSERT INTO "user" (name, email, principal_type) VALUES ('Token subject', $1, 'workspace_access_token') RETURNING id`, fmt.Sprintf("token-subject-%d@internal.invalid", stamp)).Scan(&subjectID); err != nil {
		t.Fatalf("create token subject: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `INSERT INTO workspace (name, slug, issue_prefix) VALUES ('Token auth', $1, 'WAT') RETURNING id`, fmt.Sprintf("token-auth-%d", stamp)).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, workspaceID, subjectID); err != nil {
		t.Fatalf("create token subject membership: %v", err)
	}
	rawToken, err := auth.GenerateWorkspaceAccessToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO workspace_access_token (
			workspace_id, subject_user_id, name, token_hash, token_prefix,
			created_by, updated_by
		)
		VALUES ($1, $2, 'Vendor user', $3, $4, $5, $5)
		RETURNING id
	`, workspaceID, subjectID, auth.HashToken(rawToken), rawToken[:12], creatorID).Scan(&tokenID); err != nil {
		t.Fatalf("create token: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = ANY($1::uuid[])`, []string{creatorID, subjectID})
		pool.Close()
	})
	return db.New(pool), workspaceAccessAuthFixture{rawToken, tokenID, workspaceID, subjectID}
}

func TestWorkspaceAccessAuthReleaseFlagDefaultsOff(t *testing.T) {
	queries, fixture := setupWorkspaceAccessAuthFixture(t)
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next must not be called") })
	req := httptest.NewRequest(http.MethodGet, "/api/workspace-access/self", nil)
	req.Header.Set("Authorization", "Bearer "+fixture.rawToken)
	rec := httptest.NewRecorder()
	Auth(queries, nil, nil, nil)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "workspace_access_operation_not_allowed") {
		t.Fatalf("flag off status=%d body=%s", rec.Code, rec.Body.String())
	}

	provider := featureflag.NewStaticProvider()
	provider.Set(internalflags.WorkspaceAccessTokens, featureflag.Rule{Default: true})
	rec = httptest.NewRecorder()
	Auth(queries, nil, nil, featureflag.NewService(provider))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("flag on status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceAccessAuthBuildsPrincipalAndBindsWorkspace(t *testing.T) {
	queries, fixture := setupWorkspaceAccessAuthFixture(t)
	handler := Auth(queries, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := WorkspaceAccessPrincipalFromContext(r.Context())
		if !ok {
			t.Fatal("workspace access principal missing")
		}
		if principal.TokenID != fixture.tokenID || principal.WorkspaceID != fixture.workspaceID || principal.UserID != fixture.userID {
			t.Fatalf("unexpected principal: %+v", principal)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != fixture.workspaceID {
			t.Fatalf("X-Workspace-ID = %q", got)
		}
		if got := r.Header.Get("X-Actor-Source"); got != WorkspaceAccessActorSource {
			t.Fatalf("X-Actor-Source = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/workspace-access/self?workspace_id=00000000-0000-0000-0000-000000000000", nil)
	req.Header.Set("Authorization", "Bearer "+fixture.rawToken)
	req.Header.Set("X-Workspace-ID", "00000000-0000-0000-0000-000000000000")
	req.Header.Set("X-Workspace-Slug", "forged")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceAccessAuthDelegatesBusinessAuthorizationToNativeRoutes(t *testing.T) {
	queries, fixture := setupWorkspaceAccessAuthFixture(t)
	nextCalled := false
	handler := Auth(queries, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		principal, ok := WorkspaceAccessPrincipalFromContext(r.Context())
		if !ok || principal.UserID != fixture.userID {
			t.Fatalf("principal = %+v, want service member subject", principal)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/api/agents", "/api/issues", "/api/autopilots", "/api/agent-task-snapshot"} {
		nextCalled = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+fixture.rawToken)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if !nextCalled || rec.Code != http.StatusNoContent {
			t.Fatalf("%s next called=%v status=%d body=%s", path, nextCalled, rec.Code, rec.Body.String())
		}
	}
}

func TestWorkspaceAccessAuthUsesNativeMemberRole(t *testing.T) {
	queries, fixture := setupWorkspaceAccessAuthFixture(t)
	request := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
		req.Header.Set("Authorization", "Bearer "+fixture.rawToken)
		return req
	}

	t.Run("member route receives persisted member context", func(t *testing.T) {
		nextCalled := false
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nextCalled = true
			member, ok := MemberFromContext(r.Context())
			if !ok || member.Role != "member" || member.UserID.String() != fixture.userID {
				t.Fatalf("member = %+v, ok=%v", member, ok)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		handler := Auth(queries, nil, nil)(RequireWorkspaceMember(queries)(next))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request())
		if !nextCalled || rec.Code != http.StatusNoContent {
			t.Fatalf("next called=%v status=%d body=%s", nextCalled, rec.Code, rec.Body.String())
		}
	})

	t.Run("owner route is denied", func(t *testing.T) {
		next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next must not be called") })
		handler := Auth(queries, nil, nil)(RequireWorkspaceRole(queries, "owner")(next))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request())
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "insufficient permissions") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("bound workspace cannot be replaced", func(t *testing.T) {
		next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next must not be called") })
		foreignWorkspace := func(*http.Request) (string, error) {
			return "00000000-0000-4000-8000-000000000001", nil
		}
		handler := Auth(queries, nil, nil)(buildMiddleware(queries, foreignWorkspace, nil)(next))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request())
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "workspace_access_workspace_mismatch") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestWorkspaceAccessAuthExpiryAndRevokeApplyOnNextRequest(t *testing.T) {
	queries, fixture := setupWorkspaceAccessAuthFixture(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	pool := openPool(t)
	defer pool.Close()
	if _, err := pool.Exec(context.Background(), `UPDATE workspace_access_token SET expires_at = now() - interval '1 minute' WHERE id = $1`, fixture.tokenID); err != nil {
		t.Fatalf("expire token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/workspace-access/self", nil)
	req.Header.Set("Authorization", "Bearer "+fixture.rawToken)
	rec := httptest.NewRecorder()
	Auth(queries, nil, nil)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "workspace_access_token_expired") {
		t.Fatalf("expired status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := pool.Exec(context.Background(), `UPDATE workspace_access_token SET expires_at = NULL, revoked_at = now() WHERE id = $1`, fixture.tokenID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	rec = httptest.NewRecorder()
	Auth(queries, nil, nil)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "workspace_access_token_revoked") {
		t.Fatalf("revoked status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceAccessSubjectCannotAuthenticateAsHuman(t *testing.T) {
	queries, fixture := setupWorkspaceAccessAuthFixture(t)
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next must not be called") })

	t.Run("JWT", func(t *testing.T) {
		claims := validClaims()
		claims["sub"] = fixture.userID
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("Authorization", "Bearer "+generateToken(claims, auth.JWTSecret()))
		rec := httptest.NewRecorder()
		Auth(queries, nil, nil)(next).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("JWT status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("personal PAT", func(t *testing.T) {
		rawToken, err := auth.GeneratePATToken()
		if err != nil {
			t.Fatalf("generate PAT: %v", err)
		}
		pool := openPool(t)
		defer pool.Close()
		if _, err := pool.Exec(context.Background(), `INSERT INTO personal_access_token (user_id, name, token_hash, token_prefix) VALUES ($1, 'forbidden token PAT', $2, $3)`, fixture.userID, auth.HashToken(rawToken), rawToken[:12]); err != nil {
			t.Fatalf("insert PAT: %v", err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("Authorization", "Bearer "+rawToken)
		rec := httptest.NewRecorder()
		Auth(queries, nil, nil)(next).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("PAT status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}
