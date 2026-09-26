package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
)

type workspaceMCPTool struct {
	name, method, path, scope string
	body                      bool
	fields                    map[string]any
}

// Every target is a fixed business API route. A caller cannot provide an HTTP
// method, URL, or command text. The ordinary route/membership/handler checks
// run again for each tool call under the token's real member identity.
var workspaceMCPTools = []workspaceMCPTool{
	{"issue_list", "GET", "/api/issues/", "read", false, nil},
	{"issue_search", "GET", "/api/issues/search", "read", false, nil},
	{"issue_get", "GET", "/api/issues/{id}", "read", false, nil},
	{"agent_get_issue", "GET", "/api/issues/{id}", "read", false, nil},
	{"issue_create", "POST", "/api/issues/", "write", true, map[string]any{"title": map[string]any{"type": "string", "minLength": 1}, "description": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "priority": map[string]any{"type": "string"}, "assignee_type": map[string]any{"type": "string"}, "assignee_id": map[string]any{"type": "string", "format": "uuid"}, "parent_issue_id": map[string]any{"type": "string", "format": "uuid"}, "project_id": map[string]any{"type": "string", "format": "uuid"}, "stage": map[string]any{"type": "integer", "minimum": 1}, "start_date": map[string]any{"type": "string"}, "due_date": map[string]any{"type": "string"}, "attachment_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "label_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}},
	{"agent_delegate_task", "POST", "/api/issues/", "write", true, map[string]any{"title": map[string]any{"type": "string", "minLength": 1}, "description": map[string]any{"type": "string"}, "assignee_type": map[string]any{"type": "string", "enum": []string{"agent"}}, "assignee_id": map[string]any{"type": "string", "format": "uuid"}}},
	{"issue_update", "PUT", "/api/issues/{id}", "write", true, nil},
	{"issue_status", "PUT", "/api/issues/{id}", "write", true, map[string]any{"status": map[string]any{"type": "string"}}},
	{"issue_assign", "PUT", "/api/issues/{id}", "write", true, map[string]any{"assignee_type": map[string]any{"type": "string", "enum": []string{"member", "agent", "squad"}}, "assignee_id": map[string]any{"type": "string", "format": "uuid"}, "suppress_run": map[string]any{"type": "boolean"}}},
	{"issue_delete", "DELETE", "/api/issues/{id}", "manage", false, nil},
	{"issue_children", "GET", "/api/issues/{id}/children", "read", false, nil},
	{"issue_comment_list", "GET", "/api/issues/{id}/comments", "read", false, nil},
	{"issue_comment_add", "POST", "/api/issues/{id}/comments", "write", true, map[string]any{"content": map[string]any{"type": "string", "minLength": 1}, "parent_id": map[string]any{"type": "string", "format": "uuid"}}},
	{"agent_continue_issue", "POST", "/api/issues/{id}/comments", "write", true, map[string]any{"content": map[string]any{"type": "string", "minLength": 1}}},
	{"issue_comment_update", "PUT", "/api/comments/{commentId}", "write", true, nil},
	{"issue_comment_delete", "DELETE", "/api/comments/{commentId}", "write", false, nil},
	{"issue_comment_resolve", "POST", "/api/comments/{commentId}/resolve", "write", false, nil},
	{"issue_comment_unresolve", "DELETE", "/api/comments/{commentId}/resolve", "write", false, nil},
	{"issue_runs", "GET", "/api/issues/{id}/task-runs", "read", false, nil},
	{"issue_run_status", "GET", "/api/issues/{id}/task-runs", "read", false, nil},
	{"issue_run_messages", "GET", "/api/tasks/{taskId}/messages", "read", false, nil},
	{"issue_timeline", "GET", "/api/issues/{id}/timeline", "read", false, nil},
	{"issue_usage", "GET", "/api/issues/{id}/usage", "read", false, nil},
	{"issue_pull_requests", "GET", "/api/issues/{id}/pull-requests", "read", false, nil},
	{"agent_list_artifacts", "GET", "/api/issues/{id}/attachments", "read", false, nil},
	{"issue_rerun", "POST", "/api/issues/{id}/rerun", "write", true, map[string]any{"task_id": map[string]any{"type": "string", "format": "uuid"}}},
	{"issue_cancel_task", "POST", "/api/issues/{id}/tasks/{taskId}/cancel", "write", false, nil},
	{"issue_labels", "GET", "/api/issues/{id}/labels", "read", false, nil},
	{"issue_label_add", "POST", "/api/issues/{id}/labels", "write", true, nil},
	{"issue_label_remove", "DELETE", "/api/issues/{id}/labels/{labelId}", "write", false, nil},
	{"issue_metadata", "GET", "/api/issues/{id}/metadata", "read", false, nil},
	{"issue_metadata_set", "PUT", "/api/issues/{id}/metadata/{key}", "write", true, nil},
	{"issue_metadata_delete", "DELETE", "/api/issues/{id}/metadata/{key}", "write", false, nil},
	{"issue_property_set", "PUT", "/api/issues/{id}/properties/{propertyId}", "write", true, nil},
	{"issue_property_unset", "DELETE", "/api/issues/{id}/properties/{propertyId}", "write", false, nil},
	{"issue_reorder", "POST", "/api/issues/{id}/move", "write", true, nil},
	{"issue_subscribers", "GET", "/api/issues/{id}/subscribers", "read", false, nil},
	{"issue_subscribe", "POST", "/api/issues/{id}/subscribe", "write", false, nil},
	{"issue_unsubscribe", "POST", "/api/issues/{id}/unsubscribe", "write", false, nil},
	{"agent_list", "GET", "/api/agents/", "read", false, nil},
	{"agent_get", "GET", "/api/agents/{id}", "read", false, nil},
	{"agent_describe_agent", "GET", "/api/agents/{id}", "read", false, nil},
	{"agent_create", "POST", "/api/agents/", "manage", true, nil},
	{"agent_update", "PUT", "/api/agents/{id}", "manage", true, nil},
	{"agent_archive", "POST", "/api/agents/{id}/archive", "manage", false, nil},
	{"agent_restore", "POST", "/api/agents/{id}/restore", "manage", false, nil},
	{"agent_tasks", "GET", "/api/agents/{id}/tasks", "read", false, nil},
	{"agent_skills", "GET", "/api/agents/{id}/skills", "read", false, nil},
	{"agent_skills_set", "PUT", "/api/agents/{id}/skills", "manage", true, nil},
	{"agent_skills_add", "POST", "/api/agents/{id}/skills/add", "manage", true, nil},
	{"agent_skills_remove", "DELETE", "/api/agents/{id}/skills/{skillId}", "manage", false, nil},
	{"runtime_list", "GET", "/api/runtimes/", "read", false, nil},
	{"runtime_activity", "GET", "/api/runtimes/{runtimeId}/activity", "read", false, nil},
	{"runtime_usage", "GET", "/api/runtimes/{runtimeId}/usage", "read", false, nil},
	{"runtime_rename", "PATCH", "/api/runtimes/{runtimeId}", "manage", true, map[string]any{"custom_name": map[string]any{"type": "string"}}},
	{"runtime_update", "POST", "/api/runtimes/{runtimeId}/update", "manage", true, nil},
	{"runtime_config_update", "PATCH", "/api/runtimes/{runtimeId}", "manage", true, nil},
	{"runtime_delete", "DELETE", "/api/runtimes/{runtimeId}", "manage", false, nil},
	{"runtime_profile_list", "GET", "/api/workspaces/{workspaceId}/runtime-profiles", "read", false, nil},
	{"runtime_profile_get", "GET", "/api/workspaces/{workspaceId}/runtime-profiles/{profileId}", "read", false, nil},
	{"runtime_profile_create", "POST", "/api/workspaces/{workspaceId}/runtime-profiles", "manage", true, nil},
	{"runtime_profile_update", "PATCH", "/api/workspaces/{workspaceId}/runtime-profiles/{profileId}", "manage", true, nil},
	{"runtime_profile_delete", "DELETE", "/api/workspaces/{workspaceId}/runtime-profiles/{profileId}", "manage", false, nil},
	{"workspace_get", "GET", "/api/workspaces/{workspaceId}", "read", false, nil},
	{"workspace_members", "GET", "/api/workspaces/{workspaceId}/members", "read", false, nil},
	{"workspace_mcp", "GET", "/api/workspaces/{workspaceId}/mcp", "read", false, nil},
	{"workspace_update", "PUT", "/api/workspaces/{workspaceId}", "manage", true, nil},
	{"workspace_member_invite", "POST", "/api/workspaces/{workspaceId}/members", "manage", true, nil},
	{"project_list", "GET", "/api/projects/", "read", false, nil},
	{"project_get", "GET", "/api/projects/{id}", "read", false, nil},
	{"project_create", "POST", "/api/projects/", "write", true, nil},
	{"project_update", "PUT", "/api/projects/{id}", "write", true, nil},
	{"project_status", "PUT", "/api/projects/{id}", "write", true, map[string]any{"status": map[string]any{"type": "string"}}},
	{"project_delete", "DELETE", "/api/projects/{id}", "manage", false, nil},
	{"project_resources", "GET", "/api/projects/{id}/resources", "read", false, nil},
	{"project_resource_add", "POST", "/api/projects/{id}/resources", "write", true, nil},
	{"project_resource_update", "PUT", "/api/projects/{id}/resources/{resourceId}", "write", true, nil},
	{"project_resource_remove", "DELETE", "/api/projects/{id}/resources/{resourceId}", "write", false, nil},
	{"squad_list", "GET", "/api/squads/", "read", false, nil},
	{"squad_get", "GET", "/api/squads/{id}", "read", false, nil},
	{"squad_create", "POST", "/api/squads/", "manage", true, nil},
	{"squad_update", "PUT", "/api/squads/{id}", "manage", true, nil},
	{"squad_delete", "DELETE", "/api/squads/{id}", "manage", false, nil},
	{"squad_members", "GET", "/api/squads/{id}/members", "read", false, nil},
	{"squad_member_add", "POST", "/api/squads/{id}/members", "manage", true, nil},
	{"squad_member_remove", "DELETE", "/api/squads/{id}/members", "manage", true, nil},
	{"squad_member_set_role", "PATCH", "/api/squads/{id}/members/role", "manage", true, nil},
	{"squad_activity", "POST", "/api/issues/{id}/squad-evaluated", "write", true, nil},
	{"autopilot_list", "GET", "/api/autopilots/", "read", false, nil},
	{"autopilot_get", "GET", "/api/autopilots/{id}", "read", false, nil},
	{"autopilot_create", "POST", "/api/autopilots/", "manage", true, nil},
	{"autopilot_update", "PATCH", "/api/autopilots/{id}", "manage", true, nil},
	{"autopilot_delete", "DELETE", "/api/autopilots/{id}", "manage", false, nil},
	{"autopilot_trigger_list", "GET", "/api/autopilots/{id}", "read", false, nil},
	{"autopilot_runs", "GET", "/api/autopilots/{id}/runs", "read", false, nil},
	{"autopilot_trigger", "POST", "/api/autopilots/{id}/trigger", "manage", false, nil},
	{"autopilot_trigger_add", "POST", "/api/autopilots/{id}/triggers", "manage", true, nil},
	{"autopilot_trigger_update", "PATCH", "/api/autopilots/{id}/triggers/{triggerId}", "manage", true, nil},
	{"autopilot_trigger_delete", "DELETE", "/api/autopilots/{id}/triggers/{triggerId}", "manage", false, nil},
	{"autopilot_trigger_rotate_url", "POST", "/api/autopilots/{id}/triggers/{triggerId}/rotate-webhook-token", "manage", false, nil},
	{"autopilot_deliveries", "GET", "/api/autopilots/{id}/deliveries", "read", false, nil},
	{"skill_list", "GET", "/api/skills/", "read", false, nil},
	{"skill_search_workspace", "GET", "/api/skills/search", "read", false, nil},
	{"skill_get", "GET", "/api/skills/{id}", "read", false, nil},
	{"skill_create", "POST", "/api/skills/", "manage", true, nil},
	{"skill_update", "PUT", "/api/skills/{id}", "manage", true, nil},
	{"skill_delete", "DELETE", "/api/skills/{id}", "manage", false, nil},
	{"skill_files", "GET", "/api/skills/{id}/files", "read", false, nil},
	{"skill_file_upsert", "PUT", "/api/skills/{id}/files", "manage", true, nil},
	{"skill_file_delete", "DELETE", "/api/skills/{id}/files/{fileId}", "manage", false, nil},
	{"skill_label_add", "POST", "/api/skills/{id}/labels", "manage", true, nil},
	{"skill_label_remove", "DELETE", "/api/skills/{id}/labels/{labelId}", "manage", false, nil},
	{"label_list", "GET", "/api/labels/", "read", false, nil},
	{"label_create", "POST", "/api/labels/", "write", true, nil},
	{"label_get", "GET", "/api/labels/{id}", "read", false, nil},
	{"label_update", "PUT", "/api/labels/{id}", "write", true, nil},
	{"label_delete", "DELETE", "/api/labels/{id}", "manage", false, nil},
	{"property_list", "GET", "/api/properties/", "read", false, nil},
	{"property_create", "POST", "/api/properties/", "manage", true, nil},
	{"property_get", "GET", "/api/properties/{id}", "read", false, nil},
	{"property_update", "PATCH", "/api/properties/{id}", "manage", true, nil},
	{"property_archive", "PATCH", "/api/properties/{id}", "manage", true, map[string]any{"archived": map[string]any{"const": true}}},
	{"property_unarchive", "PATCH", "/api/properties/{id}", "manage", true, map[string]any{"archived": map[string]any{"const": false}}},
}

