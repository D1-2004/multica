package dingtalk

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	streamEmotionAPITimeout        = 3 * time.Second
	streamEmotionReconcileInterval = time.Second
	streamEmotionReconcileBatch    = 64
	streamEmotionRetryMax          = 30 * time.Second
)

// streamTypingQueries is deliberately optional on TypingIndicatorManager so
// channel-generic unit fakes do not need to know about the Stream-only durable
// lifecycle. Production *db.Queries implements the whole surface.
type streamTypingQueries interface {
	BeginDingTalkProcessingEmotion(context.Context, db.BeginDingTalkProcessingEmotionParams) (db.DingtalkProcessingEmotion, error)
	BindDingTalkProcessingEmotionTask(context.Context, db.BindDingTalkProcessingEmotionTaskParams) (db.DingtalkProcessingEmotion, error)
	MarkDingTalkProcessingEmotionAdded(context.Context, pgtype.UUID) (db.DingtalkProcessingEmotion, error)
	SettleDingTalkProcessingEmotionTask(context.Context, db.SettleDingTalkProcessingEmotionTaskParams) ([]db.DingtalkProcessingEmotion, error)
	SettleDingTalkProcessingEmotionSource(context.Context, db.SettleDingTalkProcessingEmotionSourceParams) ([]db.DingtalkProcessingEmotion, error)
	MarkOrphanedDingTalkProcessingEmotionsSettled(context.Context, int32) ([]db.DingtalkProcessingEmotion, error)
	ClaimDueDingTalkProcessingEmotions(context.Context, int32) ([]db.DingtalkProcessingEmotion, error)
	RetryDingTalkProcessingEmotion(context.Context, db.RetryDingTalkProcessingEmotionParams) error
	DeleteDingTalkProcessingEmotion(context.Context, pgtype.UUID) error
}

func (m *TypingIndicatorManager) streamStore() (streamTypingQueries, bool) {
	q, ok := m.q.(streamTypingQueries)
	return q, ok
}

// beginStreamEmotion durably records the exact callback robot/message target
// before making the DingTalk API call. The Stream inbox calls this before the
// channel router starts identity/runtime resolution, so users see the reaction
// at the beginning of processing rather than near task launch.
func (m *TypingIndicatorManager) beginStreamEmotion(
	ctx context.Context,
	installationID pgtype.UUID,
	sourceMessageID string,
	target EmotionTarget,
	createAtMs int64,
) {
	q, ok := m.streamStore()
	if !ok || !installationID.Valid || strings.TrimSpace(sourceMessageID) == "" ||
		strings.TrimSpace(target.OpenConversationID) == "" || strings.TrimSpace(target.OpenMsgID) == "" {
		return
	}
	if createAtMs > 0 && time.Since(time.UnixMilli(createAtMs)) > typingIndicatorMaxAge {
		return
	}
	inst, err := m.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID: installationID, ChannelType: string(TypeDingtalk),
	})
	if err != nil {
		m.log.Warn("dingtalk stream emotion: installation lookup failed",
			"event", "dingtalk_stream_emotion_begin_failed",
			"installation_id", util.UUIDToString(installationID), "error", err)
		return
	}
	creds, err := decodeChannelCredentials(inst.Config, m.decrypt)
	if err != nil {
		m.log.Warn("dingtalk stream emotion: credentials decode failed",
			"event", "dingtalk_stream_emotion_begin_failed",
			"installation_id", util.UUIDToString(installationID), "error", err)
		return
	}
	if callbackRobotCode := strings.TrimSpace(target.RobotCode); callbackRobotCode != "" {
		creds.RobotCode = callbackRobotCode
	}
	if strings.TrimSpace(creds.RobotCode) == "" {
		m.log.Warn("dingtalk stream emotion: robot code missing",
			"event", "dingtalk_stream_emotion_begin_failed",
			"reason", "robot_code_missing",
			"installation_id", util.UUIDToString(installationID),
			"open_msg_id_hash", dingtalkTraceHash(target.OpenMsgID))
		return
	}
	row, err := q.BeginDingTalkProcessingEmotion(ctx, db.BeginDingTalkProcessingEmotionParams{
		InstallationID:     installationID,
		SourceMessageID:    strings.TrimSpace(sourceMessageID),
		OpenConversationID: strings.TrimSpace(target.OpenConversationID),
		OpenMsgID:          strings.TrimSpace(target.OpenMsgID),
		RobotCode:          strings.TrimSpace(creds.RobotCode),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return // durable idempotency row already owns this callback
	}
	if err != nil {
		m.log.Warn("dingtalk stream emotion: durable begin failed",
			"event", "dingtalk_stream_emotion_begin_failed",
			"installation_id", util.UUIDToString(installationID),
			"open_msg_id_hash", dingtalkTraceHash(target.OpenMsgID), "error", err)
		return
	}
	m.addStreamEmotionRow(ctx, q, row, creds)
}

