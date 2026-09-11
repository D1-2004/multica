package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

func (c DispatchCommand) validateMessageStatistics() error {
	e := c.Event.Data.MessageStatistics
	if c.Source.Type != "digital_employee" || c.ExternalIdentity.DWS == nil || c.Control != nil || c.Continuation != nil || c.Outbound.Mode != "none" {
		return errors.New("message.statistics requires a digital employee with DWS identity and no action")
	}
	if e == nil || e.EventID == "" || len(e.EventID) > 256 || e.SourceID == "" || len(e.SourceID) > 256 || e.ReceivedAt.IsZero() || e.OccurredAt.IsZero() || e.ReceivedAt.After(time.Now().Add(time.Minute)) {
		return errors.New("invalid message.statistics metadata")
	}
	conversation := c.Event.Data.Conversation
	if conversation.OpenConversationID == "" || len(conversation.OpenConversationID) > 1024 || (conversation.Type != "group" && conversation.Type != "single") {
		return errors.New("message.statistics requires a conversation")
	}
	return nil
}

func (h *Handler) handleMessageStatistics(w http.ResponseWriter, r *http.Request, c DispatchCommand, dc agentDispatchContext) bool {
	retired := c.Event.Domain == "channel" && c.Event.Type == dispatchEventTypeConversationSummary
	if !retired && !(c.Event.Domain == "channel" && c.Event.Type == "message.statistics") {
		return false
	}
	if c.AgentID != "" && c.AgentID != uuidToString(dc.AgentID) {
		writeError(w, http.StatusForbidden, "agent does not match dispatch endpoint")
		return true
	}
	admitted := 0
	if !retired && !dispatchIsAgentSelfMessage(c) {
		if h.MessageAutomations == nil {
			writeError(w, http.StatusServiceUnavailable, "message automations unavailable")
			return true
		}
		event := *c.Event.Data.MessageStatistics
		event.ConversationID = c.Event.Data.Conversation.OpenConversationID
		event.ConversationTitle = c.Event.Data.Conversation.Title
		event.ConversationType = c.Event.Data.Conversation.Type
		// Stable UID/org only; OAuth/context tokens never enter collection data.
		runtimeContext, err := json.Marshal(map[string]any{"external_identity": map[string]any{"dws": c.ExternalIdentity.DWS}})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invalid account identity")
			return true
		}
		admitted, err = h.MessageAutomations.Admit(r.Context(), dc.WorkspaceID, dc.AgentID, event, runtimeContext)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "failed to persist message statistics")
			return true
		}
	}
	if c.CompletionCallback != nil {
		if h.TaskService == nil {
			writeError(w, http.StatusServiceUnavailable, "receipt service unavailable")
			return true
		}
		if err := h.TaskService.EnqueueSynchronousSilence(r.Context(), c.CompletionCallback.URL, c.CompletionCallback.Target, dc.AgentID); err != nil {
			writeError(w, http.StatusServiceUnavailable, "failed to persist statistics receipt")
			return true
		}
	}
	code := "message_statistics_accepted"
	if retired {
		code = "hourly_summary_retired"
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "code": code, "automations": admitted})
	return true
}
