package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
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
	q          outboundQueries
	decrypt    Decrypter
	messenger  *RobotMessenger
	typing     *TypingIndicatorManager
	logger     *slog.Logger
	dispatchMu sync.Mutex
	dispatched map[string]struct{}
}

// NewOutbound builds the DingTalk outbound subscriber. typing is the
// "processing" emotion manager to clear before the reply lands; nil
// disables the clear.
func NewOutbound(q outboundQueries, decrypt Decrypter, messenger *RobotMessenger, typing *TypingIndicatorManager, logger *slog.Logger) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	return &Outbound{q: q, decrypt: decrypt, messenger: messenger, typing: typing, logger: logger, dispatched: make(map[string]struct{})}
}

// Register covers both ingress generations. Legacy Stream sessions settle on
// chat-done/task-failed through TypingIndicatorManager. Dispatch Command 2.0
// issue tasks use task:queued for the processing emotion and terminal task
// events for recall plus robot_sdk reply.
func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, o.handleEvent)
	bus.Subscribe(protocol.EventTaskQueued, o.handleEvent)
	bus.Subscribe(protocol.EventTaskFailed, o.handleEvent)
	bus.Subscribe(protocol.EventTaskCompleted, o.handleEvent)
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
	if handled, err := o.processDispatchEvent(ctx, e); handled {
		return err
	}
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
	// Stream callbacks carry a per-message session webhook. Prefer it whenever
	// present: legacy Stream installations predate robot_code persistence, and
	// client_id must never be guessed as the robot identity. HTTP callbacks do
	// not carry this locator and continue through the robot API below.
	if reply, ok := o.sessionReplyContext(ctx, taskID); ok {
		if err := postSessionWebhook(ctx, o.messenger.httpClient, reply.Webhook, content); err != nil {
			return fmt.Errorf("post dingtalk Stream reply: %w", err)
		}
		return nil
	}
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

func (o *Outbound) sessionReplyContext(ctx context.Context, taskID pgtype.UUID) (dingtalkSessionReplyContext, bool) {
	task, err := o.q.GetAgentTask(ctx, taskID)
	if err != nil || len(task.Context) == 0 {
		return dingtalkSessionReplyContext{}, false
	}
	var private map[string]json.RawMessage
	if err := json.Unmarshal(task.Context, &private); err != nil {
		return dingtalkSessionReplyContext{}, false
	}
	var reply dingtalkSessionReplyContext
	if err := json.Unmarshal(private[dingtalkSessionReplyContextKey], &reply); err != nil {
		return dingtalkSessionReplyContext{}, false
	}
	reply.Webhook = strings.TrimSpace(reply.Webhook)
	return reply, reply.Webhook != ""
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

// dispatchInstallationResolver is optional to preserve the existing chat
// outbound test seam. The generated DingTalk binding query resolves the
// installation from (workspace, agent), which is the stable locator carried
// by an issue-backed Dispatch Command.
type dispatchInstallationResolver interface {
	GetDingTalkAccountBindingByAgent(context.Context, db.GetDingTalkAccountBindingByAgentParams) (db.ChannelInstallation, error)
}

type dispatchOutboundClaimStore interface {
	ClaimDispatchOutbound(context.Context, pgtype.UUID) (bool, error)
}

type dispatchProcessingReactionClaimStore interface {
	ClaimDispatchProcessingReaction(context.Context, pgtype.UUID) (bool, error)
}

type dispatchProcessingRecallClaimStore interface {
	ClaimDispatchProcessingRecall(context.Context, pgtype.UUID) (bool, error)
}

type dispatchTaskResultResolver interface {
	GetAgentTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error)
}

type dispatchRobotRoute struct {
	credentials channelCredentials
	reply       RobotTarget
	emotion     EmotionTarget
}

const (
	dispatchPhaseProcessing = "processing"
	dispatchPhaseRecall     = "recall"
	dispatchPhaseOutbound   = "outbound"
)

