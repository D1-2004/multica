package dshhost

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
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

// BindExecution commits native identities before any external prompt admission.
// A lost response, server replica change, or rebuilt Host must reuse the same
// Session and request ID. Native DSH deduplicates prompts by that request ID.
func (s PostgresStore) BindExecution(ctx context.Context, scope SessionScope, taskID uuid.UUID) (Execution, error) {
	if scope.WorkspaceID == uuid.Nil || scope.AgentID == uuid.Nil || scope.ID == uuid.Nil || taskID == uuid.Nil {
		return Execution{}, errors.New("DSH session binding requires employee, scope and task identities")
	}
	switch scope.Kind {
	case "chat", "issue", "task":
	default:
		return Execution{}, errors.New("invalid DSH session scope")
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
	parsed, err := uuid.Parse(strings.TrimPrefix(sessionID, "session-"))
	if err != nil || parsed == uuid.Nil || sessionID != "session-"+parsed.String() {
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
