package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var errAgentDispatchDuplicateNotReady = errors.New("duplicate agent chat dispatch result is not ready")

type agentDispatchDuplicateQueries interface {
	GetChannelInboundDedupStatus(context.Context, db.GetChannelInboundDedupStatusParams) (pgtype.Timestamptz, error)
	GetChannelChatSessionBinding(context.Context, db.GetChannelChatSessionBindingParams) (db.ChannelChatSessionBinding, error)
}

func textValue(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: strings.TrimSpace(s) != ""}
}
func formatIssueNumber(n int32) string { return fmt.Sprint(n) }

func dispatchRuntimeContext(c DispatchCommand, idempotencyKey string) []byte {
	// Do not include ExternalIdentity: the token has its own dedicated private
	// task-context field and must never be duplicated in a JSON snapshot.
	payload := map[string]any{
		"dispatch_schema_version":         c.SchemaVersion,
		"dispatch_source":                 c.Source,
		"dispatch_domain":                 c.Event.Domain,
		"dispatch_type":                   c.Event.Type,
		"dispatch_event_data":             c.Event.Data,
		protocol.DispatchSurfaceJSONKey: c.Surface,
		protocol.DispatchOutboundJSONKey: c.Outbound,
		"dispatch_idempotency_key":        idempotencyKey,
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

func (h *Handler) handleAgentDispatchV2(
	w http.ResponseWriter,
	r *http.Request,
	raw []byte,
	dispatchContext agentDispatchContext,
) {
	var request AgentDispatchV2Request
	if err := json.Unmarshal(raw, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid dispatch command")
		return
	}
	command := request.DispatchCommand()
	if err := command.validate(); err != nil {
		slog.Warn("MULTICA_AGENT_DISPATCH_REQUEST",
			"outcome", "rejected",
			"protocol", "dispatch_command_v2",
			"schemaVersion", command.SchemaVersion,
			"failureCode", "invalid_dispatch_command",
			"validationError", err,
		)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := buildAgentDispatchExecutionPlan(command, dispatchContext)
	if err != nil {
		slog.Error("MULTICA_AGENT_DISPATCH_REQUEST",
			"outcome", "failed",
			"protocol", "dispatch_command_v2",
			"schemaVersion", command.SchemaVersion,
			"failureCode", "prompt_build_failed",
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "failed to build dispatch prompt")
		return
	}
	slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
		"outcome", "validated",
		"protocol", "dispatch_command_v2",
		"schemaVersion", command.SchemaVersion,
		"sourcePlatform", command.Source.Platform,
		"sourceType", command.Source.Type,
		"domain", command.Event.Domain,
		"eventType", command.Event.Type,
		"surfaceType", command.Surface.Type,
		"outboundMode", command.Outbound.Mode,
		"messageCount", len(command.Event.Data.Messages),
		"continuationPresent", command.Continuation != nil,
		"promptBuilder", "multica",
		"identityMode", "dispatch_endpoint_actor",
		"serverOutboundSuppressed", plan.SuppressServerOutbound,
		"displayBytes", len(plan.Prompt.DisplayContent),
		"runtimeBytes", len(plan.Prompt.RuntimePrompt),
		"workflowBytes", len(plan.Prompt.WorkflowPrompt),
	)
	if command.AgentID != "" {
		agentID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(command.AgentID), "agentId")
		if !ok {
			return
		}
		if agentID != dispatchContext.AgentID {
			writeError(w, http.StatusForbidden, "agentId does not match dispatch endpoint")
			return
		}
	}

	if plan.SurfaceType == "chat" {
		if command.Continuation != nil &&
			(command.Continuation.Kind != "chat" || strings.TrimSpace(command.Continuation.ChatSessionID) == "") {
			writeError(w, http.StatusBadRequest, "continuation must identify a chat")
			return
		}
		h.createAgentDispatchChatV2(w, r, command, plan, dispatchContext)
		return
	}

	if command.AgentID != "" {
		agent, ok := h.resolveAgentDispatchAgent(
			w, r, dispatchContext.UserID, dispatchContext.WorkspaceID, dispatchContext.AgentID)
		if !ok {
			return
		}
		h.createAgentDispatchIssueV2(w, r, command, plan.Prompt, dispatchContext, agent)
		return
	}
	if command.Continuation == nil || command.Continuation.Kind != "issue" ||
		strings.TrimSpace(command.Continuation.IssueID) == "" {
		writeError(w, http.StatusBadRequest, "continuation must identify an issue")
		return
	}
	h.createAgentDispatchCommentV2(w, r, command, plan.Prompt, dispatchContext)
}

// AgentDispatchV2Request preserves the exact Router JSON contract while the
// internal DispatchCommand owns validation and execution semantics.
type AgentDispatchV2Request struct {
	SchemaVersion    string                        `json:"schemaVersion"`
	AgentID          string                        `json:"agentId,omitempty"`
	Continuation     *AgentDispatchContinuation    `json:"continuation"`
	Source           DispatchSource                `json:"source"`
	Event            DispatchEvent                 `json:"event"`
	Surface          DispatchSurface               `json:"surface"`
	Outbound         DispatchOutbound              `json:"outbound"`
	ExternalIdentity AgentDispatchExternalIdentity `json:"externalIdentity"`
}

func (r AgentDispatchV2Request) DispatchCommand() DispatchCommand {
	return DispatchCommand{
		SchemaVersion: r.SchemaVersion, AgentID: r.AgentID, Continuation: r.Continuation,
		Source: r.Source, Event: r.Event, Surface: r.Surface, Outbound: r.Outbound,
		ExternalIdentity: r.ExternalIdentity,
	}
}

func (h *Handler) createAgentDispatchChatV2(
	w http.ResponseWriter,
	r *http.Request,
	command DispatchCommand,
	plan agentDispatchExecutionPlan,
	dispatchContext agentDispatchContext,
) {
	if h.ChannelRouter == nil || h.DingTalkInstallations == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk chat dispatch not configured")
		return
	}
	row, err := h.Queries.GetActiveDingTalkBotInstallationByAgent(
		r.Context(),
		db.GetActiveDingTalkBotInstallationByAgentParams{
			WorkspaceID: dispatchContext.WorkspaceID,
			AgentID:     dispatchContext.AgentID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "dingtalk robot installation not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to resolve dingtalk robot installation")
		}
		return
	}
	installation, err := h.DingTalkInstallations.GetInWorkspace(
		r.Context(), row.ID, dispatchContext.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load dingtalk robot installation")
		return
	}
	if installation.Status != "active" ||
		installation.TransportMode != dingtalk.TransportModeHTTPCallback ||
		strings.TrimSpace(installation.DispatchEndpointID) != dispatchContext.EndpointID {
		writeError(w, http.StatusForbidden, "dingtalk robot installation is not an HTTP callback source")
		return
	}

	var textParts []string
	for _, message := range command.Event.Data.Messages {
		if len(message.Attachments) > 0 {
			writeError(w, http.StatusUnprocessableEntity, "chat attachments are not supported yet")
			return
		}
		if value := strings.TrimSpace(message.Text); value != "" {
			textParts = append(textParts, value)
		}
	}
	latest := command.Event.Data.Messages[len(command.Event.Data.Messages)-1]
	senderID := strings.TrimSpace(command.Event.Data.Sender.OpenDingTalkID)
	if senderID == "" {
		senderID = strings.TrimSpace(command.Event.Data.Sender.SenderOpenDingTalkID)
	}
	dispatchText := strings.Join(textParts, "\n\n")
	message, err := dingtalk.InboundFromHTTPCallback(dingtalk.HTTPCallbackMessage{
		ConversationID:       command.Event.Data.Conversation.OpenConversationID,
		ConversationType:     command.Event.Data.Conversation.Type,
		ConversationTitle:    command.Event.Data.Conversation.Title,
		MessageID:            latest.OpenMsgID,
		CreatedAt:            latest.OccurredAt,
		SenderID:             senderID,
		SenderStaffID:        command.Event.Data.Sender.StaffID,
		SenderName:           command.Event.Data.Sender.DisplayName,
		Text:                 dispatchText,
		IdentityContextToken: command.ExternalIdentity.ContextToken,
		DispatchContext:      dispatchRuntimeContext(command, dispatchIdempotencyKey(r, command)),
	}, installation.ClientID, uuidToString(installation.ID))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Dispatch Command V2 already selected the surface. Preserve slash-prefixed
	// input as prompt content instead of letting the channel adapter reinterpret
	// it as /new, /reset, /issue, or /unbind.
	message.Text = dispatchText
	message.ForceFresh = false
	result, err := h.ChannelRouter.HandleResultWithOptions(r.Context(), message, plan.channelHandleOptions())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to dispatch dingtalk chat")
		return
	}
	if writeAgentChatNeedsBindingACK(w, result) {
		return
	}
	if result.Outcome == engine.OutcomeDropped && result.DropReason == engine.DropReasonDuplicate {
		response, recoverErr := recoverDuplicateAgentChatDispatch(
			r.Context(), h.Queries, row.ID, latest.OpenMsgID, message)
		if recoverErr != nil {
			writeError(w, http.StatusServiceUnavailable, errAgentDispatchDuplicateNotReady.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, response)
		return
	}
	chatSessionID := uuidToString(result.ChatSessionID)
	if chatSessionID == "" && command.Continuation != nil {
		chatSessionID = command.Continuation.ChatSessionID
	}
	writeJSON(w, http.StatusAccepted, AgentChatDispatchResponse{
		Continuation: AgentDispatchContinuation{Kind: "chat", ChatSessionID: chatSessionID},
		TaskID:       uuidToString(result.TaskID),
	})
}

