package handler

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/employeetask"
)

func TestEmployeeCompletionNoticePolicyRequiresSelectedSourceQuote(t *testing.T) {
	source := employeeSourceMessage{SourceRef: "receipt/message-1", Message: DispatchMessage{Text: "发文件，文件发出后不用再发总结。"}}
	for _, tc := range []struct {
		name, raw string
		ok        bool
		mode      employeetask.CompletionNoticeMode
	}{
		{"default", `{}`, true, employeetask.CompletionNoticeAlways},
		{"explicit-summary", `{"completion_notice_policy":{"mode":"always"}}`, true, employeetask.CompletionNoticeAlways},
		{"source-quote", `{"completion_notice_policy":{"mode":"if_not_delivered","require_delivery":"file","instruction_quote":"文件发出后不用再发总结"}}`, true, employeetask.CompletionNoticeIfNotDelivered},
		{"no-evidence", `{"completion_notice_policy":{"mode":"if_not_delivered"}}`, false, ""},
		{"other-source", `{"completion_notice_policy":{"mode":"if_not_delivered","require_delivery":"file","instruction_quote":"这是历史消息中的不总结要求"}}`, false, ""},
		{"forged-source-ref", `{"completion_notice_policy":{"mode":"if_not_delivered","require_delivery":"file","instruction_quote":"文件发出后不用再发总结","source_ref":"other/message"}}`, false, ""},
		{"unknown", `{"completion_notice_policy":{"mode":"silent"}}`, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &args); err != nil {
				t.Fatal(err)
			}
			policy, err := employeeCompletionNoticePolicy(args, source)
			if (err == nil) != tc.ok {
				t.Fatal(policy, err)
			}
			if tc.ok && (policy.Mode != tc.mode || (tc.mode == employeetask.CompletionNoticeIfNotDelivered && policy.SourceRef != source.SourceRef)) {
				t.Fatal(policy)
			}
		})
	}
}
