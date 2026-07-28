package sandboxrelay

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSignerVerifierRoundTrip(t *testing.T) {
	publicKey, privateKey := testKeyPair(t)
	signer, err := NewSigner("key-1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	signer.now = func() time.Time { return now }
	raw, err := signer.Mint(MintRequest{
		TaskID:             "task-1",
		AgentID:            "agent-1",
		RuntimeID:          "runtime-1",
		SandboxID:          "sandbox-1",
		DaemonToken:        "mdt_daemon-secret",
		AgentIdentityToken: "context-secret",
		ExpiresAt:          now.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	verifier, err := NewVerifier(map[string]ed25519.PublicKey{"key-1": publicKey})
	if err != nil {
		t.Fatal(err)
	}
	verifier.now = func() time.Time { return now.Add(time.Minute) }
	claims, err := verifier.Verify(raw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.TaskID != "task-1" || claims.AgentID != "agent-1" ||
		claims.RuntimeID != "runtime-1" || claims.SandboxID != "sandbox-1" {
		t.Fatalf("unexpected claims: %#v", claims)
	}
	if !claims.Allows(TargetMultica) || !claims.Allows(TargetAgentIdentity) {
		t.Fatalf("unexpected targets: %v", claims.Targets)
	}
	if claims.DaemonTokenSHA256 == "" || claims.AgentIdentityTokenSHA256 == "" {
		t.Fatal("token hashes were not bound into the assertion")
	}
}

func TestSignerOmitsAgentIdentityTargetWithoutContextToken(t *testing.T) {
	publicKey, privateKey := testKeyPair(t)
	signer, _ := NewSigner("key-1", privateKey)
	raw, err := signer.Mint(MintRequest{
		TaskID:      "task-1",
		AgentID:     "agent-1",
		RuntimeID:   "runtime-1",
		SandboxID:   "sandbox-1",
		DaemonToken: "mdt_daemon-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := NewVerifier(map[string]ed25519.PublicKey{"key-1": publicKey})
	claims, err := verifier.Verify(raw)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Allows(TargetAgentIdentity) || claims.AgentIdentityTokenSHA256 != "" {
		t.Fatalf("unexpected Agent Identity authorization: %#v", claims)
	}
}

func TestVerifierRejectsExpiredAndTamperedTokens(t *testing.T) {
	publicKey, privateKey := testKeyPair(t)
	signer, _ := NewSigner("key-1", privateKey)
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	signer.now = func() time.Time { return now }
	raw, err := signer.Mint(MintRequest{
		TaskID:      "task-1",
		AgentID:     "agent-1",
		RuntimeID:   "runtime-1",
		SandboxID:   "sandbox-1",
		DaemonToken: "mdt_daemon-secret",
		ExpiresAt:   now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := NewVerifier(map[string]ed25519.PublicKey{"key-1": publicKey})
	verifier.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := verifier.Verify(raw); err == nil {
		t.Fatal("expired token unexpectedly verified")
	}
	verifier.now = func() time.Time { return now }
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected compact token: %q", raw)
	}
	replacement := byte('A')
	if parts[1][len(parts[1])-1] == replacement {
		replacement = 'B'
	}
	parts[1] = parts[1][:len(parts[1])-1] + string(replacement)
	if _, err := verifier.Verify(strings.Join(parts, ".")); err == nil {
		t.Fatal("tampered token unexpectedly verified")
	}
}

func TestLoadSignerAndVerifierFromDEREnvironment(t *testing.T) {
	publicKey, privateKey := testKeyPair(t)
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(signingKeyIDEnv, "rotation-1")
	t.Setenv(signingPrivateEnv, base64.StdEncoding.EncodeToString(privateDER))
	encodedKeys, err := json.Marshal(map[string]string{
		"rotation-1": base64.StdEncoding.EncodeToString(publicDER),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(verifyKeysEnv, string(encodedKeys))

	signer, err := LoadSignerFromEnv()
	if err != nil {
		t.Fatalf("LoadSignerFromEnv: %v", err)
	}
	verifier, err := loadVerifierFromEnv()
	if err != nil {
		t.Fatalf("loadVerifierFromEnv: %v", err)
	}
	raw, err := signer.Mint(MintRequest{
		TaskID:      "task-1",
		AgentID:     "agent-1",
		RuntimeID:   "runtime-1",
		SandboxID:   "sandbox-1",
		DaemonToken: "mdt_daemon-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(raw); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestLoadSignerRejectsPartialConfiguration(t *testing.T) {
	t.Setenv(signingKeyIDEnv, "key-1")
	t.Setenv(signingPrivateEnv, "")
	if _, err := LoadSignerFromEnv(); err == nil {
		t.Fatal("partial signer configuration unexpectedly succeeded")
	}
}

func testKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return publicKey, privateKey
}
