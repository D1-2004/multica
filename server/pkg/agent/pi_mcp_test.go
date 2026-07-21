package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizePiMCPConfigCanonicalizesTransports(t *testing.T) {
	raw := json.RawMessage(`{
		"mcpServers": {
			"z_sse": {"type":"sse","url":"https://mcp.example.test/events","headers":{"Authorization":"Bearer secret"},"timeout":30},
			"a_local": {"command":"node","args":["server.mjs"],"env":{"API_KEY":"secret"}},
			"m_http": {"type":"http","url":"https://mcp.example.test/mcp"},
			"disabled": {"command":"false","enabled":false}
		}
	}`)
	data, err := normalizePiMCPConfig(raw)
	if err != nil {
		t.Fatalf("normalizePiMCPConfig: %v", err)
	}
	var got piMCPConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode canonical config: %v", err)
	}
	if got.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d, want 1", got.SchemaVersion)
	}
	if len(got.Servers) != 3 {
		t.Fatalf("servers = %d, want 3", len(got.Servers))
	}
	if got.Servers[0].Name != "a_local" || got.Servers[0].Transport != "stdio" || got.Servers[0].Command != "node" {
		t.Fatalf("unexpected stdio server: %#v", got.Servers[0])
	}
	if got.Servers[1].Name != "m_http" || got.Servers[1].Transport != "streamable-http" {
		t.Fatalf("unexpected http server: %#v", got.Servers[1])
	}
	if got.Servers[2].Name != "z_sse" || got.Servers[2].Transport != "sse" || got.Servers[2].RequestTimeoutMS != 30000 {
		t.Fatalf("unexpected sse server: %#v", got.Servers[2])
	}
}

func TestNormalizePiMCPConfigManagedEmpty(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"mcpServers":{}}`)} {
		data, err := normalizePiMCPConfig(raw)
		if err != nil {
			t.Fatalf("normalizePiMCPConfig(%s): %v", raw, err)
		}
		if string(data) != `{"schemaVersion":1,"servers":[]}` {
			t.Fatalf("canonical empty config = %s", data)
		}
	}
}

func TestNormalizePiMCPConfigAcceptsOpenCodeNativeContainer(t *testing.T) {
	raw := json.RawMessage(`{
		"mcp": {
			"local": {"type":"local","command":["node","server.mjs"],"environment":{"API_KEY":"secret"},"timeout":5000},
			"remote": {"type":"remote","url":"https://example.test/mcp","headers":{"Authorization":"Bearer secret"}},
			"disabled": {"enabled":false}
		}
	}`)
	data, err := normalizePiMCPConfig(raw)
	if err != nil {
		t.Fatalf("normalizePiMCPConfig: %v", err)
	}
	var got piMCPConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 2 {
		t.Fatalf("servers = %d, want 2", len(got.Servers))
	}
	if got.Servers[0].Name != "local" || got.Servers[0].Command != "node" || len(got.Servers[0].Args) != 1 || got.Servers[0].RequestTimeoutMS != 5000 {
		t.Fatalf("native local server = %#v", got.Servers[0])
	}
	if got.Servers[1].Name != "remote" || got.Servers[1].Transport != "streamable-http" {
		t.Fatalf("native remote server = %#v", got.Servers[1])
	}
}

func TestNormalizePiMCPConfigRejectsInvalidDocuments(t *testing.T) {
	tests := map[string]string{
		"not json":                 `not-json`,
		"top-level array":          `[]`,
		"unknown top-level":        `{"mcpServers":{},"other":true}`,
		"duplicate containers":     `{"mcpServers":{"x":{"command":"x"}},"mcp":{"x":{"type":"local","command":["x"]}}}`,
		"servers array":            `{"mcpServers":[]}`,
		"bad name":                 `{"mcpServers":{"has space":{"command":"x"}}}`,
		"unknown server field":     `{"mcpServers":{"x":{"command":"x","secretOption":true}}}`,
		"both command and url":     `{"mcpServers":{"x":{"command":"x","url":"https://example.test"}}}`,
		"neither command nor url":  `{"mcpServers":{"x":{}}}`,
		"wrong stdio type":         `{"mcpServers":{"x":{"type":"sse","command":"x"}}}`,
		"wrong remote type":        `{"mcpServers":{"x":{"type":"stdio","url":"https://example.test"}}}`,
		"url credentials":          `{"mcpServers":{"x":{"url":"https://user:pass@example.test"}}}`,
		"bad header":               "{\"mcpServers\":{\"x\":{\"url\":\"https://example.test\",\"headers\":{\"X-Test\":\"line\\nbreak\"}}}}",
		"header control byte":      `{"mcpServers":{"x":{"url":"https://example.test","headers":{"X-Test":"bad\u0001value"}}}}`,
		"duplicate header casing":  `{"mcpServers":{"x":{"url":"https://example.test","headers":{"Authorization":"a","authorization":"b"}}}}`,
		"bad env name":             `{"mcpServers":{"x":{"command":"x","env":{"BAD-NAME":"v"}}}}`,
		"nul command":              `{"mcpServers":{"x":{"command":"x\u0000y"}}}`,
		"nul arg":                  `{"mcpServers":{"x":{"command":"x","args":["a\u0000b"]}}}`,
		"zero timeout":             `{"mcpServers":{"x":{"command":"x","timeout":0}}}`,
		"multiple values":          `{"mcpServers":{}} {}`,
		"duplicate top-level key":  `{"mcpServers":{},"mcpServers":{}}`,
		"duplicate nested key":     `{"mcpServers":{"x":{"command":"a","command":"b"}}}`,
		"native oauth unsupported": `{"mcp":{"x":{"type":"remote","url":"https://example.test","oauth":false}}}`,
		"native timeout too small": `{"mcp":{"x":{"type":"local","command":["x"],"timeout":999}}}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizePiMCPConfig(json.RawMessage(raw)); err == nil {
				t.Fatalf("normalizePiMCPConfig(%s) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestNormalizePiMCPConfigRejectsOversizedDocument(t *testing.T) {
	raw := json.RawMessage(`{"mcpServers":{},"padding":"` + strings.Repeat("x", piMCPMaxConfigBytes) + `"}`)
	if _, err := normalizePiMCPConfig(raw); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("normalizePiMCPConfig oversized error = %v", err)
	}
}

