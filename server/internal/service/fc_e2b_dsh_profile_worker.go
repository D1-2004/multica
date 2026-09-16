package service

import (
	"context"
	"errors"
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
 RETURNING p.workspace_id,p.agent_id,p.desired_revision`).Scan(&key.WorkspaceID, &key.AgentID, &revision)
	if err != nil {
		return
	}
	status, err := l.DSHEmployeeProfile(ctx, key, true)
	if err == nil {
		// Prepare may publish a new revision for a saved plugin edit.
		revision, err = strconv.ParseInt(status.DesiredRevision, 10, 64)
		if err != nil {
			return
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
	cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	if err == nil {
		_, _ = l.Pool.Exec(cleanup, `UPDATE dsh_employee_profile SET next_apply_at=now()+interval '30 seconds',apply_attempts=0,apply_error=''
 WHERE workspace_id=$1 AND agent_id=$2 AND desired_revision=$3 AND applied_revision=$3`, key.WorkspaceID, key.AgentID, revision)
		return
	}
	// Active tasks and confirmed retirement are expected transitions, not a
	// failed plugin. An actual native startup failure consumes a bounded retry.
	failed := !errors.Is(err, errDSHHostWaiting) || errors.Is(err, errDSHHostStartup)
	_, _ = l.Pool.Exec(cleanup, `UPDATE dsh_employee_profile SET next_apply_at=now()+interval '10 seconds',
 apply_attempts=apply_attempts+CASE WHEN $4 THEN 1 ELSE 0 END,
 apply_error=CASE WHEN $4 THEN 'host_start_failed' ELSE apply_error END
 WHERE workspace_id=$1 AND agent_id=$2 AND desired_revision=$3`, key.WorkspaceID, key.AgentID, revision, failed)
}
