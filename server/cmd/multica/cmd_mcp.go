package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

const mcpCLIProtocolVersion = "2025-06-18"

type mcpCLIRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type mcpCLIResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *mcpCLIError    `json:"error,omitempty"`
}

type mcpCLIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpCLIToolsListResult struct {
	Tools      []json.RawMessage `json:"tools"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Discover and call server-provided MCP tools",
}

var mcpToolsCmd = newMCPToolsCommand()

var mcpCallCmd = newMCPCallCommand()

func newMCPToolsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "Discover server-provided MCP tools",
		Args:  cobra.NoArgs,
		RunE:  runMCPTools,
	}
	cmd.Flags().String("output", "json", "Output format: json")
	return cmd
}

func newMCPCallCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "call",
		Short: "Call a server-provided MCP tool",
		Args:  cobra.NoArgs,
		RunE:  runMCPCall,
	}
	cmd.Flags().String("method", "", "Tool name returned by 'multica mcp tools'")
	cmd.Flags().String("arguments", "", "MCP tool arguments as a JSON object")
	cmd.Flags().Bool("arguments-stdin", false, "Read MCP tool arguments as a JSON object from stdin")
	cmd.Flags().String("arguments-file", "", "Read MCP tool arguments as a JSON object from a file")
	cmd.Flags().String("output", "json", "Output format: json")
	return cmd
}

func init() {
	mcpCmd.AddCommand(mcpToolsCmd, mcpCallCmd)
}

func runMCPTools(cmd *cobra.Command, _ []string) error {
	if err := requireMCPJSONOutput(cmd); err != nil {
		return err
	}
	client, ctx, cancel, err := newMCPClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	tools := make([]json.RawMessage, 0)
	cursor := ""
	seenCursors := map[string]struct{}{}
	for requestID := 1; ; requestID++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, err := callMCPServer(ctx, client, requestID, "tools/list", params)
		if err != nil {
			return fmt.Errorf("discover MCP tools: %w", err)
		}
		var page mcpCLIToolsListResult
		if err := json.Unmarshal(result, &page); err != nil {
			return fmt.Errorf("discover MCP tools: decode result: %w", err)
		}
		tools = append(tools, page.Tools...)
		nextCursor := strings.TrimSpace(page.NextCursor)
		if nextCursor == "" {
			break
		}
		if _, exists := seenCursors[nextCursor]; exists {
			return fmt.Errorf("discover MCP tools: server repeated pagination cursor %q", nextCursor)
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
	}
	return cli.PrintJSON(cmd.OutOrStdout(), mcpCLIToolsListResult{Tools: tools})
}

func runMCPCall(cmd *cobra.Command, _ []string) error {
	if err := requireMCPJSONOutput(cmd); err != nil {
		return err
	}
	method, _ := cmd.Flags().GetString("method")
	method = strings.TrimSpace(method)
	if method == "" {
		return fmt.Errorf("--method is required")
	}
	arguments, err := resolveMCPArguments(cmd)
	if err != nil {
		return err
	}

	client, ctx, cancel, err := newMCPClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	result, err := callMCPServer(ctx, client, 1, "tools/call", map[string]any{
		"name":      method,
		"arguments": arguments,
	})
	if err != nil {
		return fmt.Errorf("call MCP method %q: %w", method, err)
	}
	if err := cli.PrintJSON(cmd.OutOrStdout(), result); err != nil {
		return err
	}
	var toolResult struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &toolResult); err == nil && toolResult.IsError {
		return fmt.Errorf("MCP method %q returned an error", method)
	}
	return nil
}

func newMCPClient(cmd *cobra.Command) (*cli.APIClient, context.Context, context.CancelFunc, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	// /api/mcp derives its Workspace from the authenticated token or target
	// resource. Keeping this header empty makes the CLI usable without a
	// caller-managed Workspace selection and matches generic MCP clients.
	client.WorkspaceID = ""
	ctx, cancel := cli.APIContext(context.Background())
	return client, ctx, cancel, nil
}

func callMCPServer(
	ctx context.Context,
	client *cli.APIClient,
	id int,
	method string,
	params any,
) (json.RawMessage, error) {
	headers := map[string]string{
		"Accept":               "application/json, text/event-stream",
		"MCP-Protocol-Version": mcpCLIProtocolVersion,
	}
	request := mcpCLIRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	var response mcpCLIResponse
	if err := client.PostJSONWithHeaders(ctx, "/api/mcp", request, &response, headers); err != nil {
		return nil, err
	}
	if response.JSONRPC != "2.0" {
		return nil, fmt.Errorf("server returned an invalid JSON-RPC response")
	}
	if response.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error %d: %s", response.Error.Code, response.Error.Message)
	}
	if len(response.Result) == 0 || string(response.Result) == "null" {
		return nil, fmt.Errorf("server returned an empty MCP result")
	}
	return response.Result, nil
}

func resolveMCPArguments(cmd *cobra.Command) (json.RawMessage, error) {
	inlineChanged := cmd.Flags().Changed("arguments")
	fromStdin, _ := cmd.Flags().GetBool("arguments-stdin")
	fileChanged := cmd.Flags().Changed("arguments-file")
	filePath, _ := cmd.Flags().GetString("arguments-file")

	sources := 0
	if inlineChanged {
		sources++
	}
	if fromStdin {
		sources++
	}
	if fileChanged {
		sources++
	}
	if sources > 1 {
		return nil, fmt.Errorf("--arguments, --arguments-stdin, and --arguments-file are mutually exclusive; pick one")
	}

	raw := "{}"
	var err error
	switch {
	case inlineChanged:
		raw, _ = cmd.Flags().GetString("arguments")
	case fromStdin:
		var data []byte
		data, err = io.ReadAll(cmd.InOrStdin())
		raw = string(data)
		if err != nil {
			return nil, fmt.Errorf("read --arguments-stdin: %w", err)
		}
	case fileChanged:
		if strings.TrimSpace(filePath) == "" {
			return nil, fmt.Errorf("--arguments-file: path must not be empty")
		}
		var data []byte
		data, err = os.ReadFile(filePath)
		raw = string(data)
		if err != nil {
			return nil, fmt.Errorf("read --arguments-file: %w", err)
		}
	}

	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("MCP arguments must be a JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil || object == nil {
		return nil, fmt.Errorf("MCP arguments must be a JSON object")
	}
	return json.RawMessage(trimmed), nil
}

func requireMCPJSONOutput(cmd *cobra.Command) error {
	output, _ := cmd.Flags().GetString("output")
	if strings.TrimSpace(output) != "json" {
		return fmt.Errorf("unsupported output format %q: MCP commands support json", output)
	}
	return nil
}
