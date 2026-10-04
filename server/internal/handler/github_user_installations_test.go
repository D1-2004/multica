package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseGitHubUserInstallationsSkipsSuspendedAndUnsafeURLs(t *testing.T) {
	body := []byte(`{"installations":[
		{"id":1,"repository_selection":"all","html_url":"https://github.com/settings/installations/1","account":{"login":"dingtalk-fde","type":"User"}},
		{"id":2,"repository_selection":"selected","html_url":"https://github.com/organizations/acme/settings/installations/2","account":{"login":"acme","type":"Organization"}},
		{"id":3,"suspended_at":"2026-01-01T00:00:00Z","account":{"login":"paused","type":"Organization"}},
		{"id":4,"html_url":"javascript:alert(1)","account":{"login":"bad-url","type":"User"}},
		{"id":5,"html_url":"https://evil.example/installations/5","account":{"login":"evil","type":"User"}},
		{"id":0,"account":{"login":"zero","type":"User"}},
		{"id":6,"account":{"login":"","type":"User"}},
		{"id":7,"repository_selection":"nope","account":{"login":"not a login","type":"Bot"}}
	]}`)
	parsed, err := parseGitHubUserInstallations(body)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.rawCount != 8 || parsed.filteredCount != 4 || parsed.totalCount != nil {
		t.Fatalf("counts %+v", parsed)
	}
	views := parsed.views
	if len(views) != 4 {
		t.Fatalf("views %d: %+v", len(views), views)
	}
	if views[0].AccountLogin != "dingtalk-fde" || views[0].AccountType != "User" || views[0].RepositorySelection != "all" || views[0].SettingsURL == "" {
		t.Fatalf("user installation %+v", views[0])
	}
	if views[1].AccountLogin != "acme" || views[1].AccountType != "Organization" || views[1].RepositorySelection != "selected" {
		t.Fatalf("org installation %+v", views[1])
	}
	if views[2].AccountLogin != "bad-url" || views[2].SettingsURL != "" || views[3].AccountLogin != "evil" || views[3].SettingsURL != "" {
		t.Fatalf("unsafe urls kept: %+v %+v", views[2], views[3])
	}
	for _, view := range views {
		if view.AccountLogin == "not a login" || view.AccountLogin == "paused" || view.AccountLogin == "zero" {
			t.Fatalf("filtered installation kept: %+v", view)
		}
	}
}

func TestParseGitHubUserInstallationsUsesProviderTotal(t *testing.T) {
	parsed, err := parseGitHubUserInstallations([]byte(`{"total_count":0,"installations":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.totalCount == nil || *parsed.totalCount != 0 || parsed.filteredCount != 0 || parsed.rawCount != 0 {
		t.Fatalf("empty %+v", parsed)
	}
	parsed, err = parseGitHubUserInstallations([]byte(`{"total_count":3,"installations":[
		{"id":1,"repository_selection":"selected","account":{"login":"acme","type":"Organization"}},
		{"id":2,"suspended_at":"2026-01-01T00:00:00Z","account":{"login":"paused","type":"Organization"}},
		{"id":0,"account":{"login":"zero","type":"User"}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.totalCount == nil || *parsed.totalCount != 3 || parsed.filteredCount != 2 || len(parsed.views) != 1 {
		t.Fatalf("filtered %+v", parsed)
	}
}

func TestListGitHubUserInstallationsPagesAndStops(t *testing.T) {
	oldSize, oldCap, oldBase := githubUserInstallationPageSize, githubUserInstallationPageCap, githubAPIBase
	githubUserInstallationPageSize = 2
	githubUserInstallationPageCap = 10
	t.Cleanup(func() {
		githubUserInstallationPageSize, githubUserInstallationPageCap, githubAPIBase = oldSize, oldCap, oldBase
	})
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghu_test" {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		pages = append(pages, r.URL.Query().Get("page"))
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{"installations":[{"id":1,"repository_selection":"selected","html_url":"https://github.com/settings/installations/1","account":{"login":"dingtalk-fde","type":"User"}},{"id":2,"repository_selection":"all","html_url":"https://github.com/organizations/acme/settings/installations/2","account":{"login":"acme","type":"Organization"}}]}`))
		default:
			_, _ = w.Write([]byte(`{"installations":[{"id":1,"repository_selection":"selected","account":{"login":"dingtalk-fde","type":"User"}}]}`))
		}
	}))
	defer srv.Close()
	githubAPIBase = srv.URL

	out, err := listGitHubUserInstallations(t.Context(), "ghu_test")
	if err != nil {
		t.Fatal(err)
	}
	if out.Error != "" || out.Truncated || !out.Connected {
		t.Fatalf("response %+v", out)
	}
	if len(out.Installations) != 2 || out.Installations[1].AccountLogin != "acme" {
		t.Fatalf("installations %+v", out.Installations)
	}
	if out.TotalCount == nil || *out.TotalCount != 3 || out.FilteredCount == nil || *out.FilteredCount != 0 {
		t.Fatalf("counts total=%v filtered=%v", out.TotalCount, out.FilteredCount)
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Fatalf("pages %v", pages)
	}
}

func TestListGitHubUserInstallationsAuthErrors(t *testing.T) {
	oldBase := githubAPIBase
	t.Cleanup(func() { githubAPIBase = oldBase })
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, githubInstallationsReconnect},
		{http.StatusForbidden, githubInstallationsNotAppToken},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"token ghu_secret should not leak"}`))
			}))
			defer srv.Close()
			githubAPIBase = srv.URL
			out, err := listGitHubUserInstallations(t.Context(), "ghu_secret")
			if err != nil {
				t.Fatal(err)
			}
			if out.Error != tc.want || len(out.Installations) != 0 || !out.Connected || out.TotalCount != nil || out.FilteredCount != nil {
				t.Fatalf("response %+v", out)
			}
			if calls != 1 {
				t.Fatalf("calls %d", calls)
			}
		})
	}
}

