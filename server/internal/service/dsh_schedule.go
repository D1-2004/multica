package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshschedule"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DSHScheduleActor comes exclusively from task-token middleware. Neither the
// HTTP body nor a native browser grant may select an employee or source task.
type DSHScheduleActor struct {
	dshhost.Key
	TaskID uuid.UUID
}

type DSHScheduleInput struct {
	SessionID    string    `json:"session_id"`
	ScheduleID   string    `json:"schedule_id"`
	Prompt       string    `json:"prompt"`
	FirstDue     time.Time `json:"first_due_at"`
	EverySeconds int64     `json:"every_seconds"`
}

type DSHScheduleView struct {
	SessionID    string     `json:"session_id"`
	ScheduleID   string     `json:"schedule_id"`
	Prompt       string     `json:"prompt"`
	FirstDue     time.Time  `json:"first_due_at"`
	EverySeconds int64      `json:"every_seconds"`
	NextDue      *time.Time `json:"next_due_at"`
	State        string     `json:"state"`
}

type DSHScheduleList struct {
	Items     []DSHScheduleView `json:"items"`
	Truncated bool              `json:"truncated"`
}

func scheduleView(state dshschedule.State, now time.Time) DSHScheduleView {
	view := DSHScheduleView{SessionID: state.SessionID, ScheduleID: state.ScheduleID,
		Prompt: state.Prompt, FirstDue: state.FirstDue, EverySeconds: state.EverySeconds, State: "consumed"}
	if state.NextDue.Valid {
		next := state.NextDue.Time
		view.NextDue = &next
		view.State = "scheduled"
		if !next.After(now) {
			view.State = "overdue"
		}
	}
	if state.CancelledAt.Valid {
		view.State = "cancelled"
	}
	return view
}

func scheduleTaskMatches(actor DSHScheduleActor, task db.AgentTaskQueue, agent db.Agent) bool {
	return actor.WorkspaceID != uuid.Nil && actor.AgentID != uuid.Nil && actor.TaskID != uuid.Nil &&
		task.ID.Valid && uuid.UUID(task.ID.Bytes) == actor.TaskID && task.AgentID == agent.ID &&
		agent.ID.Valid && uuid.UUID(agent.ID.Bytes) == actor.AgentID && agent.WorkspaceID.Valid &&
		uuid.UUID(agent.WorkspaceID.Bytes) == actor.WorkspaceID && agent.Kind == "user" && !agent.ArchivedAt.Valid &&
		agent.RuntimeMode == "cloud" && agent.RuntimeID.Valid && task.RuntimeID == agent.RuntimeID &&
		(task.Status == "running" || task.Status == "dispatched")
}

