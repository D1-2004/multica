package dwsclient

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestA2UIReceiptRequiresExplicitSuccess(t *testing.T) {
	good := `{"ok":true,"result":{"success":true,"result":{"bizId":"card","cardInstanceId":42}}}`
	r, e := parseA2UIReceipt([]byte(good))
	if e != nil || r.BizID != "card" {
		t.Fatal(r, e)
	}
	for _, raw := range []string{`{}`, `{"ok":true}`, `{"ok":false,"result":{"success":true,"result":{"bizId":"card","cardInstanceId":42}}}`, `{"ok":true,"result":{"success":false}}`, `{"ok":true,"result":{"success":true,"result":{"bizId":"card"}}}`} {
		if _, e := parseA2UIReceipt([]byte(raw)); e == nil {
			t.Fatalf("unconfirmed send accepted: %s", raw)
		}
	}
}

func TestSendA2UIAuthorizesNoninteractiveCardCreation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dws")
	script := `#!/bin/sh
for arg in "$@"; do
 if [ "$arg" = "--a2ui-annotations" ]; then exit 2; fi
done
for arg in "$@"; do
 if [ "$arg" = "--yes" ]; then
  printf '%s' '{"ok":true,"result":{"success":true,"result":{"bizId":"card","cardInstanceId":42}}}'
  exit 0
 fi
done
printf '%s' '{"error":{"code":3,"category":"validation","reason":"confirmation_required"}}'
exit 3
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	receipt, err := (CLI{Path: path}).SendA2UI(context.Background(), dir, A2UISendRequest{ConversationID: "cid", BizID: "card", RequestID: "request", Summary: "问题", Messages: []string{"{}"}})
	if err != nil || receipt.BizID != "card" {
		t.Fatalf("noninteractive card creation failed: %v, %+v", err, receipt)
	}
}

func TestA2UIUpdatesPreserveArtifactAnnotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dws")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$DWS_CONFIG_DIR/args"
printf '%s' '{"success":true}'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	annotations := []A2UIAnnotation{{SurfaceID: "decision", ComponentID: "status", Type: "artifact"}}
	if err := (CLI{Path: path}).UpdateA2UI(context.Background(), dir, "card", "CONFIRMING", []string{"{}"}, annotations); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(raw), "\n")
	for i, arg := range args {
		if arg == "--a2ui-annotations" && i+1 < len(args) {
			var actual []A2UIAnnotation
			if err := json.Unmarshal([]byte(args[i+1]), &actual); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, annotations) {
				t.Fatalf("annotation changed: %+v", actual)
			}
			return
		}
	}
	t.Fatal("update would clear artifact annotations")
}

func TestDecisionOrganizationUsesSubscriptionNotConversationOwnership(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dws")
	// Any conversation-info lookup would reintroduce a local channel restriction.
	script := `#!/bin/sh
case "$*" in
 'profile list --format json') printf '%s' '{"success":true,"currentProfile":"active","profiles":[{"profile":"other","corpId":"group-owner"},{"profile":"active","corpId":"sender-org"}]}' ;;
 *) exit 2 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	corp, err := (CLI{Path: path}).DecisionOrganization(context.Background(), dir)
	if err != nil || corp != "sender-org" {
		t.Fatalf("subscription organization: %q %v", corp, err)
	}
}

func TestSendA2UITargetArguments(t *testing.T) {
	for _, tc := range []struct {
		name, cid, recipient, flag, target string
		fail                               bool
	}{
		{"direct", "", "trusted-actor", "--open-dingtalk-id", "trusted-actor", false},
		{"group", "source-cid", "", "--chat-id", "source-cid", false},
		{"ambiguous", "source-cid", "trusted-actor", "", "", true},
		{"missing", "", "", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "dws")
			script := `#!/bin/sh
printf '%s\n' "$@" > "$DWS_CONFIG_DIR/args"
printf '%s' '{"ok":true,"result":{"success":true,"result":{"bizId":"card","cardInstanceId":42}}}'
`
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			_, err := (CLI{Path: path}).SendA2UI(context.Background(), dir, A2UISendRequest{ConversationID: tc.cid, ReceiverOpenDingTalkID: tc.recipient, BizID: "card", RequestID: "request", Summary: "question", Messages: []string{"{}"}})
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v", err)
			}
			raw, readErr := os.ReadFile(filepath.Join(dir, "args"))
			if tc.fail {
				if !os.IsNotExist(readErr) {
					t.Fatal("invalid target invoked DWS")
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			args := strings.Split(string(raw), "\n")
			targets := 0
			for i, arg := range args {
				if arg == "--chat-id" || arg == "--open-dingtalk-id" {
					targets++
					if arg != tc.flag || args[i+1] != tc.target {
						t.Fatalf("wrong target: %v", args)
					}
				}
			}
			if targets != 1 {
				t.Fatalf("target count=%d", targets)
			}
		})
	}
}

func TestCardSendRejectionPreservesSafeProviderMessage(t *testing.T) {
	e := messageCLIError([]byte(`{"error":{"category":"api","reason":"business_error","server_error_code":"A2UI_TARGET_INVALID","trace_id":"trace123","message":"A2UI card target group does not belong to the creator organization"}}`))
	msg, ok := e.CardSendRejection()
	if !ok || !strings.Contains(msg, "A2UI_TARGET_INVALID") || !strings.Contains(msg, "does not belong") || !strings.Contains(msg, "trace123") {
		t.Fatal(msg, ok)
	}
	if strings.Contains(e.Error(), "does not belong") {
		t.Fatal("provider message leaked into generic log error")
	}
	for _, code := range []string{"INTERNAL_ERROR", "A2UI_DELIVER_FAILED", "A2UI_WAVE_FAILED"} {
		raw, _ := json.Marshal(map[string]any{"error": map[string]any{"category": "api", "reason": "business_error", "server_error_code": code, "message": "uncertain"}})
		if _, ok := messageCLIError(raw).CardSendRejection(); ok {
			t.Fatalf("ambiguous %s treated as rejected", code)
		}
	}
	e = messageCLIError([]byte(`{"error":{"category":"api","reason":"business_error","server_error_code":"A2UI_TARGET_INVALID","message":"denied token=private-credential"}}`))
	msg, _ = e.CardSendRejection()
	if strings.Contains(msg, "private-credential") {
		t.Fatal("credential leaked")
	}
}
