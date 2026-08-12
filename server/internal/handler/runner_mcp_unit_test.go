package handler

import (
	"encoding/json"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRunnerPairingIDFromToken(t *testing.T) {
	const id = "29f6cd78-cfaf-4023-9fbd-c804a299a10d"
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef"
	pairingID, ok := runnerPairingIDFromToken("rps_" + id + "_" + secret)
	if !ok || uuidToString(pairingID) != id {
		t.Fatalf("pairing token id = %q, ok = %v", uuidToString(pairingID), ok)
	}
	for _, token := range []string{
		"rps_0123456789abcdef",
		"rps_not-a-uuid_" + secret,
		"rps_" + id + "_",
		"rps_" + id + "_z123456789abcdef0123456789abcdef0123456789abcdef",
		id + "_" + secret,
	} {
		if _, ok := runnerPairingIDFromToken(token); ok {
			t.Fatalf("invalid pairing token accepted: %q", token)
		}
	}
}

func TestRunnerCallTimeoutTracksForegroundShellTimeout(t *testing.T) {
	if got := runnerCallTimeout("read_file", []byte(`{}`)); got != time.Minute {
		t.Fatalf("read timeout = %s, want 1m", got)
	}
	if got := runnerCallTimeout("shell", []byte(`{"timeout_seconds":300}`)); got != 310*time.Second {
		t.Fatalf("shell timeout = %s, want 310s", got)
	}
	if got := runnerCallTimeout("shell", []byte(`{"background":true,"timeout_seconds":300}`)); got != time.Minute {
		t.Fatalf("background shell timeout = %s, want 1m", got)
	}
}

func TestRunnerMCPRejectsPiRuntimeWithoutManagedMCPCapability(t *testing.T) {
	runtime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "pi",
		Metadata: json.RawMessage(`{
			"kind":"fc-e2b",
			"provider":"pi",
			"template":"runner-test",
			"capabilities":[]
		}`),
	}
	if !runnerMCPRuntimeUnsupported(runtime) {
		t.Fatal("Pi runtime without mcp capability accepted Runner MCP")
	}
	runtime.Metadata = json.RawMessage(`{
		"kind":"fc-e2b",
		"provider":"pi",
		"template":"runner-test",
		"capabilities":["mcp"]
	}`)
	if runnerMCPRuntimeUnsupported(runtime) {
		t.Fatal("Pi runtime with mcp capability rejected Runner MCP")
	}
}

func TestValidateRunnerRootsRequiresAbsoluteUniquePaths(t *testing.T) {
	if err := validateRunnerRoots([]string{"/workspace", "/tmp/project"}); err != nil {
		t.Fatalf("valid roots rejected: %v", err)
	}
	for _, roots := range [][]string{{}, {"relative"}, {"/workspace", "/workspace"}} {
		if err := validateRunnerRoots(roots); err == nil {
			t.Fatalf("invalid roots accepted: %#v", roots)
		}
	}
}

func TestRunnerInstallCommandValuesAreShellQuoted(t *testing.T) {
	quoted := runnerShellQuote("https://example.test/path'with-quote")
	if quoted != `'https://example.test/path'"'"'with-quote'` {
		t.Fatalf("unexpected shell quoting %q", quoted)
	}
}

func TestRunnerBaseURLRequiresAnOrigin(t *testing.T) {
	for _, valid := range []string{"https://multica.example", "http://localhost:8080/"} {
		if _, err := runnerBaseURL(valid); err != nil {
			t.Fatalf("valid Runner origin %q rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"ftp://multica.example",
		"https://user:secret@multica.example",
		"https://multica.example/base",
		"https://multica.example?token=secret",
	} {
		if _, err := runnerBaseURL(invalid); err == nil {
			t.Fatalf("invalid Runner origin %q accepted", invalid)
		}
	}
}

func TestRunnerMCPForwardedToolsAlwaysRequireMachineID(t *testing.T) {
	for _, definition := range runnerMCPToolDefinitions() {
		tool := definition.(map[string]any)
		if tool["name"] == "list_machines" {
			continue
		}
		schema := tool["inputSchema"].(map[string]any)
		required := schema["required"].([]string)
		if len(required) == 0 || required[0] != "machine_id" {
			t.Fatalf("tool %q does not require machine_id first: %#v", tool["name"], required)
		}
	}
}
