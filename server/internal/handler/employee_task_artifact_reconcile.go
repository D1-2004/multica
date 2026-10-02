package handler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// MarkWorkspaceEmployeeArtifactsDeleting runs inside workspace teardown before
// attachments/tasks are removed. The cleanup ledger deliberately outlives its
// workspace until object deletion and its late-PUT tombstone passes settle.
func (h *Handler) MarkWorkspaceEmployeeArtifactsDeleting(ctx context.Context, tx pgx.Tx, workspaceID pgtype.UUID) error {
	if tx == nil || !workspaceID.Valid {
		return errEmployeeArtifactInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_task_artifact SET state=CASE WHEN state='tombstoned' THEN state ELSE 'deleting' END,lease_token=NULL,lease_expires_at=NULL,next_attempt_at=now(),updated_at=now() WHERE workspace_id=$1`, workspaceID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM attachment a USING employee_task_artifact f WHERE a.id=f.attachment_id AND a.workspace_id=$1 AND f.workspace_id=$1`, workspaceID)
	return err
}

// ReconcileEmployeeTaskArtifacts settles abandoned upload intents, explicit
// deletes and missing task roots. All storage I/O is outside the DB transaction.
func (h *Handler) ReconcileEmployeeTaskArtifacts(ctx context.Context, limit int) (int, error) {
	if h.Storage == nil {
		return 0, nil
	}
	if limit < 1 || limit > 100 {
		return 0, errEmployeeArtifactInvalid
	}
	processed := 0
	for range limit {
		a, err := h.claimEmployeeArtifactCleanup(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return processed, nil
		}
		if err != nil {
			return processed, err
		}
		processed++
		expected := "workspaces/" + a.Binding.WorkspaceID + "/employee-artifacts/" + a.ID + ".enc"
		if a.StorageKey != expected {
			h.releaseEmployeeArtifactCleanup(ctx, a, "invalid_storage_key")
			return processed, errEmployeeArtifactInvalid
		}
		deleteCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = h.Storage.DeleteObject(deleteCtx, a.StorageKey)
		cancel()
		if err != nil {
			h.releaseEmployeeArtifactCleanup(ctx, a, "object_delete_failed")
			return processed, fmt.Errorf("employee artifact object deletion failed: %w", err)
		}
		if err = h.settleEmployeeArtifactCleanup(ctx, a); err != nil {
			return processed, err
		}
	}
	return processed, nil
}
func (h *Handler) claimEmployeeArtifactCleanup(ctx context.Context) (employeeArtifact, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return employeeArtifact{}, err
	}
	defer tx.Rollback(ctx)
	a, err := scanEmployeeArtifact(tx.QueryRow(ctx, `SELECT `+employeeArtifactColumns+` FROM employee_task_artifact f
 WHERE ((f.state IN ('pending','deleting','tombstoned') AND f.next_attempt_at<=now() AND (f.lease_expires_at IS NULL OR f.lease_expires_at<=now()))
 OR (f.state='ready' AND (
  NOT EXISTS(SELECT 1 FROM workspace w WHERE w.id=f.workspace_id)
  OR NOT EXISTS(SELECT 1 FROM attachment a WHERE a.id=f.attachment_id AND a.workspace_id=f.workspace_id AND a.task_id=f.queue_task_id)
  OR NOT EXISTS(SELECT 1 FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id JOIN agent_task_queue q ON q.id=r.queue_task_id
    WHERE r.id=f.run_id AND r.task_id=f.task_id AND r.queue_task_id=f.queue_task_id AND r.workspace_id=f.workspace_id AND r.agent_id=f.agent_id AND r.tenant_org_id=f.tenant_org_id
    AND t.workspace_id=f.workspace_id AND t.agent_id=f.agent_id AND t.tenant_org_id=f.tenant_org_id AND t.scene_id=f.scene_id
    AND q.agent_id=f.agent_id AND q.context->>'type'='employee_direct' AND q.context->>'workspace_id'=f.workspace_id::text AND q.context->>'employee_task_id'=f.task_id::text)
 ))) ORDER BY f.next_attempt_at,f.attachment_id LIMIT 1 FOR UPDATE OF f SKIP LOCKED`))
	if err != nil {
		return a, err
	}
	a.LeaseToken = pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err = tx.Exec(ctx, `UPDATE employee_task_artifact SET state=CASE WHEN state='tombstoned' THEN state ELSE 'deleting' END,lease_token=$2,lease_expires_at=now()+interval '1 minute',attempt_count=attempt_count+1,updated_at=now() WHERE attachment_id=$1::uuid`, a.ID, a.LeaseToken); err != nil {
		return a, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM attachment WHERE id=$1::uuid AND workspace_id=$2::uuid`, a.ID, a.Binding.WorkspaceID); err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}
func (h *Handler) releaseEmployeeArtifactCleanup(ctx context.Context, a employeeArtifact, code string) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = h.DB.Exec(releaseCtx, `UPDATE employee_task_artifact SET lease_token=NULL,lease_expires_at=NULL,last_error=$3,next_attempt_at=now()+interval '30 seconds',updated_at=now() WHERE attachment_id=$1::uuid AND lease_token=$2 AND state IN ('deleting','tombstoned')`, a.ID, a.LeaseToken, code)
}
func (h *Handler) settleEmployeeArtifactCleanup(ctx context.Context, a employeeArtifact) error {
	// Keep tombstones through a widening tail, like the existing channel media
	// reconciler: an abandoned PUT can materialize after its first DELETE.
	delays := []time.Duration{time.Minute, time.Hour, 24 * time.Hour}
	if a.TombstonePass >= len(delays) {
		_, err := h.DB.Exec(ctx, `DELETE FROM employee_task_artifact WHERE attachment_id=$1::uuid AND lease_token=$2 AND state IN ('deleting','tombstoned')`, a.ID, a.LeaseToken)
		return err
	}
	_, err := h.DB.Exec(ctx, `UPDATE employee_task_artifact SET state='tombstoned',tombstone_pass=tombstone_pass+1,lease_token=NULL,lease_expires_at=NULL,last_error='',next_attempt_at=now()+($3::bigint*interval '1 second'),updated_at=now() WHERE attachment_id=$1::uuid AND lease_token=$2 AND state IN ('deleting','tombstoned')`, a.ID, a.LeaseToken, int64(delays[a.TombstonePass]/time.Second))
	return err
}
