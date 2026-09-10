package handler

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type AgentEventBatchResponse struct {
	ID          string    `json:"id"`
	ResourceKey string    `json:"resource_key"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	RunID       string    `json:"run_id,omitempty"`
	TaskID      string    `json:"task_id,omitempty"`
	EventCount  int       `json:"event_count"`
	CreatedAt   time.Time `json:"created_at"`
	LastError   string    `json:"last_error,omitempty"`
}

func (h *Handler) ListAgentEventBatches(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if h.EventTriggers == nil {
		writeError(w, http.StatusServiceUnavailable, "event triggers are unavailable")
		return
	}
	rows, err := h.EventTriggers.Pool.Query(r.Context(), `SELECT b.id,st.resource_key,b.status,b.attempts,b.run_id,b.task_id,b.created_at,COALESCE(b.last_error,''),(SELECT count(*) FROM agent_event e WHERE e.batch_id=b.id)
		FROM agent_event_batch b JOIN agent_event_stream st ON st.id=b.stream_id WHERE st.agent_id=$1 AND st.workspace_id=$2 ORDER BY b.created_at DESC LIMIT 50`, agent.ID, agent.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list event batches")
		return
	}
	defer rows.Close()
	result := []AgentEventBatchResponse{}
	for rows.Next() {
		var item AgentEventBatchResponse
		var id, runID, taskID pgtype.UUID
		if err = rows.Scan(&id, &item.ResourceKey, &item.Status, &item.Attempts, &runID, &taskID, &item.CreatedAt, &item.LastError, &item.EventCount); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read event batch")
			return
		}
		item.ID = uuidToString(id)
		item.RunID = uuidToString(runID)
		item.TaskID = uuidToString(taskID)
		result = append(result, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "failed to read event batches")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) RetryAgentEventBatch(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageAgent(w, r, agent) {
		return
	}
	batchID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "batchId"), "batchId")
	if !ok {
		return
	}
	if h.EventTriggers == nil {
		writeError(w, http.StatusServiceUnavailable, "event triggers are unavailable")
		return
	}
	tx, err := h.EventTriggers.Pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "event retry unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	var streamID pgtype.UUID
	err = tx.QueryRow(r.Context(), `SELECT st.id FROM agent_event_stream st JOIN agent_event_trigger c ON c.agent_id=st.agent_id AND c.workspace_id=st.workspace_id
		JOIN agent_event_batch b ON b.id=st.active_batch_id AND b.stream_id=st.id
		WHERE st.agent_id=$1 AND st.workspace_id=$2 AND b.id=$3 AND b.status='failed' AND c.enabled FOR UPDATE OF st`, agent.ID, agent.WorkspaceID, batchID).Scan(&streamID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "enable event triggers and choose a failed batch to retry")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read batch for retry")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE agent_event_batch SET status='pending',attempts=0,last_error=NULL WHERE id=$1 AND stream_id=$2`, batchID, streamID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retry batch")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE agent_event_stream SET due_at=GREATEST(now(),last_dispatch_at+interval '30 seconds') WHERE id=$1`, streamID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to schedule batch retry")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit batch retry")
		return
	}
	h.EventTriggers.Notify()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending", "batch_id": uuidToString(batchID)})
}

// Observed group messages enter the ordinary durable Coordinator admission.
// The internal proactive flag is derived from server configuration, never from wire input.
func (h *Handler) handleObservedEvent(w http.ResponseWriter, r *http.Request, command *DispatchCommand, dc agentDispatchContext) bool {
	c := *command
	if c.Source.Type != "digital_employee" || c.Event.Domain != "channel" {
		return false
	}
	observed := c.Event.Type == "message.observed"
	legacyGroup := (c.Event.Type == "message.created" && c.Event.Data.Conversation.Type == "group") || c.Event.Type == dispatchEventTypeConversationSummary
	if !observed && !legacyGroup {
		return false
	}
	if h.EventTriggers == nil {
		if observed {
			writeError(w, http.StatusServiceUnavailable, "proactive conversations are unavailable")
			return true
		}
		return false
	}
	if c.AgentID != "" && c.AgentID != uuidToString(dc.AgentID) {
		writeError(w, http.StatusForbidden, "agent does not match dispatch endpoint")
		return true
	}
	enabled, err := h.EventTriggers.Enabled(r.Context(), dc.AgentID, dc.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "proactive conversation configuration is unavailable")
		return true
	}
	if !observed && !enabled {
		return false
	}
	if enabled && c.Event.Type != dispatchEventTypeConversationSummary && !dispatchIsAgentSelfMessage(c) {
		agent, ok := h.resolveAgentDispatchAgent(w, r, dc.UserID, dc.WorkspaceID, dc.AgentID)
		if !ok {
			return true
		}
		if agent.ArchivedAt.Valid {
			writeError(w, http.StatusConflict, "agent is archived")
			return true
		}
		if c.ExternalIdentity.DWS == nil {
			writeError(w, http.StatusBadRequest, "observed messages require DWS identity")
			return true
		}
		if h.InboundCoordinatorWorker == nil {
			writeError(w, http.StatusServiceUnavailable, "inbound coordinator is unavailable")
			return true
		}
		command.Event.Type = "message.created"
		command.ProactiveConversation = true
		command.Continuation = nil
		command.AgentID = uuidToString(dc.AgentID)
		return false
	}
	// Receipt only: disabled, own messages, and superseded hourly summaries perform no work.
	if c.CompletionCallback != nil {
		if h.TaskService == nil {
			writeError(w, http.StatusServiceUnavailable, "event receipt service is unavailable")
			return true
		}
		if err = h.TaskService.EnqueueSynchronousSilence(r.Context(), c.CompletionCallback.URL, c.CompletionCallback.Target, dc.AgentID); err != nil {
			writeError(w, http.StatusServiceUnavailable, "failed to persist event receipt")
			return true
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "code": "observed_message_ignored"})
	return true
}
