package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const runtimeReadinessChannel = "multica_runtime_ready"

// Persist before notifying. A nil agent means a workspace-wide Profile build.
// The readiness timestamp survives notification loss and the launch-lease race.
func (s *TaskService) NotifyDSHReadiness(ctx context.Context, database dshhost.Database, key dshhost.Key, reason string) {
	if s == nil || database == nil || !s.CurrentRuntimeStartRecoveryConfig().DSHEventWakeup {
		return
	}
	_, err := database.Exec(ctx, `WITH ready AS (
 INSERT INTO runtime_readiness_event(workspace_id,agent_id,event_at,reason)
 VALUES($1,$2,clock_timestamp(),$3)
 ON CONFLICT(workspace_id,agent_id) DO UPDATE SET event_at=EXCLUDED.event_at,reason=EXCLUDED.reason
 RETURNING workspace_id
 ) SELECT pg_notify('multica_runtime_ready','') FROM ready`, key.WorkspaceID, key.AgentID, reason)
	if err != nil {
		slog.Warn("runtime readiness notification failed; sweeper will recover", "reason", reason, "error", err)
	}
}

func (s *TaskService) notifyRuntimeReadinessHint(ctx context.Context) {
	if s == nil || s.runtimeReadinessPool() == nil || !s.CurrentRuntimeStartRecoveryConfig().DSHEventWakeup {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := s.runtimeReadinessPool().Exec(ctx, `SELECT pg_notify('multica_runtime_ready','')`); err != nil {
		slog.Warn("runtime lease release notification failed", "error", err)
	}
}

func (s *TaskService) recoverReadyRuntimeTasks(ctx context.Context) {
	cfg := s.CurrentRuntimeStartRecoveryConfig()
	if !cfg.DSHEventWakeup || s.Queries == nil || s.RuntimeLauncher == nil {
		return
	}
	tasks, err := s.Queries.ListDSHHostWaitingTasks(ctx, db.ListDSHHostWaitingTasksParams{DshEventWakeup: true, EventsOnly: true})
	if err != nil {
		slog.Warn("runtime readiness scan failed", "error", err)
		return
	}
	for _, task := range tasks {
		s.RecoverQueuedFCE2BTask(ctx, task)
	}
}

// A dedicated connection follows the existing daemonws PostgreSQL notifier.
// Every replica may receive the same hint: the launch lease elects one owner.
func (s *TaskService) RunRuntimeReadinessListener(ctx context.Context) {
	if s == nil || s.runtimeReadinessPool() == nil {
		return
	}
	for ctx.Err() == nil {
		s.listenRuntimeReadiness(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
func (s *TaskService) listenRuntimeReadiness(ctx context.Context) {
	conn, err := pgx.ConnectConfig(ctx, s.runtimeReadinessPool().Config().ConnConfig.Copy())
	if err != nil {
		return
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	if _, err = conn.Exec(ctx, "LISTEN "+runtimeReadinessChannel); err != nil {
		return
	}
	// Read after LISTEN so events preceding connection/reconnection cannot vanish.
	s.recoverReadyRuntimeTasks(ctx)
	for ctx.Err() == nil {
		if _, err = conn.WaitForNotification(ctx); err != nil {
			return
		}
		s.recoverReadyRuntimeTasks(ctx)
	}
}

func (l *FCE2BLauncher) notifyDSHReady(ctx context.Context, database dshhost.Database, key dshhost.Key, reason string) {
	if l != nil && l.Tasks != nil {
		l.Tasks.NotifyDSHReadiness(ctx, database, key, reason)
	}
}

func (l *FCE2BLauncher) profileBuildReady(ctx context.Context, workspaceID uuid.UUID) {
	l.notifyDSHReady(ctx, l.Pool, dshhost.Key{WorkspaceID: workspaceID}, "profile_ready")
}

func (s *TaskService) runtimeReadinessPool() *pgxpool.Pool {
	if s == nil {
		return nil
	}
	pool, _ := s.TxStarter.(*pgxpool.Pool)
	return pool
}
