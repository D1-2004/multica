package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	FCE2BStableReleaseValidating       = "validating"
	FCE2BStableReleaseDeveloperRollout = "developer_rollout"
	FCE2BStableReleaseAwaitingRollout  = "awaiting_rollout"
	FCE2BStableReleaseRollingOut       = "rolling_out"
	FCE2BStableReleaseObserving        = "observing"
	FCE2BStableReleaseCompleted        = "completed"
	FCE2BStableReleasePaused           = "paused"
	FCE2BStableReleaseRollingBack      = "rolling_back"
	FCE2BStableReleaseRolledBack       = "rolled_back"
	FCE2BStableReleaseTerminated       = "terminated"
	FCE2BStableReleaseFailed           = "failed"

	// Candidate validation creates a native sandbox and executes multiple E2B
	// commands. Each command may use the configured sandbox-ready timeout, so
	// the release lease must outlive the complete validation sequence.
	stableReleaseLeaseDuration = 10 * time.Minute
	stableWorkerPollInterval   = 5 * time.Second
)

var (
	ErrFCE2BStableChannelUninitialized = errors.New("FC/E2B stable channel is not initialized")
	ErrFCE2BStableReleaseConflict      = errors.New("another FC/E2B stable release is active")
	ErrFCE2BStableIdempotencyConflict  = errors.New("idempotency key was already used with different release parameters")
	ErrFCE2BStableReleaseState         = errors.New("FC/E2B stable release does not allow this operation")
	ErrFCE2BStableAdvanceBlocked       = errors.New("FC/E2B stable release cannot advance until the current stage passes its gates")
	ErrFCE2BStableObservationBlocked   = errors.New("FC/E2B stable release cannot complete observation until its final gates pass")
)

type FCE2BStableTemplateBinding struct {
	SandboxBackend  string `json:"sandbox_backend"`
	ArtifactKind    string `json:"artifact_kind"`
	ArtifactRef     string `json:"artifact_ref"`
	ArtifactBuildID string `json:"artifact_build_id"`
	ArtifactAlias   string `json:"artifact_alias"`
	ArtifactDigest  string `json:"artifact_digest"`
	TemplateID      string `json:"template_id"`
	TemplateBuildID string `json:"template_build_id"`
	TemplateAlias   string `json:"template_alias"`
	ReleaseID       string `json:"release_id"`
}

type FCE2BStableChannel struct {
	Current       *FCE2BStableTemplateBinding `json:"current"`
	ActiveRelease *FCE2BStableRelease         `json:"active_release"`
}

type FCE2BStableRolloutMilestone struct {
	Batch       int       `json:"batch"`
	Percentage  int       `json:"percentage"`
	ScheduledAt time.Time `json:"scheduled_at"`
	Kind        string    `json:"kind"`
}

type FCE2BStableRelease struct {
	SandboxBackend              string                        `json:"sandbox_backend"`
	ArtifactKind                string                        `json:"artifact_kind"`
	ArtifactRef                 string                        `json:"artifact_ref"`
	ArtifactBuildID             string                        `json:"artifact_build_id"`
	ArtifactAlias               string                        `json:"artifact_alias"`
	ArtifactDigest              string                        `json:"artifact_digest"`
	ID                          string                        `json:"id"`
	TemplateID                  string                        `json:"template_id"`
	TemplateBuildID             string                        `json:"template_build_id"`
	TemplateAlias               string                        `json:"template_alias"`
	GitCommit                   string                        `json:"git_commit"`
	ACRDigest                   string                        `json:"acr_digest"`
	SourceRevision              string                        `json:"source_revision"`
	Note                        string                        `json:"note"`
	ActorUserID                 string                        `json:"actor_user_id"`
	Bootstrap                   bool                          `json:"bootstrap"`
	Status                      string                        `json:"status"`
	CurrentBatch                int                           `json:"current_batch"`
	TargetPercentage            int                           `json:"target_percentage"`
	PreviousTemplateID          string                        `json:"previous_template_id"`
	PreviousTemplateBuildID     string                        `json:"previous_template_build_id"`
	PreviousTemplateAlias       string                        `json:"previous_template_alias"`
	PreviousArtifactRef         string                        `json:"previous_artifact_ref"`
	PreviousArtifactBuildID     string                        `json:"previous_artifact_build_id"`
	PreviousArtifactDigest      string                        `json:"previous_artifact_digest"`
	Manifest                    map[string]any                `json:"manifest,omitempty"`
	TotalTargets                int                           `json:"total_targets"`
	UpdatedTargets              int                           `json:"updated_targets"`
	FailedTargets               int                           `json:"failed_targets"`
	DeveloperTargets            int                           `json:"developer_targets"`
	DeveloperUpdatedTargets     int                           `json:"developer_updated_targets"`
	DeveloperRolloutStartedAt   *time.Time                    `json:"developer_rollout_started_at,omitempty"`
	DeveloperRolloutCompletedAt *time.Time                    `json:"developer_rollout_completed_at,omitempty"`
	RolloutStartedAt            *time.Time                    `json:"rollout_started_at,omitempty"`
	BatchStartedAt              *time.Time                    `json:"batch_started_at,omitempty"`
	NextBatchAt                 *time.Time                    `json:"next_batch_at,omitempty"`
	RolloutSchedule             []FCE2BStableRolloutMilestone `json:"rollout_schedule,omitempty"`
	CompletedAt                 *time.Time                    `json:"completed_at,omitempty"`
	ValidationError             string                        `json:"validation_error,omitempty"`
	CreatedAt                   time.Time                     `json:"created_at"`
	UpdatedAt                   time.Time                     `json:"updated_at"`
}

type CreateFCE2BStableReleaseInput struct {
	IdempotencyKey  string
	SandboxBackend  SandboxBackendKind
	ArtifactRef     string
	ExpectedBuildID string
	ArtifactDigest  string
	GitCommit       string
	TemplateID      string
	Note            string
	ActorUserID     pgtype.UUID
	Bootstrap       bool
}

type stableRuntimeTarget struct {
	RuntimeID               pgtype.UUID
	WorkspaceID             pgtype.UUID
	OwnerID                 pgtype.UUID
	Provider                string
	SandboxBackend          string
	PreviousTemplateID      string
	PreviousTemplateBuildID string
	PreviousTemplateAlias   string
	PreviousArtifactRef     string
	PreviousArtifactBuildID string
	PreviousArtifactDigest  string
	BatchIndex              int
	IsDeveloper             bool
}

type FCE2BStableRuntimeOverview struct {
	RuntimeID                 string    `json:"runtime_id"`
	WorkspaceID               string    `json:"workspace_id"`
	WorkspaceName             string    `json:"workspace_name"`
	RuntimeName               string    `json:"runtime_name"`
	SandboxBackend            string    `json:"sandbox_backend"`
	Provider                  string    `json:"provider"`
	Status                    string    `json:"status"`
	ArtifactChannel           string    `json:"artifact_channel"`
	ArtifactAlias             string    `json:"artifact_alias"`
	ArtifactRef               string    `json:"artifact_ref"`
	ArtifactBuildID           string    `json:"artifact_build_id"`
	ArtifactDigest            string    `json:"artifact_digest"`
	TemplateChannel           string    `json:"template_channel"`
	TemplateAlias             string    `json:"template_alias"`
	TemplateID                string    `json:"template_id"`
	TemplateBuildID           string    `json:"template_build_id"`
	MatchesCurrentStable      bool      `json:"matches_current_stable"`
	MatchesActiveRelease      bool      `json:"matches_active_release"`
	ActiveReleaseTargetStatus string    `json:"active_release_target_status"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

type FCE2BStableService struct {
	Pool             *pgxpool.Pool
	Launcher         *FCE2BLauncher
	ASBLauncher      *ASBLauncher
	DeveloperUserIDs map[string]struct{}
	wake             chan struct{}
}

func NewFCE2BStableService(
	pool *pgxpool.Pool,
	launcher *FCE2BLauncher,
	developerUserIDs map[string]struct{},
	asbLaunchers ...*ASBLauncher,
) *FCE2BStableService {
	developers := make(map[string]struct{}, len(developerUserIDs))
	for userID := range developerUserIDs {
		developers[userID] = struct{}{}
	}
	service := &FCE2BStableService{
		Pool:             pool,
		Launcher:         launcher,
		DeveloperUserIDs: developers,
		wake:             make(chan struct{}, 1),
	}
	if len(asbLaunchers) > 0 {
		service.ASBLauncher = asbLaunchers[0]
	}
	return service
}

func (s *FCE2BStableService) Notify() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *FCE2BStableService) Run(ctx context.Context) {
	if s == nil || s.Pool == nil || s.Launcher == nil {
		return
	}
	ticker := time.NewTicker(stableWorkerPollInterval)
	defer ticker.Stop()
	for {
		if err := s.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("FC/E2B stable release worker failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

func (s *FCE2BStableService) GetChannel(
	ctx context.Context,
	backends ...SandboxBackendKind,
) (FCE2BStableChannel, error) {
	if s == nil || s.Pool == nil {
		return FCE2BStableChannel{}, errors.New("FC/E2B stable channel service is unavailable")
	}
	backend, err := stableSandboxBackend(backends...)
	if err != nil {
		return FCE2BStableChannel{}, err
	}
	var result FCE2BStableChannel
	var current FCE2BStableTemplateBinding
	err = s.Pool.QueryRow(ctx, `
		SELECT sandbox_backend, artifact_kind, current_artifact_ref,
		       current_artifact_build_id, current_template_alias,
		       current_artifact_digest, current_template_id,
		       current_template_build_id, current_template_alias,
		       current_release_id::text
		FROM fc_e2b_stable_channel
		WHERE sandbox_backend = $1 AND channel = 'stable'
	`, backend).Scan(
		&current.SandboxBackend,
		&current.ArtifactKind,
		&current.ArtifactRef,
		&current.ArtifactBuildID,
		&current.ArtifactAlias,
		&current.ArtifactDigest,
		&current.TemplateID,
		&current.TemplateBuildID,
		&current.TemplateAlias,
		&current.ReleaseID,
	)
	switch {
	case err == nil:
		result.Current = &current
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return FCE2BStableChannel{}, fmt.Errorf("load FC/E2B stable channel: %w", err)
	}
	active, err := s.getActiveRelease(ctx, backend)
	if err != nil {
		return FCE2BStableChannel{}, err
	}
	result.ActiveRelease = active
	return result, nil
}

func (s *FCE2BStableService) GetRelease(ctx context.Context, releaseID pgtype.UUID) (FCE2BStableRelease, error) {
	return s.scanRelease(s.Pool.QueryRow(ctx, stableReleaseSelect+` WHERE id = $1`, releaseID))
}

func (s *FCE2BStableService) CurrentTemplate(ctx context.Context) (FCE2BStableTemplateBinding, error) {
	channel, err := s.GetChannel(ctx, SandboxBackendAliyunFC)
	if err != nil {
		return FCE2BStableTemplateBinding{}, err
	}
	if channel.Current == nil {
		return FCE2BStableTemplateBinding{}, ErrFCE2BStableChannelUninitialized
	}
	return *channel.Current, nil
}

func (s *FCE2BStableService) CurrentArtifact(
	ctx context.Context,
	backend SandboxBackendKind,
) (FCE2BStableTemplateBinding, error) {
	channel, err := s.GetChannel(ctx, backend)
	if err != nil {
		return FCE2BStableTemplateBinding{}, err
	}
	if channel.Current == nil {
		return FCE2BStableTemplateBinding{}, ErrFCE2BStableChannelUninitialized
	}
	return *channel.Current, nil
}

func (s *FCE2BStableService) CurrentASBArtifact(ctx context.Context) (ASBArtifact, error) {
	current, err := s.CurrentArtifact(ctx, SandboxBackendASB)
	if err != nil {
		return ASBArtifact{}, err
	}
	return s.loadVerifiedASBArtifact(
		ctx,
		current.ArtifactRef,
		current.ArtifactBuildID,
		current.ArtifactDigest,
	)
}

func (s *FCE2BStableService) ListRuntimeOverview(
	ctx context.Context,
	backends ...SandboxBackendKind,
) ([]FCE2BStableRuntimeOverview, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("FC/E2B stable channel service is unavailable")
	}
	backend, err := stableSandboxBackend(backends...)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT
			runtime.id::text,
			runtime.workspace_id::text,
			workspace.name,
			COALESCE(NULLIF(runtime.custom_name, ''), runtime.name),
			$1::text,
			COALESCE(NULLIF(runtime.provider, ''), 'hermes'),
			runtime.status,
			CASE WHEN COALESCE(
			    runtime.metadata->>'artifact_channel',
			    runtime.metadata->>'template_channel'
			) = 'candidate' THEN 'candidate' ELSE 'stable' END,
			COALESCE(
			    runtime.metadata->>'artifact_alias',
			    runtime.metadata->>'template',
			    ''
			),
			COALESCE(
			    runtime.metadata->>'artifact_ref',
			    runtime.metadata->>'template_id',
			    ''
			),
			COALESCE(
			    runtime.metadata->>'artifact_build_id',
			    runtime.metadata->>'template_build_id',
			    ''
			),
			COALESCE(runtime.metadata->>'artifact_digest', ''),
			CASE WHEN COALESCE(
			    runtime.metadata->>'template_channel',
			    runtime.metadata->>'artifact_channel'
			) = 'candidate' THEN 'candidate' ELSE 'stable' END,
			COALESCE(
			    runtime.metadata->>'template_alias',
			    runtime.metadata->>'template',
			    ''
			),
			COALESCE(runtime.metadata->>'template_id', ''),
			COALESCE(runtime.metadata->>'template_build_id', ''),
			COALESCE(
				COALESCE(runtime.metadata->>'artifact_ref', runtime.metadata->>'template_id') =
				    channel.current_artifact_ref
				AND COALESCE(runtime.metadata->>'artifact_build_id', runtime.metadata->>'template_build_id') =
				    channel.current_artifact_build_id,
				false
			),
			COALESCE(
				COALESCE(runtime.metadata->>'artifact_ref', runtime.metadata->>'template_id') =
				    active.artifact_ref
				AND COALESCE(runtime.metadata->>'artifact_build_id', runtime.metadata->>'template_build_id') =
				    active.artifact_build_id,
				false
			),
			COALESCE(target.status, ''),
			runtime.updated_at
		FROM agent_runtime runtime
		JOIN workspace ON workspace.id = runtime.workspace_id
		LEFT JOIN fc_e2b_stable_channel channel
		  ON channel.sandbox_backend = $1
		 AND channel.channel = 'stable'
		LEFT JOIN fc_e2b_stable_release active ON active.id = channel.active_release_id
		LEFT JOIN fc_e2b_stable_release_target target
		  ON target.release_id = active.id
		 AND target.runtime_id = runtime.id
		 AND target.sandbox_backend = $1
		WHERE runtime.runtime_mode = 'cloud'
		  AND CASE
		        WHEN runtime.metadata->>'kind' = 'fc-e2b' THEN 'aliyun_fc'
		        ELSE runtime.metadata->>'sandbox_backend'
		      END = $1
		ORDER BY lower(workspace.name), lower(COALESCE(NULLIF(runtime.custom_name, ''), runtime.name)), runtime.id
	`, backend)
	if err != nil {
		return nil, fmt.Errorf("list FC/E2B stable runtime overview: %w", err)
	}
	defer rows.Close()
	overview := make([]FCE2BStableRuntimeOverview, 0)
	for rows.Next() {
		var item FCE2BStableRuntimeOverview
		if err := rows.Scan(
			&item.RuntimeID,
			&item.WorkspaceID,
			&item.WorkspaceName,
			&item.RuntimeName,
			&item.SandboxBackend,
			&item.Provider,
			&item.Status,
			&item.ArtifactChannel,
			&item.ArtifactAlias,
			&item.ArtifactRef,
			&item.ArtifactBuildID,
			&item.ArtifactDigest,
			&item.TemplateChannel,
			&item.TemplateAlias,
			&item.TemplateID,
			&item.TemplateBuildID,
			&item.MatchesCurrentStable,
			&item.MatchesActiveRelease,
			&item.ActiveReleaseTargetStatus,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan FC/E2B stable runtime overview: %w", err)
		}
		if item.TemplateChannel == "" {
			item.TemplateChannel = item.ArtifactChannel
		}
		overview = append(overview, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate FC/E2B stable runtime overview: %w", err)
	}
	return overview, nil
}

