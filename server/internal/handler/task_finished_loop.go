package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const taskFinishedLoopTimeout = 55 * time.Second

func (h *Handler) maybeRunTaskFinishedLoop(ctx context.Context, task *db.AgentTaskQueue) {
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
		}
	}()
	if h == nil || task == nil || !task.AgentID.Valid {
		return
	}
	status := strings.TrimSpace(task.Status)
	if status != "completed" && status != "failed" && status != "cancelled" {
		return
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
	if err != nil || !on {
		return
	}
	if !coordinatorIssueFollowUp(task.Context) {
		return
	}
	if status != "completed" {
		h.deliverTaskFinishedDecision(ctx, task, inboundcoord.Decision{Action: inboundcoord.ActionSilence, Reason: "task_" + status})
		return
	}
	envelope, ok := parseTaskFinishedEnvelope(task.Context)
	if !ok {
		return
	}
	cid := strings.TrimSpace(envelope.EventData.Conversation.OpenConversationID)
	if cid == "" {
		return
	}
	agent, err := h.Queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		slog.Warn("task finished loop: load agent failed", "agent_id", uuidToString(task.AgentID), "error", err)
		return
	}
	result := clipTaskCompleteOutput(task.Result, 800)
	turn := inboundcoord.Turn{
		Loop:                 inboundcoord.LoopTaskFinished,
		Source:               inboundcoord.SourceDigitalEmployee,
		Addressed:            true,
		ChatType:             coordinatorChatType(envelope.EventData.Conversation.Type),
		ConversationTitle:    strings.TrimSpace(envelope.EventData.Conversation.Title),
		SenderName:           strings.TrimSpace(envelope.EventData.Sender.DisplayName),
		Message:              "任务已完成，请向委托人汇报。",
		AgentID:              agent.ID,
		AgentName:            agent.Name,
		Instructions:         agent.Instructions,
		WorkspaceID:          uuidToString(agent.WorkspaceID),
		ConversationID:       cid,
		IssueID:              uuidToString(task.IssueID),
		TaskResult:           result,
		IssueDispatchContext: task.Context,
		AlreadyToldScene:     h.taskFinishedSceneAlreadyTold(ctx, task, agent, cid),
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
	h.deliverTaskFinishedDecision(ctx, task, decision)
}

func (h *Handler) taskFinishedSceneAlreadyTold(ctx context.Context, task *db.AgentTaskQueue, agent db.Agent, cid string) bool {
	if h == nil || h.Assoc == nil || task == nil {
		return false
	}
	var issueCreated time.Time
	if task.IssueID.Valid && h.Queries != nil {
		issue, err := h.Queries.GetIssue(ctx, task.IssueID)
		if err == nil && issue.CreatedAt.Valid {
			issueCreated = issue.CreatedAt.Time
		}
	}
	since := inboundcoord.WrapupAlreadyToldSince(task.CreatedAt.Time, issueCreated)
	events, err := h.Assoc.ListEventsByScene(ctx, uuidToString(agent.WorkspaceID), uuidToString(task.AgentID), cid, since, 20)
	if err != nil {
		return false
	}
	return inboundcoord.TaskFinishedAlreadyToldScene(events, cid, uuidToString(task.ID))
}

func (h *Handler) deliverTaskFinishedDecision(ctx context.Context, task *db.AgentTaskQueue, decision inboundcoord.Decision) {
	decision.UserText = stripReplyDecisionLeak(decision.UserText)
	if decision.Action == inboundcoord.ActionReply && decision.UserText == "" {
		decision.Action = inboundcoord.ActionSilence
	}
	filtered := inboundcoord.FilterTaskFinishedWrapup(decision)
	if filtered.Action != decision.Action || filtered.UserText != decision.UserText {
		slog.Info("task finished loop skipped wrap-up delivery",
			"event", "task_finished_loop_skip_delivery",
			"task_id", uuidToString(task.ID),
			"reason", "redundant_wrapup",
		)
	}
	decision = filtered
	text := strings.TrimSpace(decision.UserText)
	if h.TaskService == nil {
		return
	}
	if decision.Action != inboundcoord.ActionReply || text == "" {
		callbackURL, target, ok := inboundcoord.WrapupCallback(task.Context)
		if !ok {
			return
		}
		if err := h.TaskService.EnqueueSynchronousSilence(ctx, callbackURL, target, task.AgentID); err != nil {
			slog.Warn("task finished loop: enqueue wrap-up silence failed",
				"event", "task_finished_loop_delivery_failed",
				"task_id", uuidToString(task.ID),
				"error", err,
			)
		}
		return
	}
	callbackURL, target, ok := inboundcoord.WrapupCallback(task.Context)
	if !ok {
		slog.Info("task finished loop skipped wrap-up delivery",
			"event", "task_finished_loop_skip_delivery",
			"task_id", uuidToString(task.ID),
			"reason", "no_wrapup_callback",
		)
		return
	}
	if err := h.TaskService.EnqueueSynchronousWrapup(
		ctx, callbackURL, target, task.AgentID, text, uuidToString(task.ID),
	); err != nil {
		slog.Warn("task finished loop: enqueue wrap-up failed",
			"event", "task_finished_loop_delivery_failed",
			"task_id", uuidToString(task.ID),
			"error", err,
		)
	}
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

func coordinatorChatType(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "group") {
		return "group"
	}
	return "p2p"
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
	h.maybeRunTaskFinishedLoop(ctx, &task)
	return nil
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
