package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// outboundQueries is the slice of generated queries the DingTalk outbound
// subscriber needs. *db.Queries satisfies it.
type outboundQueries interface {
	GetChannelChatSessionBindingBySession(ctx context.Context, arg db.GetChannelChatSessionBindingBySessionParams) (db.ChannelChatSessionBinding, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
	ListPendingChatMessagePreviewsAfterTask(ctx context.Context, taskID pgtype.UUID) ([]db.ListPendingChatMessagePreviewsAfterTaskRow, error)
}

// taskFailedText is the user-visible notice for a failed chat run. The
// error detail stays in Multica (the failure payload carries no message);
// the point is that the wait visibly ended in failure, not silence.
const taskFailedText = "⚠️ 本次处理失败了，请稍后重试；详情可在 Multica 中查看。"

// Outbound delivers an agent's chat reply back to DingTalk — the outbound
// half of the round trip. It mirrors slack.Outbound: on EventChatDone it
// finds the DingTalk chat binding for the finished task's session and posts
// the reply via the robot message API (group: openConversationId; DM: the
// staff id captured on the binding config at session creation). Sessions
// with no DingTalk binding are ignored, so it coexists with the Feishu
// Patcher and the Slack Outbound on the shared event bus.
type Outbound struct {
	q         outboundQueries
	decrypt   Decrypter
	messenger *RobotMessenger
	typing    *TypingIndicatorManager
	logger    *slog.Logger
}

// NewOutbound builds the DingTalk outbound subscriber. typing is the
// "processing" emotion manager to clear before the reply lands; nil
// disables the clear.
func NewOutbound(q outboundQueries, decrypt Decrypter, messenger *RobotMessenger, typing *TypingIndicatorManager, logger *slog.Logger) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	return &Outbound{q: q, decrypt: decrypt, messenger: messenger, typing: typing, logger: logger}
}

// Register subscribes to the chat-done and task-failed events on the bus:
// chat-done delivers the reply, task-failed only clears the "processing"
// emotion (there is no failure reply on this channel).
func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, o.handleEvent)
	bus.Subscribe(protocol.EventTaskFailed, o.handleEvent)
}

func (o *Outbound) handleEvent(e events.Event) {
	// Bus delivery is synchronous, so a stuck DingTalk HTTP call must not
	// wedge the publish call site: use a fresh ctx with a tight timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.processEvent(ctx, e); err != nil {
		if trace, present, traceErr := chatTraceFromEvent(e); traceErr != nil {
			o.logger.Error("dingtalk outbound event has invalid chat trace", "task_id", util.UUIDToString(taskIDFromEvent(e)), "error", traceErr)
		} else if present {
			chattrace.LogStage(o.logger, trace, "dingtalk_api", "failed",
				"task_id", util.UUIDToString(taskIDFromEvent(e)),
				"chat_session_id", util.UUIDToString(sessionIDFromEvent(e)),
				"delivery_boundary", "api_response",
				"error", err,
			)
		}
		o.logger.WarnContext(ctx, "dingtalk outbound: reply delivery failed",
			"error", err, "chat_session_id", e.ChatSessionID)
	}
}

func (o *Outbound) processEvent(ctx context.Context, e events.Event) error {
	sessionID := sessionIDFromEvent(e)
	if !sessionID.Valid {
		// Issue / autopilot tasks carry no chat_session.
		return nil
	}
	taskID := taskIDFromEvent(e)
	if !taskID.Valid {
		return fmt.Errorf("dingtalk outbound event has no task id")
	}
	binding, err := o.q.GetChannelChatSessionBindingBySession(ctx, db.GetChannelChatSessionBindingBySessionParams{
		ChatSessionID: sessionID,
		ChannelType:   string(TypeDingtalk),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // not a DingTalk session (Feishu / Slack / web-only)
		}
		return fmt.Errorf("lookup dingtalk chat binding: %w", err)
	}
	// Clear only this task's "processing" emotions before the reply is visible.
	// Later queued turns in the same chat keep their own per-message indicators.
	if o.typing != nil {
		o.typing.ClearTask(ctx, sessionID, taskID)
	}
	// task-failed delivers the failure notice; chat-done delivers the reply.
	// Without the notice a failed run is indistinguishable from a silent
	// success — the processing emotion just vanishes (the Lark channel
	// surfaces failures through its run card instead).
	content := taskFailedText
	if e.Type == protocol.EventChatDone {
		content = chatDoneContent(e.Payload)
		if content == "" {
			return nil // nothing to say (empty completion)
		}
	}
	pending, err := o.q.ListPendingChatMessagePreviewsAfterTask(ctx, taskID)
	if err != nil {
		o.logger.WarnContext(ctx, "dingtalk outbound: pending message summary lookup failed",
			"chat_session_id", util.UUIDToString(sessionID),
			"task_id", util.UUIDToString(taskID),
			"error", err)
	} else {
		content = appendPendingQueueSummary(content, pending)
	}
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          binding.InstallationID,
		ChannelType: string(TypeDingtalk),
	})
	if err != nil {
		return fmt.Errorf("load dingtalk installation: %w", err)
	}
	if inst.Status != "active" {
		return nil // revoked between trigger and reply
	}
	creds, err := decodeChannelCredentials(inst.Config, o.decrypt)
	if err != nil {
		return fmt.Errorf("decode dingtalk credentials: %w", err)
	}
	sendStarted := time.Now()
	if err := o.messenger.SendMarkdown(ctx, creds, outboundTarget(binding), content); err != nil {
		return fmt.Errorf("post dingtalk reply: %w", err)
	}
	if trace, present, err := chatTraceFromEvent(e); err != nil {
		return err
	} else if present {
		chattrace.LogStage(o.logger, trace, "dingtalk_api", "accepted",
			"task_id", util.UUIDToString(taskID),
			"chat_session_id", util.UUIDToString(sessionID),
			"stage_elapsed_ms", time.Since(sendStarted).Milliseconds(),
			"delivery_boundary", "api_accepted",
		)
	}
	return nil
}

