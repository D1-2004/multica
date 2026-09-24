package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshschedule"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The internal scheduler uses the same current invocation policy as native
// entry. This is not an HTTP endpoint and accepts no caller credentials.
func (h *Handler) DispatchDSHSchedule(ctx context.Context, key dshschedule.Key) (dshschedule.Receipt, error) {
	if h == nil || h.TaskService == nil {
		return dshschedule.Receipt{}, service.ErrDSHAccessDenied
	}
	return h.TaskService.DispatchDSHSchedule(ctx, key, h.dshNativeInvoke)
}

func (h *Handler) applyDSHScheduleClaim(r *http.Request, task db.AgentTaskQueue, runtime db.AgentRuntime, backend service.SandboxBackendKind, workspace string, resp *AgentTaskResponse) *claimBuildFailure {
	if task.TriggerEvidenceKind.String != dshschedule.EvidenceKind {
		return nil
	}
	invalid := func() *claimBuildFailure {
		return &claimBuildFailure{outcome: "error_dsh_schedule_binding", status: http.StatusConflict, message: "DSH schedule task input or binding is invalid"}
	}
	if h.DB == nil || resp == nil || runtime.Provider != "dsh" || !service.IsFCE2BRuntime(runtime) || backend != service.SandboxBackendAliyunFC || !task.RuntimeID.Valid || task.RuntimeID != runtime.ID || !runtime.WorkspaceID.Valid || uuidToString(runtime.WorkspaceID) != workspace {
		return invalid()
	}
	if !requestHasDaemonCapability(r, protocol.DaemonCapabilityDSHNativePromptV1) {
		return &claimBuildFailure{outcome: "error_dsh_schedule_capability", status: http.StatusServiceUnavailable, message: "DSH Runtime must support native prompt transport for reminders"}
	}
	execution, err := dshschedule.LoadExecution(r.Context(), h.DB, dshhost.Key{WorkspaceID: uuid.UUID(runtime.WorkspaceID.Bytes), AgentID: uuid.UUID(task.AgentID.Bytes)}, uuid.UUID(task.ID.Bytes))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, dshschedule.ErrInvalid) {
		return &claimBuildFailure{outcome: "error_dsh_schedule_load", status: http.StatusServiceUnavailable, message: "DSH schedule execution is temporarily unavailable"}
	}
	if err != nil || !service.ScheduleExecutionMatches(task, execution) {
		return invalid()
	}
	prompt := execution.NativePrompt()
	if prompt.Validate() != nil {
		return invalid()
	}
	resp.DSHNativePrompt = prompt
	resp.WorkspaceID = workspace
	return nil
}
