package dshprofile

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// PostgresBuildLedger claims bounded work across replicas. External effects must
// occur only after Save commits the preceding intent or receipt boundary.
type PostgresBuildLedger struct{ DB Database }

var ErrBuildCleanupPending = errors.New("DSH plugin build cleanup pending")

// GuardBuildDeletion runs after locking the workspace parent FOR UPDATE. Hold
// every build row until deletion commits: a queued claimant then cannot persist
// an intent and create a cloud resource after its ledger has been deleted.
func GuardBuildDeletion(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID) error {
	rows, err := tx.Query(ctx, `SELECT worker_phase FROM dsh_plugin_build WHERE workspace_id=$1 ORDER BY build_key FOR UPDATE`, workspaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var phase string
		if err := rows.Scan(&phase); err != nil {
			return err
		}
		if phase != "queued" && phase != "done" {
			return ErrBuildCleanupPending
		}
	}
	return rows.Err()
}

func (s PostgresBuildLedger) Claim(ctx context.Context) (BuildJob, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return BuildJob{}, err
	}
	defer tx.Rollback(ctx)
	var job BuildJob
	var intent pgtype.UUID
	var started pgtype.Timestamptz
	job.ClaimID = uuid.New()
	job.Plugin.Config = map[string]any{}
	err = tx.QueryRow(ctx, `WITH candidate AS (
 SELECT workspace_id,build_key FROM dsh_plugin_build
 WHERE worker_phase <> 'done' AND (state='queued' OR worker_phase='cleanup')
 AND next_attempt_at<=now() AND (claim_expires_at IS NULL OR claim_expires_at<=now())
 ORDER BY next_attempt_at,workspace_id,build_key FOR UPDATE SKIP LOCKED LIMIT 1
 ) UPDATE dsh_plugin_build b SET claim_id=$1,claim_expires_at=now()+interval '3 minutes',updated_at=now()
 FROM candidate c WHERE b.workspace_id=c.workspace_id AND b.build_key=c.build_key
 RETURNING b.workspace_id,b.id,b.build_key,b.template_id,b.plugin_id,b.package_name,b.package_version,
 b.package_integrity,b.source_kind,b.source_spec,b.source_artifact_key,b.state,b.worker_phase,b.create_intent,
 b.provider_scope,b.sandbox_id,b.artifact_key,b.build_digest,b.archive_sha256,b.archive_size,
 b.runtime_lock_sha256,b.worker_error,b.worker_started_at`, job.ClaimID).Scan(
		&job.WorkspaceID, &job.BuildID, &job.BuildKey, &job.TemplateID, &job.Plugin.ID, &job.Plugin.PackageName,
		&job.Plugin.Version, &job.Plugin.Integrity, &job.Plugin.SourceKind, &job.Plugin.SourceSpec, &job.Plugin.ArtifactKey,
		&job.State, &job.Phase, &intent, &job.Scope, &job.SandboxID, &job.ArtifactKey, &job.Artifact.BuildDigest,
		&job.Artifact.ArchiveSHA256, &job.Artifact.ArchiveSize, &job.Artifact.RuntimeLockSHA256, &job.ErrorCode, &started)
	if errors.Is(err, pgx.ErrNoRows) {
		return BuildJob{}, ErrNoBuildJob
	}
	if err != nil {
		return BuildJob{}, err
	}
	if intent.Valid {
		job.Intent = uuid.UUID(intent.Bytes)
	}
	if started.Valid {
		job.StartedAt = started.Time
	}
	if err := tx.Commit(ctx); err != nil {
		return BuildJob{}, err
	}
	return job, nil
}

func validBuildTransition(old, next BuildJob) bool {
	if !old.valid() || !next.valid() || old.WorkspaceID != next.WorkspaceID || old.BuildID != next.BuildID ||
		old.BuildKey != next.BuildKey || old.ClaimID != next.ClaimID || old.TemplateID != next.TemplateID ||
		!reflect.DeepEqual(old.Plugin, next.Plugin) {
		return false
	}
	if old.Phase != "queued" && (old.Intent != next.Intent || old.Scope != next.Scope || old.ArtifactKey != next.ArtifactKey || !old.StartedAt.Equal(next.StartedAt)) {
		return false
	}
	if old.Phase != "creating" && old.SandboxID != next.SandboxID {
		return false
	}
	if old.Phase != "building" && old.Artifact != next.Artifact {
		return false
	}
	if next.Phase == "cleanup" && next.State == "failed" {
		return old.State == "queued" && (old.Phase == "starting" || old.Phase == "building" || old.Phase == "publishing") &&
			(next.ErrorCode == "build_failed" || next.ErrorCode == "build_timeout" || next.ErrorCode == "invalid_artifact_receipt" || next.ErrorCode == "artifact_verification_failed")
	}
	if old.ErrorCode != next.ErrorCode {
		return false
	}
	switch old.Phase {
	case "queued":
		return old.State == "queued" && next.State == "queued" && next.Phase == "creating" && next.SandboxID == ""
	case "creating":
		return old.State == "queued" && next.State == "queued" && next.Phase == "starting"
	case "starting":
		return old.State == "queued" && next.State == "queued" && next.Phase == "building"
	case "building":
		return old.State == "queued" && next.State == "queued" && next.Phase == "publishing" && next.Artifact.Valid()
	case "publishing":
		return old.State == "queued" && next.State == "ready" && next.Phase == "cleanup" && next.Artifact.Valid()
	case "cleanup":
		return (old.State == "ready" || old.State == "failed") && old.State == next.State && next.Phase == "done"
	default:
		return false
	}
}

func (s PostgresBuildLedger) Save(ctx context.Context, old, next BuildJob) error {
	if !validBuildTransition(old, next) {
		return errors.New("invalid DSH build transition")
	}
	var updated bool
	err := s.DB.QueryRow(ctx, `UPDATE dsh_plugin_build SET worker_phase=$6,state=$7,create_intent=$8,
 provider_scope=$9,sandbox_id=$10,artifact_key=$11,build_digest=$12,archive_sha256=$13,archive_size=$14,
 runtime_lock_sha256=$15,worker_error=$16,worker_started_at=$17,updated_at=now()
 WHERE workspace_id=$1 AND build_key=$2 AND id=$3 AND claim_id=$4 AND worker_phase=$5
 AND claim_expires_at>now() RETURNING true`, old.WorkspaceID, old.BuildKey, old.BuildID, old.ClaimID, old.Phase,
		next.Phase, next.State, next.Intent, next.Scope, next.SandboxID, next.ArtifactKey, next.Artifact.BuildDigest,
		next.Artifact.ArchiveSHA256, next.Artifact.ArchiveSize, next.Artifact.RuntimeLockSHA256, next.ErrorCode, next.StartedAt).Scan(&updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBuildClaimLost
	}
	return err
}

func (s PostgresBuildLedger) Release(ctx context.Context, job BuildJob, delay time.Duration) error {
	if job.WorkspaceID == uuid.Nil || job.ClaimID == uuid.Nil || delay < 0 || delay > time.Hour {
		return errors.New("invalid DSH build claim release")
	}
	var updated bool
	err := s.DB.QueryRow(ctx, `UPDATE dsh_plugin_build SET claim_id=NULL,claim_expires_at=NULL,
 next_attempt_at=now()+$5*interval '1 second',updated_at=now()
 WHERE workspace_id=$1 AND build_key=$2 AND id=$3 AND claim_id=$4 RETURNING true`,
		job.WorkspaceID, job.BuildKey, job.BuildID, job.ClaimID, delay.Seconds()).Scan(&updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBuildClaimLost
	}
	return err
}