func (s *FCE2BStableService) CreateRelease(ctx context.Context, input CreateFCE2BStableReleaseInput) (FCE2BStableRelease, bool, error) {
	if s == nil || s.Pool == nil {
		return FCE2BStableRelease{}, false, errors.New("FC/E2B stable channel service is unavailable")
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.TemplateID = strings.TrimSpace(input.TemplateID)
	input.ArtifactRef = strings.TrimSpace(input.ArtifactRef)
	input.ExpectedBuildID = strings.TrimSpace(input.ExpectedBuildID)
	input.ArtifactDigest = strings.TrimSpace(input.ArtifactDigest)
	input.GitCommit = strings.ToLower(strings.TrimSpace(input.GitCommit))
	input.Note = strings.TrimSpace(input.Note)
	backend, err := stableSandboxBackend(input.SandboxBackend)
	if err != nil {
		return FCE2BStableRelease{}, false, err
	}
	input.SandboxBackend = backend
	artifactKind := CloudSandboxArtifactE2BTemplate
	switch backend {
	case SandboxBackendAliyunFC:
		if input.ArtifactRef == "" {
			input.ArtifactRef = input.TemplateID
		}
		if input.TemplateID == "" {
			input.TemplateID = input.ArtifactRef
		}
		if input.IdempotencyKey == "" || input.TemplateID == "" || input.ExpectedBuildID == "" {
			return FCE2BStableRelease{}, false, errors.New("idempotency key, template_id and expected_build_id are required")
		}
	case SandboxBackendASB:
		artifactKind = CloudSandboxArtifactOCIImage
		if input.IdempotencyKey == "" ||
			!cloudSandboxOCIDigestPattern.MatchString(input.ArtifactRef) ||
			input.ExpectedBuildID == "" ||
			!isSHA256Digest(input.ArtifactDigest) ||
			len(input.GitCommit) != 40 ||
			!isLowerHex(input.GitCommit) {
			return FCE2BStableRelease{}, false, errors.New("ASB release requires an immutable artifact_ref, expected_build_id, artifact_digest, and 40-character git_commit")
		}
		if !strings.HasSuffix(input.ArtifactRef, "@"+input.ArtifactDigest) {
			return FCE2BStableRelease{}, false, errors.New("ASB artifact_ref and artifact_digest do not match")
		}
		input.TemplateID = ""
	}
	if !input.ActorUserID.Valid {
		return FCE2BStableRelease{}, false, errors.New("actor user ID is required")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return FCE2BStableRelease{}, false, fmt.Errorf("begin stable release: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var channelExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM fc_e2b_stable_channel
			WHERE sandbox_backend = $1 AND channel = 'stable'
		)
	`, backend).Scan(&channelExists); err != nil {
		return FCE2BStableRelease{}, false, fmt.Errorf("check stable channel: %w", err)
	}
	input.Bootstrap = !channelExists
	fingerprint := stableReleaseFingerprint(input)

	existing, found, err := s.findExistingRelease(ctx, tx, input, fingerprint)
	if err != nil {
		return FCE2BStableRelease{}, false, err
	}
	if found {
		if err := tx.Commit(ctx); err != nil {
			return FCE2BStableRelease{}, false, err
		}
		return existing, false, nil
	}

	var releaseID pgtype.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO fc_e2b_stable_release (
			idempotency_key,
			request_fingerprint,
			template_id,
			template_build_id,
			sandbox_backend,
			artifact_kind,
			artifact_ref,
			artifact_build_id,
			artifact_digest,
			git_commit,
			note,
			actor_user_id,
			bootstrap
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id
	`, input.IdempotencyKey, fingerprint, input.TemplateID,
		func() string {
			if backend == SandboxBackendAliyunFC {
				return input.ExpectedBuildID
			}
			return ""
		}(),
		backend, artifactKind, input.ArtifactRef, input.ExpectedBuildID,
		input.ArtifactDigest, input.GitCommit, input.Note, input.ActorUserID, input.Bootstrap,
	).Scan(&releaseID)
	if err != nil {
		if isUniqueViolation(err) {
			// A concurrent identical request may have committed after the
			// initial lookup. Re-read outside the now-aborted transaction so
			// both callers receive the same persistent release record.
			_ = tx.Rollback(ctx)
			existing, found, lookupErr := s.findExistingRelease(ctx, s.Pool, input, fingerprint)
			if lookupErr != nil {
				return FCE2BStableRelease{}, false, lookupErr
			}
			if found {
				return existing, false, nil
			}
			return FCE2BStableRelease{}, false, ErrFCE2BStableReleaseConflict
		}
		return FCE2BStableRelease{}, false, fmt.Errorf("insert stable release: %w", err)
	}
	if channelExists {
		if _, err := tx.Exec(ctx, `
			UPDATE fc_e2b_stable_channel
			SET active_release_id = $1, updated_at = now()
			WHERE sandbox_backend = $2 AND channel = 'stable'
		`, releaseID, backend); err != nil {
			return FCE2BStableRelease{}, false, fmt.Errorf("set active stable release: %w", err)
		}
	}
	release, err := s.scanRelease(tx.QueryRow(ctx, stableReleaseSelect+` WHERE id = $1`, releaseID))
	if err != nil {
		return FCE2BStableRelease{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FCE2BStableRelease{}, false, fmt.Errorf("commit stable release: %w", err)
	}
	s.Notify()
	return release, true, nil
}

type stableReleaseQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *FCE2BStableService) findExistingRelease(
	ctx context.Context,
	queryer stableReleaseQueryer,
	input CreateFCE2BStableReleaseInput,
	fingerprint string,
) (FCE2BStableRelease, bool, error) {
	existing, err := s.scanRelease(queryer.QueryRow(ctx, stableReleaseSelect+`
		WHERE idempotency_key = $1
	`, input.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return FCE2BStableRelease{}, false, nil
	}
	if err != nil {
		return FCE2BStableRelease{}, false, err
	}
	var storedKey, storedFingerprint string
	if err := queryer.QueryRow(ctx, `
		SELECT idempotency_key, request_fingerprint
		FROM fc_e2b_stable_release
		WHERE id = $1
	`, existing.ID).Scan(&storedKey, &storedFingerprint); err != nil {
		return FCE2BStableRelease{}, false, fmt.Errorf("load stable release fingerprint: %w", err)
	}
	if storedKey == input.IdempotencyKey && storedFingerprint != fingerprint {
		return FCE2BStableRelease{}, false, ErrFCE2BStableIdempotencyConflict
	}
	if storedFingerprint == fingerprint {
		return existing, true, nil
	}
	return FCE2BStableRelease{}, false, ErrFCE2BStableIdempotencyConflict
}

func (s *FCE2BStableService) Pause(ctx context.Context, releaseID pgtype.UUID) (FCE2BStableRelease, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET paused_from_status = status, status = 'paused', lease_token = NULL,
		    lease_expires_at = NULL, updated_at = now()
		WHERE id = $1
		  AND status IN ('validating', 'developer_rollout', 'rolling_out', 'observing')
	`, releaseID)
	if err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("pause stable release: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return FCE2BStableRelease{}, ErrFCE2BStableReleaseState
	}
	return s.GetRelease(ctx, releaseID)
}

func (s *FCE2BStableService) Resume(ctx context.Context, releaseID pgtype.UUID) (FCE2BStableRelease, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("begin stable release resume: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `
		UPDATE fc_e2b_stable_release_target
		SET status = 'pending', last_error = '', lease_token = NULL,
		    lease_expires_at = NULL, updated_at = now()
		WHERE release_id = $1 AND status = 'failed'
	`, releaseID); err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("reset failed stable release targets: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET status = CASE
		        WHEN paused_from_status IN (
		            'validating',
		            'developer_rollout',
		            'rolling_out',
		            'observing'
		        )
		        THEN paused_from_status
		        ELSE 'rolling_out'
		    END,
		    paused_from_status = '',
		    failed_targets = 0,
		    validation_error = '',
		    next_batch_at = LEAST(COALESCE(next_batch_at, now()), now()),
		    updated_at = now()
		WHERE id = $1 AND status = 'paused'
	`, releaseID)
	if err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("resume stable release: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return FCE2BStableRelease{}, ErrFCE2BStableReleaseState
	}
	if err := tx.Commit(ctx); err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("commit stable release resume: %w", err)
	}
	s.Notify()
	return s.GetRelease(ctx, releaseID)
}

