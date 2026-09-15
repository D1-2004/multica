package dshschedule

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Candidate is an observation, not a lease. Dispatch must recheck the record in
// its own transaction. NextDue fences delayed failure bookkeeping after another
// replica has already committed the occurrence.
type Candidate struct {
	Key
	NextDue time.Time
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
	rows, err := q.DB.Query(ctx, `SELECT workspace_id,agent_id,session_id,schedule_id,next_due_at
 FROM dsh_schedule WHERE cancelled_at IS NULL AND next_due_at<=clock_timestamp()
 AND (next_attempt_at IS NULL OR next_attempt_at<=clock_timestamp())
 ORDER BY COALESCE(next_attempt_at,next_due_at),workspace_id,agent_id,session_id,schedule_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Candidate, 0, limit)
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.WorkspaceID, &c.AgentID, &c.SessionID, &c.ScheduleID, &c.NextDue); err != nil {
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
	if q.DB == nil || !c.Key.valid() || !validInstant(c.NextDue) {
		return ErrInvalid
	}
	_, err := q.DB.Exec(ctx, `UPDATE dsh_schedule SET
 next_attempt_at=clock_timestamp()+LEAST(300,30*power(2,LEAST(failure_count,4))) * interval '1 second',
 failure_count=LEAST(failure_count+1,1000000),updated_at=clock_timestamp()
 WHERE workspace_id=$1 AND agent_id=$2 AND session_id=$3 AND schedule_id=$4
 AND next_due_at=$5 AND cancelled_at IS NULL
 AND (next_attempt_at IS NULL OR next_attempt_at<=clock_timestamp())`,
		c.WorkspaceID, c.AgentID, c.SessionID, c.ScheduleID, c.NextDue)
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
