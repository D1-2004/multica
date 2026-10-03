package handler

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ResolveEmployeeWatchdogTarget implements service.EmployeeWatchdogTargetResolver.
// The anchor is the accepted source of the execution an episode belongs to:
// the same conversation, requester, DWS identity and tenant fences the Run's
// final notice uses (employeeNoticeTarget). It never uses the current mode or
// a guessed DM recipient. The result is a scene notice: it closes no
// dispatch, quotes no message and carries no Task, so it is not a Run reply.
func (h *Handler) ResolveEmployeeWatchdogTarget(ctx context.Context, tx pgx.Tx, ref service.EmployeeWatchdogNoticeRef) (dingtalkresponse.ActionInput, error) {
	if h == nil || tx == nil {
		return dingtalkresponse.ActionInput{}, errors.New("employee watchdog target services are unavailable")
	}
	if ref.RunID == "" || ref.QueueTaskID == "" {
		return dingtalkresponse.ActionInput{}, &service.EmployeeWatchdogHold{Reason: "delivery_anchor_unavailable"}
	}
	b, err := h.loadEmployeeWatchdogBinding(ctx, tx, ref)
	if err == nil {
		var target dingtalkresponse.ActionInput
		target, _, err = h.employeeNoticeTarget(ctx, tx, b)
		if err == nil {
			return dingtalkresponse.ActionInput{
				WorkspaceID: target.WorkspaceID, AgentID: target.AgentID, DWSUID: target.DWSUID, DWSOrgID: target.DWSOrgID,
				SceneID: target.SceneID, ConversationID: target.ConversationID, SenderOpenDingTalkID: target.SenderOpenDingTalkID,
				IsGroup: target.IsGroup, ShowAITag: target.ShowAITag, DWSEnvironment: target.DWSEnvironment,
			}, nil
		}
	}
	var held *employeeNoticeHold
	if errors.As(err, &held) {
		return dingtalkresponse.ActionInput{}, &service.EmployeeWatchdogHold{Reason: held.reason}
	}
	// errEmployeeNoticeSourcePending and database errors are retried later.
	return dingtalkresponse.ActionInput{}, err
}

// loadEmployeeWatchdogBinding loads the exact Task/Run/queue binding for an
// active or stopping execution. Unlike the final notice it accepts a Run that
// has not finished, and a stop request does not revoke the anchor: the stop
// notice is addressed to the same requester.
func (h *Handler) loadEmployeeWatchdogBinding(ctx context.Context, tx pgx.Tx, ref service.EmployeeWatchdogNoticeRef) (employeeNoticeBinding, error) {
	var b employeeNoticeBinding
	err := tx.QueryRow(ctx, `SELECT t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scene_id::text,t.id::text,t.requester_ref,r.id::text,r.queue_task_id::text,t.goal_revision,r.goal_revision
 FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id AND r.workspace_id=t.workspace_id AND r.agent_id=t.agent_id AND r.tenant_org_id=t.tenant_org_id
 WHERE t.workspace_id=$1::uuid AND t.agent_id=$2::uuid AND t.tenant_org_id=$3 AND t.scene_id=$4::uuid AND t.id=$5::uuid AND r.id=$6::uuid AND r.queue_task_id=$7::uuid
 AND t.owner_loop='employee' AND t.dispatch_mode='direct' AND t.scope_kind='scene'`,
		ref.Scope.WorkspaceID, ref.Scope.AgentID, ref.Scope.TenantOrgID, ref.Scope.Scene.SceneID, ref.TaskID, ref.RunID, ref.QueueTaskID).
		Scan(&b.Scope.WorkspaceID, &b.Scope.AgentID, &b.Scope.TenantOrgID, &b.Scope.Scene.SceneID, &b.TaskID, &b.Requester, &b.RunID, &b.QueueID, &b.TaskGoalRevision, &b.RunGoalRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, holdEmployeeNotice("run_binding_removed")
	}
	if err != nil {
		return b, err
	}
	b.Scope.Kind = employeetask.ScopeScene
	// The watchdog never applies a completion exemption: the anchor proof
	// below verifies the accepted delivery source without the quiet policy.
	b.ResultState = "watchdog"
	b.CompletionNotice = employeetask.CompletionNoticePolicy{Mode: employeetask.CompletionNoticeAlways}
	queueID, err := util.ParseUUID(b.QueueID)
	if err != nil {
		return b, holdEmployeeNotice("run_queue_binding_mismatch")
	}
	b.Queue, err = db.New(tx).GetAgentTask(ctx, queueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, holdEmployeeNotice("run_queue_binding_mismatch")
	}
	if err != nil {
		return b, err
	}
	var metadata struct {
		JobID     string    `json:"employee_job_id"`
		SourceRef string    `json:"employee_source_ref"`
		Owner     string    `json:"employee_delivery_owner"`
		Scene     scene.Ref `json:"agent_scene"`
	}
	if json.Unmarshal(b.Queue.Context, &metadata) != nil {
		return b, holdEmployeeNotice("invalid_queue_context")
	}
	b.JobID, b.SourceRef = metadata.JobID, metadata.SourceRef
	if _, err := util.ParseUUID(b.JobID); err != nil {
		b.JobID = ""
		return b, holdEmployeeNotice("invalid_job_binding")
	}
	c, ok := service.ParseDirectTaskContext(b.Queue)
	if !ok || metadata.Owner != "employee" || c.EmployeeTaskID != b.TaskID || c.WorkspaceID != b.Scope.WorkspaceID || metadata.Scene.SceneID != b.Scope.Scene.SceneID || uuidToString(b.Queue.AgentID) != b.Scope.AgentID {
		return b, holdEmployeeNotice("run_queue_binding_mismatch")
	}
	return b, nil
}

// BeforeEmployeeResponseSend chains the watchdog fence before the Run notice
// fence. Watchdog notices are scene notices identified by their intent row;
// every other action falls through unchanged.
func (h *Handler) BeforeEmployeeResponseSend(watchdog *service.EmployeeWatchdog) func(context.Context, dingtalkresponse.ActionInput) error {
	return func(ctx context.Context, in dingtalkresponse.ActionInput) error {
		if handled, err := watchdog.BeforeSend(ctx, in); handled {
			return err
		}
		return h.BeforeEmployeeRunNoticeSend(ctx, in)
	}
}
