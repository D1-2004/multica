package dingtalk

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSanitizeDingTalkAgentSourcePayloadPreservesMessageDataAndRemovesCredentials(t *testing.T) {
	plain := []byte(`{
		"msgId":"m1",
		"msgtype":"futureType",
		"sessionWebhook":"https://api.example.test/session/secret-reply-key",
		"sessionWebhookExpiredTime":1784600000000,
		"text":{"content":"分析 sessionWebhook 这个字段，并保留消息里的 https://example.test/article"},
		"content":{
			"fileName":"invoice.pdf",
			"downloadCode":"secret-download-code",
			"signedDownloadURL":"https://files.example.test/secret-signed-url",
			"cardContent":[{"elementType":"LINK","value":"https://example.test/article"}],
			"future":{"clientSecret":"future-secret","tokenCount":42,"arbitraryField":{"enabled":true}}
		},
		"headers":{"Authorization":"Bearer secret-access-token","X-Trace-ID":"trace-1"},
		"unknownTopLevel":{"items":[{"label":"keep me"}]}
	}`)

	got, err := sanitizeDingTalkAgentSourcePayload(plain)
	if err != nil {
		t.Fatalf("sanitizeDingTalkAgentSourcePayload: %v", err)
	}
	for _, secret := range []string{
		"secret-reply-key",
		"secret-download-code",
		"secret-signed-url",
		"future-secret",
		"secret-access-token",
	} {
		if strings.Contains(string(got), secret) {
			t.Errorf("sanitized payload contains credential %q: %s", secret, got)
		}
	}

	var envelope dingtalkAgentSourcePayload
	if err := json.Unmarshal(got, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.SchemaVersion != 1 || envelope.Platform != "dingtalk" {
		t.Fatalf("envelope identity = version %d platform %q", envelope.SchemaVersion, envelope.Platform)
	}
	wantRedacted := []string{
		"$.content.downloadCode",
		"$.content.future.clientSecret",
		"$.content.signedDownloadURL",
		"$.headers.Authorization",
		"$.sessionWebhook",
	}
	if !reflect.DeepEqual(envelope.RedactedFields, wantRedacted) {
		t.Fatalf("redacted fields = %#v, want %#v", envelope.RedactedFields, wantRedacted)
	}

	payload := envelope.Payload.(map[string]any)
	if payload["msgtype"] != "futureType" || payload["sessionWebhookExpiredTime"] != float64(1784600000000) {
		t.Fatalf("top-level message metadata was not preserved: %#v", payload)
	}
	if payload["text"].(map[string]any)["content"] != "分析 sessionWebhook 这个字段，并保留消息里的 https://example.test/article" {
		t.Fatalf("user-authored text was altered: %#v", payload["text"])
	}
	content := payload["content"].(map[string]any)
	if content["fileName"] != "invoice.pdf" {
		t.Fatalf("safe file metadata missing: %#v", content)
	}
	future := content["future"].(map[string]any)
	if future["tokenCount"] != float64(42) || future["arbitraryField"].(map[string]any)["enabled"] != true {
		t.Fatalf("unknown future fields were not preserved: %#v", future)
	}
	if payload["headers"].(map[string]any)["X-Trace-ID"] != "trace-1" {
		t.Fatalf("non-credential header was not preserved: %#v", payload["headers"])
	}
}

func TestSanitizeDingTalkAgentSourcePayloadRejectsNonObjectOrTrailingJSON(t *testing.T) {
	for _, input := range []string{`[]`, `{"msgId":"m1"} {"msgId":"m2"}`} {
		if _, err := sanitizeDingTalkAgentSourcePayload([]byte(input)); err == nil {
			t.Fatalf("sanitize accepted invalid callback %q", input)
		}
	}
}
