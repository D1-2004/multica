package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type recentCoordinatorQueryStub struct {
	t    *testing.T
	sql  string
	args []any
}

func (s *recentCoordinatorQueryStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	s.t.Fatal("coordination_state must never write")
	return pgconn.CommandTag{}, nil
}
func (s *recentCoordinatorQueryStub) QueryRow(context.Context, string, ...any) pgx.Row {
	s.t.Fatal("unexpected query method")
	return nil
}
func (s *recentCoordinatorQueryStub) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	s.sql = sql
	s.args = args
	return nil, errors.New("read stopped by fixture")
}

func TestRecentCoordinatorStateQueryUsesHostScopeAndPrecedingWindows(t *testing.T) {
	stub := &recentCoordinatorQueryStub{t: t}
	p := ListRecentCoordinatorStateParams{WorkspaceID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, AgentID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, AnchorJobID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}}
	if _, err := New(stub).ListRecentCoordinatorState(context.Background(), p); err == nil {
		t.Fatal("fixture query error swallowed")
	}
	if len(stub.args) != 3 || stub.args[0] != p.WorkspaceID || stub.args[1] != p.AgentID || stub.args[2] != p.AnchorJobID {
		t.Fatalf("model-selectable scope appeared: %+v", stub.args)
	}
	for _, required := range []string{"current_job.id = $3", "current_job.workspace_id = $1", "current_job.agent_id = $2", "NULLIF(BTRIM(current_job.command #>> '{event,data,conversation,openConversationId}'), '') IS NOT NULL", "prior.workspace_id = $1", "prior.agent_id = $2", "prior.endpoint_namespace_id = anchor.endpoint_namespace_id", "(prior.command #>> '{source,platform}') IS NOT DISTINCT FROM (anchor.command #>> '{source,platform}')", "(prior.command #>> '{source,type}') IS NOT DISTINCT FROM (anchor.command #>> '{source,type}')", "= BTRIM(anchor.command #>> '{event,data,conversation,openConversationId}')", "prior.id <> anchor.id AND prior.created_at < anchor.created_at", "ORDER BY prior.created_at DESC, prior.id DESC", "LIMIT 3", "LEFT JOIN LATERAL", "'{_coordinator_plan,CompletedActionKeys}'", "'{_coordinator_plan,IssueResults}'"} {
		if !strings.Contains(stub.sql, required) {
			t.Fatalf("lost query guard/provenance: %s", required)
		}
	}
	for _, banned := range []string{"SELECT *", "job.command,", "last_error", "context_token", "FOR UPDATE", "UPDATE ", "INSERT ", "DELETE "} {
		if strings.Contains(stub.sql, banned) {
			t.Fatalf("query acquired raw payload or writes: %s", banned)
		}
	}
}
