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
var routerExecutionUpdateCallbackPattern = regexp.MustCompile(`^/api/v1/dispatch-tasks/[A-Za-z0-9_-]{1,128}/execution-update$`)
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
	ExecutionSummary  []byte
	Error             string
	FailureReason     string
	ReplyDecision     *protocol.ReplyDecision
}

type taskCompletionTarget struct {
	RootTaskID     pgtype.UUID
	AgentID        pgtype.UUID
	CallbackURL    string
	TargetIdentity string
	CommentID      pgtype.UUID
}

type TaskCompletionNotifier interface {
	NotifyTaskCompletion()
}

type lastTaskReplyReader interface {
	GetLastTaskReplyText(context.Context, pgtype.UUID) (pgtype.Text, error)
}

func taskExecutionUpdateResultMessage(result []byte) string {
	var payload protocol.TaskCompletedPayload
	if json.Unmarshal(result, &payload) != nil {
		return ""
	}
	return redact.Text(util.UnescapeBackslashEscapes(payload.Output))
}

func freezeTaskExecutionUpdateResultMessage(
	ctx context.Context,
	qtx *db.Queries,
	terminalTaskID pgtype.UUID,
	result []byte,
) (bool, error) {
	resultMessage := taskExecutionUpdateResultMessage(result)
	executionUpdate, err := qtx.FreezeTaskExecutionUpdateResultMessage(ctx, db.FreezeTaskExecutionUpdateResultMessageParams{
		TerminalTaskID: terminalTaskID,
		ResultMessage:  pgtype.Text{String: resultMessage, Valid: strings.TrimSpace(resultMessage) != ""},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := qtx.ReleaseTaskCompletionsForExecutionUpdate(ctx, executionUpdate.RootTaskID); err != nil {
		return false, err
	}
	return true, nil
}

func resolveFailedCompletionReply(
	ctx context.Context,
	reader lastTaskReplyReader,
	taskID pgtype.UUID,
) (string, error) {
	reply, err := reader.GetLastTaskReplyText(ctx, taskID)
	if err == nil {
		return reply.String, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return "", err
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
	result = normalizeTaskCompletionResult(result)
	resultMessage := ""
	// Root callbacks use the canonical provider output. Streamed task messages
	// may be partial chunks or internal control frames; only comment callbacks
	// intentionally select their thread-specific Agent reply.
	if status != "completed" || target.CommentID.Valid {
		resultMessage = redact.Text(util.UnescapeBackslashEscapes(lastReply))
	}
	var replyDecision *protocol.ReplyDecision
	if status == "completed" {
		var payload protocol.TaskCompletedPayload
		if json.Unmarshal(result, &payload) == nil {
			replyDecision = payload.ReplyDecision
			if !target.CommentID.Valid {
				resultMessage = redact.Text(util.UnescapeBackslashEscapes(payload.Output))
			}
		}
		// A thread-specific Agent comment is already a concrete response to that
		// delivered member input. A run-wide silence decision must not suppress
		// the callback for this comment target.
		if target.CommentID.Valid && strings.TrimSpace(resultMessage) != "" {
			replyDecision = &protocol.ReplyDecision{ShouldReply: true, Reason: "comment_reply"}
		}
	}
	return TaskCompletion{
		RequestID:         taskCompletionRequestID(target),
		CallbackURL:       target.CallbackURL,
		TargetIdentity:    target.TargetIdentity,
		RootTaskID:        target.RootTaskID,
		TerminalTaskID:    task.ID,
		AgentID:           target.AgentID,
		ExternalSessionID: task.SessionID.String,
		ExecutionStatus:   status,
		ResultMessage:     resultMessage,
		Error:             redact.Text(errMessage),
		FailureReason:     failureReason,
		ReplyDecision:     replyDecision,
	}
}

func taskCompletionRequestID(target taskCompletionTarget) string {
	if target.CommentID.Valid {
		return "multica-comment-terminal:" + util.UUIDToString(target.RootTaskID)
	}
	return "multica-terminal:" + util.UUIDToString(target.RootTaskID)
}

func (s *TaskService) enqueueTaskCompletionInTx(
	ctx context.Context,
	qtx *db.Queries,
	tx pgx.Tx,
	task db.AgentTaskQueue,
	status string,
	result []byte,
	errMessage string,
	failureReason string,
) (bool, error) {
	// A scene routine run posts its end notice into its scene in the same
	// terminal transaction (docs/context-capabilities.md §9).
	if s.SceneRoutines != nil && task.AutopilotRunID.Valid && IsSceneRoutineContext(task.Context) {
		if err := s.SceneRoutines.RoutineTaskFinished(ctx, tx, task, status, result, errMessage); err != nil {
			return false, fmt.Errorf("scene routine end notice: %w", err)
		}
	}
	commentRows, err := qtx.ListTaskCommentCompletionTargets(ctx, task.ID)
	if err != nil {
		return false, err
	}
	targets := make([]taskCompletionTarget, 0, len(commentRows)+1)
	commentRoots := make(map[string]struct{}, len(commentRows))
	for _, row := range commentRows {
		targets = append(targets, taskCompletionTarget{
			RootTaskID:     row.RootTaskID,
			AgentID:        row.RootAgentID,
			CallbackURL:    row.CallbackUrl,
			TargetIdentity: row.TargetIdentity,
			CommentID:      row.CommentID,
		})
		commentRoots[util.UUIDToString(row.RootTaskID)] = struct{}{}
	}
	targetRow, rootErr := qtx.GetTaskCompletionTarget(ctx, task.ID)
	if rootErr != nil && !errors.Is(rootErr, pgx.ErrNoRows) {
		return false, rootErr
	}
	if rootErr == nil {
		if _, delegated := commentRoots[util.UUIDToString(targetRow.RootTaskID)]; !delegated {
			targets = append(targets, taskCompletionTarget{
				RootTaskID:     targetRow.RootTaskID,
				AgentID:        targetRow.RootAgentID,
				CallbackURL:    targetRow.CallbackUrl,
				TargetIdentity: targetRow.TargetIdentity,
			})
		}
	}
	if len(targets) == 0 {
		return false, nil
	}
	executionSummary, err := BuildTaskExecutionSummary(ctx, qtx, task)
	if err != nil {
		return false, err
	}
	executionSummaryJSON, err := json.Marshal(executionSummary)
	if err != nil {
		return false, err
	}
	fallbackReply := ""
	if status == "failed" || status == "canceled" {
		var replyErr error
		fallbackReply, replyErr = resolveFailedCompletionReply(ctx, qtx, task.ID)
		if replyErr != nil {
			return false, replyErr
		}
	}
	queued := false
	for _, target := range targets {
		lastReply := fallbackReply
		if target.CommentID.Valid {
			reply, replyErr := qtx.GetTaskReplyForComment(ctx, db.GetTaskReplyForCommentParams{
				TerminalTaskID: task.ID,
				CommentID:      target.CommentID,
			})
			if replyErr == nil {
				lastReply = reply
			} else if !errors.Is(replyErr, pgx.ErrNoRows) {
				return false, replyErr
			}
		}
		completion := buildTaskCompletion(
			target,
			task,
			status,
			result,
			lastReply,
			errMessage,
			failureReason,
		)
		if completion.ReplyDecision == nil {
			completion.ExecutionSummary = executionSummaryJSON
		} else {
			var frozen map[string]any
			if err := json.Unmarshal(executionSummaryJSON, &frozen); err != nil {
				return false, err
			}
			frozen[protocol.TaskReplyDecisionSummaryKey] = completion.ReplyDecision
			completion.ExecutionSummary, err = json.Marshal(frozen)
			if err != nil {
				return false, err
			}
		}
		_, enqueueErr := qtx.EnqueueTaskCompletion(ctx, db.EnqueueTaskCompletionParams{
			RootTaskID:        completion.RootTaskID,
			TerminalTaskID:    completion.TerminalTaskID,
			CallbackUrl:       completion.CallbackURL,
			TargetIdentity:    completion.TargetIdentity,
			RequestID:         completion.RequestID,
			AgentID:           completion.AgentID,
			ExecutionStatus:   completion.ExecutionStatus,
			ResultMessage:     completion.ResultMessage,
			ExecutionSummary:  completion.ExecutionSummary,
			ExternalSessionID: pgtype.Text{String: completion.ExternalSessionID, Valid: completion.ExternalSessionID != ""},
			Error:             pgtype.Text{String: completion.Error, Valid: completion.Error != ""},
			FailureReason:     pgtype.Text{String: completion.FailureReason, Valid: completion.FailureReason != ""},
		})
		if errors.Is(enqueueErr, pgx.ErrNoRows) {
			return false, ErrTaskCompletionConflict
		}
		if enqueueErr != nil {
			return false, enqueueErr
		}
		queued = true
	}
	return queued, nil
}

type failedTaskFinalization struct {
	Task                 db.AgentTaskQueue
	Retry                *db.AgentTaskQueue
	RetryCreated         bool
	CompletionQueued     bool
	ExecutionUpdateReady bool
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

	err = s.runInTxWithHandle(ctx, func(qtx *db.Queries, terminalTx pgx.Tx) error {
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
		if locked.ChatSessionID.Valid {
			ready, freezeErr := freezeTaskExecutionUpdateResultMessage(ctx, qtx, locked.ID, locked.Result)
			if freezeErr != nil {
				return fmt.Errorf("freeze task execution update result message: %w", freezeErr)
			}
			result.ExecutionUpdateReady = ready
		}

		queued, enqueueErr := s.enqueueTaskCompletionInTx(
			ctx,
			qtx,
			terminalTx,
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
	if (result.CompletionQueued || result.ExecutionUpdateReady) && s.CompletionNotifier != nil {
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
	executionUpdateReady := false
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
		if err := s.runInTxWithHandle(ctx, func(qtx *db.Queries, terminalTx pgx.Tx) error {
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
				status = "canceled"
				if errMessage == "" {
					errMessage = "task cancelled"
				}
				if failureReason == "" {
					failureReason = "cancelled"
				}
			} else if task.Status != "failed" {
				return nil
			}
			if task.ChatSessionID.Valid {
				ready, freezeErr := freezeTaskExecutionUpdateResultMessage(ctx, qtx, task.ID, task.Result)
				if freezeErr != nil {
					return fmt.Errorf("freeze task execution update result message: %w", freezeErr)
				}
				executionUpdateReady = executionUpdateReady || ready
			}
			var enqueueErr error
			queued, enqueueErr = s.enqueueTaskCompletionInTx(
				ctx,
				qtx,
				terminalTx,
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
	if (reconciled > 0 || executionUpdateReady) && s.CompletionNotifier != nil {
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
	return s.EnqueueSynchronousTaskFailureReply(ctx, callbackURL, targetIdentity, agentID, errMessage, failureReason, "")
}

// EnqueueSynchronousTaskFailureReply retains a failed execution while durably
// carrying its optional human-facing response through the same callback outbox.
func (s *TaskService) EnqueueSynchronousTaskFailureReply(
	ctx context.Context, callbackURL, targetIdentity string, agentID pgtype.UUID,
	errMessage, failureReason, reply string,
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
		CallbackUrl:    callbackURL,
		TargetIdentity: targetIdentity,
		RequestID:      "multica-terminal:sync:" + dispatchTaskID,
		AgentID:        agentID,
		Error:          pgtype.Text{String: redact.Text(errMessage), Valid: errMessage != ""},
		FailureReason:  pgtype.Text{String: failureReason, Valid: failureReason != ""},
		ResultMessage:  reply,
	})
	if err != nil {
		return fmt.Errorf("enqueue synchronous task completion: %w", err)
	}
	if s.CompletionNotifier != nil {
		s.CompletionNotifier.NotifyTaskCompletion()
	}
	return nil
}

// EnqueueSynchronousSilence closes a Router dispatch as completed with no IM.
// Coordinator silence must not use EnqueueSynchronousTaskCompletion: that
// path is execution_status=failed and stamps 处理失败 on the inbound.
func (s *TaskService) EnqueueSynchronousSilence(
	ctx context.Context,
	callbackURL string,
	targetIdentity string,
	agentID pgtype.UUID,
) error {
	const prefix = "/api/v1/dispatch-tasks/"
	const suffix = "/execution-result"
	dispatchTaskID := strings.TrimSuffix(strings.TrimPrefix(callbackURL, prefix), suffix)
	if !strings.HasPrefix(callbackURL, prefix) ||
		!strings.HasSuffix(callbackURL, suffix) ||
		!synchronousCompletionCallbackPattern.MatchString(dispatchTaskID) ||
		!routerTargetIdentityPattern.MatchString(targetIdentity) ||
		!agentID.Valid {
		return errors.New("synchronous silence target is invalid")
	}
	summary, err := json.Marshal(map[string]any{
		protocol.TaskReplyDecisionSummaryKey: protocol.ReplyDecision{
			ShouldReply: false,
			Reason:      "coordinator_silence",
		},
	})
	if err != nil {
		return err
	}
	_, err = s.Queries.EnqueueSynchronousSilenceTaskCompletion(ctx, db.EnqueueSynchronousSilenceTaskCompletionParams{
		CallbackUrl:      callbackURL,
		TargetIdentity:   targetIdentity,
		RequestID:        "multica-terminal:sync-silence:" + dispatchTaskID,
		AgentID:          agentID,
		ExecutionSummary: summary,
	})
	if err != nil {
		return fmt.Errorf("enqueue synchronous silence: %w", err)
	}
	if s.CompletionNotifier != nil {
		s.CompletionNotifier.NotifyTaskCompletion()
	}
	return nil
}

func (s *TaskService) EnqueueSynchronousCompleted(
	ctx context.Context,
	callbackURL string,
	targetIdentity string,
	agentID pgtype.UUID,
	resultMessage string,
) error {
	return s.enqueueSynchronousCompleted(ctx, callbackURL, targetIdentity, agentID, resultMessage, "sync-completed", false)
}

// EnqueueSynchronousWorkReceipt reuses an already frozen work acknowledgement
// after plan restoration. Its original payload and delivery state remain intact.
func (s *TaskService) EnqueueSynchronousWorkReceipt(ctx context.Context, callbackURL, targetIdentity string, agentID pgtype.UUID, resultMessage string) error {
	return s.enqueueSynchronousCompleted(ctx, callbackURL, targetIdentity, agentID, resultMessage, "sync-completed", true)
}

// EnqueueSynchronousWrapup delivers the task-finished Coordinator reply on the
// original inbound Router callback without colliding with the issue-create ACK.
func (s *TaskService) EnqueueSynchronousWrapup(
	ctx context.Context,
	callbackURL string,
	targetIdentity string,
	agentID pgtype.UUID,
	resultMessage string,
	taskID string,
) error {
	suffix := strings.TrimSpace(taskID)
	if suffix == "" {
		return errors.New("synchronous wrap-up task id is required")
	}
	return s.enqueueSynchronousCompleted(ctx, callbackURL, targetIdentity, agentID, resultMessage, "sync-wrapup:"+suffix, false)
}

func (s *TaskService) enqueueSynchronousCompleted(
	ctx context.Context,
	callbackURL string,
	targetIdentity string,
	agentID pgtype.UUID,
	resultMessage string,
	requestKind string,
	reuseWorkReceipt bool,
) error {
	const prefix = "/api/v1/dispatch-tasks/"
	const suffix = "/execution-result"
	dispatchTaskID := strings.TrimSuffix(strings.TrimPrefix(callbackURL, prefix), suffix)
	if !strings.HasPrefix(callbackURL, prefix) ||
		!strings.HasSuffix(callbackURL, suffix) ||
		!synchronousCompletionCallbackPattern.MatchString(dispatchTaskID) ||
		!routerTargetIdentityPattern.MatchString(targetIdentity) ||
		!agentID.Valid {
		return errors.New("synchronous completed task target is invalid")
	}
	message, _ := NormalizeReplyDecisionOutput(resultMessage, nil)
	message = strings.TrimSpace(message)
	if message == "" {
		return errors.New("synchronous completed result message is required")
	}
	kind := strings.TrimSpace(requestKind)
	if kind == "" {
		kind = "sync-completed"
	}
	requestID := "multica-terminal:" + kind + ":" + dispatchTaskID
	_, err := s.Queries.EnqueueSynchronousCompletedTaskCompletion(ctx, db.EnqueueSynchronousCompletedTaskCompletionParams{
		CallbackUrl:    callbackURL,
		TargetIdentity: targetIdentity,
		RequestID:      requestID,
		AgentID:        agentID,
		ResultMessage:  redact.Text(message),
	})
	if errors.Is(err, pgx.ErrNoRows) && reuseWorkReceipt {
		existing, loadErr := s.Queries.GetTaskCompletionByRequestID(ctx, requestID)
		if loadErr != nil {
			return fmt.Errorf("load frozen work receipt: %w", loadErr)
		}
		if existing.RequestID == requestID && existing.CallbackUrl == callbackURL && existing.TargetIdentity == targetIdentity && existing.AgentID == agentID && !existing.RootTaskID.Valid && !existing.TerminalTaskID.Valid && existing.ExecutionStatus == "completed" && !existing.Error.Valid && !existing.FailureReason.Valid && strings.TrimSpace(existing.ResultMessage) != "" {
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("enqueue synchronous completed task: %w", err)
	}
	if s.CompletionNotifier != nil {
		s.CompletionNotifier.NotifyTaskCompletion()
	}
	return nil
}

func (s *TaskService) EnqueueCoordinatorIssueAck(
	ctx context.Context,
	issueTask db.AgentTaskQueue,
	issue db.Issue,
	issueIdentifier string,
	updateURL string,
	targetIdentity string,
	ackText string,
) error {
	if !issueTask.ID.Valid || !issue.ID.Valid || !issueTask.AgentID.Valid {
		return errors.New("coordinator issue ack target is invalid")
	}
	if !routerExecutionUpdateCallbackPattern.MatchString(strings.TrimSpace(updateURL)) {
		return errors.New("coordinator issue ack update url is invalid")
	}
	if !routerTargetIdentityPattern.MatchString(targetIdentity) {
		return errors.New("coordinator issue ack target identity is invalid")
	}
	message := strings.TrimSpace(ackText)
	if message == "" {
		return errors.New("coordinator issue ack text is required")
	}
	_, err := s.Queries.EnqueueFrozenTaskExecutionUpdate(ctx, db.EnqueueFrozenTaskExecutionUpdateParams{
		RootTaskID:      issueTask.ID,
		TargetTaskID:    issueTask.ID,
		IssueID:         issue.ID,
		IssueIdentifier: issueIdentifier,
		CallbackUrl:     updateURL,
		TargetIdentity:  targetIdentity,
		RequestID:       "multica-coord-issue:" + util.UUIDToString(issueTask.ID),
		AgentID:         issueTask.AgentID,
		TargetAgentID:   issueTask.AgentID,
		UpdateType:      "delegated_to_issue",
		ResultMessage:   pgtype.Text{String: redact.Text(message), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, loadErr := s.Queries.GetTaskExecutionUpdateByRootTaskID(ctx, issueTask.ID)
		if loadErr != nil {
			return fmt.Errorf("load frozen coordinator issue ack: %w", loadErr)
		}
		if existing.RootTaskID == issueTask.ID && existing.TargetTaskID == issueTask.ID && existing.IssueID == issue.ID && existing.IssueIdentifier == issueIdentifier && existing.CallbackUrl == updateURL && existing.TargetIdentity == targetIdentity && existing.RequestID == "multica-coord-issue:"+util.UUIDToString(issueTask.ID) && existing.AgentID == issueTask.AgentID && existing.TargetAgentID == issueTask.AgentID && existing.UpdateType == "delegated_to_issue" && existing.ResultMessageFrozen && existing.ResultMessage.Valid && strings.TrimSpace(existing.ResultMessage.String) != "" {
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("enqueue coordinator issue ack: %w", err)
	}
	if s.CompletionNotifier != nil {
		s.CompletionNotifier.NotifyTaskCompletion()
	}
	return nil
}
