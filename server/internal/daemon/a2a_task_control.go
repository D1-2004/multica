package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const (
	a2aTaskControlServerName = "multica_a2a_task_control"
	a2aTaskControlBodyLimit  = 16 << 20
	a2aMCPProtocolVersion    = "2025-06-18"
	a2aMCPCompatVersion      = "2025-03-26"
)

type a2aTaskControlCapability struct {
	mu     sync.RWMutex
	active bool
	taskID string
	cancel context.CancelFunc
}

type a2aMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type a2aMCPResponse struct {
	JSONRPC string       `json:"jsonrpc"`
	ID      any          `json:"id,omitempty"`
	Result  any          `json:"result,omitempty"`
	Error   *a2aMCPError `json:"error,omitempty"`
}

type a2aMCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type a2aMCPToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func newA2ATaskControlToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "mca2actl_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func (d *Daemon) registerA2ATaskControlCapability(task Task, cancel context.CancelFunc) (string, func(), error) {
	if !task.A2AInvocation || strings.TrimSpace(task.ID) == "" {
		return "", nil, errors.New("A2A task control requires an A2A task")
	}
	token, err := newA2ATaskControlToken()
	if err != nil {
		return "", nil, fmt.Errorf("generate capability: %w", err)
	}
	digest := sha256.Sum256([]byte(token))
	if cancel == nil {
		return "", nil, errors.New("A2A task control requires a run cancellation function")
	}
	capability := &a2aTaskControlCapability{active: true, taskID: task.ID, cancel: cancel}
	d.a2aTaskCapabilitiesMu.Lock()
	if d.a2aTaskCapabilities == nil {
		d.a2aTaskCapabilities = make(map[[sha256.Size]byte]*a2aTaskControlCapability)
	}
	d.a2aTaskCapabilities[digest] = capability
	d.a2aTaskCapabilitiesMu.Unlock()

	release := func() {
		capability.mu.Lock()
		capability.active = false
		capability.mu.Unlock()
		d.a2aTaskCapabilitiesMu.Lock()
		if d.a2aTaskCapabilities[digest] == capability {
			delete(d.a2aTaskCapabilities, digest)
		}
		d.a2aTaskCapabilitiesMu.Unlock()
	}
	return token, release, nil
}

func (d *Daemon) acquireA2ATaskControlCapability(r *http.Request) (*a2aTaskControlCapability, func(), bool) {
	token, ok := localBearerToken(r)
	if !ok || !strings.HasPrefix(token, "mca2actl_") {
		return nil, nil, false
	}
	digest := sha256.Sum256([]byte(token))
	d.a2aTaskCapabilitiesMu.RLock()
	capability := d.a2aTaskCapabilities[digest]
	if capability != nil {
		capability.mu.RLock()
	}
	d.a2aTaskCapabilitiesMu.RUnlock()
	if capability == nil || !capability.active {
		if capability != nil {
			capability.mu.RUnlock()
		}
		return nil, nil, false
	}
	return capability, capability.mu.RUnlock, true
}

func injectA2ATaskControlMCP(raw json.RawMessage, port int, token string) (json.RawMessage, error) {
	if port <= 0 || strings.TrimSpace(token) == "" {
		return nil, errors.New("local A2A task control endpoint is not configured")
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse MCP config: %w", err)
	}
	if document == nil {
		return nil, errors.New("A2A MCP config must be a JSON object")
	}
	servers := map[string]any{}
	if configured, exists := document["mcpServers"]; exists {
		var ok bool
		servers, ok = configured.(map[string]any)
		if !ok {
			return nil, errors.New("A2A MCP config mcpServers field must be an object")
		}
	}
	// This reserved name always wins over Agent configuration. The bearer is a
	// per-run loopback capability and is never placed in the child environment.
	// OpenCode-native configurations may contain only a top-level `mcp` map;
	// retaining it while adding this canonical `mcpServers` entry lets the
	// provider adapter merge both shapes, with this reserved entry taking final
	// precedence.
	servers[a2aTaskControlServerName] = map[string]any{
		"type": "http",
		"url":  fmt.Sprintf("http://127.0.0.1:%d/a2a/task-control", port),
		"headers": map[string]string{
			"Authorization": "Bearer " + token,
		},
	}
	document["mcpServers"] = servers
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode MCP config: %w", err)
	}
	return encoded, nil
}

