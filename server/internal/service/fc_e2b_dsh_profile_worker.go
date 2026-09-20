package service

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/multica-ai/multica/server/internal/dshhost"
)

// Reconcile saved configuration even after the browser closes. A database
// claim fences concurrent application replicas; failed revisions need a user
// retry or a changed configuration before another Host startup is attempted.
// Existing employee/scope locks remain the only authority for Host changes.
func (l *FCE2BLauncher) RunDSHProfileWorker(ctx context.Context) {
	if l == nil || l.Pool == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.applyNextDSHProfile(ctx)
		}
	}
}

func (l *FCE2BLauncher) applyNextDSHProfile(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	// Provisioned employees participate even when their first plugin was saved
	// before anyone visited the Profile page. No sandbox is created by this seed.
	if _, err := l.Pool.Exec(ctx, `INSERT INTO dsh_employee_profile(workspace_id,agent_id)
 SELECT a.workspace_id,a.id FROM agent a JOIN agent_runtime r ON r.id=a.runtime_id
 JOIN employee_filesystem_host h ON h.workspace_id=a.workspace_id AND h.agent_id=a.id
 WHERE a.archived_at IS NULL AND a.runtime_mode='cloud' AND r.provider='dsh'
 AND CASE WHEN r.metadata->>'kind'='fc-e2b' THEN 'aliyun_fc' ELSE r.metadata->>'sandbox_backend' END='aliyun_fc'
 ON CONFLICT(workspace_id,agent_id) DO NOTHING`); err != nil {
		return
	}
	var key dshhost.Key
	var revision int64
	var revisionUpdatedAt time.Time
	err := l.Pool.QueryRow(ctx, `WITH candidate AS (
 SELECT p.workspace_id,p.agent_id FROM dsh_employee_profile p
 JOIN agent a ON a.id=p.agent_id AND a.workspace_id=p.workspace_id
 JOIN agent_runtime r ON r.id=a.runtime_id
 WHERE p.next_apply_at<=now()
 AND a.archived_at IS NULL AND a.runtime_mode='cloud' AND r.provider='dsh'
 AND CASE WHEN r.metadata->>'kind'='fc-e2b' THEN 'aliyun_fc' ELSE r.metadata->>'sandbox_backend' END='aliyun_fc'
 ORDER BY p.next_apply_at FOR UPDATE OF p SKIP LOCKED LIMIT 1
 ) UPDATE dsh_employee_profile p SET next_apply_at=now()+interval '2 minutes'
 FROM candidate c WHERE p.workspace_id=c.workspace_id AND p.agent_id=c.agent_id
 RETURNING p.workspace_id,p.agent_id,p.desired_revision,p.updated_at`).Scan(&key.WorkspaceID, &key.AgentID, &revision, &revisionUpdatedAt)
	if err != nil {
		return
	}
	status, err := l.DSHEmployeeProfile(ctx, key, true)
	if err == nil {
		// Prepare may publish a new revision for a saved plugin edit.
		previousRevision := revision
		revision, err = strconv.ParseInt(status.DesiredRevision, 10, 64)
		if err != nil {
			return
		}
		if revision != previousRevision {
			revisionUpdatedAt = time.Now()
		}
		if status.State == "waiting_for_builds" || status.State == "build_failed" || status.State == "apply_failed" {
			_, _ = l.Pool.Exec(ctx, `UPDATE dsh_employee_profile SET next_apply_at=now()+interval '10 seconds'
 WHERE workspace_id=$1 AND agent_id=$2 AND desired_revision=$3`, key.WorkspaceID, key.AgentID, revision)
			return
		}
		if !status.Current {
			_, err = l.EnsureDSHEmployeeHost(ctx, key)
		}
	}
	if err == nil && l.RefreshDSHSessionInputs != nil {
		refresh, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		if refreshErr := l.RefreshDSHSessionInputs(refresh, key); refreshErr != nil {
			slog.Warn("DSH background session input connection is not ready", "agent_id", key.AgentID, "workspace_id", key.WorkspaceID)
		}
		cancel()
	}
	cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	if err == nil {
		_, _ = l.Pool.Exec(cleanup, `UPDATE dsh_employee_profile SET next_apply_at=now()+interval '30 seconds',apply_attempts=0,apply_error=''
 WHERE workspace_id=$1 AND agent_id=$2 AND desired_revision=$3 AND applied_revision=$3`, key.WorkspaceID, key.AgentID, revision)
		return
	}
	// Active tasks and confirmed retirement are expected transitions, not a
	// failed plugin. An actual native startup failure consumes a bounded retry.
	failed := dshProfileApplyConsumesAttempt(ctx.Err(), err, time.Since(revisionUpdatedAt))
	_, _ = l.Pool.Exec(cleanup, `UPDATE dsh_employee_profile SET next_apply_at=now()+interval '10 seconds',
 apply_attempts=apply_attempts+CASE WHEN $4 THEN 1 ELSE 0 END,
 apply_error=CASE WHEN $4 THEN $5 ELSE apply_error END
 WHERE workspace_id=$1 AND agent_id=$2 AND desired_revision=$3`, key.WorkspaceID, key.AgentID, revision, failed, dshProfileApplyError(err))
}

// The worker's 90-second reconciliation window is shorter than a native cold
// start (320 seconds). Remote work can finish after that window, so cancellation
// is not a confirmed startup failure. Keep reconciling instead of exhausting the
// three-attempt budget while the same owned process is still starting. The
// grace window is durable across replicas and bounded: persistent timeouts
// eventually consume attempts too, rather than applying forever.
func dshProfileApplyConsumesAttempt(contextErr, err error, revisionAge time.Duration) bool {
	if contextErr != nil {
		return revisionAge >= 15*time.Minute
	}
	return !errors.Is(err, errDSHHostWaiting) || errors.Is(err, errDSHHostStartup)
}

func dshProfileApplyError(err error) string {
	if errors.Is(err, errDSHNativeSync) {
		return "native_sync_failed"
	}
	if errors.Is(err, errDSHHostStartup) {
		return "host_start_failed"
	}
	return "profile_apply_failed"
}
