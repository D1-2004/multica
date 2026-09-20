package dshhost

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type SessionScope struct {
	Key
	Kind  string
	ID    uuid.UUID
	Epoch uuid.UUID
}

type Execution struct {
	SessionID string
	RequestID uuid.UUID
	Workdir   string
}

// BindSandboxScope preserves the execution location chosen when a session was
// first admitted. A native browser prompt must run on the host serving that
// browser; later platform turns and scheduled turns reuse the same scope.
func (s PostgresStore) BindSandboxScope(ctx context.Context, scope SessionScope, preferred uuid.UUID) (uuid.UUID, error) {
	if !validSessionScope(scope) {
		return uuid.Nil, errors.New("invalid DSH session sandbox scope")
	}
	var selected uuid.UUID
	err := s.DB.QueryRow(ctx, `UPDATE dsh_employee_session SET sandbox_scope_id=COALESCE(sandbox_scope_id,$5)
 WHERE workspace_id=$1 AND agent_id=$2 AND scope_kind=$3 AND scope_id=$4 AND epoch_id=$6 RETURNING sandbox_scope_id`,
		scope.WorkspaceID, scope.AgentID, scope.Kind, scope.ID, preferred, scope.Epoch).Scan(&selected)
	return selected, err
}

// ValidSessionID accepts both the official browser's UUID and platform-created
// native identities. Preserve their spelling: changing it changes the log.
func ValidSessionID(value string) bool {
	return protocol.ValidDSHSessionID(value)
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
 (workspace_id,agent_id,scope_kind,scope_id,session_id,epoch_id) VALUES ($1,$2,$3,$4,$5,$6)
 ON CONFLICT (workspace_id,agent_id,scope_kind,scope_id,epoch_id) DO NOTHING`,
		scope.WorkspaceID, scope.AgentID, scope.Kind, scope.ID, sessionID, scope.Epoch)
	if err != nil {
		return err
	}
	var current string
	if err := s.DB.QueryRow(ctx, `SELECT session_id FROM dsh_employee_session
 WHERE workspace_id=$1 AND agent_id=$2 AND scope_kind=$3 AND scope_id=$4 AND epoch_id=$5`,
		scope.WorkspaceID, scope.AgentID, scope.Kind, scope.ID, scope.Epoch).Scan(&current); err != nil {
		return err
	}
	if current != sessionID {
		return ErrChanged
	}
	return nil
}

// BindWorkdir pins the native directory during session registration. Existing
// sessions cannot move, including sessions created before the workdir column.
func (s PostgresStore) BindWorkdir(ctx context.Context, scope SessionScope, workdir string, created bool) error {
	if !validSessionScope(scope) || !protocol.ValidDSHWorkdir(workdir) {
		return ErrChanged
	}
	if created {
		_, err := s.DB.Exec(ctx, `UPDATE dsh_employee_session SET workdir=$5 WHERE workspace_id=$1 AND agent_id=$2 AND scope_kind=$3 AND scope_id=$4 AND epoch_id=$6 AND workdir IS NULL`, scope.WorkspaceID, scope.AgentID, scope.Kind, scope.ID, workdir, scope.Epoch)
		if err != nil {
			return err
		}
	}
	current, err := s.SessionWorkdir(ctx, scope)
	if err != nil {
		return err
	}
	if current != workdir {
		return ErrChanged
	}
	return nil
}

func (s PostgresStore) SessionWorkdir(ctx context.Context, scope SessionScope) (string, error) {
	var workdir string
	err := s.DB.QueryRow(ctx, `SELECT COALESCE(workdir,'/mnt/multica/workspaces/' || session_id) FROM dsh_employee_session WHERE workspace_id=$1 AND agent_id=$2 AND scope_kind=$3 AND scope_id=$4 AND epoch_id=$5`, scope.WorkspaceID, scope.AgentID, scope.Kind, scope.ID, scope.Epoch).Scan(&workdir)
	if err == nil && !protocol.ValidDSHWorkdir(workdir) {
		err = ErrChanged
	}
	return workdir, err
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
	binding.Workdir, err = s.SessionWorkdir(ctx, scope)
	return binding, err
}

// BindExecution commits native identities before any external prompt admission.
// A lost response, server replica change, or rebuilt Host must reuse the same
// Session and request ID. Native DSH deduplicates prompts by that request ID.
func (s PostgresStore) BindExecution(ctx context.Context, scope SessionScope, taskID uuid.UUID) (Execution, error) {
	if !validSessionScope(scope) || taskID == uuid.Nil {
		return Execution{}, errors.New("DSH session binding requires employee, scope and task identities")
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO dsh_employee_session
 (workspace_id,agent_id,scope_kind,scope_id,session_id,epoch_id) VALUES ($1,$2,$3,$4,$5,$6)
 ON CONFLICT (workspace_id,agent_id,scope_kind,scope_id,epoch_id) DO NOTHING`,
		scope.WorkspaceID, scope.AgentID, scope.Kind, scope.ID, "session-"+uuid.NewString(), scope.Epoch)
	if err != nil {
		return Execution{}, err
	}
	var sessionID string
	// A separate statement observes a concurrent winner after ON CONFLICT;
	// a CTE sharing the INSERT snapshot can miss that just-committed row.
	err = s.DB.QueryRow(ctx, `SELECT session_id FROM dsh_employee_session
 WHERE workspace_id=$1 AND agent_id=$2 AND scope_kind=$3 AND scope_id=$4 AND epoch_id=$5`,
		scope.WorkspaceID, scope.AgentID, scope.Kind, scope.ID, scope.Epoch).Scan(&sessionID)
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
	binding.Workdir, err = s.SessionWorkdir(ctx, scope)
	return binding, err
}
