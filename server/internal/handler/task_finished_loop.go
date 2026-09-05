package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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
	if strings.TrimSpace(task.Status) != "completed" {
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
	since := task.CreatedAt.Time
	if task.StartedAt.Valid {
		since = task.StartedAt.Time
	}
	if since.IsZero() {
		since = time.Now().Add(-2 * time.Hour)
	}
	events, err := h.Assoc.ListEventsByScene(ctx, uuidToString(agent.WorkspaceID), uuidToString(task.AgentID), cid, since, 20)
	if err != nil {
		return false
	}
	return inboundcoord.TaskFinishedAlreadyToldScene(events, cid, uuidToString(task.ID))
}

func (h *Handler) deliverTaskFinishedDecision(ctx context.Context, task *db.AgentTaskQueue, decision inboundcoord.Decision) {
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
	if decision.Action != inboundcoord.ActionReply || text == "" || h.TaskService == nil {
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