func workspaceMCPToolAllowed(tool workspaceMCPTool, scopes []string, role string) bool {
	if !middleware.WorkspaceMCPHasScope(scopes, tool.scope) {
		return false
	}
	return tool.scope != "manage" || role == "owner" || role == "admin"
}

func workspaceMCPQueryFields(name string) map[string]any {
	var keys []string
	switch name {
	case "issue_list":
		keys = []string{"status", "priority", "assignee_type", "assignee_id", "project_id", "parent_issue_id", "limit", "offset"}
	case "issue_search":
		keys = []string{"q", "limit"}
	case "issue_comment_list":
		keys = []string{"roots_only", "summary", "compact", "thread", "tail", "since"}
	case "issue_runs", "agent_tasks", "autopilot_runs":
		keys = []string{"limit"}
	case "skill_search_workspace":
		keys = []string{"q"}
	}
	if len(keys) == 0 {
		return nil
	}
	fields := make(map[string]any, len(keys))
	for _, key := range keys {
		fields[key] = map[string]any{"type": "string"}
	}
	return fields
}

// workspaceMCPPayloadOptional lists body tools whose API also accepts an empty
// body, so callers may omit payload entirely.
func workspaceMCPPayloadOptional(name string) bool {
	return name == "issue_rerun"
}

