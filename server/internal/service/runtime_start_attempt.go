package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const (
	RuntimeStartCapabilityEventsV1 = "runtime_start_events_v1"
	RuntimeStartProtocolLegacyV1   = "legacy-v1"
	RuntimeStartProtocolHTTPJSONV1 = "http-json-v1"
)

var runtimeStartStagePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func ValidateRuntimeStartStage(stage string) error {
	if !runtimeStartStagePattern.MatchString(strings.TrimSpace(stage)) {
		return errors.New("invalid runtime start stage")
	}
	return nil
}

type RuntimeStartFailure struct {
	Code           string
	Phase          string
	Backend        SandboxBackendKind
	Retryable      bool
	PublicMessage  string
	InternalDetail string
	hasUserDetail  bool
}

// runtimeStartUserDetailer marks an error detail that is safe to return to the
// task owner after redaction. Third-party clients attach this at the boundary
// where the response origin is still known; launcher-internal errors remain
// private and continue to use the generic stage message.
type runtimeStartUserDetailer interface {
	runtimeStartUserDetail() string
}

type runtimeStartExternalError struct {
	cause      error
	userDetail string
}

func (e *runtimeStartExternalError) Error() string {
	if e == nil || e.cause == nil {
		return "Runtime startup external service failed"
	}
	return e.cause.Error()
}

func (e *runtimeStartExternalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *runtimeStartExternalError) runtimeStartUserDetail() string {
	if e == nil {
		return ""
	}
	return e.userDetail
}

func withRuntimeStartUserDetail(cause error, detail string) error {
	detail = sanitizeRuntimeStartUserDetail(detail)
	if cause == nil {
		cause = errors.New(detail)
	}
	if detail == "" {
		return cause
	}
	return &runtimeStartExternalError{cause: cause, userDetail: detail}
}

func runtimeStartUserDetailFromError(err error) string {
	if err == nil {
		return ""
	}
	var detailer runtimeStartUserDetailer
	if !errors.As(err, &detailer) {
		return ""
	}
	return sanitizeRuntimeStartUserDetail(detailer.runtimeStartUserDetail())
}

func sanitizeRuntimeStartUserDetail(detail string) string {
	detail = strings.Join(strings.Fields(redact.Text(detail)), " ")
	const maxRunes = 1024
	runes := []rune(detail)
	if len(runes) > maxRunes {
		detail = string(runes[:maxRunes]) + "…"
	}
	return detail
}

func NewRuntimeStartFailure(
	backend SandboxBackendKind,
	code string,
	phase string,
	retryable bool,
	publicMessage string,
	internalDetail string,
) RuntimeStartFailure {
	return RuntimeStartFailure{
		Code:           strings.TrimSpace(code),
		Phase:          strings.TrimSpace(phase),
		Backend:        backend,
		Retryable:      retryable,
		PublicMessage:  strings.TrimSpace(publicMessage),
		InternalDetail: redact.Text(internalDetail),
	}
}

