package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/logger"
)

type DingTalkUserResponse struct {
	UserID        string  `json:"user_id"`
	UnionID       string  `json:"union_id,omitempty"`
	Name          string  `json:"name"`
	AvatarURL     string  `json:"avatar_url,omitempty"`
	Mobile        string  `json:"mobile,omitempty"`
	Title         string  `json:"title,omitempty"`
	Email         string  `json:"email,omitempty"`
	DepartmentIDs []int64 `json:"department_ids,omitempty"`
}

type DingTalkUserSearchResponse struct {
	Users []DingTalkUserResponse `json:"users"`
}

type AddDingTalkGroupMembersRequest struct {
	ChatID  string   `json:"chat_id"`
	UserIDs []string `json:"user_ids"`
}

type AddDingTalkGroupMembersResponse struct {
	AddedUserIDs []string `json:"added_user_ids"`
}

func (h *Handler) requireDingTalkAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.DingTalk == nil || !h.DingTalk.IsConfigured() {
		writeError(w, http.StatusServiceUnavailable, "DingTalk integration is not configured")
		return false
	}
	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return false
	}
	if !roleAllowed(requester.Role, "owner", "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return false
	}
	return true
}

func (h *Handler) SearchDingTalkUsers(w http.ResponseWriter, r *http.Request) {
	if !h.requireDingTalkAdmin(w, r) {
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	limit := 10
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	if limit > 20 {
		limit = 20
	}

	users, err := h.DingTalk.SearchUsers(r.Context(), query, limit)
	if err != nil {
		slog.Warn("dingtalk user search failed", append(logger.RequestAttrs(r), "error", err, "query", query)...)
		writeError(w, http.StatusBadGateway, "failed to search DingTalk users")
		return
	}

	resp := DingTalkUserSearchResponse{Users: make([]DingTalkUserResponse, 0, len(users))}
	for _, user := range users {
		resp.Users = append(resp.Users, DingTalkUserResponse{
			UserID:        user.UserID,
			UnionID:       user.UnionID,
			Name:          user.Name,
			AvatarURL:     user.AvatarURL,
			Mobile:        user.Mobile,
			Title:         user.Title,
			Email:         user.Email,
			DepartmentIDs: user.Department,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) AddDingTalkGroupMembers(w http.ResponseWriter, r *http.Request) {
	if !h.requireDingTalkAdmin(w, r) {
		return
	}

	var req AddDingTalkGroupMembersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	chatID := strings.TrimSpace(req.ChatID)
	if chatID == "" {
		writeError(w, http.StatusBadRequest, "chat_id is required")
		return
	}
	userIDs := make([]string, 0, len(req.UserIDs))
	seen := make(map[string]struct{}, len(req.UserIDs))
	for _, userID := range req.UserIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		userIDs = append(userIDs, userID)
	}
	if len(userIDs) == 0 {
		writeError(w, http.StatusBadRequest, "user_ids is required")
		return
	}
	if len(userIDs) > 30 {
		writeError(w, http.StatusBadRequest, "at most 30 users can be added at once")
		return
	}

	if err := h.DingTalk.AddGroupMembers(r.Context(), chatID, userIDs); err != nil {
		slog.Warn("dingtalk add group members failed", append(logger.RequestAttrs(r), "error", err, "chat_id", chatID, "count", len(userIDs))...)
		writeError(w, http.StatusBadGateway, "failed to add DingTalk group members")
		return
	}

	writeJSON(w, http.StatusOK, AddDingTalkGroupMembersResponse{AddedUserIDs: userIDs})
}
