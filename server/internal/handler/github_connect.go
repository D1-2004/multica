package handler

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const githubConnectCookie = "multica_github_connect"
const githubConnectTTL = 15 * time.Minute

var githubOAuthBase = "https://github.com"

// The browser carries only a short-lived, sealed connection intent. No GitHub
// credential or process-local state is needed between replicas or redirects.
type githubConnectIntent struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	ReturnTo       string `json:"return_to"`
	InstallationID int64  `json:"installation_id,omitempty"`
	jwt.RegisteredClaims
}

func signGitHubConnectIntent(intent githubConnectIntent) (string, error) {
	if githubWebhookSecret() == "" {
		return "", errors.New("GitHub integration is not configured")
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, intent).SignedString([]byte(githubWebhookSecret()))
}

func readGitHubConnectIntent(value string) (githubConnectIntent, error) {
	var intent githubConnectIntent
	if githubWebhookSecret() == "" {
		return intent, errors.New("GitHub integration is not configured")
	}
	_, err := jwt.ParseWithClaims(value, &intent, func(*jwt.Token) (any, error) { return []byte(githubWebhookSecret()), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("multica-github-connect"), jwt.WithExpirationRequired())
	if err != nil {
		return intent, err
	}
	if _, err := parseStrictUUID(intent.WorkspaceID); err != nil {
		return intent, err
	}
	if _, err := parseStrictUUID(intent.UserID); err != nil {
		return intent, err
	}
	if !isAllowedGitHubReturnTo(intent.ReturnTo) || intent.ID == "" {
		return intent, errors.New("invalid connection intent")
	}
	return intent, nil
}

func (h *Handler) githubFrontend() string {
	if origin := strings.TrimRight(strings.TrimSpace(h.currentConfig().FrontendOrigin), "/"); origin != "" {
		return origin
	}
	return "http://localhost:3000"
}

func (h *Handler) githubIntentSettingsURL(ctx context.Context, intent githubConnectIntent) string {
	base := h.githubFrontend()
	if workspace, err := h.Queries.GetWorkspace(ctx, parseUUID(intent.WorkspaceID)); err == nil {
		base += "/" + url.PathEscape(workspace.Slug)
	}
	return githubSettingsURL(base, intent.ReturnTo)
}

func (h *Handler) githubIntentAllowed(ctx context.Context, intent githubConnectIntent) bool {
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: parseUUID(intent.UserID), WorkspaceID: parseUUID(intent.WorkspaceID)})
	return err == nil && (member.Role == "owner" || member.Role == "admin")
}

