package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func textValue(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: strings.TrimSpace(s) != ""}
}
func formatIssueNumber(n int32) string { return fmt.Sprint(n) }

func dispatchRuntimeContext(c DispatchCommand, prompt DispatchPrompt, idempotencyKey string) []byte {
	// Do not include ExternalIdentity: the token has its own dedicated private
	// task-context field and must never be duplicated in a JSON snapshot.
	payload := map[string]any{
		"dispatch_schema_version":  c.SchemaVersion,
		"dispatch_source":          c.Source,
		"dispatch_domain":          c.Event.Domain,
		"dispatch_type":            c.Event.Type,
		"dispatch_event_data":      c.Event.Data,
		protocol.DispatchSurfaceJSONKey: c.Surface,
		protocol.DispatchOutboundJSONKey: c.Outbound,
		protocol.DispatchRuntimePromptJSONKey: prompt.RuntimePrompt,
		protocol.DispatchWorkflowPromptJSONKey: prompt.WorkflowPrompt,
		"dispatch_idempotency_key": idempotencyKey,
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func dispatchIdempotencyKey(r *http.Request, c DispatchCommand) string {
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" && len(key) <= 256 {
		return key
	}
	return dispatchWindowIdempotencyKey(c)
}

func (h *Handler) createAgentDispatchIssueV2(w http.ResponseWriter, r *http.Request, c DispatchCommand, prompt DispatchPrompt, dispatchContext agentDispatchContext, agent db.Agent) {
	attachments := make([]AgentDispatchAttachment, 0)
	for _, m := range c.Event.Data.Messages {
		for _, a := range m.Attachments {
			attachments = append(attachments, AgentDispatchAttachment{Type: a.Type, Name: a.Name, ContentType: a.ContentType, SizeBytes: a.SizeBytes, DownloadURL: a.DownloadURL, ExpiresAt: a.ExpiresAt})
		}
	}
	attachmentService := service.NewExternalAttachmentService(h.Queries, h.Storage, h.AgentDispatchHTTPClient)
	imported, err := attachmentService.Import(r.Context(), service.ExternalAttachmentImportParams{
		WorkspaceID: dispatchContext.WorkspaceID,
		UploaderID:  dispatchContext.UserID,
		Sources:     agentDispatchAttachmentSources(attachments),
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

	result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:               dispatchContext.WorkspaceID,
		Title:                     dispatchIssueTitle(c),
		Description:               textValue(prompt.DisplayContent),
		Status:                    "todo",
		Priority:                  "none",
		AssigneeType:              textValue("agent"),
		AssigneeID:                agent.ID,
		CreatorType:               "member",
		CreatorID:                 dispatchContext.UserID,
		AttachmentIDs:             attachmentIDs(imported),
		AllowDuplicate:            false,
		AgentIdentityContextToken: c.ExternalIdentity.ContextToken,
		DispatchContext:           dispatchRuntimeContext(c, prompt, dispatchIdempotencyKey(r, c)),
	}, service.IssueCreateOpts{
		ActorID:          uuidToString(dispatchContext.UserID),
		AnalyticsAgentID: uuidToString(agent.ID),
		Platform:         "webhook",
	})
	if errors.Is(err, service.ErrActiveDuplicate) {
		writeError(w, http.StatusConflict, "dispatch already accepted")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue")
		return
	}
	keepAttachments = true
	if result.EnqueuedTask == nil {
		writeError(w, http.StatusInternalServerError, "issue created but agent task was not enqueued")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), dispatchContext.WorkspaceID)
	writeJSON(w, http.StatusCreated, AgentDispatchResponse{
		Continuation:    AgentDispatchContinuation{Kind: "issue", IssueID: uuidToString(result.Issue.ID)},
		IssueIdentifier: prefix + "-" + formatIssueNumber(result.Issue.Number),
		TaskID:          uuidToString(result.EnqueuedTask.ID),
	})
}

func (h *Handler) createAgentDispatchCommentV2(w http.ResponseWriter, r *http.Request, c DispatchCommand, prompt DispatchPrompt, dispatchContext agentDispatchContext) {
	issueID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(c.Continuation.IssueID), "continuation.issueId")
	if !ok {
		return
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: dispatchContext.WorkspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "continuation issue not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load continuation issue")
		}
		return
	}
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || issue.AssigneeID != dispatchContext.AgentID {
		writeError(w, http.StatusForbidden, "continuation issue does not match dispatch endpoint")
		return
	}
	attachments := make([]AgentDispatchAttachment, 0)
	for _, m := range c.Event.Data.Messages {
		for _, a := range m.Attachments {
			attachments = append(attachments, AgentDispatchAttachment{Type: a.Type, Name: a.Name, ContentType: a.ContentType, SizeBytes: a.SizeBytes, DownloadURL: a.DownloadURL, ExpiresAt: a.ExpiresAt})
		}
	}
	attachmentService := service.NewExternalAttachmentService(h.Queries, h.Storage, h.AgentDispatchHTTPClient)
	imported, err := attachmentService.Import(r.Context(), service.ExternalAttachmentImportParams{WorkspaceID: dispatchContext.WorkspaceID, UploaderID: dispatchContext.UserID, IssueID: issue.ID, Sources: agentDispatchAttachmentSources(attachments)})
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
		Issue: issue, AuthorID: dispatchContext.UserID, Content: prompt.DisplayContent,
		AttachmentIDs: attachmentIDs(imported), AgentIdentityContextToken: c.ExternalIdentity.ContextToken,
		DispatchContext: dispatchRuntimeContext(c, prompt, dispatchIdempotencyKey(r, c)),
	}, service.IssueCommentCreateOpts{})
	if errors.Is(err, service.ErrIssueDispatchPending) {
		writeError(w, http.StatusConflict, "issue already has a pending agent task")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue follow-up")
		return
	}
	keepAttachments = true
	writeJSON(w, http.StatusCreated, AgentDispatchResponse{Continuation: AgentDispatchContinuation{Kind: "issue", IssueID: uuidToString(issue.ID)}, CommentID: uuidToString(result.Comment.ID), TaskID: uuidToString(result.Task.ID)})
}