func recoverDuplicateAgentChatDispatch(
	ctx context.Context,
	queries agentDispatchDuplicateQueries,
	installationID pgtype.UUID,
	messageID string,
	message channel.InboundMessage,
) (AgentChatDispatchResponse, error) {
	processedAt, err := queries.GetChannelInboundDedupStatus(ctx, db.GetChannelInboundDedupStatusParams{
		InstallationID: installationID,
		MessageID:      messageID,
	})
	if err != nil || !processedAt.Valid {
		return AgentChatDispatchResponse{}, errAgentDispatchDuplicateNotReady
	}
	binding, err := queries.GetChannelChatSessionBinding(ctx, db.GetChannelChatSessionBindingParams{
		InstallationID: installationID,
		ChannelChatID:  dingtalk.SessionBindingKey(message),
	})
	if err != nil || !binding.ChatSessionID.Valid {
		return AgentChatDispatchResponse{}, errAgentDispatchDuplicateNotReady
	}
	return AgentChatDispatchResponse{
		Continuation: AgentDispatchContinuation{
			Kind:          "chat",
			ChatSessionID: uuidToString(binding.ChatSessionID),
		},
	}, nil
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
		DispatchContext:           dispatchRuntimeContext(c, dispatchIdempotencyKey(r, c)),
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
	issueID := uuidToString(result.Issue.ID)
	taskID := uuidToString(result.EnqueuedTask.ID)
	slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
		"outcome", "created_issue",
		"protocol", "dispatch_command_v2",
		"httpStatus", http.StatusCreated,
		"continuationReturned", true,
		"continuationFingerprint", agentDispatchIdentifierFingerprint(issueID),
		"taskFingerprint", agentDispatchIdentifierFingerprint(taskID),
	)
	writeJSON(w, http.StatusCreated, AgentDispatchResponse{
		Continuation:    AgentDispatchContinuation{Kind: "issue", IssueID: issueID},
		IssueIdentifier: prefix + "-" + formatIssueNumber(result.Issue.Number),
		TaskID:          taskID,
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
			slog.Warn("MULTICA_AGENT_DISPATCH_CONTINUATION",
				"outcome", "recreated_missing_issue",
				"previousIssueFingerprint", agentDispatchIdentifierFingerprint(c.Continuation.IssueID),
			)
			agent, resolved := h.resolveAgentDispatchAgent(
				w,
				r,
				dispatchContext.UserID,
				dispatchContext.WorkspaceID,
				dispatchContext.AgentID,
			)
			if !resolved {
				return
			}
			h.createAgentDispatchIssueV2(w, r, c, prompt, dispatchContext, agent)
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
		DispatchContext: dispatchRuntimeContext(c, dispatchIdempotencyKey(r, c)),
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
	issueIDString := uuidToString(issue.ID)
	commentID := uuidToString(result.Comment.ID)
	taskID := uuidToString(result.Task.ID)
	slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
		"outcome", "created_follow_up",
		"protocol", "dispatch_command_v2",
		"httpStatus", http.StatusCreated,
		"continuationReturned", true,
		"continuationFingerprint", agentDispatchIdentifierFingerprint(issueIDString),
		"commentFingerprint", agentDispatchIdentifierFingerprint(commentID),
		"taskFingerprint", agentDispatchIdentifierFingerprint(taskID),
	)
	writeJSON(w, http.StatusCreated, AgentDispatchResponse{Continuation: AgentDispatchContinuation{Kind: "issue", IssueID: issueIDString}, CommentID: commentID, TaskID: taskID})
}
