package handler

import (
	"encoding/json"
	"net/http"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// applyEmployeeRunClaim validates the durable queue-to-run mapping before the
// daemon receives a Direct prompt. Older daemons must never fall through to the
// ordinary issue prompt with an empty issue ID.
func (h *Handler) applyEmployeeRunClaim(r *http.Request, task db.AgentTaskQueue, runtime db.AgentRuntime, resp *AgentTaskResponse) *claimBuildFailure {
	if !service.IsEmployeeDirectTask(task) {
		return nil
	}
	if !h.canExecuteEmployeeDirectTask(r, task) {
		return &claimBuildFailure{outcome: "error_employee_direct_executor", status: http.StatusForbidden, message: "Caller cannot execute this Employee Direct task"}
	}
	invalid := func() *claimBuildFailure {
		return &claimBuildFailure{outcome: "error_employee_direct_binding", status: http.StatusConflict, message: "Employee Direct execution binding is invalid"}
	}
	c, ok := service.ParseDirectTaskContext(task)
	if !ok || h.DB == nil || resp == nil || task.RuntimeID != runtime.ID || c.WorkspaceID != uuidToString(runtime.WorkspaceID) {
		return invalid()
	}
	if !requestHasDaemonCapability(r, protocol.DaemonCapabilityEmployeeDirectV1) {
		return &claimBuildFailure{outcome: "error_employee_direct_capability", status: http.StatusServiceUnavailable, message: "Runtime must support Employee Direct tasks"}
	}
	var valid bool
	err := h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id AND t.workspace_id=r.workspace_id AND t.agent_id=r.agent_id AND t.tenant_org_id=r.tenant_org_id WHERE r.queue_task_id=$1 AND r.state='running' AND t.id=$2::uuid AND t.workspace_id=$3 AND t.agent_id=$4 AND t.dispatch_mode='direct' AND t.state='running' AND t.active_run_id=r.id)`, task.ID, c.EmployeeTaskID, runtime.WorkspaceID, task.AgentID).Scan(&valid)
	if err != nil {
		return &claimBuildFailure{outcome: "error_employee_direct_load", status: http.StatusServiceUnavailable, message: "Employee Direct execution is temporarily unavailable"}
	}
	if !valid {
		return invalid()
	}
	resp.DirectTaskPrompt = c.Prompt
	resp.WorkspaceID = c.WorkspaceID
	resp.ThreadName = task.TriggerSummary.String
	if len(resp.Repos) == 0 {
		if ws, err := h.Queries.GetWorkspace(r.Context(), runtime.WorkspaceID); err == nil && len(ws.Repos) > 0 {
			_ = json.Unmarshal(ws.Repos, &resp.Repos)
		}
	}
	return nil
}
