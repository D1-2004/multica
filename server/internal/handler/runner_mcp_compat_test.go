package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestInjectRunnerMCPLegacyDaemonCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name            string
		binding         bool
		inventory       string
		mounts          bool
		relayRoutes     bool
		wantMount       bool
		wantNative      bool
		wantUnsupported bool
	}{
		{name: "old daemon without bindings"},
		{name: "old daemon with empty inventory", binding: true, inventory: `{}`},
		{name: "old daemon with only direct MCP collision", binding: true, inventory: `{"direct":"fingerprint"}`},
		{name: "old daemon with dynamic mount", binding: true, inventory: `{"local-tool":"fingerprint"}`, wantUnsupported: true},
		{name: "mount-capable daemon without native relay", binding: true, inventory: `{"local-tool":"fingerprint"}`, mounts: true, wantMount: true},
		{name: "inconsistent capabilities without mounts", relayRoutes: true},
		{name: "inconsistent capabilities with mounts", binding: true, inventory: `{"local-tool":"fingerprint"}`, relayRoutes: true, wantUnsupported: true},
		{name: "current daemon retains native relay", mounts: true, relayRoutes: true, wantNative: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tx, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			agentID := parseUUID(uuid.NewString())
			if _, err := tx.Exec(ctx, `INSERT INTO agent
				(id, workspace_id, name, runtime_mode, runtime_id, owner_id)
				VALUES ($1, $2, 'Runner compatibility test', 'cloud', $3, $4)`,
				agentID, testWorkspaceID, testRuntimeID, testUserID); err != nil {
				t.Fatal(err)
			}
			runtime := db.AgentRuntime{WorkspaceID: parseUUID(testWorkspaceID), RuntimeMode: "cloud", Metadata: []byte(`{"kind":"fc-e2b","provider":"opencode"}`)}
			if tc.binding {
				machineID := uuid.NewString()
				if _, err := tx.Exec(ctx, `INSERT INTO runner_machine
					(id, owner_id, name, os, arch, public_key, client_version, last_seen_at, connection_id)
					VALUES ($1, $2, 'compat-test', 'linux', 'amd64', $3, 'test', now(), $4)`,
					machineID, testUserID, []byte(strings.ReplaceAll(uuid.NewString(), "-", "")), uuid.NewString()); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO agent_runner_binding
					(workspace_id, agent_id, machine_id, bound_by, roots, enabled_mcp_servers)
					VALUES ($1, $2, $3, $4, '[]'::jsonb, $5::jsonb)`,
					testWorkspaceID, agentID, machineID, testUserID, tc.inventory); err != nil {
					t.Fatal(err)
				}
			}
			h := &Handler{Queries: db.New(tx), DB: tx, cfg: Config{PublicURL: "https://multica.example"}}
			original := json.RawMessage(`{"mcpServers":{"direct":{"type":"http","url":"https://direct.example/mcp"}}}`)
			data := &TaskAgentData{McpConfig: append(json.RawMessage(nil), original...)}
			err = h.injectRunnerMCP(ctx, runtime, db.AgentTaskQueue{AgentID: agentID}, "test-task-token", data, tc.mounts, tc.relayRoutes)
			if tc.wantUnsupported {
				if !errors.Is(err, errRunnerMCPMountsUnsupported) {
					t.Fatalf("error = %v, want unsupported mounts", err)
				}
			} else if err != nil {
				t.Fatalf("compatible task rejected: %v", err)
			}
			if tc.wantMount {
				if !strings.Contains(string(data.McpConfig), "/servers/local-tool") {
					t.Fatalf("dynamic mount missing: %s", data.McpConfig)
				}
			} else if tc.wantNative {
				if data.McpRelayRoutes["multica"].Path != "/api/mcp" {
					t.Fatalf("native relay missing: %#v", data.McpRelayRoutes)
				}
			} else if string(data.McpConfig) != string(original) {
				t.Fatalf("legacy or rejected claim changed Agent MCP config: %s", data.McpConfig)
			}
			if !strings.Contains(string(data.McpConfig), "https://direct.example/mcp") {
				t.Fatal("Agent's direct MCP configuration was lost")
			}
			if !tc.wantNative && len(data.McpRelayRoutes) != 0 {
				t.Fatalf("legacy daemon received native relay routes: %#v", data.McpRelayRoutes)
			}
		})
	}
}
