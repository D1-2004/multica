package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/util"
)

// The routine end outbox remains the terminal ledger even when its business
// message was already delivered by a sandbox tool. Only a never-submitted
// notice reaches this guard; unknown provider submissions keep querying.
func (h *Handler) beforeEmployeeRoutineNoticeSend(ctx context.Context, in dingtalkresponse.ActionInput) error {
	if in.RequestID != dingtalkresponse.RoutineNoticeRequestID(in.RoutineRunID, dingtalkresponse.RoutineNoticeEnd) {
		return nil
	}
	runID, err := util.ParseUUID(in.RoutineRunID)
	if err != nil {
		return err
	}
	run, err := h.Queries.GetAutopilotRun(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return &dingtalkresponse.SuppressSendError{Reason: "routine_run_removed"}
	}
	if err != nil {
		return err
	}
	if !run.TaskID.Valid {
		return nil
	}
	task, err := h.Queries.GetAgentTask(ctx, run.TaskID)
	if err != nil {
		return err
	}
	direct, ok := service.ParseDirectTaskContext(task)
	if !ok || direct.AutomationOrigin == nil || (direct.AutomationOrigin.Kind != service.AutomationOriginSceneRoutine && direct.AutomationOrigin.Kind != service.AutomationOriginSceneRoutineWebhook) {
		return nil
	}
	// Failed and cancelled executions still need a terminal explanation, even
	// if an earlier partial action reached this conversation.
	if task.Status != "completed" {
		return nil
	}
	routine, err := contextcap.GetRoutineByAutopilot(ctx, h.DB, uuidToString(run.AutopilotID))
	if errors.Is(err, contextcap.ErrNotFound) {
		return &dingtalkresponse.SuppressSendError{Reason: "routine_removed"}
	}
	if err != nil {
		return err
	}
	target, err := h.routineNoticeInput(ctx, h.Queries, routine, "")
	if err != nil {
		return err
	}
	if in.WorkspaceID != direct.WorkspaceID || in.AgentID != uuidToString(task.AgentID) ||
		in.WorkspaceID != target.WorkspaceID || in.AgentID != target.AgentID || in.SceneID != target.SceneID ||
		in.DWSUID != target.DWSUID || in.DWSOrgID != target.DWSOrgID || in.ConversationID != target.ConversationID ||
		in.IsGroup != target.IsGroup || in.SenderOpenDingTalkID != target.SenderOpenDingTalkID {
		return &dingtalkresponse.SuppressSendError{Reason: "routine_delivery_binding_changed"}
	}
	receiptInput := target
	receiptInput.TaskID = uuidToString(task.ID)
	delivery, err := h.DingTalkResponses.SandboxDeliveryInTx(ctx, h.DB, receiptInput)
	if err != nil {
		return err
	}
	if delivery.HasPending {
		return errors.New("routine message delivery is still being verified")
	}
	// One delivered message cannot hide another failed or unconfirmed send.
	text := ""
	if delivery.HasFailure {
		text = "本次执行已结束，但有消息发送失败，请查看运行记录。"
	} else if delivery.HasUnknown {
		text = "本次执行已结束，但有消息是否送达尚未确认，请查看运行记录。"
	} else if len(delivery.MessageIDs) > 0 {
		return &dingtalkresponse.SuppressSendError{Reason: "routine_result_delivered_in_scene"}
	}
	if text == "" || text == in.Text {
		return nil
	}
	// A refresh must force the worker to reload before reserving submission.
	// Both the lease owner and any restart continue using the same outbox ID.
	tag, err := h.DB.Exec(ctx, `UPDATE response_action SET input=jsonb_set(input,'{text}',to_jsonb($2::text)),updated_at=now()
 WHERE id=$1 AND state='pending' AND provider_task_id='' AND input->>'text'=$3 AND input->>'routine_run_id'=$4`, in.ActionID, text, in.Text, in.RoutineRunID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("routine notice changed or was already submitted")
	}
	return errors.New("routine notice delivery status refreshed; reload before submission")
}
