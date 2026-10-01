package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// This suite calls a real model only after explicit opt-in. It has no DB,
// dispatch, DWS client, callback, or write-tool implementation. Successful
// finish.items are inspected as plans; they can never execute business work.
//
// MULTICA_RUN_COORDINATOR_REPLAY=1 enables the suite.
// MULTICA_LLM_API_KEY / MULTICA_LLM_BASE_URL configure the normal llm.Client.
// MULTICA_COORDINATOR_REPLAY_CASES is an optional comma-separated case filter.
// MULTICA_COORDINATOR_REPLAY_REPEAT is 1 by default (maximum 10).
// MULTICA_COORDINATOR_REPLAY_REPORT selects the JSON report file.
// MULTICA_COORDINATOR_REPLAY_MODEL optionally overrides the production model;
// the report records both requested and actual response model names.
func TestCoordinatorPolicyReplay(t *testing.T) {
	if os.Getenv("MULTICA_RUN_COORDINATOR_REPLAY") != "1" {
		t.Skip("real model replay is disabled; set MULTICA_RUN_COORDINATOR_REPLAY=1 explicitly")
	}
	cfg := llm.Config{APIKey: os.Getenv("MULTICA_LLM_API_KEY"), BaseURL: os.Getenv("MULTICA_LLM_BASE_URL"), DefaultModel: coordinatorModel, MaxRetries: -1}
	client := llm.New(cfg)
	if !client.Enabled() {
		t.Skip("no model configuration; supply MULTICA_LLM_API_KEY and/or MULTICA_LLM_BASE_URL; no credential or endpoint is printed")
	}
	repeat := 1
	if value := os.Getenv("MULTICA_COORDINATOR_REPLAY_REPEAT"); value != "" {
		var err error
		repeat, err = strconv.Atoi(value)
		if err != nil || repeat < 1 || repeat > 10 {
			t.Fatal("MULTICA_COORDINATOR_REPLAY_REPEAT must be between 1 and 10")
		}
	}
	filter := map[string]bool{}
	for _, id := range strings.Split(os.Getenv("MULTICA_COORDINATOR_REPLAY_CASES"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			filter[id] = true
		}
	}
	fixtures := append(append(coordinatorReplayFixtures(), proactiveRelevanceFixtures()...), genericConversationFixtures()...)
	known := map[string]bool{}
	for _, fixture := range fixtures {
		known[fixture.ID] = true
	}
	for id := range filter {
		if !known[id] {
			t.Fatalf("unknown replay case %q", id)
		}
	}
	report := coordinatorReplayReport{
		PolicyVersion: coordinatorPolicy.Version, AssemblyVersion: coordinatorPolicy.AssemblyVersion, StartedAt: time.Now().UTC(),
		Safety: "Real model, synthetic frozen dialogue, fake read-only tools and history; no Host submission or business effects.",
		Scope:  "Assertions cover action/work count, continuation target, source attribution, work basis and disclosure path; answer prose requires separate human review.",
		Cases:  []coordinatorReplayResult{},
	}
	for iteration := 1; iteration <= repeat; iteration++ {
		for _, fixture := range fixtures {
			if len(filter) > 0 && !filter[fixture.ID] {
				continue
			}
			t.Run(fmt.Sprintf("%s/repeat-%d", fixture.ID, iteration), func(t *testing.T) {
				started := time.Now()
				fakeTools := &replayReadTools{scene: fixture.Turn.ConversationID, cards: fixture.Cards}
				fakeHistory := &replayHistory{lines: fixture.History, scene: fixture.Turn.ConversationID}
				observer := &replayCompleter{client: client, readTools: fakeTools, modelOverride: os.Getenv("MULTICA_COORDINATOR_REPLAY_MODEL")}
				coordinator := &Coordinator{LLM: client, Chat: observer, Tools: fakeTools, DWSHistory: fakeHistory}
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				decision, err := coordinator.runLoop(ctx, fixture.Turn)
				cancel()
				result := evaluateCoordinatorReplay(fixture, decision, err, observer, fakeTools, fakeHistory)
				result.Repeat = iteration
				result.ElapsedMS = time.Since(started).Milliseconds()
				report.Cases = append(report.Cases, result)
				t.Logf("replay case=%s status=%s models=%v input_tokens=%v output_tokens=%v rounds=%d work_items=%d", result.ID, result.Status, result.Models, replayTokenLabel(result.InputTokens), replayTokenLabel(result.OutputTokens), len(result.Rounds), len(result.Items))
				for _, failure := range result.Failures {
					t.Error(failure)
				}
			})
		}
	}
	report.CompletedAt = time.Now().UTC()
	report.Status = "PASS"
	for _, result := range report.Cases {
		if result.Status != "PASS" {
			report.Status = "FAIL"
		}
	}
	path := os.Getenv("MULTICA_COORDINATOR_REPLAY_REPORT")
	if path == "" {
		_, thisFile, _, _ := runtime.Caller(0)
		path = filepath.Join(filepath.Dir(thisFile), "../../../../docs/evals/results", report.StartedAt.Format("20060102T150405Z")+"-coordinator-policy-replay.json")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal("resolve replay report path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal("create replay report directory")
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal("encode replay report")
	}
	if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
		t.Fatal("write replay report")
	}
	t.Logf("replay report: %s", path)
}

