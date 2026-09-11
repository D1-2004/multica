package inboundcoord

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestR5ParkedAskNotSilencedByAckBurst(t *testing.T) {
	t.Parallel()
	got := Turn{
		Message: "冬翔 在钉钉会话中的消息：\n\n帮我订下周去上海的高铁，token=R5-4059-W5\n\n谢谢\n\n好的",
		Utterances: []WindowUtterance{
			{Sender: "冬翔", Text: "<@DIBwz3Bm4ugAGaaIaZvSXyAiEiE> 帮我订下周去上海的高铁，token=R5-4059-W5"},
			{Sender: "冬翔", Text: "<@DIBwz3Bm4ugAGaaIaZvSXyAiEiE> 谢谢"},
			{Sender: "冬翔", Text: "<@DIBwz3Bm4ugAGaaIaZvSXyAiEiE> 好的"},
		},
	}
	if len(windowUtterances(got)) != 3 || !strings.Contains(got.Utterances[0].Text, "R5-4059-W5") {
		t.Fatalf("must keep the ask and all original replies: %#v", got.Utterances)
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

func TestWindowUtterancesRetainAssentAndOutputPreference(t *testing.T) {
	t.Parallel()
	utterances := []WindowUtterance{
		{Sender: "冬翔", Text: "@东翔测试号 帮我订下周去上海的高铁"},
		{Sender: "冬翔", Text: "谢谢"},
		{Sender: "dxxh", Text: "好的"},
		{Sender: "冬翔", Text: "不用回复了"},
	}
	got := windowUtterances(Turn{Utterances: utterances})
	if !reflect.DeepEqual(got, utterances) {
		t.Fatalf("the complete conversational meaning must remain: %#v", got)
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

func TestWindowUtterancesPreserveEvidenceAndMissingIdentity(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 9, 7, 11, 14, 1, 0, time.UTC)
	utterances := []WindowUtterance{
		{Sender: "同名", SenderID: "uid-a", EvidenceID: "message-a", Timestamp: ts, ReplyToEvidenceID: "question-a", Text: " 好\n"},
		{Sender: "同名", SenderID: "uid-b", EvidenceID: "message-b", Timestamp: ts.Add(time.Second), Text: "谢谢"},
		{Text: "行"},
	}
	got := windowUtterances(Turn{SenderName: "另一位", PersonID: "other-uid", EvidenceID: "other-message", Utterances: utterances})
	if !reflect.DeepEqual(got, utterances) {
		t.Fatalf("source metadata and missing identity must not be rewritten: %#v", got)
	}
	fallback := windowUtterances(Turn{SenderName: "同名", PersonID: "uid-a", EvidenceID: "message-a", MessageTimestamp: ts, Message: " 好\n"})
	if len(fallback) != 1 || fallback[0].SenderID != "uid-a" || fallback[0].EvidenceID != "message-a" || !fallback[0].Timestamp.Equal(ts) || fallback[0].Text != " 好\n" {
		t.Fatalf("single-message source evidence was lost: %#v", fallback)
	}
	missing := windowUtterances(Turn{Message: "好"})
	if missing[0].EvidenceID != "" || missing[0].SenderID != "" || !missing[0].Timestamp.IsZero() {
		t.Fatalf("missing source metadata must not be manufactured: %#v", missing)
	}
}
