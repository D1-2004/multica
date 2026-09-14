package dshhost

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SessionScope struct {
	Key
	Kind string
	ID   uuid.UUID
}

type Execution struct {
	SessionID string
	RequestID uuid.UUID
	Workdir   string
}

// ValidSessionID accepts both the official browser's UUID and platform-created
// native identities. Preserve their spelling: changing it changes the log.
func ValidSessionID(value string) bool {
	raw := strings.TrimPrefix(value, "session-")
	id, err := uuid.Parse(raw)
	return err == nil && id != uuid.Nil && raw == id.String()
}

func validSessionScope(scope SessionScope) bool {
	return scope.WorkspaceID != uuid.Nil && scope.AgentID != uuid.Nil && scope.ID != uuid.Nil &&
		(scope.Kind == "chat" || scope.Kind == "issue" || scope.Kind == "task")
}

// AdoptNativeSession shares the transaction that creates the platform scope.
// It never moves an existing native Session to a different scope or employee.
func (s PostgresStore) AdoptNativeSession(ctx context.Context, scope SessionScope, sessionID string) error {
	if _, ok := s.DB.(pgx.Tx); !ok {
		return errors.New("native DSH admission requires a transaction")
	}
	if !validSessionScope(scope) || !ValidSessionID(sessionID) {
		return errors.New("invalid native DSH session identity")
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO dsh_employee_session
 (workspace_id,agent_id,scope_kind,scope_id,session_id) VALUES ($1,$2,$3,$4,$5)
 ON CONFLICT (workspace_id,agent_id,scope_kind,scope_id) DO NOTHING`,
		scope.WorkspaceID, scope.AgentID, scope.Kind, scope.ID, sessionID)
	if err != nil {
		return err
	}
	var current string
	if err := s.DB.QueryRow(ctx, `SELECT session_id FROM dsh_employee_session
 WHERE workspace_id=$1 AND agent_id=$2 AND scope_kind=$3 AND scope_id=$4`,
		scope.WorkspaceID, scope.AgentID, scope.Kind, scope.ID).Scan(&current); err != nil {
		return err
	}
	if current != sessionID {
		return ErrChanged
	}
	return nil
}

// AdoptNativeExecution must share the transaction that creates the platform
// task and its input. A scope, task or request already owned elsewhere is
// rejected; no conflict handler overwrites an existing binding.
func (s PostgresStore) AdoptNativeExecution(ctx context.Context, scope SessionScope, taskID uuid.UUID, sessionID string, requestID uuid.UUID) (Execution, error) {
	if taskID == uuid.Nil || requestID == uuid.Nil {
		return Execution{}, errors.New("invalid native DSH execution identity")
	}
	if err := s.AdoptNativeSession(ctx, scope, sessionID); err != nil {
		return Execution{}, err
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO dsh_task_binding
 (workspace_id,agent_id,task_id,session_id,request_id) VALUES ($1,$2,$3,$4,$5)
 ON CONFLICT (workspace_id,agent_id,task_id) DO NOTHING`,
		scope.WorkspaceID, scope.AgentID, taskID, sessionID, requestID)
	if err != nil {
		return Execution{}, err
	}
	var binding Execution
	if err := s.DB.QueryRow(ctx, `SELECT session_id,request_id FROM dsh_task_binding
 WHERE workspace_id=$1 AND agent_id=$2 AND task_id=$3`,
		scope.WorkspaceID, scope.AgentID, taskID).Scan(&binding.SessionID, &binding.RequestID); err != nil {
		return Execution{}, err
	}
	if binding.SessionID != sessionID || binding.RequestID != requestID {
		return Execution{}, ErrChanged
	}
	binding.Workdir = MountPath + "/workspaces/" + sessionID
	return binding, nil
}

// BindExecution commits native identities before any external prompt admission.
// A lost response, server replica change, or rebuilt Host must reuse the same
// Session and request ID. Native DSH deduplicates prompts by that request ID.
func (s PostgresStore) BindExecution(ctx context.Context, scope SessionScope, taskID uuid.UUID) (Execution, error) {
	if !validSessionScope(scope) || taskID == uuid.Nil {
		return Execution{}, errors.New("DSH session binding requires employee, scope and task identities")
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO dsh_employee_session
 (workspace_id,agent_id,scope_kind,scope_id,session_id) VALUES ($1,$2,$3,$4,$5)
 ON CONFLICT (workspace_id,agent_id,scope_kind,scope_id) DO NOTHING`,
		scope.WorkspaceID, scope.AgentID, scope.Kind, scope.ID, "session-"+uuid.NewString())
	if err != nil {
		return Execution{}, err
	}
	var sessionID string
	// A separate statement observes a concurrent winner after ON CONFLICT;
	// a CTE sharing the INSERT snapshot can miss that just-committed row.
	err = s.DB.QueryRow(ctx, `SELECT session_id FROM dsh_employee_session
 WHERE workspace_id=$1 AND agent_id=$2 AND scope_kind=$3 AND scope_id=$4`,
		scope.WorkspaceID, scope.AgentID, scope.Kind, scope.ID).Scan(&sessionID)
	if err != nil {
		return Execution{}, err
	}
	if !ValidSessionID(sessionID) {
		return Execution{}, errors.New("invalid persisted DSH session identity")
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO dsh_task_binding
 (workspace_id,agent_id,task_id,session_id,request_id) VALUES ($1,$2,$3,$4,$5)
 ON CONFLICT (workspace_id,agent_id,task_id) DO NOTHING`,
		scope.WorkspaceID, scope.AgentID, taskID, sessionID, uuid.New())
	if err != nil {
		return Execution{}, err
	}
	var binding Execution
	err = s.DB.QueryRow(ctx, `SELECT session_id,request_id FROM dsh_task_binding
 WHERE workspace_id=$1 AND agent_id=$2 AND task_id=$3`, scope.WorkspaceID, scope.AgentID, taskID).Scan(&binding.SessionID, &binding.RequestID)
	if err != nil {
		return Execution{}, err
	}
	if binding.SessionID != sessionID {
		return Execution{}, errors.New("DSH task cannot move to another native Session")
	}
	binding.Workdir = MountPath + "/workspaces/" + binding.SessionID
	return binding, nil
}
