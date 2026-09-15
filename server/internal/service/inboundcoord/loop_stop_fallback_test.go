package inboundcoord

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// Each deterministic stop reason, driven through Decide so the conversion in
// the Decide error branch is what is tested, not runLoop alone.
func loopStopScripts(t *testing.T) map[string]func() (*scriptedCompleter, Tools) {
	t.Helper()
	exhausted := func() (*scriptedCompleter, Tools) {
		rounds := make([]openai.ChatCompletion, 0, maxLoopRounds)
		for range maxLoopRounds {
			rounds = append(rounds, assistantTool("recall", toolAssocRecall, `{"since":"48h"}`))
		}
		return &scriptedCompleter{rounds: rounds}, &stubTools{}
	}
	invalidPlan := func() (*scriptedCompleter, Tools) {
		reads := 0
		return &scriptedCompleter{rounds: []openai.ChatCompletion{
			assistantTool("f1", toolFinish, retryTestBadIssue), assistantTool("f2", toolFinish, retryTestBadIssue), assistantTool("f3", toolFinish, retryTestBadIssue),
		}}, retryTestTools(t, &reads)
	}
	deadlock := func() (*scriptedCompleter, Tools) {
		reads := 0
		reason := "Candidate c1 only addresses u1; ignores the pending status update."
		return &scriptedCompleter{rounds: []openai.ChatCompletion{
			assistantTool("f1", toolFinish, retryTestReceipt), assistantTool("f2", toolFinish, retryTestReceipt), assistantTool("f3", toolFinish, retryTestReceipt),
		}, checkRounds: []openai.ChatCompletion{
			scriptedFinishVerdict("revise", reason), scriptedFinishVerdict("revise", reason), scriptedFinishVerdict("revise", reason),
		}}, retryTestTools(t, &reads)
	}
	return map[string]func() (*scriptedCompleter, Tools){
		loopStopRoundsExhausted:     exhausted,
		loopStopRepeatedInvalidPlan: invalidPlan,
		loopStopReviewDeadlock:      deadlock,
	}
}

func assertLoopStopFallback(t *testing.T, d Decision, chat *scriptedCompleter, wantAction Action, wantReason string) {
	t.Helper()
	if d.Action != wantAction || d.Reason != wantReason {
		t.Fatalf("action=%s reason=%s, want %s/%s: %#v", d.Action, d.Reason, wantAction, wantReason, d)
	}
	if wantAction == ActionReply && d.UserText != coordinatorFallbackReply {
		t.Fatalf("reply text = %q", d.UserText)
	}
	if wantAction == ActionSilence && d.UserText != "" {
		t.Fatalf("silence must carry no text: %q", d.UserText)
	}
	if len(d.Items) != 0 || d.IssueComment != nil || len(d.CoordinationActions) != 0 || len(d.NonWorkRefs) != 0 || d.IssueID != "" || d.Purpose != "" || d.LookInto != "" {
		t.Fatalf("fallback must not carry work or model actions: %#v", d)
	}
	if d.PlanVersion != WindowPlanVersion || !d.LoopStopFallback() {
		t.Fatalf("fallback must be a checkpointable window plan: %#v", d)
	}
	if len(d.Steps) == 0 || d.Steps[len(d.Steps)-1].Type != "error" {
		t.Fatalf("fallback must keep the failure evidence: %#v", d.Steps)
	}
	if d.ToolRounds != chat.calls {
		t.Fatalf("tool_rounds=%d, model calls=%d", d.ToolRounds, chat.calls)
	}
	// tools_used keeps the Host prefetch plus one entry per model round.
	wantLast := toolFinish
	if wantReason == loopStopRoundsExhausted {
		wantLast = toolAssocRecall
	}
	if len(d.ToolsUsed) < chat.calls || d.ToolsUsed[len(d.ToolsUsed)-1] != wantLast {
		t.Fatalf("tools_used must keep the diagnostic trail: %v (calls=%d)", d.ToolsUsed, chat.calls)
	}
	if wantReason == loopStopRoundsExhausted && !reflect.DeepEqual(d.ToolsUsed[len(d.ToolsUsed)-maxLoopRounds:], repeatStrings(toolAssocRecall, maxLoopRounds)) {
		t.Fatalf("tools_used = %v", d.ToolsUsed)
	}
}

