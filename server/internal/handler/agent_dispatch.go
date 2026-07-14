package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type AgentDispatchPrompt struct {
	Text string `json:"text"`
}

type AgentDispatchAttachment struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	DownloadURL string `json:"downloadUrl"`
	ExpiresAt   *int64 `json:"expiresAt,omitempty"`
}

type AgentDispatchInput struct {
	SystemPrompt AgentDispatchPrompt       `json:"systemPrompt"`
	UserPrompt   AgentDispatchPrompt       `json:"userPrompt"`
	Attachments  []AgentDispatchAttachment `json:"attachments"`
}

// AgentDispatchContinuation is opaque routing state to the upstream message
// router. The current endpoint is intentionally issue-only: a nil continuation
// creates an issue and an issue continuation creates a follow-up comment.
type AgentDispatchContinuation struct {
	Kind    string `json:"kind"`
	IssueID string `json:"issueId"`
}

type AgentDispatchRequest struct {
	Continuation *AgentDispatchContinuation `json:"continuation,omitempty"`
	AgentID      string                     `json:"agentId,omitempty"`
	Input        AgentDispatchInput         `json:"input"`
	ContextToken string                     `json:"contextToken"`
}

type AgentDispatchResponse struct {
	Continuation    AgentDispatchContinuation `json:"continuation"`
	IssueIdentifier string                    `json:"issueIdentifier,omitempty"`
	CommentID       string                    `json:"commentId,omitempty"`
	TaskID          string                    `json:"taskId"`
}

// HandleAgentDispatch consumes a message-router delivery using target identity
// embedded in the callback URL. New issues require a body-level agentId;
// continuations resolve the agent from the existing issue assignment.
// schemaVersion is intentionally not gated and the request body has no
// handler-level size cap. contextToken is handed to the runtime launcher only
// and is never rendered into issue/comment content or persisted as task
// context. dispatchTaskId remains owned by the upstream router; when present
// in a legacy payload it is ignored by the JSON decoder.
func (h *Handler) HandleAgentDispatch(w http.ResponseWriter, r *http.Request) {
	var req AgentDispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ContextToken = strings.TrimSpace(req.ContextToken)
	if req.ContextToken == "" {
		writeError(w, http.StatusBadRequest, "contextToken is required")
		return
	}
	if strings.TrimSpace(req.Input.UserPrompt.Text) == "" {
		writeError(w, http.StatusBadRequest, "input.userPrompt.text is required")
		return
	}
	for _, attachment := range req.Input.Attachments {
		if strings.TrimSpace(attachment.Name) == "" || strings.TrimSpace(attachment.DownloadURL) == "" {
			writeError(w, http.StatusBadRequest, "attachment name and downloadUrl are required")
			return
		}
	}

	userID, workspaceID, ok := h.resolveAgentDispatchContext(w, r)
	if !ok {
		return
	}
	launchOpts := service.RuntimeLaunchOptions{
		AgentIdentityContextToken: req.ContextToken,
	}
	if req.Continuation == nil {
		if strings.TrimSpace(req.AgentID) == "" {
			writeError(w, http.StatusBadRequest, "agentId is required")
			return
		}
		agentID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(req.AgentID), "agentId")
		if !ok {
			return
		}
		agent, ok := h.resolveAgentDispatchAgent(w, r, userID, workspaceID, agentID)
		if !ok {
			return
		}
		h.createAgentDispatchIssue(w, r, req, userID, workspaceID, agent, launchOpts)
		return
	}
	if req.Continuation.Kind != "issue" || strings.TrimSpace(req.Continuation.IssueID) == "" {
		writeError(w, http.StatusBadRequest, "continuation must identify an issue")
		return
	}
	h.createAgentDispatchComment(w, r, req, userID, workspaceID, launchOpts)
}

func (h *Handler) resolveAgentDispatchContext(w http.ResponseWriter, r *http.Request) (pgtype.UUID, pgtype.UUID, bool) {
	userID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "userId"), "userId")
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "workspaceId"), "workspaceId")
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID: userID, WorkspaceID: workspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "dispatch member not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to resolve dispatch member")
		}
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	return userID, workspaceID, true
}

