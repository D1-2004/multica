package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service/userdecision"
	"github.com/multica-ai/multica/server/pkg/llm"
	openai "github.com/openai/openai-go/v3"
)

// Live, read-only model checks against the explicitly supplied preproduction
// model configuration. Synthetic fixtures never reach a card, DB or executor.
func TestUserDecisionRealModel(t *testing.T) {
	if os.Getenv("MULTICA_RUN_USER_DECISION_REPLAY") != "1" {
		t.Skip("explicit preproduction model opt-in required")
	}
	client := llm.New(llm.Config{APIKey: os.Getenv("MULTICA_LLM_API_KEY"), BaseURL: os.Getenv("MULTICA_LLM_BASE_URL"), DefaultModel: coordinatorModel, MaxRetries: -1})
	if !client.Enabled() {
		t.Fatal("preproduction model configuration required")
	}
	fixtures := coordinatorReplayFixtures()
	for _, fixture := range fixtures {
		if fixture.ID == "supplied_message_payload" {
			ambiguous := fixture
			ambiguous.ID = "ambiguous_reports"
			ambiguous.Turn.Message = "把报告整理成可以转发的版本。"
			ambiguous.Cards = []replayCard{
				{ID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Purpose: "客户周报：整理本周客户进展报告", Status: "in_progress", Comment: "客户周报初稿已准备好，可以调整转发格式。"},
				{ID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", Purpose: "项目进展报告：整理本周项目里程碑", Status: "in_progress", Comment: "项目进展报告初稿已准备好，可以调整转发格式。"},
			}
			fixtures = append(fixtures, ambiguous)
			break
		}
	}
	for _, f := range fixtures {
		if f.ID != "capability_inventory" && f.ID != "supplied_message_payload" && f.ID != "explicit_retry" && f.ID != "ambiguous_reports" {
			continue
		}
		t.Run(f.ID, func(t *testing.T) {
			reads := &replayReadTools{scene: f.Turn.ConversationID, cards: f.Cards}
			history := &replayHistory{lines: f.History, scene: f.Turn.ConversationID}
			observer := &replayCompleter{client: client, readTools: reads, modelOverride: os.Getenv("MULTICA_COORDINATOR_REPLAY_MODEL")}
			c := &Coordinator{LLM: client, Chat: &userDecisionReplayRecorder{inner: observer, t: t}, model: os.Getenv("MULTICA_COORDINATOR_REPLAY_MODEL"), Tools: reads, DWSHistory: history}
			f.Turn.UserDecisionEnabled = true
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			commits := 0
			ctx = ContextWithPlanCheckpoint(ctx, nil, func(Decision) error { commits++; return nil })
			d, err := c.runLoop(ctx, f.Turn)
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != ActionAwaitUser || d.UserDecision == nil || commits != 0 {
				t.Fatalf("no frozen question or premature commit: action=%s commits=%d", d.Action, commits)
			}
			if err = d.UserDecision.Proposal.Validate(); err != nil {
				t.Fatal(err)
			}
			t.Logf("question=%s options=%d", d.UserDecision.Proposal.Question, len(d.UserDecision.Proposal.Options))
			if f.ID == "ambiguous_reports" {
				continuations := 0
				for _, option := range d.UserDecision.Proposal.Options {
					if option.Kind == "continue_work" {
						continuations++
					}
				}
				if continuations != 2 {
					t.Fatalf("two recalled report alternatives required, got %d", continuations)
				}
			}
			selected := 0
			for _, o := range d.UserDecision.Proposal.Options {
				if (f.ID == "capability_inventory" && o.Kind != "reply") || (f.ID == "supplied_message_payload" && o.Kind != "start_work") || ((f.ID == "explicit_retry" || f.ID == "ambiguous_reports") && o.Kind != "continue_work") {
					continue
				}
				selected++
				resolved, audit, err := c.ResolveUserDecisionWithAudit(ctx, *d.UserDecision, userdecision.Submission{OptionID: o.ID})
				if err != nil {
					t.Fatal(err)
				}
				if err = validateChoiceDirection(o, resolved); err != nil {
					t.Fatal(err)
				}
				t.Logf("selected=%s action=%s model=%s", o.Kind, resolved.Action, audit.Model)
				break
			}
			if f.ID == "supplied_message_payload" {
				newID := ""
				for _, option := range d.UserDecision.Proposal.Options {
					if option.Kind == "start_work" {
						newID = option.ID
					}
				}
				for _, followup := range []struct{ name, text, option string }{
					{"text_only", "请新建任务，给小林发送你好。", ""},
					{"negation", "不要发消息，取消这个发送要求。", ""},
					{"refinement", "问候正文请改为：你好，小林。", newID},
					{"contradiction", "我选了新建，但补充要求是不要新建任务，也不要发送消息。", newID},
				} {
					resolutionCtx, resolutionCancel := context.WithTimeout(context.Background(), 60*time.Second)
					resolutionCtx = ContextWithPlanCheckpoint(resolutionCtx, nil, func(Decision) error { commits++; return nil })
					result, audit, resolveErr := c.ResolveUserDecisionWithAudit(resolutionCtx, *d.UserDecision, userdecision.Submission{Custom: followup.text, OptionID: followup.option})
					resolutionCancel()
					if followup.name == "contradiction" || followup.name == "negation" {
						var parsed struct {
							Executable bool   `json:"executable"`
							Reason     string `json:"reason"`
						}
						if json.Unmarshal([]byte(audit.RawInterpretation), &parsed) != nil || parsed.Executable || parsed.Reason == "" || resolveErr == nil {
							t.Fatalf("%s was not explicitly non-executable: %v", followup.name, resolveErr)
						}
						continue
					}
					if resolveErr != nil {
						t.Fatalf("%s: %v", followup.name, resolveErr)
					}
					if (followup.name == "text_only" || followup.name == "refinement") && result.Action != ActionIssue {
						t.Fatal("custom-only work was not interpreted")
					}
					if followup.name == "negation" && result.Action == ActionIssue {
						t.Fatal("negated work was executed")
					}
					if audit.RawInterpretation == "" {
						t.Fatal("interpretation missing")
					}
				}
			}
			if f.ID == "ambiguous_reports" {
				resolutionCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				resolutionCtx = ContextWithPlanCheckpoint(resolutionCtx, nil, func(Decision) error { commits++; return nil })
				result, _, resolveErr := c.ResolveUserDecisionWithAudit(resolutionCtx, *d.UserDecision, userdecision.Submission{Custom: "不是客户周报，是第二份项目进展报告，继续整理成一页。"})
				cancel()
				if resolveErr != nil || workTarget(result) != "continue_work:bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
					t.Fatalf("reference or negation resolved incorrectly: %s %v", workTarget(result), resolveErr)
				}
			}
			if selected != 1 {
				t.Fatalf("expected one tested choice, got %d", selected)
			}
			if commits != 0 {
				t.Fatal("model replay committed business work")
			}
		})
	}
}

