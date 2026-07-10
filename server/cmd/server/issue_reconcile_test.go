package main

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// setupLostCompletionFixture creates an in_progress agent issue whose latest
// task completed at the given SQL interval expression in the past.
func setupLostCompletionFixture(t *testing.T, completedAgo string) (string, string, string) {
	t.Helper()
	ctx := context.Background()

	var agentID, runtimeID string
	err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id
		JOIN "user" u ON u.id = m.user_id
		WHERE u.email = $1
		LIMIT 1
	`, integrationTestEmail).Scan(&agentID, &runtimeID)
	if err != nil {
		t.Fatalf("failed to find test agent: %v", err)
	}

	var issueID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id)
		SELECT $1, 'Lost completion test issue', 'in_progress', 'none', 'member', m.user_id, 'agent', $2
		FROM member m WHERE m.workspace_id = $1 LIMIT 1
		RETURNING id
	`, testWorkspaceID, agentID).Scan(&issueID)
	if err != nil {
		t.Fatalf("failed to create test issue: %v", err)
	}

	var taskID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at, completed_at)
		VALUES ($1, $2, $3, 'completed', 0, now() - interval '1 hour', now() - interval '1 hour', now() - `+completedAgo+`::interval)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID)
	if err != nil {
		t.Fatalf("failed to create completed task: %v", err)
	}

	return issueID, agentID, taskID
}

func issueStatus(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("failed to read issue status: %v", err)
	}
	return status
}

func TestReconcileIssuesWithLostCompletion(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	taskSvc := service.NewTaskService(queries, testPool, nil, bus)

	issueID, agentID, _ := setupLostCompletionFixture(t, "'30 minutes'")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })

	if n := taskSvc.ReconcileIssuesWithLostCompletion(ctx, 900, 100); n < 1 {
		t.Fatalf("reconciled %d issues, want >= 1", n)
	}
	if got := issueStatus(t, issueID); got != "in_review" {
		t.Fatalf("issue status = %q, want in_review", got)
	}
}

func TestReconcileIssuesWithLostCompletionRespectsGrace(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	taskSvc := service.NewTaskService(queries, testPool, nil, bus)

	// Completed only a minute ago: inside the grace window, must stay put.
	issueID, agentID, _ := setupLostCompletionFixture(t, "'1 minute'")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })

	taskSvc.ReconcileIssuesWithLostCompletion(ctx, 900, 100)
	if got := issueStatus(t, issueID); got != "in_progress" {
		t.Fatalf("issue inside grace window moved to %q, want in_progress", got)
	}
}

func TestReconcileIssuesWithLostCompletionSkipsActiveTasks(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	taskSvc := service.NewTaskService(queries, testPool, nil, bus)

	issueID, agentID, _ := setupLostCompletionFixture(t, "'30 minutes'")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })

	// A newer queued task means the issue is legitimately still in flight.
	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("failed to read runtime id: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
	`, agentID, runtimeID, issueID); err != nil {
		t.Fatalf("failed to create queued task: %v", err)
	}

	taskSvc.ReconcileIssuesWithLostCompletion(ctx, 900, 100)
	if got := issueStatus(t, issueID); got != "in_progress" {
		t.Fatalf("issue with active task moved to %q, want in_progress", got)
	}
}

func TestReconcileIssuesWithLostCompletionSkipsFailedLatest(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	taskSvc := service.NewTaskService(queries, testPool, nil, bus)

	issueID, agentID, taskID := setupLostCompletionFixture(t, "'30 minutes'")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })

	// Latest task failed → HandleFailedTasks owns the rollback, not us.
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'failed' WHERE id = $1`, taskID); err != nil {
		t.Fatalf("failed to mark task failed: %v", err)
	}

	taskSvc.ReconcileIssuesWithLostCompletion(ctx, 900, 100)
	if got := issueStatus(t, issueID); got != "in_progress" {
		t.Fatalf("issue with failed latest task moved to %q, want in_progress", got)
	}
}
