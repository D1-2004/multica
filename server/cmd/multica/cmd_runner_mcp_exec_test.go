package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"bufio"
	"fmt"
	"strings"
	"testing"
)

func TestRunnerMCPManagerForwardsHTTPAndKeepsSessionByTask(t *testing.T) {
	var gotSession string
	var gotProtocol string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession = r.Header.Get("Mcp-Session-Id")
		gotProtocol = r.Header.Get("MCP-Protocol-Version")
		w.Header().Set("Mcp-Session-Id", "local-session")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	defer server.Close()

	document, err := parseRunnerMCPConfig([]byte(`{"mcpServers":{"local":{"url":"` + server.URL + `","headers":{"X-Local-Secret":"kept-local"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	manager := newRunnerMCPManager(document)
	defer manager.Close()
	request := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	args, _ := json.Marshal(runnerMCPCallArguments{ServerName: "local", Fingerprint: document.Servers["local"].Fingerprint, SessionKey: "task/mount/local", ProtocolVersion: "2025-06-18", Request: request})

	got, toolErr := manager.Execute(context.Background(), args)
	if toolErr != nil {
		t.Fatalf("execute HTTP MCP: %v", toolErr)
	}
	if string(got) != `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` {
		t.Fatalf("response = %s", got)
	}
	if gotSession != "" {
		t.Fatalf("first request unexpectedly had session %q", gotSession)
	}
	if gotProtocol != "2025-06-18" { t.Fatalf("protocol version = %q", gotProtocol) }
	if _, toolErr = manager.Execute(context.Background(), args); toolErr != nil {
		t.Fatalf("execute second HTTP MCP: %v", toolErr)
	}
	if gotSession != "local-session" {
		t.Fatalf("second request session = %q", gotSession)
	}
}

func TestRunnerMCPManagerForwardsStdio(t *testing.T) {
	document, err := parseRunnerMCPConfig([]byte(`{"mcpServers":{"local":{"command":"` + os.Args[0] + `","args":["-test.run=TestRunnerMCPStdioHelper"],"env":{"RUNNER_MCP_TEST_HELPER":"1"}}}}`))
	if err != nil { t.Fatal(err) }
	manager := newRunnerMCPManager(document)
	defer manager.Close()
	args, _ := json.Marshal(runnerMCPCallArguments{ServerName: "local", Fingerprint: document.Servers["local"].Fingerprint, Request: json.RawMessage(`{"jsonrpc":"2.0","id":"a","method":"ping"}`)})
	got, toolErr := manager.Execute(context.Background(), args)
	if toolErr != nil { t.Fatalf("execute stdio MCP: %v", toolErr) }
	if string(got) != `{"jsonrpc":"2.0","id":"a","result":{"transport":"stdio"}}` { t.Fatalf("response = %s", got) }
}

func TestRunnerMCPStdioHelper(t *testing.T) {
	if os.Getenv("RUNNER_MCP_TEST_HELPER") != "1" { return }
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct { ID json.RawMessage `json:"id"` }
		_ = json.Unmarshal(scanner.Bytes(), &request)
		_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"transport":"stdio"}}`+"\n", request.ID)
	}
	os.Exit(0)
}

func TestRunnerMCPManagerRejectsFingerprintMismatch(t *testing.T) {
	document, err := parseRunnerMCPConfig([]byte(`{"mcpServers":{"local":{"url":"http://127.0.0.1:1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	manager := newRunnerMCPManager(document)
	defer manager.Close()
	args, _ := json.Marshal(runnerMCPCallArguments{ServerName: "local", Fingerprint: "sha256:stale", Request: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)})
	_, toolErr := manager.Execute(context.Background(), args)
	if toolErr == nil || toolErr.code != "runner_mcp_configuration_changed" {
		t.Fatalf("error = %#v", toolErr)
	}
}

func TestRunnerBuiltinShellMCPListsTools(t *testing.T) {
	document, err := parseRunnerMCPConfigWithBuiltins([]byte(`{"mcpServers":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	manager := newRunnerMCPManager(document)
	defer manager.Close()
	initialize := json.RawMessage(`{"jsonrpc":"2.0","id":"init-1","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}}}`)
	initializeArgs, _ := json.Marshal(runnerMCPCallArguments{
		ServerName: runnerBuiltinShellMCPName,
		Fingerprint: document.Servers[runnerBuiltinShellMCPName].Fingerprint,
		Request: initialize,
	})
	initializeResponse, toolErr := manager.Execute(context.Background(), initializeArgs)
	if toolErr != nil || !strings.Contains(string(initializeResponse), `"protocolVersion":"2025-06-18"`) {
		t.Fatalf("initialize built-in MCP: response=%s error=%v", initializeResponse, toolErr)
	}
	request := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	args, _ := json.Marshal(runnerMCPCallArguments{
		ServerName: runnerBuiltinShellMCPName,
		Fingerprint: document.Servers[runnerBuiltinShellMCPName].Fingerprint,
		Request: request,
	})
	response, toolErr := manager.Execute(context.Background(), args)
	if toolErr != nil {
		t.Fatalf("execute built-in MCP: %v", toolErr)
	}
	var decoded struct {
		Result struct {
			Tools []struct{ Name string `json:"name"` } `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(decoded.Result.Tools))
	for _, tool := range decoded.Result.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"list_roots", "read_file", "write_file", "edit_file", "list_directory", "stat", "glob", "grep", "shell", "shell_output", "shell_kill"} {
		if !names[name] {
			t.Fatalf("built-in MCP tools missing %q: %s", name, response)
		}
	}
}

func TestRunnerBuiltinShellMCPCallsShellInsideConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	document, err := parseRunnerMCPConfigWithBuiltins([]byte(`{"mcpServers":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	manager := newRunnerMCPManager(document, root)
	defer manager.Close()
	requestBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "shell-1", "method": "tools/call",
		"params": map[string]any{
			"name": "shell",
			"arguments": map[string]any{"command": "pwd", "cwd": root},
		},
	})
	args, _ := json.Marshal(runnerMCPCallArguments{
		ServerName: runnerBuiltinShellMCPName,
		Fingerprint: document.Servers[runnerBuiltinShellMCPName].Fingerprint,
		Request: requestBody,
	})
	response, toolErr := manager.Execute(context.Background(), args)
	if toolErr != nil {
		t.Fatalf("execute built-in Shell MCP: %v", toolErr)
	}
	var decoded struct {
		Result struct {
			IsError bool `json:"isError"`
			StructuredContent struct {
				ExitCode  int    `json:"exit_code"`
				Output    string `json:"output"`
				Succeeded bool   `json:"succeeded"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Result.IsError || !decoded.Result.StructuredContent.Succeeded || decoded.Result.StructuredContent.ExitCode != 0 || strings.TrimSpace(decoded.Result.StructuredContent.Output) != root {
		t.Fatalf("Shell MCP response = %s", response)
	}
}
