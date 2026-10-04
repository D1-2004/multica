package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/contextcap"
)

// GitHub catalog tools are the remote server's default toolsets
// (context, repos, issues, pull_requests, users, and copilot). The product
// keeps only tools that the minimum App permission set can cover: Contents,
// Pull requests, Issues, and Metadata. Anything else stays hidden.

const (
	githubGrantApp     = "app"
	githubGrantPAT     = "pat"
	githubGrantUnknown = "unknown"

	githubMCPToolsets = "context,repos,issues,pull_requests,users"
)

type githubPermNeed struct {
	Key   string
	Level string
}

type githubToolSpec struct {
	Needs []githubPermNeed
}

type githubInstallGrant struct {
	login       string
	permissions map[string]string
}

type githubAccess struct {
	kind     string
	ceiling  map[string]string
	installs []githubInstallGrant
	cachedAt time.Time
}

func githubNeed(key, level string) []githubPermNeed {
	return []githubPermNeed{{Key: key, Level: level}}
}

func githubNeeds(needs ...githubPermNeed) []githubPermNeed {
	return needs
}

// githubMCPTools is the connector's usable set. Names match
// github/github-mcp-server at 71ef826 (2026-10-04). Tools absent from this
// map are hidden even when upstream lists them.
var githubMCPTools = map[string]githubToolSpec{
	"get_me":       {},
	"search_users": {},

	"search_repositories":           {Needs: githubNeed("metadata", "read")},
	"list_repository_collaborators": {Needs: githubNeed("metadata", "read")},

	"get_file_contents":     {Needs: githubNeed("contents", "read")},
	"get_file_blame":        {Needs: githubNeed("contents", "read")},
	"get_commit":            {Needs: githubNeed("contents", "read")},
	"list_commits":          {Needs: githubNeed("contents", "read")},
	"list_branches":         {Needs: githubNeed("contents", "read")},
	"list_tags":             {Needs: githubNeed("contents", "read")},
	"get_tag":               {Needs: githubNeed("contents", "read")},
	"list_releases":         {Needs: githubNeed("contents", "read")},
	"get_latest_release":    {Needs: githubNeed("contents", "read")},
	"get_release_by_tag":    {Needs: githubNeed("contents", "read")},
	"search_code":           {Needs: githubNeed("contents", "read")},
	"search_commits":        {Needs: githubNeed("contents", "read")},
	"create_or_update_file": {Needs: githubNeed("contents", "write")},
	"push_files":            {Needs: githubNeed("contents", "write")},
	"delete_file":           {Needs: githubNeed("contents", "write")},
	"create_branch":         {Needs: githubNeed("contents", "write")},

	"list_pull_requests":                 {Needs: githubNeed("pull_requests", "read")},
	"pull_request_read":                  {Needs: githubNeed("pull_requests", "read")},
	"search_pull_requests":               {Needs: githubNeed("pull_requests", "read")},
	"create_pull_request":                {Needs: githubNeed("pull_requests", "write")},
	"update_pull_request":                {Needs: githubNeed("pull_requests", "write")},
	"pull_request_review_write":          {Needs: githubNeed("pull_requests", "write")},
	"add_reply_to_pull_request_comment":  {Needs: githubNeed("pull_requests", "write")},
	"add_comment_to_pending_review":      {Needs: githubNeed("pull_requests", "write")},
	"add_pull_request_review_comment":    {Needs: githubNeed("pull_requests", "write")},
	"create_pull_request_review":         {Needs: githubNeed("pull_requests", "write")},
	"delete_pending_pull_request_review": {Needs: githubNeed("pull_requests", "write")},
	"request_pull_request_reviewers":     {Needs: githubNeed("pull_requests", "write")},
	"resolve_review_thread":              {Needs: githubNeed("pull_requests", "write")},
	"submit_pending_pull_request_review": {Needs: githubNeed("pull_requests", "write")},
	"unresolve_review_thread":            {Needs: githubNeed("pull_requests", "write")},
	"update_pull_request_draft_state":    {Needs: githubNeed("pull_requests", "write")},
	"merge_pull_request": {Needs: githubNeeds(
		githubPermNeed{Key: "contents", Level: "write"},
		githubPermNeed{Key: "pull_requests", Level: "write"},
	)},
	"update_pull_request_branch": {Needs: githubNeeds(
		githubPermNeed{Key: "contents", Level: "write"},
		githubPermNeed{Key: "pull_requests", Level: "write"},
	)},

	"issue_read":             {Needs: githubNeed("issues", "read")},
	"list_issues":            {Needs: githubNeed("issues", "read")},
	"search_issues":          {Needs: githubNeed("issues", "read")},
	"get_label":              {Needs: githubNeed("issues", "read")},
	"issue_dependency_read":  {Needs: githubNeed("issues", "read")},
	"find_duplicate":         {Needs: githubNeed("issues", "read")},
	"list_issue_fields":      {Needs: githubNeed("issues", "read")},
	"list_issue_types":       {Needs: githubNeed("issues", "read")},
	"issue_write":            {Needs: githubNeed("issues", "write")},
	"create_issue":           {Needs: githubNeed("issues", "write")},
	"add_issue_comment":      {Needs: githubNeed("issues", "write")},
	"update_issue_comment":   {Needs: githubNeed("issues", "write")},
	"update_issue_state":     {Needs: githubNeed("issues", "write")},
	"update_issue_labels":    {Needs: githubNeed("issues", "write")},
	"update_issue_assignees": {Needs: githubNeed("issues", "write")},
	"update_issue_type":      {Needs: githubNeed("issues", "write")},
	"set_issue_fields":       {Needs: githubNeed("issues", "write")},
	"sub_issue_write":        {Needs: githubNeed("issues", "write")},
	"add_sub_issue":          {Needs: githubNeed("issues", "write")},
	"remove_sub_issue":       {Needs: githubNeed("issues", "write")},
	"reprioritize_sub_issue": {Needs: githubNeed("issues", "write")},
	"issue_dependency_write": {Needs: githubNeed("issues", "write")},
}

