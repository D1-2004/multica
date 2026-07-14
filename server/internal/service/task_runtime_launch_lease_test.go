package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestFCE2BFailLaunchDoesNotFailTaskAfterLeaseLoss(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errRuntimeLaunchLeaseLost)

	launcher := &FCE2BLauncher{}
	err := launcher.failLaunch(ctx, db.AgentTaskQueue{}, "runner stopped")
	if !errors.Is(err, errRuntimeLaunchLeaseLost) {
		t.Fatalf("failLaunch error = %v, want runtime launch lease loss", err)
	}
}

func TestPostgresTaskRuntimeLaunchLeaseIsFenced(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	if _, err := tx.Exec(ctx, `
		ALTER TABLE agent_task_queue
		  ADD COLUMN IF NOT EXISTS runtime_launch_lease_token UUID,
		  ADD COLUMN IF NOT EXISTS runtime_launch_lease_expires_at TIMESTAMPTZ
	`); err != nil {
		t.Fatalf("prepare lease columns: %v", err)
	}

	suffix := time.Now().UnixNano()
	var userID, workspaceID, runtimeID, agentID, issueID, taskID pgtype.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('Runtime Lease Tester', $1)
		RETURNING id
	`, fmt.Sprintf("runtime-lease-%d@multica.ai", suffix)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Runtime Lease Tests', $1, '', 'RLT')
		RETURNING id
	`, fmt.Sprintf("runtime-lease-%d", suffix)).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, 'runtime-lease-daemon', 'Runtime Lease Runtime', 'cloud',
			'test', 'online', '', '{"kind":"fc-e2b"}'::jsonb, now(), 'private', $2)
		RETURNING id
	`, workspaceID, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Runtime Lease Agent', '', 'cloud', '{}'::jsonb,
			$2, 'private', 1, $3)
		RETURNING id
	`, workspaceID, runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_id, creator_type,
			number, position
		)
		VALUES ($1, 'Runtime lease issue', 'in_progress', 'none', $2,
			'member', 1, 0)
		RETURNING id
	`, workspaceID, userID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority
		)
		VALUES ($1, $2, $3, 'queued', 0)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create task: %v", err)
	}

	store := newPostgresTaskRuntimeLaunchLeaseStore(db.New(tx))
	first, acquired, err := store.Acquire(ctx, taskID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first acquire: acquired=%v err=%v", acquired, err)
	}
	if _, acquired, err := store.Acquire(ctx, taskID, time.Minute); err != nil || acquired {
		t.Fatalf("second acquire while held: acquired=%v err=%v", acquired, err)
	}

	forged := first
	forged.token = testUUID(99)
	if err := store.Release(ctx, forged); err != nil {
		t.Fatalf("forged release: %v", err)
	}
	if _, acquired, err := store.Acquire(ctx, taskID, time.Minute); err != nil || acquired {
		t.Fatalf("acquire after forged release: acquired=%v err=%v", acquired, err)
	}
	if _, renewed, err := store.Renew(ctx, forged, time.Minute); err != nil || renewed {
		t.Fatalf("forged renew: renewed=%v err=%v", renewed, err)
	}
	if _, renewed, err := store.Renew(ctx, first, time.Minute); err != nil || !renewed {
		t.Fatalf("owner renew: renewed=%v err=%v", renewed, err)
	}

	if err := store.Release(ctx, first); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if _, acquired, err := store.Acquire(ctx, taskID, time.Minute); err != nil || !acquired {
		t.Fatalf("acquire after owner release: acquired=%v err=%v", acquired, err)
	}
}
