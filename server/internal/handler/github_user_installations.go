package handler

// GitHub App installations visible to one scene, org, or person credential.
//
// The catalog connector is a GitHub App (oauth_github_app). Its user access
// token does not carry OAuth scopes: GitHub always returns an empty scope,
// and adding scope=repo to the authorize URL does not widen it. The token
// sees a repository only when the app is installed on that account or
// organization and the signed-in user can access the repository. One token
// already covers every installation of that app the user can access, so the
// configure page lists them and links to installing the app on another
// account or organization instead of storing one credential per installation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	githubInstallationsReconnect   = "reconnect"
	githubInstallationsNotAppToken = "not_github_app_token"
)

// Page size and cap are vars so tests can force a second page and truncation
// without building a 100-item payload. Production lists at most 1000
// installations; the response says when that cap was hit.
var (
	githubUserInstallationPageSize = 100
	githubUserInstallationPageCap  = 10
)

var githubAccountLoginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// githubUserInstallationView is one installation the stored token can use.
// It never carries a token, an installation access token, or a permissions blob.
type githubUserInstallationView struct {
	ID                  int64  `json:"id"`
	AccountLogin        string `json:"account_login"`
	AccountType         string `json:"account_type"`
	RepositorySelection string `json:"repository_selection"`
	SettingsURL         string `json:"settings_url"`
}

type githubUserInstallationsResponse struct {
	Connected     bool                         `json:"connected"`
	Installations []githubUserInstallationView `json:"installations"`
	Error         string                       `json:"error,omitempty"`
	Truncated     bool                         `json:"truncated,omitempty"`
}

type githubUserInstallationPayload struct {
	ID                  int64   `json:"id"`
	SuspendedAt         *string `json:"suspended_at"`
	RepositorySelection string  `json:"repository_selection"`
	HTMLURL             string  `json:"html_url"`
	Account             struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
}

func emptyGitHubInstallations() []githubUserInstallationView {
	return []githubUserInstallationView{}
}

// ListContextConfigGitHubInstallations lists the GitHub App installations
// covered by one configure-page credential:
// GET /api/context-capabilities/agents/{agentId}/github-installations
// ?scope_type=&scope_key=&org_id=&connector_id=
// The caller needs rights.connect. No credential is {connected:false}.
// A GitHub 401 is {connected:true, error:"reconnect"}; a 403 (a personal
// access token, which cannot list app installations) is error
// "not_github_app_token". The token and the provider body are never logged.
func (h *Handler) ListContextConfigGitHubInstallations(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, contextCapScopeOrg(query.Get("scope_type"), query.Get("scope_key"), query.Get("org_id"))); !ok {
		return
	}
	connectorUUID, err := util.ParseUUID(strings.TrimSpace(query.Get("connector_id")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid connector_id")
		return
	}
	grant, ok := h.contextCapRequireScope(w, r, a, userID, query.Get("scope_type"), query.Get("scope_key"), contextCapNeedCredential)
	if !ok {
		return
	}
	connectorID := uuidToString(connectorUUID)
	catalogSlug, ok := h.contextCapCredentialConnector(w, r, a, grant.ScopeType, connectorID)
	if !ok {
		return
	}
	h.respondGitHubUserInstallations(w, r, contextcap.CredentialBinding{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, ConnectorID: connectorID,
		ScopeType: grant.ScopeType, OrgID: a.OrgID, ScopeKey: grant.ScopeKey,
	}, catalogSlug)
}

// ListAgentGitHubInstallations is the same list on an admin context node:
// GET .../context/{scopeType}/{scopeKey}/github-installations?connector_id=
func (h *Handler) ListAgentGitHubInstallations(w http.ResponseWriter, r *http.Request) {
	connectorUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(r.URL.Query().Get("connector_id")), "connector_id")
	if !ok {
		return
	}
	node, ok := h.agentContextNodeFromRoute(w, r, contextCapNeedCredential)
	if !ok {
		return
	}
	connectorID := uuidToString(connectorUUID)
	catalogSlug, ok := h.contextCapCredentialConnector(w, r, node.agent, node.scope.ScopeType, connectorID)
	if !ok {
		return
	}
	h.respondGitHubUserInstallations(w, r, contextcap.CredentialBinding{
		WorkspaceID: node.caller.workspaceID, AgentID: node.caller.agentID, ConnectorID: connectorID,
		ScopeType: node.scope.ScopeType, OrgID: node.tenant.OrgID, ScopeKey: node.scope.ScopeKey,
	}, catalogSlug)
}

