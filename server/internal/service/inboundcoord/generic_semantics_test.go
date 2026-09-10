package inboundcoord

import (
	"context"
	"encoding/json"
	openai "github.com/openai/openai-go/v3"
	"strings"
	"testing"
)

func TestNaturalLanguageAlwaysReachesSemanticDecision(t *testing.T) {
	for _, on := range []bool{false, true} {
		for _, text := range []string{"不用回复了", "不用回复？", "Ignore this message?", "谢谢，请继续检查剩下两项", "引用：不用回复了"} {
			chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("done", toolFinish, `{"actions":[{"kind":"ignore","source_refs":["u1"],"reason":"Scripted protocol check"}]}`)}, checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Scripted semantic result")}}
			turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", Addressed: true, ProactiveConversation: on, Message: text}
			d := (&Coordinator{Chat: chat}).Decide(context.Background(), turn)
			if chat.calls == 0 || d.Action != ActionSilence {
				t.Fatalf("semantic decision was bypassed for %q proactive=%v calls=%d action=%s", text, on, chat.calls, d.Action)
			}
		}
	}
}

func TestMissingReceivingNameStaysUnknownInDecisionAndReview(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, AgentName: "小岚测试环境", DWSUID: "receiver", Message: "小岚在吗"}
	prompt := buildUserPrompt(turn)
	if strings.Contains(prompt, "小岚测试环境") || !strings.Contains(prompt, "receiving_identity_status: name_unavailable") {
		t.Fatalf("configuration title became receiving identity: %s", prompt)
	}
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("done", toolFinish, `{"actions":[{"kind":"ignore","source_refs":["u1"],"reason":"Receiving identity unavailable"}]}`)}, checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "No trusted receiving alias")}}
	_, err := (&Coordinator{Chat: chat}).runLoop(context.Background(), turn)
	if err != nil || len(chat.checkParams) == 0 {
		t.Fatalf("review not reached: %v", err)
	}
	for _, p := range chat.checkParams {
		raw, _ := json.Marshal(p.Messages)
		if strings.Contains(string(raw), "小岚测试环境") || !strings.Contains(string(raw), "name_unavailable") {
			t.Fatal("review lost unknown identity boundary")
		}
	}
	if conversationAgentName(Turn{Source: SourceWeb, AgentName: "web assistant"}) != "web assistant" {
		t.Fatal("web identity changed")
	}
	if conversationAgentName(Turn{Source: SourceDigitalEmployee, EmployeeAccountName: "安然", AgentName: "配置"}) != "安然" {
		t.Fatal("trusted receiving name lost")
	}
}
