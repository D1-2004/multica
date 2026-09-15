package dshschedule

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Store only accepts a caller-owned transaction. The service must acquire its
// normal runtime/employee/parent locks and verify current membership, invocation
// policy and originating task authority before calling these methods. A native
// browser grant is not standing authorization. Roll back on EVERY error; publish
// receipts or wake hints only after Commit succeeds. No network I/O belongs in
// this transaction. Workspace/employee deletion must delete both tables while
// holding the same parent locks before deleting the parent.
type Store struct{ Tx pgx.Tx }

type State struct {
	Record
	NextDue     pgtype.Timestamptz
	CancelledAt pgtype.Timestamptz
}

// Read returns immutable provenance and the current lifecycle state. The caller
// must already hold the employee authorization locks for this transaction.
func (s Store) Read(ctx context.Context, key Key) (State, error) {
	if s.Tx == nil || !key.valid() {
		return State{}, ErrInvalid
	}
	return readState(s.Tx.QueryRow(ctx, selectRecord+" FOR SHARE", keyArgs(key)...), key)
}

// List reads one authenticated native Session, with an explicit truncation bit.
// Never turn a bounded list into a claim that the complete Session is empty.
func (s Store) List(ctx context.Context, workspace, agent uuid.UUID, session string) ([]State, bool, error) {
	if s.Tx == nil || !(Key{WorkspaceID: workspace, AgentID: agent, SessionID: session, ScheduleID: "schedule-1"}).valid() {
		return nil, false, ErrInvalid
	}
	rows, err := s.Tx.Query(ctx, `SELECT schedule_id,owner_member_id,source_task_id,prompt,first_due_at,every_seconds,next_due_at,cancelled_at
 FROM dsh_schedule WHERE workspace_id=$1 AND agent_id=$2 AND session_id=$3
 AND cancelled_at IS NULL AND next_due_at IS NOT NULL ORDER BY first_due_at,schedule_id LIMIT 257`, workspace, agent, session)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	states := make([]State, 0, 256)
	truncated := false
	for rows.Next() {
		state := State{Record: Record{Key: Key{WorkspaceID: workspace, AgentID: agent, SessionID: session}}}
		if err := rows.Scan(&state.ScheduleID, &state.OwnerMemberID, &state.SourceTaskID, &state.Prompt, &state.FirstDue, &state.EverySeconds, &state.NextDue, &state.CancelledAt); err != nil {
			return nil, false, err
		}
		if len(states) == 256 {
			truncated = true
			break
		}
		states = append(states, state)
	}
	return states, truncated, rows.Err()
}

const selectRecord = `SELECT owner_member_id,source_task_id,prompt,first_due_at,every_seconds,next_due_at,cancelled_at
 FROM dsh_schedule WHERE workspace_id=$1 AND agent_id=$2 AND session_id=$3 AND schedule_id=$4`

func keyArgs(k Key) []any {
	return []any{k.WorkspaceID, k.AgentID, k.SessionID, k.ScheduleID}
}

func readState(row pgx.Row, key Key) (State, error) {
	state := State{Record: Record{Key: key}}
	err := row.Scan(&state.OwnerMemberID, &state.SourceTaskID, &state.Prompt,
		&state.FirstDue, &state.EverySeconds, &state.NextDue, &state.CancelledAt)
	return state, err
}

// Register is retry-safe even after cancellation or dispatch. A replay returns
// the existing state; it cannot reactivate the reminder or change its owner.
// The service validates future-time rules on first creation. A delayed retry of
// the same durable native record remains valid after its due instant has passed.
func (s Store) Register(ctx context.Context, r Record) (State, error) {
	if s.Tx == nil || r.Validate() != nil {
		return State{}, ErrInvalid
	}
	var bound bool
	err := s.Tx.QueryRow(ctx, `SELECT true FROM dsh_task_binding
 WHERE workspace_id=$1 AND agent_id=$2 AND task_id=$3 AND session_id=$4 FOR SHARE`,
		r.WorkspaceID, r.AgentID, r.SourceTaskID, r.SessionID).Scan(&bound)
	if err != nil {
		return State{}, err
	}
	_, err = s.Tx.Exec(ctx, `INSERT INTO dsh_schedule
 (workspace_id,agent_id,session_id,schedule_id,owner_member_id,source_task_id,prompt,first_due_at,every_seconds,next_due_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$8)
 ON CONFLICT(workspace_id,agent_id,session_id,schedule_id) DO NOTHING`,
		r.WorkspaceID, r.AgentID, r.SessionID, r.ScheduleID, r.OwnerMemberID,
		r.SourceTaskID, r.Prompt, r.FirstDue, r.EverySeconds)
	if err != nil {
		return State{}, err
	}
	state, err := readState(s.Tx.QueryRow(ctx, selectRecord+" FOR UPDATE", keyArgs(r.Key)...), r.Key)
	if err != nil {
		return State{}, err
	}
	if state.OwnerMemberID != r.OwnerMemberID || state.SourceTaskID != r.SourceTaskID ||
		state.Prompt != r.Prompt || !state.FirstDue.Equal(r.FirstDue) || state.EverySeconds != r.EverySeconds {
		return State{}, ErrConflict
	}
	return state, nil
}

