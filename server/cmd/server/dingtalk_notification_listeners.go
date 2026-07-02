package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const dingTalkNotificationTimeout = 8 * time.Second

type dingTalkNotificationListener struct {
	queries  *db.Queries
	notifier dingtalk.PersonalNotificationClient
	appURL   string
}

func registerDingTalkNotificationListeners(bus *events.Bus, queries *db.Queries, notifier dingtalk.PersonalNotificationClient, appURL string) {
	if bus == nil || queries == nil || notifier == nil || !notifier.IsConfigured() {
		return
	}
	l := &dingTalkNotificationListener{
		queries:  queries,
		notifier: notifier,
		appURL:   strings.TrimRight(strings.TrimSpace(appURL), "/"),
	}
	bus.Subscribe(protocol.EventInboxNew, l.handleInboxNew)
	bus.Subscribe(protocol.EventInvitationCreated, l.handleInvitationCreated)
	bus.Subscribe(protocol.EventMemberAdded, l.handleMemberAdded)
}

func (l *dingTalkNotificationListener) handleInboxNew(e events.Event) {
	item, ok := dingTalkInboxItemFromPayload(e.Payload)
	if !ok || item.recipientType != "member" || item.recipientID == "" {
		return
	}
	if item.workspaceID == "" {
		item.workspaceID = e.WorkspaceID
	}
	go l.pushInboxNotification(item)
}

func (l *dingTalkNotificationListener) handleInvitationCreated(e events.Event) {
	inv, workspaceName, ok := dingTalkInvitationFromPayload(e.Payload)
	if !ok || inv.ID == "" {
		return
	}
	go l.pushInvitationNotification(inv, workspaceName)
}

func (l *dingTalkNotificationListener) handleMemberAdded(e events.Event) {
	member, workspaceName, ok := dingTalkMemberAddedFromPayload(e.Payload)
	if !ok || member.UserID == "" {
		return
	}
	// Invitation acceptance already has a dedicated invitation card. Skip the
	// follow-up member:added event created by the invitee accepting it.
	if shouldSkipDingTalkMemberAddedNotification(e.ActorID, member.UserID) {
		return
	}
	go l.pushMemberAddedNotification(member, workspaceName)
}

func shouldSkipDingTalkMemberAddedNotification(actorID, memberUserID string) bool {
	return actorID != "" && actorID == memberUserID
}

type dingTalkInboxItem struct {
	id            string
	workspaceID   string
	recipientType string
	recipientID   string
	issueID       string
	title         string
	body          string
}

func dingTalkInboxItemFromPayload(payload any) (dingTalkInboxItem, bool) {
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		return dingTalkInboxItem{}, false
	}
	switch item := payloadMap["item"].(type) {
	case db.InboxItem:
		return dingTalkInboxItem{
			id:            uuidString(item.ID),
			workspaceID:   uuidString(item.WorkspaceID),
			recipientType: item.RecipientType,
			recipientID:   uuidString(item.RecipientID),
			issueID:       uuidString(item.IssueID),
			title:         item.Title,
			body:          textString(item.Body),
		}, true
	case map[string]any:
		return dingTalkInboxItem{
			id:            stringFromMap(item, "id"),
			workspaceID:   stringFromMap(item, "workspace_id"),
			recipientType: stringFromMap(item, "recipient_type"),
			recipientID:   stringFromMap(item, "recipient_id"),
			issueID:       stringFromMap(item, "issue_id"),
			title:         stringFromMap(item, "title"),
			body:          stringFromMap(item, "body"),
		}, true
	default:
		return dingTalkInboxItem{}, false
	}
}

func (l *dingTalkNotificationListener) pushInboxNotification(item dingTalkInboxItem) {
	ctx, cancel := context.WithTimeout(context.Background(), dingTalkNotificationTimeout)
	defer cancel()

	user, ok := l.loadUser(ctx, item.recipientID)
	if !ok {
		return
	}
	workspaceName, workspaceSlug := l.loadWorkspaceLabel(ctx, item.workspaceID)
	title := firstNonEmpty(item.title, "Multica 通知")
	text := markdownLines(
		"## "+title,
		workspaceLine(workspaceName),
		item.body,
	)
	req := dingtalk.PersonalNotification{
		Recipient: notificationRecipientFromUser(user),
		Card: dingtalk.PersonalNotificationCard{
			Title:      title,
			Text:       text,
			URL:        l.issueURL(workspaceSlug, item.issueID),
			ButtonText: "在 Multica 查看",
		},
		Source:    "inbox:new",
		DedupeKey: "inbox:" + item.id,
	}
	if err := l.notifier.SendPersonalNotification(ctx, req); err != nil {
		slog.Warn("dingtalk inbox notification failed", "recipient_id", item.recipientID, "inbox_id", item.id, "error", err)
	}
}

