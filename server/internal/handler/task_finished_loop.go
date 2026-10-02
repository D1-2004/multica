package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const taskFinishedLoopTimeout = 55 * time.Second

var errTaskFinishedResponsePending = errors.New("task-finished response receipt is unresolved")

func (h *Handler) maybeRunTaskFinishedLoop(ctx context.Context, task *db.AgentTaskQueue) (loopErr error) {
	defer func() {
		if rec := recover(); rec != nil {
			taskID := ""
			if task != nil {
				taskID = uuidToString(task.ID)
			}
			slog.Error("task finished loop panicked",
				"event", "task_finished_loop_panicked",
				"task_id", taskID,
				"panic", rec,
			)
			loopErr = fmt.Errorf("task finished loop panicked: %v", rec)
		}
		if loopErr != nil && task != nil {
			slog.Warn("task finished loop failed", "event", "task_finished_loop_failed", "task_id", uuidToString(task.ID), "error", loopErr)
		}
	}()
	if h == nil || task == nil || !task.AgentID.Valid {
		return nil
	}
	status := strings.TrimSpace(task.Status)
	if status != "completed" && status != "failed" && status != "cancelled" {
		return nil
	}
	// Completing a sandbox task frees a scene slot. Wake parked inbound
	// windows even when the wrap-up switch is off; otherwise they wait
	// on the 5s claim ticker.
	if h.InboundCoordinatorWorker != nil {
		h.InboundCoordinatorWorker.Notify()
	}
	ctx, cancel := context.WithTimeout(ctx, taskFinishedLoopTimeout)
	defer cancel()
	on, err := h.Queries.GetAgentTaskFinishedLoop(ctx, task.AgentID)
	if err != nil {
		return fmt.Errorf("read task finished setting: %w", err)
	}
	if !on {
		return nil
	}
	if !coordinatorIssueFollowUp(task.Context) {
		return nil
	}
	if status == "cancelled" {
		return h.deliverTaskFinishedDecision(ctx, task, inboundcoord.Decision{Action: inboundcoord.ActionSilence, Reason: "task_" + status})
	}
	envelope, ok := parseTaskFinishedEnvelope(task.Context)
	if !ok {
		return fmt.Errorf("task finished dispatch context is invalid")
	}
	cid := strings.TrimSpace(envelope.EventData.Conversation.OpenConversationID)
	if cid == "" {
		return fmt.Errorf("task finished conversation is missing")
	}
	agent, err := h.Queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		slog.Warn("task finished loop: load agent failed", "agent_id", uuidToString(task.AgentID), "error", err)
		return fmt.Errorf("load task finished agent: %w", err)
	}
	if status == "failed" && task.IssueID.Valid {
		active, lookupErr := h.Queries.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{IssueID: task.IssueID, AgentID: task.AgentID})
		if lookupErr != nil {
			slog.Warn("task finished retry state unavailable", "task_id", uuidToString(task.ID))
			return fmt.Errorf("read task finished retry state: %w", lookupErr)
		}
		if active {
			return h.deliverTaskFinishedDecision(ctx, task, inboundcoord.Decision{Action: inboundcoord.ActionSilence, Reason: "retry_still_active"})
		}
	}
	fullResult := clipTaskCompleteOutput(task.Result, 0)
	if status == "failed" {
		fullResult = "本次执行已失败，未确认业务完成。"
	}

	managedState, managed, stateErr := h.taskFinishedManagedResponseState(ctx, task, agent, cid)
	if stateErr != nil {
		return stateErr
	}
	if managed && (managedState == "pending" || managedState == "provider_accepted" || managedState == "unknown") {
		// Preserve the response ledger's unresolved-delivery park. Neither an
		// optimistic tool result nor a loop failure authorizes another send.
		if task.CreatedAt.Valid && time.Since(task.CreatedAt.Time) > 24*time.Hour {
			return h.deliverTaskFinishedDecision(ctx, task, inboundcoord.Decision{Action: inboundcoord.ActionSilence, Reason: "delivery_unresolved"})
		}
		return errTaskFinishedResponsePending
	}
	delivery := newTaskDeliveryContext(task)
	alreadyTold := false
	if managed {
		// Managed response actions register before sending; only their verified
		// server ledger is authoritative. Do not fall back to CLI stdout or
		// issue-wide Assoc events when that ledger has no delivered response.
		delivery.Status = "loaded"
		delivery.Coverage = "managed_response_ledger_current_task_current_scene"
		alreadyTold = managedState == "delivered" && status == "completed"
	} else {
		delivery = h.taskFinishedDeliveryContext(ctx, task)
		alreadyTold = inboundcoord.TaskFinishedResultAlreadyDelivered(fullResult, cid, delivery)
	}
	var outstanding string
	if h.IssueCommentService != nil && task.IssueID.Valid {
		var pendingErr error
		outstanding, pendingErr = h.IssueCommentService.OutstandingCoordinatorFollowUps(ctx, *task, cid)
		if pendingErr != nil {
			return fmt.Errorf("load outstanding issue follow-ups: %w", pendingErr)
		}
	}
	deliveryJSON, _ := json.Marshal(delivery)
	turn := inboundcoord.Turn{
		Loop:                 inboundcoord.LoopTaskFinished,
		Source:               inboundcoord.SourceDigitalEmployee,
		Addressed:            true,
		ChatType:             coordinatorChatType(envelope.EventData.Conversation.Type),
		ConversationTitle:    strings.TrimSpace(envelope.EventData.Conversation.Title),
		SenderName:           strings.TrimSpace(envelope.EventData.Sender.DisplayName),
		Message:              "任务结束事件，状态=" + status + "。判断当前会话是否还缺一条有用的结果或失败说明。",
		AgentID:              agent.ID,
		AgentName:            agent.Name,
		Instructions:         agent.Instructions,
		WorkspaceID:          uuidToString(agent.WorkspaceID),
		SceneID:              sceneRefID(h.envelopeSceneRef(ctx, envelope, agent.WorkspaceID, task.AgentID)),
		ConversationID:       cid,
		IssueID:              uuidToString(task.IssueID),
		TaskResult:           fullResult,
		OutstandingFollowUps: outstanding,
		TaskDeliveryContext:  string(deliveryJSON),
		IssueDispatchContext: task.Context,
		AlreadyToldScene:     alreadyTold,
	}
	if envelope.ExternalIdentity != nil && envelope.ExternalIdentity.DWS != nil {
		turn.DWSUID = strings.TrimSpace(envelope.ExternalIdentity.DWS.UID)
		turn.DWSOrgID = strings.TrimSpace(envelope.ExternalIdentity.DWS.OrgID)
	}
	coord := h.inboundCoordinator()
	coord.FillVoice(ctx, &turn)
	decision := coord.Decide(ctx, turn)
	slog.Info("task finished loop decided",
		"event", "task_finished_loop_decided",
		"agent_id", uuidToString(task.AgentID),
		"issue_id", uuidToString(task.IssueID),
		"task_id", uuidToString(task.ID),
		"action", string(decision.Action),
		"reason", decision.Reason,
		"text", clipRunes(decision.UserText, 80),
	)
	if decision.Action == inboundcoord.ActionDeferred {
		return fmt.Errorf("task finished decision is deferred: %s", decision.Reason)
	}
	return h.deliverTaskFinishedDecision(ctx, task, decision)
}

