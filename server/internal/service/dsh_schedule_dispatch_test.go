package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshschedule"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDSHScheduleExecutionRetainsScopeWithoutHumanImpersonation(t *testing.T) {
	u := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
	task := db.AgentTaskQueue{ID: u(), AgentID: u(), TriggerEvidenceKind: pgtype.Text{String: dshschedule.EvidenceKind, Valid: true}, TriggerEvidenceRefID: u(), OriginatorSource: pgtype.Text{String: string(attribution.SourceTriggerOwner), Valid: true}}
	e := dshschedule.Execution{SessionScope: dshhost.SessionScope{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.UUID(task.AgentID.Bytes)}, Kind: "task", ID: uuid.New()}, Receipt: dshschedule.Receipt{TaskID: uuid.UUID(task.ID.Bytes), Due: dshschedule.Due{RequestID: uuid.UUID(task.TriggerEvidenceRefID.Bytes)}}}
	if !ScheduleExecutionMatches(task, e) || e.ID == uuid.UUID(task.ID.Bytes) {
		t.Fatal("new standalone task did not retain prior scope")
	}
	for _, scope := range []string{"chat", "issue"} {
		copy := task
		execution := e
		execution.Kind = scope
		parent := u()
		execution.ID = uuid.UUID(parent.Bytes)
		if scope == "chat" {
			copy.ChatSessionID = parent
			copy.ChatInputTaskID = copy.ID
		} else {
			copy.IssueID = parent
		}
		if !ScheduleExecutionMatches(copy, execution) {
			t.Fatal("matching scope rejected", scope)
		}
		execution.ID = uuid.New()
		if ScheduleExecutionMatches(copy, execution) {
			t.Fatal("foreign scope accepted", scope)
		}
	}
	for _, alter := range []func(*db.AgentTaskQueue){
		func(t *db.AgentTaskQueue) { t.ID = u() }, func(t *db.AgentTaskQueue) { t.AgentID = u() }, func(t *db.AgentTaskQueue) { t.TriggerEvidenceRefID = u() },
		func(t *db.AgentTaskQueue) { t.TriggerEvidenceKind.String = "chat" }, func(t *db.AgentTaskQueue) { t.OriginatorUserID = u() }, func(t *db.AgentTaskQueue) { t.OriginatorSource.String = "direct_human" }, func(t *db.AgentTaskQueue) { t.AutopilotRunID = u() },
	} {
		copy := task
		alter(&copy)
		if ScheduleExecutionMatches(copy, e) {
			t.Fatal("mismatched execution accepted")
		}
	}
}

func TestDSHScheduleDispatchDeniesUnconfiguredAuthority(t *testing.T) {
	key := dshschedule.Key{WorkspaceID: uuid.New(), AgentID: uuid.New(), SessionID: uuid.NewString(), ScheduleID: "schedule-1"}
	for _, s := range []*TaskService{nil, {}, {Queries: db.New(nil)}} {
		if _, err := s.DispatchDSHSchedule(context.Background(), key, nil); !errors.Is(err, dshhost.ErrNativeAccessDenied) {
			t.Fatal(err)
		}
	}
}
