package scenememory

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestParseFlushCommit(t *testing.T) {
	old := "## 场域定位\n- keep"
	text, err := parseFlushCommit(old, `{"decision":"unchanged"}`)
	if err != nil || text != old {
		t.Fatalf("unchanged: text=%q err=%v", text, err)
	}
	text, err = parseFlushCommit(old, `{"decision":"replace","full_text":"## 稳定知识与约定\n- GoalMate 是工具"}`)
	if err != nil || !strings.Contains(text, "GoalMate 是工具") {
		t.Fatalf("replace: text=%q err=%v", text, err)
	}
	tooLong := strings.Repeat("字", MaxMemoryCodePoints+1)
	if _, err := parseFlushCommit(old, `{"decision":"replace","full_text":"`+tooLong+`"}`); err == nil {
		t.Fatal("over-budget replace must fail")
	}
	if _, err := parseFlushCommit(old, `{"decision":"maybe"}`); err == nil {
		t.Fatal("unknown decision must fail")
	}
	if _, err := parseFlushCommit(old, `{"decision":"replace"}`); err == nil {
		t.Fatal("replace without full_text must fail")
	}
	redacted, err := parseFlushCommit(old, `{"decision":"replace","full_text":"token Bearer abcdefghijklmnop"}`)
	if err != nil || !strings.Contains(redacted, "[REDACTED]") || strings.Contains(redacted, "abcdefghijklmnop") {
		t.Fatalf("replace must redact secrets: %q err=%v", redacted, err)
	}
}

func TestFallbackMergeKeepsExistingText(t *testing.T) {
	old := "## 场域定位\n- 已有口径"
	got := fallbackMerge(old, []HistoryEvent{{Speaker: "冬翔", Content: "GoalMate 是工具"}})
	if got != old {
		t.Fatalf("got %q", got)
	}
}

func TestFallbackMergeSeedsEmptyMemory(t *testing.T) {
	got := fallbackMerge("", []HistoryEvent{{Speaker: "冬翔", Content: "GoalMate 是工具，不是数字员工"}})
	if !strings.Contains(got, "GoalMate 是工具") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "## 纠正信号") || !strings.Contains(got, "## 待确认") {
		t.Fatalf("missing headings: %q", got)
	}
	if utf8.RuneCountInString(got) > MaxMemoryCodePoints {
		t.Fatal("fallback exceeded budget")
	}
}

func TestPlanFlushRetriesWhenClaimedEvidenceMissing(t *testing.T) {
	cutoff := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	row := db.SceneMemory{
		LeaseTargetThroughAt:         timestamptz(cutoff),
		LeaseTargetThroughEvidenceID: "msg-inbound",
	}
	_, err := planFlush(row, []HistoryEvent{{
		EvidenceID: "older",
		OccurredAt: cutoff.Add(-time.Minute),
		Content:    "灌水",
	}})
	if FlushErrorCode(err) != ErrorIncomplete {
		t.Fatalf("err=%v", err)
	}
}

