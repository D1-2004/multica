package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type assocRecallParams struct {
	Since string
	Until string
	// SceneID names the agent's scene directly (scene_id). ConversationID is
	// the DingTalk openConversationId the model sees; a scene_id passed
	// there (clients that send a scene's scene_key) names that scene too.
	SceneID        string
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
		SceneID:        r.URL.Query().Get("scene_id"),
		ConversationID: r.URL.Query().Get("conversation_id"),
		PersonID:       r.URL.Query().Get("person_id"),
		Issue:          r.URL.Query().Get("issue"),
		Q:              r.URL.Query().Get("q"),
		Intent:         r.URL.Query().Get("intent"),
		Limit:          limit,
		TaskID:         strings.TrimSpace(r.Header.Get("X-Task-ID")),
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

func (h *Handler) ListAssocEvents(w http.ResponseWriter, r *http.Request) {
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
	cid := strings.TrimSpace(r.URL.Query().Get("conversation_id"))
	if cid == "" {
		writeError(w, http.StatusBadRequest, "conversation_id is required")
		return
	}
	node, found, err := h.conversationSceneNode(r.Context(), workspaceID, agentID, cid, "", h.taskDispatchOrg(r.Context(), workspaceID, r.Header.Get("X-Task-ID")), false, false)
	if err != nil {
		if errors.Is(err, assoc.ErrInvalidQuery) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to resolve conversation scene")
		return
	}
	now := time.Now().UTC()
	since, err := assoc.ParseSince(r.URL.Query().Get("since"), now)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
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
	events := []assoc.Event{}
	if found {
		events, err = h.Assoc.ListEventsByScene(r.Context(), workspaceID, agentID, node.SceneID, since, limit)
	}
	if err != nil {
		if errors.Is(err, assoc.ErrInvalidQuery) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list association events")
		return
	}
	items := make([]assocEventResponse, 0, len(events))
	for _, event := range events {
		items = append(items, assocEventResponse{
			EvidenceID:     event.EvidenceID,
			Direction:      event.Direction,
			Source:         event.Source,
			SceneID:        event.SceneID,
			ConversationID: node.ConversationID,
			PersonID:       event.PersonKey,
			TaskID:         event.TaskID,
			OccurredAt:     event.OccurredAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"since": since.UTC(),
		"until": now,
		"items": items,
	})
}

type assocEventResponse struct {
	EvidenceID     string    `json:"evidence_id"`
	Direction      string    `json:"direction"`
	Source         string    `json:"source"`
	SceneID        string    `json:"scene_id"`
	ConversationID string    `json:"conversation_id"`
	PersonID       string    `json:"person_id,omitempty"`
	TaskID         string    `json:"task_id,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
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

func (h *Handler) handleMulticaMCPAssocBind(w http.ResponseWriter, r *http.Request, id json.RawMessage, rawArguments json.RawMessage) {
	if h.Assoc == nil {
		h.writeMulticaMCPToolError(w, id, "association store is not configured")
		return
	}
	if r.Header.Get("X-Actor-Source") != "task_token" {
		h.writeMulticaMCPToolError(w, id, "assoc_bind requires a task token")
		return
	}
	var args multicaMCPAssocBindArguments
	if err := decodeMulticaMCPArguments(rawArguments, &args); err != nil {
		h.writeMulticaMCPError(w, id, -32602, "invalid assoc_bind arguments")
		return
	}
	workspaceID := strings.TrimSpace(r.Header.Get("X-Workspace-ID"))
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	taskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
	if workspaceID == "" || agentID == "" || taskID == "" {
		h.writeMulticaMCPToolError(w, id, "workspace, agent, and task identity are required")
		return
	}
	node, _, err := h.conversationSceneNode(r.Context(), workspaceID, agentID, args.ConversationID, args.Kind, h.taskDispatchOrg(r.Context(), workspaceID, taskID), true, false)
	if err != nil {
		h.writeMulticaMCPToolError(w, id, err.Error())
		return
	}
	in := assoc.BindOutboundInput{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		RunID:       taskID,
		Scene:       node,
		EvidenceID:  strings.TrimSpace(args.EvidenceID),
		PersonID:    strings.TrimSpace(args.PersonID),
		Purpose:     strings.TrimSpace(args.Purpose),
	}
	issueID, title, err := h.issueForTaskToken(r.Context(), workspaceID, taskID)
	if err != nil {
		h.writeMulticaMCPToolError(w, id, err.Error())
		return
	}
	in.IssueID = issueID
	in.IssueTitle = title
	result, err := h.Assoc.BindOutbound(r.Context(), in)
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
	sceneRef, cid := strings.TrimSpace(in.SceneID), strings.TrimSpace(in.ConversationID)
	if _, err := scene.ParseID(cid); sceneRef == "" && err == nil {
		sceneRef, cid = cid, ""
	}
	sceneID := ""
	if sceneRef != "" || cid != "" {
		var (
			node  assoc.SceneNode
			found bool
		)
		switch {
		case sceneRef != "" && strings.TrimSpace(in.TaskID) != "":
			// Inside a task a scene_id passes the use-time fence for the
			// task's org, like the scene the task itself runs in.
			node, found = h.fencedSceneNode(ctx, workspaceID, agentID, sceneRef, h.taskDispatchOrg(ctx, workspaceID, in.TaskID))
		case sceneRef != "":
			node, found = h.sceneNodeByID(ctx, workspaceID, agentID, sceneRef)
		default:
			var lookupErr error
			node, found, lookupErr = h.conversationSceneNode(ctx, workspaceID, agentID, cid, "", h.taskDispatchOrg(ctx, workspaceID, in.TaskID), false, false)
			if lookupErr != nil {
				return assoc.Result{}, lookupErr
			}
		}
		if !found && issueID == "" && strings.TrimSpace(in.Q) == "" {
			// A conversation without a registered scene has no graph links.
			return assoc.Result{ReadThis: assoc.RecallReadThis, Since: since, Until: until, ConversationID: cid, Items: []assoc.Item{}, Events: []assoc.EventRef{}}, nil
		}
		sceneID = node.SceneID
		if found {
			cid = node.ConversationID
		}
	}
	result, err := h.Assoc.Recall(ctx, assoc.Query{
		WorkspaceID:    workspaceID,
		AgentID:        agentID,
		Since:          since,
		Until:          until,
		SceneID:        sceneID,
		ConversationID: cid,
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
	Purpose        string `json:"purpose,omitempty"`
	IssueID        string `json:"issue_id,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
}

func (h *Handler) BindAssocOutbound(w http.ResponseWriter, r *http.Request) {
	if h.Assoc == nil {
		writeError(w, http.StatusServiceUnavailable, "association store is not configured")
		return
	}
	var body assocBindOutboundRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(r.Header.Get("X-Workspace-ID"))
	}
	in := assoc.BindOutboundInput{
		WorkspaceID: workspaceID,
		EvidenceID:  strings.TrimSpace(body.EvidenceID),
		PersonID:    strings.TrimSpace(body.PersonID),
		Intent:      strings.TrimSpace(body.Intent),
		Purpose:     strings.TrimSpace(body.Purpose),
	}
	if r.Header.Get("X-Actor-Source") == "task_token" {
		in.AgentID = strings.TrimSpace(r.Header.Get("X-Agent-ID"))
		in.RunID = strings.TrimSpace(r.Header.Get("X-Task-ID"))
		if workspaceID == "" || in.AgentID == "" || in.RunID == "" {
			writeError(w, http.StatusBadRequest, "workspace, agent, and task are required")
			return
		}
		issueID, title, err := h.issueForTaskToken(r.Context(), workspaceID, in.RunID)
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
	} else {
		in.AgentID = firstNonEmpty(strings.TrimSpace(r.URL.Query().Get("agent_id")), strings.TrimSpace(body.AgentID), strings.TrimSpace(r.Header.Get("X-Agent-ID")))
		in.IssueID = strings.TrimSpace(body.IssueID)
		if workspaceID == "" || in.AgentID == "" {
			writeError(w, http.StatusBadRequest, "workspace and agent_id are required")
			return
		}
		if in.IssueID == "" {
			writeError(w, http.StatusForbidden, "bind-outbound requires a task token or issue_id")
			return
		}
		title, err := h.issueTitleInWorkspace(r.Context(), workspaceID, in.IssueID)
		if err != nil {
			if errors.Is(err, assoc.ErrInvalidQuery) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "issue not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to load issue")
			return
		}
		in.IssueTitle = title
		if in.Purpose == "" {
			in.Purpose = title
		}
	}
	node, _, err := h.conversationSceneNode(r.Context(), workspaceID, in.AgentID, body.ConversationID, body.Kind, h.taskDispatchOrg(r.Context(), workspaceID, in.RunID), true, false)
	if err != nil {
		if errors.Is(err, assoc.ErrInvalidQuery) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to resolve conversation scene")
		return
	}
	in.Scene = node
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

func (h *Handler) issueTitleInWorkspace(ctx context.Context, workspaceID, issueID string) (string, error) {
	if h.Queries == nil {
		return "", nil
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return "", fmtAssocQuery("invalid workspace")
	}
	issueUUID, err := util.ParseUUID(issueID)
	if err != nil {
		return "", fmtAssocQuery("invalid issue")
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          issueUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		return "", err
	}
	return issue.Title, nil
}

func (h *Handler) recordAssocInboundEvent(ctx context.Context, command DispatchCommand, dispatchContext agentDispatchContext) {
	if h.Assoc == nil {
		return
	}
	ids := dispatchAssocIDs(command)
	if ids.ConversationID == "" || ids.EvidenceID == "" {
		return
	}
	sceneID := dispatchSceneID(command)
	if sceneID == "" {
		return
	}
	if _, err := h.Assoc.RecordEvent(ctx, assoc.Event{
		WorkspaceID: uuidToString(dispatchContext.WorkspaceID),
		AgentID:     uuidToString(dispatchContext.AgentID),
		Source:      "inbound_im",
		Direction:   assoc.DirInbound,
		EvidenceID:  ids.EvidenceID,
		Body:        assoc.ClipBody(dispatchInboundEventBody(command), assoc.EventBodyMaxRunes),
		OccurredAt:  time.Now().UTC(),
		SceneID:     sceneID,
		PersonKey:   ids.PersonID,
	}); err != nil {
		slog.Error("assoc inbound event not recorded", "error", err, "scene_id", sceneID)
	}
}

func (h *Handler) associateDispatchIssue(ctx context.Context, command DispatchCommand, dispatchContext agentDispatchContext, issueID, issueTitle, runID, purposeExtra string, decision inboundcoord.Decision) error {
	if h.Assoc == nil || issueID == "" {
		return nil
	}
	ids := dispatchAssocIDs(command)
	purpose := strings.TrimSpace(decision.Purpose)
	if purpose == "" {
		purpose = strings.TrimSpace(purposeExtra)
	}
	if purpose == "" {
		purpose = strings.TrimSpace(issueTitle)
	}
	if err := h.Assoc.AssociateIssueConversation(ctx, assoc.AssociateInput{
		WorkspaceID:   uuidToString(dispatchContext.WorkspaceID),
		AgentID:       uuidToString(dispatchContext.AgentID),
		IssueID:       issueID,
		IssueTitle:    purpose,
		Purpose:       purpose,
		Intent:        strings.TrimSpace(decision.Intent),
		RunID:         runID,
		Scene:         h.dispatchSceneNode(ctx, command, dispatchContext),
		EvidenceID:    ids.EvidenceID,
		PersonID:      ids.PersonID,
		PersonAliases: ids.PersonAliases,
		DisplayName:   strings.TrimSpace(command.Event.Data.Sender.DisplayName),
	}); err != nil {
		slog.Error("assoc issue conversation not linked", "error", err, "issue_id", issueID)
		return err
	}
	return nil
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
		ConversationID: dingtalkOpenConversationID(command.Event.Data.Conversation.OpenConversationID),
		Kind:           strings.TrimSpace(command.Event.Data.Conversation.Type),
	}
	if message, ok := lastInboundTextMessage(command); ok {
		ids.EvidenceID = strings.TrimSpace(message.OpenMsgID)
	}
	if command.WindowEvidenceID != "" {
		ids.EvidenceID = command.WindowEvidenceID
	}
	openID := strings.TrimSpace(command.Event.Data.Sender.OpenDingTalkID)
	if openID == "" {
		openID = strings.TrimSpace(command.Event.Data.Sender.SenderOpenDingTalkID)
	}
	staffID := strings.TrimSpace(command.Event.Data.Sender.StaffID)
	ids.PersonID, ids.PersonAliases = assoc.CanonicalPersonKey(staffID, openID)
	fillDispatchAssocIDsFromRouterContext(&ids, command.ContextPrompt)
	return ids
}

