package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
)

// EmployeeActivitySource names where an observation came from. Every source
// keeps its own row identity and the Host time PostgreSQL recorded when the
// Host accepted the row; a reporter's own clock is never used.
type EmployeeActivitySource string

const (
	// Progress-capable sources.
	EmployeeActivityTaskMessage EmployeeActivitySource = "task_message"
	EmployeeActivityTaskEntry   EmployeeActivitySource = "task_entry"
	EmployeeActivityArtifact    EmployeeActivitySource = "task_artifact"
	// EmployeeActivityTaskInput is an input the Host accepted for a Task wait
	// (for example a collected answer). A wait reader reports it.
	EmployeeActivityTaskInput EmployeeActivitySource = "task_input"

	// Sources that are observable but never progress. They are named so tests,
	// logs and future readers cannot accidentally count them.
	EmployeeActivityRuntimeHeartbeat EmployeeActivitySource = "runtime_heartbeat"
	EmployeeActivityLeaseRenewal     EmployeeActivitySource = "lease_renewal"
	EmployeeActivityRoutineScan      EmployeeActivitySource = "routine_scan"
	EmployeeActivityTraceExport      EmployeeActivitySource = "trace_export"
	EmployeeActivityHostNotice       EmployeeActivitySource = "host_notice"
	EmployeeActivityHumanChat        EmployeeActivitySource = "human_chat"
)

// EmployeeActivity is one observation about a Task or its execution.
type EmployeeActivity struct {
	Source EmployeeActivitySource
	// Ref is the source row identity (message id, task:seq, attachment id).
	Ref string
	// Kind is the source-specific type: a task_message type, a task entry kind,
	// or an artifact state.
	Kind string
	// Operation is a task entry payload operation (for example "stop").
	Operation string
	// ActorRef is the accepted human actor for task entries.
	ActorRef string
	// At is Host time: when PostgreSQL accepted the row.
	At time.Time
}

// Classification reasons. They are stable strings for logs and tests.
const (
	activityExecutionOutput   = "execution_output"
	activityToolResult        = "tool_result"
	activityAcceptedInput     = "accepted_task_input"
	activityVerifiedArtifact  = "verified_artifact"
	activityExecutionStatus   = "execution_status"
	activityExecutionLog      = "execution_log"
	activityExecutionError    = "execution_error"
	activityUnknownMessage    = "unknown_message_type"
	activityStopRequest       = "stop_request"
	activityTaskRequest       = "task_request"
	activityLedgerBookkeeping = "ledger_bookkeeping"
	activityUnknownEntry      = "unknown_entry_kind"
	activityAnonymousEntry    = "entry_without_actor"
	activityArtifactPending   = "artifact_not_verified"
	activityNotProgressSource = "not_progress_source"
	activityNoHostTime        = "no_host_time"
)

// ClassifyEmployeeActivity decides whether one observation is real progress.
//
// Progress is real execution output, a tool result, a verified artifact, or a
// Task input the Host accepted from a human. Heartbeats, lease renewals,
// routine scans, trace export, Host notices (including the watchdog's own),
// empty polls, ledger bookkeeping and chat that never became a Task input (a
// progress question, thanks, an @) are not progress. Unknown kinds are not
// progress: a new bookkeeping kind must never silently clear a stall.
func ClassifyEmployeeActivity(a EmployeeActivity) (bool, string) {
	if a.At.IsZero() {
		return false, activityNoHostTime
	}
	switch a.Source {
	case EmployeeActivityTaskMessage:
		switch a.Kind {
		case "text", "thinking", "tool_use":
			return true, activityExecutionOutput
		case "tool_result":
			return true, activityToolResult
		case "status":
			return false, activityExecutionStatus
		case "log":
			return false, activityExecutionLog
		case "error":
			return false, activityExecutionError
		}
		return false, activityUnknownMessage
	case EmployeeActivityTaskEntry:
		switch a.Kind {
		case "input":
			if a.Operation == "stop" {
				return false, activityStopRequest
			}
		case "amendment", "steer", "resumed":
		case "request":
			return false, activityTaskRequest
		case "run_started", "result", "issue_bound", "writer_fenced":
			return false, activityLedgerBookkeeping
		default:
			return false, activityUnknownEntry
		}
		if a.Operation != "" {
			return false, activityUnknownEntry
		}
		if a.ActorRef == "" {
			return false, activityAnonymousEntry
		}
		return true, activityAcceptedInput
	case EmployeeActivityArtifact:
		if a.Kind == "ready" {
			return true, activityVerifiedArtifact
		}
		return false, activityArtifactPending
	case EmployeeActivityTaskInput:
		return true, activityAcceptedInput
	}
	return false, activityNotProgressSource
}

