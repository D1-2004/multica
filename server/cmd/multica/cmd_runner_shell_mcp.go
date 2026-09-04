package main

import (
	"context"
	"encoding/json"
	"strings"
)

const runnerBuiltinMCPProtocolVersion = "2025-06-18"

type runnerBuiltinMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func executeRunnerBuiltinShellMCP(ctx context.Context, roots []string, raw json.RawMessage) (json.RawMessage, *runnerToolError) {
	var request runnerBuiltinMCPRequest
	if json.Unmarshal(raw, &request) != nil || request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
		return nil, &runnerToolError{code: "runner_mcp_invalid_request", message: "Invalid built-in MCP JSON-RPC request"}
	}
	if _, hasID := runnerMCPJSONRPCID(raw); !hasID {
		return json.RawMessage(`null`), nil
	}
	switch request.Method {
	case "initialize":
		return runnerBuiltinMCPResult(request.ID, map[string]any{
			"protocolVersion": runnerBuiltinMCPProtocolVersion,
			"capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo": map[string]string{"name": runnerBuiltinShellMCPName, "version": "1.0.0"},
		}), nil
	case "ping":
		return runnerBuiltinMCPResult(request.ID, map[string]any{}), nil
	case "tools/list":
		return runnerBuiltinMCPResult(request.ID, map[string]any{"tools": runnerBuiltinShellMCPTools()}), nil
	case "tools/call":
		return runnerBuiltinMCPToolsCall(ctx, roots, request)
	default:
		return runnerBuiltinMCPError(request.ID, -32601, "method not found"), nil
	}
}

func runnerBuiltinMCPResult(id json.RawMessage, result any) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return raw
}

func runnerBuiltinMCPError(id json.RawMessage, code int, message string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
	return raw
}

func runnerBuiltinMCPTool(name, title, description string, properties map[string]any, required []string, readOnly, destructive, idempotent bool) map[string]any {
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

func runnerBuiltinShellMCPTools() []any {
	path := map[string]any{"type": "string", "minLength": 1, "description": "Absolute path inside one of the Runner's configured file roots."}
	return []any{
		runnerBuiltinMCPTool("list_roots", "List file roots", "List the file roots exposed by this Runner.", map[string]any{}, nil, true, false, true),
		runnerBuiltinMCPTool("read_file", "Read a local file", "Read a file from an exposed root.", map[string]any{
			"path": path, "offset": map[string]any{"type": "integer", "minimum": 0, "default": 0},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1048576, "default": 1048576},
			"encoding": map[string]any{"type": "string", "enum": []string{"utf8", "base64"}, "default": "utf8"},
		}, []string{"path"}, true, false, true),
		runnerBuiltinMCPTool("write_file", "Write a local file", "Replace a file inside an exposed root.", map[string]any{
			"path": path, "content": map[string]any{"type": "string"},
			"encoding": map[string]any{"type": "string", "enum": []string{"utf8", "base64"}, "default": "utf8"},
			"create_parents": map[string]any{"type": "boolean", "default": false},
		}, []string{"path", "content"}, false, true, true),
		runnerBuiltinMCPTool("edit_file", "Edit a local text file", "Replace exact text inside a UTF-8 file in an exposed root.", map[string]any{
			"path": path, "old_text": map[string]any{"type": "string", "minLength": 1},
			"new_text": map[string]any{"type": "string"}, "replace_all": map[string]any{"type": "boolean", "default": false},
		}, []string{"path", "old_text", "new_text"}, false, true, true),
		runnerBuiltinMCPTool("list_directory", "List a local directory", "List direct children of a directory in an exposed root.", map[string]any{"path": path}, []string{"path"}, true, false, true),
		runnerBuiltinMCPTool("stat", "Inspect a local path", "Return metadata for a file or directory in an exposed root.", map[string]any{"path": path}, []string{"path"}, true, false, true),
		runnerBuiltinMCPTool("glob", "Match local paths", "Match a glob pattern recursively under an exposed root.", map[string]any{
			"root": path, "pattern": map[string]any{"type": "string", "minLength": 1},
			"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "default": 200},
		}, []string{"root", "pattern"}, true, false, true),
		runnerBuiltinMCPTool("grep", "Search local text files", "Search text recursively under an exposed root.", map[string]any{
			"root": path, "pattern": map[string]any{"type": "string", "minLength": 1},
			"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "default": 200},
		}, []string{"root", "pattern"}, true, false, true),
		runnerBuiltinMCPTool("shell", "Run a local shell command", "Run /bin/sh as the operating-system user that installed Runner. File roots do not restrict command access outside the selected working directory.", map[string]any{
			"command": map[string]any{"type": "string", "minLength": 1}, "cwd": path,
			"background": map[string]any{"type": "boolean", "default": false},
			"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 600, "default": 60},
		}, []string{"command", "cwd"}, false, true, false),
		runnerBuiltinMCPTool("shell_output", "Read background shell output", "Read accumulated output and exit state for a process started by shell.", map[string]any{
			"process_id": map[string]any{"type": "string", "minLength": 1},
		}, []string{"process_id"}, true, false, true),
		runnerBuiltinMCPTool("shell_kill", "Stop a background shell", "Terminate a process started by shell.", map[string]any{
			"process_id": map[string]any{"type": "string", "minLength": 1},
		}, []string{"process_id"}, false, true, true),
	}
}

func runnerBuiltinMCPToolsCall(ctx context.Context, roots []string, request runnerBuiltinMCPRequest) (json.RawMessage, *runnerToolError) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(request.Params, &params) != nil || strings.TrimSpace(params.Name) == "" {
		return runnerBuiltinMCPError(request.ID, -32602, "invalid tool call parameters"), nil
	}
	if len(params.Arguments) == 0 {
		params.Arguments = json.RawMessage(`{}`)
	}
	var value any
	var toolErr *runnerToolError
	if params.Name == "list_roots" {
		var args map[string]any
		if json.Unmarshal(params.Arguments, &args) != nil || len(args) != 0 {
			return runnerBuiltinMCPError(request.ID, -32602, "invalid list_roots arguments"), nil
		}
		value = map[string]any{"roots": append([]string(nil), roots...)}
	} else {
		value, toolErr = runRunnerTool(ctx, roots, params.Name, params.Arguments)
	}
	if toolErr != nil {
		return runnerBuiltinMCPToolResult(request.ID, map[string]string{"code": toolErr.code, "message": toolErr.message}, true), nil
	}
	return runnerBuiltinMCPToolResult(request.ID, value, false), nil
}

func runnerBuiltinMCPToolResult(id json.RawMessage, value any, isError bool) json.RawMessage {
	payload, _ := json.Marshal(value)
	return runnerBuiltinMCPResult(id, map[string]any{
		"content": []map[string]string{{"type": "text", "text": string(payload)}},
		"structuredContent": value,
		"isError": isError,
	})
}