func repeatStrings(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// Production evidence: trace 342b8b1cfe8040a29f79e4a613a59ecf, six deferred
// runs and 48 generations for one @ in a group that never got any reply.
func TestDeterministicLoopStopFallsBackInsteadOfDeferring(t *testing.T) {
	for reason, script := range loopStopScripts(t) {
		for _, addressed := range []bool{true, false} {
			chat, tools := script()
			// Unaddressed turns that still reach the loop are proactive DMs
			// or group turns the relevance gate let through.
			turn := Turn{Source: SourceDigitalEmployee, Addressed: addressed, ProactiveConversation: !addressed, ChatType: "p2p", ConversationID: "cid-current", Message: "继续整理那条决策", SenderName: "冬翔"}
			var saved []Decision
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(d Decision) error { saved = append(saved, d); return nil })
			d := (&Coordinator{LLM: llm.New(llm.Config{APIKey: "test-key"}), Chat: chat, Tools: tools}).Decide(ctx, turn)
			want := ActionSilence
			if addressed {
				want = ActionReply
			}
			assertLoopStopFallback(t, d, chat, want, reason)
			if len(saved) != 1 || saved[0].Action != want || saved[0].Reason != reason || saved[0].PlanVersion != WindowPlanVersion {
				t.Fatalf("%s addressed=%v: fallback must be checkpointed once: %#v", reason, addressed, saved)
			}
		}
	}
}

func TestLoopStopFallbackStaysDeferredWhenCheckpointFails(t *testing.T) {
	chat, tools := loopStopScripts(t)[loopStopReviewDeadlock]()
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { return errors.New("db down") })
	turn := Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "p2p", ConversationID: "cid-current", Message: "收到了吗", SenderName: "冬翔"}
	d := (&Coordinator{LLM: llm.New(llm.Config{APIKey: "test-key"}), Chat: chat, Tools: tools}).Decide(ctx, turn)
	if d.Action != ActionDeferred || d.UserText != "" || d.Reason != loopStopReviewDeadlock {
		t.Fatalf("an unsaved fallback must not be spoken: %#v", d)
	}
}

func TestTransientLoopFailureStaysDeferred(t *testing.T) {
	// One scripted round, then the completer runs dry: a model error, which
	// the job worker should retry rather than answer with the fallback.
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("recall", toolAssocRecall, `{"since":"48h"}`)}}
	turn := Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "p2p", ConversationID: "cid-current", Message: "在吗", SenderName: "冬翔"}
	d := (&Coordinator{LLM: llm.New(llm.Config{APIKey: "test-key"}), Chat: chat, Tools: &stubTools{}}).Decide(context.Background(), turn)
	if d.Action != ActionDeferred || d.Reason != "coordinator_undecided" || d.UserText != "" || d.LoopStopFallback() {
		t.Fatalf("model errors must stay deferred for the job retry: %#v", d)
	}
}

func TestTaskFinishedLoopStopStaysDeferred(t *testing.T) {
	d, ok := loopStopFallback(Turn{Loop: LoopTaskFinished, Addressed: true}, Decision{Action: ActionDeferred, Reason: loopStopRoundsExhausted})
	if ok || d.Action != ActionDeferred {
		t.Fatalf("task_finished owns its own retry: %#v ok=%v", d, ok)
	}
	for _, reason := range []string{"coordinator_undecided", "coordinator_model_unavailable", "coordinator_rollout_wait", ""} {
		if deterministicLoopStop(reason) {
			t.Fatalf("%q is transient", reason)
		}
	}
	if (Decision{Action: ActionIssue, Reason: loopStopReviewDeadlock}).LoopStopFallback() {
		t.Fatal("only reply/silence verdicts are fallbacks")
	}
	if strings.Contains(coordinatorFallbackReply, "再说一遍") || strings.Contains(coordinatorFallbackReply, "reason") {
		t.Fatalf("fallback must not expose diagnostics or encourage replay: %q", coordinatorFallbackReply)
	}
}

func TestDirectInboundSilenceGetsVisibleReceipt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		turn  Turn
		reply bool
	}{
		{"direct message", Turn{Source: SourceDigitalEmployee, ChatType: "p2p"}, true},
		{"stale name matched mention", Turn{Source: SourceDigitalEmployee, ChatType: "group", DWSUID: "6899376218", EmployeeAccountName: "旧昵称", Utterances: []WindowUtterance{{Text: "@新昵称 你怎么不说话", Mentions: []MessageMention{{OpenDingTalkID: "6899376218"}}}}}, true},
		{"other colleague", Turn{Source: SourceDigitalEmployee, ChatType: "group", DWSUID: "6899376218", Utterances: []WindowUtterance{{Text: "@别人 帮我查一下", Mentions: []MessageMention{{UID: "other"}}}}}, false},
		{"completion already delivered", Turn{Source: SourceDigitalEmployee, ChatType: "p2p", Loop: LoopTaskFinished}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := ensureDirectInboundReply(tc.turn, Decision{Action: ActionSilence, Reason: "internal reviewer detail", CoordinationActions: []CoordinationAction{{Kind: "ignore", SourceRefs: []string{"u1"}}}})
			if (d.Action == ActionReply) != tc.reply {
				t.Fatalf("unexpected decision: %+v", d)
			}
			if tc.reply && (d.UserText == "" || strings.Contains(d.UserText, "internal") || !d.LoopStopFallback() || len(d.CoordinationActions) > 0) {
				t.Fatalf("invalid visible receipt: %+v", d)
			}
		})
	}
}