func workspaceMCPToolDefinition(tool workspaceMCPTool) map[string]any {
	properties := map[string]any{}
	required := []string{}
	for _, segment := range strings.Split(tool.path, "/") {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			key := strings.Trim(segment, "{}")
			if key == "workspaceId" {
				continue
			}
			properties[key] = map[string]any{"type": "string", "minLength": 1}
			required = append(required, key)
		}
	}
	if tool.body {
		body := map[string]any{"type": "object"}
		if tool.fields != nil {
			body["properties"] = tool.fields
			body["additionalProperties"] = false
		}
		properties["payload"] = body
		if !workspaceMCPPayloadOptional(tool.name) {
			required = append(required, "payload")
		}
	}
	if queryFields := workspaceMCPQueryFields(tool.name); queryFields != nil {
		properties["query"] = map[string]any{"type": "object", "properties": queryFields, "additionalProperties": false}
	}
	return map[string]any{
		"name": tool.name, "description": "Workspace-scoped Multica " + strings.ReplaceAll(tool.name, "_", " ") + "; native business authorization applies.",
		"inputSchema": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false},
		"annotations": map[string]any{"readOnlyHint": tool.method == "GET", "destructiveHint": tool.method == "DELETE"},
	}
}

func (h *Handler) WorkspaceMCP(w http.ResponseWriter, r *http.Request) {
	principal, ok := middleware.WorkspaceMCPPrincipalFromContext(r.Context())
	if !ok || r.Header.Get("X-Actor-Source") != "workspace_mcp_token" {
		writeError(w, http.StatusUnauthorized, "workspace MCP token required")
		return
	}
	workspaceID := chi.URLParam(r, "workspaceId")
	if workspaceID == "" || workspaceID != util.UUIDToString(principal.WorkspaceID) {
		writeError(w, http.StatusForbidden, "workspace mismatch")
		return
	}
	if !h.multicaMCPOriginAllowed(r) {
		writeError(w, http.StatusForbidden, "untrusted MCP Origin")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, multicaMCPMaxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	var req multicaMCPRequest
	if err := decoder.Decode(&req); err != nil {
		h.writeMulticaMCPError(w, nil, -32700, "parse error")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		h.writeMulticaMCPError(w, req.ID, -32600, "invalid request")
		return
	}
	if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
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
		h.handleMulticaMCPInitialize(w, req)
	case "ping":
		h.writeMulticaMCPResult(w, req.ID, map[string]any{})
	case "tools/list":
		definitions := make([]any, 0, len(workspaceMCPTools))
		for _, tool := range workspaceMCPTools {
			if workspaceMCPToolAllowed(tool, principal.Scopes, principal.Role) {
				definitions = append(definitions, workspaceMCPToolDefinition(tool))
			}
		}
		h.writeMulticaMCPResult(w, req.ID, map[string]any{"tools": definitions})
	case "tools/call":
		h.callWorkspaceMCPTool(w, r, req)
	default:
		h.writeMulticaMCPError(w, req.ID, -32601, "method not found")
	}
}

