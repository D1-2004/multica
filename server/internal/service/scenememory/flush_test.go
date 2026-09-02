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
