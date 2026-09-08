package scenememory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
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

func TestMergeFallsBackOnLLMTimeoutForBusyGroups(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(150 * time.Millisecond):
		}
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	t.Cleanup(server.Close)
	f := &MemoryFlusher{LLM: llm.New(llm.Config{APIKey: "test", BaseURL: server.URL, MaxRetries: -1})}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	old := strings.Join([]string{
		"## 场域定位",
		"OwnerGraph",
		"## 稳定知识与约定",
		"- 主链路跳转顺序已冻结 (来自圆畅, 9月3日 20:44的发言)",
	}, "\n")
	got, fallback, err := f.merge(ctx, db.SceneMemory{SceneKey: "cid-ownergraph", MemoryText: old}, []HistoryEvent{
		{Speaker: "圆畅", Content: "PoC主链路Demo筹备群口径不变"},
	}, nil)
	if err == nil || fallback {
		t.Fatalf("timeout must hold the dirty batch, not commit: fallback=%v err=%v got=%q", fallback, err, got)
	}
	if FlushErrorCode(err) != ErrorLLMTimeout {
		t.Fatalf("timeout code = %q, want %s", FlushErrorCode(err), ErrorLLMTimeout)
	}
}

func TestMergeDisablesThinkingAndRequiresCommitTool(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode flush request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"commit","type":"function","function":{"name":"memory_flush_commit","arguments":"{\"decision\":\"unchanged\"}"}}]}}]}`))
	}))
	t.Cleanup(server.Close)
	f := &MemoryFlusher{LLM: llm.New(llm.Config{APIKey: "test", BaseURL: server.URL, MaxRetries: -1})}
	got, fallback, err := f.merge(context.Background(), db.SceneMemory{SceneKey: "cid-ownergraph", MemoryText: "口径"}, []HistoryEvent{
		{Speaker: "圆畅", Content: "口径不变"},
	}, nil)
	if err != nil || fallback || got != "口径" {
		t.Fatalf("merge: text=%q fallback=%v err=%v", got, fallback, err)
	}
	if body["enable_thinking"] != false {
		t.Fatalf("enable_thinking = %#v, want false", body["enable_thinking"])
	}
	if body["tool_choice"] != "required" {
		t.Fatalf("tool_choice = %#v, want required", body["tool_choice"])
	}
	if body["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %#v, want none", body["reasoning_effort"])
	}
	if body["max_completion_tokens"] != float64(flushMaxCompletionTokens) {
		t.Fatalf("max_completion_tokens = %#v, want %d", body["max_completion_tokens"], flushMaxCompletionTokens)
	}
	if body["temperature"] != flushTemperature {
		t.Fatalf("temperature = %#v, want %v", body["temperature"], flushTemperature)
	}
	if body["model"] != flushModel {
		t.Fatalf("model = %#v, want %s", body["model"], flushModel)
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
	_, err := planCompleteFlush(row, []HistoryEvent{{
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
	plan, err := planCompleteFlush(row, []HistoryEvent{{
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
	plan, err := planCompleteFlush(row, []HistoryEvent{{
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
	plan, err := planCompleteFlush(row, []HistoryEvent{
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
	_, err := planCompleteFlush(row, []HistoryEvent{{
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
	plan, err := planCompleteFlush(row, []HistoryEvent{
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
	plan, err := planCompleteFlush(row, []HistoryEvent{
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
	plan, err := planCompleteFlush(db.SceneMemory{}, nil)
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
	}}, nil)
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
	}}, nil)
	if !strings.Contains(prompt, "[self] 测试号: (omitted — this digital employee is not a memory source)") {
		t.Fatalf("missing self tag: %q", prompt)
	}
	if strings.Contains(prompt, "我记下了") {
		t.Fatalf("self utterance must not be extractable: %q", prompt)
	}
	if !strings.Contains(prompt, "[peer] 冬翔: GoalMate 是工具") {
		t.Fatalf("missing peer tag: %q", prompt)
	}
	if !strings.Contains(prompt, "self_speakers: 测试号") {
		t.Fatalf("missing self speaker list: %q", prompt)
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
	}}, nil)
	if !strings.Contains(prompt, "9月1日 20:00 [peer] 辰驷: 记住，须莫喜欢打球") {
		t.Fatalf("prompt=%q", prompt)
	}
	if strings.Contains(prompt, "T12:00:00Z") {
		t.Fatal("flush events should not use UTC RFC3339; the model copies the printed stamp")
	}
}

func TestFlushSystemPromptKeepsLightBackgroundAndCitations(t *testing.T) {
	for _, tt := range []struct {
		name string
		text string
	}{
		{"commit tool only", "Call memory_flush_commit only"},
		{"no analysis or reply", "Do not write analysis, reasoning, or a reply"},
		{"no issue IDs", "No issue IDs"},
		{"memory budget", "1600 Unicode code points"},
		{"next-turn background", "inbound judge's next turn"},
		{"locating section", "## 场域定位"},
		{"knowledge section", "## 稳定知识与约定"},
		{"corrections section", "## 纠正信号"},
		{"uncertainty section", "## 待确认"},
		{"group purpose", "For a group, add a short 用途：… after 成员 answering 这个群是做什么的"},
		{"DM purpose", "For a DM, do not invent a purpose"},
		{"DM human provenance", "Cite only that human as a DM source"},
		{"durable human background", "Keep compact, declarative, human-sourced facts"},
		{"human remember requests", `human [peer] "记住"`},
		{"skip procedures and dumps", "procedures (those are Issues/skills)"},
		{"skip public trivia", "easily rediscoverable public facts"},
		{"skip uncited paraphrase", "including uncited paraphrase"},
		{"uncertain background", "Mark uncertainty [推断] or [待确认] rather than dropping useful background"},
		{"human retractions", "delete matching bullets from every section, without tombstones"},
		{"process debris", "git/pipeline/e2e"},
		{"tasks and sensitive facts", "open tasks/issue progress"},
		{"skip secrets and health", "secrets; health/pay"},
		{"self identity", "[self] events and self_speakers (including aliases) identify this digital employee"},
		{"self sources forbidden", "never use its speech as facts or citations in any section"},
		{"existing self content", "remove existing self-sourced content"},
		{"mixed provenance", "For mixed human+self citations, retain only the human-supported fact and human citation"},
		{"DE reply-style recitation", "Exclude DE recitation of 回复偏好/回复风格 unless stated by a human"},
		{"DE operational limits", "operational limits such as 每次只能回一条"},
		{"citation format", "(来自{speaker}, {M}月{D}日 {HH:mm}的发言)"},
		{"citation clock", "Copy speaker and Asia/Shanghai stamp from the events; never invent them"},
		{"older provenance", "Preserve older citations unless a newer event rewrites the fact"},
		{"unchanged text", "unchanged must equal the old text exactly"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(flushSystemPrompt, tt.text) {
				t.Fatalf("flush prompt missing %q", tt.text)
			}
		})
	}
}

func TestSanitizeFlushTextDropsSelfCitationsAndProcessDebris(t *testing.T) {
	raw := strings.Join([]string{
		"## 场域定位",
		"冬翔",
		"成员：冬翔",
		"## 稳定知识与约定",
		"- feat/agentic-memory-view 已合入 commit d2d5c86ed，阶段 A→D 完成",
		"- GoalMate 是账本/报表工具，不是数字员工或人（来自冬翔, 9月3日 13:31的发言）",
		"- 多件事情沟通时使用 markdown 无序列表格式 (来自冬翔, 9月3日 13:34的发言)",
		"## 纠正信号",
		"- GoalMate 定位为账本/报表工具，非数字员工/人 (来自冬翔, 9月3日 10:27的发言)",
		"- WS-42「向辰驷确认其数字员工是否具备记忆功能并测试交互」状态已改为 cancelled，该记忆测试记录已清除 (来自东翔测试号, 9月3日 14:33的发言)",
		"- 需从冬翔机器中移除 GoalMate 相关内容 (来自冬翔, 9月3日 14:32的发言)",
		"## 待确认",
		"- 从冬翔机器中移除 GoalMate 相关内容的执行情况 (来自冬翔, 9月3日 14:32的发言)",
	}, "\n")
	got := sanitizeFlushText(raw, []HistoryEvent{
		{Speaker: "东翔测试号", Self: true, Content: "WS-42 cancelled"},
		{Speaker: "冬翔", Content: "从记忆里去掉"},
	})
	if strings.Contains(got, "东翔测试号") {
		t.Fatalf("self citation must not survive: %q", got)
	}
	if strings.Contains(got, "d2d5c86ed") || strings.Contains(got, "feat/agentic-memory-view") {
		t.Fatalf("git/process debris must not survive: %q", got)
	}
	if strings.Contains(got, "WS-42") || strings.Contains(got, "需从") || strings.Contains(got, "执行情况") {
		t.Fatalf("issue/task bullets must not survive: %q", got)
	}
	if !strings.Contains(got, "GoalMate 是账本/报表工具") {
		t.Fatalf("human term must stay: %q", got)
	}
	if !strings.Contains(got, "markdown 无序列表") {
		t.Fatalf("human preference must stay: %q", got)
	}
}

func TestMessageIsSelfMatchesDisplayAlias(t *testing.T) {
	if !messageIsSelf(nil, false, "", "菲迪-FDE教练", "", "", "菲迪") {
		t.Fatal("菲迪 must match 菲迪-FDE教练")
	}
	if messageIsSelf(nil, false, "", "菲迪-FDE教练", "", "", "新之助") {
		t.Fatal("peer must not match agent alias")
	}
}

func TestSanitizeFlushTextDropsGroupSelfLimitEvenWithoutSelfTag(t *testing.T) {
	raw := strings.Join([]string{
		"## 场域定位",
		"黑客松项目群",
		"成员：新之助、菲迪（菲迪-FDE教练）",
		"用途：黑客松项目交流",
		"## 稳定知识与约定",
		"- 菲迪每次触发只能回复一条消息，无法一次发两条 (来自菲迪, 9月3日 15:39的发言)",
		"- 听记命名规范：统一增加犇犇犇-前缀 (来自新之助, 9月3日 23:33的发言)",
		"## 纠正信号",
		"## 待确认",
	}, "\n")
	got := sanitizeFlushText(raw, []HistoryEvent{
		{Speaker: "菲迪-FDE教练", Self: true, Content: "我每次只能回一条"},
		{Speaker: "新之助", Content: "听记加前缀"},
	})
	if strings.Contains(got, "每次触发只能回复一条") || strings.Contains(got, "无法一次发两条") {
		t.Fatalf("self operational limit must drop: %q", got)
	}
	if !strings.Contains(got, "听记命名规范") {
		t.Fatalf("human fact must stay: %q", got)
	}
}

func TestSanitizeFlushTextDropsDMSelfCitationWithoutSelfTag(t *testing.T) {
	raw := strings.Join([]string{
		"## 场域定位",
		"冬翔",
		"成员：冬翔",
		"## 稳定知识与约定",
		"- 多件事情沟通时使用 markdown 无序列表格式 (来自冬翔, 9月3日 13:34的发言)",
		"- 回复偏好：简短直接不啰嗦 (来自东翔测试号, 9月3日 17:27的发言)",
		"- 回复偏好：未注明来源的自我复述",
		"## 纠正信号",
		"## 待确认",
	}, "\n")
	got := sanitizeFlushText(raw, nil)
	if strings.Contains(got, "东翔测试号") {
		t.Fatalf("DM citation outside 成员 must drop: %q", got)
	}
	if strings.Contains(got, "未注明来源") {
		t.Fatalf("uncited 回复偏好 must drop: %q", got)
	}
	if !strings.Contains(got, "markdown 无序列表") {
		t.Fatalf("human peer fact must stay: %q", got)
	}
}

func TestSanitizeMemoryTextForAgentDropsGroupSelfCites(t *testing.T) {
	raw := strings.Join([]string{
		"## 场域定位",
		"PoC主链路Demo筹备群",
		"成员：明明就、菲迪、润新",
		"## 稳定知识与约定",
		"- 首屏只露结论 (来自菲迪, 9月3日 19:43的发言)",
		"- 主链路跳转顺序已冻结 (来自圆畅, 9月3日 20:44的发言)",
	}, "\n")
	got := SanitizeMemoryTextForAgent(raw, "菲迪")
	if strings.Contains(got, "来自菲迪") {
		t.Fatalf("group self-cite must drop on host inject: %q", got)
	}
	if !strings.Contains(got, "来自圆畅") {
		t.Fatalf("peer cite must stay: %q", got)
	}
}

func TestSanitizeMemoryTextStripsProcessDebrisForHost(t *testing.T) {
	raw := strings.Join([]string{
		"## 场域定位",
		"冬翔",
		"成员：冬翔",
		"## 稳定知识与约定",
		"- feat/agentic-memory-view 已合入 commit d2d5c86ed，阶段 A→D 完成",
		"- 多件事情沟通时使用 markdown 无序列表格式 (来自冬翔, 9月3日 13:34的发言)",
		"## 纠正信号",
		"- 需从冬翔机器中移除 GoalMate 相关内容 (来自冬翔, 9月3日 14:32的发言)",
		"## 待确认",
		"- 从冬翔机器中移除 GoalMate 相关内容的执行情况 (来自冬翔, 9月3日 14:32的发言)",
	}, "\n")
	got := SanitizeMemoryText(raw, nil)
	if strings.Contains(got, "d2d5c86ed") || strings.Contains(got, "需从") || strings.Contains(got, "执行情况") {
		t.Fatalf("host inject must not see debris: %q", got)
	}
	if !strings.Contains(got, "markdown 无序列表") {
		t.Fatalf("human fact must reach host: %q", got)
	}
}

func TestFallbackMergeSkipsSelfEvents(t *testing.T) {
	got := fallbackMerge("", []HistoryEvent{
		{OccurredAt: parseFlushTime(), Speaker: "东翔测试号", Content: "WS-42 cancelled", Self: true},
		{OccurredAt: parseFlushTime(), Speaker: "冬翔", Content: "GoalMate 是工具"},
	})
	if strings.Contains(got, "WS-42") || strings.Contains(got, "东翔测试号") {
		t.Fatalf("self event must not seed memory: %q", got)
	}
	if !strings.Contains(got, "GoalMate 是工具") {
		t.Fatalf("peer event must seed: %q", got)
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

func TestBuildFlushUserPromptIncludesIdentitySelfNamesWithoutSelfEvents(t *testing.T) {
	prompt := buildFlushUserPrompt(testFlushRow("口径"), []HistoryEvent{{
		OccurredAt: parseFlushTime(),
		Speaker:    "璟琦",
		Content:    "随风统一处理大模型技术问题",
	}}, []string{"金龙"})
	if !strings.Contains(prompt, "self_speakers: 金龙") {
		t.Fatalf("identity must be listed without a self event: %q", prompt)
	}
	if strings.Contains(prompt, "[self] 金龙") {
		t.Fatal("must not invent a self event")
	}
}

func TestSanitizeFlushTextDropsIdentityCitationsWithoutSelfBatch(t *testing.T) {
	raw := strings.Join([]string{
		"## 场域定位",
		"璟琦",
		"成员：璟琦、金龙",
		"## 稳定知识与约定",
		"- 金龙擅长方向：VOC、知识查询 (来自金龙, 9月6日 16:45的发言)",
		"- 宜搭问题找宜搭团队 (来自璟琦, 9月6日 16:37的发言)",
		"## 纠正信号",
		"## 待确认",
	}, "\n")
	got := sanitizeFlushText(raw, []HistoryEvent{
		{Speaker: "璟琦", Content: "宜搭问题找宜搭团队"},
	}, "金龙")
	if strings.Contains(got, "来自金龙") || strings.Contains(got, "金龙擅长方向") {
		t.Fatalf("identity self-cite must drop without a self event in the batch: %q", got)
	}
	if !strings.Contains(got, "宜搭问题找宜搭团队") {
		t.Fatalf("human fact must stay: %q", got)
	}
}

func TestSanitizeFlushTextKeepsHumanFactWhenMixedWithSelfCite(t *testing.T) {
	raw := strings.Join([]string{
		"## 场域定位",
		"璟琦",
		"成员：璟琦、金龙",
		"## 稳定知识与约定",
		"- 随风（算法团队）统一处理大模型技术问题及钉钉产品问题（文档、翻译等），替换了之前由璟琦或蓝派处理的口径。(来自璟琦(主用钉), 9月7日 14:13的发言；来自金龙, 9月7日 18:53的发言)",
		"## 纠正信号",
		"## 待确认",
	}, "\n")
	got := sanitizeFlushText(raw, nil, "金龙")
	if strings.Contains(got, "来自金龙") {
		t.Fatalf("self citation must be stripped from mixed provenance: %q", got)
	}
	if !strings.Contains(got, "随风（算法团队）统一处理大模型技术问题") {
		t.Fatalf("human fact must stay: %q", got)
	}
	if !strings.Contains(got, "来自璟琦(主用钉), 9月7日 14:13的发言") {
		t.Fatalf("human citation must stay: %q", got)
	}
}

func TestSanitizeMemoryTextForAgentDropsDisplayNameWhenAgentNameDiffers(t *testing.T) {
	raw := strings.Join([]string{
		"## 场域定位",
		"PoC主链路Demo筹备群",
		"成员：璟琦、金龙",
		"## 稳定知识与约定",
		"- 金龙背后使用的是通义千问大模型 (来自金龙, 9月7日 13:44的发言)",
		"- 主链路跳转顺序已冻结 (来自圆畅, 9月3日 20:44的发言)",
	}, "\n")
	got := SanitizeMemoryTextForAgent(raw, "VOC数字员工突击队", "金龙")
	if strings.Contains(got, "来自金龙") || strings.Contains(got, "通义千问") {
		t.Fatalf("bound display name must drop even when agent.Name differs: %q", got)
	}
	if !strings.Contains(got, "来自圆畅") {
		t.Fatalf("peer cite must stay: %q", got)
	}
}

func TestSanitizeFlushTextKeepsHumanFactWithChineseCommaMixedCite(t *testing.T) {
	raw := strings.Join([]string{
		"## 场域定位",
		"璟琦",
		"成员：璟琦、金龙",
		"## 稳定知识与约定",
		"- 随风统一处理大模型技术问题 (来自璟琦(主用钉)，9月7日14:13的发言；来自金龙，9月7日 18:53的发言)",
		"## 纠正信号",
		"## 待确认",
	}, "\n")
	got := sanitizeFlushText(raw, nil, "金龙")
	if strings.Contains(got, "来自金龙") {
		t.Fatalf("self citation must strip with Chinese comma: %q", got)
	}
	if !strings.Contains(got, "随风统一处理大模型技术问题") {
		t.Fatalf("human fact must stay: %q", got)
	}
	if !strings.Contains(got, "来自璟琦(主用钉)") {
		t.Fatalf("human citation must stay: %q", got)
	}
}

func TestSanitizeFlushTextDropsLocatingSelfCiteAndUncitedParaphrase(t *testing.T) {
	raw := strings.Join([]string{
		"## 场域定位",
		"VOC群",
		"成员：璟琦、金龙",
		"用途：客户声音",
		"金龙：擅长VOC (来自金龙, 9月6日 16:45的发言)",
		"## 稳定知识与约定",
		"- 金龙擅长方向：VOC、知识查询",
		"- 宜搭问题找宜搭团队 (来自璟琦, 9月6日 16:37的发言)",
		"## 纠正信号",
		"## 待确认",
	}, "\n")
	got := sanitizeFlushText(raw, nil, "金龙")
	if strings.Contains(got, "擅长VOC") || strings.Contains(got, "金龙擅长方向") {
		t.Fatalf("locating/uncited DE paraphrase must drop: %q", got)
	}
	if !strings.Contains(got, "成员：璟琦、金龙") {
		t.Fatalf("member list must stay: %q", got)
	}
	if !strings.Contains(got, "宜搭问题找宜搭团队") {
		t.Fatalf("human fact must stay: %q", got)
	}
}

func TestBuildFlushUserPromptSanitizesMemoryAndOmitsSelfContent(t *testing.T) {
	old := strings.Join([]string{
		"## 场域定位",
		"璟琦",
		"成员：璟琦、金龙",
		"## 稳定知识与约定",
		"- 金龙擅长方向：VOC (来自金龙, 9月6日 16:45的发言)",
		"- 宜搭问题找宜搭团队 (来自璟琦, 9月6日 16:37的发言)",
	}, "\n")
	prompt := buildFlushUserPrompt(testFlushRow(old), []HistoryEvent{{
		OccurredAt: parseFlushTime(),
		Speaker:    "金龙",
		Content:    "我擅长VOC和知识查询",
		Self:       true,
	}, {
		OccurredAt: parseFlushTime(),
		Speaker:    "随风",
		Content:    "算法团队口径",
		NonHuman:   true,
	}, {
		OccurredAt: parseFlushTime(),
		Speaker:    "璟琦",
		Content:    "宜搭问题找宜搭团队",
	}}, []string{"金龙"})
	if strings.Contains(prompt, "来自金龙") || strings.Contains(prompt, "我擅长VOC") {
		t.Fatalf("current_memory and self body must not leak DE speech: %q", prompt)
	}
	if !strings.Contains(prompt, "[agent] 随风: (omitted — bot/digital-employee speech is not a memory source)") {
		t.Fatalf("other DE must be omitted: %q", prompt)
	}
	if !strings.Contains(prompt, "宜搭问题找宜搭团队") {
		t.Fatalf("human fact must reach the model: %q", prompt)
	}
}
