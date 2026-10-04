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
	"net/url"
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

	// githubCredentialBoundary is appended to GitHub tool text. The sandbox
	// has a gh CLI, and a failed connector call otherwise gets retried with
	// whatever token is lying around in the environment.
	githubCredentialBoundary = "不要寻找或使用本机的 gh、git 或环境变量里的令牌。这一步失败就只报告失败。"
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
	"delete_branch":         {Needs: githubNeed("contents", "write")},

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
	return "GitHub 安装 @" + matched[0].login + " 还没有 " + strings.Join(missing, "、") + "，不能调用 " + name + "。写文件、建分支、建 PR、改 Issue 在这个账号或组织的管理员于 GitHub 安装页重新批准前不会执行。修改仓库范围不会补上这些权限。" + githubCredentialBoundary
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
	return "GitHub 拒绝了 " + name + "。" + who + " 没有 " + strings.Join(labels, "、") + "。已有安装要管理员在 GitHub 安装页重新批准后才会带上新权限。" + githubCredentialBoundary
}

// githubAgentRefusal is an ordinary tool result. The sandbox MCP bridge
// replaces an isError result's text with "MCP tool reported an error", so a
// permission explanation has to travel in content with isError left false.
func githubAgentRefusal(text string) multicaMCPToolResult {
	return multicaMCPToolResult{Content: []multicaMCPContent{{Type: "text", Text: text}}}
}

// githubPresentTools keeps the permission-filtered list and, when writes are
// on, adds delete_branch. Upstream GitHub MCP has no delete-ref tool, so the
// connector implements that one call itself.
func githubPresentTools(tools []map[string]any, access githubAccess, writeEnabled bool) []map[string]any {
	kept := make([]map[string]any, 0, len(tools)+1)
	seenDelete := false
	for _, item := range tools {
		name, _ := item["name"].(string)
		if githubToolBlockReason(name, access) != "" {
			continue
		}
		if name == "delete_branch" {
			seenDelete = true
		}
		githubAnnotateTool(item)
		kept = append(kept, item)
	}
	if !seenDelete && writeEnabled && githubToolBlockReason("delete_branch", access) == "" {
		kept = append(kept, githubDeleteBranchTool())
	}
	return kept
}

func githubAnnotateTool(item map[string]any) {
	description, _ := item["description"].(string)
	if strings.Contains(description, "不要寻找或使用本机的 gh") {
		return
	}
	if description != "" && !strings.HasSuffix(description, " ") {
		description += " "
	}
	item["description"] = description + githubCredentialBoundary
}

func githubDeleteBranchTool() map[string]any {
	return map[string]any{
		"name":        "delete_branch",
		"description": "删除一个非默认分支。delete_file 只删除文件，不会删除分支。不能删除默认分支，也不能删除 tag。 " + githubCredentialBoundary,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"owner":  map[string]any{"type": "string", "description": "仓库所有者"},
				"repo":   map[string]any{"type": "string", "description": "仓库名"},
				"branch": map[string]any{"type": "string", "description": "要删除的分支名，不含 refs/heads/"},
			},
			"required": []string{"owner", "repo", "branch"},
		},
	}
}

func githubLocalTool(slug, name string, writeEnabled bool) bool {
	return slug == "github" && writeEnabled && name == "delete_branch"
}

type githubDeleteOutcome struct {
	text string
	ok   bool
}

func (h *Handler) githubDeleteBranchCall(ctx context.Context, c *internalConnector, arguments json.RawMessage) (multicaMCPToolResult, bool) {
	token, err := h.freshConnectorToken(ctx, c, "")
	if err != nil || !contextcap.ValidBearer(token) {
		return githubAgentRefusal("暂时读不到 GitHub 连接，没有删除分支。" + githubCredentialBoundary), true
	}
	outcome := githubDeleteBranch(ctx, token, arguments)
	return githubAgentRefusal(outcome.text), !outcome.ok
}

