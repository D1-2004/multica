package dshprofile

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// Delivery contains only immutable object identity. Employee configuration and
// transient download grants do not belong in artifact metadata.
type Delivery struct {
	ArtifactKey string
	Artifact    BuildArtifact
}

func (s Store) Delivery(ctx context.Context, workspace uuid.UUID, template string, build Build) (Delivery, error) {
	var result Delivery
	var intent uuid.UUID
	if s.DB == nil || workspace == uuid.Nil || build.ID == uuid.Nil || build.State != "ready" || !digestPattern.MatchString(build.Key) || !digestPattern.MatchString(build.Digest) {
		return result, ErrPending
	}
	err := s.DB.QueryRow(ctx, `SELECT artifact_key,build_digest,archive_sha256,archive_size,runtime_lock_sha256,create_intent
 FROM dsh_plugin_build WHERE workspace_id=$1 AND id=$2 AND build_key=$3 AND template_id=$4 AND state='ready'`,
		workspace, build.ID, build.Key, template).Scan(&result.ArtifactKey, &result.Artifact.BuildDigest,
		&result.Artifact.ArchiveSHA256, &result.Artifact.ArchiveSize, &result.Artifact.RuntimeLockSHA256, &intent)
	if err != nil {
		return Delivery{}, err
	}
	expected := BuildJob{WorkspaceID: workspace, BuildKey: build.Key, Intent: intent}
	if intent == uuid.Nil || result.ArtifactKey != expected.objectKey() || result.ArtifactKey != build.ArtifactKey || result.Artifact.BuildDigest != build.Digest || !result.Artifact.Valid() {
		return Delivery{}, errors.New("DSH plugin artifact publication changed")
	}
	return result, nil
}