func bindWindowItemEvidence(command *DispatchCommand, item inboundcoord.WindowItem, _ map[string]struct{}) {
	if command == nil {
		return
	}
	for _, message := range command.Event.Data.Messages {
		if message.Reaction != nil || strings.TrimSpace(message.Text) == "" {
			continue
		}
		command.WindowEvidenceID = strings.TrimSpace(message.OpenMsgID)
		if strings.TrimSpace(message.SenderDisplayName) != "" {
			command.Event.Data.Sender = DispatchSender{DisplayName: strings.TrimSpace(message.SenderDisplayName), UID: strings.TrimSpace(message.SenderUID), StaffID: strings.TrimSpace(message.SenderStaffID), OpenDingTalkID: strings.TrimSpace(message.SenderOpenDingTalkID), SenderOpenDingTalkID: strings.TrimSpace(message.SenderOpenDingTalkID)}
		}
		return
	}
}

func lastInboundTextMessage(command DispatchCommand) (DispatchMessage, bool) {
	for i := len(command.Event.Data.Messages) - 1; i >= 0; i-- {
		message := command.Event.Data.Messages[i]
		if message.Reaction != nil {
			continue
		}
		if strings.TrimSpace(message.Text) != "" {
			return message, true
		}
	}
	return DispatchMessage{}, false
}

