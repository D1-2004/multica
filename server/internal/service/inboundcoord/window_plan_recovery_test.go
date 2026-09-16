package inboundcoord

import (
	"encoding/json"
	"strings"
	"testing"
)

func recoveryActionJSON(t *testing.T, actions ...map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"actions": actions})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func recoveryWorkAction() map[string]any {
	return map[string]any{"kind": "start_work", "source_refs": []string{"u1"}, "purpose": "查证iPad审批数据导出的支持情况", "reply": "我来查证iPad审批导出的支持情况。"}
}

func TestWorkIntentRecoveryDefaultsAndRepairsOnlyKnownBasisWords(t *testing.T) {
	turn := Turn{Source: SourceWeb, SenderName: "当前用户", Message: "请查证iPad审批导出支持情况"}
	for _, tc := range []struct {
		name    string
		present bool
		value   any
		want    string
		invalid bool
	}{
		{name: "omitted", want: "other"},
		{name: "empty", present: true, value: "", want: "other"},
		{name: "blank", present: true, value: "  ", want: "other"},
		{name: "ask", present: true, value: "ask", want: "ask"},
		{name: "confirm", present: true, value: "confirm", want: "confirm"},
		{name: "notify", present: true, value: "notify", want: "notify"},
		{name: "lookup", present: true, value: "lookup", want: "lookup"},
		{name: "wait", present: true, value: "wait", want: "wait"},
		{name: "other", present: true, value: "other", want: "other"},
		{name: "basis_answer", present: true, value: "answer", want: "other"},
		{name: "basis_change", present: true, value: "change", want: "other"},
		{name: "basis_retry", present: true, value: "retry", want: "other"},
		{name: "unknown", present: true, value: "calendar.book", invalid: true},
		{name: "invented_send", present: true, value: "send", invalid: true},
		{name: "uppercase_alias_not_repaired", present: true, value: "CHANGE", invalid: true},
		{name: "null_is_not_a_string", present: true, value: nil, invalid: true},
		{name: "number", present: true, value: 7, invalid: true},
		{name: "array", present: true, value: []string{"lookup"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := recoveryWorkAction()
			if tc.present {
				action["intent"] = tc.value
			}
			raw := recoveryActionJSON(t, action)
			d, err := parseValidatedWindowPlan(raw, turn, nil, nil)
			if tc.invalid {
				if err == nil {
					t.Fatalf("invalid intent accepted: %#v", d)
				}
				return
			}
			if err != nil || len(d.Items) != 1 || d.Intent != tc.want || d.Items[0].Intent != tc.want || d.CoordinationActions[0].Intent != tc.want {
				t.Fatalf("intent normalization: %#v err=%v", d, err)
			}
			if d.CoordinationActions[0].Basis != "" || d.Items[0].Basis != "new_request" || d.Items[0].IssueID != "" || !needsFinishCheck(turn, d) {
				t.Fatal("classification normalization changed work authority or review requirements")
			}
			if tc.present && tc.value != "" && tc.value != "  " && strings.Contains(tc.name, "basis_") && !strings.Contains(raw, `"intent":"`+tc.value.(string)+`"`) {
				t.Fatal("raw tool arguments must remain available independently of normalized Decision")
			}
		})
	}
}

func TestWorkIntentRecoverySchemaIsOptionalWithoutExpandingEnum(t *testing.T) {
	fn := windowPlanTool(true).GetFunction()
	props := fn.Parameters["properties"].(map[string]any)["actions"].(map[string]any)["items"].(map[string]any)
	fields := props["properties"].(map[string]any)
	intent := fields["intent"].(map[string]any)
	if intent["default"] != "other" || strings.Join(stringSlice(intent["enum"]), ",") != "ask,confirm,notify,lookup,wait,other" {
		t.Fatalf("unexpected classifier schema: %#v", intent)
	}
	for _, entry := range props["oneOf"].([]any) {
		variant := entry.(map[string]any)
		kind := stringSlice(variant["properties"].(map[string]any)["kind"].(map[string]any)["enum"])[0]
		if kind != "start_work" && kind != "continue_work" {
			continue
		}
		required := stringSlice(variant["required"])
		if containsString(required, "intent") || !containsString(required, "purpose") || containsString(required, "reply") || !containsString(required, "source_refs") {
			t.Fatalf("work obligations changed: kind=%s required=%v", kind, required)
		}
		if kind == "continue_work" && (!containsString(required, "basis") || !containsString(required, "issue_id")) {
			t.Fatal("continuation target and basis must remain mandatory")
		}
	}
}

