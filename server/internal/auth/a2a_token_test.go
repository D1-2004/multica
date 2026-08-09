package auth

import (
	"regexp"
	"testing"
)

func TestGenerateA2AToken(t *testing.T) {
	first, err := GenerateA2AToken()
	if err != nil {
		t.Fatalf("GenerateA2AToken: %v", err)
	}
	second, err := GenerateA2AToken()
	if err != nil {
		t.Fatalf("GenerateA2AToken second call: %v", err)
	}
	if !regexp.MustCompile(`^mca2a_[0-9a-f]{40}$`).MatchString(first) {
		t.Fatalf("token %q does not match the A2A credential format", first)
	}
	if first == second {
		t.Fatal("generated A2A tokens must be unique")
	}
	if got := HashToken(first); got == first || len(got) != 64 {
		t.Fatal("HashToken must return a non-plaintext SHA-256 hex digest")
	}
}
