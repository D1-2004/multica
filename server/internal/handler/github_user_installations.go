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

// githubInstallationRepository is one repository an installation covers.
// full_name is owner/name. It never carries an id the page could treat as a
// second allow-list: add and remove stay on GitHub's installation settings.
type githubInstallationRepository struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

// githubUserInstallationView is one installation the stored token can use.
// It never carries a token, an installation access token, or the raw
// permissions object. missing_permissions is only the three write grants the
// connector still needs, so the configure page can ask an admin to approve.
type githubUserInstallationView struct {
	ID                    int64                          `json:"id"`
	AccountLogin          string                         `json:"account_login"`
	AccountType           string                         `json:"account_type"`
	RepositorySelection   string                         `json:"repository_selection"`
	SettingsURL           string                         `json:"settings_url"`
	Repositories          []githubInstallationRepository `json:"repositories"`
	RepositoryCount       int                            `json:"repository_count"`
	RepositoriesTruncated bool                           `json:"repositories_truncated,omitempty"`
	MissingPermissions    []string                       `json:"missing_permissions,omitempty"`
}

type githubUserInstallationsResponse struct {
	Connected     bool                         `json:"connected"`
	Installations []githubUserInstallationView `json:"installations"`
	// TotalCount is GitHub's total_count when the payload has one, otherwise
	// the number of installation objects we actually read. FilteredCount is
	// how many of those objects were dropped (suspended, id 0, or a login
	// that cannot be shown). Both are omitted on auth errors so a 403 is not
	// reported as zero installations. Repeated ids across pages are not
	// counted as filtered.
	TotalCount    *int   `json:"total_count,omitempty"`
	FilteredCount *int   `json:"filtered_count,omitempty"`
	Error         string `json:"error,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
}

type githubUserInstallationPayload struct {
	ID                  int64           `json:"id"`
	SuspendedAt         *string         `json:"suspended_at"`
	RepositorySelection string          `json:"repository_selection"`
	HTMLURL             string          `json:"html_url"`
	Permissions         json.RawMessage `json:"permissions"`
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
	if listed.Error == "" {
		attachGitHubInstallationRepositories(ctx, secret.Bearer, listed.Installations)
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
	var githubTotal *int
	rawSum := 0
	filtered := 0
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
		parsed, err := parseGitHubUserInstallations(body)
		if err != nil {
			return out, errors.New("GitHub installation list failed")
		}
		rawSum += parsed.rawCount
		filtered += parsed.filteredCount
		if page == 1 {
			githubTotal = parsed.totalCount
		}
		for _, view := range parsed.views {
			if _, ok := seen[view.ID]; ok {
				continue
			}
			seen[view.ID] = struct{}{}
			out.Installations = append(out.Installations, view)
		}
		if parsed.rawCount < githubUserInstallationPageSize {
			out.setInstallationCounts(githubTotal, rawSum, filtered)
			return out, nil
		}
		if page == githubUserInstallationPageCap {
			out.Truncated = true
		}
	}
	out.setInstallationCounts(githubTotal, rawSum, filtered)
	return out, nil
}

func (out *githubUserInstallationsResponse) setInstallationCounts(githubTotal *int, rawSum, filtered int) {
	total := rawSum
	if githubTotal != nil {
		total = *githubTotal
	}
	out.TotalCount = &total
	out.FilteredCount = &filtered
}

type parsedGitHubUserInstallations struct {
	views         []githubUserInstallationView
	rawCount      int
	filteredCount int
	totalCount    *int
}

func parseGitHubUserInstallations(body []byte) (parsedGitHubUserInstallations, error) {
	var payload struct {
		TotalCount    *int                            `json:"total_count"`
		Installations []githubUserInstallationPayload `json:"installations"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return parsedGitHubUserInstallations{}, err
	}
	views := make([]githubUserInstallationView, 0, len(payload.Installations))
	filtered := 0
	for _, item := range payload.Installations {
		login := strings.TrimSpace(item.Account.Login)
		if item.SuspendedAt != nil || item.ID == 0 || !githubAccountLoginPattern.MatchString(login) {
			filtered++
			continue
		}
		views = append(views, githubUserInstallationView{
			ID:                  item.ID,
			AccountLogin:        login,
			AccountType:         githubAccountType(item.Account.Type),
			RepositorySelection: githubRepositorySelection(item.RepositorySelection),
			SettingsURL:         safeGitHubSettingsURL(item.HTMLURL),
			Repositories:        emptyGitHubRepos(),
			MissingPermissions:  missingGitHubWrites(item.Permissions),
		})
	}
	return parsedGitHubUserInstallations{
		views:         views,
		rawCount:      len(payload.Installations),
		filteredCount: filtered,
		totalCount:    payload.TotalCount,
	}, nil
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

