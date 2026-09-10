package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func coordinatorWaitText(plan *inboundcoord.Decision, reason string) string {
	if plan == nil || plan.Action != inboundcoord.ActionIssue || len(plan.Items) == 0 {
		return ""
	}
	remaining := 0
	for _, item := range plan.Items {
		if !planItemCompleted(*plan, item.ActionKey) {
			remaining++
		}
	}
	if remaining == 0 {
		return ""
	}
	prefix := "这次请求已保存，但新的执行还没开始。"
	if remaining < len(plan.Items) {
		prefix = "剩余请求已保存，但剩余部分还没开始。"
	}
	if strings.Contains(reason, "two in-flight matters") {
		return prefix + "当前处理名额已满，空出名额后会继续。"
	}
	return prefix + "同一事项还有任务未结束，结束后会继续。"
}

// persistCoordinatorWait is a Host-only effect of a claimed, validated plan.
// Its metadata, local notice and optional message action commit together. The
// terminal coordinator message uses another message kind, so this notice cannot
// hide eventual completion or consume the original callback.
func (h *Handler) persistCoordinatorWait(ctx context.Context, job db.InboundCoordinatorJob, reason string) error {
	if h == nil || h.TxStarter == nil || h.Queries == nil {
		return nil
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT command FROM inbound_coordinator_job WHERE id=$1 AND workspace_id=$2 AND agent_id=$3 AND status='running' AND lease_token=$4 FOR UPDATE`, job.ID, job.WorkspaceID, job.AgentID, job.LeaseToken).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if len(envelope["_coordinator_wait"]) > 0 {
		return nil
	}
	plan, err := coordinatorJobCheckpoint(raw)
	if err != nil {
		return err
	}
	text := coordinatorWaitText(plan, reason)
	if text == "" {
		return nil
	}
	command, err := restoreJobCommand(db.InboundCoordinatorJob{Command: raw})
	if err != nil {
		return err
	}
	if command.ProactiveConversation || command.TaskFinishedTaskID != "" {
		return nil
	}
	qtx := h.Queries.WithTx(tx)
	metadata := map[string]any{"job_id": util.UUIDToString(job.ID), "status": "waiting", "reason": reason, "text": text, "recorded_at": time.Now().UTC()}
	payload, _ := json.Marshal(map[string]any{"coordinator_wait": metadata})
	message, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{ChatSessionID: job.ChatSessionID, Role: "assistant", Content: text, MessageKind: pgtype.Text{String: protocol.ChatMessageKindMessage, Valid: true}, SourcePayload: payload})
	if err != nil {
		return err
	}
	actionID := ""
	if managedDingTalkResponse(command) && h.DingTalkResponses != nil && command.CompletionCallback != nil {
		var routeRaw []byte
		lookupErr := tx.QueryRow(ctx, `SELECT input FROM response_route WHERE callback_url=$1 AND workspace_id=$2 AND agent_id=$3`, command.CompletionCallback.URL, job.WorkspaceID, job.AgentID).Scan(&routeRaw)
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return lookupErr
		}
		if lookupErr == nil {
			var in dingtalkresponse.ActionInput
			if err = json.Unmarshal(routeRaw, &in); err != nil {
				return err
			}
			if in.WorkspaceID == util.UUIDToString(job.WorkspaceID) && in.AgentID == util.UUIDToString(job.AgentID) &&
				in.ConversationID == dispatchConversationID(command) && in.CallbackTarget == strings.TrimSpace(h.TaskCompletionTargetIdentity) &&
				in.DWSUID != "" && in.DWSOrgID != "" && (in.IsGroup || in.SenderOpenDingTalkID != "") {
				in.Text = text
				if len(command.Event.Data.Messages) > 0 {
					in.ReplyToOpenMsgID = command.Event.Data.Messages[0].OpenMsgID
				}
				actionID, err = h.DingTalkResponses.EnqueueCoordinatorWait(ctx, tx, in, util.UUIDToString(job.ID))
				if err != nil {
					return err
				}
			}
		}
	}
	metadata["message_id"], metadata["response_action_id"] = util.UUIDToString(message.ID), actionID
	waitRaw, _ := json.Marshal(metadata)
	tag, err := tx.Exec(ctx, `UPDATE inbound_coordinator_job SET command=jsonb_set(command,'{_coordinator_wait}',$3::jsonb),updated_at=now() WHERE id=$1 AND lease_token=$2 AND status='running'`, job.ID, job.LeaseToken, waitRaw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("coordinator wait lease lost")
	}
	if err = qtx.TouchChatSession(ctx, job.ChatSessionID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if actionID != "" {
		h.DingTalkResponses.Notify()
	}
	h.publishChatToUser(protocol.EventChatMessage, util.UUIDToString(job.WorkspaceID), util.UUIDToString(job.UserID), "agent", util.UUIDToString(job.AgentID), util.UUIDToString(job.ChatSessionID), protocol.ChatMessagePayload{ChatSessionID: util.UUIDToString(job.ChatSessionID), MessageID: util.UUIDToString(message.ID), Role: "assistant", Content: text, CreatedAt: timestampToString(message.CreatedAt), MessageKind: protocol.ChatMessageKindMessage})
	return nil
}
