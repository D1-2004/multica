package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestTaskCompletionCancellationRunsAfterOutbox(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")
	versions := map[string]int{}
	for _, file := range files {
		base := filepath.Base(file)
		for _, name := range []string{"task_completion_outbox", "task_completion_canceled_status"} {
			if strings.HasSuffix(base, "_"+name+".up.sql") {
				version, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
				if err != nil {
					t.Fatal(err)
				}
				versions[name] = version
			}
		}
	}
	if versions["task_completion_outbox"] == 0 || versions["task_completion_canceled_status"] <= versions["task_completion_outbox"] {
		t.Fatalf("cancellation migration must run after its outbox table is created: %v", versions)
	}
}

func TestTaskCompletionCanceledStatusMigrationPreservesHistory(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	migrationsDir := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "migrations"))
	up, err := os.ReadFile(filepath.Join(
		migrationsDir, "9540_task_completion_canceled_status.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(filepath.Join(
		migrationsDir, "9540_task_completion_canceled_status.down.sql"))
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
