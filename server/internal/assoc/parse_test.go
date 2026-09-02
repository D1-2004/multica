package assoc

import (
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	got, err := ParseSince("48h", now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now.Add(-48 * time.Hour)) {
		t.Fatalf("got %s", got)
	}
	got, err = ParseSince("7d", now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Fatalf("got %s", got)
	}
	if _, err := ParseSince("", now); err == nil {
		t.Fatal("expected error")
	}
}
