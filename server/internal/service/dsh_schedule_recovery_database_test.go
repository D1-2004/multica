package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshschedule"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Only the explicitly configured, migrated preproduction database is allowed.
// The creating task is already completed when publication first reaches the API.
func TestDSHScheduleDatabaseRecoveryPreservesOriginalCreator(t *testing.T) {
	s, a, session, agent, pool, _ := nativeChatDatabaseFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM dsh_schedule WHERE agent_id=$1`, agent.ID); err != nil {
			t.Error(err)
		}
	})
	source, current, unproven := uuid.New(), uuid.New(), uuid.New()
	scope := dshhost.SessionScope{Key: a.access.Key, Kind: "chat", ID: uuid.UUID(session.ID.Bytes)}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, id := range []uuid.UUID{source, current, unproven} {
		status := "completed"
		if id == current {
			status = "running"
		}
		originator := session.CreatorID
		if id == unproven {
			originator = pgtype.UUID{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,status,originator_user_id,accountable_user_id,chat_session_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, agent.ID, agent.RuntimeID, status, originator, session.CreatorID, session.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := (dshhost.PostgresStore{DB: tx}).AdoptNativeExecution(ctx, scope, id, a.input.SessionID, uuid.New()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var overdue time.Time
	if err := pool.QueryRow(ctx, `SELECT date_trunc('milliseconds',clock_timestamp())-interval '1 hour'`).Scan(&overdue); err != nil {
		t.Fatal(err)
	}
	actor := DSHScheduleActor{Key: a.access.Key, TaskID: current}
	input := DSHScheduleInput{SourceTaskID: source.String(), SessionID: a.input.SessionID, ScheduleID: "schedule-1", Prompt: "late durable reminder", FirstDue: overdue}
	deny := func(context.Context, *db.Queries, db.Agent, pgtype.UUID) error { return dshhost.ErrNativeAccessDenied }
	if _, err := s.RegisterDSHSchedule(ctx, actor, input, deny); !errors.Is(err, dshhost.ErrNativeAccessDenied) {
		t.Fatal("current authority was bypassed", err)
	}
	for _, invalidSource := range []uuid.UUID{uuid.New(), unproven} {
		invalid := input
		invalid.SourceTaskID = invalidSource.String()
		if _, err := s.RegisterDSHSchedule(ctx, actor, invalid, a.invoke); !errors.Is(err, dshhost.ErrNativeAccessDenied) {
			t.Fatal("unbound or accountable-only source accepted", err)
		}
	}
	// A valid historical human creator does not authorize an expired bearer task.
	if _, err := s.RegisterDSHSchedule(ctx, DSHScheduleActor{Key: a.access.Key, TaskID: source}, input, a.invoke); !errors.Is(err, dshhost.ErrNativeAccessDenied) {
		t.Fatal("completed current task accepted", err)
	}
	view, err := s.RegisterDSHSchedule(ctx, actor, input, a.invoke)
	if err != nil || view.SourceTaskID != source.String() || view.State != "overdue" {
		t.Fatal("fresh-context recovery failed", view, err)
	}
	replay, err := s.RegisterDSHSchedule(ctx, actor, input, a.invoke)
	if err != nil || replay.SourceTaskID != source.String() || replay.State != "overdue" {
		t.Fatal("replay changed provenance", replay, err)
	}
	changed := input
	changed.SourceTaskID = current.String()
	if _, err := s.RegisterDSHSchedule(ctx, actor, changed, a.invoke); !errors.Is(err, dshschedule.ErrConflict) {
		t.Fatal("replay replaced creating task", err)
	}
	// Changing the historical source to another user must not transfer a pending
	// native intent to the current task's owner, even if all bindings still match.
	foreign := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO "user"(id,name,email) VALUES($1,'Other reminder owner',$2)`, foreign, uuid.NewString()+"@multica.test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `UPDATE agent_task_queue SET originator_user_id=$1 WHERE id=$2`, session.CreatorID, source); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, agent.WorkspaceID, foreign); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1`, foreign); err != nil {
			t.Error(err)
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO member(workspace_id,user_id,role) VALUES($1,$2,'member')`, agent.WorkspaceID, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET originator_user_id=$1 WHERE id=$2`, foreign, source); err != nil {
		t.Fatal(err)
	}
	input.ScheduleID = "schedule-2"
	if _, err := s.RegisterDSHSchedule(ctx, actor, input, a.invoke); !errors.Is(err, dshhost.ErrNativeAccessDenied) {
		t.Fatal("cross-owner recovery accepted", err)
	}
	found, err := s.CancelDSHSchedule(ctx, actor, a.input.SessionID, "schedule-1", a.invoke)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	input.ScheduleID = "schedule-1"
	replay, err = s.RegisterDSHSchedule(ctx, actor, input, a.invoke)
	if err != nil || replay.State != "cancelled" {
		t.Fatal("replay resurrected cancelled intent", replay, err)
	}
}