// githubUnknownFloor is what a call may assume when the installation list
// cannot be read. It matches the permissions qwen-tag-pre requests today, so
// a lookup failure does not advertise writes the App cannot perform.
var githubUnknownFloor = map[string]string{
	"metadata":      "read",
	"contents":      "read",
	"pull_requests": "read",
}

func githubProductTool(name string) bool {
	_, ok := githubMCPTools[name]
	return ok
}

func githubPermRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "read":
		return 1
	case "write":
		return 2
	case "admin":
		return 3
	default:
		return 0
	}
}

func githubPermLabel(key, level string) string {
	name := map[string]string{
		"contents":      "Contents",
		"pull_requests": "Pull requests",
		"issues":        "Issues",
		"metadata":      "Metadata",
	}[key]
	if name == "" {
		name = key
	}
	switch strings.ToLower(level) {
	case "read":
		if key == "metadata" {
			return name + " 只读"
		}
		return name + " 读"
	case "write":
		return name + " 写"
	default:
		return name + " " + level
	}
}

func (spec githubToolSpec) missing(ceiling map[string]string) []string {
	var missing []string
	for _, need := range spec.Needs {
		if githubPermRank(ceiling[need.Key]) < githubPermRank(need.Level) {
			missing = append(missing, githubPermLabel(need.Key, need.Level))
		}
	}
	return missing
}

// githubToolBlockReason is empty when the tool may be listed and called.
// A non-empty string is the agent-facing explanation.
func githubToolBlockReason(name string, access githubAccess) string {
	spec, ok := githubMCPTools[name]
	if !ok {
		return "GitHub 连接器不提供 " + name + "。它需要的权限不在 Contents、Pull requests、Issues、Metadata 里，已经从工具列表隐藏。"
	}
	if access.kind == githubGrantPAT {
		return ""
	}
	ceiling := access.ceiling
	confirmed := access.kind == githubGrantApp
	if !confirmed {
		ceiling = githubUnknownFloor
	}
	missing := spec.missing(ceiling)
	if len(missing) == 0 {
		return ""
	}
	need := strings.Join(missing, "、")
	if !confirmed {
		return "暂时没有读到这个 GitHub 安装的实际权限，先按 Contents 读、Pull requests 读、Metadata 只读处理，所以不能调用 " + name + "。它需要 " + need + "。请稍后重试。如果 App 刚改过权限，已有安装要管理员在 GitHub 上重新批准后才会生效。"
	}
	return "GitHub 安装权限不够，不能调用 " + name + "。需要 " + need + "，当前安装没有。App 所有者把权限调高之后，已有安装不会自动升级，要由这个账号或组织的管理员在 GitHub 安装页重新批准。"
}