type replayCard struct {
	ID, Purpose, Status, Comment string
}

type replayExpectedItem struct {
	IssueID string
	Refs    []string
	Basis   string
}

type coordinatorReplayFixture struct {
	ID, ContractID string
	Turn           Turn
	History        []HistoryLine
	Cards          []replayCard
	Actions        []Action
	Items          []replayExpectedItem
	NonWorkRefs    []string
	RequireHistory bool
	RequireRecall  bool
	ForbidReads    bool
}

func coordinatorReplayFixtures() []coordinatorReplayFixture {
	stamp := time.Date(2026, 9, 7, 11, 14, 1, 0, time.UTC)
	base := func(message string) Turn {
		return Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "p2p", SceneID: testSceneID("cidReplaySyntheticSceneA=="), ConversationID: "cidReplaySyntheticSceneA==", PersonID: "synthetic-speaker", SenderName: "小周", AgentName: "小助", Message: message, HistoryStatus: "not_loaded", SkillsStatus: "empty", SceneMemoryStatus: "empty", MessageTimestamp: stamp, HistoryBefore: stamp, EvidenceID: "synthetic-current"}
	}
	history := func(question string) []HistoryLine {
		return []HistoryLine{{Role: "assistant", Content: question, EvidenceID: "synthetic-prior-question", SenderID: "synthetic-agent", Timestamp: stamp.Add(-time.Minute)}}
	}
	meeting := replayCard{ID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Purpose: "小周委托：向小林发送已经写好的周五三点会议通知", Status: "waiting", Comment: "通知已起草，等待小周确认是否发送；尚未发送。"}
	document := replayCard{ID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", Purpose: "小周委托：整理项目方案文档并补齐评审内容", Status: "in_progress", Comment: "文档初稿已保存，还可以补充内容。"}
	missing := base("给小林发条消息。")
	echo := base("取消刚才给小林发消息的请求。这是一项独立预发测试：实际执行 printf PGCTX_SYNTHETIC_OK，把命令输出发到当前会话，只发一次。")
	supplied := base("给小林发个你好。")
	oldGreeting := replayCard{ID: "cccccccc-cccc-cccc-cccc-cccccccccccc", Purpose: "向小林发送打招呼消息", Status: "waiting", Comment: "昨天已经发送你好并确认送达。"}
	capabilities := base("你会什么，有哪些 skill？")
	capabilities.SkillsStatus = "loaded"
	capabilities.Skills = []SkillSnapshot{{Name: "会议整理", Description: "读取指定会议记录，整理结论和行动项"}}
	execute := capabilities
	execute.Message = "请用会议整理这个 skill，把今天十点的产品评审会整理成结论和行动项给我。"
	approval := base("好")
	closing := base("好")
	progress := base("通知怎么还没发好？")
	retry := base("刚才发送失败了，权限已经恢复，请按原方案再试一次，还是把周五三点的会议通知发给小林。")
	failed := meeting
	failed.Status = "blocked"
	failed.Comment = "发送接口因权限不足失败；没有任何消息送达。"
	different := base("和小林确认一下明天几点有空去打球，得到答复后告诉我。")
	other := meeting
	other.Purpose = "小周委托：向小林确认明天洗脚的时间"
	other.Comment = "已经问了洗脚时间，等待小林回答。"
	mixed := base("unused aggregate")
	mixed.Utterances = []WindowUtterance{{Sender: "小周", SenderID: "synthetic-speaker", Text: "刚才那份给小林的周五三点会议通知，地点改为线上。"}, {Sender: "小周", SenderID: "synthetic-speaker", Text: "另外帮我查一下杭州明天的天气。"}, {Sender: "小周", SenderID: "synthetic-speaker", Text: "谢谢"}}
	two := base("unused aggregate")
	two.Utterances = []WindowUtterance{{Sender: "小周", SenderID: "synthetic-speaker", Text: "给小林的周五三点会议通知，地点改成线上。"}, {Sender: "小周", SenderID: "synthetic-speaker", Text: "项目方案文档再加一节风险清单。"}}
	memory := base("你现在记得我的哪些偏好和约定？请列出来。")
	memory.SceneMemoryStatus, memory.SceneMemoryRevision = "loaded", 3
	memory.SceneMemory = "## 稳定知识与约定\n- 和小周沟通优先中文。\n- 周报应先列决策，再列行动项。\n- 会议时间均按北京时间理解。"
	inventory := memory
	inventory.Message = "你手头还有哪些事情没办完？"
	return []coordinatorReplayFixture{
		{ID: "sandbox_command_handoff", ContractID: "f04_contrast", Turn: echo, Actions: []Action{ActionIssue}, RequireRecall: true, Items: []replayExpectedItem{{Refs: []string{"u1"}, Basis: "new_request"}}},
		{ID: "missing_message_payload", ContractID: "f05_contrast", Turn: missing, Cards: []replayCard{meeting, oldGreeting}, Actions: []Action{ActionReply}},
		{ID: "supplied_message_payload", ContractID: "f05_contrast", Turn: supplied, Actions: []Action{ActionIssue}, RequireRecall: true, Items: []replayExpectedItem{{Refs: []string{"u1"}, Basis: "new_request"}}},
		{ID: "capability_inventory", ContractID: "f04_contrast", Turn: capabilities, Actions: []Action{ActionReply}, ForbidReads: true},
		{ID: "capability_execution", ContractID: "f04_contrast", Turn: execute, Actions: []Action{ActionIssue}, RequireRecall: true, Items: []replayExpectedItem{{Refs: []string{"u1"}, Basis: "new_request"}}},
		{ID: "approval_good", ContractID: "f05_contrast", Turn: approval, History: history("给小林的周五三点会议通知已经起草好了。现在按这版发给小林吗？"), Cards: []replayCard{meeting}, Actions: []Action{ActionIssue}, RequireHistory: true, RequireRecall: true, Items: []replayExpectedItem{{IssueID: meeting.ID, Refs: []string{"u1"}, Basis: "answer"}}},
		{ID: "closing_good", ContractID: "f05_contrast", Turn: closing, History: history("周五三点的会议通知已成功发给小林，他也已经确认收到。这件事办完了。"), Actions: []Action{ActionReply, ActionSilence}, RequireHistory: true},
		{ID: "status_ping", ContractID: "f05_contrast", Turn: progress, Cards: []replayCard{meeting}, Actions: []Action{ActionReply}, RequireRecall: true},
		{ID: "explicit_retry", ContractID: "f05_contrast", Turn: retry, Cards: []replayCard{failed}, Actions: []Action{ActionIssue}, RequireRecall: true, Items: []replayExpectedItem{{IssueID: meeting.ID, Refs: []string{"u1"}, Basis: "retry"}}},
		{ID: "same_person_different_work", ContractID: "f07_contrast", Turn: different, Cards: []replayCard{other}, Actions: []Action{ActionIssue}, RequireRecall: true, Items: []replayExpectedItem{{Refs: []string{"u1"}, Basis: "new_request"}}},
		{ID: "mixed_continuation_and_new_work", ContractID: "f10_contrast", Turn: mixed, Cards: []replayCard{meeting}, Actions: []Action{ActionIssue}, RequireRecall: true, Items: []replayExpectedItem{{IssueID: meeting.ID, Refs: []string{"u1"}, Basis: "change"}, {Refs: []string{"u2"}, Basis: "new_request"}}, NonWorkRefs: []string{"u3"}},
		{ID: "two_continuations", ContractID: "f10_contrast", Turn: two, Cards: []replayCard{meeting, document}, Actions: []Action{ActionIssue}, RequireRecall: true, Items: []replayExpectedItem{{IssueID: meeting.ID, Refs: []string{"u1"}, Basis: "change"}, {IssueID: document.ID, Refs: []string{"u2"}, Basis: "change"}}},
		{ID: "memory_inventory", ContractID: "f11_contrast", Turn: memory, Actions: []Action{ActionReply}, ForbidReads: true},
		{ID: "open_work_inventory", ContractID: "f03_contrast", Turn: inventory, Cards: []replayCard{meeting, document}, Actions: []Action{ActionReply}, RequireRecall: true},
	}
}

