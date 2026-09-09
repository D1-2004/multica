package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/service/scenememory"
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
		AllowDuplicate:            true,
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
	if c.ResponsePolicy != nil {
		payload["dispatch_response_policy"] = c.ResponsePolicy
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
		if c.CompletionCallback.ResponseURL != "" {
			callback["response_url"] = c.CompletionCallback.ResponseURL
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
	if managedDingTalkResponse(command) && (h.DingTalkResponses == nil || h.InboundCoordinatorWorker == nil) {
		writeError(w, http.StatusServiceUnavailable, "managed DingTalk response service is unavailable")
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
	if h.InboundCoordinatorWorker != nil &&
		shouldDeferInboundCoordinator(command, plan) &&
		!dispatchIsAgentSelfMessage(command) &&
		!dispatchIsAgentSelfEmotion(command) {
		response, _, enqueueErr := h.enqueueInboundCoordinatorJob(
			r.Context(), acceptance, command, dispatchContext, idempotencyKey, plan.Prompt.DisplayContent,
		)
		if enqueueErr != nil {
			h.releaseAgentDispatchAcceptance(r.Context(), acceptance)
			writeError(w, http.StatusInternalServerError, "failed to persist inbound coordinator job")
			return
		}
		if h.InboundCoordinatorWorker != nil {
			h.InboundCoordinatorWorker.Notify()
		}
		response.Forward(w)
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
	h.recordAssocInboundEvent(r.Context(), command, dispatchContext)

	if h.tryDispatchResetMemory(w, r, command, dispatchContext) {
		return
	}

	if dispatchIsAgentSelfMessage(command) || dispatchIsAgentSelfEmotion(command) {
		slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
			"outcome", "skipped_self_inbound",
			"protocol", "dispatch_command_v2",
			"eventType", command.Event.Type,
			"sourceType", command.Source.Type,
		)
		decision := inboundcoord.Decision{Action: inboundcoord.ActionSilence, Source: coordinatorSource(command)}
		inboundcoord.RecordDecision(r.Context(), decision)
		if writeDispatchCoordinatorTerminal(w, r.Context(), h, command, dispatchContext, decision) {
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if plan.MaterializerType == protocol.DispatchSurfaceTypeChat {
		_, hasSavedPlan := inboundcoord.RestoredPlan(r.Context())
		coordinatorOn, coordinatorFlagErr := h.Queries.GetAgentInboundCoordinator(r.Context(), dispatchContext.AgentID)
		if inboundcoord.HasPlanCheckpoint(r.Context()) && (hasSavedPlan || (coordinatorFlagErr == nil && coordinatorOn)) && command.Event.Domain == "channel" && command.Event.Type == "message.created" {
			agent, ok := h.resolveAgentDispatchAgent(w, r, dispatchContext.UserID, dispatchContext.WorkspaceID, dispatchContext.AgentID)
			if !ok {
				return
			}
			h.createAgentDispatchIssueV2(w, r, command, plan.Prompt, dispatchContext, agent)
			return
		}
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
	targetIdentity = strings.TrimSpace(targetIdentity)
	if command.CompletionCallback == nil && len(command.ExtraCompletionCallbacks) == 0 {
		return command, nil
	}
	if !routerCompletionTargetPattern.MatchString(targetIdentity) {
		return DispatchCommand{}, errors.New("task completion target is not configured")
	}
	if command.CompletionCallback != nil {
		callback := *command.CompletionCallback
		callback.Target = targetIdentity
		command.CompletionCallback = &callback
	}
	for i := range command.ExtraCompletionCallbacks {
		command.ExtraCompletionCallbacks[i].Target = targetIdentity
	}
	return command, nil
}

// AgentDispatchV2Request preserves the exact Router JSON contract while the
// internal DispatchCommand owns validation and execution semantics.
type AgentDispatchV2Request struct {
	SchemaVersion      string                           `json:"schemaVersion"`
	AgentID            string                           `json:"agentId,omitempty"`
	Continuation       *AgentDispatchContinuation       `json:"continuation"`
	Source             DispatchSource                   `json:"source"`
	Event              DispatchEvent                    `json:"event"`
	Surface            DispatchSurface                  `json:"surface"`
	Outbound           DispatchOutbound                 `json:"outbound"`
	Control            *DispatchControl                 `json:"control,omitempty"`
	ContextPrompt      string                           `json:"contextPrompt,omitempty"`
	ResponsePolicy     *protocol.DingTalkResponsePolicy `json:"responsePolicy,omitempty"`
	ExternalIdentity   AgentDispatchExternalIdentity    `json:"externalIdentity"`
	CompletionCallback *DispatchCompletionCallback      `json:"completionCallback,omitempty"`
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
		ResponsePolicy:     r.ResponsePolicy,
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
	assocIDs := dispatchAssocIDs(command)
	senderID := strings.TrimSpace(command.Event.Data.Sender.OpenDingTalkID)
	if senderID == "" {
		senderID = strings.TrimSpace(command.Event.Data.Sender.SenderOpenDingTalkID)
	}
	if senderID == "" {
		senderID = assocIDs.PersonID
	}
	conversationID := dispatchChatConversationID(command, assocIDs)
	messageID := strings.TrimSpace(latest.OpenMsgID)
	if messageID == "" {
		messageID = assocIDs.EvidenceID
	}
	conversationType := strings.TrimSpace(command.Event.Data.Conversation.Type)
	if conversationType == "" {
		conversationType = assocIDs.Kind
	}
	dispatchText := strings.Join(textParts, "\n\n")
	dispatchMessage := dingtalk.AgentDispatchMessage{
		ConversationID:                conversationID,
		ConversationType:              conversationType,
		ConversationTitle:             command.Event.Data.Conversation.Title,
		MessageID:                     messageID,
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
		if errors.Is(loadErr, pgx.ErrNoRows) {
			// Router can replay a Chat ID after the session was deleted.
			// Fail closed on ownership/archived, but missing is stale state:
			// start a new unbound session so the inbound is not dropped.
			slog.Info("chat continuation missing; starting a new unbound session",
				"event", "agent_dispatch_chat_continuation_missing",
				"continuation_chat_session_id", uuidToString(chatSessionID),
			)
			options.CreateUnboundSession = true
		} else if loadErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to load chat continuation")
			return
		} else {
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
	}
	// A durable coordinator job pins its id as the turn's trace id; give the
	// channel engine that id as the inbound chat trace so the coordinator
	// trace, the task's chat trace, and the Scene Memory trigger recorded at
	// enqueue time all resolve to the same identifier.
	if traceID := inboundcoord.TraceIDFromContext(r.Context()); traceID != "" && strings.TrimSpace(message.TraceID) == "" {
		message.TraceID = traceID
		message.TraceChannel = string(message.Source.ChannelType)
		message.TraceStartedAtUnixMS = time.Now().UnixMilli()
	}
	result, err := h.ChannelRouter.HandleResultWithOptions(r.Context(), message, options)
	if err != nil {
		if errors.Is(err, service.ErrIssueDispatchPending) {
			writeError(w, http.StatusConflict, "recalled issue already has a pending agent task")
			return
		}
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
	if h.writeAgentChatCoordinatorOutcomeV2(w, r.Context(), command, dispatchContext, result) {
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

func (h *Handler) writeAgentChatCoordinatorOutcomeV2(
	w http.ResponseWriter,
	ctx context.Context,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
	result engine.Result,
) bool {
	if result.CoordinatorIssue {
		if !result.IssueID.Valid || !result.TaskID.Valid || strings.TrimSpace(result.ReplyText) == "" {
			writeError(w, http.StatusInternalServerError, "coordinator issue result is incomplete")
			return true
		}
		if command.CompletionCallback != nil {
			if err := h.enqueueCoordinatorIssueAckOrComplete(
				ctx,
				command,
				dispatchContext,
				dispatchContext.AgentID,
				db.AgentTaskQueue{ID: result.TaskID, AgentID: dispatchContext.AgentID},
				db.Issue{ID: result.IssueID},
				result.IssueIdentifier,
				result.ReplyText,
			); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to persist coordinator issue reply")
				return true
			}
		}
		writeJSON(w, http.StatusAccepted, AgentChatDispatchResponse{
			Continuation: AgentDispatchContinuation{Kind: "issue", IssueID: uuidToString(result.IssueID)},
			TaskID:       uuidToString(result.TaskID),
		})
		return true
	}
	if command.CompletionCallback == nil {
		if result.Outcome == engine.OutcomeCoordinatorReply || result.Outcome == engine.OutcomeCoordinatorSilence {
			chatSessionID := uuidToString(result.ChatSessionID)
			if chatSessionID == "" && command.Continuation != nil {
				chatSessionID = command.Continuation.ChatSessionID
			}
			writeJSON(w, http.StatusAccepted, AgentChatDispatchResponse{
				Continuation: AgentDispatchContinuation{Kind: "chat", ChatSessionID: chatSessionID},
				TaskID:       uuidToString(result.TaskID),
			})
			return true
		}
		return false
	}
	switch result.Outcome {
	case engine.OutcomeCoordinatorReply:
		visible := stripReplyDecisionLeak(result.ReplyText)
		if visible == "" {
			if err := h.TaskService.EnqueueSynchronousSilence(
				ctx,
				command.CompletionCallback.URL,
				command.CompletionCallback.Target,
				dispatchContext.AgentID,
			); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to persist coordinator silence")
				return true
			}
			w.WriteHeader(http.StatusAccepted)
			return true
		}
		if err := h.TaskService.EnqueueSynchronousCompleted(
			ctx,
			command.CompletionCallback.URL,
			command.CompletionCallback.Target,
			dispatchContext.AgentID,
			visible,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist coordinator reply")
			return true
		}
		chatSessionID := uuidToString(result.ChatSessionID)
		writeJSON(w, http.StatusAccepted, AgentChatDispatchResponse{
			Continuation: AgentDispatchContinuation{Kind: "chat", ChatSessionID: chatSessionID},
		})
		return true
	case engine.OutcomeCoordinatorSilence:
		if err := h.TaskService.EnqueueSynchronousSilence(
			ctx,
			command.CompletionCallback.URL,
			command.CompletionCallback.Target,
			dispatchContext.AgentID,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist coordinator silence")
			return true
		}
		w.WriteHeader(http.StatusAccepted)
		return true
	}
	if result.IssueID.Valid && result.TaskID.Valid && result.ReplyText != "" &&
		command.CompletionCallback.UpdateURL != "" {
		issue, err := h.Queries.GetIssue(ctx, result.IssueID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load coordinator issue")
			return true
		}
		task, err := h.Queries.GetAgentTask(ctx, result.TaskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load coordinator issue task")
			return true
		}
		if err := h.TaskService.EnqueueCoordinatorIssueAck(
			ctx,
			task,
			issue,
			result.IssueIdentifier,
			command.CompletionCallback.UpdateURL,
			command.CompletionCallback.Target,
			result.ReplyText,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist coordinator issue ack")
			return true
		}
		writeJSON(w, http.StatusAccepted, AgentChatDispatchResponse{
			Continuation: AgentDispatchContinuation{Kind: "issue", IssueID: uuidToString(result.IssueID)},
			TaskID:       uuidToString(result.TaskID),
		})
		return true
	}
	return false
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
	decision := inboundcoord.Decision{Action: inboundcoord.ActionContinue, Reason: "attachment_execution_path"}
	hasAttachments := false
	for _, message := range c.Event.Data.Messages {
		if message.Reaction == nil && len(message.Attachments) > 0 {
			hasAttachments = true
		}
	}
	if !hasAttachments {
		decision = decideDispatchCoordinator(
			r.Context(), h, c, agent, prompt.DisplayContent,
			dispatchContext.UserID,
			dispatchRuntimeContext(c, dispatchIdempotencyKey(r, c)),
		)
	}
	// Publish committed results after materialization, not just the model verdict.
	defer func() { inboundcoord.RecordDecision(r.Context(), decision) }()
	if decision.Action == inboundcoord.ActionDeferred {
		writeError(w, http.StatusServiceUnavailable, "coordinator has not decided this window; retry without executing")
		return
	}
	if decision.Action == inboundcoord.ActionRetry {
		writeError(w, http.StatusConflict, "recalled issue already has a pending agent task")
		return
	}
	if continuedCommand, continuedPrompt, ok := coordinatorRecalledIssueContinuation(c, prompt, decision); ok {
		h.createAgentDispatchCommentWithCoordinatorV2(w, r, continuedCommand, continuedPrompt, dispatchContext, &decision)
		return
	}
	if decision.Action == inboundcoord.ActionReply || decision.Action == inboundcoord.ActionSilence {
		if writeDispatchCoordinatorTerminal(w, r.Context(), h, c, dispatchContext, decision) {
			return
		}
	}
	// Classification is independent of sandbox capacity. Enforce capacity
	// only on execution, including the LLM-unavailable fallback.
	if decision.Action == inboundcoord.ActionContinue && c.CompletionCallback != nil &&
		c.Event.Domain == "channel" && c.Event.Type == "message.created" &&
		sceneWindowCreateSlots(r.Context(), h, dispatchContext.WorkspaceID, agent.ID, dispatchConversationID(c)) <= 0 {
		writeError(w, http.StatusConflict, "scene already has two in-flight matters")
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

	idempotencyKey := dispatchIdempotencyKey(r, c)
	items := decision.Items
	if decision.Action == inboundcoord.ActionIssue && len(items) == 0 {
		items = []inboundcoord.WindowItem{{
			Delegator: strings.TrimSpace(c.Event.Data.Sender.DisplayName),
			Purpose:   decision.Purpose,
			Intent:    decision.Intent,
			LookInto:  decision.LookInto,
		}}
	}
	if decision.Action == inboundcoord.ActionIssue && len(items) > 0 {
		for i := range items {
			if items[i].ActionKey == "" {
				items[i].ActionKey = fmt.Sprintf("item-%d", i+1)
			}
		}
		decision.Items = items
		pending := 0
		newNeeded := 0
		for i, item := range items {
			if planItemCompleted(decision, item.ActionKey) {
				continue
			}
			if pending >= inboundcoord.SceneWindowMaxItems {
				break
			}
			pending++
			key := windowItemKey(idempotencyKey, item, i)
			if item.IssueID != "" {
				_, lookupErr := h.Queries.GetExternalIssueFollowUpTaskID(r.Context(), db.GetExternalIssueFollowUpTaskIDParams{WorkspaceID: dispatchContext.WorkspaceID, AgentID: agent.ID, IdempotencyKey: key})
				if lookupErr == nil {
					continue
				}
				if !errors.Is(lookupErr, pgx.ErrNoRows) {
					writeError(w, 500, "cannot verify continuation admission")
					return
				}
			} else {
				itemCommand := windowItemCommand(c, item)
				if _, _, _, ok := h.lookupCoordinatorWindowItem(r.Context(), agent.ID, key, dispatchIdempotencyEndpointID(itemCommand, dispatchContext), dispatchContext.WorkspaceID); ok {
					continue
				}
			}
			newNeeded++
		}
		slots := sceneWindowCreateSlots(r.Context(), h, dispatchContext.WorkspaceID, agent.ID, dispatchConversationID(c))
		if newNeeded > slots {
			writeError(w, http.StatusConflict, "scene already has two in-flight matters")
			return
		}
		var firstIssueID, firstTaskID, firstIdentifier string
		var firstIssue db.Issue
		var firstTask db.AgentTaskQueue
		created := len(decision.CompletedActionKeys)
		if created > 0 && len(decision.IssueResults) > 0 {
			prior := decision.IssueResults[0]
			firstIssueID, firstTaskID, firstIdentifier = prior.IssueID, prior.TaskID, prior.IssueIdentifier
			firstIssue, err = h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: parseUUID(prior.IssueID), WorkspaceID: dispatchContext.WorkspaceID})
			if err != nil {
				writeError(w, 500, "cannot recover committed issue")
				return
			}
			firstTask, err = h.Queries.GetAgentTask(r.Context(), parseUUID(prior.TaskID))
			if err != nil {
				writeError(w, 500, "cannot recover committed task")
				return
			}
		}
		incomplete := false
		processed := 0
		usedEvidence := map[string]struct{}{}
		for i, item := range items {
			if planItemCompleted(decision, item.ActionKey) {
				continue
			}
			if processed >= inboundcoord.SceneWindowMaxItems {
				break
			}
			processed++
			itemDecision := decision
			itemDecision.Purpose = item.Purpose
			itemDecision.Intent = item.Intent
			itemDecision.LookInto = item.LookInto
			itemDecision.Items = []inboundcoord.WindowItem{item}
			overrides := agentDispatchIssueCreateOverrides{
				Title:          inboundcoord.IssueTitle(itemDecision, prompt.DisplayContent),
				DisplayContent: inboundcoord.IssueDescription(itemDecision, firstNonEmpty(item.Content, prompt.DisplayContent)),
			}
			itemKey := windowItemKey(idempotencyKey, item, i)
			itemCommand := windowItemCommand(c, item)
			bindWindowItemEvidence(&itemCommand, item, usedEvidence)
			if item.IssueID != "" {
				issue, task, result, followErr := h.materializeWindowContinuation(r.Context(), itemCommand, dispatchContext, item, itemKey, itemDecision)
				if followErr != nil {
					if errors.Is(followErr, service.ErrIssueDispatchPending) {
						writeError(w, 409, "recalled issue already has a pending agent task")
					} else {
						writeError(w, 500, "failed to commit coordinator continuation")
					}
					return
				}
				if err := recordPlanItem(r.Context(), &decision, item, result); err != nil {
					writeError(w, 500, "failed to checkpoint continuation")
					return
				}
				if created == 0 {
					firstIssueID, firstTaskID, firstIdentifier = result.IssueID, result.TaskID, result.IssueIdentifier
					firstIssue, firstTask = issue, task
				}
				created++
				continue
			}
			if existingIssue, existingTask, existingIdent, ok := h.lookupCoordinatorWindowItem(
				r.Context(), agent.ID, itemKey, dispatchIdempotencyEndpointID(itemCommand, dispatchContext), dispatchContext.WorkspaceID,
			); ok {
				keepAttachments = true
				if err := h.associateDispatchIssue(r.Context(), itemCommand, dispatchContext, uuidToString(existingIssue.ID), existingIssue.Title, uuidToString(existingTask.ID), item.Content, itemDecision); err != nil {
					writeError(w, 500, "failed to bind committed coordinator issue")
					return
				}
				if err := recordPlanItem(r.Context(), &decision, item, protocol.ChatCoordinatorIssueResult{Action: "issue_created", IssueID: uuidToString(existingIssue.ID), IssueIdentifier: existingIdent, IssueTitle: existingIssue.Title, TaskID: uuidToString(existingTask.ID)}); err != nil {
					writeError(w, 500, "failed to checkpoint recovered issue")
					return
				}
				if created == 0 {
					firstIssueID, firstTaskID, firstIdentifier = uuidToString(existingIssue.ID), uuidToString(existingTask.ID), existingIdent
					firstIssue, firstTask = existingIssue, existingTask
				}
				created++
				continue
			}
			independentContext, contextErr := inboundcoord.IndependentIssueTaskContext(
				dispatchRuntimeContext(itemCommand, itemKey),
				inboundcoord.CoordinatorIssueTriggerCreate,
			)
			if contextErr != nil {
				if created == 0 {
					writeError(w, http.StatusInternalServerError, "failed to prepare coordinator issue context")
					return
				}
				incomplete = true
				break
			}
			overrides.DispatchContext = inboundcoord.StampCoordinatorTrace(independentContext, itemDecision, "dingtalk", time.Now())
			createParams := buildAgentDispatchIssueCreateParams(
				itemCommand, prompt, dispatchContext, agent, itemKey, overrides,
			)
			if i == 0 {
				createParams.AttachmentIDs = attachmentIDs(imported)
			}
			result, err := h.IssueService.Create(r.Context(), createParams, service.IssueCreateOpts{
				ActorID:          uuidToString(dispatchContext.UserID),
				AnalyticsAgentID: uuidToString(agent.ID),
				Platform:         "webhook",
			})
			if errors.Is(err, service.ErrActiveDuplicate) {
				if existingIssue, existingTask, existingIdent, ok := h.lookupCoordinatorWindowItem(
					r.Context(), agent.ID, itemKey, dispatchIdempotencyEndpointID(itemCommand, dispatchContext), dispatchContext.WorkspaceID,
				); ok {
					keepAttachments = true
					if err := h.associateDispatchIssue(r.Context(), itemCommand, dispatchContext, uuidToString(existingIssue.ID), existingIssue.Title, uuidToString(existingTask.ID), item.Content, itemDecision); err != nil {
						writeError(w, 500, "failed to bind committed coordinator issue")
						return
					}
					if err := recordPlanItem(r.Context(), &decision, item, protocol.ChatCoordinatorIssueResult{Action: "issue_created", IssueID: uuidToString(existingIssue.ID), IssueIdentifier: existingIdent, IssueTitle: existingIssue.Title, TaskID: uuidToString(existingTask.ID)}); err != nil {
						writeError(w, 500, "failed to checkpoint recovered issue")
						return
					}
					if created == 0 {
						firstIssueID, firstTaskID, firstIdentifier = uuidToString(existingIssue.ID), uuidToString(existingTask.ID), existingIdent
						firstIssue, firstTask = existingIssue, existingTask
					}
					created++
					continue
				}
				if created == 0 {
					writeError(w, http.StatusConflict, service.ErrActiveDuplicate.Error())
					return
				}
				incomplete = true
				break
			}
			if err != nil {
				if created == 0 {
					writeError(w, http.StatusInternalServerError, "failed to create issue")
					return
				}
				incomplete = true
				break
			}
			keepAttachments = true
			issueResult := protocol.ChatCoordinatorIssueResult{
				Action: "issue_created", IssueID: uuidToString(result.Issue.ID),
				IssueIdentifier: service.IssueIdentifier(h.getIssuePrefix(r.Context(), dispatchContext.WorkspaceID), result.Issue.Number),
				IssueTitle:      result.Issue.Title,
			}
			if result.EnqueuedTask != nil {
				issueResult.TaskID = uuidToString(result.EnqueuedTask.ID)
			}
			if result.EnqueuedTask != nil {
				if err := h.associateDispatchIssue(r.Context(), itemCommand, dispatchContext, uuidToString(result.Issue.ID), result.Issue.Title, uuidToString(result.EnqueuedTask.ID), item.Content, itemDecision); err != nil {
					writeError(w, 500, "failed to bind committed coordinator issue")
					return
				}
				if err := recordPlanItem(r.Context(), &decision, item, issueResult); err != nil {
					writeError(w, 500, "failed to checkpoint created issue")
					return
				}
			} else {
				decision.IssueResults = append(decision.IssueResults, issueResult)
			}
			if result.EnqueuedTask == nil {
				if created == 0 {
					writeError(w, http.StatusInternalServerError, "issue created but agent task was not enqueued")
					return
				}
				incomplete = true
				break
			}
			prefix := h.getIssuePrefix(r.Context(), dispatchContext.WorkspaceID)
			issueID := uuidToString(result.Issue.ID)
			taskID := uuidToString(result.EnqueuedTask.ID)
			issueIdentifier := prefix + "-" + formatIssueNumber(result.Issue.Number)
			if created == 0 {
				firstIssueID, firstTaskID, firstIdentifier = issueID, taskID, issueIdentifier
				firstIssue = result.Issue
				firstTask = *result.EnqueuedTask
			}
			created++
		}
		if created == 0 {
			writeError(w, http.StatusInternalServerError, "failed to create issue")
			return
		}
		if incomplete {
			writeError(w, 500, "failed to commit remaining scene items")
			return
		}
		if created < len(items) {
			if err := inboundcoord.SavePlan(r.Context(), decision); err != nil {
				writeError(w, 500, "failed to retain remaining scene items")
				return
			}
			writeError(w, 409, "scene already has two in-flight matters; remaining plan retained")
			return
		}
		if h.TaskService != nil && c.CompletionCallback != nil {
			spoken := stripReplyDecisionLeak(decision.UserText)
			if spoken != "" {
				if err := h.enqueueCoordinatorIssueAckOrComplete(
					r.Context(), c, dispatchContext, agent.ID,
					firstTask, firstIssue, firstIdentifier, spoken,
				); err != nil {
					writeError(w, http.StatusInternalServerError, "failed to persist coordinator issue reply")
					return
				}
			}
		}
		slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
			"outcome", "created_issue",
			"protocol", "dispatch_command_v2",
			"httpStatus", http.StatusCreated,
			"continuationReturned", true,
			"window_items", created,
			"continuationFingerprint", agentDispatchIdentifierFingerprint(firstIssueID),
			"taskFingerprint", agentDispatchIdentifierFingerprint(firstTaskID),
		)
		h.closeExtraCoordinatorCallbacks(r.Context(), c, agent.ID)
		writeJSON(w, http.StatusCreated, AgentDispatchResponse{
			Continuation:    AgentDispatchContinuation{Kind: "issue", IssueID: firstIssueID},
			IssueIdentifier: firstIdentifier,
			TaskID:          firstTaskID,
		})
		return
	}
	overrides := agentDispatchIssueCreateOverrides{}
	createParams := buildAgentDispatchIssueCreateParams(
		c,
		prompt,
		dispatchContext,
		agent,
		idempotencyKey,
		overrides,
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
	issueIdentifier := prefix + "-" + formatIssueNumber(result.Issue.Number)
	h.associateDispatchIssue(r.Context(), c, dispatchContext, issueID, result.Issue.Title, taskID, prompt.DisplayContent, decision)
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
		IssueIdentifier: issueIdentifier,
		TaskID:          taskID,
	})
}

func (h *Handler) enqueueCoordinatorIssueAckOrComplete(
	ctx context.Context,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
	agentID pgtype.UUID,
	issueTask db.AgentTaskQueue,
	issue db.Issue,
	issueIdentifier string,
	text string,
) error {
	if command.CompletionCallback == nil {
		return errors.New("coordinator issue callback is required")
	}
	wrapupOn := false
	if on, err := h.Queries.GetAgentTaskFinishedLoop(ctx, agentID); err == nil {
		wrapupOn = on
	}
	if wrapupOn && strings.TrimSpace(command.CompletionCallback.UpdateURL) != "" {
		return h.TaskService.EnqueueCoordinatorIssueAck(
			ctx,
			issueTask,
			issue,
			issueIdentifier,
			command.CompletionCallback.UpdateURL,
			command.CompletionCallback.Target,
			text,
		)
	}
	return h.TaskService.EnqueueSynchronousCompleted(
		ctx,
		command.CompletionCallback.URL,
		command.CompletionCallback.Target,
		dispatchContext.AgentID,
		text,
	)
}

func coordinatorRecalledIssueContinuation(
	command DispatchCommand,
	prompt DispatchPrompt,
	decision inboundcoord.Decision,
) (DispatchCommand, DispatchPrompt, bool) {
	issueID := strings.TrimSpace(decision.IssueID)
	if decision.Action != inboundcoord.ActionIssue || issueID == "" {
		return command, prompt, false
	}
	command.AgentID = ""
	command.Continuation = &AgentDispatchContinuation{Kind: "issue", IssueID: issueID}
	prompt.DisplayContent = recalledIssueFollowUpContent(prompt.DisplayContent, decision.LookInto)
	return command, prompt, true
}

func recalledIssueFollowUpContent(message, lookInto string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(message))
	b.WriteString("\n\n系统关联说明：这是已关联外呼会话的回信。当前可信钉钉派发事件中的发信人才是这条消息的真实说话人；Multica 的 Issue 评论人只表示谁执行了 Issue 工具，是协助者，不能当作委托人、当前钉钉发信人或消息接收人。数字员工事件的身份字段完整；机器人事件的用户标识可能缺失，只能使用事件里已有的发信人名称、会话和原文，不能虚构身份或改用 Issue 署名。请继续原事项并先用 assoc_recall current_issue=true since=48h 从原始钉钉委托会话和关联图中明确原委托人、当前钉钉发信人、原消息接收人和下一位应答人。你是中间转达人：首次联系接收人时要说明是谁委托、具体问什么；得到答复后注明是谁说了什么，再通知需要结果的人。遇到阻塞时，把问题发给当前能解除阻塞、且正在处理其问题的人，不要固定发给委托人。每次 Issue 评论触发的任务结束前，必须实际给一个明确的人发送进度、阻塞或结果；Issue 评论和终端输出都只做记录，不算钉钉送达。未取得成功发送回执时，不得写“任务完成”。")
	if lookInto = strings.TrimSpace(lookInto); lookInto != "" {
		b.WriteString("\n本轮要继续处理：")
		b.WriteString(lookInto)
	}
	return b.String()
}

func decideDispatchCoordinator(
	ctx context.Context,
	h *Handler,
	command DispatchCommand,
	agent db.Agent,
	message string,
	userID pgtype.UUID,
	issueDispatchContext []byte,
) inboundcoord.Decision {
	if h == nil || h.Queries == nil || command.Event.Domain != "channel" || command.Event.Type != "message.created" {
		return inboundcoord.Decision{Action: inboundcoord.ActionContinue}
	}
	source := inboundcoord.SourceRobot
	if command.Source.Type == "digital_employee" {
		source = inboundcoord.SourceDigitalEmployee
	}
	chatType := "p2p"
	if strings.EqualFold(strings.TrimSpace(command.Event.Data.Conversation.Type), "group") {
		chatType = "group"
	}
	ids := dispatchAssocIDs(command)
	coord := h.inboundCoordinator()
	turn := inboundcoord.Turn{
		Source:               source,
		Addressed:            true,
		ChatType:             chatType,
		ConversationTitle:    strings.TrimSpace(command.Event.Data.Conversation.Title),
		SenderName:           command.Event.Data.Sender.DisplayName,
		Message:              message,
		AgentID:              agent.ID,
		UserID:               userID,
		AgentName:            agent.Name,
		Instructions:         agent.Instructions,
		IdentityNote:         inboundcoord.IdentityNote(source, ids.ConversationID, ids.PersonID),
		WorkspaceID:          uuidToString(agent.WorkspaceID),
		ConversationID:       ids.ConversationID,
		PersonID:             ids.PersonID,
		EvidenceID:           ids.EvidenceID,
		Kind:                 ids.Kind,
		IssueDispatchContext: issueDispatchContext,
		Utterances:           windowUtterancesFromCommand(command),
	}
	turn.HistoryBefore = inboundcoord.HistoryBeforeFromContext(ctx)
	for _, u := range turn.Utterances {
		if u.Timestamp.After(turn.MessageTimestamp) {
			turn.MessageTimestamp = u.Timestamp
		}
	}
	if !turn.MessageTimestamp.IsZero() {
		turn.HistoryBefore = turn.MessageTimestamp
	}
	turn.DWSUID, turn.DWSOrgID = dispatchCoordinatorDWSIdentity(command)
	if n, err := h.Queries.CountRunningTasks(ctx, agent.ID); err == nil && n > 0 {
		turn.Busy = true
	}
	coord.FillVoice(ctx, &turn)
	return coord.Decide(ctx, turn)
}

func windowUtterancesFromCommand(command DispatchCommand) []inboundcoord.WindowUtterance {
	fallback := strings.TrimSpace(command.Event.Data.Sender.DisplayName)
	out := make([]inboundcoord.WindowUtterance, 0, len(command.Event.Data.Messages))
	for _, message := range command.Event.Data.Messages {
		if message.Reaction != nil {
			continue
		}
		text := strings.TrimSpace(message.Text)
		if text == "" {
			continue
		}
		sender := strings.TrimSpace(message.SenderDisplayName)
		if sender == "" {
			sender = fallback
		}
		senderID := firstNonEmpty(message.SenderUID, message.SenderOpenDingTalkID)
		if senderID == "" && (message.SenderDisplayName == "" || sender == fallback) {
			senderID = firstNonEmpty(command.Event.Data.Sender.UID, command.Event.Data.Sender.OpenDingTalkID)
		}
		at := time.Time{}
		if message.OccurredAt > 0 {
			at = time.UnixMilli(message.OccurredAt).UTC()
		}
		ref := ""
		refContent, refSenderID := "", ""
		if message.ReferencedMessage != nil {
			refContent = message.ReferencedMessage.Text
			refSenderID = message.ReferencedMessage.SenderUID
			ref = firstNonEmpty(message.ReferencedMessage.OpenMsgID, message.ReferencedMessage.MessageID)
		}
		out = append(out, inboundcoord.WindowUtterance{Sender: sender, Text: text, EvidenceID: message.OpenMsgID, Timestamp: at, SenderID: senderID, ReplyToEvidenceID: ref, ReplyToContent: refContent, ReplyToSenderID: refSenderID})
	}
	return out
}

func dispatchCoordinatorDWSIdentity(command DispatchCommand) (string, string) {
	if command.ExternalIdentity.DWS == nil {
		return "", ""
	}
	uid := strings.TrimSpace(command.ExternalIdentity.DWS.UID)
	orgID := strings.TrimSpace(command.ExternalIdentity.DWS.OrgID)
	if uid == "" || orgID == "" {
		return "", ""
	}
	return uid, orgID
}

func (h *Handler) inboundCoordinator() *inboundcoord.Coordinator {
	if h == nil {
		return inboundcoord.New(nil, nil, nil)
	}
	if h.InboundCoordinator != nil {
		return h.InboundCoordinator
	}
	return inboundcoord.New(h.LLM, h.Queries, h.Assoc)
}

const inboundResetMemoryCommand = "/reset-memory"

func isInboundResetMemory(text string) bool {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return false
	}
	token := fields[0]
	if isInboundMentionToken(token) && len(fields) > 1 {
		token = fields[1]
	}
	return strings.EqualFold(token, inboundResetMemoryCommand)
}

func isInboundMentionToken(token string) bool {
	if strings.HasPrefix(token, "@") {
		return true
	}
	return strings.HasPrefix(token, "<@") && strings.HasSuffix(token, ">")
}

func resetMemoryReply(conversationID string, err error) string {
	if err != nil {
		return "清理事项关联或场域记忆失败，请稍后再试。"
	}
	if strings.TrimSpace(conversationID) == "" {
		return "没法识别这个会话，事项关联没有改。"
	}
	return "已清理这个会话上的事项关联和场域记忆。之后不会再按旧事项接话。"
}

func (h *Handler) tryDispatchResetMemory(
	w http.ResponseWriter,
	r *http.Request,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
) bool {
	if command.Event.Domain != "channel" || command.Event.Type != "message.created" {
		return false
	}
	if !isInboundResetMemory(dispatchInboundEventBody(command)) {
		return false
	}
	ids := dispatchAssocIDs(command)
	conversationID := ids.ConversationID
	if conversationID != "" && !assoc.ValidSceneID(conversationID) {
		conversationID = ""
	}
	var closeErr error
	closedEdges := 0
	unlinkedEvents := 0
	if h != nil && h.Assoc != nil && conversationID != "" {
		result, err := h.Assoc.CloseSceneAssociations(
			r.Context(),
			uuidToString(dispatchContext.WorkspaceID),
			uuidToString(dispatchContext.AgentID),
			conversationID,
		)
		closeErr = err
		closedEdges = result.ClosedEdges
		unlinkedEvents = result.UnlinkedEvents
	}
	if h != nil && h.SceneMemoryStore != nil && conversationID != "" &&
		command.Source.Type == "digital_employee" {
		kind, title := dispatchSceneIdentity(command)
		orgID := ""
		var identityErr error
		if h.Queries != nil {
			identity, err := h.Queries.GetAgentDingTalkIdentity(r.Context(), db.GetAgentDingTalkIdentityParams{
				WorkspaceID: dispatchContext.WorkspaceID,
				AgentID:     dispatchContext.AgentID,
			})
			if err == nil {
				orgID = identity.OrgID
			} else {
				identityErr = err
			}
		}
		if orgID == "" && command.ExternalIdentity.DWS != nil {
			orgID = strings.TrimSpace(command.ExternalIdentity.DWS.OrgID)
		}
		if orgID == "" && closeErr == nil {
			if identityErr != nil {
				closeErr = identityErr
			} else {
				closeErr = errors.New("scene memory identity is unavailable")
			}
		}
		if orgID != "" {
			identity := scenememory.Identity{
				WorkspaceID: dispatchContext.WorkspaceID,
				AgentID:     dispatchContext.AgentID,
				OrgID:       orgID,
				SceneKey:    conversationID,
				SceneKind:   kind,
				SceneTitle:  title,
			}
			oldRevision := int64(0)
			if existing, err := h.SceneMemoryStore.Get(r.Context(), identity); err == nil {
				oldRevision = existing.MemoryRevision
			}
			if _, err := h.SceneMemoryStore.Reset(r.Context(), identity, scenememory.DirtyTrigger{
				OccurredAt: dispatchMessageOccurredAt(command),
				EvidenceID: ids.EvidenceID,
			}); err != nil {
				slog.Warn("scene memory reset-memory failed",
					"event", "scene_memory_reset",
					"scene_key", conversationID,
					"old_revision", oldRevision,
					"error", err,
				)
				if closeErr == nil {
					closeErr = err
				}
			} else {
				slog.Info("scene memory reset",
					"event", "scene_memory_reset",
					"scene_key", conversationID,
					"old_revision", oldRevision,
				)
			}
		}
	}
	slog.Info("MULTICA_AGENT_DISPATCH_REQUEST",
		"outcome", "reset_memory",
		"event", "inbound_reset_memory",
		"protocol", "dispatch_command_v2",
		"conversation_id", conversationID,
		"closed_edges", closedEdges,
		"unlinked_events", unlinkedEvents,
		"error", closeErr != nil,
	)
	if closeErr != nil {
		slog.Error("assoc reset-memory failed",
			"event", "inbound_reset_memory",
			"conversation_id", conversationID,
			"error", closeErr,
		)
	}
	decision := inboundcoord.Decision{
		Action:   inboundcoord.ActionReply,
		UserText: resetMemoryReply(conversationID, closeErr),
	}
	if writeDispatchCoordinatorTerminal(w, r.Context(), h, command, dispatchContext, decision) {
		return true
	}
	writeJSON(w, http.StatusAccepted, AgentDispatchResponse{})
	return true
}

func stripReplyDecisionLeak(text string) string {
	visible, _ := service.NormalizeReplyDecisionOutput(text, nil)
	return strings.TrimSpace(visible)
}

func writeDispatchCoordinatorTerminal(
	w http.ResponseWriter,
	ctx context.Context,
	h *Handler,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
	decision inboundcoord.Decision,
) bool {
	if h.TaskService == nil || command.CompletionCallback == nil {
		return false
	}
	visible := stripReplyDecisionLeak(decision.UserText)
	if decision.Action == inboundcoord.ActionReply && visible == "" {
		decision.Action = inboundcoord.ActionSilence
		decision.UserText = ""
	}
	switch decision.Action {
	case inboundcoord.ActionReply:
		if err := h.TaskService.EnqueueSynchronousCompleted(
			ctx,
			command.CompletionCallback.URL,
			command.CompletionCallback.Target,
			dispatchContext.AgentID,
			visible,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist coordinator reply")
			return true
		}
		response := AgentDispatchResponse{}
		if decision.IssueComment != nil {
			response.Continuation = AgentDispatchContinuation{Kind: "issue", IssueID: decision.IssueComment.IssueID}
			response.IssueIdentifier = decision.IssueComment.IssueIdentifier
			response.CommentID = decision.IssueComment.CommentID
		}
		h.closeExtraCoordinatorCallbacks(ctx, command, dispatchContext.AgentID)
		writeJSON(w, http.StatusAccepted, response)
		return true
	case inboundcoord.ActionSilence:
		if err := h.TaskService.EnqueueSynchronousSilence(
			ctx,
			command.CompletionCallback.URL,
			command.CompletionCallback.Target,
			dispatchContext.AgentID,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist coordinator silence")
			return true
		}
		h.closeExtraCoordinatorCallbacks(ctx, command, dispatchContext.AgentID)
		w.WriteHeader(http.StatusAccepted)
		return true
	default:
		return false
	}
}

func (h *Handler) closeExtraCoordinatorCallbacks(ctx context.Context, command DispatchCommand, agentID pgtype.UUID) {
	if managedDingTalkResponse(command) {
		// The response receipt closes the entire collected window after
		// actual delivery; persisting a model result is too early.
		return
	}
	for i := range command.ExtraCompletionCallbacks {
		cb := command.ExtraCompletionCallbacks[i]
		h.enqueueCoordinatorSilenceCallback(ctx, &cb, agentID, "inbound_coordinator_extra_silence_failed", "")
	}
}

// dispatchIdempotencyEndpointID is the value stored on the task as
// dispatch_endpoint_id. V2 snapshots the namespace UUID, not the public
// endpoint id; lookup must use the same string or retry creates a second Issue.
func dispatchIdempotencyEndpointID(command DispatchCommand, dispatchContext agentDispatchContext) string {
	if id := strings.TrimSpace(command.DispatchEndpointID); id != "" {
		return id
	}
	if dispatchContext.EndpointNamespaceID.Valid {
		return uuidToString(dispatchContext.EndpointNamespaceID)
	}
	return strings.TrimSpace(dispatchContext.EndpointID)
}

func (h *Handler) lookupCoordinatorWindowItem(
	ctx context.Context,
	agentID pgtype.UUID,
	itemKey, endpointID string,
	workspaceID pgtype.UUID,
) (db.Issue, db.AgentTaskQueue, string, bool) {
	var noneIssue db.Issue
	var noneTask db.AgentTaskQueue
	if h == nil || h.Queries == nil || strings.TrimSpace(itemKey) == "" {
		return noneIssue, noneTask, "", false
	}
	existing, err := h.Queries.GetAgentDispatchRootTaskByIdempotency(ctx, db.GetAgentDispatchRootTaskByIdempotencyParams{
		AgentID:        agentID,
		IdempotencyKey: itemKey,
		EndpointID:     endpointID,
	})
	if err != nil || !existing.IssueID.Valid {
		return noneIssue, noneTask, "", false
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: existing.IssueID, WorkspaceID: workspaceID})
	if err != nil {
		return noneIssue, noneTask, "", false
	}
	ident := h.getIssuePrefix(ctx, workspaceID) + "-" + formatIssueNumber(issue.Number)
	return issue, existing, ident, true
}