func (h *Handler) callWorkspaceMCPTool(w http.ResponseWriter, r *http.Request, req multicaMCPRequest) {
	principal, _ := middleware.WorkspaceMCPPrincipalFromContext(r.Context())
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		h.writeMulticaMCPError(w, req.ID, -32602, "invalid parameters")
		return
	}
	var selected *workspaceMCPTool
	for i := range workspaceMCPTools {
		if workspaceMCPTools[i].name == params.Name {
			selected = &workspaceMCPTools[i]
			break
		}
	}
	if selected == nil || !workspaceMCPToolAllowed(*selected, principal.Scopes, principal.Role) {
		h.writeMulticaMCPToolError(w, req.ID, "tool unavailable")
		return
	}
	if len(params.Arguments) == 0 {
		params.Arguments = []byte("{}")
	}
	var args struct {
		Payload json.RawMessage   `json:"payload"`
		Query   map[string]string `json:"query"`
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params.Arguments, &raw); err != nil || raw == nil {
		h.writeMulticaMCPError(w, req.ID, -32602, "invalid arguments")
		return
	}
	if err := json.Unmarshal(params.Arguments, &args); err != nil {
		h.writeMulticaMCPError(w, req.ID, -32602, "invalid arguments")
		return
	}
	allowedArgs := map[string]bool{}
	for _, segment := range strings.Split(selected.path, "/") {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			key := strings.Trim(segment, "{}")
			if key != "workspaceId" {
				allowedArgs[key] = true
			}
		}
	}
	if selected.body {
		allowedArgs["payload"] = true
	}
	if workspaceMCPQueryFields(selected.name) != nil {
		allowedArgs["query"] = true
	}
	for key := range raw {
		if !allowedArgs[key] {
			h.writeMulticaMCPError(w, req.ID, -32602, "unsupported argument: "+key)
			return
		}
	}
	path := selected.path
	for _, segment := range strings.Split(selected.path, "/") {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		key := strings.Trim(segment, "{}")
		value := ""
		if key == "workspaceId" {
			value = util.UUIDToString(principal.WorkspaceID)
		} else if err := json.Unmarshal(raw[key], &value); err != nil {
			h.writeMulticaMCPError(w, req.ID, -32602, "missing path argument: "+key)
			return
		}
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "/\\?#") {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid path argument: "+key)
			return
		}
		path = strings.Replace(path, segment, url.PathEscape(value), 1)
	}
	payloadOmitted := len(args.Payload) == 0 && workspaceMCPPayloadOptional(selected.name)
	if selected.body && !payloadOmitted && (len(args.Payload) == 0 || !json.Valid(args.Payload) || args.Payload[0] != '{') {
		h.writeMulticaMCPError(w, req.ID, -32602, "payload object required")
		return
	}
	if selected.fields != nil && !payloadOmitted {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(args.Payload, &fields); err != nil {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid payload")
			return
		}
		for key := range fields {
			if _, allowed := selected.fields[key]; !allowed {
				h.writeMulticaMCPError(w, req.ID, -32602, "unsupported payload field: "+key)
				return
			}
		}
	}
	if selected.name == "issue_status" || selected.name == "issue_assign" || selected.name == "project_status" {
		var body map[string]json.RawMessage
		_ = json.Unmarshal(args.Payload, &body)
		required := "status"
		if selected.name == "issue_assign" {
			required = "assignee_type"
		}
		if len(body[required]) == 0 {
			h.writeMulticaMCPError(w, req.ID, -32602, "missing "+required)
			return
		}
	}
	if selected.name == "property_archive" || selected.name == "property_unarchive" {
		var body struct {
			Archived *bool `json:"archived"`
		}
		wantArchived := selected.name == "property_archive"
		if json.Unmarshal(args.Payload, &body) != nil || body.Archived == nil || *body.Archived != wantArchived {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid archived value")
			return
		}
	}
	if selected.name == "agent_delegate_task" {
		var body struct {
			AssigneeType string `json:"assignee_type"`
			AssigneeID   string `json:"assignee_id"`
		}
		if json.Unmarshal(args.Payload, &body) != nil || body.AssigneeType != "agent" || body.AssigneeID == "" {
			h.writeMulticaMCPError(w, req.ID, -32602, "agent_delegate_task requires an agent assignee")
			return
		}
	}
	if len(args.Query) > 0 {
		values := url.Values{}
		queryFields := workspaceMCPQueryFields(selected.name)
		for key, value := range args.Query {
			if _, allowed := queryFields[key]; !allowed || strings.ContainsAny(key, "&=?") {
				h.writeMulticaMCPError(w, req.ID, -32602, "invalid query key")
				return
			}
			values.Set(key, value)
		}
		path += "?" + values.Encode()
	}
	if h.WorkspaceMCPDispatcher == nil {
		h.writeMulticaMCPToolError(w, req.ID, "workspace MCP dispatcher unavailable")
		return
	}
	if err := h.Queries.RecordWorkspaceMCPCall(r.Context(), principal, selected.name, strings.SplitN(path, "?", 2)[0], "started"); err != nil {
		h.writeMulticaMCPToolError(w, req.ID, "audit unavailable")
		return
	}
	dispatchContext := middleware.WithWorkspaceMCPDispatch(r.Context())
	dispatchContext = context.WithValue(dispatchContext, chi.RouteCtxKey, chi.NewRouteContext())
	subrequest, err := http.NewRequestWithContext(dispatchContext, selected.method, path, bytes.NewReader(args.Payload))
	if err != nil {
		h.writeMulticaMCPToolError(w, req.ID, "invalid tool request")
		return
	}
	subrequest.Header = r.Header.Clone()
	subrequest.Header.Set("Content-Type", "application/json")
	subrequest.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()
	h.WorkspaceMCPDispatcher.ServeHTTP(recorder, subrequest)
	response := recorder.Result()
	defer response.Body.Close()
	resultBytes, _ := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if len(bytes.TrimSpace(resultBytes)) == 0 {
		// 204 and other empty responses (every DELETE route) still owe the
		// client a readable text block, not an empty string.
		resultBytes, _ = json.Marshal(map[string]any{"ok": response.StatusCode < http.StatusBadRequest, "status": response.StatusCode})
	}
	result := "success"
	if response.StatusCode >= http.StatusBadRequest {
		result = "denied"
	}
	if err := h.Queries.RecordWorkspaceMCPCall(r.Context(), principal, selected.name, strings.SplitN(path, "?", 2)[0], result); err != nil {
		slog.Warn("workspace MCP audit completion failed", "tool", selected.name, "error", err)
	}
	var structured any
	if json.Unmarshal(resultBytes, &structured) != nil {
		structured = map[string]any{"text": string(resultBytes)}
	}
	h.writeMulticaMCPResult(w, req.ID, multicaMCPToolResult{
		Content:           []multicaMCPContent{{Type: "text", Text: string(resultBytes)}},
		StructuredContent: map[string]any{"status": response.StatusCode, "data": structured},
		IsError:           response.StatusCode >= http.StatusBadRequest,
	})
}
