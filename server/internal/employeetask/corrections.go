package employeetask

import (
	"context"
	"errors"
)

const MaxCorrectionCount = 100
const MaxCorrectionBytes = 64 << 10

var ErrCorrectionBounds = errors.New("complete task corrections exceed supported bounds")

// CorrectionsThrough reads the complete accepted constraints at a ledger
// boundary. Callers freeze the Task version under a shared or update lock.
func (s *Store) CorrectionsThrough(ctx context.Context, scope Scope, id string, through int64) ([]Entry, error) {
	if through < 0 {
		return nil, ErrInvalid
	}
	task, err := s.Get(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	if through == 0 {
		through = task.LastEntrySeq
	}
	if through > task.LastEntrySeq {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT `+entryColumns+` FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND kind='steer' AND seq<=$5 ORDER BY seq LIMIT $6`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, through, MaxCorrectionCount+1)
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
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = CheckCorrectionBounds(entries, ""); err != nil {
		return nil, err
	}
	return entries, nil
}

// CheckCorrectionBounds includes an optional pending correction before any
// cancellation or queue mutation. Oversize input never silently drops history.
func CheckCorrectionBounds(entries []Entry, additional string) error {
	count, size := len(entries), len(additional)
	if additional != "" {
		count++
	}
	for _, entry := range entries {
		size += len(entry.Body)
	}
	if count > MaxCorrectionCount || size > MaxCorrectionBytes {
		return ErrCorrectionBounds
	}
	return nil
}
