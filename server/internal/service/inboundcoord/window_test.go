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

func TestAllWindowAckLiveInboundForms(t *testing.T) {
	t.Parallel()
	acks := []string{
		"谢谢",
		"@东翔测试号 谢谢",
		"@东翔测试号  谢谢",
		"<@DIBwz3Bm4ugAGaaIaZvSXyAiEiE> 谢谢",
		"冬翔 在钉钉会话中的消息：\n\n谢谢",
		"冬翔 在钉钉会话中的消息：\n\n@东翔测试号  谢谢",
		"冬翔 在钉钉会话中的消息：\n\n谢谢\n\n好的",
		"钉钉会话消息：\n\n不用回了",
	}
	for _, msg := range acks {
		if !AllWindowAck(Turn{Message: msg}) {
			t.Fatalf("ack form must Host-silence: %q", msg)
		}
	}
	if AllWindowAck(Turn{Message: "冬翔 在钉钉会话中的消息：\n\n帮我订下周去上海的高铁"}) {
		t.Fatal("display-wrapped ask is not ack")
	}
}

func TestR5ParkedAskNotSilencedByAckBurst(t *testing.T) {
	t.Parallel()
	got := KeepWorkUtterances(Turn{
		Message: "冬翔 在钉钉会话中的消息：\n\n帮我订下周去上海的高铁，token=R5-4059-W5\n\n谢谢\n\n好的",
		Utterances: []WindowUtterance{
			{Sender: "冬翔", Text: "<@DIBwz3Bm4ugAGaaIaZvSXyAiEiE> 帮我订下周去上海的高铁，token=R5-4059-W5"},
			{Sender: "冬翔", Text: "<@DIBwz3Bm4ugAGaaIaZvSXyAiEiE> 谢谢"},
			{Sender: "冬翔", Text: "<@DIBwz3Bm4ugAGaaIaZvSXyAiEiE> 好的"},
		},
	})
	if AllWindowAck(got) {
		t.Fatal("parked ask plus ACK burst must not become an ACK window")
	}
	if len(got.Utterances) != 1 || !strings.Contains(got.Utterances[0].Text, "R5-4059-W5") {
		t.Fatalf("must keep the ask: %#v", got.Utterances)
	}
	decision := (&Coordinator{}).Decide(context.Background(), Turn{
		Source:     SourceDigitalEmployee,
		Addressed:  true,
		ChatType:   "group",
		Message:    got.Message,
		SenderName: "冬翔",
		Utterances: got.Utterances,
	})
	if decision.Action == ActionSilence {
		t.Fatal("Host must not silence the parked 高铁 ask")
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

func TestKeepWorkUtterancesDropsAckLines(t *testing.T) {
	t.Parallel()
	got := KeepWorkUtterances(Turn{
		Message: "@东翔测试号 帮我订高铁\n谢谢",
		Utterances: []WindowUtterance{
			{Sender: "冬翔", Text: "@东翔测试号 帮我订下周去上海的高铁"},
			{Sender: "冬翔", Text: "谢谢"},
			{Sender: "dxxh", Text: "好的"},
		},
	})
	if len(got.Utterances) != 1 || got.Utterances[0].Text != "@东翔测试号 帮我订下周去上海的高铁" {
		t.Fatalf("utterances=%#v", got.Utterances)
	}
	if got.Message != "@东翔测试号 帮我订下周去上海的高铁" {
		t.Fatalf("message=%q", got.Message)
	}
	unchanged := KeepWorkUtterances(Turn{Message: "谢谢", Utterances: []WindowUtterance{{Text: "谢谢"}}})
	if unchanged.Message != "谢谢" || len(unchanged.Utterances) != 1 {
		t.Fatalf("all-ack window must stay intact: %#v", unchanged)
	}
}

func TestDecideKeepsWorkWhenMixedWithAck(t *testing.T) {
	t.Parallel()
	got := (&Coordinator{}).Decide(context.Background(), Turn{
		Source:     SourceDigitalEmployee,
		Addressed:  true,
		ChatType:   "group",
		Message:    "帮我订高铁\n谢谢",
		SenderName: "冬翔",
		Utterances: []WindowUtterance{
			{Sender: "冬翔", Text: "帮我订下周去上海的高铁"},
			{Sender: "冬翔", Text: "谢谢"},
		},
	})
	if got.Action == ActionSilence {
		t.Fatal("mixed ACK+ask must not Host-silence the ask")
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
	wrapped := (&Coordinator{}).Decide(context.Background(), Turn{
		Source:     SourceDigitalEmployee,
		Addressed:  true,
		ChatType:   "group",
		Message:    "冬翔 在钉钉会话中的消息：\n\n谢谢\n\n好的",
		SenderName: "冬翔",
	})
	if wrapped.Action != ActionSilence || wrapped.Reason != "window_ack" {
		t.Fatalf("display-wrapped ACK burst must Host-silence, got action=%s reason=%q", wrapped.Action, wrapped.Reason)
	}
}