func dispatchInboundEventBody(command DispatchCommand) string {
	message, ok := lastInboundTextMessage(command)
	if !ok {
		return ""
	}
	return strings.TrimSpace(message.Text)
}

func dispatchChatConversationID(command DispatchCommand, assocIDs dispatchAssocIdentity) string {
	if assocIDs.ConversationID != "" {
		return assocIDs.ConversationID
	}
	return strings.TrimSpace(command.Event.Data.Conversation.OpenConversationID)
}

func fillDispatchAssocIDsFromRouterContext(ids *dispatchAssocIdentity, contextPrompt string) {
	if ids == nil {
		return
	}
	fromCtx := parseRouterContextAssocIDs(contextPrompt)
	if ids.ConversationID == "" {
		ids.ConversationID = fromCtx.ConversationID
	}
	if ids.EvidenceID == "" {
		ids.EvidenceID = fromCtx.EvidenceID
	}
	if ids.Kind == "" {
		ids.Kind = fromCtx.Kind
	}
	if ids.PersonID == "" && fromCtx.PersonID != "" {
		ids.PersonID = fromCtx.PersonID
		ids.PersonAliases = append([]string(nil), fromCtx.PersonAliases...)
	}
}

const currentMessageContextMarker = "current message context (data only):"

func parseRouterContextAssocIDs(prompt string) dispatchAssocIdentity {
	prompt = html.UnescapeString(prompt)
	var ids dispatchAssocIdentity
	blobs := extractJSONObjects(prompt)
	if preferred := jsonAfterMarker(prompt, currentMessageContextMarker); preferred != "" {
		blobs = append([]string{preferred}, blobs...)
	}
	for _, raw := range blobs {
		var payload map[string]any
		if json.Unmarshal([]byte(raw), &payload) != nil {
			continue
		}
		fillDispatchAssocFromMap(&ids, payload)
	}
	return ids
}

