package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Cloud provisioning APIs have no push callback. Reconcile their durable
// pending receipts independently of task scheduling, then emit actual ready
// events. No new resource intent or runner is created by this worker.
func (l *FCE2BLauncher) RunRuntimeReadinessReconciler(ctx context.Context) {
	if l == nil || l.Pool == nil || l.Tasks == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := l.withCurrentConfig()
			if !current.Config.QuickWins.DSHEventWakeup {
				continue
			}
			current.reconcileRuntimeReadiness(ctx)
		}
	}
}

func (l *FCE2BLauncher) reconcileRuntimeReadiness(ctx context.Context) {
	rows, err := l.Pool.Query(ctx, `SELECT pending.workspace_id,pending.agent_id,pending.scope_id,pending.kind,waiting.id FROM (
 SELECT workspace_id,agent_id,'00000000-0000-0000-0000-000000000000'::uuid AS scope_id,'storage' AS kind,updated_at FROM dsh_storage_provision WHERE state<>'complete'
 UNION ALL SELECT workspace_id,agent_id,scope_id,'host',updated_at FROM employee_filesystem_sandbox WHERE state='creating'
 UNION ALL SELECT workspace_id,agent_id,'00000000-0000-0000-0000-000000000000'::uuid,'host',updated_at FROM dsh_employee_host WHERE state='creating'
 ) pending LEFT JOIN LATERAL (
 SELECT task.id FROM agent_task_queue task JOIN agent ON agent.id=task.agent_id
 WHERE task.agent_id=pending.agent_id AND task.status='queued' AND agent.archived_at IS NULL
 AND (pending.scope_id='00000000-0000-0000-0000-000000000000'::uuid OR COALESCE(task.chat_session_id,task.issue_id)=pending.scope_id)
 ORDER BY task.created_at,task.id LIMIT 1
 ) waiting ON true
 WHERE EXISTS (SELECT 1 FROM agent_task_queue task JOIN agent ON agent.id=task.agent_id WHERE task.agent_id=pending.agent_id AND task.status='queued' AND agent.archived_at IS NULL)
 ORDER BY pending.updated_at LIMIT 8`)
	if err != nil {
		slog.Warn("runtime readiness reconciliation scan failed", "error", err)
		return
	}
	type candidate struct {
		key    dshhost.Key
		scope  uuid.UUID
		kind   string
		taskID pgtype.UUID
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.key.WorkspaceID, &c.key.AgentID, &c.scope, &c.kind, &c.taskID) == nil {
			candidates = append(candidates, c)
		}
	}
	rows.Close()
	var workers sync.WaitGroup
	slots := make(chan struct{}, 4)
	defer workers.Wait()
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		workers.Add(1)
		go func(c candidate) {
			defer workers.Done()
			defer func() { <-slots }()
			work, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			conn, err := l.Pool.Acquire(work)
			if err != nil {
				return
			}
			defer conn.Release()
			// Link shared preparation to its oldest queued waiter. This is only
			// diagnostic attribution, never authority to launch that task. Reuse
			// this connection so all workers cannot deadlock acquiring another.
			if c.taskID.Valid && l.Tasks.CurrentRuntimeStartRecoveryConfig().StartupObservability {
				if task, err := db.New(conn).GetAgentTask(work, c.taskID); err == nil {
					work = l.Tasks.withStartupObservability(work, task)
				}
			}
			// The existing provisioning/host ledger CAS fences competing replicas.
			if c.kind == "storage" {
				if l.ProvisionDSHStorage != nil {
					_, _ = l.ProvisionDSHStorage(work, conn, c.key)
				}
				return
			}
			var store dshhost.Store = dshhost.PostgresStore{DB: conn}
			if c.scope != uuid.Nil {
				store = dshhost.FilesystemSandboxStore{DB: conn, ScopeID: c.scope}
			}
			host, err := store.Get(work, c.key)
			if err != nil || host.State != "creating" {
				return
			}
			provider, err := l.dshHostProvider(host.Storage)
			if err != nil {
				return
			}
			ready, err := (dshhost.Manager{Store: store, Provider: provider}).ReconcileCreate(work, c.key)
			if err == nil && ready.State == "running" {
				l.notifyDSHReady(work, conn, c.key, "host_create_ready")
			}
		}(c)
	}
}