// githubOwnerBlockReason names the installation the arguments point at.
// The ceiling used by githubToolBlockReason is the highest grant on the
// token, so an approved account would otherwise let a call through to an
// account that has not re-approved the new permissions.
func githubOwnerBlockReason(name string, arguments json.RawMessage, access githubAccess) string {
	if access.kind != githubGrantApp {
		return ""
	}
	owner := githubArgumentOwner(arguments)
	if owner == "" {
		return ""
	}
	spec, ok := githubMCPTools[name]
	if !ok {
		return ""
	}
	var matched []githubInstallGrant
	for _, install := range access.installs {
		if install.login != "" && strings.EqualFold(install.login, owner) {
			matched = append(matched, install)
		}
	}
	if len(matched) == 0 {
		return ""
	}
	for _, install := range matched {
		if len(spec.missing(install.permissions)) == 0 {
			return ""
		}
	}
	missing := spec.missing(matched[0].permissions)
	return "GitHub 安装 @" + matched[0].login + " 还没有 " + strings.Join(missing, "、") + "，不能调用 " + name + "。写文件、建分支、建 PR、改 Issue 在这个账号或组织的管理员于 GitHub 安装页重新批准前不会执行。修改仓库范围不会补上这些权限。不要改用本机的 gh、git 或环境变量里的令牌。"
}

func githubArgumentOwner(arguments json.RawMessage) string {
	if strings.TrimSpace(string(arguments)) == "" || string(arguments) == "null" {
		return ""
	}
	var payload struct {
		Owner string `json:"owner"`
	}
	if json.Unmarshal(arguments, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Owner)
}

func githubPermissionCeiling(levels []map[string]string) map[string]string {
	ceiling := map[string]string{}
	for _, one := range levels {
		for key, level := range one {
			key = strings.ToLower(strings.TrimSpace(key))
			if key == "" || githubPermRank(level) == 0 {
				continue
			}
			if githubPermRank(level) > githubPermRank(ceiling[key]) {
				ceiling[key] = strings.ToLower(strings.TrimSpace(level))
			}
		}
	}
	return ceiling
}

// missingGitHubWrites is the config-page list. nil means the payload did not
// say, or every minimum write is already granted.
func missingGitHubWrites(raw json.RawMessage) []string {
	if len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		return nil
	}
	var levels map[string]string
	if json.Unmarshal(raw, &levels) != nil {
		return nil
	}
	order := []struct{ key, code string }{
		{"contents", "contents:write"},
		{"pull_requests", "pull_requests:write"},
		{"issues", "issues:write"},
	}
	var missing []string
	for _, item := range order {
		if githubPermRank(levels[item.key]) < githubPermRank("write") {
			missing = append(missing, item.code)
		}
	}
	return missing
}

var githubGrantCache = struct {
	mu      sync.Mutex
	entries map[string]githubAccess
}{entries: map[string]githubAccess{}}

func githubGrantCacheTTL(kind string) time.Duration {
	if kind == githubGrantApp || kind == githubGrantPAT {
		return time.Minute
	}
	return 15 * time.Second
}

func loadGitHubGrantCeiling(ctx context.Context, token string) githubAccess {
	if !contextcap.ValidBearer(token) {
		return githubAccess{kind: githubGrantUnknown}
	}
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:16])
	now := time.Now()
	githubGrantCache.mu.Lock()
	if cached, ok := githubGrantCache.entries[key]; ok && now.Sub(cached.cachedAt) < githubGrantCacheTTL(cached.kind) {
		githubGrantCache.mu.Unlock()
		return cached
	}
	githubGrantCache.mu.Unlock()

	access := fetchGitHubGrantCeiling(ctx, token)
	access.cachedAt = now
	githubGrantCache.mu.Lock()
	githubGrantCache.entries[key] = access
	githubGrantCache.mu.Unlock()
	return access
}

