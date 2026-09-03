package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

const (
	runnerMCPCallTimeout      = 60 * time.Second
	runnerMCPShellGracePeriod = 10 * time.Second
)

var errRunnerMCPRuntimeUnsupported = errors.New("runtime does not support managed MCP")

type runnerMountedMCPArguments struct {
	ServerName  string          `json:"server_name"`
	Fingerprint string          `json:"fingerprint"`
	SessionKey  string          `json:"session_key"`
	ProtocolVersion string      `json:"protocol_version,omitempty"`
	Request     json.RawMessage `json:"request"`
}

// RunnerMountedMCP is a task-token-scoped transparent JSON-RPC relay. The
// server never receives the local command, URL, headers, environment, or
// credentials; it only routes to an explicitly enabled inventory fingerprint.
func (h *Handler) RunnerMountedMCP(w http.ResponseWriter, r *http.Request) {
	if !multicaMCPTaskTokenAuthenticated(r) {
		writeError(w, http.StatusForbidden, "Runner MCP requires an Agent task token")
		return
	}
	if !h.multicaMCPOriginAllowed(r) {
		writeError(w, http.StatusForbidden, "untrusted MCP Origin")
		return
	}
	workspaceID, agentID, ok := runnerMCPTaskScope(r)
	if !ok {
		writeError(w, http.StatusForbidden, "invalid Runner MCP task scope")
		return
	}
	bindingID, err := util.ParseUUID(strings.TrimSpace(chi.URLParam(r, "mountId")))
	if err != nil {
		writeError(w, http.StatusNotFound, "Runner MCP mount not found")
		return
	}
	serverName := strings.TrimSpace(chi.URLParam(r, "serverName"))
	bindingRecord, err := h.Queries.GetAgentRunnerBindingByID(r.Context(), db.GetAgentRunnerBindingByIDParams{ID: bindingID, AgentID: agentID})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && bindingRecord.WorkspaceID != workspaceID {
		writeError(w, http.StatusNotFound, "Runner MCP mount not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load Runner MCP mount")
		return
	}
	enabled := decodeEnabledRunnerMCPServers(bindingRecord.EnabledMcpServers)
	inventory, inventoryOK := h.runnerMCPInventory(r.Context(), uuidToString(bindingRecord.MachineID))
	expectedFingerprint, enabledNow := resolveEnabledRunnerMCPServers(enabled, inventory, inventoryOK)[serverName]
	if !enabledNow {
		writeError(w, http.StatusConflict, "Runner MCP mount is disabled, unavailable, or changed")
		return
	}
	binding, err := h.Queries.GetActiveAgentRunnerBinding(r.Context(), db.GetActiveAgentRunnerBindingParams{
		WorkspaceID: workspaceID, AgentID: agentID, MachineID: bindingRecord.MachineID,
	})
	if err != nil || !runnerBindingOnline(binding.DisconnectedAt, binding.ConnectionID, binding.LastSeenAt, time.Now()) {
		writeError(w, http.StatusConflict, "Runner machine is offline")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, multicaMCPMaxRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil || !json.Valid(body) {
		h.writeMulticaMCPError(w, nil, -32700, "parse error")
		return
	}
	var request multicaMCPRequest
	if json.Unmarshal(body, &request) != nil || request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
		h.writeMulticaMCPError(w, request.ID, -32600, "invalid request")
		return
	}
	arguments, _ := json.Marshal(runnerMountedMCPArguments{
		ServerName: serverName, Fingerprint: expectedFingerprint,
		SessionKey: strings.Join([]string{r.Header.Get("X-Task-ID"), uuidToString(bindingID), serverName}, "/"),
		ProtocolVersion: strings.TrimSpace(r.Header.Get("MCP-Protocol-Version")),
		Request: body,
	})
	result, callErr := h.callRunnerMCP(r, binding, "mcp", arguments)
	if callErr != nil {
		h.writeMulticaMCPError(w, request.ID, -32000, callErr.code+": "+callErr.message)
		return
	}
	if !multicaMCPRequestHasID(request.ID) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

func runnerMCPInventoryServer(inventory runnerprotocol.MCPInventory, name string) (runnerprotocol.MCPServerSummary, bool) {
	for _, server := range inventory.Servers {
		if server.Name == name {
			return server, true
		}
	}
	return runnerprotocol.MCPServerSummary{}, false
}

func runnerMCPRuntimeUnsupported(runtime db.AgentRuntime) bool {
	return service.IsCloudSandboxRuntime(runtime) &&
		service.CloudSandboxRuntimeProvider(runtime) == "pi" &&
		!service.CloudSandboxRuntimeHasCapability(runtime, "mcp")
}

var runnerMCPForwardedTools = map[string]struct{}{
	"read_file":      {},
	"write_file":     {},
	"edit_file":      {},
	"list_directory": {},
	"stat":           {},
	"glob":           {},
	"grep":           {},
	"shell":          {},
	"shell_output":   {},
	"shell_kill":     {},
}

// RunnerMCP used to expose a fixed filesystem/shell tool bundle. It is no
// longer task-injected: machine pairing must not implicitly enable a local
// capability. Keep the old route closed instead of letting existing task
// tokens bypass the per-server mount allowlist.
func (h *Handler) RunnerMCP(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "legacy Runner MCP is disabled; enable a mounted local MCP server")
}