const (
	pendingQueuePreviewLimit     = 10
	pendingQueuePreviewRuneLimit = 60
)

func appendPendingQueueSummary(content string, pending []db.ListPendingChatMessagePreviewsAfterTaskRow) string {
	if len(pending) == 0 {
		return content
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(content))
	fmt.Fprintf(&b, "\n\n---\n\n**后续待处理（%d 条）**\n", len(pending))
	visible := len(pending)
	if visible > pendingQueuePreviewLimit {
		visible = pendingQueuePreviewLimit
	}
	for i := 0; i < visible; i++ {
		fmt.Fprintf(&b, "%d. %s\n", i+1, summarizePendingMessage(pending[i].Content))
	}
	if remaining := len(pending) - visible; remaining > 0 {
		fmt.Fprintf(&b, "\n另外还有 %d 条待处理。", remaining)
	}
	return strings.TrimSpace(b.String())
}

func summarizePendingMessage(content string) string {
	text := strings.Join(strings.Fields(content), " ")
	if text == "" {
		return "（空消息）"
	}
	runes := []rune(text)
	if len(runes) <= pendingQueuePreviewRuneLimit {
		return text
	}
	return string(runes[:pendingQueuePreviewRuneLimit]) + "…"
}

// outboundTarget recovers the robot-API send target from the chat binding:
// a DM addresses the recipient by the staff id captured on the binding
// config; a group reads the real conversation id from config because its
// binding key also contains the sender-isolation suffix.
func outboundTarget(b db.ChannelChatSessionBinding) RobotTarget {
	var cfg dingtalkBindingConfig
	if len(b.Config) > 0 {
		_ = json.Unmarshal(b.Config, &cfg)
	}
	if b.ChatType == string(channel.ChatTypeP2P) {
		if cfg.SenderStaffID != "" {
			return RobotTarget{UserStaffID: cfg.SenderStaffID}
		}
	}
	if cfg.OpenConversationID != "" {
		return RobotTarget{OpenConversationID: cfg.OpenConversationID}
	}
	// Bindings created before sender isolation stored the real group id in
	// channel_chat_id. Keep in-flight replies valid during a rolling deploy.
	return RobotTarget{OpenConversationID: b.ChannelChatID}
}

// sessionIDFromEvent recovers the chat session id from a bus event. The
// top-level scope hint is only stamped on chat:done; task:failed goes
// through broadcastTaskEvent, which carries chat_session_id solely inside
// the payload map — so fall back to the payload like lark's
// taskAndSessionFromEvent does.
func sessionIDFromEvent(e events.Event) pgtype.UUID {
	if id, err := util.ParseUUID(e.ChatSessionID); err == nil && id.Valid {
		return id
	}
	switch p := e.Payload.(type) {
	case map[string]any:
		if s, _ := p["chat_session_id"].(string); s != "" {
			if id, err := util.ParseUUID(s); err == nil {
				return id
			}
		}
	case protocol.ChatDonePayload:
		if id, err := util.ParseUUID(p.ChatSessionID); err == nil {
			return id
		}
	}
	return pgtype.UUID{}
}

func taskIDFromEvent(e events.Event) pgtype.UUID {
	switch p := e.Payload.(type) {
	case map[string]any:
		if s, _ := p["task_id"].(string); s != "" {
			if id, err := util.ParseUUID(s); err == nil {
				return id
			}
		}
	case protocol.ChatDonePayload:
		if id, err := util.ParseUUID(p.TaskID); err == nil {
			return id
		}
	}
	return pgtype.UUID{}
}

// chatDoneContent extracts the reply text from an EventChatDone payload
// (the typed payload, or its map form after a serialization round trip).
func chatDoneContent(payload any) string {
	switch p := payload.(type) {
	case protocol.ChatDonePayload:
		return p.Content
	case map[string]any:
		if s, ok := p["content"].(string); ok {
			return s
		}
	}
	return ""
}

func chatTraceFromEvent(e events.Event) (chattrace.Trace, bool, error) {
	if e.Type != protocol.EventChatDone {
		return chattrace.Trace{}, false, nil
	}
	var traceID string
	var startedAtUnixMS int64
	switch p := e.Payload.(type) {
	case protocol.ChatDonePayload:
		traceID = p.TraceID
		startedAtUnixMS = p.TraceStartedAtUnixMS
	case map[string]any:
		traceID, _ = p["trace_id"].(string)
		switch value := p["trace_started_at_unix_ms"].(type) {
		case int64:
			startedAtUnixMS = value
		case float64:
			startedAtUnixMS = int64(value)
		}
	}
	if traceID == "" && startedAtUnixMS == 0 {
		return chattrace.Trace{}, false, nil
	}
	trace, err := chattrace.From(traceID, "dingtalk_stream", startedAtUnixMS)
	if err != nil {
		return chattrace.Trace{}, true, err
	}
	return trace, true, nil
}