// taskFinishedManagedResponseState keeps response-actions delivery evidence
// independent of the legacy tool transcript. Unknown availability is retriable,
// never proof that a result has been delivered.
func (h *Handler) taskFinishedManagedResponseState(ctx context.Context, task *db.AgentTaskQueue, agent db.Agent, cid string) (string, bool, error) {
	stored, present := parsePersistedDispatchContext(task.Context)
	if !present || !stored.ResponsePolicy.Managed() {
		return "", false, nil
	}
	if h.DingTalkResponses == nil {
		return "", true, fmt.Errorf("%w: response service unavailable", errTaskFinishedResponsePending)
	}
	state, err := h.DingTalkResponses.SandboxResponseState(ctx, uuidToString(agent.WorkspaceID), uuidToString(task.AgentID), uuidToString(task.IssueID), uuidToString(task.ID), cid)
	if err != nil {
		return "", true, fmt.Errorf("%w: %v", errTaskFinishedResponsePending, err)
	}
	return state, true, nil
}

// taskFinishedSceneAlreadyTold is a read-only delivery predicate. Legacy tasks
// require this run's exact result receipt; managed tasks require verified
// response-actions delivery. Pending and unavailable are never delivered.
func (h *Handler) taskFinishedSceneAlreadyTold(ctx context.Context, task *db.AgentTaskQueue, agent db.Agent, cid string) bool {
	if h == nil || task == nil {
		return false
	}
	state, managed, err := h.taskFinishedManagedResponseState(ctx, task, agent, cid)
	if managed {
		return err == nil && state == "delivered"
	}
	return inboundcoord.TaskFinishedResultAlreadyDelivered(clipTaskCompleteOutput(task.Result, 0), cid, h.taskFinishedDeliveryContext(ctx, task))
}

