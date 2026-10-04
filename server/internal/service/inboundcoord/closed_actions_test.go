package inboundcoord

import (
	"encoding/json"
	"strings"
	"testing"
)

func completionFinishJSON(turn Turn, reply string) string {
	raw, _ := json.Marshal(map[string]any{"actions": []CoordinationAction{{Kind: "report_result", Reply: reply, ResultRef: currentResultRef(turn)}}})
	return string(raw)
}

func TestClosedActionsRejectGenericAndCrossKindFields(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "你好"}
	for _, raw := range []string{
		`{"action":"reply","text":"只生成一份。"}`,
		`{"actions":[{"kind":"reply","source_refs":["u1"],"reply":"只生成一份。"}]}`,
		`{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"other","reply":"收到。"}]}`,
		`{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"receipt","reply":"收到。","purpose":"隐藏的业务执行"}]}`,
		`{"actions":[{"kind":"ignore","source_refs":["u1"],"reason":"无需回复","reply":"夹带业务答案"}]}`,
		`{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我来查证。","purpose":"查证主持人与参会人听记生成数量","intent":"lookup","basis":"new_request"}]}`,
		`{"actions":[{"kind":"clarify","source_refs":["u1"],"reply":"要发给谁？","missing_fields":["unknown"]}]}`,
		`{"actions":[{"kind":"acknowledge","source_refs":["u1","u1"],"ack_kind":"receipt","reply":"收到。"}]}`,
		`{"actions":[{"kind":"acknowledge","source_refs":["u2"],"ack_kind":"receipt","reply":"收到。"}]}`,
	} {
		if d, err := parseValidatedWindowPlan(raw, turn, nil, nil); err == nil {
			t.Fatalf("invalid action accepted: %s => %#v", raw, d)
		}
	}
}

func TestClosedActionsCoverWholeWindowAndRetainMixedIntents(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, SenderName: "冬翔", Utterances: []WindowUtterance{{Sender: "冬翔", Text: "请查一下听记生成规则；另外帮我发个通知。"}, {Sender: "须莫", Text: "谢谢"}}}
	raw := `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我来查证听记生成规则。","purpose":"查证主持人和参会人开启听记的生成数量","intent":"lookup"},{"kind":"clarify","source_refs":["u1"],"missing_fields":["recipient","message_body"],"reply":"通知发给谁，正文是什么？"},{"kind":"acknowledge","source_refs":["u2"],"ack_kind":"thanks","reply":"不客气。"}]}`
	got, err := parseValidatedWindowPlan(raw, turn, nil, nil)
	if err != nil || got.Action != ActionIssue || len(got.Items) != 1 || len(got.CoordinationActions) != 3 {
		t.Fatalf("mixed intents lost: %#v err=%v", got, err)
	}
	if strings.Join(got.NonWorkRefs, ",") != "u2" || got.Items[0].Delegator != "冬翔" || !strings.Contains(got.Items[0].Content, turn.Utterances[0].Text) {
		t.Fatalf("source identity or complete request lost: %#v", got)
	}
	var partial map[string][]CoordinationAction
	_ = json.Unmarshal([]byte(raw), &partial)
	partial["actions"] = partial["actions"][:2]
	incomplete, _ := json.Marshal(partial)
	if _, err := parseValidatedWindowPlan(string(incomplete), turn, nil, nil); err == nil {
		t.Fatal("omitting the final non-work source must reject the whole proposal")
	}
}

