package dwsclient

import "testing"

// The usual credential keeps the key shared tokens already live under; a
// credential issued another way (a version) never shares it.
func TestCredentialVersionSeparatesSharedTokens(t *testing.T) {
	id := Identity{AgentID: "agent-1", UID: "406516560", OrgID: "439446171"}
	const mcp = "https://mcp.dingtalk.com"
	usual := Shared{}.identityKey(mcp, id)
	if usual != mcp+"\x00agent-1\x00406516560\x00439446171" {
		t.Fatalf("usual key = %q", usual)
	}
	versioned := id
	versioned.CredentialVersion = "deap-1"
	key := Shared{}.identityKey(mcp, versioned)
	if key == usual {
		t.Fatal("a versioned credential shares the usual key")
	}
	versioned.CredentialVersion = " deap-1 "
	if again := (Shared{}).identityKey(mcp, versioned); again != key {
		t.Fatalf("version is not trimmed: %q", again)
	}
}
