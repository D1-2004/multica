package scenememory

import (
	"strings"
	"testing"
	"time"
)

func TestFilterUntilDropsAfterCutoff(t *testing.T) {
	cutoff := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	events := []HistoryEvent{
		{EvidenceID: "a", OccurredAt: cutoff.Add(-time.Minute), Content: "before"},
		{EvidenceID: "b", OccurredAt: cutoff, Content: "at"},
		{EvidenceID: "c", OccurredAt: cutoff.Add(time.Minute), Content: "after"},
	}
	got := filterUntil(events, cutoff, "b")
	if len(got) != 2 || got[0].EvidenceID != "a" || got[1].EvidenceID != "b" {
		t.Fatalf("got %#v", got)
	}
	got = filterUntil(events, cutoff, "a")
	if len(got) != 1 || got[0].EvidenceID != "a" {
		t.Fatalf("same-time later evidence must drop, got %#v", got)
	}
}

func TestForceIncludeEvidenceRestoresDroppedTrigger(t *testing.T) {
	cursor := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	events := []HistoryEvent{
		{EvidenceID: "aaa", OccurredAt: cursor.Add(-time.Second), Content: "early"},
		{EvidenceID: "zzz", OccurredAt: cursor, Content: "later"},
	}
	delta := afterCursor(events, cursor, "zzz")
	if len(delta) != 0 {
		t.Fatalf("afterCursor should drop both, got %#v", delta)
	}
	got := forceIncludeEvidence(delta, events, "aaa")
	if len(got) != 1 || got[0].EvidenceID != "aaa" {
		t.Fatalf("force-include must restore the pending trigger: %#v", got)
	}
}

func TestAfterCursorIsExclusive(t *testing.T) {
	cursor := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	events := []HistoryEvent{
		{EvidenceID: "b", OccurredAt: cursor},
		{EvidenceID: "a", OccurredAt: cursor.Add(-time.Second)},
		{EvidenceID: "c", OccurredAt: cursor.Add(time.Second)},
	}
	got := afterCursor(events, cursor, "b")
	if len(got) != 1 || got[0].EvidenceID != "c" {
		t.Fatalf("got %#v", got)
	}
	got = afterCursor(events, time.Time{}, "")
	if len(got) != 3 || got[0].EvidenceID != "a" {
		t.Fatalf("empty cursor should sort oldest first, got %#v", got)
	}
}

func TestRedactSecrets(t *testing.T) {
	got := redactSecrets("api_key=sk-abcdefghijklmnopqrstuvwxyz password=secret token=plainval https://x?token=abc&ok=1")
	if strings.Contains(got, "sk-abcdefghijklmnopqrstuvwxyz") || strings.Contains(got, "password=secret") || strings.Contains(got, "token=plainval") || strings.Contains(got, "token=abc") {
		t.Fatalf("leaked: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("missing redaction: %q", got)
	}
}

func TestClipRunes(t *testing.T) {
	if got := clipRunes("  工具不是数字员工  ", 4); got != "工具不是" {
		t.Fatalf("got %q", got)
	}
	if got := clipRunes("short", 40); got != "short" {
		t.Fatalf("got %q", got)
	}
}
