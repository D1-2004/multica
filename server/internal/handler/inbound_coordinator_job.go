package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/service/scenememory"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	inboundCoordinatorWorkerConcurrency  = 8
	inboundCoordinatorWorkerPollInterval = 500 * time.Millisecond
	inboundCoordinatorWorkerMaxAttempts  = 6
)

// InboundCoordinatorJobWorker executes accepted short loops from PostgreSQL.
// Router acknowledgement never depends on model/DWS latency, and a replica
// restart only releases a lease — it does not lose the inbound message.
type InboundCoordinatorJobWorker struct {
	handler *Handler
	notify  chan struct{}
	done    chan struct{}
}

func NewInboundCoordinatorJobWorker(h *Handler) *InboundCoordinatorJobWorker {
	return &InboundCoordinatorJobWorker{
		handler: h,
		notify:  make(chan struct{}, inboundCoordinatorWorkerConcurrency),
		done:    make(chan struct{}),
	}
}

func (w *InboundCoordinatorJobWorker) Notify() {
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *InboundCoordinatorJobWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	defer close(w.done)
	var workers sync.WaitGroup
	workers.Add(inboundCoordinatorWorkerConcurrency)
	for range inboundCoordinatorWorkerConcurrency {
		go func() {
			defer workers.Done()
			w.runLoop(ctx)
		}()
	}
	workers.Wait()
}

func (w *InboundCoordinatorJobWorker) runLoop(ctx context.Context) {
	ticker := time.NewTicker(inboundCoordinatorWorkerPollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("inbound coordinator job failed", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-w.notify:
		case <-ticker.C:
		}
	}
}

func (w *InboundCoordinatorJobWorker) WaitWithTimeout(timeout time.Duration) bool {
	if w == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return true
	case <-timer.C:
		return false
	}
}

