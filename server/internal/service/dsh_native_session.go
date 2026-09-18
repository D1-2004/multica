package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type DSHNativeChatSession struct {
	Session db.ChatSession
	Created bool
}

// RegisterDSHNativeChatSession creates a human-owned platform session and its
// native mapping together. Identical native identities resolve the same row
// across replicas or lost responses. No task or user message is fabricated.
func (s *TaskService) RegisterDSHNativeChatSession(ctx context.Context, agent db.Agent, access dshhost.NativeAccess, sessionID, workdir string, invoke DSHNativeInvokeCheck) (DSHNativeChatSession, error) {
	var out DSHNativeChatSession
	if s == nil || s.Queries == nil || s.TxStarter == nil {
		return out, errors.New("DSH native session registration requires a transaction")
	}
	a := dshNativeChatAdmission{access: access, invoke: invoke, runtimeID: agent.RuntimeID}
	userID := pgtype.UUID{Bytes: access.UserID, Valid: true}
	if err := a.validateEmployee(agent, userID); err != nil {
		return out, err
	}
	if err := validateDSHNativeSession(sessionID, workdir); err != nil {
		return out, err
	}
	if err := invoke(ctx, s.Queries, agent, userID); err != nil {
		return out, dshhost.ErrNativeAccessDenied
	}
	err := s.runInTxWithHandle(ctx, func(q *db.Queries, tx pgx.Tx) error {
		if err := a.lockAdmission(ctx, tx); err != nil {
			return err
		}
		// Match ordinary chat creation's workspace deletion fence. An existing
		// session is locked before its employee, matching send/archive/rebind.
		if _, err := q.LockWorkspaceForChatSessionCreate(ctx, agent.WorkspaceID); err != nil {
			return err
		}
		var kind string
		var scopeID pgtype.UUID
		var epoch uuid.UUID
		err := tx.QueryRow(ctx, `SELECT scope_kind,scope_id,epoch_id FROM dsh_employee_session
 WHERE workspace_id=$1 AND agent_id=$2 AND session_id=$3`, access.WorkspaceID, access.AgentID, sessionID).Scan(&kind, &scopeID, &epoch)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if exists {
			if kind != "chat" {
				return dshhost.ErrChanged
			}
			if _, err := q.LockChatSessionForRuntimeBind(ctx, scopeID); err != nil {
				return err
			}
			out.Session, err = q.GetChatSession(ctx, scopeID)
			if err != nil {
				return err
			}
		}
		current, err := q.GetAgentForClaimUpdate(ctx, agent.ID)
		if err != nil {
			return err
		}
		if err := a.checkGrantLocked(ctx, tx, q, current, userID); err != nil {
			return err
		}
		if exists {
			if err := a.validateIdentity(out.Session, current, userID); err != nil {
				return err
			}
			if out.Session.Status != "active" {
				return ErrChatSessionArchived
			}
			return (dshhost.PostgresStore{DB: tx}).BindWorkdir(ctx, dshhost.SessionScope{Key: access.Key, Kind: "chat", ID: uuid.UUID(out.Session.ID.Bytes), Epoch: epoch}, workdir, false)
		}
		out.Session, err = q.CreateChatSession(ctx, db.CreateChatSessionParams{
			WorkspaceID: current.WorkspaceID, AgentID: current.ID, CreatorID: userID,
		})
		if err != nil {
			return err
		}
		if err := (dshhost.PostgresStore{DB: tx}).AdoptNativeSession(ctx,
			dshhost.SessionScope{Key: access.Key, Kind: "chat", ID: uuid.UUID(out.Session.ID.Bytes)}, sessionID); err != nil {
			return err
		}
		if err := (dshhost.PostgresStore{DB: tx}).BindWorkdir(ctx, dshhost.SessionScope{Key: access.Key, Kind: "chat", ID: uuid.UUID(out.Session.ID.Bytes)}, workdir, true); err != nil {
			return err
		}
		out.Created = true
		return nil
	})
	if err != nil {
		return DSHNativeChatSession{}, err
	}
	return out, nil
}
