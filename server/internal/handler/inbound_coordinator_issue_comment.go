package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type inboundCoordinatorIssueCommentWriter struct {
	handler *Handler
}

// NewInboundCoordinatorIssueCommentWriter keeps the short loop on the same
// member-comment path as the product UI. That path publishes the comment and
// enqueues the Issue assignee; an agent-authored row would only be a record.
func NewInboundCoordinatorIssueCommentWriter(h *Handler) inboundcoord.IssueCommentWriter {
	return &inboundCoordinatorIssueCommentWriter{handler: h}
}

func (w *inboundCoordinatorIssueCommentWriter) AddMemberComment(
	ctx context.Context,
	turn inboundcoord.Turn,
	issue db.Issue,
	content string,
	parentID pgtype.UUID,
) (inboundcoord.IssueCommentEffect, error) {
	if w == nil || w.handler == nil || w.handler.IssueCommentService == nil {
		return inboundcoord.IssueCommentEffect{}, fmt.Errorf("issue comment service is not configured")
	}
	if parentID.Valid {
		parent, err := w.handler.Queries.GetComment(ctx, parentID)
		if err != nil || parent.IssueID != issue.ID {
			return inboundcoord.IssueCommentEffect{}, fmt.Errorf("parent comment is not on the recalled Issue")
		}
	}
	result, err := w.handler.IssueCommentService.CreateExternalFollowUp(ctx, service.IssueCommentCreateParams{
		Issue:           issue,
		AuthorID:        turn.UserID,
		Content:         content,
		ParentID:        parentID,
		DispatchContext: turn.IssueDispatchContext,
	}, service.IssueCommentCreateOpts{})
	if errors.Is(err, service.ErrIssueDispatchPending) {
		return inboundcoord.IssueCommentEffect{}, inboundcoord.ErrIssueBusy
	}
	if err != nil {
		return inboundcoord.IssueCommentEffect{}, err
	}
	prefix := w.handler.getIssuePrefix(ctx, issue.WorkspaceID)
	return inboundcoord.IssueCommentEffect{
		IssueID:         util.UUIDToString(issue.ID),
		IssueIdentifier: prefix + "-" + formatIssueNumber(issue.Number),
		CommentID:       util.UUIDToString(result.Comment.ID),
		TaskID:          util.UUIDToString(result.Task.ID),
	}, nil
}
