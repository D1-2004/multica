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
	GetAgentDispatchTaskIDByMessage(context.Context, db.GetAgentDispatchTaskIDByMessageParams) (pgtype.UUID, error)
}

func textValue(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: strings.TrimSpace(s) != ""}
}
func formatIssueNumber(n int32) string { return fmt.Sprint(n) }

type agentDispatchIssueCreateOverrides struct {
	Title                  string
	DisplayContent         string
	DispatchContext        []byte
	Metadata               []byte
	SystemLabelName        string
	SystemLabelDescription string
	SystemLabelColor       string
	ParentTaskID           pgtype.UUID
}

func buildAgentDispatchIssueCreateParams(
	command DispatchCommand,
	prompt DispatchPrompt,
	dispatchContext agentDispatchContext,
	agent db.Agent,
	idempotencyKey string,
	overrides agentDispatchIssueCreateOverrides,
) service.IssueCreateParams {
	title := strings.TrimSpace(overrides.Title)
	if title == "" {
		title = dispatchIssueTitle(command, idempotencyKey)
	}
	displayContent := overrides.DisplayContent
	if strings.TrimSpace(displayContent) == "" {
		displayContent = prompt.DisplayContent
	}
	privateContext := overrides.DispatchContext
	if len(privateContext) == 0 {
		privateContext = dispatchRuntimeContext(command, idempotencyKey)
	}
	return service.IssueCreateParams{
		WorkspaceID:               dispatchContext.WorkspaceID,
		Title:                     title,
		Description:               textValue(displayContent),
		Status:                    "todo",
		Priority:                  "none",
		AssigneeType:              textValue("agent"),
		AssigneeID:                agent.ID,
		CreatorType:               "member",
		CreatorID:                 dispatchContext.UserID,
		AllowDuplicate:            command.CompletionCallback != nil,
		AgentIdentityContextToken: command.ExternalIdentity.ContextToken,
		DispatchContext:           privateContext,
		Metadata:                  overrides.Metadata,
		SystemLabelName:           overrides.SystemLabelName,
		SystemLabelDescription:    overrides.SystemLabelDescription,
		SystemLabelColor:          overrides.SystemLabelColor,
		ParentTaskID:              overrides.ParentTaskID,
	}
}

func buildAgentDispatchIssueFollowUpParams(
	command DispatchCommand,
	prompt DispatchPrompt,
	dispatchContext agentDispatchContext,
	issue db.Issue,
	idempotencyKey string,
	privateContext []byte,
	parentTaskID pgtype.UUID,
) service.IssueCommentCreateParams {
	if len(privateContext) == 0 {
		privateContext = dispatchRuntimeContext(command, idempotencyKey)
	}
	return service.IssueCommentCreateParams{
		Issue:                     issue,
		AuthorID:                  dispatchContext.UserID,
		Content:                   prompt.DisplayContent,
		AgentIdentityContextToken: command.ExternalIdentity.ContextToken,
		DispatchContext:           privateContext,
		ParentTaskID:              parentTaskID,
	}
}