func (l *dingTalkNotificationListener) pushInvitationNotification(inv handler.InvitationResponse, workspaceName string) {
	ctx, cancel := context.WithTimeout(context.Background(), dingTalkNotificationTimeout)
	defer cancel()

	recipient := dingtalk.PersonalNotificationRecipient{Email: inv.InviteeEmail}
	if inv.InviteeUserID != nil && *inv.InviteeUserID != "" {
		if user, ok := l.loadUser(ctx, *inv.InviteeUserID); ok {
			recipient = notificationRecipientFromUser(user)
		}
	}
	if workspaceName == "" {
		workspaceName = inv.WorkspaceName
	}
	if workspaceName == "" {
		workspaceName, _ = l.loadWorkspaceLabel(ctx, inv.WorkspaceID)
	}
	if workspaceName == "" {
		workspaceName = "Multica 工作区"
	}
	inviterName := firstNonEmpty(inv.InviterName, inv.InviterEmail)
	if inviterName == "" {
		inviterName = l.loadUserName(ctx, inv.InviterID)
	}

	title := fmt.Sprintf("邀请加入 %s", workspaceName)
	text := markdownLines(
		"## "+title,
		firstNonEmpty(inviterLine(inviterName, roleLabel(inv.Role)), fmt.Sprintf("你被邀请以 %s 身份加入该工作区。", roleLabel(inv.Role))),
	)
	req := dingtalk.PersonalNotification{
		Recipient: recipient,
		Card: dingtalk.PersonalNotificationCard{
			Title:      title,
			Text:       text,
			URL:        l.invitationURL(inv.ID),
			ButtonText: "查看邀请",
		},
		Source:    "invitation:created",
		DedupeKey: "invitation:" + inv.ID,
	}
	if err := l.notifier.SendPersonalNotification(ctx, req); err != nil {
		slog.Warn("dingtalk invitation notification failed", "invitation_id", inv.ID, "error", err)
	}
}

func (l *dingTalkNotificationListener) pushMemberAddedNotification(member handler.MemberWithUserResponse, workspaceName string) {
	ctx, cancel := context.WithTimeout(context.Background(), dingTalkNotificationTimeout)
	defer cancel()

	loadedWorkspaceName, workspaceSlug := l.loadWorkspaceLabel(ctx, member.WorkspaceID)
	if workspaceName == "" {
		workspaceName = loadedWorkspaceName
	}
	if workspaceName == "" {
		workspaceName = "Multica 工作区"
	}
	title := fmt.Sprintf("你已加入 %s", workspaceName)
	text := markdownLines(
		"## "+title,
		fmt.Sprintf("你已被添加为 %s，可以打开 Multica 查看工作区内容。", roleLabel(member.Role)),
	)
	req := dingtalk.PersonalNotification{
		Recipient: dingtalk.PersonalNotificationRecipient{
			Email: member.Email,
			Name:  member.Name,
		},
		Card: dingtalk.PersonalNotificationCard{
			Title:      title,
			Text:       text,
			URL:        l.workspaceURL(workspaceSlug),
			ButtonText: "打开工作区",
		},
		Source:    "member:added",
		DedupeKey: "member:" + member.WorkspaceID + ":" + member.UserID,
	}
	if err := l.notifier.SendPersonalNotification(ctx, req); err != nil {
		slog.Warn("dingtalk member-added notification failed", "workspace_id", member.WorkspaceID, "user_id", member.UserID, "error", err)
	}
}

func dingTalkInvitationFromPayload(payload any) (handler.InvitationResponse, string, bool) {
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		return handler.InvitationResponse{}, "", false
	}
	workspaceName := stringFromMap(payloadMap, "workspace_name")
	switch inv := payloadMap["invitation"].(type) {
	case handler.InvitationResponse:
		return inv, workspaceName, true
	case map[string]any:
		return handler.InvitationResponse{
			ID:            stringFromMap(inv, "id"),
			WorkspaceID:   stringFromMap(inv, "workspace_id"),
			InviterID:     stringFromMap(inv, "inviter_id"),
			InviteeEmail:  stringFromMap(inv, "invitee_email"),
			InviteeUserID: stringPtrFromMap(inv, "invitee_user_id"),
			Role:          stringFromMap(inv, "role"),
			Status:        stringFromMap(inv, "status"),
			InviterName:   stringFromMap(inv, "inviter_name"),
			InviterEmail:  stringFromMap(inv, "inviter_email"),
			WorkspaceName: stringFromMap(inv, "workspace_name"),
		}, workspaceName, true
	default:
		return handler.InvitationResponse{}, workspaceName, false
	}
}

