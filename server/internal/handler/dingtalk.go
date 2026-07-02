package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	dingtalkintegration "github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
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

type AddDingTalkWorkspaceMembersRequest struct {
	Users []DingTalkUserResponse `json:"users"`
	Role  string                 `json:"role"`
}

type AddDingTalkWorkspaceMembersResponse struct {
	Members            []MemberWithUserResponse `json:"members"`
	AddedCount         int                      `json:"added_count"`
	AlreadyMemberCount int                      `json:"already_member_count"`
	UnresolvedUserIDs  []string                 `json:"unresolved_user_ids,omitempty"`
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
		resp.Users = append(resp.Users, dingTalkUserToResponse(user))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) AddDingTalkWorkspaceMembers(w http.ResponseWriter, r *http.Request) {
	if !h.requireDingTalkAdmin(w, r) {
		return
	}

	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	var req AddDingTalkWorkspaceMembersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	role, valid := normalizeMemberRole(req.Role)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid member role")
		return
	}
	if role == "owner" {
		writeError(w, http.StatusBadRequest, "cannot add DingTalk members as owner")
		return
	}

	users := uniqueDingTalkUsers(req.Users)
	if len(users) == 0 {
		writeError(w, http.StatusBadRequest, "users is required")
		return
	}
	if len(users) > 30 {
		writeError(w, http.StatusBadRequest, "at most 30 users can be added at once")
		return
	}

	resp := AddDingTalkWorkspaceMembersResponse{
		Members: make([]MemberWithUserResponse, 0, len(users)),
	}
	for _, selected := range users {
		resolved := h.resolveDingTalkSelectedUser(r.Context(), selected)
		email := dingTalkIdentityEmail(resolved)
		if email == "" {
			resp.UnresolvedUserIDs = append(resp.UnresolvedUserIDs, selected.UserID)
			continue
		}

		user, err := h.findOrCreateDingTalkWorkspaceUser(r.Context(), resolved, email)
		if err != nil {
			slog.Warn("create dingtalk workspace user failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID, "dingtalk_user_id", selected.UserID)...)
			writeError(w, http.StatusInternalServerError, "failed to add DingTalk members")
			return
		}

		member, err := h.Queries.CreateMember(r.Context(), db.CreateMemberParams{
			WorkspaceID: requester.WorkspaceID,
			UserID:      user.ID,
			Role:        role,
		})
		if err != nil {
			if !isUniqueViolation(err) {
				slog.Warn("create dingtalk member failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID, "dingtalk_user_id", selected.UserID)...)
				writeError(w, http.StatusInternalServerError, "failed to add DingTalk members")
				return
			}
			existing, existingErr := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
				UserID:      user.ID,
				WorkspaceID: requester.WorkspaceID,
			})
			if existingErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to load existing member")
				return
			}
			resp.AlreadyMemberCount++
			resp.Members = append(resp.Members, memberWithUserResponse(existing, user))
			continue
		}

		resp.AddedCount++
		resp.Members = append(resp.Members, memberWithUserResponse(member, user))
		slog.Info("dingtalk member added", append(logger.RequestAttrs(r), "member_id", uuidToString(member.ID), "workspace_id", workspaceID, "dingtalk_user_id", selected.UserID, "email", user.Email, "role", role)...)
		eventPayload := map[string]any{"member": memberWithUserResponse(member, user)}
		if ws, err := h.Queries.GetWorkspace(r.Context(), requester.WorkspaceID); err == nil {
			eventPayload["workspace_name"] = ws.Name
		}
		h.publish(protocol.EventMemberAdded, uuidToString(requester.WorkspaceID), "member", requestUserID(r), eventPayload)
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

func dingTalkUserToResponse(user dingtalkintegration.User) DingTalkUserResponse {
	return DingTalkUserResponse{
		UserID:        strings.TrimSpace(user.UserID),
		UnionID:       strings.TrimSpace(user.UnionID),
		Name:          strings.TrimSpace(user.Name),
		AvatarURL:     strings.TrimSpace(user.AvatarURL),
		Mobile:        strings.TrimSpace(user.Mobile),
		Title:         strings.TrimSpace(user.Title),
		Email:         strings.TrimSpace(user.Email),
		DepartmentIDs: user.Department,
	}
}

func uniqueDingTalkUsers(users []DingTalkUserResponse) []DingTalkUserResponse {
	seen := make(map[string]struct{}, len(users))
	unique := make([]DingTalkUserResponse, 0, len(users))
	for _, user := range users {
		normalized := normalizeDingTalkUserResponse(user)
		key := normalized.UserID
		if key == "" {
			key = normalized.UnionID
		}
		if key == "" {
			key = strings.ToLower(normalized.Email)
		}
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, normalized)
	}
	return unique
}