func TestClosedActionsStatusAndMemoryReferencesAreHostOwned(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "进展如何？", SceneMemoryRevision: 4,
		CoordinationReads: []CoordinationRead{{ReadRef: "r2", Tool: toolAssocRecall, Result: json.RawMessage(`{"items":[],"complete":false}`)}, {ReadRef: "r3", Tool: toolWorkState, Failed: true, Result: json.RawMessage(`{"status":"unavailable"}`)}, {ReadRef: "r4", Tool: toolContextRead, Result: json.RawMessage(`{"status":"loaded"}`)}, {ReadRef: "r5", Tool: toolContextRead, Kind: coordinationStateKind, Result: json.RawMessage(`{"status":"not_loaded","records":[]}`)}}}
	for _, ref := range []string{"r2", "r3", "r5"} {
		raw := `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["` + ref + `"],"reply":"这次查询范围不足，还不能确认是否完成。"}]}`
		if _, err := parseValidatedWindowPlan(raw, turn, nil, nil); err != nil {
			t.Fatalf("bounded empty/unavailable state remains reportable: %v", err)
		}
	}
	for _, refs := range []string{`["r1"]`, `["r4"]`, `["u1"]`, `["r2","r2"]`, `[]`} {
		raw := `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":` + refs + `,"reply":"这件事已经完成。"}]}`
		if _, err := parseValidatedWindowPlan(raw, turn, nil, nil); err == nil {
			t.Fatalf("unread/removed/non-state reference accepted: %s", refs)
		}
	}
	for _, revision := range []int{0, 3, 5} {
		raw, _ := json.Marshal(map[string]any{"actions": []map[string]any{{"kind": "report_memory", "source_refs": []string{"u1"}, "memory_revision": revision, "reply": "当前记忆如下。"}}})
		if _, err := parseValidatedWindowPlan(string(raw), turn, nil, nil); err == nil {
			t.Fatalf("foreign memory revision accepted: %d", revision)
		}
	}
	if _, err := parseValidatedWindowPlan(`{"actions":[{"kind":"report_memory","source_refs":["u1"],"memory_revision":4,"reply":"当前记忆尚未加载。"}]}`, turn, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClosedActionsCompletionBindsCurrentResultAndScene(t *testing.T) {
	turn := Turn{Loop: LoopTaskFinished, Source: SourceDigitalEmployee, IssueID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", SceneID: testSceneID("cid-current"), ConversationID: "cid-current", TaskResult: "查证结果：会生成两份。", TaskDeliveryContext: `{"status":"not_loaded"}`}
	raw := completionFinishJSON(turn, "已查证，会生成两份。")
	if d, err := parseValidatedWindowPlan(raw, turn, nil, nil); err != nil || d.Action != ActionReply || len(d.CoordinationActions) != 1 {
		t.Fatalf("current result not accepted: %#v err=%v", d, err)
	}
	variants := []Turn{turn, turn, turn}
	variants[0].TaskResult = "更新后的结论"
	variants[1].ConversationID = "cid-other"
	variants[2].TaskDeliveryContext = `{"status":"loaded"}`
	for _, other := range variants {
		if _, err := parseValidatedWindowPlan(raw, other, nil, nil); err == nil {
			t.Fatal("a stale result or different scene/delivery snapshot cannot share result_ref")
		}
	}
	for _, invalid := range []string{
		`{"actions":[{"kind":"start_work","reply":"继续查。","purpose":"查询新的产品机制细节","intent":"lookup"}]}`,
		`{"actions":[{"kind":"ignore","reason":"无需重复发送。"},{"kind":"ignore","reason":"第二次终结。"}]}`,
		`{"actions":[{"kind":"report_result","result_ref":"other","reply":"已完成。"}]}`,
	} {
		if _, err := parseValidatedWindowPlan(invalid, turn, nil, nil); err == nil {
			t.Fatalf("completion accepted invalid action: %s", invalid)
		}
	}
}

func TestClosedActionsKeepEachExecutorContextOnItsOwnWork(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, SenderName: "冬翔", Utterances: []WindowUtterance{{Sender: "冬翔", Text: "请查听记规则"}, {Sender: "須莫", Text: "帮我起草会议通知"}}}
	raw := `{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我来查证听记。","purpose":"查证主持人与参会人开启听记的生成规则","intent":"lookup","context":"只查官方产品说明。"},{"kind":"start_work","source_refs":["u2"],"reply":"我来起草通知。","purpose":"起草周五三点举行的会议通知正文","intent":"other","context":"只起草不发送。"}]}`
	d, err := parseValidatedWindowPlan(raw, turn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range d.Items {
		selected := d.ForWindowItem(item)
		body := IssueDescription(selected, item.Content)
		other := d.Items[1-i]
		if selected.UserText != item.Reply || len(selected.Items) != 1 || len(selected.CoordinationActions) != 0 || strings.Contains(body, other.Content) || strings.Contains(body, other.LookInto) {
			t.Fatalf("executor inherited another task's context: item=%d body=%s", i, body)
		}
		if !strings.Contains(body, item.Reply) || !strings.Contains(body, item.Content) {
			t.Fatalf("own reply or original input missing: %s", body)
		}
	}
}

func TestClosedActionsDeclineRequiresActualBoundary(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "把草稿直接发给所有人", Instructions: "Only draft. Never send until approved."}
	for _, tc := range []struct {
		quote, code string
		valid       bool
	}{
		{"Never send until approved.", "authorization", true},
		{"Users can never request any work.", "scope", false},
		{"", "authorization", false},
		{"Never send until approved.", "other", false},
	} {
		raw, _ := json.Marshal(map[string]any{"actions": []CoordinationAction{{Kind: "decline", SourceRefs: []string{"u1"}, Reply: "目前只授权起草，尚不能发送。", ReasonCode: tc.code, ConstraintQuote: tc.quote}}})
		_, err := parseValidatedWindowPlan(string(raw), turn, nil, nil)
		if (err == nil) != tc.valid {
			t.Fatalf("quote=%q code=%q valid=%t err=%v", tc.quote, tc.code, tc.valid, err)
		}
	}
}

func TestClosedActionsWebCannotFinishWithOnlyIgnore(t *testing.T) {
	raw := `{"actions":[{"kind":"ignore","source_refs":["u1"],"reason":"只是表情无需回应。"}]}`
	for _, source := range []Source{SourceWeb, SourceDigitalEmployee} {
		_, err := parseValidatedWindowPlan(raw, Turn{Source: source, Message: "👍"}, nil, nil)
		if (err != nil) != (source == SourceWeb) {
			t.Fatalf("source=%s error=%v", source, err)
		}
	}
}

func TestClosedActionsMergeSourcesWithoutMergingSpeakers(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Utterances: []WindowUtterance{{Sender: "冬翔", Text: "请写会议通知草稿"}, {Sender: "须莫", Text: "会议改到周五三点"}}}
	raw := `{"actions":[{"kind":"start_work","source_refs":["u2","u1"],"reply":"我按周五三点起草。","purpose":"起草周五下午三点举行会议的通知正文","intent":"other"}]}`
	d, err := parseValidatedWindowPlan(raw, turn, nil, nil)
	if err != nil || len(d.Items) != 1 || d.Items[0].Delegator != "冬翔" || strings.Join(d.Items[0].SourceRefs, ",") != "u1,u2" || !strings.Contains(d.Items[0].Content, "须莫 在钉钉会话中的消息") {
		t.Fatalf("actual ordered sources were not retained: %#v err=%v", d, err)
	}
}

func TestClosedActionsRejectedProposalKeepsExactJSONWithinItsOwnBudget(t *testing.T) {
	raw := `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"你好。"}]}`
	if got := boundedRejectedProposal(raw); got != raw {
		t.Fatalf("valid proposal changed: %q", got)
	}
	oversized := `{"actions":[{"reply":"` + strings.Repeat("界", coordinationProposalBudget) + `TAIL_MUST_NOT_LEAK"}]}`
	for _, input := range []string{oversized, `{"actions":[INVALID_JSON_TAIL`} {
		got := boundedRejectedProposal(input)
		if !strings.Contains(got, "omitted") || strings.Contains(got, "TAIL") || len([]rune(got)) > coordinationProposalBudget {
			t.Fatalf("invalid/oversized proposal was not explicitly omitted: %q", got)
		}
	}
	messages := buildCoordinationMessages(Turn{Source: SourceWeb, Message: "你好"}, false, "Repair the previous reply.", raw)
	body, _ := json.Marshal(messages)
	if !strings.Contains(string(body), "Repair the previous reply.") || !strings.Contains(string(body), "acknowledge") {
		t.Fatal("latest feedback and rejected proposal must both be visible")
	}
}
