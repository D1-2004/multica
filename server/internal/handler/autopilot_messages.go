package handler

import (
	"net/http"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) requireMessageAutomationAssignee(w http.ResponseWriter, r *http.Request, ap db.Autopilot) bool {
	if ap.AssigneeType != "agent" {
		writeError(w, http.StatusBadRequest, "DingTalk message triggers require a bound Agent executor")
		return false
	}
	if h.MessageAutomations == nil {
		writeError(w, http.StatusServiceUnavailable, "message automations unavailable")
		return false
	}
	bound, err := h.MessageAutomations.HasBinding(r.Context(), ap.WorkspaceID, ap.AssigneeID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "failed to check DingTalk account binding")
		return false
	}
	if !bound {
		writeError(w, http.StatusBadRequest, "DingTalk message triggers require an active digital employee account binding")
		return false
	}
	return true
}