func ClassifyRuntimeStartFailure(backend SandboxBackendKind, detail string) RuntimeStartFailure {
	detail = redact.Text(detail)
	lower := strings.ToLower(detail)
	prefix := "FCE2B"
	if backend == SandboxBackendASB {
		prefix = "ASB"
	}
	switch {
	case backend == SandboxBackendAliyunFC && isFCE2BSandboxCapacityRateLimitText(detail):
		return NewRuntimeStartFailure(
			backend,
			"FCE2B-SANDBOX-CAPACITY-429",
			"sandbox_create",
			true,
			"沙箱并发容量暂时不足，系统重试后仍未获得可用容量，请稍后重试。",
			detail,
		)
	case strings.Contains(lower, "did not claim task"):
		return NewRuntimeStartFailure(
			backend,
			prefix+"-RUNNER-CLAIM-TIMEOUT",
			"claim_wait",
			true,
			"Runner 已提交，但没有在规定时间内完成任务领取。",
			detail,
		)
	case strings.Contains(lower, "sandbox create"):
		return NewRuntimeStartFailure(
			backend,
			prefix+"-SANDBOX-CREATE-FAILED",
			"sandbox_create",
			true,
			"沙箱创建失败。",
			detail,
		)
	case strings.Contains(lower, "sandbox") && strings.Contains(lower, "ready"):
		return NewRuntimeStartFailure(
			backend,
			prefix+"-SANDBOX-NOT-READY",
			"sandbox_ready",
			true,
			"沙箱未在规定时间内就绪。",
			detail,
		)
	case strings.Contains(lower, "runner exec"):
		return NewRuntimeStartFailure(
			backend,
			prefix+"-RUNNER-EXEC-FAILED",
			"runner_exec",
			true,
			"Runner 启动命令提交失败。",
			detail,
		)
	default:
		return NewRuntimeStartFailure(
			backend,
			prefix+"-RUNTIME-START-FAILED",
			"runtime_start",
			false,
			"Runtime 启动失败。",
			detail,
		)
	}
}

// ClassifyRuntimeStartError preserves details explicitly marked at an
// external-service boundary. It never promotes an arbitrary internal error to
// user-visible text merely because it happened during Runtime startup.
func ClassifyRuntimeStartError(backend SandboxBackendKind, err error) RuntimeStartFailure {
	if err == nil {
		return ClassifyRuntimeStartFailure(backend, "")
	}
	var identityErr *asbIdentityStartError
	if backend == SandboxBackendASB && errors.As(err, &identityErr) {
		failure := NewRuntimeStartFailure(backend, identityErr.code, "sandbox_ready", false, identityErr.detail, identityErr.detail)
		failure.hasUserDetail = true
		return failure
	}
	detail := redact.Text(err.Error())
	userDetail := runtimeStartUserDetailFromError(err)
	if userDetail != "" && !strings.Contains(detail, userDetail) {
		detail += "; upstream_error=" + userDetail
	}
	failure := ClassifyRuntimeStartFailure(backend, detail)
	if userDetail != "" {
		failure.PublicMessage = userDetail
		failure.hasUserDetail = true
	}
	return failure
}

func RuntimeStartFailureForStage(
	backend SandboxBackendKind,
	stage string,
	code string,
	detail string,
) (RuntimeStartFailure, error) {
	stage = strings.TrimSpace(stage)
	if err := ValidateRuntimeStartStage(stage); err != nil {
		return RuntimeStartFailure{}, err
	}
	code = strings.TrimSpace(code)
	if code == "" {
		code = strings.ToUpper(strings.ReplaceAll(stage, "_", "-")) + "-FAILED"
	}
	backendPrefix := "FCE2B"
	if backend == SandboxBackendASB {
		backendPrefix = "ASB"
	}
	if !strings.HasPrefix(code, backendPrefix+"-") {
		code = backendPrefix + "-" + code
	}
	return NewRuntimeStartFailure(
		backend,
		code,
		stage,
		false,
		"Runtime 在 "+stage+" 阶段启动失败。",
		detail,
	), nil
}

func FormatRuntimeStartUserMessage(taskID pgtype.UUID, failure RuntimeStartFailure) string {
	return fmt.Sprintf(
		"Runtime 启动失败（%s，阶段：%s，任务：%s）：%s",
		failure.Code,
		failure.Phase,
		util.UUIDToString(taskID),
		failure.PublicMessage,
	)
}

func runtimeStartProtocolForRuntime(runtime db.AgentRuntime) string {
	if CloudSandboxRuntimeHasCapability(runtime, RuntimeStartCapabilityEventsV1) {
		return RuntimeStartProtocolHTTPJSONV1
	}
	return RuntimeStartProtocolLegacyV1
}