func (h *Handler) createAgentDispatchCommentV2(w http.ResponseWriter, r *http.Request, c DispatchCommand, prompt DispatchPrompt, dispatchContext agentDispatchContext) {
	h.createAgentDispatchCommentWithCoordinatorV2(w, r, c, prompt, dispatchContext, nil)
}

func (h *Handler) createAgentDispatchCommentWithCoordinatorV2(
	w http.ResponseWriter,
	r *http.Request,
	c DispatchCommand,
	prompt DispatchPrompt,
	dispatchContext agentDispatchContext,
	coordinatorDecision *inboundcoord.Decision,
) {
	issueID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(c.Continuation.IssueID), "continuation.issueId")
	if !ok {
		return
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: dispatchContext.WorkspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if coordinatorDecision != nil {
				writeError(w, http.StatusConflict, "recalled issue no longer exists")
				return
			}
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
	idempotencyKey := dispatchIdempotencyKey(r, c)
	var privateContext []byte
	if coordinatorDecision != nil {
		independentContext, contextErr := inboundcoord.IndependentIssueTaskContext(
			dispatchRuntimeContext(c, idempotencyKey),
			inboundcoord.CoordinatorIssueTriggerComment,
		)
		if contextErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to prepare coordinator issue follow-up context")
			return
		}
		privateContext = independentContext
	}
	followUpParams := buildAgentDispatchIssueFollowUpParams(
		c,
		prompt,
		dispatchContext,
		issue,
		idempotencyKey,
		privateContext,
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
	if coordinatorDecision != nil {
		coordinatorDecision.IssueResults = []protocol.ChatCoordinatorIssueResult{{
			Action: "issue_commented", IssueID: issueIDString,
			IssueIdentifier: service.IssueIdentifier(h.getIssuePrefix(r.Context(), issue.WorkspaceID), issue.Number),
			IssueTitle:      issue.Title, CommentID: commentID, TaskID: taskID,
		}}
		inboundcoord.RecordDecision(r.Context(), *coordinatorDecision)
	}
	assocDecision := inboundcoord.Decision{}
	if coordinatorDecision != nil {
		assocDecision = *coordinatorDecision
	}
	h.associateDispatchIssue(r.Context(), c, dispatchContext, issueIDString, issue.Title, taskID, prompt.DisplayContent, assocDecision)
	if coordinatorDecision != nil &&
		h.TaskService != nil &&
		c.CompletionCallback != nil &&
		strings.TrimSpace(coordinatorDecision.UserText) != "" {
		if err := h.TaskService.EnqueueSynchronousCompleted(
			r.Context(),
			c.CompletionCallback.URL,
			c.CompletionCallback.Target,
			dispatchContext.AgentID,
			coordinatorDecision.UserText,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist coordinator issue follow-up reply")
			return
		}
	}
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
