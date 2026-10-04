package connectorconfig

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

func TestSealHidesTheSecret(t *testing.T) {
	box, err := secretbox.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	const secret = "github-client-secret-value"
	sealed, err := SealString(box, secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), secret) {
		t.Fatal("ciphertext contains the client secret")
	}
	opened, err := OpenString(box, sealed)
	if err != nil || opened != secret {
		t.Fatalf("open = %q, %v", opened, err)
	}
	if Hint(secret) != "••••alue" {
		t.Fatalf("hint = %q", Hint(secret))
	}
	token, err := SealToken(box, "ghp_exampletokenvalue")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(token), "ghp_exampletokenvalue") {
		t.Fatal("ciphertext contains the access token")
	}
	got, err := OpenToken(box, token)
	if err != nil || got.Bearer != "ghp_exampletokenvalue" {
		t.Fatalf("token = %+v, %v", got, err)
	}
	if _, err := SealString(box, " padded "); err == nil {
		t.Fatal("expected surrounding whitespace to be rejected")
	}
}
