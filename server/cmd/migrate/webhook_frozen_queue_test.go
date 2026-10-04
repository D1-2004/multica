package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWebhookFrozenQueueMigrationIsolatesExistingSources(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = f.schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE webhook_delivery(id text PRIMARY KEY,status text NOT NULL CHECK(status IN ('queued','dispatched','rejected','ignored','failed')),source_binding jsonb,lease_token text);
        INSERT INTO webhook_delivery VALUES ('frozen','queued','{"v":1}','old-lease'),('legacy','queued','{}',NULL),('unknown','queued','{"v":0}',NULL),('string-version','queued','{"v":"1"}',NULL)`); err != nil {
		t.Fatal(err)
	}
	up := filepath.Join("..", "..", "migrations", "9997_webhook_frozen_queue.up.sql")
	down := filepath.Join("..", "..", "migrations", "9997_webhook_frozen_queue.down.sql")
	run := func(direction, file string) error {
		return runMigrations(ctx, pool, runOptions{Direction: direction, Files: []string{file}, SchemaMigrationsTable: f.tableFQN, AdvisoryLockKey: f.lockKey})
	}
	if err := run("up", up); err != nil {
		t.Fatal(err)
	}
	var isolated int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM webhook_delivery WHERE status='queued_frozen'`).Scan(&isolated); err != nil || isolated != 1 {
		t.Fatalf("migration classified wrong binding: count=%d err=%v", isolated, err)
	}
	var status, lease string
	if err := pool.QueryRow(ctx, `SELECT status,lease_token FROM webhook_delivery WHERE id='frozen'`).Scan(&status, &lease); err != nil || status != "queued_frozen" || lease != "old-lease" {
		t.Fatalf("isolation changed source/lease: %s %s %v", status, lease, err)
	}
	if err := run("up", up); err != nil {
		t.Fatalf("guard migration replay: %v", err)
	}
	var dbErr *pgconn.PgError
	if err := run("down", down); !errors.As(err, &dbErr) || dbErr.Code != "55000" {
		t.Fatalf("rollback exposed pending source: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE webhook_delivery SET status='dispatched',lease_token=NULL WHERE id='frozen'`); err != nil {
		t.Fatal(err)
	}
	if err := run("down", down); err != nil {
		t.Fatalf("drained rollback: %v", err)
	}
	if err := run("up", up); err != nil {
		t.Fatalf("forward recovery: %v", err)
	}
}
