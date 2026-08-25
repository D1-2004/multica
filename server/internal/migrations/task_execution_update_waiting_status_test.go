package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskExecutionUpdateWaitingStatusMigrationIsReversible(t *testing.T) {
	upPath := filepath.Join(realMigrationsDir(t), "9076_task_execution_update_result_message.up.sql")
	downPath := filepath.Join(realMigrationsDir(t), "9076_task_execution_update_result_message.down.sql")
	up, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatal(err)
	}

	upSQL := string(up)
	for _, required := range []string{
		"DROP CONSTRAINT task_execution_update_outbox_status_check",
		"status IN ('waiting_result', 'queued', 'delivered', 'dead_letter')",
		"CONSTRAINT ck_task_execution_update_outbox_result_state",
		"status = 'waiting_result' AND result_message_frozen = FALSE",
		"hold_task_completion_for_waiting_execution_update",
		"CREATE TRIGGER trg_hold_task_completion_for_waiting_execution_update",
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("up migration missing %q", required)
		}
	}

	downSQL := string(down)
	updateWaiting := strings.Index(downSQL, "SET status = 'queued'")
	dropExpandedConstraint := strings.Index(downSQL, "DROP CONSTRAINT task_execution_update_outbox_status_check")
	if updateWaiting < 0 {
		t.Error("down migration must converge waiting_result rows to queued")
	}
	if dropExpandedConstraint < 0 {
		t.Error("down migration must replace the expanded status constraint")
	}
	if updateWaiting >= 0 && dropExpandedConstraint >= 0 && updateWaiting > dropExpandedConstraint {
		t.Error("down migration must converge waiting_result rows before restoring the old constraint")
	}
	if !strings.Contains(downSQL, "status IN ('queued', 'delivered', 'dead_letter')") {
		t.Error("down migration must restore the original status constraint")
	}
	if !strings.Contains(downSQL, "DROP CONSTRAINT ck_task_execution_update_outbox_result_state") {
		t.Error("down migration must drop the result-state constraint before dropping its columns")
	}
	if !strings.Contains(downSQL, "SET available_at = now()") {
		t.Error("down migration must release terminal completions held by waiting_result rows")
	}
	if !strings.Contains(downSQL, "DROP TRIGGER IF EXISTS trg_hold_task_completion_for_waiting_execution_update") {
		t.Error("down migration must remove the terminal hold trigger")
	}
}