func runtimeStartFailureAtLastStage(
	ctx context.Context,
	queries *db.Queries,
	attempt db.AgentTaskRuntimeStartAttempt,
	failure RuntimeStartFailure,
) RuntimeStartFailure {
	if queries == nil || !attempt.ID.Valid || failure.Phase != "runtime_start" {
		return failure
	}
	current, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID:        attempt.ID,
		TaskID:    attempt.TaskID,
		RuntimeID: attempt.RuntimeID,
	})
	if err != nil || ValidateRuntimeStartStage(current.LastStage) != nil {
		return failure
	}
	return refineRuntimeStartFailureAtStage(failure, current.LastStage)
}

func refineRuntimeStartFailureAtStage(failure RuntimeStartFailure, stage string) RuntimeStartFailure {
	stage = strings.TrimSpace(stage)
	if ValidateRuntimeStartStage(stage) != nil {
		return failure
	}
	failure.Phase = stage
	if strings.HasSuffix(failure.Code, "-RUNTIME-START-FAILED") {
		prefix := "FCE2B"
		if failure.Backend == SandboxBackendASB {
			prefix = "ASB"
		}
		failure.Code = prefix + "-" + strings.ToUpper(strings.ReplaceAll(stage, "_", "-")) + "-FAILED"
		if !failure.hasUserDetail {
			failure.PublicMessage = "Runtime 在 " + stage + " 阶段启动失败。"
		}
	}
	if failure.InternalDetail == "" {
		failure.InternalDetail = "last_stage=" + stage
	} else {
		failure.InternalDetail += "; last_stage=" + stage
	}
	return failure
}

func (s *TaskService) BeginRuntimeStartAttempt(
	ctx context.Context,
	task db.AgentTaskQueue,
	backend SandboxBackendKind,
	protocol string,
) (db.AgentTaskRuntimeStartAttempt, error) {
	attemptID := pgtype.UUID{Bytes: [16]byte(uuid.New()), Valid: true}
	var attempt db.AgentTaskRuntimeStartAttempt
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		if lease, ok := taskRuntimeLaunchLeaseFromContext(ctx); ok {
			if lease.taskID != task.ID {
				return errors.New("runtime launch lease task does not match startup task")
			}
			fence, err := qtx.SupersedeAgentTaskRuntimeStartAttemptForLease(ctx, db.SupersedeAgentTaskRuntimeStartAttemptForLeaseParams{
				TaskID:     task.ID,
				RuntimeID:  task.RuntimeID,
				LeaseToken: lease.token,
			})
			if err != nil {
				return fmt.Errorf("fence runtime start attempt with task lease: %w", err)
			}
			if !fence.LeaseValid {
				return errRuntimeLaunchLeaseLost
			}
			if fence.SupersededCount > 0 {
				slog.Warn("superseded abandoned runtime start attempt",
					"task_id", util.UUIDToString(task.ID),
					"runtime_id", util.UUIDToString(task.RuntimeID),
					"superseded_count", fence.SupersededCount,
				)
			}
		}
		created, err := qtx.CreateAgentTaskRuntimeStartAttempt(ctx, db.CreateAgentTaskRuntimeStartAttemptParams{
			ID:        attemptID,
			TaskID:    task.ID,
			RuntimeID: task.RuntimeID,
			Backend:   string(backend),
			Protocol:  protocol,
		})
		if err != nil {
			return fmt.Errorf("create runtime start attempt: %w", err)
		}
		attempt = created
		return nil
	})
	if err != nil {
		return db.AgentTaskRuntimeStartAttempt{}, err
	}
	slog.Info("runtime start attempt created",
		"task_id", util.UUIDToString(task.ID),
		"runtime_id", util.UUIDToString(task.RuntimeID),
		"runtime_start_attempt_id", util.UUIDToString(attempt.ID),
		"backend", backend,
		"startup_status_protocol", protocol,
		"stage", attempt.LastStage,
	)
	return attempt, nil
}

