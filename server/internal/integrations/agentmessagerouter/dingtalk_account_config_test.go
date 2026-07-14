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
		SchemaVersion:       1,
		DispatchEndpointID:  "v1_AAECAwQFBgcICQoLDA0ODw",
		DispatchKeyID:       "v1",
		DispatchURL:         "https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw",
		CallbackTokenHash:   strings.Repeat("a", 64),
		CallbackExpiresAt:  expiresAt,
		RouterSourceID:      "source-1",
		AccountDisplayName:  "Zhang San",
		AccountAvatarURL:    "https://example.com/avatar.png",
		BoundAt:             &boundAt,
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

	public := got.PublicBinding("installation-1", "workspace-1", "agent-1", "active")
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
		SchemaVersion:       1,
		DispatchEndpointID:  "v1_AAECAwQFBgcICQoLDA0ODw",
		DispatchKeyID:       "v1",
		DispatchURL:         "https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw",
		CallbackTokenHash:   strings.Repeat("a", 64),
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
		SchemaVersion:       1,
		DispatchEndpointID:  "v1_AAECAwQFBgcICQoLDA0ODw",
		DispatchKeyID:       "v1",
		DispatchURL:         "https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw",
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
