package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	"github.com/multica-ai/multica/server/pkg/llm"
)

// Opt-in private-trace replay. Raw instructions, people and conversation IDs
// stay in the operator-supplied file; none are checked into the repository.
// The only external operation is an LLM completion. Tools have frozen read
// results, and no checkpoint, dispatcher, DWS client or write implementation.
// Use MULTICA_RUN_COORDINATOR_REPLAY=1 and MULTICA_COORDINATOR_FROZEN_TRACE.
// MULTICA_COORDINATOR_FROZEN_REPORT defaults to the system temporary directory.
func TestCoordinatorFrozenTraceReplay(t *testing.T) {
	if os.Getenv("MULTICA_RUN_COORDINATOR_REPLAY") != "1" {
		t.Skip("real model replay requires explicit opt-in")
	}
	path := os.Getenv("MULTICA_COORDINATOR_FROZEN_TRACE")
	if path == "" {
		t.Skip("private frozen trace path is not configured")
	}
	fixture, err := loadFrozenCoordinatorTrace(path)
	if err != nil {
		t.Fatalf("load frozen trace: %v", err)
	}
	client := llm.New(llm.Config{APIKey: os.Getenv("MULTICA_LLM_API_KEY"), BaseURL: os.Getenv("MULTICA_LLM_BASE_URL"), DefaultModel: coordinatorModel, MaxRetries: -1})
	if !client.Enabled() {
		t.Skip("LLM credentials unavailable; no configuration printed")
	}
	type result struct {
		ID                 string               `json:"id"`
		Status             string               `json:"status"`
		Action             Action               `json:"action,omitempty"`
		Verdict            string               `json:"verdict,omitempty"`
		Reason             string               `json:"reason,omitempty"`
		ElapsedMS          int64                `json:"elapsed_ms"`
		Rounds             []frozenReplayRound  `json:"rounds"`
		ToolCalls          []string             `json:"fake_read_calls"`
		Items              []WindowItem         `json:"planned_items,omitempty"`
		Actions            []CoordinationAction `json:"coordination_actions,omitempty"`
		Reply              string               `json:"reply_for_manual_review,omitempty"`
		Failure            string               `json:"failure,omitempty"`
		ContractState      string               `json:"contract_state"`
		ContractCharacters int                  `json:"contract_characters"`
	}
	report := struct {
		PolicyVersion       string   `json:"policy_version"`
		SourceTrace         string   `json:"source_trace"`
		Safety              string   `json:"safety"`
		OldSystemCharacters int      `json:"old_system_characters"`
		OldUserCharacters   int      `json:"old_user_characters"`
		NewSystemCharacters int      `json:"new_system_characters"`
		NewUserCharacters   int      `json:"new_user_characters"`
		PolicyCharacters    int      `json:"job_policy_characters"`
		Cases               []result `json:"cases"`
	}{PolicyVersion: coordinatorPolicy.Version, SourceTrace: fixture.TraceID, Safety: "Real model with private frozen input and fake read-only tools. No E2E, external messages, memory writes, task creation or deployment verification.", OldSystemCharacters: utf8.RuneCountInString(fixture.System), OldUserCharacters: utf8.RuneCountInString(fixture.User), NewSystemCharacters: utf8.RuneCountInString(buildSystemPrompt(fixture.Turn)), NewUserCharacters: utf8.RuneCountInString(buildUserPrompt(fixture.Turn)), PolicyCharacters: utf8.RuneCountInString(fixture.Turn.Instructions)}
	verifiedProgressEvidence := `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","title":"查询钉钉 AI 听记生成规则","status":"done","original_goal":"查询钉钉 AI 听记生成规则","status_source":"issue_database","scope":"current_agent_workspace_issue","updated_at":"2026-09-09T02:58:00Z"}`
	cases := []struct {
		id               string
		message          string
		candidate        string
		expect           Action
		verdict          string
		hostEvidence     string
		policy           string
		candidateIssue   bool
		candidatePurpose string
		contract         bool
		kinds            []string
		taskFinished     bool
	}{
		{id: "original_product_question", expect: ActionIssue, kinds: []string{"start_work"}},
		{id: "task_finished_current_result", message: "任务完成，请向委托人汇报当前查证结果。", expect: ActionReply, taskFinished: true, kinds: []string{"report_result"}},
		{id: "original_product_question_with_contract", expect: ActionIssue, contract: true, kinds: []string{"start_work"}},
		{id: "original_unsupported_answer", candidate: fixture.BadReply, verdict: "revise"},
		{id: "greeting_and_capabilities", message: "你好，你可以帮我做什么？", expect: ActionReply, kinds: []string{"describe_capabilities"}},
		{id: "necessary_clarification", message: "帮我给同事发一条通知。", expect: ActionReply, kinds: []string{"clarify"}},
		{id: "two_work_items_plus_clarification", message: "请查证主持人和参会人各自开启听记会生成几份；另外起草周五下午三点全员例会通知，正文写明请带周报，先不发送；最后帮我发个消息。", expect: ActionIssue, contract: true, kinds: []string{"start_work", "clarify"}},
		{id: "verified_progress", message: "之前的查询任务完成了吗？", candidate: "查证任务已完成。", verdict: "allow", hostEvidence: verifiedProgressEvidence},
		{id: "verified_progress_named_task", message: "“查询钉钉 AI 听记生成规则”这个任务完成了吗？", candidate: "查证任务已完成。", verdict: "allow", hostEvidence: verifiedProgressEvidence},
		{id: "merged_independent_deliverables", message: "请查证听记生成份数；另起草周五三点例会通知，先不发送。", candidate: "我来查证听记并起草通知。", candidatePurpose: "1. 查证听记生成数量；2. 起草周五例会通知（不发送）", candidateIssue: true, contract: true, verdict: "revise"},
		{id: "single_deliverable_steps", message: "帮我起草周五三点例会通知，整理议程、写初稿并校对，先不发送。", candidate: "我来整理议程并起草校对通知。", candidatePurpose: "起草周五三点例会通知：先整理议程，再写初稿并校对（不发送）", candidateIssue: true, contract: true, verdict: "allow"},
		{id: "trailing_draft_restriction", message: "帮我给同事起草一条周五开会通知。", candidate: "我现在把周五开会通知发给同事。", candidateIssue: true, verdict: "revise", policy: strings.Repeat("Reference background without authorization. ", 200) + "Only draft. Never send until the user explicitly approves."},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			started := time.Now()
			turn := fixture.Turn
			if tc.message != "" {
				turn.Message = tc.message
			}
			if tc.policy != "" {
				turn.Instructions = tc.policy
			}
			if tc.contract {
				turn.CoordinatorContract, err = coordinatorcontract.Bind(&coordinatorcontract.Contract{Version: 1, Scope: "接待与协调钉钉产品问答、培训材料整理和授权通知工作。", MustDelegate: []string{"产品事实与机制必须由执行 Agent 查证后回答。", "专业分析、培训材料编写、通知起草与发送交执行 Agent。"}, Constraints: []string{"遵守用户当前限制；只起草不发送的授权不能变成发送。", "协调回复不能替代业务查证或宣称任务已完成。"}, ClarifyWhen: []string{"通知对象或内容缺失且无法从当前上下文恢复时，先澄清。"}}, turn.Instructions)
				if err != nil {
					t.Fatal(err)
				}
				turn.CoordinatorContractState = coordinatorcontract.StateLoaded
			}
			if tc.taskFinished {
				turn.Loop = LoopTaskFinished
				turn.IssueID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
				turn.TaskResult = "会议记录显示，主持人和参会人各自启动的听记均已完成，共有两条记录。本轮尚未向当前会话发送此结果。"
				turn.TaskDeliveryContext = `{"status":"loaded","task_id":"frozen-current-task","scope":"current_task","deliveries":[]}`
			}
			reads := &frozenReplayTools{scene: turn.ConversationID, recall: fixture.Recall}
			observer := &frozenReplayCompleter{client: client, model: os.Getenv("MULTICA_COORDINATOR_REPLAY_MODEL")}
			coordinator := &Coordinator{Chat: observer, Tools: reads, DWSHistory: &replayHistory{scene: turn.ConversationID}}
			ctx, cancel := context.WithTimeout(context.Background(), decisionTimeout)
			defer cancel()
			_, contractState := currentCoordinatorContract(turn)
			r := result{ID: tc.id, Status: "PASS", ContractState: contractState, ContractCharacters: utf8.RuneCount(coordinatorcontract.Marshal(turn.CoordinatorContract))}
			if tc.candidate != "" {
				messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(buildSystemPrompt(turn)), openai.UserMessage(buildUserPrompt(turn))}
				if tc.id == "original_unsupported_answer" {
					normalized, normalizeErr := NormalizeCoordinationRead(toolAssocRecall, fixture.Recall)
					if normalizeErr != nil {
						t.Fatal(normalizeErr)
					}
					turn.CoordinationReads = []CoordinationRead{{ReadRef: "r1", Tool: toolAssocRecall, Result: json.RawMessage(normalized)}}
				}
				if tc.hostEvidence != "" {
					normalized, normalizeErr := NormalizeCoordinationRead(toolWorkState, tc.hostEvidence)
					if normalizeErr != nil {
						t.Fatal(normalizeErr)
					}
					turn.CoordinationReads = []CoordinationRead{{ReadRef: "r1", Tool: toolWorkState, Result: json.RawMessage(normalized)}}
				}
				candidate := Decision{Action: ActionReply, UserText: tc.candidate, CoordinationActions: []CoordinationAction{{Kind: "describe_capabilities", SourceRefs: []string{"u1"}, Reply: tc.candidate}}}
				if tc.hostEvidence != "" {
					candidate.CoordinationActions[0].Kind = "report_status"
					candidate.CoordinationActions[0].StateRefs = []string{"r1"}
				}
				if tc.candidateIssue {
					candidate.Action = ActionIssue
					purpose := firstNonEmpty(tc.candidatePurpose, "向同事发送周五开会通知")
					candidate.Items = []WindowItem{{SourceRefs: []string{"u1"}, Purpose: purpose, Intent: "other", Basis: "new_request"}}
					candidate.CoordinationActions = []CoordinationAction{{Kind: "start_work", SourceRefs: []string{"u1"}, Purpose: purpose, Intent: "other", Reply: tc.candidate}}
				}
				r.Action = candidate.Action
				check, callErr := coordinator.checkFinish(ctx, turn, candidate, messages, 0, map[string]finishCheckResult{})
				r.Verdict, r.Reason = check.Verdict, check.Reason
				if callErr != nil {
					r.Failure = fmt.Sprintf("review failed (%T); upstream body omitted", callErr)
				} else if check.Verdict != tc.verdict {
					r.Failure = "unexpected semantic verdict: " + check.Verdict
				}
			} else {
				d, callErr := coordinator.runLoop(ctx, turn)
				r.Action, r.Items, r.Reply, r.Actions = d.Action, d.Items, d.UserText, d.CoordinationActions
				if callErr != nil {
					r.Failure = fmt.Sprintf("loop failed (%T); upstream body omitted", callErr)
				} else if d.Action != tc.expect {
					r.Failure = "unexpected routing action: " + string(d.Action)
				}
				for _, expectedKind := range tc.kinds {
					found := false
					for _, action := range d.CoordinationActions {
						if action.Kind == expectedKind {
							found = true
						}
					}
					if !found && r.Failure == "" {
						r.Failure = "missing required coordination action: " + expectedKind
					}
				}
				if tc.id == "two_work_items_plus_clarification" && len(d.Items) != 2 && r.Failure == "" {
					r.Failure = "two independent deliverables were not retained with the clarification"
				}
				if tc.expect == ActionIssue {
					if len(d.Items) == 0 && r.Failure == "" {
						r.Failure = "original product question did not produce any executable work plan"
					}
					for _, item := range d.Items {
						if item.IssueID != "" || !slices.Contains(item.SourceRefs, "u1") {
							r.Failure = "product question was attached to unrelated historical work or lost its source"
						}
					}
				} else if len(d.Items) != 0 {
					r.Failure = "conversation-only control created work"
				}
			}
			observer.waitSettled()
			for _, round := range observer.recordedRounds() {
				for _, tool := range round.AllowedTools {
					if tool == toolAssocBind || tool == toolIssueCommentAdd {
						r.Failure = "business-write tool exposed to replay"
					}
				}
			}
			r.ElapsedMS, r.Rounds, r.ToolCalls = time.Since(started).Milliseconds(), observer.recordedRounds(), reads.calls
			if r.Failure != "" {
				r.Status = "FAIL"
				t.Error(r.Failure)
			}
			report.Cases = append(report.Cases, r)
			t.Logf("frozen replay case=%s status=%s action=%s verdict=%s rounds=%d elapsed_ms=%d", r.ID, r.Status, r.Action, r.Verdict, len(r.Rounds), r.ElapsedMS)
		})
	}
	out := os.Getenv("MULTICA_COORDINATOR_FROZEN_REPORT")
	if out == "" {
		out = filepath.Join(os.TempDir(), "coordinator-frozen-trace-replay.json")
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal("encode private replay report")
	}
	if err = os.WriteFile(out, append(body, '\n'), 0600); err != nil {
		t.Fatal("write private replay report")
	}
	t.Logf("private replay report: %s", out)
}

