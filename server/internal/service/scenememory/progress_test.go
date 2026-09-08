package scenememory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
)

type pageHistoryFunc func(context.Context, db.SceneMemory) (HistoryPage, error)

func (f pageHistoryFunc) Read(ctx context.Context, row db.SceneMemory) (HistoryPage, error) {
	return f(ctx, row)
}

func TestMemoryFlusherResumesPageAndOnlyFinishesAtTarget(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	dirtyReady(t, pool, store, id)
	_, err := pool.Exec(ctx, "UPDATE scene_memory SET memory_text='existing fact',history_resume_before=now()-interval '1 day' WHERE workspace_id=$1", id.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := row.LeaseTargetThroughAt.Time.Add(-time.Second)
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"commit","type":"function","function":{"name":"memory_flush_commit","arguments":"{\"decision\":\"unchanged\"}"}}]}}]}`))
	}))
	defer model.Close()
	client := llm.New(llm.Config{APIKey: "test", BaseURL: model.URL, MaxRetries: -1})
	first := &MemoryFlusher{Store: store, LLM: client, History: pageHistoryFunc(func(_ context.Context, claimed db.SceneMemory) (HistoryPage, error) {
		return HistoryPage{PaginationKnown: true, HasMore: true, NextCursor: next, Events: []HistoryEvent{{EvidenceID: "older", OccurredAt: next.Add(-time.Second), Content: "older fact", Speaker: "peer"}}, EvidenceIDs: []string{"older"}}, nil
	})}
	if err := first.Flush(ctx, row); err != nil {
		t.Fatal(err)
	}
	middle, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if middle.DirtyRevision == middle.FlushedRevision || middle.HistoryResumeBefore.Valid || middle.MemoryText != "existing fact" {
		t.Fatalf("first page lost pending/text state: %+v", middle)
	}
	if middle.AttemptCount != 0 {
		t.Fatalf("successful page must reset attempt_count, got %d", middle.AttemptCount)
	}
	// A new flusher/store instance has no process-local history from page one.
	store = NewStore(db.New(pool))
	row, err = store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second := &MemoryFlusher{Store: store, LLM: client, History: pageHistoryFunc(func(_ context.Context, claimed db.SceneMemory) (HistoryPage, error) {
		if !historyStartAfter(claimed, false, time.Now()).Equal(next) {
			t.Fatal("restart did not resume page two")
		}
		return HistoryPage{PaginationKnown: true, Events: []HistoryEvent{{EvidenceID: claimed.LeaseTargetThroughEvidenceID, OccurredAt: claimed.LeaseTargetThroughAt.Time, Content: "target fact", Speaker: "peer"}}, EvidenceIDs: []string{claimed.LeaseTargetThroughEvidenceID}}, nil
	})}
	if err := second.Flush(ctx, row); err != nil {
		t.Fatal(err)
	}
	finished, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if StatusOf(finished) != "clean" || finished.MemoryText != "existing fact" || modelCalls.Load() != 2 {
		t.Fatalf("completion status=%s text=%q calls=%d", StatusOf(finished), finished.MemoryText, modelCalls.Load())
	}
}

func planCompleteFlush(row db.SceneMemory, events []HistoryEvent) (flushPlan, error) {
	page := HistoryPage{Events: events, PaginationKnown: true}
	for _, event := range events {
		page.EvidenceIDs = append(page.EvidenceIDs, event.EvidenceID)
	}
	return planFlush(row, page)
}

func commitPlanForTest(t *testing.T, row db.SceneMemory, plan flushPlan) db.SceneMemory {
	t.Helper()
	meta, err := json.Marshal(map[string]any{"history_progress": plan.progress})
	if err != nil {
		t.Fatal(err)
	}
	row.LastFlushMeta = meta
	row.SourceCursorAt = timestamptz(plan.cursorAt)
	row.SourceCursorEvidenceID = plan.cursorEv
	row.BootstrappedAt = timestamptz(time.Now())
	row.HistoryResumeBefore = pgtype.Timestamptz{}
	return row
}

func TestForwardHistoryProgressSurvivesMoreThanEightPagesAndRestart(t *testing.T) {
	start := time.Date(2026, 9, 3, 14, 2, 14, 0, time.UTC)
	const total = 310
	cutoff := start.Add((total - 1) * time.Second)
	row := db.SceneMemory{
		SceneKind: KindGroup, DirtyRevision: 34,
		LeaseTargetDirtyRevision: pgtype.Int8{Int64: 34, Valid: true},
		LeaseTargetThroughAt:     timestamptz(cutoff), LeaseTargetThroughEvidenceID: "event-309",
		LastTriggerAt: timestamptz(cutoff), LastTriggerEvidenceID: "event-309",
		PendingFromAt: timestamptz(start), PendingFromEvidenceID: "event-000",
		SourceCursorAt: timestamptz(start.Add(90 * time.Second)), SourceCursorEvidenceID: "event-090",
		HistoryResumeBefore: timestamptz(start.Add(120 * time.Second)),
		BootstrappedAt:      timestamptz(start.Add(-time.Hour)),
	}
	if got := historyStartAfter(row, true, time.Now()); !got.Equal(start.Add(-time.Second)) {
		t.Fatalf("stale backward continuation must not exclude the target: %s", got)
	}
	seen := make(map[string]int)
	for offset := 0; offset < total; offset += 30 {
		end := min(offset+30, total)
		page := HistoryPage{PaginationKnown: true, HasMore: end < total, NextCursor: start.Add(time.Duration(end) * time.Second)}
		for i := offset; i < end; i++ {
			e := HistoryEvent{EvidenceID: fmt.Sprintf("event-%03d", i), OccurredAt: start.Add(time.Duration(i) * time.Second), Content: "fact"}
			page.Events = append(page.Events, e)
			page.EvidenceIDs = append(page.EvidenceIDs, e.EvidenceID)
		}
		plan, err := planFlush(row, page)
		if err != nil {
			t.Fatalf("page %d: %v", offset/30, err)
		}
		if plan.caughtUp != (end == total) {
			t.Fatalf("premature/missing completion at %d", end)
		}
		if plan.cursorAt.Before(row.SourceCursorAt.Time) {
			t.Fatal("source cursor regressed during late replay")
		}
		for _, event := range plan.batch {
			seen[event.EvidenceID]++
		}
		row = commitPlanForTest(t, row, plan)
		if end < total && !historyStartAfter(row, true, time.Now()).Equal(page.NextCursor) {
			t.Fatal("restart lost committed continuation")
		}
	}
	if len(seen) != total {
		t.Fatalf("merged %d of %d", len(seen), total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("%s merged %d times", id, count)
		}
	}
}

func TestForwardHistorySameSecondAcrossPagesUsesExactCursor(t *testing.T) {
	at := time.Date(2026, 9, 4, 7, 36, 23, 0, time.UTC)
	row := db.SceneMemory{DirtyRevision: 1, LeaseTargetThroughAt: timestamptz(at), LeaseTargetThroughEvidenceID: "middle", LastTriggerEvidenceID: "middle"}
	page := HistoryPage{PaginationKnown: true, HasMore: true, NextCursor: at.Add(500 * time.Millisecond), Events: []HistoryEvent{{EvidenceID: "zzz", OccurredAt: at, Content: "first"}}, EvidenceIDs: []string{"zzz"}}
	first, err := planFlush(row, page)
	if err != nil || first.caughtUp {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	row = commitPlanForTest(t, row, first)
	if !historyStartAfter(row, false, time.Now()).Equal(page.NextCursor) {
		t.Fatal("millisecond cursor was rounded")
	}
	last, err := planCompleteFlush(row, []HistoryEvent{{EvidenceID: "aaa", OccurredAt: at, Content: "second"}, {EvidenceID: "middle", OccurredAt: at, Content: "target"}})
	if err != nil || !last.caughtUp || len(last.batch) != 2 {
		t.Fatalf("last=%+v err=%v", last, err)
	}
}

func TestForwardHistoryNewDirtyRevisionReplaysLateWindow(t *testing.T) {
	at := time.Date(2026, 9, 4, 7, 0, 0, 0, time.UTC)
	row := db.SceneMemory{DirtyRevision: 8, SourceCursorAt: timestamptz(at), PendingFromAt: timestamptz(at.Add(-time.Hour)), PendingFromEvidenceID: "late"}
	row = commitPlanForTest(t, row, flushPlan{cursorAt: at, progress: historyProgress{DirtyRevision: 7, After: at.Add(time.Minute), PendingSeen: true}})
	if _, ok := restoredHistoryProgress(row); ok {
		t.Fatal("new revision reused old evidence coverage")
	}
	if got := historyStartAfter(row, false, time.Now()); !got.Equal(at.Add(-time.Hour - time.Second)) {
		t.Fatalf("late message excluded: %s", got)
	}
	// An in-flight claim still commits its own revision; the next claim must
	// reject that metadata after a concurrent MarkDirty.
	row.LeaseTargetDirtyRevision = pgtype.Int8{Int64: 7, Valid: true}
	if _, ok := restoredHistoryProgress(row); !ok {
		t.Fatal("captured claim lost its own progress")
	}
}

func TestForwardHistoryEmptyTextPageAdvancesWithoutPretendingCaughtUp(t *testing.T) {
	at := time.Date(2026, 9, 4, 7, 0, 0, 0, time.UTC)
	row := db.SceneMemory{DirtyRevision: 2, LeaseTargetThroughAt: timestamptz(at.Add(time.Hour)), LeaseTargetThroughEvidenceID: "target"}
	plan, err := planFlush(row, HistoryPage{PaginationKnown: true, HasMore: true, NextCursor: at.Add(time.Second)})
	if err != nil || plan.caughtUp || len(plan.batch) != 0 || !plan.cursorAt.IsZero() {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	row = commitPlanForTest(t, row, plan)
	row.LastTriggerAt = row.LeaseTargetThroughAt
	row.DirtyRevision++
	if got := historyStartAfter(row, true, time.Now()); got.After(plan.progress.After) {
		t.Fatal("new inbound skipped the remainder of an empty bootstrap page")
	}
	_, err = planFlush(row, HistoryPage{PaginationKnown: true})
	if FlushErrorCode(err) != ErrorIncomplete {
		t.Fatalf("missing target was silently completed: %v", err)
	}
}

func TestForwardHistoryRejectsOversizedPageInsteadOfDroppingTail(t *testing.T) {
	page := HistoryPage{PaginationKnown: true, Events: make([]HistoryEvent, flushBatchEvents+1)}
	if _, err := planFlush(db.SceneMemory{}, page); FlushErrorCode(err) != ErrorIncomplete {
		t.Fatalf("oversized page accepted: %v", err)
	}
}
