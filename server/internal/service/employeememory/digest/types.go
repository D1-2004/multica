// Package digest is the Employee scene digest writer (12-memory-design M11)
// and the deterministic scene ledger it reads.
//
// Timing (docs: _shared/memory-flush-timing.md) follows GawkBot's split at
// 71e82a1809565281cbd0bf8185d3c125b715d934, re-implemented on PostgreSQL:
//   - every observed group message only marks the scene dirty (no model);
//   - every finished Employee wake and every terminal Task appends one
//     deterministic ledger entry (task_ledger.go: no model self-summary);
//   - the only model work is a budgeted flush when a conversation segment
//     closes (30 minutes without new human messages) or when enough new human
//     messages accumulate (entity_synthesizer.go threshold + coalescing);
//   - a periodic zero-model maintenance pass (memory_workflow_reconciler.go
//     cadence) retires stale or superseded flush output.
//
// GawkBot's in-process queues, single-flight maps and newest-wins merges are
// replaced by one PostgreSQL state row per scene, SKIP LOCKED claims, a lease
// token plus generation CAS and a durable model journal, so two replicas and
// restarts are safe. No code is copied verbatim.
package digest

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	openai "github.com/openai/openai-go/v3"
)

const (
	// SegmentIdle closes a conversation segment: the same boundary the
	// foreground uses to split history.
	SegmentIdle = 30 * time.Minute
	// ThresholdHuman flushes before undigested human lines scroll out of the
	// 30-line foreground transcript window.
	ThresholdHuman = 12
	// Debounce re-arms on each new human line once the threshold is crossed;
	// MaxWait bounds it from the first undigested line.
	Debounce = 60 * time.Second
	MaxWait  = 10 * time.Minute
	LeaseTTL = 3 * time.Minute
	// CallTimeout bounds one provider request.
	CallTimeout = 25 * time.Second
	// MaxCallsPerClaim is one call plus at most one repair round.
	MaxCallsPerClaim = 2
	// SceneDailyCalls and AgentDailyCalls are Asia/Shanghai calendar-day caps.
	SceneDailyCalls = 24
	AgentDailyCalls = 300
	// BlockAfterNoProgress consecutive failed or fully rejected claims block
	// the scene until a new human line arrives.
	BlockAfterNoProgress = 6
	// PageLimit bounds the transcript lines one claim reads after its cursor.
	PageLimit = 60
	// ContextLines are read before the cursor as non-evidence context.
	ContextLines = 6
	// MaxActiveFlushItems caps flush-origin scene facts per scene.
	MaxActiveFlushItems = 60
	// OpenItemTTL retires flush-origin open items nobody restated.
	OpenItemTTL = 14 * 24 * time.Hour
	// Retention keeps runs and ledger entries for this long.
	Retention = 30 * 24 * time.Hour
	// MaintainEvery is the zero-model maintenance cadence per scene.
	MaintainEvery = 10 * time.Minute
	// MaxOpsPerCall bounds what one model call may propose.
	MaxOpsPerCall = 8
	// MemoryMarker gates claims: every live replica must understand the
	// flush tables, journal and scene-fact origin before any writer runs.
	MemoryMarker = "[employee-memory:3]"
	// PromptVersion participates in the page hash, so a prompt change never
	// replays a journal recorded for another prompt.
	PromptVersion = "employee-scene-digest-v1"
	// FlushActorPrefix identifies the writer's own outputs (retract-own-only).
	FlushActorPrefix = "employee-flush:"
)

// Trigger names why a scene was claimed.
const (
	TriggerSegmentClose = "segment_close"
	TriggerThreshold    = "threshold"
	TriggerCatchUp      = "catch_up"
)

// Outcomes recorded on employee_scene_digest_run.
const (
	OutcomeCommitted       = "committed"
	OutcomeNoChange        = "no_change"
	OutcomeRejected        = "rejected"
	OutcomeTimeout         = "timeout"
	OutcomeError           = "error"
	OutcomeBudgetExhausted = "budget_exhausted"
	OutcomeBlocked         = "blocked"
	OutcomeSkippedTrivial  = "skipped_trivial"
)

// Fact kinds the writer may propose. Preferences and person profiles are
// never proposed: they belong to the person, not to the scene.
const (
	KindFact     = "fact"
	KindDecision = "decision"
	KindOpenItem = "open_item"
)

