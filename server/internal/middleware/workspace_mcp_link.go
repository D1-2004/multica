package middleware

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// WorkspaceMCPLinkCredential adapts a copied URL to the existing token verifier.
// The URL credential is authoritative; browser cookies or an alternate Bearer
// header must never rescue a revoked link or change its identity.
func WorkspaceMCPLinkCredential(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		secret := chi.URLParam(r, "accessToken")
		if !strings.HasPrefix(secret, "wmcp_") || len(secret) > 128 || strings.ContainsAny(secret, " /\r\n\t") {
			writeWorkspaceAccessAuthError(w, http.StatusUnauthorized, "workspace_mcp_token_invalid")
			return
		}
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Del("Cookie")
		next.ServeHTTP(w, r)
	})
}
