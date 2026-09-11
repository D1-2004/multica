package scenememory

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dwsclient"
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

	ErrorAuth               = "AUTH"
	ErrorRouteInactive      = "ROUTE_INACTIVE"
	ErrorConfig             = "CONFIG"
	ErrorIncomplete         = "INCOMPLETE"
	ErrorLLMTimeout         = "LLM_TIMEOUT"
	ErrorInvalidCommit      = "INVALID_COMMIT"
	ErrorHistoryUnavailable = "HISTORY_UNAVAILABLE"
	// ErrorNotInConversation: DingTalk answered 130003, the employee is no
	// longer a member of the scene. Blocked until a new inbound trigger from
	// that scene proves membership again (the dirty upsert clears blocked_at).
	ErrorNotInConversation = "NOT_IN_CONVERSATION"
)

// MaxHistoryBusinessErrorAttempts caps how many claims a scene may spend
// without committing a page before a DingTalk business-level history
// rejection blocks it. attempt_count counts claims since the last committed
// page (it is not a per-code streak), so the rule is "twelve claims, about
// 1.5h of backoff, with no progress and the latest failure a business
// rejection": such rejections repeat identically until the condition
// changes, so the scene is blocked and alerted instead of retried every 15
// minutes. A new trigger from the scene unblocks it; if the rejection is
// still there, the inherited count blocks it again on the next claim, which
// is the same condition, not a new failure. Timeouts, transport and local
// errors keep the unbounded backoff, and the cross-org scope rejection is
// excluded because the Coordinator's next read renews that grant.
// Evidence: 口香糖小队 reached attempt 130 on 2026-09-10 (trace 4aceb4ae).
const MaxHistoryBusinessErrorAttempts int32 = 12

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
	case ErrorAuth, ErrorRouteInactive, ErrorConfig, ErrorNotInConversation:
		return true
	default:
		return false
	}
}

// HistoryBusinessError reports whether a flush failed on a DingTalk
// business-level history rejection (as opposed to a timeout or transport
// error), which decides whether the attempt ceiling applies.
func HistoryBusinessError(err error) bool {
	var cliErr *dwsclient.HistoryError
	return errors.As(err, &cliErr) && cliErr.BusinessError()
}

// BlockAfterFailure decides whether a failed flush should block the scene
// instead of scheduling another retry: terminal codes always block, and a
// repeating business rejection blocks once the attempt ceiling is reached.
func BlockAfterFailure(code string, err error, attempt int32) bool {
	if TerminalFlushCode(code) {
		return true
	}
	if dwsclient.IsCrossOrgPermissionDenied(err) {
		return false
	}
	return code == ErrorHistoryUnavailable && HistoryBusinessError(err) && attempt >= MaxHistoryBusinessErrorAttempts
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
