package scenememory

import (
	"strings"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestParseDWSEventsSkipsEmptyContent(t *testing.T) {
	raw := []byte(`{
		"success": true,
		"result": {
			"messages": [
				{"content":"GoalMate 是工具","createTime":"2026-09-01 12:00:00","openMessageId":"m1","sender":"冬翔"},
				{"content":"  ","createTime":"2026-09-01 12:01:00","openMessageId":"m2","sender":"东翔测试号"}
			]
		}
	}`)
	got, err := parseDWSEvents(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].EvidenceID != "m1" || got[0].Speaker != "冬翔" {
		t.Fatalf("got %#v", got)
	}
	if !strings.Contains(got[0].Content, "工具") {
		t.Fatalf("content=%q", got[0].Content)
	}
}

func TestParseDWSEventsRejected(t *testing.T) {
	if _, err := parseDWSEvents([]byte(`{"success":false,"errorCode":"auth_failed"}`)); err == nil {
		t.Fatal("rejected history must error")
	}
	if _, err := parseDWSEvents([]byte(`not-json`)); err == nil {
		t.Fatal("invalid json must error")
	}
}

func TestClassifyHistoryAuth(t *testing.T) {
	err := classifyHistory(errString("DWS AuthCode exchange failed"))
	if FlushErrorCode(err) != ErrorAuth {
		t.Fatalf("code=%q", FlushErrorCode(err))
	}
}

func TestParseDWSPageCountsEmptyMessages(t *testing.T) {
	raw := []byte(`{
		"success": true,
		"result": {
			"messages": [
				{"content":"GoalMate 是工具","createTime":"2026-09-01 12:00:00","openMessageId":"m1","sender":"冬翔"},
				{"content":"","createTime":"2026-09-01 11:59:00","openMessageId":"m2","sender":"系统"}
			]
		}
	}`)
	page, err := parseDWSPage(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.RawCount != 2 {
		t.Fatalf("rawCount=%d", page.RawCount)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events=%d", len(page.Events))
	}
	if page.Oldest.IsZero() {
		t.Fatal("oldest must include empty-content messages")
	}
}

func TestParseDWSPageAcceptsTopLevelMessages(t *testing.T) {
	raw := []byte(`{
		"messages": [
			{"text":"GoalMate 是工具","createTime":"2026-09-01 12:00:00","messageId":"m1","sender":"冬翔"}
		]
	}`)
	page, err := parseDWSPage(raw, "")
	if err != nil || len(page.Events) != 1 || page.Events[0].EvidenceID != "m1" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if page.Events[0].Content != "GoalMate 是工具" {
		t.Fatalf("content=%q", page.Events[0].Content)
	}
}

func TestFilterAfterLookbackDropsOlderEvents(t *testing.T) {
	lookback := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	got := filterAfterLookback([]HistoryEvent{
		{EvidenceID: "old", OccurredAt: lookback.Add(-time.Minute), Content: "old"},
		{EvidenceID: "keep", OccurredAt: lookback, Content: "keep"},
	}, lookback)
	if len(got) != 1 || got[0].EvidenceID != "keep" {
		t.Fatalf("got %#v", got)
	}
}

func TestParseDWSPageMarksSelfByUID(t *testing.T) {
	raw := []byte(`{
		"success": true,
		"result": {
			"messages": [
				{"content":"我记住了","createTime":"2026-09-01 12:00:00","openMessageId":"m1","sender":"测试号","senderId":"24710833"},
				{"content":"GoalMate 是工具","createTime":"2026-09-01 12:01:00","openMessageId":"m2","sender":"冬翔","senderId":"103262"}
			]
		}
	}`)
	page, err := parseDWSPage(raw, "24710833")
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if !page.Events[0].Self || page.Events[1].Self {
		t.Fatalf("self flags %+v %+v", page.Events[0], page.Events[1])
	}
}

func TestHistoryLookbackRespectsBootstrap(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-time.Minute)
	row := db.SceneMemory{LeaseTargetThroughAt: timestamptz(cutoff)}
	off := HistoryLookback(row, false, now)
	on := HistoryLookback(row, true, now)
	if !off.After(now.Add(-2 * time.Hour)) {
		t.Fatalf("bootstrap off lookback=%s", off)
	}
	if on.After(now.Add(-13 * 24 * time.Hour)) {
		t.Fatalf("bootstrap on lookback=%s", on)
	}
	row.SourceCursorAt = timestamptz(now.Add(-time.Hour))
	got := HistoryLookback(row, true, now)
	if !got.Equal(now.Add(-time.Hour)) {
		t.Fatalf("cursor lookback=%s", got)
	}
}

func TestHistoryHasGap(t *testing.T) {
	cursor := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	row := db.SceneMemory{SourceCursorAt: timestamptz(cursor)}
	if !historyHasGap(row, cursor.Add(time.Hour), true) {
		t.Fatal("page cap above cursor is a gap")
	}
	if historyHasGap(row, cursor.Add(-time.Minute), true) {
		t.Fatal("reached cursor is not a gap")
	}
	if historyHasGap(row, cursor.Add(time.Hour), false) {
		t.Fatal("no page cap is not a gap")
	}
}

func testFlushRow(text string) db.SceneMemory {
	return db.SceneMemory{
		SceneTitle:     "冬翔",
		SceneKind:      KindDM,
		MemoryText:     text,
		MemoryRevision: 3,
	}
}

func parseFlushTime() time.Time {
	return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
}

type errString string

func (e errString) Error() string { return string(e) }
