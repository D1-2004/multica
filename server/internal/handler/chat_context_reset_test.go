package handler

import (
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
	"time"
)

func TestChatHistoryAfterBoundary(t *testing.T) {
	stamp := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	message := func(n byte, offset time.Duration) db.ChatMessage {
		var id [16]byte
		id[15] = n
		return db.ChatMessage{ID: pgtype.UUID{Bytes: id, Valid: true}, CreatedAt: pgtype.Timestamptz{Time: stamp.Add(offset), Valid: true}}
	}
	old, reset, next := message(1, 0), message(2, 0), message(3, time.Second)
	late := message(4, 2*time.Second)
	late.TaskID = old.ID
	boundary := db.GetChatContextResetBoundaryRow{ID: reset.ID, CreatedAt: reset.CreatedAt, ExcludedTaskIds: []pgtype.UUID{old.ID}}
	for _, tc := range []struct {
		name     string
		messages []db.ChatMessage
		want     int
	}{
		{"reset turn excludes all earlier context", []db.ChatMessage{old}, 0},
		{"later turn retains only new conversation", []db.ChatMessage{old, reset, next}, 2},
		{"empty history", nil, 0},
		{"old task finishing after reset stays excluded", []db.ChatMessage{old, reset, next, late}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := chatHistoryAfterBoundary(tc.messages, boundary)
			if len(got) != tc.want {
				t.Fatalf("got %d messages, want %d", len(got), tc.want)
			}
			if len(got) > 0 && got[0].ID != reset.ID {
				t.Fatal("lost reset boundary or retained earlier message")
			}
		})
	}
}