func dispatchRuntimeContext(c DispatchCommand, idempotencyKey string) []byte {
	// The token has its own dedicated private task-context field and must never
	// be duplicated in a JSON snapshot. The stable DWS descriptor is retained so
	// identity can be resolved immediately before a cloud sandbox starts.
	payload := map[string]any{
		"dispatch_schema_version":        c.SchemaVersion,
		"dispatch_source":                c.Source,
		"dispatch_domain":                c.Event.Domain,
		"dispatch_type":                  c.Event.Type,
		"dispatch_event_data":            c.Event.Data,
		protocol.DispatchSurfaceJSONKey:  c.Surface,
		protocol.DispatchOutboundJSONKey: c.Outbound,
		"dispatch_idempotency_key":       idempotencyKey,
	}
	if c.Control != nil {
		payload["dispatch_control"] = c.Control
	}
	if strings.TrimSpace(c.ContextPrompt) != "" {
		payload[protocol.DispatchContextPromptJSONKey] = c.ContextPrompt
	}
	if c.ExternalIdentity.ContextToken != "" {
		payload[protocol.AgentIdentityContextTokenExpiresAtJSONKey] = c.ExternalIdentity.ExpiresAt
		payload[protocol.AgentIdentityContextTokenSourceJSONKey] = protocol.AgentIdentityContextTokenSourceExternal
	}
	if c.ExternalIdentity.DWS != nil {
		payload["external_identity"] = struct {
			DWS *AgentDispatchDWSIdentity `json:"dws"`
		}{DWS: c.ExternalIdentity.DWS}
	}
	if c.DispatchEndpointID != "" {
		payload["dispatch_endpoint_id"] = c.DispatchEndpointID
	}
	if c.CompletionCallback != nil {
		callback := map[string]any{
			"url":    c.CompletionCallback.URL,
			"target": c.CompletionCallback.Target,
		}
		if c.CompletionCallback.UpdateURL != "" {
			callback["update_url"] = c.CompletionCallback.UpdateURL
		}
		if c.CompletionCallback.TelemetryURL != "" {
			callback["telemetry_url"] = c.CompletionCallback.TelemetryURL
			callback["telemetry_token"] = c.CompletionCallback.TelemetryToken
			callback["telemetry_expires_at"] = c.CompletionCallback.TelemetryExpiresAt
		}
		payload["completion_callback"] = callback
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
	command, err := bindDispatchCompletionTarget(command, h.TaskCompletionTargetIdentity)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "task completion delivery is not configured")
		return
	}
	if command.Control != nil && command.Control.Action == "cancel" {
		h.cancelAgentDispatchIMTask(w, r, command, dispatchContext)
		return
	}
	if shouldSkipApprovalDispatch(command) {
		slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
			"outcome", "skipped_auto_approve",
			"protocol", "dispatch_command_v2",
			"domain", command.Event.Domain,
			"eventType", command.Event.Type,
			"nodeType", "auto_approve",
			"processInstanceId", strings.TrimSpace(command.Event.Data.Approval.FormCode),
		)
		if command.CompletionCallback != nil && h.TaskService != nil {
			if err := h.TaskService.EnqueueSynchronousTaskCompletion(
				r.Context(),
				command.CompletionCallback.URL,
				command.CompletionCallback.Target,
				dispatchContext.AgentID,
				"approval auto_approve node — DingTalk engine handles auto-approval, no agent task needed",
				"auto_approve_skipped",
			); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to persist task completion")
				return
			}
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	command.DispatchEndpointID = uuidToString(dispatchContext.EndpointNamespaceID)
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

	if command.CompletionCallback == nil {
		h.executeAgentDispatchV2(w, r, command, plan, dispatchContext)
		return
	}
	idempotencyKey := dispatchIdempotencyKey(r, command)
	acceptance, replay, err := h.claimAgentDispatchAcceptance(
		r.Context(),
		command,
		dispatchContext,
		idempotencyKey,
	)
	if err != nil {
		switch {
		case errors.Is(err, errAgentDispatchAcceptanceConflict):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, errAgentDispatchAcceptancePending):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "failed to persist dispatch acceptance")
		}
		return
	}
	if replay {
		writeAgentDispatchAcceptanceReplay(w, acceptance)
		return
	}
	recovered, ok, err := h.recoverAgentDispatchAcceptance(
		r.Context(),
		command,
		dispatchContext,
		acceptance,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to recover dispatch acceptance")
		return
	}
	if ok {
		if err := h.completeAgentDispatchAcceptance(r.Context(), acceptance, recovered); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to finalize dispatch acceptance")
			return
		}
		recovered.Forward(w)
		return
	}

	buffered := newBufferedDispatchResponse()
	h.executeAgentDispatchV2(buffered, r, command, plan, dispatchContext)
	if buffered.Status() >= http.StatusOK && buffered.Status() < http.StatusMultipleChoices {
		if err := h.completeAgentDispatchAcceptance(r.Context(), acceptance, buffered); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to finalize dispatch acceptance")
			return
		}
	} else if buffered.Status() >= http.StatusBadRequest &&
		buffered.Status() < http.StatusInternalServerError {
		h.releaseAgentDispatchAcceptance(r.Context(), acceptance)
	}
	buffered.Forward(w)
}