// processDispatchEvent owns only the issue-backed Dispatch Command 2.0 robot
// lifecycle. It returns handled=false for legacy chat events and for DWS
// digital-employee commands, which perform outbound inside the sandbox.
func (o *Outbound) processDispatchEvent(ctx context.Context, e events.Event) (bool, error) {
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return false, nil
	}
	source, _ := payload["dispatch_source"].(map[string]any)
	if source["type"] != "robot" {
		return false, nil
	}
	outbound, _ := payload["dispatch_outbound"].(map[string]any)
	if outbound["mode"] != "robot_sdk" {
		return false, nil
	}
	if e.Type != protocol.EventTaskQueued && e.Type != protocol.EventTaskCompleted && e.Type != protocol.EventTaskFailed {
		return false, nil
	}
	taskID, _ := payload["task_id"].(string)
	taskUUID, err := util.ParseUUID(taskID)
	if err != nil {
		return true, fmt.Errorf("parse dispatch task id: %w", err)
	}
	idempotencyKey, _ := payload["dispatch_idempotency_key"].(string)
	if idempotencyKey == "" {
		idempotencyKey = taskID
	}
	if idempotencyKey == "" {
		return true, errors.New("dingtalk dispatch outbound: missing idempotency key")
	}

	wantProcessing := false
	wantRecall := false
	wantReply := false
	content := ""
	switch e.Type {
	case protocol.EventTaskQueued:
		wantProcessing, err = o.claimDispatchPhase(ctx, taskUUID, idempotencyKey, dispatchPhaseProcessing)
	case protocol.EventTaskCompleted:
		wantRecall, err = o.claimDispatchPhase(ctx, taskUUID, idempotencyKey, dispatchPhaseRecall)
		content = o.dispatchCompletionContent(ctx, taskUUID, payload)
		if strings.TrimSpace(content) != "" {
			wantReply, err = o.claimDispatchPhaseAfter(ctx, taskUUID, idempotencyKey, dispatchPhaseOutbound, err)
		}
	case protocol.EventTaskFailed:
		wantRecall, err = o.claimDispatchPhase(ctx, taskUUID, idempotencyKey, dispatchPhaseRecall)
		content = taskFailedText
		wantReply, err = o.claimDispatchPhaseAfter(ctx, taskUUID, idempotencyKey, dispatchPhaseOutbound, err)
	}
	if err != nil {
		return true, err
	}
	if !wantProcessing && !wantRecall && !wantReply {
		return true, nil
	}

	route, err := o.resolveDispatchRobotRoute(ctx, payload)
	if err != nil {
		return true, err
	}
	var lifecycleErr error
	if wantProcessing {
		if err := o.messenger.AddEmotionReply(ctx, route.credentials, route.emotion); err != nil {
			lifecycleErr = errors.Join(lifecycleErr, fmt.Errorf("add dingtalk dispatch processing reaction: %w", err))
		}
	}
	if wantRecall {
		if err := o.messenger.RecallEmotionReply(ctx, route.credentials, route.emotion); err != nil {
			lifecycleErr = errors.Join(lifecycleErr, fmt.Errorf("recall dingtalk dispatch processing reaction: %w", err))
		}
	}
	if wantReply {
		if err := o.messenger.SendMarkdown(ctx, route.credentials, route.reply, content); err != nil {
			lifecycleErr = errors.Join(lifecycleErr, fmt.Errorf("post dingtalk dispatch reply: %w", err))
		}
	}
	return true, lifecycleErr
}

func (o *Outbound) dispatchCompletionContent(ctx context.Context, taskID pgtype.UUID, payload map[string]any) string {
	output, _ := payload["output"].(string)
	if strings.TrimSpace(output) != "" {
		return output
	}
	resolver, ok := o.q.(dispatchTaskResultResolver)
	if !ok {
		return ""
	}
	task, err := resolver.GetAgentTask(ctx, taskID)
	if err != nil {
		return ""
	}
	var result protocol.TaskCompletedPayload
	if json.Unmarshal(task.Result, &result) != nil {
		return ""
	}
	return result.Output
}

