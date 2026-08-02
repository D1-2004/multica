package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestWorkspaceAccessProfileUsesSelfWithoutHumanEndpoints(t *testing.T) {
	const (
		token       = "dta_test_workspace_access"
		workspaceID = "11111111-1111-1111-1111-111111111111"
		tokenID     = "22222222-2222-2222-2222-222222222222"
	)
	requests := make([]string, 0, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/workspace-access/self" {
			t.Fatalf("workspace access profile called human endpoint: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"principal_type": "workspace_access_token",
			"token_id":       tokenID,
			"name":           "Vendor A",
			"workspace_id":   workspaceID,
			"workspace": map[string]any{
				"id": workspaceID, "name": "Acme", "slug": "acme", "issue_prefix": "ACM",
			},
			"capabilities":   []string{"deployment.manage"},
			"resource_scope": "own_agents",
			"version":        1,
		})
	}))
	defer srv.Close()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "")
	t.Setenv("MULTICA_WORKSPACE_ID", "")

	login := testCmd()
	login.Flags().String("profile", "vendor-a", "")
	if err := runAuthLoginToken(login, token); err != nil {
		t.Fatalf("runAuthLoginToken: %v", err)
	}
	cfg, err := cli.LoadCLIConfigForProfile("vendor-a")
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if cfg.Token != token || cfg.WorkspaceID != workspaceID || cfg.ServerURL != srv.URL {
		t.Fatalf("profile = %#v", cfg)
	}

	status := testCmd()
	status.Flags().String("profile", "vendor-a", "")
	stderr := captureStderr(t)
	if err := runAuthStatus(status, nil); err != nil {
		t.Fatalf("runAuthStatus: %v", err)
	}
	statusOut := stderr.read()
	if !strings.Contains(statusOut, "User:    Vendor A (workspace access token "+tokenID+")") {
		t.Fatalf("auth status = %q", statusOut)
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	workspaces, err := fetchWorkspaces(ctx, status)
	if err != nil {
		t.Fatalf("fetchWorkspaces: %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].ID != workspaceID || workspaces[0].Slug != "acme" {
		t.Fatalf("workspaces = %#v", workspaces)
	}

	getCmd := testCmd()
	getCmd.Flags().String("output", "json", "")
	getCmd.Flags().String("profile", "vendor-a", "")
	out, err := captureStdout(t, func() error { return runWorkspaceGet(getCmd, []string{workspaceID}) })
	if err != nil {
		t.Fatalf("runWorkspaceGet: %v", err)
	}
	if !strings.Contains(out, `"id": "`+workspaceID+`"`) || !strings.Contains(out, `"slug": "acme"`) {
		t.Fatalf("workspace get = %q", out)
	}

	for _, request := range requests {
		if strings.Contains(request, "/api/me") || request == "GET /api/workspaces" || strings.HasPrefix(request, "GET /api/workspaces/") {
			t.Fatalf("workspace access profile reached human workspace API: %s", request)
		}
	}
}