func (h *Handler) executeAgentDispatchV2(
	w http.ResponseWriter,
	r *http.Request,
	command DispatchCommand,
	plan agentDispatchExecutionPlan,
	dispatchContext agentDispatchContext,
) {
	if plan.MaterializerType == protocol.DispatchSurfaceTypeChat {
		if command.Continuation != nil &&
			(command.Continuation.Kind != "chat" || strings.TrimSpace(command.Continuation.ChatSessionID) == "") {
			writeError(w, http.StatusBadRequest, "continuation must identify a chat")
			return
		}
		h.createAgentDispatchChatV2(w, r, command, plan, dispatchContext)
		return
	}

	// Approval event auto-linking: when the agent created the approval instance,
	// it wrote the current issue identifier (e.g. "WS-50") into the form field
	// named 关联Issue. The Router includes form values in AIReadableContent. If
	// we can recover the identifier and match it to an existing issue assigned to
	// this agent, redirect to a continuation (comment) on that issue instead of
	// creating a new one — preserving the original conversation context.
	if command.AgentID != "" &&
		command.Event.Domain == "approval" &&
		command.Event.Type == "approval.status_changed" &&
		command.Event.Data.Approval != nil {
		issuePrefix := h.getIssuePrefix(r.Context(), dispatchContext.WorkspaceID)
		rawContent := command.Event.Data.Approval.AIReadableContent
		identifier := extractIssueIdentifierFromApprovalContent(rawContent, issuePrefix)
		if identifier != "" {
			if issue, ok := h.lookupIssueByIdentifier(r.Context(), dispatchContext.WorkspaceID, issuePrefix, identifier); ok {
				if issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" &&
					uuidToString(issue.AssigneeID) == uuidToString(dispatchContext.AgentID) {
					command.Continuation = &AgentDispatchContinuation{
						Kind:    "issue",
						IssueID: uuidToString(issue.ID),
					}
					command.AgentID = "" // fall through to continuation path
					slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
						"outcome", "linked_approval_to_issue",
						"issueIdentifier", identifier,
						"processInstanceId", strings.TrimSpace(command.Event.Data.Approval.FormCode),
					)
				} else {
					slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
						"outcome", "approval_link_skipped_assignee_mismatch",
						"issueIdentifier", identifier,
						"issueAssigneeType", issue.AssigneeType.String,
						"issueAssigneeID", uuidToString(issue.AssigneeID),
						"dispatchAgentID", uuidToString(dispatchContext.AgentID),
					)
				}
			} else {
				slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
					"outcome", "approval_link_skipped_issue_not_found",
					"issueIdentifier", identifier,
					"issuePrefix", issuePrefix,
				)
			}
		} else {
			slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
				"outcome", "approval_link_skipped_no_identifier",
				"issuePrefix", issuePrefix,
				"aiReadableContentBytes", len(rawContent),
				"processInstanceId", strings.TrimSpace(command.Event.Data.Approval.FormCode),
			)
		}
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
	// "New Issue every time": an Agent may opt out of conversational Issue
	// threading so each inbound channel message becomes its own Issue instead
	// of a follow-up comment on the one the Router is still pointing at. The
	// Router keeps persisting and replaying the issue continuation; Multica
	// ignores it here, which is why this needs no Router-side change.
	//
	// Deliberately scoped to channel messages. The approval auto-link above
	// and the calendar contract rewrite a continuation to correlate a system
	// callback back to the Issue that produced it — that is not conversational
	// threading, and forcing a new Issue there would orphan every approval
	// status change from the approval it belongs to.
	if command.Event.Domain == "channel" &&
		h.agentAlwaysCreatesNewIssue(r.Context(), dispatchContext.WorkspaceID, dispatchContext.AgentID) {
		agent, ok := h.resolveAgentDispatchAgent(
			w, r, dispatchContext.UserID, dispatchContext.WorkspaceID, dispatchContext.AgentID)
		if !ok {
			return
		}
		slog.Info("MULTICA_AGENT_DISPATCH_CONTINUATION",
			"outcome", "always_new_issue_ignored_continuation",
			"previousIssueFingerprint", agentDispatchIdentifierFingerprint(command.Continuation.IssueID),
		)
		h.createAgentDispatchIssueV2(w, r, command, plan.Prompt, dispatchContext, agent)
		return
	}
	h.createAgentDispatchCommentV2(w, r, command, plan.Prompt, dispatchContext)
}

