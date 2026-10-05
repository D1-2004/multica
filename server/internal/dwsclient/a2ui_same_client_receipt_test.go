package dwsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSDKA2UIQueriesReceiptWithOriginalClient(t *testing.T) {
	f, cli, dir := startSDK(t, func(tool string, args map[string]any) string {
		switch tool {
		case "create_and_send_a2ui_card":
			return toolOK(`{"bizId":"real-biz","cardInstanceId":42,"openTaskId":"task"}`)
		case "query_message_send_status":
			if args["openTaskId"] != "task" {
				t.Errorf("query args = %v", args)
			}
			return toolOK(`{"sendStatus":"SUCCESS","openMessageId":"real-mid","openConversationId":"cid"}`)
		default:
			t.Errorf("unexpected tool %s", tool)
			return toolOK(`{}`)
		}
	})
	logs := captureReceiptLogs(t)
	receipt, err := cli.SendA2UI(context.Background(), dir, receiptTestRequest())
	if err != nil || receipt.BizID != "real-biz" || receipt.TaskID != "task" || receipt.MessageID != "real-mid" || receipt.ConversationID != "cid" {
		t.Fatalf("receipt = %+v, %v", receipt, err)
	}
	assertReceiptLog(t, logs.String(), "sdk", true)
	sends, queries := f.toolCalls("create_and_send_a2ui_card"), f.toolCalls("query_message_send_status")
	if len(sends) != 1 || len(queries) != 1 || len(f.tokens) != 1 || sends[0].Header.Get("x-user-access-token") != "uat-1" || queries[0].Header.Get("x-user-access-token") != "uat-1" {
		t.Fatal("receipt query must reuse original authenticated client without another exchange or send")
	}
}

func TestSDKA2UIReceiptQueryCannotUndoConfirmedSend(t *testing.T) {
	for _, tc := range []struct {
		name, created, status string
		wantQueries           int
	}{
		{"rejected-query", `{"bizId":"real-biz","cardInstanceId":42,"openTaskId":"task"}`, `{"success":false,"errorCode":"INTERNAL_ERROR"}`, 1},
		{"pending", `{"bizId":"real-biz","cardInstanceId":42,"openTaskId":"task"}`, toolOK(`{"sendStatus":"PENDING","openMessageId":"mid","openConversationId":"cid"}`), 6},
		{"wrong-conversation", `{"bizId":"real-biz","cardInstanceId":42,"openTaskId":"task"}`, toolOK(`{"sendStatus":"SUCCESS","openMessageId":"mid","openConversationId":"other-cid"}`), 1},
		{"no-task", `{"bizId":"real-biz","cardInstanceId":42}`, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cli, dir := startSDK(t, func(tool string, _ map[string]any) string {
				if tool == "create_and_send_a2ui_card" {
					return toolOK(tc.created)
				}
				return tc.status
			})
			receipt, err := cli.SendA2UI(context.Background(), dir, receiptTestRequest())
			if err != nil || receipt.BizID != "real-biz" || receipt.CardInstanceID != 42 || receipt.MessageID != "" || receipt.ConversationID != "" {
				t.Fatalf("confirmed receipt changed: %+v %v", receipt, err)
			}
			if len(f.toolCalls("create_and_send_a2ui_card")) != 1 || len(f.toolCalls("query_message_send_status")) != tc.wantQueries {
				t.Fatal("unexpected resend or query")
			}
		})
	}
}

func TestCLIA2UIDoesNotQueryTokenOwnedTaskInAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dws")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$DWS_CONFIG_DIR/calls"
printf '%s' '{"ok":true,"result":{"success":true,"result":{"bizId":"real-biz","cardInstanceId":42,"openTaskId":"task"}}}'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	logs := captureReceiptLogs(t)
	receipt, err := (CLI{Path: path}).SendA2UI(context.Background(), dir, receiptTestRequest())
	if err != nil || receipt.BizID != "real-biz" || receipt.TaskID != "task" || receipt.MessageID != "" {
		t.Fatalf("receipt %+v %v", receipt, err)
	}
	assertReceiptLog(t, logs.String(), "cli", false)
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(calls), "\n"); count != 1 {
		t.Fatalf("started %d CLI processes", count)
	}
}

func receiptTestRequest() A2UISendRequest {
	return A2UISendRequest{ConversationID: "cid", BizID: "caller-biz", RequestID: "request", Summary: "question", Messages: []string{`{}`}}
}

func captureReceiptLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	return &logs
}

func assertReceiptLog(t *testing.T, raw, transport string, hasMessage bool) {
	t.Helper()
	var log map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if entry["event"] == "dws_a2ui_receipt_identity" {
			log = entry
		}
	}
	if log["event"] != "dws_a2ui_receipt_identity" || log["transport"] != transport || log["request_id"] != "request" || log["has_biz_id"] != true || log["has_message_id"] != hasMessage || log["has_conversation_id"] != hasMessage || log["has_task_id"] != true {
		t.Fatalf("receipt diagnostic = %v", log)
	}
	for _, forbidden := range []string{"real-biz", "real-mid", "uat-1", "rt-1", "secret"} {
		if strings.Contains(raw, forbidden) {
			t.Fatal("receipt diagnostic exposed private identity or credential")
		}
	}
}
