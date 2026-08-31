package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type assocRecallParams struct {
	Since          string
	Until          string
	ConversationID string
	PersonID       string
	Issue          string
	CurrentIssue   bool
	Q              string
	Intent         string
	Limit          int
	TaskID         string
}

func (h *Handler) RecallAssoc(w http.ResponseWriter, r *http.Request) {
	if h.Assoc == nil {
		writeError(w, http.StatusServiceUnavailable, "association store is not configured")
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace is required")
		return
	}
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	agentID := actorID
	if actorType != "agent" {
		agentID = strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			writeError(w, http.StatusBadRequest, "agent_id is required")
			return
		}
		if _, err := util.ParseUUID(agentID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid agent_id")
			return
		}
	}
	limit := 0
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		n, nerr := strconv.Atoi(rawLimit)
		if nerr != nil {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}
	result, err := h.recallAssoc(r.Context(), workspaceID, agentID, assocRecallParams{
		Since:          r.URL.Query().Get("since"),
		Until:          r.URL.Query().Get("until"),
		ConversationID: r.URL.Query().Get("conversation_id"),
		PersonID:       r.URL.Query().Get("person_id"),
		Issue:          r.URL.Query().Get("issue"),
		Q:              r.URL.Query().Get("q"),
		Intent:         r.URL.Query().Get("intent"),
		Limit:          limit,
	})
	if err != nil {
		if errors.Is(err, assoc.ErrInvalidQuery) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to recall associations")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleMulticaMCPAssocRecall(w http.ResponseWriter, r *http.Request, id json.RawMessage, rawArguments json.RawMessage) {
	if h.Assoc == nil {
		h.writeMulticaMCPToolError(w, id, "association store is not configured")
		return
	}
	var args multicaMCPAssocRecallArguments
	if err := decodeMulticaMCPArguments(rawArguments, &args); err != nil {
		h.writeMulticaMCPError(w, id, -32602, "invalid assoc_recall arguments")
		return
	}
	workspaceID := strings.TrimSpace(r.Header.Get("X-Workspace-ID"))
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	if workspaceID == "" || agentID == "" {
		h.writeMulticaMCPToolError(w, id, "workspace and agent identity are required")
		return
	}
	result, err := h.recallAssoc(r.Context(), workspaceID, agentID, assocRecallParams{
		Since:          args.Since,
		Until:          args.Until,
		ConversationID: args.ConversationID,
		PersonID:       args.PersonID,
		Issue:          args.Issue,
		CurrentIssue:   args.CurrentIssue,
		Q:              args.Q,
		Intent:         args.Intent,
		Limit:          args.Limit,
		TaskID:         strings.TrimSpace(r.Header.Get("X-Task-ID")),
	})
	if err != nil {
		h.writeMulticaMCPToolError(w, id, err.Error())
		return
	}
	textResult, _ := json.Marshal(result)
	h.writeMulticaMCPResult(w, id, multicaMCPToolResult{
		Content:           []multicaMCPContent{{Type: "text", Text: string(textResult)}},
		StructuredContent: result,
	})
}

func (h *Handler) recallAssoc(ctx context.Context, workspaceID, agentID string, in assocRecallParams) (assoc.Result, error) {
	now := time.Now().UTC()
	since, err := assoc.ParseSince(in.Since, now)
	if err != nil {
		return assoc.Result{}, err
	}
	until := now
	if rawUntil := strings.TrimSpace(in.Until); rawUntil != "" {
		parsed, perr := time.Parse(time.RFC3339, rawUntil)
		if perr != nil {
			return assoc.Result{}, fmtAssocQuery("invalid until")
		}
		until = parsed.UTC()
	}
	issueID, err := h.resolveAssocIssueID(ctx, workspaceID, in)
	if err != nil {
		return assoc.Result{}, err
	}
	result, err := h.Assoc.Recall(ctx, assoc.Query{
		WorkspaceID:    workspaceID,
		AgentID:        agentID,
		Since:          since,
		Until:          until,
		ConversationID: strings.TrimSpace(in.ConversationID),
		PersonID:       strings.TrimSpace(in.PersonID),
		IssueID:        issueID,
		Q:              strings.TrimSpace(in.Q),
		Intent:         strings.TrimSpace(in.Intent),
		Limit:          in.Limit,
	})
	if err != nil {
		return assoc.Result{}, err
	}
	return h.hydrateAssocIssueIdentifiers(ctx, workspaceID, result), nil
}

