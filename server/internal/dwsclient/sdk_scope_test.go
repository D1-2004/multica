package dwsclient

import "testing"

// The usual scope keeps the key shared tokens already live under; another
// scope never shares an identity's credentials with it.
func TestCredentialScopeSeparatesSharedTokens(t *testing.T) {
	id := Identity{AgentID: "agent-1", UID: "406516560", OrgID: "439446171"}
	const mcp = "https://mcp.dingtalk.com"
	usual := Shared{CLI: CLI{}}.identityKey(mcp, id)
	if usual != mcp+"\x00agent-1\x00406516560\x00439446171" {
		t.Fatalf("usual key = %q", usual)
	}
	scoped := Shared{CLI: CLI{CredentialScope: "native-subscription"}}.identityKey(mcp, id)
	if scoped == usual {
		t.Fatal("a scoped credential shares the usual key")
	}
	if again := (Shared{CLI: CLI{CredentialScope: " native-subscription "}}).identityKey(mcp, id); again != scoped {
		t.Fatalf("scope is not trimmed: %q", again)
	}
	// A new credential version is minted afresh, never served the old token.
	versioned := id
	versioned.CredentialVersion = "deap:1"
	if key := (Shared{CLI: CLI{CredentialScope: "native-subscription"}}).identityKey(mcp, versioned); key == scoped || key == usual {
		t.Fatalf("versioned key = %q", key)
	}
}
