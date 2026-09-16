package inboundcoord

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkReceiptDiscardsOptionalMalformedAndFullArtifactReplies(t *testing.T) {
	const issue = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	for _, kind := range []string{"start_work", "continue_work"} {
		for _, reply := range []any{nil, "", strings.Repeat("交付物正文绝不能进入接单", 400), map[string]any{"draft": "交付物正文"}, []string{"交付物正文"}, float64(123)} {
			action := map[string]any{"kind": kind, "source_refs": []string{"u1"}, "purpose": "起草会议通知并保留为草稿", "reply": reply}
			if reply == nil {
				delete(action, "reply")
			}
			if kind == "continue_work" {
				action["issue_id"] = issue
				action["basis"] = "retry"
			}
			raw, _ := json.Marshal(map[string]any{"actions": []any{action}})
			turn := Turn{Source: SourceDigitalEmployee, Message: "请起草会议通知"}
			d, err := parseValidatedWindowPlan(string(raw), turn, nil, map[string]struct{}{issue: {}})
			if err != nil {
				t.Fatalf("valid %s blocked by discarded reply of type %T: %v", kind, reply, err)
			}
			want := hostWorkReceipt(kind, "zh")
			if d.Action != ActionIssue || d.UserText != want || len(d.Items) != 1 || d.Items[0].Reply != want || len(d.CoordinationActions) != 1 || d.CoordinationActions[0].Reply != want {
				t.Fatalf("work ACK was not Host-owned: %+v", d)
			}
			if d.Items[0].Purpose != "用户委托：起草会议通知并保留为草稿" && !strings.Contains(d.Items[0].Purpose, "起草会议通知并保留为草稿") {
				t.Fatalf("work purpose changed: %+v", d.Items[0])
			}
			if !strings.Contains(d.Items[0].Content, "请起草会议通知") || d.Items[0].ActionKey != "item-1" {
				t.Fatal("execution source or key was changed")
			}
		}
	}
}

func TestWorkReceiptLanguageIsOptionalAndClosed(t *testing.T) {
	for _, tc := range []struct {
		message  string
		language any
		want     string
	}{
		{"请起草通知", nil, "zh"}, {"Please draft a notice", nil, "en"}, {"案内を書いてください", nil, "ja"}, {"안내문을 작성해 주세요", nil, "ko"},
		{"请用英语起草通知", "en", "en"}, {"请起草通知", "not-a-language", "zh"}, {"请起草通知", map[string]string{"reply": "artifact"}, "zh"},
	} {
		a := map[string]any{"kind": "start_work", "source_refs": []string{"u1"}, "purpose": "Draft an internal meeting notice"}
		if tc.language != nil {
			a["receipt_language"] = tc.language
		}
		raw, _ := json.Marshal(map[string]any{"actions": []any{a}})
		d, err := parseValidatedWindowPlan(string(raw), Turn{Message: tc.message}, nil, nil)
		if err != nil || d.UserText != hostWorkReceipt("start_work", tc.want) {
			t.Fatalf("language presentation failed: got=%q want=%s err=%v", d.UserText, tc.want, err)
		}
	}
}

func TestWorkReceiptDoesNotRelaxExecutionValidation(t *testing.T) {
	for _, action := range []map[string]any{
		{"kind": "start_work", "source_refs": []string{"u1"}, "reply": map[string]any{}},
		{"kind": "start_work", "source_refs": []string{"u2"}, "purpose": "起草会议通知", "reply": map[string]any{}},
		{"kind": "continue_work", "source_refs": []string{"u1"}, "purpose": "继续起草会议通知", "issue_id": "not-recalled", "basis": "retry", "reply": map[string]any{}},
	} {
		raw, _ := json.Marshal(map[string]any{"actions": []any{action}})
		if _, err := parseValidatedWindowPlan(string(raw), Turn{Message: "请起草通知"}, nil, nil); err == nil {
			t.Fatalf("invalid execution plan accepted: %s", raw)
		}
	}
}

