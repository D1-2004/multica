package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

func TestDTALoadSmokeCLIUsesDedicatedAPI(t *testing.T) {
	const (
		token       = "dta_contract"
		workspaceID = "11111111-1111-1111-1111-111111111111"
		agentID     = "22222222-2222-2222-2222-222222222222"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/dta/load-smokes" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Workspace-ID") != workspaceID {
			t.Fatalf("workspace header = %q", r.Header.Get("X-Workspace-ID"))
		}
		var body struct {
			AgentID        string   `json:"agent_id"`
			Marker         string   `json:"marker"`
			RequiredSkills []string `json:"required_skills"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.AgentID != agentID || body.Marker != "marker-1" ||
			len(body.RequiredSkills) != 2 || body.RequiredSkills[1] != "skill-b" {
			t.Fatalf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "issue-1"})
	}))
	defer server.Close()

	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_TOKEN", token)
	t.Setenv("MULTICA_WORKSPACE_ID", workspaceID)
	cmd := &cobra.Command{}
	cmd.PersistentFlags().String("profile", "", "")
	cmd.Flags().String("agent", agentID, "")
	cmd.Flags().String("marker", "marker-1", "")
	cmd.Flags().StringArray("required-skill", []string{"skill-a", "skill-b"}, "")
	out, err := captureStdout(t, func() error { return runDTALoadSmokeCreate(cmd, nil) })
	if err != nil {
		t.Fatalf("run create: %v", err)
	}
	if out != "{\n  \"id\": \"issue-1\"\n}\n" {
		t.Fatalf("stdout = %q", out)
	}
}
