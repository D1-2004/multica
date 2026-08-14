package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTaskCompletionCanceledStatusMigrationPreservesHistory(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	migrationsDir := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "migrations"))
	up, err := os.ReadFile(filepath.Join(
		migrationsDir, "271_task_completion_canceled_status.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(filepath.Join(
		migrationsDir, "271_task_completion_canceled_status.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	upSQL := string(up)
	downSQL := string(down)

	for _, required := range []string{
		"execution_status IN ('completed', 'failed', 'canceled')",
		"'canceled',",
		"CREATE OR REPLACE FUNCTION enqueue_cancelled_task_completion()",
		"root_agent_id_value UUID",
		"comment_root_task_ids UUID[]",
		"'multica-comment-terminal:'",
		"comment_target.root_agent_id",
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("up migration missing %q", required)
		}
	}
	if strings.Contains(upSQL, "UPDATE task_completion_outbox") {
		t.Fatal("up migration must not rewrite historical completion rows")
	}
	if !strings.Contains(downSQL, "SET execution_status = 'failed'") {
		t.Fatal("down migration must normalize canceled rows before restoring the old constraint")
	}
	for _, required := range []string{
		"root_agent_id_value UUID",
		"comment_root_task_ids UUID[]",
		"'multica-comment-terminal:'",
		"comment_target.root_agent_id",
	} {
		if !strings.Contains(downSQL, required) {
			t.Errorf("down migration must preserve migration 257 behavior: missing %q", required)
		}
	}
}
