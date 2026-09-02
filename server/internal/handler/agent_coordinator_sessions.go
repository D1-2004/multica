package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ListAgentCoordinatorSessions returns inbound short-loop transcripts for one
// agent. Anyone who can view the agent can read this list; Chat inbox does not
// include these sessions.
func (h *Handler) ListAgentCoordinatorSessions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	workspaceID := uuidToString(agent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}

	rows, err := h.Queries.ListCoordinatorChatSessionsByAgent(r.Context(), db.ListCoordinatorChatSessionsByAgentParams{
		WorkspaceID: agent.WorkspaceID,
		AgentID:     agent.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list coordinator sessions")
		return
	}

	resp := make([]ChatSessionResponse, 0, len(rows))
	for _, s := range rows {
		resp = append(resp, ChatSessionResponse{
			ID:            uuidToString(s.ID),
			WorkspaceID:   uuidToString(s.WorkspaceID),
			AgentID:       uuidToString(s.AgentID),
			CreatorID:     uuidToString(s.CreatorID),
			ProjectID:     uuidToPtr(s.ProjectID),
			Title:         s.Title,
			Status:        s.Status,
			HasUnread:     false,
			UnreadCount:   0,
			LastMessage:   buildChatLastMessage(s.LastMessageAt, s.LastMessageContent, s.LastMessageRole, s.LastMessageFailureReason, s.LastMessageKind),
			IsA2A:         s.IsA2a,
			IsCoordinator: true,
			Pinned:        s.PinnedAt.Valid,
			CreatedAt:     timestampToString(s.CreatedAt),
			UpdatedAt:     timestampToString(s.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
