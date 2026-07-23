package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

var synchronousCompletionCallbackPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var routerTargetIdentityPattern = regexp.MustCompile(`^router-target:v1:sha256:[a-f0-9]{64}$`)
var ErrTaskCompletionConflict = errors.New("task completion conflicts with existing terminal result")

type TaskCompletion struct {
	RequestID         string
	CallbackURL       string
	TargetIdentity    string
	RootTaskID        pgtype.UUID
	TerminalTaskID    pgtype.UUID
	AgentID           pgtype.UUID
	ExternalSessionID string
	ExecutionStatus   string
	ResultMessage     string
	Error             string
	FailureReason     string
}

type taskCompletionTarget struct {
	RootTaskID  pgtype.UUID
	CallbackURL string
	TargetIdentity string
}

type TaskCompletionNotifier interface {
	NotifyTaskCompletion()
}

func buildTaskCompletion(
	target taskCompletionTarget,
	task db.AgentTaskQueue,
	status string,
	result []byte,
	lastReply string,
	errMessage string,
	failureReason string,
) TaskCompletion {
	resultMessage := redact.Text(util.UnescapeBackslashEscapes(lastReply))
	if status == "completed" {
		var payload protocol.TaskCompletedPayload
		if json.Unmarshal(result, &payload) == nil {
			resultMessage = redact.Text(util.UnescapeBackslashEscapes(payload.Output))
		}
	}
	return TaskCompletion{
		RequestID:         "multica-terminal:" + util.UUIDToString(target.RootTaskID),
		CallbackURL:       target.CallbackURL,
		TargetIdentity:    target.TargetIdentity,
		RootTaskID:        target.RootTaskID,
		TerminalTaskID:    task.ID,
		AgentID:           task.AgentID,
		ExternalSessionID: task.SessionID.String,
		ExecutionStatus:   status,
		ResultMessage:     resultMessage,
		Error:             redact.Text(errMessage),
		FailureReason:     failureReason,
	}
}

