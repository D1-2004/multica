package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestA2ADeferredTurnIndexMigrationKeepsChannelConflictTargetAligned(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve server root: %v", err)
	}
	read := func(relative string) string {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		return string(content)
	}

	const predicate = "WHERE status = 'deferred'\n      AND chat_session_id IS NOT NULL\n      AND fire_at IS NOT NULL"
	if !strings.Contains(read("pkg/db/queries/chat.sql"), predicate) {
		t.Fatal("channel upsert conflict target does not use the channel-only deferred predicate")
	}
	if !strings.Contains(read("migrations/9055_agent_task_deferred_channel_unique.up.sql"), predicate) {
		t.Fatal("replacement unique index does not match the channel upsert conflict target")
	}
	drop := read("migrations/9056_drop_broad_deferred_chat_unique.up.sql")
	if !strings.Contains(drop, "DROP INDEX CONCURRENTLY IF EXISTS uq_agent_task_queue_deferred_chat_session") {
		t.Fatal("broad deferred Chat Session index is not removed after the replacement")
	}
}
