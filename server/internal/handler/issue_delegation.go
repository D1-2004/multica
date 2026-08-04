package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const delegatedFromTaskContextKey = "dispatch_delegated_from_task_id"

type IssueDelegationRequest struct {
	SourceTaskID string `json:"source_task_id"`
	Mode         string `json:"mode"`
	IssueID      string `json:"issue_id,omitempty"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
	AssigneeID   string `json:"assignee_id,omitempty"`
	Status       string `json:"status,omitempty"`
	Priority     string `json:"priority,omitempty"`
	Content      string `json:"content,omitempty"`
}

type IssueDelegationResponse struct {
	IssueID          string `json:"issue_id"`
	IssueIdentifier string `json:"issue_identifier"`
	TargetTaskID     string `json:"target_task_id,omitempty"`
	TriggerCommentID string `json:"trigger_comment_id,omitempty"`
	Queued           bool   `json:"queued"`
	ReleaseParent    bool   `json:"release_parent"`
}

type issueDelegationPrivateContext struct {
	SchemaVersion string            `json:"dispatch_schema_version"`
	Source        DispatchSource    `json:"dispatch_source"`
	Domain        string            `json:"dispatch_domain"`
	Type          string            `json:"dispatch_type"`
	EventData     DispatchEventData `json:"dispatch_event_data"`
	Outbound      DispatchOutbound  `json:"dispatch_outbound"`

	IdentityContextToken          string                        `json:"agent_identity_context_token"`
	IdentityContextTokenExpiresAt int64                         `json:"agent_identity_context_token_expires_at"`
	ExternalIdentity             AgentDispatchExternalIdentity `json:"external_identity"`
	DispatchEndpointID            string                        `json:"dispatch_endpoint_id"`
	IdempotencyKey                string                        `json:"dispatch_idempotency_key"`

	CompletionCallback *struct {
		URL       string `json:"url"`
		UpdateURL string `json:"update_url"`
		Target    string `json:"target"`
	} `json:"completion_callback,omitempty"`
}

func issueDelegationSessionLabel(chatSessionID string) string {
	sum := sha256.Sum256([]byte(chatSessionID))
	return "chat:" + hex.EncodeToString(sum[:10])
}

func (h *Handler) loadIssueDelegationSource(
	w http.ResponseWriter,
	r *http.Request,
	requestedTaskID string,
) (db.AgentTaskQueue, pgtype.UUID, bool) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "issue delegation requires task-scoped authentication")
		return db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	trustedTaskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
	if requestedTaskID == "" || trustedTaskID == "" || requestedTaskID != trustedTaskID {
		writeError(w, http.StatusForbidden, "source_task_id does not match the authenticated task")
		return db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	taskID, err := util.ParseUUID(trustedTaskID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid source_task_id")
		return db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	workspaceID, err := util.ParseUUID(h.resolveWorkspaceID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace_id")
		return db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          taskID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "source task not found")
		return db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	if !task.ChatSessionID.Valid {
		writeError(w, http.StatusConflict, "only Chat tasks can delegate to Issue")
		return db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	if trustedAgentID := strings.TrimSpace(r.Header.Get("X-Agent-ID")); trustedAgentID == "" ||
		trustedAgentID != uuidToString(task.AgentID) {
		writeError(w, http.StatusForbidden, "authenticated Agent does not own the source task")
		return db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	return task, workspaceID, true
}

func delegatedIssueDispatch(
	sourceTask db.AgentTaskQueue,
) (DispatchCommand, DispatchPrompt, []byte, error) {
	private := issueDelegationPrivateContext{}
	if len(sourceTask.Context) > 0 {
		if err := json.Unmarshal(sourceTask.Context, &private); err != nil {
			return DispatchCommand{}, DispatchPrompt{}, nil, err
		}
	}

	raw := make(map[string]json.RawMessage)
	if len(sourceTask.Context) > 0 {
		if err := json.Unmarshal(sourceTask.Context, &raw); err != nil {
			return DispatchCommand{}, DispatchPrompt{}, nil, err
		}
	}
	surfaceJSON, err := json.Marshal(DispatchSurface{Type: protocol.DispatchSurfaceTypeIssue})
	if err != nil {
		return DispatchCommand{}, DispatchPrompt{}, nil, err
	}
	sourceTaskIDJSON, err := json.Marshal(uuidToString(sourceTask.ID))
	if err != nil {
		return DispatchCommand{}, DispatchPrompt{}, nil, err
	}
	raw[protocol.DispatchSurfaceJSONKey] = surfaceJSON
	raw[delegatedFromTaskContextKey] = sourceTaskIDJSON
	transferredContext, err := json.Marshal(raw)
	if err != nil {
		return DispatchCommand{}, DispatchPrompt{}, nil, err
	}

	command := DispatchCommand{
		SchemaVersion: private.SchemaVersion,
		Source:        private.Source,
		Event: DispatchEvent{
			Domain: private.Domain,
			Type:   private.Type,
			Data:   private.EventData,
		},
		Surface: DispatchSurface{Type: protocol.DispatchSurfaceTypeIssue},
		Outbound: private.Outbound,
		ExternalIdentity: private.ExternalIdentity,
		DispatchEndpointID: private.DispatchEndpointID,
	}
	command.ExternalIdentity.ContextToken = private.IdentityContextToken
	command.ExternalIdentity.ExpiresAt = private.IdentityContextTokenExpiresAt
	if private.CompletionCallback != nil {
		command.CompletionCallback = &DispatchCompletionCallback{
			URL:       private.CompletionCallback.URL,
			UpdateURL: private.CompletionCallback.UpdateURL,
			Target:    private.CompletionCallback.Target,
		}
	}
	if strings.TrimSpace(private.SchemaVersion) == "" {
		return command, DispatchPrompt{}, transferredContext, nil
	}
	prompt, err := BuildDispatchPrompt(command)
	if err != nil {
		return DispatchCommand{}, DispatchPrompt{}, nil, err
	}
	return command, prompt, transferredContext, nil
}

func sourceTaskOriginator(task db.AgentTaskQueue) pgtype.UUID {
	if task.OriginatorUserID.Valid {
		return task.OriginatorUserID
	}
	return task.InitiatorUserID
}

func (h *Handler) existingDelegatedTaskResponse(
	w http.ResponseWriter,
	r *http.Request,
	sourceTask db.AgentTaskQueue,
	workspaceID pgtype.UUID,
) bool {
	task, err := h.Queries.GetDelegatedChildTaskByParent(r.Context(), sourceTask.ID)
	triggerCommentID := pgtype.UUID{}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load delegated task")
		return true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		comment, commentErr := h.Queries.GetDelegatedMemberCommentBySourceTask(r.Context(), sourceTask.ID)
		if errors.Is(commentErr, pgx.ErrNoRows) {
			return false
		}
		if commentErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to load delegated comment")
			return true
		}
		issue, issueErr := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID:          comment.IssueID,
			WorkspaceID: workspaceID,
		})
		if issueErr != nil {
			writeError(w, http.StatusConflict, "delegated issue no longer exists")
			return true
		}
		task, err = h.Queries.GetTaskForDelegatedComment(r.Context(), db.GetTaskForDelegatedCommentParams{
			IssueID:   issue.ID,
			AgentID:   issue.AssigneeID,
			CommentID: comment.ID,
		})
		if err != nil {
			writeError(w, http.StatusConflict, "delegated issue task no longer exists")
			return true
		}
		triggerCommentID = comment.ID
		h.writeIssueDelegationResponse(
			r.Context(),
			w,
			http.StatusOK,
			issue,
			task,
			triggerCommentID,
		)
		return true
	}
	triggerCommentID = task.TriggerCommentID
	if !task.IssueID.Valid {
		writeError(w, http.StatusConflict, "delegated task has no issue")
		return true
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          task.IssueID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, http.StatusConflict, "delegated issue no longer exists")
		return true
	}
	h.writeIssueDelegationResponse(
		r.Context(),
		w,
		http.StatusOK,
		issue,
		task,
		triggerCommentID,
	)
	return true
}

// DelegateIssue is a task-scoped surface handoff. The request carries only
// Issue routing choices; trusted dispatch identity, callback, and outbound
// context are recovered from the authenticated Chat task and transferred into
// the normal Issue dispatch materializers.
func (h *Handler) DelegateIssue(w http.ResponseWriter, r *http.Request) {
	var req IssueDelegationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.SourceTaskID = strings.TrimSpace(req.SourceTaskID)
	req.Mode = strings.TrimSpace(req.Mode)
	req.IssueID = strings.TrimSpace(req.IssueID)
	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	req.AssigneeID = strings.TrimSpace(req.AssigneeID)
	req.Status = strings.TrimSpace(req.Status)
	req.Priority = strings.TrimSpace(req.Priority)
	req.Content = strings.TrimSpace(req.Content)

	sourceTask, workspaceID, ok := h.loadIssueDelegationSource(w, r, req.SourceTaskID)
	if !ok {
		return
	}
	if h.existingDelegatedTaskResponse(w, r, sourceTask, workspaceID) {
		return
	}
	if sourceTask.Status != "running" && sourceTask.Status != "dispatched" {
		writeError(w, http.StatusConflict, "source task is not active")
		return
	}

	command, prompt, privateContext, err := delegatedIssueDispatch(sourceTask)
	if err != nil {
		writeError(w, http.StatusConflict, "source task has invalid dispatch context")
		return
	}
	if command.CompletionCallback != nil && command.CompletionCallback.UpdateURL == "" {
		writeError(w, http.StatusConflict, "source task has no execution update callback")
		return
	}
	switch req.Mode {
	case "create":
		h.createDelegatedIssue(w, r, req, sourceTask, workspaceID, command, prompt, privateContext)
	case "continue":
		h.continueDelegatedIssue(w, r, req, sourceTask, workspaceID, command, prompt, privateContext)
	default:
		writeError(w, http.StatusBadRequest, "mode must be 'create' or 'continue'")
	}
}

func (h *Handler) createDelegatedIssue(
	w http.ResponseWriter,
	r *http.Request,
	req IssueDelegationRequest,
	sourceTask db.AgentTaskQueue,
	workspaceID pgtype.UUID,
	command DispatchCommand,
	prompt DispatchPrompt,
	privateContext []byte,
) {
	if req.Title == "" || req.Description == "" {
		writeError(w, http.StatusBadRequest, "title and description are required")
		return
	}
	assigneeID, err := util.ParseUUID(req.AssigneeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "valid assignee_id is required")
		return
	}
	originator := sourceTaskOriginator(sourceTask)
	if !originator.Valid {
		writeError(w, http.StatusConflict, "source task has no human originator")
		return
	}
	agent, ok := h.resolveAgentDispatchAgent(w, r, originator, workspaceID, assigneeID)
	if !ok {
		return
	}

	status := req.Status
	if status == "" {
		status = "todo"
	}
	if !validateIssueEnum(w, "status", status, validIssueStatuses) {
		return
	}
	if status == "backlog" || status == "done" || status == "cancelled" {
		writeError(w, http.StatusBadRequest, "delegated issue status must start runnable")
		return
	}
	priority := req.Priority
	if priority == "" {
		priority = "none"
	}
	if !validateIssueEnum(w, "priority", priority, validIssuePriorities) {
		return
	}

	command.AgentID = req.AssigneeID
	prompt.DisplayContent = req.Description
	metadata, _ := json.Marshal(map[string]string{
		"multica.chat_session_id":         uuidToString(sourceTask.ChatSessionID),
		"multica.delegated_from_task_id":  req.SourceTaskID,
		"multica.delegated_from_agent_id": uuidToString(sourceTask.AgentID),
	})
	createParams := buildAgentDispatchIssueCreateParams(
		command,
		prompt,
		agentDispatchContext{
			UserID:      originator,
			WorkspaceID: workspaceID,
			AgentID:     agent.ID,
		},
		agent,
		"",
		agentDispatchIssueCreateOverrides{
			Title:                  req.Title,
			DisplayContent:         req.Description,
			DispatchContext:        privateContext,
			Metadata:               metadata,
			SystemLabelName:        issueDelegationSessionLabel(uuidToString(sourceTask.ChatSessionID)),
			SystemLabelDescription: "Issues delegated from Chat session " + uuidToString(sourceTask.ChatSessionID),
			SystemLabelColor:       "#64748B",
			ParentTaskID:           sourceTask.ID,
		},
	)
	createParams.Status = status
	createParams.Priority = priority
	// The Agent already performed semantic routing before calling the tool.
	// A same-titled Issue in another Chat must not override that decision.
	createParams.AllowDuplicate = true
	createParams.Delegation = &service.IssueDelegationCreateParams{}
	if command.CompletionCallback != nil {
		createParams.Delegation.CallbackUpdateURL = command.CompletionCallback.UpdateURL
		createParams.Delegation.CallbackTarget = command.CompletionCallback.Target
	}

	result, err := h.IssueService.Create(r.Context(), createParams, service.IssueCreateOpts{
		ActorID:          uuidToString(originator),
		AnalyticsAgentID: uuidToString(agent.ID),
		Platform: func() string {
			platform, _, _ := middleware.ClientMetadataFromContext(r.Context())
			return platform
		}(),
	})
	if errors.Is(err, service.ErrDelegationAlreadyExists) {
		if h.existingDelegatedTaskResponse(w, r, sourceTask, workspaceID) {
			return
		}
	}
	if errors.Is(err, service.ErrDelegationSourceInactive) {
		writeError(w, http.StatusConflict, "source task is not active")
		return
	}
	if err != nil {
		slog.Error(
			"failed to create delegated issue",
			"source_task_id", req.SourceTaskID,
			"target_agent_id", req.AssigneeID,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "failed to create delegated issue")
		return
	}
	if result.EnqueuedTask == nil {
		writeError(w, http.StatusInternalServerError, "issue created but delegated task was not enqueued")
		return
	}
	if command.CompletionCallback != nil && h.TaskCompletionWorker != nil {
		h.TaskCompletionWorker.NotifyTaskExecutionUpdate()
	}
	slog.Info(
		"issue dispatch handoff created",
		"source_task_id", req.SourceTaskID,
		"source_chat_session_id", uuidToString(sourceTask.ChatSessionID),
		"issue_id", uuidToString(result.Issue.ID),
		"target_task_id", uuidToString(result.EnqueuedTask.ID),
	)
	h.writeIssueDelegationResponse(
		r.Context(),
		w,
		http.StatusCreated,
		result.Issue,
		*result.EnqueuedTask,
		pgtype.UUID{},
	)
}

func (h *Handler) continueDelegatedIssue(
	w http.ResponseWriter,
	r *http.Request,
	req IssueDelegationRequest,
	sourceTask db.AgentTaskQueue,
	workspaceID pgtype.UUID,
	command DispatchCommand,
	prompt DispatchPrompt,
	privateContext []byte,
) {
	issueID, err := util.ParseUUID(req.IssueID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "valid issue_id is required")
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          issueID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		writeError(w, http.StatusConflict, "existing issue must be assigned to an Agent")
		return
	}
	originator := sourceTaskOriginator(sourceTask)
	if !originator.Valid {
		writeError(w, http.StatusConflict, "source task has no human originator")
		return
	}
	if _, ok := h.resolveAgentDispatchAgent(w, r, originator, workspaceID, issue.AssigneeID); !ok {
		return
	}

	command.AgentID = ""
	command.Continuation = &AgentDispatchContinuation{
		Kind:    "issue",
		IssueID: req.IssueID,
	}
	prompt.DisplayContent = req.Content
	params := buildAgentDispatchIssueFollowUpParams(
		command,
		prompt,
		agentDispatchContext{
			UserID:      originator,
			WorkspaceID: workspaceID,
			AgentID:     issue.AssigneeID,
		},
		issue,
		"",
		privateContext,
		sourceTask.ID,
	)
	params.Delegation = &service.IssueDelegationFollowUpParams{}
	if command.CompletionCallback != nil {
		params.Delegation.CallbackUpdateURL = command.CompletionCallback.UpdateURL
		params.Delegation.CallbackTarget = command.CompletionCallback.Target
	}
	result, err := h.IssueCommentService.CreateExternalFollowUp(
		r.Context(),
		params,
		service.IssueCommentCreateOpts{},
	)
	if errors.Is(err, service.ErrIssueDispatchPending) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"code":  "issue_dispatch_pending",
			"error": "issue already has a pending agent task",
		})
		return
	}
	if errors.Is(err, service.ErrDelegationAlreadyExists) {
		if h.existingDelegatedTaskResponse(w, r, sourceTask, workspaceID) {
			return
		}
	}
	if errors.Is(err, service.ErrDelegationSourceInactive) {
		writeError(w, http.StatusConflict, "source task is not active")
		return
	}
	if err != nil {
		slog.Error(
			"failed to create delegated issue follow-up",
			"source_task_id", req.SourceTaskID,
			"issue_id", req.IssueID,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "failed to create delegated issue follow-up")
		return
	}
	if command.CompletionCallback != nil && h.TaskCompletionWorker != nil {
		h.TaskCompletionWorker.NotifyTaskExecutionUpdate()
	}
	slog.Info(
		"issue dispatch handoff continued",
		"source_task_id", req.SourceTaskID,
		"source_chat_session_id", uuidToString(sourceTask.ChatSessionID),
		"issue_id", uuidToString(issue.ID),
		"trigger_comment_id", uuidToString(result.Comment.ID),
		"target_task_id", uuidToString(result.Task.ID),
	)
	h.writeIssueDelegationResponse(
		r.Context(),
		w,
		http.StatusCreated,
		issue,
		result.Task,
		result.Comment.ID,
	)
}

func (h *Handler) writeIssueDelegationResponse(
	ctx context.Context,
	w http.ResponseWriter,
	status int,
	issue db.Issue,
	task db.AgentTaskQueue,
	triggerCommentID pgtype.UUID,
) {
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	writeJSON(w, status, IssueDelegationResponse{
		IssueID:          uuidToString(issue.ID),
		IssueIdentifier: prefix + "-" + formatIssueNumber(issue.Number),
		TargetTaskID:     uuidToString(task.ID),
		TriggerCommentID: uuidToString(triggerCommentID),
		Queued:           true,
		ReleaseParent:    true,
	})
}
