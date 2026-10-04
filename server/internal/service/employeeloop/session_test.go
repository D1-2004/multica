// Copyright (c) 2026 Nex.
// Adapted from internal/bot/session_test.go at 71e82a1809565281cbd0bf8185d3c125b715d934.
// See LICENSE and SOURCE_MAP.md for provenance and modification details.
package employeeloop

import "testing"

// Adapted from TestSessionCreateAppendRead; durable storage now belongs to Host.
func TestSessionAppendRead(t *testing.T) {
	store := &SessionStore{}
	store.Append(SessionEntry{Type: "user", Content: "hello"})
	store.Append(SessionEntry{Type: "assistant", Content: "world"})
	entries := store.GetHistory()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Content != "hello" || entries[1].Content != "world" {
		t.Fatalf("entries mismatch: %+v", entries)
	}
	entries[0] = SessionEntry{Content: "outside mutation"}
	if store.GetHistory()[0].Content != "hello" {
		t.Fatal("history leaked its backing slice")
	}
}