func (m *TypingIndicatorManager) addStreamEmotionRow(
	ctx context.Context,
	q streamTypingQueries,
	row db.DingtalkProcessingEmotion,
	creds channelCredentials,
) {
	creds.RobotCode = row.RobotCode
	target := EmotionTarget{OpenConversationID: row.OpenConversationID, OpenMsgID: row.OpenMsgID, RobotCode: row.RobotCode}
	apiCtx, cancel := context.WithTimeout(ctx, streamEmotionAPITimeout)
	err := m.messenger.AddEmotionReply(apiCtx, creds, target)
	cancel()
	if err != nil {
		m.log.Warn("dingtalk stream emotion: add failed",
			"event", "dingtalk_typing_indicator_add_failed",
			"installation_id", util.UUIDToString(row.InstallationID),
			"open_msg_id_hash", dingtalkTraceHash(row.OpenMsgID), "error", err)
		m.retryStreamEmotion(ctx, q, row, err)
		return
	}
	updated, err := q.MarkDingTalkProcessingEmotionAdded(ctx, row.ID)
	if err != nil {
		// The durable row still says "adding". Reconciliation deliberately
		// repeats the idempotent add, then observes any concurrent settlement.
		m.log.Warn("dingtalk stream emotion: mark added failed",
			"event", "dingtalk_stream_emotion_mark_added_failed",
			"installation_id", util.UUIDToString(row.InstallationID),
			"open_msg_id_hash", dingtalkTraceHash(row.OpenMsgID), "error", err)
		return
	}
	m.log.Info("dingtalk typing indicator added",
		"event", "dingtalk_typing_indicator_added",
		"installation_id", util.UUIDToString(row.InstallationID),
		"chat_session_id", util.UUIDToString(updated.ChatSessionID),
		"task_id", util.UUIDToString(updated.TaskID),
		"open_msg_id_hash", dingtalkTraceHash(row.OpenMsgID),
		"robot_code_source", "stream_callback")
	if updated.State == "settled" {
		m.recallStreamEmotionRow(ctx, q, updated)
	}
}

