package handler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
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
	Kind          string `json:"kind"`
	IssueID       string `json:"issueId,omitempty"`
	ChatSessionID string `json:"chatSessionId,omitempty"`
}

type AgentDispatchChannelContext struct {
	Platform     string `json:"platform"`
	AccountID    string `json:"accountId"`
	TenantID     string `json:"tenantId"`
	Conversation struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Title string `json:"title"`
	} `json:"conversation"`
	Sender struct {
		ID       string `json:"id"`
		TenantID string `json:"tenantId"`
		StaffID  string `json:"staffId"`
		Name     string `json:"name"`
	} `json:"sender"`
	Message struct {
		ID        string `json:"id"`
		CreatedAt int64  `json:"createdAt"`
		Text      string `json:"text"`
	} `json:"message"`
}

type AgentDispatchExternalIdentity struct {
	ContextToken string `json:"contextToken"`
	ExpiresAt    int64  `json:"expiresAt"`
}

type AgentDispatchRequest struct {
	Continuation     *AgentDispatchContinuation     `json:"continuation,omitempty"`
	AgentID          string                         `json:"agentId,omitempty"`
	ExternalIdentity *AgentDispatchExternalIdentity `json:"externalIdentity,omitempty"`
	Input            AgentDispatchInput             `json:"input"`
	ChannelContext   *AgentDispatchChannelContext   `json:"channelContext,omitempty"`
}

type AgentDispatchResponse struct {
	Continuation    AgentDispatchContinuation `json:"continuation"`
	IssueIdentifier string                    `json:"issueIdentifier,omitempty"`
	CommentID       string                    `json:"commentId,omitempty"`
	TaskID          string                    `json:"taskId"`
}

type AgentChatDispatchResponse struct {
	Continuation AgentDispatchContinuation `json:"continuation"`
	TaskID       string                    `json:"taskId,omitempty"`
}

// HandleAgentDispatch consumes an authenticated message-router delivery. The
// endpoint id is a non-secret locator; its Bearer secret authenticates the
// caller and resolves the actor, workspace, and allowed agent server-side.
// New issues still require a body-level agentId and it must match the endpoint;
// continuations resolve the issue while remaining scoped to the same agent.
// schemaVersion is intentionally not gated and the request body has no
// handler-level size cap. externalIdentity.contextToken is accepted only here,
// after the endpoint-specific Bearer credential has authenticated the internal
// caller; it enters server-private task context and never issue/comment content.
func (h *Handler) HandleAgentDispatch(w http.ResponseWriter, r *http.Request) {
	dispatchContext, ok := h.resolveAgentDispatchContext(w, r)
	if !ok {
		return
	}

	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var version struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &version); err == nil && version.SchemaVersion == "2.0" {
		h.handleAgentDispatchV2(w, r, raw, dispatchContext)
		return
	}
	legacySchemaVersion := strings.TrimSpace(version.SchemaVersion)
	if legacySchemaVersion == "" {
		legacySchemaVersion = "missing"
	}
	slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
		"outcome", "received",
		"protocol", "legacy",
		"schemaVersion", legacySchemaVersion,
	)
	var req AgentDispatchRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Input.UserPrompt.Text) == "" {
		slog.Warn("MULTICA_AGENT_DISPATCH_REQUEST",
			"outcome", "rejected",
			"protocol", "legacy",
			"schemaVersion", legacySchemaVersion,
			"failureCode", "user_prompt_required",
		)
		writeError(w, http.StatusBadRequest, "input.userPrompt.text is required")
		return
	}
	if req.ExternalIdentity != nil {
		contextToken := strings.TrimSpace(req.ExternalIdentity.ContextToken)
		if contextToken == "" || contextToken != req.ExternalIdentity.ContextToken || len(contextToken) > 8192 {
			writeError(w, http.StatusBadRequest, "externalIdentity.contextToken is invalid")
			return
		}
		if req.ExternalIdentity.ExpiresAt <= 0 {
			writeError(w, http.StatusBadRequest, "externalIdentity.expiresAt is invalid")
			return
		}
		req.ExternalIdentity.ContextToken = contextToken
	}
	for _, attachment := range req.Input.Attachments {
		if strings.TrimSpace(attachment.Name) == "" || strings.TrimSpace(attachment.DownloadURL) == "" {
			writeError(w, http.StatusBadRequest, "attachment name and downloadUrl are required")
			return
		}
	}
	if req.ChannelContext != nil {
		h.handleAgentChatDispatch(w, r, req, dispatchContext)
		return
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
		if agentID != dispatchContext.AgentID {
			writeError(w, http.StatusForbidden, "agentId does not match dispatch endpoint")
			return
		}
		agent, ok := h.resolveAgentDispatchAgent(w, r, dispatchContext.UserID, dispatchContext.WorkspaceID, agentID)
		if !ok {
			return
		}
		h.createAgentDispatchIssue(w, r, req, dispatchContext.UserID, dispatchContext.WorkspaceID, agent)
		return
	}
	if req.Continuation.Kind != "issue" || strings.TrimSpace(req.Continuation.IssueID) == "" {
		writeError(w, http.StatusBadRequest, "continuation must identify an issue")
		return
	}
	h.createAgentDispatchComment(w, r, req, dispatchContext)
}