func (s *FCE2BStableService) StartRollout(ctx context.Context, releaseID pgtype.UUID) (FCE2BStableRelease, error) {
	startedAt := time.Now()
	tag, err := s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET status = 'rolling_out',
		    current_batch = 1,
		    target_percentage = 5,
		    rollout_started_at = $2,
		    batch_started_at = $2,
		    next_batch_at = $2,
		    validation_error = '',
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $1 AND status = 'awaiting_rollout'
	`, releaseID, startedAt)
	if err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("start 24-hour stable rollout: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return FCE2BStableRelease{}, ErrFCE2BStableReleaseState
	}
	s.Notify()
	return s.GetRelease(ctx, releaseID)
}

func (s *FCE2BStableService) AdvanceRollout(ctx context.Context, releaseID pgtype.UUID) (FCE2BStableRelease, error) {
	token := uuid.New()
	var release FCE2BStableRelease
	var rolloutStartedAt, batchStartedAt pgtype.Timestamptz
	if err := s.Pool.QueryRow(ctx, `
		UPDATE fc_e2b_stable_release
		SET lease_token = $2,
		    lease_expires_at = now() + $3::interval,
		    updated_at = now()
		WHERE id = $1
		  AND status = 'rolling_out'
		  AND current_batch BETWEEN 1 AND 3
		  AND (
		      lease_token IS NULL
		      OR lease_expires_at IS NULL
		      OR lease_expires_at < now()
		  )
		RETURNING id::text, template_alias, status, current_batch,
		          target_percentage, rollout_started_at, batch_started_at
	`, releaseID, token, stableReleaseLeaseDuration.String()).Scan(
		&release.ID,
		&release.TemplateAlias,
		&release.Status,
		&release.CurrentBatch,
		&release.TargetPercentage,
		&rolloutStartedAt,
		&batchStartedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return FCE2BStableRelease{}, fmt.Errorf(
				"%w: the rollout worker is updating the current stage or no next stage is available",
				ErrFCE2BStableReleaseState,
			)
		}
		return FCE2BStableRelease{}, fmt.Errorf("claim stable rollout for advance: %w", err)
	}
	defer func() {
		_, _ = s.Pool.Exec(context.Background(), `
			UPDATE fc_e2b_stable_release
			SET lease_token = NULL, lease_expires_at = NULL, updated_at = now()
			WHERE id = $1 AND lease_token = $2
		`, releaseID, token)
	}()
	if !rolloutStartedAt.Valid || !batchStartedAt.Valid {
		return FCE2BStableRelease{}, fmt.Errorf("%w: rollout timing is incomplete", ErrFCE2BStableReleaseState)
	}
	release.RolloutStartedAt = &rolloutStartedAt.Time
	release.BatchStartedAt = &batchStartedAt.Time

	var pending, failed int
	if err := s.Pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status IN ('pending', 'updating')),
			count(*) FILTER (WHERE status = 'failed')
		FROM fc_e2b_stable_release_target
		WHERE release_id = $1 AND batch_index <= $2
	`, releaseID, release.CurrentBatch).Scan(&pending, &failed); err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("check stable rollout stage targets: %w", err)
	}
	if pending > 0 {
		return FCE2BStableRelease{}, fmt.Errorf(
			"%w: %d runtime targets are still updating",
			ErrFCE2BStableAdvanceBlocked,
			pending,
		)
	}
	if failed > 0 {
		return FCE2BStableRelease{}, fmt.Errorf(
			"%w: %d runtime targets failed",
			ErrFCE2BStableAdvanceBlocked,
			failed,
		)
	}
	if err := s.stableLaunchHealthGate(ctx, release); err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("%w: %v", ErrFCE2BStableAdvanceBlocked, err)
	}

	nextBatch, percentage, _ := stableNextBatch(release.CurrentBatch, rolloutStartedAt.Time)
	if nextBatch == 0 {
		return FCE2BStableRelease{}, ErrFCE2BStableReleaseState
	}
	now := time.Now()
	tag, err := s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET current_batch = $1,
		    target_percentage = $2,
		    batch_started_at = $3,
		    next_batch_at = $3,
		    updated_targets = (
		        SELECT count(*) FROM fc_e2b_stable_release_target
		        WHERE release_id = $4 AND status = 'updated'
		    ),
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $4
		  AND status = 'rolling_out'
		  AND current_batch = $5
		  AND lease_token = $6
	`, nextBatch, percentage, now, releaseID, release.CurrentBatch, token)
	if err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("advance stable rollout stage: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return FCE2BStableRelease{}, ErrFCE2BStableReleaseState
	}
	s.Notify()
	return s.GetRelease(ctx, releaseID)
}

func (s *FCE2BStableService) CompleteObservation(
	ctx context.Context,
	releaseID pgtype.UUID,
) (FCE2BStableRelease, error) {
	token := uuid.New()
	release, err := s.scanRelease(s.Pool.QueryRow(ctx, `
		UPDATE fc_e2b_stable_release release
		SET lease_token = $2,
		    lease_expires_at = now() + $3::interval,
		    updated_at = now()
		WHERE id = $1
		  AND status = 'observing'
		  AND (
		      lease_token IS NULL
		      OR lease_expires_at IS NULL
		      OR lease_expires_at < now()
		  )
		RETURNING `+stableReleaseColumns,
		releaseID,
		token,
		stableReleaseLeaseDuration.String(),
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return FCE2BStableRelease{}, ErrFCE2BStableReleaseState
		}
		return FCE2BStableRelease{}, fmt.Errorf("claim stable observation completion: %w", err)
	}
	defer func() {
		_, _ = s.Pool.Exec(context.Background(), `
			UPDATE fc_e2b_stable_release
			SET lease_token = NULL, lease_expires_at = NULL, updated_at = now()
			WHERE id = $1 AND lease_token = $2
		`, releaseID, token)
	}()

	if err := s.reconcileTargets(ctx, release); err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("reconcile stable observation targets: %w", err)
	}
	var missing, failed int
	if err := s.Pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status <> 'updated'),
			count(*) FILTER (WHERE status = 'failed')
		FROM fc_e2b_stable_release_target
		WHERE release_id = $1
	`, release.ID).Scan(&missing, &failed); err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("check stable observation targets: %w", err)
	}
	if err := stableObservationTargetsError(missing, failed); err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("%w: %v", ErrFCE2BStableObservationBlocked, err)
	}
	if err := s.stableLaunchHealthGate(ctx, release); err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("%w: %v", ErrFCE2BStableObservationBlocked, err)
	}
	completed, err := s.finalizeObservation(ctx, release, token, false)
	if err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("complete stable observation: %w", err)
	}
	if !completed {
		return FCE2BStableRelease{}, ErrFCE2BStableReleaseState
	}
	return s.GetRelease(ctx, releaseID)
}

