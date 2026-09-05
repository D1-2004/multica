package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

const (
	runnerMCPInspectTimeout       = 8 * time.Second
	runnerMCPMaxToolsPerServer   = 128
	runnerMCPMaxDetailTextLength = 2048
)

type runnerMCPInitializeResponse struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Result struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	} `json:"result"`
}

type runnerMCPToolsListResponse struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Result struct {
		Tools []struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"tools"`
		NextCursor string `json:"nextCursor"`
	} `json:"result"`
}

func inspectRunnerMCPInventory(ctx context.Context, manager *runnerMCPManager, inventory runnerprotocol.MCPInventory) runnerprotocol.MCPInventory {
	result := inventory
	result.Servers = append([]runnerprotocol.MCPServerSummary(nil), inventory.Servers...)
	var wg sync.WaitGroup
	for index := range result.Servers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, runnerMCPInspectTimeout)
			defer cancel()
			detail, err := inspectRunnerMCPServer(probeCtx, manager, result.Servers[index])
			if err != nil {
				detail.DetailStatus = "unavailable"
				result.Servers[index] = detail
				return
			}
			result.Servers[index] = detail
		}(index)
	}
	wg.Wait()
	return result
}

func inspectRunnerMCPServer(ctx context.Context, manager *runnerMCPManager, summary runnerprotocol.MCPServerSummary) (runnerprotocol.MCPServerSummary, error) {
	sessionKey := "inventory/" + summary.Name + "/" + summary.Fingerprint
	initializeRequest, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "multica-runner-inventory-initialize",
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": runnerBuiltinMCPProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]string{
				"name":    "multica-runner",
				"version": version,
			},
		},
	})
	initializeRaw, toolErr := executeRunnerMCPInspection(ctx, manager, summary, sessionKey, runnerBuiltinMCPProtocolVersion, initializeRequest)
	if toolErr != nil {
		return summary, fmt.Errorf("initialize MCP server: %s", toolErr.message)
	}
	var initialized runnerMCPInitializeResponse
	if err := json.Unmarshal(initializeRaw, &initialized); err != nil {
		return summary, fmt.Errorf("decode MCP initialize response: %w", err)
	}
	if initialized.Error != nil {
		return summary, fmt.Errorf("initialize MCP server: %s", initialized.Error.Message)
	}

	protocolVersion := strings.TrimSpace(initialized.Result.ProtocolVersion)
	if protocolVersion == "" {
		protocolVersion = runnerBuiltinMCPProtocolVersion
	}
	summary.Title = trimRunnerMCPDetail(initialized.Result.ServerInfo.Title)
	if summary.Title == "" {
		summary.Title = trimRunnerMCPDetail(initialized.Result.ServerInfo.Name)
	}
	if summary.Title == "" {
		summary.Title = summary.Name
	}
	summary.Version = trimRunnerMCPDetail(initialized.Result.ServerInfo.Version)
	summary.Description = trimRunnerMCPDetail(initialized.Result.Instructions)
	for capability, raw := range initialized.Result.Capabilities {
		if capability != "" && len(raw) > 0 && string(raw) != "null" && string(raw) != "false" {
			summary.Capabilities = append(summary.Capabilities, capability)
		}
	}
	sort.Strings(summary.Capabilities)

	initializedNotification := json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	_, _ = executeRunnerMCPInspection(ctx, manager, summary, sessionKey, protocolVersion, initializedNotification)

	if !containsRunnerMCPCapability(summary.Capabilities, "tools") {
		summary.DetailStatus = "available"
		return summary, nil
	}
	summary.Tools = make([]runnerprotocol.MCPToolSummary, 0)
	cursor := ""
	for page := 0; page < runnerMCPMaxToolsPerServer && len(summary.Tools) < runnerMCPMaxToolsPerServer; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		toolsRequest, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      fmt.Sprintf("multica-runner-inventory-tools-%d", page),
			"method":  "tools/list",
			"params":  params,
		})
		toolsRaw, toolErr := executeRunnerMCPInspection(ctx, manager, summary, sessionKey, protocolVersion, toolsRequest)
		if toolErr != nil {
			return summary, fmt.Errorf("list MCP tools: %s", toolErr.message)
		}
		var listed runnerMCPToolsListResponse
		if err := json.Unmarshal(toolsRaw, &listed); err != nil {
			return summary, fmt.Errorf("decode MCP tools response: %w", err)
		}
		if listed.Error != nil {
			return summary, fmt.Errorf("list MCP tools: %s", listed.Error.Message)
		}
		for _, tool := range listed.Result.Tools {
			if len(summary.Tools) >= runnerMCPMaxToolsPerServer {
				break
			}
			name := trimRunnerMCPDetail(tool.Name)
			if name == "" {
				continue
			}
			summary.Tools = append(summary.Tools, runnerprotocol.MCPToolSummary{
				Name:        name,
				Title:       trimRunnerMCPDetail(tool.Title),
				Description: trimRunnerMCPDetail(tool.Description),
			})
		}
		nextCursor := strings.TrimSpace(listed.Result.NextCursor)
		if nextCursor == "" || nextCursor == cursor {
			break
		}
		cursor = nextCursor
	}
	summary.DetailStatus = "available"
	return summary, nil
}

func executeRunnerMCPInspection(ctx context.Context, manager *runnerMCPManager, summary runnerprotocol.MCPServerSummary, sessionKey, protocolVersion string, request json.RawMessage) (json.RawMessage, *runnerToolError) {
	arguments, _ := json.Marshal(runnerMCPCallArguments{
		ServerName:      summary.Name,
		Fingerprint:     summary.Fingerprint,
		SessionKey:      sessionKey,
		ProtocolVersion: protocolVersion,
		Request:         request,
	})
	return manager.Execute(ctx, arguments)
}

func containsRunnerMCPCapability(capabilities []string, target string) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

func trimRunnerMCPDetail(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > runnerMCPMaxDetailTextLength {
		return string(runes[:runnerMCPMaxDetailTextLength])
	}
	return value
}
