package handler

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestGitHubToolBlockReasonMatchesCurrentApp(t *testing.T) {
	current := githubAccess{kind: githubGrantApp, ceiling: map[string]string{
		"contents": "read", "metadata": "read", "pull_requests": "read",
	}}
	for _, name := range []string{"get_file_contents", "pull_request_read", "get_me", "search_users", "search_repositories", "list_repository_collaborators"} {
		if reason := githubToolBlockReason(name, current); reason != "" {
			t.Fatalf("%s should be callable: %s", name, reason)
		}
	}
	for _, name := range []string{"push_files", "create_or_update_file", "delete_file", "create_branch", "delete_branch", "create_pull_request", "merge_pull_request", "issue_read", "create_issue", "assign_copilot_to_issue", "get_teams", "create_repository", "fork_repository", "add_issue_reaction"} {
		reason := githubToolBlockReason(name, current)
		if reason == "" {
			t.Fatalf("%s should be blocked", name)
		}
		if name == "push_files" && !strings.Contains(reason, "Contents 写") {
			t.Fatalf("push_files reason = %s", reason)
		}
		if name == "create_pull_request" && !strings.Contains(reason, "Pull requests 写") {
			t.Fatalf("create_pull_request reason = %s", reason)
		}
		if name == "issue_read" && !strings.Contains(reason, "Issues 读") {
			t.Fatalf("issue_read reason = %s", reason)
		}
		if name == "assign_copilot_to_issue" && !strings.Contains(reason, "已经从工具列表隐藏") {
			t.Fatalf("hidden tool reason = %s", reason)
		}
	}
}

func TestGitHubToolBlockReasonAfterReapproval(t *testing.T) {
	granted := githubAccess{kind: githubGrantApp, ceiling: map[string]string{
		"contents": "write", "metadata": "read", "pull_requests": "write", "issues": "write",
	}}
	for _, name := range []string{"push_files", "create_or_update_file", "delete_branch", "create_pull_request", "merge_pull_request", "issue_read", "create_issue"} {
		if reason := githubToolBlockReason(name, granted); reason != "" {
			t.Fatalf("%s still blocked: %s", name, reason)
		}
	}
	if reason := githubToolBlockReason("merge_pull_request", githubAccess{kind: githubGrantApp, ceiling: map[string]string{
		"contents": "read", "pull_requests": "write", "metadata": "read", "issues": "write",
	}}); !strings.Contains(reason, "Contents 写") {
		t.Fatalf("merge without contents write: %s", reason)
	}
	if reason := githubToolBlockReason("push_files", githubAccess{kind: githubGrantPAT}); reason != "" {
		t.Fatalf("PAT write blocked: %s", reason)
	}
	if reason := githubToolBlockReason("push_files", githubAccess{kind: githubGrantUnknown}); !strings.Contains(reason, "暂时没有读到") {
		t.Fatalf("unknown grant: %s", reason)
	}
	if reason := githubToolBlockReason("get_file_contents", githubAccess{kind: githubGrantUnknown}); reason != "" {
		t.Fatalf("unknown grant hid a read: %s", reason)
	}
}

func TestGitHubPermissionCeilingTakesTheHighestGrant(t *testing.T) {
	ceiling := githubPermissionCeiling([]map[string]string{
		{"contents": "read", "metadata": "read"},
		{"contents": "write", "issues": "read", "pull_requests": "write"},
	})
	if ceiling["contents"] != "write" || ceiling["issues"] != "read" || ceiling["pull_requests"] != "write" || ceiling["metadata"] != "read" {
		t.Fatalf("ceiling = %#v", ceiling)
	}
}