func TestPlanFlushCaughtUpWhenClaimedEvidenceVisible(t *testing.T) {
	cutoff := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	row := db.SceneMemory{
		LeaseTargetThroughAt:         timestamptz(cutoff),
		LeaseTargetThroughEvidenceID: "msg-inbound",
		SourceCursorAt:               timestamptz(cutoff.Add(-time.Hour)),
		SourceCursorEvidenceID:       "old",
	}
	plan, err := planFlush(row, []HistoryEvent{{
		EvidenceID: "msg-inbound",
		OccurredAt: cutoff,
		Content:    "记住：ALPHA-7749 是会议室预约脚本",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.caughtUp || len(plan.batch) != 1 || plan.batch[0].EvidenceID != "msg-inbound" {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestPlanFlushIncludesTriggerBeforeCursor(t *testing.T) {
	cutoff := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	row := db.SceneMemory{
		LeaseTargetThroughAt:         timestamptz(cutoff),
		LeaseTargetThroughEvidenceID: "msg-new",
		SourceCursorAt:               timestamptz(cutoff.Add(time.Second)),
		SourceCursorEvidenceID:       "zzz",
	}
	plan, err := planFlush(row, []HistoryEvent{{
		EvidenceID: "msg-new",
		OccurredAt: cutoff,
		Content:    "纠正：DELTA-5520 是排班表",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.batch) != 1 || plan.batch[0].EvidenceID != "msg-new" {
		t.Fatalf("trigger before cursor must still merge: %+v", plan)
	}
	if !plan.caughtUp {
		t.Fatalf("cutoff already behind cursor must still catch up: %+v", plan)
	}
	if !CursorCovers(plan.cursorAt, plan.cursorEv, cutoff.Add(time.Second), "zzz") {
		t.Fatalf("cursor must not rewind: %+v", plan)
	}
}

func TestPlanFlushMergesEarlierTriggerWhenLeaseTargetIsLater(t *testing.T) {
	later := time.Date(2026, 9, 2, 12, 0, 1, 0, time.UTC)
	earlier := later.Add(-time.Second)
	row := claimedAfterLaterThenEarlier(later, "msg-later", earlier, "msg-early")
	plan, err := planFlush(row, []HistoryEvent{
		{EvidenceID: "msg-early", OccurredAt: earlier, Content: "纠正：DELTA-5520 是排班表"},
		{EvidenceID: "msg-later", OccurredAt: later, Content: "灌水"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsEvidence(plan.batch, "msg-early") {
		t.Fatalf("MarkDirty high-water later must still merge the current trigger: %+v", plan)
	}
	if !plan.caughtUp {
		t.Fatalf("covered lease target with visible trigger must catch up: %+v", plan)
	}
	if !CursorCovers(plan.cursorAt, plan.cursorEv, later, "msg-later") {
		t.Fatalf("cursor must stay at the later high-water: %+v", plan)
	}
}

func TestPlanFlushIncompleteWhenEarlierTriggerMissingEvenIfCovered(t *testing.T) {
	later := time.Date(2026, 9, 2, 12, 0, 1, 0, time.UTC)
	earlier := later.Add(-time.Second)
	row := claimedAfterLaterThenEarlier(later, "msg-later", earlier, "msg-early")
	_, err := planFlush(row, []HistoryEvent{{
		EvidenceID: "msg-later",
		OccurredAt: later,
		Content:    "灌水",
	}})
	if FlushErrorCode(err) != ErrorIncomplete {
		t.Fatalf("missing pending trigger must not finish via covered empty-delta, err=%v", err)
	}
}

func TestPlanFlushMergesAllLateTriggersInWindow(t *testing.T) {
	later := time.Date(2026, 9, 2, 12, 0, 2, 0, time.UTC)
	mid := later.Add(-time.Second)
	early := later.Add(-2 * time.Second)
	row := db.SceneMemory{
		LeaseTargetThroughAt:         timestamptz(later),
		LeaseTargetThroughEvidenceID: "msg-later",
		LastTriggerAt:                timestamptz(mid),
		LastTriggerEvidenceID:        "msg-mid",
		PendingFromAt:                timestamptz(early),
		PendingFromEvidenceID:        "msg-early",
		SourceCursorAt:               timestamptz(later),
		SourceCursorEvidenceID:       "msg-later",
	}
	plan, err := planFlush(row, []HistoryEvent{
		{EvidenceID: "msg-early", OccurredAt: early, Content: "late A"},
		{EvidenceID: "msg-mid", OccurredAt: mid, Content: "late B"},
		{EvidenceID: "msg-later", OccurredAt: later, Content: "high-water"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsEvidence(plan.batch, "msg-early") || !containsEvidence(plan.batch, "msg-mid") {
		t.Fatalf("debounce window must merge every late trigger, not only last_trigger: %+v", plan)
	}
}

func TestPlanFlushSameSecondSmallerEvidence(t *testing.T) {
	at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	row := claimedAfterLaterThenEarlier(at, "zzz", at, "aaa")
	plan, err := planFlush(row, []HistoryEvent{
		{EvidenceID: "aaa", OccurredAt: at, Content: "纠正：同一秒更小 id"},
		{EvidenceID: "zzz", OccurredAt: at, Content: "灌水"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsEvidence(plan.batch, "aaa") {
		t.Fatalf("same-second smaller evidence must still merge: %+v", plan)
	}
	if !plan.caughtUp || plan.cursorEv != "zzz" {
		t.Fatalf("cursor must stay on the high-water evidence: %+v", plan)
	}
}

func claimedAfterLaterThenEarlier(laterAt time.Time, laterEv string, earlierAt time.Time, earlierEv string) db.SceneMemory {
	return db.SceneMemory{
		LeaseTargetThroughAt:         timestamptz(laterAt),
		LeaseTargetThroughEvidenceID: laterEv,
		LastTriggerAt:                timestamptz(earlierAt),
		LastTriggerEvidenceID:        earlierEv,
		SourceCursorAt:               timestamptz(laterAt),
		SourceCursorEvidenceID:       laterEv,
	}
}

func TestPlanFlushEmptyDeltaWithoutCutoffIsCaughtUp(t *testing.T) {
	plan, err := planFlush(db.SceneMemory{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.caughtUp || len(plan.batch) != 0 {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestClassifyHistoryKeepsRedeemNetworkErrorsRetryable(t *testing.T) {
	err := classifyHistory(errString("Agent Identity DWS redeem request failed"))
	if FlushErrorCode(err) != "" {
		t.Fatalf("network redeem must retry, code=%q", FlushErrorCode(err))
	}
	err = classifyHistory(errString("Agent Identity DWS redeem rejected: unauthorized"))
	if FlushErrorCode(err) != ErrorAuth {
		t.Fatalf("rejected redeem must be AUTH, code=%q", FlushErrorCode(err))
	}
}

func TestBuildFlushUserPromptOmitsIssueIDs(t *testing.T) {
	prompt := buildFlushUserPrompt(testFlushRow("口径"), []HistoryEvent{{
		OccurredAt: parseFlushTime(),
		Speaker:    "冬翔",
		Content:    "GoalMate 是工具",
	}})
	if !strings.Contains(prompt, "current_memory:") || !strings.Contains(prompt, "GoalMate 是工具") {
		t.Fatalf("prompt=%q", prompt)
	}
	if strings.Contains(prompt, "issue_id") {
		t.Fatal("flush prompt must not mention issue_id")
	}
}

func TestBuildFlushUserPromptTagsSelfEvents(t *testing.T) {
	prompt := buildFlushUserPrompt(testFlushRow("口径"), []HistoryEvent{{
		OccurredAt: parseFlushTime(),
		Speaker:    "测试号",
		Content:    "我记下了",
		Self:       true,
	}, {
		OccurredAt: parseFlushTime(),
		Speaker:    "冬翔",
		Content:    "GoalMate 是工具",
	}})
	if !strings.Contains(prompt, "[self] 测试号: 我记下了") {
		t.Fatalf("missing self tag: %q", prompt)
	}
	if !strings.Contains(prompt, "[peer] 冬翔: GoalMate 是工具") {
		t.Fatalf("missing peer tag: %q", prompt)
	}
}

func TestFormatFlushStampUsesShanghaiClock(t *testing.T) {
	// 12:00 UTC on 1 Sep is 20:00 in Asia/Shanghai.
	got := formatFlushStamp(parseFlushTime())
	if got != "9月1日 20:00" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildFlushUserPromptPrintsShanghaiStamp(t *testing.T) {
	prompt := buildFlushUserPrompt(testFlushRow("口径"), []HistoryEvent{{
		OccurredAt: parseFlushTime(),
		Speaker:    "辰驷",
		Content:    "记住，须莫喜欢打球",
	}})
	if !strings.Contains(prompt, "9月1日 20:00 [peer] 辰驷: 记住，须莫喜欢打球") {
		t.Fatalf("prompt=%q", prompt)
	}
	if strings.Contains(prompt, "T12:00:00Z") {
		t.Fatal("flush events should not use UTC RFC3339; the model copies the printed stamp")
	}
}

func TestFlushSystemPromptKeepsLightBackgroundAndCitations(t *testing.T) {
	if !strings.Contains(flushSystemPrompt, "(来自{speaker}, {M}月{D}日 {HH:mm}的发言)") {
		t.Fatal("flush must ask for a simple provenance citation")
	}
	if !strings.Contains(flushSystemPrompt, "inbound judge") {
		t.Fatal("flush must keep background the next Coordinator turn needs")
	}
	if !strings.Contains(flushSystemPrompt, "For a group, add one short line per known person") {
		t.Fatal("flush must keep counterpart lines on groups")
	}
	if !strings.Contains(flushSystemPrompt, `A human [peer] "记住 …"`) {
		t.Fatal("flush must keep an explicit 记住 from a human")
	}
	if !strings.Contains(flushSystemPrompt, "If unsure, write one [待确认] line instead of dropping the fact") {
		t.Fatal("flush must not drop borderline facts")
	}
}

func TestFallbackMergeCitesSpeakerWhenTimeIsKnown(t *testing.T) {
	got := fallbackMerge("", []HistoryEvent{{
		OccurredAt: parseFlushTime(),
		Speaker:    "辰驷",
		Content:    "须莫喜欢打球",
	}})
	if !strings.Contains(got, "(来自辰驷, 9月1日 20:00的发言)") {
		t.Fatalf("got %q", got)
	}
}
