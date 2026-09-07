package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunnerMCPRawConfigMigrationContract(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	dir := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "migrations"))
	raw, err := os.ReadFile(filepath.Join(dir, "9129_runner_mcp_raw_config.up.sql"))
	if err != nil {
		t.Fatalf("read Runner MCP raw-config migration: %v", err)
	}
	up := string(raw)
	for _, required := range []string{"CREATE TABLE IF NOT EXISTS runner_mcp_config", "config BYTEA NOT NULL", "revision TEXT NOT NULL", "updated_at TIMESTAMPTZ"} {
		if !strings.Contains(up, required) {
			t.Errorf("Runner MCP raw-config migration missing %q", required)
		}
	}
	if strings.Contains(up, "REFERENCES ") || strings.Contains(up, "CREATE INDEX") {
		t.Fatal("Runner MCP raw-config migration must not add foreign keys or indexes")
	}
	indexRaw, err := os.ReadFile(filepath.Join(dir, "9130_runner_mcp_config_machine_idx.up.sql"))
	if err != nil || !strings.Contains(string(indexRaw), "CREATE UNIQUE INDEX CONCURRENTLY") {
		t.Fatal("Runner MCP machine index must be built concurrently")
	}
	primaryRaw, err := os.ReadFile(filepath.Join(dir, "9131_runner_mcp_config_primary_key.up.sql"))
	if err != nil || !strings.Contains(string(primaryRaw), "PRIMARY KEY USING INDEX") {
		t.Fatal("Runner MCP primary key must attach the concurrent unique index")
	}
}
