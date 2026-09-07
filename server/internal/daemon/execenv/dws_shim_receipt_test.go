package execenv

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDWSMessagePolicyRecordsIntentBeforeSend(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, output, wantState string
	}{
		{"accepted", `{"success":true,"result":{"openTaskId":"provider-task"}}`, "accepted"},
		{"ambiguous envelope", `{"success":true}`, "unknown"},
		{"bare IDs are insufficient", `{"success":true,"result":{"openConversationId":"cid","openMessageId":"msg"}}`, "unknown"},
		{"delivered", `{"success":true,"result":{"sendStatus":"SUCCESS","openConversationId":"cid","openMessageId":"msg"}}`, "delivered"},
		{"rejected", `{"success":false,"errorCode":"INVALID_RECIPIENT"}`, "failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var order []string
			var receipts []protocol.DingTalkSendReceipt
			var sentArgs []string
			code := RunDWSWrap(DWSWrapDeps{
				Args:   []string{"chat", "message", "send", "--open-dingtalk-id", "recipient", "--content", "fixture", "--idempotency-key", "existing-key"},
				Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
				Getenv:   func(string) string { return `{"show_ai_tag":false,"platform_managed_lifecycle":true}` },
				LookPath: func(string) (string, error) { return "/unused-fixture", nil },
				Report: func(receipt protocol.DingTalkSendReceipt) error {
					order = append(order, receipt.State)
					receipts = append(receipts, receipt)
					return nil
				},
				Run: func(_ string, args []string, stdout, _ io.Writer) error {
					order = append(order, "send")
					sentArgs = args
					_, err := io.WriteString(stdout, tt.output)
					return err
				},
			})
			if code != 0 || !reflect.DeepEqual(order, []string{"pending", "send", tt.wantState}) {
				t.Fatalf("exit=%d, order=%v", code, order)
			}
			if len(receipts) != 2 || receipts[0].ClientActionID != receipts[1].ClientActionID || receipts[0].PayloadHash != receipts[1].PayloadHash {
				t.Fatalf("receipt identity changed: %#v", receipts)
			}
			if _, err := uuid.Parse(receipts[0].ClientActionID); err != nil || len(receipts[0].PayloadHash) != 64 {
				t.Fatalf("receipt identity invalid: %#v", receipts[0])
			}
			if receipts[0].RecipientOpenDingTalkID != "recipient" || receipts[0].IdempotencyKey != "existing-key" || !strings.Contains(strings.Join(sentArgs, " "), "--idempotency-key existing-key") {
				t.Fatalf("routing or idempotency changed: %#v, argv=%v", receipts[0], sentArgs)
			}
		})
	}
}

func TestDWSMessagePolicyReceiptFailureBoundaries(t *testing.T) {
	t.Parallel()
	for _, failAt := range []string{"pending", "accepted"} {
		t.Run(failAt, func(t *testing.T) {
			sent := 0
			var stderr bytes.Buffer
			code := RunDWSWrap(DWSWrapDeps{
				Args:   []string{"chat", "+dm", "--to", "fixture", "--content", "hello"},
				Stdout: &bytes.Buffer{}, Stderr: &stderr,
				Getenv:   func(string) string { return `{"platform_managed_lifecycle":true}` },
				LookPath: func(string) (string, error) { return "/unused-fixture", nil },
				Report: func(receipt protocol.DingTalkSendReceipt) error {
					if receipt.State == failAt {
						return errors.New("fixture transport failure")
					}
					return nil
				},
				Run: func(_ string, _ []string, stdout, _ io.Writer) error {
					sent++
					_, err := io.WriteString(stdout, `{"success":true,"result":{"openTaskId":"fixture-provider-task"}}`)
					return err
				},
			})
			if failAt == "pending" && (code == 0 || sent != 0) {
				t.Fatalf("unrecorded send executed: exit=%d, sent=%d", code, sent)
			}
			if failAt == "accepted" && (code != 0 || sent != 1 || !strings.Contains(stderr.String(), "do not resend")) {
				t.Fatalf("accepted send became retryable: exit=%d, sent=%d, stderr=%q", code, sent, stderr.String())
			}
		})
	}
}

func TestDWSMessagePolicyPayloadHashIsStableAndCredentialFree(t *testing.T) {
	t.Parallel()
	base, err := newDWSSendReceipt(parseDWSCommand([]string{"chat", "message", "send", "--content", "same body", "--token", "first-secret"}))
	if err != nil {
		t.Fatal(err)
	}
	shortcut, err := newDWSSendReceipt(parseDWSCommand([]string{"chat", "+messages-send", "--text", "same body", "--token", "second-secret", "--ai-tag"}))
	if err != nil {
		t.Fatal(err)
	}
	positional, err := newDWSSendReceipt(parseDWSCommand([]string{"chat", "message", "send", "--", "same body"}))
	if err != nil {
		t.Fatal(err)
	}
	if base.PayloadHash != shortcut.PayloadHash || base.PayloadHash != positional.PayloadHash {
		t.Fatalf("equivalent content has different hash: %s, %s, %s", base.PayloadHash, shortcut.PayloadHash, positional.PayloadHash)
	}
}

func TestDWSMessagePolicyReceiptHTTPUsesTaskIdentity(t *testing.T) {
	t.Parallel()
	taskID, workspaceID := uuid.NewString(), uuid.NewString()
	var got protocol.DingTalkSendReceipt
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/tasks/"+taskID+"/dingtalk-send-receipts" || r.Header.Get("Authorization") != "Bearer mat_fixture" || r.Header.Get("X-Workspace-ID") != workspaceID {
			t.Errorf("unexpected request identity: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = io.WriteString(w, `{"accepted":true}`)
	}))
	defer server.Close()
	env := map[string]string{"MULTICA_TOKEN": "mat_fixture", "MULTICA_TASK_ID": taskID, "MULTICA_WORKSPACE_ID": workspaceID, "MULTICA_SERVER_URL": server.URL}
	want := protocol.DingTalkSendReceipt{ClientActionID: uuid.NewString(), State: "pending", PayloadHash: strings.Repeat("a", 64)}
	if err := reportDWSSendReceipt(func(k string) string { return env[k] }, server.Client(), want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	env["MULTICA_TOKEN"] = "pat_owner"
	if err := reportDWSSendReceipt(func(k string) string { return env[k] }, server.Client(), want); err == nil {
		t.Fatal("owner credential fallback accepted")
	}
}

func TestDWSMessagePolicyReceiptHTTPRejectsRedirectAndMissingAck(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"redirect", "missing_ack"} {
		t.Run(state, func(t *testing.T) {
			redirected := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if state == "redirect" {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				} else {
					_, _ = io.WriteString(w, `{"success":true}`)
				}
			}))
			defer server.Close()
			env := map[string]string{"MULTICA_TOKEN": "mat_fixture", "MULTICA_TASK_ID": uuid.NewString(), "MULTICA_WORKSPACE_ID": uuid.NewString(), "MULTICA_SERVER_URL": server.URL}
			if err := reportDWSSendReceipt(func(k string) string { return env[k] }, server.Client(), protocol.DingTalkSendReceipt{}); err == nil || redirected {
				t.Fatalf("invalid acknowledgement accepted or token redirected: %v, %v", err, redirected)
			}
		})
	}
}
