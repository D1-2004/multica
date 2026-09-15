package dshschedule

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Candidate is an observation, not a lease. Dispatch must recheck the record in
// its own transaction. NextDue fences delayed failure bookkeeping after another
// replica has already committed the occurrence.
type Candidate struct {
	Key
	NextDue       time.Time
	OwnerMemberID uuid.UUID
	ObservedAt    time.Time
}

type QueueDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type Queue struct{ DB QueueDB }

func (q Queue) Candidates(ctx context.Context, limit int) ([]Candidate, error) {
	if q.DB == nil || limit < 1 || limit > 256 {
		return nil, ErrInvalid
	}
	rows, err := q.DB.Query(ctx, `SELECT workspace_id,agent_id,session_id,schedule_id,next_due_at,owner_member_id,clock_timestamp()
 FROM (SELECT DISTINCT ON (s.workspace_id,s.agent_id,s.session_id,s.owner_member_id)
 s.workspace_id,s.agent_id,s.session_id,s.schedule_id,s.next_due_at,s.owner_member_id
 FROM dsh_schedule s WHERE s.cancelled_at IS NULL AND s.next_due_at<=clock_timestamp()
 AND NOT EXISTS(SELECT 1 FROM dsh_schedule pending
 WHERE pending.workspace_id=s.workspace_id AND pending.agent_id=s.agent_id AND pending.session_id=s.session_id AND pending.owner_member_id=s.owner_member_id
 AND pending.cancelled_at IS NULL AND pending.next_due_at<=clock_timestamp() AND pending.next_attempt_at>clock_timestamp())
 ORDER BY s.workspace_id,s.agent_id,s.session_id,s.owner_member_id,s.next_due_at,s.created_at,s.schedule_id) candidates
 ORDER BY next_due_at,workspace_id,agent_id,session_id,schedule_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Candidate, 0, limit)
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.WorkspaceID, &c.AgentID, &c.SessionID, &c.ScheduleID, &c.NextDue, &c.OwnerMemberID, &c.ObservedAt); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// Defer never consumes a reminder or stores an error message (which may contain
// private payloads). A later retry checks authority again. Capping the delay
// keeps revoked/restored permissions recoverable without starving other records.
func (q Queue) Defer(ctx context.Context, c Candidate) error {
	if q.DB == nil || !c.Key.valid() || !validInstant(c.NextDue) || c.OwnerMemberID == uuid.Nil || c.ObservedAt.IsZero() {
		return ErrInvalid
	}
	_, err := q.DB.Exec(ctx, `UPDATE dsh_schedule s SET
 next_attempt_at=clock_timestamp()+LEAST(300,30*power(2,LEAST(s.failure_count,4))) * interval '1 second',
 failure_count=LEAST(s.failure_count+1,1000000),updated_at=clock_timestamp()
 WHERE s.workspace_id=$1 AND s.agent_id=$2 AND s.session_id=$3 AND s.owner_member_id=$6
 AND s.next_due_at<=$7 AND s.updated_at<=$7 AND s.cancelled_at IS NULL
 AND (s.next_attempt_at IS NULL OR s.next_attempt_at<=clock_timestamp())
 AND EXISTS(SELECT 1 FROM dsh_schedule seed WHERE seed.workspace_id=$1 AND seed.agent_id=$2 AND seed.session_id=$3 AND seed.schedule_id=$4
 AND seed.owner_member_id=$6 AND seed.next_due_at=$5 AND seed.cancelled_at IS NULL)`,
		c.WorkspaceID, c.AgentID, c.SessionID, c.ScheduleID, c.NextDue, c.OwnerMemberID, c.ObservedAt)
	return err
}

type WorkQueue interface {
	Candidates(context.Context, int) ([]Candidate, error)
	Defer(context.Context, Candidate) error
}
type Dispatcher interface {
	DispatchDSHSchedule(context.Context, Key) (Receipt, error)
}

type SweepResult struct{ Examined, Admitted, Deferred, Skipped int }

// Sweep uses the durable task queue for recovery after admission. It does not
// require a live sandbox and never interprets a task receipt as model delivery.
// Each candidate is bounded independently so a slow/revoked employee cannot
// monopolize an entire scan. Crash before defer leaves the record eligible.
func Sweep(ctx context.Context, queue WorkQueue, dispatcher Dispatcher) (SweepResult, error) {
	var result SweepResult
	if queue == nil || dispatcher == nil {
		return result, ErrInvalid
	}
	candidates, err := queue.Candidates(ctx, 64)
	if err != nil {
		return result, err
	}
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Examined++
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := dispatcher.DispatchDSHSchedule(attempt, c.Key)
		cancel()
		switch {
		case err == nil:
			result.Admitted++
		case errors.Is(err, ErrNotDue):
			result.Skipped++
		default:
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if err := queue.Defer(ctx, c); err != nil {
				return result, err
			}
			result.Deferred++
		}
	}
	return result, nil
}
