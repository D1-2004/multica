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
	err := classifyHistory(errString("redeem token failed"))
	if FlushErrorCode(err) != ErrorAuth {
		t.Fatalf("code=%q", FlushErrorCode(err))
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