// agentAlwaysCreatesNewIssue reads the Agent's Issue-threading preference. It
// fails closed to the existing threading behavior: a lookup error must not
// turn one conversation into a stream of orphan Issues.
func (h *Handler) agentAlwaysCreatesNewIssue(ctx context.Context, workspaceID, agentID pgtype.UUID) bool {
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: agentID, WorkspaceID: workspaceID,
	})
	if err != nil {
		slog.Warn("agent dispatch: failed to read Issue-threading preference",
			"agent_id", uuidToString(agentID),
			"error", err,
		)
		return false
	}
	return agent.DispatchAlwaysNewIssue
}

func bindDispatchCompletionTarget(
	command DispatchCommand,
	targetIdentity string,
) (DispatchCommand, error) {
	if command.CompletionCallback == nil {
		return command, nil
	}
	targetIdentity = strings.TrimSpace(targetIdentity)
	if !routerCompletionTargetPattern.MatchString(targetIdentity) {
		return DispatchCommand{}, errors.New("task completion target is not configured")
	}
	callback := *command.CompletionCallback
	callback.Target = targetIdentity
	command.CompletionCallback = &callback
	return command, nil
}

// AgentDispatchV2Request preserves the exact Router JSON contract while the
// internal DispatchCommand owns validation and execution semantics.
type AgentDispatchV2Request struct {
	SchemaVersion      string                        `json:"schemaVersion"`
	AgentID            string                        `json:"agentId,omitempty"`
	Continuation       *AgentDispatchContinuation    `json:"continuation"`
	Source             DispatchSource                `json:"source"`
	Event              DispatchEvent                 `json:"event"`
	Surface            DispatchSurface               `json:"surface"`
	Outbound           DispatchOutbound              `json:"outbound"`
	Control            *DispatchControl              `json:"control,omitempty"`
	ContextPrompt      string                        `json:"contextPrompt,omitempty"`
	ExternalIdentity   AgentDispatchExternalIdentity `json:"externalIdentity"`
	CompletionCallback *DispatchCompletionCallback   `json:"completionCallback,omitempty"`
}

func (r AgentDispatchV2Request) DispatchCommand() DispatchCommand {
	return DispatchCommand{
		SchemaVersion:      r.SchemaVersion,
		AgentID:            r.AgentID,
		Continuation:       r.Continuation,
		Source:             r.Source,
		Event:              r.Event,
		Surface:            r.Surface,
		Outbound:           r.Outbound,
		Control:            r.Control,
		ContextPrompt:      r.ContextPrompt,
		ExternalIdentity:   r.ExternalIdentity,
		CompletionCallback: r.CompletionCallback,
	}
}

