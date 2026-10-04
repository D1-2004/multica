package dws

import (
	"context"
	"testing"
)

func TestA2UISendKeepsTheCardMessageID(t *testing.T) {
	_, url := startGateway(t, func(tool string, args map[string]any, _ string) (int, string) {
		switch tool {
		case "create_and_send_a2ui_card":
			return ok(`{"bizId":"transformer_card_1","cardInstanceId":42,"openMessageId":"msg-card-1","openConversationId":"cid-9"}`)
		default:
			t.Errorf("unexpected tool %s %v", tool, args)
			return 500, `{}`
		}
	})
	client, err := NewWithToken(context.Background(), Config{GatewayURL: url, SkipVerify: true}, Token{AccessToken: "tok-1"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Messages.SendA2UI(context.Background(), A2UISend{
		Target:    Target{ConversationID: "cid-1"},
		Messages:  []string{`{"version":"v1.0"}`},
		Summary:   "确认",
		BizCardID: "card-1",
		RequestID: "req-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.BizID != "transformer_card_1" || receipt.CardInstanceID != 42 || receipt.MessageID != "msg-card-1" || receipt.ConversationID != "cid-9" {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestA2UISendResolvesMessageIDFromTheTask(t *testing.T) {
	_, url := startGateway(t, func(tool string, args map[string]any, _ string) (int, string) {
		switch tool {
		case "create_and_send_a2ui_card":
			return ok(`{"bizId":"transformer_card_2","cardInstanceId":7,"openTaskId":"task-2"}`)
		case "query_message_send_status":
			if args["openTaskId"] != "task-2" {
				t.Errorf("task = %v", args["openTaskId"])
			}
			return ok(`{"openMessageId":"msg-from-task"}`)
		default:
			return 500, `{}`
		}
	})
	client, err := NewWithToken(context.Background(), Config{GatewayURL: url, SkipVerify: true}, Token{AccessToken: "tok-1"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Messages.SendA2UI(context.Background(), A2UISend{
		Target:   Target{UserOpenDingTalkID: "DAAAAAAAAAAAiE"},
		Messages: []string{`{"version":"v1.0"}`},
		Summary:  "确认",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.MessageID != "msg-from-task" || receipt.TaskID != "task-2" {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestA2UISendConfirmsWithoutAMessageID(t *testing.T) {
	_, url := startGateway(t, func(string, map[string]any, string) (int, string) {
		return ok(`{"bizId":"transformer_card_3","cardInstanceId":9}`)
	})
	client, err := NewWithToken(context.Background(), Config{GatewayURL: url, SkipVerify: true}, Token{AccessToken: "tok-1"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Messages.SendA2UI(context.Background(), A2UISend{
		Target:   Target{ConversationID: "cid-1"},
		Messages: []string{`{"version":"v1.0"}`},
		Summary:  "确认",
	})
	if err != nil || receipt.MessageID != "" || receipt.BizID != "transformer_card_3" {
		t.Fatalf("receipt = %+v %v", receipt, err)
	}
}

func TestA2UISendKeepsTheCardWhenStatusQueryFails(t *testing.T) {
	_, url := startGateway(t, func(tool string, _ map[string]any, _ string) (int, string) {
		switch tool {
		case "create_and_send_a2ui_card":
			return ok(`{"bizId":"transformer_card_4","cardInstanceId":11,"openTaskId":"task-4"}`)
		case "query_message_send_status":
			return 200, toolText(`{"success":false,"errorCode":"1001","errorMsg":"The task does not belong to this token","result":null}`)
		default:
			return 500, `{}`
		}
	})
	client, err := NewWithToken(context.Background(), Config{GatewayURL: url, SkipVerify: true}, Token{AccessToken: "tok-1"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Messages.SendA2UI(context.Background(), A2UISend{
		Target:   Target{ConversationID: "cid-1"},
		Messages: []string{`{"version":"v1.0"}`},
		Summary:  "确认",
	})
	if err != nil || receipt.BizID != "transformer_card_4" || receipt.CardInstanceID != 11 || receipt.MessageID != "" || receipt.TaskID != "task-4" {
		t.Fatalf("receipt = %+v %v", receipt, err)
	}
}

func TestA2UISendReadsANestedReceipt(t *testing.T) {
	_, url := startGateway(t, func(tool string, _ map[string]any, _ string) (int, string) {
		if tool != "create_and_send_a2ui_card" {
			t.Errorf("tool = %s", tool)
		}
		return ok(`{"result":{"bizId":"transformer_card_nested","cardInstanceId":5,"msgId":"msg-nested","openConversationId":"cid-nested"}}`)
	})
	client, err := NewWithToken(context.Background(), Config{GatewayURL: url, SkipVerify: true}, Token{AccessToken: "tok-1"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Messages.SendA2UI(context.Background(), A2UISend{
		Target:   Target{ConversationID: "cid-1"},
		Messages: []string{`{"version":"v1.0"}`},
		Summary:  "确认",
	})
	if err != nil || receipt.BizID != "transformer_card_nested" || receipt.MessageID != "msg-nested" || receipt.ConversationID != "cid-nested" {
		t.Fatalf("receipt = %+v %v", receipt, err)
	}
}

func TestReadA2UIReceiptRejectsAnUnconfirmedSend(t *testing.T) {
	if _, err := readA2UIReceipt([]byte(`{"bizId":"x"}`)); err == nil {
		t.Fatal("missing card instance was accepted")
	}
	if _, err := readA2UIReceipt([]byte(`{"cardInstanceId":1}`)); err == nil {
		t.Fatal("missing biz id was accepted")
	}
}