func normalizeDingTalkUserResponse(user DingTalkUserResponse) DingTalkUserResponse {
	user.UserID = strings.TrimSpace(user.UserID)
	user.UnionID = strings.TrimSpace(user.UnionID)
	user.Name = strings.TrimSpace(user.Name)
	user.AvatarURL = strings.TrimSpace(user.AvatarURL)
	user.Mobile = strings.TrimSpace(user.Mobile)
	user.Title = strings.TrimSpace(user.Title)
	user.Email = strings.TrimSpace(user.Email)
	if user.Name == "" {
		user.Name = user.UserID
	}
	return user
}

func (h *Handler) resolveDingTalkSelectedUser(ctx context.Context, selected DingTalkUserResponse) DingTalkUserResponse {
	selected = normalizeDingTalkUserResponse(selected)
	if dingTalkIdentityEmail(selected) != "" || selected.UserID == "" {
		return selected
	}

	users, err := h.DingTalk.SearchUsers(ctx, selected.UserID, 5)
	if err != nil {
		return selected
	}
	for _, user := range users {
		candidate := dingTalkUserToResponse(user)
		if candidate.UserID == selected.UserID {
			return mergeDingTalkUsers(selected, candidate)
		}
	}
	return selected
}

func mergeDingTalkUsers(base DingTalkUserResponse, detail DingTalkUserResponse) DingTalkUserResponse {
	if detail.UserID != "" {
		base.UserID = detail.UserID
	}
	if detail.UnionID != "" {
		base.UnionID = detail.UnionID
	}
	if detail.Name != "" {
		base.Name = detail.Name
	}
	if detail.AvatarURL != "" {
		base.AvatarURL = detail.AvatarURL
	}
	if detail.Mobile != "" {
		base.Mobile = detail.Mobile
	}
	if detail.Title != "" {
		base.Title = detail.Title
	}
	if detail.Email != "" {
		base.Email = detail.Email
	}
	if len(detail.DepartmentIDs) > 0 {
		base.DepartmentIDs = detail.DepartmentIDs
	}
	return base
}

func dingTalkIdentityEmail(user DingTalkUserResponse) string {
	unionID := strings.TrimSpace(user.UnionID)
	if unionID != "" {
		return unionID + "@dingtalk.com"
	}
	email := strings.ToLower(strings.TrimSpace(user.Email))
	if email != "" {
		return email
	}
	return ""
}

func (h *Handler) findOrCreateDingTalkWorkspaceUser(ctx context.Context, dtUser DingTalkUserResponse, email string) (db.User, error) {
	email = strings.TrimSpace(email)
	user, err := h.Queries.GetUserByEmail(ctx, email)
	if err == nil {
		return h.refreshDingTalkWorkspaceUser(ctx, user, dtUser, email)
	}
	if !isNotFound(err) {
		return db.User{}, err
	}

	name := strings.TrimSpace(dtUser.Name)
	if name == "" {
		name = strings.TrimSpace(dtUser.UserID)
	}
	if name == "" {
		name = email
		if at := strings.Index(email, "@"); at > 0 {
			name = email[:at]
		}
	}
	params := db.CreateUserParams{
		Name:  name,
		Email: email,
	}
	if avatarURL := strings.TrimSpace(dtUser.AvatarURL); avatarURL != "" {
		params.AvatarUrl = pgtype.Text{String: avatarURL, Valid: true}
	}
	created, err := h.Queries.CreateUser(ctx, params)
	if err == nil {
		return created, nil
	}
	if isUniqueViolation(err) {
		existing, getErr := h.Queries.GetUserByEmail(ctx, email)
		if getErr == nil {
			return h.refreshDingTalkWorkspaceUser(ctx, existing, dtUser, email)
		}
	}
	return db.User{}, err
}

func (h *Handler) refreshDingTalkWorkspaceUser(ctx context.Context, user db.User, dtUser DingTalkUserResponse, email string) (db.User, error) {
	needsUpdate := false
	name := user.Name
	identityPrefix := email
	if at := strings.Index(email, "@"); at > 0 {
		identityPrefix = email[:at]
	}
	if displayName := strings.TrimSpace(dtUser.Name); displayName != "" && (strings.TrimSpace(user.Name) == "" || user.Name == email || user.Name == identityPrefix) {
		name = displayName
		needsUpdate = true
	}

	avatar := user.AvatarUrl
	if avatarURL := strings.TrimSpace(dtUser.AvatarURL); avatarURL != "" && !avatar.Valid {
		avatar = pgtype.Text{String: avatarURL, Valid: true}
		needsUpdate = true
	}

	if !needsUpdate {
		return user, nil
	}
	updated, err := h.Queries.UpdateUser(ctx, db.UpdateUserParams{
		ID:        user.ID,
		Name:      name,
		AvatarUrl: avatar,
	})
	if err != nil {
		return user, nil
	}
	return updated, nil
}
