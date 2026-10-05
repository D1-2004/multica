package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
)

// EmployeeTaskWaitFacts reads the wait a lifecycle v2 goal is blocked on from
// PostgreSQL: the P1 wait row (employee_task_wait), and for a collection wait
// the taskinput counts (expected/received). It never reads an answer body or
// a participant identity, so nothing a notice renders can leak one.
type EmployeeTaskWaitFacts struct{}

// Wait ref prefixes understood for schedule waits.
const (
	// employeeScheduleRoutineTrigger names a scene routine trigger whose
	// scheduler-owned next_run_at is the due time.
	employeeScheduleRoutineTrigger = "routine_trigger:"
)

// ReadEmployeeTaskWait reports the dominant open wait of a goal that is
// waiting: the earliest-opened open mandatory wait of the current goal
// revision. A goal that is not in state waiting (for example a Run is still
// active) has no aged wait; its execution is watched instead.
func (EmployeeTaskWaitFacts) ReadEmployeeTaskWait(ctx context.Context, tx pgx.Tx, task employeetask.Task) (EmployeeTaskWait, bool, error) {
	if tx == nil || task.State != employeetask.StateWaiting {
		return EmployeeTaskWait{}, false, nil
	}
	scope := task.Scope
	var id, kind, ref string
	var since time.Time
	// Disposing a question mutes only that question's reminder. Its durable
	// wait and Task remain unchanged; select another open wait if one exists.
	err := tx.QueryRow(ctx, `SELECT w.id::text,w.kind,w.ref_id,w.created_at FROM employee_task_wait w
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid
 AND state='open' AND mandatory AND goal_revision=$5
 AND NOT (w.kind='human_input' AND EXISTS (
 SELECT 1 FROM employee_human_question q JOIN employee_human_response r ON r.id=q.response_id AND r.question_id=q.id
 WHERE q.id::text=w.ref_id AND q.workspace_id=w.workspace_id AND q.agent_id=w.agent_id
 AND q.tenant_org_id=w.tenant_org_id AND q.scene_id=$6::uuid AND q.task_id=w.task_id
 AND q.goal_revision=w.goal_revision AND ((q.state='answered' AND r.body->>'intent'='dismiss')
 OR (q.state='deferred' AND r.body->>'intent'='defer'))))
 ORDER BY opened_seq,w.id LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, task.ID, task.GoalRevision, scope.Scene.SceneID).Scan(&id, &kind, &ref, &since)
	if errors.Is(err, pgx.ErrNoRows) {
		return EmployeeTaskWait{}, false, nil
	}
	if err != nil {
		return EmployeeTaskWait{}, false, err
	}
	// The P1 wait id is stable for the wait's lifetime; counts and progress
	// change inside one boundary instead of superseding the episode.
	wait := EmployeeTaskWait{Key: id, Since: since}
	switch employeetask.WaitKind(kind) {
	case employeetask.WaitCollection:
		return readEmployeeCollectionWait(ctx, tx, task, ref, wait)
	case employeetask.WaitHumanInput:
		wait.Kind = EmployeeTaskWaitInputs
		return wait, true, nil
	case employeetask.WaitSchedule:
		due, ok, err := readEmployeeScheduleDue(ctx, tx, task, ref)
		if err != nil || !ok {
			return EmployeeTaskWait{}, false, err
		}
		wait.Kind, wait.DueAt = EmployeeTaskWaitSchedule, due
		return wait, true, nil
	case employeetask.WaitUpstreamTask:
		wait.Kind, wait.Reason = EmployeeTaskWaitDependency, EmployeeWatchdogUpstreamTask
		return wait, true, nil
	case employeetask.WaitExternal:
		wait.Kind, wait.Reason = EmployeeTaskWaitDependency, EmployeeWatchdogExternalEvent
		return wait, true, nil
	}
	return EmployeeTaskWait{}, false, nil
}

// readEmployeeCollectionWait counts the collection's slots. Only an open
// collection is a wait for inputs: once every slot is filled (ready) the
// collection.ready wake owns the next step, and a closed collection is
// resolved by its own lifecycle.
func readEmployeeCollectionWait(ctx context.Context, tx pgx.Tx, task employeetask.Task, ref string, wait EmployeeTaskWait) (EmployeeTaskWait, bool, error) {
	parsed, err := uuid.Parse(ref)
	if err != nil || parsed.String() != ref {
		return EmployeeTaskWait{}, false, nil
	}
	scope := task.Scope
	var state string
	err = tx.QueryRow(ctx, `SELECT state,expected_count,received_count FROM employee_task_collection
 WHERE id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4 AND task_id=$5::uuid`,
		ref, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, task.ID).Scan(&state, &wait.Expected, &wait.Received)
	if errors.Is(err, pgx.ErrNoRows) {
		return EmployeeTaskWait{}, false, nil
	}
	if err != nil {
		return EmployeeTaskWait{}, false, err
	}
	if state != "open" {
		return EmployeeTaskWait{}, false, nil
	}
	// An accepted answer is progress for this wait: Host time of the newest
	// accepted input row (an answer or an explicit correction).
	var inputID string
	var at time.Time
	err = tx.QueryRow(ctx, `SELECT id::text,created_at FROM employee_task_input
 WHERE collection_id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4 AND task_id=$5::uuid
 ORDER BY created_at DESC,id DESC LIMIT 1`, ref, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, task.ID).Scan(&inputID, &at)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return EmployeeTaskWait{}, false, err
	}
	if err == nil {
		wait.Progress, _ = wait.Progress.Observe(EmployeeActivity{Source: EmployeeActivityTaskInput, Ref: inputID, Kind: "answer", At: at})
	}
	wait.Kind = EmployeeTaskWaitInputs
	return wait, true, nil
}

// readEmployeeScheduleDue resolves a schedule wait's due time. An RFC 3339
// ref is the due time itself; a routine trigger ref uses the scheduler's
// next_run_at for an enabled trigger of an autopilot in the Task's workspace.
// Any other ref has no known due time and is not aged.
func readEmployeeScheduleDue(ctx context.Context, tx pgx.Tx, task employeetask.Task, ref string) (time.Time, bool, error) {
	if at, err := time.Parse(time.RFC3339, ref); err == nil {
		return at, true, nil
	}
	trigger, ok := strings.CutPrefix(ref, employeeScheduleRoutineTrigger)
	if !ok {
		return time.Time{}, false, nil
	}
	if parsed, err := uuid.Parse(trigger); err != nil || parsed.String() != trigger {
		return time.Time{}, false, nil
	}
	var next *time.Time
	err := tx.QueryRow(ctx, `SELECT t.next_run_at FROM autopilot_trigger t JOIN autopilot a ON a.id=t.autopilot_id
 WHERE t.id=$1::uuid AND a.workspace_id=$2::uuid AND t.enabled`, trigger, task.Scope.WorkspaceID).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && next == nil) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return *next, true, nil
}
