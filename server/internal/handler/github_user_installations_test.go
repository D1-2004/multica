package handler

import (
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
	views, raw, err := parseGitHubUserInstallations(body)
	if err != nil {
		t.Fatal(err)
	}
	if raw != 8 {
		t.Fatalf("raw count %d", raw)
	}
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
			if out.Error != tc.want || len(out.Installations) != 0 || !out.Connected {
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
}
