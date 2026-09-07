package scenememory

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
)

const (
	KindGroup = "group"
	KindDM    = "dm"

	PlatformDingTalk = "dingtalk"

	MaxMemoryCodePoints = 1600
	MaxFlushMetaBytes   = 8192

	ColdStartDebounce   = 4 * time.Second
	IncrementalDebounce = 30 * time.Second
	MaxDirtyWait        = 2 * time.Minute
	LeaseTTL            = 2 * time.Minute

	ErrorAuth          = "AUTH"
	ErrorRouteInactive = "ROUTE_INACTIVE"
	ErrorConfig        = "CONFIG"
	ErrorIncomplete    = "INCOMPLETE"
)

// KindFromChatType maps a DingTalk/dispatch chat type onto a Scene kind.
func KindFromChatType(chatType string) string {
	if strings.EqualFold(strings.TrimSpace(chatType), "group") {
		return KindGroup
	}
	return KindDM
}

// Identity is the exact Scene key. chat_session_id is never part of it.
type Identity struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	Platform    string
	OrgID       string
	SceneKey    string
	SceneKind   string
	SceneTitle  string
}

type DirtyTrigger struct {
	OccurredAt     time.Time
	EvidenceID     string
	JobID          pgtype.UUID
	CoordTraceID   string
	IdempotencyKey string
}

type CommitBatch struct {
	ReplaceText            bool
	MemoryText             string
	SceneTitle             string
	SourceCursorAt         time.Time
	SourceCursorEvidenceID string
	FlushMeta              []byte
	ExpectedMemoryRevision int64
}

func (id Identity) normalized() Identity {
	out := id
	out.Platform = strings.TrimSpace(out.Platform)
	if out.Platform == "" {
		out.Platform = PlatformDingTalk
	}
	out.OrgID = strings.TrimSpace(out.OrgID)
	out.SceneKey = strings.TrimSpace(out.SceneKey)
	out.SceneKind = strings.TrimSpace(out.SceneKind)
	out.SceneTitle = strings.TrimSpace(out.SceneTitle)
	return out
}

func (id Identity) valid() bool {
	id = id.normalized()
	return id.WorkspaceID.Valid && id.AgentID.Valid &&
		id.OrgID != "" && id.SceneKey != "" &&
		(id.SceneKind == KindGroup || id.SceneKind == KindDM)
}

func ValidateMemoryText(text string) bool {
	return utf8.RuneCountInString(text) <= MaxMemoryCodePoints
}

// FlushError is returned by a Flusher so the worker can Block vs Retry.
type FlushError struct {
	Code string
	Err  error
}

func (e *FlushError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *FlushError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func FlushErrorCode(err error) string {
	var fe *FlushError
	if errors.As(err, &fe) {
		return fe.Code
	}
	return ""
}

func TerminalFlushCode(code string) bool {
	switch code {
	case ErrorAuth, ErrorRouteInactive, ErrorConfig:
		return true
	default:
		return false
	}
}

func CursorCovers(cursorAt time.Time, cursorEvidence string, cutoffAt time.Time, cutoffEvidence string) bool {
	if cursorAt.IsZero() || cutoffAt.IsZero() {
		return false
	}
	if cursorAt.After(cutoffAt) {
		return true
	}
	if cursorAt.Before(cutoffAt) {
		return false
	}
	return cursorEvidence >= cutoffEvidence
}

func maxCursor(aAt time.Time, aEv string, bAt time.Time, bEv string) (time.Time, string) {
	if aAt.IsZero() {
		return bAt, bEv
	}
	if bAt.IsZero() || CursorCovers(aAt, aEv, bAt, bEv) {
		return aAt, aEv
	}
	return bAt, bEv
}