func TestWorkReceiptMixedPlanDeduplicatesOnlyWorkInOrder(t *testing.T) {
	raw := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"起草内部会议通知","reply":"不该先发的正文一"},{"kind":"acknowledge","ack_kind":"thanks","source_refs":["u2"],"reply":"不客气。"},{"kind":"start_work","source_refs":["u3"],"purpose":"查询本周会议安排","reply":"不该先发的正文二"},{"kind":"clarify","source_refs":["u4"],"missing_fields":["recipient"],"reply":"这条通知发给谁？"}]}`
	turn := Turn{Utterances: []WindowUtterance{{Text: "起草通知"}, {Text: "谢谢"}, {Text: "查询安排"}, {Text: "帮我发送通知"}}}
	d, err := parseValidatedWindowPlan(raw, turn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "收到，我来处理。\n\n不客气。\n\n这条通知发给谁？"; d.UserText != want {
		t.Fatalf("mixed replies lost, reordered or duplicated: %q", d.UserText)
	}
	if len(d.Items) != 2 || d.Items[0].ActionKey != "item-1" || d.Items[1].ActionKey != "item-2" {
		t.Fatal("work item identity changed")
	}
	// A social action can legitimately have the same words; do not deduplicate
	// it merely because a Host work receipt has that presentation text.
	actions := []CoordinationAction{{Kind: "acknowledge", Reply: "收到，我来处理。"}, {Kind: "start_work", Reply: "收到，我来处理。"}, {Kind: "start_work", Reply: "收到，我来处理。"}}
	if strings.Count(ComposeDecisionReplies(actions), "收到，我来处理。") != 2 {
		t.Fatal("deduplication swallowed a non-work reply")
	}
}

func TestWorkReceiptSchemaDoesNotRequireModelWorkText(t *testing.T) {
	tool := windowPlanTool(true)
	item := tool.OfFunction.Function.Parameters["properties"].(map[string]any)["actions"].(map[string]any)["items"].(map[string]any)
	for _, variant := range item["oneOf"].([]any) {
		v := variant.(map[string]any)
		kind := v["properties"].(map[string]any)["kind"].(map[string]any)["enum"].([]string)[0]
		required := v["required"].([]string)
		if workCoordinationKind(kind) && containsString(required, "reply") {
			t.Fatalf("%s still requires model receipt text", kind)
		}
		if kind == "acknowledge" && !containsString(required, "reply") {
			t.Fatal("social reply requirement lost")
		}
	}
}

func TestRestoredWorkReceiptNormalizationPreservesSnapshotAndCommittedLedger(t *testing.T) {
	saved := Decision{Action: ActionIssue, PlanVersion: WindowPlanVersion, UserText: "unsafe aggregate artifact", Items: []WindowItem{{ActionKey: "existing-a", Reply: "artifact-a", Purpose: "keep purpose a", Content: "keep source a"}, {ActionKey: "existing-b", Reply: "artifact-b", Purpose: "keep purpose b", Content: "keep source b"}}, CoordinationActions: []CoordinationAction{{Kind: "start_work", SourceRefs: []string{"u1"}, Reply: "artifact-a"}, {Kind: "acknowledge", AckKind: "thanks", SourceRefs: []string{"u2"}, Reply: "不客气。"}, {Kind: "start_work", SourceRefs: []string{"u3"}, Reply: "artifact-b"}}, CompletedActionKeys: []string{"existing-a"}, IssueResults: []protocol.ChatCoordinatorIssueResult{{Action: "issue_created", IssueID: "old-issue", TaskID: "old-task"}}}
	original, _ := json.Marshal(saved)
	saves := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), &saved, func(Decision) error { saves++; return nil })
	c := &Coordinator{}
	d := c.Decide(ctx, Turn{Source: SourceDigitalEmployee, Message: "继续本次请求"})
	if d.UserText != "收到，我来处理。\n\n不客气。" || strings.Contains(d.Items[0].Reply, "artifact") || strings.Contains(d.Items[1].Reply, "artifact") {
		t.Fatalf("restored unsafe receipt survived: %+v", d)
	}
	if !reflect.DeepEqual(d.CompletedActionKeys, saved.CompletedActionKeys) || !reflect.DeepEqual(d.IssueResults, saved.IssueResults) || d.Items[0].ActionKey != "existing-a" || d.Items[1].ActionKey != "existing-b" || d.Items[0].Content != "keep source a" || saves != 0 {
		t.Fatal("presentation normalization changed committed effects or replayed a save")
	}
	after, _ := json.Marshal(saved)
	if string(after) != string(original) {
		t.Fatal("restoring mutated the shared checkpoint pointer")
	}
	twice := d
	NormalizeWorkReceipts(Turn{Source: SourceDigitalEmployee, Message: "继续本次请求"}, &twice)
	if !reflect.DeepEqual(twice, d) {
		t.Fatal("receipt normalization was not idempotent")
	}
}

func TestLegacyOpaqueWorkPlanCannotResendItsAggregateArtifact(t *testing.T) {
	for _, withItems := range []bool{false, true} {
		d := Decision{Action: ActionIssue, PlanVersion: WindowPlanVersion, UserText: "unsafe opaque artifact and unknown mixed text", CompletedActionKeys: []string{"kept"}}
		if withItems {
			d.Items = []WindowItem{{ActionKey: "kept", Reply: "unsafe artifact", Content: "execution input", Basis: "retry", IssueID: "existing-issue"}}
		}
		NormalizeWorkReceipts(Turn{Source: SourceDigitalEmployee}, &d)
		if strings.Contains(d.UserText, "unsafe") || d.UserText == "" || !reflect.DeepEqual(d.CompletedActionKeys, []string{"kept"}) {
			t.Fatalf("legacy recovery unsafe: %+v", d)
		}
		if withItems && (d.Items[0].ActionKey != "kept" || d.Items[0].Content != "execution input") {
			t.Fatal("legacy work identity/source changed")
		}
	}
}
