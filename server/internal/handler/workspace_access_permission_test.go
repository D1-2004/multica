package handler

import (
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkspaceServiceGuard(t *testing.T) {
	for _, tc := range []struct {
		permission, path, workspace string
		status                      int
	}{
		{"all", "/api/agents/a/dsh-profile", "w", 204},
		{"dsh_config", "/api/agents/a/dsh-profile", "w", 204},
		{"dsh_config", "/api/agents/a/dsh-native/access", "w", 403},
		{"all", "/api/me", "", 403},
		{"all", "/api/billing/balance", "", 403},
		{"all", "/api/agents/a/dsh-profile", "other", 403},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("X-Actor-Source", "workspace_access_token")
		ctx := middleware.WithWorkspaceAccessPrincipal(r.Context(), middleware.WorkspaceAccessPrincipal{WorkspaceID: "w", Permission: tc.permission})
		if tc.workspace != "" {
			ctx = middleware.SetMemberContext(ctx, tc.workspace, db.Member{})
		}
		w := httptest.NewRecorder()
		RequireHumanActor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r.WithContext(ctx))
		if w.Code != tc.status {
			t.Errorf("%+v got %d", tc, w.Code)
		}
	}
}
func TestPluginReplaceAlias(t *testing.T) {
	for _, v := range []string{"replace", "overwrite", " replace "} {
		if normalizeDshPluginConflict(v) != "overwrite" {
			t.Fatal(v)
		}
	}
	if normalizeDshPluginConflict("skip") != "skip" {
		t.Fatal("skip changed")
	}
}
