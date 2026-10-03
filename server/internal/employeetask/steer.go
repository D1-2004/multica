package employeetask

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Steer is the Task Service control that interrupts the current execution and
// resumes it with a human correction as the next input. It is not live steer:
// no backend writes into a running process. The old writer is cancelled, a new
// Run carries every recorded correction, and the new Run may only be claimed
// once the old writer is fenced.

// SteerOutcome says what a steer request actually did to the executions.
type SteerOutcome string

const (
	// SteerInterrupted cancelled a claimed Run; its successor waits for exit proof.
	SteerInterrupted SteerOutcome = "interrupted"
	// SteerMerged joined an unclaimed Run, including an earlier steer successor.
	SteerMerged SteerOutcome = "merged"
	// SteerContinued found no active Run and started a continuation.
	SteerContinued SteerOutcome = "continued"
)

// ControlCapabilities describes which controls a dispatch backend honors.
// Steer here always means cancel plus resume; LiveSteer stays false until a
// runtime proves it can apply input to a running process.
type ControlCapabilities struct {
	Cancel      bool `json:"cancel"`
	Steer       bool `json:"steer"`
	LiveSteer   bool `json:"live_steer"`
	ResumeAfter bool `json:"resume_after_stop"`
}

// Capabilities reports the controls each dispatch backend implements.
func Capabilities(mode DispatchMode) ControlCapabilities {
	switch mode {
	case DispatchDirect, DispatchIssue:
		return ControlCapabilities{Cancel: true, Steer: true, ResumeAfter: true}
	default:
		return ControlCapabilities{}
	}
}

// Writer fence evidence recorded by the host for a failed or cancelled Run.
const (
	// FenceNeverClaimed: the queue row was cancelled before any runtime claimed it.
	FenceNeverClaimed = "never_claimed"
	// FenceClaimBarrier: the cancelled row holds the exit barrier, so no successor
	// of the same task can be claimed until the runtime proves the process exited.
	FenceClaimBarrier = "claim_barrier"
	// FenceProcessStopped: the owning runtime acknowledged the process exit.
	FenceProcessStopped = "process_stopped"
)

type SteerParams struct {
	Source   Source `json:"source"`
	ActorRef string `json:"actor_ref"`
	// Body is the human correction, verbatim. It becomes the next Run's input.
	Body string `json:"body"`
	// MergeRunID names an active Run whose queue row is not yet claimed; the
	// correction joins that Run's frozen input. Empty means the host has already
	// recorded the interrupted Run, or the task had no active Run.
	MergeRunID string `json:"merge_run_id,omitempty"`
	// InterruptedRunID names the Run the host cancelled for this same request.
	// Only that cancellation may be reopened; a task a human stopped stays stopped.
	InterruptedRunID string `json:"interrupted_run_id,omitempty"`
	// ExpectedVersion is optional; zero skips the aggregate CAS.
	ExpectedVersion int64 `json:"-"`
}

type FenceWriterParams struct {
	Source   Source `json:"source"`
	RunID    string `json:"run_id"`
	Evidence string `json:"evidence"`
}

