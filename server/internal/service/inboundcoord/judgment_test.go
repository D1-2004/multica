package inboundcoord

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// TestRoutingContractUsesToolLoop locks the shipped prompt: Decide is a
// bounded assoc tool loop. DWS/search still live in the sandbox.
func TestRoutingContractUsesToolLoop(t *testing.T) {
	for _, rule := range []string{
		"assoc_recall",
		"assoc_bind",
		"issue_get",
		"issue_comment_list",
		"issue_comment_add",
		"finish",
		"You MUST call tools",
		"At most 8 model rounds",
		"No DWS, no search, no files",
		"Never finish action=reply with a capability refusal",
		"帮我约冬翔明天下午开半小时会对一下上海行程",
		"我没法查日程或订会议室",
		"pass that exact conversation_id",
		"empty items only answers a question explicitly asking for recorded matters",
		"Never invent conversation_id, person_id, or issue_id",
		"who is asking, who must be contacted",
		"without guessing whether that sender is the requester or the contacted recipient",
		"source=digital_employee or source=robot",
		"A robot sender uid may be absent",
		"Issue identity invariant",
		"Issue creator or issue_comment_add comment author",
		"tool executor/assistant",
		"original DingTalk task scene and assoc graph",
		"matched_via=event",
		"purpose and intent",
	} {
		if !strings.Contains(systemPrompt, rule) {
			t.Errorf("systemPrompt missing routing rule %q", rule)
		}
	}
	for _, banned := range []string{
		"You have no tools",
		"This loop's lack of tools is never a reason to reply",
	} {
		if strings.Contains(systemPrompt, banned) {
			t.Errorf("systemPrompt must not keep short-router wording %q", banned)
		}
	}
}

func TestJudgmentParseDecision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		turn    Turn
		raw     string
		want    Action
		look    bool
		ack     bool
		notText []string
	}{
		{
			name: "greeting_hello_reply",
			turn: Turn{Source: SourceWeb, Addressed: true, Message: "你好"},
			raw:  `{"action":"reply","text":"在，有事直接说。","look_into":"","reason":"打招呼"}`,
			want: ActionReply,
		},
		{
			name: "thanks_small_talk_reply",
			turn: Turn{Source: SourceRobot, Addressed: true, ChatType: "p2p", Message: "谢谢"},
			raw:  `{"action":"reply","text":"没事，随时叫我。","look_into":"","reason":"道谢"}`,
			want: ActionReply,
		},
		{
			name: "live_info_today_news_issue",
			turn: Turn{Source: SourceRobot, Addressed: true, ChatType: "p2p", Message: "帮我看看今天有什么新闻"},
			raw:  `{"action":"issue","text":"我先去看今天新闻","look_into":"今天新闻","reason":"要查实时资讯"}`,
			want: ActionIssue,
			look: true,
			ack:  true,
			notText: []string{
				"验收环境",
				"没有联网",
				"没法抓新闻",
				"没法抓",
			},
		},
		{
			name: "lookup_deadline_issue",
			turn: Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "p2p", Message: "查一下截止时间"},
			raw:  `{"action":"issue","text":"我先去对一下截止时间","look_into":"截止时间","reason":"要查资料"}`,
			want: ActionIssue,
			look: true,
			ack:  true,
		},
		{
			name: "do_work_write_issue",
			turn: Turn{Source: SourceWeb, Addressed: true, Message: "帮我整理一份报名表"},
			raw:  `{"action":"issue","text":"我先去整理报名表","look_into":"报名表","reason":"要动手做"}`,
			want: ActionIssue,
			look: true,
			ack:  true,
		},
		{
			name: "track_followup_issue",
			turn: Turn{Source: SourceDigitalEmployee, Addressed: true, Message: "跟一下昨天那笔报销"},
			raw:  `{"action":"issue","text":"我先去跟昨天那笔报销","look_into":"昨天报销","reason":"要持续跟进"}`,
			want: ActionIssue,
			look: true,
			ack:  true,
		},
		{
			name: "unaddressed_group_eat_silence",
			turn: Turn{Source: SourceDigitalEmployee, Addressed: false, ChatType: "group", Message: "晚上吃饭吗"},
			raw:  `{"action":"silence","text":"","look_into":"","reason":"群里闲聊不是叫我"}`,
			want: ActionSilence,
		},
		{
			name: "web_silence_json_continue",
			turn: Turn{Source: SourceWeb, Addressed: true, Message: "你好"},
			raw:  `{"action":"silence","text":"","look_into":"","reason":"误判"}`,
			want: ActionContinue,
		},
		{
			name: "invalid_json_continue",
			turn: Turn{Source: SourceWeb, Addressed: true, Message: "你好"},
			raw:  "not-json",
			want: ActionContinue,
		},
		{
			name: "empty_reply_text_continue",
			turn: Turn{Source: SourceWeb, Addressed: true, Message: "你好"},
			raw:  `{"action":"reply","text":"","look_into":"","reason":"空"}`,
			want: ActionContinue,
		},
		{
			name: "unknown_action_continue",
			turn: Turn{Source: SourceWeb, Addressed: true, Message: "你好"},
			raw:  `{"action":"chat","text":"hi","look_into":"","reason":"旧字段"}`,
			want: ActionContinue,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseDecision(tc.raw, tc.turn)
			if got.Action != tc.want {
				t.Fatalf("action = %s, want %s (%#v)", got.Action, tc.want, got)
			}
			if tc.look && strings.TrimSpace(got.LookInto) == "" {
				t.Fatalf("issue look_into empty: %#v", got)
			}
			if tc.ack && strings.TrimSpace(got.UserText) == "" {
				t.Fatalf("issue ack empty: %#v", got)
			}
			for _, banned := range tc.notText {
				if strings.Contains(got.UserText, banned) || strings.Contains(got.Reason, banned) {
					t.Fatalf("lookup/work landed a capability refusal %q in %#v", banned, got)
				}
			}
		})
	}
}

