package employeetask

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ListCurrent exposes only the selected requester's independent Employee tasks.
// The caller supplies a trusted scene and a fixed new-wake watermark.
func (s *Store) ListCurrent(ctx context.Context, scope Scope, requester string, before time.Time, limit int) ([]Task, error) {
	if validateScope(scope) != nil || scope.Kind != ScopeScene || strings.TrimSpace(requester) == "" || before.IsZero() || limit < 1 || limit > 10 {
		return nil, ErrInvalid
	}
	args := append(scopeArgs(scope), requester, before, limit)
	rows, err := s.db.Query(ctx, `SELECT `+taskColumns+` FROM employee_task WHERE `+taskScope+` AND requester_ref=$6 AND created_at<=$7 AND owner_loop='employee' AND dispatch_mode='direct' ORDER BY updated_at DESC,id DESC LIMIT $8`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

type CurrentSnapshot struct {
	Task                 Task    `json:"task"`
	LatestRun            *Run    `json:"latest_run,omitempty"`
	Entries              []Entry `json:"entries"`
	Truncated            bool    `json:"truncated"`
	Corrections          []Entry `json:"corrections,omitempty"`
	CorrectionsTruncated bool    `json:"corrections_truncated,omitempty"`
}

// ReadCurrent freezes a consistent ledger boundary under the aggregate's lock.
// Run.Result is the executor's report, not proof of an external delivery.
func (s *Store) ReadCurrent(ctx context.Context, scope Scope, requester, id string) (CurrentSnapshot, error) {
	var out CurrentSnapshot
	if validateScope(scope) != nil || !validUUID(id) || requester == "" {
		return out, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockWorkspace(ctx, tx, scope.WorkspaceID); err != nil {
		return out, err
	}
	out.Task, err = scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM employee_task WHERE `+taskWhere+` FOR SHARE`, taskArgs(scope, id)...))
	if err != nil {
		return out, err
	}
	if out.Task.RequesterRef != requester || out.Task.OwnerLoop != LoopEmployee || out.Task.DispatchMode != DispatchDirect {
		return CurrentSnapshot{}, ErrNotFound
	}
	run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid ORDER BY created_at DESC,id DESC LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id))
	if err == nil {
		out.LatestRun = &run
	} else if !errors.Is(err, ErrNotFound) {
		return out, err
	}
	out.Entries, err = NewStore(tx).ReadEntries(ctx, scope, id, max(int64(0), out.Task.LastEntrySeq-20), 20)
	if err != nil {
		return out, err
	}
	out.Truncated = out.Task.LastEntrySeq > 20
	// Binding constraints have a separate complete, version-fenced snapshot.
	out.Corrections, err = NewStore(tx).CorrectionsThrough(ctx, scope, id, out.Task.LastEntrySeq)
	if errors.Is(err, ErrCorrectionBounds) {
		out.CorrectionsTruncated = true
	} else if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// MaxRunPage bounds one ListRuns page.
const MaxRunPage = 100

// ListRuns pages a Task's Runs in creation order after the Run afterID (empty
// for the first page). An afterID that is not a Run of this Task is not_found.
func (s *Store) ListRuns(ctx context.Context, scope Scope, id, afterID string, limit int) ([]Run, error) {
	if limit < 1 || limit > MaxRunPage || (afterID != "" && !validUUID(afterID)) {
		return nil, ErrInvalid
	}
	if _, err := s.Get(ctx, scope, id); err != nil {
		return nil, err
	}
	args := []any{scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, limit}
	after := ""
	if afterID != "" {
		if _, err := s.getRun(ctx, scope, id, afterID); err != nil {
			return nil, err
		}
		after = ` AND (created_at, id) > (SELECT created_at, id FROM employee_task_run WHERE id=$6::uuid AND task_id=$4::uuid)`
		args = append(args, afterID)
	}
	rows, err := s.db.Query(ctx, `SELECT `+runColumns+` FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid`+after+` ORDER BY created_at, id LIMIT $5`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}
