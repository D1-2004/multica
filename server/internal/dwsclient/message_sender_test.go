package dwsclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMessageSenderRequiresExactSourceAndConversation(t *testing.T) {
	good := `{"success":true,"result":{"messages":[{"openMessageId":"msg-source","openConversationId":"cid-source","senderOpenDingTalkId":"sender-scoped"}]}}`
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"packaged CLI", good, true},
		{"wrong message", strings.ReplaceAll(good, "msg-source", "msg-other"), false},
		{"wrong conversation", strings.ReplaceAll(good, "cid-source", "cid-other"), false},
		{"no identity", strings.ReplaceAll(good, "sender-scoped", ""), false},
		{"rejected", strings.ReplaceAll(good, `"success":true`, `"success":false`), false},
		{"conflicting alias", strings.ReplaceAll(good, `"openMessageId":`, `"messageId":"msg-other","openMessageId":`), false},
		{"duplicate source", `{"success":true,"messages":[{"openMessageId":"msg-source"},{"openMessageId":"msg-source"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender, err := parseMessageSender([]byte(tc.raw), "cid-source", "msg-source")
			if tc.valid {
				if err != nil || sender != "sender-scoped" {
					t.Fatalf("sender=%q err=%v", sender, err)
				}
			} else if err == nil {
				t.Fatal("unverified source accepted")
			}
		})
	}
}

func TestSendResolvesMentionInActualSendingIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dws")
	log := filepath.Join(dir, "args")
	script := `#!/bin/sh
case "$*" in
 *list-by-ids*) echo '{"success":true,"result":{"messages":[{"openMessageId":"msg-source","openConversationId":"cid-source","senderOpenDingTalkId":"scoped-id"}]}}' ;;
 *) printf '%s\n' "$@" > '` + log + `'; echo '{"success":true,"result":{"openTaskId":"task-1"}}' ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := (CLI{Path: path}).Send(context.Background(), dir, SendRequest{ConversationID: "cid-source", SourceConversationID: "cid-source", SourceOpenMessageID: "msg-source", AtOpenDingTalkID: "ingress-id", Content: "<@ingress-id> **完成**", IdempotencyKey: "key"})
	if err != nil || result.OpenTaskID != "task-1" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	args, _ := os.ReadFile(log)
	if strings.Contains(string(args), "ingress-id") || !strings.Contains(string(args), "<@scoped-id> **完成**") || !strings.Contains(string(args), "--at-open-dingtalk-ids\nscoped-id") {
		t.Fatalf("wrong send arguments: %s", args)
	}
}