// Cancel serializes with admission on the schedule row. Cancellation prevents
// future admissions; already committed tasks retain their own cancellation flow.
// Keep a tombstone so a delayed create retry cannot resurrect the reminder.
func (s Store) Cancel(ctx context.Context, key Key) (bool, error) {
	if s.Tx == nil || !key.valid() {
		return false, ErrInvalid
	}
	result, err := s.Tx.Exec(ctx, `UPDATE dsh_schedule
 SET cancelled_at=COALESCE(cancelled_at,clock_timestamp()),updated_at=clock_timestamp()
 WHERE workspace_id=$1 AND agent_id=$2 AND session_id=$3 AND schedule_id=$4`, keyArgs(key)...)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

// Enqueue must recheck the standing owner's current authority, create a fresh
// platform task and adopt its native execution with due.RequestID in this exact
// transaction. It must not commit or dispatch externally. The return value is
// checked against the persisted native binding before an occurrence is accepted.
type Enqueue func(context.Context, pgx.Tx, Due) (uuid.UUID, error)

type Receipt struct {
	Due
	TaskID uuid.UUID
}

// Dispatch locks one reminder across replicas through the task/occurrence commit.
// SKIP LOCKED permits unrelated Sessions to advance without waiting. Database
// time is sampled after the lock, and late one-shot reminders remain eligible.
func (s Store) Dispatch(ctx context.Context, key Key, enqueue Enqueue) (Receipt, error) {
	if s.Tx == nil || !key.valid() || enqueue == nil {
		return Receipt{}, ErrInvalid
	}
	state, err := readState(s.Tx.QueryRow(ctx, selectRecord+" AND (next_attempt_at IS NULL OR next_attempt_at<=clock_timestamp()) FOR UPDATE SKIP LOCKED", keyArgs(key)...), key)
	if errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, ErrNotDue
	}
	if err != nil {
		return Receipt{}, err
	}
	if state.CancelledAt.Valid || !state.NextDue.Valid {
		return Receipt{}, ErrNotDue
	}
	var now time.Time
	if err := s.Tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return Receipt{}, err
	}
	due, err := Plan(state.Record, state.NextDue.Time, now)
	if err != nil {
		return Receipt{}, err
	}
	taskID, err := enqueue(ctx, s.Tx, due)
	if err != nil {
		return Receipt{}, err
	}
	if taskID == uuid.Nil || taskID == state.SourceTaskID {
		return Receipt{}, ErrInvalid
	}
	var bound bool
	err = s.Tx.QueryRow(ctx, `SELECT true FROM dsh_task_binding
 WHERE workspace_id=$1 AND agent_id=$2 AND session_id=$3 AND request_id=$4 AND task_id=$5 FOR SHARE`,
		key.WorkspaceID, key.AgentID, key.SessionID, due.RequestID, taskID).Scan(&bound)
	if err != nil {
		return Receipt{}, err
	}
	_, err = s.Tx.Exec(ctx, `INSERT INTO dsh_schedule_occurrence
 (workspace_id,agent_id,session_id,schedule_id,occurrence_at,request_id,task_id)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, key.WorkspaceID, key.AgentID, key.SessionID,
		key.ScheduleID, due.At, due.RequestID, taskID)
	if err != nil {
		return Receipt{}, err
	}
	_, err = s.Tx.Exec(ctx, `UPDATE dsh_schedule SET next_due_at=$5,next_attempt_at=NULL,failure_count=0,updated_at=clock_timestamp()
 WHERE workspace_id=$1 AND agent_id=$2 AND session_id=$3 AND schedule_id=$4`,
		key.WorkspaceID, key.AgentID, key.SessionID, key.ScheduleID,
		pgtype.Timestamptz{Time: due.Next, Valid: !due.Next.IsZero()})
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Due: due, TaskID: taskID}, nil
}