func (o *Outbound) resolveDispatchRobotRoute(ctx context.Context, payload map[string]any) (dispatchRobotRoute, error) {
	resolver, ok := o.q.(dispatchInstallationResolver)
	if !ok {
		return dispatchRobotRoute{}, errors.New("dingtalk dispatch outbound: installation resolver is not configured")
	}
	workspaceID, err := util.ParseUUID(fmt.Sprint(payload["workspace_id"]))
	if err != nil {
		return dispatchRobotRoute{}, fmt.Errorf("parse dispatch workspace id: %w", err)
	}
	agentID, err := util.ParseUUID(fmt.Sprint(payload["agent_id"]))
	if err != nil {
		return dispatchRobotRoute{}, fmt.Errorf("parse dispatch agent id: %w", err)
	}
	inst, err := resolver.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{WorkspaceID: workspaceID, AgentID: agentID})
	if err != nil {
		return dispatchRobotRoute{}, fmt.Errorf("lookup dispatch DingTalk installation: %w", err)
	}
	if inst.Status != "active" {
		return dispatchRobotRoute{}, errors.New("dingtalk dispatch outbound: installation is not active")
	}
	creds, err := decodeChannelCredentials(inst.Config, o.decrypt)
	if err != nil {
		return dispatchRobotRoute{}, fmt.Errorf("decode dispatch DingTalk credentials: %w", err)
	}
	data, _ := payload["dispatch_event_data"].(map[string]any)
	conversation, _ := data["conversation"].(map[string]any)
	cid, _ := conversation["openConversationId"].(string)
	if strings.TrimSpace(cid) == "" {
		return dispatchRobotRoute{}, errors.New("dingtalk dispatch outbound: missing openConversationId")
	}
	messages, _ := data["messages"].([]any)
	if len(messages) == 0 {
		return dispatchRobotRoute{}, errors.New("dingtalk dispatch outbound: missing messages")
	}
	latest, _ := messages[len(messages)-1].(map[string]any)
	latestOpenMsgID, _ := latest["openMsgId"].(string)
	if strings.TrimSpace(latestOpenMsgID) == "" {
		return dispatchRobotRoute{}, errors.New("dingtalk dispatch outbound: latest message has no openMsgId")
	}
	reply := RobotTarget{OpenConversationID: cid, ReplyToOpenMsgID: latestOpenMsgID}
	if kind, _ := conversation["type"].(string); kind == "single" || kind == "p2p" || kind == "private" || kind == "direct" {
		sender, _ := data["sender"].(map[string]any)
		if staffID, _ := sender["staffId"].(string); strings.TrimSpace(staffID) != "" {
			reply = RobotTarget{UserStaffID: staffID, ReplyToOpenMsgID: latestOpenMsgID}
		} else {
			return dispatchRobotRoute{}, errors.New("dingtalk dispatch outbound: private conversation sender has no staffId")
		}
	}
	return dispatchRobotRoute{
		credentials: creds,
		reply:       reply,
		emotion:     EmotionTarget{OpenConversationID: cid, OpenMsgID: latestOpenMsgID},
	}, nil
}

func (o *Outbound) claimDispatchPhaseAfter(ctx context.Context, taskID pgtype.UUID, idempotencyKey, phase string, previous error) (bool, error) {
	if previous != nil {
		return false, previous
	}
	return o.claimDispatchPhase(ctx, taskID, idempotencyKey, phase)
}

func (o *Outbound) claimDispatchPhase(ctx context.Context, taskID pgtype.UUID, idempotencyKey, phase string) (bool, error) {
	switch phase {
	case dispatchPhaseProcessing:
		if store, ok := o.q.(dispatchProcessingReactionClaimStore); ok {
			claimed, err := store.ClaimDispatchProcessingReaction(ctx, taskID)
			if err != nil {
				return false, fmt.Errorf("claim dispatch processing reaction: %w", err)
			}
			return claimed, nil
		}
	case dispatchPhaseRecall:
		if store, ok := o.q.(dispatchProcessingRecallClaimStore); ok {
			claimed, err := store.ClaimDispatchProcessingRecall(ctx, taskID)
			if err != nil {
				return false, fmt.Errorf("claim dispatch processing recall: %w", err)
			}
			return claimed, nil
		}
	case dispatchPhaseOutbound:
		if store, ok := o.q.(dispatchOutboundClaimStore); ok {
			claimed, err := store.ClaimDispatchOutbound(ctx, taskID)
			if err != nil {
				return false, fmt.Errorf("claim dispatch outbound: %w", err)
			}
			return claimed, nil
		}
	}
	key := phase + ":" + idempotencyKey
	o.dispatchMu.Lock()
	defer o.dispatchMu.Unlock()
	if _, exists := o.dispatched[key]; exists {
		return false, nil
	}
	o.dispatched[key] = struct{}{}
	return true, nil
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
