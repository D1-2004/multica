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
	if err := (CLI{Path: path}).UpdateA2UI(context.Background(), dir, "card", "FINISH", []string{"{}"}, annotations); err != nil {
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
