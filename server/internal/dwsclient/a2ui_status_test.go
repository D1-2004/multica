package dwsclient

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSDKA2UIReceiptEventuallyVisibleWithoutResend(t *testing.T) {
	var queries atomic.Int32
	f, cli, dir := startSDK(t, func(tool string, _ map[string]any) string {
		if tool == "create_and_send_a2ui_card" {
			return toolOK(`{"bizId":"real-biz","cardInstanceId":42,"openTaskId":"task"}`)
		}
		switch queries.Add(1) {
		case 1:
			return `{"success":false,"errorCode":"PARAM_ERROR","errorMsg":"消息不存在"}`
		case 2:
			return toolOK(`{"sendStatus":"PENDING"}`)
		default:
			return toolOK(`{"status":"SUCCESS","messageId":"mid-only"}`)
		}
	})
	logs := captureReceiptLogs(t)
	in := receiptTestRequest()
	in.ConversationID, in.ReceiverOpenDingTalkID = "", "DiiD01p03EN0M1jO14tDSsZD3LG5GGiiPRd"
	receipt, err := cli.SendA2UI(context.Background(), dir, in)
	if err != nil || receipt.MessageID != "mid-only" || receipt.ConversationID != "" || queries.Load() != 3 {
		t.Fatalf("receipt=%+v queries=%d err=%v", receipt, queries.Load(), err)
	}
	if len(f.toolCalls("create_and_send_a2ui_card")) != 1 || len(f.tokens) != 1 {
		t.Fatal("receipt lookup re-created the card or exchanged another credential")
	}
	if !strings.Contains(logs.String(), `"outcome":"identity"`) || strings.Contains(logs.String(), "mid-only") {
		t.Fatal("missing safe lookup diagnostic or leaked identity")
	}
}

func TestA2UIStatusIdentityDoesNotInventDelivery(t *testing.T) {
	for _, tc := range []struct{ raw, message, conversation, outcome string }{
		{`{"success":true,"result":{"status":"SUCCESS","messageId":"m","conversationId":"c"}}`, "m", "c", "identity"},
		{`{"success":true,"result":{"openMessageId":"m"}}`, "m", "", "identity_unconfirmed"},
		{`{"success":true,"messageRef":{"openMessageId":"m","openConversationId":"c"}}`, "m", "c", "identity_unconfirmed"},
		{`{"success":true,"sendStatus":"PENDING","openMessageId":"m"}`, "", "", "pending"},
		{`{"success":true,"sendStatus":"FAILED","openMessageId":"m"}`, "", "", "failed"},
		{`{"success":true,"sendStatus":1,"openMessageId":"m"}`, "", "", "schema_unrecognized"},
		{`{"success":true,"status":"UNKNOWN","openMessageId":"m"}`, "", "", "schema_unrecognized"},
		{`{"success":true,"openMessageId":"m","messageRef":{"openMessageId":"different"}}`, "", "", "schema_unrecognized"},
		{`{"success":false,"openMessageId":"m"}`, "", "", "schema_unrecognized"},
	} {
		m, c, outcome := a2uiStatusIdentity([]byte(tc.raw))
		if m != tc.message || c != tc.conversation || outcome != tc.outcome {
			t.Fatalf("%s: %s/%s/%s", tc.raw, m, c, outcome)
		}
	}
}
