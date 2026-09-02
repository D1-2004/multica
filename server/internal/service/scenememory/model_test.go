package scenememory

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestValidateMemoryText(t *testing.T) {
	if !ValidateMemoryText("") || !ValidateMemoryText("短") {
		t.Fatal("short text must be valid")
	}
	if ValidateMemoryText(strings.Repeat("字", MaxMemoryCodePoints+1)) {
		t.Fatal("text over 1600 code points must be rejected")
	}
}

func TestCursorCovers(t *testing.T) {
	cutoff := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	if CursorCovers(time.Time{}, "a", cutoff, "a") {
		t.Fatal("zero cursor cannot cover")
	}
	if !CursorCovers(cutoff.Add(time.Second), "a", cutoff, "z") {
		t.Fatal("later timestamp covers")
	}
	if CursorCovers(cutoff.Add(-time.Second), "z", cutoff, "a") {
		t.Fatal("earlier timestamp does not cover")
	}
	if !CursorCovers(cutoff, "b", cutoff, "a") {
		t.Fatal("same time later evidence covers")
	}
}

func TestClipErrKeepsUTF8(t *testing.T) {
	raw := strings.Repeat("错误", 200)
	got := clipErr(raw, 500)
	if !utf8.ValidString(got) {
		t.Fatal("clipErr must not split a UTF-8 code point")
	}
	if len(got) > 500 {
		t.Fatalf("clipped length %d", len(got))
	}
	if clipErr("short", 500) != "short" {
		t.Fatal("short text must pass through")
	}
}

func TestFlushErrorCode(t *testing.T) {
	err := &FlushError{Code: ErrorAuth, Err: errors.New("binding gone")}
	if FlushErrorCode(err) != ErrorAuth || !TerminalFlushCode(ErrorAuth) {
		t.Fatalf("auth code = %q", FlushErrorCode(err))
	}
	if TerminalFlushCode("") || FlushErrorCode(errors.New("boom")) != "" {
		t.Fatal("plain errors are retryable")
	}
}

func TestRetryDelayCaps(t *testing.T) {
	if RetryDelay(1) != 5*time.Second {
		t.Fatalf("attempt 1 = %s", RetryDelay(1))
	}
	if RetryDelay(2) != 10*time.Second {
		t.Fatalf("attempt 2 = %s", RetryDelay(2))
	}
	if RetryDelay(100) != 15*time.Minute {
		t.Fatalf("cap = %s", RetryDelay(100))
	}
}
