package handler

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestCoordinatorCollectQuietIsPerAgentWithTheDefaultOtherwise(t *testing.T) {
	fast := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	other := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	var h *Handler
	if got := h.coordinatorCollectQuiet(fast); got != inboundCoordinatorCollectWindow {
		t.Fatalf("no provider keeps 4 s, got %v", got)
	}
	h = &Handler{CoordinatorCollectQuiet: func(agent pgtype.UUID) time.Duration {
		if agent == fast {
			return time.Second
		}
		return 0
	}}
	if got := h.coordinatorCollectQuiet(fast); got != time.Second {
		t.Fatalf("the selected agent collects for 1 s, got %v", got)
	}
	if got := h.coordinatorCollectQuiet(other); got != inboundCoordinatorCollectWindow {
		t.Fatalf("other agents keep 4 s, got %v", got)
	}
}

func TestCoordinatorCollectDeadlineUsesTheAgentWindowUnderTheCap(t *testing.T) {
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		want    time.Duration
	}{
		{"first message", 0, time.Second},
		{"a line 600 ms later re-arms 1 s", 600 * time.Millisecond, 1600 * time.Millisecond},
		{"lines keep arriving until the cap", 11500 * time.Millisecond, 12 * time.Second},
	} {
		if got := coordinatorCollectDeadline(start, start.Add(tc.elapsed), time.Second); !got.Equal(start.Add(tc.want)) {
			t.Fatalf("%s: got %v want %v", tc.name, got.Sub(start), tc.want)
		}
	}
}