func fmtAssocQuery(msg string) error {
	return fmt.Errorf("%w: %s", assoc.ErrInvalidQuery, msg)
}

func (h *Handler) resolveAssocIssueID(ctx context.Context, workspaceID string, in assocRecallParams) (string, error) {
	if in.CurrentIssue {
		if h.Queries == nil || strings.TrimSpace(in.TaskID) == "" {
			return "", fmtAssocQuery("current_issue requires an authenticated task")
		}
		wsUUID, wsErr := util.ParseUUID(workspaceID)
		taskUUID, taskErr := util.ParseUUID(in.TaskID)
		if wsErr != nil || taskErr != nil {
			return "", fmtAssocQuery("invalid task identity")
		}
		task, loadErr := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
			ID:          taskUUID,
			WorkspaceID: wsUUID,
		})
		if loadErr != nil || !task.IssueID.Valid {
			return "", fmtAssocQuery("current task has no Issue")
		}
		return util.UUIDToString(task.IssueID), nil
	}
	issueID := strings.TrimSpace(in.Issue)
	if issueID == "" {
		return "", nil
	}
	if _, perr := util.ParseUUID(issueID); perr == nil {
		return issueID, nil
	}
	issue, ok := h.resolveIssueByIdentifier(ctx, issueID, workspaceID)
	if !ok {
		return "", fmtAssocQuery("issue not found")
	}
	return util.UUIDToString(issue.ID), nil
}

func (h *Handler) hydrateAssocIssueIdentifiers(ctx context.Context, workspaceID string, result assoc.Result) assoc.Result {
	if h.Queries == nil {
		return result
	}
	wsUUID, perr := util.ParseUUID(workspaceID)
	if perr != nil {
		return result
	}
	prefix := h.getIssuePrefix(ctx, wsUUID)
	for i := range result.Items {
		issueUUID, ierr := util.ParseUUID(result.Items[i].Issue)
		if ierr != nil {
			continue
		}
		issue, gerr := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID:          issueUUID,
			WorkspaceID: wsUUID,
		})
		if gerr != nil {
			continue
		}
		result.Items[i].Issue = issueToResponse(issue, prefix).Identifier
	}
	return result
}

