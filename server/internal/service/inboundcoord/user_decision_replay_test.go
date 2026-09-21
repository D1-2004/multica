package inboundcoord

import (
	"context"
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
	for _, f := range coordinatorReplayFixtures() {
		if f.ID != "capability_inventory" && f.ID != "supplied_message_payload" && f.ID != "explicit_retry" {
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
			selected := 0
			for _, o := range d.UserDecision.Proposal.Options {
				if (f.ID == "capability_inventory" && o.Kind != "reply") || (f.ID == "supplied_message_payload" && o.Kind != "start_work") || (f.ID == "explicit_retry" && o.Kind != "continue_work") {
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
