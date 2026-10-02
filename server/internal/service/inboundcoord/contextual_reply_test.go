package inboundcoord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func TestDirectIgnoreIsRepairedToConversation(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", DWSUID: "6899376218", EmployeeAccountName: "Old role title", SceneID: testSceneID("cid"), ConversationID: "cid", HistoryStatus: "loaded", Utterances: []WindowUtterance{{Text: "@New name 反复就一句话？", Mentions: []MessageMention{{OpenDingTalkID: "6899376218"}}}}}
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("ignore", toolFinish, `{"actions":[{"kind":"ignore","source_refs":["u1"],"reason":"Different persona name"}]}`),
		assistantTool("conversation", toolFinish, `{"actions":[{"kind":"acknowledge","ack_kind":"conversation","source_refs":["u1"],"reply":"你说得对，刚才一直重复确认，没回答到你的问题。"}]}`),
	}, conversationRounds: []openai.ChatCompletion{assistantTool("render", toolConversationReplies, `{"replies":[{"action_ref":"a1","reply":"你说得对，刚才一直重复确认，没回答到你的问题。"}]}`)}, checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Responds to feedback about visible dialogue.")}}
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), turn)
	if err != nil || d.Action != ActionReply || d.LoopStopFallback() || !strings.Contains(d.UserText, "没回答到") || chat.calls != 2 {
		t.Fatalf("contextual repair failed: d=%+v calls=%d err=%v", d, chat.calls, err)
	}
	if len(d.CoordinationActions) != 1 || d.CoordinationActions[0].AckKind != "conversation" {
		t.Fatalf("missing explicit conversation action: %+v", d)
	}
}

func TestDirectResponseSchemaExcludesIgnorePerSource(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", DWSUID: "123", Utterances: []WindowUtterance{{Text: "hello", Mentions: []MessageMention{{UID: "123"}}}, {Text: "hello other", Mentions: []MessageMention{{UID: "456"}}}}}
	tool := windowPlanToolFor(turn, false)
	raw, _ := json.Marshal(tool.OfFunction.Function.Parameters)
	var params map[string]any
	_ = json.Unmarshal(raw, &params)
	item := params["properties"].(map[string]any)["actions"].(map[string]any)["items"].(map[string]any)
	found := false
	for _, v := range item["oneOf"].([]any) {
		props := v.(map[string]any)["properties"].(map[string]any)
		if props["kind"].(map[string]any)["enum"].([]any)[0] != "ignore" {
			continue
		}
		refs := props["source_refs"].(map[string]any)["items"].(map[string]any)["enum"].([]any)
		if len(refs) != 1 || refs[0] != "u2" {
			t.Fatalf("ignore exposed direct source: %s", raw)
		}
		found = true
	}
	if !found {
		t.Fatal("other-only source still needs ignore")
	}
	turn.Utterances = turn.Utterances[:1]
	tool = windowPlanToolFor(turn, false)
	raw, _ = json.Marshal(tool.OfFunction.Function.Parameters)
	if strings.Contains(string(raw), `"enum":["ignore"]`) {
		t.Fatalf("single direct message advertises ignore: %s", raw)
	}
}

func TestTrustedMentionGroundsConversationWithoutNameQuote(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Loop: LoopInbound, ProactiveConversation: true, ChatType: "group", DWSUID: "123", Utterances: []WindowUtterance{{Text: "你之前干啥去了", Mentions: []MessageMention{{UID: "123"}}}}}
	result := finishCheckResult{Verdict: "allow", ParticipationChecks: []finishParticipationCheck{{SourceRefs: []string{"u1"}, Basis: "direct", Disposition: "coordinate"}}}
	d := Decision{CoordinationActions: []CoordinationAction{{Kind: "acknowledge", AckKind: "conversation", SourceRefs: []string{"u1"}, Reply: "刚才没有回应到你的问题，抱歉。"}}}
	if err := validateFinishParticipationChecks(&result, turn, d); err != nil || result.Verdict != "allow" {
		t.Fatalf("trusted UID must ground recipient without display-name quote: %+v err=%v", result, err)
	}
}