func (s *TaskService) enqueueTaskCompletionInTx(
	ctx context.Context,
	qtx *db.Queries,
	task db.AgentTaskQueue,
	status string,
	result []byte,
	errMessage string,
	failureReason string,
) (bool, error) {
	targetRow, err := qtx.GetTaskCompletionTarget(ctx, task.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lastReply := ""
	if status == "failed" {
		reply, replyErr := qtx.GetLastTaskReplyText(ctx, task.ID)
		if replyErr == nil {
			lastReply = reply.String
		} else if !errors.Is(replyErr, pgx.ErrNoRows) {
			return false, replyErr
		}
	}
	completion := buildTaskCompletion(
		taskCompletionTarget{
			RootTaskID:  targetRow.RootTaskID,
			CallbackURL: targetRow.CallbackUrl,
			TargetIdentity: targetRow.TargetIdentity,
		},
		task,
		status,
		result,
		lastReply,
		errMessage,
		failureReason,
	)
	_, err = qtx.EnqueueTaskCompletion(ctx, db.EnqueueTaskCompletionParams{
		RootTaskID:        completion.RootTaskID,
		TerminalTaskID:    completion.TerminalTaskID,
		CallbackUrl:       completion.CallbackURL,
		TargetIdentity:    completion.TargetIdentity,
		RequestID:         completion.RequestID,
		AgentID:           completion.AgentID,
		ExecutionStatus:   completion.ExecutionStatus,
		ResultMessage:     completion.ResultMessage,
		ExternalSessionID: pgtype.Text{String: completion.ExternalSessionID, Valid: completion.ExternalSessionID != ""},
		Error:             pgtype.Text{String: completion.Error, Valid: completion.Error != ""},
		FailureReason:     pgtype.Text{String: completion.FailureReason, Valid: completion.FailureReason != ""},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrTaskCompletionConflict
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

type failedTaskFinalization struct {
	Task             db.AgentTaskQueue
	Retry            *db.AgentTaskQueue
	RetryCreated     bool
	CompletionQueued bool
}

func (s *TaskService) finalizeFailedTask(
	ctx context.Context,
	taskID pgtype.UUID,
) (failedTaskFinalization, error) {
	var result failedTaskFinalization
	parent, err := s.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		return result, err
	}

	var retryOverlay runtimeMCPOverlayData
	reason := parent.FailureReason.String
	if retryEligible(reason, parent) {
		if agent, agentErr := s.Queries.GetAgent(ctx, parent.AgentID); agentErr != nil {
			slog.Warn("task auto-retry: load agent for overlay failed",
				"parent_task_id", util.UUIDToString(parent.ID),
				"agent_id", util.UUIDToString(parent.AgentID),
				"error", agentErr,
			)
		} else {
			retryOverlay = s.buildRuntimeMCPOverlay(ctx, parent.OriginatorUserID, agent)
		}
	}

	err = s.runInTx(ctx, func(qtx *db.Queries) error {
		locked, lockErr := qtx.GetAgentTaskForCompletionFinalization(ctx, taskID)
		if lockErr != nil {
			return lockErr
		}
		result.Task = locked
		if locked.Status != "failed" {
			return nil
		}
		child, childErr := qtx.GetRetryChildByParent(ctx, locked.ID)
		if childErr == nil {
			result.Retry = &child
			return nil
		}
		if !errors.Is(childErr, pgx.ErrNoRows) {
			return childErr
		}

		reason = locked.FailureReason.String
		if retryEligible(reason, locked) {
			child, createErr := qtx.CreateRetryTask(ctx, db.CreateRetryTaskParams{
				ID:                   locked.ID,
				RuntimeMcpOverlay:    retryOverlay.Overlay,
				RuntimeConnectedApps: retryOverlay.ConnectedApps,
			})
			if createErr != nil {
				return fmt.Errorf("create retry task: %w", createErr)
			}
			result.Retry = &child
			result.RetryCreated = true
			return nil
		}

		queued, enqueueErr := s.enqueueTaskCompletionInTx(
			ctx,
			qtx,
			locked,
			"failed",
			locked.Result,
			locked.Error.String,
			reason,
		)
		if enqueueErr != nil {
			return fmt.Errorf("enqueue task completion: %w", enqueueErr)
		}
		result.CompletionQueued = queued
		return nil
	})
	if err != nil {
		return failedTaskFinalization{}, err
	}
	if result.RetryCreated && result.Retry != nil {
		slog.Info("task auto-retry enqueued",
			"parent_task_id", util.UUIDToString(result.Task.ID),
			"child_task_id", util.UUIDToString(result.Retry.ID),
			"reason", reason,
			"attempt", result.Retry.Attempt,
			"max_attempts", result.Retry.MaxAttempts,
		)
		s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, *result.Retry)
		s.NotifyTaskEnqueued(ctx, *result.Retry)
	}
	if result.CompletionQueued && s.CompletionNotifier != nil {
		s.CompletionNotifier.NotifyTaskCompletion()
	}
	return result, nil
}

func (s *TaskService) ReconcileTaskCompletions(
	ctx context.Context,
	targetIdentity string,
	limit int32,
) (int, error) {
	if s == nil || s.Queries == nil || limit <= 0 ||
		!routerTargetIdentityPattern.MatchString(targetIdentity) {
		return 0, nil
	}
	taskIDs, err := s.Queries.ListTerminalTaskIDsMissingCompletion(
		ctx,
		db.ListTerminalTaskIDsMissingCompletionParams{
			BatchLimit:           limit,
			WorkerTargetIdentity: targetIdentity,
		},
	)
	if err != nil {
		return 0, err
	}
	reconciled := 0
	for _, taskID := range taskIDs {
		queued := false
		task, taskErr := s.Queries.GetAgentTask(ctx, taskID)
		if taskErr != nil {
			return reconciled, taskErr
		}
		if task.Status == "failed" {
			finalized, finalizeErr := s.finalizeFailedTask(ctx, taskID)
			if finalizeErr != nil {
				return reconciled, finalizeErr
			}
			if finalized.CompletionQueued {
				reconciled++
			}
			continue
		}
		if err := s.runInTx(ctx, func(qtx *db.Queries) error {
			task, err := qtx.GetAgentTask(ctx, taskID)
			if err != nil {
				return err
			}
			status := "failed"
			errMessage := task.Error.String
			failureReason := task.FailureReason.String
			if task.Status == "completed" {
				status = "completed"
				errMessage = ""
				failureReason = ""
			} else if task.Status == "cancelled" {
				if errMessage == "" {
					errMessage = "task cancelled"
				}
				if failureReason == "" {
					failureReason = "cancelled"
				}
			} else if task.Status != "failed" {
				return nil
			}
			var enqueueErr error
			queued, enqueueErr = s.enqueueTaskCompletionInTx(
				ctx,
				qtx,
				task,
				status,
				task.Result,
				errMessage,
				failureReason,
			)
			return enqueueErr
		}); err != nil {
			return reconciled, err
		}
		if queued {
			reconciled++
		}
	}
	if reconciled > 0 && s.CompletionNotifier != nil {
		s.CompletionNotifier.NotifyTaskCompletion()
	}
	return reconciled, nil
}

func (s *TaskService) EnqueueSynchronousTaskCompletion(
	ctx context.Context,
	callbackURL string,
	targetIdentity string,
	agentID pgtype.UUID,
	errMessage string,
	failureReason string,
) error {
	const prefix = "/api/v1/dispatch-tasks/"
	const suffix = "/execution-result"
	dispatchTaskID := strings.TrimSuffix(strings.TrimPrefix(callbackURL, prefix), suffix)
	if !strings.HasPrefix(callbackURL, prefix) ||
		!strings.HasSuffix(callbackURL, suffix) ||
		!synchronousCompletionCallbackPattern.MatchString(dispatchTaskID) ||
		!routerTargetIdentityPattern.MatchString(targetIdentity) ||
		!agentID.Valid {
		return errors.New("synchronous task completion target is invalid")
	}
	_, err := s.Queries.EnqueueSynchronousTaskCompletion(ctx, db.EnqueueSynchronousTaskCompletionParams{
		CallbackUrl:   callbackURL,
		TargetIdentity: targetIdentity,
		RequestID:     "multica-terminal:sync:" + dispatchTaskID,
		AgentID:       agentID,
		Error:         pgtype.Text{String: redact.Text(errMessage), Valid: errMessage != ""},
		FailureReason: pgtype.Text{String: failureReason, Valid: failureReason != ""},
	})
	if err != nil {
		return fmt.Errorf("enqueue synchronous task completion: %w", err)
	}
	if s.CompletionNotifier != nil {
		s.CompletionNotifier.NotifyTaskCompletion()
	}
	return nil
}
