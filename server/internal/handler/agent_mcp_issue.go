package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const (
	agentMCPIssueOriginType = "agent_mcp"
	agentMCPTitleMaxRunes   = 160
)

type agentMCPIssueDelegateArguments struct {
	Instruction    string   `json:"instruction"`
	RequestID      string   `json:"request_id,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	Title          string   `json:"title,omitempty"`
	Priority       string   `json:"priority,omitempty"`
	ProjectID      string   `json:"project_id,omitempty"`
	ParentIssueID  string   `json:"parent_issue_id,omitempty"`
	Stage          *int32   `json:"stage,omitempty"`
	StartDate      string   `json:"start_date,omitempty"`
	DueDate        string   `json:"due_date,omitempty"`
	AttachmentIDs  []string `json:"attachment_ids,omitempty"`
	AllowDuplicate bool     `json:"allow_duplicate,omitempty"`
}

type agentMCPContinueIssueArguments struct {
	IssueID       string   `json:"issue_id"`
	Instruction   string   `json:"instruction"`
	RequestID     string   `json:"request_id,omitempty"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
}

type agentMCPIssueArguments struct {
	IssueID string `json:"issue_id"`
}

type agentMCPReadArtifactArguments struct {
	ArtifactID string `json:"artifact_id"`
}

type agentMCPEmbeddedResource struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type agentMCPReadArtifactResult struct {
	Metadata map[string]any           `json:"metadata"`
	Resource agentMCPEmbeddedResource `json:"resource"`
}

type agentMCPPrincipalIDs struct {
	WorkspaceID  pgtype.UUID
	AgentID      pgtype.UUID
	EndpointID   pgtype.UUID
	ClientID     pgtype.UUID
	CredentialID pgtype.UUID
	OwnerID      pgtype.UUID
}

