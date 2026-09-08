package inboundcoord

import (
	"context"
	"fmt"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func TestCoordinatorRolloutNotReadyDefersBeforeModelOrTools(t *testing.T) {
	t.Parallel()
	for _, gateErr := range []error{nil, fmt.Errorf("replica heartbeat query unavailable")} {
		chat := &scriptedCompleter{}
		tools := &stubTools{}
		calls := 0
		c := &Coordinator{Chat: chat, Tools: tools, Ready: func(context.Context) (bool, error) { calls++; return false, gateErr }}
		for _, restore := range []bool{false, true} {
			ctx := context.Background()
			if restore {
				ctx = ContextWithPlanCheckpoint(ctx, &Decision{Action: ActionIssue, PlanVersion: "window-plan-v1"}, nil)
			}
			got := c.Decide(ctx, Turn{Source: SourceWeb, Addressed: true, Message: "整理本次会议决策"})
			if got.Action != ActionDeferred || got.Reason != "coordinator_rollout_wait" || len(got.Items) != 0 || chat.calls != 0 || len(tools.calls) != 0 {
				t.Fatalf("restore=%v gateErr=%v decision=%#v", restore, gateErr, got)
			}
		}
		if calls != 2 {
			t.Fatalf("gate not checked before fresh/restored decisions: %d", calls)
		}
	}
}

func TestCoordinatorRolloutReadyAllowsNormalDecision(t *testing.T) {
	t.Parallel()
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("done", toolFinish, `{"action":"reply","text":"在，你说。"}`)}}
	tools := &stubTools{}
	calls := 0
	got := (&Coordinator{Chat: chat, Tools: tools, Ready: func(context.Context) (bool, error) { calls++; return true, nil }}).Decide(context.Background(), Turn{Source: SourceWeb, Addressed: true, Message: "你在吗"})
	if got.Action != ActionReply || got.UserText != "在，你说。" || chat.calls != 1 || calls != 1 || len(tools.calls) != 0 {
		t.Fatalf("ready gate changed normal response: %#v gate=%d model=%d", got, calls, chat.calls)
	}
}