func (w *InboundCoordinatorJobWorker) ProcessNext(ctx context.Context) (bool, error) {
	if w == nil || w.handler == nil || w.handler.Queries == nil {
		return false, nil
	}
	job, err := w.handler.Queries.ClaimInboundCoordinatorJob(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	slog.Info("inbound coordinator job claimed",
		"event", "inbound_coordinator_job_claimed",
		"job_id", util.UUIDToString(job.ID),
		"attempt", job.AttemptCount,
	)
	command, err := restoreInboundCoordinatorCommand(
		job.Command, job.EndpointNamespaceID, w.handler.TaskCompletionTargetIdentity,
	)
	if err != nil {
		return true, w.fail(ctx, job, command, "invalid persisted dispatch command")
	}
	dispatchContext := agentDispatchContext{
		EndpointID:          job.DispatchEndpointID,
		EndpointNamespaceID: job.EndpointNamespaceID,
		UserID:              job.UserID,
		WorkspaceID:         job.WorkspaceID,
		AgentID:             job.AgentID,
	}
	plan, err := buildAgentDispatchExecutionPlan(command, dispatchContext)
	if err != nil {
		return true, w.fail(ctx, job, command, "failed to rebuild dispatch plan")
	}
	acceptance, err := w.handler.Queries.GetAgentDispatchAcceptance(ctx, db.GetAgentDispatchAcceptanceParams{
		EndpointID: job.EndpointNamespaceID, IdempotencyKey: job.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return true, w.fail(ctx, job, command, "dispatch acceptance no longer exists")
	}
	if err != nil {
		return true, w.retry(ctx, job, fmt.Errorf("load dispatch acceptance: %w", err))
	}
	recovered, recoveredOK, recoverErr := w.handler.recoverAgentDispatchAcceptance(
		ctx, command, dispatchContext, acceptance,
	)
	if recoverErr != nil {
		return true, w.retry(ctx, job, fmt.Errorf("recover dispatch result: %w", recoverErr))
	}
	if recoveredOK {
		decision := recoveredCoordinatorDecision(command, recovered)
		if err := w.handler.persistCoordinatorJobChat(ctx, job, decision); err != nil {
			return true, w.retry(ctx, job, fmt.Errorf("persist recovered coordinator Chat: %w", err))
		}
		return true, w.complete(ctx, job)
	}
	jobCtx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	// The job id is the coordinator trace id of this turn, so the Scene
	// Memory trigger recorded at enqueue time (coord_trace_id = job id) and
	// the turn's SLS/Langfuse trace resolve to the same identifier.
	jobCtx = inboundcoord.ContextWithTraceID(jobCtx, util.UUIDToString(job.ID))
	var recordedDecision *inboundcoord.Decision
	jobCtx = inboundcoord.WithDecisionObserver(jobCtx, func(decision inboundcoord.Decision) {
		copied := decision
		recordedDecision = &copied
	})
	req, err := http.NewRequestWithContext(jobCtx, http.MethodPost, "/internal/inbound-coordinator", nil)
	if err != nil {
		return true, err
	}
	req.Header.Set("Idempotency-Key", job.IdempotencyKey)
	response := newBufferedDispatchResponse()
	w.handler.executeAgentDispatchV2(response, req, command, plan, dispatchContext)
	if response.Status() >= http.StatusOK && response.Status() < http.StatusMultipleChoices {
		if recordedDecision != nil {
			if err := w.handler.persistCoordinatorJobChat(jobCtx, job, *recordedDecision); err != nil {
				return true, w.retry(ctx, job, fmt.Errorf("persist coordinator Chat: %w", err))
			}
		}
		return true, w.complete(ctx, job)
	}
	if response.Status() == http.StatusConflict || response.Status() >= http.StatusInternalServerError {
		return true, w.retry(ctx, job, fmt.Errorf("%s", dispatchRejectReason(response)))
	}
	return true, w.fail(ctx, job, command, dispatchRejectReason(response))
}

func dispatchRejectReason(response *bufferedDispatchResponse) string {
	status := 0
	body := ""
	if response != nil {
		status = response.Status()
		body = strings.TrimSpace(response.body.String())
		runes := []rune(body)
		if len(runes) > 300 {
			body = string(runes[:300])
		}
	}
	if body == "" {
		return fmt.Sprintf("dispatch rejected with HTTP %d", status)
	}
	return fmt.Sprintf("dispatch rejected with HTTP %d: %s", status, body)
}

func restoreInboundCoordinatorCommand(raw []byte, endpointID pgtype.UUID, targetIdentity string) (DispatchCommand, error) {
	var command DispatchCommand
	if err := json.Unmarshal(raw, &command); err != nil {
		return command, err
	}
	command.DispatchEndpointID = uuidToString(endpointID)
	return bindDispatchCompletionTarget(command, targetIdentity)
}

func (w *InboundCoordinatorJobWorker) complete(ctx context.Context, job db.InboundCoordinatorJob) error {
	rows, err := w.handler.Queries.CompleteInboundCoordinatorJob(ctx, db.CompleteInboundCoordinatorJobParams{
		ID: job.ID, LeaseToken: job.LeaseToken,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("complete inbound coordinator job: lease no longer owned")
	}
	slog.Info("inbound coordinator job completed",
		"event", "inbound_coordinator_job_completed",
		"job_id", util.UUIDToString(job.ID),
		"attempt", job.AttemptCount,
	)
	return nil
}

func recoveredCoordinatorDecision(command DispatchCommand, response *bufferedDispatchResponse) inboundcoord.Decision {
	action := inboundcoord.ActionReply
	text := "已从已有执行结果恢复。"
	var dispatchResponse AgentDispatchResponse
	if response != nil && json.Unmarshal(response.body.Bytes(), &dispatchResponse) == nil &&
		dispatchResponse.Continuation.Kind == "issue" {
		action = inboundcoord.ActionIssue
		text = "已恢复到原 Issue 继续处理。"
	}
	return inboundcoord.Decision{
		Action:   action,
		UserText: text,
		Reason:   "recovered durable dispatch result",
		Source:   coordinatorSource(command),
		Steps: []protocol.ChatCoordinatorStep{{
			Seq: 1, Type: "thinking", Content: "从已有的幂等执行记录恢复，未重复创建任务。",
		}, {
			Seq: 2, Type: "text", Content: text,
		}},
	}
}

func (w *InboundCoordinatorJobWorker) retry(ctx context.Context, job db.InboundCoordinatorJob, cause error) error {
	if job.AttemptCount >= inboundCoordinatorWorkerMaxAttempts {
		var command DispatchCommand
		_ = json.Unmarshal(job.Command, &command)
		return w.fail(ctx, job, command, cause.Error())
	}
	delay := time.Second * time.Duration(1<<min(job.AttemptCount-1, 4))
	slog.Warn("inbound coordinator job retry scheduled",
		"event", "inbound_coordinator_job_retry_scheduled",
		"job_id", util.UUIDToString(job.ID),
		"attempt", job.AttemptCount,
		"delay_ms", delay.Milliseconds(),
		"error", cause.Error(),
	)
	rows, err := w.handler.Queries.RetryInboundCoordinatorJob(ctx, db.RetryInboundCoordinatorJobParams{
		ID: job.ID, LeaseToken: job.LeaseToken,
		AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true},
		LastError:   pgtype.Text{String: cause.Error(), Valid: true},
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("retry inbound coordinator job: lease no longer owned")
	}
	return nil
}

func (w *InboundCoordinatorJobWorker) fail(ctx context.Context, job db.InboundCoordinatorJob, command DispatchCommand, reason string) error {
	if command.CompletionCallback != nil && w.handler.TaskService != nil {
		_ = w.handler.TaskService.EnqueueSynchronousTaskCompletion(
			ctx, command.CompletionCallback.URL, command.CompletionCallback.Target,
			job.AgentID, reason, "coordinator_job_failed",
		)
	}
	decision := inboundcoord.Decision{
		Action: inboundcoord.ActionContinue,
		Reason: reason,
		Source: coordinatorSource(command),
		Steps:  []protocol.ChatCoordinatorStep{{Seq: 1, Type: "error", Content: reason, Error: true}},
	}
	if err := w.handler.persistCoordinatorJobChat(ctx, job, decision); err != nil {
		slog.Error("persist failed coordinator Chat failed", "job_id", util.UUIDToString(job.ID), "error", err)
	}
	rows, err := w.handler.Queries.FailInboundCoordinatorJob(ctx, db.FailInboundCoordinatorJobParams{
		ID: job.ID, LeaseToken: job.LeaseToken,
		LastError: pgtype.Text{String: reason, Valid: true},
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("fail inbound coordinator job: lease no longer owned")
	}
	slog.Error("inbound coordinator job terminal failure",
		"event", "inbound_coordinator_job_failed",
		"job_id", util.UUIDToString(job.ID),
		"attempt", job.AttemptCount,
		"reason", reason,
	)
	return nil
}

func coordinatorSource(command DispatchCommand) inboundcoord.Source {
	if command.Source.Type == string(inboundcoord.SourceDigitalEmployee) {
		return inboundcoord.SourceDigitalEmployee
	}
	return inboundcoord.SourceRobot
}

func dispatchSceneIdentity(command DispatchCommand) (kind, title string) {
	chatType := strings.TrimSpace(command.Event.Data.Conversation.Type)
	if chatType == "" {
		chatType = strings.TrimSpace(dispatchAssocIDs(command).Kind)
	}
	kind = scenememory.KindFromChatType(chatType)
	title = strings.TrimSpace(command.Event.Data.Conversation.Title)
	if title == "" && kind == scenememory.KindDM {
		title = strings.TrimSpace(command.Event.Data.Sender.DisplayName)
	}
	return kind, title
}

func coordinatorJobTitle(command DispatchCommand) string {
	kind, name := dispatchSceneIdentity(command)
	label := "单聊"
	if kind == scenememory.KindGroup {
		label = "群聊"
	}
	text := ""
	for _, message := range command.Event.Data.Messages {
		if message.Reaction == nil && strings.TrimSpace(message.Text) != "" {
			text = strings.TrimSpace(message.Text)
			break
		}
	}
	if text == "" {
		text = "Inbound event"
	}
	runes := []rune(text)
	if len(runes) > 40 {
		text = string(runes[:40]) + "…"
	}
	if name != "" {
		return label + " · " + name + " · " + text
	}
	return label + " · " + text
}

func coordinatorJobMessage(command DispatchCommand) string {
	parts := make([]string, 0, len(command.Event.Data.Messages))
	for _, message := range command.Event.Data.Messages {
		if message.Reaction == nil && strings.TrimSpace(message.Text) != "" {
			parts = append(parts, strings.TrimSpace(message.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}

func (h *Handler) enqueueInboundCoordinatorJob(
	ctx context.Context,
	acceptance db.AgentDispatchAcceptance,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
	idempotencyKey string,
	displayContent string,
) (*bufferedDispatchResponse, db.InboundCoordinatorJob, error) {
	var job db.InboundCoordinatorJob
	if h == nil || h.TxStarter == nil {
		return nil, job, errors.New("coordinator job store is not configured")
	}
	rawCommand, err := json.Marshal(command)
	if err != nil {
		return nil, job, err
	}
	response := newBufferedDispatchResponse()
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "accepted"})
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, job, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	session, err := qtx.CreateChatSession(ctx, db.CreateChatSessionParams{
		WorkspaceID: dispatchContext.WorkspaceID,
		AgentID:     dispatchContext.AgentID,
		CreatorID:   dispatchContext.UserID,
		Title:       coordinatorJobTitle(command),
	})
	if err != nil {
		return nil, job, err
	}
	userMessage, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: session.ID,
		Role:          "user",
		Content:       firstNonEmpty(strings.TrimSpace(displayContent), coordinatorJobMessage(command)),
		MessageKind:   pgtype.Text{String: protocol.ChatMessageKindMessage, Valid: true},
	})
	if err != nil {
		return nil, job, err
	}
	job, err = qtx.CreateInboundCoordinatorJob(ctx, db.CreateInboundCoordinatorJobParams{
		AcceptanceID:        acceptance.ID,
		WorkspaceID:         dispatchContext.WorkspaceID,
		AgentID:             dispatchContext.AgentID,
		UserID:              dispatchContext.UserID,
		EndpointNamespaceID: dispatchContext.EndpointNamespaceID,
		DispatchEndpointID:  dispatchContext.EndpointID,
		IdempotencyKey:      idempotencyKey,
		Command:             rawCommand,
		ChatSessionID:       session.ID,
		UserMessageID:       userMessage.ID,
	})
	if err != nil {
		return nil, job, err
	}
	_, err = qtx.CompleteAgentDispatchAcceptance(ctx, db.CompleteAgentDispatchAcceptanceParams{
		ResponseStatus:      pgtype.Int4{Int32: int32(response.Status()), Valid: true},
		ResponseContentType: pgtype.Text{String: response.header.Get("Content-Type"), Valid: true},
		ResponseBody:        append([]byte{}, response.body.Bytes()...),
		ID:                  acceptance.ID,
		LeaseToken:          acceptance.LeaseToken,
	})
	if err != nil {
		return nil, job, err
	}
	h.markSceneMemoryDirty(ctx, qtx, command, dispatchContext, job)
	if err := tx.Commit(ctx); err != nil {
		return nil, job, err
	}
	if h.SceneMemoryWorker != nil {
		h.SceneMemoryWorker.Notify()
	}
	slog.Info("inbound coordinator job accepted",
		"event", "inbound_coordinator_job_accepted",
		"job_id", util.UUIDToString(job.ID),
		"source", command.Source.Type,
	)
	h.publishChatToUser(protocol.EventChatMessage, util.UUIDToString(job.WorkspaceID), util.UUIDToString(job.UserID), "member", util.UUIDToString(job.UserID), util.UUIDToString(job.ChatSessionID), protocol.ChatMessagePayload{
		ChatSessionID: util.UUIDToString(job.ChatSessionID),
		MessageID:     util.UUIDToString(job.UserMessageID),
		Role:          "user", Content: userMessage.Content,
		CreatedAt:      timestampToString(userMessage.CreatedAt),
		SessionCreated: true,
	})
	return response, job, nil
}

func (h *Handler) persistCoordinatorJobChat(ctx context.Context, job db.InboundCoordinatorJob, decision inboundcoord.Decision) error {
	content := strings.TrimSpace(decision.UserText)
	if content == "" {
		switch decision.Action {
		case inboundcoord.ActionIssue:
			content = "已转入 Issue 继续处理。"
		case inboundcoord.ActionContinue:
			content = "已交给 Agent 继续处理。"
		case inboundcoord.ActionSilence:
			content = "本轮无需回复。"
		case inboundcoord.ActionRetry:
			content = "关联事项正在处理中，稍后重试。"
		default:
			content = "Coordinator 已完成本轮处理。"
		}
	}
	trace := decision.TraceJSON()
	var message db.ChatMessage
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	exists, err := qtx.CoordinatorChatMessageExists(ctx, job.ChatSessionID)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	created, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: job.ChatSessionID,
		Role:          "assistant",
		Content:       content,
		MessageKind:   pgtype.Text{String: protocol.ChatMessageKindCoordinator, Valid: true},
		ElapsedMs:     pgtype.Int8{Int64: decision.ElapsedMs, Valid: decision.ElapsedMs > 0},
		SourcePayload: trace,
	})
	if err != nil {
		return err
	}
	message = created
	if err := qtx.TouchChatSession(ctx, job.ChatSessionID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	tracePayload := decision.Trace()
	h.publishChatToUser(protocol.EventChatMessage, util.UUIDToString(job.WorkspaceID), util.UUIDToString(job.UserID), "agent", util.UUIDToString(job.AgentID), util.UUIDToString(job.ChatSessionID), protocol.ChatMessagePayload{
		ChatSessionID: util.UUIDToString(job.ChatSessionID),
		MessageID:     util.UUIDToString(message.ID),
		Role:          "assistant", Content: message.Content,
		CreatedAt:   timestampToString(message.CreatedAt),
		MessageKind: protocol.ChatMessageKindCoordinator,
		ElapsedMs:   decision.ElapsedMs,
		Coordinator: &tracePayload,
	})
	return nil
}

func shouldDeferInboundCoordinator(command DispatchCommand, plan agentDispatchExecutionPlan) bool {
	return command.CompletionCallback != nil &&
		command.Event.Domain == "channel" && command.Event.Type == "message.created" &&
		(plan.MaterializerType == protocol.DispatchSurfaceTypeIssue ||
			plan.MaterializerType == protocol.DispatchSurfaceTypeChat)
}

func (h *Handler) markSceneMemoryDirty(
	ctx context.Context,
	qtx *db.Queries,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
	job db.InboundCoordinatorJob,
) {
	if h == nil || h.SceneMemoryStore == nil || qtx == nil {
		return
	}
	if command.Source.Type != "digital_employee" {
		return
	}
	body := dispatchInboundEventBody(command)
	if body == "" || isInboundResetMemory(body) {
		return
	}
	flags, err := qtx.GetAgentSceneMemoryFlags(ctx, dispatchContext.AgentID)
	if err != nil || !flags.WriteEnabled {
		return
	}
	ids := dispatchAssocIDs(command)
	if ids.ConversationID == "" || !assoc.ValidSceneID(ids.ConversationID) {
		return
	}
	identity, err := qtx.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
		WorkspaceID: dispatchContext.WorkspaceID,
		AgentID:     dispatchContext.AgentID,
	})
	if err != nil {
		slog.Warn("scene memory mark dirty skipped; no DWS identity",
			"event", "scene_memory_mark_dirty",
			"error", err,
		)
		return
	}
	kind, title := dispatchSceneIdentity(command)
	store := scenememory.NewStore(qtx)
	row, err := store.MarkDirty(ctx, scenememory.Identity{
		WorkspaceID: dispatchContext.WorkspaceID,
		AgentID:     dispatchContext.AgentID,
		OrgID:       identity.OrgID,
		SceneKey:    ids.ConversationID,
		SceneKind:   kind,
		SceneTitle:  title,
	}, scenememory.DirtyTrigger{
		OccurredAt:     dispatchMessageOccurredAt(command),
		EvidenceID:     ids.EvidenceID,
		JobID:          job.ID,
		CoordTraceID:   util.UUIDToString(job.ID),
		IdempotencyKey: job.IdempotencyKey,
	})
	if err != nil {
		slog.Warn("scene memory mark dirty failed",
			"event", "scene_memory_mark_dirty",
			"conversation_id", ids.ConversationID,
			"error", err,
		)
		return
	}
	slog.Info("scene memory marked dirty",
		"event", "scene_memory_mark_dirty",
		"workspace_id", util.UUIDToString(dispatchContext.WorkspaceID),
		"agent_id", util.UUIDToString(dispatchContext.AgentID),
		"scene_key", ids.ConversationID,
		"dirty_revision", row.DirtyRevision,
		"idempotency", job.IdempotencyKey,
		"scene_memory_id", util.UUIDToString(row.ID),
	)
}

func dispatchMessageOccurredAt(command DispatchCommand) time.Time {
	if message, ok := lastInboundTextMessage(command); ok {
		if occurred := unixMillis(message.OccurredAt); !occurred.IsZero() {
			return occurred
		}
	}
	return time.Now().UTC()
}

func unixMillis(raw int64) time.Time {
	if raw <= 0 {
		return time.Time{}
	}
	if raw < 1e12 {
		return time.Unix(raw, 0).UTC().Truncate(time.Second)
	}
	return time.UnixMilli(raw).UTC().Truncate(time.Second)
}
