package handler

import (
	"strings"
	"testing"
)

func TestBuildEnterpriseIdentityAuthorizationURL(t *testing.T) {
	t.Parallel()

	got := buildEnterpriseIdentityAuthorizationURL(
		"https://multica-pre.example.com/",
		"yufa",
		"11111111-1111-1111-1111-111111111111",
	)
	want := "https://multica-pre.example.com/login?next=%2Fyufa%2Fagents%2F11111111-1111-1111-1111-111111111111%3Fenterprise_identity%3Dauthorize%26view%3Didentity"
	if got != want {
		t.Fatalf("authorization URL = %q, want %q", got, want)
	}
}

func TestBuildEnterpriseIdentityAuthorizationURLRejectsUnsafeBase(t *testing.T) {
	t.Parallel()

	for _, appURL := range []string{
		"",
		"/relative",
		"javascript:alert(1)",
		"https://user@example.com",
	} {
		if got := buildEnterpriseIdentityAuthorizationURL(appURL, "yufa", "agent-id"); got != "" {
			t.Fatalf("authorization URL for %q = %q, want empty", appURL, got)
		}
	}
}

func TestApplyEnterpriseIdentityAuthorizationInstructionPreservesExistingInstruction(t *testing.T) {
	t.Parallel()

	response := AgentTaskResponse{Instruction: "existing trusted instruction"}
	authorizationURL := "https://multica-pre.example.com/login?next=%2Fyufa%2Fagents%2Fagent-id%3Fenterprise_identity%3Dauthorize%26view%3Didentity"
	applyEnterpriseIdentityAuthorizationInstruction(&response, authorizationURL)

	for _, expected := range []string{
		"existing trusted instruction",
		"a1 command",
		"nw-aliwork-cli",
		"IdentityAuthFailed",
		"BUC SSO ticket expired",
		"集团账号权限助手",
		authorizationURL,
		"never ask the user for a password, ticket, token, or cookie",
	} {
		if !strings.Contains(response.Instruction, expected) {
			t.Fatalf("authorization instruction does not contain %q: %q", expected, response.Instruction)
		}
	}
}
