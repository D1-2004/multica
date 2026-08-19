package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var ErrIssueDispatchPending = errors.New("issue already has a pending dispatch")

type IssueCommentService struct {
	Queries     *db.Queries
	Bus         *events.Bus
	TaskService *TaskService
}

func NewIssueCommentService(q *db.Queries, bus *events.Bus, tasks *TaskService) *IssueCommentService {
	return &IssueCommentService{Queries: q, Bus: bus, TaskService: tasks}
}

type IssueCommentCreateParams struct {
	Issue                     db.Issue
	AuthorID                  pgtype.UUID
	Content                   string
	AttachmentIDs             []pgtype.UUID
	AgentIdentityContextToken string
	DispatchContext           []byte
	ParentTaskID              pgtype.UUID
	// AgentMCPClaimID makes an external MCP follow-up recoverable across a
	// process crash between comment creation and task binding.
	AgentMCPClaimID pgtype.UUID
	Delegation      *IssueDelegationFollowUpParams
}

type IssueDelegationFollowUpParams struct {
	CallbackUpdateURL string
	CallbackTarget    string
}

type IssueCommentCreateOpts struct {
	BroadcastPayload func(comment db.Comment, attachments []db.Attachment) map[string]any
}

type IssueCommentCreateResult struct {
	Comment     db.Comment
	Attachments []db.Attachment
	Task        db.AgentTaskQueue
}

// CreateExternalFollowUp persists a top-level member comment, binds imported
// attachments, and enqueues the issue's assigned agent through TaskService.
// It intentionally does not run mention/chat routing: this service is the
// issue-only continuation primitive used by external dispatch.
func (s *IssueCommentService) CreateExternalFollowUp(ctx context.Context, params IssueCommentCreateParams, opts IssueCommentCreateOpts) (IssueCommentCreateResult, error) {
	issue := params.Issue
	if s == nil || s.Queries == nil || s.TaskService == nil {
		return IssueCommentCreateResult{}, errors.New("issue comment service is not configured")
	}
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		return IssueCommentCreateResult{}, errors.New("issue is not assigned to an agent")
	}
	if params.Delegation != nil {
		return s.createDelegatedExternalFollowUp(ctx, params, opts)
	}
	active, err := s.Queries.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: issue.AssigneeID,
	})
	if err != nil {
		return IssueCommentCreateResult{}, fmt.Errorf("check active issue task: %w", err)
	}
	// External follow-ups must not merge with or queue behind an active task:
	// their task-scoped context token belongs to a distinct runtime launch.
	if active {
		return IssueCommentCreateResult{}, ErrIssueDispatchPending
	}

	comment, err := s.Queries.CreateComment(ctx, db.CreateCommentParams{
		IssueID:         issue.ID,
		WorkspaceID:     issue.WorkspaceID,
		AuthorType:      "member",
		AuthorID:        params.AuthorID,
		Content:         params.Content,
		Type:            "comment",
		AgentMcpClaimID: params.AgentMCPClaimID,
	})
	if err != nil {
		return IssueCommentCreateResult{}, fmt.Errorf("create issue comment: %w", err)
	}
	if len(params.AttachmentIDs) > 0 {
		if err := s.Queries.LinkAttachmentsToComment(ctx, db.LinkAttachmentsToCommentParams{
			CommentID: comment.ID,
			IssueID:   issue.ID,
			Column3:   params.AttachmentIDs,
		}); err != nil {
			return IssueCommentCreateResult{Comment: comment}, fmt.Errorf("link comment attachments: %w", err)
		}
	}
	attachments, err := s.Queries.ListAttachmentsByComment(ctx, db.ListAttachmentsByCommentParams{
		CommentID:   comment.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return IssueCommentCreateResult{Comment: comment}, fmt.Errorf("list comment attachments: %w", err)
	}
	if s.Bus != nil {
		payload := map[string]any{"comment_id": util.UUIDToString(comment.ID), "issue_id": util.UUIDToString(issue.ID)}
		if opts.BroadcastPayload != nil {
			payload = opts.BroadcastPayload(comment, attachments)
		}
		s.Bus.Publish(events.Event{
			Type:        protocol.EventCommentCreated,
			WorkspaceID: util.UUIDToString(issue.WorkspaceID),
			ActorType:   "member",
			ActorID:     util.UUIDToString(params.AuthorID),
			Payload:     payload,
		})
	}
	var task db.AgentTaskQueue
	if params.ParentTaskID.Valid {
		task, err = s.TaskService.EnqueueTaskForIssueWithDispatchContextAndParent(
			ctx,
			issue,
			params.AgentIdentityContextToken,
			params.DispatchContext,
			params.ParentTaskID,
			comment.ID,
		)
	} else if len(params.DispatchContext) > 0 {
		task, err = s.TaskService.EnqueueTaskForIssueWithDispatchContext(ctx, issue, params.AgentIdentityContextToken, params.DispatchContext, comment.ID)
	} else {
		task, err = s.TaskService.EnqueueTaskForIssueWithAgentIdentityContext(ctx, issue, params.AgentIdentityContextToken, comment.ID)
	}
	if err != nil {
		return IssueCommentCreateResult{Comment: comment, Attachments: attachments}, fmt.Errorf("enqueue issue follow-up: %w", err)
	}
	return IssueCommentCreateResult{Comment: comment, Attachments: attachments, Task: task}, nil
}