func (h *Handler) runnerLegacyMCP(w http.ResponseWriter, r *http.Request) {
	if !multicaMCPTaskTokenAuthenticated(r) {
		writeError(w, http.StatusForbidden, "Runner MCP requires an Agent task token")
		return
	}
	if !h.multicaMCPOriginAllowed(r) {
		writeError(w, http.StatusForbidden, "untrusted MCP Origin")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, multicaMCPMaxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	var req multicaMCPRequest
	if err := decoder.Decode(&req); err != nil {
		h.writeMulticaMCPError(w, nil, -32700, "parse error")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
		h.writeMulticaMCPError(w, req.ID, -32600, "invalid request")
		return
	}
	if !multicaMCPRequestHasID(req.ID) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.Method != "initialize" && !supportedMulticaMCPProtocolVersion(r.Header.Get("MCP-Protocol-Version")) {
		writeError(w, http.StatusBadRequest, "unsupported MCP protocol version")
		return
	}

	switch req.Method {
	case "initialize":
		h.handleRunnerMCPInitialize(w, req)
	case "ping":
		h.writeMulticaMCPResult(w, req.ID, map[string]any{})
	case "tools/list":
		h.writeMulticaMCPResult(w, req.ID, map[string]any{"tools": runnerMCPToolDefinitions()})
	case "tools/call":
		h.handleRunnerMCPToolsCall(w, r, req)
	default:
		h.writeMulticaMCPError(w, req.ID, -32601, "method not found")
	}
}

func (h *Handler) handleRunnerMCPInitialize(w http.ResponseWriter, req multicaMCPRequest) {
	h.writeMulticaMCPResult(w, req.ID, map[string]any{
		"protocolVersion": multicaMCPProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]string{"name": "multica-runner", "version": "1.0.0"},
	})
}

func runnerMachineIDProperty() map[string]any {
	return map[string]any{
		"type":        "string",
		"format":      "uuid",
		"description": "Exact machine_id returned by list_machines. Multica never selects or switches machines automatically.",
	}
}

func runnerTool(name, title, description string, properties map[string]any, required []string, readOnly, destructive, idempotent bool) map[string]any {
	properties["machine_id"] = runnerMachineIDProperty()
	required = append([]string{"machine_id"}, required...)
	return map[string]any{
		"name": name, "title": title, "description": description,
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": properties, "required": required,
		},
		"annotations": map[string]any{
			"readOnlyHint": readOnly, "destructiveHint": destructive,
			"idempotentHint": idempotent, "openWorldHint": false,
		},
	}
}

