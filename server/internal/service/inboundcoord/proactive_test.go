package inboundcoord

import (
	"context"
	openai "github.com/openai/openai-go/v3"
	"strings"
	"testing"
)

func TestProactiveUnaddressedReachesSameCoordinator(t *testing.T) {
	for _, on := range []bool{false, true} {
		chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("done", toolFinish, `{"action":"reply","text":"在，你说。"}`)}}
		turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", Message: "有数字员工在吗", Addressed: false, ProactiveConversation: on}
		got := (&Coordinator{Chat: chat}).Decide(context.Background(), turn)
		if on && (chat.calls != 1 || got.Action != ActionReply) {
			t.Fatalf("proactive message was skipped: %s calls=%d", got.Action, chat.calls)
		}
		if !on && (chat.calls != 0 || got.Action != ActionSilence) {
			t.Fatal("ordinary unaddressed behavior changed")
		}
	}
}

func TestProactivePolicyAndCompletionCoverage(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, Message: "帮我查一下"}
	if !strings.Contains(buildUserPrompt(turn), "proactive_conversation: true") || !strings.Contains(buildSystemPrompt(turn), "不要求 @") {
		t.Fatal("proactive host and prompt contracts diverged")
	}
	turn.Loop = LoopTaskFinished
	turn.OutstandingFollowUps = `{"items":[{"content":"改为昨天","execution_status":"queued"}]}`
	if !strings.Contains(buildUserPrompt(turn), "改为昨天") || !strings.Contains(buildSystemPrompt(turn), "waiting or queued is not running") {
		t.Fatal("completion cannot distinguish received and processed")
	}
}
