package dshprofile

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type Database interface {
	Begin(context.Context) (pgx.Tx, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct{ DB Database }

func (s Store) Prepare(ctx context.Context, key dshhost.Key, template string, read ReadSource) (Revision, error) {
	if s.DB == nil || read == nil || key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil {
		return Revision{}, errors.New("employee Profile storage unavailable")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Revision{}, err
	}
	defer tx.Rollback(ctx)
	// There are no foreign keys. Hold both parents until publication commits
	// so workspace deletion cannot leave private revisions behind.
	var parentExists bool
	err = tx.QueryRow(ctx, `SELECT true FROM workspace w JOIN agent a ON a.workspace_id=w.id
 WHERE w.id=$1 AND a.id=$2 AND a.kind='user' AND a.archived_at IS NULL AND a.runtime_mode='cloud'
 FOR SHARE OF w,a`, key.WorkspaceID, key.AgentID).Scan(&parentExists)
	if err != nil {
		return Revision{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO dsh_employee_profile(workspace_id,agent_id) VALUES($1,$2) ON CONFLICT(workspace_id,agent_id) DO NOTHING`, key.WorkspaceID, key.AgentID)
	if err != nil {
		return Revision{}, err
	}
	var current int64
	if err = tx.QueryRow(ctx, `SELECT desired_revision FROM dsh_employee_profile WHERE workspace_id=$1 AND agent_id=$2 FOR UPDATE`, key.WorkspaceID, key.AgentID).Scan(&current); err != nil {
		return Revision{}, err
	}
	source, err := read(ctx, db.New(tx), key, template)
	if err != nil {
		return Revision{}, err
	}
	if source.TemplateID != template {
		return Revision{}, errors.New("employee Profile template mismatch")
	}
	raw, digest, err := EncodeSource(source)
	if err != nil {
		return Revision{}, err
	}
	var revision Revision
	if current > 0 {
		err = tx.QueryRow(ctx, `SELECT revision,source_digest,template_id,descriptor_json,descriptor_digest FROM dsh_profile_revision WHERE workspace_id=$1 AND agent_id=$2 AND revision=$3`, key.WorkspaceID, key.AgentID, current).Scan(&revision.ID, &revision.SourceDigest, &revision.TemplateID, &revision.Descriptor, &revision.Digest)
		if err != nil {
			return Revision{}, err
		}
	}
	if revision.ID == 0 || revision.SourceDigest != digest {
		revision = Revision{SourceDigest: digest, TemplateID: template}
		err = tx.QueryRow(ctx, `INSERT INTO dsh_profile_revision(workspace_id,agent_id,template_id,source_json,source_digest) VALUES($1,$2,$3,$4,$5) RETURNING revision`, key.WorkspaceID, key.AgentID, template, raw, digest).Scan(&revision.ID)
		if err != nil {
			return Revision{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE dsh_employee_profile SET desired_revision=$3,updated_at=now() WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID, revision.ID); err != nil {
			return Revision{}, err
		}
	}
	// All employees acquire shared package rows in the same order.
	sort.Slice(source.Plugins, func(i, j int) bool { return source.Plugins[i].PackageName < source.Plugins[j].PackageName })
	// Build rows contain package provenance only, never employee credentials.
	builds := map[string]Build{}
	for _, plugin := range source.Plugins {
		if !plugin.Enabled {
			continue
		}
		build := Build{Key: BuildKey(template, plugin)}
		_, err = tx.Exec(ctx, `INSERT INTO dsh_plugin_build(workspace_id,build_key,id,plugin_id,template_id,package_name,package_version,package_integrity,source_kind,source_spec,source_artifact_key)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(workspace_id,build_key) DO NOTHING`, key.WorkspaceID, build.Key, uuid.New(), plugin.ID, template, plugin.PackageName, plugin.Version, plugin.Integrity, plugin.SourceKind, plugin.SourceSpec, plugin.ArtifactKey)
		if err != nil {
			return Revision{}, err
		}
		err = tx.QueryRow(ctx, `SELECT id,state,build_digest,artifact_key FROM dsh_plugin_build WHERE workspace_id=$1 AND build_key=$2`, key.WorkspaceID, build.Key).Scan(&build.ID, &build.State, &build.Digest, &build.ArtifactKey)
		if err != nil {
			return Revision{}, err
		}
		builds[build.Key] = build
		revision.Builds = append(revision.Builds, build)
	}
	if revision.Descriptor == "" {
		resolved, resolvedDigest, resolveErr := Resolve(key, revision.ID, source, builds)
		if resolveErr != nil && !errors.Is(resolveErr, ErrPending) {
			return Revision{}, resolveErr
		}
		if resolveErr == nil {
			_, err = tx.Exec(ctx, `UPDATE dsh_profile_revision SET descriptor_json=$4,descriptor_digest=$5 WHERE workspace_id=$1 AND agent_id=$2 AND revision=$3 AND descriptor_json=''`, key.WorkspaceID, key.AgentID, revision.ID, resolved, resolvedDigest)
			if err != nil {
				return Revision{}, err
			}
			revision.Descriptor, revision.Digest = resolved, resolvedDigest
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Revision{}, err
	}
	return revision, nil
}

// Acknowledge is called only after the native supervisor confirms this exact
// descriptor. The database also fences a newer desired revision or generation.
func (s Store) Acknowledge(ctx context.Context, host dshhost.Host, revision Revision, read ReadSource) error {
	if s.DB == nil || read == nil || host.WorkspaceID == uuid.Nil || host.AgentID == uuid.Nil || host.Generation < 1 || host.SandboxID == "" || revision.ID < 1 || !digestPattern.MatchString(revision.Digest) || revision.Descriptor == "" || hash([]byte(revision.Descriptor)) != revision.Digest || revision.TemplateID != host.TemplateID {
		return ErrChanged
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var desired int64
	if err = tx.QueryRow(ctx, `SELECT desired_revision FROM dsh_employee_profile WHERE workspace_id=$1 AND agent_id=$2 FOR UPDATE`, host.WorkspaceID, host.AgentID).Scan(&desired); errors.Is(err, pgx.ErrNoRows) {
		return ErrChanged
	} else if err != nil {
		return err
	}
	if desired != revision.ID {
		return ErrChanged
	}
	// Hold the exact Host generation through commit, including when retirement
	// races this receipt on another replica.
	var running bool
	err = tx.QueryRow(ctx, `SELECT true FROM dsh_employee_host WHERE workspace_id=$1 AND agent_id=$2 AND state='running' AND generation=$3 AND sandbox_id=$4 AND template_id=$5 FOR SHARE`, host.WorkspaceID, host.AgentID, host.Generation, host.SandboxID, host.TemplateID).Scan(&running)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrChanged
	}
	if err != nil {
		return err
	}
	source, err := read(ctx, db.New(tx), host.Key, host.TemplateID)
	if err != nil {
		return err
	}
	_, digest, err := EncodeSource(source)
	if err != nil {
		return err
	}
	if digest != revision.SourceDigest {
		return ErrChanged
	}
	var applied int64
	err = tx.QueryRow(ctx, `UPDATE dsh_employee_profile p SET applied_revision=$3,applied_generation=$4,applied_sandbox_id=$5,applied_at=CASE WHEN applied_revision=$3 AND applied_generation=$4 AND applied_sandbox_id=$5 THEN applied_at ELSE now() END,updated_at=now()
 WHERE p.workspace_id=$1 AND p.agent_id=$2 AND p.desired_revision=$3
 AND EXISTS(SELECT 1 FROM dsh_profile_revision r WHERE r.workspace_id=$1 AND r.agent_id=$2 AND r.revision=$3 AND r.descriptor_digest=$6 AND r.template_id=$7 AND r.source_digest=$8 AND r.descriptor_json<>'')
 AND EXISTS(SELECT 1 FROM dsh_employee_host h WHERE h.workspace_id=$1 AND h.agent_id=$2 AND h.state='running' AND h.generation=$4 AND h.sandbox_id=$5 AND h.template_id=$7)
 RETURNING applied_revision`, host.WorkspaceID, host.AgentID, revision.ID, host.Generation, host.SandboxID, revision.Digest, host.TemplateID, revision.SourceDigest).Scan(&applied)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrChanged
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type Status struct {
	State             string `json:"state"`
	DesiredRevision   string `json:"desired_revision,omitempty"`
	AppliedRevision   string `json:"applied_revision,omitempty"`
	AppliedGeneration int64  `json:"applied_generation"`
	AppliedSandboxID  string `json:"applied_sandbox_id,omitempty"`
	Current           bool   `json:"current"`
	SourceDigest      string `json:"-"`
}

func (s Store) Status(ctx context.Context, key dshhost.Key) (Status, error) {
	value := Status{State: "unprepared"}
	var desired, applied int64
	var descriptor string
	err := s.DB.QueryRow(ctx, `SELECT p.desired_revision,p.applied_revision,p.applied_generation,p.applied_sandbox_id,
 COALESCE(r.source_digest,''),COALESCE(r.descriptor_digest,''),
 COALESCE(p.desired_revision>0 AND p.desired_revision=p.applied_revision AND h.state='running' AND h.generation=p.applied_generation AND h.sandbox_id=p.applied_sandbox_id AND h.template_id=r.template_id,false)
 FROM dsh_employee_profile p LEFT JOIN dsh_profile_revision r ON r.workspace_id=p.workspace_id AND r.agent_id=p.agent_id AND r.revision=p.desired_revision
 LEFT JOIN dsh_employee_host h ON h.workspace_id=p.workspace_id AND h.agent_id=p.agent_id
 WHERE p.workspace_id=$1 AND p.agent_id=$2`, key.WorkspaceID, key.AgentID).Scan(&desired, &applied, &value.AppliedGeneration, &value.AppliedSandboxID, &value.SourceDigest, &descriptor, &value.Current)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, nil
	}
	if err != nil {
		return Status{}, err
	}
	if desired > 0 {
		value.DesiredRevision = strconv.FormatInt(desired, 10)
		value.State = "waiting_for_builds"
		if descriptor != "" {
			value.State = "pending_host"
		}
	}
	if applied > 0 {
		value.AppliedRevision = strconv.FormatInt(applied, 10)
	}
	if value.Current && descriptor != "" {
		value.State = "applied"
	} else {
		value.Current = false
	}
	return value, nil
}

// CompareConfiguration prevents a historical receipt being shown as current
// after saved settings change but before the next explicit preparation/admission.
func (status Status) CompareConfiguration(source Source) (Status, error) {
	_, digest, err := EncodeSource(source)
	if err != nil {
		return Status{}, err
	}
	if status.DesiredRevision != "" && status.SourceDigest != digest {
		status.State = "configuration_changed"
		status.Current = false
	}
	return status, nil
}

func (s Store) Load(ctx context.Context, key dshhost.Key, revision int64) (Revision, error) {
	var result Revision
	err := s.DB.QueryRow(ctx, `SELECT revision,template_id,source_digest,descriptor_json,descriptor_digest FROM dsh_profile_revision WHERE workspace_id=$1 AND agent_id=$2 AND revision=$3`, key.WorkspaceID, key.AgentID, revision).Scan(&result.ID, &result.TemplateID, &result.SourceDigest, &result.Descriptor, &result.Digest)
	return result, err
}

// PrivateSource is for build/recovery workers. It is not an HTTP response type.
func (s Store) PrivateSource(ctx context.Context, key dshhost.Key, revision int64) (Source, error) {
	var raw string
	var source Source
	err := s.DB.QueryRow(ctx, `SELECT source_json FROM dsh_profile_revision WHERE workspace_id=$1 AND agent_id=$2 AND revision=$3`, key.WorkspaceID, key.AgentID, revision).Scan(&raw)
	if err != nil {
		return source, err
	}
	err = json.Unmarshal([]byte(raw), &source)
	return source, err
}