func TestListGitHubUserInstallationsDoesNotFollowRedirectsOrLeakBodies(t *testing.T) {
	oldBase := githubAPIBase
	t.Cleanup(func() { githubAPIBase = oldBase })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://evil.example/steal")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte("provider-secret-body"))
	}))
	defer srv.Close()
	githubAPIBase = srv.URL
	out, err := listGitHubUserInstallations(t.Context(), "ghu_secret")
	if err == nil || strings.Contains(err.Error(), "provider-secret-body") || strings.Contains(err.Error(), "ghu_secret") {
		t.Fatalf("err %v response %+v", err, out)
	}
}

func TestListGitHubUserInstallationsTruncatesAtThePageCap(t *testing.T) {
	oldSize, oldCap, oldBase := githubUserInstallationPageSize, githubUserInstallationPageCap, githubAPIBase
	githubUserInstallationPageSize = 1
	githubUserInstallationPageCap = 1
	t.Cleanup(func() {
		githubUserInstallationPageSize, githubUserInstallationPageCap, githubAPIBase = oldSize, oldCap, oldBase
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"installations":[{"id":9,"repository_selection":"all","account":{"login":"acme","type":"Organization"}}]}`))
	}))
	defer srv.Close()
	githubAPIBase = srv.URL
	out, err := listGitHubUserInstallations(t.Context(), "ghu_test")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || len(out.Installations) != 1 {
		t.Fatalf("response %+v", out)
	}
	if out.TotalCount == nil || *out.TotalCount != 1 || out.FilteredCount == nil || *out.FilteredCount != 0 {
		t.Fatalf("counts total=%v filtered=%v", out.TotalCount, out.FilteredCount)
	}
}

func TestListGitHubUserInstallationsKeepsProviderTotalAndFilteredCount(t *testing.T) {
	oldBase := githubAPIBase
	t.Cleanup(func() { githubAPIBase = oldBase })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"total_count":2,"installations":[
			{"id":1,"repository_selection":"all","account":{"login":"acme","type":"Organization"}},
			{"id":2,"suspended_at":"2026-01-01T00:00:00Z","account":{"login":"paused","type":"Organization"}}
		]}`))
	}))
	defer srv.Close()
	githubAPIBase = srv.URL
	out, err := listGitHubUserInstallations(t.Context(), "ghu_test")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if out.TotalCount == nil || *out.TotalCount != 2 || out.FilteredCount == nil || *out.FilteredCount != 1 || len(out.Installations) != 1 {
		t.Fatalf("response %+v", out)
	}
	if !strings.Contains(string(encoded), `"total_count":2`) || !strings.Contains(string(encoded), `"filtered_count":1`) {
		t.Fatalf("json %s", encoded)
	}
}

func TestListGitHubUserInstallationsWritesZeroCounts(t *testing.T) {
	oldBase := githubAPIBase
	t.Cleanup(func() { githubAPIBase = oldBase })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"total_count":0,"installations":[]}`))
	}))
	defer srv.Close()
	githubAPIBase = srv.URL
	out, err := listGitHubUserInstallations(t.Context(), "ghu_test")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"total_count":0`) || !strings.Contains(string(encoded), `"filtered_count":0`) || !strings.Contains(string(encoded), `"installations":[]`) {
		t.Fatalf("json %s", encoded)
	}
}

func TestParseGitHubInstallationRepositoriesDropsUnsafeNames(t *testing.T) {
	repos, total, raw, err := parseGitHubInstallationRepositories([]byte(`{"total_count":4,"repositories":[
		{"full_name":"acme/one","private":false},
		{"full_name":"acme/two","private":true},
		{"full_name":"acme/three","private":false},
		{"full_name":"javascript:alert(1)","private":true},
		{"full_name":"../secret","private":true}
	]}`))
	if err != nil || total == nil || *total != 4 || raw != 5 || len(repos) != 3 {
		t.Fatalf("parsed %+v total %v raw %d err %v", repos, total, raw, err)
	}
	if repos[0].FullName != "acme/one" || repos[1].FullName != "acme/two" || !repos[1].Private || repos[2].FullName != "acme/three" {
		t.Fatalf("repos %+v", repos)
	}
}

func TestAttachGitHubInstallationRepositoriesLeavesAFailedRowEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Bearer ghu_test") {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/user/installations/7/repositories":
			_, _ = w.Write([]byte(`{"total_count":3,"repositories":[
				{"full_name":"acme/one","private":false},
				{"full_name":"acme/two","private":true},
				{"full_name":"acme/three","private":false}
			]}`))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()
	oldBase := githubAPIBase
	t.Cleanup(func() { githubAPIBase = oldBase })
	githubAPIBase = srv.URL
	views := []githubUserInstallationView{{ID: 7, Repositories: emptyGitHubRepos()}, {ID: 8, Repositories: emptyGitHubRepos()}}
	attachGitHubInstallationRepositories(t.Context(), "ghu_test", views)
	if views[0].RepositoryCount != 3 || views[0].RepositoriesTruncated || len(views[0].Repositories) != 3 || views[0].Repositories[1].FullName != "acme/two" {
		t.Fatalf("selected %+v", views[0])
	}
	if views[1].RepositoryCount != 0 || len(views[1].Repositories) != 0 {
		t.Fatalf("failed row %+v", views[1])
	}
}