func (h *Handler) deliverTaskFinishedDecision(ctx context.Context, task *db.AgentTaskQueue, decision inboundcoord.Decision) (deliveryErr error) {
	if task == nil {
		return fmt.Errorf("task finished delivery task is missing")
	}
	defer func() {
		if deliveryErr != nil {
			slog.Warn("task finished loop: delivery failed", "event", "task_finished_loop_delivery_failed", "task_id", uuidToString(task.ID), "error", deliveryErr)
		}
	}()
	if decision.Action != inboundcoord.ActionReply && decision.Action != inboundcoord.ActionSilence {
		return fmt.Errorf("task finished decision is not terminal: %s", decision.Action)
	}
	decision.UserText = stripReplyDecisionLeak(decision.UserText)
	decision = inboundcoord.FilterTaskFinishedWrapup(decision)
	text := strings.TrimSpace(decision.UserText)
	callbackURL, target, ok := inboundcoord.WrapupCallback(task.Context)
	if !ok {
		if decision.Action == inboundcoord.ActionReply {
			return fmt.Errorf("required task finished reply has no callback target")
		}
		return nil
	}
	if h == nil || h.TaskService == nil {
		return fmt.Errorf("task finished delivery service unavailable")
	}
	if decision.Action == inboundcoord.ActionSilence {
		if err := h.TaskService.EnqueueSynchronousSilence(ctx, callbackURL, target, task.AgentID); err != nil {
			return fmt.Errorf("enqueue task finished silence: %w", err)
		}
		return nil
	}
	if err := h.TaskService.EnqueueSynchronousWrapup(ctx, callbackURL, target, task.AgentID, text, uuidToString(task.ID)); err != nil {
		return fmt.Errorf("enqueue task finished reply: %w", err)
	}
	return nil
}

func coordinatorIssueFollowUp(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil {
		return false
	}
	v := strings.TrimSpace(string(payload["coordinator_issue_follow_up"]))
	return v == "true"
}

func parseTaskFinishedEnvelope(raw []byte) (persistedDispatchContext, bool) {
	var envelope persistedDispatchContext
	if len(raw) == 0 || json.Unmarshal(raw, &envelope) != nil {
		return envelope, false
	}
	return envelope, true
}

// envelopeSceneRef is the task's SceneRef from its dispatch context, through
// the use-time fence (fenceSceneRef); nil when the task has no scene or the
// agent no longer serves the scene's org.
func (h *Handler) envelopeSceneRef(ctx context.Context, envelope persistedDispatchContext, workspaceID, agentID pgtype.UUID) *scene.Ref {
	dispatchOrg := ""
	if envelope.ExternalIdentity != nil && envelope.ExternalIdentity.DWS != nil {
		dispatchOrg = envelope.ExternalIdentity.DWS.OrgID
	}
	return h.fenceSceneRef(ctx, envelope.AgentScene, scene.Owner{WorkspaceID: workspaceID, AgentID: agentID}, dispatchOrg)
}

// sceneRefID is a SceneRef's scene_id, "" for nil.
func sceneRefID(ref *scene.Ref) string {
	if ref == nil {
		return ""
	}
	return strings.TrimSpace(ref.SceneID)
}

// coordinatorChatType maps a dispatch conversation type onto the
// Coordinator's chat type: "group", "p2p" for a 1:1 chat, and "unknown"
// for anything else, which is never assumed to be a 1:1 chat.
func coordinatorChatType(raw string) string {
	switch kind, known := scene.KindFromConversationType(raw); {
	case !known:
		return "unknown"
	case kind == scene.KindGroup:
		return "group"
	default:
		return "p2p"
	}
}

