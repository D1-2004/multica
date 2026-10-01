package scenememory

import (
	"strings"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestParseDWSPageKeepsPaginationWithProjectedMessages(t *testing.T) {
	for _, cursor := range []string{`1788450204970`, `"1788450204970"`} {
		page, err := parseDWSPage([]byte(`{"success":true,"messages":[{"messageId":"m1","content":""}],"result":{"hasMore":true,"nextCursor":`+cursor+`}}`), "", "")
		if err != nil || !page.PaginationKnown || !page.HasMore || page.NextCursor.UnixMilli() != 1788450204970 {
			t.Fatalf("cursor=%s page=%+v err=%v", cursor, page, err)
		}
		if len(page.Events) != 0 || len(page.EvidenceIDs) != 1 || page.EvidenceIDs[0] != "m1" {
			t.Fatalf("empty text must keep transport evidence: %+v", page)
		}
	}
}

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
	_, err := parseDWSEvents([]byte(`{"success":false,"errorCode":"auth_failed"}`))
	if err == nil {
		t.Fatal("rejected history must error")
	}
	if !strings.Contains(err.Error(), "auth_failed") {
		t.Fatalf("got %v", err)
	}
	_, err = parseDWSEvents([]byte(`{"success":false,"errorCode":null,"errorMsg":"无权限查看会话"}`))
	if err == nil {
		t.Fatal("rejected history with errorMsg must error")
	}
	if !strings.Contains(err.Error(), "operation_failed") || !strings.Contains(err.Error(), "无权限查看会话") {
		t.Fatalf("got %v", err)
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
	page, err := parseDWSPage(raw, "", "")
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

func TestParseDWSPageAcceptsArrayResult(t *testing.T) {
	raw := []byte(`{
		"messages": [
			{"text":"GoalMate 是工具","createTime":"2026-09-01 12:00:00","messageId":"m1","sender":"冬翔"}
		],
		"result": []
	}`)
	page, err := parseDWSPage(raw, "", "")
	if err != nil || len(page.Events) != 1 || page.Events[0].EvidenceID != "m1" {
		t.Fatalf("array result must not fail unmarshal: page=%+v err=%v", page, err)
	}
}

func TestParseDWSPageRedactsSecrets(t *testing.T) {
	raw := []byte(`{
		"success": true,
		"result": {
			"messages": [
				{"content":"password=hunter2 Bearer abcdefghijklmnop","createTime":"2026-09-01 12:00:00","openMessageId":"m1","sender":"冬翔"}
			]
		}
	}`)
	page, err := parseDWSPage(raw, "", "")
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if strings.Contains(page.Events[0].Content, "hunter2") || strings.Contains(page.Events[0].Content, "abcdefghijklmnop") {
		t.Fatalf("secrets leaked: %q", page.Events[0].Content)
	}
	if !strings.Contains(page.Events[0].Content, "[REDACTED]") {
		t.Fatalf("missing redaction: %q", page.Events[0].Content)
	}
}

func TestParseDWSPageAcceptsTopLevelMessages(t *testing.T) {
	raw := []byte(`{
		"messages": [
			{"text":"GoalMate 是工具","createTime":"2026-09-01 12:00:00","messageId":"m1","sender":"冬翔"}
		]
	}`)
	page, err := parseDWSPage(raw, "", "")
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
	page, err := parseDWSPage(raw, "24710833", "")
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if !page.Events[0].Self || page.Events[1].Self {
		t.Fatalf("self flags %+v %+v", page.Events[0], page.Events[1])
	}
}

func TestParseDWSPageRecordsSelfNamesFromDisplayName(t *testing.T) {
	page, err := parseDWSPage([]byte(`{"success":true,"messages":[{"content":"记一下","createTime":"2026-09-01 12:00:00","openMessageId":"m1","sender":"璟琦"}]}`), "", "金龙")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Self {
		t.Fatalf("peer must not be self: %+v", page.Events)
	}
	if len(page.SelfNames) == 0 || page.SelfNames[0] != "金龙" {
		t.Fatalf("page must carry identity names without a self event: %+v", page.SelfNames)
	}
}

func TestParseDWSPageMarksOtherDigitalEmployeeNonHuman(t *testing.T) {
	raw := []byte(`{
		"success": true,
		"result": {
			"messages": [
				{"content":"我记下了","createTime":"2026-09-01 12:00:00","openMessageId":"m1","sender":"随风","senderType":"digital_employee"},
				{"content":"GoalMate 是工具","createTime":"2026-09-01 12:01:00","openMessageId":"m2","sender":"璟琦","senderType":"user"}
			]
		}
	}`)
	page, err := parseDWSPage(raw, "", "金龙")
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if page.Events[0].Self || !page.Events[0].NonHuman {
		t.Fatalf("other DE must be non-human, not self: %+v", page.Events[0])
	}
	if page.Events[1].Self || page.Events[1].NonHuman {
		t.Fatalf("human must stay peer: %+v", page.Events[1])
	}
}

func TestParseDWSPageSenderTypeDoesNotOverrideIdentity(t *testing.T) {
	raw := []byte(`{
		"success": true,
		"result": {
			"messages": [
				{"content":"我记下了","createTime":"2026-09-01 12:00:00","openMessageId":"m1","sender":"金龙","senderType":"digital_employee"}
			]
		}
	}`)
	page, err := parseDWSPage(raw, "", "金龙")
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if !page.Events[0].Self || page.Events[0].NonHuman {
		t.Fatalf("bound DE must be self, not other-agent: %+v", page.Events[0])
	}
}

func TestParseDWSPageMarksSelfByDisplayName(t *testing.T) {
	raw := []byte(`{
		"success": true,
		"result": {
			"messages": [
				{"content":"WS-42 已 cancelled","createTime":"2026-09-03 14:33:00","openMessageId":"m1","sender":"东翔测试号","senderId":"other"},
				{"content":"从记忆里去掉","createTime":"2026-09-03 14:31:00","openMessageId":"m2","sender":"冬翔","senderId":"103262"}
			]
		}
	}`)
	page, err := parseDWSPage(raw, "24710833", "东翔测试号")
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if !page.Events[0].Self || page.Events[1].Self {
		t.Fatalf("display-name self flags %+v %+v", page.Events[0], page.Events[1])
	}
}

func TestHistoryLookbackRespectsBootstrap(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-time.Minute)
	row := Memory{AgentSceneMemory: db.AgentSceneMemory{LeaseTargetThroughAt: timestamptz(cutoff)}, Scene: db.AgentScene{}}
	off := HistoryLookback(row, false, now)
	on := HistoryLookback(row, true, now)
	if !off.After(now.Add(-2 * time.Hour)) {
		t.Fatalf("bootstrap off lookback=%s", off)
	}
	if on.After(now.Add(-13 * 24 * time.Hour)) {
		t.Fatalf("bootstrap on lookback=%s", on)
	}
	row.LastTriggerAt = timestamptz(now.Add(-time.Minute))
	row.LastTriggerEvidenceID = "msg-now"
	got := HistoryLookback(row, true, now)
	if got.After(now.Add(-13 * 24 * time.Hour)) {
		t.Fatalf("first bootstrap must not collapse to the current trigger, lookback=%s", got)
	}
	row.BootstrappedAt = timestamptz(now.Add(-time.Hour))
	row.SourceCursorAt = timestamptz(now.Add(-time.Hour))
	got = HistoryLookback(row, true, now)
	if !got.Equal(now.Add(-time.Hour)) {
		t.Fatalf("incremental lookback=%s", got)
	}
}

func TestHistoryLookbackBootstrapIncludesOlderPendingTrigger(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	older := now.Add(-20 * 24 * time.Hour)
	row := Memory{AgentSceneMemory: db.AgentSceneMemory{LastTriggerAt: timestamptz(older), LastTriggerEvidenceID: "msg-old"}, Scene: db.AgentScene{}}
	got := HistoryLookback(row, true, now)
	if !got.Equal(older) {
		t.Fatalf("bootstrap lookback must reach an older pending trigger, got %s", got)
	}
}

func TestHistoryLookbackIncludesPendingTriggerBeforeCursor(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	cursor := now.Add(-time.Hour)
	trigger := now.Add(-2 * time.Hour)
	row := Memory{AgentSceneMemory: db.AgentSceneMemory{SourceCursorAt: timestamptz(cursor), LastTriggerAt: timestamptz(trigger), LastTriggerEvidenceID: "msg-early"}, Scene: db.AgentScene{}}
	lookback := HistoryLookback(row, false, now)
	if !lookback.Equal(trigger) {
		t.Fatalf("lookback=%s want pending trigger %s", lookback, trigger)
	}
	got := filterAfterLookback([]HistoryEvent{
		{EvidenceID: "too-old", OccurredAt: trigger.Add(-time.Minute)},
		{EvidenceID: "msg-early", OccurredAt: trigger, Content: "纠正"},
		{EvidenceID: "after-cursor", OccurredAt: cursor},
	}, lookback)
	if len(got) != 2 || got[0].EvidenceID != "msg-early" || got[1].EvidenceID != "after-cursor" {
		t.Fatalf("pending trigger must survive lookback filter: %#v", got)
	}
}

func testFlushRow(text string) Memory {
	return Memory{AgentSceneMemory: db.AgentSceneMemory{MemoryText: text, MemoryRevision: 3}, Scene: db.AgentScene{Title: "冬翔", SceneKind: KindDM}}
}

func parseFlushTime() time.Time {
	return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
}

type errString string

func (e errString) Error() string { return string(e) }
