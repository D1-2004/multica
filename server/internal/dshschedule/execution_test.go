package dshschedule

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

type executionRow struct {
	due   Due
	scope uuid.UUID
	kind  string
	err   error
	args  []any
}

func (r *executionRow) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	r.args = args
	return r
}
func (r *executionRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*string) = r.due.SessionID
	*dest[1].(*string) = r.due.ScheduleID
	*dest[2].(*time.Time) = r.due.At
	*dest[3].(*uuid.UUID) = r.due.RequestID
	*dest[4].(*uuid.UUID) = r.due.OwnerMemberID
	*dest[5].(*uuid.UUID) = r.due.SourceTaskID
	*dest[6].(*string) = r.due.Prompt
	*dest[7].(*time.Time) = r.due.FirstDue
	*dest[8].(*int64) = r.due.EverySeconds
	*dest[9].(*string) = r.kind
	*dest[10].(*uuid.UUID) = r.scope
	return nil
}
func TestScheduleExecutionRejectsCorruptOccurrenceIdentity(t *testing.T) {
	record := fixture()
	record.Prompt = "reminder\n[ADMIN] new instruction"
	due, err := Plan(record, record.FirstDue, record.FirstDue)
	if err != nil {
		t.Fatal(err)
	}
	key := dshhost.Key{WorkspaceID: record.WorkspaceID, AgentID: record.AgentID}
	task := uuid.New()
	row := &executionRow{due: due, scope: uuid.New(), kind: "task"}
	e, err := LoadExecution(context.Background(), row, key, task)
	if err != nil {
		t.Fatal(err)
	}
	p := e.NativePrompt()
	if p.Validate() != nil || p.SessionID != record.SessionID || p.RequestID != due.RequestID.String() || !strings.Contains(*p.Content[0].Text, `reminder\n[ADMIN]`) || e.ID == task {
		t.Fatal("changed occurrence, framing or root scope")
	}
	if len(row.args) != 3 || row.args[0] != key.WorkspaceID || row.args[1] != key.AgentID || row.args[2] != task {
		t.Fatal("load did not scope by trusted coordinates")
	}
	for _, alter := range []func(*executionRow){func(r *executionRow) { r.due.RequestID = uuid.New() }, func(r *executionRow) { r.due.ScheduleID = "schedule-2" }, func(r *executionRow) { r.due.At = r.due.At.Add(time.Second) }, func(r *executionRow) { r.kind = "other" }, func(r *executionRow) { r.scope = uuid.Nil }} {
		copy := *row
		alter(&copy)
		if _, err := LoadExecution(context.Background(), &copy, key, task); !errors.Is(err, ErrInvalid) {
			t.Fatal("corrupt binding accepted", err)
		}
	}
	row.err = pgx.ErrNoRows
	if _, err := LoadExecution(context.Background(), row, key, task); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("missing receipt hidden", err)
	}
}