func githubDeleteBranch(ctx context.Context, token string, arguments json.RawMessage) githubDeleteOutcome {
	owner, repo, branch, ok := githubDeleteBranchArguments(arguments)
	if !ok {
		return githubDeleteOutcome{text: "分支名或仓库名不合法，没有删除。" + githubCredentialBoundary}
	}
	defaultBranch, status, err := githubDefaultBranch(ctx, token, owner, repo)
	if err != nil || defaultBranch == "" {
		if status == http.StatusNotFound {
			return githubDeleteOutcome{text: "看不到仓库 " + owner + "/" + repo + "，没有删除分支。" + githubCredentialBoundary}
		}
		return githubDeleteOutcome{text: "没有读到这个仓库的默认分支，所以没有删除。" + githubCredentialBoundary}
	}
	if strings.EqualFold(branch, defaultBranch) {
		return githubDeleteOutcome{text: "不能删除默认分支 " + defaultBranch + "。" + githubCredentialBoundary}
	}
	code, body, err := githubREST(ctx, http.MethodDelete, githubBranchRefURL(owner, repo, branch), token)
	if err != nil {
		return githubDeleteOutcome{text: "删除分支没有完成。" + githubCredentialBoundary}
	}
	switch code {
	case http.StatusNoContent, http.StatusOK:
		return githubDeleteOutcome{
			ok:   true,
			text: "已删除 " + owner + "/" + repo + " 的分支 " + branch + "。默认分支 " + defaultBranch + " 没有动。",
		}
	case http.StatusNotFound:
		return githubDeleteOutcome{text: "分支 " + branch + " 不存在，或这个安装看不到它。没有删除别的分支。" + githubCredentialBoundary}
	case http.StatusForbidden, http.StatusUnauthorized:
		if message := githubUpstreamPermissionMessage("delete_branch", owner); message != "" {
			return githubDeleteOutcome{text: message}
		}
		return githubDeleteOutcome{text: "GitHub 没有接受这次删除。" + githubCredentialBoundary}
	default:
		if bytesContainsFold(body, "protected") {
			return githubDeleteOutcome{text: "分支 " + branch + " 受保护，没有删除。" + githubCredentialBoundary}
		}
		return githubDeleteOutcome{text: "GitHub 没有接受这次删除。" + githubCredentialBoundary}
	}
}

func githubDeleteBranchArguments(arguments json.RawMessage) (owner, repo, branch string, ok bool) {
	var payload struct {
		Owner  string `json:"owner"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if len(arguments) == 0 || json.Unmarshal(arguments, &payload) != nil {
		return "", "", "", false
	}
	owner = strings.TrimSpace(payload.Owner)
	repo = strings.TrimSpace(payload.Repo)
	branch = strings.TrimSpace(payload.Branch)
	if !githubAccountName(owner) || !githubRepoName(repo) || !githubBranchName(branch) {
		return "", "", "", false
	}
	return owner, repo, branch, true
}

func githubAccountName(name string) bool {
	if name == "" || len(name) > 39 {
		return false
	}
	for i, r := range name {
		digit := r >= '0' && r <= '9'
		letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !digit && !letter && r != '-' {
			return false
		}
		if r == '-' && (i == 0 || i == len(name)-1) {
			return false
		}
	}
	return true
}

func githubRepoName(name string) bool {
	if name == "" || len(name) > 100 || strings.HasPrefix(name, ".") || strings.HasSuffix(strings.ToLower(name), ".git") {
		return false
	}
	for _, r := range name {
		digit := r >= '0' && r <= '9'
		letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !digit && !letter && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func githubBranchName(branch string) bool {
	if branch == "" || len(branch) > 244 || strings.EqualFold(branch, "head") {
		return false
	}
	if strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasPrefix(branch, "-") {
		return false
	}
	lower := strings.ToLower(branch)
	if strings.HasPrefix(lower, "refs/") || strings.HasSuffix(lower, ".lock") || strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.Contains(branch, "@{") || strings.Contains(branch, `\`) {
		return false
	}
	for _, segment := range strings.Split(branch, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".") || strings.HasSuffix(strings.ToLower(segment), ".lock") {
			return false
		}
		for _, r := range segment {
			if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\'\"`", r) {
				return false
			}
		}
	}
	return true
}

func githubBranchRefURL(owner, repo, branch string) string {
	parts := strings.Split("heads/"+branch, "/")
	escaped := make([]string, len(parts))
	for i, part := range parts {
		escaped[i] = url.PathEscape(part)
	}
	return strings.TrimRight(githubAPIBase, "/") + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/git/refs/" + strings.Join(escaped, "/")
}

func githubDefaultBranch(ctx context.Context, token, owner, repo string) (string, int, error) {
	endpoint := strings.TrimRight(githubAPIBase, "/") + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	status, body, err := githubREST(ctx, http.MethodGet, endpoint, token)
	if err != nil {
		return "", 0, err
	}
	if status != http.StatusOK {
		return "", status, nil
	}
	var payload struct {
		DefaultBranch string `json:"default_branch"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return "", status, errors.New("github repository payload")
	}
	return strings.TrimSpace(payload.DefaultBranch), status, nil
}

func githubREST(ctx context.Context, method, endpoint, token string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	setGitHubAPIHeaders(req, token)
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func bytesContainsFold(body []byte, needle string) bool {
	return strings.Contains(strings.ToLower(string(body)), strings.ToLower(needle))
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
