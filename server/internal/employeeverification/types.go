// Package employeeverification records Host-owned, deterministic verification
// of Employee Task Runs. A verification result is derived from structured
// evidence the Host holds (artifact bytes bound to the exact Run, provider
// delivery receipts); an assistant's claim or a sandbox exit status is never
// evidence. A passing verification set writes a durable distill intent in the
// same transaction; ProcessVerifiedDistill consumes it into memory.
package employeeverification

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CheckerVersion identifies the deterministic checker implementation. A
// different version re-checking the same (run, check, evidence) conflicts
// instead of overwriting the retained result.
const CheckerVersion = "employee-verification/v1"

type CheckKind string

const (
	// KindArtifactContents checks the bytes of an artifact produced by this
	// exact Run: existence, literals, CSV/TSV rows and columns, exact hash.
	KindArtifactContents CheckKind = "artifact_contents"
	// KindExecutionOutput compares a known expected value with the Run's
	// produced output, or requires a Host-run command result.
	KindExecutionOutput CheckKind = "execution_output"
	// KindDeliveryReceipt proves only that a provider delivered the Run's
	// result. It never proves that the result is correct.
	KindDeliveryReceipt CheckKind = "delivery_receipt"
)

func knownKind(k CheckKind) bool {
	return k == KindArtifactContents || k == KindExecutionOutput || k == KindDeliveryReceipt
}

// correctnessKind reports whether a passing check of this kind is evidence
// that the work itself is correct (as opposed to merely delivered).
func correctnessKind(k CheckKind) bool {
	return k == KindArtifactContents || k == KindExecutionOutput
}

// Origin records who authored a verification contract.
type Origin string

const (
	// OriginHumanCue: Host derivation from the requester's own words that
	// carry an explicit done-criteria cue.
	OriginHumanCue Origin = "human_cue"
	// OriginAutomation: a routine or webhook configuration.
	OriginAutomation Origin = "automation_config"
	// OriginHostFixture: a Host-preset fixture (known calculation/schema).
	OriginHostFixture Origin = "host_fixture"
	// OriginModelProposed: checks proposed by a model. Stored as proposed and
	// inactive until the requester confirms them.
	OriginModelProposed Origin = "model_proposed"
)

type SpecState string

const (
	SpecActive   SpecState = "active"
	SpecProposed SpecState = "proposed"
)

// LearningScope selects where an automation-originated verified learning may
// be stored. Human-originated Tasks always use the requester-private scope and
// must leave it empty.
type LearningScope string

const (
	LearningScopeDefault LearningScope = ""
	LearningScopeScene   LearningScope = "scene"
	LearningScopeNone    LearningScope = "none"
)

type Outcome string

const (
	OutcomePassed  Outcome = "passed"
	OutcomeFailed  Outcome = "failed"
	OutcomeUnknown Outcome = "unknown"
)