func TestJudgmentDecideDeterministic(t *testing.T) {
	enabledLLM := llm.New(llm.Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"})
	disabledLLM := llm.New(llm.Config{})
	cases := []struct {
		name string
		c    *Coordinator
		turn Turn
		want Action
	}{
		{
			name: "group_unaddressed_chatter_silence",
			c:    &Coordinator{LLM: enabledLLM},
			turn: Turn{Source: SourceDigitalEmployee, Addressed: false, ChatType: "group", Message: "晚上吃饭吗"},
			want: ActionSilence,
		},
		{
			name: "robot_group_unaddressed_silence",
			c:    &Coordinator{LLM: enabledLLM},
			turn: Turn{Source: SourceRobot, Addressed: false, ChatType: "group", Message: "你们晚上吃饭吗"},
			want: ActionSilence,
		},
		{
			name: "empty_web_message_continue",
			c:    &Coordinator{LLM: enabledLLM},
			turn: Turn{Source: SourceWeb, Addressed: true, Message: "   "},
			want: ActionContinue,
		},
		{
			name: "empty_dingtalk_message_silence",
			c:    &Coordinator{LLM: enabledLLM},
			turn: Turn{Source: SourceRobot, Addressed: true, ChatType: "p2p", Message: ""},
			want: ActionSilence,
		},
		{
			name: "llm_disabled_continue",
			c:    &Coordinator{LLM: disabledLLM},
			turn: Turn{Source: SourceWeb, Addressed: true, Message: "你好"},
			want: ActionContinue,
		},
		{
			name: "switch_off_continue",
			c:    &Coordinator{LLM: enabledLLM, Queries: &coordQueriesStub{inbound: false}},
			turn: Turn{Source: SourceWeb, Addressed: true, Message: "帮我看看今天有什么新闻", AgentID: testAgentID()},
			want: ActionContinue,
		},
		{
			name: "nil_coordinator_continue",
			c:    nil,
			turn: Turn{Source: SourceWeb, Addressed: true, Message: "你好"},
			want: ActionContinue,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.c.Decide(context.Background(), tc.turn)
			if got.Action != tc.want {
				t.Fatalf("action = %s, want %s", got.Action, tc.want)
			}
		})
	}
}

func TestParseDecisionIssueFillsLookIntoFromMessage(t *testing.T) {
	got := parseDecision(`{"action":"issue","text":"我先去看","look_into":"","reason":"要查"}`, Turn{
		Source:  SourceWeb,
		Message: "帮我看看今天有什么新闻",
	})
	if got.Action != ActionIssue {
		t.Fatalf("action = %s", got.Action)
	}
	if got.LookInto != "帮我看看今天有什么新闻" {
		t.Fatalf("look_into = %q", got.LookInto)
	}
}