func (h *Handler) handleAgentChatDispatch(
	w http.ResponseWriter,
	r *http.Request,
	req AgentDispatchRequest,
	dispatchContext agentDispatchContext,
) {
	ctx := req.ChannelContext
	if h.ChannelRouter == nil || h.DingTalkInstallations == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk chat dispatch not configured")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(ctx.Platform), "dingtalk") ||
		strings.TrimSpace(ctx.AccountID) == "" ||
		strings.TrimSpace(ctx.TenantID) == "" ||
		strings.TrimSpace(ctx.Conversation.ID) == "" ||
		(strings.TrimSpace(ctx.Conversation.Type) != "single" && strings.TrimSpace(ctx.Conversation.Type) != "group") ||
		strings.TrimSpace(ctx.Sender.ID) == "" ||
		strings.TrimSpace(ctx.Sender.TenantID) != strings.TrimSpace(ctx.TenantID) ||
		strings.TrimSpace(ctx.Message.ID) == "" {
		writeError(w, http.StatusBadRequest, "invalid channelContext")
		return
	}
	if strings.TrimSpace(ctx.Conversation.Type) == "single" && strings.TrimSpace(ctx.Sender.StaffID) == "" {
		writeError(w, http.StatusBadRequest, "channelContext.sender.staffId is required for single chat")
		return
	}
	if req.Continuation == nil {
		agentID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(req.AgentID), "agentId")
		if !ok {
			return
		}
		if agentID != dispatchContext.AgentID {
			writeError(w, http.StatusForbidden, "agentId does not match dispatch endpoint")
			return
		}
	} else if req.Continuation.Kind != "chat" || strings.TrimSpace(req.Continuation.ChatSessionID) == "" {
		writeError(w, http.StatusBadRequest, "continuation must identify a chat")
		return
	}

	installation, err := h.Queries.GetActiveDingTalkHTTPInstallationForDispatch(r.Context(), db.GetActiveDingTalkHTTPInstallationForDispatchParams{
		WorkspaceID:        dispatchContext.WorkspaceID,
		AgentID:            dispatchContext.AgentID,
		DispatchEndpointID: dispatchContext.EndpointID,
		RobotCode:          strings.TrimSpace(ctx.AccountID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "dingtalk robot installation not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to resolve dingtalk robot installation")
		}
		return
	}
	if installation.Status != "active" ||
		installation.WorkspaceID != dispatchContext.WorkspaceID ||
		installation.AgentID != dispatchContext.AgentID {
		writeError(w, http.StatusForbidden, "dingtalk robot installation does not match dispatch endpoint")
		return
	}
	robotInstallation, err := h.DingTalkInstallations.GetInWorkspace(r.Context(), installation.ID, dispatchContext.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load dingtalk robot installation")
		return
	}
	if robotInstallation.TransportMode != dingtalk.TransportModeHTTPCallback ||
		!strings.EqualFold(strings.TrimSpace(robotInstallation.RobotCode), strings.TrimSpace(ctx.AccountID)) {
		writeError(w, http.StatusForbidden, "dingtalk robot installation is not an HTTP callback source")
		return
	}

	message, err := dingtalk.InboundFromHTTPCallback(dingtalk.HTTPCallbackMessage{
		ConversationID:       ctx.Conversation.ID,
		ConversationType:     ctx.Conversation.Type,
		ConversationTitle:    ctx.Conversation.Title,
		MessageID:            ctx.Message.ID,
		CreatedAt:            ctx.Message.CreatedAt,
		SenderUID:            ctx.Sender.ID,
		SenderOrgID:          ctx.Sender.TenantID,
		SenderStaffID:        ctx.Sender.StaffID,
		SenderName:           ctx.Sender.Name,
		Text:                 ctx.Message.Text,
		IdentityContextToken: agentDispatchContextToken(req),
		IdentityContextTokenExpiresAt: agentDispatchContextTokenExpiresAt(req),
	}, robotInstallation.ClientID, uuidToString(installation.ID))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.ChannelRouter.HandleResult(r.Context(), message)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to dispatch dingtalk chat")
		return
	}
	if writeAgentChatNeedsBindingACK(w, result) {
		return
	}
	chatSessionID := uuidToString(result.ChatSessionID)
	if chatSessionID == "" && req.Continuation != nil {
		chatSessionID = req.Continuation.ChatSessionID
	}
	writeJSON(w, http.StatusAccepted, AgentChatDispatchResponse{
		Continuation: AgentDispatchContinuation{Kind: "chat", ChatSessionID: chatSessionID},
		TaskID:       uuidToString(result.TaskID),
	})
}