func (s *IssueCommentService) createDelegatedExternalFollowUp(ctx context.Context, params IssueCommentCreateParams, opts IssueCommentCreateOpts) (IssueCommentCreateResult, error) {
	if !params.ParentTaskID.Valid {
		return IssueCommentCreateResult{}, errors.New("delegation parent task is required")
	}
	agent, err := s.Queries.GetAgent(ctx, params.Issue.AssigneeID)
	if err != nil {
		return IssueCommentCreateResult{}, fmt.Errorf("load delegation target agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return IssueCommentCreateResult{}, errors.New("delegation target agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return IssueCommentCreateResult{}, errors.New("delegation target agent has no runtime")
	}
	overlay := s.TaskService.buildRuntimeMCPOverlay(ctx, params.AuthorID, agent)
	tx, err := s.TaskService.TxStarter.Begin(ctx)
	if err != nil {
		return IssueCommentCreateResult{}, fmt.Errorf("begin delegated follow-up: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	sourceTask, err := qtx.GetAgentTaskInWorkspaceForUpdate(ctx, db.GetAgentTaskInWorkspaceForUpdateParams{
		ID:          params.ParentTaskID,
		WorkspaceID: params.Issue.WorkspaceID,
	})
	if err != nil {
		return IssueCommentCreateResult{}, fmt.Errorf("lock delegation source task: %w", err)
	}
	if sourceTask.Status != "running" && sourceTask.Status != "dispatched" {
		return IssueCommentCreateResult{}, ErrDelegationSourceInactive
	}
	if _, childErr := qtx.GetDelegatedChildTaskByParent(ctx, sourceTask.ID); childErr == nil {
		return IssueCommentCreateResult{}, ErrDelegationAlreadyExists
	} else if !errors.Is(childErr, pgx.ErrNoRows) {
		return IssueCommentCreateResult{}, fmt.Errorf("check delegated child task: %w", childErr)
	}
	if _, commentErr := qtx.GetDelegatedMemberCommentBySourceTask(ctx, sourceTask.ID); commentErr == nil {
		return IssueCommentCreateResult{}, ErrDelegationAlreadyExists
	} else if !errors.Is(commentErr, pgx.ErrNoRows) {
		return IssueCommentCreateResult{}, fmt.Errorf("check delegated member comment: %w", commentErr)
	}
	comment, err := qtx.CreateComment(ctx, db.CreateCommentParams{
		IssueID:         params.Issue.ID,
		WorkspaceID:     params.Issue.WorkspaceID,
		AuthorType:      "member",
		AuthorID:        params.AuthorID,
		Content:         params.Content,
		Type:            "comment",
		SourceTaskID:    sourceTask.ID,
		AgentMcpClaimID: params.AgentMCPClaimID,
	})
	if err != nil {
		return IssueCommentCreateResult{}, fmt.Errorf("create issue comment: %w", err)
	}
	if len(params.AttachmentIDs) > 0 {
		if err := qtx.LinkAttachmentsToComment(ctx, db.LinkAttachmentsToCommentParams{
			CommentID: comment.ID,
			IssueID:   params.Issue.ID,
			Column3:   params.AttachmentIDs,
		}); err != nil {
			return IssueCommentCreateResult{}, fmt.Errorf("link comment attachments: %w", err)
		}
	}
	attachments, err := qtx.ListAttachmentsByComment(ctx, db.ListAttachmentsByCommentParams{
		CommentID:   comment.ID,
		WorkspaceID: params.Issue.WorkspaceID,
	})
	if err != nil {
		return IssueCommentCreateResult{}, fmt.Errorf("list comment attachments: %w", err)
	}
	agent, err = qtx.GetAgent(ctx, params.Issue.AssigneeID)
	if err != nil {
		return IssueCommentCreateResult{}, fmt.Errorf("reload delegation target agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return IssueCommentCreateResult{}, errors.New("delegation target agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return IssueCommentCreateResult{}, errors.New("delegation target agent has no runtime")
	}
	triggerSummary := truncateForSummary(params.Content, triggerSummaryMaxLen)
	taskCreated := false
	task := db.AgentTaskQueue{}
	merged, mergeErr := qtx.MergeDelegatedCommentIntoPendingTask(ctx, db.MergeDelegatedCommentIntoPendingTaskParams{
		IssueID:             params.Issue.ID,
		AgentID:             params.Issue.AssigneeID,
		NewTriggerCommentID: comment.ID,
		NewTriggerSummary:   pgtype.Text{String: triggerSummary, Valid: triggerSummary != ""},
	})
	switch {
	case mergeErr == nil:
		task, err = qtx.GetAgentTask(ctx, merged.ID)
		if err != nil {
			return IssueCommentCreateResult{}, fmt.Errorf("load merged delegated follow-up task: %w", err)
		}
	case !errors.Is(mergeErr, pgx.ErrNoRows):
		return IssueCommentCreateResult{}, fmt.Errorf("merge delegated follow-up task: %w", mergeErr)
	default:
		pending, pendingErr := qtx.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
			IssueID: params.Issue.ID,
			AgentID: params.Issue.AssigneeID,
		})
		if pendingErr != nil {
			return IssueCommentCreateResult{}, fmt.Errorf("check pending delegated follow-up task: %w", pendingErr)
		}
		if pending {
			task, err = qtx.GetLatestActiveTaskForIssueAndAgent(ctx, db.GetLatestActiveTaskForIssueAndAgentParams{
				IssueID: params.Issue.ID,
				AgentID: params.Issue.AssigneeID,
			})
			if err != nil {
				return IssueCommentCreateResult{}, fmt.Errorf("load delegated follow-up queue anchor: %w", err)
			}
			break
		}
		// The delegated follow-up carries the same accountable human as the
		// source Chat run, exactly like the create-mode delegation in
		// IssueService. accountable_user_id must be stamped alongside
		// originator_user_id: migration 199 dropped the transitional
		// originator_source IS NULL exemption, so a row with an originator but
		// no accountable now fails agent_task_queue_accountable_matches_originator.
		originatorUserID := params.AuthorID
		if !originatorUserID.Valid {
			originatorUserID = sourceTask.OriginatorUserID
		}
		accountableUserID := originatorUserID
		task, err = qtx.CreateAgentTask(ctx, db.CreateAgentTaskParams{
			AgentID:                   params.Issue.AssigneeID,
			RuntimeID:                 agent.RuntimeID,
			IssueID:                   params.Issue.ID,
			Priority:                  priorityToInt(params.Issue.Priority),
			TriggerCommentID:          comment.ID,
			TriggerSummary:            pgtype.Text{String: triggerSummary, Valid: triggerSummary != ""},
			OriginatorUserID:          originatorUserID,
			AccountableUserID:         accountableUserID,
			OriginatorSource:          pgtype.Text{String: "delegation", Valid: true},
			DelegatedFromTaskID:       sourceTask.ID,
			TriggerEvidenceKind:       pgtype.Text{String: "comment", Valid: true},
			TriggerEvidenceRefID:      comment.ID,
			RuntimeMcpOverlay:         overlay.Overlay,
			RuntimeConnectedApps:      overlay.ConnectedApps,
			ParentTaskID:              sourceTask.ID,
			AgentIdentityContextToken: agentIdentityContextTokenText(params.AgentIdentityContextToken),
			DispatchContext:           params.DispatchContext,
		})
		if err != nil {
			return IssueCommentCreateResult{}, fmt.Errorf("create delegated follow-up task: %w", err)
		}
		taskCreated = true
	}
	if params.Delegation.CallbackUpdateURL != "" {
		workspace, workspaceErr := qtx.GetWorkspace(ctx, params.Issue.WorkspaceID)
		if workspaceErr != nil {
			return IssueCommentCreateResult{}, fmt.Errorf("load delegation workspace: %w", workspaceErr)
		}
		if _, updateErr := qtx.EnqueueTaskExecutionUpdate(ctx, db.EnqueueTaskExecutionUpdateParams{
			RootTaskID:      sourceTask.ID,
			TargetTaskID:    task.ID,
			IssueID:         params.Issue.ID,
			IssueIdentifier: fmt.Sprintf("%s-%d", workspace.IssuePrefix, params.Issue.Number),
			CallbackUrl:     params.Delegation.CallbackUpdateURL,
			TargetIdentity:  params.Delegation.CallbackTarget,
			RequestID:       "multica-handoff:" + util.UUIDToString(sourceTask.ID),
			AgentID:         sourceTask.AgentID,
			TargetAgentID:   task.AgentID,
			UpdateType:      "delegated_to_issue",
		}); updateErr != nil {
			return IssueCommentCreateResult{}, fmt.Errorf("enqueue delegation update: %w", updateErr)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return IssueCommentCreateResult{}, fmt.Errorf("commit delegated follow-up: %w", err)
	}
	s.publishExternalFollowUpComment(params, opts, comment, attachments)
	if taskCreated {
		s.TaskService.publishIssueTaskEnqueued(ctx, task)
	}
	return IssueCommentCreateResult{Comment: comment, Attachments: attachments, Task: task}, nil
}

func (s *IssueCommentService) publishExternalFollowUpComment(params IssueCommentCreateParams, opts IssueCommentCreateOpts, comment db.Comment, attachments []db.Attachment) {
	if s.Bus == nil {
		return
	}
	payload := map[string]any{"comment_id": util.UUIDToString(comment.ID), "issue_id": util.UUIDToString(params.Issue.ID)}
	if opts.BroadcastPayload != nil {
		payload = opts.BroadcastPayload(comment, attachments)
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: util.UUIDToString(params.Issue.WorkspaceID),
		ActorType:   "member",
		ActorID:     util.UUIDToString(params.AuthorID),
		Payload:     payload,
	})
}