func (s *FCE2BStableService) Terminate(ctx context.Context, releaseID pgtype.UUID) (FCE2BStableRelease, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("begin stable release termination: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tag, err := tx.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET status = 'terminated',
		    next_batch_at = NULL,
		    completed_at = now(),
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $1
		  AND status IN (
		      'validating',
		      'developer_rollout',
		      'awaiting_rollout',
		      'rolling_out',
		      'observing',
		      'paused'
		  )
	`, releaseID)
	if err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("terminate stable release: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return FCE2BStableRelease{}, ErrFCE2BStableReleaseState
	}
	if _, err := tx.Exec(ctx, `
		UPDATE fc_e2b_stable_channel
		SET active_release_id = NULL, updated_at = now()
		WHERE channel = 'stable' AND active_release_id = $1
	`, releaseID); err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("clear terminated stable release: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("commit stable release termination: %w", err)
	}
	return s.GetRelease(ctx, releaseID)
}

func (s *FCE2BStableService) Rollback(ctx context.Context, releaseID pgtype.UUID) (FCE2BStableRelease, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET status = 'rolling_back', next_batch_at = now(), lease_token = NULL,
		    lease_expires_at = NULL, updated_at = now()
		WHERE id = $1
		  AND bootstrap = false
		  AND (
		      (sandbox_backend = 'aliyun_fc' AND previous_template_id <> '')
		      OR (
		          sandbox_backend = 'asb'
		          AND previous_artifact_ref <> ''
		          AND previous_artifact_build_id <> ''
		          AND previous_artifact_digest <> ''
		      )
		  )
		  AND status IN (
		      'developer_rollout',
		      'awaiting_rollout',
		      'rolling_out',
		      'observing',
		      'paused',
		      'failed'
		  )
	`, releaseID)
	if err != nil {
		return FCE2BStableRelease{}, fmt.Errorf("start stable release rollback: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return FCE2BStableRelease{}, ErrFCE2BStableReleaseState
	}
	s.Notify()
	return s.GetRelease(ctx, releaseID)
}

func (s *FCE2BStableService) runOnce(ctx context.Context) error {
	release, token, err := s.claimRelease(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	switch release.Status {
	case FCE2BStableReleaseValidating:
		return s.validateRelease(ctx, release, token)
	case FCE2BStableReleaseDeveloperRollout:
		return s.developerRolloutRelease(ctx, release, token)
	case FCE2BStableReleaseRollingOut:
		return s.rolloutRelease(ctx, release, token)
	case FCE2BStableReleaseObserving:
		return s.observeRelease(ctx, release, token)
	case FCE2BStableReleaseRollingBack:
		return s.rollbackRelease(ctx, release, token)
	default:
		return nil
	}
}

func (s *FCE2BStableService) claimRelease(ctx context.Context) (FCE2BStableRelease, uuid.UUID, error) {
	token := uuid.New()
	row := s.Pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id
			FROM fc_e2b_stable_release
			WHERE status IN (
			    'validating',
			    'developer_rollout',
			    'rolling_out',
			    'observing',
			    'rolling_back'
			)
			  AND (next_batch_at IS NULL OR next_batch_at <= now())
			  AND (lease_expires_at IS NULL OR lease_expires_at < now())
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE fc_e2b_stable_release release
		SET lease_token = $1,
		    lease_expires_at = now() + $2::interval,
		    updated_at = now()
		FROM candidate
		WHERE release.id = candidate.id
		RETURNING `+stableReleaseColumns, token, stableReleaseLeaseDuration.String())
	release, err := s.scanRelease(row)
	return release, token, err
}

func (s *FCE2BStableService) validateRelease(ctx context.Context, release FCE2BStableRelease, token uuid.UUID) error {
	if release.SandboxBackend == string(SandboxBackendASB) {
		return s.validateASBRelease(ctx, release, token)
	}
	templates, err := ListFCE2BTemplates(ctx, s.Launcher.Config, s.Launcher.Runner)
	if err != nil {
		return s.failValidation(ctx, release.ID, token, err)
	}
	var selected FCE2BTemplate
	found := false
	for _, template := range templates {
		if template.ID == release.TemplateID && template.BuildID == release.TemplateBuildID {
			selected = template
			found = true
			break
		}
	}
	if !found {
		return s.failValidation(ctx, release.ID, token, errors.New("template_id and expected_build_id do not match a READY catalog entry"))
	}
	if !IsFCE2BTemplateReady(selected) || !IsFCE2BTemplatePublished(selected) {
		return s.failValidation(ctx, release.ID, token, errors.New("candidate template is not READY with a published manifest"))
	}
	if !isStableSourceRevision(selected.SourceRevision) {
		return s.failValidation(ctx, release.ID, token, errors.New("candidate template has no valid source revision"))
	}
	manifest, err := s.Launcher.VerifyStableTemplate(ctx, selected)
	if err != nil {
		return s.failValidation(ctx, release.ID, token, err)
	}
	manifest["source_revision"] = selected.SourceRevision
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return s.failValidation(ctx, release.ID, token, err)
	}
	if release.Bootstrap {
		_, err = s.Pool.Exec(ctx, `
			WITH completed AS (
				UPDATE fc_e2b_stable_release
				SET template_alias = $1,
				    manifest = $2::jsonb,
				    source_revision = $3,
				    status = 'completed',
				    completed_at = now(),
				    lease_token = NULL,
				    lease_expires_at = NULL,
				    updated_at = now()
				WHERE id = $4 AND lease_token = $5
				RETURNING id
			)
			INSERT INTO fc_e2b_stable_channel (
				sandbox_backend,
				channel,
				artifact_kind,
				current_artifact_ref,
				current_artifact_build_id,
				current_artifact_digest,
				current_template_id,
				current_template_build_id,
				current_template_alias,
				current_release_id,
				active_release_id
			)
			SELECT 'aliyun_fc', 'stable', 'e2b_template',
			       $6, $7, $8, $6, $7, $1, id, NULL
			FROM completed
		`, selected.Template, string(manifestJSON), selected.SourceRevision,
			release.ID, token, selected.ID, selected.BuildID, release.ArtifactDigest)
		return err
	}
	current, err := s.CurrentTemplate(ctx)
	if err != nil {
		return s.failValidation(ctx, release.ID, token, err)
	}
	targets, err := s.listStableRuntimes(ctx, release.ID)
	if err != nil {
		return s.failValidation(ctx, release.ID, token, err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, target := range targets {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fc_e2b_stable_release_target (
				release_id,
				runtime_id,
				workspace_id,
				provider,
				batch_index,
				is_developer,
				previous_template_id,
				previous_template_build_id,
				previous_template_alias,
				sandbox_backend,
				previous_artifact_ref,
				previous_artifact_build_id,
				previous_artifact_digest
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (release_id, runtime_id) DO NOTHING
		`, release.ID, target.RuntimeID, target.WorkspaceID, target.Provider, target.BatchIndex, target.IsDeveloper,
			target.PreviousTemplateID, target.PreviousTemplateBuildID, target.PreviousTemplateAlias,
			release.SandboxBackend, target.PreviousArtifactRef,
			target.PreviousArtifactBuildID, target.PreviousArtifactDigest,
		); err != nil {
			return err
		}
	}
	startedAt := time.Now()
	_, err = tx.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET template_alias = $1,
		    manifest = $2::jsonb,
		    source_revision = $3,
		    previous_template_id = $4,
		    previous_template_build_id = $5,
		    previous_template_alias = $6,
		    previous_artifact_ref = $7,
		    previous_artifact_build_id = $8,
		    previous_artifact_digest = $9,
		    status = 'developer_rollout',
		    current_batch = 0,
		    target_percentage = 0,
		    total_targets = $10,
		    developer_rollout_started_at = $11,
		    rollout_started_at = NULL,
		    batch_started_at = NULL,
		    next_batch_at = $11,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $12 AND lease_token = $13
	`, selected.Template, string(manifestJSON), selected.SourceRevision, current.TemplateID,
		current.TemplateBuildID, current.TemplateAlias, current.ArtifactRef,
		current.ArtifactBuildID, current.ArtifactDigest, len(targets), startedAt,
		release.ID, token)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.Notify()
	return nil
}

func (s *FCE2BStableService) validateASBRelease(
	ctx context.Context,
	release FCE2BStableRelease,
	token uuid.UUID,
) error {
	if s.ASBLauncher == nil {
		return s.failValidation(ctx, release.ID, token, errors.New("ASB stable validation launcher is unavailable"))
	}
	artifact := releaseASBArtifact(release)
	if err := validateASBArtifact(artifact); err != nil {
		return s.failValidation(ctx, release.ID, token, err)
	}
	manifest, err := s.ASBLauncher.VerifyStableArtifact(ctx, artifact)
	if err != nil {
		return s.failValidation(ctx, release.ID, token, err)
	}
	if len(release.GitCommit) != 40 || !isLowerHex(release.GitCommit) {
		return s.failValidation(ctx, release.ID, token, errors.New("ASB release has no valid source commit"))
	}
	sourceRevision := release.GitCommit[:6]
	manifest["source_revision"] = release.GitCommit
	manifest["artifact_digest"] = release.ArtifactDigest
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return s.failValidation(ctx, release.ID, token, err)
	}
	alias := asbArtifactAlias(release.ArtifactRef, release.ArtifactBuildID)
	if release.Bootstrap {
		_, err = s.Pool.Exec(ctx, `
			WITH completed AS (
				UPDATE fc_e2b_stable_release
				SET template_alias = $1,
				    manifest = $2::jsonb,
				    source_revision = $3,
				    status = 'completed',
				    completed_at = now(),
				    lease_token = NULL,
				    lease_expires_at = NULL,
				    updated_at = now()
				WHERE id = $4 AND lease_token = $5
				RETURNING id
			)
			INSERT INTO fc_e2b_stable_channel (
				sandbox_backend,
				channel,
				artifact_kind,
				current_artifact_ref,
				current_artifact_build_id,
				current_artifact_digest,
				current_template_id,
				current_template_build_id,
				current_template_alias,
				current_release_id,
				active_release_id
			)
			SELECT 'asb', 'stable', 'oci_image', $6, $7, $8,
			       '', '', $1, id, NULL
			FROM completed
		`, alias, string(manifestJSON), sourceRevision, release.ID, token,
			release.ArtifactRef, release.ArtifactBuildID, release.ArtifactDigest)
		return err
	}
	current, err := s.CurrentArtifact(ctx, SandboxBackendASB)
	if err != nil {
		return s.failValidation(ctx, release.ID, token, err)
	}
	targets, err := s.listStableRuntimes(ctx, release.ID, SandboxBackendASB)
	if err != nil {
		return s.failValidation(ctx, release.ID, token, err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, target := range targets {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fc_e2b_stable_release_target (
				release_id,
				runtime_id,
				workspace_id,
				provider,
				batch_index,
				is_developer,
				previous_template_id,
				previous_template_build_id,
				previous_template_alias,
				sandbox_backend,
				previous_artifact_ref,
				previous_artifact_build_id,
				previous_artifact_digest
			) VALUES ($1, $2, $3, $4, $5, $6, '', '', $7, 'asb', $8, $9, $10)
			ON CONFLICT (release_id, runtime_id) DO NOTHING
		`, release.ID, target.RuntimeID, target.WorkspaceID, target.Provider,
			target.BatchIndex, target.IsDeveloper, target.PreviousTemplateAlias,
			target.PreviousArtifactRef, target.PreviousArtifactBuildID,
			target.PreviousArtifactDigest,
		); err != nil {
			return err
		}
	}
	startedAt := time.Now()
	if _, err := tx.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET template_alias = $1,
		    manifest = $2::jsonb,
		    source_revision = $3,
		    previous_artifact_ref = $4,
		    previous_artifact_build_id = $5,
		    previous_artifact_digest = $6,
		    previous_template_alias = $7,
		    status = 'developer_rollout',
		    current_batch = 0,
		    target_percentage = 0,
		    total_targets = $8,
		    developer_rollout_started_at = $9,
		    rollout_started_at = NULL,
		    batch_started_at = NULL,
		    next_batch_at = $9,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $10 AND lease_token = $11
	`, alias, string(manifestJSON), sourceRevision, current.ArtifactRef,
		current.ArtifactBuildID, current.ArtifactDigest, current.ArtifactAlias,
		len(targets), startedAt, release.ID, token); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.Notify()
	return nil
}

func (s *FCE2BStableService) updateRuntimeForStableRelease(
	ctx context.Context,
	release FCE2BStableRelease,
	runtimeID pgtype.UUID,
	batchIndex int,
) (db.AgentRuntime, error) {
	switch SandboxBackendKind(release.SandboxBackend) {
	case SandboxBackendAliyunFC:
		if s.Launcher == nil {
			return db.AgentRuntime{}, errors.New("FC/E2B stable release launcher is unavailable")
		}
		result, err := s.Launcher.UpdateRuntimeTemplateForStableRelease(
			ctx,
			runtimeID,
			releaseTemplate(release),
			release.ID,
			batchIndex,
		)
		return result.Runtime, err
	case SandboxBackendASB:
		if s.ASBLauncher == nil {
			return db.AgentRuntime{}, errors.New("ASB stable release launcher is unavailable")
		}
		result, err := s.ASBLauncher.UpdateRuntimeArtifactForStableRelease(
			ctx,
			runtimeID,
			releaseASBArtifact(release),
			release.ID,
			batchIndex,
		)
		return result.Runtime, err
	default:
		return db.AgentRuntime{}, errors.New("stable release has an unsupported sandbox backend")
	}
}

func (s *FCE2BStableService) developerRolloutRelease(
	ctx context.Context,
	release FCE2BStableRelease,
	token uuid.UUID,
) error {
	if err := s.reconcileTargets(ctx, release); err != nil {
		return s.releaseLease(ctx, release.ID, token, err)
	}
	target, err := s.claimTarget(ctx, release.ID, 0, true)
	if err == nil {
		runtime, updateErr := s.updateRuntimeForStableRelease(ctx, release, target.RuntimeID, 0)
		if updateErr != nil {
			return s.failTarget(ctx, release.ID, target.RuntimeID, token, updateErr)
		}
		if !runtimeUsesStableRelease(runtime, release) {
			return s.failTarget(
				ctx,
				release.ID,
				target.RuntimeID,
				token,
				errors.New("developer runtime artifact readback does not match the stable release"),
			)
		}
		if _, err := s.Pool.Exec(ctx, `
			UPDATE fc_e2b_stable_release_target
			SET status = 'updated', completed_at = now(), lease_token = NULL,
			    lease_expires_at = NULL, last_error = '', updated_at = now()
			WHERE release_id = $1 AND runtime_id = $2
		`, release.ID, target.RuntimeID); err != nil {
			return err
		}
		return s.releaseLease(ctx, release.ID, token, nil)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return s.releaseLease(ctx, release.ID, token, err)
	}

	var pending, failed int
	if err := s.Pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status IN ('pending', 'updating')),
			count(*) FILTER (WHERE status = 'failed')
		FROM fc_e2b_stable_release_target
		WHERE release_id = $1 AND is_developer = true
	`, release.ID).Scan(&pending, &failed); err != nil {
		return s.releaseLease(ctx, release.ID, token, err)
	}
	if failed > 0 {
		_, err := s.Pool.Exec(ctx, `
			UPDATE fc_e2b_stable_release
			SET status = 'paused', paused_from_status = 'developer_rollout',
			    failed_targets = $1, lease_token = NULL, lease_expires_at = NULL,
			    updated_at = now()
			WHERE id = $2 AND lease_token = $3
		`, failed, release.ID, token)
		return err
	}
	if pending > 0 {
		return s.releaseLease(ctx, release.ID, token, nil)
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET status = 'awaiting_rollout',
		    developer_rollout_completed_at = now(),
		    updated_targets = (
		        SELECT count(*) FROM fc_e2b_stable_release_target
		        WHERE release_id = $1 AND status = 'updated'
		    ),
		    next_batch_at = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $1 AND lease_token = $2
	`, release.ID, token)
	return err
}

func (s *FCE2BStableService) rolloutRelease(ctx context.Context, release FCE2BStableRelease, token uuid.UUID) error {
	if err := s.reconcileTargets(ctx, release); err != nil {
		return s.releaseLease(ctx, release.ID, token, err)
	}
	target, err := s.claimTarget(ctx, release.ID, release.CurrentBatch, false)
	if err == nil {
		runtime, updateErr := s.updateRuntimeForStableRelease(
			ctx,
			release,
			target.RuntimeID,
			target.BatchIndex,
		)
		if updateErr != nil {
			return s.failTarget(ctx, release.ID, target.RuntimeID, token, updateErr)
		}
		if !runtimeUsesStableRelease(runtime, release) {
			return s.failTarget(
				ctx,
				release.ID,
				target.RuntimeID,
				token,
				errors.New("runtime artifact readback does not match the stable release"),
			)
		}
		if _, err := s.Pool.Exec(ctx, `
			UPDATE fc_e2b_stable_release_target
			SET status = 'updated', completed_at = now(), lease_token = NULL,
			    lease_expires_at = NULL, last_error = '', updated_at = now()
			WHERE release_id = $1 AND runtime_id = $2
		`, release.ID, target.RuntimeID); err != nil {
			return err
		}
		return s.releaseLease(ctx, release.ID, token, nil)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return s.releaseLease(ctx, release.ID, token, err)
	}

	var pending, failed int
	if err := s.Pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status IN ('pending', 'updating')),
			count(*) FILTER (WHERE status = 'failed')
		FROM fc_e2b_stable_release_target
		WHERE release_id = $1 AND batch_index <= $2
	`, release.ID, release.CurrentBatch).Scan(&pending, &failed); err != nil {
		return s.releaseLease(ctx, release.ID, token, err)
	}
	if failed > 0 {
		_, err := s.Pool.Exec(ctx, `
			UPDATE fc_e2b_stable_release
			SET status = 'paused', paused_from_status = 'rolling_out',
			    failed_targets = $1, lease_token = NULL, lease_expires_at = NULL,
			    updated_at = now()
			WHERE id = $2 AND lease_token = $3
		`, failed, release.ID, token)
		return err
	}
	if pending > 0 {
		return s.releaseLease(ctx, release.ID, token, nil)
	}

	nextBatch, percentage, due := stableNextBatch(release.CurrentBatch, *release.RolloutStartedAt)
	if nextBatch == 0 {
		_, err := s.Pool.Exec(ctx, `
			UPDATE fc_e2b_stable_release
			SET status = 'observing',
			    updated_targets = total_targets,
			    next_batch_at = rollout_started_at + interval '24 hours',
			    lease_token = NULL,
			    lease_expires_at = NULL,
			    updated_at = now()
			WHERE id = $1 AND lease_token = $2
		`, release.ID, token)
		return err
	}
	if time.Now().Before(due) {
		_, err := s.Pool.Exec(ctx, `
			UPDATE fc_e2b_stable_release
			SET updated_targets = (
			        SELECT count(*) FROM fc_e2b_stable_release_target
			        WHERE release_id = $1 AND status = 'updated'
			    ),
			    next_batch_at = $2,
			    lease_token = NULL,
			    lease_expires_at = NULL,
			    updated_at = now()
			WHERE id = $1 AND lease_token = $3
		`, release.ID, due, token)
		return err
	}
	if err := s.stableLaunchHealthGate(ctx, release); err != nil {
		return s.pauseForGate(ctx, release.ID, token, FCE2BStableReleaseRollingOut, err)
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET current_batch = $1,
		    target_percentage = $2,
		    batch_started_at = now(),
		    updated_targets = (
		        SELECT count(*) FROM fc_e2b_stable_release_target
		        WHERE release_id = $3 AND status = 'updated'
		    ),
		    next_batch_at = $4,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $3 AND lease_token = $5
	`, nextBatch, percentage, due, release.ID, token)
	return err
}

func (s *FCE2BStableService) observeRelease(ctx context.Context, release FCE2BStableRelease, token uuid.UUID) error {
	if err := s.reconcileTargets(ctx, release); err != nil {
		return s.releaseLease(ctx, release.ID, token, err)
	}
	var missing int
	if err := s.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM fc_e2b_stable_release_target
		WHERE release_id = $1 AND status <> 'updated'
	`, release.ID).Scan(&missing); err != nil {
		return s.releaseLease(ctx, release.ID, token, err)
	}
	if missing > 0 {
		_, err := s.Pool.Exec(ctx, `
			UPDATE fc_e2b_stable_release
			SET status = 'rolling_out', current_batch = 4, target_percentage = 100,
			    next_batch_at = now(), lease_token = NULL, lease_expires_at = NULL,
			    updated_at = now()
			WHERE id = $1 AND lease_token = $2
		`, release.ID, token)
		return err
	}
	if err := s.stableLaunchHealthGate(ctx, release); err != nil {
		return s.pauseForGate(ctx, release.ID, token, FCE2BStableReleaseObserving, err)
	}
	completed, err := s.finalizeObservation(ctx, release, token, true)
	if err != nil {
		return err
	}
	if !completed {
		return s.releaseLease(ctx, release.ID, token, nil)
	}
	return nil
}