func writeAgentChatNeedsBindingACK(w http.ResponseWriter, result engine.Result) bool {
	if result.Outcome != engine.OutcomeNeedsBinding {
		return false
	}
	// The binding prompt is sent asynchronously by the DingTalk replier.
	// Router only needs a successful delivery ACK; an error makes it retry an
	// event that Multica has already accepted and handled.
	w.WriteHeader(http.StatusAccepted)
	return true
}

func agentDispatchContextToken(req AgentDispatchRequest) string {
	if req.ExternalIdentity == nil {
		return ""
	}
	return req.ExternalIdentity.ContextToken
}

func agentDispatchContextTokenExpiresAt(req AgentDispatchRequest) int64 {
	if req.ExternalIdentity == nil {
		return 0
	}
	return req.ExternalIdentity.ExpiresAt
}

func agentDispatchIdentityExpiryContext(req AgentDispatchRequest) []byte {
	expiresAt := agentDispatchContextTokenExpiresAt(req)
	if expiresAt <= 0 {
		return nil
	}
	raw, _ := json.Marshal(map[string]any{
		protocol.AgentIdentityContextTokenExpiresAtJSONKey: expiresAt,
		protocol.AgentIdentityContextTokenSourceJSONKey:    protocol.AgentIdentityContextTokenSourceExternal,
	})
	return raw
}

type agentDispatchContext struct {
	EndpointID          string
	EndpointNamespaceID pgtype.UUID
	UserID              pgtype.UUID
	WorkspaceID         pgtype.UUID
	AgentID             pgtype.UUID
}

