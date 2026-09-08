package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type runnerMCPCallArguments struct {
	ServerName  string          `json:"server_name"`
	Fingerprint string          `json:"fingerprint"`
	SessionKey  string          `json:"session_key"`
	ProtocolVersion string      `json:"protocol_version,omitempty"`
	Request     json.RawMessage `json:"request"`
}

type runnerMCPManager struct {
	document runnerMCPConfigDocument
	roots    []string
	client   *http.Client
	mu       sync.Mutex
	sessions map[string]string
	stdio    map[string]*runnerMCPStdioProcess
}

type runnerMCPStdioProcess struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	decoder *json.Decoder
}

func newRunnerMCPManager(document runnerMCPConfigDocument, roots ...string) *runnerMCPManager {
	return &runnerMCPManager{
		document: document,
		roots:    append([]string(nil), roots...),
		client:   &http.Client{},
		sessions: make(map[string]string),
		stdio:    make(map[string]*runnerMCPStdioProcess),
	}
}

func (m *runnerMCPManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, process := range m.stdio {
		process.stop()
	}
	m.stdio = make(map[string]*runnerMCPStdioProcess)
}

func (m *runnerMCPManager) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, *runnerToolError) {
	var args runnerMCPCallArguments
	if err := decodeRunnerArguments(raw, &args); err != nil {
		return nil, err
	}
	config, ok := m.document.Servers[args.ServerName]
	if !ok {
		return nil, &runnerToolError{code: "runner_mcp_not_found", message: "Local MCP server is not configured"}
	}
	if args.Fingerprint == "" || args.Fingerprint != config.Fingerprint {
		return nil, &runnerToolError{code: "runner_mcp_configuration_changed", message: "Local MCP configuration changed after it was enabled"}
	}
	if !json.Valid(args.Request) || len(bytes.TrimSpace(args.Request)) == 0 {
		return nil, &runnerToolError{code: "runner_mcp_invalid_request", message: "Invalid MCP JSON-RPC request"}
	}
	if config.Builtin {
		return executeRunnerBuiltinShellMCP(ctx, m.roots, args.Request)
	}
	if config.Transport == "http" {
		return m.executeHTTP(ctx, config, args)
	}
	return m.executeStdio(ctx, config, args)
}

func (m *runnerMCPManager) executeHTTP(ctx context.Context, config runnerMCPServerConfig, args runnerMCPCallArguments) (json.RawMessage, *runnerToolError) {
	var entry struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if json.Unmarshal(config.Raw, &entry) != nil {
		return nil, &runnerToolError{code: "runner_mcp_config_invalid", message: "Local MCP HTTP configuration is invalid"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.URL, bytes.NewReader(args.Request))
	if err != nil {
		return nil, &runnerToolError{code: "runner_mcp_request_failed", message: "Could not create local MCP request"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if args.ProtocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", args.ProtocolVersion)
	}
	for key, value := range entry.Headers {
		req.Header.Set(key, value)
	}
	m.mu.Lock()
	session := m.sessions[args.SessionKey]
	m.mu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	response, err := m.client.Do(req)
	if err != nil {
		return nil, &runnerToolError{code: "runner_mcp_unavailable", message: "Local MCP HTTP server is unavailable"}
	}
	defer response.Body.Close()
	if next := strings.TrimSpace(response.Header.Get("Mcp-Session-Id")); next != "" && args.SessionKey != "" {
		m.mu.Lock()
		m.sessions[args.SessionKey] = next
		m.mu.Unlock()
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, runnerEncodedResultLimit+1))
	if err != nil || len(body) > runnerEncodedResultLimit {
		return nil, &runnerToolError{code: "runner_mcp_response_invalid", message: "Could not read local MCP response"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &runnerToolError{code: "runner_mcp_http_error", message: "Local MCP HTTP server returned " + response.Status}
	}
	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(body)) == 0 {
		return json.RawMessage(`null`), nil
	}
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		body = runnerMCPSSEData(body)
	}
	if !json.Valid(body) {
		return nil, &runnerToolError{code: "runner_mcp_response_invalid", message: "Local MCP server returned invalid JSON"}
	}
	return json.RawMessage(body), nil
}

func runnerMCPSSEData(body []byte) []byte {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			return bytes.TrimSpace([]byte(strings.TrimPrefix(line, "data:")))
		}
	}
	return nil
}

func (m *runnerMCPManager) executeStdio(ctx context.Context, config runnerMCPServerConfig, args runnerMCPCallArguments) (json.RawMessage, *runnerToolError) {
	m.mu.Lock()
	process := m.stdio[config.Name]
	if process == nil {
		process = &runnerMCPStdioProcess{}
		m.stdio[config.Name] = process
	}
	m.mu.Unlock()
	return process.execute(ctx, config, args.Request)
}

func (p *runnerMCPStdioProcess) start(config runnerMCPServerConfig) error {
	var entry struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	if err := json.Unmarshal(config.Raw, &entry); err != nil || strings.TrimSpace(entry.Command) == "" {
		return errors.New("invalid stdio configuration")
	}
	p.cmd = exec.Command(entry.Command, entry.Args...)
	p.cmd.Env = os.Environ()
	for key, value := range entry.Env {
		p.cmd.Env = append(p.cmd.Env, key+"="+value)
	}
	stdin, err := p.cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	p.cmd.Stderr = os.Stderr
	if err := p.cmd.Start(); err != nil {
		return err
	}
	p.stdin = stdin
	p.decoder = json.NewDecoder(stdout)
	return nil
}

func (p *runnerMCPStdioProcess) execute(ctx context.Context, config runnerMCPServerConfig, request json.RawMessage) (json.RawMessage, *runnerToolError) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil {
		if err := p.start(config); err != nil {
			p.stop()
			return nil, &runnerToolError{code: "runner_mcp_unavailable", message: "Could not start local MCP process"}
		}
	}
	if _, err := p.stdin.Write(append(append([]byte(nil), request...), '\n')); err != nil {
		p.stop()
		return nil, &runnerToolError{code: "runner_mcp_unavailable", message: "Could not write to local MCP process"}
	}
	requestID, hasID := runnerMCPJSONRPCID(request)
	if !hasID {
		return json.RawMessage(`null`), nil
	}
	type decoded struct {
		raw json.RawMessage
		err error
	}
	resultCh := make(chan decoded, 1)
	go func() {
		for {
			var raw json.RawMessage
			if err := p.decoder.Decode(&raw); err != nil {
				resultCh <- decoded{err: err}
				return
			}
			if responseID, ok := runnerMCPJSONRPCID(raw); ok && bytes.Equal(responseID, requestID) {
				resultCh <- decoded{raw: raw}
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		p.stop()
		return nil, &runnerToolError{code: "runner_mcp_cancelled", message: "Local MCP request was cancelled"}
	case result := <-resultCh:
		if result.err != nil {
			p.stop()
			return nil, &runnerToolError{code: "runner_mcp_unavailable", message: "Local MCP process closed unexpectedly"}
		}
		return result.raw, nil
	}
}

func runnerMCPJSONRPCID(raw json.RawMessage) (json.RawMessage, bool) {
	var envelope struct{ ID json.RawMessage `json:"id"` }
	if json.Unmarshal(raw, &envelope) != nil || len(bytes.TrimSpace(envelope.ID)) == 0 || bytes.Equal(bytes.TrimSpace(envelope.ID), []byte("null")) {
		return nil, false
	}
	return bytes.TrimSpace(envelope.ID), true
}

func (p *runnerMCPStdioProcess) stop() {
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	p.cmd = nil
	p.stdin = nil
	p.decoder = nil
}