type replayReadTools struct {
	scene      string
	cards      []replayCard
	calls      []string
	recalled   bool
	violations []string
}

func (f *replayReadTools) Call(_ context.Context, _ Turn, name, arguments string) (string, error) {
	f.calls = append(f.calls, name)
	var args struct {
		ConversationID string `json:"conversation_id"`
		IssueID        string `json:"issue_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid fake read arguments")
	}
	if name == toolAssocRecall {
		if args.ConversationID != f.scene {
			f.violations = append(f.violations, "recall escaped the fixture scene")
			return "", fmt.Errorf("fixture scene mismatch")
		}
		f.recalled = true
		items := []map[string]any{}
		for _, card := range f.cards {
			items = append(items, map[string]any{"issue_id": card.ID, "purpose": card.Purpose, "status": card.Status, "on_this_scene": true, "why": "本会话事项", "last_touched": "1分钟前", "last_comment": card.Comment})
		}
		return replayJSON(map[string]any{"status": "loaded", "conversation_id": f.scene, "complete": true, "items": items, "events": []any{}, "read_this": "These are candidate matters in this scene; only current substantive input on the same deliverable can continue work."}), nil
	}
	if name == toolWorkState || name == toolIssueGet || name == toolIssueCommentList {
		if !f.recalled {
			f.violations = append(f.violations, "issue read occurred before recall")
			return "", fmt.Errorf("fixture target not recalled")
		}
		for _, card := range f.cards {
			if card.ID == args.IssueID {
				if name == toolWorkState {
					return replayJSON(map[string]any{"issue_id": card.ID, "title": card.Purpose, "original_goal": card.Purpose, "status": card.Status, "status_source": coordinationIssueStatusSource, "scope": coordinationIssueScope}), nil
				}
				if name == toolIssueGet {
					return replayJSON(map[string]any{"issue_id": card.ID, "title": card.Purpose, "status": card.Status, "description": card.Comment}), nil
				}
				return replayJSON(map[string]any{"issue_id": card.ID, "comments": []map[string]any{{"author_type": "agent", "content": card.Comment}}}), nil
			}
		}
		f.violations = append(f.violations, "issue read used an unknown fixture target")
		return "", fmt.Errorf("unknown fixture target")
	}
	f.violations = append(f.violations, "non-read tool attempted: "+name)
	return "", fmt.Errorf("replay has no business write implementation")
}

type replayHistory struct {
	lines      []HistoryLine
	scene      string
	calls      int
	violations []string
}

func (f *replayHistory) Load(_ context.Context, turn Turn) ([]HistoryLine, error) {
	f.calls++
	if turn.ConversationID != f.scene {
		f.violations = append(f.violations, "history escaped the fixture scene")
		return nil, fmt.Errorf("fixture history scene mismatch")
	}
	return append([]HistoryLine(nil), f.lines...), nil
}

func replayJSON(value any) string {
	body, _ := json.Marshal(value)
	return string(body)
}

type coordinatorReplayRound struct {
	Round          int      `json:"round"`
	RequestModel   string   `json:"request_model"`
	ResponseModel  string   `json:"response_model"`
	PromptHash     string   `json:"prompt_hash"`
	ToolSchemaHash string   `json:"tool_schema_hash"`
	Modules        []string `json:"modules"`
	AllowedTools   []string `json:"allowed_tools"`
	CalledTools    []string `json:"called_tools"`
	InputTokens    *int64   `json:"input_tokens"`
	OutputTokens   *int64   `json:"output_tokens"`
}

type replayCompleter struct {
	client        *llm.Client
	readTools     *replayReadTools
	modelOverride string
	// The speculative conversation render shares this recorder with routing.
	mu         sync.Mutex
	rounds     []coordinatorReplayRound
	violations []string
}

func (o *replayCompleter) recordedRounds() []coordinatorReplayRound {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]coordinatorReplayRound(nil), o.rounds...)
}

func (o *replayCompleter) recordedViolations() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.violations...)
}

var replayModulePattern = regexp.MustCompile(`\[policy:([^@\]]+)@[^\]]+\]`)

func (o *replayCompleter) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	if model := strings.TrimSpace(o.modelOverride); model != "" {
		params.Model = shared.ChatModel(model)
	}
	toolSchema, _ := json.Marshal(params.Tools)
	o.mu.Lock()
	defer o.mu.Unlock()
	round := coordinatorReplayRound{ToolSchemaHash: policyHash(string(toolSchema)), Round: len(o.rounds) + 1, RequestModel: string(params.Model), AllowedTools: toolParamNames(params.Tools), Modules: []string{}, CalledTools: []string{}}
	if len(params.Messages) > 0 {
		raw, _ := json.Marshal(params.Messages[0])
		var system struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(raw, &system)
		round.PromptHash = policyHash(system.Content)
		for _, match := range replayModulePattern.FindAllStringSubmatch(system.Content, -1) {
			round.Modules = append(round.Modules, match[1])
		}
	}
	for _, tool := range round.AllowedTools {
		if tool == toolAssocBind || tool == toolIssueCommentAdd {
			o.violations = append(o.violations, "business write tool was exposed to replay model")
		}
		if (tool == toolIssueGet || tool == toolIssueCommentList) && (!o.readTools.recalled || !slices.Contains(round.Modules, "recall_match")) {
			o.violations = append(o.violations, "issue read exposed before successful recall and matching policy")
		}
	}
	completion, err := o.client.Chat(ctx, params)
	if completion != nil {
		round.ResponseModel = completion.Model
		if completion.Usage.JSON.PromptTokens.Valid() {
			value := completion.Usage.PromptTokens
			round.InputTokens = &value
		}
		if completion.Usage.JSON.CompletionTokens.Valid() {
			value := completion.Usage.CompletionTokens
			round.OutputTokens = &value
		}
		if len(completion.Choices) > 0 {
			for _, call := range functionToolCalls(completion.Choices[0].Message) {
				round.CalledTools = append(round.CalledTools, call.Name)
				if call.Name == toolAssocBind || call.Name == toolIssueCommentAdd {
					o.violations = append(o.violations, "model attempted a removed business write tool")
				}
				if call.Name == toolFinish {
					var plan struct {
						Action string `json:"action"`
					}
					_ = json.Unmarshal([]byte(call.Arguments), &plan)
					if plan.Action == "issue" && (!o.readTools.recalled || !slices.Contains(round.Modules, "recall_match")) {
						o.violations = append(o.violations, "work plan attempted before recall/matching disclosure")
					}
				}
			}
		}
	}
	o.rounds = append(o.rounds, round)
	return completion, err
}

type coordinatorReplayResult struct {
	ID           string                   `json:"id"`
	ContractID   string                   `json:"contract_id"`
	FixtureHash  string                   `json:"fixture_hash"`
	Repeat       int                      `json:"repeat"`
	Status       string                   `json:"status"`
	Action       Action                   `json:"action"`
	Reply        string                   `json:"reply_for_manual_review"`
	Items        []WindowItem             `json:"planned_items"`
	NonWorkRefs  []string                 `json:"non_work_refs"`
	HistoryReads int                      `json:"fake_history_reads"`
	ReadCalls    []string                 `json:"fake_read_calls"`
	Models       []string                 `json:"models"`
	Rounds       []coordinatorReplayRound `json:"rounds"`
	InputTokens  *int64                   `json:"input_tokens"`
	OutputTokens *int64                   `json:"output_tokens"`
	ElapsedMS    int64                    `json:"elapsed_ms"`
	Assertions   []string                 `json:"assertions"`
	Failures     []string                 `json:"failures"`
}

type coordinatorReplayReport struct {
	PolicyVersion   string                    `json:"policy_version"`
	AssemblyVersion string                    `json:"assembly_version"`
	StartedAt       time.Time                 `json:"started_at"`
	CompletedAt     time.Time                 `json:"completed_at"`
	Status          string                    `json:"status"`
	Safety          string                    `json:"safety"`
	Scope           string                    `json:"assertion_scope"`
	Cases           []coordinatorReplayResult `json:"cases"`
}

func evaluateCoordinatorReplay(f coordinatorReplayFixture, d Decision, err error, observer *replayCompleter, reads *replayReadTools, history *replayHistory) coordinatorReplayResult {
	r := coordinatorReplayResult{ID: f.ID, ContractID: f.ContractID, FixtureHash: policyHash(replayJSON(f)), Status: "PASS", Action: d.Action, Reply: d.UserText, Items: d.Items, NonWorkRefs: d.NonWorkRefs, HistoryReads: history.calls, ReadCalls: reads.calls, Rounds: observer.recordedRounds(), Assertions: []string{}, Failures: []string{}, Models: []string{}}
	assert := func(ok bool, message string) {
		r.Assertions = append(r.Assertions, message)
		if !ok {
			r.Failures = append(r.Failures, message)
		}
	}
	if err != nil {
		// Error bodies can contain gateway URLs or credentials. Report only the
		// class; the owning operator can investigate upstream logs separately.
		r.Failures = append(r.Failures, fmt.Sprintf("loop failed (%T); upstream body omitted", err))
	}
	assert(slices.Contains(f.Actions, d.Action), fmt.Sprintf("action must be one of %v", f.Actions))
	assert(len(d.Items) == len(f.Items), fmt.Sprintf("work item count must be %d", len(f.Items)))
	if len(f.Items) > 0 {
		assert(sameReplayRefs(d.NonWorkRefs, f.NonWorkRefs), fmt.Sprintf("non-work references must be %v", f.NonWorkRefs))
	} else {
		for _, ref := range d.NonWorkRefs {
			index, parseErr := strconv.Atoi(strings.TrimPrefix(ref, "u"))
			assert(parseErr == nil && index > 0 && index <= len(windowUtterances(f.Turn)), "conversation-only references must stay inside the current window")
		}
	}
	for _, expected := range f.Items {
		matched := false
		for _, actual := range d.Items {
			if actual.IssueID == expected.IssueID && actual.Basis == expected.Basis && sameReplayRefs(actual.SourceRefs, expected.Refs) {
				matched = true
				var wantedContent []string
				for _, ref := range actual.SourceRefs {
					index, _ := strconv.Atoi(strings.TrimPrefix(ref, "u"))
					utterances := windowUtterances(f.Turn)
					if index > 0 && index <= len(utterances) {
						u := utterances[index-1]
						wantedContent = append(wantedContent, firstNonEmpty(u.Sender, "用户")+" 在钉钉会话中的消息：\n\n"+u.Text)
					}
				}
				assert(actual.Content == strings.Join(wantedContent, "\n\n"), "Host must preserve exact attributed source content")
				break
			}
		}
		assert(matched, fmt.Sprintf("work plan must include target=%q refs=%v basis=%s", expected.IssueID, expected.Refs, expected.Basis))
	}
	if f.RequireHistory {
		assert(history.calls > 0, "original dialogue must be read before interpreting this short answer")
	}
	if f.RequireRecall {
		assert(reads.recalled, "current-scene recall must establish work evidence")
	}
	if f.ForbidReads {
		assert(history.calls == 0 && len(reads.calls) == 0, "direct inventory answer must use provided facts without extra reads")
		routingRounds := 0
		for _, round := range observer.recordedRounds() {
			if !slices.Contains(round.AllowedTools, "finish_check") {
				routingRounds++
			}
		}
		assert(routingRounds == 1, "direct inventory answer must retain one routing round, plus independent terminal review")
	}
	for _, failure := range append(append(append([]string{}, observer.recordedViolations()...), reads.violations...), history.violations...) {
		r.Failures = append(r.Failures, failure)
	}
	var input, output int64
	recorded := observer.recordedRounds()
	allInput, allOutput := len(recorded) > 0, len(recorded) > 0
	for _, round := range recorded {
		model := firstNonEmpty(round.ResponseModel, round.RequestModel)
		if !slices.Contains(r.Models, model) {
			r.Models = append(r.Models, model)
		}
		if round.InputTokens == nil {
			allInput = false
		} else {
			input += *round.InputTokens
		}
		if round.OutputTokens == nil {
			allOutput = false
		} else {
			output += *round.OutputTokens
		}
	}
	if allInput {
		r.InputTokens = &input
	}
	if allOutput {
		r.OutputTokens = &output
	}
	if len(r.Failures) > 0 {
		r.Status = "FAIL"
	}
	return r
}

func sameReplayRefs(a, b []string) bool {
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func replayTokenLabel(tokens *int64) any {
	if tokens == nil {
		return "unavailable"
	}
	return *tokens
}

func TestCoordinatorPolicyReplayFixtures(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	body, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "policy/cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Cases []struct {
			ID string `json:"id"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatal(err)
	}
	contracts := map[string]bool{}
	for _, item := range catalog.Cases {
		contracts[item.ID] = true
	}
	seen := map[string]bool{}
	for _, fixture := range append(append(coordinatorReplayFixtures(), proactiveRelevanceFixtures()...), genericConversationFixtures()...) {
		if seen[fixture.ID] || fixture.ID == "" {
			t.Fatalf("duplicate or empty replay fixture %q", fixture.ID)
		}
		seen[fixture.ID] = true
		if !contracts[fixture.ContractID] {
			t.Fatalf("unknown contrast contract %q", fixture.ContractID)
		}
		for _, item := range fixture.Items {
			for _, ref := range item.Refs {
				index, err := strconv.Atoi(strings.TrimPrefix(ref, "u"))
				if err != nil || index < 1 || index > len(windowUtterances(fixture.Turn)) {
					t.Fatalf("fixture %s has invalid expected reference %s", fixture.ID, ref)
				}
			}
		}
	}
	if len(seen) != 40 {
		t.Fatalf("minimum replay suite has %d cases, want 40", len(seen))
	}
}
