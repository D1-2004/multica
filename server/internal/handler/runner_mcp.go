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

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/mcpprotocol"
	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

func mergeManagedMCPConfig(base, managed json.RawMessage) (json.RawMessage, []string, error) {
	return mcpprotocol.MergeManagedConfig(base, managed)
}

const (
	runnerMCPCallTimeout      = 60 * time.Second
	runnerMCPShellGracePeriod = 10 * time.Second
)

var errRunnerMCPRuntimeUnsupported = errors.New("runtime does not support managed MCP")
var errRunnerMCPMountsUnsupported = errors.New("sandbox daemon does not support dynamic Runner MCP mounts")

type runnerMountedMCPArguments struct {
	ServerName      string          `json:"server_name"`
	Fingerprint     string          `json:"fingerprint"`
	SessionKey      string          `json:"session_key"`
	ProtocolVersion string          `json:"protocol_version,omitempty"`
	Request         json.RawMessage `json:"request"`
}

// RunnerMountedMCP is a task-token-scoped transparent JSON-RPC relay. The
// server never receives the local command, URL, headers, environment, or
// credentials; it only routes to an available inventory fingerprint.
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
	snapshot := decodeEnabledRunnerMCPServers(bindingRecord.EnabledMcpServers)
	inventory, inventoryOK := h.runnerMCPInventory(r.Context(), uuidToString(bindingRecord.MachineID))
	expectedFingerprint, availableNow := resolveRunnerMCPServers(snapshot, inventory, inventoryOK)[serverName]
	if !availableNow {
		writeError(w, http.StatusConflict, "Runner MCP mount is unavailable or changed")
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
		SessionKey:      strings.Join([]string{r.Header.Get("X-Task-ID"), uuidToString(bindingID), serverName}, "/"),
		ProtocolVersion: strings.TrimSpace(r.Header.Get("MCP-Protocol-Version")),
		Request:         body,
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

func runnerMCPRuntimeUnsupported(runtime db.AgentRuntime) bool {
	return service.IsCloudSandboxRuntime(runtime) &&
		service.CloudSandboxRuntimeProvider(runtime) == "pi" &&
		!service.CloudSandboxRuntimeHasCapability(runtime, "mcp")
}

// RunnerMCP used to expose a fixed filesystem/shell tool bundle. It is no
// longer implemented by the backend. Keep the old route closed so existing
// clients cannot bypass the dynamic mounted-server relay.
func (h *Handler) RunnerMCP(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "legacy Runner MCP is disabled; select a Local Runner on the Agent")
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
		slog.Error("Runner call create failed",
			"event", "runner_call_create_failed",
			"task_id", uuidToString(taskID),
			"agent_id", uuidToString(binding.AgentID),
			"machine_id", uuidToString(binding.MachineID),
			"tool_name", toolName,
			"error", err,
		)
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
	if toolName != "mcp" {
		return runnerMCPCallTimeout
	}
	var mounted runnerMountedMCPArguments
	if json.Unmarshal(arguments, &mounted) != nil || mounted.ServerName != runnerprotocol.BuiltinMachineMCPServerName {
		return runnerMCPCallTimeout
	}
	var request struct {
		Method string `json:"method"`
		Params struct {
			Name      string `json:"name"`
			Arguments struct {
				Background     bool `json:"background"`
				TimeoutSeconds int  `json:"timeout_seconds"`
			} `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(mounted.Request, &request) != nil || request.Method != "tools/call" || request.Params.Name != "shell" {
		return runnerMCPCallTimeout
	}
	shell := request.Params.Arguments
	if shell.Background || shell.TimeoutSeconds <= 50 || shell.TimeoutSeconds > 600 {
		return runnerMCPCallTimeout
	}
	return time.Duration(shell.TimeoutSeconds)*time.Second + runnerMCPShellGracePeriod
}

// injectDEAPA2ARunnerMCP exposes backend-hosted MCP and any Agent-bound local
// Runner MCP on DEAP A2A claims without putting a task token into AuthToken /
// MULTICA_TOKEN. Ordinary external A2A stays tokenless.
func (h *Handler) injectDEAPA2ARunnerMCP(
	ctx context.Context,
	runtime db.AgentRuntime,
	task db.AgentTaskQueue,
	workspaceID pgtype.UUID,
	agentData *TaskAgentData,
	supportsRunnerMCPMounts bool,
	supportsManagedRelayRoutes bool,
) error {
	if !service.ShouldInjectA2ARunnerMCP(task.Context) {
		return nil
	}
	if !runtime.OwnerID.Valid {
		return errors.New("DEAP Runner MCP requires a Runtime owner")
	}
	token, err := auth.GenerateAgentTaskToken()
	if err != nil {
		return err
	}
	// A2A-origin tasks resolve to the global connector layer only.
	if err := h.injectRunnerMCP(ctx, runtime, task, token, agentData, supportsRunnerMCPMounts, supportsManagedRelayRoutes); err != nil {
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

// injectRunnerMCP mounts the backend-hosted multica MCP, the task's internal
// connectors (global grants plus the task's scene/personal context layers,
// see authorizedTaskConnectors) and any Agent-bound Runner MCP servers.
func (h *Handler) injectRunnerMCP(ctx context.Context, runtime db.AgentRuntime, task db.AgentTaskQueue, taskToken string, agentData *TaskAgentData, supportsRunnerMCPMounts, supportsManagedRelayRoutes bool) error {
	if agentData == nil {
		return errors.New("claimed task is missing Agent data")
	}
	agentID := task.AgentID
	if !supportsRunnerMCPMounts || !supportsManagedRelayRoutes {
		slog.WarnContext(ctx, "internal MCP connectors unavailable: sandbox daemon lacks managed MCP support", "agent_id", uuidToString(agentID), "runtime_id", uuidToString(runtime.ID), "supports_runner_mcp_mounts", supportsRunnerMCPMounts, "supports_managed_relay_routes", supportsManagedRelayRoutes)
		return h.injectLegacyRunnerMCP(ctx, runtime, agentID, taskToken, agentData, supportsRunnerMCPMounts)
	}
	if runnerMCPRuntimeUnsupported(runtime) {
		return errRunnerMCPRuntimeUnsupported
	}
	publicURL, err := runnerBaseURL(h.currentConfig().PublicURL)
	if err != nil {
		return errors.New("managed MCP requires MULTICA_PUBLIC_URL")
	}
	managedServers := map[string]any{
		"multica": map[string]any{
			"type": "http", "url": publicURL + "/api/mcp",
			"headers": map[string]string{"Authorization": "Bearer " + taskToken},
		},
	}
	routes := map[string]MCPRelayRoute{
		"multica": {Path: "/api/mcp", Authorization: "Bearer " + taskToken},
	}
	// The current scene's configuration server, bound to this task and scene
	// by the scene token in its path (scene_config_mcp.go).
	if path, ok := h.sceneConfigMCPRoute(ctx, runtime.WorkspaceID, task); ok {
		managedServers[sceneConfigMCPServerName] = map[string]any{
			"type": "http", "url": publicURL + path,
			"headers": map[string]string{"Authorization": "Bearer " + taskToken},
		}
		routes[sceneConfigMCPServerName] = MCPRelayRoute{Path: path, Authorization: "Bearer " + taskToken}
	}
	connectors, err := h.authorizedTaskConnectors(ctx, runtime.WorkspaceID, task)
	if err != nil {
		slog.WarnContext(ctx, "internal MCP connector discovery failed; continuing task claim without connectors", "agent_id", uuidToString(agentID), "runtime_id", uuidToString(runtime.ID), "error", err)
		connectors = nil
	}
	for _, connector := range connectors {
		if validateConnectorInput(connectorInput{Name: connector.Name, UpstreamURL: connector.UpstreamURL, AllowedTools: connector.AllowedTools, AgentIDs: []string{uuidToString(agentID)}, Enabled: true}) != nil {
			continue
		}
		name := connectorServerName(connector.ID)
		if name == "" {
			continue
		}
		if _, exists := managedServers[name]; exists {
			slog.WarnContext(ctx, "internal MCP connector server name collision; skipping connector", "connector_id", connector.ID, "server_name", name)
			continue
		}
		path := "/api/internal-connectors/" + connector.ID + "/mcp"
		managedServers[name] = map[string]any{
			"type": "http", "url": publicURL + path,
			"headers": map[string]string{"Authorization": "Bearer " + taskToken},
		}
		routes[name] = MCPRelayRoute{Path: path, Authorization: "Bearer " + taskToken}
	}
	backendConfig, err := json.Marshal(map[string]any{"mcpServers": managedServers})
	if err != nil {
		return err
	}
	effectiveConfig, _, err := mergeManagedMCPConfig(agentData.McpConfig, backendConfig)
	if err != nil {
		return err
	}
	bindings, err := h.Queries.ListAgentRunnerBindings(ctx, db.ListAgentRunnerBindingsParams{WorkspaceID: runtime.WorkspaceID, AgentID: agentID})
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		rawConfig, _, found, loadErr := h.loadRunnerMCPConfig(ctx, binding.MachineID)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			continue
		}
		effectiveConfig, agentData.contextMCPServers = dropContextMCPServersShadowingRunner(ctx, task, effectiveConfig, rawConfig, agentData.contextMCPServers)
		var names []string
		effectiveConfig, names, err = mergeManagedMCPConfig(effectiveConfig, rawConfig)
		if err != nil {
			return err
		}
		for _, name := range names {
			routes[name] = MCPRelayRoute{
				Path:          "/api/runner-mcp/mounts/" + url.PathEscape(uuidToString(binding.BindingID)) + "/servers/" + url.PathEscape(name),
				Authorization: "Bearer " + taskToken,
			}
		}
	}
	agentData.McpConfig = effectiveConfig
	agentData.McpRelayRoutes = routes
	return nil
}

// dropContextMCPServersShadowingRunner leaves out of config the custom MCP
// servers of the task's context layers (contextServers) that the Runner MCP
// config runner also defines, so a scene, person or org server never makes
// the managed merge fail the claim (mcp_server_name_conflict) and requeue
// it forever: the agent's Runner mount keeps its name. It returns the
// context servers still in config. A config it cannot read is returned as
// it is, for the merge to report.
func dropContextMCPServersShadowingRunner(ctx context.Context, task db.AgentTaskQueue, config, runner json.RawMessage, contextServers []string) (json.RawMessage, []string) {
	if len(contextServers) == 0 {
		return config, contextServers
	}
	runnerNames, err := mcpServerNames(runner)
	if err != nil {
		return config, contextServers
	}
	var dropped, kept []string
	for _, name := range contextServers {
		if _, shadowed := runnerNames[name]; shadowed {
			dropped = append(dropped, name)
		} else {
			kept = append(kept, name)
		}
	}
	if len(dropped) == 0 {
		return config, contextServers
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(config, &document); err != nil || document == nil {
		return config, contextServers
	}
	servers, err := unmarshalServerMap(document["mcpServers"])
	if err != nil {
		return config, contextServers
	}
	for _, name := range dropped {
		delete(servers, name)
	}
	encoded, err := json.Marshal(servers)
	if err != nil {
		return config, contextServers
	}
	document["mcpServers"] = encoded
	merged, err := json.Marshal(document)
	if err != nil {
		return config, contextServers
	}
	slog.WarnContext(ctx, "context builder: custom MCP servers share a Runner MCP server name; the Runner mount keeps the name",
		"task_id", uuidToString(task.ID), "agent_id", uuidToString(task.AgentID), "servers", dropped)
	return merged, kept
}

func (h *Handler) injectLegacyRunnerMCP(ctx context.Context, runtime db.AgentRuntime, agentID pgtype.UUID, taskToken string, agentData *TaskAgentData, supportsRunnerMCPMounts bool) error {
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
		for name := range resolveRunnerMCPServers(decodeEnabledRunnerMCPServers(binding.EnabledMcpServers), inventory, inventoryOK) {
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
				"url":  publicURL + "/api/runner-mcp/mounts/" + url.PathEscape(uuidToString(binding.BindingID)) + "/servers/" + url.PathEscape(name),
				"headers": map[string]string{
					"Authorization":                        "Bearer " + taskToken,
					runnerprotocol.ManagedMCPRoutingHeader: runnerprotocol.ManagedMCPRoutingValue,
				},
			}
		}
	}
	if len(overlayServers) == 0 {
		return nil
	}
	// Old daemons can still execute tasks that need no dynamic Runner mounts.
	// Require mount routing only once an effective mount would be injected.
	if !supportsRunnerMCPMounts {
		return errRunnerMCPMountsUnsupported
	}
	if runnerMCPRuntimeUnsupported(runtime) {
		return errRunnerMCPRuntimeUnsupported
	}
	overlay, err := json.Marshal(map[string]any{"mcpServers": overlayServers})
	if err != nil {
		return err
	}
	agentData.McpConfig, err = mergeMCPOverlay(agentData.McpConfig, overlay)
	return err
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
