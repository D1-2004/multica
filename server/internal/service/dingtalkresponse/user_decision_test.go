package dingtalkresponse

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/service/userdecision"
	"testing"
)

func TestDecisionCardUsesTrustedChannelTarget(t *testing.T) {
	for _, tc := range []struct {
		name, snapshot, initiator, cid, recipient string
		fail                                      bool
	}{
		{"direct", `{"turn":{"ChatType":"p2p"}}`, "trusted-actor", "", "trusted-actor", false},
		{"group", `{"turn":{"ChatType":"group"}}`, "trusted-actor", "source-cid", "", false},
		{"missing actor", `{"turn":{"ChatType":"p2p"}}`, "", "", "", true},
		{"missing channel", `{}`, "trusted-actor", "", "", true},
		{"unhydrated snapshot", `{"blob_key":"snapshot"}`, "trusted-actor", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := userdecision.Request{ID: "decision", ConversationID: "source-cid", InitiatorID: tc.initiator, CardID: "stable-card", SendRequestID: "stable-request", Snapshot: json.RawMessage(tc.snapshot), Proposal: userdecision.Proposal{Question: "choose"}}
			in, err := decisionCardRequest(r)
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v", err)
			}
			if tc.fail {
				return
			}
			if in.ConversationID != tc.cid || in.ReceiverOpenDingTalkID != tc.recipient || in.BizID != r.CardID || in.RequestID != r.SendRequestID {
				t.Fatalf("wrong target or changed send identity: %+v", in)
			}
		})
	}
}