func fetchGitHubGrantCeiling(ctx context.Context, token string) githubAccess {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	var grants []githubInstallGrant
	for page := 1; page <= githubUserInstallationPageCap; page++ {
		endpoint := fmt.Sprintf("%s/user/installations?per_page=%d&page=%d", strings.TrimRight(githubAPIBase, "/"), githubUserInstallationPageSize, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return githubAccess{kind: githubGrantUnknown}
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return githubAccess{kind: githubGrantUnknown}
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, githubAPIResponseLimit))
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusForbidden:
			return githubAccess{kind: githubGrantPAT}
		case http.StatusUnauthorized:
			return githubAccess{kind: githubGrantUnknown}
		}
		if resp.StatusCode != http.StatusOK || readErr != nil {
			return githubAccess{kind: githubGrantUnknown}
		}
		pageGrants, count, err := parseGitHubInstallationPermissionPage(body)
		if err != nil {
			return githubAccess{kind: githubGrantUnknown}
		}
		grants = append(grants, pageGrants...)
		if count < githubUserInstallationPageSize {
			break
		}
	}
	levels := make([]map[string]string, 0, len(grants))
	for _, grant := range grants {
		levels = append(levels, grant.permissions)
	}
	return githubAccess{kind: githubGrantApp, ceiling: githubPermissionCeiling(levels), installs: grants}
}

func parseGitHubInstallationPermissionPage(body []byte) ([]githubInstallGrant, int, error) {
	var payload struct {
		Installations []struct {
			ID          int64             `json:"id"`
			SuspendedAt *string           `json:"suspended_at"`
			Permissions map[string]string `json:"permissions"`
			Account     struct {
				Login string `json:"login"`
			} `json:"account"`
		} `json:"installations"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0, err
	}
	grants := make([]githubInstallGrant, 0, len(payload.Installations))
	for _, item := range payload.Installations {
		if item.SuspendedAt != nil || item.ID == 0 || item.Permissions == nil {
			continue
		}
		grants = append(grants, githubInstallGrant{
			login:       strings.TrimSpace(item.Account.Login),
			permissions: item.Permissions,
		})
	}
	if len(payload.Installations) > 0 && len(grants) == 0 {
		// A page of installations with no permissions object is not a grant.
		return nil, len(payload.Installations), errors.New("GitHub installation permissions missing")
	}
	return grants, len(payload.Installations), nil
}

func githubUpstreamPermissionFailure(result multicaMCPToolResult) bool {
	var text strings.Builder
	for _, item := range result.Content {
		text.WriteString(strings.ToLower(item.Text))
		text.WriteByte('\n')
	}
	body := text.String()
	return strings.Contains(body, "resource not accessible") || strings.Contains(body, "not accessible by integration")
}

func githubUpstreamPermissionMessage(name, owner string) string {
	spec, ok := githubMCPTools[name]
	if !ok || len(spec.Needs) == 0 {
		return ""
	}
	labels := make([]string, 0, len(spec.Needs))
	for _, need := range spec.Needs {
		labels = append(labels, githubPermLabel(need.Key, need.Level))
	}
	who := "这个安装"
	if login := strings.TrimSpace(owner); login != "" {
		who = "@" + login
	}
	return "GitHub 拒绝了 " + name + "。" + who + " 没有 " + strings.Join(labels, "、") + "。已有安装要管理员在 GitHub 安装页重新批准后才会带上新权限。不要改用本机的 gh、git 或环境变量里的令牌。"
}

// githubAgentRefusal is an ordinary tool result. The sandbox MCP bridge
// replaces an isError result's text with "MCP tool reported an error", so a
// permission explanation has to travel in content with isError left false.
func githubAgentRefusal(text string) multicaMCPToolResult {
	return multicaMCPToolResult{Content: []multicaMCPContent{{Type: "text", Text: text}}}
}

func (h *Handler) githubGrantView(ctx context.Context, c *internalConnector) githubAccess {
	if c == nil {
		return githubAccess{kind: githubGrantUnknown}
	}
	token, err := h.freshConnectorToken(ctx, c, "")
	if err != nil || !contextcap.ValidBearer(token) {
		return githubAccess{kind: githubGrantUnknown}
	}
	return loadGitHubGrantCeiling(ctx, token)
}

func catalogMCPHeaders(slug, token string) http.Header {
	header := bearerHeader(token)
	if slug == "github" {
		header.Set("X-MCP-Toolsets", githubMCPToolsets)
	}
	return header
}

func catalogConnectorSessionKey(slug, connectorID, token string) string {
	extra := ""
	if slug == "github" {
		extra = "gh-toolsets-v1"
	}
	sum := sha256.Sum256([]byte(token))
	key := connectorID + ":" + hex.EncodeToString(sum[:16])
	if extra != "" {
		key += ":" + extra
	}
	return key
}
