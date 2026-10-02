package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// canReadEmployeeDirectTask is an additional boundary only for Direct content.
// Agent visibility, workspace membership, endpoint operators and accountable
// attribution do not grant access to a requester's personal connector output.
// The caller retains the existing access policy for non-Direct task families.
func (h *Handler) canReadEmployeeDirectTask(r *http.Request, task db.AgentTaskQueue, agent db.Agent) bool {
	direct, ok := service.ParseDirectTaskContext(task)
	workspaceID := uuidToString(agent.WorkspaceID)
	if !ok || workspaceID == "" || direct.WorkspaceID != workspaceID || task.AgentID != agent.ID {
		return false
	}
	if scope := middleware.WorkspaceIDFromContext(r.Context()); scope != "" && scope != workspaceID {
		return false
	}
	// Only Auth's server-stamped task token identity can take the executor path.
	// A task token never inherits its issuing user's management privileges.
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return r.Header.Get("X-Task-ID") == uuidToString(task.ID) && r.Header.Get("X-Agent-ID") == uuidToString(task.AgentID) && r.Header.Get("X-Workspace-ID") == workspaceID
	}
	if !isEmployeeHumanCredential(r) {
		return false
	}
	member, err := h.getWorkspaceMember(r.Context(), requestUserID(r), workspaceID)
	if err != nil {
		return false
	}
	return memberManagesAgent(agent, member) || (task.OriginatorUserID.Valid && task.OriginatorUserID == member.UserID)
}

func (h *Handler) canReadEmployeeDirectTaskByID(r *http.Request, task db.AgentTaskQueue) bool {
	agent, err := h.Queries.GetAgent(r.Context(), task.AgentID)
	return err == nil && h.canReadEmployeeDirectTask(r, task, agent)
}

// canReadDaemonEmployeeTask preserves the trusted daemon credential's execution
// access while closing the daemon routes' ordinary PAT/JWT read bypass.
func (h *Handler) canReadDaemonEmployeeTask(r *http.Request, task db.AgentTaskQueue) bool {
	if !service.IsEmployeeDirectTask(task) {
		return true
	}
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return h.canReadEmployeeDirectTaskByID(r, task)
	}
	if h.canReadEmployeeDirectTaskByID(r, task) {
		return true
	}
	// Execution reads use the same persisted runtime/credential binding as
	// claim and completion; human management rights do not grant execution.
	return h.canExecuteEmployeeDirectTask(r, task)
}
func (h *Handler) requireDaemonEmployeeTaskRead(w http.ResponseWriter, r *http.Request, task db.AgentTaskQueue) bool {
	if h.canReadDaemonEmployeeTask(r, task) {
		return true
	}
	writeError(w, http.StatusForbidden, "you do not have access to this task")
	return false
}

// isEmployeeHumanCredential prevents machine credentials from inheriting the
// human-looking UserID that authentication uses for other workspace operations.
// Task and daemon tokens have their own independently scoped branches above.
func isEmployeeHumanCredential(r *http.Request) bool {
	switch middleware.DaemonAuthPathFromContext(r.Context()) {
	case "", middleware.DaemonAuthPathPAT, middleware.DaemonAuthPathJWT:
	default:
		return false
	}
	if r.Header.Get("X-Actor-Source") != "" {
		return false
	}
	if _, ok := middleware.WorkspaceAccessPrincipalFromContext(r.Context()); ok {
		return false
	}
	if _, ok := middleware.WorkspaceMCPPrincipalFromContext(r.Context()); ok {
		return false
	}
	return true
}