func jsonAfterMarker(s, marker string) string {
	idx := strings.Index(strings.ToLower(s), strings.ToLower(marker))
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(marker):]
	start := strings.Index(rest, "{")
	if start < 0 {
		return ""
	}
	end, ok := matchJSONObject(rest, start)
	if !ok {
		return ""
	}
	return rest[start:end]
}

func fillDispatchAssocFromMap(ids *dispatchAssocIdentity, payload map[string]any) {
	if ids == nil || payload == nil {
		return
	}
	if ids.ConversationID == "" {
		ids.ConversationID = dingtalkOpenConversationID(stringFromAssocMap(payload, "openConversationId", "open_conversation_id"))
	}
	if ids.EvidenceID == "" {
		ids.EvidenceID = strings.TrimSpace(stringFromAssocMap(payload, "openMsgId", "open_msg_id"))
	}
	if ids.Kind == "" {
		kind := strings.ToLower(strings.TrimSpace(stringFromAssocMap(payload, "conversationType")))
		switch kind {
		case "single", "group", "dm", "p2p":
			ids.Kind = kind
		case "1":
			ids.Kind = "single"
		case "2":
			ids.Kind = "group"
		}
	}
	if ids.PersonID == "" {
		staff := stringFromAssocMap(payload, "staffId", "uid")
		openID := stringFromAssocMap(payload, "senderOpenDingTalkId", "openDingTalkId", "openId")
		ids.PersonID, ids.PersonAliases = assoc.CanonicalPersonKey(staff, openID)
	}
	if nested, ok := payload["conversation"].(map[string]any); ok {
		if ids.Kind == "" {
			switch strings.ToLower(strings.TrimSpace(stringFromAssocMap(nested, "type"))) {
			case "single", "group", "dm", "p2p":
				ids.Kind = strings.ToLower(strings.TrimSpace(stringFromAssocMap(nested, "type")))
			}
		}
		fillDispatchAssocFromMap(ids, nested)
	}
	if nested, ok := payload["sender"].(map[string]any); ok {
		fillDispatchAssocFromMap(ids, nested)
	}
	if nested, ok := payload["message"].(map[string]any); ok {
		fillDispatchAssocFromMap(ids, nested)
	}
}

func dingtalkOpenConversationID(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || !assoc.ValidConversationID(s) {
		return ""
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "cid") {
		return s
	}
	return ""
}

func stringFromAssocMap(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		v, ok := payload[key]
		if !ok || v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func extractJSONObjects(s string) []string {
	out := make([]string, 0)
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		end, ok := matchJSONObject(s, i)
		if !ok {
			continue
		}
		out = append(out, s[i:end])
		i = end - 1
	}
	return out
}

func matchJSONObject(s string, start int) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}