func TestGitHubPinnedToolsKeepWritesAndDropUnused(t *testing.T) {
	names := []string{
		"get_me", "get_teams", "get_team_members", "assign_copilot_to_issue", "request_copilot_review",
		"create_repository", "fork_repository", "push_files", "create_or_update_file", "get_file_contents",
		"create_pull_request", "pull_request_read", "issue_read", "create_issue", "add_issue_reaction",
	}
	discovered := make([]discoveredConnectorTool, 0, len(names))
	readOnly := map[string]bool{"get_me": true, "get_teams": true, "get_team_members": true, "get_file_contents": true, "pull_request_read": true, "issue_read": true}
	for _, name := range names {
		discovered = append(discovered, discoveredConnectorTool{Name: name, ReadOnly: readOnly[name]})
	}
	pinned := pinnedCatalogToolsForSlug("github", discovered, true)
	if len(pinned) > maxPinnedConnectorTools {
		t.Fatalf("pinned %d", len(pinned))
	}
	for _, want := range []string{"push_files", "create_or_update_file", "create_pull_request", "get_file_contents", "issue_read", "get_me"} {
		if !slices.Contains(pinned, want) {
			t.Fatalf("missing %s in %v", want, pinned)
		}
	}
	for _, hidden := range []string{"assign_copilot_to_issue", "get_teams", "create_repository", "fork_repository", "add_issue_reaction"} {
		if slices.Contains(pinned, hidden) {
			t.Fatalf("kept %s", hidden)
		}
	}
	if len(githubMCPTools) > maxPinnedConnectorTools {
		t.Fatalf("product set %d exceeds pin cap", len(githubMCPTools))
	}
}

func TestMissingGitHubWrites(t *testing.T) {
	if missing := missingGitHubWrites(nil); missing != nil {
		t.Fatalf("omitted permissions: %#v", missing)
	}
	if missing := missingGitHubWrites([]byte("null")); missing != nil {
		t.Fatalf("null permissions: %#v", missing)
	}
	missing := missingGitHubWrites([]byte(`{"contents":"read","metadata":"read","pull_requests":"read"}`))
	if strings.Join(missing, ",") != "contents:write,pull_requests:write,issues:write" {
		t.Fatalf("missing = %#v", missing)
	}
	if missing := missingGitHubWrites([]byte(`{"contents":"write","pull_requests":"admin","issues":"write","metadata":"read"}`)); missing != nil {
		t.Fatalf("full grant still missing %#v", missing)
	}
}

func TestParseGitHubUserInstallationsReportsMissingWrites(t *testing.T) {
	body := []byte(`{"installations":[
		{"id":1,"repository_selection":"all","html_url":"https://github.com/settings/installations/1","permissions":{"contents":"read","metadata":"read","pull_requests":"read"},"account":{"login":"xdxer","type":"User"}},
		{"id":2,"repository_selection":"selected","permissions":{"contents":"write","metadata":"read","pull_requests":"write","issues":"write"},"account":{"login":"acme","type":"Organization"}}
	]}`)
	parsed, err := parseGitHubUserInstallations(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(parsed.views[0].MissingPermissions, ",") != "contents:write,pull_requests:write,issues:write" {
		t.Fatalf("read-only install %#v", parsed.views[0].MissingPermissions)
	}
	if parsed.views[1].MissingPermissions != nil {
		t.Fatalf("approved install %#v", parsed.views[1].MissingPermissions)
	}
}

func TestLoadGitHubGrantCeiling(t *testing.T) {
	githubGrantCache.mu.Lock()
	githubGrantCache.entries = map[string]githubAccess{}
	githubGrantCache.mu.Unlock()
	t.Cleanup(func() {
		githubGrantCache.mu.Lock()
		githubGrantCache.entries = map[string]githubAccess{}
		githubGrantCache.mu.Unlock()
	})
	oldBase := githubAPIBase
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghu_grant" {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"installations":[{"id":9,"permissions":{"contents":"read","metadata":"read","pull_requests":"read"},"account":{"login":"xdxer","type":"User"}}]}`))
	}))
	defer srv.Close()
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = oldBase })

	access := loadGitHubGrantCeiling(t.Context(), "ghu_grant")
	if access.kind != githubGrantApp || access.ceiling["contents"] != "read" || access.ceiling["issues"] != "" {
		t.Fatalf("access %+v", access)
	}
	if len(access.installs) != 1 || access.installs[0].login != "xdxer" {
		t.Fatalf("installs %+v", access.installs)
	}
	if reason := githubToolBlockReason("push_files", access); !strings.Contains(reason, "Contents 写") {
		t.Fatalf("reason %s", reason)
	}
}