func (d *Daemon) a2aTaskControlHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		capability, release, ok := d.acquireA2ATaskControlCapability(r)
		if !ok {
			writeLocalUnauthorized(w)
			return
		}
		defer release()

		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, a2aTaskControlBodyLimit))
		decoder.DisallowUnknownFields()
		var request a2aMCPRequest
		if err := decoder.Decode(&request); err != nil {
			writeA2AMCPResponse(w, a2aMCPResponse{JSONRPC: "2.0", Error: &a2aMCPError{Code: -32700, Message: "parse error"}})
			return
		}
		if err := ensureJSONEOF(decoder); err != nil || request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
			writeA2AMCPResponse(w, a2aMCPResponse{JSONRPC: "2.0", ID: decodeA2AMCPID(request.ID), Error: &a2aMCPError{Code: -32600, Message: "invalid request"}})
			return
		}
		if len(request.ID) == 0 || string(request.ID) == "null" {
			if request.Method == "notifications/initialized" || strings.HasPrefix(request.Method, "notifications/") {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		response := d.handleA2AMCPRequest(r, capability, request)
		writeA2AMCPResponse(w, response)
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func decodeA2AMCPID(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var id any
	if json.Unmarshal(raw, &id) != nil {
		return nil
	}
	return id
}

func writeA2AMCPResponse(w http.ResponseWriter, response a2aMCPResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(response)
}

func (d *Daemon) handleA2AMCPRequest(r *http.Request, capability *a2aTaskControlCapability, request a2aMCPRequest) a2aMCPResponse {
	response := a2aMCPResponse{JSONRPC: "2.0", ID: decodeA2AMCPID(request.ID)}
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(request.Params) > 0 && json.Unmarshal(request.Params, &params) != nil {
			response.Error = &a2aMCPError{Code: -32602, Message: "invalid initialize parameters"}
			return response
		}
		protocolVersion := a2aMCPProtocolVersion
		if params.ProtocolVersion == a2aMCPProtocolVersion || params.ProtocolVersion == a2aMCPCompatVersion {
			protocolVersion = params.ProtocolVersion
		}
		response.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": a2aTaskControlServerName, "version": "1.0.0"},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": a2aTaskControlTools()}
	case "tools/call":
		var params a2aMCPToolCallParams
		if err := json.Unmarshal(request.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
			response.Error = &a2aMCPError{Code: -32602, Message: "invalid tool arguments"}
			return response
		}
		control, err := a2aMCPControlRequest(params)
		if err != nil {
			response.Result = a2aMCPToolResult(err.Error(), true)
			return response
		}
		var controlResponse map[string]any
		if err := d.client.ControlA2ATask(r.Context(), capability.taskID, control, &controlResponse); err != nil {
			response.Result = a2aMCPToolResult("A2A task control rejected the operation", true)
			return response
		}
		if params.Name == "request_input" || params.Name == "request_auth" {
			capability.cancel()
		}
		encoded, _ := json.Marshal(controlResponse)
		response.Result = a2aMCPToolResult(string(encoded), false)
	default:
		response.Error = &a2aMCPError{Code: -32601, Message: "method not found"}
	}
	return response
}

func a2aMCPToolResult(message string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": message}},
		"isError": isError,
	}
}