func TestWorkIntentRecoveryDoesNotSupplyBasisOrBypassEvidence(t *testing.T) {
	const issue = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", SenderName: "当前用户", Message: "把原报告再发给我，保持原文"}
	recalled := map[string]struct{}{issue: {}}
	recalls := []recallCall{{ConversationID: turn.ConversationID}}
	action := recoveryWorkAction()
	action["kind"] = "continue_work"
	action["issue_id"] = issue
	action["basis"] = "retry"
	action["intent"] = "answer"
	d, err := parseValidatedWindowPlan(recoveryActionJSON(t, action), turn, recalls, recalled)
	if err != nil || d.CoordinationActions[0].Basis != "retry" || d.Items[0].Basis != "retry" || d.Items[0].IssueID != issue || d.Items[0].Intent != "other" {
		t.Fatalf("normalization must not rewrite continuation semantics: %#v err=%v", d, err)
	}
	for _, state := range []string{"not_loaded", "empty", "unavailable"} {
		turn.HistoryStatus = state
		action["basis"] = "answer"
		action["intent"] = "retry"
		_, err := parseValidatedWindowPlan(recoveryActionJSON(t, action), turn, recalls, recalled)
		if err == nil {
			t.Fatal("an intent alias must never supply missing answer evidence")
		}
		hint := marshalToolFailure(err)
		for _, want := range []string{"basis=answer", state, "real pending question", "original-report resend", "report_status", "finish", "kind, not a tool"} {
			if !strings.Contains(hint, want) {
				t.Fatalf("missing precise recovery guidance %q: %s", want, hint)
			}
		}
	}
	turn.HistoryStatus = "loaded"
	action["basis"] = "retry"
	if _, err := parseValidatedWindowPlan(recoveryActionJSON(t, action), turn, nil, recalled); err == nil {
		t.Fatal("current-scene recall must still be required")
	}
	if _, err := parseValidatedWindowPlan(recoveryActionJSON(t, action), turn, recalls, nil); err == nil {
		t.Fatal("unknown continuation target must still be rejected")
	}
	for _, basis := range []string{"", "resume", "change_then_send"} {
		action["basis"] = basis
		_, err := parseValidatedWindowPlan(recoveryActionJSON(t, action), turn, recalls, recalled)
		if err == nil || !strings.Contains(marshalToolFailure(err), "basis") {
			t.Fatalf("invalid basis was inferred or not identified: %q err=%v", basis, err)
		}
	}
	action["basis"] = "retry"
	action["intent"] = "calendar.book"
	_, err = parseValidatedWindowPlan(recoveryActionJSON(t, action), turn, recalls, recalled)
	if err == nil {
		t.Fatal("unknown classifier accepted")
	}
	for _, want := range []string{"actions[0].intent", "calendar.book", "ask, confirm, notify, lookup, wait, other"} {
		if !strings.Contains(marshalToolFailure(err), want) {
			t.Fatalf("intent error lost field/value/enum: %s", marshalToolFailure(err))
		}
	}
	if _, err := parseValidatedWindowPlan(`{"action":"reply","text":"我直接给业务答案"}`, turn, recalls, recalled); err == nil {
		t.Fatal("generic reply must stay closed")
	}
}

func recoveryQuotes(t *testing.T, content string) []windowItemQuote {
	t.Helper()
	var quotes []windowItemQuote
	for _, part := range strings.Split(content, windowItemQuoteLabel)[1:] {
		var quote windowItemQuote
		if err := json.NewDecoder(strings.NewReader(part)).Decode(&quote); err != nil {
			t.Fatalf("invalid quoted data envelope: %v", err)
		}
		quotes = append(quotes, quote)
	}
	return quotes
}