func (m *TypingIndicatorManager) bindStreamEmotion(
	ctx context.Context,
	installationID pgtype.UUID,
	sourceMessageID string,
	chatSessionID, taskID pgtype.UUID,
) {
	q, ok := m.streamStore()
	if !ok || !installationID.Valid || !chatSessionID.Valid || !taskID.Valid {
		return
	}
	row, err := q.BindDingTalkProcessingEmotionTask(ctx, db.BindDingTalkProcessingEmotionTaskParams{
		ChatSessionID: chatSessionID, TaskID: taskID,
		InstallationID: installationID, SourceMessageID: strings.TrimSpace(sourceMessageID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		m.log.Warn("dingtalk stream emotion: task bind failed",
			"event", "dingtalk_stream_emotion_bind_failed",
			"installation_id", util.UUIDToString(installationID),
			"chat_session_id", util.UUIDToString(chatSessionID),
			"task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	if row.State == "settled" && row.AddCompleted {
		m.recallStreamEmotionRow(ctx, q, row)
	}
}

func (m *TypingIndicatorManager) settleStreamSource(ctx context.Context, installationID pgtype.UUID, sourceMessageID string) {
	q, ok := m.streamStore()
	if !ok || !installationID.Valid || strings.TrimSpace(sourceMessageID) == "" {
		return
	}
	rows, err := q.SettleDingTalkProcessingEmotionSource(ctx, db.SettleDingTalkProcessingEmotionSourceParams{
		InstallationID: installationID, SourceMessageID: strings.TrimSpace(sourceMessageID),
	})
	if err != nil {
		m.log.Warn("dingtalk stream emotion: source settle failed",
			"event", "dingtalk_stream_emotion_settle_failed",
			"installation_id", util.UUIDToString(installationID), "error", err)
		return
	}
	for _, row := range rows {
		if row.AddCompleted {
			m.recallStreamEmotionRow(ctx, q, row)
		}
	}
}

func (m *TypingIndicatorManager) settleStreamTask(ctx context.Context, chatSessionID, taskID pgtype.UUID) {
	q, ok := m.streamStore()
	if !ok || !chatSessionID.Valid || !taskID.Valid {
		return
	}
	rows, err := q.SettleDingTalkProcessingEmotionTask(ctx, db.SettleDingTalkProcessingEmotionTaskParams{
		ChatSessionID: chatSessionID, TaskID: taskID,
	})
	if err != nil {
		m.log.Warn("dingtalk stream emotion: task settle failed",
			"event", "dingtalk_stream_emotion_settle_failed",
			"chat_session_id", util.UUIDToString(chatSessionID),
			"task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	for _, row := range rows {
		if row.AddCompleted {
			m.recallStreamEmotionRow(ctx, q, row)
		}
	}
}

func (m *TypingIndicatorManager) recallStreamEmotionRow(ctx context.Context, q streamTypingQueries, row db.DingtalkProcessingEmotion) {
	inst, err := m.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID: row.InstallationID, ChannelType: string(TypeDingtalk),
	})
	if err != nil {
		m.retryStreamEmotion(ctx, q, row, err)
		return
	}
	creds, err := decodeChannelCredentials(inst.Config, m.decrypt)
	if err != nil {
		m.retryStreamEmotion(ctx, q, row, err)
		return
	}
	creds.RobotCode = row.RobotCode
	target := EmotionTarget{OpenConversationID: row.OpenConversationID, OpenMsgID: row.OpenMsgID, RobotCode: row.RobotCode}
	apiCtx, cancel := context.WithTimeout(ctx, streamEmotionAPITimeout)
	err = m.messenger.RecallEmotionReply(apiCtx, creds, target)
	cancel()
	if err != nil {
		m.log.Warn("dingtalk stream emotion: recall failed",
			"event", "dingtalk_typing_indicator_recall_failed",
			"installation_id", util.UUIDToString(row.InstallationID),
			"chat_session_id", util.UUIDToString(row.ChatSessionID),
			"task_id", util.UUIDToString(row.TaskID),
			"open_msg_id_hash", dingtalkTraceHash(row.OpenMsgID), "error", err)
		m.retryStreamEmotion(ctx, q, row, err)
		return
	}
	if err := q.DeleteDingTalkProcessingEmotion(ctx, row.ID); err != nil {
		m.retryStreamEmotion(ctx, q, row, err)
		return
	}
	m.log.Info("dingtalk typing indicator recalled",
		"event", "dingtalk_typing_indicator_recalled",
		"installation_id", util.UUIDToString(row.InstallationID),
		"chat_session_id", util.UUIDToString(row.ChatSessionID),
		"task_id", util.UUIDToString(row.TaskID),
		"open_msg_id_hash", dingtalkTraceHash(row.OpenMsgID))
}

func (m *TypingIndicatorManager) retryStreamEmotion(ctx context.Context, q streamTypingQueries, row db.DingtalkProcessingEmotion, cause error) {
	delay := time.Second << min(int(row.AttemptCount), 5)
	if delay > streamEmotionRetryMax {
		delay = streamEmotionRetryMax
	}
	err := q.RetryDingTalkProcessingEmotion(ctx, db.RetryDingTalkProcessingEmotionParams{
		ID:            row.ID,
		NextAttemptAt: pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true},
	})
	if err != nil {
		m.log.Error("dingtalk stream emotion: retry scheduling failed",
			"event", "dingtalk_stream_emotion_retry_schedule_failed",
			"installation_id", util.UUIDToString(row.InstallationID),
			"open_msg_id_hash", dingtalkTraceHash(row.OpenMsgID),
			"cause", cause, "error", err)
	}
}

// runStreamEmotionReconciler retries durable add/recall work after transient
// DingTalk/API/DB failures and removes an unbound reaction left by a process
// crash during routing. The Stream inbox owns its lifecycle.
func (m *TypingIndicatorManager) runStreamEmotionReconciler(ctx context.Context) {
	q, ok := m.streamStore()
	if !ok {
		return
	}
	ticker := time.NewTicker(streamEmotionReconcileInterval)
	defer ticker.Stop()
	for {
		m.reconcileStreamEmotions(ctx, q)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *TypingIndicatorManager) reconcileStreamEmotions(ctx context.Context, q streamTypingQueries) {
	orphans, err := q.MarkOrphanedDingTalkProcessingEmotionsSettled(ctx, streamEmotionReconcileBatch)
	if err != nil && !errors.Is(err, context.Canceled) {
		m.log.Warn("dingtalk stream emotion: orphan reconciliation failed",
			"event", "dingtalk_stream_emotion_reconcile_failed", "error", err)
	}
	for _, row := range orphans {
		if row.AddCompleted {
			m.recallStreamEmotionRow(ctx, q, row)
		}
	}
	rows, err := q.ClaimDueDingTalkProcessingEmotions(ctx, streamEmotionReconcileBatch)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			m.log.Warn("dingtalk stream emotion: due reconciliation failed",
				"event", "dingtalk_stream_emotion_reconcile_failed", "error", err)
		}
		return
	}
	for _, row := range rows {
		switch row.State {
		case "adding":
			inst, loadErr := m.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
				ID: row.InstallationID, ChannelType: string(TypeDingtalk),
			})
			if loadErr != nil {
				m.retryStreamEmotion(ctx, q, row, loadErr)
				continue
			}
			creds, decodeErr := decodeChannelCredentials(inst.Config, m.decrypt)
			if decodeErr != nil {
				m.retryStreamEmotion(ctx, q, row, decodeErr)
				continue
			}
			m.addStreamEmotionRow(ctx, q, row, creds)
		case "settled":
			m.recallStreamEmotionRow(ctx, q, row)
		default:
			slog.Warn("dingtalk stream emotion: claimed unexpected state", "state", row.State)
		}
	}
}
