package inboundcoord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func TestGroupReviewSharesParticipationPolicyAndTrustedIdentity(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, AgentName: "小周 Runtime", EmployeeAccountName: "小助", DWSUID: "employee-id", Utterances: []WindowUtterance{
		{Text: "@小周 在吗", Sender: "小林", Mentions: []MessageMention{{UID: "other-id"}}},
		{Text: "小助在吗", Sender: "小林", Mentions: []MessageMention{}},
	}}
	prompt := buildUserPrompt(turn)
	for _, want := range []string{"employee_account_name: 小助", "employee_uid: employee-id", `mentions=[{"uid":"other-id"}]`, "mentions=[]"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("trusted facts missing from main loop: %s", want)
		}
	}
	for _, action := range []Action{ActionReply, ActionSilence, ActionIssue} {
		modules := policyModuleIDs(Turn{Loop: LoopFinishCheck, FinishCheckAction: action, Source: turn.Source, ChatType: turn.ChatType}, false)
		if !modules["group"] || !modules["channel"] {
			t.Fatalf("review %s lost participation policy: %v", action, modules)
		}
	}
	verdict := func(value, reason string) openai.ChatCompletion {
		body, _ := json.Marshal(map[string]any{
			"verdict": value, "reason": reason, "missing_source_refs": []string{},
			"request_quote_ref": scriptedRequestQuoteRef, "candidate_quote_ref": scriptedCandidateQuoteRef,
			"work_checks": []any{}, "participation_checks": []finishParticipationCheck{
				{SourceRefs: []string{"u1"}, Basis: "other", RecipientQuote: "@小周", Disposition: "ignore"},
				{SourceRefs: []string{"u2"}, Basis: "direct", RecipientQuote: "小助", Disposition: "coordinate"},
			},
		})
		return assistantTool("review", toolFinishCheck, string(body))
	}
	// Protocol regression: a reviewer rejection must prevent the wrong reply
	// from being saved; this scripted test does NOT certify model judgement.
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("bad", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1","u2"],"ack_kind":"greeting","reply":"小周和小助都在。"}]}`),
		assistantTool("fixed", toolFinish, `{"actions":[{"kind":"ignore","source_refs":["u1"],"reason":"Addressed to another person."},{"kind":"acknowledge","source_refs":["u2"],"ack_kind":"greeting","reply":"小助在。"}]}`),
	}, checkRounds: []openai.ChatCompletion{verdict("revise", "u1 addresses another person, not this employee."), verdict("allow", "Only the employee's greeting is answered.")}}
	var saved []Decision
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(d Decision) error { saved = append(saved, d); return nil })
	d, err := (&Coordinator{Chat: chat}).runLoop(ctx, turn)
	if err != nil || len(saved) != 1 || d.UserText != "小助在。" {
		t.Fatalf("wrong reply escaped review: d=%+v err=%v saved=%d", d, err, len(saved))
	}
	for _, p := range chat.checkParams {
		raw, _ := json.Marshal(p.Messages)
		s := string(raw)
		for _, want := range []string{"policy:group@", "employee_account_name", "other-id"} {
			if !strings.Contains(s, want) {
				t.Fatalf("review input lost %s", want)
			}
		}
	}
}