type frozenCoordinatorFixture struct {
	TraceID, System, User, Recall, BadReply string
	Turn                                    Turn
}

func loadFrozenCoordinatorTrace(path string) (frozenCoordinatorFixture, error) {
	var result frozenCoordinatorFixture
	body, err := os.ReadFile(path)
	if err != nil {
		return result, fmt.Errorf("cannot read configured private file")
	}
	var envelope struct {
		Data []struct {
			ID        string         `json:"id"`
			Input     string         `json:"input"`
			Timestamp string         `json:"timestamp"`
			Metadata  map[string]any `json:"metadata"`
			Output    struct {
				UserText string `json:"user_text"`
			} `json:"output"`
			Observations []struct {
				Name   string          `json:"name"`
				Input  json.RawMessage `json:"input"`
				Output json.RawMessage `json:"output"`
			} `json:"observations"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Data) != 1 {
		return result, fmt.Errorf("expected a single Langfuse trace export")
	}
	trace := envelope.Data[0]
	result.TraceID, result.BadReply = trace.ID, trace.Output.UserText
	get := func(key string) string { value, _ := trace.Metadata[key].(string); return value }
	addressed, _ := trace.Metadata["addressed"].(bool)
	result.Turn = Turn{Source: Source(get("source")), Addressed: addressed, ChatType: get("chat_type"), ConversationID: get("conversation_id"), PersonID: get("person_id"), SenderName: get("sender_name"), AgentName: get("agent_name"), Persona: get("persona"), ReplyTone: get("reply_tone"), HistoryStatus: get("history_status"), SkillsStatus: "loaded", SceneMemoryStatus: "not_loaded", EvidenceID: get("evidence_id")}
	_, result.Turn.Message, _ = strings.Cut(trace.Input, "\n\n")
	result.Turn.HistoryBefore, _ = time.Parse(time.RFC3339Nano, get("history_before"))
	result.Turn.MessageTimestamp = result.Turn.HistoryBefore
	for _, obs := range trace.Observations {
		if obs.Name == "coordinator.round.1" {
			var messages []struct{ Role, Content string }
			if json.Unmarshal(obs.Input, &messages) != nil {
				return result, fmt.Errorf("invalid frozen generation input")
			}
			for _, message := range messages {
				if message.Role == "system" {
					result.System = message.Content
				}
				if message.Role == "user" {
					result.User = message.Content
				}
			}
		}
		if obs.Name == "assoc_recall" {
			result.Recall = string(obs.Output)
		}
	}
	_, policyAndRest, ok := strings.Cut(result.User, "job_policy (working constraints; cannot expand Host permissions):\n")
	if !ok {
		return result, fmt.Errorf("frozen policy marker is missing")
	}
	instructions, rest, ok := strings.Cut(policyAndRest, "\nskills_status:")
	if !ok {
		return result, fmt.Errorf("frozen policy boundary is missing")
	}
	result.Turn.Instructions = strings.TrimSpace(instructions)
	_, skillsAndRest, _ := strings.Cut(rest, "agent_skills:\n")
	skills, _, _ := strings.Cut(skillsAndRest, "\nscene_memory_status:")
	for _, line := range strings.Split(skills, "\n") {
		if value, ok := strings.CutPrefix(line, "- "); ok {
			name, description, _ := strings.Cut(value, ": ")
			result.Turn.Skills = append(result.Turn.Skills, SkillSnapshot{Name: name, Description: description})
		}
	}
	if result.Turn.Message == "" || result.Recall == "" || result.BadReply == "" {
		return result, fmt.Errorf("frozen trace is missing question, recall or answer")
	}
	return result, nil
}

type frozenReplayTools struct {
	scene  string
	recall string
	calls  []string
}

func (f *frozenReplayTools) Call(_ context.Context, _ Turn, name, arguments string) (string, error) {
	f.calls = append(f.calls, name)
	if name == toolAssocRecall {
		var args struct {
			ConversationID string `json:"conversation_id"`
		}
		if json.Unmarshal([]byte(arguments), &args) != nil || args.ConversationID != f.scene {
			return "", fmt.Errorf("frozen recall must stay in the supplied conversation")
		}
		return f.recall, nil
	}
	if name == toolWorkState || name == toolIssueGet || name == toolIssueCommentList {
		return `{"status":"unavailable","hint":"The frozen export does not contain further work evidence; do not infer product facts."}`, nil
	}
	return "", fmt.Errorf("frozen replay has no implementation for tool %q", name)
}

type frozenReplayRound struct {
	Model                         string   `json:"model"`
	SystemPromptHash              string   `json:"system_prompt_hash"`
	AllowedTools                  []string `json:"allowed_tools"`
	CalledTools                   []string `json:"called_tools"`
	InputTokens                   int64    `json:"input_tokens"`
	OutputTokens                  int64    `json:"output_tokens"`
	ElapsedMS                     int64    `json:"elapsed_ms"`
	InputCharacters               int      `json:"input_characters"`
	FinishReason                  string   `json:"finish_reason,omitempty"`
	OutputCharacters              int      `json:"output_characters"`
	ToolArgumentCharacters        int      `json:"tool_argument_characters"`
	ProposedAction                string   `json:"proposed_action,omitempty"`
	ReviewVerdict                 string   `json:"review_verdict,omitempty"`
	ToolArgumentsForPrivateReview []string `json:"tool_arguments_for_private_review,omitempty"`
}
type frozenReplayCompleter struct {
	client *llm.Client
	model  string
	// The speculative conversation render shares this recorder with the
	// routing request, so the ledger is serialized and in-flight calls are
	// counted: a cost report that silently drops one is not evidence.
	mu       sync.Mutex
	inFlight int
	settled  *sync.Cond
	rounds   []frozenReplayRound
}

func (f *frozenReplayCompleter) enter() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.settled == nil {
		f.settled = sync.NewCond(&f.mu)
	}
	f.inFlight++
}

func (f *frozenReplayCompleter) leave(round frozenReplayRound) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rounds = append(f.rounds, round)
	f.inFlight--
	if f.settled != nil {
		f.settled.Broadcast()
	}
}

// waitSettled blocks until no request is outstanding, so an unconsumed
// speculation still lands in the report before the caller reads it.
func (f *frozenReplayCompleter) waitSettled() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.settled == nil {
		return
	}
	for f.inFlight > 0 {
		f.settled.Wait()
	}
}

func (f *frozenReplayCompleter) recordedRounds() []frozenReplayRound {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]frozenReplayRound(nil), f.rounds...)
}

func (f *frozenReplayCompleter) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	if f.model != "" {
		params.Model = shared.ChatModel(f.model)
	}
	f.enter()
	started := time.Now()
	round := frozenReplayRound{Model: string(params.Model), AllowedTools: toolParamNames(params.Tools)}
	body, _ := json.Marshal(params.Messages)
	if len(params.Messages) > 0 {
		raw, _ := json.Marshal(params.Messages[0])
		var system struct{ Content string }
		_ = json.Unmarshal(raw, &system)
		round.SystemPromptHash = policyHash(system.Content)
	}
	round.InputCharacters = utf8.RuneCount(body)
	response, err := f.client.Chat(ctx, params)
	if response != nil {
		round.Model, round.InputTokens, round.OutputTokens = response.Model, response.Usage.PromptTokens, response.Usage.CompletionTokens
		if len(response.Choices) > 0 {
			choice := response.Choices[0]
			round.FinishReason = choice.FinishReason
			round.OutputCharacters = utf8.RuneCountInString(choice.Message.Content)
			for _, call := range functionToolCalls(choice.Message) {
				round.CalledTools = append(round.CalledTools, call.Name)
				round.ToolArgumentsForPrivateReview = append(round.ToolArgumentsForPrivateReview, call.Arguments)
				characters := utf8.RuneCountInString(call.Arguments)
				round.ToolArgumentCharacters += characters
				round.OutputCharacters += characters
				var terminal struct {
					Verdict string
					Actions []CoordinationAction
				}
				if json.Unmarshal([]byte(call.Arguments), &terminal) == nil {
					if call.Name == toolFinish {
						kinds := make([]string, 0, len(terminal.Actions))
						for _, action := range terminal.Actions {
							kinds = append(kinds, action.Kind)
						}
						round.ProposedAction = strings.Join(kinds, ",")
					}
					if call.Name == toolFinishCheck {
						round.ReviewVerdict = terminal.Verdict
					}
				}
			}
		}
	}
	round.ElapsedMS = time.Since(started).Milliseconds()
	f.leave(round)
	return response, err
}