func (h *Handler) createAgentDispatchChatV2(
	w http.ResponseWriter,
	r *http.Request,
	command DispatchCommand,
	plan agentDispatchExecutionPlan,
	dispatchContext agentDispatchContext,
) {
	if h.ChannelRouter == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk chat dispatch not configured")
		return
	}

	var namespaceID pgtype.UUID
	var robotClientID string
	if plan.InstallationOverride != nil {
		namespaceID = plan.InstallationOverride.ID
	} else {
		if h.DingTalkInstallations == nil {
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
		namespaceID = row.ID
		robotClientID = installation.ClientID
	}

	var textParts []string
	identities := dispatchDisplayIdentitiesFrom(command)
	for _, message := range command.Event.Data.Messages {
		if message.Reaction != nil {
			// 表情条目的 text 是被反应消息的原文（可能是数字员工自己发的），
			// 不能当用户输入注入会话；用渲染句表达语义，与 issue/comment surface 一致。
			// 其 attachments 是被反应消息的快照，不触发 chat 附件拒绝。
			textParts = append(textParts, dispatchReactionDisplay(message))
			continue
		}
		if len(message.Attachments) > 0 {
			writeError(w, http.StatusUnprocessableEntity, "chat attachments are not supported yet")
			return
		}
		if value := dispatchMessageDisplay(message, identities); value != "" {
			textParts = append(textParts, value)
		}
	}
	latest := command.Event.Data.Messages[len(command.Event.Data.Messages)-1]
	senderID := strings.TrimSpace(command.Event.Data.Sender.OpenDingTalkID)
	if senderID == "" {
		senderID = strings.TrimSpace(command.Event.Data.Sender.SenderOpenDingTalkID)
	}
	dispatchText := strings.Join(textParts, "\n\n")
	dispatchMessage := dingtalk.AgentDispatchMessage{
		ConversationID:                command.Event.Data.Conversation.OpenConversationID,
		ConversationType:              command.Event.Data.Conversation.Type,
		ConversationTitle:             command.Event.Data.Conversation.Title,
		MessageID:                     latest.OpenMsgID,
		CreatedAt:                     latest.OccurredAt,
		SenderID:                      senderID,
		SenderStaffID:                 command.Event.Data.Sender.StaffID,
		SenderName:                    command.Event.Data.Sender.DisplayName,
		Text:                          dispatchText,
		IdentityContextToken:          command.ExternalIdentity.ContextToken,
		IdentityContextTokenExpiresAt: command.ExternalIdentity.ExpiresAt,
		DispatchContext:               dispatchRuntimeContext(command, dispatchIdempotencyKey(r, command)),
	}
	var message channel.InboundMessage
	var err error
	if plan.InstallationOverride != nil {
		message, err = dingtalk.InboundFromAgentDispatch(dispatchMessage)
	} else {
		message, err = dingtalk.InboundFromHTTPCallback(
			dingtalk.HTTPCallbackMessage(dispatchMessage),
			robotClientID,
			uuidToString(namespaceID),
		)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Dispatch Command V2 already selected the surface. Preserve slash-prefixed
	// input as prompt content instead of letting the channel adapter reinterpret
	// it as /new, /reset, /issue, or /unbind.
	message.Text = dispatchText
	message.ForceFresh = false
	options := plan.channelHandleOptions()
	if command.Continuation == nil {
		options.CreateUnboundSession = true
	} else {
		chatSessionID, ok := parseUUIDOrBadRequest(
			w, strings.TrimSpace(command.Continuation.ChatSessionID), "continuation.chatSessionId")
		if !ok {
			return
		}
		session, loadErr := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
			ID: chatSessionID, WorkspaceID: dispatchContext.WorkspaceID,
		})
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "chat continuation not found")
			} else {
				writeError(w, http.StatusInternalServerError, "failed to load chat continuation")
			}
			return
		}
		if session.AgentID != dispatchContext.AgentID {
			writeError(w, http.StatusForbidden, "chat continuation belongs to another agent")
			return
		}
		if session.CreatorID != dispatchContext.UserID {
			writeError(w, http.StatusForbidden, "chat continuation belongs to another endpoint actor")
			return
		}
		if session.Status != "active" {
			writeError(w, http.StatusBadRequest, "chat continuation is archived")
			return
		}
		options.ChatSessionOverride = &chatSessionID
	}
	result, err := h.ChannelRouter.HandleResultWithOptions(r.Context(), message, options)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to dispatch dingtalk chat")
		return
	}
	if h.writeAgentChatNeedsBindingACKV2(w, r.Context(), command, dispatchContext, result) {
		return
	}
	if result.Outcome == engine.OutcomeDropped && result.DropReason == engine.DropReasonDuplicate {
		if command.CompletionCallback != nil {
			writeError(w, http.StatusConflict, "message was already accepted under another dispatch")
			return
		}
		response, recoverErr := recoverDuplicateAgentChatDispatch(
			r.Context(), h.Queries, namespaceID, latest.OpenMsgID, message)
		if recoverErr != nil {
			writeError(w, http.StatusServiceUnavailable, errAgentDispatchDuplicateNotReady.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, response)
		return
	}
	if h.writeAgentChatNoTaskOutcomeV2(w, r.Context(), command, dispatchContext, result) {
		return
	}
	chatSessionID := uuidToString(result.ChatSessionID)
	if chatSessionID == "" && command.Continuation != nil {
		chatSessionID = command.Continuation.ChatSessionID
	}
	response := AgentChatDispatchResponse{
		Continuation: AgentDispatchContinuation{Kind: "chat", ChatSessionID: chatSessionID},
		TaskID:       uuidToString(result.TaskID),
	}
	if command.Control != nil && command.Control.Action == "dispatch" && command.Control.QueueMode == "steer" {
		if h.TaskService == nil {
			writeError(w, http.StatusServiceUnavailable, "IM task control is not configured")
			return
		}
		target, preempted, err := h.TaskService.SteerAgentDispatchChatTask(
			r.Context(), result.TaskID, result.ChatSessionID, dispatchContext.AgentID,
		)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		response.TaskID = uuidToString(target.ID)
		response.ControlResult = &AgentDispatchControlResult{
			Action:               "steer",
			Status:               "queued",
			TargetExternalTaskID: response.TaskID,
		}
		if preempted != nil {
			response.ControlResult.PreemptedExternalTaskID = uuidToString(preempted.ID)
		}
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (h *Handler) cancelAgentDispatchIMTask(
	w http.ResponseWriter,
	r *http.Request,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
) {
	targetID := parseUUID(command.Control.TargetExternalTaskID)
	task, err := h.Queries.GetAgentTask(r.Context(), targetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "target IM task not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load target IM task")
		}
		return
	}
	if task.AgentID != dispatchContext.AgentID || !task.ChatSessionID.Valid ||
		uuidToString(task.ChatSessionID) != command.Continuation.ChatSessionID {
		writeError(w, http.StatusForbidden, "target task does not belong to the IM dispatch session")
		return
	}
	switch task.Status {
	case "completed", "failed", "cancelled":
		writeJSON(w, http.StatusOK, AgentDispatchControlResponse{ControlResult: AgentDispatchControlResult{
			Action:               "cancel",
			Status:               "already_terminal",
			TargetExternalTaskID: uuidToString(task.ID),
		}})
		return
	case "dispatched", "running", "waiting_local_directory":
	default:
		writeError(w, http.StatusConflict, "target IM task is not active")
		return
	}
	if h.TaskService == nil {
		writeError(w, http.StatusServiceUnavailable, "IM task control is not configured")
		return
	}
	cancelled, err := h.TaskService.CancelTaskWithResult(r.Context(), targetID, service.CancelTaskOptions{
		ClientSupportsDraftRestore: true,
		PreserveChatInput:          true,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to cancel target IM task")
		return
	}
	status := "already_terminal"
	if cancelled.Task.Status == "cancelled" {
		status = "cancelled"
	}
	writeJSON(w, http.StatusOK, AgentDispatchControlResponse{ControlResult: AgentDispatchControlResult{
		Action:               "cancel",
		Status:               status,
		TargetExternalTaskID: uuidToString(cancelled.Task.ID),
	}})
}

func (h *Handler) writeAgentChatNoTaskOutcomeV2(
	w http.ResponseWriter,
	ctx context.Context,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
	result engine.Result,
) bool {
	if command.CompletionCallback == nil || result.TaskID.Valid {
		return false
	}
	var errMessage, failureReason string
	switch result.Outcome {
	case engine.OutcomeAgentOffline:
		errMessage = "agent is offline"
		failureReason = string(engine.OutcomeAgentOffline)
	case engine.OutcomeAgentArchived:
		errMessage = "agent is archived"
		failureReason = string(engine.OutcomeAgentArchived)
	default:
		writeError(w, http.StatusUnprocessableEntity, "dispatch did not create a task")
		return true
	}
	if h.TaskService == nil {
		writeError(w, http.StatusInternalServerError, "task completion callback is not configured")
		return true
	}
	if err := h.TaskService.EnqueueSynchronousTaskCompletion(
		ctx,
		command.CompletionCallback.URL,
		command.CompletionCallback.Target,
		dispatchContext.AgentID,
		errMessage,
		failureReason,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist task completion")
		return true
	}
	w.WriteHeader(http.StatusAccepted)
	return true
}

func (h *Handler) writeAgentChatNeedsBindingACKV2(
	w http.ResponseWriter,
	ctx context.Context,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
	result engine.Result,
) bool {
	if result.Outcome != engine.OutcomeNeedsBinding {
		return false
	}
	if command.CompletionCallback == nil {
		return writeAgentChatNeedsBindingACK(w, result)
	}
	if h.TaskService == nil {
		writeError(w, http.StatusInternalServerError, "task completion callback is not configured")
		return true
	}
	if err := h.TaskService.EnqueueSynchronousTaskCompletion(
		ctx,
		command.CompletionCallback.URL,
		command.CompletionCallback.Target,
		dispatchContext.AgentID,
		"dingtalk account binding required",
		"needs_binding",
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist task completion")
		return true
	}
	w.WriteHeader(http.StatusAccepted)
	return true
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
	taskID, err := queries.GetAgentDispatchTaskIDByMessage(ctx, db.GetAgentDispatchTaskIDByMessageParams{
		ChatSessionID: binding.ChatSessionID,
		MessageID:     messageID,
	})
	if err != nil || !taskID.Valid {
		return AgentChatDispatchResponse{}, errAgentDispatchDuplicateNotReady
	}
	return AgentChatDispatchResponse{
		Continuation: AgentDispatchContinuation{
			Kind:          "chat",
			ChatSessionID: uuidToString(binding.ChatSessionID),
		},
		TaskID: uuidToString(taskID),
	}, nil
}

func (h *Handler) createAgentDispatchIssueV2(w http.ResponseWriter, r *http.Request, c DispatchCommand, prompt DispatchPrompt, dispatchContext agentDispatchContext, agent db.Agent) {
	attachments := make([]AgentDispatchAttachment, 0)
	for _, m := range c.Event.Data.Messages {
		if m.Reaction != nil {
			// 表情条目的附件是被反应消息的快照，不作为新附件导入。
			continue
		}
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

	createParams := buildAgentDispatchIssueCreateParams(
		c,
		prompt,
		dispatchContext,
		agent,
		dispatchIdempotencyKey(r, c),
		agentDispatchIssueCreateOverrides{},
	)
	createParams.AttachmentIDs = attachmentIDs(imported)
	result, err := h.IssueService.Create(r.Context(), createParams, service.IssueCreateOpts{
		ActorID:          uuidToString(dispatchContext.UserID),
		AnalyticsAgentID: uuidToString(agent.ID),
		Platform:         "webhook",
	})
	if errors.Is(err, service.ErrActiveDuplicate) {
		writeError(w, http.StatusConflict, service.ErrActiveDuplicate.Error())
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
		if m.Reaction != nil {
			// 表情条目的附件是被反应消息的快照，不作为新附件导入。
			continue
		}
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
	followUpParams := buildAgentDispatchIssueFollowUpParams(
		c,
		prompt,
		dispatchContext,
		issue,
		dispatchIdempotencyKey(r, c),
		nil,
		pgtype.UUID{},
	)
	followUpParams.AttachmentIDs = attachmentIDs(imported)
	result, err := h.IssueCommentService.CreateExternalFollowUp(
		r.Context(),
		followUpParams,
		service.IssueCommentCreateOpts{},
	)
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
