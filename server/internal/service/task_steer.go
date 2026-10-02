package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func taskProcessStopPending(task db.AgentTaskQueue) bool {
	var private struct {
		Pending bool `json:"process_stop_pending"`
	}
	return task.Status == "cancelled" && json.Unmarshal(task.Context, &private) == nil && private.Pending
}

// AcknowledgeTaskProcessStopped opens the durable cancellation barrier only
// after a daemon has positively confirmed process-group exit.
func (s *TaskService) AcknowledgeTaskProcessStopped(ctx context.Context, taskID pgtype.UUID) error {
	task, err := s.Queries.AckAgentTaskProcessStopped(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		task, err = s.Queries.GetAgentTask(ctx, taskID)
	}
	if err != nil {
		return err
	}
	s.triggerNextQueuedTaskForTerminal(ctx, task)
	return nil
}

type steerInputReceipt struct {
	Fingerprint string `json:"fingerprint"`
	CommentID   string `json:"comment_id"`
}

func (s *IssueCommentService) createSteeredExternalFollowUp(ctx context.Context, params IssueCommentCreateParams, opts IssueCommentCreateOpts) (IssueCommentCreateResult, error) {
	if s.TaskService.TxStarter == nil {
		return IssueCommentCreateResult{}, errors.New("steer transaction starter is not configured")
	}
	if params.Delegation != nil {
		return IssueCommentCreateResult{}, errors.New("delegation cannot steer")
	}
	tx, err := s.TaskService.TxStarter.Begin(ctx)
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	defer tx.Rollback(ctx)
	q := s.Queries.WithTx(tx)
	if _, err = q.LockIssueForExternalFollowUp(ctx, db.LockIssueForExternalFollowUpParams{ID: params.Issue.ID, WorkspaceID: params.Issue.WorkspaceID}); err != nil {
		return IssueCommentCreateResult{}, err
	}
	issue, err := q.GetIssue(ctx, params.Issue.ID)
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	if issue.AssigneeType.String != "agent" || issue.AssigneeID != params.Issue.AssigneeID {
		return IssueCommentCreateResult{}, errors.New("issue assignee changed before steer")
	}
	if _, err = q.GetAgentForClaimUpdate(ctx, issue.AssigneeID); err != nil {
		return IssueCommentCreateResult{}, err
	}
	key := strings.TrimSpace(params.IdempotencyKey)
	fingerprint := issueFollowUpFingerprint(params)
	if key != "" {
		id, lookupErr := q.GetSteerIssueReceiptTaskID(ctx, db.GetSteerIssueReceiptTaskIDParams{IssueID: issue.ID, AgentID: issue.AssigneeID, RequestKey: key})
		if lookupErr == nil {
			task, err := q.GetAgentTask(ctx, id)
			if err != nil {
				return IssueCommentCreateResult{}, err
			}
			var stored struct {
				Receipts map[string]steerInputReceipt `json:"steer_requests"`
			}
			if err = json.Unmarshal(task.Context, &stored); err != nil {
				return IssueCommentCreateResult{}, err
			}
			receipt := stored.Receipts[key]
			if receipt.Fingerprint != fingerprint {
				return IssueCommentCreateResult{}, ErrIssueFollowUpIdempotencyConflict
			}
			commentID, err := util.ParseUUID(receipt.CommentID)
			if err != nil {
				return IssueCommentCreateResult{}, err
			}
			comment, err := q.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: commentID, WorkspaceID: issue.WorkspaceID})
			if err != nil {
				return IssueCommentCreateResult{}, err
			}
			attachments, err := q.ListAttachmentsByComment(ctx, db.ListAttachmentsByCommentParams{CommentID: comment.ID, WorkspaceID: issue.WorkspaceID})
			if err != nil {
				return IssueCommentCreateResult{}, err
			}
			if err = tx.Commit(ctx); err != nil {
				return IssueCommentCreateResult{}, err
			}
			if task.Status == "queued" {
				s.TaskService.NotifyTaskEnqueued(ctx, task)
			}
			return IssueCommentCreateResult{Comment: comment, Attachments: attachments, Task: task}, nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return IssueCommentCreateResult{}, lookupErr
		}
	}
	active, activeErr := q.GetClaimedIssueTaskForSteer(ctx, db.GetClaimedIssueTaskForSteerParams{IssueID: issue.ID, AgentID: issue.AssigneeID})
	if activeErr != nil && !errors.Is(activeErr, pgx.ErrNoRows) {
		return IssueCommentCreateResult{}, activeErr
	}
	var preempted *db.AgentTaskQueue
	if activeErr == nil {
		cancelled, err := q.CancelAgentTaskForSteer(ctx, active.ID)
		if err != nil {
			return IssueCommentCreateResult{}, err
		}
		preempted = &cancelled
		if _, err := enqueueSteerCallbackCompletions(ctx, q, cancelled, "canceled", nil, "", ""); err != nil {
			return IssueCommentCreateResult{}, err
		}
	}
	comment, err := q.CreateComment(ctx, db.CreateCommentParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AuthorType: "member", AuthorID: params.AuthorID, Content: params.Content, Type: "comment", ParentID: params.ParentID, AgentMcpClaimID: params.AgentMCPClaimID})
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	if len(params.AttachmentIDs) > 0 {
		if err = q.LinkAttachmentsToComment(ctx, db.LinkAttachmentsToCommentParams{CommentID: comment.ID, IssueID: issue.ID, Column3: params.AttachmentIDs}); err != nil {
			return IssueCommentCreateResult{}, err
		}
	}
	private := map[string]json.RawMessage{}
	if len(params.DispatchContext) == 0 && activeErr == nil {
		var previous map[string]json.RawMessage
		if json.Unmarshal(active.Context, &previous) == nil {
			if ref, ok := previous[protocol.AgentSceneContextKey]; ok {
				private[protocol.AgentSceneContextKey] = ref
			}
		}
	}
	if len(params.DispatchContext) > 0 {
		if err = json.Unmarshal(params.DispatchContext, &private); err != nil {
			return IssueCommentCreateResult{}, err
		}
	}
	if private == nil {
		private = map[string]json.RawMessage{}
	}
	private["task_steer"] = json.RawMessage("true")
	if preempted != nil {
		private["steer_predecessor_task_id"], _ = json.Marshal(util.UUIDToString(preempted.ID))
	}
	private["agent_identity_context_token"], _ = json.Marshal(strings.TrimSpace(params.AgentIdentityContextToken))
	correctionContext, err := json.Marshal(private)
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	pending, pendingErr := q.GetLatestActiveTaskForIssueAndAgent(ctx, db.GetLatestActiveTaskForIssueAndAgentParams{IssueID: issue.ID, AgentID: issue.AssigneeID})
	if pendingErr == nil && pending.Status == "queued" {
		correctionContext, err = mergeSteerCorrectionContext(pending.Context, correctionContext)
		if err != nil {
			return IssueCommentCreateResult{}, err
		}
		if err = json.Unmarshal(correctionContext, &private); err != nil {
			return IssueCommentCreateResult{}, err
		}
	} else if pendingErr != nil && !errors.Is(pendingErr, pgx.ErrNoRows) {
		return IssueCommentCreateResult{}, pendingErr
	}
	agent, err := q.GetAgent(ctx, issue.AssigneeID)
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	overlay := s.TaskService.buildRuntimeMCPOverlay(ctx, params.AuthorID, agent)
	summary := truncateForSummary(params.Content, triggerSummaryMaxLen)
	task, err := q.MergeSteerIssueComment(ctx, db.MergeSteerIssueCommentParams{IssueID: issue.ID, AgentID: issue.AssigneeID, CommentID: comment.ID, AuthorID: params.AuthorID, Summary: pgtype.Text{String: summary, Valid: summary != ""}, RuntimeMcpOverlay: overlay.Overlay, RuntimeConnectedApps: overlay.ConnectedApps, CorrectionContext: correctionContext})
	if errors.Is(err, pgx.ErrNoRows) {
		txTasks := &TaskService{Queries: q, Bus: events.New(), FeatureFlags: s.TaskService.FeatureFlags, Composio: s.TaskService.Composio}
		task, err = txTasks.EnqueueTaskForIssueWithDispatchContext(ctx, issue, params.AgentIdentityContextToken, correctionContext, comment.ID)
	}
	if err != nil {
		return IssueCommentCreateResult{}, fmt.Errorf("enqueue steer successor: %w", err)
	}
	var stored struct {
		Receipts map[string]steerInputReceipt `json:"steer_requests"`
	}
	if err = json.Unmarshal(task.Context, &stored); err != nil {
		return IssueCommentCreateResult{}, err
	}
	if stored.Receipts == nil {
		stored.Receipts = map[string]steerInputReceipt{}
	}
	if key != "" {
		stored.Receipts[key] = steerInputReceipt{Fingerprint: fingerprint, CommentID: util.UUIDToString(comment.ID)}
	}
	private["steer_requests"], _ = json.Marshal(stored.Receipts)
	correctionContext, err = json.Marshal(private)
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	if err = q.SetSteerSuccessorContext(ctx, db.SetSteerSuccessorContextParams{ID: task.ID, CorrectionContext: correctionContext}); err != nil {
		return IssueCommentCreateResult{}, err
	}
	task, err = q.GetAgentTask(ctx, task.ID)
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	attachments, err := q.ListAttachmentsByComment(ctx, db.ListAttachmentsByCommentParams{CommentID: comment.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return IssueCommentCreateResult{}, err
	}
	s.publishExternalFollowUpComment(params, opts, comment, attachments)
	if preempted != nil {
		s.TaskService.captureTaskCancelled(ctx, *preempted)
		s.TaskService.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, *preempted)
		s.TaskService.NotifyTaskFinished(*preempted)
	}
	s.TaskService.publishIssueTaskEnqueued(ctx, task)
	return IssueCommentCreateResult{Comment: comment, Attachments: attachments, Task: task, PreemptedTask: preempted}, nil
}

// NotifySteerPredecessor restores post-commit stop observation when an outer
// Coordinator transaction used a buffered TaskService without a launcher.
func (s *TaskService) NotifySteerPredecessor(ctx context.Context, successor db.AgentTaskQueue) {
	var private struct {
		Predecessor string `json:"steer_predecessor_task_id"`
	}
	if json.Unmarshal(successor.Context, &private) != nil || private.Predecessor == "" {
		return
	}
	id, err := util.ParseUUID(private.Predecessor)
	if err != nil {
		return
	}
	old, err := s.Queries.GetAgentTask(ctx, id)
	if err != nil || old.AgentID != successor.AgentID || old.IssueID != successor.IssueID || !taskProcessStopPending(old) {
		return
	}
	s.CaptureCancelledTasks(ctx, []db.AgentTaskQueue{old})
	s.NotifyTaskFinished(old)
}
