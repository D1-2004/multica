package sitehosting

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresStoreKeepsUserOwnershipAndSwitchesRevisionAtomically(t *testing.T) {
	ctx := context.Background()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil || admin.Ping(ctx) != nil {
		t.Skip("PostgreSQL is unavailable")
	}
	defer admin.Close()
	schema := "sitehosting_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Skipf("cannot create isolated test schema: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	migration, err := os.ReadFile("../../migrations/9079_hosted_site_tables.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE agent (id UUID PRIMARY KEY, owner_id UUID);
		CREATE TABLE member (workspace_id UUID, user_id UUID, role TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
	`); err != nil {
		t.Fatal(err)
	}
	for migrationNumber := 9080; migrationNumber <= 9087; migrationNumber++ {
		matches, err := filepath.Glob(fmt.Sprintf("../../migrations/%d_hosted_site*_index.up.sql", migrationNumber))
		if err != nil || len(matches) != 1 {
			t.Fatalf("find index migration %d: matches=%v err=%v", migrationNumber, matches, err)
		}
		body, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", matches[0], err)
		}
	}
	for migrationNumber := 9088; migrationNumber <= 9089; migrationNumber++ {
		matches, err := filepath.Glob(fmt.Sprintf("../../migrations/%d_hosted_site_user_owner*.up.sql", migrationNumber))
		if err != nil || len(matches) != 1 {
			t.Fatalf("find user owner migration %d: matches=%v err=%v", migrationNumber, matches, err)
		}
		body, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", matches[0], err)
		}
	}
	workspaceIndexMigration, err := os.ReadFile("../../migrations/9093_hosted_site_workspace_user_index.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(workspaceIndexMigration)); err != nil {
		t.Fatalf("apply workspace index migration: %v", err)
	}

	store := NewPostgresStore(pool)
	ownerUserID := uuid.NewString()
	workspaceID := uuid.NewString()
	otherWorkspaceID := uuid.NewString()
	now := time.Now().UTC()
	first := Upload{
		ID: uuid.NewString(), SiteID: uuid.NewString(), PublicSiteID: "public-opaque-id",
		RevisionID: uuid.NewString(), OwnerUserID: ownerUserID, WorkspaceID: workspaceID,
		TokenHash: []byte(strings.Repeat("a", 32)), ExpectedSHA256: strings.Repeat("1", 64),
		ExpectedLength: 123, Entrypoint: "index.html", ExpiresAt: now.Add(time.Minute),
	}
	created, err := store.Prepare(ctx, PrepareRecord{Upload: first})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare(ctx, PrepareRecord{
		ExistingSiteID: created.SiteID,
		Upload: Upload{ID: uuid.NewString(), SiteID: created.SiteID, RevisionID: uuid.NewString(), OwnerUserID: uuid.NewString(), WorkspaceID: workspaceID, TokenHash: []byte(strings.Repeat("b", 32)), ExpectedSHA256: strings.Repeat("2", 64), ExpectedLength: 1, Entrypoint: "index.html", ExpiresAt: now.Add(time.Minute)},
	}); !errors.Is(err, ErrSiteForbidden) {
		t.Fatalf("cross-user prepare error=%v", err)
	}
	claimed, err := store.ClaimUpload(ctx, first.ID, first.TokenHash, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimUpload(ctx, first.ID, first.TokenHash, now); !errors.Is(err, ErrUploadCapabilityInvalid) {
		t.Fatalf("reused capability error=%v", err)
	}
	manifest := Manifest{Entrypoint: "index.html", Title: "Weekly Review", TotalBytes: 2, Files: map[string]ArchiveFile{"index.html": {Path: "index.html", Size: 2, ContentType: "text/html; charset=utf-8", ETag: `"etag"`}}}
	if err := store.ActivateRevision(ctx, Activation{UploadID: first.ID, SiteID: first.SiteID, PublicSiteID: first.PublicSiteID, RevisionID: claimed.RevisionID, Manifest: manifest, ArchiveSHA: strings.Repeat("1", 64), ActivatedAt: now}); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolvePublic(ctx, first.PublicSiteID)
	if err != nil || resolved.RevisionID != first.RevisionID {
		t.Fatalf("first resolve=%#v err=%v", resolved, err)
	}
	sites, err := store.ListSites(ctx, ownerUserID, workspaceID)
	if err != nil || len(sites) != 1 || sites[0].SiteID != first.SiteID || sites[0].Title != "Weekly Review" {
		t.Fatalf("workspace sites=%#v err=%v", sites, err)
	}
	sites, err = store.ListSites(ctx, ownerUserID, otherWorkspaceID)
	if err != nil || len(sites) != 0 {
		t.Fatalf("other workspace sites=%#v err=%v", sites, err)
	}

	if _, err := store.Prepare(ctx, PrepareRecord{
		ExistingSiteID: created.SiteID,
		Upload: Upload{ID: uuid.NewString(), SiteID: created.SiteID, RevisionID: uuid.NewString(), OwnerUserID: ownerUserID, WorkspaceID: otherWorkspaceID, TokenHash: []byte(strings.Repeat("e", 32)), ExpectedSHA256: strings.Repeat("5", 64), ExpectedLength: 1, Entrypoint: "index.html", ExpiresAt: now.Add(time.Minute)},
	}); !errors.Is(err, ErrSiteForbidden) {
		t.Fatalf("cross-workspace prepare error=%v", err)
	}

	second := Upload{ID: uuid.NewString(), SiteID: first.SiteID, RevisionID: uuid.NewString(), OwnerUserID: ownerUserID, WorkspaceID: workspaceID, TokenHash: []byte(strings.Repeat("c", 32)), ExpectedSHA256: strings.Repeat("3", 64), ExpectedLength: 50, Entrypoint: "index.html", ExpiresAt: now.Add(time.Minute)}
	second, err = store.Prepare(ctx, PrepareRecord{ExistingSiteID: first.SiteID, Upload: second})
	if err != nil {
		t.Fatal(err)
	}
	if second.PublicSiteID != first.PublicSiteID {
		t.Fatalf("public id changed: %q", second.PublicSiteID)
	}
	if err := store.FailRevision(ctx, second.RevisionID, "injected failure"); err != nil {
		t.Fatal(err)
	}
	resolved, err = store.ResolvePublic(ctx, first.PublicSiteID)
	if err != nil || resolved.RevisionID != first.RevisionID {
		t.Fatalf("failed revision changed active resolve=%#v err=%v", resolved, err)
	}
	status, err := store.GetStatus(ctx, first.SiteID, ownerUserID, workspaceID)
	if err != nil || status.Title != "Weekly Review" || status.LatestRevisionID != second.RevisionID || status.LatestStatus != "failed" {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	if _, err := store.GetStatus(ctx, first.SiteID, uuid.NewString(), workspaceID); !errors.Is(err, ErrSiteForbidden) {
		t.Fatalf("non-owner status error=%v", err)
	}
	if _, err := store.GetStatus(ctx, first.SiteID, ownerUserID, otherWorkspaceID); !errors.Is(err, ErrSiteForbidden) {
		t.Fatalf("cross-workspace status error=%v", err)
	}
	third := Upload{ID: uuid.NewString(), SiteID: first.SiteID, RevisionID: uuid.NewString(), OwnerUserID: ownerUserID, WorkspaceID: workspaceID, TokenHash: []byte(strings.Repeat("d", 32)), ExpectedSHA256: strings.Repeat("4", 64), ExpectedLength: 60, Entrypoint: "index.html", ExpiresAt: now.Add(time.Minute)}
	third, err = store.Prepare(ctx, PrepareRecord{ExistingSiteID: first.SiteID, Upload: third})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimUpload(ctx, third.ID, third.TokenHash, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateRevision(ctx, Activation{UploadID: third.ID, SiteID: third.SiteID, PublicSiteID: third.PublicSiteID, RevisionID: third.RevisionID, Manifest: manifest, ArchiveSHA: strings.Repeat("4", 64), ActivatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	resolved, err = store.ResolvePublic(ctx, first.PublicSiteID)
	if err != nil || resolved.RevisionID != third.RevisionID {
		t.Fatalf("second successful revision did not replace active resolve=%#v err=%v", resolved, err)
	}
}