func TestWritePiMCPConfigToTempUsesOwnerOnlyPermissions(t *testing.T) {
	taskTempDir := t.TempDir()
	if err := os.Chmod(taskTempDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := writePiMCPConfigToTemp(json.RawMessage(`{"mcpServers":{"x":{"command":"echo"}}}`), taskTempDir)
	if err != nil {
		t.Fatalf("writePiMCPConfigToTemp: %v", err)
	}
	dir := filepath.Dir(path)
	t.Cleanup(func() { cleanupPiMCPConfigTemp(path) })

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("config dir mode = %o, want 700", got)
	}

	cleanupPiMCPConfigTemp(path)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("config dir still exists after cleanup: %v", err)
	}
}

func TestValidatePiMCPExtensionPathRejectsWritableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.mjs")
	if err := os.WriteFile(path, []byte("export default {}"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePiMCPExtensionPath(path); err == nil {
		t.Fatal("world-writable extension unexpectedly accepted")
	}
}

func TestWritePiMCPConfigRequiresOwnerOnlyTaskTempDir(t *testing.T) {
	if _, err := writePiMCPConfigToTemp(json.RawMessage(`{"mcpServers":{}}`), ""); err == nil {
		t.Fatal("managed MCP accepted an empty task TMPDIR")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := writePiMCPConfigToTemp(json.RawMessage(`{"mcpServers":{}}`), dir); err == nil {
		t.Fatal("managed MCP accepted a non-private task TMPDIR")
	}
}

func TestAddPiManagedExtensionArgKeepsPromptLast(t *testing.T) {
	got := addPiManagedExtensionArg([]string{"-p", "--mode", "json", "prompt"}, "/opt/multica-pi-mcp/index.mjs")
	want := []string{"-p", "--mode", "json", "--no-extensions", "--extension", "/opt/multica-pi-mcp/index.mjs", "prompt"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestBuildPiArgsManagedMCPBlocksUntrustedExtensions(t *testing.T) {
	args := buildPiArgs("prompt", "/tmp/s.jsonl", ExecOptions{
		McpConfig: json.RawMessage(`{"mcpServers":{}}`),
		CustomArgs: []string{
			"--extension", "/tmp/untrusted.mjs",
			"-e=/tmp/also-untrusted.mjs",
			"--no-extensions",
			"--tools", "read",
		},
	}, slog.Default())
	joined := strings.Join(args, "\x00")
	if strings.Contains(joined, "untrusted") || strings.Contains(joined, "--no-extensions") {
		t.Fatalf("managed MCP custom args retained extension controls: %#v", args)
	}
	if !strings.Contains(joined, "--tools\x00read") {
		t.Fatalf("unrelated custom args were removed: %#v", args)
	}
}

func TestPiExecuteLoadsManagedMCPExtensionAndCleansConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	tmp := t.TempDir()
	if err := os.Chmod(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	extensionPath := filepath.Join(tmp, "index.mjs")
	if err := os.WriteFile(extensionPath, []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(tmp, "captured-config-path")
	fakePath := filepath.Join(tmp, "pi")
	script := `#!/bin/sh
found=0
discovery_disabled=0
while [ "$#" -gt 0 ]; do
	if [ "$1" = "--no-extensions" ]; then
		discovery_disabled=1
	fi
  if [ "$1" = "--extension" ]; then
    shift
    [ "$1" = "$EXPECT_EXTENSION" ] || exit 21
    found=1
  fi
  shift
done
[ "$found" = "1" ] || exit 22
[ "$discovery_disabled" = "1" ] || exit 27
[ -f "$MULTICA_PI_MCP_CONFIG_PATH" ] || exit 23
perm=$(stat -c '%a' "$MULTICA_PI_MCP_CONFIG_PATH" 2>/dev/null || stat -f '%Lp' "$MULTICA_PI_MCP_CONFIG_PATH")
[ "$perm" = "600" ] || exit 24
grep -q '"schemaVersion":1' "$MULTICA_PI_MCP_CONFIG_PATH" || exit 25
grep -q '"transport":"streamable-http"' "$MULTICA_PI_MCP_CONFIG_PATH" || exit 26
printf '%s' "$MULTICA_PI_MCP_CONFIG_PATH" > "$CAPTURE_PATH"
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"turn_end","message":{"role":"assistant","model":"test","usage":{"input":1,"output":1}}}'
`
	writeTestExecutable(t, fakePath, []byte(script))

	backend, err := New("pi", Config{
		ExecutablePath: fakePath,
		Logger:         slog.Default(),
		Env: map[string]string{
			piMCPExtensionPathEnv: extensionPath,
			"EXPECT_EXTENSION":    extensionPath,
			"CAPTURE_PATH":        capturePath,
			"TMPDIR":              tmp,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := backend.Execute(context.Background(), "managed mcp", ExecOptions{
		Timeout:   5 * time.Second,
		McpConfig: json.RawMessage(`{"mcpServers":{"remote":{"type":"http","url":"https://example.test/mcp","headers":{"Authorization":"Bearer secret"}}}}`),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read captured config path: %v", err)
	}
	if _, err := os.Stat(string(captured)); !os.IsNotExist(err) {
		t.Fatalf("managed config still exists after Pi exit: %v", err)
	}
}

func TestPiExecuteManagedMCPFailsBeforeStartWithoutTrustedExtension(t *testing.T) {
	fakePath := filepath.Join(t.TempDir(), "pi")
	writeTestExecutable(t, fakePath, []byte("#!/bin/sh\nexit 99\n"))
	backend, err := New("pi", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Execute(context.Background(), "prompt", ExecOptions{
		McpConfig: json.RawMessage(`{"mcpServers":{"x":{"command":"echo"}}}`),
	})
	if err == nil || !strings.Contains(err.Error(), piMCPExtensionPathEnv) {
		t.Fatalf("Execute error = %v, want missing trusted extension", err)
	}
}
