package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type CoordinatorConversationResponse struct {
	ID               string `json:"id"`
	SessionID        string `json:"session_id"`
	Title            string `json:"title"`
	ConversationType string `json:"conversation_type"`
	Source           string `json:"source"`
	SessionCount     int64  `json:"session_count"`
	UpdatedAt        string `json:"updated_at"`
}

type CoordinatorConversationsPageResponse struct {
	Conversations []CoordinatorConversationResponse `json:"conversations"`
	HasMore       bool                              `json:"has_more"`
	NextOffset    int                               `json:"next_offset"`
}

func (h *Handler) loadCoordinatorAgent(w http.ResponseWriter, r *http.Request) (db.Agent, bool) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return agent, false
	}
	workspaceID := uuidToString(agent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return agent, false
	}
	return agent, true
}

// ListAgentCoordinatorConversations projects per-judgment sessions into conversations.
func (h *Handler) ListAgentCoordinatorConversations(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadCoordinatorAgent(w, r)
	if !ok {
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 2147483622 {
			writeError(w, http.StatusBadRequest, "invalid offset")
			return
		}
		offset = parsed
	}
	const limit = 25
	rows, err := h.Queries.ListCoordinatorConversations(r.Context(), db.ListCoordinatorConversationsParams{
		WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, PageLimit: limit + 1, PageOffset: int32(offset),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list coordinator conversations")
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	resp := CoordinatorConversationsPageResponse{Conversations: make([]CoordinatorConversationResponse, 0, len(rows)), HasMore: hasMore, NextOffset: offset + len(rows)}
	for _, row := range rows {
		title := strings.TrimSpace(row.ConversationTitle)
		if title == "" && row.ConversationType != "group" {
			title = strings.TrimSpace(row.SenderName)
		}
		if title == "" {
			title = row.Title
		}
		resp.Conversations = append(resp.Conversations, CoordinatorConversationResponse{
			ID: row.ID, SessionID: uuidToString(row.SessionID), Title: title,
			ConversationType: row.ConversationType, Source: row.Source,
			SessionCount: row.SessionCount, UpdatedAt: timestampToString(row.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// ListAgentCoordinatorConversationMessages reads a bounded chronological window across all sessions in a conversation.
func (h *Handler) ListAgentCoordinatorConversationMessages(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadCoordinatorAgent(w, r)
	if !ok {
		return
	}
	sessionID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "sessionId"), "sessionId")
	if !ok {
		return
	}
	session, err := h.Queries.GetChatSession(r.Context(), sessionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load coordinator conversation")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) || session.WorkspaceID != agent.WorkspaceID || session.AgentID != agent.ID {
		writeError(w, http.StatusNotFound, "coordinator conversation not found")
		return
	}
	isCoordinator, err := h.Queries.IsCoordinatorChatSession(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve coordinator conversation")
		return
	}
	if !isCoordinator {
		writeError(w, http.StatusNotFound, "coordinator conversation not found")
		return
	}
	limit, beforeCreatedAt, beforeID, err := parseChatMessagesPageParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	messages, err := h.Queries.ListCoordinatorConversationMessages(r.Context(), db.ListCoordinatorConversationMessagesParams{
		WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, SessionID: session.ID,
		BeforeCreatedAt: beforeCreatedAt, BeforeID: beforeID, PageLimit: int32(limit + 1),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list coordinator messages")
		return
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	var nextCursor *ChatMessagesCursorResponse
	if hasMore {
		oldest := messages[len(messages)-1]
		nextCursor = &ChatMessagesCursorResponse{CreatedAt: oldest.CreatedAt.Time.Format(time.RFC3339Nano), ID: uuidToString(oldest.ID)}
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	ids := make([]pgtype.UUID, len(messages))
	for i, m := range messages {
		ids[i] = m.ID
	}
	attachments := h.groupChatMessageAttachments(r.Context(), uuidToString(agent.WorkspaceID), ids)
	resp := make([]ChatMessageResponse, len(messages))
	for i, m := range messages {
		resp[i] = chatMessageToResponse(m, attachments[uuidToString(m.ID)])
	}
	writeJSON(w, http.StatusOK, ChatMessagesPageResponse{Messages: resp, Limit: limit, HasMore: hasMore, NextCursor: nextCursor})
}