func TestGitHubOwnerBlockNamesTheInstallation(t *testing.T) {
	access := githubAccess{
		kind: githubGrantApp,
		ceiling: map[string]string{
			"contents": "write", "metadata": "read", "pull_requests": "write", "issues": "write",
		},
		installs: []githubInstallGrant{
			{login: "xdxer", permissions: map[string]string{"contents": "write", "metadata": "read", "pull_requests": "write", "issues": "write"}},
			{login: "dingtalk-fde", permissions: map[string]string{"contents": "read", "metadata": "read", "pull_requests": "read"}},
		},
	}
	approved := githubOwnerBlockReason("create_branch", []byte(`{"owner":"xdxer","repo":"dsh-github-agent-lab","branch":"e2e"}`), access)
	if approved != "" {
		t.Fatalf("approved install blocked: %s", approved)
	}
	denied := githubOwnerBlockReason("create_branch", []byte(`{"owner":"DingTalk-FDE","repo":"dingtalk-workforce-harness"}`), access)
	if !strings.Contains(denied, "@dingtalk-fde") || !strings.Contains(denied, "Contents 写") || !strings.Contains(denied, "重新批准") {
		t.Fatalf("unapproved install reason = %s", denied)
	}
	if githubOwnerBlockReason("get_file_contents", []byte(`{"owner":"dingtalk-fde","path":"README.md"}`), access) != "" {
		t.Fatal("read on an unapproved install should stay allowed")
	}
	if githubOwnerBlockReason("create_branch", []byte(`{"repo":"dingtalk-workforce-harness"}`), access) != "" {
		t.Fatal("missing owner must fall through to the ceiling check")
	}
	refusal := githubAgentRefusal(denied)
	if refusal.IsError || len(refusal.Content) != 1 || refusal.Content[0].Text != denied {
		t.Fatalf("refusal must keep the text and leave isError false: %+v", refusal)
	}
	upstream := githubUpstreamPermissionMessage("create_branch", "dingtalk-fde")
	if !strings.Contains(upstream, "@dingtalk-fde") || !strings.Contains(upstream, "Contents 写") || !strings.Contains(upstream, "不要寻找或使用本机的 gh") {
		t.Fatalf("upstream message = %s", upstream)
	}
	if !strings.Contains(denied, "不要寻找或使用本机的 gh") {
		t.Fatalf("owner block missing credential boundary: %s", denied)
	}
}

func TestGitHubPresentToolsAddsDeleteBranchAndBoundary(t *testing.T) {
	write := githubAccess{kind: githubGrantApp, ceiling: map[string]string{"contents": "write", "metadata": "read"}}
	tools := []map[string]any{
		{"name": "get_file_contents", "description": "Read a file."},
		{"name": "create_branch", "description": "Create a branch."},
	}
	shown := githubPresentTools(tools, write, true)
	var names []string
	for _, item := range shown {
		name, _ := item["name"].(string)
		names = append(names, name)
		description, _ := item["description"].(string)
		if !strings.Contains(description, "不要寻找或使用本机的 gh") {
			t.Fatalf("%s description = %s", name, description)
		}
	}
	if !slices.Contains(names, "delete_branch") || !slices.Contains(names, "create_branch") || !slices.Contains(names, "get_file_contents") {
		t.Fatalf("shown = %v", names)
	}
	off := githubPresentTools([]map[string]any{{"name": "create_branch", "description": "Create a branch."}}, write, false)
	for _, item := range off {
		if item["name"] == "delete_branch" {
			t.Fatal("writes disabled still offered delete_branch")
		}
	}
	read := githubAccess{kind: githubGrantApp, ceiling: map[string]string{"contents": "read", "metadata": "read"}}
	hidden := githubPresentTools(tools, read, true)
	for _, item := range hidden {
		name, _ := item["name"].(string)
		if name == "create_branch" || name == "delete_branch" {
			t.Fatalf("read-only ceiling kept %s", name)
		}
	}
}

