package evalreport

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests never inherit DATABASE_URL or connect to an ambient application DB.
// The explicit opt-in URL must point to a disposable, authorized test database.
func postgresFixture(t *testing.T) (*pgxpool.Pool, *Store) {
	t.Helper()
	databaseURL := os.Getenv("EVAL_REPORT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EVAL_REPORT_TEST_DATABASE_URL required for PostgreSQL eval-report integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal("connect to explicitly configured eval-report test database")
	}
	schema := "evalreport_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal("create isolated eval-report schema")
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); err != nil {
			t.Error("drop isolated eval-report schema")
		}
		_ = admin.Close(context.Background())
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("parse explicit eval-report test URL")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("create eval-report test pool")
	}
	t.Cleanup(pool.Close)
	for _, sql := range []string{
		`CREATE TABLE workspace(id uuid NOT NULL,name text NOT NULL)`,
		`CREATE UNIQUE INDEX CONCURRENTLY workspace_id_idx ON workspace(id)`,
		`CREATE TABLE member(id uuid NOT NULL,workspace_id uuid NOT NULL,user_id uuid NOT NULL)`,
		`CREATE UNIQUE INDEX CONCURRENTLY member_id_idx ON member(id)`,
		`CREATE UNIQUE INDEX CONCURRENTLY member_user_workspace_idx ON member(user_id,workspace_id)`,
		`CREATE TABLE eval_report(id uuid NOT NULL,workspace_id uuid NOT NULL,submitted_by uuid NOT NULL,run_id uuid NOT NULL,content_sha256 text NOT NULL,definition_sha256 text NOT NULL,submission jsonb NOT NULL,summary jsonb NOT NULL,received_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE UNIQUE INDEX CONCURRENTLY eval_report_id_idx ON eval_report(id)`,
		`CREATE UNIQUE INDEX CONCURRENTLY eval_report_workspace_run_idx ON eval_report(workspace_id,run_id)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal("create isolated eval-report test tables")
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace(id,name)VALUES($1::uuid,'Isolated evaluation project')`, testWorkspace); err != nil {
		t.Fatal("create test workspace")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member(id,workspace_id,user_id)VALUES($1::uuid,$2::uuid,$3::uuid)`, uuid.NewString(), testWorkspace, testUser); err != nil {
		t.Fatal("create test member")
	}
	return pool, NewStore(pool, pool)
}

func TestPostgresConcurrentSameRunIsImmutable(t *testing.T) {
	pool, store := postgresFixture(t)
	ctx := context.Background()
	const callers = 8
	type result struct {
		record   Record
		replayed bool
		err      error
	}
	results := make(chan result, callers)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range callers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			r, replayed, err := store.Submit(ctx, testWorkspace, testUser, exampleSubmission())
			results <- result{r, replayed, err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	first := 0
	reportID := ""
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent submission: %v", result.err)
		}
		if !result.replayed {
			first++
		}
		if reportID == "" {
			reportID = result.record.ID
		}
		if result.record.ID != reportID {
			t.Fatal("same run created more than one report")
		}
	}
	if first != 1 {
		t.Fatalf("first submissions: %d", first)
	}
	changed := exampleSubmission()
	changed.Results[0].Summary = "Different actual observation"
	if _, _, err := store.Submit(ctx, testWorkspace, testUser, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("different content did not conflict: %v", err)
	}
	detail, err := store.Get(ctx, testWorkspace, testUser, reportID)
	if err != nil || detail.Submission.Results[1].Summary == changed.Results[0].Summary {
		t.Fatalf("immutable report was overwritten: %v", err)
	}
	reports, more, err := store.List(ctx, testWorkspace, testUser, "", 1)
	if err != nil || more || len(reports) != 1 || len(reports[0].Submission.SelectedCases) != 0 || len(reports[0].Submission.Results) != 0 {
		t.Fatalf("metadata listing: %+v %v", reports, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1::uuid AND user_id=$2::uuid`, testWorkspace, testUser); err != nil {
		t.Fatal("revoke test membership")
	}
	if _, err := store.Get(ctx, testWorkspace, testUser, reportID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked member read report: %v", err)
	}
	visible, err := store.ListVisible(ctx, testUser, 10)
	if err != nil || len(visible) != 0 {
		t.Fatalf("revoked visible list: %+v %v", visible, err)
	}
}

func TestPostgresMemberRemovalWinsBeforeSubmission(t *testing.T) {
	pool, store := postgresFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal("begin revocation")
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1::uuid AND user_id=$2::uuid`, testWorkspace, testUser); err != nil {
		t.Fatal("lock removed member")
	}
	result := make(chan error, 1)
	go func() { _, _, err := store.Submit(ctx, testWorkspace, testUser, exampleSubmission()); result <- err }()
	select {
	case err := <-result:
		t.Fatalf("submission escaped pending revocation row lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal("commit revocation")
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("submission after revocation: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("submission did not finish after revocation")
	}
	var count int
	if pool.QueryRow(ctx, `SELECT count(*) FROM eval_report`).Scan(&count) != nil || count != 0 {
		t.Fatal("revoked member inserted an orphan report")
	}
}

func TestPostgresWorkspaceRemovalWinsBeforeSubmission(t *testing.T) {
	pool, store := postgresFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal("begin workspace teardown")
	}
	defer tx.Rollback(context.Background())
	var id string
	if tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR UPDATE`, testWorkspace).Scan(&id) != nil {
		t.Fatal("lock workspace before dependent cleanup")
	}
	result := make(chan error, 1)
	go func() { _, _, err := store.Submit(ctx, testWorkspace, testUser, exampleSubmission()); result <- err }()
	select {
	case err := <-result:
		t.Fatalf("submission escaped workspace teardown lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	for _, sql := range []string{`DELETE FROM eval_report WHERE workspace_id=$1::uuid`, `DELETE FROM member WHERE workspace_id=$1::uuid`, `DELETE FROM workspace WHERE id=$1::uuid`} {
		if _, err := tx.Exec(ctx, sql, testWorkspace); err != nil {
			t.Fatal("delete isolated workspace data")
		}
	}
	if tx.Commit(ctx) != nil {
		t.Fatal("commit workspace teardown")
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("submission after workspace deletion: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("submission did not finish after workspace deletion")
	}
	var count int
	if pool.QueryRow(ctx, `SELECT count(*) FROM eval_report`).Scan(&count) != nil || count != 0 {
		t.Fatal("deleted workspace acquired an orphan report")
	}
}
