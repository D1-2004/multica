package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"

	"github.com/multica-ai/multica/server/internal/util"
)

const (
	// ChannelChatDebounceWindow preserves the existing three-second silence
	// window while moving its state from a process-local timer into Postgres.
	ChannelChatDebounceWindow = 3 * time.Second

	channelChatTaskPromoteInterval = time.Second
	channelChatTaskNotifyRetry     = 15 * time.Second
	channelChatTaskPromoteBatch    = 64
)

// ChatTaskIdentity keeps the Multica principal that authorizes a chat task
// distinct from the person attributed as its initiator. Bound senders set both
// fields to the same user. Allow-unbound channels set PrincipalUserID to their
// installer and leave InitiatorUserID invalid; their external display identity
// travels in server-private task context instead.
type ChatTaskIdentity struct {
	PrincipalUserID pgtype.UUID
	InitiatorUserID pgtype.UUID
}

// PreparedChannelChatTask is the side-effect-free task envelope the channel
// engine carries into its append transaction. Runtime/agent validation and the
// optional Composio overlay are resolved before the transaction because the
// overlay builder may perform network I/O. No message payload or credential is
// retained here beyond the existing encrypted runtime overlay destined for the
// task row.
type PreparedChannelChatTask struct {
	ID                   pgtype.UUID
	AgentID              pgtype.UUID
	RuntimeID            pgtype.UUID
	InitiatorUserID      pgtype.UUID
	OriginatorUserID     pgtype.UUID
	ForceFreshSession    bool
	TaskContext          []byte
	RuntimeMCPOverlay    []byte
	RuntimeConnectedApps []byte
	DebounceSeconds      float64
}

// PrepareChannelChatTask resolves the same runtime and per-user MCP data as
// EnqueueChatTask without writing a task. The channel session service consumes
// the result inside its message+dedup transaction via
// UpsertDeferredChannelChatTask.
func (s *TaskService) PrepareChannelChatTask(
	ctx context.Context,
	session db.ChatSession,
	identity ChatTaskIdentity,
	forceFreshSession bool,
	taskContext []byte,
) (PreparedChannelChatTask, error) {
	agent, err := s.Queries.GetAgent(ctx, session.AgentID)
	if err != nil {
		slog.Error("durable channel task prepare failed",
			"event", "channel_chat_task_prepare_failed",
			"chat_session_id", util.UUIDToString(session.ID),
			"agent_id", util.UUIDToString(session.AgentID),
			"error", err,
		)
		return PreparedChannelChatTask{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return PreparedChannelChatTask{}, ErrChatTaskAgentArchived
	}
	if !agent.RuntimeID.Valid {
		return PreparedChannelChatTask{}, ErrChatTaskAgentNoRuntime
	}

	overlay := s.buildRuntimeMCPOverlay(ctx, identity.PrincipalUserID, agent)
	taskID := uuid.New()
	return PreparedChannelChatTask{
		ID:                   pgtype.UUID{Bytes: [16]byte(taskID), Valid: true},
		AgentID:              session.AgentID,
		RuntimeID:            agent.RuntimeID,
		InitiatorUserID:      identity.InitiatorUserID,
		OriginatorUserID:     identity.PrincipalUserID,
		ForceFreshSession:    forceFreshSession,
		TaskContext:          append([]byte(nil), taskContext...),
		RuntimeMCPOverlay:    append([]byte(nil), overlay.Overlay...),
		RuntimeConnectedApps: append([]byte(nil), overlay.ConnectedApps...),
		DebounceSeconds:      ChannelChatDebounceWindow.Seconds(),
	}, nil
}

// RunDeferredChannelTaskPromoter owns the durable deferred->queued transition
// and queued notification retries. Every API replica may run it: the SQL uses
// SKIP LOCKED for single-winner state changes, while runtime launch leases
// fence FC/E2B startup across replicas.
func (s *TaskService) RunDeferredChannelTaskPromoter(ctx context.Context) {
	if s == nil || s.Queries == nil {
		return
	}
	slog.Info("durable channel task promoter started",
		"event", "channel_chat_task_promoter_started",
		"interval_ms", channelChatTaskPromoteInterval.Milliseconds(),
		"notify_retry_seconds", int64(channelChatTaskNotifyRetry/time.Second),
		"batch_size", channelChatTaskPromoteBatch,
	)
	defer slog.Info("durable channel task promoter stopped",
		"event", "channel_chat_task_promoter_stopped",
	)

	runOnce := func() {
		if _, err := s.PromoteAndNotifyDueChannelTasks(ctx); err != nil && ctx.Err() == nil {
			slog.Error("durable channel task promoter iteration failed",
				"event", "channel_chat_task_promoter_failed",
				"error", err,
			)
		}
	}

	// Recover already-due rows immediately after process start rather than
	// waiting through one ticker interval.
	runOnce()
	ticker := time.NewTicker(channelChatTaskPromoteInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

// PromoteAndNotifyDueChannelTasks promotes due durable chat batches and claims
// due queued notifications. Claiming a notification first pushes fire_at
// forward, so a crash between the database commit and the asynchronous wake or
// runtime launch can only postpone the next attempt by the retry interval.
func (s *TaskService) PromoteAndNotifyDueChannelTasks(ctx context.Context) (int, error) {
	promoted, err := s.Queries.PromoteDueDeferredChannelChatTasks(ctx, channelChatTaskPromoteBatch)
	if err != nil {
		return 0, fmt.Errorf("promote due channel chat tasks: %w", err)
	}
	for _, task := range promoted {
		slog.Info("durable channel chat task promoted",
			"event", "channel_chat_task_promoted",
			"task_id", util.UUIDToString(task.ID),
			"chat_session_id", util.UUIDToString(task.ChatSessionID),
			"runtime_id", util.UUIDToString(task.RuntimeID),
			"agent_id", util.UUIDToString(task.AgentID),
			"fire_at", task.FireAt.Time.UTC(),
			"promotion_lag_ms", time.Since(task.FireAt.Time).Milliseconds(),
		)
		s.captureTaskQueued(ctx, task)
		s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	}

	pending, err := s.Queries.ClaimPendingChannelChatTaskNotifications(ctx, db.ClaimPendingChannelChatTaskNotificationsParams{
		BatchSize:    channelChatTaskPromoteBatch,
		RetrySeconds: channelChatTaskNotifyRetry.Seconds(),
	})
	if err != nil {
		return len(promoted), fmt.Errorf("claim pending channel chat task notifications: %w", err)
	}
	for _, task := range pending {
		// Do not call NotifyTaskEnqueued here: retries would double-count the
		// enqueue metric. The durable row already emitted task:queued exactly
		// once at promotion; only the idempotent wake and lease-fenced launcher
		// need retrying.
		s.notifyTaskAvailable(task)
		s.launchRuntimeForTask(task)
		slog.Info("durable channel chat task notification scheduled",
			"event", "channel_chat_task_notification_scheduled",
			"task_id", util.UUIDToString(task.ID),
			"chat_session_id", util.UUIDToString(task.ChatSessionID),
			"runtime_id", util.UUIDToString(task.RuntimeID),
			"agent_id", util.UUIDToString(task.AgentID),
			"next_notify_at", task.FireAt.Time.UTC(),
		)
	}

	return len(promoted), nil
}
