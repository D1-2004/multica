package handler

import (
	"context"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// employeeRoutineSendInput derives a sandbox receipt scope from the committed
// automation graph, never from receipt payload, prompt or claimed actor fields.
// Claim and callback admission share this exact identity/scene boundary.
func (h *Handler) employeeRoutineSendInput(ctx context.Context, task db.AgentTaskQueue) (dingtalkresponse.ActionInput, bool, error) {
	direct, ok := service.ParseDirectTaskContext(task)
	if !ok || direct.AutomationOrigin == nil || (direct.AutomationOrigin.Kind != service.AutomationOriginSceneRoutine && direct.AutomationOrigin.Kind != service.AutomationOriginSceneRoutineWebhook) {
		return dingtalkresponse.ActionInput{}, false, nil
	}
	origin, err := service.LoadAutomationOrigin(ctx, h.DB, task)
	if err != nil {
		return dingtalkresponse.ActionInput{}, true, err
	}
	run, err := h.Queries.GetAutopilotRun(ctx, task.AutopilotRunID)
	if err != nil {
		return dingtalkresponse.ActionInput{}, true, err
	}
	routine, err := contextcap.GetRoutineByAutopilot(ctx, h.DB, uuidToString(run.AutopilotID))
	if err != nil {
		return dingtalkresponse.ActionInput{}, true, err
	}
	scope := origin.Scope()
	if routine.WorkspaceID != scope.WorkspaceID || routine.AgentID != scope.AgentID || routine.SceneID != scope.Scene.SceneID || routine.TenantOrgID != scope.TenantOrgID {
		return dingtalkresponse.ActionInput{}, true, service.ErrAutomationOriginInvalid
	}
	in, err := h.routineNoticeInput(ctx, h.Queries, routine, "")
	if err != nil {
		return dingtalkresponse.ActionInput{}, true, err
	}
	in.TaskID = uuidToString(task.ID)
	return in, true, nil
}
