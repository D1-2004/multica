package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func addTemplateTestFlags(cmd *cobra.Command) {
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("token", "", "")
}

func templateTestEnv(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	server := httptest.NewServer(handler)
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	return server
}

func TestAgentTemplateCommandsUseWorkspaceTemplatePaths(t *testing.T) {
	var requests []string
	server := templateTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/agent-templates") {
			_ = json.NewEncoder(w).Encode(map[string]any{"templates": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"slug": "fde-agent", "template": map[string]any{"slug": "fde-agent"}})
	})
	defer server.Close()

	list := &cobra.Command{Use: "list"}
	addTemplateTestFlags(list)
	list.Flags().String("output", "json", "")
	if err := runAgentTemplateList(list, nil); err != nil {
		t.Fatal(err)
	}
	get := &cobra.Command{Use: "get"}
	addTemplateTestFlags(get)
	get.Flags().String("output", "json", "")
	if err := runAgentTemplateGet(get, []string{"fde-agent"}); err != nil {
		t.Fatal(err)
	}
	sync := &cobra.Command{Use: "sync"}
	addTemplateTestFlags(sync)
	sync.Flags().String("output", "json", "")
	if err := runAgentTemplateSync(sync, []string{"fde-agent"}); err != nil {
		t.Fatal(err)
	}
	del := &cobra.Command{Use: "delete"}
	addTemplateTestFlags(del)
	if err := runAgentTemplateDelete(del, []string{"fde-agent"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/workspaces/ws-123/agent-templates",
		"GET /api/workspaces/ws-123/agent-templates/fde-agent",
		"POST /api/workspaces/ws-123/agent-templates/fde-agent/sync",
		"DELETE /api/workspaces/ws-123/agent-templates/fde-agent",
	}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestRunAgentTemplateCreateFromGitAndCreateAgentBodies(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	server := templateTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"slug": "fde-agent", "agent_id": "agent-1", "template_slug": "fde-agent"})
	})
	defer server.Close()
	createGit := &cobra.Command{Use: "create-from-git"}
	addTemplateTestFlags(createGit)
	createGit.Flags().String("installation-id", "", "")
	createGit.Flags().String("repository", "", "")
	createGit.Flags().String("ref", "", "")
	createGit.Flags().String("output", "json", "")
	_ = createGit.Flags().Set("installation-id", "install-1")
	_ = createGit.Flags().Set("repository", "owner/repo")
	_ = createGit.Flags().Set("ref", "main")
	if err := runAgentTemplateCreateFromGit(createGit, []string{"custom"}); err != nil {
		t.Fatal(err)
	}
	createAgent := &cobra.Command{Use: "create-from-template"}
	addTemplateTestFlags(createAgent)
	createAgent.Flags().String("runtime-id", "", "")
	createAgent.Flags().String("name", "", "")
	createAgent.Flags().String("description", "", "")
	createAgent.Flags().String("output", "json", "")
	_ = createAgent.Flags().Set("runtime-id", "runtime-1")
	_ = createAgent.Flags().Set("name", "My Agent")
	if err := runAgentCreateFromTemplate(createAgent, []string{"fde-agent"}); err != nil {
		t.Fatal(err)
	}
	if paths[0] != "/api/workspaces/ws-123/agent-templates/github" || paths[1] != "/api/workspaces/ws-123/agent-templates/fde-agent/agents" {
		t.Fatalf("paths = %#v", paths)
	}
	if bodies[0]["slug"] != "custom" || bodies[0]["repository"] != "owner/repo" || bodies[1]["runtime_id"] != "runtime-1" || bodies[1]["name"] != "My Agent" {
		t.Fatalf("bodies = %#v", bodies)
	}
}
