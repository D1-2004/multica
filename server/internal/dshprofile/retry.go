package dshprofile

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RetryBuild admits one explicit retry of an observed, cleaned failed attempt.
// The caller's build ID is a compare-and-swap token, not a package selector.
// Replaying it cannot retry a subsequent failure or reset a live worker claim.
func (s Store) RetryBuild(ctx context.Context, key dshhost.Key, template string, read ReadSource, revision int64, buildID uuid.UUID) error {
	if s.DB == nil || read == nil || key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || revision < 1 || buildID == uuid.Nil {
		return ErrChanged
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	err = tx.QueryRow(ctx, `SELECT true FROM workspace w JOIN agent a ON a.workspace_id=w.id
 WHERE w.id=$1 AND a.id=$2 AND a.kind='user' AND a.archived_at IS NULL AND a.runtime_mode='cloud'
 FOR SHARE OF w,a`, key.WorkspaceID, key.AgentID).Scan(&exists)
	if err != nil {
		return err
	}
	var desired int64
	err = tx.QueryRow(ctx, `SELECT desired_revision FROM dsh_employee_profile WHERE workspace_id=$1 AND agent_id=$2 FOR UPDATE`, key.WorkspaceID, key.AgentID).Scan(&desired)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && desired != revision) {
		return ErrChanged
	}
	if err != nil {
		return err
	}
	source, err := read(ctx, db.New(tx), key, template)
	if err != nil {
		return err
	}
	_, digest, err := EncodeSource(source)
	if err != nil {
		return err
	}
	var savedDigest, savedTemplate string
	err = tx.QueryRow(ctx, `SELECT source_digest,template_id FROM dsh_profile_revision WHERE workspace_id=$1 AND agent_id=$2 AND revision=$3`, key.WorkspaceID, key.AgentID, revision).Scan(&savedDigest, &savedTemplate)
	if err != nil {
		return err
	}
	if savedDigest != digest || savedTemplate != template || source.TemplateID != template {
		return ErrChanged
	}
	keys := []string{}
	for _, plugin := range source.Plugins {
		if plugin.Enabled {
			keys = append(keys, BuildKey(template, plugin))
		}
	}
	// Only cleanup -> done proves the old sandbox is absent and its potential
	// artifact was removed. Preserve the old intent before replacing the ID;
	// Save and Release also compare this ID, fencing delayed old claimants.
	var updated bool
	err = tx.QueryRow(ctx, `UPDATE dsh_plugin_build SET
 attempt_history=attempt_history || jsonb_build_array(jsonb_build_object(
 'id',id,'create_intent',create_intent,'provider_scope',provider_scope,'sandbox_id',sandbox_id,
 'artifact_key',artifact_key,'build_digest',build_digest,'archive_sha256',archive_sha256,
 'archive_size',archive_size,'runtime_lock_sha256',runtime_lock_sha256,'worker_error',worker_error,
 'worker_started_at',worker_started_at,'completed_at',updated_at,'retried_at',now())),
 id=$5,state='queued',worker_phase='queued',create_intent=NULL,provider_scope='',sandbox_id='',
 artifact_key='',build_digest='',archive_sha256='',archive_size=0,runtime_lock_sha256='',
 worker_error='',worker_started_at=NULL,claim_id=NULL,claim_expires_at=NULL,next_attempt_at=now(),updated_at=now()
 WHERE workspace_id=$1 AND template_id=$2 AND build_key=ANY($3) AND id=$4 AND state='failed' AND worker_phase='done'
 RETURNING true`, key.WorkspaceID, template, keys, buildID, uuid.New()).Scan(&updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrChanged
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