func (h *Handler) resolveAgentDispatchContext(w http.ResponseWriter, r *http.Request) (agentDispatchContext, bool) {
	endpointID := strings.TrimSpace(chi.URLParam(r, "endpointId"))
	deliverySecret, ok := agentDispatchBearerSecret(r.Header.Get("Authorization"))
	if !ok {
		h.Metrics.RecordDispatchAuth("missing_credential")
		logAgentDispatchAuthFailure("missing_credential", endpointID)
		writeError(w, http.StatusUnauthorized, "invalid dispatch credentials")
		return agentDispatchContext{}, false
	}
	if h.AgentDispatchKeys == nil || !h.AgentDispatchKeys.VerifyDeliverySecret(endpointID, deliverySecret) {
		h.Metrics.RecordDispatchAuth("invalid_credential")
		logAgentDispatchAuthFailure("invalid_credential", endpointID)
		writeError(w, http.StatusUnauthorized, "invalid dispatch credentials")
		return agentDispatchContext{}, false
	}
	endpoint, err := h.Queries.GetAgentDispatchEndpointByEndpointID(r.Context(), endpointID)
	if err == nil {
		h.Metrics.RecordDispatchAuth("success")
		return agentDispatchContext{
			EndpointID:          endpointID,
			EndpointNamespaceID: endpoint.ID,
			UserID:              endpoint.ActorUserID,
			WorkspaceID:         endpoint.WorkspaceID,
			AgentID:             endpoint.AgentID,
		}, true
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		h.Metrics.RecordDispatchAuth("storage_error")
		slog.Error("MULTICA_AGENT_DISPATCH_AUTH", "outcome", "storage_error",
			"endpointKeyId", agentDispatchEndpointKeyID(endpointID),
			"endpointFingerprint", agentDispatchEndpointFingerprint(endpointID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve dispatch endpoint")
		return agentDispatchContext{}, false
	}
	// Rolling-upgrade fallback for endpoints created before the Agent-scoped
	// endpoint table. New HTTP_CALLBACK installs always resolve above.
	legacy, err := h.Queries.GetActiveDingTalkAccountBindingByEndpoint(r.Context(), endpointID)
	if errors.Is(err, pgx.ErrNoRows) {
		legacy, err = h.Queries.GetActiveDingTalkBotInstallationByEndpoint(r.Context(), endpointID)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.Metrics.RecordDispatchAuth("endpoint_not_found")
			logAgentDispatchAuthFailure("endpoint_not_found", endpointID)
			writeError(w, http.StatusUnauthorized, "invalid dispatch credentials")
		} else {
			h.Metrics.RecordDispatchAuth("storage_error")
			slog.Error("MULTICA_AGENT_DISPATCH_AUTH", "outcome", "storage_error",
				"endpointKeyId", agentDispatchEndpointKeyID(endpointID),
				"endpointFingerprint", agentDispatchEndpointFingerprint(endpointID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to resolve dispatch endpoint")
		}
		return agentDispatchContext{}, false
	}
	h.Metrics.RecordDispatchAuth("success")
	return agentDispatchContext{
		EndpointID:  endpointID,
		UserID:      legacy.InstallerUserID,
		WorkspaceID: legacy.WorkspaceID,
		AgentID:     legacy.AgentID,
	}, true
}

func logAgentDispatchAuthFailure(outcome, endpointID string) {
	slog.Warn("MULTICA_AGENT_DISPATCH_AUTH",
		"outcome", outcome,
		"endpointKeyId", agentDispatchEndpointKeyID(endpointID),
		"endpointFingerprint", agentDispatchEndpointFingerprint(endpointID),
	)
}

func agentDispatchEndpointKeyID(endpointID string) string {
	keyID, suffix, ok := strings.Cut(strings.TrimSpace(endpointID), "_")
	if !ok || keyID == "" || suffix == "" {
		return "invalid"
	}
	return keyID
}

func agentDispatchEndpointFingerprint(endpointID string) string {
	return agentDispatchIdentifierFingerprint(endpointID)
}

func agentDispatchIdentifierFingerprint(identifier string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(identifier)))
	return base64.RawURLEncoding.EncodeToString(digest[:])[:12]
}

func agentDispatchBearerSecret(authorization string) (string, bool) {
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
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

func (h *Handler) createAgentDispatchIssue(w http.ResponseWriter, r *http.Request, req AgentDispatchRequest, userID, workspaceID pgtype.UUID, agent db.Agent) {
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
		WorkspaceID:               workspaceID,
		Title:                     agentDispatchIssueTitle(req.Input.UserPrompt.Text),
		Description:               pgtype.Text{String: buildAgentDispatchContent(req.Input), Valid: true},
		Status:                    "todo",
		Priority:                  "none",
		AssigneeType:              pgtype.Text{String: "agent", Valid: true},
		AssigneeID:                agent.ID,
		CreatorType:               "member",
		CreatorID:                 userID,
		AttachmentIDs:             attachmentIDs(imported),
		AllowDuplicate:            true,
		AgentIdentityContextToken: agentDispatchContextToken(req),
		DispatchContext:           agentDispatchIdentityExpiryContext(req),
	}, service.IssueCreateOpts{
		ActorID:          uuidToString(userID),
		AnalyticsAgentID: uuidToString(agent.ID),
		Platform:         "webhook",
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

func (h *Handler) createAgentDispatchComment(w http.ResponseWriter, r *http.Request, req AgentDispatchRequest, dispatchContext agentDispatchContext) {
	issueID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(req.Continuation.IssueID), "continuation.issueId")
	if !ok {
		return
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID: issueID, WorkspaceID: dispatchContext.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			agent, ok := h.resolveAgentDispatchAgent(
				w,
				r,
				dispatchContext.UserID,
				dispatchContext.WorkspaceID,
				dispatchContext.AgentID,
			)
			if !ok {
				return
			}
			h.createAgentDispatchIssue(w, r, req, dispatchContext.UserID, dispatchContext.WorkspaceID, agent)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load continuation issue")
		return
	}
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		writeError(w, http.StatusConflict, "continuation issue is not assigned to an agent")
		return
	}
	if issue.AssigneeID != dispatchContext.AgentID {
		writeError(w, http.StatusForbidden, "continuation issue does not match dispatch endpoint")
		return
	}
	_, ok = h.resolveAgentDispatchAgent(w, r, dispatchContext.UserID, dispatchContext.WorkspaceID, issue.AssigneeID)
	if !ok {
		return
	}

	attachmentService := service.NewExternalAttachmentService(h.Queries, h.Storage, h.AgentDispatchHTTPClient)
	imported, err := attachmentService.Import(r.Context(), service.ExternalAttachmentImportParams{
		WorkspaceID: dispatchContext.WorkspaceID,
		UploaderID:  dispatchContext.UserID,
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
		Issue:                     issue,
		AuthorID:                  dispatchContext.UserID,
		Content:                   buildAgentDispatchContent(req.Input),
		AttachmentIDs:             attachmentIDs(imported),
		AgentIdentityContextToken: agentDispatchContextToken(req),
		DispatchContext:           agentDispatchIdentityExpiryContext(req),
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
		writeError(w, http.StatusConflict, "issue already has a pending agent task; retry after it finishes")
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