func (s *TaskService) UpdateRuntimeStartSandbox(
	ctx context.Context,
	attempt db.AgentTaskRuntimeStartAttempt,
	sandboxID string,
	coldStart bool,
	stage string,
) (db.AgentTaskRuntimeStartAttempt, error) {
	updated, err := s.Queries.UpdateAgentTaskRuntimeStartSandbox(ctx, db.UpdateAgentTaskRuntimeStartSandboxParams{
		SandboxID: sandboxID,
		ColdStart: pgtype.Bool{Bool: coldStart, Valid: true},
		Stage:     stage,
		ID:        attempt.ID,
		TaskID:    attempt.TaskID,
		RuntimeID: attempt.RuntimeID,
	})
	if err != nil {
		return db.AgentTaskRuntimeStartAttempt{}, fmt.Errorf("update runtime start sandbox: %w", err)
	}
	return updated, nil
}

func (s *TaskService) RecordRuntimeStartStage(
	ctx context.Context,
	attemptID pgtype.UUID,
	taskID pgtype.UUID,
	runtimeID pgtype.UUID,
	stage string,
) (db.AgentTaskRuntimeStartAttempt, error) {
	stage = strings.TrimSpace(stage)
	if err := ValidateRuntimeStartStage(stage); err != nil {
		return db.AgentTaskRuntimeStartAttempt{}, err
	}
	updated, err := s.Queries.RecordAgentTaskRuntimeStartStage(ctx, db.RecordAgentTaskRuntimeStartStageParams{
		Stage:     stage,
		ID:        attemptID,
		TaskID:    taskID,
		RuntimeID: runtimeID,
	})
	if err != nil {
		return db.AgentTaskRuntimeStartAttempt{}, err
	}
	slog.Info("runtime start stage",
		"task_id", util.UUIDToString(taskID),
		"runtime_id", util.UUIDToString(runtimeID),
		"runtime_start_attempt_id", util.UUIDToString(attemptID),
		"backend", updated.Backend,
		"startup_status_protocol", updated.Protocol,
		"stage", stage,
	)
	return updated, nil
}

