package inboundcoord

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// These checks preserve the disclosure and Host-tool contracts behind earlier
// incidents. They do not substitute for real-model judgment replays.
func TestRoutingContractUsesToolLoop(t *testing.T) {
	inbound := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", HistoryStatus: "not_loaded", Message: "你会什么"}
	inbound.Skills = []SkillSnapshot{{Name: "dingtalk-minutes", Description: "查询听记并整理行动项"}}
	for _, tc := range []struct {
		name     string
		turn     Turn
		recalled bool
		modules  map[string][]string
		absent   []string
		tools    []string
	}{
		{
			name: "capabilities_before_recall", turn: inbound,
			modules: map[string][]string{
				"core":    {"COORD.F01", "COORD.F03", "COORD.F13", "COORD.F17"},
				"inbound": {"COORD.F04", "COORD.F05", "COORD.F06", "COORD.F07"},
				"channel": {"COORD.F01", "COORD.F08"},
				"skills":  {"COORD.F03", "COORD.F04", "COORD.F14"},
				"voice":   {"COORD.F14", "COORD.F15"},
			},
			absent: []string{"recall_match", "dialogue", "completion"},
			tools:  []string{toolAssocRecall, toolContextRead, toolFinish},
		},
		{
			name: "candidate_comparison_before_work_submission", turn: inbound, recalled: true,
			modules: map[string][]string{"recall_match": {"COORD.F02", "COORD.F03", "COORD.F07", "COORD.F08", "COORD.F13", "COORD.F17"}},
			absent:  []string{"completion"},
			tools:   []string{toolAssocRecall, toolContextRead, toolIssueGet, toolIssueCommentList, toolFinish},
		},
		{
			name: "previous_question_before_short_answer", turn: Turn{Source: SourceRobot, HistoryStatus: "loaded", DingTalkHistory: []HistoryLine{{Role: "assistant", Content: "周五三点可以吗？"}}, Message: "可以，三点没问题"},
			modules: map[string][]string{"dialogue": {"COORD.F02", "COORD.F03", "COORD.F06", "COORD.F08", "COORD.F18"}},
			absent:  []string{"recall_match", "skills", "completion"},
			tools:   []string{toolAssocRecall, toolFinish},
		},
		{
			name: "completion_isolated_from_inbound", turn: Turn{Loop: LoopTaskFinished, Source: SourceDigitalEmployee, Skills: inbound.Skills, SceneMemory: "unrelated fact"},
			modules: map[string][]string{"core": {"COORD.F03", "COORD.F13", "COORD.F17"}, "voice": {"COORD.F14"}, "completion": {"COORD.F03", "COORD.F12", "COORD.F13", "COORD.F17"}},
			absent:  []string{"inbound", "channel", "skills", "memory", "recall_match", "dialogue"},
			tools:   []string{toolIssueGet, toolIssueCommentList, toolFinish},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertDisclosedObligations(t, tc.turn, tc.recalled, tc.modules, tc.absent)
			got := toolParamNames(toolsForDisclosure(tc.turn, 0, tc.recalled))
			slices.Sort(got)
			slices.Sort(tc.tools)
			if !slices.Equal(got, tc.tools) {
				t.Fatalf("tool boundary = %v, want %v; writes and external execution belong to Host/sandbox", got, tc.tools)
			}
		})
	}
}

// assertDisclosedObligations checks that a historical obligation is actually
// supplied by its registered module in this stage, rather than merely existing
// somewhere in the policy repository. Prompt wording is intentionally not frozen.
func assertDisclosedObligations(t *testing.T, turn Turn, recalled bool, want map[string][]string, absent []string) {
	t.Helper()
	manifest := policyManifestForStage(turn, recalled)
	selected := map[string]policyModule{}
	for _, module := range selectedPolicyModules(turn, recalled) {
		selected[module.ID] = module
	}
	for moduleID, rules := range want {
		module, ok := selected[moduleID]
		if !ok {
			t.Errorf("stage omitted historical obligations in %s", moduleID)
			continue
		}
		for _, rule := range rules {
			if !slices.Contains(module.OwnsRuleIDs, rule) || !slices.Contains(manifest.ActiveRuleIDs, rule) {
				t.Errorf("%s obligation %s is not owned and active in this stage", moduleID, rule)
			}
		}
		if strings.TrimSpace(policyModuleBody(module)) == "" {
			t.Errorf("%s has metadata but no model-visible policy", moduleID)
		}
	}
	for _, moduleID := range absent {
		if _, ok := selected[moduleID]; ok {
			t.Errorf("stage prematurely disclosed %s", moduleID)
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
			name: "empty_issue_text_continue",
			turn: Turn{Source: SourceRobot, Addressed: true, ChatType: "p2p", Message: "hi"},
			raw:  `{"action":"issue","issue_id":"8aae2a90-009e-4338-b17a-13ce6ff2f82a","text":""}`,
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
			name: "llm_disabled_defers_without_execution",
			c:    &Coordinator{LLM: disabledLLM},
			turn: Turn{Source: SourceWeb, Addressed: true, Message: "你好"},
			want: ActionDeferred,
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

func TestParseDecisionNewIssueComposesPurpose(t *testing.T) {
	t.Parallel()
	got := parseDecision(`{"action":"issue","text":"我去问须莫v6明早有没有会议","delegator":"须莫🥥","purpose":"向须莫v6询问明早有没有会议","intent":"ask","reason":"新事项"}`, Turn{
		Source:     SourceDigitalEmployee,
		SenderName: "须莫🥥",
		Message:    "问一下须莫v6明早有没有会议",
	})
	if got.Action != ActionIssue {
		t.Fatalf("action=%s", got.Action)
	}
	if got.Purpose != "须莫🥥委托：向须莫v6询问明早有没有会议" || got.Intent != "ask" {
		t.Fatalf("purpose=%q intent=%q", got.Purpose, got.Intent)
	}
	if got.LookInto != "须莫🥥委托：向须莫v6询问明早有没有会议" {
		t.Fatalf("look_into=%q", got.LookInto)
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