// Steer records a correction. With MergeRunID the active Run absorbs it;
// otherwise the task must have no active Run and becomes ready for a successor,
// which the host starts with StartRun in the same transaction.
func (s *Store) Steer(ctx context.Context, scope Scope, id string, p SteerParams) (Task, Entry, error) {
	if strings.TrimSpace(p.Body) == "" || strings.TrimSpace(p.ActorRef) == "" || p.ExpectedVersion < 0 {
		return Task{}, Entry{}, ErrInvalid
	}
	if (p.MergeRunID != "" && !validUUID(p.MergeRunID)) || (p.InterruptedRunID != "" && (!validUUID(p.InterruptedRunID) || p.MergeRunID != "")) {
		return Task{}, Entry{}, ErrInvalid
	}
	return s.mutate(ctx, scope, id, p.Source, p, p.ExpectedVersion, func(tx pgx.Tx, task *Task) (Entry, error) {
		e := Entry{Kind: "steer", ActorRef: p.ActorRef, Body: p.Body, RunID: p.MergeRunID}
		if p.MergeRunID != "" {
			if task.ActiveRunID != p.MergeRunID || task.State != StateRunning {
				return Entry{}, ErrConflict
			}
			// The merged Run has not started executing, so its frozen input
			// boundary moves to include this correction.
			tag, err := tx.Exec(ctx, `UPDATE employee_task_run SET input_seq=$6 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND id=$5::uuid AND state='running'`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, p.MergeRunID, task.LastEntrySeq+1)
			if err != nil {
				return Entry{}, err
			}
			if tag.RowsAffected() != 1 {
				return Entry{}, ErrConflict
			}
			return e, nil
		}
		if task.ActiveRunID != "" {
			return Entry{}, ErrActiveRun
		}
		if task.State == StateCancelled {
			// Like AppendInput, a correction never lifts a human stop. The only
			// cancellation it may reopen is the one this request just made.
			var latestID string
			var latestState State
			err := tx.QueryRow(ctx, `SELECT id::text, state FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid ORDER BY created_at DESC, id DESC LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id).Scan(&latestID, &latestState)
			if err != nil {
				return Entry{}, err
			}
			if p.InterruptedRunID == "" || p.InterruptedRunID != latestID || latestState != StateCancelled {
				return Entry{}, ErrStopped
			}
		}
		// A correction reopens a finished goal. It does not prove that a failed
		// or cancelled writer exited; StartRun still enforces the writer fence.
		task.State = StateReady
		return e, nil
	})
}

// FenceRunWriter records host evidence that a failed or cancelled Run can no
// longer race a successor. It is the only way such a Run stops blocking StartRun.
func (s *Store) FenceRunWriter(ctx context.Context, scope Scope, id string, p FenceWriterParams) (Task, Entry, error) {
	if !validUUID(p.RunID) {
		return Task{}, Entry{}, ErrInvalid
	}
	switch p.Evidence {
	case FenceNeverClaimed, FenceClaimBarrier, FenceProcessStopped:
	default:
		return Task{}, Entry{}, ErrInvalid
	}
	return s.mutate(ctx, scope, id, p.Source, p, 0, func(tx pgx.Tx, task *Task) (Entry, error) {
		var state State
		err := tx.QueryRow(ctx, `SELECT state FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND id=$5::uuid`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, p.RunID).Scan(&state)
		if err != nil {
			return Entry{}, err
		}
		if state != StateFailed && state != StateCancelled {
			return Entry{}, ErrConflict
		}
		return Entry{Kind: "writer_fenced", RunID: p.RunID, Body: p.Evidence}, nil
	})
}

// Corrections returns the most recent recorded steer inputs in ledger order.
// Rendering them never consumes them: every successor sees all of them.
func (s *Store) Corrections(ctx context.Context, scope Scope, id string, limit int) ([]Entry, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	if _, err := s.Get(ctx, scope, id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+entryColumns+` FROM (SELECT * FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND kind='steer' ORDER BY seq DESC LIMIT $5) latest ORDER BY seq`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// RunBySource finds the Run a source started, so a replayed steer returns the
// successor it created instead of creating another.
func (s *Store) RunBySource(ctx context.Context, scope Scope, id string, source Source) (Run, error) {
	if err := validateSource(source); err != nil {
		return Run{}, err
	}
	return scanRun(s.db.QueryRow(ctx, `SELECT `+prefixedRunColumns+` FROM employee_task_entry e JOIN employee_task_run r ON r.id=e.run_id AND r.task_id=e.task_id WHERE e.workspace_id=$1::uuid AND e.agent_id=$2::uuid AND e.tenant_org_id=$3 AND e.task_id=$4::uuid AND e.source_namespace=$5 AND e.source_key=$6 AND e.kind='run_started'`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, source.Namespace, source.Key))
}

// EntryBySource returns the entry an exact source recorded, if any.
func (s *Store) EntryBySource(ctx context.Context, scope Scope, id string, source Source) (Entry, error) {
	if err := validateSource(source); err != nil {
		return Entry{}, err
	}
	return scanEntry(s.db.QueryRow(ctx, `SELECT `+entryColumns+` FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND source_namespace=$5 AND source_key=$6`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, source.Namespace, source.Key))
}

// LatestRun returns the task's most recent Run.
func (s *Store) LatestRun(ctx context.Context, scope Scope, id string) (Run, error) {
	return scanRun(s.db.QueryRow(ctx, `SELECT `+runColumns+` FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid ORDER BY created_at DESC, id DESC LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id))
}

const prefixedRunColumns = `r.id::text, r.task_id::text, r.queue_task_id::text, r.goal_revision,
 r.input_seq, r.state, r.result, r.result_ref, r.created_at, r.finished_at`

// SteerCorrection is one recorded correction rendered into a successor packet.
type SteerCorrection struct {
	Ref      string
	ActorRef string
	Body     string
}

// WithCorrections renders recorded corrections into an already compiled work
// packet, in the compiler's CURRENT CORRECTIONS form and position. The base
// packet is the original Direct input; every successor is rebuilt from it, so
// corrections are never rendered twice and never consumed by rendering.
func WithCorrections(packet string, corrections []SteerCorrection) string {
	if len(corrections) == 0 {
		return packet
	}
	block := []string{"- CURRENT CORRECTIONS (Host verified; apply before older task text):"}
	for _, c := range corrections {
		if strings.TrimSpace(c.Body) == "" {
			continue
		}
		block = append(block, fmt.Sprintf("  [%s] from %s\n%s", c.Ref, c.ActorRef, c.Body))
	}
	block = append(block, "  An earlier run of this task was interrupted to apply these corrections. If its conversation and workspace are still available, continue from that progress; do not redo finished work that the corrections leave valid.")
	lines := strings.Split(packet, "\n")
	at := 0
	for i, line := range lines {
		if strings.HasPrefix(line, "- Principal:") {
			at = i + 1
			break
		}
	}
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:at]...)
	out = append(out, block...)
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n")
}
