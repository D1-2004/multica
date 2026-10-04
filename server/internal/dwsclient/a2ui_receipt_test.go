package dwsclient

import (
	"context"
	"testing"
)

func TestA2UIReceiptPreservesRealDeliveryIdentifiers(t *testing.T) {
	for _, fields := range []string{
		`"openMessageId":"m","openConversationId":"c","openTaskId":"t"`,
		`"messageId":"m","conversationId":"c","taskId":"t"`,
		`"msgId":"m","conversationId":"c","taskId":"t"`,
	} {
		raw := `{"ok":true,"result":{"success":true,"result":{"bizId":"business-card","cardInstanceId":42,` + fields + `}}}`
		receipt, err := parseA2UIReceipt([]byte(raw))
		if err != nil || receipt.MessageID != "m" || receipt.ConversationID != "c" || receipt.TaskID != "t" {
			t.Fatalf("receipt lost actual ids: %+v %v", receipt, err)
		}
	}
	receipt, err := parseA2UIReceipt([]byte(`{"ok":true,"result":{"success":true,"result":{"bizId":"business-card","cardInstanceId":42}}}`))
	if err != nil || receipt.TaskID != "" || receipt.MessageID != "" {
		t.Fatalf("card acknowledgement forged IM ids: %+v %v", receipt, err)
	}
}

func TestSDKA2UIReceiptRetainsOptionalDeliveryFields(t *testing.T) {
	_, cli, dir := startSDK(t, func(string, map[string]any) string {
		return toolOK(`{"bizId":"business-card","cardInstanceId":42,"openMessageId":"m","openConversationId":"c","openTaskId":"t"}`)
	})
	receipt, err := cli.SendA2UI(context.Background(), dir, A2UISendRequest{ConversationID: "c", BizID: "q", RequestID: "request", Summary: "question", Messages: []string{`{}`}})
	if err != nil || receipt.MessageID != "m" || receipt.ConversationID != "c" || receipt.TaskID != "t" {
		t.Fatalf("SDK projection lost actual receipt: %+v %v", receipt, err)
	}
}