type assocBindOutboundRequest struct {
	ConversationID string `json:"conversation_id"`
	EvidenceID     string `json:"evidence_id,omitempty"`
	PersonID       string `json:"person_id,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Intent         string `json:"intent,omitempty"`
}

func (h *Handler) BindAssocOutbound(w http.ResponseWriter, r *http.Request) {
	if h.Assoc == nil {
		writeError(w, http.StatusServiceUnavailable, "association store is not configured")
		return
	}
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "bind-outbound requires a task token")
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(r.Header.Get("X-Workspace-ID"))
	}
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	taskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
	if workspaceID == "" || agentID == "" || taskID == "" {
		writeError(w, http.StatusBadRequest, "workspace, agent, and task are required")
		return
	}
	var body assocBindOutboundRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	in := assoc.BindOutboundInput{
		WorkspaceID:    workspaceID,
		AgentID:        agentID,
		RunID:          taskID,
		ConversationID: strings.TrimSpace(body.ConversationID),
		EvidenceID:     strings.TrimSpace(body.EvidenceID),
		PersonID:       strings.TrimSpace(body.PersonID),
		Kind:           strings.TrimSpace(body.Kind),
		Intent:         strings.TrimSpace(body.Intent),
	}
	issueID, title, err := h.issueForTaskToken(r.Context(), workspaceID, taskID)
	if err != nil {
		if errors.Is(err, assoc.ErrInvalidQuery) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load task issue")
		return
	}
	in.IssueID = issueID
	in.IssueTitle = title
	result, err := h.Assoc.BindOutbound(r.Context(), in)
	if err != nil {
		if errors.Is(err, assoc.ErrInvalidQuery) || errors.Is(err, assoc.ErrInvalidTask) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to bind outbound conversation")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) issueForTaskToken(ctx context.Context, workspaceID, taskID string) (issueID, title string, err error) {
	if h.Queries == nil {
		return "", "", nil
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return "", "", fmtAssocQuery("invalid workspace")
	}
	taskUUID, err := util.ParseUUID(taskID)
	if err != nil {
		return "", "", fmtAssocQuery("invalid task")
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
		ID:          taskUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmtAssocQuery("task not found")
		}
		return "", "", err
	}
	if !task.IssueID.Valid {
		return "", "", nil
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          task.IssueID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		return "", "", err
	}
	return util.UUIDToString(task.IssueID), issue.Title, nil
}

func (h *Handler) recordAssocInboundEvent(ctx context.Context, command DispatchCommand, dispatchContext agentDispatchContext) {
	if h.Assoc == nil {
		return
	}
	ids := dispatchAssocIDs(command)
	if ids.ConversationID == "" || ids.EvidenceID == "" {
		return
	}
	if _, err := h.Assoc.RecordEvent(ctx, assoc.Event{
		WorkspaceID: uuidToString(dispatchContext.WorkspaceID),
		AgentID:     uuidToString(dispatchContext.AgentID),
		Source:      "inbound_im",
		Direction:   assoc.DirInbound,
		EvidenceID:  ids.EvidenceID,
		OccurredAt:  time.Now().UTC(),
		SceneKey:    ids.ConversationID,
		PersonKey:   ids.PersonID,
	}); err != nil {
		slog.Error("assoc inbound event not recorded", "error", err, "conversation_id", ids.ConversationID)
	}
}

func (h *Handler) associateDispatchIssue(ctx context.Context, command DispatchCommand, dispatchContext agentDispatchContext, issueID, issueTitle, runID string) {
	if h.Assoc == nil || issueID == "" {
		return
	}
	ids := dispatchAssocIDs(command)
	if err := h.Assoc.AssociateIssueConversation(ctx, assoc.AssociateInput{
		WorkspaceID:    uuidToString(dispatchContext.WorkspaceID),
		AgentID:        uuidToString(dispatchContext.AgentID),
		IssueID:        issueID,
		IssueTitle:     issueTitle,
		RunID:          runID,
		ConversationID: ids.ConversationID,
		EvidenceID:     ids.EvidenceID,
		PersonID:       ids.PersonID,
		PersonAliases:  ids.PersonAliases,
		Kind:           ids.Kind,
	}); err != nil {
		slog.Error("assoc issue conversation not linked", "error", err, "issue_id", issueID)
	}
}

type dispatchAssocIdentity struct {
	ConversationID string
	EvidenceID     string
	PersonID       string
	PersonAliases  []string
	Kind           string
}

func dispatchAssocIDs(command DispatchCommand) dispatchAssocIdentity {
	ids := dispatchAssocIdentity{
		ConversationID: strings.TrimSpace(command.Event.Data.Conversation.OpenConversationID),
		Kind:           command.Event.Data.Conversation.Type,
	}
	if n := len(command.Event.Data.Messages); n > 0 {
		ids.EvidenceID = strings.TrimSpace(command.Event.Data.Messages[n-1].OpenMsgID)
	}
	openID := strings.TrimSpace(command.Event.Data.Sender.OpenDingTalkID)
	if openID == "" {
		openID = strings.TrimSpace(command.Event.Data.Sender.SenderOpenDingTalkID)
	}
	staffID := strings.TrimSpace(command.Event.Data.Sender.StaffID)
	ids.PersonID, ids.PersonAliases = assoc.CanonicalPersonKey(staffID, openID)
	return ids
}
