package agentmessagerouter

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDingTalkAccountConfigRoundTripAndPublicProjection(t *testing.T) {
	expiresAt := time.Date(2026, 7, 14, 10, 10, 0, 0, time.UTC)
	boundAt := time.Date(2026, 7, 14, 10, 0, 12, 0, time.UTC)
	want := DingTalkAccountConfig{
		SchemaVersion:      1,
		DispatchEndpointID: "v1_AAECAwQFBgcICQoLDA0ODw",
		DispatchKeyID:      "v1",
		DispatchURL:        "https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw",
		CallbackTokenHash:  strings.Repeat("a", 64),
		CallbackExpiresAt:  expiresAt,
		RouterSourceID:     "source-1",
		AccountDisplayName: "Zhang San",
		AccountAvatarURL:   "https://example.com/avatar.png",
		BoundAt:            &boundAt,
	}
	raw, err := want.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := ParseDingTalkAccountConfig(raw)
	if err != nil {
		t.Fatalf("ParseDingTalkAccountConfig: %v", err)
	}
	if got.DispatchEndpointID != want.DispatchEndpointID ||
		got.CallbackExpiresAt != expiresAt || got.RouterSourceID != want.RouterSourceID ||
		got.BoundAt == nil || *got.BoundAt != boundAt {
		t.Fatalf("round trip mismatch: %#v", got)
	}

	public := got.PublicBinding(
		"workspace-1",
		"agent-1",
		"active",
		PublicDingTalkBindingOutcome{Status: "unbound"},
	)
	if public.ID != "agent-1" {
		t.Fatalf("public binding id = %q, want agent id", public.ID)
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatalf("marshal public binding: %v", err)
	}
	for _, forbidden := range []string{
		"callback_token_hash", "callback_expires_at", "router_source_id",
		"dispatch_endpoint_id", "dispatch_key_id", "dispatch_url",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("public response leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestDingTalkAccountConfigPreservesConversationSnapshots(t *testing.T) {
	raw := []byte(`{
		"schema_version":1,
		"dispatch_endpoint_id":"v1_AAECAwQFBgcICQoLDA0ODw",
		"dispatch_key_id":"v1",
		"dispatch_url":"https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw",
		"router_source_id":"source-1",
		"bound_at":"2026-07-14T10:00:12Z",
		"message_scope":"custom",
		"conversations":[
			{"cid":"cid-alpha","name":"Project Alpha","avatar_media_id":"@media-alpha","avatar_url":"https://example.com/alpha.png"},
			{"cid":"cid-beta","name":"Project Beta"}
		]
	}`)

	config, err := ParseDingTalkAccountConfig(raw)
	if err != nil {
		t.Fatalf("ParseDingTalkAccountConfig: %v", err)
	}
	reencoded, err := config.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(reencoded, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["message_scope"] != "custom" {
		t.Fatalf("stored message scope = %#v", stored["message_scope"])
	}
	storedConversations, ok := stored["conversations"].([]any)
	if !ok || len(storedConversations) != 2 {
		t.Fatalf("stored conversations = %#v", stored["conversations"])
	}

	public := config.PublicBinding(
		"workspace-1",
		"agent-1",
		"active",
		PublicDingTalkBindingOutcome{Status: "unbound"},
	)
	publicJSON, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		MessageRoute struct {
			MessageScope  string           `json:"message_scope"`
			Conversations []map[string]any `json:"conversations"`
		} `json:"message_route"`
	}
	if err := json.Unmarshal(publicJSON, &response); err != nil {
		t.Fatal(err)
	}
	if response.MessageRoute.MessageScope != "custom" || len(response.MessageRoute.Conversations) != 2 {
		t.Fatalf("public message route = %#v", response.MessageRoute)
	}
}

func TestDingTalkAccountConfigDefaultsLegacyRowsToDirectOnly(t *testing.T) {
	raw := []byte(`{
		"schema_version":1,
		"dispatch_endpoint_id":"v1_AAECAwQFBgcICQoLDA0ODw",
		"dispatch_key_id":"v1",
		"dispatch_url":"https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw"
	}`)
	config, err := ParseDingTalkAccountConfig(raw)
	if err != nil {
		t.Fatalf("ParseDingTalkAccountConfig: %v", err)
	}
	publicJSON, err := json.Marshal(config.PublicBinding(
		"workspace-1",
		"agent-1",
		"active",
		PublicDingTalkBindingOutcome{Status: "unbound"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(publicJSON), `"message_scope":"direct_only"`) {
		t.Fatalf("legacy public binding did not default to direct_only: %s", publicJSON)
	}
}

func TestCallbackTokenUsesOnlyHashForVerification(t *testing.T) {
	raw, hash, err := GenerateCallbackToken(strings.NewReader(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatalf("GenerateCallbackToken: %v", err)
	}
	if raw == "" || hash == "" || raw == hash {
		t.Fatalf("raw/hash separation failed: raw=%q hash=%q", raw, hash)
	}
	if !VerifyCallbackToken(raw, hash) {
		t.Fatal("generated callback token did not verify")
	}
	if VerifyCallbackToken(raw+"x", hash) {
		t.Fatal("modified callback token unexpectedly verified")
	}
}

func TestCallbackTokenRejectsNonCanonicalRawValuesBeforeHashing(t *testing.T) {
	for _, raw := range []string{
		"",
		"short",
		strings.Repeat("a", 42),
		strings.Repeat("a", 44),
		strings.Repeat("+", 43),
		strings.Repeat("_", 43),
	} {
		if got := HashCallbackToken(raw); got != "" {
			t.Fatalf("HashCallbackToken(%q) = %q, want empty", raw, got)
		}
		if VerifyCallbackToken(raw, strings.Repeat("a", 64)) {
			t.Fatalf("VerifyCallbackToken(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestDingTalkAccountConfigRequiresCanonicalMatchingDispatchURL(t *testing.T) {
	base := DingTalkAccountConfig{
		SchemaVersion:      1,
		DispatchEndpointID: "v1_AAECAwQFBgcICQoLDA0ODw",
		DispatchKeyID:      "v1",
		DispatchURL:        "https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw",
		CallbackTokenHash:  strings.Repeat("a", 64),
		CallbackExpiresAt:  time.Date(2026, 7, 14, 10, 10, 0, 0, time.UTC),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}

	for _, dispatchURL := range []string{
		"https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODx",
		"https://MULTICA.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw",
		"https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw/",
		"https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw?token=secret",
		"http://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw",
	} {
		config := base
		config.DispatchURL = dispatchURL
		if err := config.Validate(); err == nil {
			t.Fatalf("expected dispatch URL %q to fail", dispatchURL)
		}
	}
}

func TestDingTalkAccountConfigRequiresCallbackHashAndExpiryTogether(t *testing.T) {
	config := DingTalkAccountConfig{
		SchemaVersion:      1,
		DispatchEndpointID: "v1_AAECAwQFBgcICQoLDA0ODw",
		DispatchKeyID:      "v1",
		DispatchURL:        "https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw",
		CallbackExpiresAt:  time.Date(2026, 7, 14, 10, 10, 0, 0, time.UTC),
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected callback expiry without hash to fail")
	}

	config.CallbackTokenHash = strings.Repeat("a", 64)
	config.CallbackExpiresAt = time.Time{}
	if err := config.Validate(); err == nil {
		t.Fatal("expected callback hash without expiry to fail")
	}
}