func a2aMCPControlRequest(params a2aMCPToolCallParams) (map[string]any, error) {
	args := params.Arguments
	if args == nil {
		args = map[string]any{}
	}
	copyField := func(target map[string]any, source, destination string) {
		if value, exists := args[source]; exists {
			target[destination] = value
		}
	}
	request := map[string]any{}
	validateKeys := func(allowed []string, required []string) error {
		allowedSet := make(map[string]struct{}, len(allowed))
		for _, key := range allowed {
			allowedSet[key] = struct{}{}
		}
		for key := range args {
			if _, ok := allowedSet[key]; !ok {
				return fmt.Errorf("unknown argument %q", key)
			}
		}
		for _, key := range required {
			if value, ok := args[key]; !ok || value == nil {
				return fmt.Errorf("missing required argument %q", key)
			}
		}
		return nil
	}
	switch params.Name {
	case "request_input":
		if err := validateKeys([]string{"parts", "schema"}, []string{"parts"}); err != nil {
			return nil, err
		}
		request["action"] = "request_input"
		copyField(request, "parts", "parts")
		copyField(request, "schema", "schema")
	case "request_auth":
		if err := validateKeys([]string{"parts", "authRequest"}, []string{"parts", "authRequest"}); err != nil {
			return nil, err
		}
		request["action"] = "request_auth"
		copyField(request, "parts", "parts")
		copyField(request, "authRequest", "auth_request")
	case "publish_artifact":
		allowed := []string{"parts", "name", "description", "extensions", "metadata", "append", "artifactId", "lastChunk"}
		if err := validateKeys(allowed, []string{"parts"}); err != nil {
			return nil, err
		}
		request["action"] = "publish_artifact"
		for _, field := range []string{"parts", "name", "description", "extensions", "metadata", "append"} {
			copyField(request, field, field)
		}
		copyField(request, "artifactId", "artifact_id")
		copyField(request, "lastChunk", "last_chunk")
	default:
		return nil, errors.New("unknown A2A task control tool")
	}
	return request, nil
}

func a2aTaskControlTools() []map[string]any {
	commonPartProperties := map[string]any{
		"filename":  map[string]any{"type": "string"},
		"mediaType": map[string]any{"type": "string"},
		"metadata":  map[string]any{"type": "object"},
		"text":      map[string]any{"type": "string"},
		"data":      map[string]any{},
	}
	promptPartSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           commonPartProperties,
		"oneOf": []map[string]any{
			{"required": []string{"text"}}, {"required": []string{"data"}},
		},
	}
	artifactPartProperties := make(map[string]any, len(commonPartProperties)+2)
	for key, schema := range commonPartProperties {
		artifactPartProperties[key] = schema
	}
	artifactPartProperties["raw"] = map[string]any{"type": "string", "contentEncoding": "base64"}
	artifactPartProperties["url"] = map[string]any{"type": "string", "format": "uri"}
	artifactPartSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           artifactPartProperties,
		"oneOf": []map[string]any{
			{"required": []string{"text"}}, {"required": []string{"data"}},
			{"required": []string{"raw"}}, {"required": []string{"url"}},
		},
	}
	promptParts := map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": promptPartSchema}
	artifactParts := map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": artifactPartSchema}
	return []map[string]any{
		{
			"name":        "request_input",
			"description": "Pause this A2A task and ask the caller for additional natural-language or structured input.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"parts"}, "properties": map[string]any{"parts": promptParts, "schema": map[string]any{}}},
		},
		{
			"name":        "request_auth",
			"description": "Pause this A2A task and tell the caller which authentication must be supplied on the next turn. Never include credentials.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"parts", "authRequest"}, "properties": map[string]any{"parts": promptParts, "authRequest": map[string]any{"type": "object"}}},
		},
		{
			"name":        "publish_artifact",
			"description": "Publish or append a text, structured-data, or file artifact for the current A2A task.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"parts"},
				"properties": map[string]any{
					"artifactId": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"}, "parts": artifactParts,
					"extensions": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"metadata":   map[string]any{"type": "object"}, "append": map[string]any{"type": "boolean"},
					"lastChunk": map[string]any{"type": "boolean"},
				},
			},
		},
	}
}
