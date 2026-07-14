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

func TestAgentTemplateCommandsRegisteredWithoutModelFlags(t *testing.T) {
	for _, path := range [][]string{{"template", "list"}, {"template", "get"}, {"create-from-template"}} {
		cmd, _, err := agentCmd.Find(path)
		if err != nil || cmd == nil {
			t.Fatalf("agent command %v not registered: %v", path, err)
		}
	}
	for _, name := range []string{"model", "thinking-level"} {
		if flag := agentCreateFromTemplateCmd.Flags().Lookup(name); flag != nil {
			t.Fatalf("create-from-template unexpectedly exposes --%s", name)
		}
	}
}

func TestRunAgentTemplateListAndGet(t *testing.T) {
	var paths []string
	server := templateTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/git-agent-templates") {
			_ = json.NewEncoder(w).Encode(map[string]any{"templates": []map[string]any{{"key": "factory/default", "available": true}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"key": "factory/default", "available": true})
	})
	defer server.Close()

	list := &cobra.Command{Use: "list"}
	addTemplateTestFlags(list)
	list.Flags().String("output", "json", "")
	if err := runAgentTemplateList(list, nil); err != nil {
		t.Fatalf("runAgentTemplateList: %v", err)
	}

	get := &cobra.Command{Use: "get"}
	addTemplateTestFlags(get)
	get.Flags().String("output", "json", "")
	if err := runAgentTemplateGet(get, []string{"factory/default"}); err != nil {
		t.Fatalf("runAgentTemplateGet: %v", err)
	}

	if len(paths) != 2 || paths[0] != "/api/workspaces/ws-123/git-agent-templates" || paths[1] != "/api/workspaces/ws-123/git-agent-templates/factory%2Fdefault" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestRunAgentCreateFromTemplateSendsOnlyCreationOverrides(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	server := templateTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.EscapedPath()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"agent_id": "agent-1", "template_key": "factory/default"})
	})
	defer server.Close()

	cmd := &cobra.Command{Use: "create-from-template"}
	addTemplateTestFlags(cmd)
	cmd.Flags().String("runtime-id", "", "")
	cmd.Flags().String("name", "", "")
	cmd.Flags().String("description", "", "")
	cmd.Flags().String("output", "json", "")
	_ = cmd.Flags().Set("runtime-id", "runtime-1")
	_ = cmd.Flags().Set("name", "My factory agent")
	_ = cmd.Flags().Set("description", "Locally owned description")

	if err := runAgentCreateFromTemplate(cmd, []string{"factory/default"}); err != nil {
		t.Fatalf("runAgentCreateFromTemplate: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/workspaces/ws-123/git-agent-templates/factory%2Fdefault/agents" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotBody["runtime_id"] != "runtime-1" || gotBody["name"] != "My factory agent" || gotBody["description"] != "Locally owned description" {
		t.Fatalf("body = %#v", gotBody)
	}
	for _, forbidden := range []string{"model", "thinking_level", "repository", "ref", "resolved_sha", "installation_id"} {
		if _, exists := gotBody[forbidden]; exists {
			t.Errorf("body unexpectedly contains %q: %#v", forbidden, gotBody)
		}
	}
}

func TestRunAgentTemplateListUsesTaskTokenInAgentExecutionContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_AGENT_ID", "agent-123")
	t.Setenv("MULTICA_TASK_ID", "task-123")
	t.Setenv("MULTICA_TOKEN", "mat_task-token")
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"templates": []any{}})
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)

	cmd := &cobra.Command{Use: "list"}
	addTemplateTestFlags(cmd)
	cmd.Flags().String("output", "json", "")
	if err := runAgentTemplateList(cmd, nil); err != nil {
		t.Fatalf("runAgentTemplateList: %v", err)
	}
	if authorization != "Bearer mat_task-token" {
		t.Fatalf("Authorization = %q, want task token", authorization)
	}
}
