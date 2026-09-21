package inboundcoord

import (
	"context"
	"encoding/json"
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
					if followup.name == "contradiction" {
						var parsed struct {
							Executable bool   `json:"executable"`
							Reason     string `json:"reason"`
						}
						if json.Unmarshal([]byte(audit.RawInterpretation), &parsed) != nil || parsed.Executable || parsed.Reason == "" || resolveErr == nil {
							t.Fatalf("conflict was not explicitly rejected: %v", resolveErr)
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
