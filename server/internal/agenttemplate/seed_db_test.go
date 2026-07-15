package agenttemplate

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPlatformTemplateSeedOnlyMovesForward(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	queries := db.New(tx)
	key := "seed-monotonic-test-" + time.Now().UTC().Format("20060102150405.000000000")
	params := db.UpsertPlatformTemplateSeedParams{
		SystemKey: key, ReleaseVersion: 2, DisplayName: "New", Description: "new",
		ContentHash: "new-hash", BundleSchemaVersion: BundleSchemaVersion,
		BundleSizeBytes: 2, Bundle: []byte(`{}`),
	}
	if _, err := queries.UpsertPlatformTemplateSeed(ctx, params); err != nil {
		t.Fatalf("insert current seed: %v", err)
	}
	params.ReleaseVersion = 1
	params.DisplayName = "Old"
	params.ContentHash = "old-hash"
	if _, err := queries.UpsertPlatformTemplateSeed(ctx, params); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("lower release error = %v, want pgx.ErrNoRows", err)
	}
	current, err := queries.GetPlatformTemplateSeed(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if current.ReleaseVersion != 2 || current.ContentHash != "new-hash" || current.DisplayName != "New" {
		t.Fatalf("lower release overwrote seed: %#v", current)
	}
}