func (h *Handler) resolveAgentDispatchAgent(w http.ResponseWriter, r *http.Request, userID, workspaceID, agentID pgtype.UUID) (db.Agent, bool) {
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID: agentID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "dispatch agent not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to resolve dispatch agent")
		}
		return db.Agent{}, false
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "agent is archived")
		return db.Agent{}, false
	}
	if agent.Kind != "user" {
		writeError(w, http.StatusNotFound, "dispatch agent not found")
		return db.Agent{}, false
	}
	if !agent.RuntimeID.Valid {
		writeError(w, http.StatusConflict, "agent has no runtime")
		return db.Agent{}, false
	}
	memberID := uuidToString(userID)
	if !h.canInvokeAgent(r.Context(), agent, "member", memberID, memberID, uuidToString(workspaceID)) {
		writeError(w, http.StatusForbidden, "dispatch member cannot invoke agent")
		return db.Agent{}, false
	}
	return agent, true
}

func (h *Handler) createAgentDispatchIssue(w http.ResponseWriter, r *http.Request, req AgentDispatchRequest, userID, workspaceID pgtype.UUID, agent db.Agent, launchOpts service.RuntimeLaunchOptions) {
	attachmentService := service.NewExternalAttachmentService(h.Queries, h.Storage, h.AgentDispatchHTTPClient)
	imported, err := attachmentService.Import(r.Context(), service.ExternalAttachmentImportParams{
		WorkspaceID: workspaceID,
		UploaderID:  userID,
		Sources:     agentDispatchAttachmentSources(req.Input.Attachments),
	})
	if err != nil {
		writeAgentDispatchAttachmentError(w, err)
		return
	}
	keepAttachments := false
	defer func() {
		if !keepAttachments {
			attachmentService.DeleteImported(r.Context(), imported)
		}
	}()

	prefix := h.getIssuePrefix(r.Context(), workspaceID)
	result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:    workspaceID,
		Title:          agentDispatchIssueTitle(req.Input.UserPrompt.Text),
		Description:    pgtype.Text{String: buildAgentDispatchContent(req.Input), Valid: true},
		Status:         "todo",
		Priority:       "none",
		AssigneeType:   pgtype.Text{String: "agent", Valid: true},
		AssigneeID:     agent.ID,
		CreatorType:    "member",
		CreatorID:      userID,
		AttachmentIDs:  attachmentIDs(imported),
		AllowDuplicate: true,
	}, service.IssueCreateOpts{
		ActorID:             uuidToString(userID),
		AnalyticsAgentID:    uuidToString(agent.ID),
		Platform:            "webhook",
		RuntimeLaunchOptions: launchOpts,
		BroadcastPayload: func(issue db.Issue, _ []db.Attachment) map[string]any {
			return map[string]any{"issue": issueToResponse(issue, prefix)}
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue")
		return
	}
	keepAttachments = true
	if result.EnqueuedTask == nil {
		writeError(w, http.StatusInternalServerError, "issue created but agent task was not enqueued")
		return
	}
	issueID := uuidToString(result.Issue.ID)
	writeJSON(w, http.StatusCreated, AgentDispatchResponse{
		Continuation: AgentDispatchContinuation{
			Kind: "issue", IssueID: issueID,
		},
		IssueIdentifier: prefix + "-" + fmt.Sprint(result.Issue.Number),
		TaskID:          uuidToString(result.EnqueuedTask.ID),
	})
}