// Page size and cap for one installation's repositories. Three pages is
// enough to show a selected set; a larger installation says it was cut.
const (
	githubInstallationRepoPageSize = 100
	githubInstallationRepoPageCap  = 3
)

var githubFullNamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?/[\w.-]{1,100}$`)

func emptyGitHubRepos() []githubInstallationRepository {
	return []githubInstallationRepository{}
}

// attachGitHubInstallationRepositories fills each installation with the
// repositories that token can see. One installation's failure leaves that
// row's list empty and does not fail the account list. The fetches share
// one timeout so a long account list cannot hang the page.
func attachGitHubInstallationRepositories(ctx context.Context, token string, views []githubUserInstallationView) {
	if len(views) == 0 || !contextcap.ValidBearer(token) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for i := range views {
		repos, count, truncated, err := listGitHubInstallationRepositories(ctx, client, token, views[i].ID)
		if err != nil {
			slog.ErrorContext(ctx, "github installations: repository list failed", "installation_id", views[i].ID, "error", err)
			views[i].Repositories = emptyGitHubRepos()
			continue
		}
		views[i].Repositories = repos
		views[i].RepositoryCount = count
		views[i].RepositoriesTruncated = truncated
	}
}

func listGitHubInstallationRepositories(ctx context.Context, client *http.Client, token string, installationID int64) ([]githubInstallationRepository, int, bool, error) {
	if installationID <= 0 {
		return emptyGitHubRepos(), 0, false, errors.New("GitHub repository list failed")
	}
	all := emptyGitHubRepos()
	var total *int
	truncated := false
	for page := 1; page <= githubInstallationRepoPageCap; page++ {
		if ctx.Err() != nil {
			return emptyGitHubRepos(), 0, false, errors.New("GitHub repository list failed")
		}
		endpoint := fmt.Sprintf("%s/user/installations/%d/repositories?per_page=%d&page=%d", strings.TrimRight(githubAPIBase, "/"), installationID, githubInstallationRepoPageSize, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return emptyGitHubRepos(), 0, false, errors.New("GitHub repository list failed")
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return emptyGitHubRepos(), 0, false, errors.New("GitHub repository list failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, githubAPIResponseLimit))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || readErr != nil {
			return emptyGitHubRepos(), 0, false, errors.New("GitHub repository list failed")
		}
		repos, pageTotal, raw, err := parseGitHubInstallationRepositories(body)
		if err != nil {
			return emptyGitHubRepos(), 0, false, errors.New("GitHub repository list failed")
		}
		if page == 1 {
			total = pageTotal
		}
		all = append(all, repos...)
		if raw < githubInstallationRepoPageSize {
			break
		}
		if page == githubInstallationRepoPageCap {
			truncated = true
		}
	}
	count := len(all)
	if total != nil {
		count = *total
		if *total > len(all) {
			truncated = true
		}
	}
	return all, count, truncated, nil
}

func parseGitHubInstallationRepositories(body []byte) ([]githubInstallationRepository, *int, int, error) {
	var payload struct {
		TotalCount   *int `json:"total_count"`
		Repositories []struct {
			FullName string `json:"full_name"`
			Private  bool   `json:"private"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, 0, err
	}
	repos := emptyGitHubRepos()
	for _, item := range payload.Repositories {
		name := strings.TrimSpace(item.FullName)
		if !githubFullNamePattern.MatchString(name) {
			continue
		}
		repos = append(repos, githubInstallationRepository{FullName: name, Private: item.Private})
	}
	return repos, payload.TotalCount, len(payload.Repositories), nil
}