func (s *FCE2BStableService) finalizeObservation(
	ctx context.Context,
	release FCE2BStableRelease,
	token uuid.UUID,
	requireScheduledTime bool,
) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tag, err := tx.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET status = 'completed', completed_at = now(), updated_targets = total_targets,
		    next_batch_at = NULL, validation_error = '',
		    lease_token = NULL, lease_expires_at = NULL, updated_at = now()
		WHERE id = $1
		  AND status = 'observing'
		  AND lease_token = $2
		  AND ($3 = false OR rollout_started_at + interval '24 hours' <= now())
	`, release.ID, token, requireScheduledTime)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	channelTag, err := tx.Exec(ctx, `
		UPDATE fc_e2b_stable_channel
		SET artifact_kind = $1,
		    current_artifact_ref = $2,
		    current_artifact_build_id = $3,
		    current_artifact_digest = $4,
		    current_template_id = $5,
		    current_template_build_id = $6,
		    current_template_alias = $7,
		    current_release_id = $8,
		    active_release_id = NULL,
		    updated_at = now()
		WHERE sandbox_backend = $9
		  AND channel = 'stable'
		  AND active_release_id = $8
	`, release.ArtifactKind, release.ArtifactRef, release.ArtifactBuildID,
		release.ArtifactDigest, release.TemplateID, release.TemplateBuildID,
		release.ArtifactAlias, release.ID, release.SandboxBackend)
	if err != nil {
		return false, err
	}
	if channelTag.RowsAffected() != 1 {
		return false, errors.New("stable channel no longer points to the observed release")
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *FCE2BStableService) rollbackRelease(ctx context.Context, release FCE2BStableRelease, token uuid.UUID) error {
	target, err := s.claimRollbackTarget(ctx, release.ID)
	if err == nil {
		if release.SandboxBackend == string(SandboxBackendASB) {
			previous, artifactErr := s.loadVerifiedASBArtifact(
				ctx,
				target.PreviousArtifactRef,
				target.PreviousArtifactBuildID,
				target.PreviousArtifactDigest,
			)
			if artifactErr != nil {
				return s.failTarget(ctx, release.ID, target.RuntimeID, token, artifactErr)
			}
			if s.ASBLauncher == nil {
				return s.failTarget(
					ctx,
					release.ID,
					target.RuntimeID,
					token,
					errors.New("ASB stable rollback launcher is unavailable"),
				)
			}
			result, updateErr := s.ASBLauncher.UpdateRuntimeArtifactForStableRelease(
				ctx,
				target.RuntimeID,
				previous,
				release.ID,
				0,
			)
			if updateErr != nil {
				return s.failTarget(ctx, release.ID, target.RuntimeID, token, updateErr)
			}
			if !runtimeUsesArtifact(
				result.Runtime,
				previous.Ref,
				previous.BuildID,
				previous.Digest,
			) {
				return s.failTarget(
					ctx,
					release.ID,
					target.RuntimeID,
					token,
					errors.New("runtime artifact readback does not match the rollback artifact"),
				)
			}
			return s.completeRollbackTarget(ctx, release.ID, target.RuntimeID, token)
		}
		previous := FCE2BTemplate{
			ID:                release.PreviousTemplateID,
			BuildID:           release.PreviousTemplateBuildID,
			Name:              release.PreviousTemplateAlias,
			Template:          release.PreviousTemplateAlias,
			Status:            "READY",
			ManifestVersion:   2,
			Providers:         []string{"hermes", "opencode", "pi"},
			Capabilities:      []string{"dws", "dws.im_event", "mcp"},
			ComponentVersions: releaseComponentVersions(release.Manifest),
			RunnerProtocol:    string(fcE2BRunnerLaunchRootLog),
		}
		templates, listErr := ListFCE2BTemplates(ctx, s.Launcher.Config, s.Launcher.Runner)
		if listErr != nil {
			return s.releaseLease(ctx, release.ID, token, listErr)
		}
		for _, candidate := range templates {
			if candidate.ID == previous.ID && candidate.BuildID == previous.BuildID {
				previous = candidate
				break
			}
		}
		if previous.SourceRevision == "" {
			return s.failTarget(ctx, release.ID, target.RuntimeID, token, errors.New("previous stable template is no longer in the verified template catalog"))
		}
		result, updateErr := s.Launcher.UpdateRuntimeTemplateForStableRelease(ctx, target.RuntimeID, previous, release.ID, 0)
		if updateErr != nil {
			return s.failTarget(ctx, release.ID, target.RuntimeID, token, updateErr)
		}
		if !runtimeUsesTemplate(result.Runtime, previous.ID, previous.BuildID) {
			return s.failTarget(
				ctx,
				release.ID,
				target.RuntimeID,
				token,
				errors.New("runtime template readback does not match the rollback template"),
			)
		}
		return s.completeRollbackTarget(ctx, release.ID, target.RuntimeID, token)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return s.releaseLease(ctx, release.ID, token, err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET status = 'rolled_back', completed_at = now(), lease_token = NULL,
		    lease_expires_at = NULL, updated_at = now()
		WHERE id = $1 AND lease_token = $2
	`, release.ID, token); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE fc_e2b_stable_channel
		SET active_release_id = NULL, updated_at = now()
		WHERE channel = 'stable' AND active_release_id = $1
	`, release.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *FCE2BStableService) loadVerifiedASBArtifact(
	ctx context.Context,
	ref string,
	buildID string,
	digest string,
) (ASBArtifact, error) {
	ref = strings.TrimSpace(ref)
	buildID = strings.TrimSpace(buildID)
	digest = strings.TrimSpace(digest)
	var artifact ASBArtifact
	var manifestJSON []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT artifact_ref, artifact_build_id, template_alias,
		       artifact_digest, manifest
		FROM fc_e2b_stable_release
		WHERE sandbox_backend = 'asb'
		  AND artifact_ref = $1
		  AND artifact_build_id = $2
		  AND artifact_digest = $3
		  AND status = 'completed'
		ORDER BY completed_at DESC
		LIMIT 1
	`, ref, buildID, digest).Scan(
		&artifact.Ref,
		&artifact.BuildID,
		&artifact.Alias,
		&artifact.Digest,
		&manifestJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ASBArtifact{}, errors.New("previous stable ASB artifact is no longer in the verified release catalog")
	}
	if err != nil {
		return ASBArtifact{}, fmt.Errorf("load previous stable ASB artifact: %w", err)
	}
	if err := json.Unmarshal(manifestJSON, &artifact.Manifest); err != nil {
		return ASBArtifact{}, errors.New("decode previous stable ASB artifact manifest")
	}
	if err := validateASBArtifact(artifact); err != nil {
		return ASBArtifact{}, err
	}
	if err := validateASBRuntimeManifest(artifact.Manifest); err != nil {
		return ASBArtifact{}, err
	}
	return artifact, nil
}

