package dwsclient

import (
	"strings"
	"testing"
)

func TestSafeCode(t *testing.T) {
	if SafeCode("AUTH") != "AUTH" {
		t.Fatal("passthrough")
	}
	if SafeCode("bad code!") != "operation_failed" {
		t.Fatal("reject punctuation")
	}
}

func TestCommandEnvIsolatesSecrets(t *testing.T) {
	t.Setenv("DWS_CLIENT_SECRET", "should-not-leak")
	t.Setenv("DWS_AUTH_CODE", "should-not-leak")
	env := CommandEnv("/tmp/dws-test", map[string]string{"DWS_CLIENT_ID": "abc"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "DWS_CONFIG_DIR=/tmp/dws-test") {
		t.Fatal("config dir missing")
	}
	if !strings.Contains(joined, "DWS_CLIENT_ID=abc") {
		t.Fatal("explicit client id missing")
	}
	if strings.Contains(joined, "should-not-leak") {
		t.Fatal("blocked secret leaked into command env")
	}
}
