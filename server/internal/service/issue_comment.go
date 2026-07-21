package service

import (
	"context"
	"errors"
	"fmt"

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
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "member",
		AuthorID:    params.AuthorID,
		Content:     params.Content,
		Type:        "comment",
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
	if len(params.DispatchContext) > 0 {
		task, err = s.TaskService.EnqueueTaskForIssueWithDispatchContext(ctx, issue, params.AgentIdentityContextToken, params.DispatchContext, comment.ID)
	} else {
		task, err = s.TaskService.EnqueueTaskForIssueWithAgentIdentityContext(ctx, issue, params.AgentIdentityContextToken, comment.ID)
	}
	if err != nil {
		return IssueCommentCreateResult{Comment: comment, Attachments: attachments}, fmt.Errorf("enqueue issue follow-up: %w", err)
	}
	return IssueCommentCreateResult{Comment: comment, Attachments: attachments, Task: task}, nil
}
