package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatMessageClientReceiptMigrationCanReplayAfterRenumbering(t *testing.T) {
	dir := realMigrationsDir(t)

	up := readSingleMigrationForTest(t, dir, "*_chat_message_client_receipt.up.sql")
	if !strings.Contains(up, "add column if not exists client_receipt_recorded_at") {
		t.Fatal("up migration must tolerate the receipt column already existing under an earlier migration stem")
	}

	down := readSingleMigrationForTest(t, dir, "*_chat_message_client_receipt.down.sql")
	if !strings.Contains(down, "drop column if exists client_receipt_recorded_at") {
		t.Fatal("down migration must tolerate the receipt column already being absent")
	}
}

func readSingleMigrationForTest(t *testing.T, dir, pattern string) string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("migration pattern %q matched %d files, want 1", pattern, len(matches))
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return strings.ToLower(string(body))
}
