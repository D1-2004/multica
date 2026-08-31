package sitehosting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) Prepare(ctx context.Context, record PrepareRecord) (Upload, error) {
	if s == nil || s.pool == nil {
		return Upload{}, ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Upload{}, fmt.Errorf("begin static site prepare: %w", err)
	}
	defer tx.Rollback(ctx)
	upload := record.Upload
	if record.ExistingSiteID == "" {
		_, err = tx.Exec(ctx, `
			INSERT INTO hosted_site (id, public_id, owner_user_id, workspace_id)
			VALUES ($1::uuid, $2, $3::uuid, $4::uuid)
		`, upload.SiteID, upload.PublicSiteID, upload.OwnerUserID, upload.WorkspaceID)
	} else {
		err = tx.QueryRow(ctx, `
			UPDATE hosted_site site
			SET workspace_id = COALESCE(site.workspace_id, $3::uuid)
			WHERE site.id = $1::uuid AND site.status = 'active'
			  AND COALESCE(
				site.owner_user_id,
				(SELECT agent.owner_id FROM agent WHERE agent.id = site.owner_agent_id),
				(SELECT member.user_id FROM member WHERE member.workspace_id = site.workspace_id AND member.role = 'owner' ORDER BY member.created_at LIMIT 1)
			  ) = $2::uuid
			  AND (site.workspace_id = $3::uuid OR site.workspace_id IS NULL)
			RETURNING public_id
		`, record.ExistingSiteID, upload.OwnerUserID, upload.WorkspaceID).Scan(&upload.PublicSiteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Upload{}, ErrSiteForbidden
		}
	}
	if err != nil {
		return Upload{}, fmt.Errorf("prepare static site: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO hosted_site_revision (id, site_id, entrypoint, spa_fallback)
		VALUES ($1::uuid, $2::uuid, $3, $4)
	`, upload.RevisionID, upload.SiteID, upload.Entrypoint, upload.SPAFallback); err != nil {
		return Upload{}, fmt.Errorf("create static site revision: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO hosted_site_upload (
			id, site_id, revision_id, token_hash, expected_sha256,
			expected_length, expires_at
		) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7)
	`, upload.ID, upload.SiteID, upload.RevisionID, upload.TokenHash,
		upload.ExpectedSHA256, upload.ExpectedLength, upload.ExpiresAt); err != nil {
		return Upload{}, fmt.Errorf("create static site upload: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Upload{}, fmt.Errorf("commit static site prepare: %w", err)
	}
	return upload, nil
}

func (s *PostgresStore) ClaimUpload(ctx context.Context, uploadID string, tokenHash []byte, now time.Time) (Upload, error) {
	if s == nil || s.pool == nil {
		return Upload{}, ErrUnavailable
	}
	var upload Upload
	err := s.pool.QueryRow(ctx, `
		WITH claimed AS (
			UPDATE hosted_site_upload
			SET used_at = $3
			WHERE id = $1::uuid AND token_hash = $2
			  AND used_at IS NULL AND expires_at > $3
			RETURNING *
		)
		UPDATE hosted_site_revision revision
		SET status = 'uploading', updated_at = $3
		FROM claimed upload, hosted_site site
		WHERE revision.id = upload.revision_id
		  AND site.id = upload.site_id
		  AND site.status = 'active'
		RETURNING
			upload.id::text, upload.site_id::text, site.public_id,
			upload.revision_id::text, upload.token_hash,
			upload.expected_sha256, upload.expected_length,
			revision.entrypoint, revision.spa_fallback,
			upload.expires_at, upload.used_at
	`, uploadID, tokenHash, now).Scan(
		&upload.ID, &upload.SiteID, &upload.PublicSiteID, &upload.RevisionID,
		&upload.TokenHash, &upload.ExpectedSHA256, &upload.ExpectedLength, &upload.Entrypoint,
		&upload.SPAFallback, &upload.ExpiresAt, &upload.UsedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Upload{}, ErrUploadCapabilityInvalid
	}
	if err != nil {
		return Upload{}, fmt.Errorf("claim static site upload: %w", err)
	}
	return upload, nil
}

func (s *PostgresStore) ActivateRevision(ctx context.Context, activation Activation) error {
	manifest, err := json.Marshal(activation.Manifest)
	if err != nil {
		return fmt.Errorf("encode static site manifest: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin static site activation: %w", err)
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `
		UPDATE hosted_site_revision
		SET status = 'active', manifest = $3::jsonb, archive_sha256 = $4,
		    file_count = $5, total_bytes = $6, error = '',
		    activated_at = $7, updated_at = $7
		WHERE id = $1::uuid AND site_id = $2::uuid AND status = 'uploading'
	`, activation.RevisionID, activation.SiteID, manifest, activation.ArchiveSHA,
		len(activation.Manifest.Files), activation.Manifest.TotalBytes, activation.ActivatedAt)
	if err != nil {
		return fmt.Errorf("activate static site revision: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("activate static site revision: revision is not uploadable")
	}
	result, err = tx.Exec(ctx, `
		UPDATE hosted_site
		SET active_revision_id = $2::uuid, updated_at = $3
		WHERE id = $1::uuid AND public_id = $4 AND status = 'active'
	`, activation.SiteID, activation.RevisionID, activation.ActivatedAt, activation.PublicSiteID)
	if err != nil {
		return fmt.Errorf("switch active static site revision: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("switch active static site revision: site is unavailable")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit static site activation: %w", err)
	}
	return nil
}

func (s *PostgresStore) FailRevision(ctx context.Context, revisionID, reason string) error {
	if s == nil || s.pool == nil {
		return ErrUnavailable
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE hosted_site_revision
		SET status = 'failed', error = $2, updated_at = now()
		WHERE id = $1::uuid AND status IN ('pending', 'uploading')
	`, revisionID, reason)
	if err != nil {
		return fmt.Errorf("fail static site revision: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetStatus(ctx context.Context, siteID, ownerUserID, workspaceID string) (SiteStatus, error) {
	if s == nil || s.pool == nil {
		return SiteStatus{}, ErrUnavailable
	}
	var status SiteStatus
	err := s.pool.QueryRow(ctx, `
		-- The fallback only covers legacy rows without owner_user_id during
		-- the rolling migration window; it is not the Site ownership model.
		SELECT site.id::text, site.public_id,
		       COALESCE(active.manifest->>'title', ''), site.status,
		       site.active_revision_id::text, latest.id::text,
		       latest.status, latest.error, site.created_at, site.updated_at
		FROM hosted_site site
		JOIN LATERAL (
			SELECT id, status, error
			FROM hosted_site_revision
			WHERE site_id = site.id
			ORDER BY created_at DESC
			LIMIT 1
		) latest ON true
		LEFT JOIN hosted_site_revision active ON active.id = site.active_revision_id
		WHERE site.id = $1::uuid AND site.status = 'active'
		  AND site.workspace_id = $3::uuid
		  AND COALESCE(
			site.owner_user_id,
			(SELECT agent.owner_id FROM agent WHERE agent.id = site.owner_agent_id),
			(SELECT member.user_id FROM member WHERE member.workspace_id = site.workspace_id AND member.role = 'owner' ORDER BY member.created_at LIMIT 1)
		  ) = $2::uuid
	`, siteID, ownerUserID, workspaceID).Scan(
		&status.SiteID, &status.PublicSiteID, &status.Title, &status.Status, &status.ActiveRevisionID,
		&status.LatestRevisionID, &status.LatestStatus, &status.LatestError,
		&status.CreatedAt, &status.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SiteStatus{}, ErrSiteForbidden
	}
	if err != nil {
		return SiteStatus{}, fmt.Errorf("get static site status: %w", err)
	}
	status.OwnerUserID = ownerUserID
	status.WorkspaceID = workspaceID
	return status, nil
}

func (s *PostgresStore) ListSites(ctx context.Context, ownerUserID, workspaceID string) ([]SiteStatus, error) {
	if s == nil || s.pool == nil {
		return nil, ErrUnavailable
	}
	rows, err := s.pool.Query(ctx, `
		SELECT site.id::text, site.public_id,
		       COALESCE(active.manifest->>'title', ''), site.status,
		       site.active_revision_id::text, latest.id::text,
		       latest.status, latest.error, site.created_at, site.updated_at
		FROM hosted_site site
		JOIN LATERAL (
			SELECT id, status, error
			FROM hosted_site_revision
			WHERE site_id = site.id
			ORDER BY created_at DESC
			LIMIT 1
		) latest ON true
		LEFT JOIN hosted_site_revision active ON active.id = site.active_revision_id
		WHERE site.status = 'active'
		  AND site.workspace_id = $2::uuid
		  AND COALESCE(
			site.owner_user_id,
			(SELECT agent.owner_id FROM agent WHERE agent.id = site.owner_agent_id),
			(SELECT member.user_id FROM member WHERE member.workspace_id = site.workspace_id AND member.role = 'owner' ORDER BY member.created_at LIMIT 1)
		  ) = $1::uuid
		ORDER BY site.updated_at DESC, site.id DESC
	`, ownerUserID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list static sites: %w", err)
	}
	defer rows.Close()
	result := make([]SiteStatus, 0)
	for rows.Next() {
		var status SiteStatus
		if err := rows.Scan(
			&status.SiteID, &status.PublicSiteID, &status.Title, &status.Status,
			&status.ActiveRevisionID, &status.LatestRevisionID,
			&status.LatestStatus, &status.LatestError,
			&status.CreatedAt, &status.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan static site: %w", err)
		}
		status.OwnerUserID = ownerUserID
		status.WorkspaceID = workspaceID
		result = append(result, status)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list static sites: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) DeleteSite(ctx context.Context, siteID, ownerUserID, workspaceID string) error {
	if s == nil || s.pool == nil {
		return ErrUnavailable
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE hosted_site site
		SET status = 'deleted', updated_at = now()
		WHERE site.id = $1::uuid AND site.status = 'active'
		  AND site.workspace_id = $3::uuid
		  AND COALESCE(
			site.owner_user_id,
			(SELECT agent.owner_id FROM agent WHERE agent.id = site.owner_agent_id),
			(SELECT member.user_id FROM member WHERE member.workspace_id = site.workspace_id AND member.role = 'owner' ORDER BY member.created_at LIMIT 1)
		  ) = $2::uuid
	`, siteID, ownerUserID, workspaceID)
	if err != nil {
		return fmt.Errorf("delete static site: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrSiteForbidden
	}
	return nil
}

func (s *PostgresStore) ResolvePublic(ctx context.Context, publicSiteID string) (ResolvedSite, error) {
	if s == nil || s.pool == nil {
		return ResolvedSite{}, ErrUnavailable
	}
	var resolved ResolvedSite
	var manifest []byte
	err := s.pool.QueryRow(ctx, `
		SELECT site.id::text, site.public_id, revision.id::text,
		       revision.manifest, revision.spa_fallback
		FROM hosted_site site
		JOIN hosted_site_revision revision ON revision.id = site.active_revision_id
		WHERE site.public_id = $1 AND site.status = 'active'
		  AND revision.status = 'active'
	`, publicSiteID).Scan(
		&resolved.SiteID, &resolved.PublicSiteID, &resolved.RevisionID,
		&manifest, &resolved.SPAFallback,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolvedSite{}, ErrSiteNotFound
	}
	if err != nil {
		return ResolvedSite{}, fmt.Errorf("resolve public static site: %w", err)
	}
	if err := json.Unmarshal(manifest, &resolved.Manifest); err != nil {
		return ResolvedSite{}, fmt.Errorf("decode static site manifest: %w", err)
	}
	return resolved, nil
}
