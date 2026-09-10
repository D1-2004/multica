package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type coordinatorExecutionQueryStub struct {
	t    *testing.T
	sql  string
	args []any
}

func (s *coordinatorExecutionQueryStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	s.t.Fatal("latest execution must not execute a write")
	return pgconn.CommandTag{}, nil
}
func (s *coordinatorExecutionQueryStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	s.t.Fatal("latest execution must not fetch an unbounded list")
	return nil, nil
}
func (s *coordinatorExecutionQueryStub) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	s.sql = sql
	s.args = args
	return coordinatorExecutionRow{}
}

type coordinatorExecutionRow struct{}

func (coordinatorExecutionRow) Scan(dest ...any) error {
	*dest[0].(*pgtype.UUID) = pgtype.UUID{Bytes: [16]byte{4}, Valid: true}
	*dest[1].(*string) = "completed"
	for _, d := range dest[2:] {
		*d.(*pgtype.Timestamptz) = pgtype.Timestamptz{Time: time.Date(2026, 9, 8, 13, 19, 0, 0, time.UTC), Valid: true}
	}
	return nil
}
func TestLatestCoordinatorExecutionQueryIsScopedReadOnlyAndBounded(t *testing.T) {
	stub := &coordinatorExecutionQueryStub{t: t}
	args := GetLatestCoordinatorIssueExecutionParams{WorkspaceID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, IssueID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, AgentID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}}
	row, err := New(stub).GetLatestCoordinatorIssueExecution(context.Background(), args)
	if err != nil || row.Status != "completed" || !row.CompletedAt.Valid {
		t.Fatalf("metadata scan failed: %#v %v", row, err)
	}
	if len(stub.args) != 3 || stub.args[0] != args.WorkspaceID || stub.args[1] != args.IssueID || stub.args[2] != args.AgentID {
		t.Fatalf("scope arguments changed: %#v", stub.args)
	}
	for _, required := range []string{"current_issue.workspace_id = $1", "current_agent.workspace_id = $1", "current_issue.id = $2", "task.agent_id = $3", "current_issue.assignee_type = 'agent'", "current_issue.assignee_id = $3", "ORDER BY task.created_at DESC, task.id DESC", "LIMIT 1"} {
		if !strings.Contains(stub.sql, required) {
			t.Fatalf("query lost required scope/order: %s", required)
		}
	}
	for _, forbidden := range []string{"SELECT *", "task.result", "task.context", "failure_reason", "FOR UPDATE", "UPDATE ", "INSERT ", "DELETE "} {
		if strings.Contains(stub.sql, forbidden) {
			t.Fatalf("query acquired payload or mutation: %s", forbidden)
		}
	}
}
