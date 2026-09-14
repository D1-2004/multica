package main

import (
	"net/http"
	"strings"
)

// internalProductFeaturePublishHandler protects the append-only publishing
// endpoint with the shared operator token. When no token is configured, only
// direct loopback requests may use it for local development.
func internalProductFeaturePublishHandler(token string, next http.HandlerFunc) http.HandlerFunc {
	token = strings.TrimSpace(token)
	return func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			if !hasBearerToken(r, token) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="feature-releases"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		} else if !isDirectLoopbackRequest(r) {
			http.NotFound(w, r)
			return
		}

		next(w, r)
	}
}
