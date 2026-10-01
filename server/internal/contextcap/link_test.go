package contextcap

import (
	"strings"
	"testing"
)

func TestLinkTokens(t *testing.T) {
	first, err := NewLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("link tokens repeat")
	}
	if len(first) != 43 || strings.ContainsAny(first, "+/=") || !ValidLinkTokenFormat(first) {
		t.Fatalf("token is not unpadded base64url of 32 bytes: %q", first)
	}
	hash := HashLinkToken(first)
	if len(hash) != 64 || hash != HashLinkToken(first) || hash == HashLinkToken(second) || strings.Contains(hash, first) {
		t.Fatalf("unexpected token hash %q", hash)
	}
	if got := HashLinkToken("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("hash is not hex sha256: %s", got)
	}
	for _, bad := range []string{"", first[:42], first + "A", strings.Repeat("*", 43)} {
		if ValidLinkTokenFormat(bad) {
			t.Errorf("malformed token accepted: %q", bad)
		}
	}
}
