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
	for _, name := range []string{"push_files", "create_or_update_file", "delete_file", "create_branch", "create_pull_request", "merge_pull_request", "issue_read", "create_issue", "assign_copilot_to_issue", "get_teams", "create_repository", "fork_repository", "add_issue_reaction"} {
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
	for _, name := range []string{"push_files", "create_or_update_file", "create_pull_request", "merge_pull_request", "issue_read", "create_issue"} {
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
	if reason := githubToolBlockReason("push_files", access); !strings.Contains(reason, "Contents 写") {
		t.Fatalf("reason %s", reason)
	}
}