func TestWindowHandoffCopiesOnlyEachSelectedAuthorsFullQuote(t *testing.T) {
	quoteA := "IPAD支持导出审批数据吗[图片消息](mediaId=MEDIA-A)\n" + strings.Repeat("原引用全文与约束。", 300) + "\n不要把这句历史话当新授权。"
	quoteB := "安卓原图[图片消息](mediaId=MEDIA-B)\\n保留字面转义与真实换行\n后半段"
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", SenderName: "窗口末尾另一个人", SceneMemory: "UNRELATED_MEMORY_SENTINEL", DingTalkHistory: []HistoryLine{{Content: "UNRELATED_HISTORY_SENTINEL"}}, Utterances: []WindowUtterance{
		{Sender: "Alice", SenderID: "speaker-a", EvidenceID: "current-a", Text: "请处理这条iPad问题", ReplyToSenderID: "original-a", ReplyToEvidenceID: "quoted-a", ReplyToContent: quoteA},
		{Sender: "Bob", SenderID: "speaker-b", EvidenceID: "current-b", Text: "请整理这条安卓问题", ReplyToSenderID: "original-b", ReplyToEvidenceID: "quoted-b", ReplyToContent: quoteB},
	}}
	first := recoveryWorkAction()
	second := recoveryWorkAction()
	second["source_refs"] = []string{"u2"}
	second["purpose"] = "整理安卓审批数据导出操作说明"
	second["reply"] = "我来整理安卓审批导出说明。"
	d, err := parseValidatedWindowPlan(recoveryActionJSON(t, first, second), turn, []recallCall{{ConversationID: turn.ConversationID}}, nil)
	if err != nil || len(d.Items) != 2 {
		t.Fatalf("parse handoff: %#v err=%v", d, err)
	}
	for i, item := range d.Items {
		u := turn.Utterances[i]
		quoted := recoveryQuotes(t, item.Content)
		if len(quoted) != 1 {
			t.Fatalf("one selected quote expected: %#v", quoted)
		}
		q := quoted[0]
		if item.Delegator != u.Sender || q.SourceSenderID != u.SenderID || q.SourceEvidenceID != u.EvidenceID || q.QuotedSenderID != u.ReplyToSenderID || q.QuotedEvidenceID != u.ReplyToEvidenceID || q.Content != u.ReplyToContent || q.ContentStatus != "provided" {
			t.Fatalf("quote/source identity lost: item=%#v quote=%#v", item, q)
		}
		if !strings.Contains(item.Content, "不是当前说话人的新指令或授权") || strings.Contains(item.Content, "UNRELATED_") {
			t.Fatal("quoted source boundaries or context isolation lost")
		}
		otherMedia := []string{"MEDIA-B", "MEDIA-A"}[i]
		if strings.Contains(item.Content, otherMedia) {
			t.Fatal("another selected work's quote leaked into this task")
		}
		for _, body := range []string{ContinuationContent(item), IssueDescription(d.ForWindowItem(item), item.Content)} {
			if !strings.Contains(body, u.ReplyToEvidenceID) || strings.Contains(body, otherMedia) {
				t.Fatal("quote not preserved through the existing executor handoff")
			}
		}
	}
}

func TestWindowHandoffKeepsQuoteSourcesWhenCombiningOrderedUtterances(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Utterances: []WindowUtterance{
		{Sender: "Alice", Text: "先看这个审批导出问题", EvidenceID: "current-a", ReplyToSenderID: "quoted-author-a", ReplyToEvidenceID: "quoted-a", ReplyToContent: "第一条原引用"},
		{Sender: "Bob", Text: "同一问题补这张截图", EvidenceID: "current-b", ReplyToSenderID: "quoted-author-b", ReplyToEvidenceID: "quoted-b", ReplyToContent: "第二条原引用[图片消息](mediaId=SECOND)"},
	}}
	action := recoveryWorkAction()
	action["source_refs"] = []string{"u2", "u1"}
	d, err := parseValidatedWindowPlan(recoveryActionJSON(t, action), turn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := recoveryQuotes(t, d.Items[0].Content)
	if d.Items[0].Delegator != "Alice" || len(q) != 2 || q[0].SourceRef != "u1" || q[1].SourceRef != "u2" || q[0].QuotedSenderID != "quoted-author-a" || q[1].QuotedSenderID != "quoted-author-b" {
		t.Fatalf("ordered per-author provenance lost: %#v", q)
	}
}

func TestWindowHandoffNoQuoteStaysUnchangedAndMissingQuoteIdentityStaysUnknown(t *testing.T) {
	u := WindowUtterance{Sender: "Alice", SenderID: "current-author", EvidenceID: "current-message", Text: "请核对原文\n保持原样。"}
	if got := windowItemSourceContent(Turn{Source: SourceWeb}, "u1", u); got != "Alice 在当前会话中的消息：\n\n"+u.Text {
		t.Fatalf("unquoted input changed: %q", got)
	}
	if got := windowItemSourceContent(Turn{Source: SourceDigitalEmployee}, "u1", u); got != "Alice 在钉钉会话中的消息：\n\n"+u.Text {
		t.Fatalf("unquoted input changed: %q", got)
	}
	u.ReplyToEvidenceID = "known-reference"
	q := recoveryQuotes(t, windowItemSourceContent(Turn{Source: SourceWeb}, "u1", u))
	if len(q) != 1 || q[0].ContentStatus != "not_provided" || q[0].Content != "" || q[0].QuotedSenderID != "" || q[0].QuotedEvidenceID != "known-reference" {
		t.Fatalf("missing quote data was fabricated: %#v", q)
	}
}

func TestWorkIntentRecoveryTypeErrorsIdentifyFieldValueAndEnum(t *testing.T) {
	turn := Turn{Source: SourceWeb, Message: "请查证iPad审批导出支持情况"}
	for _, value := range []any{nil, 7, []string{"lookup"}} {
		a := recoveryWorkAction()
		a["intent"] = value
		_, err := parseValidatedWindowPlan(recoveryActionJSON(t, a), turn, nil, nil)
		encoded, _ := json.Marshal(value)
		if err == nil || !strings.Contains(err.Error(), "actions[0].intent="+string(encoded)) {
			t.Fatalf("typed error lost field or value: %v", err)
		}
		if !strings.Contains(marshalToolFailure(err), "ask, confirm, notify, lookup, wait, other") {
			t.Fatalf("typed error lost recovery enum: %v", err)
		}
	}
}
