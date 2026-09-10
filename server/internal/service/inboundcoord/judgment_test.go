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
			tools:   []string{toolAssocRecall, toolContextRead, toolWorkState, toolFinish},
		},
		{
			name: "previous_question_before_short_answer", turn: Turn{Source: SourceRobot, HistoryStatus: "loaded", DingTalkHistory: []HistoryLine{{Role: "assistant", Content: "周五三点可以吗？"}}, Message: "可以，三点没问题"},
			modules: map[string][]string{"dialogue": {"COORD.F02", "COORD.F03", "COORD.F06", "COORD.F08", "COORD.F18"}},
			absent:  []string{"recall_match", "skills", "completion"},
			tools:   []string{toolAssocRecall, toolContextRead, toolFinish},
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
			name: "switch_off_skips_window_ack_silence",
			c:    &Coordinator{LLM: enabledLLM, Queries: &coordQueriesStub{inbound: false}},
			turn: Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "p2p", Message: "谢谢", AgentID: testAgentID()},
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
