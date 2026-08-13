package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
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

func TestRunnerReconnectIDFromToken(t *testing.T) {
	const id = "29f6cd78-cfaf-4023-9fbd-c804a299a10d"
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef"
	sessionID, ok := runnerReconnectIDFromToken("rrs_" + id + "_" + secret)
	if !ok || uuidToString(sessionID) != id {
		t.Fatalf("reconnect token id = %q, ok = %v", uuidToString(sessionID), ok)
	}
	for _, token := range []string{
		"rrs_0123456789abcdef",
		"rrs_not-a-uuid_" + secret,
		"rrs_" + id + "_",
		"rrs_" + id + "_z123456789abcdef0123456789abcdef0123456789abcdef",
		id + "_" + secret,
	} {
		if _, ok := runnerReconnectIDFromToken(token); ok {
			t.Fatalf("invalid reconnect token accepted: %q", token)
		}
	}
}

func TestRunnerBindingOnlineRequiresConnectedRecentSocket(t *testing.T) {
	now := time.Now()
	connected := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	recent := pgtype.Timestamptz{Time: now.Add(-runnerOnlineTTL / 2), Valid: true}
	stale := pgtype.Timestamptz{Time: now.Add(-runnerOnlineTTL - time.Second), Valid: true}
	disconnected := pgtype.Timestamptz{Time: now, Valid: true}

	if !runnerBindingOnline(pgtype.Timestamptz{}, connected, recent, now) {
		t.Fatal("connected Runner with a recent heartbeat reported offline")
	}
	for name, online := range map[string]bool{
		"logical disconnect": runnerBindingOnline(disconnected, connected, recent, now),
		"socket absent":      runnerBindingOnline(pgtype.Timestamptz{}, pgtype.UUID{}, recent, now),
		"heartbeat stale":    runnerBindingOnline(pgtype.Timestamptz{}, connected, stale, now),
	} {
		if online {
			t.Fatalf("%s reported online", name)
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

func TestRunnerMCPOverlayMarksManagedRoute(t *testing.T) {
	overlay, err := runnerMCPOverlay("https://pre.example", "mat_task")
	if err != nil {
		t.Fatalf("build Runner MCP overlay: %v", err)
	}
	var document struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(overlay, &document); err != nil {
		t.Fatalf("parse Runner MCP overlay: %v", err)
	}
	runner := document.MCPServers[runnerprotocol.ManagedMCPServerName]
	if runner.URL != "https://pre.example/api/runner-mcp" {
		t.Fatalf("Runner MCP URL = %q", runner.URL)
	}
	if runner.Headers["Authorization"] != "Bearer mat_task" || runner.Headers[runnerprotocol.ManagedMCPRoutingHeader] != runnerprotocol.ManagedMCPRoutingValue {
		t.Fatalf("Runner MCP headers = %#v", runner.Headers)
	}
}

func TestRunnerInstallScriptSupportsPairAndReconnect(t *testing.T) {
	for _, required := range []string{
		`--pairing-token`,
		`runner bind --server-url "$server_url" --pairing-token "$pairing_token"`,
		`--reconnect-token`,
		`runner reconnect --server-url "$server_url" --reconnect-token "$reconnect_token"`,
	} {
		if !strings.Contains(runnerInstallScript, required) {
			t.Fatalf("Runner installer is missing %q", required)
		}
	}
}