// withDSHScheduleTask retains current task, membership and employee authority
// through the write. Historical accountability alone never grants permission:
// an unattended parent must have an actual persisted Schedule occurrence.
func (s *TaskService) withDSHScheduleTask(ctx context.Context, actor DSHScheduleActor, sessionID string, invoke DSHNativeInvokeCheck,
	fn func(pgx.Tx, uuid.UUID, time.Time) error) error {
	if s == nil || s.Queries == nil || s.TxStarter == nil || invoke == nil || fn == nil ||
		actor.TaskID == uuid.Nil || actor.WorkspaceID == uuid.Nil || actor.AgentID == uuid.Nil || !dshhost.ValidSessionID(sessionID) {
		return dshhost.ErrNativeAccessDenied
	}
	taskID := pgtype.UUID{Bytes: actor.TaskID, Valid: true}
	initial, err := s.Queries.GetAgentTask(ctx, taskID)
	if err != nil || !initial.RuntimeID.Valid {
		return dshhost.ErrNativeAccessDenied
	}
	return s.runInTxWithHandle(ctx, func(q *db.Queries, tx pgx.Tx) error {
		if err := lockDSHEmployeeAdmission(ctx, tx, actor.Key, initial.RuntimeID); err != nil {
			return err
		}
		if _, err := q.LockWorkspaceForChatSessionCreate(ctx, pgtype.UUID{Bytes: actor.WorkspaceID, Valid: true}); err != nil {
			return err
		}
		agent, err := q.GetAgentForClaimUpdate(ctx, pgtype.UUID{Bytes: actor.AgentID, Valid: true})
		if err != nil {
			return dshhost.ErrNativeAccessDenied
		}
		var locked pgtype.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE id=$1 FOR SHARE`, taskID).Scan(&locked); err != nil {
			return dshhost.ErrNativeAccessDenied
		}
		task, err := q.GetAgentTask(ctx, taskID)
		if err != nil || task.RuntimeID != initial.RuntimeID || !scheduleTaskMatches(actor, task, agent) {
			return dshhost.ErrNativeAccessDenied
		}
		if err := tx.QueryRow(ctx, `SELECT task_id FROM dsh_task_binding WHERE workspace_id=$1 AND agent_id=$2 AND task_id=$3 AND session_id=$4 FOR SHARE`,
			actor.WorkspaceID, actor.AgentID, actor.TaskID, sessionID).Scan(&locked); err != nil {
			return dshhost.ErrNativeAccessDenied
		}
		if err := tx.QueryRow(ctx, `SELECT id FROM agent_runtime WHERE id=$1 AND workspace_id=$2 FOR SHARE`, agent.RuntimeID, agent.WorkspaceID).Scan(&locked); err != nil {
			return dshhost.ErrNativeAccessDenied
		}
		runtime, err := q.GetAgentRuntime(ctx, agent.RuntimeID)
		if err != nil || runtime.Provider != "dsh" || !IsFCE2BRuntime(runtime) {
			return dshhost.ErrNativeAccessDenied
		}
		var memberID, userID pgtype.UUID
		if task.OriginatorUserID.Valid {
			userID = task.OriginatorUserID
			err = tx.QueryRow(ctx, `SELECT id FROM member WHERE workspace_id=$1 AND user_id=$2 FOR SHARE`, agent.WorkspaceID, userID).Scan(&memberID)
		} else {
			err = tx.QueryRow(ctx, `SELECT m.id,m.user_id FROM dsh_schedule_occurrence o
 JOIN dsh_schedule s ON s.workspace_id=o.workspace_id AND s.agent_id=o.agent_id AND s.session_id=o.session_id AND s.schedule_id=o.schedule_id
 JOIN member m ON m.id=s.owner_member_id AND m.workspace_id=s.workspace_id
 WHERE o.workspace_id=$1 AND o.agent_id=$2 AND o.task_id=$3 AND o.session_id=$4 FOR SHARE OF m`,
				actor.WorkspaceID, actor.AgentID, actor.TaskID, sessionID).Scan(&memberID, &userID)
		}
		if err != nil || !memberID.Valid || !userID.Valid {
			return dshhost.ErrNativeAccessDenied
		}
		if err := invoke(ctx, q, agent, userID); err != nil {
			return dshhost.ErrNativeAccessDenied
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		return fn(tx, uuid.UUID(memberID.Bytes), now)
	})
}

func (s *TaskService) RegisterDSHSchedule(ctx context.Context, actor DSHScheduleActor, input DSHScheduleInput, invoke DSHNativeInvokeCheck) (DSHScheduleView, error) {
	var view DSHScheduleView
	err := s.withDSHScheduleTask(ctx, actor, input.SessionID, invoke, func(tx pgx.Tx, owner uuid.UUID, now time.Time) error {
		r := dshschedule.Record{Key: dshschedule.Key{WorkspaceID: actor.WorkspaceID, AgentID: actor.AgentID, SessionID: input.SessionID, ScheduleID: input.ScheduleID},
			OwnerMemberID: owner, SourceTaskID: actor.TaskID, Prompt: input.Prompt, FirstDue: input.FirstDue, EverySeconds: input.EverySeconds}
		if err := r.Validate(); err != nil {
			return err
		}
		store := dshschedule.Store{Tx: tx}
		existing, err := store.Read(ctx, r.Key)
		if err == nil {
			// Recovery may arrive under a fresh task. Preserve the original task
			// provenance, require the same owner, and never reactivate a tombstone.
			if existing.OwnerMemberID != owner || existing.Prompt != r.Prompt || !existing.FirstDue.Equal(r.FirstDue) || existing.EverySeconds != r.EverySeconds {
				return dshschedule.ErrConflict
			}
			view = scheduleView(existing, now)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !input.FirstDue.After(now) {
			return dshschedule.ErrInvalid
		}
		state, err := store.Register(ctx, r)
		if err != nil {
			return err
		}
		view = scheduleView(state, now)
		return nil
	})
	return view, err
}

func (s *TaskService) ListDSHSchedules(ctx context.Context, actor DSHScheduleActor, sessionID string, invoke DSHNativeInvokeCheck) (DSHScheduleList, error) {
	result := DSHScheduleList{Items: []DSHScheduleView{}}
	err := s.withDSHScheduleTask(ctx, actor, sessionID, invoke, func(tx pgx.Tx, _ uuid.UUID, now time.Time) error {
		states, truncated, err := (dshschedule.Store{Tx: tx}).List(ctx, actor.WorkspaceID, actor.AgentID, sessionID)
		if err != nil {
			return err
		}
		result.Truncated = truncated
		for _, state := range states {
			result.Items = append(result.Items, scheduleView(state, now))
		}
		return nil
	})
	return result, err
}

func (s *TaskService) CancelDSHSchedule(ctx context.Context, actor DSHScheduleActor, sessionID, scheduleID string, invoke DSHNativeInvokeCheck) (bool, error) {
	found := false
	err := s.withDSHScheduleTask(ctx, actor, sessionID, invoke, func(tx pgx.Tx, owner uuid.UUID, _ time.Time) error {
		key := dshschedule.Key{WorkspaceID: actor.WorkspaceID, AgentID: actor.AgentID, SessionID: sessionID, ScheduleID: scheduleID}
		store := dshschedule.Store{Tx: tx}
		state, err := store.Read(ctx, key)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if state.OwnerMemberID != owner {
			return dshhost.ErrNativeAccessDenied
		}
		if state.CancelledAt.Valid || !state.NextDue.Valid {
			return nil
		}
		found, err = store.Cancel(ctx, key)
		return err
	})
	return found, err
}
