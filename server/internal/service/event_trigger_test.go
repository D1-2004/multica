package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func eventFixture(t *testing.T) (*EventTriggerService, db.Agent) {
	t.Helper()
	url := os.Getenv("MULTICA_EVENT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set MULTICA_EVENT_TEST_DATABASE_URL to an isolated migrated test database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	id := createClaimCapacityFixture(t, ctx, pool)
	q := db.New(pool)
	agent, err := q.GetAgent(ctx, util.MustParseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id=$1`, agent.ID); err != nil {
		t.Fatal(err)
	}
	taskSvc := NewTaskService(q, pool, nil, events.New())
	s := NewEventTriggerService(pool, NewAutopilotService(q, pool, events.New(), taskSvc))
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM agent_event WHERE stream_id IN (SELECT id FROM agent_event_stream WHERE agent_id=$1)`, agent.ID)
		pool.Exec(ctx, `DELETE FROM agent_event_batch WHERE stream_id IN (SELECT id FROM agent_event_stream WHERE agent_id=$1)`, agent.ID)
		pool.Exec(ctx, `DELETE FROM agent_event_stream WHERE agent_id=$1`, agent.ID)
		pool.Exec(ctx, `DELETE FROM autopilot_run WHERE autopilot_id IN (SELECT autopilot_id FROM agent_event_trigger WHERE agent_id=$1)`, agent.ID)
		pool.Exec(ctx, `DELETE FROM autopilot_rule_version WHERE autopilot_id IN (SELECT autopilot_id FROM agent_event_trigger WHERE agent_id=$1)`, agent.ID)
		pool.Exec(ctx, `DELETE FROM autopilot WHERE id IN (SELECT autopilot_id FROM agent_event_trigger WHERE agent_id=$1)`, agent.ID)
		pool.Exec(ctx, `DELETE FROM agent_event_trigger WHERE agent_id=$1`, agent.ID)
	})
	return s, agent
}

func eventInput(id string) []ObservedEvent {
	return []ObservedEvent{{ID: id, Payload: json.RawMessage(`{"text":"test event"}`), RuntimeContext: json.RawMessage(`{"external_identity":{"dws":{"uid":"123","orgId":"456"}}}`)}}
}

