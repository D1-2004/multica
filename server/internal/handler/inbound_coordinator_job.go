package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/service/scenememory"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	inboundCoordinatorWorkerConcurrency  = 8
	inboundCoordinatorWorkerPollInterval = 500 * time.Millisecond
	inboundCoordinatorWorkerMaxAttempts  = 6
	inboundCoordinatorSceneParkDelay     = 5 * time.Second
	inboundCoordinatorSceneBusyDelay     = 500 * time.Millisecond
	// coordinatorShadowAssemblyTimeout bounds the database reads that build
	// the collect-window shadow's Turn.
	coordinatorShadowAssemblyTimeout = 2 * time.Second
)

// InboundCoordinatorJobWorker executes accepted short loops from PostgreSQL.
// Router acknowledgement never depends on model/DWS latency, and a replica
// restart only releases a lease — it does not lose the inbound message.
type InboundCoordinatorJobWorker struct {
	handler *Handler
	notify  chan struct{}
	done    chan struct{}

	wakeMu  sync.Mutex
	wakeups map[string]windowWakeup
}

// windowWakeup is the pending collect-deadline wake-up of one job.
type windowWakeup struct {
	at    time.Time
	timer *time.Timer
}

func NewInboundCoordinatorJobWorker(h *Handler) *InboundCoordinatorJobWorker {
	return &InboundCoordinatorJobWorker{
		handler: h,
		notify:  make(chan struct{}, inboundCoordinatorWorkerConcurrency),
		done:    make(chan struct{}),
		wakeups: make(map[string]windowWakeup),
	}
}

// WakeAt notifies the workers when job becomes claimable. A job's collect
// deadline only moves later (coordinatorCollectDeadline), but callers run
// asynchronously and can arrive out of order, so only a later deadline
// replaces the pending wake-up; an earlier one is dropped.
func (w *InboundCoordinatorJobWorker) WakeAt(jobID string, at time.Time) {
	if w == nil || jobID == "" {
		return
	}
	w.wakeMu.Lock()
	defer w.wakeMu.Unlock()
	if w.wakeups == nil {
		w.wakeups = make(map[string]windowWakeup)
	}
	if previous, ok := w.wakeups[jobID]; ok {
		if !at.After(previous.at) {
			return
		}
		previous.timer.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(time.Until(at)+5*time.Millisecond, func() {
		w.wakeMu.Lock()
		if w.wakeups[jobID].timer == timer {
			delete(w.wakeups, jobID)
		}
		w.wakeMu.Unlock()
		w.Notify()
	})
	w.wakeups[jobID] = windowWakeup{at: at, timer: timer}
}

func (w *InboundCoordinatorJobWorker) Notify() {
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *InboundCoordinatorJobWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	defer close(w.done)
	var workers sync.WaitGroup
	workers.Add(inboundCoordinatorWorkerConcurrency)
	for range inboundCoordinatorWorkerConcurrency {
		go func() {
			defer workers.Done()
			w.runLoop(ctx)
		}()
	}
	workers.Wait()
}

func (w *InboundCoordinatorJobWorker) runLoop(ctx context.Context) {
	ticker := time.NewTicker(inboundCoordinatorWorkerPollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("inbound coordinator job failed", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-w.notify:
		case <-ticker.C:
		}
	}
}

func (w *InboundCoordinatorJobWorker) WaitWithTimeout(timeout time.Duration) bool {
	if w == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return true
	case <-timer.C:
		return false
	}
}

