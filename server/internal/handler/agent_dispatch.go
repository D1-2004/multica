package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const agentDispatchSchemaVersion = "3.0"

var (
	errDispatchAgentNotFound  = errors.New("dispatch agent not found")
	errDispatchAgentAmbiguous = errors.New("dispatch agent name is ambiguous")
)

type AgentDispatchPrompt struct {
	Text string `json:"text"`
}

type AgentDispatchAttachment struct {
	ID          string `json:"id"`
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

type AgentDispatchRequest struct {
	SchemaVersion  string             `json:"schemaVersion"`
	DispatchTaskID string             `json:"dispatchTaskId"`
	AgentID        string             `json:"agentId"`
	SessionID      string             `json:"sessionId"`
	Input          AgentDispatchInput `json:"input"`
	ContextToken   string             `json:"contextToken"`
}

type AgentDispatchResponse struct {
	DispatchTaskID  string `json:"dispatchTaskId"`
	Mode            string `json:"mode"`
	IssueID         string `json:"issueId,omitempty"`
	IssueIdentifier string `json:"issueIdentifier,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
	MessageID       string `json:"messageId,omitempty"`
	TaskID          string `json:"taskId,omitempty"`
}

// HandleAgentDispatch accepts the schema v3 callback emitted by the external
// message router. An empty sessionId creates an assigned issue; a non-empty
// sessionId sends another turn to that existing Multica direct-chat session.
// Agent identity supplies the workspace and owning user, so callers cannot
// select either with forgeable request headers.
func (h *Handler) HandleAgentDispatch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)
	var req AgentDispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SchemaVersion != agentDispatchSchemaVersion {
		writeError(w, http.StatusBadRequest, "unsupported schemaVersion")
		return
	}
	req.DispatchTaskID = strings.TrimSpace(req.DispatchTaskID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.DispatchTaskID == "" {
		writeError(w, http.StatusBadRequest, "dispatchTaskId is required")
		return
	}
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agentId is required")
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

	agent, err := h.resolveAgentDispatchTarget(r.Context(), req.AgentID)
	if errors.Is(err, errDispatchAgentNotFound) {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if errors.Is(err, errDispatchAgentAmbiguous) {
		writeError(w, http.StatusConflict, "agentId matches multiple agents; use the agent UUID")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent")
		return
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "agent is archived")
		return
	}
	if !agent.RuntimeID.Valid {
		writeError(w, http.StatusConflict, "agent has no runtime")
		return
	}

	content := buildAgentDispatchContent(req)
	if strings.TrimSpace(req.SessionID) == "" {
		h.createAgentDispatchIssue(w, r, req, agent, content)
		return
	}
	h.sendAgentDispatchChat(w, r, req, agent, content)
}

func (h *Handler) resolveAgentDispatchTarget(ctx context.Context, ref string) (db.Agent, error) {
	if id, err := util.ParseUUID(ref); err == nil {
		agent, err := h.Queries.GetAgent(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && agent.Kind != "user") {
			return db.Agent{}, errDispatchAgentNotFound
		}
		return agent, err
	}

	rows, err := h.DB.Query(ctx, `
		SELECT id
		FROM agent
		WHERE name = $1 AND archived_at IS NULL AND kind = 'user'
		ORDER BY created_at ASC
		LIMIT 2
	`, ref)
	if err != nil {
		return db.Agent{}, err
	}
	defer rows.Close()
	ids := make([]pgtype.UUID, 0, 2)
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return db.Agent{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return db.Agent{}, err
	}
	if len(ids) == 0 {
		return db.Agent{}, errDispatchAgentNotFound
	}
	if len(ids) > 1 {
		return db.Agent{}, errDispatchAgentAmbiguous
	}
	return h.Queries.GetAgent(ctx, ids[0])
}

func (h *Handler) createAgentDispatchIssue(w http.ResponseWriter, r *http.Request, req AgentDispatchRequest, agent db.Agent, content string) {
	if existing, found, err := h.findExistingAgentDispatchIssue(r.Context(), req.DispatchTaskID, agent); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check dispatch idempotency")
		return
	} else if found {
		writeJSON(w, http.StatusOK, existing)
		return
	}

	title := agentDispatchIssueTitle(req.DispatchTaskID, req.Input.UserPrompt.Text)
	prefix := h.getIssuePrefix(r.Context(), agent.WorkspaceID)
	res, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:    agent.WorkspaceID,
		Title:          title,
		Description:    pgtype.Text{String: content, Valid: true},
		Status:         "todo",
		Priority:       "none",
		AssigneeType:   pgtype.Text{String: "agent", Valid: true},
		AssigneeID:     agent.ID,
		CreatorType:    "member",
		CreatorID:      agent.OwnerID,
		AllowDuplicate: false,
	}, service.IssueCreateOpts{
		ActorID:          uuidToString(agent.OwnerID),
		AnalyticsAgentID: uuidToString(agent.ID),
		Platform:         "webhook",
		BroadcastPayload: func(issue db.Issue, _ []db.Attachment) map[string]any {
			return map[string]any{"issue": issueToResponse(issue, prefix)}
		},
	})
	if errors.Is(err, service.ErrActiveDuplicate) && res.DuplicateIssue != nil {
		issue := *res.DuplicateIssue
		writeJSON(w, http.StatusOK, AgentDispatchResponse{
			DispatchTaskID:  req.DispatchTaskID,
			Mode:            "issue",
			IssueID:         uuidToString(issue.ID),
			IssueIdentifier: prefix + "-" + fmt.Sprint(issue.Number),
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue")
		return
	}
	writeJSON(w, http.StatusCreated, AgentDispatchResponse{
		DispatchTaskID:  req.DispatchTaskID,
		Mode:            "issue",
		IssueID:         uuidToString(res.Issue.ID),
		IssueIdentifier: prefix + "-" + fmt.Sprint(res.Issue.Number),
	})
}

func (h *Handler) sendAgentDispatchChat(w http.ResponseWriter, r *http.Request, req AgentDispatchRequest, agent db.Agent, content string) {
	sessionID, err := util.ParseUUID(strings.TrimSpace(req.SessionID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "sessionId must be a Multica chat session UUID")
		return
	}
	session, err := h.Queries.GetChatSession(r.Context(), sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "chat session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat session")
		return
	}
	if session.AgentID != agent.ID {
		writeError(w, http.StatusConflict, "chat session belongs to a different agent")
		return
	}
	if session.Status != "active" {
		writeError(w, http.StatusConflict, "chat session is archived")
		return
	}
	if existing, found, err := h.findExistingAgentDispatchChat(r.Context(), req.DispatchTaskID, session); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check dispatch idempotency")
		return
	} else if found {
		writeJSON(w, http.StatusOK, existing)
		return
	}

	sent, err := h.TaskService.SendDirectChatMessage(
		r.Context(),
		session,
		agent,
		session.CreatorID,
		content,
		nil,
		"member",
		session.CreatorID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to send chat message")
		return
	}

	resolvedSessionID := uuidToString(session.ID)
	h.publishChat(protocol.EventChatMessage, uuidToString(session.WorkspaceID), "member", uuidToString(session.CreatorID), resolvedSessionID, protocol.ChatMessagePayload{
		ChatSessionID: resolvedSessionID,
		MessageID:     uuidToString(sent.Message.ID),
		Role:          "user",
		Content:       content,
		TaskID:        uuidToString(sent.Task.ID),
		CreatedAt:     timestampToString(sent.Message.CreatedAt),
	})

	writeJSON(w, http.StatusCreated, AgentDispatchResponse{
		DispatchTaskID: req.DispatchTaskID,
		Mode:           "chat",
		SessionID:      resolvedSessionID,
		MessageID:      uuidToString(sent.Message.ID),
		TaskID:         uuidToString(sent.Task.ID),
	})
}

func (h *Handler) findExistingAgentDispatchIssue(ctx context.Context, dispatchTaskID string, agent db.Agent) (AgentDispatchResponse, bool, error) {
	var issueID pgtype.UUID
	var number int32
	err := h.DB.QueryRow(ctx, `
		SELECT id, number
		FROM issue
		WHERE workspace_id = $1
		  AND assignee_type = 'agent'
		  AND assignee_id = $2
		  AND strpos(COALESCE(description, ''), $3) = 1
		ORDER BY created_at ASC
		LIMIT 1
	`, agent.WorkspaceID, agent.ID, agentDispatchMarker(dispatchTaskID)).Scan(&issueID, &number)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentDispatchResponse{}, false, nil
	}
	if err != nil {
		return AgentDispatchResponse{}, false, err
	}
	prefix := h.getIssuePrefix(ctx, agent.WorkspaceID)
	return AgentDispatchResponse{
		DispatchTaskID:  dispatchTaskID,
		Mode:            "issue",
		IssueID:         uuidToString(issueID),
		IssueIdentifier: prefix + "-" + fmt.Sprint(number),
	}, true, nil
}

func (h *Handler) findExistingAgentDispatchChat(ctx context.Context, dispatchTaskID string, session db.ChatSession) (AgentDispatchResponse, bool, error) {
	var messageID pgtype.UUID
	var taskID pgtype.UUID
	err := h.DB.QueryRow(ctx, `
		SELECT id, task_id
		FROM chat_message
		WHERE chat_session_id = $1
		  AND role = 'user'
		  AND task_id IS NOT NULL
		  AND strpos(content, $2) = 1
		ORDER BY created_at ASC
		LIMIT 1
	`, session.ID, agentDispatchMarker(dispatchTaskID)).Scan(&messageID, &taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentDispatchResponse{}, false, nil
	}
	if err != nil {
		return AgentDispatchResponse{}, false, err
	}
	return AgentDispatchResponse{
		DispatchTaskID: dispatchTaskID,
		Mode:           "chat",
		SessionID:      uuidToString(session.ID),
		MessageID:      uuidToString(messageID),
		TaskID:         uuidToString(taskID),
	}, true, nil
}

func agentDispatchIssueTitle(dispatchTaskID, userPrompt string) string {
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
	return "[" + dispatchTaskID + "] " + firstLine
}

func buildAgentDispatchContent(req AgentDispatchRequest) string {
	var b strings.Builder
	b.WriteString(agentDispatchMarker(req.DispatchTaskID))
	b.WriteString("\n\n")
	if systemPrompt := strings.TrimSpace(req.Input.SystemPrompt.Text); systemPrompt != "" {
		b.WriteString("## External system prompt\n\n")
		b.WriteString(systemPrompt)
		b.WriteString("\n\n")
	}
	b.WriteString("## User request\n\n")
	b.WriteString(strings.TrimSpace(req.Input.UserPrompt.Text))
	b.WriteString("\n")
	if len(req.Input.Attachments) > 0 {
		b.WriteString("\n## Attachments\n")
		for _, attachment := range req.Input.Attachments {
			fmt.Fprintf(&b, "\n- [%s](%s)", attachment.Name, attachment.DownloadURL)
			metadata := make([]string, 0, 4)
			if attachment.ID != "" {
				metadata = append(metadata, "id="+attachment.ID)
			}
			if attachment.Type != "" {
				metadata = append(metadata, "type="+attachment.Type)
			}
			if attachment.ContentType != "" {
				metadata = append(metadata, "contentType="+attachment.ContentType)
			}
			if attachment.SizeBytes > 0 {
				metadata = append(metadata, fmt.Sprintf("sizeBytes=%d", attachment.SizeBytes))
			}
			if len(metadata) > 0 {
				fmt.Fprintf(&b, " (%s)", strings.Join(metadata, ", "))
			}
			if attachment.ExpiresAt != nil {
				fmt.Fprintf(&b, " expiresAt=%d", *attachment.ExpiresAt)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## Dispatch context\n\n")
	fmt.Fprintf(&b, "- schemaVersion: `%s`\n- dispatchTaskId: `%s`\n", req.SchemaVersion, req.DispatchTaskID)
	if req.ContextToken != "" {
		fmt.Fprintf(&b, "- contextToken: `%s`\n", req.ContextToken)
	}
	return b.String()
}

func agentDispatchMarker(dispatchTaskID string) string {
	return "<!-- multica-agent-dispatch:" + dispatchTaskID + " -->"
}
