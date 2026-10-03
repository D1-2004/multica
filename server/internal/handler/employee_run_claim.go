package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
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
	resp.DirectTaskPrompt = employeeDirectPrompt(c.Prompt)
	if c.AutomationOrigin != nil {
		// A routine-origin execution runs only the frozen packet of its
		// verified receipt. The current autopilot instructions never apply.
		origin, err := service.LoadAutomationOrigin(r.Context(), h.DB, task)
		if errors.Is(err, service.ErrAutomationOriginInvalid) {
			return invalid()
		}
		if err != nil {
			return &claimBuildFailure{outcome: "error_employee_direct_load", status: http.StatusServiceUnavailable, message: "Employee Direct execution is temporarily unavailable"}
		}
		resp.DirectTaskPrompt = employeeAutomationPrompt(c.Prompt, origin)
	}
	h.applyEmployeeSteerResume(r, task, c, resp)
	resp.WorkspaceID = c.WorkspaceID
	resp.ThreadName = task.TriggerSummary.String
	if len(resp.Repos) == 0 {
		if ws, err := h.Queries.GetWorkspace(r.Context(), runtime.WorkspaceID); err == nil && len(ws.Repos) > 0 {
			_ = json.Unmarshal(ws.Repos, &resp.Repos)
		}
	}
	return nil
}

// The server appends the delivery owner after the frozen work packet. This is
// claim-time execution guidance; it never changes persisted input or replay keys.
const employeeDirectOutputInstruction = `## Output

Your final assistant text is the user-facing reply. The Host sends it to the originating conversation for both success and failure, applying the requester's explicit file-only/no-summary policy after verifying delivery.
Do not call dws-rpc final or reply, or send another DWS message to post this same completion/error text before returning it. A tool send followed by final assistant text would produce two replies.
Continue to deliver explicitly requested files and proactive messages to their requested destinations; this ownership rule does not prohibit those actions.
Keep it concise: state the requested result or actionable failure. Do not list internal tools, commands, local paths, receipt IDs or debugging steps unless the requester explicitly asks for them.`

func employeeDirectPrompt(compiled string) string {
	return compiled + "\n\n" + employeeDirectOutputInstruction
}

// employeeRoutineOutputInstruction is the claim-time delivery guidance of a
// scene routine occurrence: the routine's own end notice is the only sender.
const employeeRoutineOutputInstruction = `## Output

This is one run of a scene routine. Your final assistant text is this run's result: the Host posts a start notice and an end notice into the routine's scene and attaches your final output to the end notice, for both success and failure.
Do not call dws-rpc final or reply, and do not send the result to the routine's scene yourself; that would post it twice.
Deliver files or messages to other destinations only when the routine's instructions explicitly ask for them.
Keep it concise: state the result or an actionable failure. Do not list internal tools, commands, local paths, receipt IDs or debugging steps.`

func employeeAutomationPrompt(compiled string, origin service.AutomationOrigin) string {
	switch origin.Kind() {
	case service.AutomationOriginSceneRoutine, service.AutomationOriginSceneRoutineWebhook:
		return compiled + "\n\n" + employeeRoutineOutputInstruction
	default:
		return employeeDirectPrompt(compiled)
	}
}

// employeeSteerResumeHops bounds the predecessor walk for chained steers.
const employeeSteerResumeHops = 5

// applyEmployeeSteerResume offers a steer successor the provider session and
// workdir of the execution it replaced. The claim barrier guarantees that the
// predecessor's process has exited, so its pinned session is complete. Only
// the same EmployeeTask and runtime qualify; the daemon's workdir and context
// compatibility gates still decide whether the session is actually resumed.
func (h *Handler) applyEmployeeSteerResume(r *http.Request, task db.AgentTaskQueue, c service.DirectTaskContext, resp *AgentTaskResponse) {
	current := task
	for range employeeSteerResumeHops {
		var private struct {
			Predecessor string `json:"steer_predecessor_task_id"`
		}
		if json.Unmarshal(current.Context, &private) != nil || private.Predecessor == "" {
			return
		}
		id, err := util.ParseUUID(private.Predecessor)
		if err != nil {
			return
		}
		prior, err := h.Queries.GetAgentTask(r.Context(), id)
		if err != nil || prior.AgentID != task.AgentID {
			return
		}
		pc, ok := service.ParseDirectTaskContext(prior)
		if !ok || pc.EmployeeTaskID != c.EmployeeTaskID || pc.WorkspaceID != c.WorkspaceID {
			return
		}
		if prior.SessionID.Valid && prior.SessionID.String != "" {
			if prior.RuntimeID == task.RuntimeID && !service.ResumeUnsafeFailure(prior.FailureReason.String, prior.Error.String) {
				resp.PriorSessionID = prior.SessionID.String
			}
			if prior.WorkDir.Valid {
				resp.PriorWorkDir = prior.WorkDir.String
			}
			return
		}
		// Only a predecessor that never started defers to the execution it
		// replaced. A started run without a session withheld or retired it, so
		// an older session is not offered.
		if prior.StartedAt.Valid {
			return
		}
		current = prior
	}
}