type agentMCPIssueRef struct {
	ID            string `json:"id"`
	Identifier    string `json:"identifier"`
	URL           string `json:"url,omitempty"`
	Title         string `json:"title"`
	Description   string `json:"description,omitempty"`
	Status        string `json:"status"`
	Priority      string `json:"priority"`
	ProjectID     string `json:"project_id,omitempty"`
	ParentIssueID string `json:"parent_issue_id,omitempty"`
	Stage         *int32 `json:"stage,omitempty"`
	StartDate     string `json:"start_date,omitempty"`
	DueDate       string `json:"due_date,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

type agentMCPIssueTaskProjection struct {
	ID         string           `json:"id"`
	Mode       string           `json:"mode"`
	RequestID  string           `json:"request_id"`
	Operation  string           `json:"operation"`
	Status     map[string]any   `json:"status"`
	Issue      agentMCPIssueRef `json:"issue"`
	Artifacts  []map[string]any `json:"artifacts"`
	Resources  []map[string]any `json:"resources"`
	Error      string           `json:"error,omitempty"`
	Failure    string           `json:"failure_reason,omitempty"`
	CreatedAt  string           `json:"created_at,omitempty"`
	StartedAt  string           `json:"started_at,omitempty"`
	FinishedAt string           `json:"completed_at,omitempty"`
}

func parseAgentMCPPrincipalIDs(principal a2aintegration.Principal) (agentMCPPrincipalIDs, error) {
	parse := func(raw string) (pgtype.UUID, error) {
		id, err := util.ParseUUID(strings.TrimSpace(raw))
		if err != nil || !id.Valid {
			return pgtype.UUID{}, errors.New("invalid MCP principal")
		}
		return id, nil
	}
	workspaceID, err := parse(principal.WorkspaceID)
	if err != nil {
		return agentMCPPrincipalIDs{}, err
	}
	agentID, err := parse(principal.AgentID)
	if err != nil {
		return agentMCPPrincipalIDs{}, err
	}
	endpointID, err := parse(principal.EndpointID)
	if err != nil {
		return agentMCPPrincipalIDs{}, err
	}
	clientID, err := parse(principal.ClientID)
	if err != nil {
		return agentMCPPrincipalIDs{}, err
	}
	credentialID, err := parse(principal.CredentialID)
	if err != nil {
		return agentMCPPrincipalIDs{}, err
	}
	ownerID, err := parse(principal.OwnerID)
	if err != nil {
		return agentMCPPrincipalIDs{}, err
	}
	return agentMCPPrincipalIDs{
		WorkspaceID: workspaceID, AgentID: agentID, EndpointID: endpointID,
		ClientID: clientID, CredentialID: credentialID, OwnerID: ownerID,
	}, nil
}

func (h *Handler) delegateAgentMCPIssue(ctx context.Context, args agentMCPIssueDelegateArguments, principal a2aintegration.Principal) (agentMCPIssueTaskProjection, error) {
	if h == nil || h.Queries == nil || h.TxStarter == nil || h.IssueService == nil {
		return agentMCPIssueTaskProjection{}, errors.New("Issue delegation service is unavailable")
	}
	ids, err := parseAgentMCPPrincipalIDs(principal)
	if err != nil {
		return agentMCPIssueTaskProjection{}, err
	}
	if err := validateAgentMCPIssueArguments(&args); err != nil {
		return agentMCPIssueTaskProjection{}, err
	}
	requestID := strings.TrimSpace(args.RequestID)
	if requestID == "" {
		requestID, err = newAgentMCPPublicID("req_")
		if err != nil {
			return agentMCPIssueTaskProjection{}, errors.New("failed to allocate request id")
		}
	}
	args.RequestID = requestID
	fingerprint, err := fingerprintAgentMCPIssueRequest(args)
	if err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("failed to fingerprint request")
	}
	publicTaskID, err := newAgentMCPPublicID("tsk_")
	if err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("failed to allocate task id")
	}
	claim, err := h.Queries.CreateAgentMCPDelegationClaim(ctx, db.CreateAgentMCPDelegationClaimParams{
		PublicTaskID: publicTaskID, RequestID: requestID, RequestFingerprint: fingerprint,
		Operation: "delegate", ClientID: ids.ClientID, CredentialID: ids.CredentialID,
		EndpointID: ids.EndpointID, WorkspaceID: ids.WorkspaceID, AgentID: ids.AgentID,
		PublicAgentID: principal.PublicAgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		claim, err = h.Queries.GetAgentMCPDelegationByRequest(ctx, db.GetAgentMCPDelegationByRequestParams{
			EndpointID: ids.EndpointID, ClientID: ids.ClientID, RequestID: requestID,
		})
	}
	if err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("MCP delegation is no longer authorized")
	}
	if claim.RequestFingerprint != fingerprint || claim.Operation != "delegate" {
		return agentMCPIssueTaskProjection{}, errors.New("request_id conflicts with a different MCP request")
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("failed to begin MCP delegation")
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	claim, err = qtx.LockAgentMCPDelegationAdmission(ctx, db.LockAgentMCPDelegationAdmissionParams{
		EndpointID: ids.EndpointID, ClientID: ids.ClientID, RequestID: requestID,
		AgentID: ids.AgentID, WorkspaceID: ids.WorkspaceID, OwnerID: ids.OwnerID,
		PublicAgentID: principal.PublicAgentID, CredentialID: ids.CredentialID,
	})
	if err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("MCP delegation is no longer authorized")
	}
	if claim.RequestFingerprint != fingerprint || claim.Operation != "delegate" {
		return agentMCPIssueTaskProjection{}, errors.New("request_id conflicts with a different MCP request")
	}
	if claim.IssueID.Valid && claim.RootLocalTaskID.Valid {
		if err := tx.Commit(ctx); err != nil {
			return agentMCPIssueTaskProjection{}, errors.New("failed to replay MCP delegation")
		}
		return h.getAgentMCPIssueTask(ctx, claim.PublicTaskID, ids)
	}

	issue, task, err := h.recoverOrCreateAgentMCPIssue(ctx, args, claim, ids)
	if err != nil {
		return agentMCPIssueTaskProjection{}, err
	}
	if _, err := qtx.BindAgentMCPDelegation(ctx, db.BindAgentMCPDelegationParams{
		ID: claim.ID, IssueID: issue.ID, RootLocalTaskID: task.ID,
	}); err != nil {
		return agentMCPIssueTaskProjection{}, fmt.Errorf("bind MCP delegation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("failed to commit MCP delegation")
	}
	return h.getAgentMCPIssueTask(ctx, claim.PublicTaskID, ids)
}

func (h *Handler) recoverOrCreateAgentMCPIssue(ctx context.Context, args agentMCPIssueDelegateArguments, claim db.AgentMcpDelegation, ids agentMCPPrincipalIDs) (db.Issue, db.AgentTaskQueue, error) {
	issue, err := h.Queries.GetIssueByOrigin(ctx, db.GetIssueByOriginParams{
		WorkspaceID: ids.WorkspaceID,
		OriginType:  pgtype.Text{String: agentMCPIssueOriginType, Valid: true},
		OriginID:    claim.ID,
	})
	if err == nil {
		task, taskErr := h.Queries.GetFirstAgentTaskForIssueAndAgent(ctx, db.GetFirstAgentTaskForIssueAndAgentParams{
			IssueID: issue.ID, AgentID: ids.AgentID,
		})
		if taskErr != nil {
			return db.Issue{}, db.AgentTaskQueue{}, errors.New("MCP Issue exists but its Agent task is unavailable")
		}
		return issue, task, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.Issue{}, db.AgentTaskQueue{}, errors.New("failed to recover MCP Issue")
	}

	projectID, err := optionalAgentMCPUUID(args.ProjectID)
	if err != nil {
		return db.Issue{}, db.AgentTaskQueue{}, errors.New("project_id must be a valid UUID")
	}
	parentIssueID, err := optionalAgentMCPUUID(args.ParentIssueID)
	if err != nil {
		return db.Issue{}, db.AgentTaskQueue{}, errors.New("parent_issue_id must be a valid UUID")
	}
	startDate, err := optionalAgentMCPDate(args.StartDate)
	if err != nil {
		return db.Issue{}, db.AgentTaskQueue{}, errors.New("start_date must use YYYY-MM-DD")
	}
	dueDate, err := optionalAgentMCPDate(args.DueDate)
	if err != nil {
		return db.Issue{}, db.AgentTaskQueue{}, errors.New("due_date must use YYYY-MM-DD")
	}
	attachmentIDs, err := optionalAgentMCPUUIDs(args.AttachmentIDs)
	if err != nil {
		return db.Issue{}, db.AgentTaskQueue{}, errors.New("attachment_ids must contain valid UUIDs")
	}
	priority := strings.TrimSpace(args.Priority)
	if priority == "" {
		priority = "none"
	}
	metadata, _ := json.Marshal(map[string]any{
		"source": "agent_mcp", "public_task_id": claim.PublicTaskID, "request_id": claim.RequestID,
	})
	result, err := h.IssueService.Create(ctx, service.IssueCreateParams{
		WorkspaceID: ids.WorkspaceID,
		Title:       deriveAgentMCPIssueTitle(args.Title, args.Instruction),
		Description: pgtype.Text{String: strings.TrimSpace(args.Instruction), Valid: true},
		Status:      "todo", Priority: priority,
		AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: ids.AgentID,
		CreatorType: "member", CreatorID: ids.OwnerID,
		ParentIssueID: parentIssueID, ProjectID: projectID, Stage: optionalAgentMCPStage(args.Stage),
		StartDate: startDate, DueDate: dueDate,
		OriginType: pgtype.Text{String: agentMCPIssueOriginType, Valid: true}, OriginID: claim.ID,
		AttachmentIDs: attachmentIDs, AllowDuplicate: args.AllowDuplicate, Metadata: metadata,
	}, service.IssueCreateOpts{
		ActorID: util.UUIDToString(ids.OwnerID), AnalyticsAgentID: util.UUIDToString(ids.AgentID), Platform: "mcp",
		BroadcastPayload: func(created db.Issue, _ []db.Attachment, _ []db.IssueLabel) map[string]any {
			return map[string]any{"issue": issueToResponse(created, h.getIssuePrefix(ctx, ids.WorkspaceID))}
		},
	})
	if err != nil {
		if errors.Is(err, service.ErrActiveDuplicate) && result.DuplicateIssue != nil {
			prefix := h.getIssuePrefix(ctx, ids.WorkspaceID)
			return db.Issue{}, db.AgentTaskQueue{}, fmt.Errorf(
				"active duplicate Issue %s-%d (%s); retry with allow_duplicate=true to create another",
				prefix, result.DuplicateIssue.Number, result.DuplicateIssue.Title,
			)
		}
		if errors.Is(err, service.ErrParentIssueNotFound) {
			return db.Issue{}, db.AgentTaskQueue{}, errors.New("parent_issue_id was not found in this workspace")
		}
		if errors.Is(err, service.ErrProjectNotFound) {
			return db.Issue{}, db.AgentTaskQueue{}, errors.New("project_id was not found in this workspace")
		}
		return db.Issue{}, db.AgentTaskQueue{}, fmt.Errorf("create MCP Issue: %w", err)
	}
	if result.EnqueuedTask == nil {
		return db.Issue{}, db.AgentTaskQueue{}, errors.New("MCP Issue was created but its Agent task was not enqueued")
	}
	return result.Issue, *result.EnqueuedTask, nil
}

func (h *Handler) getAgentMCPIssueTask(ctx context.Context, publicTaskID string, ids agentMCPPrincipalIDs) (agentMCPIssueTaskProjection, error) {
	row, err := h.Queries.GetAgentMCPDelegationTask(ctx, db.GetAgentMCPDelegationTaskParams{
		EndpointID: ids.EndpointID, ClientID: ids.ClientID, PublicTaskID: strings.TrimSpace(publicTaskID),
	})
	if err != nil {
		return agentMCPIssueTaskProjection{}, err
	}
	projection := h.projectAgentMCPIssueTask(row)
	attachments, err := h.listAgentMCPIssueArtifacts(ctx, row.IssueID, ids)
	if err != nil {
		return agentMCPIssueTaskProjection{}, err
	}
	projection.Resources = make([]map[string]any, 0, len(attachments))
	for _, attachment := range attachments {
		resource := make(map[string]any, len(attachment)+2)
		for key, value := range attachment {
			resource[key] = value
		}
		resource["type"] = "resource"
		resource["uri"] = fmt.Sprintf("multica://artifacts/%v", attachment["id"])
		projection.Resources = append(projection.Resources, resource)
	}
	return projection, nil
}

func (h *Handler) continueAgentMCPIssue(ctx context.Context, args agentMCPContinueIssueArguments, principal a2aintegration.Principal) (agentMCPIssueTaskProjection, error) {
	if h == nil || h.Queries == nil || h.TxStarter == nil || h.IssueCommentService == nil || h.TaskService == nil {
		return agentMCPIssueTaskProjection{}, errors.New("Issue continuation service is unavailable")
	}
	ids, err := parseAgentMCPPrincipalIDs(principal)
	if err != nil {
		return agentMCPIssueTaskProjection{}, err
	}
	issueID, err := util.ParseUUID(strings.TrimSpace(args.IssueID))
	if err != nil || !issueID.Valid {
		return agentMCPIssueTaskProjection{}, errors.New("issue_id must be a valid UUID")
	}
	args.Instruction = strings.TrimSpace(args.Instruction)
	if args.Instruction == "" {
		return agentMCPIssueTaskProjection{}, errors.New("instruction is required")
	}
	attachmentIDs, err := optionalAgentMCPUUIDs(args.AttachmentIDs)
	if err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("attachment_ids must contain valid UUIDs")
	}
	if _, err := h.Queries.GetAgentMCPDelegationByIssue(ctx, db.GetAgentMCPDelegationByIssueParams{
		EndpointID: ids.EndpointID, ClientID: ids.ClientID, IssueID: issueID,
	}); err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("Issue is not available through this MCP connection")
	}
	requestID := strings.TrimSpace(args.RequestID)
	if requestID == "" {
		requestID, err = newAgentMCPPublicID("req_")
		if err != nil {
			return agentMCPIssueTaskProjection{}, errors.New("failed to allocate request id")
		}
	}
	if len(requestID) > 256 || requestID != strings.TrimSpace(requestID) {
		return agentMCPIssueTaskProjection{}, errors.New("request_id must be trimmed and at most 256 characters")
	}
	fingerprintPayload := struct {
		IssueID       string   `json:"issue_id"`
		Instruction   string   `json:"instruction"`
		AttachmentIDs []string `json:"attachment_ids,omitempty"`
	}{IssueID: util.UUIDToString(issueID), Instruction: args.Instruction, AttachmentIDs: args.AttachmentIDs}
	encoded, _ := json.Marshal(fingerprintPayload)
	sum := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(sum[:])
	publicTaskID, err := newAgentMCPPublicID("tsk_")
	if err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("failed to allocate task id")
	}
	claim, err := h.Queries.CreateAgentMCPDelegationClaim(ctx, db.CreateAgentMCPDelegationClaimParams{
		PublicTaskID: publicTaskID, RequestID: requestID, RequestFingerprint: fingerprint,
		Operation: "follow_up", ClientID: ids.ClientID, CredentialID: ids.CredentialID,
		EndpointID: ids.EndpointID, WorkspaceID: ids.WorkspaceID, AgentID: ids.AgentID,
		PublicAgentID: principal.PublicAgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		claim, err = h.Queries.GetAgentMCPDelegationByRequest(ctx, db.GetAgentMCPDelegationByRequestParams{
			EndpointID: ids.EndpointID, ClientID: ids.ClientID, RequestID: requestID,
		})
	}
	if err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("MCP continuation is no longer authorized")
	}
	if claim.RequestFingerprint != fingerprint || claim.Operation != "follow_up" {
		return agentMCPIssueTaskProjection{}, errors.New("request_id conflicts with a different MCP request")
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("failed to begin MCP continuation")
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	claim, err = qtx.LockAgentMCPDelegationAdmission(ctx, db.LockAgentMCPDelegationAdmissionParams{
		EndpointID: ids.EndpointID, ClientID: ids.ClientID, RequestID: requestID,
		AgentID: ids.AgentID, WorkspaceID: ids.WorkspaceID, OwnerID: ids.OwnerID,
		PublicAgentID: principal.PublicAgentID, CredentialID: ids.CredentialID,
	})
	if err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("MCP continuation is no longer authorized")
	}
	if claim.RequestFingerprint != fingerprint || claim.Operation != "follow_up" {
		return agentMCPIssueTaskProjection{}, errors.New("request_id conflicts with a different MCP request")
	}
	if claim.IssueID.Valid && !sameUUIDValue(claim.IssueID, issueID) {
		return agentMCPIssueTaskProjection{}, errors.New("request_id is already bound to another Issue")
	}
	if !claim.IssueID.Valid {
		claim, err = qtx.SetAgentMCPDelegationIssue(ctx, db.SetAgentMCPDelegationIssueParams{IssueID: issueID, ID: claim.ID})
		if err != nil {
			return agentMCPIssueTaskProjection{}, errors.New("failed to bind MCP continuation to Issue")
		}
	}
	if claim.RootLocalTaskID.Valid {
		if err := tx.Commit(ctx); err != nil {
			return agentMCPIssueTaskProjection{}, errors.New("failed to replay MCP continuation")
		}
		return h.getAgentMCPIssueTask(ctx, claim.PublicTaskID, ids)
	}

	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: ids.WorkspaceID})
	if err != nil || !issue.AssigneeID.Valid || !sameUUIDValue(issue.AssigneeID, ids.AgentID) || !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" {
		return agentMCPIssueTaskProjection{}, errors.New("Issue is no longer assigned to this Agent")
	}
	comment, commentErr := h.Queries.GetAgentMCPFollowUpCommentByClaim(ctx, claim.ID)
	var task db.AgentTaskQueue
	if commentErr == nil {
		task, err = h.Queries.GetTaskForDelegatedComment(ctx, db.GetTaskForDelegatedCommentParams{
			IssueID: issue.ID, AgentID: ids.AgentID, CommentID: comment.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			task, err = h.TaskService.EnqueueTaskForIssueWithAgentIdentityContext(ctx, issue, "", comment.ID)
		}
	} else if errors.Is(commentErr, pgx.ErrNoRows) {
		followUp, followErr := h.IssueCommentService.CreateExternalFollowUp(ctx, service.IssueCommentCreateParams{
			Issue: issue, AuthorID: ids.OwnerID, Content: args.Instruction,
			AttachmentIDs: attachmentIDs, AgentMCPClaimID: claim.ID,
		}, service.IssueCommentCreateOpts{})
		if followErr != nil {
			if errors.Is(followErr, service.ErrIssueDispatchPending) {
				return agentMCPIssueTaskProjection{}, errors.New("Issue already has an active Agent task; retry after it reaches a terminal state")
			}
			return agentMCPIssueTaskProjection{}, fmt.Errorf("continue MCP Issue: %w", followErr)
		}
		task = followUp.Task
	} else {
		return agentMCPIssueTaskProjection{}, errors.New("failed to recover MCP follow-up")
	}
	if err != nil {
		return agentMCPIssueTaskProjection{}, fmt.Errorf("recover MCP follow-up task: %w", err)
	}
	if _, err := qtx.BindAgentMCPDelegationTask(ctx, db.BindAgentMCPDelegationTaskParams{
		RootLocalTaskID: task.ID, ID: claim.ID, IssueID: issue.ID,
	}); err != nil {
		return agentMCPIssueTaskProjection{}, fmt.Errorf("bind MCP continuation task: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentMCPIssueTaskProjection{}, errors.New("failed to commit MCP continuation")
	}
	return h.getAgentMCPIssueTask(ctx, claim.PublicTaskID, ids)
}

func (h *Handler) getAgentMCPIssue(ctx context.Context, issueID pgtype.UUID, ids agentMCPPrincipalIDs) (map[string]any, error) {
	if _, err := h.Queries.GetAgentMCPDelegationByIssue(ctx, db.GetAgentMCPDelegationByIssueParams{
		EndpointID: ids.EndpointID, ClientID: ids.ClientID, IssueID: issueID,
	}); err != nil {
		return nil, errors.New("Issue is not available through this MCP connection")
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: ids.WorkspaceID})
	if err != nil {
		return nil, errors.New("Issue was not found")
	}
	workspace, err := h.Queries.GetWorkspace(ctx, ids.WorkspaceID)
	if err != nil {
		return nil, errors.New("Workspace was not found")
	}
	comments, err := h.Queries.ListAgentMCPDelegationComments(ctx, db.ListAgentMCPDelegationCommentsParams{
		EndpointID: ids.EndpointID, ClientID: ids.ClientID, IssueID: issueID, RowLimit: 200,
	})
	if err != nil {
		return nil, errors.New("failed to list Issue comments")
	}
	commentPayload := make([]map[string]any, 0, len(comments))
	for _, comment := range comments {
		commentPayload = append(commentPayload, map[string]any{
			"id": util.UUIDToString(comment.ID), "author_type": comment.AuthorType,
			"content": comment.Content, "type": comment.Type, "created_at": timeOrEmpty(comment.CreatedAt),
		})
	}
	delegations, err := h.Queries.ListAgentMCPDelegationsByIssue(ctx, db.ListAgentMCPDelegationsByIssueParams{
		EndpointID: ids.EndpointID, ClientID: ids.ClientID, IssueID: issueID,
	})
	if err != nil {
		return nil, errors.New("failed to list Issue tasks")
	}
	taskPayload := make([]agentMCPIssueTaskProjection, 0, len(delegations))
	for _, delegation := range delegations {
		task, taskErr := h.getAgentMCPIssueTask(ctx, delegation.PublicTaskID, ids)
		if taskErr != nil {
			return nil, errors.New("failed to load Issue task")
		}
		taskPayload = append(taskPayload, task)
	}
	ref := h.agentMCPIssueRefFromRow(issue, workspace.Slug, workspace.IssuePrefix)
	return map[string]any{"issue": ref, "tasks": taskPayload, "comments": commentPayload}, nil
}

func (h *Handler) listAgentMCPIssueArtifacts(ctx context.Context, issueID pgtype.UUID, ids agentMCPPrincipalIDs) ([]map[string]any, error) {
	if _, err := h.Queries.GetAgentMCPDelegationByIssue(ctx, db.GetAgentMCPDelegationByIssueParams{
		EndpointID: ids.EndpointID, ClientID: ids.ClientID, IssueID: issueID,
	}); err != nil {
		return nil, errors.New("Issue is not available through this MCP connection")
	}
	attachments, err := h.Queries.ListAgentMCPDelegationArtifacts(ctx, db.ListAgentMCPDelegationArtifactsParams{
		WorkspaceID: ids.WorkspaceID, EndpointID: ids.EndpointID, ClientID: ids.ClientID, IssueID: issueID,
	})
	if err != nil {
		return nil, errors.New("failed to list Issue artifacts")
	}
	result := make([]map[string]any, 0, len(attachments))
	for _, attachment := range attachments {
		result = append(result, agentMCPArtifactMetadata(attachment))
	}
	return result, nil
}

func (h *Handler) readAgentMCPArtifact(ctx context.Context, artifactID pgtype.UUID, ids agentMCPPrincipalIDs) (agentMCPReadArtifactResult, error) {
	if h.Storage == nil {
		return agentMCPReadArtifactResult{}, errors.New("artifact storage is unavailable")
	}
	attachment, err := h.Queries.GetAgentMCPDelegationArtifact(ctx, db.GetAgentMCPDelegationArtifactParams{
		AttachmentID: artifactID, EndpointID: ids.EndpointID, ClientID: ids.ClientID, WorkspaceID: ids.WorkspaceID,
	})
	if err != nil {
		return agentMCPReadArtifactResult{}, errors.New("artifact was not found")
	}
	const maxInlineArtifactBytes = 2 << 20
	if attachment.SizeBytes > maxInlineArtifactBytes {
		return agentMCPReadArtifactResult{}, errors.New("artifact is larger than the 2 MiB inline MCP limit")
	}
	reader, err := h.Storage.GetReader(ctx, h.Storage.KeyFromURL(attachment.Url))
	if err != nil {
		return agentMCPReadArtifactResult{}, errors.New("artifact object was not found")
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, maxInlineArtifactBytes+1))
	if err != nil {
		return agentMCPReadArtifactResult{}, errors.New("failed to read artifact")
	}
	if len(body) > maxInlineArtifactBytes {
		return agentMCPReadArtifactResult{}, errors.New("artifact is larger than the 2 MiB inline MCP limit")
	}
	resource := agentMCPEmbeddedResource{
		URI: fmt.Sprintf("multica://artifacts/%s", util.UUIDToString(attachment.ID)), MIMEType: attachment.ContentType,
	}
	if isTextPreviewable(attachment.ContentType, attachment.Filename) && utf8.Valid(body) {
		resource.Text = string(body)
	} else {
		resource.Blob = base64.StdEncoding.EncodeToString(body)
	}
	return agentMCPReadArtifactResult{Metadata: agentMCPArtifactMetadata(attachment), Resource: resource}, nil
}

func (h *Handler) describeAgentMCP(ctx context.Context, principal a2aintegration.Principal, ids agentMCPPrincipalIDs) (map[string]any, error) {
	endpoint, err := h.Queries.GetPublishedAgentA2AEndpointByPublicID(ctx, db.GetPublishedAgentA2AEndpointByPublicIDParams{
		PublicAgentID: principal.PublicAgentID, AllowDisabledEndpoint: true,
	})
	if err != nil || !sameUUIDValue(endpoint.AgentID, ids.AgentID) || !sameUUIDValue(endpoint.WorkspaceID, ids.WorkspaceID) {
		return nil, errors.New("Agent profile is unavailable")
	}
	var skills any = []any{}
	if len(endpoint.CardSkills) > 0 {
		_ = json.Unmarshal(endpoint.CardSkills, &skills)
	}
	return map[string]any{
		"name": endpoint.CardName, "description": endpoint.CardDescription,
		"version": endpoint.CardVersion, "skills": skills,
		"workflow": "delegate_task creates a visible Issue by default; continue_issue adds a follow-up on the same Issue; files must be uploaded as Issue attachments before list_artifacts/read_artifact can return them",
	}, nil
}

func (h *Handler) agentMCPIssueRefFromRow(issue db.Issue, workspaceSlug, issuePrefix string) agentMCPIssueRef {
	issueURL := ""
	if origin := strings.TrimRight(firstNonEmpty(h.currentConfig().AppURL, h.currentConfig().FrontendOrigin), "/"); origin != "" {
		issueURL = fmt.Sprintf("%s/%s/issues/%s", origin, workspaceSlug, util.UUIDToString(issue.ID))
	}
	return agentMCPIssueRef{
		ID: util.UUIDToString(issue.ID), Identifier: fmt.Sprintf("%s-%d", issuePrefix, issue.Number), URL: issueURL,
		Title: issue.Title, Description: textOrEmpty(issue.Description), Status: issue.Status, Priority: issue.Priority,
		ProjectID: uuidOrEmpty(issue.ProjectID), ParentIssueID: uuidOrEmpty(issue.ParentIssueID), Stage: int4Ptr(issue.Stage),
		StartDate: dateOrEmpty(issue.StartDate), DueDate: dateOrEmpty(issue.DueDate),
		CreatedAt: timeOrEmpty(issue.CreatedAt), UpdatedAt: timeOrEmpty(issue.UpdatedAt),
	}
}

func agentMCPArtifactMetadata(attachment db.Attachment) map[string]any {
	return map[string]any{
		"id": util.UUIDToString(attachment.ID), "filename": attachment.Filename,
		"content_type": attachment.ContentType, "size_bytes": attachment.SizeBytes,
		"created_at": timeOrEmpty(attachment.CreatedAt),
	}
}

func sameUUIDValue(left, right pgtype.UUID) bool {
	return left.Valid && right.Valid && left.Bytes == right.Bytes
}

func (h *Handler) projectAgentMCPIssueTask(row db.GetAgentMCPDelegationTaskRow) agentMCPIssueTaskProjection {
	state := agentMCPLocalTaskState(row.TaskStatus)
	status := map[string]any{"state": state}
	if timestamp := agentMCPTaskTimestamp(row, state); timestamp != "" {
		status["timestamp"] = timestamp
	}
	artifacts := make([]map[string]any, 0, 1)
	if state == "TASK_STATE_COMPLETED" {
		var payload protocol.TaskCompletedPayload
		if json.Unmarshal(row.TaskResult, &payload) == nil {
			text := redact.Text(util.UnescapeBackslashEscapes(payload.Output))
			artifacts = append(artifacts, map[string]any{
				"id": "result", "name": "result", "parts": []map[string]any{{"type": "text", "text": text}},
			})
		}
	}
	issueURL := ""
	if origin := strings.TrimRight(firstNonEmpty(h.currentConfig().AppURL, h.currentConfig().FrontendOrigin), "/"); origin != "" {
		issueURL = fmt.Sprintf("%s/%s/issues/%s", origin, row.WorkspaceSlug, util.UUIDToString(row.IssueID))
	}
	projection := agentMCPIssueTaskProjection{
		ID: row.PublicTaskID, Mode: "issue", RequestID: row.RequestID, Operation: row.Operation,
		Status: status, Artifacts: artifacts,
		Issue: agentMCPIssueRef{
			ID: util.UUIDToString(row.IssueID), Identifier: fmt.Sprintf("%s-%d", row.IssuePrefix, row.IssueNumber), URL: issueURL,
			Title: row.IssueTitle, Description: textOrEmpty(row.IssueDescription), Status: row.IssueStatus, Priority: row.IssuePriority,
			ProjectID: uuidOrEmpty(row.ProjectID), ParentIssueID: uuidOrEmpty(row.ParentIssueID), Stage: int4Ptr(row.IssueStage),
			StartDate: dateOrEmpty(row.IssueStartDate), DueDate: dateOrEmpty(row.IssueDueDate),
			CreatedAt: timeOrEmpty(row.IssueCreatedAt), UpdatedAt: timeOrEmpty(row.IssueUpdatedAt),
		},
		Error: textOrEmpty(row.TaskError), Failure: textOrEmpty(row.TaskFailureReason),
		CreatedAt: timeOrEmpty(row.TaskCreatedAt), StartedAt: timeOrEmpty(row.TaskStartedAt), FinishedAt: timeOrEmpty(row.TaskCompletedAt),
	}
	return projection
}

func validateAgentMCPIssueArguments(args *agentMCPIssueDelegateArguments) error {
	args.Instruction = strings.TrimSpace(args.Instruction)
	if args.Instruction == "" {
		return errors.New("instruction is required")
	}
	args.Mode = strings.ToLower(strings.TrimSpace(args.Mode))
	if args.Mode == "" {
		args.Mode = "issue"
	}
	if args.Mode != "issue" && args.Mode != "direct" {
		return errors.New("mode must be issue or direct")
	}
	if args.RequestID != "" && (strings.TrimSpace(args.RequestID) != args.RequestID || utf8.RuneCountInString(args.RequestID) > 256) {
		return errors.New("request_id must be trimmed and at most 256 characters")
	}
	if args.Title != "" && utf8.RuneCountInString(strings.TrimSpace(args.Title)) > agentMCPTitleMaxRunes {
		return fmt.Errorf("title must be at most %d characters", agentMCPTitleMaxRunes)
	}
	if args.Priority != "" && !agentMCPStringIn(validIssuePriorities, args.Priority) {
		return errors.New("priority must be urgent, high, medium, low, or none")
	}
	if args.Stage != nil && *args.Stage < 1 {
		return errors.New("stage must be >= 1")
	}
	if _, err := optionalAgentMCPUUID(args.ProjectID); err != nil {
		return errors.New("project_id must be a valid UUID")
	}
	if _, err := optionalAgentMCPUUID(args.ParentIssueID); err != nil {
		return errors.New("parent_issue_id must be a valid UUID")
	}
	if _, err := optionalAgentMCPDate(args.StartDate); err != nil {
		return errors.New("start_date must use YYYY-MM-DD")
	}
	if _, err := optionalAgentMCPDate(args.DueDate); err != nil {
		return errors.New("due_date must use YYYY-MM-DD")
	}
	if _, err := optionalAgentMCPUUIDs(args.AttachmentIDs); err != nil {
		return errors.New("attachment_ids must contain valid UUIDs")
	}
	return nil
}

func agentMCPStringIn(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func fingerprintAgentMCPIssueRequest(args agentMCPIssueDelegateArguments) (string, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func newAgentMCPPublicID(prefix string) (string, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(random), nil
}

func deriveAgentMCPIssueTitle(title, instruction string) string {
	if candidate := strings.TrimSpace(title); candidate != "" {
		return candidate
	}
	candidate := strings.TrimSpace(strings.SplitN(instruction, "\n", 2)[0])
	if candidate == "" {
		candidate = "Delegated task"
	}
	runes := []rune(candidate)
	if len(runes) > agentMCPTitleMaxRunes {
		candidate = strings.TrimSpace(string(runes[:agentMCPTitleMaxRunes-1])) + "…"
	}
	return candidate
}

func optionalAgentMCPUUID(raw string) (pgtype.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return pgtype.UUID{}, nil
	}
	return util.ParseUUID(strings.TrimSpace(raw))
}

func optionalAgentMCPUUIDs(raw []string) ([]pgtype.UUID, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	result := make([]pgtype.UUID, 0, len(raw))
	seen := make(map[[16]byte]struct{}, len(raw))
	for _, value := range raw {
		id, err := util.ParseUUID(strings.TrimSpace(value))
		if err != nil || !id.Valid {
			return nil, errors.New("invalid UUID")
		}
		if _, exists := seen[id.Bytes]; exists {
			continue
		}
		seen[id.Bytes] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

func optionalAgentMCPDate(raw string) (pgtype.Date, error) {
	if strings.TrimSpace(raw) == "" {
		return pgtype.Date{}, nil
	}
	return util.ParseCalendarDate(strings.TrimSpace(raw))
}

func optionalAgentMCPStage(stage *int32) pgtype.Int4 {
	if stage == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *stage, Valid: true}
}

func agentMCPLocalTaskState(status string) string {
	switch status {
	case "queued", "deferred":
		return "TASK_STATE_SUBMITTED"
	case "dispatched", "running", "waiting_local_directory":
		return "TASK_STATE_WORKING"
	case "completed":
		return "TASK_STATE_COMPLETED"
	case "failed":
		return "TASK_STATE_FAILED"
	case "cancelled":
		return "TASK_STATE_CANCELED"
	default:
		return "TASK_STATE_UNKNOWN"
	}
}

func agentMCPTaskTimestamp(row db.GetAgentMCPDelegationTaskRow, state string) string {
	candidates := []pgtype.Timestamptz{row.TaskCreatedAt, row.DelegationCreatedAt}
	if state == "TASK_STATE_WORKING" {
		candidates = []pgtype.Timestamptz{row.TaskStartedAt, row.TaskDispatchedAt, row.TaskCreatedAt}
	}
	if strings.HasPrefix(state, "TASK_STATE_COMPLETED") || state == "TASK_STATE_FAILED" || state == "TASK_STATE_CANCELED" {
		candidates = []pgtype.Timestamptz{row.TaskCompletedAt, row.TaskStartedAt, row.TaskCreatedAt}
	}
	for _, candidate := range candidates {
		if candidate.Valid {
			return candidate.Time.UTC().Format(time.RFC3339Nano)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func textOrEmpty(value pgtype.Text) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func uuidOrEmpty(value pgtype.UUID) string {
	if value.Valid {
		return util.UUIDToString(value)
	}
	return ""
}

func int4Ptr(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	result := value.Int32
	return &result
}

func dateOrEmpty(value pgtype.Date) string {
	if !value.Valid {
		return ""
	}
	return value.Time.Format("2006-01-02")
}

func timeOrEmpty(value pgtype.Timestamptz) string {
	if !value.Valid {
		return ""
	}
	return value.Time.UTC().Format(time.RFC3339Nano)
}
