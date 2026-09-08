package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestListRunnerBindingsForOwnerIncludesMachineWithoutMount(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	conn, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	defer conn.Close(context.Background())
	if err := conn.Ping(context.Background()); err != nil {
		t.Skipf("database unavailable: %v", err)
	}

	ctx := context.Background()
	schemaName := "runner_query_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaSQL := pgx.Identifier{schemaName}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schemaSQL); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+schemaSQL+" CASCADE")
	})
	if _, err := conn.Exec(ctx, "SET search_path TO "+schemaSQL); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		CREATE TABLE workspace (id uuid PRIMARY KEY, name text NOT NULL, slug text NOT NULL);
		CREATE TABLE agent (id uuid PRIMARY KEY, workspace_id uuid NOT NULL, name text NOT NULL);
		CREATE TABLE runner_machine (
			id uuid PRIMARY KEY, owner_id uuid NOT NULL, name text NOT NULL, os text NOT NULL,
			arch text NOT NULL, client_version text NOT NULL, last_seen_at timestamptz,
			connection_id uuid, revoked_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE TABLE agent_runner_binding (
			id uuid PRIMARY KEY, workspace_id uuid NOT NULL, agent_id uuid NOT NULL,
			machine_id uuid NOT NULL, roots jsonb NOT NULL, disconnected_at timestamptz,
			revoked_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
		);
	`); err != nil {
		t.Fatalf("create query tables: %v", err)
	}

	ownerID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	machineID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err := conn.Exec(ctx, `
		INSERT INTO runner_machine (id, owner_id, name, os, arch, client_version)
		VALUES ($1, $2, 'Unmounted query machine', 'darwin', 'arm64', 'test')
	`, machineID, ownerID); err != nil {
		t.Fatalf("seed machine: %v", err)
	}

	rows, err := New(conn).ListRunnerBindingsForOwner(ctx, ownerID)
	if err != nil {
		t.Fatalf("list owner machines: %v", err)
	}
	if len(rows) != 1 || rows[0].MachineID != machineID {
		t.Fatalf("owner machines = %#v, want unmounted machine %s", rows, machineID)
	}
}

func TestCreateRunnerMCPCallRequiresEnabledFingerprint(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" { databaseURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable" }
	conn, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil { t.Skipf("database unavailable: %v", err) }
	defer conn.Close(context.Background())
	ctx := context.Background()
	schemaName := "runner_call_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaSQL := pgx.Identifier{schemaName}.Sanitize()
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+schemaSQL); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "DROP SCHEMA "+schemaSQL+" CASCADE") })
	if _, err = conn.Exec(ctx, "SET search_path TO "+schemaSQL); err != nil { t.Fatal(err) }
	if _, err = conn.Exec(ctx, `
		CREATE TABLE runner_machine (id uuid PRIMARY KEY, revoked_at timestamptz);
		CREATE TABLE agent_runner_binding (
			id uuid PRIMARY KEY, workspace_id uuid NOT NULL, agent_id uuid NOT NULL,
			machine_id uuid NOT NULL, roots jsonb NOT NULL DEFAULT '[]', enabled_mcp_servers jsonb NOT NULL DEFAULT '{}',
			disconnected_at timestamptz, revoked_at timestamptz
		);
		CREATE TABLE runner_call (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL, agent_id uuid NOT NULL,
			task_id uuid NOT NULL, user_id uuid NOT NULL, machine_id uuid NOT NULL, tool_name varchar(64) NOT NULL,
			arguments jsonb NOT NULL, roots jsonb NOT NULL, result jsonb, status text NOT NULL DEFAULT 'queued',
			error_code text, error_message text, expires_at timestamptz NOT NULL, started_at timestamptz,
			completed_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
		);
	`); err != nil { t.Fatalf("create tables: %v", err) }
	workspaceID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	agentID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	machineID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err = conn.Exec(ctx, `INSERT INTO runner_machine (id) VALUES ($1)`, machineID); err != nil { t.Fatal(err) }
	if _, err = conn.Exec(ctx, `INSERT INTO agent_runner_binding (id,workspace_id,agent_id,machine_id,enabled_mcp_servers) VALUES ($1,$2,$3,$4,'{"local":"sha256:current"}')`, uuid.New(), workspaceID, agentID, machineID); err != nil { t.Fatal(err) }
	base := CreateRunnerCallParams{WorkspaceID: workspaceID, AgentID: agentID, TaskID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, UserID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, MachineID: machineID, ToolName: "mcp", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true}}
	base.Arguments = []byte(`{"server_name":"local","fingerprint":"sha256:stale"}`)
	if _, err = New(conn).CreateRunnerCall(ctx, base); !errors.Is(err, pgx.ErrNoRows) { t.Fatalf("stale fingerprint error = %v", err) }
	base.Arguments = []byte(`{"server_name":"local","fingerprint":"sha256:current"}`)
	if _, err = New(conn).CreateRunnerCall(ctx, base); err != nil { t.Fatalf("enabled fingerprint rejected: %v", err) }
}
