package inboundcoord

import "testing"

func TestTaskFinishedResultAlreadyDelivered(t *testing.T) {
	t.Parallel()
	const result = "已确认周五三点线上开会。"
	const cid = "cid-current"
	base := TaskDeliveryContext{Status: "loaded", TaskID: "current-run", Scope: "current_task", Deliveries: []TaskDeliveryEvidence{{
		ConversationID: cid, MessageID: "real-message", SentText: result, TextComplete: true,
	}}}
	if !TaskFinishedResultAlreadyDelivered(result, cid, base) {
		t.Fatal("the same result with this run's successful receipt should not be repeated")
	}
	for _, tc := range []struct{ name, result, cid string }{
		{"new result", "已确认周五改为四点。", cid},
		{"different recipient", result, "cid-other"},
		{"empty result", "", cid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if TaskFinishedResultAlreadyDelivered(tc.result, tc.cid, base) {
				t.Fatal("receipt does not establish this result reached this recipient")
			}
		})
	}
	for _, status := range []string{"unavailable", "not_loaded"} {
		unknown := base
		unknown.Status = status
		if TaskFinishedResultAlreadyDelivered(result, cid, unknown) {
			t.Fatal("unknown evidence must not suppress a new result")
		}
	}
	base.Deliveries[0].TextComplete = false
	if TaskFinishedResultAlreadyDelivered(result, cid, base) {
		t.Fatal("a clipped prefix cannot establish exact result delivery")
	}
}

func TestFilterTaskFinishedWrapupPreservesUsefulPhrases(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"已确认周五三点线上开会。",
		"我已问 dxxh，等他回；你不用再联系了。",
		"请查收银行发来的验证码，再继续付款。",
		"机票没查到，需要航班号或航司。",
	} {
		got := FilterTaskFinishedWrapup(Decision{Action: ActionReply, UserText: text})
		if got.Action != ActionReply || got.UserText != text {
			t.Fatalf("phrases cannot replace delivery evidence: %#v", got)
		}
	}
	empty := FilterTaskFinishedWrapup(Decision{Action: ActionReply, UserText: "  "})
	if empty.Action != ActionSilence || empty.UserText != "" || empty.Reason != "empty_wrapup" {
		t.Fatalf("empty: %#v", empty)
	}
}