func dingTalkMemberAddedFromPayload(payload any) (handler.MemberWithUserResponse, string, bool) {
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		return handler.MemberWithUserResponse{}, "", false
	}
	workspaceName := stringFromMap(payloadMap, "workspace_name")
	switch member := payloadMap["member"].(type) {
	case handler.MemberWithUserResponse:
		return member, workspaceName, true
	case map[string]any:
		return handler.MemberWithUserResponse{
			ID:          stringFromMap(member, "id"),
			WorkspaceID: stringFromMap(member, "workspace_id"),
			UserID:      stringFromMap(member, "user_id"),
			Role:        stringFromMap(member, "role"),
			Name:        stringFromMap(member, "name"),
			Email:       stringFromMap(member, "email"),
		}, workspaceName, true
	default:
		return handler.MemberWithUserResponse{}, workspaceName, false
	}
}

func (l *dingTalkNotificationListener) loadUser(ctx context.Context, userID string) (db.User, bool) {
	uuid, err := util.ParseUUID(userID)
	if err != nil {
		slog.Warn("dingtalk notification: invalid user id", "user_id", userID, "error", err)
		return db.User{}, false
	}
	user, err := l.queries.GetUser(ctx, uuid)
	if err != nil {
		slog.Warn("dingtalk notification: load user failed", "user_id", userID, "error", err)
		return db.User{}, false
	}
	return user, true
}

func (l *dingTalkNotificationListener) loadUserName(ctx context.Context, userID string) string {
	if user, ok := l.loadUser(ctx, userID); ok {
		return firstNonEmpty(user.Name, user.Email)
	}
	return ""
}

func (l *dingTalkNotificationListener) loadWorkspaceLabel(ctx context.Context, workspaceID string) (string, string) {
	uuid, err := util.ParseUUID(workspaceID)
	if err != nil {
		return "", ""
	}
	ws, err := l.queries.GetWorkspace(ctx, uuid)
	if err != nil {
		return "", ""
	}
	return ws.Name, ws.Slug
}

func notificationRecipientFromUser(user db.User) dingtalk.PersonalNotificationRecipient {
	return dingtalk.PersonalNotificationRecipient{
		Email: user.Email,
		Name:  user.Name,
	}
}

func (l *dingTalkNotificationListener) issueURL(workspaceSlug, issueID string) string {
	if l.appURL == "" || workspaceSlug == "" {
		return ""
	}
	if issueID == "" {
		return l.workspaceURL(workspaceSlug)
	}
	return l.appURL + "/" + url.PathEscape(workspaceSlug) + "/issues/" + url.PathEscape(issueID)
}

func (l *dingTalkNotificationListener) workspaceURL(workspaceSlug string) string {
	if l.appURL == "" || workspaceSlug == "" {
		return ""
	}
	return l.appURL + "/" + url.PathEscape(workspaceSlug) + "/issues"
}

func (l *dingTalkNotificationListener) invitationURL(invitationID string) string {
	if l.appURL == "" || invitationID == "" {
		return ""
	}
	return l.appURL + "/invite/" + url.PathEscape(invitationID)
}

func roleLabel(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin":
		return "管理员"
	case "owner":
		return "所有者"
	default:
		return "成员"
	}
}

func inviterLine(inviterName, role string) string {
	if strings.TrimSpace(inviterName) == "" {
		return ""
	}
	return fmt.Sprintf("%s 邀请你以 %s 身份加入该工作区。", inviterName, role)
}

func workspaceLine(workspaceName string) string {
	if workspaceName == "" {
		return ""
	}
	return "工作区：" + workspaceName
}

func markdownLines(lines ...string) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func stringFromMap(m map[string]any, key string) string {
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case *string:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(*v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func stringPtrFromMap(m map[string]any, key string) *string {
	value := stringFromMap(m, key)
	if value == "" {
		return nil
	}
	return &value
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return util.UUIDToString(u)
}

func textString(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return strings.TrimSpace(t.String)
}
