package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestWorkspaceMCPLinkCredentialPrecedence(t *testing.T) {
	for _, secret := range []string{"wmcp_test", "mul_personal", "mat_task", "jwt", "wmcp_bad value"} {
		t.Run(secret, func(t *testing.T) {
			router := chi.NewRouter()
			called := false
			router.With(WorkspaceMCPLinkCredential).Post("/api/mcp/workspaces/{workspaceId}/connect/{accessToken}", func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("Cookie") != "" {
					t.Fatal("ambient credentials survived link authentication")
				}
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest("POST", "/api/mcp/workspaces/ws/connect/"+strings.ReplaceAll(secret, " ", "%20"), nil)
			req.Header.Set("Authorization", "Bearer mul_other")
			req.Header.Set("Cookie", "multica_auth=other")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if called != (secret == "wmcp_test") {
				t.Fatalf("unexpected credential accepted: %v", called)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("secret response is cacheable")
			}
		})
	}
}

func TestWorkspaceMCPLinkPathRedacted(t *testing.T) {
	path := "/api/mcp/workspaces/ws/connect/wmcp_secret"
	if got := redactWebhookPath(path); got != "/api/mcp/workspaces/ws/connect/[redacted]" {
		t.Fatalf("unredacted path: %s", got)
	}
}
