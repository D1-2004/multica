package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDSHTrajectoryPersistenceRequiresTransaction(t *testing.T) {
	if _, err := (&Handler{}).persistDSHTrajectory(context.Background(), pgtype.UUID{}, db.AgentTaskQueue{}, db.PutAgentTaskDSHTrajectoryParams{}); err == nil {
		t.Fatal("unconfigured persistence accepted")
	}
}

// Run with the unit-only handler entry and an explicit actual preproduction URL.
// This private schema uses separate connections; it never starts a database.
func TestDSHTrajectoryDatabaseDeletionAndUploadRace(t *testing.T) {
	url := os.Getenv("DSH_TRAJECTORY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("explicit preproduction trajectory database is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "dsh_trajectory_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE workspace(id uuid PRIMARY KEY);
 CREATE TABLE agent(id uuid PRIMARY KEY,workspace_id uuid NOT NULL,runtime_id uuid,kind text DEFAULT 'user',system_key text);
 CREATE TABLE agent_runtime(id uuid PRIMARY KEY,workspace_id uuid NOT NULL,provider text DEFAULT 'dsh',status text DEFAULT 'offline',last_seen_at timestamptz DEFAULT now()-interval '90 days');
 CREATE TABLE issue(id uuid PRIMARY KEY,workspace_id uuid NOT NULL);
 CREATE TABLE issue_vcs_pull_request(issue_id uuid);
 CREATE TABLE agent_task_queue(id uuid PRIMARY KEY,agent_id uuid NOT NULL,runtime_id uuid NOT NULL,issue_id uuid);`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"9064_agent_task_dsh_trajectory.up.sql", "9256_dsh_trajectory_application_cleanup.up.sql", "9256_dsh_trajectory_application_cleanup.up.sql", "9256_dsh_trajectory_application_cleanup.down.sql"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(name, err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='agent_task_dsh_trajectory'::regclass AND contype='f'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("foreign key was retained", count, err)
	}
	id := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
	workspace, other, agentID, runtimeID, taskID, issueID := id(), id(), id(), id(), id(), id()
	for _, ws := range []pgtype.UUID{workspace, other} {
		if _, err := pool.Exec(ctx, `INSERT INTO workspace VALUES($1)`, ws); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent(id,workspace_id) VALUES($1,$2)`, agentID, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_runtime(id,workspace_id) VALUES($1,$2)`, runtimeID, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO issue VALUES($1,$2)`, issueID, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,issue_id) VALUES($1,$2,$3,$4)`, taskID, agentID, runtimeID, issueID); err != nil {
		t.Fatal(err)
	}
	h := &Handler{TxStarter: pool}
	task := db.AgentTaskQueue{ID: taskID, AgentID: agentID, RuntimeID: runtimeID, IssueID: issueID}
	input := db.PutAgentTaskDSHTrajectoryParams{TaskID: taskID, SessionID: "session", StorageKey: "fixture.enc", EncryptionScheme: dshTrajectoryEncryptionScheme, EncryptionKey: make([]byte, 32), Sha256: strings.Repeat("a", 64), SizeBytes: 10, StoredSizeBytes: 38, EventCount: 1, FormatVersion: 3}
	for _, parent := range []struct {
		table string
		id    pgtype.UUID
	}{{"workspace", workspace}, {"agent_runtime", runtimeID}, {"issue", issueID}, {"agent", agentID}, {"agent_task_queue", taskID}} {
		t.Run("locks_"+parent.table, func(t *testing.T) {
			held, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Rollback(ctx)
			if _, err := held.Exec(ctx, `SELECT id FROM `+pgx.Identifier{parent.table}.Sanitize()+` WHERE id=$1 FOR UPDATE`, parent.id); err != nil {
				t.Fatal(err)
			}
			blocked, stop := context.WithTimeout(ctx, 150*time.Millisecond)
			defer stop()
			if _, err := h.persistDSHTrajectory(blocked, workspace, task, input); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("upload crossed the deletion lock", err)
			}
		})
	}
	// GC must retain an offline DSH runtime with tasks even before its first
	// trajectory upload, while an empty stale runtime is still collectable.
	emptyRuntime := id()
	if _, err := pool.Exec(ctx, `INSERT INTO agent_runtime(id,workspace_id) VALUES($1,$2)`, emptyRuntime, workspace); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.New(pool).DeleteStaleOfflineRuntimes(ctx, 60)
	if err != nil || len(deleted) != 1 || deleted[0].ID != emptyRuntime {
		t.Fatal("runtime GC lost DSH task history or retained an empty runtime", deleted, err)
	}
	stored, err := h.persistDSHTrajectory(ctx, workspace, task, input)
	if err != nil || stored.StorageKey != input.StorageKey {
		t.Fatal("upload after rollback failed", err)
	}
	if _, err := h.persistDSHTrajectory(ctx, other, task, input); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-workspace write accepted", err)
	}
	input.StorageKey = "duplicate.enc"
	stored, err = h.persistDSHTrajectory(ctx, workspace, task, input)
	if err != nil || stored.StorageKey != "fixture.enc" {
		t.Fatal("retry replaced ciphertext identity", err)
	}
	// Exercise the actual cleanup queries, including their tenant/kind guards.
	queries := db.New(pool)
	if err := queries.DeleteIssue(ctx, db.DeleteIssueParams{ID: issueID, WorkspaceID: other}); err != nil {
		t.Fatal(err)
	}
	if err := queries.DeleteSystemAgentByID(ctx, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.GetAgentTaskDSHTrajectory(ctx, taskID); err != nil {
		t.Fatal("foreign issue or user agent cleanup removed the trajectory", err)
	}
	if err := queries.DeleteIssue(ctx, db.DeleteIssueParams{ID: issueID, WorkspaceID: workspace}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.GetAgentTaskDSHTrajectory(ctx, taskID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("issue cleanup left a trajectory", err)
	}
	if _, err := h.persistDSHTrajectory(ctx, workspace, task, input); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("deleted issue accepted a late upload", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO issue VALUES($1,$2)`, issueID, workspace); err != nil {
		t.Fatal(err)
	}
	for _, byRuntime := range []bool{false, true} {
		builder, builderTask := id(), id()
		if _, err := pool.Exec(ctx, `INSERT INTO agent(id,workspace_id,runtime_id,kind,system_key) VALUES($1,$2,$3,'system','agent_builder:fixture')`, builder, workspace, runtimeID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id) VALUES($1,$2,$3)`, builderTask, builder, runtimeID); err != nil {
			t.Fatal(err)
		}
		builderInput := input
		builderInput.TaskID = builderTask
		if _, err := h.persistDSHTrajectory(ctx, workspace, db.AgentTaskQueue{ID: builderTask, AgentID: builder, RuntimeID: runtimeID}, builderInput); err != nil {
			t.Fatal(err)
		}
		if byRuntime {
			err = queries.DeleteSystemAgentsByRuntime(ctx, runtimeID)
		} else {
			err = queries.DeleteSystemAgentByID(ctx, builder)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := queries.GetAgentTaskDSHTrajectory(ctx, builderTask); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("builder cleanup left a trajectory", byRuntime, err)
		}
	}
	if _, err := h.persistDSHTrajectory(ctx, workspace, task, input); err != nil {
		t.Fatal(err)
	}
	deletion, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer deletion.Rollback(ctx)
	if _, err := deletion.Exec(ctx, `SELECT id FROM workspace WHERE id=$1 FOR UPDATE`, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := deletion.Exec(ctx, deleteWorkspaceDSHTrajectories, other); err != nil {
		t.Fatal(err)
	}
	if err := deletion.QueryRow(ctx, `SELECT count(*) FROM agent_task_dsh_trajectory`).Scan(&count); err != nil || count != 1 {
		t.Fatal("foreign cleanup removed data", err)
	}
	if _, err := deletion.Exec(ctx, deleteWorkspaceDSHTrajectories, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := deletion.Exec(ctx, `DELETE FROM agent_task_queue WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := deletion.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.persistDSHTrajectory(ctx, workspace, task, input); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("deleted task accepted late upload", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_dsh_trajectory`).Scan(&count); err != nil || count != 0 {
		t.Fatal("orphaned metadata remained", err)
	}
}