func (h *Handler) createAgentDispatchComment(w http.ResponseWriter, r *http.Request, req AgentDispatchRequest, userID, workspaceID pgtype.UUID, launchOpts service.RuntimeLaunchOptions) {
	issueID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(req.Continuation.IssueID), "continuation.issueId")
	if !ok {
		return
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID: issueID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "continuation issue not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load continuation issue")
		}
		return
	}
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		writeError(w, http.StatusConflict, "continuation issue is not assigned to an agent")
		return
	}
	_, ok = h.resolveAgentDispatchAgent(w, r, userID, workspaceID, issue.AssigneeID)
	if !ok {
		return
	}

	attachmentService := service.NewExternalAttachmentService(h.Queries, h.Storage, h.AgentDispatchHTTPClient)
	imported, err := attachmentService.Import(r.Context(), service.ExternalAttachmentImportParams{
		WorkspaceID: workspaceID,
		UploaderID:  userID,
		IssueID:     issue.ID,
		Sources:     agentDispatchAttachmentSources(req.Input.Attachments),
	})
	if err != nil {
		writeAgentDispatchAttachmentError(w, err)
		return
	}
	keepAttachments := false
	defer func() {
		if !keepAttachments {
			attachmentService.DeleteImported(r.Context(), imported)
		}
	}()
	result, err := h.IssueCommentService.CreateExternalFollowUp(r.Context(), service.IssueCommentCreateParams{
		Issue:                issue,
		AuthorID:             userID,
		Content:              buildAgentDispatchContent(req.Input),
		AttachmentIDs:        attachmentIDs(imported),
		RuntimeLaunchOptions: launchOpts,
	}, service.IssueCommentCreateOpts{
		BroadcastPayload: func(comment db.Comment, attachments []db.Attachment) map[string]any {
			responses := make([]AttachmentResponse, 0, len(attachments))
			for _, attachment := range attachments {
				responses = append(responses, h.attachmentToResponse(attachment))
			}
			return map[string]any{
				"comment":             commentToResponse(comment, nil, responses),
				"issue_title":         issue.Title,
				"issue_assignee_type": textToPtr(issue.AssigneeType),
				"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
				"issue_status":        issue.Status,
			}
		},
	})
	if errors.Is(err, service.ErrIssueDispatchPending) {
		writeError(w, http.StatusConflict, "issue already has a pending agent task; retry with a fresh contextToken")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue follow-up")
		return
	}
	keepAttachments = true
	writeJSON(w, http.StatusCreated, AgentDispatchResponse{
		Continuation: AgentDispatchContinuation{
			Kind: "issue", IssueID: uuidToString(issue.ID),
		},
		CommentID: uuidToString(result.Comment.ID),
		TaskID:    uuidToString(result.Task.ID),
	})
}

func writeAgentDispatchAttachmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrExternalAttachmentStorageUnavailable):
		writeError(w, http.StatusServiceUnavailable, "attachment storage is not configured")
	case errors.Is(err, service.ErrExternalAttachmentExpired):
		writeError(w, http.StatusUnprocessableEntity, "attachment download URL has expired")
	case errors.Is(err, service.ErrExternalAttachmentTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "attachment is too large")
	default:
		writeError(w, http.StatusBadGateway, "failed to import external attachment")
	}
}

func agentDispatchAttachmentSources(attachments []AgentDispatchAttachment) []service.ExternalAttachmentSource {
	sources := make([]service.ExternalAttachmentSource, 0, len(attachments))
	for _, attachment := range attachments {
		var expiresAt *time.Time
		if attachment.ExpiresAt != nil {
			value := time.UnixMilli(*attachment.ExpiresAt)
			expiresAt = &value
		}
		sources = append(sources, service.ExternalAttachmentSource{
			Name:        attachment.Name,
			ContentType: attachment.ContentType,
			SizeBytes:   attachment.SizeBytes,
			DownloadURL: attachment.DownloadURL,
			ExpiresAt:   expiresAt,
		})
	}
	return sources
}

func attachmentIDs(attachments []db.Attachment) []pgtype.UUID {
	ids := make([]pgtype.UUID, 0, len(attachments))
	for _, attachment := range attachments {
		ids = append(ids, attachment.ID)
	}
	return ids
}

func agentDispatchIssueTitle(userPrompt string) string {
	firstLine := "External event"
	for _, line := range strings.Split(userPrompt, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			firstLine = trimmed
			break
		}
	}
	const maxRunes = 160
	if utf8.RuneCountInString(firstLine) > maxRunes {
		firstLine = string([]rune(firstLine)[:maxRunes])
	}
	return firstLine
}

func buildAgentDispatchContent(input AgentDispatchInput) string {
	var b strings.Builder
	if systemPrompt := strings.TrimSpace(input.SystemPrompt.Text); systemPrompt != "" {
		b.WriteString("## System prompt\n\n")
		b.WriteString(systemPrompt)
		b.WriteString("\n\n")
	}
	b.WriteString("## User prompt\n\n")
	b.WriteString(strings.TrimSpace(input.UserPrompt.Text))
	if len(input.Attachments) > 0 {
		b.WriteString("\n\n## Attachments\n")
		for _, attachment := range input.Attachments {
			b.WriteString("\n- ")
			b.WriteString(strings.TrimSpace(attachment.Name))
		}
	}
	b.WriteString("\n")
	return b.String()
}
