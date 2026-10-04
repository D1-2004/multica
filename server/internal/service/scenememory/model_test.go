package scenememory

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Scene Memory exists for conversation scenes only.
func TestValidSceneRequiresAConversationScene(t *testing.T) {
	sc := db.AgentScene{
		ID: util.MustParseUUID("aaaaaaaa-0000-4000-8000-000000000001"), WorkspaceID: util.MustParseUUID("aaaaaaaa-0000-4000-8000-000000000002"),
		AgentID: util.MustParseUUID("aaaaaaaa-0000-4000-8000-000000000003"), TenantOrgID: "org-1", ExternalSceneID: "cidA", SceneKind: KindGroup,
	}
	if !validScene(sc) {
		t.Fatal("group scene rejected")
	}
	sc.SceneKind = KindDM
	if !validScene(sc) {
		t.Fatal("dm scene rejected")
	}
	for name, bad := range map[string]func(db.AgentScene) db.AgentScene{
		"enterprise": func(s db.AgentScene) db.AgentScene { s.SceneKind = "enterprise"; return s },
		"no id":      func(s db.AgentScene) db.AgentScene { s.ID.Valid = false; return s },
		"no org":     func(s db.AgentScene) db.AgentScene { s.TenantOrgID = ""; return s },
	} {
		if validScene(bad(sc)) {
			t.Errorf("%s accepted", name)
		}
	}
}

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

func TestMaxCursorDoesNotRewind(t *testing.T) {
	later := time.Date(2026, 9, 2, 12, 0, 1, 0, time.UTC)
	earlier := later.Add(-time.Second)
	at, ev := maxCursor(later, "zzz", earlier, "aaa")
	if !at.Equal(later) || ev != "zzz" {
		t.Fatalf("later cursor must win: %s %s", at, ev)
	}
	at, ev = maxCursor(earlier, "aaa", later, "zzz")
	if !at.Equal(later) || ev != "zzz" {
		t.Fatalf("later argument must win: %s %s", at, ev)
	}
	at, ev = maxCursor(later, "aaa", later, "zzz")
	if ev != "zzz" {
		t.Fatalf("same-second larger evidence must win: %s", ev)
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
	if TerminalFlushCode(ErrorIncomplete) {
		t.Fatal("incomplete history must retry")
	}
	if TerminalFlushCode(ErrorLLMTimeout) {
		t.Fatal("llm timeout must retry")
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

func TestRetryDelayForLLMTimeoutDoesNotInheritCatchUpBackoff(t *testing.T) {
	if RetryDelayFor(ErrorLLMTimeout, 1) != 5*time.Second {
		t.Fatalf("first timeout = %s", RetryDelayFor(ErrorLLMTimeout, 1))
	}
	if RetryDelayFor(ErrorLLMTimeout, 800) != time.Minute {
		t.Fatalf("catch-up attempt must not park 15m after an llm timeout, got %s", RetryDelayFor(ErrorLLMTimeout, 800))
	}
	if RetryDelayFor(ErrorIncomplete, 800) != 15*time.Minute {
		t.Fatalf("history gaps keep the long cap, got %s", RetryDelayFor(ErrorIncomplete, 800))
	}
}
