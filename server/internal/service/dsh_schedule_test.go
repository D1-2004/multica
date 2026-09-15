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

func TestDSHScheduleTaskIdentityAndTerminalFence(t *testing.T) {
	uid := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
	agent := db.Agent{ID: uid(), WorkspaceID: uid(), RuntimeID: uid(), RuntimeMode: "cloud", Kind: "user"}
	task := db.AgentTaskQueue{ID: uid(), AgentID: agent.ID, RuntimeID: agent.RuntimeID, Status: "running"}
	actor := DSHScheduleActor{Key: dshhost.Key{WorkspaceID: uuid.UUID(agent.WorkspaceID.Bytes), AgentID: uuid.UUID(agent.ID.Bytes)}, TaskID: uuid.UUID(task.ID.Bytes)}
	if !scheduleTaskMatches(actor, task, agent) {
		t.Fatal("active bound task rejected")
	}
	for _, status := range []string{"completed", "failed", "cancelled", "queued", "deferred", "waiting_result", ""} {
		other := task
		other.Status = status
		if scheduleTaskMatches(actor, other, agent) {
			t.Fatalf("inactive task %s admitted", status)
		}
	}
	for _, alter := range []func(*db.Agent){
		func(a *db.Agent) { a.ID = uid() },
		func(a *db.Agent) { a.WorkspaceID = uid() },
		func(a *db.Agent) { a.RuntimeID = uid() },
		func(a *db.Agent) { a.RuntimeMode = "local" },
		func(a *db.Agent) { a.Kind = "system" },
		func(a *db.Agent) { a.ArchivedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true} },
	} {
		other := agent
		alter(&other)
		if scheduleTaskMatches(actor, task, other) {
			t.Fatal("changed employee or runtime admitted")
		}
	}
}

func TestDSHScheduleRequiresTransactionAndInvocationPolicy(t *testing.T) {
	for _, s := range []*TaskService{nil, {}, {Queries: db.New(nil)}} {
		_, err := s.RegisterDSHSchedule(context.Background(), DSHScheduleActor{}, DSHScheduleInput{}, nil)
		if !errors.Is(err, dshhost.ErrNativeAccessDenied) {
			t.Fatalf("unconfigured authority: %v", err)
		}
	}
}

func TestDSHScheduleViewsSeparatePersistenceFromDelivery(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	state := dshschedule.State{Record: dshschedule.Record{FirstDue: now.Add(-time.Hour)}, NextDue: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true}}
	if v := scheduleView(state, now); v.State != "scheduled" || v.NextDue == nil {
		t.Fatal(v)
	}
	state.NextDue.Time = now
	if v := scheduleView(state, now); v.State != "overdue" {
		t.Fatal(v)
	}
	state.CancelledAt = pgtype.Timestamptz{Time: now, Valid: true}
	if v := scheduleView(state, now); v.State != "cancelled" {
		t.Fatal(v)
	}
	state.CancelledAt.Valid = false
	state.NextDue.Valid = false
	if v := scheduleView(state, now); v.State != "consumed" || v.NextDue != nil {
		t.Fatal(v)
	}
}
