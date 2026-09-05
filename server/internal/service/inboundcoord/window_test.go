package inboundcoord

import (
	"context"
	"strings"
	"testing"
)

func TestAllWindowAck(t *testing.T) {
	t.Parallel()
	if !AllWindowAck(Turn{Message: "谢谢"}) {
		t.Fatal("thanks")
	}
	if !AllWindowAck(Turn{Message: "@菲迪 谢谢"}) {
		t.Fatal("display mention thanks")
	}
	if !AllWindowAck(Turn{Message: "@菲迪 不用回复了"}) {
		t.Fatal("stop-reply")
	}
	if AllWindowAck(Turn{Message: "问一下 dxxh 周五三点"}) {
		t.Fatal("real ask")
	}
	if AllWindowAck(Turn{Message: "这个问题不用回复领导，要写进周报"}) {
		t.Fatal("ask containing 不用回复 must not silence")
	}
	if !AllWindowAck(Turn{Utterances: []WindowUtterance{
		{Sender: "测试号", Text: "好的"},
		{Sender: "dxxh", Text: "谢谢"},
	}}) {
		t.Fatal("all ack utterances")
	}
	if AllWindowAck(Turn{Utterances: []WindowUtterance{
		{Sender: "测试号", Text: "好的"},
		{Sender: "dxxh", Text: "查一下 token"},
	}}) {
		t.Fatal("mixed window is not ack")
	}
}

func TestParseDecisionTwoWindowItems(t *testing.T) {
	t.Parallel()
	got := parseDecision(`{
		"action":"issue",
		"text":"我去问 dxxh，顺便查 token",
		"items":[
			{"delegator":"测试号","purpose":"向dxxh确认周五三点开会","intent":"ask","look_into":"委托人测试号；对象dxxh；交付物确认周五三点"},
			{"delegator":"dxxh","purpose":"查询今日token消耗","intent":"lookup","look_into":"委托人dxxh；交付物今日token"}
		]
	}`, Turn{
		Source:         SourceDigitalEmployee,
		SenderName:     "测试号",
		Message:        "问 dxxh",
		ConversationID: "cid-group",
		Utterances: []WindowUtterance{
			{Sender: "测试号", Text: "问 dxxh 周五三点"},
			{Sender: "dxxh", Text: "查一下今天 token"},
		},
	})
	if got.Action != ActionIssue {
		t.Fatalf("action=%s", got.Action)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items=%d", len(got.Items))
	}
	if got.Items[0].Delegator != "测试号" || got.Items[1].Delegator != "dxxh" {
		t.Fatalf("delegators=%q %q", got.Items[0].Delegator, got.Items[1].Delegator)
	}
	if !strings.Contains(got.Items[0].LookInto, "cid-group") {
		t.Fatalf("look_into missing scene cid: %q", got.Items[0].LookInto)
	}
}

func TestParseDecisionRejectsForeignDelegator(t *testing.T) {
	t.Parallel()
	got := parseDecision(`{
		"action":"issue",
		"text":"我去问",
		"items":[{"delegator":"冬翔","purpose":"向dxxh确认周五三点开会","intent":"ask"}]
	}`, Turn{
		Source:     SourceDigitalEmployee,
		SenderName: "测试号",
		Utterances: []WindowUtterance{{Sender: "测试号", Text: "问 dxxh"}},
	})
	if got.Action == ActionIssue && len(got.Items) > 0 && got.Items[0].Delegator == "冬翔" {
		t.Fatal("must not keep a delegator who did not speak in the window")
	}
	if strings.Contains(got.Purpose, "冬翔") {
		t.Fatalf("purpose must not keep foreign delegator: %q", got.Purpose)
	}
}

func TestParseDecisionSameDeliverableStaysOneItemWhenModelSendsOne(t *testing.T) {
	t.Parallel()
	got := parseDecision(`{
		"action":"issue",
		"text":"我去问 dxxh 周五三点",
		"delegator":"测试号",
		"purpose":"向dxxh确认周五三点开会",
		"intent":"ask"
	}`, Turn{
		Source:     SourceDigitalEmployee,
		SenderName: "测试号",
		Utterances: []WindowUtterance{
			{Sender: "测试号", Text: "帮我问 dxxh"},
			{Sender: "测试号", Text: "就说周五三点开会"},
		},
	})
	if got.Action != ActionIssue || len(got.Items) != 1 {
		t.Fatalf("got action=%s items=%d", got.Action, len(got.Items))
	}
	if got.Items[0].Delegator != "测试号" {
		t.Fatalf("delegator=%q", got.Items[0].Delegator)
	}
}

func TestDecideSilencesAckWindowWithoutLLM(t *testing.T) {
	t.Parallel()
	got := (&Coordinator{}).Decide(context.Background(), Turn{
		Source:     SourceDigitalEmployee,
		Addressed:  true,
		ChatType:   "group",
		Message:    "谢谢",
		SenderName: "测试号",
	})
	if got.Action != ActionSilence {
		t.Fatalf("ack window must silence without LLM, got %s", got.Action)
	}
}
