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

func TestWorkspaceMCPAuthBindsMemberAndRejectsRevocationOrRemoval(t *testing.T) {
	pool := openPool(t)
	t.Cleanup(pool.Close)
	stamp := time.Now().UnixNano()
	var subjectID, workspaceID, tokenID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO "user" (name,email) VALUES ('MCP member',$1) RETURNING id`, fmt.Sprintf("wmcp-%d@example.invalid", stamp)).Scan(&subjectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO workspace (name,slug,issue_prefix) VALUES ('MCP auth',$1,'WMC') RETURNING id`, fmt.Sprintf("wmcp-%d", stamp)).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace_mcp_audit WHERE workspace_id=$1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace_mcp_token WHERE workspace_id=$1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1`, subjectID)
	})
	if _, err := pool.Exec(t.Context(), `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'member')`, workspaceID, subjectID); err != nil {
		t.Fatal(err)
	}
	secret, err := auth.GenerateWorkspaceMCPToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO workspace_mcp_token
		(workspace_id,subject_user_id,created_by,name,token_hash,token_prefix,scopes,expires_at)
		VALUES ($1,$2,$2,'test',$3,$4,ARRAY['read'],now()+interval '1 day') RETURNING id`,
		workspaceID, subjectID, auth.HashToken(secret), secret[:13]).Scan(&tokenID); err != nil {
		t.Fatal(err)
	}
	provider := featureflag.NewStaticProvider()
	provider.Set(internalflags.WorkspaceMCPEndpoint, featureflag.Rule{Default: true})
	called := false
	handler := Auth(db.New(pool), nil, nil, featureflag.NewService(provider))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		principal, ok := WorkspaceMCPPrincipalFromContext(r.Context())
		if !ok || principal.Role != "member" || r.Header.Get("X-User-ID") != subjectID || r.Header.Get("X-Workspace-ID") != workspaceID {
			t.Fatalf("wrong principal or member role: %+v", principal)
		}
		if r.Header.Get("X-Actor-Source") != "workspace_mcp_token" || r.Header.Get("X-Task-ID") != "" {
			t.Fatalf("untrusted actor headers survived: %v", r.Header)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func(path string) int {
		t.Helper()
		called = false
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("X-User-ID", "forged")
		req.Header.Set("X-Task-ID", "forged")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusNoContent && !called {
			t.Fatal("success without handler")
		}
		return rec.Code
	}
	path := "/api/mcp/workspaces/" + workspaceID
	if got := request(path); got != http.StatusNoContent {
		t.Fatalf("valid status=%d", got)
	}
	if got := request("/api/issues/"); got != http.StatusForbidden {
		t.Fatalf("direct API status=%d", got)
	}
	if got := request("/api/mcp/workspaces/00000000-0000-0000-0000-000000000000"); got != http.StatusNoContent {
		// Authentication is valid; endpoint handler enforces URL workspace equality.
		t.Fatalf("authentication unexpectedly rejected cross-workspace URL: %d", got)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE workspace_mcp_token SET revoked_at=now() WHERE id=$1`, tokenID); err != nil {
		t.Fatal(err)
	}
	if got := request(path); got != http.StatusUnauthorized {
		t.Fatalf("revoked status=%d", got)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE workspace_mcp_token SET revoked_at=NULL,expires_at=now()-interval '1 second' WHERE id=$1`, tokenID); err != nil {
		t.Fatal(err)
	}
	if got := request(path); got != http.StatusUnauthorized {
		t.Fatalf("expired status=%d", got)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE workspace_mcp_token SET expires_at=now()+interval '1 day' WHERE id=$1`, tokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, workspaceID, subjectID); err != nil {
		t.Fatal(err)
	}
	if got := request(path); got != http.StatusUnauthorized {
		t.Fatalf("removed member status=%d", got)
	}
	if !strings.HasPrefix(secret, "wmcp_") {
		t.Fatal("wrong token prefix")
	}
}