func eventSQL(t *testing.T, s *EventTriggerService, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func eventCount(t *testing.T, s *EventTriggerService, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func admitEvent(t *testing.T, s *EventTriggerService, a db.Agent, id string) {
	t.Helper()
	ok, err := s.Admit(context.Background(), a.WorkspaceID, a.ID, "dingtalk:456:123", "group-1", eventInput(id))
	if err != nil || !ok {
		t.Fatalf("admit=%v: %v", ok, err)
	}
}
func processEvent(t *testing.T, s *EventTriggerService) bool {
	t.Helper()
	ok, err := s.ProcessNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ok
}
func makeEventDue(t *testing.T, s *EventTriggerService, a db.Agent) {
	eventSQL(t, s, `UPDATE agent_event_stream SET due_at=now()-interval '1 second' WHERE agent_id=$1`, a.ID)
}

func TestEventTriggerDefaultOffDedupAndConcurrentDispatch(t *testing.T) {
	s, a := eventFixture(t)
	ctx := context.Background()
	if ok, err := s.Admit(ctx, a.WorkspaceID, a.ID, "dingtalk:456:123", "group-1", eventInput("one")); err != nil || ok {
		t.Fatalf("disabled admission=%v %v", ok, err)
	}
	if n := eventCount(t, s, `SELECT count(*) FROM agent_event_stream WHERE agent_id=$1`, a.ID); n != 0 {
		t.Fatal("disabled event persisted")
	}
	if err := s.SetEnabled(ctx, a, a.OwnerID, true); err != nil {
		t.Fatal(err)
	}
	admitEvent(t, s, a, "one")
	var before time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT last_pending_at FROM agent_event_stream WHERE agent_id=$1`, a.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	admitEvent(t, s, a, "one")
	var after time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT last_pending_at FROM agent_event_stream WHERE agent_id=$1`, a.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) {
		t.Fatal("duplicate extended collection window")
	}
	if processEvent(t, s) {
		t.Fatal("dispatched before quiet window")
	}
	makeEventDue(t, s, a)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.ProcessNext(ctx); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := eventCount(t, s, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, a.ID); n != 1 {
		t.Fatalf("created %d tasks", n)
	}
	if n := eventCount(t, s, `SELECT count(*) FROM agent_event WHERE consumed_at IS NOT NULL`); n != 0 {
		t.Fatal("admission marked consumed")
	}
	var payload []byte
	if err := s.Pool.QueryRow(ctx, `SELECT trigger_payload FROM autopilot_run WHERE task_id IN (SELECT id FROM agent_task_queue WHERE agent_id=$1)`, a.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Events []any  `json:"events"`
		Batch  string `json:"batch_id"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil || len(parsed.Events) != 1 || parsed.Batch == "" {
		t.Fatalf("missing runtime payload: %s", payload)
	}
}

func TestEventTriggerFrozenBatchNewArrivalsAndRestart(t *testing.T) {
	s, a := eventFixture(t)
	ctx := context.Background()
	if err := s.SetEnabled(ctx, a, a.OwnerID, true); err != nil {
		t.Fatal(err)
	}
	admitEvent(t, s, a, "first")
	makeEventDue(t, s, a)
	processEvent(t, s)
	admitEvent(t, s, a, "second")
	makeEventDue(t, s, a)
	processEvent(t, s)
	if n := eventCount(t, s, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, a.ID); n != 1 {
		t.Fatal("overlapping task created")
	}
	eventSQL(t, s, `UPDATE agent_task_queue SET status='completed' WHERE agent_id=$1`, a.ID)
	s = NewEventTriggerService(s.Pool, s.Autopilot)
	makeEventDue(t, s, a)
	processEvent(t, s)
	if n := eventCount(t, s, `SELECT count(*) FROM agent_event e JOIN agent_event_stream st ON st.id=e.stream_id WHERE st.agent_id=$1 AND e.consumed_at IS NOT NULL`, a.ID); n != 1 {
		t.Fatalf("consumed %d, want frozen first event only", n)
	}
	if processEvent(t, s) {
		t.Fatal("ignored minimum interval")
	}
	makeEventDue(t, s, a)
	processEvent(t, s)
	if n := eventCount(t, s, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, a.ID); n != 2 {
		t.Fatalf("next batch tasks=%d", n)
	}
}

func TestEventTriggerFailureRetainsBatchAndDisabledStopsDispatch(t *testing.T) {
	s, a := eventFixture(t)
	ctx := context.Background()
	if err := s.SetEnabled(ctx, a, a.OwnerID, true); err != nil {
		t.Fatal(err)
	}
	admitEvent(t, s, a, "failure")
	makeEventDue(t, s, a)
	processEvent(t, s)
	var run pgtype.UUID
	if err := s.Pool.QueryRow(ctx, `SELECT autopilot_run_id FROM agent_task_queue WHERE agent_id=$1`, a.ID).Scan(&run); err != nil {
		t.Fatal(err)
	}
	eventSQL(t, s, `UPDATE agent_task_queue SET status='failed' WHERE agent_id=$1`, a.ID)
	makeEventDue(t, s, a)
	processEvent(t, s)
	if err := s.SetEnabled(ctx, a, a.OwnerID, false); err != nil {
		t.Fatal(err)
	}
	makeEventDue(t, s, a)
	processEvent(t, s)
	if n := eventCount(t, s, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, a.ID); n != 1 {
		t.Fatal("disabled retry dispatched")
	}
	if err := s.SetEnabled(ctx, a, a.OwnerID, true); err != nil {
		t.Fatal(err)
	}
	makeEventDue(t, s, a)
	processEvent(t, s)
	if n := eventCount(t, s, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1 AND autopilot_run_id=$2`, a.ID, run); n != 2 {
		t.Fatalf("retry did not reuse run: %d", n)
	}
	if n := eventCount(t, s, `SELECT count(*) FROM agent_event e JOIN agent_event_stream st ON st.id=e.stream_id WHERE st.agent_id=$1 AND e.consumed_at IS NOT NULL`, a.ID); n != 0 {
		t.Fatal("failed event consumed")
	}
}

func TestEventTriggerBoundedBatchPreservesOverflow(t *testing.T) {
	s, a := eventFixture(t)
	ctx := context.Background()
	if err := s.SetEnabled(ctx, a, a.OwnerID, true); err != nil {
		t.Fatal(err)
	}
	var input []ObservedEvent
	for i := 0; i < EventBatchSize+5; i++ {
		input = append(input, eventInput(fmt.Sprint(i))...)
	}
	if _, err := s.Admit(ctx, a.WorkspaceID, a.ID, "source", "resource", input); err != nil {
		t.Fatal(err)
	}
	makeEventDue(t, s, a)
	processEvent(t, s)
	if n := eventCount(t, s, `SELECT count(*) FROM agent_event e JOIN agent_event_stream st ON st.id=e.stream_id WHERE st.agent_id=$1 AND e.batch_id IS NULL`, a.ID); n != 5 {
		t.Fatalf("overflow=%d", n)
	}
}

func TestEventTriggerSQLDeadlines(t *testing.T) {
	s, a := eventFixture(t)
	ctx := context.Background()
	if err := s.SetEnabled(ctx, a, a.OwnerID, true); err != nil {
		t.Fatal(err)
	}
	admitEvent(t, s, a, "initial")
	var seconds float64
	if err := s.Pool.QueryRow(ctx, `SELECT extract(epoch FROM due_at-first_pending_at)::float8 FROM agent_event_stream WHERE agent_id=$1`, a.ID).Scan(&seconds); err != nil {
		t.Fatal(err)
	}
	if seconds != EventQuietPeriod.Seconds() {
		t.Fatalf("quiet deadline=%f", seconds)
	}
	eventSQL(t, s, `UPDATE agent_event_stream SET first_pending_at=now()-interval '11 seconds' WHERE agent_id=$1`, a.ID)
	admitEvent(t, s, a, "continuous")
	if err := s.Pool.QueryRow(ctx, `SELECT extract(epoch FROM due_at-first_pending_at)::float8 FROM agent_event_stream WHERE agent_id=$1`, a.ID).Scan(&seconds); err != nil {
		t.Fatal(err)
	}
	if seconds != EventMaxCollect.Seconds() {
		t.Fatalf("hard collection deadline=%f", seconds)
	}
	eventSQL(t, s, `UPDATE agent_event_stream SET last_dispatch_at=now()-interval '5 seconds' WHERE agent_id=$1`, a.ID)
	admitEvent(t, s, a, "rate-limit")
	if err := s.Pool.QueryRow(ctx, `SELECT extract(epoch FROM due_at-last_dispatch_at)::float8 FROM agent_event_stream WHERE agent_id=$1`, a.ID).Scan(&seconds); err != nil {
		t.Fatal(err)
	}
	if seconds != EventMinInterval.Seconds() {
		t.Fatalf("minimum dispatch interval=%f", seconds)
	}
}