func proactiveRelevanceFixtures() []coordinatorReplayFixture {
	group := func(text string) Turn {
		return Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, SceneID: testSceneID("cidReplaySyntheticSceneA=="), ConversationID: "cidReplaySyntheticSceneA==", PersonID: "sender", SenderName: "小林", AgentName: "小周 Runtime", EmployeeAccountName: "小助", DWSUID: "employee-id", Message: text, Instructions: "你是小助，群里的数字员工。职责是协助排查支付故障、整理经用户授权的工作材料。不要替同事答应工作。", HistoryStatus: "empty", SceneMemoryStatus: "empty", SkillsStatus: "empty"}
	}
	otherMention := group("@小周 在吗")
	otherMention.Utterances = []WindowUtterance{{Text: otherMention.Message, Sender: "小林", Mentions: []MessageMention{{UID: "other-id"}}}}
	mixed := group("unused")
	mixed.Addressed = true
	mixed.Utterances = []WindowUtterance{{Text: "@小周 在吗", Sender: "小林", Mentions: []MessageMention{{UID: "other-id"}}}, {Text: "@小助 在吗", Sender: "小林", Mentions: []MessageMention{{UID: "employee-id"}}}}
	old := replayCard{ID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Purpose: "小林委托小助排查支付接口超时", Status: "in_progress", Comment: "正在排查支付超时，等待错误日志。"}
	fixtures := []coordinatorReplayFixture{
		{ID: "proactive_other_mention", Turn: otherMention, Actions: []Action{ActionSilence}, Cards: []replayCard{old}},
		{ID: "proactive_other_name", Turn: group("小陈在吗"), Actions: []Action{ActionSilence}, Cards: []replayCard{old}},
		{ID: "proactive_other_assignment", Turn: group("小陈，帮我查一下刚才那笔支付为什么超时。"), Actions: []Action{ActionSilence}, Cards: []replayCard{old}},
		{ID: "proactive_chatter", Turn: group("小陈，晚上一起吃饭，我已经订好位了。"), Actions: []Action{ActionSilence}},
		{ID: "proactive_bare_greeting", Turn: group("在吗"), Actions: []Action{ActionSilence}},
		{ID: "proactive_employee_name", Turn: group("小助在吗"), Actions: []Action{ActionReply}},
		{ID: "proactive_employee_role", Turn: group("有数字员工在吗"), Actions: []Action{ActionReply}},
		{ID: "proactive_invitation", Turn: group("小助，帮小陈整理下面这条故障记录成一句摘要：支付接口超时。"), Actions: []Action{ActionIssue}, Items: []replayExpectedItem{{Refs: []string{"u1"}, Basis: "new_request"}}, RequireRecall: true},
		{ID: "proactive_open_role_issue", Turn: group("支付接口从刚才开始大量超时，大家有空帮忙排查一下吗？"), Actions: []Action{ActionIssue}, Items: []replayExpectedItem{{Refs: []string{"u1"}, Basis: "new_request"}}, RequireRecall: true},
		{ID: "proactive_mixed_recipients", Turn: mixed, Actions: []Action{ActionReply}, NonWorkRefs: []string{"u1", "u2"}},
		{ID: "proactive_same_work_addition", Turn: group("补充你正在查的支付超时：错误码是 E408，只在华东出现。"), Cards: []replayCard{old}, Actions: []Action{ActionIssue}, Items: []replayExpectedItem{{IssueID: old.ID, Refs: []string{"u1"}, Basis: "change"}}, RequireRecall: true},
		{ID: "proactive_presence_no_restart", Turn: group("小助还在吗？之前的支付排查继续处理就好，这句只需简单回应。"), Cards: []replayCard{old}, Actions: []Action{ActionReply}},
	}
	for i := range fixtures {
		fixtures[i].ContractID = "group_recipient_role_contrast"
	}
	return fixtures
}

func TestMentionRelationUsesOnlyTrustedUIDs(t *testing.T) {
	cases := []struct {
		mentions  []MessageMention
		uid, want string
	}{
		{nil, "employee", "unknown"}, {[]MessageMention{}, "employee", "none"},
		{[]MessageMention{{UID: "other"}}, "employee", "other_only"},
		{[]MessageMention{{UID: "other"}, {UID: "employee"}}, "employee", "includes_employee"},
		{[]MessageMention{{OpenDingTalkID: "unresolved"}}, "employee", "unknown"},
		{[]MessageMention{{OpenDingTalkID: "6899376218"}}, "6899376218", "includes_employee"},
		{[]MessageMention{{UID: "other", OpenDingTalkID: "6899376218"}}, "6899376218", "other_only"},
		{[]MessageMention{{UID: "6899376218", OpenDingTalkID: "opaque"}}, "6899376218", "includes_employee"},
		{[]MessageMention{{OpenDingTalkID: "6899376219"}}, "6899376218", "unknown"},
		{[]MessageMention{{OpenDingTalkID: "employee"}}, "employee", "unknown"},
		{[]MessageMention{{UID: "other"}}, "", "unknown"},
	}
	for _, c := range cases {
		if got := mentionRelation(Turn{DWSUID: c.uid}, WindowUtterance{Mentions: c.mentions}); got != c.want {
			t.Fatalf("got %s, want %s", got, c.want)
		}
	}
}
