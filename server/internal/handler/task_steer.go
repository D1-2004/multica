package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
)

// SteerIssue gives human clients the same atomic continuation primitive as
// routed IM corrections. The Issue locator is already the execution target.
func (h *Handler) SteerIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		writeError(w, http.StatusConflict, "issue is not assigned to an agent")
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), issue.AssigneeID)
	if err != nil || !h.canInvokeAgent(r.Context(), agent, "member", userID, userID, uuidToString(issue.WorkspaceID)) {
		writeError(w, http.StatusForbidden, "cannot invoke issue agent")
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err = json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid correction")
		return
	}
	req.Content = strings.TrimSpace(sanitizeNullBytes(req.Content))
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if req.Content == "" || key == "" || len(key) > 256 {
		writeError(w, http.StatusBadRequest, "content and Idempotency-Key (at most 256 bytes) are required")
		return
	}
	result, err := h.IssueCommentService.CreateExternalFollowUp(r.Context(), service.IssueCommentCreateParams{Issue: issue, AuthorID: parseUUID(userID), Content: req.Content, QueueMode: "steer", IdempotencyKey: key}, service.IssueCommentCreateOpts{})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if result.PreemptedTask != nil {
		h.reconcileCommentsOnCompletion(r.Context(), result.PreemptedTask)
	}
	control := &AgentDispatchControlResult{Action: "steer", Status: result.Task.Status, TargetExternalTaskID: uuidToString(result.Task.ID)}
	if result.PreemptedTask != nil {
		control.PreemptedExternalTaskID = uuidToString(result.PreemptedTask.ID)
	}
	writeJSON(w, http.StatusAccepted, AgentDispatchResponse{Continuation: AgentDispatchContinuation{Kind: "issue", IssueID: uuidToString(issue.ID)}, CommentID: uuidToString(result.Comment.ID), TaskID: uuidToString(result.Task.ID), ControlResult: control})
}