func (s *FCE2BStableService) completeRollbackTarget(
	ctx context.Context,
	releaseID string,
	runtimeID pgtype.UUID,
	token uuid.UUID,
) error {
	if _, err := s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_release_target
		SET status = 'rolled_back', completed_at = now(), lease_token = NULL,
		    lease_expires_at = NULL, last_error = '', updated_at = now()
		WHERE release_id = $1 AND runtime_id = $2
	`, releaseID, runtimeID); err != nil {
		return err
	}
	return s.releaseLease(ctx, releaseID, token, nil)
}

func (s *FCE2BStableService) reconcileTargets(ctx context.Context, release FCE2BStableRelease) error {
	targets, err := s.listStableRuntimes(ctx, release.ID, SandboxBackendKind(release.SandboxBackend))
	if err != nil {
		return err
	}
	for _, target := range targets {
		batchIndex := target.BatchIndex
		if batchIndex < release.CurrentBatch {
			batchIndex = release.CurrentBatch
		}
		if _, err := s.Pool.Exec(ctx, `
			INSERT INTO fc_e2b_stable_release_target (
				release_id, runtime_id, workspace_id, provider, batch_index,
				is_developer, previous_template_id, previous_template_build_id,
				previous_template_alias, sandbox_backend, previous_artifact_ref,
				previous_artifact_build_id, previous_artifact_digest
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (release_id, runtime_id) DO UPDATE
			SET is_developer = EXCLUDED.is_developer,
			    updated_at = now()
		`, release.ID, target.RuntimeID, target.WorkspaceID, target.Provider, batchIndex, target.IsDeveloper,
			target.PreviousTemplateID, target.PreviousTemplateBuildID, target.PreviousTemplateAlias,
			release.SandboxBackend, target.PreviousArtifactRef,
			target.PreviousArtifactBuildID, target.PreviousArtifactDigest,
		); err != nil {
			return err
		}
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET total_targets = (
		    SELECT count(*) FROM fc_e2b_stable_release_target WHERE release_id = $1
		),
		updated_targets = (
		    SELECT count(*) FROM fc_e2b_stable_release_target
		    WHERE release_id = $1 AND status = 'updated'
		),
		updated_at = now()
		WHERE id = $1
	`, release.ID)
	return err
}

func (s *FCE2BStableService) listStableRuntimes(
	ctx context.Context,
	releaseID string,
	backends ...SandboxBackendKind,
) ([]stableRuntimeTarget, error) {
	backend, err := stableSandboxBackend(backends...)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, workspace_id, owner_id, provider, metadata
		FROM agent_runtime
		WHERE runtime_mode = 'cloud'
		  AND CASE
		        WHEN metadata->>'kind' = 'fc-e2b' THEN 'aliyun_fc'
		        ELSE metadata->>'sandbox_backend'
		      END = $1
		  AND COALESCE(
		        NULLIF(metadata->>'artifact_channel', ''),
		        NULLIF(metadata->>'template_channel', ''),
		        'stable'
		      ) = 'stable'
		ORDER BY id
	`, backend)
	if err != nil {
		return nil, fmt.Errorf("list stable FC/E2B runtimes: %w", err)
	}
	defer rows.Close()
	var targets []stableRuntimeTarget
	for rows.Next() {
		var target stableRuntimeTarget
		var metadataJSON []byte
		if err := rows.Scan(
			&target.RuntimeID,
			&target.WorkspaceID,
			&target.OwnerID,
			&target.Provider,
			&metadataJSON,
		); err != nil {
			return nil, err
		}
		target.IsDeveloper = stableRuntimeOwnedByDeveloper(target.OwnerID, s.DeveloperUserIDs)
		rawProvider := target.Provider
		target.Provider = stableRuntimeProvider(rawProvider)
		if target.Provider == "" {
			return nil, fmt.Errorf(
				"stable FC/E2B runtime %s has unsupported provider %q",
				util.UUIDToString(target.RuntimeID),
				rawProvider,
			)
		}
		var metadata struct {
			Template        string `json:"template"`
			TemplateID      string `json:"template_id"`
			TemplateBuildID string `json:"template_build_id"`
			ArtifactAlias   string `json:"artifact_alias"`
			ArtifactRef     string `json:"artifact_ref"`
			ArtifactBuildID string `json:"artifact_build_id"`
			ArtifactDigest  string `json:"artifact_digest"`
		}
		if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
			return nil, fmt.Errorf("decode stable runtime metadata: %w", err)
		}
		target.PreviousTemplateID = metadata.TemplateID
		target.PreviousTemplateBuildID = metadata.TemplateBuildID
		target.PreviousTemplateAlias = metadata.Template
		target.PreviousArtifactRef = firstNonEmptyString(metadata.ArtifactRef, metadata.TemplateID)
		target.PreviousArtifactBuildID = firstNonEmptyString(metadata.ArtifactBuildID, metadata.TemplateBuildID)
		target.PreviousArtifactDigest = metadata.ArtifactDigest
		if target.PreviousTemplateAlias == "" {
			target.PreviousTemplateAlias = metadata.ArtifactAlias
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	assignStableBatches(releaseID, targets)
	return targets, nil
}

func stableRuntimeOwnedByDeveloper(ownerID pgtype.UUID, developerUserIDs map[string]struct{}) bool {
	if !ownerID.Valid {
		return false
	}
	_, ok := developerUserIDs[util.UUIDToString(ownerID)]
	return ok
}

func stableRuntimeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return "hermes"
	}
	switch provider {
	case "hermes", "opencode", "pi":
		return provider
	default:
		return ""
	}
}

func assignStableBatches(releaseID string, targets []stableRuntimeTarget) {
	byProviderWorkspace := make(map[string]map[string][]stableRuntimeTarget, 3)
	for _, target := range targets {
		workspaceID := util.UUIDToString(target.WorkspaceID)
		if byProviderWorkspace[target.Provider] == nil {
			byProviderWorkspace[target.Provider] = make(map[string][]stableRuntimeTarget)
		}
		byProviderWorkspace[target.Provider][workspaceID] = append(
			byProviderWorkspace[target.Provider][workspaceID],
			target,
		)
	}
	byProvider := make(map[string][]stableRuntimeTarget, 3)
	for _, provider := range []string{"hermes", "opencode", "pi"} {
		workspaces := byProviderWorkspace[provider]
		workspaceIDs := make([]string, 0, len(workspaces))
		for workspaceID := range workspaces {
			workspaceIDs = append(workspaceIDs, workspaceID)
			sort.Slice(workspaces[workspaceID], func(i, j int) bool {
				return stableTargetHash(releaseID, workspaces[workspaceID][i]) <
					stableTargetHash(releaseID, workspaces[workspaceID][j])
			})
		}
		sort.Slice(workspaceIDs, func(i, j int) bool {
			return stableWorkspaceHash(releaseID, provider, workspaceIDs[i]) <
				stableWorkspaceHash(releaseID, provider, workspaceIDs[j])
		})
		for index := 0; ; index++ {
			added := false
			for _, workspaceID := range workspaceIDs {
				if index < len(workspaces[workspaceID]) {
					byProvider[provider] = append(
						byProvider[provider],
						workspaces[workspaceID][index],
					)
					added = true
				}
			}
			if !added {
				break
			}
		}
	}
	ordered := make([]stableRuntimeTarget, 0, len(targets))
	for index := 0; len(ordered) < len(targets); index++ {
		for _, provider := range []string{"hermes", "opencode", "pi"} {
			if index < len(byProvider[provider]) {
				ordered = append(ordered, byProvider[provider][index])
			}
		}
	}
	cutoffs := stableBatchCutoffs(len(ordered))
	batches := make(map[string]int, len(ordered))
	for index, target := range ordered {
		batch := 4
		switch {
		case index < cutoffs[0]:
			batch = 1
		case index < cutoffs[1]:
			batch = 2
		case index < cutoffs[2]:
			batch = 3
		}
		batches[util.UUIDToString(target.RuntimeID)] = batch
	}
	for index := range targets {
		targets[index].BatchIndex = batches[util.UUIDToString(targets[index].RuntimeID)]
	}
}

func stableBatchCutoffs(total int) [4]int {
	if total <= 0 {
		return [4]int{}
	}
	first := int(math.Ceil(float64(total) * 0.05))
	if total >= 3 && first < 3 {
		first = 3
	}
	second := max(first, int(math.Ceil(float64(total)*0.25)))
	third := max(second, int(math.Ceil(float64(total)*0.50)))
	return [4]int{
		min(total, first),
		min(total, second),
		min(total, third),
		total,
	}
}

func stableTargetHash(releaseID string, target stableRuntimeTarget) string {
	sum := sha256.Sum256([]byte(releaseID + ":" + util.UUIDToString(target.WorkspaceID) + ":" + util.UUIDToString(target.RuntimeID)))
	return hex.EncodeToString(sum[:])
}

func stableWorkspaceHash(releaseID, provider, workspaceID string) string {
	sum := sha256.Sum256([]byte(releaseID + ":" + provider + ":" + workspaceID))
	return hex.EncodeToString(sum[:])
}

func stableNextBatch(currentBatch int, startedAt time.Time) (int, int, time.Time) {
	switch currentBatch {
	case 1:
		return 2, 25, startedAt.Add(2 * time.Hour)
	case 2:
		return 3, 50, startedAt.Add(8 * time.Hour)
	case 3:
		return 4, 100, startedAt.Add(20 * time.Hour)
	default:
		return 0, 100, startedAt.Add(24 * time.Hour)
	}
}

func stableRolloutSchedule(startedAt time.Time) []FCE2BStableRolloutMilestone {
	return []FCE2BStableRolloutMilestone{
		{Batch: 1, Percentage: 5, ScheduledAt: startedAt, Kind: "rollout"},
		{Batch: 2, Percentage: 25, ScheduledAt: startedAt.Add(2 * time.Hour), Kind: "rollout"},
		{Batch: 3, Percentage: 50, ScheduledAt: startedAt.Add(8 * time.Hour), Kind: "rollout"},
		{Batch: 4, Percentage: 100, ScheduledAt: startedAt.Add(20 * time.Hour), Kind: "rollout"},
		{Batch: 5, Percentage: 100, ScheduledAt: startedAt.Add(24 * time.Hour), Kind: "complete"},
	}
}

func (s *FCE2BStableService) claimTarget(
	ctx context.Context,
	releaseID string,
	batch int,
	developerOnly bool,
) (stableRuntimeTarget, error) {
	var target stableRuntimeTarget
	err := s.Pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id
			FROM fc_e2b_stable_release_target
			WHERE release_id = $1
			  AND (
			      ($3 = true AND is_developer = true)
			      OR ($3 = false AND batch_index <= $2)
			  )
			  AND (
			      status = 'pending'
			      OR (status = 'updating' AND lease_expires_at < now())
			  )
			ORDER BY batch_index, runtime_id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE fc_e2b_stable_release_target target
		SET status = 'updating',
		    attempt_count = attempt_count + 1,
		    lease_token = gen_random_uuid(),
		    lease_expires_at = now() + interval '2 minutes',
		    updated_at = now()
		FROM candidate
		WHERE target.id = candidate.id
		RETURNING target.runtime_id, target.workspace_id, target.provider,
		          target.sandbox_backend,
		          target.previous_template_id, target.previous_template_build_id,
		          target.previous_template_alias, target.previous_artifact_ref,
		          target.previous_artifact_build_id, target.previous_artifact_digest,
		          target.batch_index
	`, releaseID, batch, developerOnly).Scan(
		&target.RuntimeID,
		&target.WorkspaceID,
		&target.Provider,
		&target.SandboxBackend,
		&target.PreviousTemplateID,
		&target.PreviousTemplateBuildID,
		&target.PreviousTemplateAlias,
		&target.PreviousArtifactRef,
		&target.PreviousArtifactBuildID,
		&target.PreviousArtifactDigest,
		&target.BatchIndex,
	)
	return target, err
}

