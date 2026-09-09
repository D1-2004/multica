package handler

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
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

// Event-mode requests stop before prompt planning, coordinator admission or
// issue/chat materialization. The callback confirms inbox delivery only.
func (h *Handler) handleObservedEvent(w http.ResponseWriter, r *http.Request, c DispatchCommand, dc agentDispatchContext) bool {
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
			writeError(w, http.StatusServiceUnavailable, "event triggers are unavailable")
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
		writeError(w, http.StatusServiceUnavailable, "event trigger configuration is unavailable")
		return true
	}
	if !observed && !enabled {
		return false
	}
	// A source can switch while a legacy dispatch is already queued. Admit its
	// message IDs into the same inbox; hourly summaries never create a second run.
	code := "event_trigger_disabled"
	if enabled && c.Event.Type != dispatchEventTypeConversationSummary {
		agent, ok := h.resolveAgentDispatchAgent(w, r, dc.UserID, dc.WorkspaceID, dc.AgentID)
		if !ok {
			return true
		}
		if agent.ArchivedAt.Valid {
			writeError(w, http.StatusConflict, "agent is archived")
			return true
		}
		if c.ExternalIdentity.DWS == nil {
			writeError(w, http.StatusBadRequest, "event requires DWS identity")
			return true
		}
		if dispatchIsAgentSelfMessage(c) {
			code = "self_event_ignored"
		} else {
			identity := c.ExternalIdentity.DWS
			runtimeContext, _ := json.Marshal(map[string]any{"workspace_id": uuidToString(dc.WorkspaceID), "external_identity": map[string]any{"dws": identity}})
			events := make([]service.ObservedEvent, 0, len(c.Event.Data.Messages))
			for _, m := range c.Event.Data.Messages {
				payload, marshalErr := json.Marshal(map[string]any{"type": "message.created", "conversation": c.Event.Data.Conversation, "sender": c.Event.Data.Sender, "message": m})
				if marshalErr != nil {
					writeError(w, http.StatusBadRequest, "invalid event payload")
					return true
				}
				events = append(events, service.ObservedEvent{ID: m.OpenMsgID, Payload: payload, RuntimeContext: runtimeContext})
			}
			_, err = h.EventTriggers.Admit(r.Context(), dc.WorkspaceID, dc.AgentID, strings.Join([]string{c.Source.Platform, identity.OrgID, identity.UID}, ":"), c.Event.Data.Conversation.OpenConversationID, events)
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "failed to persist event inbox")
				return true
			}
			code = "event_inbox_accepted"
		}
	} else if enabled {
		code = "event_summary_superseded"
	}
	if c.CompletionCallback != nil {
		if h.TaskService == nil {
			writeError(w, http.StatusServiceUnavailable, "event receipt service is unavailable")
			return true
		}
		if err = h.TaskService.EnqueueSynchronousTaskCompletion(r.Context(), c.CompletionCallback.URL, c.CompletionCallback.Target, dc.AgentID, "Event delivery acknowledged; agent execution is tracked separately by its batch.", code); err != nil {
			writeError(w, http.StatusServiceUnavailable, "failed to persist event delivery receipt")
			return true
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "code": code})
	return true
}