func clipTaskCompleteOutput(raw []byte, n int) string {
	if len(raw) == 0 {
		return ""
	}
	var payload struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(raw, &payload) == nil && strings.TrimSpace(payload.Output) != "" {
		return clipRunes(strings.TrimSpace(payload.Output), n)
	}
	return clipRunes(strings.TrimSpace(string(raw)), n)
}

func clipRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (h *Handler) runPersistedTaskFinishedLoop(ctx context.Context, taskID string) error {
	id, err := util.ParseUUID(strings.TrimSpace(taskID))
	if err != nil {
		return err
	}
	task, err := h.Queries.GetAgentTask(ctx, id)
	if err != nil {
		return err
	}
	return h.maybeRunTaskFinishedLoop(ctx, &task)
}

func (h *Handler) enqueueTaskFinishedLoop(ctx context.Context, task *db.AgentTaskQueue) error {
	if h == nil || task == nil || h.Queries == nil || h.TxStarter == nil {
		return nil
	}
	if h.InboundCoordinatorWorker != nil {
		h.InboundCoordinatorWorker.Notify()
	}
	if !coordinatorIssueFollowUp(task.Context) {
		return nil
	}
	agent, err := h.Queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		return err
	}
	key := "task-finished:" + uuidToString(task.ID)
	_, err = h.Queries.GetInboundCoordinatorJobByIdempotency(ctx, db.GetInboundCoordinatorJobByIdempotencyParams{
		WorkspaceID:    agent.WorkspaceID,
		AgentID:        task.AgentID,
		IdempotencyKey: key,
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	envelope, _ := parseTaskFinishedEnvelope(task.Context)
	cid := strings.TrimSpace(envelope.EventData.Conversation.OpenConversationID)
	command := DispatchCommand{
		TaskFinishedTaskID: uuidToString(task.ID),
		AgentScene:         h.envelopeSceneRef(ctx, envelope, agent.WorkspaceID, task.AgentID),
		Source:             DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{
			Domain: "channel",
			Type:   "message.created",
			Data: DispatchEventData{
				Conversation: DispatchConversation{
					OpenConversationID: cid,
					Type:               envelope.EventData.Conversation.Type,
					Title:              envelope.EventData.Conversation.Title,
				},
			},
		},
	}
	raw, err := json.Marshal(command)
	if err != nil {
		return err
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	session, err := qtx.CreateChatSession(ctx, db.CreateChatSessionParams{
		WorkspaceID: agent.WorkspaceID,
		AgentID:     task.AgentID,
		CreatorID:   agent.OwnerID,
		Title:       "task-finished " + uuidToString(task.ID),
	})
	if err != nil {
		return err
	}
	userMessage, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: session.ID,
		Role:          "user",
		Content:       "任务已完成，请向委托人汇报。",
		MessageKind:   pgtype.Text{String: protocol.ChatMessageKindMessage, Valid: true},
	})
	if err != nil {
		return err
	}
	var acceptanceID pgtype.UUID
	if scanErr := acceptanceID.Scan(uuid.New().String()); scanErr != nil {
		return scanErr
	}
	endpointID := ""
	var payload map[string]json.RawMessage
	if json.Unmarshal(task.Context, &payload) == nil {
		endpointID = strings.Trim(string(payload["dispatch_endpoint_id"]), `"`)
	}
	_, err = qtx.CreateInboundCoordinatorJob(ctx, db.CreateInboundCoordinatorJobParams{
		AcceptanceID:        acceptanceID,
		WorkspaceID:         agent.WorkspaceID,
		AgentID:             task.AgentID,
		UserID:              agent.OwnerID,
		EndpointNamespaceID: agent.WorkspaceID,
		DispatchEndpointID:  endpointID,
		IdempotencyKey:      key,
		Command:             raw,
		ChatSessionID:       session.ID,
		UserMessageID:       userMessage.ID,
		AvailableAt:         pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if h.InboundCoordinatorWorker != nil {
		h.InboundCoordinatorWorker.Notify()
	}
	return nil
}
