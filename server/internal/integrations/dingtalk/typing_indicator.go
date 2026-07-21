package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// typingIndicatorMaxAge is how old a callback can be before we skip the
// "processing" emotion. This prevents stale reactions when a Stream
// reconnect redelivers un-ACKed events. Mirrors the Lark manager's bound.
const typingIndicatorMaxAge = 2 * time.Minute

// typingQueries is the narrow DB surface Clear needs to recover the
// installation credentials for a chat session. *db.Queries satisfies it.
type typingQueries interface {
	GetChannelChatSessionBindingBySession(ctx context.Context, arg db.GetChannelChatSessionBindingBySessionParams) (db.ChannelChatSessionBinding, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
	AddChannelTypingIndicator(ctx context.Context, arg db.AddChannelTypingIndicatorParams) error
	TakeChannelTypingIndicatorsByTask(ctx context.Context, arg db.TakeChannelTypingIndicatorsByTaskParams) ([]db.ChannelTypingIndicator, error)
	TakeChannelTypingIndicators(ctx context.Context, arg db.TakeChannelTypingIndicatorsParams) ([]db.ChannelTypingIndicator, error)
}

// typingIndicatorTarget is the persisted shape of one pending emotion. It is
// stored as opaque JSON in channel_typing_indicator.target (migration 182) —
// the emotion must be recallable by whichever replica handles the run's
// completion, which is not the replica that ingested the message.
type typingIndicatorTarget struct {
	OpenConversationID string `json:"open_conversation_id"`
	OpenMsgID          string `json:"open_msg_id"`
	RobotCode          string `json:"robot_code"`
	TaskID             string `json:"task_id"`
}

// TypingIndicatorManager owns the "processing" emotion lifecycle for
// inbound DingTalk messages: an ingested message gets the 🤔思考中 text
// emotion; when the agent replies (or the run settles without a task)
// the emotion is recalled. Mirrors lark.TypingIndicatorManager, with the
// reaction addressed by (conversation, message) instead of a reaction id.
//
// Safe for concurrent use; missing or stale state is tolerated (clearing
// a session with no tracked emotion is a no-op).
type TypingIndicatorManager struct {
	messenger *RobotMessenger
	decrypt   Decrypter
	q         typingQueries
	log       *slog.Logger
}

// NewTypingIndicatorManager constructs the manager. messenger, decrypt
// and q must be non-nil.
func NewTypingIndicatorManager(messenger *RobotMessenger, decrypt Decrypter, q typingQueries, log *slog.Logger) *TypingIndicatorManager {
	if log == nil {
		log = slog.Default()
	}
	return &TypingIndicatorManager{
		messenger: messenger,
		decrypt:   decrypt,
		q:         q,
		log:       log,
	}
}

// Add attaches the "processing" emotion to the message and records the
// state under the chat session. Synchronous — the caller decides whether
// to detach. Errors are logged and swallowed (the indicator is cosmetic).
//
// createAtMs is the callback's epoch-millisecond send time; callbacks
// older than typingIndicatorMaxAge are skipped so redelivered events do
// not surface misleading "processing" badges.
func (m *TypingIndicatorManager) Add(ctx context.Context, inst db.ChannelInstallation, chatSessionID, taskID pgtype.UUID, target EmotionTarget, createAtMs int64) {
	if !taskID.Valid || target.OpenConversationID == "" || target.OpenMsgID == "" {
		return
	}
	if createAtMs > 0 && time.Since(time.UnixMilli(createAtMs)) > typingIndicatorMaxAge {
		m.log.Debug("dingtalk typing indicator: message too old, skipping",
			"chat_session_id", util.UUIDToString(chatSessionID), "open_msg_id", target.OpenMsgID)
		return
	}
	creds, err := decodeChannelCredentials(inst.Config, m.decrypt)
	if err != nil {
		m.log.Warn("dingtalk typing indicator: decode credentials failed",
			"chat_session_id", util.UUIDToString(chatSessionID), "err", err)
		return
	}
	robotCodeSource := "installation"
	if callbackRobotCode := strings.TrimSpace(target.RobotCode); callbackRobotCode != "" {
		creds.RobotCode = callbackRobotCode
		robotCodeSource = "stream_callback"
	}
	if strings.TrimSpace(creds.RobotCode) == "" {
		m.log.Warn("dingtalk typing indicator: robot code missing",
			"event", "dingtalk_typing_indicator_skipped",
			"reason", "robot_code_missing",
			"chat_session_id", util.UUIDToString(chatSessionID),
			"task_id", util.UUIDToString(taskID),
			"open_msg_id_hash", dingtalkTraceHash(target.OpenMsgID))
		return
	}
	target.RobotCode = creds.RobotCode
	if err := m.messenger.AddEmotionReply(ctx, creds, target); err != nil {
		m.log.Warn("dingtalk typing indicator: add emotion failed",
			"event", "dingtalk_typing_indicator_add_failed",
			"chat_session_id", util.UUIDToString(chatSessionID),
			"task_id", util.UUIDToString(taskID),
			"open_msg_id_hash", dingtalkTraceHash(target.OpenMsgID), "err", err)
		return
	}
	m.log.Info("dingtalk typing indicator added",
		"event", "dingtalk_typing_indicator_added",
		"chat_session_id", util.UUIDToString(chatSessionID),
		"task_id", util.UUIDToString(taskID),
		"open_msg_id_hash", dingtalkTraceHash(target.OpenMsgID),
		"robot_code_source", robotCodeSource)
	// Persist, do not remember: the replica that clears this emotion is the one
	// that serves the daemon's completion POST, which the load balancer picks
	// independently of the WS lease that pinned the ingest here.
	payload, err := json.Marshal(typingIndicatorTarget{
		OpenConversationID: target.OpenConversationID,
		OpenMsgID:          target.OpenMsgID,
		RobotCode:          target.RobotCode,
		TaskID:             util.UUIDToString(taskID),
	})
	if err != nil {
		m.log.Warn("dingtalk typing indicator: encode target failed",
			"chat_session_id", util.UUIDToString(chatSessionID), "err", err)
		return
	}
	if err := m.q.AddChannelTypingIndicator(ctx, db.AddChannelTypingIndicatorParams{
		ChatSessionID:  chatSessionID,
		ChannelType:    string(TypeDingtalk),
		InstallationID: inst.ID,
		Target:         payload,
	}); err != nil {
		// The emotion is already on the message; failing to record it only
		// means it will not be recalled. Cosmetic, so log and move on.
		m.log.Warn("dingtalk typing indicator: persist target failed",
			"chat_session_id", util.UUIDToString(chatSessionID),
			"open_msg_id", target.OpenMsgID, "err", err)
	}
}

// ClearTask recalls only the indicators owned by one completed task. Messages
// coalesced into the same durable debounce batch share a task id and therefore
// settle together; later queued turns in the same chat keep their indicators.
func (m *TypingIndicatorManager) ClearTask(ctx context.Context, chatSessionID, taskID pgtype.UUID) {
	if !taskID.Valid {
		return
	}
	key := util.UUIDToString(chatSessionID)
	rows, err := m.q.TakeChannelTypingIndicatorsByTask(ctx, db.TakeChannelTypingIndicatorsByTaskParams{
		ChatSessionID: chatSessionID,
		ChannelType:   string(TypeDingtalk),
		TaskID:        util.UUIDToString(taskID),
	})
	if err != nil {
		m.log.Warn("dingtalk typing indicator: take task targets failed",
			"chat_session_id", key, "task_id", util.UUIDToString(taskID), "err", err)
		return
	}
	m.clearRows(ctx, chatSessionID, rows)
}

// Clear recalls every tracked "processing" emotion for the chat session
// and drops the state. Synchronous so the emotion is gone before the
// agent's reply lands. Individual recall failures are logged, not fatal.
func (m *TypingIndicatorManager) Clear(ctx context.Context, chatSessionID pgtype.UUID) {
	key := util.UUIDToString(chatSessionID)
	// DELETE ... RETURNING claims the pending emotions: if two replicas race to
	// clear the same run, exactly one gets the rows and only it recalls.
	rows, err := m.q.TakeChannelTypingIndicators(ctx, db.TakeChannelTypingIndicatorsParams{
		ChatSessionID: chatSessionID,
		ChannelType:   string(TypeDingtalk),
	})
	if err != nil {
		m.log.Warn("dingtalk typing indicator: take pending targets failed",
			"chat_session_id", key, "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	m.clearRows(ctx, chatSessionID, rows)
}

func (m *TypingIndicatorManager) clearRows(ctx context.Context, chatSessionID pgtype.UUID, rows []db.ChannelTypingIndicator) {
	if len(rows) == 0 {
		return
	}
	key := util.UUIDToString(chatSessionID)

	binding, err := m.q.GetChannelChatSessionBindingBySession(ctx, db.GetChannelChatSessionBindingBySessionParams{
		ChatSessionID: chatSessionID,
		ChannelType:   string(TypeDingtalk),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			m.log.Warn("dingtalk typing indicator: binding lookup for clear failed",
				"chat_session_id", key, "err", err)
		}
		return
	}
	inst, err := m.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          binding.InstallationID,
		ChannelType: string(TypeDingtalk),
	})
	if err != nil {
		m.log.Warn("dingtalk typing indicator: installation lookup for clear failed",
			"chat_session_id", key, "err", err)
		return
	}
	creds, err := decodeChannelCredentials(inst.Config, m.decrypt)
	if err != nil {
		m.log.Warn("dingtalk typing indicator: decode credentials for clear failed",
			"chat_session_id", key, "err", err)
		return
	}
	for _, row := range rows {
		var t typingIndicatorTarget
		if err := json.Unmarshal(row.Target, &t); err != nil {
			m.log.Warn("dingtalk typing indicator: decode target failed",
				"chat_session_id", key, "err", err)
			continue
		}
		target := EmotionTarget{
			OpenConversationID: t.OpenConversationID,
			OpenMsgID:          t.OpenMsgID,
			RobotCode:          strings.TrimSpace(t.RobotCode),
		}
		rowCreds := creds
		if target.RobotCode != "" {
			rowCreds.RobotCode = target.RobotCode
		}
		if strings.TrimSpace(rowCreds.RobotCode) == "" {
			m.log.Warn("dingtalk typing indicator: robot code missing for recall",
				"event", "dingtalk_typing_indicator_recall_skipped",
				"reason", "robot_code_missing",
				"chat_session_id", key,
				"task_id", t.TaskID,
				"open_msg_id_hash", dingtalkTraceHash(target.OpenMsgID))
			continue
		}
		if err := m.messenger.RecallEmotionReply(ctx, rowCreds, target); err != nil {
			m.log.Warn("dingtalk typing indicator: recall emotion failed",
				"event", "dingtalk_typing_indicator_recall_failed",
				"chat_session_id", key,
				"task_id", t.TaskID,
				"open_msg_id_hash", dingtalkTraceHash(target.OpenMsgID), "err", err)
			continue
		}
		m.log.Info("dingtalk typing indicator recalled",
			"event", "dingtalk_typing_indicator_recalled",
			"chat_session_id", key,
			"task_id", t.TaskID,
			"open_msg_id_hash", dingtalkTraceHash(target.OpenMsgID))
	}
}