func runnerMCPToolDefinitions() []any {
	path := map[string]any{"type": "string", "minLength": 1, "description": "Absolute path inside one of the Runner's configured file roots."}
	return []any{
		map[string]any{
			"name": "list_machines", "title": "List bound Runner machines",
			"description": "List every local Runner machine bound to this Agent, including its exact machine_id and online state.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
			"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
		runnerTool("list_roots", "List machine file roots", "List the file roots exposed by one exact Runner machine.", map[string]any{}, nil, true, false, true),
		runnerTool("read_file", "Read a local file", "Read a file from an exposed root on one exact machine.", map[string]any{
			"path":     path,
			"offset":   map[string]any{"type": "integer", "minimum": 0, "default": 0},
			"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 1048576, "default": 1048576},
			"encoding": map[string]any{"type": "string", "enum": []string{"utf8", "base64"}, "default": "utf8"},
		}, []string{"path"}, true, false, true),
		runnerTool("write_file", "Write a local file", "Replace a file inside an exposed root.", map[string]any{
			"path": path, "content": map[string]any{"type": "string"},
			"encoding":       map[string]any{"type": "string", "enum": []string{"utf8", "base64"}, "default": "utf8"},
			"create_parents": map[string]any{"type": "boolean", "default": false},
		}, []string{"path", "content"}, false, true, true),
		runnerTool("edit_file", "Edit a local text file", "Replace exact text inside a UTF-8 file in an exposed root.", map[string]any{
			"path": path, "old_text": map[string]any{"type": "string", "minLength": 1},
			"new_text": map[string]any{"type": "string"}, "replace_all": map[string]any{"type": "boolean", "default": false},
		}, []string{"path", "old_text", "new_text"}, false, true, true),
		runnerTool("list_directory", "List a local directory", "List direct children of a directory in an exposed root.", map[string]any{"path": path}, []string{"path"}, true, false, true),
		runnerTool("stat", "Inspect a local path", "Return metadata for a file or directory in an exposed root.", map[string]any{"path": path}, []string{"path"}, true, false, true),
		runnerTool("glob", "Match local paths", "Match a glob pattern recursively under an exposed root.", map[string]any{
			"root": path, "pattern": map[string]any{"type": "string", "minLength": 1},
			"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "default": 200},
		}, []string{"root", "pattern"}, true, false, true),
		runnerTool("grep", "Search local text files", "Search text recursively under an exposed root.", map[string]any{
			"root": path, "pattern": map[string]any{"type": "string", "minLength": 1},
			"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "default": 200},
		}, []string{"root", "pattern"}, true, false, true),
		runnerTool("shell", "Run a local shell command", "Run /bin/sh as the operating-system user that installed Runner. File roots do not restrict shell access.", map[string]any{
			"command": map[string]any{"type": "string", "minLength": 1},
			"cwd":     path, "background": map[string]any{"type": "boolean", "default": false},
			"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 600, "default": 60},
		}, []string{"command", "cwd"}, false, true, false),
		runnerTool("shell_output", "Read background shell output", "Read accumulated output and exit state for a process started by shell.", map[string]any{
			"process_id": map[string]any{"type": "string", "minLength": 1},
		}, []string{"process_id"}, true, false, true),
		runnerTool("shell_kill", "Stop a background shell", "Terminate a process started by shell on one exact Runner machine.", map[string]any{
			"process_id": map[string]any{"type": "string", "minLength": 1},
		}, []string{"process_id"}, false, true, true),
	}
}

func (h *Handler) handleRunnerMCPToolsCall(w http.ResponseWriter, r *http.Request, req multicaMCPRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(req.Params, &params) != nil || strings.TrimSpace(params.Name) == "" {
		h.writeMulticaMCPError(w, req.ID, -32602, "invalid tool call parameters")
		return
	}
	if params.Name == "list_machines" {
		var args map[string]any
		if decodeMulticaMCPArguments(params.Arguments, &args) != nil || len(args) != 0 {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid list_machines arguments")
			return
		}
		h.runnerMCPListMachines(w, r, req.ID)
		return
	}
	if params.Name != "list_roots" {
		if _, ok := runnerMCPForwardedTools[params.Name]; !ok {
			h.writeMulticaMCPError(w, req.ID, -32602, "unknown tool")
			return
		}
	}
	var args map[string]json.RawMessage
	if decodeMulticaMCPArguments(params.Arguments, &args) != nil {
		h.writeMulticaMCPError(w, req.ID, -32602, "invalid "+params.Name+" arguments")
		return
	}
	var machineIDString string
	if json.Unmarshal(args["machine_id"], &machineIDString) != nil || strings.TrimSpace(machineIDString) == "" {
		h.writeMulticaMCPError(w, req.ID, -32602, "machine_id is required")
		return
	}
	delete(args, "machine_id")
	binding, ok := h.runnerMCPBinding(w, r, req.ID, machineIDString)
	if !ok {
		return
	}
	if params.Name == "list_roots" {
		if len(args) != 0 {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid list_roots arguments")
			return
		}
		h.writeRunnerMCPResult(w, req.ID, map[string]any{"machine_id": machineIDString, "roots": decodeRunnerRoots(binding.Roots)})
		return
	}
	if binding.DisconnectedAt.Valid {
		h.writeRunnerMCPToolError(w, req.ID, "runner_disconnected", "The selected Runner binding is disconnected")
		return
	}
	if !runnerBindingOnline(binding.DisconnectedAt, binding.ConnectionID, binding.LastSeenAt, time.Now()) {
		h.writeRunnerMCPToolError(w, req.ID, "runner_offline", "The selected Runner machine is offline")
		return
	}
	forwarded, _ := json.Marshal(args)
	result, callErr := h.callRunnerMCP(r, binding, params.Name, forwarded)
	if callErr != nil {
		h.writeRunnerMCPToolError(w, req.ID, callErr.code, callErr.message)
		return
	}
	h.writeRunnerMCPResult(w, req.ID, result)
}

func (h *Handler) runnerMCPListMachines(w http.ResponseWriter, r *http.Request, id json.RawMessage) {
	workspaceID, agentID, ok := runnerMCPTaskScope(r)
	if !ok {
		h.writeRunnerMCPToolError(w, id, "runner_scope_invalid", "The authenticated task scope is invalid")
		return
	}
	rows, err := h.Queries.ListAgentRunnerBindings(r.Context(), db.ListAgentRunnerBindingsParams{WorkspaceID: workspaceID, AgentID: agentID})
	if err != nil {
		h.writeRunnerMCPToolError(w, id, "runner_query_failed", "Could not list Runner machines")
		return
	}
	machines := make([]map[string]any, 0, len(rows))
	now := time.Now()
	for _, row := range rows {
		online := runnerBindingOnline(row.DisconnectedAt, row.ConnectionID, row.LastSeenAt, now)
		machines = append(machines, map[string]any{
			"machine_id": uuidToString(row.MachineID), "name": row.Name,
			"os": row.Os, "arch": row.Arch, "online": online,
			"disconnected": row.DisconnectedAt.Valid,
		})
	}
	h.writeRunnerMCPResult(w, id, map[string]any{"machines": machines, "count": len(machines)})
}

func runnerMCPTaskScope(r *http.Request) (pgtype.UUID, pgtype.UUID, bool) {
	workspaceID, err := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-Workspace-ID")))
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	agentID, err := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-Agent-ID")))
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	return workspaceID, agentID, true
}

func (h *Handler) runnerMCPBinding(w http.ResponseWriter, r *http.Request, id json.RawMessage, machineIDString string) (db.GetActiveAgentRunnerBindingRow, bool) {
	workspaceID, agentID, ok := runnerMCPTaskScope(r)
	if !ok {
		h.writeRunnerMCPToolError(w, id, "runner_scope_invalid", "The authenticated task scope is invalid")
		return db.GetActiveAgentRunnerBindingRow{}, false
	}
	machineID, err := util.ParseUUID(machineIDString)
	if err != nil {
		h.writeMulticaMCPError(w, id, -32602, "machine_id must be a UUID")
		return db.GetActiveAgentRunnerBindingRow{}, false
	}
	binding, err := h.Queries.GetActiveAgentRunnerBinding(r.Context(), db.GetActiveAgentRunnerBindingParams{
		WorkspaceID: workspaceID, AgentID: agentID, MachineID: machineID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		h.writeRunnerMCPToolError(w, id, "runner_not_bound", "The selected machine is not bound to this Agent")
		return db.GetActiveAgentRunnerBindingRow{}, false
	}
	if err != nil {
		h.writeRunnerMCPToolError(w, id, "runner_query_failed", "Could not read the Runner binding")
		return db.GetActiveAgentRunnerBindingRow{}, false
	}
	return binding, true
}

type runnerMCPCallError struct {
	code    string
	message string
}

func (h *Handler) callRunnerMCP(r *http.Request, binding db.GetActiveAgentRunnerBindingRow, toolName string, arguments []byte) (json.RawMessage, *runnerMCPCallError) {
	taskID, err := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-Task-ID")))
	if err != nil {
		return nil, &runnerMCPCallError{code: "runner_scope_invalid", message: "The authenticated task scope is invalid"}
	}
	userID, err := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-User-ID")))
	if err != nil {
		return nil, &runnerMCPCallError{code: "runner_scope_invalid", message: "The authenticated user scope is invalid"}
	}
	expiresAt := time.Now().Add(runnerCallTimeout(toolName, arguments))
	call, err := h.Queries.CreateRunnerCall(r.Context(), db.CreateRunnerCallParams{
		WorkspaceID: binding.WorkspaceID,
		AgentID:     binding.AgentID,
		TaskID:      taskID,
		UserID:      userID,
		MachineID:   binding.MachineID,
		ToolName:    toolName,
		Arguments:   arguments,
		ExpiresAt:   pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &runnerMCPCallError{code: "runner_not_bound", message: "The selected machine is no longer bound to this Agent"}
	}
	if err != nil {
		return nil, &runnerMCPCallError{code: "runner_call_create_failed", message: "Could not create the Runner call"}
	}
	slog.Info("Runner call created",
		"event", "runner_call_created",
		"call_id", uuidToString(call.ID),
		"task_id", uuidToString(taskID),
		"agent_id", uuidToString(binding.AgentID),
		"machine_id", uuidToString(binding.MachineID),
		"tool_name", toolName,
	)
	h.notifyRunnerCall(uuidToString(binding.MachineID), uuidToString(call.ID))

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(time.Until(expiresAt))
	defer timer.Stop()
	for {
		select {
		case <-r.Context().Done():
			_ = h.Queries.ExpireRunnerCall(context.WithoutCancel(r.Context()), call.ID)
			h.notifyRunnerCallsCancelled(uuidToString(binding.MachineID), []pgtype.UUID{call.ID})
			return nil, &runnerMCPCallError{code: "runner_call_cancelled", message: "The Runner call was cancelled"}
		case <-timer.C:
			_ = h.Queries.ExpireRunnerCall(context.WithoutCancel(r.Context()), call.ID)
			h.notifyRunnerCallsCancelled(uuidToString(binding.MachineID), []pgtype.UUID{call.ID})
			return nil, &runnerMCPCallError{code: "runner_timeout", message: "The Runner did not finish before the deadline"}
		case <-ticker.C:
			current, getErr := h.Queries.GetRunnerCall(r.Context(), call.ID)
			if getErr != nil {
				return nil, &runnerMCPCallError{code: "runner_call_read_failed", message: "Could not read the Runner call"}
			}
			switch current.Status {
			case "succeeded":
				return json.RawMessage(current.Result), nil
			case "failed", "expired":
				code := current.ErrorCode.String
				if code == "" {
					code = "runner_call_failed"
				}
				message := current.ErrorMessage.String
				if message == "" {
					message = "The Runner call failed"
				}
				return nil, &runnerMCPCallError{code: code, message: message}
			}
		}
	}
}

func runnerCallTimeout(toolName string, arguments []byte) time.Duration {
	if toolName != "shell" {
		return runnerMCPCallTimeout
	}
	var shell struct {
		Background     bool `json:"background"`
		TimeoutSeconds int  `json:"timeout_seconds"`
	}
	if json.Unmarshal(arguments, &shell) != nil || shell.Background || shell.TimeoutSeconds <= 50 || shell.TimeoutSeconds > 600 {
		return runnerMCPCallTimeout
	}
	return time.Duration(shell.TimeoutSeconds)*time.Second + runnerMCPShellGracePeriod
}

func (h *Handler) writeRunnerMCPResult(w http.ResponseWriter, id json.RawMessage, result any) {
	payload, _ := json.Marshal(result)
	h.writeMulticaMCPResult(w, id, multicaMCPToolResult{
		Content:           []multicaMCPContent{{Type: "text", Text: string(payload)}},
		StructuredContent: result,
	})
}

func (h *Handler) writeRunnerMCPToolError(w http.ResponseWriter, id json.RawMessage, code, message string) {
	structured := map[string]string{"code": code, "message": message}
	payload, _ := json.Marshal(structured)
	h.writeMulticaMCPResult(w, id, multicaMCPToolResult{
		Content:           []multicaMCPContent{{Type: "text", Text: string(payload)}},
		StructuredContent: structured,
		IsError:           true,
	})
}

// injectDEAPA2ARunnerMCP exposes the Agent-bound local Runner MCP on DEAP
// A2A claims without putting a task token into AuthToken / MULTICA_TOKEN.
// Ordinary external A2A stays tokenless and does not see the owner's machines.
func (h *Handler) injectDEAPA2ARunnerMCP(
	ctx context.Context,
	runtime db.AgentRuntime,
	task db.AgentTaskQueue,
	workspaceID pgtype.UUID,
	agentData *TaskAgentData,
) error {
	if !service.ShouldInjectA2ARunnerMCP(task.Context) {
		return nil
	}
	if !runtime.OwnerID.Valid {
		return errors.New("DEAP Runner MCP requires a Runtime owner")
	}
	hasBindings, err := h.Queries.AgentHasRunnerBindings(ctx, task.AgentID)
	if err != nil {
		return err
	}
	if !hasBindings {
		return nil
	}
	token, err := auth.GenerateAgentTaskToken()
	if err != nil {
		return err
	}
	if err := h.injectRunnerMCP(ctx, runtime, task.AgentID, token, agentData); err != nil {
		return err
	}
	_, err = h.Queries.CreateTaskToken(ctx, db.CreateTaskTokenParams{
		TokenHash:   auth.HashToken(token),
		TaskID:      task.ID,
		AgentID:     task.AgentID,
		WorkspaceID: workspaceID,
		UserID:      runtime.OwnerID,
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	})
	return err
}

func (h *Handler) injectRunnerMCP(ctx context.Context, runtime db.AgentRuntime, agentID pgtype.UUID, taskToken string, agentData *TaskAgentData) error {
	if agentData == nil {
		return errors.New("claimed task is missing Agent data")
	}
	bindings, err := h.Queries.ListAgentRunnerBindings(ctx, db.ListAgentRunnerBindingsParams{WorkspaceID: runtime.WorkspaceID, AgentID: agentID})
	if err != nil {
		return err
	}
	publicURL := ""
	overlayServers := make(map[string]any)
	existingServers, err := mcpServerNames(agentData.McpConfig)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if !runnerBindingOnline(binding.DisconnectedAt, binding.ConnectionID, binding.LastSeenAt, time.Now()) {
			continue
		}
		inventory, inventoryOK := h.runnerMCPInventory(ctx, uuidToString(binding.MachineID))
		enabled := decodeEnabledRunnerMCPServers(binding.EnabledMcpServers)
		if !inventoryOK && len(enabled) > 0 {
			slog.Warn("Runner MCP inventory unavailable at task claim; using persisted enabled selection",
				"agent_id", uuidToString(agentID),
				"machine_id", uuidToString(binding.MachineID),
				"server_count", len(enabled),
			)
		}
		for name := range resolveEnabledRunnerMCPServers(enabled, inventory, inventoryOK) {
			if _, collision := existingServers[name]; collision {
				continue
			}
			if publicURL == "" {
				publicURL, err = runnerBaseURL(h.currentConfig().PublicURL)
				if err != nil {
					return errors.New("Runner MCP requires MULTICA_PUBLIC_URL")
				}
			}
			overlayServers[name] = map[string]any{
				"type": "http",
				"url": publicURL + "/api/runner-mcp/mounts/" + url.PathEscape(uuidToString(binding.BindingID)) + "/servers/" + url.PathEscape(name),
				"headers": map[string]string{
					"Authorization": "Bearer " + taskToken,
					runnerprotocol.ManagedMCPRoutingHeader: runnerprotocol.ManagedMCPRoutingValue,
				},
			}
		}
	}
	if len(overlayServers) == 0 {
		return nil
	}
	if runnerMCPRuntimeUnsupported(runtime) {
		return errRunnerMCPRuntimeUnsupported
	}
	overlay, err := json.Marshal(map[string]any{"mcpServers": overlayServers})
	if err != nil {
		return err
	}
	merged, err := mergeMCPOverlay(agentData.McpConfig, overlay)
	if err != nil {
		return err
	}
	agentData.McpConfig = merged
	return nil
}

func mcpServerNames(raw json.RawMessage) (map[string]struct{}, error) {
	if !hasManagedJSON(raw) {
		return map[string]struct{}{}, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse Agent MCP config: %w", err)
	}
	servers, err := unmarshalServerMap(document["mcpServers"])
	if err != nil {
		return nil, fmt.Errorf("parse Agent MCP servers: %w", err)
	}
	names := make(map[string]struct{}, len(servers))
	for name := range servers {
		names[name] = struct{}{}
	}
	return names, nil
}

func runnerMCPOverlay(publicURL, taskToken string) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			runnerprotocol.ManagedMCPServerName: map[string]any{
				"type": "http",
				"url":  publicURL + runnerprotocol.ManagedMCPPath,
				"headers": map[string]string{
					"Authorization":                        "Bearer " + taskToken,
					runnerprotocol.ManagedMCPRoutingHeader: runnerprotocol.ManagedMCPRoutingValue,
				},
			},
		},
	})
}
