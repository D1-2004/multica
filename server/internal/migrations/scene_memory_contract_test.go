package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSceneMemoryMigrationContract(t *testing.T) {
	t.Parallel()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	dir := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "migrations"))
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(raw)
	}

	table := read("9121_scene_memory.up.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS scene_memory",
		"workspace_id UUID NOT NULL",
		"agent_id UUID NOT NULL",
		"platform TEXT NOT NULL",
		"org_id TEXT NOT NULL",
		"scene_key TEXT NOT NULL",
		"scene_kind TEXT NOT NULL",
		"memory_text TEXT NOT NULL",
		"memory_revision BIGINT NOT NULL",
		"bootstrapped_at TIMESTAMPTZ",
		"source_cursor_at TIMESTAMPTZ",
		"dirty_revision BIGINT NOT NULL",
		"flushed_revision BIGINT NOT NULL",
		"lease_expires_at TIMESTAMPTZ",
		"last_flush_meta JSONB NOT NULL",
		"last_trigger_idempotency_key TEXT NOT NULL",
	} {
		if !strings.Contains(table, required) {
			t.Errorf("scene_memory migration missing %q", required)
		}
	}
	if strings.Contains(table, "REFERENCES ") || strings.Contains(table, "CREATE INDEX") {
		t.Fatal("scene_memory table migration must not add foreign keys or non-concurrent indexes")
	}

	for _, name := range []string{
		"9122_scene_memory_id_idx.up.sql",
		"9123_scene_memory_identity_idx.up.sql",
		"9124_scene_memory_claim_idx.up.sql",
	} {
		index := read(name)
		if !strings.Contains(index, "INDEX CONCURRENTLY IF NOT EXISTS") {
			t.Errorf("%s must build its index concurrently", name)
		}
	}

	cleanup, err := os.ReadFile(filepath.Join(dir, "..", "pkg", "db", "queries", "workspace_delete.sql"))
	if err != nil {
		t.Fatalf("read workspace_delete.sql: %v", err)
	}
	if !strings.Contains(string(cleanup), "DELETE FROM scene_memory") {
		t.Fatal("workspace delete must remove scene_memory rows")
	}

	flags := read("9125_agent_scene_memory_flags.up.sql")
	for _, required := range []string{
		"scene_memory_write_enabled BOOLEAN NOT NULL DEFAULT false",
		"scene_memory_recall_enabled BOOLEAN NOT NULL DEFAULT false",
		"scene_memory_ui_enabled BOOLEAN NOT NULL DEFAULT false",
		"scene_memory_bootstrap_enabled BOOLEAN NOT NULL DEFAULT false",
	} {
		if !strings.Contains(flags, required) {
			t.Errorf("agent scene memory flags missing %q", required)
		}
	}

	trigger := read("9126_scene_memory_last_trigger.up.sql")
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS last_trigger_at TIMESTAMPTZ",
		"ADD COLUMN IF NOT EXISTS last_trigger_evidence_id TEXT NOT NULL DEFAULT ''",
	} {
		if !strings.Contains(trigger, required) {
			t.Errorf("scene_memory last_trigger migration missing %q", required)
		}
	}
	if strings.Contains(trigger, "REFERENCES ") || strings.Contains(trigger, "CREATE INDEX") {
		t.Fatal("last_trigger migration must not add foreign keys or non-concurrent indexes")
	}

	pending := read("9127_scene_memory_pending_from.up.sql")
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS pending_from_at TIMESTAMPTZ",
		"ADD COLUMN IF NOT EXISTS pending_from_evidence_id TEXT NOT NULL DEFAULT ''",
		"ADD COLUMN IF NOT EXISTS history_resume_before TIMESTAMPTZ",
	} {
		if !strings.Contains(pending, required) {
			t.Errorf("pending_from migration missing %q", required)
		}
	}
}