func (s *FCE2BStableService) stableLaunchHealthGate(ctx context.Context, release FCE2BStableRelease) error {
	if release.RolloutStartedAt == nil || release.BatchStartedAt == nil {
		return errors.New("stable release has no rollout or batch start time")
	}
	currentBatchHasCutovers, err := s.currentBatchHasCutovers(ctx, release)
	if err != nil {
		return err
	}
	if !currentBatchHasCutovers {
		return nil
	}
	type providerHealth struct {
		total                  int
		failed                 int
		completed              int
		rotatedExistingSession int
		newSession             int
	}
	candidate := make(map[string]providerHealth)
	rows, err := s.Pool.Query(ctx, `
		WITH current_batch_target AS (
			SELECT runtime_id, provider, completed_at
			FROM fc_e2b_stable_release_target
			WHERE release_id = $1
			  AND batch_index = $2
			  AND status = 'updated'
			  AND completed_at >= $3
		)
		SELECT target.provider,
		       count(task.id),
		       count(task.id) FILTER (WHERE task.failure_reason = 'runtime_start_failed'),
		       count(task.id) FILTER (WHERE task.status = 'completed')
		FROM current_batch_target target
		LEFT JOIN agent_task_queue task
		  ON task.runtime_id = target.runtime_id
		 AND task.created_at >= target.completed_at
		GROUP BY target.provider
	`, release.ID, release.CurrentBatch, *release.BatchStartedAt)
	if err != nil {
		return fmt.Errorf("load stable candidate launch health: %w", err)
	}
	for rows.Next() {
		var provider string
		var health providerHealth
		if err := rows.Scan(&provider, &health.total, &health.failed, &health.completed); err != nil {
			rows.Close()
			return err
		}
		candidate[provider] = health
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	sessionArtifactRef := release.ArtifactRef
	if release.SandboxBackend == string(SandboxBackendAliyunFC) {
		sessionArtifactRef = release.TemplateAlias
	}
	sessionRows, err := s.Pool.Query(ctx, `
			WITH current_batch_target AS (
				SELECT runtime_id, provider, completed_at
				FROM fc_e2b_stable_release_target
				WHERE release_id = $1
				  AND batch_index = $2
				  AND status = 'updated'
				  AND completed_at >= $3
			)
			SELECT target.provider,
			       count(DISTINCT session.sandbox_id) FILTER (
			           WHERE session.created_at < target.completed_at
			       ),
			       count(DISTINCT session.sandbox_id) FILTER (
			           WHERE session.created_at >= target.completed_at
			       )
			FROM current_batch_target target
			LEFT JOIN fc_e2b_sandbox_session session
			  ON session.runtime_id = target.runtime_id
			 AND session.sandbox_backend = $4
			 AND session.artifact_ref = $5
			 AND session.updated_at >= target.completed_at
			GROUP BY target.provider
		`, release.ID, release.CurrentBatch, *release.BatchStartedAt,
		release.SandboxBackend, sessionArtifactRef)
	if err != nil {
		return fmt.Errorf("load stable candidate sandbox health: %w", err)
	}
	for sessionRows.Next() {
		var provider string
		var rotatedExistingSession, newSession int
		if err := sessionRows.Scan(&provider, &rotatedExistingSession, &newSession); err != nil {
			sessionRows.Close()
			return err
		}
		health := candidate[provider]
		health.rotatedExistingSession = rotatedExistingSession
		health.newSession = newSession
		candidate[provider] = health
	}
	sessionRows.Close()
	if err := sessionRows.Err(); err != nil {
		return err
	}
	for _, provider := range []string{"hermes", "opencode", "pi"} {
		health, present := candidate[provider]
		if !present {
			continue
		}
		if err := stableProviderProbeCoverageError(
			provider,
			health.completed,
			health.rotatedExistingSession,
			health.newSession,
		); err != nil {
			return err
		}
	}

	var candidateTotal, candidateFailed, baselineTotal, baselineFailed int
	if err := s.Pool.QueryRow(ctx, `
		WITH selected_runtimes AS (
			SELECT runtime_id, completed_at
			FROM fc_e2b_stable_release_target
			WHERE release_id = $1
			  AND batch_index = $2
			  AND status = 'updated'
			  AND completed_at >= $3
		)
		SELECT
			count(*) FILTER (WHERE task.created_at >= selected.completed_at),
			count(*) FILTER (
			    WHERE task.created_at >= selected.completed_at
			      AND task.failure_reason = 'runtime_start_failed'
			),
			count(*) FILTER (
			    WHERE task.created_at >= selected.completed_at - interval '24 hours'
			      AND task.created_at < selected.completed_at
			),
			count(*) FILTER (
			    WHERE task.created_at >= selected.completed_at - interval '24 hours'
			      AND task.created_at < selected.completed_at
			      AND task.failure_reason = 'runtime_start_failed'
			)
		FROM agent_task_queue task
		JOIN selected_runtimes selected ON selected.runtime_id = task.runtime_id
	`, release.ID, release.CurrentBatch, *release.BatchStartedAt).Scan(
		&candidateTotal,
		&candidateFailed,
		&baselineTotal,
		&baselineFailed,
	); err != nil {
		return fmt.Errorf("calculate stable release launch health: %w", err)
	}
	candidateRate := failureRate(candidateFailed, candidateTotal)
	baselineRate := failureRate(baselineFailed, baselineTotal)
	if candidateRate > 0.05 {
		return fmt.Errorf("candidate runtime-start failure rate %.2f%% exceeds 5%%", candidateRate*100)
	}
	if candidateRate > baselineRate+0.03 {
		return fmt.Errorf(
			"candidate runtime-start failure rate %.2f%% exceeds the 24-hour baseline %.2f%% by more than 3 percentage points",
			candidateRate*100,
			baselineRate*100,
		)
	}
	return nil
}

func stableProviderProbeCoverageError(
	provider string,
	completed,
	rotatedExistingSession,
	newSession int,
) error {
	if completed < 2 {
		return nil
	}
	if rotatedExistingSession < 1 || newSession < 1 {
		return fmt.Errorf(
			"provider %s has not proven both an existing-session rotation and a new-conversation sandbox",
			provider,
		)
	}
	return nil
}

func (s *FCE2BStableService) currentBatchHasCutovers(
	ctx context.Context,
	release FCE2BStableRelease,
) (bool, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT completed_at
		FROM fc_e2b_stable_release_target
		WHERE release_id = $1
		  AND batch_index = $2
		  AND status = 'updated'
	`, release.ID, release.CurrentBatch)
	if err != nil {
		return false, fmt.Errorf("load stable current-batch cutovers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var completedAt pgtype.Timestamptz
		if err := rows.Scan(&completedAt); err != nil {
			return false, err
		}
		if !completedAt.Valid {
			return false, errors.New("stable release target is updated without a completion time")
		}
		if stableTargetNeedsBatchHealthGate(completedAt.Time, *release.BatchStartedAt) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func stableTargetNeedsBatchHealthGate(completedAt, batchStartedAt time.Time) bool {
	return !completedAt.Before(batchStartedAt)
}

func stableObservationTargetsError(missing, failed int) error {
	if failed > 0 {
		return fmt.Errorf("%d runtime targets failed", failed)
	}
	if missing > 0 {
		return fmt.Errorf("%d runtime targets are not updated", missing)
	}
	return nil
}

func failureRate(failed, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(failed) / float64(total)
}

func (s *FCE2BStableService) pauseForGate(
	ctx context.Context,
	releaseID string,
	token uuid.UUID,
	previousStatus string,
	failure error,
) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET status = 'paused',
		    paused_from_status = $1,
		    validation_error = $2,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $3 AND lease_token = $4
	`, previousStatus, failure.Error(), releaseID, token)
	return err
}