func (w *InboundCoordinatorJobWorker) ProcessNext(ctx context.Context) (bool, error) {
	if w == nil || w.handler == nil || w.handler.Queries == nil {
		return false, nil
	}
	if c := w.handler.InboundCoordinator; c != nil && c.Ready != nil {
		ready, err := c.Ready(ctx)
		if err != nil || !ready {
			return false, err
		}
	}
	job, err := w.handler.Queries.ClaimInboundCoordinatorJob(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	slog.Info("inbound coordinator job claimed",
		"event", "inbound_coordinator_job_claimed",
		"job_id", util.UUIDToString(job.ID),
		"attempt", job.AttemptCount,
	)
	command, err := restoreInboundCoordinatorCommand(
		job.Command, job.EndpointNamespaceID, w.handler.TaskCompletionTargetIdentity,
	)
	if err != nil {
		return true, w.fail(ctx, job, command, "invalid persisted dispatch command")
	}
	if command.AgentScene != nil {
		// The persisted SceneRef is used only while it passes the use-time
		// fence: a job admitted before the agent was re-bound to another org
		// reads no scene state of the old org.
		command.AgentScene = w.handler.fenceSceneRef(ctx, command.AgentScene,
			scene.Owner{WorkspaceID: job.WorkspaceID, AgentID: job.AgentID}, dispatchRecordedOrg(command))
	} else if strings.TrimSpace(command.TaskFinishedTaskID) == "" {
		// A job an older replica admitted during the rollout carries no
		// SceneRef: resolve it from the persisted command with the same
		// resolver the dispatch uses (docs/agent-scene.md, rolling window).
		w.handler.attachDispatchScene(ctx, &command, agentDispatchContext{WorkspaceID: job.WorkspaceID, AgentID: job.AgentID})
	}
	if parked, parkErr := w.parkIfSceneWindowBusy(ctx, job, command); parkErr != nil {
		return true, parkErr
	} else if parked {
		return true, nil
	}
	if strings.TrimSpace(command.TaskFinishedTaskID) != "" {
		if runErr := w.handler.runPersistedTaskFinishedLoop(ctx, command.TaskFinishedTaskID); runErr != nil {
			if errors.Is(runErr, errTaskFinishedResponsePending) {
				return true, w.park(ctx, job, 5*time.Second, "response_receipt_pending")
			}
			return true, w.retry(ctx, job, runErr)
		}
		return true, w.complete(ctx, job)
	}
	dispatchContext := agentDispatchContext{
		EndpointID:          job.DispatchEndpointID,
		EndpointNamespaceID: job.EndpointNamespaceID,
		UserID:              job.UserID,
		WorkspaceID:         job.WorkspaceID,
		AgentID:             job.AgentID,
	}
	plan, err := buildAgentDispatchExecutionPlan(command, dispatchContext)
	if err != nil {
		return true, w.fail(ctx, job, command, "failed to rebuild dispatch plan")
	}
	acceptance, err := w.handler.Queries.GetAgentDispatchAcceptance(ctx, db.GetAgentDispatchAcceptanceParams{
		EndpointID: job.EndpointNamespaceID, IdempotencyKey: job.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return true, w.fail(ctx, job, command, "dispatch acceptance no longer exists")
	}
	if err != nil {
		return true, w.retry(ctx, job, fmt.Errorf("load dispatch acceptance: %w", err))
	}
	recovered, recoveredOK, recoverErr := w.handler.recoverAgentDispatchAcceptance(
		ctx, command, dispatchContext, acceptance,
	)
	if recoverErr != nil {
		return true, w.retry(ctx, job, fmt.Errorf("recover dispatch result: %w", recoverErr))
	}
	if recoveredOK && !coordinatorIssueReplayRequired(recovered) {
		decision := recoveredCoordinatorDecision(command, recovered)
		if err := w.handler.persistCoordinatorJobChat(ctx, job, decision); err != nil {
			return true, w.retry(ctx, job, fmt.Errorf("persist recovered coordinator Chat: %w", err))
		}
		return true, w.complete(ctx, job)
	}
	jobCtx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	// The job id is the coordinator trace id of this turn, so the Scene
	// Memory trigger recorded at enqueue time (coord_trace_id = job id) and
	// the turn's SLS/Langfuse trace resolve to the same identifier.
	jobCtx = inboundcoord.ContextWithTraceID(jobCtx, util.UUIDToString(job.ID))
	jobCtx = context.WithValue(jobCtx, userDecisionJobKey{}, job)
	if coordinatorCapacityWaitExpired(job) {
		// Waiting longer cannot help this window: the occupied slots are held
		// by work this loop does not control. Run it instead of parking again.
		jobCtx = withSceneCapacityWaiver(jobCtx)
		slog.Warn("inbound coordinator scene capacity wait expired",
			"event", "inbound_coordinator_scene_capacity_waived",
			"job_id", util.UUIDToString(job.ID),
			"waited_ms", time.Since(job.CreatedAt.Time).Milliseconds(),
			"reason", job.LastError.String,
		)
	}
	jobCtx, err = w.handler.coordinatorCheckpointContext(jobCtx, job)
	if err != nil {
		return true, w.retry(ctx, job, err)
	}
	if windowHistoryEligibleJob(job) {
		jobCtx = inboundcoord.ContextWithWindowHistoryEligible(jobCtx)
	}
	var recordedDecision *inboundcoord.Decision
	jobCtx = inboundcoord.WithDecisionObserver(jobCtx, func(decision inboundcoord.Decision) {
		copied := decision
		recordedDecision = &copied
	})
	req, err := http.NewRequestWithContext(jobCtx, http.MethodPost, "/internal/inbound-coordinator", nil)
	if err != nil {
		return true, err
	}
	req.Header.Set("Idempotency-Key", job.IdempotencyKey)
	response := newBufferedDispatchResponse()
	w.handler.executeAgentDispatchV2(response, req, command, plan, dispatchContext)
	if response.Status() >= http.StatusOK && response.Status() < http.StatusMultipleChoices {
		if recordedDecision != nil && recordedDecision.Action == inboundcoord.ActionAwaitUser {
			return true, nil
		}
		if recordedDecision != nil {
			*recordedDecision = coordinatorDecisionWithDispatchResult(*recordedDecision, response)
			if err := w.handler.persistCoordinatorJobChat(jobCtx, job, *recordedDecision); err != nil {
				return true, w.retry(ctx, job, fmt.Errorf("persist coordinator Chat: %w", err))
			}
		}
		return true, w.complete(ctx, job)
	}
	if response.Status() == http.StatusConflict || response.Status() >= http.StatusInternalServerError {
		reason := dispatchRejectReason(response)
		if shouldParkCoordinatorBusyResponse(response.Status(), reason) {
			// Retain the callback until this sealed window actually finishes.
			// A capacity wait is not a successful, silent execution.
			return true, w.park(ctx, job, inboundCoordinatorSceneParkDelay, reason)
		}
		if job.AttemptCount >= inboundCoordinatorWorkerMaxAttempts {
			return true, w.failWithDecision(ctx, job, command, reason, recordedDecision)
		}
		return true, w.retry(ctx, job, fmt.Errorf("%s", reason))
	}
	return true, w.failWithDecision(ctx, job, command, dispatchRejectReason(response), recordedDecision)
}

func dispatchRejectReason(response *bufferedDispatchResponse) string {
	status := 0
	body := ""
	if response != nil {
		status = response.Status()
		body = strings.TrimSpace(response.body.String())
		runes := []rune(body)
		if len(runes) > 300 {
			body = string(runes[:300])
		}
	}
	if body == "" {
		return fmt.Sprintf("dispatch rejected with HTTP %d", status)
	}
	return fmt.Sprintf("dispatch rejected with HTTP %d: %s", status, body)
}

func (h *Handler) enqueueCoordinatorSilenceCallback(
	ctx context.Context,
	callback *DispatchCompletionCallback,
	agentID pgtype.UUID,
	logEvent, jobID string,
) {
	if h == nil || h.TaskService == nil || callback == nil {
		return
	}
	if err := h.TaskService.EnqueueSynchronousSilence(ctx, callback.URL, callback.Target, agentID); err != nil {
		slog.Warn("coordinator silence callback failed",
			"event", logEvent,
			"job_id", jobID,
			"error", err,
		)
	}
}

func restoreInboundCoordinatorCommand(raw []byte, endpointID pgtype.UUID, targetIdentity string) (DispatchCommand, error) {
	var command DispatchCommand
	if err := json.Unmarshal(raw, &command); err != nil {
		return command, err
	}
	command.DispatchEndpointID = uuidToString(endpointID)
	return bindDispatchCompletionTarget(command, targetIdentity)
}

func (w *InboundCoordinatorJobWorker) complete(ctx context.Context, job db.InboundCoordinatorJob) error {
	// Cover every successful materializer, including the sandbox fallback.
	// Duplicate outbox enqueues are idempotent; failures keep the job retryable.
	// Managed windows settle extras only after the actual response receipt.
	command, err := restoreInboundCoordinatorCommand(job.Command, job.EndpointNamespaceID, w.handler.TaskCompletionTargetIdentity)
	if err != nil {
		return err
	}
	if w.handler.TaskService != nil && !managedDingTalkResponse(command) {
		for _, callback := range command.ExtraCompletionCallbacks {
			if err := w.handler.TaskService.EnqueueSynchronousSilence(ctx, callback.URL, callback.Target, job.AgentID); err != nil {
				return fmt.Errorf("settle collected callback: %w", err)
			}
		}
	}
	rows, err := w.handler.Queries.CompleteInboundCoordinatorJob(ctx, db.CompleteInboundCoordinatorJobParams{
		ID: job.ID, LeaseToken: job.LeaseToken,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("complete inbound coordinator job: lease no longer owned")
	}
	slog.Info("inbound coordinator job completed",
		"event", "inbound_coordinator_job_completed",
		"job_id", util.UUIDToString(job.ID),
		"attempt", job.AttemptCount,
	)
	w.Notify()
	return nil
}

func (w *InboundCoordinatorJobWorker) parkIfSceneWindowBusy(ctx context.Context, job db.InboundCoordinatorJob, command DispatchCommand) (bool, error) {
	cid := dispatchConversationID(command)
	if cid == "" || w.handler == nil || w.handler.Queries == nil {
		return false, nil
	}
	running, err := w.handler.Queries.CountRunningInboundCoordinatorJobsForConversation(ctx, db.CountRunningInboundCoordinatorJobsForConversationParams{
		WorkspaceID: job.WorkspaceID, AgentID: job.AgentID, ExcludeID: job.ID, ConversationID: cid,
	})
	if err != nil {
		return false, w.retry(ctx, job, fmt.Errorf("count running scene window: %w", err))
	}
	if running > 0 {
		// Half-second mutex wait: keep the Router callback so the next
		// window can still speak. Do not close 处理中 here.
		return true, w.park(ctx, job, inboundCoordinatorSceneBusyDelay, "scene window already running")
	}
	if command.ProactiveConversation || command.TaskFinishedTaskID != "" {
		return false, nil
	}
	// New windows must be understood even at capacity: a status request
	// or presence check needs no sandbox. Only previously judged work waits.
	if !job.LastError.Valid || !isCoordinatorBusyParkReason(job.LastError.String) {
		return false, nil
	}
	// A window that already waited out the capacity deadline stops waiting.
	// ProcessNext carries that waiver into this run's admission checks.
	if coordinatorCapacityWaitExpired(job) {
		return false, nil
	}
	// Wait only while nobody with unstarted work in this window can proceed.
	for _, delegator := range pendingWindowDelegators(job, command) {
		if sceneCapacitySlots(ctx, w.handler, job.WorkspaceID, job.AgentID, delegator) > 0 {
			return false, nil
		}
	}
	return true, w.park(ctx, job, inboundCoordinatorSceneParkDelay, sceneCapacityRejectReason())
}

func (w *InboundCoordinatorJobWorker) park(ctx context.Context, job db.InboundCoordinatorJob, delay time.Duration, reason string) error {
	if isCoordinatorBusyParkReason(reason) {
		feedbackCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if err := w.handler.persistCoordinatorWait(feedbackCtx, job, reason); err != nil {
			slog.Warn("coordinator wait feedback unavailable", "job_id", util.UUIDToString(job.ID), "error", err)
		}
		cancel()
	}
	slog.Info("inbound coordinator job parked for next window",
		"event", "inbound_coordinator_job_parked",
		"job_id", util.UUIDToString(job.ID),
		"attempt", job.AttemptCount,
		"delay_ms", delay.Milliseconds(),
		"reason", reason,
	)
	rows, err := w.handler.Queries.ParkInboundCoordinatorJob(ctx, db.ParkInboundCoordinatorJobParams{
		ID: job.ID, LeaseToken: job.LeaseToken,
		AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true},
		LastError:   pgtype.Text{String: reason, Valid: true},
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("park inbound coordinator job: lease no longer owned")
	}
	return nil
}

func isCoordinatorBusyParkReason(reason string) bool {
	return strings.Contains(reason, "pending agent task") ||
		strings.Contains(reason, "already has an active task") ||
		strings.Contains(reason, "issue_busy") ||
		strings.Contains(reason, "active duplicate") ||
		isSceneCapacityReason(reason)
}

func shouldParkCoordinatorBusyResponse(status int, reason string) bool {
	return status == http.StatusConflict && isCoordinatorBusyParkReason(reason)
}

func coordinatorIssueReplayRequired(response *bufferedDispatchResponse) bool {
	if response == nil {
		return false
	}
	var dispatchResponse AgentDispatchResponse
	if json.Unmarshal(response.body.Bytes(), &dispatchResponse) != nil {
		return false
	}
	return dispatchResponse.Continuation.Kind == "issue"
}

func recoveredCoordinatorDecision(command DispatchCommand, response *bufferedDispatchResponse) inboundcoord.Decision {
	action := inboundcoord.ActionReply
	text := "已从已有执行结果恢复。"
	var dispatchResponse AgentDispatchResponse
	if response != nil && json.Unmarshal(response.body.Bytes(), &dispatchResponse) == nil &&
		dispatchResponse.Continuation.Kind == "issue" {
		action = inboundcoord.ActionIssue
		text = "已恢复到原 Issue 继续处理。"
	}
	return coordinatorDecisionWithDispatchResult(inboundcoord.Decision{
		Action:   action,
		UserText: text,
		Reason:   "recovered durable dispatch result",
		Source:   coordinatorSource(command),
		Steps: []protocol.ChatCoordinatorStep{{
			Seq: 1, Type: "thinking", Content: "从已有的幂等执行记录恢复，未重复创建任务。",
		}, {
			Seq: 2, Type: "text", Content: text,
		}},
	}, response)
}

// Supplement a verdict from a successful materializer response. Multiple window
// results and terminal tool effects already carry more precise evidence.
func coordinatorDecisionWithDispatchResult(decision inboundcoord.Decision, response *bufferedDispatchResponse) inboundcoord.Decision {
	if len(decision.IssueResults) > 0 || decision.IssueComment != nil || response == nil ||
		response.Status() < http.StatusOK || response.Status() >= http.StatusMultipleChoices {
		return decision
	}
	var result AgentDispatchResponse
	if json.Unmarshal(response.body.Bytes(), &result) != nil || result.Continuation.Kind != "issue" ||
		strings.TrimSpace(result.Continuation.IssueID) == "" {
		return decision
	}
	action := "issue_linked"
	if result.CommentID != "" {
		action = "issue_commented"
	} else if response.Status() == http.StatusCreated {
		action = "issue_created"
	}
	decision.IssueResults = []protocol.ChatCoordinatorIssueResult{{
		Action: action, IssueID: result.Continuation.IssueID,
		IssueIdentifier: result.IssueIdentifier, CommentID: result.CommentID, TaskID: result.TaskID,
	}}
	return decision
}

func (w *InboundCoordinatorJobWorker) retry(ctx context.Context, job db.InboundCoordinatorJob, cause error) error {
	if job.AttemptCount >= inboundCoordinatorWorkerMaxAttempts {
		var command DispatchCommand
		_ = json.Unmarshal(job.Command, &command)
		return w.fail(ctx, job, command, cause.Error())
	}
	delay := time.Second * time.Duration(1<<min(job.AttemptCount-1, 4))
	slog.Warn("inbound coordinator job retry scheduled",
		"event", "inbound_coordinator_job_retry_scheduled",
		"job_id", util.UUIDToString(job.ID),
		"attempt", job.AttemptCount,
		"delay_ms", delay.Milliseconds(),
		"error", cause.Error(),
	)
	rows, err := w.handler.Queries.RetryInboundCoordinatorJob(ctx, db.RetryInboundCoordinatorJobParams{
		ID: job.ID, LeaseToken: job.LeaseToken,
		AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true},
		LastError:   pgtype.Text{String: cause.Error(), Valid: true},
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("retry inbound coordinator job: lease no longer owned")
	}
	return nil
}

func (w *InboundCoordinatorJobWorker) fail(ctx context.Context, job db.InboundCoordinatorJob, command DispatchCommand, reason string) error {
	return w.failWithDecision(ctx, job, command, reason, nil)
}

const coordinatorFailureReply = "抱歉，这次没能处理好，我还没法确认这件事的结果。"

func failedCoordinatorDecision(command DispatchCommand, reason string, observed *inboundcoord.Decision) inboundcoord.Decision {
	decision := inboundcoord.Decision{Action: inboundcoord.ActionContinue, Source: coordinatorSource(command)}
	if observed != nil {
		decision = *observed
	}
	decision.UserText = coordinatorFailureReply
	decision.Reason = reason
	decision.Steps = append(append([]protocol.ChatCoordinatorStep{}, decision.Steps...), protocol.ChatCoordinatorStep{
		Seq: len(decision.Steps) + 1, Type: "error", Content: reason, Error: true,
	})
	return decision
}

func coordinatorFailureNeedsReply(command DispatchCommand) bool {
	if command.TaskFinishedTaskID != "" {
		return false
	}
	if !command.ProactiveConversation || dispatchMentionsEmployee(command) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(command.Event.Data.Conversation.Type)) {
	case "single", "p2p", "direct":
		return true
	default:
		return false
	}
}

func (w *InboundCoordinatorJobWorker) failWithDecision(ctx context.Context, job db.InboundCoordinatorJob, command DispatchCommand, reason string, observed *inboundcoord.Decision) error {
	if w.handler.TaskService != nil {
		callbacks := append([]DispatchCompletionCallback{}, command.ExtraCompletionCallbacks...)
		if command.CompletionCallback != nil {
			callbacks = append(callbacks, *command.CompletionCallback)
		}
		for _, callback := range callbacks {
			reply := ""
			// Only the primary callback owns the window's visible response.
			// Collected callbacks must not repeat the same message in the group.
			if command.CompletionCallback != nil && callback.URL == command.CompletionCallback.URL && coordinatorFailureNeedsReply(command) {
				reply = coordinatorFailureReply
			}
			if err := w.handler.TaskService.EnqueueSynchronousTaskFailureReply(ctx, callback.URL, callback.Target, job.AgentID, reason, "coordinator_job_failed", reply); err != nil {
				return fmt.Errorf("fail collected callback: %w", err)
			}
		}
	}
	decision := failedCoordinatorDecision(command, reason, observed)
	if err := w.handler.persistCoordinatorJobChat(ctx, job, decision); err != nil {
		slog.Error("persist failed coordinator Chat failed", "job_id", util.UUIDToString(job.ID), "error", err)
	}
	rows, err := w.handler.Queries.FailInboundCoordinatorJob(ctx, db.FailInboundCoordinatorJobParams{
		ID: job.ID, LeaseToken: job.LeaseToken,
		LastError: pgtype.Text{String: reason, Valid: true},
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("fail inbound coordinator job: lease no longer owned")
	}
	slog.Error("inbound coordinator job terminal failure",
		"event", "inbound_coordinator_job_failed",
		"job_id", util.UUIDToString(job.ID),
		"attempt", job.AttemptCount,
		"reason", reason,
	)
	return nil
}

func coordinatorSource(command DispatchCommand) inboundcoord.Source {
	if command.Source.Type == string(inboundcoord.SourceDigitalEmployee) {
		return inboundcoord.SourceDigitalEmployee
	}
	return inboundcoord.SourceRobot
}

// dispatchSceneIdentity is the display kind and name of a dispatch's
// conversation for job titles: a type that is not positively a group reads
// as a 1:1 chat here; scene identity never uses this guess (see
// dispatchSceneLocator).
func dispatchSceneIdentity(command DispatchCommand) (kind, title string) {
	chatType := strings.TrimSpace(command.Event.Data.Conversation.Type)
	if chatType == "" {
		chatType = strings.TrimSpace(dispatchAssocIDs(command).Kind)
	}
	kind = scene.KindDM
	if k, ok := scene.KindFromConversationType(chatType); ok && k == scene.KindGroup {
		kind = scene.KindGroup
	}
	title = strings.TrimSpace(command.Event.Data.Conversation.Title)
	if title == "" && kind == scene.KindDM {
		title = strings.TrimSpace(command.Event.Data.Sender.DisplayName)
	}
	return kind, title
}

func coordinatorJobTitle(command DispatchCommand) string {
	kind, name := dispatchSceneIdentity(command)
	label := "单聊"
	if kind == scene.KindGroup {
		label = "群聊"
	}
	text := ""
	for _, message := range command.Event.Data.Messages {
		if message.Reaction == nil && strings.TrimSpace(message.Text) != "" {
			text = strings.TrimSpace(message.Text)
			break
		}
	}
	if text == "" {
		text = "Inbound event"
	}
	runes := []rune(text)
	if len(runes) > 40 {
		text = string(runes[:40]) + "…"
	}
	if name != "" {
		return label + " · " + name + " · " + text
	}
	return label + " · " + text
}

func coordinatorJobMessage(command DispatchCommand) string {
	parts := make([]string, 0, len(command.Event.Data.Messages))
	for _, message := range command.Event.Data.Messages {
		if message.Reaction == nil && strings.TrimSpace(message.Text) != "" {
			parts = append(parts, strings.TrimSpace(message.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}

func (h *Handler) enqueueInboundCoordinatorJob(
	ctx context.Context,
	acceptance db.AgentDispatchAcceptance,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
	idempotencyKey string,
	displayContent string,
) (*bufferedDispatchResponse, db.InboundCoordinatorJob, error) {
	var job db.InboundCoordinatorJob
	if h == nil || h.TxStarter == nil {
		return nil, job, errors.New("coordinator job store is not configured")
	}
	stampDispatchMessageSenders(&command)
	rawCommand, err := json.Marshal(command)
	if err != nil {
		return nil, job, err
	}
	response := newBufferedDispatchResponse()
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "accepted"})
	// Optional progress eligibility cannot abort the admission transaction.
	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
	waitPolicy, waitPolicyErr := h.Queries.GetAgentDingTalkResponsePolicy(waitCtx, dispatchContext.AgentID)
	waitCancel()
	if waitPolicyErr != nil {
		waitPolicy = db.GetAgentDingTalkResponsePolicyRow{}
		slog.Warn("coordinator waiting delivery policy unavailable", "agent_id", uuidToString(dispatchContext.AgentID))
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, job, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if command.ProactiveConversation {
		command, err = h.deduplicateObservedMessages(ctx, tx, command, dispatchContext)
		if err != nil {
			return nil, job, err
		}
		if len(command.Event.Data.Messages) == 0 {
			if command.CompletionCallback != nil {
				receipts := &service.TaskService{Queries: qtx}
				if err = receipts.EnqueueSynchronousSilence(ctx, command.CompletionCallback.URL, command.CompletionCallback.Target, dispatchContext.AgentID); err != nil {
					return nil, job, err
				}
			}
			_, err = qtx.CompleteAgentDispatchAcceptance(ctx, db.CompleteAgentDispatchAcceptanceParams{ID: acceptance.ID, LeaseToken: acceptance.LeaseToken, ResponseStatus: pgtype.Int4{Int32: 202, Valid: true}, ResponseContentType: pgtype.Text{String: "application/json", Valid: true}, ResponseBody: []byte(`{"status":"accepted","code":"duplicate_observed_message"}`)})
			if err != nil {
				return nil, job, err
			}
			return response, job, tx.Commit(ctx)
		}
		rawCommand, err = json.Marshal(command)
		if err != nil {
			return nil, job, err
		}
		displayContent = buildDingTalkChannelDisplay(command)
	}
	waitDelivery := freezeCoordinatorWaitDelivery(command, dispatchContext, waitPolicy)
	rawCommand, err = marshalCoordinatorWaitCommand(command, waitDelivery)
	if err != nil {
		return nil, job, err
	}
	collectQuiet := h.coordinatorCollectQuiet(dispatchContext.AgentID)
	collectAt := time.Now().UTC().Add(collectQuiet)
	if err := h.registerDingTalkResponseRoute(ctx, tx, command, dispatchContext); err != nil {
		return nil, job, err
	}
	if cid := dispatchConversationID(command); cid != "" {
		lockName := uuidToString(dispatchContext.WorkspaceID) + ":" + uuidToString(dispatchContext.AgentID) + ":" + cid
		if _, lockErr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockName); lockErr != nil {
			return nil, job, lockErr
		}
		pending, listErr := qtx.ListPendingInboundCoordinatorJobsForConversationCollect(ctx, db.ListPendingInboundCoordinatorJobsForConversationCollectParams{
			WorkspaceID:    dispatchContext.WorkspaceID,
			AgentID:        dispatchContext.AgentID,
			ConversationID: cid,
		})
		if listErr != nil {
			return nil, job, listErr
		}
		var splitKind bool
		for _, existing := range pending {
			base, restoreErr := restoreJobCommand(existing)
			if restoreErr != nil {
				return nil, job, restoreErr
			}
			priorWait, priorWaitErr := coordinatorWaitDeliveryFromCommand(existing.Command)
			if priorWaitErr != nil {
				return nil, job, priorWaitErr
			}
			if !sameCoordinatorCollectKind(base, command) || !sameCoordinatorWaitDelivery(priorWait, waitDelivery) || !sameUserDecisionCollectAudience(waitPolicy, base, command) {
				splitKind = true
				continue
			}
			merged := mergeDispatchCommands(base, command)
			mergedRaw, marshalErr := marshalCoordinatorWaitCommand(merged, *priorWait)
			if marshalErr != nil {
				return nil, job, marshalErr
			}
			job, err = qtx.UpdateInboundCoordinatorJobCollect(ctx, db.UpdateInboundCoordinatorJobCollectParams{
				ID:          existing.ID,
				Command:     mergedRaw,
				AvailableAt: pgtype.Timestamptz{Time: coordinatorCollectDeadline(existing.CreatedAt.Time, time.Now().UTC(), collectQuiet), Valid: true},
			})
			if err != nil {
				return nil, job, err
			}
			content := firstNonEmpty(strings.TrimSpace(buildDingTalkChannelDisplay(merged)), coordinatorJobMessage(merged))
			if _, err := qtx.AppendCoordinatorUserMessage(ctx, db.AppendCoordinatorUserMessageParams{
				ID: existing.UserMessageID, Content: content,
			}); err != nil {
				return nil, job, err
			}
			_, err = qtx.CompleteAgentDispatchAcceptance(ctx, db.CompleteAgentDispatchAcceptanceParams{
				ResponseStatus:      pgtype.Int4{Int32: int32(response.Status()), Valid: true},
				ResponseContentType: pgtype.Text{String: response.header.Get("Content-Type"), Valid: true},
				ResponseBody:        append([]byte{}, response.body.Bytes()...),
				ID:                  acceptance.ID,
				LeaseToken:          acceptance.LeaseToken,
			})
			if err != nil {
				return nil, job, err
			}
			h.markSceneMemoryDirty(ctx, qtx, command, dispatchContext, job, idempotencyKey)
			if err := tx.Commit(ctx); err != nil {
				return nil, job, err
			}
			if h.SceneMemoryWorker != nil {
				h.SceneMemoryWorker.Notify()
			}
			h.prefetchCoordinatorWindowHistory(job)
			// Every callback is durable in the merged command. Settle extras
			// only when the complete window has actually been handled.
			slog.Info("inbound coordinator job collected",
				"event", "inbound_coordinator_job_collected",
				"job_id", util.UUIDToString(job.ID),
				"source", command.Source.Type,
				"message_count", len(merged.Event.Data.Messages),
				"collect_until", job.AvailableAt.Time,
				"collect_age_ms", time.Since(job.CreatedAt.Time).Milliseconds(),
				"collect_quiet_ms", collectQuiet.Milliseconds(),
			)
			return response, job, nil
		}
		if splitKind {
			slog.Info("inbound coordinator job collect split by source or lifecycle",
				"event", "inbound_coordinator_job_collect_split",
				"source", command.Source.Type,
			)
		}
	}
	session, err := qtx.CreateChatSession(ctx, db.CreateChatSessionParams{
		WorkspaceID: dispatchContext.WorkspaceID,
		AgentID:     dispatchContext.AgentID,
		CreatorID:   dispatchContext.UserID,
		Title:       coordinatorJobTitle(command),
	})
	if err != nil {
		return nil, job, err
	}
	userMessage, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: session.ID,
		Role:          "user",
		Content:       firstNonEmpty(strings.TrimSpace(displayContent), coordinatorJobMessage(command)),
		MessageKind:   pgtype.Text{String: protocol.ChatMessageKindMessage, Valid: true},
	})
	if err != nil {
		return nil, job, err
	}
	job, err = qtx.CreateInboundCoordinatorJob(ctx, db.CreateInboundCoordinatorJobParams{
		AcceptanceID:        acceptance.ID,
		WorkspaceID:         dispatchContext.WorkspaceID,
		AgentID:             dispatchContext.AgentID,
		UserID:              dispatchContext.UserID,
		EndpointNamespaceID: dispatchContext.EndpointNamespaceID,
		DispatchEndpointID:  dispatchContext.EndpointID,
		IdempotencyKey:      idempotencyKey,
		Command:             rawCommand,
		ChatSessionID:       session.ID,
		UserMessageID:       userMessage.ID,
		AvailableAt:         pgtype.Timestamptz{Time: collectAt, Valid: true},
	})
	if err != nil {
		return nil, job, err
	}
	_, err = qtx.CompleteAgentDispatchAcceptance(ctx, db.CompleteAgentDispatchAcceptanceParams{
		ResponseStatus:      pgtype.Int4{Int32: int32(response.Status()), Valid: true},
		ResponseContentType: pgtype.Text{String: response.header.Get("Content-Type"), Valid: true},
		ResponseBody:        append([]byte{}, response.body.Bytes()...),
		ID:                  acceptance.ID,
		LeaseToken:          acceptance.LeaseToken,
	})
	if err != nil {
		return nil, job, err
	}
	h.markSceneMemoryDirty(ctx, qtx, command, dispatchContext, job, idempotencyKey)
	if err := tx.Commit(ctx); err != nil {
		return nil, job, err
	}
	if h.SceneMemoryWorker != nil {
		h.SceneMemoryWorker.Notify()
	}
	h.prefetchCoordinatorWindowHistory(job)
	slog.Info("inbound coordinator job accepted",
		"event", "inbound_coordinator_job_accepted",
		"job_id", util.UUIDToString(job.ID),
		"source", command.Source.Type,
		"collect_quiet_ms", collectQuiet.Milliseconds(),
	)
	h.publishChatToUser(protocol.EventChatMessage, util.UUIDToString(job.WorkspaceID), util.UUIDToString(job.UserID), "member", util.UUIDToString(job.UserID), util.UUIDToString(job.ChatSessionID), protocol.ChatMessagePayload{
		ChatSessionID: util.UUIDToString(job.ChatSessionID),
		MessageID:     util.UUIDToString(job.UserMessageID),
		Role:          "user", Content: userMessage.Content,
		CreatedAt:      timestampToString(userMessage.CreatedAt),
		SessionCreated: true,
	})
	return response, job, nil
}

// prefetchCoordinatorWindowHistory starts the DingTalk history read of a
// committed job that is still collecting, from the same persisted command
// the claiming worker restores, and wakes this replica's workers when the
// window closes so the replica holding the read usually claims the job. It
// does nothing unless the Coordinator enables the read for the agent.
func (h *Handler) prefetchCoordinatorWindowHistory(job db.InboundCoordinatorJob) {
	coordinator := h.InboundCoordinator
	if coordinator == nil || !job.AgentID.Valid || !job.AvailableAt.Valid {
		return
	}
	// Off the admission path: the owner-switch lookup and command restore
	// must not delay the Router's 202.
	go func() {
		command, err := restoreInboundCoordinatorCommand(job.Command, job.EndpointNamespaceID, h.TaskCompletionTargetIdentity)
		if err != nil || strings.TrimSpace(command.TaskFinishedTaskID) != "" || command.Event.Domain != "channel" || command.Event.Type != "message.created" {
			return
		}
		turn := coordinatorHistoryInputs(command, job.AgentID, job.CreatedAt.Time)
		// The job id is the claimed decision's trace id; it only labels the log.
		turn.TraceID = util.UUIDToString(job.ID)
		if !coordinator.PrefetchWindowHistory(turn) {
			return
		}
		h.InboundCoordinatorWorker.WakeAt(util.UUIDToString(job.ID), job.AvailableAt.Time)
		h.shadowCoordinatorFirstRound(job, command)
	}()
}

// shadowCoordinatorFirstRound starts the collect-window shadow of the job's
// first model request. It assembles the Turn from what the claim uses: the
// persisted command, its execution plan, the agent row, the idempotency key
// and the claim context (trace id and history cutoff), without the plan
// checkpoint and decision observer that only the lease holder installs.
func (h *Handler) shadowCoordinatorFirstRound(job db.InboundCoordinatorJob, command DispatchCommand) {
	coordinator := h.InboundCoordinator
	if coordinator == nil || h.Queries == nil || command.ProactiveConversation || dispatchHasAttachments(command) {
		return
	}
	dispatchContext := agentDispatchContext{
		EndpointID:          job.DispatchEndpointID,
		EndpointNamespaceID: job.EndpointNamespaceID,
		UserID:              job.UserID,
		WorkspaceID:         job.WorkspaceID,
		AgentID:             job.AgentID,
	}
	plan, err := buildAgentDispatchExecutionPlan(command, dispatchContext)
	if err != nil {
		return
	}
	jobID := util.UUIDToString(job.ID)
	ctx, cancel := context.WithTimeout(context.Background(), coordinatorShadowAssemblyTimeout)
	defer cancel()
	ctx = inboundcoord.ContextWithTraceID(ctx, jobID)
	ctx = inboundcoord.ContextWithHistoryBefore(ctx, job.CreatedAt.Time)
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: job.AgentID, WorkspaceID: job.WorkspaceID})
	if err != nil || agent.ArchivedAt.Valid {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/internal/inbound-coordinator", nil)
	if err != nil {
		return
	}
	req.Header.Set("Idempotency-Key", job.IdempotencyKey)
	turn := coordinatorDecisionTurn(ctx, h, coordinator, command, agent, plan.Prompt.DisplayContent, job.UserID,
		dispatchRuntimeContext(command, dispatchIdempotencyKey(req, command)))
	turn.TraceID = jobID
	coordinator.ShadowFirstRound(turn)
}

// windowHistoryEligibleJob reports whether this claim is the first,
// undisturbed claim of the job's collect window: claiming increments
// attempt_count, a park restores it but records last_error, and a retry or
// lease takeover leaves a higher count. Only this claim may reuse the
// window's early history read.
func windowHistoryEligibleJob(job db.InboundCoordinatorJob) bool {
	return job.AttemptCount == 1 && !job.LastError.Valid
}

func (h *Handler) persistCoordinatorJobChat(ctx context.Context, job db.InboundCoordinatorJob, decision inboundcoord.Decision) error {
	// The capability answer's configuration link is delivered in the
	// DingTalk reply only; the transcript keeps a placeholder.
	content := strings.TrimSpace(redactContextConfigLinks(decision.UserText))
	if content == "" {
		switch decision.Action {
		case inboundcoord.ActionIssue:
			content = "已转入 Issue 继续处理。"
		case inboundcoord.ActionContinue:
			content = "已交给 Agent 继续处理。"
		case inboundcoord.ActionSilence:
			content = "本轮无需回复。"
		case inboundcoord.ActionRetry:
			content = "关联事项正在处理中，稍后重试。"
		default:
			content = "Coordinator 已完成本轮处理。"
		}
	}
	trace := decision.TraceJSON()
	var message db.ChatMessage
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	exists, err := qtx.CoordinatorChatMessageExists(ctx, job.ChatSessionID)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	created, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: job.ChatSessionID,
		Role:          "assistant",
		Content:       content,
		MessageKind:   pgtype.Text{String: protocol.ChatMessageKindCoordinator, Valid: true},
		ElapsedMs:     pgtype.Int8{Int64: decision.ElapsedMs, Valid: decision.ElapsedMs > 0},
		SourcePayload: trace,
	})
	if err != nil {
		return err
	}
	message = created
	if err := qtx.TouchChatSession(ctx, job.ChatSessionID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	tracePayload := decision.Trace()
	h.publishChatToUser(protocol.EventChatMessage, util.UUIDToString(job.WorkspaceID), util.UUIDToString(job.UserID), "agent", util.UUIDToString(job.AgentID), util.UUIDToString(job.ChatSessionID), protocol.ChatMessagePayload{
		ChatSessionID: util.UUIDToString(job.ChatSessionID),
		MessageID:     util.UUIDToString(message.ID),
		Role:          "assistant", Content: message.Content,
		CreatedAt:   timestampToString(message.CreatedAt),
		MessageKind: protocol.ChatMessageKindCoordinator,
		ElapsedMs:   decision.ElapsedMs,
		Coordinator: &tracePayload,
	})
	return nil
}

func shouldDeferInboundCoordinator(command DispatchCommand, plan agentDispatchExecutionPlan) bool {
	return (command.CompletionCallback != nil || command.ProactiveConversation) &&
		command.Event.Domain == "channel" && command.Event.Type == "message.created" &&
		(plan.MaterializerType == protocol.DispatchSurfaceTypeIssue ||
			plan.MaterializerType == protocol.DispatchSurfaceTypeChat)
}

// shouldEnqueueInboundCoordinatorJob is the admission gate for the durable
// short-loop queue. The owner off switch must skip it: auto-mode IM has to
// land on the sandbox chat task immediately, or the original message is
// stranded on a Coordinator session and never reaches background Issue work.
func shouldEnqueueInboundCoordinatorJob(
	ctx context.Context,
	h *Handler,
	command DispatchCommand,
	plan agentDispatchExecutionPlan,
	agentID pgtype.UUID,
) bool {
	if !shouldDeferInboundCoordinator(command, plan) {
		return false
	}
	return inboundCoordinatorEnabled(ctx, h, agentID)
}

func inboundCoordinatorEnabled(ctx context.Context, h *Handler, agentID pgtype.UUID) bool {
	if h == nil || h.Queries == nil || !agentID.Valid {
		return true
	}
	on, err := h.Queries.GetAgentInboundCoordinator(ctx, agentID)
	if err != nil {
		return true
	}
	return on
}

func (h *Handler) markSceneMemoryDirty(
	ctx context.Context,
	qtx *db.Queries,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
	job db.InboundCoordinatorJob,
	idempotencyKey string,
) {
	if h == nil || h.SceneMemoryStore == nil || qtx == nil {
		return
	}
	if command.Source.Type != "digital_employee" {
		return
	}
	body := dispatchInboundEventBody(command)
	if body == "" || isInboundResetMemory(body) {
		return
	}
	flags, err := qtx.GetAgentSceneMemoryFlags(ctx, dispatchContext.AgentID)
	if err != nil || !flags.WriteEnabled {
		return
	}
	sc, err := dispatchScene(ctx, qtx, command, dispatchContext)
	if err != nil {
		slog.Info("scene memory mark dirty skipped; no agent scene",
			"event", "scene_memory_mark_dirty",
			"error", err,
		)
		return
	}
	ids := dispatchAssocIDs(command)
	store := scenememory.NewStore(qtx)
	row, err := store.MarkDirty(ctx, sc, scenememory.DirtyTrigger{
		OccurredAt:     dispatchMessageOccurredAt(command),
		EvidenceID:     ids.EvidenceID,
		JobID:          job.ID,
		CoordTraceID:   util.UUIDToString(job.ID),
		IdempotencyKey: firstNonEmpty(strings.TrimSpace(idempotencyKey), job.IdempotencyKey),
	})
	if err != nil {
		slog.Warn("scene memory mark dirty failed",
			"event", "scene_memory_mark_dirty",
			"scene_id", util.UUIDToString(sc.ID),
			"error", err,
		)
		return
	}
	slog.Info("scene memory marked dirty",
		"event", "scene_memory_mark_dirty",
		"workspace_id", util.UUIDToString(dispatchContext.WorkspaceID),
		"agent_id", util.UUIDToString(dispatchContext.AgentID),
		"scene_id", util.UUIDToString(sc.ID),
		"scene_key", sc.ExternalSceneID,
		"dirty_revision", row.DirtyRevision,
		"idempotency", firstNonEmpty(strings.TrimSpace(idempotencyKey), job.IdempotencyKey),
	)
}

func dispatchMessageOccurredAt(command DispatchCommand) time.Time {
	if message, ok := lastInboundTextMessage(command); ok {
		if occurred := unixMillis(message.OccurredAt); !occurred.IsZero() {
			return occurred
		}
	}
	return time.Now().UTC()
}

func unixMillis(raw int64) time.Time {
	if raw <= 0 {
		return time.Time{}
	}
	if raw < 1e12 {
		return time.Unix(raw, 0).UTC().Truncate(time.Second)
	}
	return time.UnixMilli(raw).UTC().Truncate(time.Second)
}