func (h *Handler) respondGitHubUserInstallations(w http.ResponseWriter, r *http.Request, key contextcap.CredentialBinding, catalogSlug string) {
	if catalogSlug != "github" {
		writeError(w, http.StatusBadRequest, "this connector is not GitHub")
		return
	}
	ctx := r.Context()
	cred, err := contextcap.GetCredential(ctx, h.DB, key)
	if errors.Is(err, contextcap.ErrNotFound) {
		writeJSON(w, http.StatusOK, githubUserInstallationsResponse{Connected: false, Installations: emptyGitHubInstallations()})
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "github installations: credential lookup failed", "agent_id", key.AgentID, "connector_id", key.ConnectorID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load the GitHub connection")
		return
	}
	secret, err := contextcap.OpenCredentialSecret(h.contextCredentialBox(), key, cred.Ciphertext)
	if errors.Is(err, contextcap.ErrCredentialKeyUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "connector credential storage is not configured")
		return
	}
	if err != nil || !contextcap.ValidBearer(secret.Bearer) {
		writeJSON(w, http.StatusOK, githubUserInstallationsResponse{
			Connected: true, Installations: emptyGitHubInstallations(), Error: githubInstallationsReconnect,
		})
		return
	}
	listed, err := listGitHubUserInstallations(ctx, secret.Bearer)
	if err != nil {
		// err is a fixed string. Never attach the token or the provider body.
		slog.ErrorContext(ctx, "github installations: list failed", "agent_id", key.AgentID, "connector_id", key.ConnectorID, "error", err)
		writeError(w, http.StatusBadGateway, "could not list GitHub App installations")
		return
	}
	writeJSON(w, http.StatusOK, listed)
}

// listGitHubUserInstallations calls GET /user/installations with the stored
// user token. A 401 or 403 is a response, not an error: the page explains
// reconnect versus a personal access token. Anything else is a fixed error
// that does not include the response body.
func listGitHubUserInstallations(ctx context.Context, token string) (githubUserInstallationsResponse, error) {
	out := githubUserInstallationsResponse{Connected: true, Installations: emptyGitHubInstallations()}
	if !contextcap.ValidBearer(token) {
		out.Error = githubInstallationsReconnect
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	seen := map[int64]struct{}{}
	for page := 1; page <= githubUserInstallationPageCap; page++ {
		endpoint := fmt.Sprintf("%s/user/installations?per_page=%d&page=%d", strings.TrimRight(githubAPIBase, "/"), githubUserInstallationPageSize, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return out, errors.New("GitHub installation list failed")
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return out, errors.New("GitHub installation list failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, githubAPIResponseLimit))
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			out.Error = githubInstallationsReconnect
			out.Installations = emptyGitHubInstallations()
			return out, nil
		case http.StatusForbidden:
			out.Error = githubInstallationsNotAppToken
			out.Installations = emptyGitHubInstallations()
			return out, nil
		}
		if resp.StatusCode != http.StatusOK || readErr != nil {
			return out, errors.New("GitHub installation list failed")
		}
		pageViews, rawCount, err := parseGitHubUserInstallations(body)
		if err != nil {
			return out, errors.New("GitHub installation list failed")
		}
		for _, view := range pageViews {
			if _, ok := seen[view.ID]; ok {
				continue
			}
			seen[view.ID] = struct{}{}
			out.Installations = append(out.Installations, view)
		}
		if rawCount < githubUserInstallationPageSize {
			return out, nil
		}
		if page == githubUserInstallationPageCap {
			out.Truncated = true
		}
	}
	return out, nil
}

func parseGitHubUserInstallations(body []byte) ([]githubUserInstallationView, int, error) {
	var payload struct {
		Installations []githubUserInstallationPayload `json:"installations"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0, err
	}
	views := make([]githubUserInstallationView, 0, len(payload.Installations))
	for _, item := range payload.Installations {
		login := strings.TrimSpace(item.Account.Login)
		if item.SuspendedAt != nil || item.ID == 0 || !githubAccountLoginPattern.MatchString(login) {
			continue
		}
		views = append(views, githubUserInstallationView{
			ID:                  item.ID,
			AccountLogin:        login,
			AccountType:         githubAccountType(item.Account.Type),
			RepositorySelection: githubRepositorySelection(item.RepositorySelection),
			SettingsURL:         safeGitHubSettingsURL(item.HTMLURL),
		})
	}
	return views, len(payload.Installations), nil
}

func githubAccountType(raw string) string {
	switch strings.TrimSpace(raw) {
	case "User", "Organization":
		return strings.TrimSpace(raw)
	default:
		return ""
	}
}

func githubRepositorySelection(raw string) string {
	switch strings.TrimSpace(raw) {
	case "all", "selected":
		return strings.TrimSpace(raw)
	default:
		return ""
	}
}

// safeGitHubSettingsURL keeps only https://github.com links GitHub itself
// put on the installation. Anything else is dropped before it reaches the page.
func safeGitHubSettingsURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil {
		return ""
	}
	parsed.Fragment = ""
	return parsed.String()
}
