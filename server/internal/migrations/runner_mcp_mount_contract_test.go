package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunnerMCPMountMigrationContract(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	dir := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "migrations"))
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(raw)
	}

	up := read("9128_runner_mcp_mounts.up.sql")
	for _, required := range []string{
		"ALTER COLUMN workspace_id DROP NOT NULL",
		"ALTER COLUMN agent_id DROP NOT NULL",
		"enabled_mcp_servers JSONB NOT NULL DEFAULT '{}'::jsonb",
		"jsonb_typeof(enabled_mcp_servers) = 'object'",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("Runner MCP mount migration missing %q", required)
		}
	}
	if strings.Contains(up, "CREATE TABLE") || strings.Contains(up, "REFERENCES ") || strings.Contains(up, "CREATE INDEX") {
		t.Fatal("Runner MCP mount migration must not add tables, foreign keys, or indexes")
	}

	down := read("9128_runner_mcp_mounts.down.sql")
	for _, required := range []string{
		"DELETE FROM runner_pairing_session WHERE workspace_id IS NULL OR agent_id IS NULL",
		"ALTER COLUMN workspace_id SET NOT NULL",
		"ALTER COLUMN agent_id SET NOT NULL",
		"DROP COLUMN IF EXISTS enabled_mcp_servers",
	} {
		if !strings.Contains(down, required) {
			t.Errorf("Runner MCP mount rollback missing %q", required)
		}
	}
}
