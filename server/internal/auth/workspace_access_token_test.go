package auth

import (
	"strings"
	"testing"
)

func TestGenerateWorkspaceAccessToken(t *testing.T) {
	first, err := GenerateWorkspaceAccessToken()
	if err != nil {
		t.Fatalf("GenerateWorkspaceAccessToken: %v", err)
	}
	second, err := GenerateWorkspaceAccessToken()
	if err != nil {
		t.Fatalf("GenerateWorkspaceAccessToken second call: %v", err)
	}
	if !strings.HasPrefix(first, "dta_") {
		t.Fatalf("token prefix = %q, want dta_", first)
	}
	if len(first) != 44 {
		t.Fatalf("token length = %d, want 44", len(first))
	}
	if first == second {
		t.Fatal("generated workspace access tokens must be unique")
	}
	if HashToken(first) == first || len(HashToken(first)) != 64 {
		t.Fatal("HashToken must return a non-plaintext SHA-256 hex digest")
	}
}
