// Package employeetask persists employee goals independently of issues and queue executions.
// Callers authenticate actors and resolve their authority before invoking this package.
package employeetask

import (
	"errors"
	"time"

	"github.com/multica-ai/multica/server/internal/scene"
)

var (
	ErrInvalid     = errors.New("invalid employee task input")
	ErrNotFound    = errors.New("employee task not found in scope")
	ErrConflict    = errors.New("employee task version or source payload conflict")
	ErrActiveRun   = errors.New("employee task already has an active run")
	ErrRunNotReady = errors.New("employee task runner termination is not confirmed")
)

type ScopeKind string

const (
	ScopeScene       ScopeKind = "scene"
	ScopeLegacyIssue ScopeKind = "legacy_issue"
	ScopeLegacyChat  ScopeKind = "legacy_chat"
)

// Scope comes from trusted admission. Legacy sources never invent a scene.
// Scene scope requires a directory entry belonging to this workspace, agent and tenant.
type Scope struct {
	WorkspaceID string    `json:"workspace_id"`
	AgentID     string    `json:"agent_id"`
	TenantOrgID string    `json:"tenant_org_id"`
	Kind        ScopeKind `json:"kind"`
	Scene       scene.Ref `json:"scene"`
	LegacyID    string    `json:"legacy_id,omitempty"`
}

type OwnerLoop string

const (
	LoopCoordinator OwnerLoop = "coordinator"
	LoopEmployee    OwnerLoop = "employee"
)

type DispatchMode string

const (
	DispatchDirect DispatchMode = "direct"
	DispatchIssue  DispatchMode = "issue"
)

type State string

const (
	StateReady     State = "ready"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// Definition describes intent; AccessNeeded does not grant any capability.
type Definition struct {
	Goal            string   `json:"goal"`
	Deliverables    []string `json:"deliverables,omitempty"`
	SuccessCriteria []string `json:"success_criteria,omitempty"`
	AccessNeeded    []string `json:"access_needed,omitempty"`
}

// Source identifies one exact source action, not the message body or its hash.
type Source struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

type Task struct {
	ID           string       `json:"id"`
	Scope        Scope        `json:"scope"`
	OwnerLoop    OwnerLoop    `json:"owner_loop"`
	DispatchMode DispatchMode `json:"dispatch_mode"`
	RequesterRef string       `json:"requester_ref"`
	Definition   Definition   `json:"definition"`
	GoalRevision int64        `json:"goal_revision"`
	Version      int64        `json:"version"`
	State        State        `json:"state"`
	LastEntrySeq int64        `json:"last_entry_seq"`
	ActiveRunID  string       `json:"active_run_id,omitempty"`
	IssueID      string       `json:"issue_id,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

type Entry struct {
	TaskID       string    `json:"task_id"`
	Seq          int64     `json:"seq"`
	Kind         string    `json:"kind"`
	Source       Source    `json:"source"`
	ActorRef     string    `json:"actor_ref,omitempty"`
	GoalRevision int64     `json:"goal_revision"`
	RunID        string    `json:"run_id,omitempty"`
	Body         string    `json:"body,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Run struct {
	ID           string     `json:"id"`
	TaskID       string     `json:"task_id"`
	QueueTaskID  string     `json:"queue_task_id"`
	GoalRevision int64      `json:"goal_revision"`
	InputSeq     int64      `json:"input_seq"`
	State        State      `json:"state"`
	Result       string     `json:"result,omitempty"`
	ResultRef    string     `json:"result_ref,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

type CreateParams struct {
	Scope        Scope        `json:"scope"`
	OwnerLoop    OwnerLoop    `json:"owner_loop"`
	DispatchMode DispatchMode `json:"dispatch_mode"`
	RequesterRef string       `json:"requester_ref"`
	Definition   Definition   `json:"definition"`
	Source       Source       `json:"source"`
	Input        string       `json:"input"`
}

type InputParams struct {
	Source   Source `json:"source"`
	ActorRef string `json:"actor_ref"`
	Body     string `json:"body"`
	// Correction is a host-verified human change, never a model-proposed definition.
	// Nil means informational input and must not advance the goal revision.
	Correction      *Definition `json:"correction,omitempty"`
	ExpectedVersion int64       `json:"-"`
}

type StartRunParams struct {
	Source      Source `json:"source"`
	QueueTaskID string `json:"queue_task_id"`
	// InputSeq freezes the accepted input boundary when a durable follow-up launches later.
	// Zero captures the current ledger boundary for immediate execution.
	InputSeq        int64 `json:"input_seq,omitempty"`
	ExpectedVersion int64 `json:"-"`
}

// ResumeParams is an explicit host-authorized continuation of a completed goal.
// It does not change the definition or assert that an unreachable worker stopped.
type ResumeParams struct {
	Source          Source `json:"source"`
	ActorRef        string `json:"actor_ref"`
	Body            string `json:"body"`
	ExpectedVersion int64  `json:"-"`
}

// ObserveIssueRunParams records a queue already accepted by the Coordinator's
// existing Issue backend. It is never an instruction to dispatch a worker.
type ObserveIssueRunParams struct {
	Source          Source `json:"source"`
	QueueTaskID     string `json:"queue_task_id"`
	GoalRevision    int64  `json:"goal_revision"`
	InputSeq        int64  `json:"input_seq"`
	ExpectedVersion int64  `json:"-"`
}

type ResultParams struct {
	Source    Source `json:"source"`
	RunID     string `json:"run_id"`
	State     State  `json:"state"`
	Result    string `json:"result"`
	ResultRef string `json:"result_ref,omitempty"`
}

type BindIssueParams struct {
	Source          Source `json:"source"`
	IssueID         string `json:"issue_id"`
	ExpectedVersion int64  `json:"-"`
}
