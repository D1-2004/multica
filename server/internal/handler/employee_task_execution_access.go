package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// canExecuteEmployeeDirectRuntime derives execution authority from the
// authenticated credential and persisted runtime. Client capability headers,
// request daemon IDs and human management/read permissions grant no execution.
func (h *Handler) canExecuteEmployeeDirectRuntime(r *http.Request, runtime db.AgentRuntime) bool {
	if runtime.RuntimeMode != "local" && !service.IsFCE2BRuntime(runtime) {
		return false
	}
	workspaceID := uuidToString(runtime.WorkspaceID)
	if workspaceID == "" {
		return false
	}
	if middleware.DaemonAuthPathFromContext(r.Context()) == middleware.DaemonAuthPathDaemonToken {
		return middleware.DaemonWorkspaceIDFromContext(r.Context()) == workspaceID &&
			runtime.DaemonID.Valid && runtime.DaemonID.String != "" &&
			middleware.DaemonIDFromContext(r.Context()) == runtime.DaemonID.String
	}
	if middleware.DaemonAuthPathFromContext(r.Context()) != middleware.DaemonAuthPathPAT || !isEmployeeHumanCredential(r) ||
		!runtime.OwnerID.Valid || uuidToString(runtime.OwnerID) != requestUserID(r) {
		return false
	}
	_, err := h.getWorkspaceMember(r.Context(), requestUserID(r), workspaceID)
	return err == nil
}

func (h *Handler) canExecuteEmployeeDirectTask(r *http.Request, task db.AgentTaskQueue) bool {
	direct, ok := service.ParseDirectTaskContext(task)
	if !ok {
		return false
	}
	runtime, err := h.Queries.GetAgentRuntime(r.Context(), task.RuntimeID)
	if err != nil || uuidToString(runtime.WorkspaceID) != direct.WorkspaceID {
		return false
	}
	agent, err := h.Queries.GetAgent(r.Context(), task.AgentID)
	if err != nil || agent.WorkspaceID != runtime.WorkspaceID {
		return false
	}
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return r.Header.Get("X-Task-ID") == uuidToString(task.ID) &&
			r.Header.Get("X-Agent-ID") == uuidToString(task.AgentID) &&
			r.Header.Get("X-Workspace-ID") == direct.WorkspaceID
	}
	return h.canExecuteEmployeeDirectRuntime(r, runtime)
}

func (h *Handler) requireEmployeeDirectExecution(w http.ResponseWriter, r *http.Request, task db.AgentTaskQueue) bool {
	if !service.IsEmployeeDirectTask(task) || h.canExecuteEmployeeDirectTask(r, task) {
		return true
	}
	writeError(w, http.StatusForbidden, "you do not have execution access to this task")
	return false
}
