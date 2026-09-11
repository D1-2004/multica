package dwsclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSendArgsPreserveContentAndExplicitPolicy(t *testing.T) {
	content := "line one\n--ai-tag=true `literal` $(literal) <@sender>"
	args, err := sendArgs(SendRequest{ConversationID: "cid", AtOpenDingTalkID: "sender", Content: content, IdempotencyKey: "stable", ShowAITag: false})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"chat", "message", "send", "--content", content, "--idempotency-key", "stable", "--ai-tag=false", "--format", "json", "--conversation-id", "cid", "--at-open-dingtalk-ids", "sender"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v", args)
	}
	args, err = sendArgs(SendRequest{RecipientOpenDingTalkID: "person", Content: content, IdempotencyKey: "stable", ShowAITag: true})
	if err != nil || !strings.Contains(strings.Join(args, "|"), "--ai-tag=true") {
		t.Fatalf("dm args %v, %v", args, err)
	}
	if _, err := sendArgs(SendRequest{ConversationID: "cid", RecipientOpenDingTalkID: "person", Content: content, IdempotencyKey: "stable"}); err == nil {
		t.Fatal("accepted ambiguous target")
	}
}

func TestSendArgsQuoteReplyUsesMessagesReply(t *testing.T) {
	content := "收到，我去查"
	args, err := sendArgs(SendRequest{
		ConversationID: "cid-origin", Content: content, IdempotencyKey: "stable",
		ShowAITag: false, ReplyToOpenMsgID: "msg-origin",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"chat", "+messages-reply", "--content", content, "--idempotency-key", "stable", "--ai-tag=false", "--format", "json", "--yes", "--group", "cid-origin", "--message-id", "msg-origin"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v", args)
	}
	if _, err := sendArgs(SendRequest{Content: content, IdempotencyKey: "stable", ReplyToOpenMsgID: "msg-origin"}); err == nil {
		t.Fatal("quoted reply without conversation")
	}
}

func TestSendStatusRequiresDeliveryEvidence(t *testing.T) {
	for _, tc := range []struct{ name, raw, state, message string }{
		{"accepted", `{"success":true,"result":{"openTaskId":"task"}}`, "provider_accepted", ""},
		{"pending_with_ids", `{"success":true,"sendStatus":"SENDING","openConversationId":"cid","openMessageId":"mid"}`, "provider_accepted", ""},
		{"success_without_ids", `{"success":true,"sendStatus":"SUCCESS"}`, "provider_accepted", ""},
		{"ids_without_status", `{"success":true,"openConversationId":"cid","openMessageId":"mid"}`, "provider_accepted", ""},
		{"unknown_numeric_status", `{"success":true,"sendStatus":1,"openConversationId":"cid","openMessageId":"mid"}`, "provider_accepted", ""},
		{"delivered", `{"success":true,"result":{"sendStatus":"SUCCESS","openConversationId":"cid","openMessageId":"mid"}}`, "delivered", "mid"},
		{"failed", `{"success":true,"result":{"sendStatus":"FAILED"}}`, "failed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSendStatus([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.state || got.OpenMessageID != tc.message {
				t.Fatalf("status = %+v", got)
			}
		})
	}
}

func TestSendResponseRejectsMissingSuccessAndMessageIDAsTask(t *testing.T) {
	for _, raw := range []string{`{}`, `{"openTaskId":"task"}`, `{"success":true,"openMessageId":"mid"}`, `{"success":true,"result":[]}`} {
		if _, err := ParseSendResult([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := ParseSendResult([]byte(`{"success":false,"errorCode":"denied"}`)); err == nil {
		t.Fatal("accepted failure")
	} else {
		var rejected *SendRejectedError
		if !errors.As(err, &rejected) || rejected.Code != "denied" {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestSendUsesIsolatedConfigAndNoShell(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-dws")
	script := "#!/usr/bin/env python3\nimport json,os,sys\nassert os.environ['DWS_CONFIG_DIR']==" + string(mustJSON(t, dir)) + "\nassert 'DWS_AUTH_CODE' not in os.environ\nassert sys.argv[1:4]==['chat','message','send']\nassert sys.argv[sys.argv.index('--content')+1]=='literal $(bad) `bad`\\n--ai-tag=true'\nprint(json.dumps({'success':True,'result':{'openTaskId':'task'}}))\n"
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DWS_AUTH_CODE", "secret-ambient")
	got, err := (CLI{Path: exe}).Send(context.Background(), dir, SendRequest{RecipientOpenDingTalkID: "person", Content: "literal $(bad) `bad`\n--ai-tag=true", IdempotencyKey: "key"})
	if err != nil || got.OpenTaskID != "task" {
		t.Fatalf("send=%+v err=%v", got, err)
	}
}

func TestMessageCommandsPreserveStructuredFailureDiagnostics(t *testing.T) {
	payload := `{"error":{"category":"api","reason":"business_error","server_error_code":1001,"trace_id":"trace-123","message":"token=secret-do-not-log","actions":["send secret-do-not-log"]}}`
	for _, tc := range []struct{ name, stdout, stderr string }{
		{"stderr", "", payload},
		{"startup_notice_then_stderr", "", "CLI startup notice: secret-do-not-log\n" + payload},
		{"stdout", payload, ""},
		{"stderr_over_success_stdout", `{"success":true,"result":{"openTaskId":"unconfirmed"}}`, payload},
	} {
		for _, operation := range []string{"send", "status"} {
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				dir := t.TempDir()
				bin := filepath.Join(dir, "dws")
				script := "#!/bin/sh\ncat <<'DWS_STDOUT'\n" + tc.stdout + "\nDWS_STDOUT\ncat >&2 <<'DWS_STDERR'\n" + tc.stderr + "\nDWS_STDERR\nexit 1\n"
				if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				cli := CLI{Path: bin}
				var err error
				if operation == "send" {
					_, err = cli.Send(context.Background(), dir, SendRequest{RecipientOpenDingTalkID: "person", Content: "hello", IdempotencyKey: "key"})
				} else {
					_, err = cli.QuerySendStatus(context.Background(), dir, "task")
				}
				var detail *MessageOperationError
				if !errors.As(err, &detail) {
					t.Fatalf("lost structured diagnostic: %v", err)
				}
				want := map[string]any{"category": "api", "reason": "business_error", "server_error_code": "1001", "trace_id": "trace-123"}
				if !reflect.DeepEqual(detail.DiagnosticFields(), want) {
					t.Fatalf("diagnostics = %#v", detail.DiagnosticFields())
				}
				if !strings.Contains(err.Error(), "DWS message operation failed; server_error_code=1001") || strings.Contains(err.Error(), "secret-do-not-log") {
					t.Fatalf("unsafe or incomplete error: %v", err)
				}
			})
		}
	}
}

func TestMessageFailureNeverExposesUnstructuredStderr(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "dws")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'Authorization: Bearer secret-do-not-log' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := (CLI{Path: bin}).Send(context.Background(), dir, SendRequest{RecipientOpenDingTalkID: "person", Content: "hello", IdempotencyKey: "key"})
	if err == nil || strings.Contains(err.Error(), "secret-do-not-log") {
		t.Fatalf("error = %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
