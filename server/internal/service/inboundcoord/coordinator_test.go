package inboundcoord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
)

type coordQueriesStub struct {
	inbound bool
}

func (s coordQueriesStub) ListChatMessagesPage(context.Context, db.ListChatMessagesPageParams) ([]db.ChatMessage, error) {
	return nil, nil
}

func (s coordQueriesStub) GetAgent(context.Context, pgtype.UUID) (db.Agent, error) {
	return db.Agent{}, nil
}

func (s coordQueriesStub) CountRunningTasks(context.Context, pgtype.UUID) (int64, error) {
	return 0, nil
}

func (s coordQueriesStub) GetAgentInboundCoordinator(context.Context, pgtype.UUID) (bool, error) {
	return s.inbound, nil
}

func testAgentID() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
}

func TestIssueTitlePrefersUserMessageWhenLookIntoIsShort(t *testing.T) {
	got := IssueTitle(Decision{LookInto: "报名表"}, "问一下冬翔，今天想吃什么")
	if got != "问一下冬翔，今天想吃什么" {
		t.Fatalf("title=%q", got)
	}
}

func TestParseDecisionReply(t *testing.T) {
	got := parseDecision(`{"action":"reply","text":"在的，今天想先对哪件事？","look_into":"","reason":"这是打招呼"}`, Turn{Source: SourceWeb})
	if got.Action != ActionReply || got.UserText == "" || got.Reason != "这是打招呼" {
		t.Fatalf("got %#v", got)
	}
}

func TestParseDecisionIssueFillsAck(t *testing.T) {
	got := parseDecision(`{"action":"issue","text":"","look_into":"报名截止时间"}`, Turn{Source: SourceDigitalEmployee, Message: "看下截止"})
	if got.Action != ActionIssue {
		t.Fatalf("action = %s", got.Action)
	}
	if !strings.Contains(got.UserText, "报名截止时间") {
		t.Fatalf("ack = %q", got.UserText)
	}
}

func TestParseDecisionSilenceRejectedOnWeb(t *testing.T) {
	got := parseDecision(`{"action":"silence","text":""}`, Turn{Source: SourceWeb, Addressed: true, Message: "你好"})
	if got.Action != ActionContinue {
		t.Fatalf("web silence should fail open, got %s", got.Action)
	}
}

func TestParseDecisionSilenceAllowedForDigitalEmployee(t *testing.T) {
	got := parseDecision(`{"action":"silence","text":""}`, Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "group", Message: "晚上吃饭吗"})
	if got.Action != ActionSilence {
		t.Fatalf("got %s", got.Action)
	}
}

func TestGroupUnaddressedSilenceWithoutLLM(t *testing.T) {
	c := &Coordinator{LLM: llm.New(llm.Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"})}
	got := c.Decide(context.Background(), Turn{
		Source:    SourceDigitalEmployee,
		Addressed: false,
		ChatType:  "group",
		Message:   "你们晚上吃饭吗",
	})
	if got.Action != ActionSilence {
		t.Fatalf("got %s", got.Action)
	}
}

func TestDecideContinueWhenLLMDisabled(t *testing.T) {
	c := &Coordinator{LLM: llm.New(llm.Config{})}
	got := c.Decide(context.Background(), Turn{Source: SourceWeb, Addressed: true, Message: "你好"})
	if got.Action != ActionContinue {
		t.Fatalf("got %s", got.Action)
	}
}

func TestDecideSkipsWhenAgentSwitchOff(t *testing.T) {
	c := &Coordinator{
		LLM:     llm.New(llm.Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"}),
		Queries: coordQueriesStub{inbound: false},
	}
	started := time.Now()
	got := c.Decide(context.Background(), Turn{
		Source:    SourceWeb,
		Addressed: true,
		Message:   "你好",
		AgentID:   testAgentID(),
	})
	if got.Action != ActionContinue {
		t.Fatalf("got %s", got.Action)
	}
	if time.Since(started) > 200*time.Millisecond {
		t.Fatalf("switch-off should not call the LLM")
	}
}

func TestIssueTitleAndDescription(t *testing.T) {
	d := Decision{Action: ActionIssue, UserText: "我先核对报名表", LookInto: "报名表截止"}
	if IssueTitle(d, "长正文") != "报名表截止" {
		t.Fatalf("title = %q", IssueTitle(d, "长正文"))
	}
	desc := IssueDescription(d, "帮我看截止时间")
	if !strings.Contains(desc, "前台已对用户说") || !strings.Contains(desc, "帮我看截止时间") {
		t.Fatalf("description = %q", desc)
	}
}

func TestParseDecisionInvalidJSON(t *testing.T) {
	got := parseDecision("not-json", Turn{Source: SourceWeb, Message: "hi"})
	if got.Action != ActionContinue {
		t.Fatalf("got %s", got.Action)
	}
}