func (s *TaskService) RecordRuntimeStartRunnerExecSubmitted(
	ctx context.Context,
	attemptID pgtype.UUID,
	taskID pgtype.UUID,
	runtimeID pgtype.UUID,
) error {
	updated, err := s.Queries.RecordAgentTaskRuntimeStartRunnerExecSubmitted(ctx, db.RecordAgentTaskRuntimeStartRunnerExecSubmittedParams{
		ID:        attemptID,
		TaskID:    taskID,
		RuntimeID: runtimeID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	slog.Info("runtime start stage",
		"task_id", util.UUIDToString(taskID),
		"runtime_id", util.UUIDToString(runtimeID),
		"runtime_start_attempt_id", util.UUIDToString(attemptID),
		"backend", updated.Backend,
		"startup_status_protocol", updated.Protocol,
		"stage", updated.LastStage,
	)
	return nil
}

func (s *TaskService) MarkRuntimeStartBlocked(ctx context.Context, attempt db.AgentTaskRuntimeStartAttempt) error {
	updated, err := s.Queries.MarkAgentTaskRuntimeStartBlocked(ctx, db.MarkAgentTaskRuntimeStartBlockedParams{
		ID:        attempt.ID,
		TaskID:    attempt.TaskID,
		RuntimeID: attempt.RuntimeID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	slog.Info("runtime start deferred",
		"task_id", util.UUIDToString(updated.TaskID),
		"runtime_id", util.UUIDToString(updated.RuntimeID),
		"runtime_start_attempt_id", util.UUIDToString(updated.ID),
		"backend", updated.Backend,
		"startup_status_protocol", updated.Protocol,
		"stage", updated.LastStage,
		"error_code", updated.ErrorCode,
	)
	return nil
}

// RuntimeStartRecoveryConfig is read afresh for each sweep from Diamond.
// Zero values retain the historical DSH-only recovery and unbounded wait.
type RuntimeStartRecoveryConfig struct {
	DSHEventWakeup       bool
	DingTalkReplyCommand bool
	ASBEventWakeup       bool
	StartupObservability bool
	BoundedReadyExec     bool
	CoalescedHotExec     bool
	BatchSkillResolve    bool

	RecoverAbandonedLaunches bool
	BoundDSHHostWait         bool
}

// RecoverWaitingDSHHosts resumes the same queued tasks after asynchronous
// Profile builds or host reconciliation, without depending on browser polling.
func (s *TaskService) RecoverWaitingDSHHosts(ctx context.Context) {
	if s == nil || s.Queries == nil || s.RuntimeLauncher == nil {
		return
	}
	cfg := RuntimeStartRecoveryConfig{}
	if s.RuntimeStartRecoveryConfig != nil {
		cfg = s.RuntimeStartRecoveryConfig()
	}
	if cfg.BoundDSHHostWait {
		failed, err := s.Queries.ExpireDSHHostWaitingTasks(ctx)
		if err != nil {
			slog.Warn("expire waiting DSH host tasks failed", "error", err)
		} else {
			for _, task := range failed {
				slog.Info("DSH host wait expired", "task_id", util.UUIDToString(task.ID), "error_code", "DSH-HOST-WAIT-TIMEOUT")
			}
			tasks := make([]db.AgentTaskQueue, 0, len(failed))
			for _, row := range failed {
				tasks = append(tasks, db.AgentTaskQueue(row))
			}
			s.HandleFailedTasks(ctx, tasks)
		}
	}
	tasks, err := s.Queries.ListDSHHostWaitingTasks(ctx, db.ListDSHHostWaitingTasksParams{RecoverAbandonedLaunches: cfg.RecoverAbandonedLaunches, DshEventWakeup: cfg.DSHEventWakeup, EventsOnly: false})
	if err != nil {
		slog.Warn("list waiting DSH host tasks failed", "error", err)
		return
	}
	for _, task := range tasks {
		s.RecoverQueuedFCE2BTask(ctx, task)
	}
}

// MarkRuntimeStartCapacityWaiting closes one startup attempt without making the
// task terminal. A later capacity-wait worker creates a fresh attempt under the
// normal task launch lease once another sandbox becomes safely reclaimable.
func (s *TaskService) MarkRuntimeStartCapacityWaiting(
	ctx context.Context,
	attempt db.AgentTaskRuntimeStartAttempt,
) (bool, error) {
	if cause := context.Cause(ctx); errors.Is(cause, errRuntimeLaunchLeaseLost) {
		return false, cause
	}
	updated, err := s.Queries.MarkAgentTaskRuntimeStartCapacityWaiting(
		ctx,
		db.MarkAgentTaskRuntimeStartCapacityWaitingParams{
			ErrorDetail: asbCapacityUnavailableMessage,
			ID:          attempt.ID,
			TaskID:      attempt.TaskID,
			RuntimeID:   attempt.RuntimeID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent claim owns its normal finalizer. A terminal transition has
		// no such finalizer, so close the abandoned starting attempt explicitly.
		if _, supersedeErr := s.Queries.SupersedeAgentTaskRuntimeStartAttemptForTerminalTask(
			ctx,
			db.SupersedeAgentTaskRuntimeStartAttemptForTerminalTaskParams{
				ID: attempt.ID, TaskID: attempt.TaskID, RuntimeID: attempt.RuntimeID,
			},
		); supersedeErr != nil {
			return false, fmt.Errorf("close terminal task Runtime start attempt: %w", supersedeErr)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mark Runtime start waiting for ASB capacity: %w", err)
	}
	slog.Info("runtime start waiting for ASB sandbox capacity",
		"task_id", util.UUIDToString(updated.TaskID),
		"runtime_id", util.UUIDToString(updated.RuntimeID),
		"runtime_start_attempt_id", util.UUIDToString(updated.ID),
		"backend", updated.Backend,
		"startup_status_protocol", updated.Protocol,
		"stage", updated.LastStage,
		"error_code", updated.ErrorCode,
	)
	return true, nil
}
