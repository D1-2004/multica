package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	internalflags "github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func uuidToString(u pgtype.UUID) string { return util.UUIDToString(u) }

// Auth middleware validates JWT tokens or Personal Access Tokens.
// Token sources (in priority order):
//  1. Authorization: Bearer <token> header (PAT or JWT)
//  2. multica_auth HttpOnly cookie (JWT) — requires valid CSRF token for state-changing requests
//
// Sets X-User-ID and X-User-Email headers on the request for downstream handlers.
//
// patCache is optional; when non-nil, PAT lookups are cached with a short
// TTL (auth.AuthCacheTTL). On cache hit the middleware skips both the DB
// SELECT and the last_used_at UPDATE — last_used_at is therefore refreshed
// at most once per TTL window per token, not per request.
//
// cloudPAT is optional; when non-nil, tokens with the mcn_ prefix are
// validated by calling the Multica Cloud Fleet service rather than the
// local DB. When nil (Fleet URL unset) mcn_ tokens are rejected at the
// prefix branch — we don't fall through to the mul_ / JWT paths, since
// an mcn_ string is by construction not a valid mul_ PAT or JWT.
func Auth(queries *db.Queries, patCache *auth.PATCache, cloudPAT *auth.CloudPATVerifier, releaseFlags ...*featureflag.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// X-Actor-Source is server-set only — any value supplied by
			// the client is untrusted and discarded before the auth
			// branches run. Only the mat_ branch below re-sets it. This
			// is what prevents a client from sending a normal mul_ PAT
			// plus a forged `X-Actor-Source: member` (or anything else)
			// to convince a downstream handler that its request came
			// from a non-task-token path.
			r.Header.Del("X-Actor-Source")
			r.Header.Del("X-Auth-Method")

			tokenString, fromCookie := extractToken(r)
			if tokenString == "" {
				slog.Debug("auth: no token found", "path", r.URL.Path)
				http.Error(w, `{"error":"missing authorization"}`, http.StatusUnauthorized)
				return
			}

			// Cookie-based auth requires CSRF validation for state-changing methods.
			if fromCookie && !auth.ValidateCSRF(r) {
				slog.Debug("auth: CSRF validation failed", "path", r.URL.Path)
				http.Error(w, `{"error":"CSRF validation failed"}`, http.StatusForbidden)
				return
			}

			// Agent task token: "mat_" prefix. Minted by the server at
			// task-claim time and injected by the daemon into the agent
			// process. Authoritative for actor identity — the bound
			// (user_id, agent_id, task_id, workspace_id) triple is
			// written into request headers here, OVERRIDING whatever the
			// client sent, so a downstream actor-resolver cannot be
			// tricked by a client that strips or forges X-Agent-ID /
			// X-Task-ID. Human-only endpoints (e.g. agent env
			// management) reject requests authenticated this way; see
			// `actorSourceFromRequest`. MUL-2600.
			if strings.HasPrefix(tokenString, "mat_") {
				if queries == nil {
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}
				hash := auth.HashToken(tokenString)
				tt, err := queries.GetTaskTokenByHash(r.Context(), hash)
				if err != nil {
					slog.Warn("auth: invalid task token", "path", protocol.RedactSceneConfigMCPPath(r.URL.Path), "error", err)
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}
				r.Header.Set("X-User-ID", uuidToString(tt.UserID))
				r.Header.Set("X-Agent-ID", uuidToString(tt.AgentID))
				r.Header.Set("X-Task-ID", uuidToString(tt.TaskID))
				r.Header.Set("X-Workspace-ID", uuidToString(tt.WorkspaceID))
				// X-Actor-Source flags the auth path so resolveActor and
				// any owner-only handler can deny without re-querying the
				// token table. The value "task_token" is the only signal
				// this header is allowed to carry — strip anything else a
				// client tried to send.
				r.Header.Set("X-Actor-Source", "task_token")
				next.ServeHTTP(w, r)
				return
			}

			// Cloud Node PAT: "mcn_" prefix. Verified by calling the
			// Multica Cloud Fleet service — Cloud (not us) is the
			// authoritative owner of the token's status and owner_id
			// binding. We never look at the local
			// personal_access_tokens table for this prefix; an mcn_
			// string is not a valid mul_ value, so falling through
			// would just be a redundant DB miss. When the verifier
			// is unconfigured (no MULTICA_CLOUD_FLEET_URL) we reject
			// at this branch rather than treating the token as a
			// JWT/PAT — failing closed avoids a misconfigured prod
			// silently downgrading auth.
			//
			// After Cloud confirms the token, we also confirm that
			// the returned owner_id maps to a real local user. The
			// Cloud `owner_id` and our `users.id` share the same UUID
			// space by contract, so this is a defense in depth: a
			// missing user means the local row was deleted out from
			// under a still-active node, or something is forging
			// owner_ids — either way we must not let the request
			// pass with a phantom X-User-ID.
			if strings.HasPrefix(tokenString, auth.CloudPATPrefix) {
				if cloudPAT == nil {
					slog.Warn("auth: mcn_ token presented but cloud verifier not configured", "path", r.URL.Path)
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}
				identity, err := cloudPAT.Verify(r.Context(), tokenString, ownerLookupFor(queries))
				if err != nil {
					if errors.Is(err, auth.ErrCloudPATInvalid) {
						slog.Warn("auth: cloud rejected mcn_ token", "path", r.URL.Path, "error", err)
						http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
						return
					}
					// Cloud unreachable / 5xx / decode error. We surface
					// 503 so callers (CLI / daemon) can retry — a 401
					// here would tell them to throw out a valid token.
					slog.Warn("auth: cloud pat verify unavailable", "path", r.URL.Path, "error", err)
					http.Error(w, `{"error":"cloud pat verifier unavailable"}`, http.StatusServiceUnavailable)
					return
				}
				r.Header.Set("X-User-ID", identity.OwnerID)
				// Tag the auth path so account-level guards (e.g.
				// handler.RequireHumanActor on /api/cloud-billing/*)
				// can distinguish a cloud-node machine credential
				// from a human PAT/JWT. Mirrors the mat_ branch's
				// stamp of "task_token" — both are server-set,
				// authoritative, and stripped from any client-
				// supplied value at the top of this middleware. Same
				// rationale as MUL-2600: a machine credential
				// (running agent or running cloud node) must not be
				// treated as the owner having approved an account-
				// level action.
				r.Header.Set("X-Actor-Source", "cloud_pat")
				next.ServeHTTP(w, r)
				return
			}

			// Workspace-bound DTA Token. The database row owns credential lifecycle
			// and a stable service-user identity. Business authorization is resolved
			// later through that subject's native workspace member row.
			if strings.HasPrefix(tokenString, "dta_") {
				// Passing the service is the production wiring. The variadic shape
				// keeps small auth unit fixtures source-compatible; when supplied,
				// a missing flag is deliberately off for rolling-release safety.
				if len(releaseFlags) > 0 && !internalflags.WorkspaceAccessTokensEnabled(r.Context(), releaseFlags[0]) {
					writeWorkspaceAccessAuthError(w, http.StatusForbidden, "workspace_access_operation_not_allowed")
					return
				}
				if queries == nil {
					writeWorkspaceAccessAuthError(w, http.StatusUnauthorized, "workspace_access_token_invalid")
					return
				}
				row, err := queries.GetWorkspaceAccessTokenByHash(r.Context(), auth.HashToken(tokenString))
				if err != nil {
					writeWorkspaceAccessAuthError(w, http.StatusUnauthorized, "workspace_access_token_invalid")
					return
				}
				if row.PrincipalType != WorkspaceAccessActorSource {
					writeWorkspaceAccessAuthError(w, http.StatusUnauthorized, "workspace_access_token_invalid")
					return
				}
				if row.RevokedAt.Valid {
					auditWorkspaceAccessRequest(r, queries, row, "denied")
					writeWorkspaceAccessAuthError(w, http.StatusUnauthorized, "workspace_access_token_revoked")
					return
				}
				if row.ExpiresAt.Valid && !row.ExpiresAt.Time.After(time.Now()) {
					auditWorkspaceAccessRequest(r, queries, row, "denied")
					writeWorkspaceAccessAuthError(w, http.StatusUnauthorized, "workspace_access_token_expired")
					return
				}

				principal := WorkspaceAccessPrincipal{
					Permission:  row.Permission,
					TokenID:     uuidToString(row.ID),
					UserID:      uuidToString(row.SubjectUserID),
					WorkspaceID: uuidToString(row.WorkspaceID),
					Name:        row.Name,
					Version:     row.Version,
				}

				// These headers are server-owned for DTA Token requests. The workspace
				// resolver also reads the Principal first, so query parameters cannot
				// negotiate a different workspace.
				r.Header.Del("X-Workspace-Slug")
				r.Header.Set("X-Workspace-ID", principal.WorkspaceID)
				r.Header.Set("X-User-ID", principal.UserID)
				r.Header.Set("X-Actor-Source", WorkspaceAccessActorSource)
				r = r.WithContext(WithWorkspaceAccessPrincipal(r.Context(), principal))
				if err := queries.UpdateWorkspaceAccessTokenLastUsed(r.Context(), row.ID); err != nil {
					slog.Warn("auth: failed to update workspace access token last_used_at", "token_id", principal.TokenID, "error", err)
				}
				recorder := &workspaceAccessAuditResponseWriter{ResponseWriter: w, status: http.StatusOK}
				if WorkspaceAccessRequestAllowed(principal.Permission, r) {
					next.ServeHTTP(recorder, r)
				} else {
					writeWorkspaceAccessAuthError(recorder, http.StatusForbidden, "workspace_access_operation_not_allowed")
				}
				result := "success"
				if recorder.status >= http.StatusBadRequest {
					result = "denied"
				}
				auditWorkspaceAccessRequest(r, queries, row, result)
				return
			}

			// Workspace MCP credentials are confined to their endpoint and to
			// server-created allowlisted business API subrequests. The member join
			// in this lookup is checked on every request, including subrequests.
			if strings.HasPrefix(tokenString, "wmcp_") {
				if len(releaseFlags) == 0 || !internalflags.WorkspaceMCPEndpointEnabled(r.Context(), releaseFlags[0]) {
					writeWorkspaceAccessAuthError(w, http.StatusForbidden, "workspace_mcp_disabled")
					return
				}
				if !strings.HasPrefix(r.URL.Path, "/api/mcp/workspaces/") && !IsWorkspaceMCPDispatch(r.Context()) && !IsConnectorAppConfigPath(r.URL.Path) {
					writeWorkspaceAccessAuthError(w, http.StatusForbidden, "workspace_mcp_endpoint_only")
					return
				}
				if queries == nil {
					writeWorkspaceAccessAuthError(w, http.StatusUnauthorized, "workspace_mcp_token_invalid")
					return
				}
				token, err := queries.GetWorkspaceMCPTokenByHash(r.Context(), auth.HashToken(tokenString))
				if err != nil || token.RevokedAt.Valid || !token.ExpiresAt.Valid || !token.ExpiresAt.Time.After(time.Now()) {
					writeWorkspaceAccessAuthError(w, http.StatusUnauthorized, "workspace_mcp_token_invalid")
					return
				}
				r.Header.Del("X-Workspace-Slug")
				r.Header.Del("X-Task-ID")
				r.Header.Del("X-Agent-ID")
				r.Header.Set("X-Workspace-ID", uuidToString(token.WorkspaceID))
				r.Header.Set("X-User-ID", uuidToString(token.SubjectUserID))
				r.Header.Set("X-Actor-Source", "workspace_mcp_token")
				r = r.WithContext(WithWorkspaceMCPPrincipal(r.Context(), token))
				if err := queries.TouchWorkspaceMCPToken(r.Context(), token.ID); err != nil {
					slog.Warn("auth: failed to touch workspace MCP token", "error", err)
				}
				next.ServeHTTP(w, r)
				return
			}

			// PAT: tokens starting with "mul_"
			if strings.HasPrefix(tokenString, "mul_") {
				hash := auth.HashToken(tokenString)

				// Cache hit: TTL has not expired, the token was valid the
				// last time we looked, and nothing has invalidated the
				// entry since. Skip the DB SELECT and the last_used_at
				// UPDATE — last_used_at is bumped once per TTL window.
				if userID, ok := patCache.Get(r.Context(), hash); ok {
					r.Header.Set("X-User-ID", userID)
					next.ServeHTTP(w, r)
					return
				}

				if queries == nil {
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}
				pat, err := queries.GetPersonalAccessTokenByHash(r.Context(), hash)
				if err != nil {
					slog.Warn("auth: invalid PAT", "path", r.URL.Path, "error", err)
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}

				userID := uuidToString(pat.UserID)
				r.Header.Set("X-User-ID", userID)

				// Clamp cache TTL to the token's remaining lifetime so a
				// PAT expiring in <AuthCacheTTL can't continue passing
				// auth on a cache hit after expires_at.
				var expiresAt time.Time
				if pat.ExpiresAt.Valid {
					expiresAt = pat.ExpiresAt.Time
				}
				patCache.Set(r.Context(), hash, userID, auth.TTLForExpiry(time.Now(), expiresAt))

				// Cache miss = TTL expired (or first use after revoke /
				// process restart). Refresh last_used_at; subsequent hits
				// within the TTL window skip this write entirely.
				TouchPATLastUsed(queries, pat.ID)

				next.ServeHTTP(w, r)
				return
			}

			// JWT
			token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrSignatureInvalid
				}
				return auth.JWTSecret(), nil
			})
			if err != nil || !token.Valid {
				slog.Warn("auth: invalid token", "path", r.URL.Path, "error", err)
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				slog.Warn("auth: invalid claims", "path", r.URL.Path)
				http.Error(w, `{"error":"invalid claims"}`, http.StatusUnauthorized)
				return
			}

			sub, ok := claims["sub"].(string)
			if !ok || strings.TrimSpace(sub) == "" {
				slog.Warn("auth: invalid claims", "path", r.URL.Path)
				http.Error(w, `{"error":"invalid claims"}`, http.StatusUnauthorized)
				return
			}
			if queries != nil {
				subjectID, parseErr := util.ParseUUID(sub)
				if parseErr != nil {
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}
				if _, lookupErr := queries.GetHumanUser(r.Context(), subjectID); lookupErr != nil {
					slog.Warn("auth: JWT subject is not a human principal", "subject", sub, "error", lookupErr)
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}
			}
			r.Header.Set("X-User-ID", sub)
			if email, ok := claims["email"].(string); ok {
				r.Header.Set("X-User-Email", email)
			}
			if authMethod, ok := claims["auth_method"].(string); ok && strings.TrimSpace(authMethod) != "" {
				r.Header.Set("X-Auth-Method", strings.TrimSpace(authMethod))
			}

			next.ServeHTTP(w, r)
		})
	}
}

type workspaceAccessAuditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *workspaceAccessAuditResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *workspaceAccessAuditResponseWriter) Write(body []byte) (int, error) {
	return w.ResponseWriter.Write(body)
}

func writeWorkspaceAccessAuthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + code + `","code":"` + code + `"}`))
}

func auditWorkspaceAccessRequest(r *http.Request, queries *db.Queries, row db.GetWorkspaceAccessTokenByHashRow, result string) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if _, err := queries.CreateWorkspaceAccessAudit(r.Context(), db.CreateWorkspaceAccessAuditParams{
		WorkspaceID: row.WorkspaceID,
		TokenID:     row.ID,
		ActorUserID: row.SubjectUserID,
		Action:      r.Method + " " + r.URL.Path,
		Result:      result,
		RequestID:   pgtype.Text{String: requestID, Valid: requestID != ""},
	}); err != nil {
		slog.Warn("auth: failed to audit workspace access request", "token_id", uuidToString(row.ID), "error", err)
	}
}

// extractToken returns the bearer token and whether it came from a cookie.
// Priority: Authorization header > multica_auth cookie.
func extractToken(r *http.Request) (token string, fromCookie bool) {
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenString != authHeader {
			return tokenString, false
		}
	}

	if cookie, err := r.Cookie(auth.AuthCookieName); err == nil && cookie.Value != "" {
		return cookie.Value, true
	}

	return "", false
}
