package fdebootstrap

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestGenerateTokenAndHash(t *testing.T) {
	a, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, TokenPrefix) || a == b {
		t.Fatalf("tokens are not distinct prefixed values: %q / %q", a, b)
	}
	if len(HashToken(a)) != 32 || bytes.Equal(HashToken(a), HashToken(b)) {
		t.Fatal("token hashes must be distinct SHA-256 values")
	}
}

func TestIdentityHMACAndAssertionRoundTrip(t *testing.T) {
	identity, err := IdentityHMAC(" union-123 ")
	if err != nil {
		t.Fatal(err)
	}
	same, _ := IdentityHMAC("union-123")
	other, _ := IdentityHMAC("union-456")
	if !IdentityMatches(identity, same) || IdentityMatches(identity, other) {
		t.Fatal("identity matching does not preserve exact normalized subject")
	}

	now := time.Unix(1_800_000_000, 0)
	raw, err := SignIdentityAssertion("user-1", identity, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseIdentityAssertion(raw, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "user-1" || !IdentityMatches(got.IdentityHMAC, identity) {
		t.Fatalf("unexpected assertion: %#v", got)
	}
	if _, err := ParseIdentityAssertion(raw, now.Add(AssertionTTL+time.Second)); err == nil {
		t.Fatal("expired assertion accepted")
	}
	if _, err := ParseIdentityAssertion(raw+"tampered", now); err == nil {
		t.Fatal("tampered assertion accepted")
	}
}

func TestIdentityHMACRejectsEmpty(t *testing.T) {
	if _, err := IdentityHMAC("  "); err == nil {
		t.Fatal("empty identity accepted")
	}
}