// EmployeeActivityWatermark is the newest real progress observed for a Task
// state. It is monotonic: older or equal Host times never move it back.
type EmployeeActivityWatermark struct {
	At     time.Time              `json:"at"`
	Source EmployeeActivitySource `json:"source,omitempty"`
	Ref    string                 `json:"ref,omitempty"`
}

func (w EmployeeActivityWatermark) IsZero() bool { return w.At.IsZero() }

// Observe advances the watermark with a progress observation. It returns
// false when the observation is not progress or not strictly newer.
func (w EmployeeActivityWatermark) Observe(a EmployeeActivity) (EmployeeActivityWatermark, bool) {
	if progress, _ := ClassifyEmployeeActivity(a); !progress || !a.At.After(w.At) {
		return w, false
	}
	return EmployeeActivityWatermark{At: a.At, Source: a.Source, Ref: a.Ref}, true
}

// Merge keeps the newer watermark; an equal time keeps the receiver.
func (w EmployeeActivityWatermark) Merge(o EmployeeActivityWatermark) EmployeeActivityWatermark {
	if o.At.After(w.At) {
		return o
	}
	return w
}

// employeeActivityQuerier is satisfied by a pool, a connection or a transaction.
type employeeActivityQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// loadEmployeeTaskProgress reads the newest progress of each source for one
// Task and, when given, the exact queue execution of its current Run. Each
// candidate row is reclassified in Go so the SQL filter cannot drift from
// ClassifyEmployeeActivity.
func loadEmployeeTaskProgress(ctx context.Context, q employeeActivityQuerier, task employeetask.Task, queueTaskID string) (EmployeeActivityWatermark, error) {
	var w EmployeeActivityWatermark
	observe := func(a EmployeeActivity) {
		w, _ = w.Observe(a)
	}
	if queueTaskID != "" {
		// Read the newest progress messages by seq through the (task_id, seq)
		// index, then pick the newest Host time among them: two reporter batches
		// may commit out of seq order.
		var a EmployeeActivity
		err := q.QueryRow(ctx, `SELECT id::text, type, created_at FROM (
 SELECT id, type, created_at, seq FROM task_message
 WHERE task_id=$1::uuid AND type IN ('text','thinking','tool_use','tool_result')
 ORDER BY seq DESC LIMIT 32) recent ORDER BY created_at DESC, seq DESC LIMIT 1`, queueTaskID).Scan(&a.Ref, &a.Kind, &a.At)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return w, err
		}
		if err == nil {
			a.Source = EmployeeActivityTaskMessage
			observe(a)
		}
	}
	var entry EmployeeActivity
	var seq int64
	err := q.QueryRow(ctx, `SELECT seq, kind, COALESCE(payload->>'operation',''), actor_ref, created_at FROM employee_task_entry
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid
 AND kind IN ('input','amendment','steer','resumed') AND actor_ref <> '' AND COALESCE(payload->>'operation','')=''
 ORDER BY created_at DESC, seq DESC LIMIT 1`, task.Scope.WorkspaceID, task.Scope.AgentID, task.Scope.TenantOrgID, task.ID).Scan(&seq, &entry.Kind, &entry.Operation, &entry.ActorRef, &entry.At)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return w, err
	}
	if err == nil {
		entry.Source, entry.Ref = EmployeeActivityTaskEntry, task.ID+":"+strconv.FormatInt(seq, 10)
		observe(entry)
	}
	var artifact EmployeeActivity
	err = q.QueryRow(ctx, `SELECT attachment_id::text, state, ready_at FROM employee_task_artifact
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND state='ready' AND ready_at IS NOT NULL
 ORDER BY ready_at DESC, attachment_id DESC LIMIT 1`, task.Scope.WorkspaceID, task.Scope.AgentID, task.Scope.TenantOrgID, task.ID).Scan(&artifact.Ref, &artifact.Kind, &artifact.At)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return w, err
	}
	if err == nil {
		artifact.Source = EmployeeActivityArtifact
		observe(artifact)
	}
	return w, nil
}
