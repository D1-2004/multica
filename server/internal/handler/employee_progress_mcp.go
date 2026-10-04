package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

const employeeProgressMCPServerName = "employee-progress"
const employeeProgressMCPPath = "/api/employee-progress/mcp"
const employeeProgressMCPTool = "report_progress"

func employeeProgressToolDefinition() map[string]any {
	return map[string]any{
		"name":        employeeProgressMCPTool,
		"description": "Report one readable, meaningful stage, finding or blocker of this currently running task. This submits a progress candidate; the EmployeeLoop may show it or stay quiet. It does not send a message, finish work, grant permission or verify your business claims. Use a stable report_id for retries; do not report thinking, raw tools, secrets, token fragments or unsupported percentages. Final results remain separate.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"report_id": map[string]any{"type": "string", "maxLength": 128}, "summary": map[string]any{"type": "string", "maxLength": 4096}}, "required": []string{"report_id", "summary"}, "additionalProperties": false},
	}
}

// EmployeeProgressMCP exposes only one task-token-scoped reporting tool. Direct
// execution deliberately does not receive the generic platform workflow MCP.
func (h *Handler) EmployeeProgressMCP(w http.ResponseWriter, r *http.Request) {
	if !multicaMCPTaskTokenAuthenticated(r) {
		writeError(w, http.StatusForbidden, "progress reporting requires a task token")
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
	if decoder.Decode(&req) != nil {
		h.writeMulticaMCPError(w, nil, -32700, "parse error")
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF || req.JSONRPC != "2.0" {
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
		h.writeMulticaMCPResult(w, req.ID, map[string]any{"tools": []any{employeeProgressToolDefinition()}})
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &params) != nil || params.Name != employeeProgressMCPTool {
			h.writeMulticaMCPError(w, req.ID, -32602, "unknown progress tool")
			return
		}
		var args struct {
			ReportID string `json:"report_id"`
			Summary  string `json:"summary"`
		}
		if err := decodeMulticaMCPArguments(params.Arguments, &args); err != nil {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid progress arguments")
			return
		}
		out, err := h.recordEmployeeProgress(r.Context(), r.Header.Get("X-Task-ID"), r.Header.Get("X-Workspace-ID"), r.Header.Get("X-Agent-ID"), r.Header.Get("X-User-ID"), args.ReportID, args.Summary)
		if err != nil {
			message := "progress reporting is temporarily unavailable"
			var refusal employeeProgressRefusal
			if errors.As(err, &refusal) {
				message = refusal.Error()
			} else {
				slog.Error("Employee progress admission failed", "source_task_id", r.Header.Get("X-Task-ID"), "error", err)
			}
			h.writeMulticaMCPToolError(w, req.ID, message)
			return
		}
		raw, _ := json.Marshal(out)
		h.writeMulticaMCPResult(w, req.ID, multicaMCPToolResult{Content: []multicaMCPContent{{Type: "text", Text: string(raw)}}})
	default:
		h.writeMulticaMCPError(w, req.ID, -32601, "method not found")
	}
}
