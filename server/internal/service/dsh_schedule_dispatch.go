package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshschedule"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Admission uses the same lock order as native human chat but has its own
// standing authority. No browser grant, previous task token or live Host is
// required. Every occurrence is a fresh automatic task, not a human message.
func (s *TaskService) withDSHScheduleAdmission(ctx context.Context, key dshschedule.Key, invoke DSHNativeInvokeCheck,
	fn func(*db.Queries, pgx.Tx, db.Agent, pgtype.UUID, dshhost.SessionScope, dshschedule.State) error) error {
	if key.Validate() != nil || s == nil || s.Queries == nil || s.TxStarter == nil || invoke == nil || fn == nil {
		return dshhost.ErrNativeAccessDenied
	}
	employee := dshhost.Key{WorkspaceID: key.WorkspaceID, AgentID: key.AgentID}
	initial, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: pgtype.UUID{Bytes: key.AgentID, Valid: true}, WorkspaceID: pgtype.UUID{Bytes: key.WorkspaceID, Valid: true}})
	if err != nil || !initial.RuntimeID.Valid {
		return dshhost.ErrNativeAccessDenied
	}
	return s.runInTxWithHandle(ctx, func(q *db.Queries, tx pgx.Tx) error {
		if err := lockDSHEmployeeAdmission(ctx, tx, employee, initial.RuntimeID); err != nil {
			return err
		}
		if _, err := q.LockWorkspaceForChatSessionCreate(ctx, initial.WorkspaceID); err != nil {
			return err
		}
		scope := dshhost.SessionScope{Key: employee}
		if err := tx.QueryRow(ctx, `SELECT scope_kind,scope_id,epoch_id FROM dsh_employee_session WHERE workspace_id=$1 AND agent_id=$2 AND session_id=$3 FOR SHARE`, key.WorkspaceID, key.AgentID, key.SessionID).Scan(&scope.Kind, &scope.ID, &scope.Epoch); err != nil {
			return dshhost.ErrNativeAccessDenied
		}
		parent := pgtype.UUID{Bytes: scope.ID, Valid: true}
		var session db.ChatSession
		switch scope.Kind {
		case "chat":
			if _, err := q.LockChatSessionForRuntimeBind(ctx, parent); err != nil {
				return err
			}
			session, err = q.GetChatSession(ctx, parent)
			if err != nil || session.Status != "active" || session.AgentID != initial.ID || session.WorkspaceID != initial.WorkspaceID || (session.RuntimeID.Valid && session.RuntimeID != initial.RuntimeID) {
				return dshhost.ErrNativeAccessDenied
			}
		case "issue":
			if _, err := q.LockIssueForChannelMediaBind(ctx, db.LockIssueForChannelMediaBindParams{ID: parent, WorkspaceID: initial.WorkspaceID}); err != nil {
				return dshhost.ErrNativeAccessDenied
			}
		case "task":
			var found bool
			if err := tx.QueryRow(ctx, `SELECT true FROM agent_task_queue t JOIN agent a ON a.id=t.agent_id WHERE t.id=$1 AND t.agent_id=$2 AND a.workspace_id=$3 FOR SHARE OF t`, parent, initial.ID, initial.WorkspaceID).Scan(&found); err != nil {
				return dshhost.ErrNativeAccessDenied
			}
		default:
			return dshhost.ErrNativeAccessDenied
		}
		agent, err := q.GetAgentForClaimUpdate(ctx, initial.ID)
		if err != nil || agent.WorkspaceID != initial.WorkspaceID || agent.RuntimeID != initial.RuntimeID || agent.Kind != "user" || agent.ArchivedAt.Valid || agent.RuntimeMode != "cloud" {
			return dshhost.ErrNativeAccessDenied
		}
		var locked pgtype.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM agent_runtime WHERE id=$1 AND workspace_id=$2 FOR SHARE`, agent.RuntimeID, agent.WorkspaceID).Scan(&locked); err != nil {
			return dshhost.ErrNativeAccessDenied
		}
		runtime, err := q.GetAgentRuntime(ctx, agent.RuntimeID)
		if err != nil || runtime.Provider != "dsh" || !IsFCE2BRuntime(runtime) {
			return dshhost.ErrNativeAccessDenied
		}
		state, err := (dshschedule.Store{Tx: tx}).Read(ctx, key)
		if errors.Is(err, pgx.ErrNoRows) {
			return dshschedule.ErrNotDue
		}
		if err != nil {
			return err
		}
		if state.CancelledAt.Valid || !state.NextDue.Valid {
			return dshschedule.ErrNotDue
		}
		var userID pgtype.UUID
		if err := tx.QueryRow(ctx, `SELECT user_id FROM member WHERE id=$1 AND workspace_id=$2 FOR SHARE`, state.OwnerMemberID, agent.WorkspaceID).Scan(&userID); err != nil || !userID.Valid {
			return dshhost.ErrNativeAccessDenied
		}
		if scope.Kind == "chat" && session.CreatorID != userID {
			return dshhost.ErrNativeAccessDenied
		}
		if err := invoke(ctx, q, agent, userID); err != nil {
			return dshhost.ErrNativeAccessDenied
		}
		return fn(q, tx, agent, userID, scope, state)
	})
}

// DispatchDSHSchedule commits task/input/native binding/occurrence together,
// then calls the normal durable queue wake path. The optional MCP overlay is
// freshly resolved outside database transactions after an authority preflight;
// the write transaction repeats authorization and rejects a changed employee.
func (s *TaskService) DispatchDSHSchedule(ctx context.Context, key dshschedule.Key, invoke DSHNativeInvokeCheck) (dshschedule.Receipt, error) {
	var snapshot db.Agent
	err := s.withDSHScheduleAdmission(ctx, key, invoke, func(_ *db.Queries, _ pgx.Tx, agent db.Agent, _ pgtype.UUID, _ dshhost.SessionScope, _ dshschedule.State) error {
		snapshot = agent
		return nil
	})
	if err != nil {
		return dshschedule.Receipt{}, err
	}
	// Automatic attribution carries no human execution principal. The optional
	// overlay follows the existing agent-owned connection policy.
	overlay := s.buildRuntimeMCPOverlay(ctx, pgtype.UUID{}, snapshot)
	var task db.AgentTaskQueue
	var receipt dshschedule.Receipt
	err = s.withDSHScheduleAdmission(ctx, key, invoke, func(q *db.Queries, tx pgx.Tx, agent db.Agent, owner pgtype.UUID, scope dshhost.SessionScope, _ dshschedule.State) error {
		if agent.OwnerID != snapshot.OwnerID || agent.RuntimeID != snapshot.RuntimeID || !slices.Equal(agent.ComposioToolkitAllowlist, snapshot.ComposioToolkitAllowlist) || agent.UpdatedAt != snapshot.UpdatedAt {
			return dshhost.ErrChanged
		}
		var err error
		receipt, err = (dshschedule.Store{Tx: tx}).Dispatch(ctx, key, func(ctx context.Context, tx pgx.Tx, due dshschedule.Batch) (uuid.UUID, error) {
			attr := attribution.TriggerOwner(owner, attribution.EvidenceDSHSchedule, pgtype.UUID{Bytes: due.RequestID, Valid: true})
			source, _, kind, ref := attributionCreateParams(attr)
			metadata, _ := json.Marshal(map[string]string{"type": dshschedule.EvidenceKind, "workspace_id": key.WorkspaceID.String()})
			var err error
			if scope.Kind == "chat" {
				task, err = q.CreateChatTask(ctx, db.CreateChatTaskParams{AgentID: agent.ID, RuntimeID: agent.RuntimeID, Priority: 2, ChatSessionID: pgtype.UUID{Bytes: scope.ID, Valid: true},
					AccountableUserID: attr.AccountableUserID, OriginatorSource: source, TriggerEvidenceKind: kind, TriggerEvidenceRefID: ref,
					ForceFreshSession: pgtype.Bool{Valid: true}, TaskContext: metadata, RuntimeMcpOverlay: overlay.Overlay, RuntimeConnectedApps: overlay.ConnectedApps})
				if err != nil {
					return uuid.Nil, err
				}
				task, err = q.SetChatTaskInputOwnerSelf(ctx, task.ID)
				if err != nil {
					return uuid.Nil, err
				}
				payload, _ := json.Marshal(map[string]any{"dsh_schedule": map[string]string{"request_id": due.RequestID.String()}})
				if _, err = q.CreateChatMessage(ctx, db.CreateChatMessageParams{ChatSessionID: task.ChatSessionID, TaskID: task.ID, Role: "user", Content: due.Framing(), SourcePayload: payload, MessageKind: pgtype.Text{String: protocol.ChatMessageKindMessage, Valid: true}}); err != nil {
					return uuid.Nil, err
				}
				if err = q.TouchChatSession(ctx, task.ChatSessionID); err != nil {
					return uuid.Nil, err
				}
			} else {
				issueID := pgtype.UUID{}
				if scope.Kind == "issue" {
					issueID = pgtype.UUID{Bytes: scope.ID, Valid: true}
				}
				task, err = q.CreateAgentTask(ctx, db.CreateAgentTaskParams{AgentID: agent.ID, RuntimeID: agent.RuntimeID, IssueID: issueID, Priority: 2,
					AccountableUserID: attr.AccountableUserID, OriginatorSource: source, TriggerEvidenceKind: kind, TriggerEvidenceRefID: ref,
					ForceFreshSession: pgtype.Bool{Valid: true}, DispatchContext: metadata, RuntimeMcpOverlay: overlay.Overlay, RuntimeConnectedApps: overlay.ConnectedApps})
				if err != nil {
					return uuid.Nil, err
				}
			}
			_, err = (dshhost.PostgresStore{DB: tx}).AdoptNativeExecution(ctx, scope, uuid.UUID(task.ID.Bytes), key.SessionID, due.RequestID)
			return uuid.UUID(task.ID.Bytes), err
		})
		return err
	})
	if err != nil {
		return dshschedule.Receipt{}, err
	}
	slog.Info("DSH schedule task admitted", "workspace_id", key.WorkspaceID.String(), "agent_id", key.AgentID.String(), "session_id", key.SessionID, "seed_schedule_id", key.ScheduleID, "request_id", receipt.RequestID.String(), "task_id", receipt.TaskID.String(), "reminder_count", len(receipt.Reminders))
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
	return receipt, nil
}

// ScheduleExecutionMatches prevents a receipt from moving a platform task out
// of its existing domain scope. A standalone reminder can retain the original
// task's Session; only the committed ledger may establish that relationship.
func ScheduleExecutionMatches(task db.AgentTaskQueue, e dshschedule.Execution) bool {
	if task.OriginatorUserID.Valid || task.OriginatorSource.String != string(attribution.SourceTriggerOwner) || !task.ID.Valid || uuid.UUID(task.ID.Bytes) != e.TaskID || !task.AgentID.Valid || uuid.UUID(task.AgentID.Bytes) != e.SessionScope.AgentID || task.TriggerEvidenceKind.String != dshschedule.EvidenceKind || !task.TriggerEvidenceRefID.Valid || uuid.UUID(task.TriggerEvidenceRefID.Bytes) != e.RequestID || task.AutopilotRunID.Valid {
		return false
	}
	if task.IssueID.Valid {
		return e.Kind == "issue" && e.ID == uuid.UUID(task.IssueID.Bytes) && !task.ChatSessionID.Valid
	}
	if task.ChatSessionID.Valid {
		return e.Kind == "chat" && e.ID == uuid.UUID(task.ChatSessionID.Bytes) && task.ChatInputTaskID == task.ID
	}
	return e.Kind == "task"
}