// Check is one machine-checkable condition. ID is assigned by the Host from
// the canonical condition (Optional excluded) and persisted with the spec, so
// a reader never recomputes another binary's IDs.
type Check struct {
	ID   string    `json:"id"`
	Kind CheckKind `json:"kind"`
	// File is the exact base name of an artifact produced by this Run.
	File     string   `json:"file,omitempty"`
	Contains []string `json:"contains,omitempty"`
	// DataRows counts CSV/TSV records excluding the header row.
	DataRows *int     `json:"data_rows,omitempty"`
	Columns  []string `json:"columns,omitempty"`
	SHA256   string   `json:"sha256,omitempty"`
	// Expect is compared with the trimmed artifact content, or with the JSON
	// top-level Field when set. Numbers compare numerically.
	Expect string `json:"expect,omitempty"`
	Field  string `json:"field,omitempty"`
	// Command is an explicit check command. Only a Host-run executor result
	// may satisfy it; none exists yet, so it stays unknown.
	Command string `json:"command,omitempty"`
	// Delivery names the delivery that must be confirmed ("origin_reply").
	Delivery string `json:"delivery,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// Spec is the Task's current verification contract.
type Spec struct {
	TaskID        string        `json:"task_id"`
	Revision      int64         `json:"revision"`
	Origin        Origin        `json:"origin"`
	State         SpecState     `json:"state"`
	SourceRef     string        `json:"source_ref"`
	AuthorRef     string        `json:"author_ref"`
	LearningScope LearningScope `json:"learning_scope,omitempty"`
	Checks        []Check       `json:"checks"`
	Digest        string        `json:"digest"`
	ConfirmedBy   string        `json:"confirmed_by,omitempty"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// SetSpecParams is built by the Host from a trusted source; it is never
// decoded from model tool arguments. ExpectedRevision 0 means "no spec yet".
type SetSpecParams struct {
	Origin           Origin
	SourceRef        string
	AuthorRef        string
	LearningScope    LearningScope
	Checks           []Check
	ExpectedRevision int64
}

// ConfirmParams records the requester's explicit confirmation of a proposed spec.
type ConfirmParams struct {
	ExpectedRevision int64
	ConfirmerRef     string
	SourceRef        string
}

// Record is one retained verification result.
type Record struct {
	ID             string    `json:"id"`
	TaskID         string    `json:"task_id"`
	RunID          string    `json:"run_id"`
	QueueTaskID    string    `json:"queue_task_id"`
	GoalRevision   int64     `json:"goal_revision"`
	RequesterRef   string    `json:"requester_ref"`
	SpecRevision   int64     `json:"spec_revision"`
	SpecDigest     string    `json:"spec_digest"`
	CheckID        string    `json:"check_id"`
	CheckKind      CheckKind `json:"check_kind"`
	Check          Check     `json:"check"`
	EvidenceRef    string    `json:"evidence_ref"`
	EvidenceSHA256 string    `json:"evidence_sha256"`
	CheckerVersion string    `json:"checker_version"`
	Outcome        Outcome   `json:"outcome"`
	Detail         string    `json:"detail,omitempty"`
	RunFinishedAt  time.Time `json:"run_finished_at"`
	CompletedAt    time.Time `json:"completed_at"`
}

type GateStatus string

const (
	// GateNone: no active verification contract; ordinary completion rules apply
	// and no verified learning can be produced.
	GateNone    GateStatus = "none"
	GatePassed  GateStatus = "passed"
	GateFailed  GateStatus = "failed"
	GatePending GateStatus = "pending"
)

// Gate is the verification state of one Run against the Task's current spec.
type Gate struct {
	Status       GateStatus `json:"status"`
	SpecRevision int64      `json:"spec_revision,omitempty"`
	SpecDigest   string     `json:"spec_digest,omitempty"`
	// Correct is true when at least one required correctness check passed.
	// A passing delivery receipt alone never sets it.
	Correct bool `json:"correct"`
	// Failed holds the decisive failed records; Pending lists required check IDs
	// without a decisive record.
	Failed  []Record `json:"failed,omitempty"`
	Pending []string `json:"pending,omitempty"`
}

var (
	ErrInvalid     = errors.New("employee verification: invalid input")
	ErrNotFound    = errors.New("employee verification: task or run not found in scope")
	ErrNoSpec      = errors.New("employee verification: task has no verification spec")
	ErrConflict    = errors.New("employee verification: conflicting spec or result for the same identity")
	ErrSpecChanged = errors.New("employee verification: spec changed while the check ran; result discarded")
	ErrEvidence    = errors.New("employee verification: evidence changed while the check ran; result discarded")
	// ErrEvidencePending: a provider send of this Run is still unconfirmed;
	// verification waits rather than judging an incomplete delivery.
	ErrEvidencePending = errors.New("employee verification: run delivery is still being confirmed")
	ErrNotVerifiable   = errors.New("employee verification: only a succeeded run of an Employee scene task is verifiable")
	ErrTaskCancelled   = errors.New("employee verification: task was cancelled")
	ErrStaleGoal       = errors.New("employee verification: run belongs to an older goal revision")
	ErrStaleTenant     = errors.New("employee verification: scene no longer belongs to the task tenant")
	ErrNotRequester    = errors.New("employee verification: only the task requester may author or confirm this spec")
)

// DB accepts a pool or a transaction (Begin on a transaction is a savepoint).
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Querier is the read subset used inside an existing transaction.
type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct{ db DB }

func NewStore(db DB) *Store { return &Store{db: db} }