func (s *FCE2BStableService) claimRollbackTarget(ctx context.Context, releaseID string) (stableRuntimeTarget, error) {
	var target stableRuntimeTarget
	err := s.Pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id
			FROM fc_e2b_stable_release_target
			WHERE release_id = $1
			  AND status IN ('updated', 'rolling_back')
			  AND (lease_expires_at IS NULL OR lease_expires_at < now())
			ORDER BY batch_index DESC, runtime_id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE fc_e2b_stable_release_target target
		SET status = 'rolling_back',
		    attempt_count = attempt_count + 1,
		    lease_token = gen_random_uuid(),
		    lease_expires_at = now() + interval '2 minutes',
		    updated_at = now()
		FROM candidate
		WHERE target.id = candidate.id
		RETURNING target.runtime_id, target.workspace_id, target.provider,
		          target.sandbox_backend,
		          target.previous_template_id, target.previous_template_build_id,
		          target.previous_template_alias, target.previous_artifact_ref,
		          target.previous_artifact_build_id, target.previous_artifact_digest,
		          target.batch_index
	`, releaseID).Scan(
		&target.RuntimeID,
		&target.WorkspaceID,
		&target.Provider,
		&target.SandboxBackend,
		&target.PreviousTemplateID,
		&target.PreviousTemplateBuildID,
		&target.PreviousTemplateAlias,
		&target.PreviousArtifactRef,
		&target.PreviousArtifactBuildID,
		&target.PreviousArtifactDigest,
		&target.BatchIndex,
	)
	return target, err
}

func (s *FCE2BStableService) failTarget(ctx context.Context, releaseID string, runtimeID pgtype.UUID, token uuid.UUID, failure error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `
		UPDATE fc_e2b_stable_release_target
		SET status = 'failed', last_error = $1, lease_token = NULL,
		    lease_expires_at = NULL, updated_at = now()
		WHERE release_id = $2 AND runtime_id = $3
	`, failure.Error(), releaseID, runtimeID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET status = 'paused', paused_from_status = status,
		    failed_targets = (
		        SELECT count(*) FROM fc_e2b_stable_release_target
		        WHERE release_id = $1 AND status = 'failed'
		    ),
		    validation_error = $2,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $1 AND lease_token = $3
	`, releaseID, failure.Error(), token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *FCE2BStableService) failValidation(ctx context.Context, releaseID string, token uuid.UUID, failure error) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET status = 'failed', validation_error = $1, lease_token = NULL,
		    lease_expires_at = NULL, completed_at = now(), updated_at = now()
		WHERE id = $2 AND lease_token = $3
	`, failure.Error(), releaseID, token)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_channel
		SET active_release_id = NULL, updated_at = now()
		WHERE channel = 'stable' AND active_release_id = $1
	`, releaseID)
	return err
}

func (s *FCE2BStableService) releaseLease(ctx context.Context, releaseID string, token uuid.UUID, operationErr error) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE fc_e2b_stable_release
		SET lease_token = NULL,
		    lease_expires_at = NULL,
		    next_batch_at = CASE WHEN next_batch_at > now() THEN next_batch_at ELSE now() END,
		    updated_at = now()
		WHERE id = $1 AND lease_token = $2
	`, releaseID, token)
	if operationErr != nil {
		return operationErr
	}
	return err
}

func (s *FCE2BStableService) getActiveRelease(
	ctx context.Context,
	backends ...SandboxBackendKind,
) (*FCE2BStableRelease, error) {
	backend, err := stableSandboxBackend(backends...)
	if err != nil {
		return nil, err
	}
	release, err := s.scanRelease(s.Pool.QueryRow(ctx, stableReleaseSelect+`
		WHERE sandbox_backend = $1
		  AND status IN (
		    'validating',
		    'developer_rollout',
		    'awaiting_rollout',
		    'rolling_out',
		    'observing',
		    'paused',
		    'rolling_back'
		)
		ORDER BY created_at
		LIMIT 1
	`, backend))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &release, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *FCE2BStableService) scanRelease(row rowScanner) (FCE2BStableRelease, error) {
	var release FCE2BStableRelease
	var manifestJSON []byte
	var actor pgtype.UUID
	var developerRolloutStartedAt, developerRolloutCompletedAt pgtype.Timestamptz
	var rolloutStartedAt, batchStartedAt, nextBatchAt, completedAt pgtype.Timestamptz
	err := row.Scan(
		&release.ID,
		&release.SandboxBackend,
		&release.ArtifactKind,
		&release.ArtifactRef,
		&release.ArtifactBuildID,
		&release.ArtifactAlias,
		&release.ArtifactDigest,
		&release.TemplateID,
		&release.TemplateBuildID,
		&release.TemplateAlias,
		&release.GitCommit,
		&release.ACRDigest,
		&release.SourceRevision,
		&release.Note,
		&actor,
		&release.Bootstrap,
		&release.Status,
		&release.CurrentBatch,
		&release.TargetPercentage,
		&release.PreviousTemplateID,
		&release.PreviousTemplateBuildID,
		&release.PreviousTemplateAlias,
		&release.PreviousArtifactRef,
		&release.PreviousArtifactBuildID,
		&release.PreviousArtifactDigest,
		&manifestJSON,
		&release.TotalTargets,
		&release.UpdatedTargets,
		&release.FailedTargets,
		&release.DeveloperTargets,
		&release.DeveloperUpdatedTargets,
		&developerRolloutStartedAt,
		&developerRolloutCompletedAt,
		&rolloutStartedAt,
		&batchStartedAt,
		&nextBatchAt,
		&completedAt,
		&release.ValidationError,
		&release.CreatedAt,
		&release.UpdatedAt,
	)
	if err != nil {
		return FCE2BStableRelease{}, err
	}
	release.ActorUserID = util.UUIDToString(actor)
	if len(manifestJSON) > 0 {
		_ = json.Unmarshal(manifestJSON, &release.Manifest)
	}
	if developerRolloutStartedAt.Valid {
		release.DeveloperRolloutStartedAt = &developerRolloutStartedAt.Time
	}
	if developerRolloutCompletedAt.Valid {
		release.DeveloperRolloutCompletedAt = &developerRolloutCompletedAt.Time
	}
	if rolloutStartedAt.Valid {
		release.RolloutStartedAt = &rolloutStartedAt.Time
		release.RolloutSchedule = stableRolloutSchedule(rolloutStartedAt.Time)
	}
	if batchStartedAt.Valid {
		release.BatchStartedAt = &batchStartedAt.Time
	}
	if nextBatchAt.Valid {
		release.NextBatchAt = &nextBatchAt.Time
	}
	if completedAt.Valid {
		release.CompletedAt = &completedAt.Time
	}
	return release, nil
}

func stableReleaseFingerprint(input CreateFCE2BStableReleaseInput) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(input.SandboxBackend),
		input.ArtifactRef,
		input.TemplateID,
		input.ExpectedBuildID,
		input.ArtifactDigest,
		input.GitCommit,
		input.Note,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func stableSandboxBackend(backends ...SandboxBackendKind) (SandboxBackendKind, error) {
	if len(backends) == 0 || strings.TrimSpace(string(backends[0])) == "" {
		return SandboxBackendAliyunFC, nil
	}
	switch backends[0] {
	case SandboxBackendAliyunFC, SandboxBackendASB:
		return backends[0], nil
	default:
		return "", errors.New("sandbox_backend must be 'aliyun_fc' or 'asb'")
	}
}

func isLowerHex(value string) bool {
	if value == "" || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isSHA256Digest(value string) bool {
	return strings.HasPrefix(value, "sha256:") &&
		len(value) == len("sha256:")+64 &&
		isLowerHex(strings.TrimPrefix(value, "sha256:"))
}

func isStableSourceRevision(value string) bool {
	if len(value) != 6 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func releaseTemplate(release FCE2BStableRelease) FCE2BTemplate {
	return FCE2BTemplate{
		ID:                release.TemplateID,
		BuildID:           release.TemplateBuildID,
		Name:              release.TemplateAlias,
		Template:          release.TemplateAlias,
		Status:            "READY",
		ManifestVersion:   2,
		Providers:         []string{"hermes", "opencode", "pi"},
		Capabilities:      []string{"dws", "dws.im_event", "mcp"},
		ComponentVersions: releaseComponentVersions(release.Manifest),
		RunnerProtocol:    string(fcE2BRunnerLaunchRootLog),
	}
}

func releaseASBArtifact(release FCE2BStableRelease) ASBArtifact {
	alias := firstNonEmptyString(
		release.ArtifactAlias,
		release.TemplateAlias,
		asbArtifactAlias(release.ArtifactRef, release.ArtifactBuildID),
	)
	return ASBArtifact{
		Ref:      release.ArtifactRef,
		BuildID:  release.ArtifactBuildID,
		Alias:    alias,
		Digest:   release.ArtifactDigest,
		Manifest: release.Manifest,
	}
}

func asbArtifactAlias(ref, buildID string) string {
	repository := strings.TrimSpace(ref)
	if at := strings.LastIndex(repository, "@"); at >= 0 {
		repository = repository[:at]
	}
	if slash := strings.LastIndex(repository, "/"); slash >= 0 {
		repository = repository[slash+1:]
	}
	buildID = strings.TrimSpace(buildID)
	if buildID == "" {
		return repository
	}
	return repository + ":" + buildID
}

func releaseComponentVersions(manifest map[string]any) map[string]string {
	result := make(map[string]string)
	raw, ok := manifest["component_versions"].(map[string]any)
	if !ok {
		return result
	}
	for key, value := range raw {
		if text, ok := value.(string); ok {
			result[key] = text
		}
	}
	return result
}

func runtimeUsesTemplate(runtime db.AgentRuntime, templateID, buildID string) bool {
	var metadata struct {
		TemplateID      string `json:"template_id"`
		TemplateBuildID string `json:"template_build_id"`
	}
	if err := json.Unmarshal(runtime.Metadata, &metadata); err != nil {
		return false
	}
	return metadata.TemplateID == templateID && metadata.TemplateBuildID == buildID
}

func runtimeUsesArtifact(runtime db.AgentRuntime, ref, buildID, digest string) bool {
	var metadata struct {
		SandboxBackend  string `json:"sandbox_backend"`
		ArtifactRef     string `json:"artifact_ref"`
		ArtifactBuildID string `json:"artifact_build_id"`
		ArtifactDigest  string `json:"artifact_digest"`
	}
	if err := json.Unmarshal(runtime.Metadata, &metadata); err != nil {
		return false
	}
	return metadata.SandboxBackend == string(SandboxBackendASB) &&
		metadata.ArtifactRef == ref &&
		metadata.ArtifactBuildID == buildID &&
		metadata.ArtifactDigest == digest
}

func runtimeUsesStableRelease(runtime db.AgentRuntime, release FCE2BStableRelease) bool {
	switch SandboxBackendKind(release.SandboxBackend) {
	case SandboxBackendAliyunFC:
		return runtimeUsesTemplate(runtime, release.TemplateID, release.TemplateBuildID)
	case SandboxBackendASB:
		return runtimeUsesArtifact(
			runtime,
			release.ArtifactRef,
			release.ArtifactBuildID,
			release.ArtifactDigest,
		)
	default:
		return false
	}
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

const stableReleaseColumns = `
	release.id::text,
	release.sandbox_backend,
	release.artifact_kind,
	release.artifact_ref,
	release.artifact_build_id,
	release.template_alias,
	release.artifact_digest,
	release.template_id,
	release.template_build_id,
	release.template_alias,
	release.git_commit,
	release.acr_digest,
	release.source_revision,
	release.note,
	release.actor_user_id,
	release.bootstrap,
	release.status,
	release.current_batch,
	release.target_percentage,
	release.previous_template_id,
	release.previous_template_build_id,
	release.previous_template_alias,
	release.previous_artifact_ref,
	release.previous_artifact_build_id,
	release.previous_artifact_digest,
	release.manifest,
	release.total_targets,
	release.updated_targets,
	release.failed_targets,
	(
	    SELECT count(*)
	    FROM fc_e2b_stable_release_target developer_target
	    WHERE developer_target.release_id = release.id
	      AND developer_target.is_developer = true
	),
	(
	    SELECT count(*)
	    FROM fc_e2b_stable_release_target developer_target
	    WHERE developer_target.release_id = release.id
	      AND developer_target.is_developer = true
	      AND developer_target.status = 'updated'
	),
	release.developer_rollout_started_at,
	release.developer_rollout_completed_at,
	release.rollout_started_at,
	release.batch_started_at,
	release.next_batch_at,
	release.completed_at,
	release.validation_error,
	release.created_at,
	release.updated_at
`

const stableReleaseSelect = `SELECT ` + stableReleaseColumns + ` FROM fc_e2b_stable_release release`
