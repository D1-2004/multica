package dshschedule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This test needs an explicitly selected real preproduction database. Default
// local runs only compile this harness and skip before attempting any connection.
func schedulePools(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("DSH_SCHEDULE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires an explicitly configured real preproduction test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("cannot connect to configured test database")
	}
	schema := "dsh_schedule_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
		if err := admin.Close(cleanup); err != nil {
			t.Error(err)
		}
	})
	pool := func() *pgxpool.Pool {
		config, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatal("invalid test database configuration")
		}
		config.ConnConfig.RuntimeParams["search_path"] = schema
		config.MaxConns = 4
		result, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(result.Close)
		return result
	}
	a, b := pool(), pool()
	for _, stem := range []string{"9248_dsh_schedule", "9249_dsh_schedule_identity", "9250_dsh_schedule_due", "9251_dsh_schedule_occurrence_identity", "9252_dsh_schedule_retry", "9253_dsh_schedule_retry_scan"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", stem+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	// The admission callback is intentionally a database fixture. This validates
	// the ledger's transaction boundary, not application authorization or delivery.
	if _, err := a.Exec(ctx, `CREATE TABLE dsh_task_binding (
 workspace_id uuid,agent_id uuid,task_id uuid,session_id text,request_id uuid);
 CREATE TABLE admitted_task (id uuid)`); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func inTransaction(ctx context.Context, pool *pgxpool.Pool, fn func(Store) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(Store{Tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func enqueueFixture(ctx context.Context, tx pgx.Tx, due Due) (uuid.UUID, error) {
	id := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO admitted_task(id) VALUES($1)`, id); err != nil {
		return uuid.Nil, err
	}
	_, err := tx.Exec(ctx, `INSERT INTO dsh_task_binding(workspace_id,agent_id,session_id,request_id,task_id)
 VALUES($1,$2,$3,$4,$5)`, due.WorkspaceID, due.AgentID, due.SessionID, due.RequestID, id)
	return id, err
}

func TestPostgresScheduleAtomicAdmission(t *testing.T) {
	a, b := schedulePools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := fixture()
	if err := a.QueryRow(ctx, `SELECT date_trunc('milliseconds',clock_timestamp())-interval '1 hour'`).Scan(&r.FirstDue); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, `INSERT INTO dsh_task_binding(workspace_id,agent_id,session_id,request_id,task_id)
 VALUES($1,$2,$3,$4,$5)`, r.WorkspaceID, r.AgentID, r.SessionID, uuid.New(), r.SourceTaskID); err != nil {
		t.Fatal(err)
	}
	register := func(s Store) error { _, err := s.Register(ctx, r); return err }
	if err := inTransaction(ctx, a, register); err != nil {
		t.Fatal(err)
	}
	// The task and receipt must both disappear when a later transaction step
	// fails. Retrying after the rollback must keep the same occurrence identity.
	forced := errors.New("forced transaction failure")
	var rolledBack Receipt
	err := inTransaction(ctx, a, func(s Store) error {
		var err error
		rolledBack, err = s.Dispatch(ctx, r.Key, enqueueFixture)
		if err != nil {
			return err
		}
		return forced
	})
	if !errors.Is(err, forced) {
		t.Fatal(err)
	}
	assertCounts := func(tasks, receipts int) {
		t.Helper()
		var gotTasks, gotReceipts int
		if err := a.QueryRow(ctx, `SELECT (SELECT count(*) FROM admitted_task),
 (SELECT count(*) FROM dsh_schedule_occurrence)`).Scan(&gotTasks, &gotReceipts); err != nil {
			t.Fatal(err)
		}
		if gotTasks != tasks || gotReceipts != receipts {
			t.Fatalf("tasks=%d receipts=%d, want %d/%d", gotTasks, gotReceipts, tasks, receipts)
		}
	}
	assertCounts(0, 0)
	type result struct {
		receipt Receipt
		err     error
	}
	results := make(chan result, 8)
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			var receipt Receipt
			err := inTransaction(ctx, []*pgxpool.Pool{a, b}[i%2], func(s Store) error {
				var err error
				receipt, err = s.Dispatch(ctx, r.Key, enqueueFixture)
				return err
			})
			results <- result{receipt, err}
		}(i)
	}
	workers.Wait()
	close(results)
	winners := 0
	for outcome := range results {
		if outcome.err == nil {
			winners++
			if outcome.receipt.RequestID != rolledBack.RequestID || outcome.receipt.TaskID == r.SourceTaskID {
				t.Fatal("retry changed request identity or reused parent task")
			}
		} else if !errors.Is(outcome.err, ErrNotDue) {
			t.Fatal(outcome.err)
		}
	}
	if winners != 1 {
		t.Fatalf("replicas admitted %d tasks", winners)
	}
	assertCounts(1, 1)
	if err := inTransaction(ctx, b, register); err != nil {
		t.Fatal("lost registration response could not be retried", err)
	}
	err = inTransaction(ctx, b, func(s Store) error { _, err := s.Dispatch(ctx, r.Key, enqueueFixture); return err })
	if !errors.Is(err, ErrNotDue) {
		t.Fatal("registration replay reactivated completed reminder", err)
	}
	assertCounts(1, 1)
	changed := r
	changed.Prompt = "different reminder"
	err = inTransaction(ctx, b, func(s Store) error { _, err := s.Register(ctx, changed); return err })
	if !errors.Is(err, ErrConflict) {
		t.Fatal("registration retry replaced immutable content", err)
	}
}

func TestPostgresScheduleCancellationAndBindingFence(t *testing.T) {
	a, b := schedulePools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := fixture()
	if err := a.QueryRow(ctx, `SELECT date_trunc('milliseconds',clock_timestamp())-interval '1 hour'`).Scan(&r.FirstDue); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, `INSERT INTO dsh_task_binding(workspace_id,agent_id,session_id,request_id,task_id)
 VALUES($1,$2,$3,$4,$5)`, r.WorkspaceID, r.AgentID, r.SessionID, uuid.New(), r.SourceTaskID); err != nil {
		t.Fatal(err)
	}
	if err := inTransaction(ctx, a, func(s Store) error { _, err := s.Register(ctx, r); return err }); err != nil {
		t.Fatal(err)
	}
	err := inTransaction(ctx, a, func(s Store) error {
		_, err := s.Dispatch(ctx, r.Key, func(ctx context.Context, tx pgx.Tx, due Due) (uuid.UUID, error) {
			due.SessionID = uuid.NewString()
			return enqueueFixture(ctx, tx, due)
		})
		return err
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("admitted a task bound to a different Session", err)
	}
	// Hold an uncommitted cancellation. A second replica skips the locked
	// record, then still refuses it after the cancellation commits.
	tx, err := a.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if found, err := (Store{Tx: tx}).Cancel(ctx, r.Key); err != nil || !found {
		t.Fatal("cancel failed", err)
	}
	check := func(s Store) error { _, err := s.Dispatch(ctx, r.Key, enqueueFixture); return err }
	if err := inTransaction(ctx, b, check); !errors.Is(err, ErrNotDue) {
		t.Fatal("dispatch overtook cancellation", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := inTransaction(ctx, b, func(s Store) error { _, err := s.Register(ctx, r); return err }); err != nil {
		t.Fatal(err)
	}
	if err := inTransaction(ctx, b, check); !errors.Is(err, ErrNotDue) {
		t.Fatal("create replay resurrected cancellation", err)
	}
	var count int
	if err := a.QueryRow(ctx, `SELECT count(*) FROM admitted_task`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed admission left a task", err)
	}
}

func TestPostgresScheduleRetryDoesNotStarveOrDelayAdvancedOccurrence(t *testing.T) {
	a, b := schedulePools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := fixture()
	r.EverySeconds = 300
	if err := a.QueryRow(ctx, `SELECT date_trunc('milliseconds',clock_timestamp())-interval '1 hour'`).Scan(&r.FirstDue); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, `INSERT INTO dsh_task_binding(workspace_id,agent_id,session_id,request_id,task_id) VALUES($1,$2,$3,$4,$5)`, r.WorkspaceID, r.AgentID, r.SessionID, uuid.New(), r.SourceTaskID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"schedule-1", "schedule-2"} {
		record := r
		record.ScheduleID = id
		if err := inTransaction(ctx, a, func(s Store) error { _, err := s.Register(ctx, record); return err }); err != nil {
			t.Fatal(err)
		}
	}
	q := Queue{DB: a}
	rows, err := q.Candidates(ctx, 1)
	if err != nil || len(rows) != 1 || rows[0].ScheduleID != "schedule-1" {
		t.Fatal(rows, err)
	}
	old := rows[0]
	if err := q.Defer(ctx, old); err != nil {
		t.Fatal(err)
	}
	rows, err = q.Candidates(ctx, 1)
	if err != nil || len(rows) != 1 || rows[0].ScheduleID != "schedule-2" {
		t.Fatal("failed oldest reminder starved next", rows, err)
	}
	if err := inTransaction(ctx, b, func(s Store) error { _, err := s.Dispatch(ctx, old.Key, enqueueFixture); return err }); !errors.Is(err, ErrNotDue) {
		t.Fatal("dispatch bypassed retry time", err)
	}
	if _, err := a.Exec(ctx, `UPDATE dsh_schedule SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE schedule_id='schedule-1'`); err != nil {
		t.Fatal(err)
	}
	if err := inTransaction(ctx, b, func(s Store) error { _, err := s.Dispatch(ctx, old.Key, enqueueFixture); return err }); err != nil {
		t.Fatal(err)
	}
	// A delayed error from a losing replica must not defer the winner's next
	// periodic occurrence, or reintroduce the prior failure count.
	if err := q.Defer(ctx, old); err != nil {
		t.Fatal(err)
	}
	var reset bool
	if err := a.QueryRow(ctx, `SELECT next_attempt_at IS NULL AND failure_count=0 AND next_due_at>$1 FROM dsh_schedule WHERE schedule_id='schedule-1'`, old.NextDue).Scan(&reset); err != nil || !reset {
		t.Fatal("stale failure changed advanced record", err)
	}
}