// Capture origins of scene facts (M5's scene fact store).
const (
	OriginWindow     = "window"
	OriginTranscript = "transcript"
	OriginFlush      = "flush"
)

// SceneKey is one exact Employee scene namespace. Field order matches M8's
// employeeentry.SceneMessageKey, so the observation hook converts directly.
type SceneKey struct {
	WorkspaceID, AgentID, TenantOrgID, SceneID string
}

// Queryer reads inside a pgx.Tx or a pool.
type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Execer writes inside the caller's transaction.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DB is a pool: it begins the writer's short transactions.
type DB interface {
	Queryer
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// TranscriptMessage is one persisted group line (M8 employee_scene_message).
type TranscriptMessage struct {
	ProviderMessageID string
	SentAt            time.Time
	SenderClass       string // human | self | bot | unknown
	SenderRef         string // dingtalk:<tenant org>:uid|open_id|staff_id:<v>
	SenderName        string
	QuotedMessageID   string
	Body              string
	Truncated         bool
}

// Transcript reads M8's persisted, non-withdrawn scene transcript inside the
// writer's read transaction.
type Transcript interface {
	// After returns lines strictly after (afterAt, afterID), ascending.
	After(ctx context.Context, tx pgx.Tx, key SceneKey, afterAt time.Time, afterID string, limit int) ([]TranscriptMessage, error)
	// Before returns lines strictly before (beforeAt, beforeID), oldest first.
	Before(ctx context.Context, tx pgx.Tx, key SceneKey, beforeAt time.Time, beforeID string, limit int) ([]TranscriptMessage, error)
}

// Fact is one active scene-layer memory record as the writer sees it.
type Fact struct {
	ID            string
	Type          string
	Key           string
	Subject       string
	Insight       string
	Source        string
	CaptureOrigin string
	CreatedBy     string
	SpeakerName   string
	EvidenceID    string
	ConflictsWith string
	CreatedAt     time.Time
}

// FactUpsert is one validated proposal. Attribution comes from Evidence, a
// Host-read transcript line, never from the model.
type FactUpsert struct {
	Kind     string
	Subject  string
	Quote    string
	Evidence TranscriptMessage
}

// FactWrite is the store's answer to one upsert.
type FactWrite struct {
	ID       string
	Replayed bool
	Changed  bool
}

// FactStore is M5's scene fact store (employeememory UpsertSceneFactTx and
// friends). Every method joins the writer's transaction and takes its own
// workspace and namespace locks there.
type FactStore interface {
	ActiveFacts(ctx context.Context, tx pgx.Tx, key SceneKey, limit int) ([]Fact, error)
	HostKey(kind, subject string) (string, error)
	Upsert(ctx context.Context, tx pgx.Tx, key SceneKey, actor string, in FactUpsert) (FactWrite, error)
	// Retract forgets one record created by actor; the tombstone remains.
	Retract(ctx context.Context, tx pgx.Tx, key SceneKey, id, actor string) error
}

// OpRejectedError is a domain refusal of one operation (ungrounded quote,
// evidence before a reset, instruction-like text, not the actor's record).
// Other store errors abort the whole commit.
type OpRejectedError struct {
	Reason string
	Err    error
}

func (e *OpRejectedError) Error() string {
	if e.Err != nil {
		return e.Reason + ": " + e.Err.Error()
	}
	return e.Reason
}
func (e *OpRejectedError) Unwrap() error { return e.Err }

// Fence re-validates the scene before reads and inside the commit
// transaction. A non-empty reason blocks the scene; an error retries.
type Fence func(ctx context.Context, tx pgx.Tx, key SceneKey) (blockReason string, err error)

// Model performs one provider request through the shared Coordinator model
// chain. It returns the model reference actually used.
type Model interface {
	Chat(ctx context.Context, request openai.ChatCompletionNewParams) (*openai.ChatCompletion, string, error)
}

var (
	ErrInvalid   = errors.New("employee digest: invalid input")
	ErrLeaseLost = errors.New("employee digest: lease lost")
)

// FlushActor is the writer identity used as created_by of flush outputs.
func FlushActor(agentID string) string { return FlushActorPrefix + agentID }
