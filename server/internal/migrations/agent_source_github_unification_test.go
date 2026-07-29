package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentSourceGitHubUnificationMigration(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	migrationsDir := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "migrations"))
	up, err := os.ReadFile(filepath.Join(migrationsDir, "252_unify_agent_source_github.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(filepath.Join(migrationsDir, "252_unify_agent_source_github.down.sql"))
	if err != nil {
		t.Fatal(err)
	}

	upSQL := string(up)
	for _, fragment := range []string{
		"SET source_type = 'github'",
		"WHERE source_type = 'managed_git'",
		"managed_source_key IS NOT NULL",
		"CHECK (source_type IN ('github'))",
		"WHERE managed_source_key IS NOT NULL",
	} {
		if !strings.Contains(upSQL, fragment) {
			t.Errorf("up migration missing %q", fragment)
		}
	}

	downSQL := string(down)
	for _, fragment := range []string{
		"SET source_type = 'managed_git'",
		"WHERE source_type = 'github'",
		"managed_source_key IS NOT NULL",
		"CHECK (source_type IN ('github', 'managed_git'))",
	} {
		if !strings.Contains(downSQL, fragment) {
			t.Errorf("down migration missing %q", fragment)
		}
	}
}
