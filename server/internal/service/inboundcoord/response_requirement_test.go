package inboundcoord

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func TestRequiredResponseRefsRespectSourceIdentityAndWindow(t *testing.T) {
	self := WindowUtterance{Text: "@旧昵称 你之前干啥去了", Mentions: []MessageMention{{UID: "123"}}}
	other := WindowUtterance{Text: "@同事 帮忙看看", Mentions: []MessageMention{{UID: "456"}}}
	missing := WindowUtterance{Text: "反复就一句话？"}
	for _, tc := range []struct {
		name string
		turn Turn
		want []string
	}{
		{"direct without uid still responds", Turn{Source: SourceDigitalEmployee, ChatType: "single", Utterances: []WindowUtterance{missing}}, []string{"u1"}},
		{"direct burst", Turn{Source: SourceDigitalEmployee, ChatType: "p2p", Utterances: []WindowUtterance{missing, other}}, []string{"u1", "u2"}},
		{"self mention ignores stale name", Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, DWSUID: "123", EmployeeAccountName: "新昵称", Persona: "我叫另外一个名字", Utterances: []WindowUtterance{self}}, []string{"u1"}},
		{"other mention", Turn{Source: SourceDigitalEmployee, ChatType: "group", DWSUID: "123", Addressed: true, Utterances: []WindowUtterance{other}}, nil},
		{"window address not inherited", Turn{Source: SourceDigitalEmployee, ChatType: "group", DWSUID: "123", Addressed: true, Utterances: []WindowUtterance{other, self, missing}}, []string{"u2"}},
		{"missing group uid", Turn{Source: SourceDigitalEmployee, ChatType: "group", Addressed: true, Utterances: []WindowUtterance{self}}, nil},
		{"legacy single addressed", Turn{Source: SourceDigitalEmployee, DWSUID: "123", Addressed: true, Utterances: []WindowUtterance{missing}}, []string{"u1"}},
		{"legacy without uid", Turn{Source: SourceDigitalEmployee, Addressed: true, Utterances: []WindowUtterance{missing}}, nil},
		{"explicit empty mentions not legacy", Turn{Source: SourceDigitalEmployee, DWSUID: "123", Addressed: true, Utterances: []WindowUtterance{{Text: "你好", Mentions: []MessageMention{}}}}, nil},
		{"proactive flag not per source proof", Turn{Source: SourceDigitalEmployee, DWSUID: "123", Addressed: true, ProactiveConversation: true, Utterances: []WindowUtterance{missing}}, nil},
		{"task completion exempt", Turn{Source: SourceDigitalEmployee, Loop: LoopTaskFinished, ChatType: "direct", DWSUID: "123", Utterances: []WindowUtterance{self}}, nil},
		{"web not dingtalk identity", Turn{Source: SourceWeb, ChatType: "single", DWSUID: "123", Utterances: []WindowUtterance{self}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requiredResponseRefs(tc.turn); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("required refs = %v; want %v", got, tc.want)
			}
			if got := toolContractFor(tc.turn).requiredResponseRefs; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("schema contract refs = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestFinishCheckRepairsAllowedDirectIgnoreAndProjectsRequirement(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "single", Message: "反复就一句话？", DWSUID: "123", EmployeeAccountName: "旧名字", Persona: "我是岗位助理"}
	decision := Decision{Action: ActionSilence, CoordinationActions: []CoordinationAction{{Kind: "ignore", SourceRefs: []string{"u1"}, Reason: "这是别人的闲聊"}}}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Social message outside job.")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, decision, nil, 0, nil)
	if err != nil || result.Verdict != "revise" || !reflect.DeepEqual(result.MissingSourceRefs, []string{"u1"}) || !strings.Contains(result.Reason, "ack_kind=conversation") {
		t.Fatalf("direct ignore must be repaired: result=%+v err=%v", result, err)
	}
	prompt := buildUserPrompt(turn)
	if !strings.Contains(prompt, "response_required=true") || !strings.Contains(prompt, receivingIdentityAuthority) {
		t.Fatalf("routing model lacks response/identity facts: %s", prompt)
	}
	raw, err := json.Marshal(chat.checkParams[0].Messages[3])
	if err != nil {
		t.Fatal(err)
	}
	var message struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	var proposal struct {
		Window []struct {
			Required bool `json:"response_required"`
		} `json:"current_window"`
	}
	if err := json.Unmarshal([]byte(message.Content), &proposal); err != nil || len(proposal.Window) != 1 || !proposal.Window[0].Required {
		t.Fatalf("review projection missing: %s err=%v", message.Content, err)
	}
}

func TestRequiredResponseEnforcementPreservesOtherActionsAndReview(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, DWSUID: "123", ChatType: "group", Utterances: []WindowUtterance{
		{Text: "@其他人 查日志", Mentions: []MessageMention{{UID: "456"}}},
		{Text: "@本人 你在吗", Mentions: []MessageMention{{UID: "123"}}},
	}}
	decision := Decision{CoordinationActions: []CoordinationAction{
		{Kind: "ignore", SourceRefs: []string{"u1"}},
		{Kind: "acknowledge", SourceRefs: []string{"u2"}, AckKind: "conversation", Reply: "在，你说。"},
	}}
	result := finishCheckResult{Verdict: "allow"}
	enforceRequiredResponses(&result, turn, decision)
	if result.Verdict != "allow" {
		t.Fatalf("allowed social reply changed: %+v", result)
	}
	decision.CoordinationActions[1].Kind = "ignore"
	result = finishCheckResult{Verdict: "revise", Reason: "Existing privacy boundary", ConstraintQuote: "Do not disclose private data"}
	enforceRequiredResponses(&result, turn, decision)
	if result.Reason != "Existing privacy boundary" || result.ConstraintQuote == "" {
		t.Fatalf("existing review was overwritten: %+v", result)
	}
	result = finishCheckResult{Verdict: "allow"}
	enforceRequiredResponses(&result, turn, decision)
	if result.Verdict != "revise" || !reflect.DeepEqual(result.MissingSourceRefs, []string{"u2"}) {
		t.Fatalf("wrong source repaired: %+v", result)
	}
	turn.Loop = LoopTaskFinished
	result = finishCheckResult{Verdict: "allow"}
	enforceRequiredResponses(&result, turn, decision)
	if result.Verdict != "allow" {
		t.Fatalf("completion ignore was blocked: %+v", result)
	}
}