func TestGitHubDeleteBranchRefusesDefaultAndBadNames(t *testing.T) {
	oldBase := githubAPIBase
	deleted := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghu_test" {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		if r.Method == http.MethodDelete {
			deleted++
			t.Errorf("deleted %s", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/repos/xdxer/dsh-github-agent-lab" {
			_, _ = w.Write([]byte(`{"default_branch":"main"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = oldBase })

	defaultBranch := githubDeleteBranch(t.Context(), "ghu_test", []byte(`{"owner":"xdxer","repo":"dsh-github-agent-lab","branch":"MAIN"}`))
	if defaultBranch.ok || !strings.Contains(defaultBranch.text, "不能删除默认分支 main") || !strings.Contains(defaultBranch.text, "不要寻找或使用本机的 gh") {
		t.Fatalf("default branch = %+v", defaultBranch)
	}
	for _, raw := range []string{
		`{"owner":"xdxer","repo":"dsh-github-agent-lab","branch":"refs/heads/main"}`,
		`{"owner":"xdxer","repo":"dsh-github-agent-lab","branch":"../main"}`,
		`{"owner":"../xdxer","repo":"dsh-github-agent-lab","branch":"e2e"}`,
		`{"owner":"xdxer","repo":"dsh-github-agent-lab","branch":""}`,
	} {
		bad := githubDeleteBranch(t.Context(), "ghu_test", []byte(raw))
		if bad.ok || !strings.Contains(bad.text, "不合法") {
			t.Fatalf("bad name %s -> %+v", raw, bad)
		}
	}
	if deleted != 0 {
		t.Fatalf("deleted %d times", deleted)
	}
}

func TestGitHubDeleteBranchRemovesFeatureBranch(t *testing.T) {
	oldBase := githubAPIBase
	deleted := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghu_test" {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/xdxer/dsh-github-agent-lab":
			_, _ = w.Write([]byte(`{"default_branch":"codex/agent-body"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/xdxer/dsh-github-agent-lab/git/refs/heads/e2e-pri104-2113":
			deleted = r.URL.RequestURI()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/xdxer/dsh-github-agent-lab/git/refs/heads/feature/e2e":
			deleted = r.URL.Path
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Cannot delete this protected branch"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = oldBase })

	removed := githubDeleteBranch(t.Context(), "ghu_test", []byte(`{"owner":"xdxer","repo":"dsh-github-agent-lab","branch":"e2e-pri104-2113"}`))
	if !removed.ok || !strings.Contains(removed.text, "已删除 xdxer/dsh-github-agent-lab 的分支 e2e-pri104-2113") || !strings.Contains(removed.text, "默认分支 codex/agent-body") {
		t.Fatalf("removed = %+v", removed)
	}
	if deleted != "/repos/xdxer/dsh-github-agent-lab/git/refs/heads/e2e-pri104-2113" {
		t.Fatalf("deleted %s", deleted)
	}
	protected := githubDeleteBranch(t.Context(), "ghu_test", []byte(`{"owner":"xdxer","repo":"dsh-github-agent-lab","branch":"feature/e2e"}`))
	if protected.ok || !strings.Contains(protected.text, "受保护") || !strings.Contains(protected.text, "没有删除") {
		t.Fatalf("protected = %+v", protected)
	}
}
