package middleware

import (
	"net/http"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
)

// RefreshCloudFrontCookies is middleware that refreshes CloudFront signed cookies
// on authenticated requests when the cookie is missing (expired or first request
// after login). This prevents 403s from the CDN when cookies expire before the
// user's session does.
func RefreshCloudFrontCookies(signer *auth.CloudFrontSigner) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if signer == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Workspace service credentials must not mint the human CDN session,
			// whose policy is broader than the token's API/workspace boundary.
			if _, service := WorkspaceAccessPrincipalFromContext(r.Context()); service {
				next.ServeHTTP(w, r)
				return
			}
			if _, err := r.Cookie("CloudFront-Policy"); err != nil {
				ttl := auth.AuthTokenTTL()
				for _, cookie := range signer.SignedCookies(time.Now().Add(ttl)) {
					http.SetCookie(w, cookie)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
