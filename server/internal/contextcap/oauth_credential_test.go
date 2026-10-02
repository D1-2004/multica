package contextcap

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func oauthTestBinding() CredentialBinding {
	return CredentialBinding{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		AgentID:     "22222222-2222-4222-8222-222222222222",
		ConnectorID: "33333333-3333-4333-8333-333333333333",
		ScopeType:   ScopeScene,
		OrgID:       "org-1",
		ScopeKey:    "aaaaaaaa-0000-4000-8000-000000000005",
	}
}

func TestSealOAuthCredentialRoundTripAndLegacyBearer(t *testing.T) {
	box := testBox(t)
	binding := oauthTestBinding()
	token := OAuthToken{AccessToken: "ghu_access_value", RefreshToken: "ghr_refresh_value", ExpiresAt: 1_900_000_000, TokenType: "bearer", Account: "octocat"}
	sealed, err := SealOAuthCredential(box, binding, token)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"ghu_access_value", "ghr_refresh_value", "octocat"} {
		if bytes.Contains(sealed, []byte(secret)) {
			t.Fatalf("ciphertext contains %q", secret)
		}
	}
	secret, err := OpenCredentialSecret(box, binding, sealed)
	if err != nil || secret.Kind() != CredentialKindOAuth || secret.Bearer != token.AccessToken || *secret.OAuth != token {
		t.Fatalf("OpenCredentialSecret = %+v %v", secret, err)
	}
	// Older readers only know the Bearer field: it must be the access token.
	if bearer, err := OpenCredential(box, binding, sealed); err != nil || bearer != token.AccessToken {
		t.Fatalf("OpenCredential = %q %v", bearer, err)
	}
	plain, _ := box.Open(sealed)
	var legacy struct {
		Bearer string `json:"bearer"`
	}
	if json.Unmarshal(plain, &legacy) != nil || legacy.Bearer != token.AccessToken {
		t.Fatalf("legacy payload bearer = %q", legacy.Bearer)
	}
	// The binding is still enforced.
	other := binding
	other.ScopeKey = "aaaaaaaa-0000-4000-8000-000000000006"
	if _, err := OpenCredentialSecret(box, other, sealed); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("foreign scope opened: %v", err)
	}
	// A plain Bearer credential has no OAuth part.
	plainSealed, err := SealCredential(box, binding, "pat-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	if secret, err := OpenCredentialSecret(box, binding, plainSealed); err != nil || secret.OAuth != nil || secret.Kind() != CredentialKindBearer {
		t.Fatalf("bearer secret = %+v %v", secret, err)
	}
}

func TestOpenCredentialSecretRejectsInconsistentOAuth(t *testing.T) {
	box := testBox(t)
	binding := oauthTestBinding()
	normalized, _ := binding.normalized()
	payload, _ := json.Marshal(sealedCredential{
		WorkspaceID: normalized.WorkspaceID, AgentID: normalized.AgentID, ConnectorID: normalized.ConnectorID,
		ScopeType: normalized.ScopeType, OrgID: normalized.OrgID, ScopeKey: normalized.ScopeKey,
		Bearer: "one", OAuth: &OAuthToken{AccessToken: "two"},
	})
	sealed, _ := box.Seal(payload)
	if _, err := OpenCredentialSecret(box, binding, sealed); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("mismatched access token accepted: %v", err)
	}
	if _, err := SealOAuthCredential(box, binding, OAuthToken{AccessToken: "ok-token", RefreshToken: "bad\nrefresh"}); err == nil {
		t.Fatal("header-unsafe refresh token sealed")
	}
}

func TestOAuthHintsNeverLeakTokens(t *testing.T) {
	if got := OAuthHint("octocat"); got != "@octocat" {
		t.Fatalf("OAuthHint = %q", got)
	}
	if got := OAuthHint(""); got != "OAuth" {
		t.Fatalf("empty account hint = %q", got)
	}
	if got := OAuthHint("evil‮account"); got != "OAuth" {
		t.Fatalf("format character account hint = %q", got)
	}
	for hint, kind := range map[string]string{"@octocat": CredentialKindOAuth, "OAuth": CredentialKindOAuth, Hint("pat-value-1234"): CredentialKindBearer, "••••": CredentialKindBearer} {
		if got := CredentialKindFromHint(hint); got != kind {
			t.Errorf("CredentialKindFromHint(%q) = %q, want %q", hint, got, kind)
		}
	}
}

func TestOAuthTokenExpiry(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	token := OAuthToken{AccessToken: "a", ExpiresAt: now.Add(30 * time.Second).Unix()}
	if !token.ExpiresWithin(now, time.Minute) || token.ExpiresWithin(now, 10*time.Second) {
		t.Fatal("ExpiresWithin boundary")
	}
	if (OAuthToken{AccessToken: "a"}).ExpiresWithin(now, time.Hour) {
		t.Fatal("token without expiry expired")
	}
	expired := Secret{Bearer: "a", OAuth: &OAuthToken{AccessToken: "a", ExpiresAt: now.Add(-time.Second).Unix()}}
	if expired.Usable(now) {
		t.Fatal("expired, non-refreshable token usable")
	}
	expired.OAuth.RefreshToken = "r"
	if !expired.Usable(now) {
		t.Fatal("refreshable token unusable")
	}
}
