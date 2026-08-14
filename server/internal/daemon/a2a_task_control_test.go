package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInjectA2ATaskControlMCPReservedServerWins(t *testing.T) {
	raw := json.RawMessage(`{"mcpServers":{"multica_a2a_task_control":{"url":"https://attacker.invalid"},"safe":{"url":"https://safe.example"}}}`)
	encoded, err := injectA2ATaskControlMCP(raw, 12345, "mca2actl_secret")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(encoded, &config); err != nil {
		t.Fatal(err)
	}
	control := config.Servers[a2aTaskControlServerName]
	if control.URL != "http://127.0.0.1:12345/a2a/task-control" || control.Headers["Authorization"] != "Bearer mca2actl_secret" {
		t.Fatalf("reserved server was not replaced: %#v", control)
	}
	if config.Servers["safe"].URL != "https://safe.example" {
		t.Fatal("unrelated MCP server was removed")
	}
}

func TestInjectA2ATaskControlMCPSupportsEmptyAndOpenCodeNativeConfig(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"empty":           json.RawMessage(`{}`),
		"opencode-native": json.RawMessage(`{"mcp":{"native":{"type":"remote","url":"https://safe.example"}}}`),
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := injectA2ATaskControlMCP(raw, 12345, "mca2actl_secret")
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			var servers map[string]json.RawMessage
			if err := json.Unmarshal(document["mcpServers"], &servers); err != nil {
				t.Fatalf("decode canonical servers: %v", err)
			}
			if _, ok := servers[a2aTaskControlServerName]; !ok {
				t.Fatalf("task control server missing from %s", encoded)
			}
			if name == "opencode-native" {
				if _, ok := document["mcp"]; !ok {
					t.Fatalf("native OpenCode servers were removed: %s", encoded)
				}
			}
		})
	}
}

func TestInjectA2ATaskControlMCPRejectsNonObjectServers(t *testing.T) {
	if _, err := injectA2ATaskControlMCP(json.RawMessage(`{"mcpServers":[]}`), 12345, "mca2actl_secret"); err == nil {
		t.Fatal("non-object mcpServers was accepted")
	}
}

func TestA2ATaskControlToolsExposeStrictPartUnions(t *testing.T) {
	encoded, err := json.Marshal(a2aTaskControlTools())
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, required := range []string{`"request_input"`, `"request_auth"`, `"publish_artifact"`, `"additionalProperties":false`, `"raw"`, `"url"`, `"data"`, `"text"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("tool schema is missing %s: %s", required, text)
		}
	}
}

func TestA2ATaskControlRequestInputSchemaIsAnObject(t *testing.T) {
	for _, tool := range a2aTaskControlTools() {
		if tool["name"] != "request_input" {
			continue
		}
		inputSchema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("request_input input schema = %#v", tool["inputSchema"])
		}
		properties, ok := inputSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("request_input properties = %#v", inputSchema["properties"])
		}
		schema, ok := properties["schema"].(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("request_input schema property = %#v, want object", properties["schema"])
		}
		return
	}
	t.Fatal("request_input tool not found")
}

func TestA2ATaskControlCapabilityIsTaskScopedAndRevoked(t *testing.T) {
	var upstreamPath string
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"TASK_STATE_INPUT_REQUIRED"}`))
	}))
	defer upstream.Close()

	d := &Daemon{client: NewClient(upstream.URL)}
	canceled := false
	token, release, err := d.registerA2ATaskControlCapability(Task{ID: "task-123", A2AInvocation: true}, func() { canceled = true })
	if err != nil {
		t.Fatal(err)
	}
	handler := d.a2aTaskControlHandler()
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"request_input","arguments":{"parts":[{"text":"Which project?"}]}}}`)
	request := httptest.NewRequest(http.MethodPost, "/a2a/task-control", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusOK || !canceled {
		t.Fatalf("control response=%d body=%s canceled=%v", response.Code, response.Body.String(), canceled)
	}
	if upstreamPath != "/api/daemon/tasks/task-123/a2a-control" || upstreamBody["action"] != "request_input" {
		t.Fatalf("upstream path=%q body=%#v", upstreamPath, upstreamBody)
	}

	release()
	request = httptest.NewRequest(http.MethodPost, "/a2a/task-control", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked capability status = %d, want 401", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/a2a/task-control", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer mca2actl_wrong")
	response = httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong capability status = %d, want 401", response.Code)
	}
}

func TestRegisterA2ATaskControlCapabilityRejectsOrdinaryTask(t *testing.T) {
	d := &Daemon{}
	if _, _, err := d.registerA2ATaskControlCapability(Task{ID: "task-123"}, func() {}); err == nil {
		t.Fatal("ordinary task received A2A control capability")
	}
	if _, _, err := d.registerA2ATaskControlCapability(Task{ID: "task-123", A2AInvocation: true}, nil); err == nil {
		t.Fatal("capability without run cancellation was accepted")
	}
}