// Record only synthetic proposal payloads to diagnose the real model contract.
type userDecisionReplayRecorder struct {
	inner *replayCompleter
	t     *testing.T
}

func (r *userDecisionReplayRecorder) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	c, err := r.inner.Chat(ctx, p)
	if err != nil {
		names := []string{}
		for _, tool := range p.Tools {
			if tool.OfFunction != nil {
				names = append(names, tool.OfFunction.Function.Name)
			}
		}
		r.t.Logf("failed model call tools=%v messages=%d reasoning=%s token_limit=%d", names, len(p.Messages), p.ReasoningEffort, p.MaxCompletionTokens.Value)
	}
	if c != nil {
		for _, choice := range c.Choices {
			for _, call := range choice.Message.ToolCalls {
				if call.Function.Name == "propose_choices" || call.Function.Name == "interpret_submission" {
					r.t.Logf("%s: %s", call.Function.Name, call.Function.Arguments)
				}
			}
		}
	}
	return c, err
}

// This contrast comes from the real R5 card: a conversation label cannot make
// a promise of future work a standalone reply.
func TestUserDecisionRealModelReplyReview(t *testing.T) {
	if os.Getenv("MULTICA_RUN_USER_DECISION_REPLAY") != "1" {
		t.Skip("explicit preproduction model opt-in required")
	}
	client := llm.New(llm.Config{APIKey: os.Getenv("MULTICA_LLM_API_KEY"), BaseURL: os.Getenv("MULTICA_LLM_BASE_URL"), DefaultModel: coordinatorModel, MaxRetries: -1})
	if !client.Enabled() {
		t.Fatal("preproduction model configuration required")
	}
	c := &Coordinator{LLM: client, model: os.Getenv("MULTICA_COORDINATOR_REPLAY_MODEL")}
	turn := Turn{Source: SourceRobot, Addressed: true, Message: "把已有报告第三行改成下一步完成验收。", UserDecisionEnabled: true}
	evidence, _ := json.Marshal(map[string]any{"request": turn.Message})
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"mislabeled_future_work", "你说要修改第三行，我将按继续修改处理。", false},
		{"standalone_fact", "你提供的修改要求是：第三行改为下一步完成验收。", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, _ := json.Marshal(map[string]any{"actions": []any{map[string]any{"kind": "acknowledge", "ack_kind": "conversation", "source_refs": []string{"u1"}, "reply": tc.body}}})
			p := userdecision.Proposal{Question: "怎么处理？", Options: []userdecision.Option{
				{ID: "new", Kind: "start_work", Label: "新建工作，整理修改后的报告", Plan: json.RawMessage(`{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"整理修改后的报告"}]}`)},
				{ID: "reply", Kind: "reply", Label: tc.body, Plan: plan},
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			raw, err := c.reviewUserDecisionProposal(ctx, turn, evidence, p)
			t.Logf("review=%s", raw)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

// Replay the exact exported snapshot without modifying the accepted record or
// invoking SavePlan. The artifact remains outside the repository.
func TestUserDecisionRealFrozenSubmission(t *testing.T) {
	if os.Getenv("MULTICA_RUN_USER_DECISION_REPLAY") != "1" {
		t.Skip("explicit preproduction model opt-in required")
	}
	path := os.Getenv("MULTICA_USER_DECISION_SNAPSHOT_FILE")
	if path == "" {
		t.Skip("explicit exported decision required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sample struct {
		Snapshot   UserDecisionSnapshot    `json:"snapshot"`
		Submission userdecision.Submission `json:"submission"`
	}
	if err := json.Unmarshal(data, &sample); err != nil {
		t.Fatal(err)
	}
	client := llm.New(llm.Config{APIKey: os.Getenv("MULTICA_LLM_API_KEY"), BaseURL: os.Getenv("MULTICA_LLM_BASE_URL"), DefaultModel: coordinatorModel, MaxRetries: -1})
	if !client.Enabled() {
		t.Fatal("preproduction model configuration required")
	}
	c := &Coordinator{LLM: client, model: os.Getenv("MULTICA_COORDINATOR_REPLAY_MODEL"), Tools: &stubTools{}}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	decision, audit, err := c.ResolveUserDecisionWithAudit(ctx, sample.Snapshot, sample.Submission)
	if os.Getenv("MULTICA_USER_DECISION_EXPECT_NONEXECUTION") == "1" {
		var rejected *userdecision.NonExecutableSubmissionError
		if !errors.As(err, &rejected) || rejected.Reason == "" || decision.Action != "" {
			t.Fatalf("frozen cancellation not explicit: error=%v action=%s", err, decision.Action)
		}
		t.Logf("frozen cancellation retained: %s", rejected.Reason)
		return
	}
	if err != nil {
		t.Fatalf("resolution failed: %v review=%+v", err, audit.Review)
	}
	if decision.Action != ActionReply {
		t.Fatalf("expected reply, got %s", decision.Action)
	}
	t.Logf("frozen submission allowed: action=%s review=%+v", decision.Action, audit.Review)
}

// R18: the final reviewer must not restore wording superseded by the initiator.
func TestUserDecisionLaterSubmissionRealModel(t *testing.T) {
	if os.Getenv("MULTICA_RUN_USER_DECISION_REPLAY") != "1" {
		t.Skip("explicit preproduction model opt-in required")
	}
	client := llm.New(llm.Config{APIKey: os.Getenv("MULTICA_LLM_API_KEY"), BaseURL: os.Getenv("MULTICA_LLM_BASE_URL"), DefaultModel: coordinatorModel, MaxRetries: -1})
	if !client.Enabled() {
		t.Fatal("preproduction model configuration required")
	}
	c := &Coordinator{LLM: client, model: os.Getenv("MULTICA_COORDINATOR_REPLAY_MODEL"), Tools: &stubTools{}}
	s := UserDecisionSnapshot{Turn: Turn{Source: SourceRobot, Addressed: true, Message: "请只回复：原始回复"}, Proposal: userdecision.Proposal{Question: "希望如何处理？", Options: []userdecision.Option{
		{ID: "reply", Kind: "reply", Label: "直接回复：原始回复", Plan: json.RawMessage(`{"actions":[{"kind":"acknowledge","ack_kind":"conversation","source_refs":["u1"],"reply":"原始回复"}]}`)},
		{ID: "new", Kind: "start_work", Label: "新建回复测试工作", Plan: json.RawMessage(`{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"回复测试"}]}`)},
	}}}
	for _, tc := range []struct {
		name, text string
		cancel     bool
	}{
		{"later_wording", "改为只回复：新的回复", false},
		{"cancel", "取消本次测试，不要发送原始回复，不要新建或续接任何工作。", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			commits := 0
			ctx = ContextWithPlanCheckpoint(ctx, nil, func(Decision) error { commits++; return nil })
			d, a, err := c.ResolveUserDecisionWithAudit(ctx, s, userdecision.Submission{Custom: tc.text})
			if commits != 0 {
				t.Fatal("replay committed a plan")
			}
			review, _ := a.Review.(finishCheckResult)
			if tc.cancel {
				var p struct {
					Executable bool
					Reason     string
				}
				if json.Unmarshal([]byte(a.RawInterpretation), &p) != nil || p.Executable || p.Reason == "" || err == nil {
					t.Fatalf("cancellation was not explicit non-execution: error=%v interpretation=%s", err, a.RawInterpretation)
				}
			} else if err != nil || d.Action != ActionReply || d.UserText != "新的回复" || review.Verdict != "allow" {
				t.Fatalf("later instruction not respected: action=%s text=%s review=%+v error=%v", d.Action, d.UserText, a.Review, err)
			}
			t.Logf("later_submission=%s action=%s non_executable=%v", tc.name, d.Action, err != nil)
		})
	}
}

func TestUserDecisionTaskLabelRealModel(t *testing.T) {
	if os.Getenv("MULTICA_RUN_USER_DECISION_REPLAY") != "1" {
		t.Skip("explicit preproduction model opt-in required")
	}
	client := llm.New(llm.Config{APIKey: os.Getenv("MULTICA_LLM_API_KEY"), BaseURL: os.Getenv("MULTICA_LLM_BASE_URL"), DefaultModel: coordinatorModel, MaxRetries: -1})
	if !client.Enabled() {
		t.Fatal("preproduction model configuration required")
	}
	c := &Coordinator{LLM: client, model: os.Getenv("MULTICA_COORDINATOR_REPLAY_MODEL")}
	evidence := []byte(`{"request":"R19：请只回复原始测试回复","history":"R18停用取消测试已取消，没有创建任何任务","recalled_tasks":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","title":"R12独立模拟结论","status":"done","purpose":"写一句模拟结论"}]}`)
	var proposal userdecision.Proposal
	if err := json.Unmarshal([]byte(`{"question":"怎么处理？","options":[{"id":"reply","kind":"reply","label":"直接回复：原始测试回复","plan":{"actions":[{"kind":"acknowledge","reply":"原始测试回复","ack_kind":"conversation"}]}},{"id":"new","kind":"start_work","label":"新建R19回复测试","plan":{"actions":[{"kind":"start_work","purpose":"R19回复测试"}]}},{"id":"continue","kind":"continue_work","label":"续接R18停用取消测试","plan":{"actions":[{"kind":"continue_work","issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"改为回复原始测试回复"}]}}]}`), &proposal); err != nil {
		t.Fatal(err)
	}
	for _, valid := range []bool{false, true} {
		if valid {
			proposal.Options[2].Label = "继续R12独立模拟结论：改为R19回复测试"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		raw, err := c.reviewUserDecisionProposal(ctx, Turn{}, evidence, proposal)
		cancel()
		var review struct {
			Allowed *bool  `json:"allowed"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal(raw, &review) != nil || review.Allowed == nil || *review.Allowed != valid || (err == nil) != valid {
			t.Fatalf("label validity=%v review=%s error=%v", valid, raw, err)
		}
		t.Logf("label_valid=%v reason=%s", valid, review.Reason)
	}
}