func (h *Handler) setGitHubConnectCookie(w http.ResponseWriter, value string) {
	cookie := &http.Cookie{Name: githubConnectCookie, Value: value, Path: "/api/github", HttpOnly: true, Secure: strings.HasPrefix(h.githubFrontend(), "https://"), SameSite: http.SameSiteLaxMode, MaxAge: int(githubConnectTTL.Seconds())}
	if value == "" {
		cookie.MaxAge = -1
	}
	http.SetCookie(w, cookie)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// GitHubInstallStart runs in the browser that will visit GitHub, including when
// a desktop client opens its external browser. An API-response cookie alone
// would land in the desktop client's cookie jar instead.
func (h *Handler) GitHubInstallStart(w http.ResponseWriter, r *http.Request) {
	value := r.URL.Query().Get("state")
	intent, err := readGitHubConnectIntent(value)
	if err != nil || intent.InstallationID != 0 {
		writeError(w, http.StatusBadRequest, "invalid or expired GitHub connection")
		return
	}
	if !h.githubIntentAllowed(r.Context(), intent) {
		writeError(w, http.StatusForbidden, "workspace administrator permission required")
		return
	}
	if previous, err := r.Cookie(githubConnectCookie); err == nil && previous.Value != value {
		if _, err := readGitHubConnectIntent(previous.Value); err == nil {
			// Invalidate both attempts instead of guessing a workspace if GitHub
			// later drops state from either tab. A fresh Connect can start again.
			h.setGitHubConnectCookie(w, "")
			http.Redirect(w, r, h.githubIntentSettingsURL(r.Context(), intent)+"&github_error=connection_in_progress", http.StatusFound)
			return
		}
	}
	h.setGitHubConnectCookie(w, value)
	http.Redirect(w, r, fmt.Sprintf("https://github.com/apps/%s/installations/new?state=%s", url.PathEscape(githubAppSlug()), url.QueryEscape(value)), http.StatusFound)
}

func githubUserAuthorizationConfigured() bool {
	return strings.TrimSpace(os.Getenv("GITHUB_APP_CLIENT_ID")) != "" && strings.TrimSpace(os.Getenv("GITHUB_APP_CLIENT_SECRET")) != ""
}

func (h *Handler) beginGitHubUserAuthorization(w http.ResponseWriter, r *http.Request, intent githubConnectIntent) {
	settingsURL := h.githubIntentSettingsURL(r.Context(), intent)
	if !githubUserAuthorizationConfigured() {
		h.setGitHubConnectCookie(w, "")
		http.Redirect(w, r, settingsURL+"&github_error=user_authorization_not_configured", http.StatusFound)
		return
	}
	// A fresh nonce binds the code exchange to this exact installation, browser,
	// destination workspace and initiating human. Expiry is not extended.
	intent.ID = uuid.NewString()
	value, err := signGitHubConnectIntent(intent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start GitHub authorization")
		return
	}
	h.setGitHubConnectCookie(w, value)
	query := url.Values{"client_id": {strings.TrimSpace(os.Getenv("GITHUB_APP_CLIENT_ID"))}, "redirect_uri": {h.githubFrontend() + "/api/github/authorize"}, "state": {value}}
	http.Redirect(w, r, githubOAuthBase+"/login/oauth/authorize?"+query.Encode(), http.StatusFound)
}

// GitHubAuthorizeCallback proves that the GitHub user can access the selected
// installation. An installation ID and an App JWT alone cannot prove that.
func (h *Handler) GitHubAuthorizeCallback(w http.ResponseWriter, r *http.Request) {
	// A valid pre-release install cookie forwards this request with the query
	// unchanged. Without that cookie, a pre-release state still forwards below.
	if h.forwardGitHubPreEnvCookie(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	restoreGitHubInstallState(r)
	value := r.URL.Query().Get("state")
	// Official app (GitHub MCP) connects reuse this registered GitHub App
	// callback. Their states carry connectorOAuthStatePrefix and never touch
	// the install cookie; every other state keeps the install flow below.
	if isConnectorOAuthState(value) {
		h.serveConnectorOAuthCallback(w, r, connectorOAuthViaGitHub)
		return
	}
	intent, err := readGitHubConnectIntent(value)
	cookie, cookieErr := r.Cookie(githubConnectCookie)
	if err != nil || cookieErr != nil || !hmac.Equal([]byte(cookie.Value), []byte(value)) || intent.InstallationID <= 0 {
		http.Redirect(w, r, githubSettingsURL(h.githubFrontend(), githubReturnToGitHub)+"&github_error=invalid_state", http.StatusFound)
		return
	}
	h.setGitHubConnectCookie(w, "")
	settingsURL := h.githubIntentSettingsURL(r.Context(), intent)
	if !h.githubIntentAllowed(r.Context(), intent) {
		http.Redirect(w, r, settingsURL+"&github_error=workspace_forbidden", http.StatusFound)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || r.URL.Query().Get("error") != "" {
		http.Redirect(w, r, settingsURL+"&github_error=authorization_cancelled", http.StatusFound)
		return
	}
	installation, err := h.authorizedGitHubInstallation(r.Context(), code, intent.InstallationID)
	if err != nil {
		http.Redirect(w, r, settingsURL+"&github_error=installation_not_authorized", http.StatusFound)
		return
	}
	// Recheck after the provider calls, in case membership changed meanwhile.
	if !h.githubIntentAllowed(r.Context(), intent) {
		http.Redirect(w, r, settingsURL+"&github_error=workspace_forbidden", http.StatusFound)
		return
	}
	h.persistGitHubSetup(w, r, intent.WorkspaceID, settingsURL, installation.ID, installation.Account.Login, installation.Account.Type, installation.Account.AvatarURL, parseUUID(intent.UserID))
}

type githubAuthorizedInstallation struct {
	ID          int64   `json:"id"`
	SuspendedAt *string `json:"suspended_at"`
	Account     struct {
		Login     string  `json:"login"`
		Type      string  `json:"type"`
		AvatarURL *string `json:"avatar_url"`
	} `json:"account"`
}

func (h *Handler) authorizedGitHubInstallation(ctx context.Context, code string, installationID int64) (githubAuthorizedInstallation, error) {
	var result githubAuthorizedInstallation
	if !githubUserAuthorizationConfigured() {
		return result, errors.New("GitHub user authorization is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Never persist or log the code, client secret, user token or provider body.
	form := url.Values{"client_id": {strings.TrimSpace(os.Getenv("GITHUB_APP_CLIENT_ID"))}, "client_secret": {os.Getenv("GITHUB_APP_CLIENT_SECRET")}, "code": {code}, "redirect_uri": {h.githubFrontend() + "/api/github/authorize"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubOAuthBase+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return result, errors.New("GitHub code exchange failed")
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, githubAPIResponseLimit)).Decode(&token)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "bearer") {
		return result, errors.New("GitHub code exchange failed")
	}
	for page := 1; page <= 100; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/user/installations?per_page=100&page=%d", strings.TrimRight(githubAPIBase, "/"), page), nil)
		if err != nil {
			return result, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		resp, err := client.Do(req)
		if err != nil {
			return result, errors.New("GitHub installation verification failed")
		}
		var body struct {
			Installations []githubAuthorizedInstallation `json:"installations"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, githubAPIResponseLimit)).Decode(&body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			return result, errors.New("GitHub installation verification failed")
		}
		for _, installation := range body.Installations {
			if installation.ID == installationID && installation.SuspendedAt == nil && installation.Account.Login != "" {
				return installation, nil
			}
		}
		if len(body.Installations) < 100 {
			break
		}
	}
	return result, errors.New("GitHub installation is not accessible to this user")
}
