package remotemcp

// Fork-only (dt-fde-multica): a session-aware Streamable HTTP MCP client for
// relaying tool calls to official remote MCP servers.
//
// Some servers (GitHub) answer tools/list and tools/call statelessly; many
// others (Cloudflare-hosted ones) require initialize → Mcp-Session-Id →
// notifications/initialized first. The client tries the request as is and
// establishes a session only when the server says it needs one. HTTP 400 and
// 404 are the Streamable HTTP transport's session answers (400: no session,
// or an unknown one on servers built on the MCP SDK's session-map pattern;
// 404: a terminated session); they prove the request was rejected before it
// ran, so every method, tools/call included, is retried once after them on a
// fresh session. A JSON-RPC error mentioning the session or initialization
// only counts for methods without side effects: its message is free server
// text, and a tools/call can fail with such text after the tool ran.
// tools/call is never retried after any other failure.
//
// Request ids are unique per process (rpcRequestIDs), not per MCPClient:
// the relay builds one MCPClient per call while a cached session is shared by
// every call with the same session key, and session servers route responses
// by request id, which must not repeat within a session.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultSessionProtocolVersion is the MCP revision offered at initialize.
const DefaultSessionProtocolVersion = "2025-06-18"

var protocolVersionPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// StatusError is a non-2xx HTTP answer of a remote MCP endpoint.
type StatusError struct{ StatusCode int }

func (e *StatusError) Error() string { return fmt.Sprintf("remote MCP returned HTTP %d", e.StatusCode) }

// RPCError is a JSON-RPC error answer of a remote MCP endpoint. Message is
// server text; callers must not forward it to users unfiltered.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("remote MCP error %d", e.Code) }

// ErrInvalidResponse reports an answer that is not a JSON-RPC response to
// the request.
var ErrInvalidResponse = errors.New("remote MCP returned an invalid response")

// SessionCache keeps established MCP sessions in process, keyed by the
// caller (for example connector id + hash of the access token). It is a
// latency optimization only: every replica establishes its own sessions.
type SessionCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]mcpSession
	now     func() time.Time
}

type mcpSession struct {
	id       string
	protocol string
	expires  time.Time
}

// NewSessionCache returns a cache whose entries live for ttl and which holds
// at most max entries.
func NewSessionCache(ttl time.Duration, max int) *SessionCache {
	if max <= 0 {
		max = 1024
	}
	return &SessionCache{ttl: ttl, max: max, entries: map[string]mcpSession{}, now: time.Now}
}

func (c *SessionCache) get(key string) (mcpSession, bool) {
	if c == nil || key == "" {
		return mcpSession{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return mcpSession{}, false
	}
	if !c.now().Before(entry.expires) {
		delete(c.entries, key)
		return mcpSession{}, false
	}
	return entry, true
}

func (c *SessionCache) put(key string, entry mcpSession) {
	if c == nil || key == "" || entry.id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.entries) >= c.max {
		for k, v := range c.entries {
			if !now.Before(v.expires) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < c.max {
				break
			}
			delete(c.entries, k)
		}
	}
	entry.expires = now.Add(c.ttl)
	c.entries[key] = entry
}

func (c *SessionCache) drop(key string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// Len returns the number of cached sessions (tests and metrics).
func (c *SessionCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// rpcRequestIDs numbers the JSON-RPC requests of every MCPClient in the
// process, so no two requests share an id within one cached session.
var rpcRequestIDs atomic.Int64

// MCPClient calls one remote MCP endpoint through an ExternalClient.
type MCPClient struct {
	client   *ExternalClient
	endpoint string
	cache    *SessionCache
}

// MCP returns a client for endpoint (an allowed https URL). cache may be nil
// (no session reuse across calls).
func (c *ExternalClient) MCP(endpoint string, cache *SessionCache) (*MCPClient, error) {
	u, err := c.CheckURL(endpoint)
	if err != nil {
		return nil, err
	}
	if u.RawQuery != "" {
		return nil, errors.New("MCP endpoint must not carry a query")
	}
	return &MCPClient{client: c, endpoint: u.String(), cache: cache}, nil
}

// Call sends one JSON-RPC request and returns its result. headers carry the
// caller's credential (Authorization). sessionKey scopes the cached session;
// it must change whenever the credential changes. A 401 is returned as
// *StatusError without any retry, so the caller can refresh its token.
func (m *MCPClient) Call(ctx context.Context, sessionKey string, headers http.Header, method string, params any) (json.RawMessage, error) {
	session, cached := m.cache.get(sessionKey)
	result, err := m.post(ctx, headers, session, method, params)
	if err == nil || !needsSession(method, err) {
		return result, err
	}
	if cached {
		m.cache.drop(sessionKey)
	}
	session, err = m.initialize(ctx, headers)
	if err != nil {
		return nil, err
	}
	m.cache.put(sessionKey, session)
	return m.post(ctx, headers, session, method, params)
}

// needsSession reports whether err says the server rejected method for lack
// of a (valid) session. HTTP 400 and 404 prove the request did not run, for
// every method. A JSON-RPC error message is only trusted for methods without
// side effects (sideEffectFreeMethod): a tools/call that ran and then failed
// can report "checkout session", "session expired" and similar text.
func needsSession(method string, err error) bool {
	var status *StatusError
	if errors.As(err, &status) {
		return status.StatusCode == http.StatusNotFound || status.StatusCode == http.StatusBadRequest
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) && sideEffectFreeMethod(method) {
		message := strings.ToLower(rpcErr.Message)
		return strings.Contains(message, "session") || strings.Contains(message, "initializ")
	}
	return false
}

// sideEffectFreeMethod reports whether repeating method can never repeat an
// action on the remote side.
func sideEffectFreeMethod(method string) bool {
	switch method {
	case "tools/list", "resources/list", "resources/templates/list", "prompts/list", "ping":
		return true
	default:
		return false
	}
}

func (m *MCPClient) initialize(ctx context.Context, headers http.Header) (mcpSession, error) {
	id := rpcRequestIDs.Add(1)
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": DefaultSessionProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "multica", "version": "1"},
		},
	})
	if err != nil {
		return mcpSession{}, err
	}
	raw, responseHeaders, err := m.send(ctx, headers, mcpSession{}, false, body, id)
	if err != nil {
		return mcpSession{}, fmt.Errorf("initialize remote MCP: %w", err)
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &initialized) != nil {
		return mcpSession{}, ErrInvalidResponse
	}
	session := mcpSession{id: strings.TrimSpace(responseHeaders.Get("Mcp-Session-Id")), protocol: DefaultSessionProtocolVersion}
	if protocolVersionPattern.MatchString(initialized.ProtocolVersion) {
		session.protocol = initialized.ProtocolVersion
	}
	if strings.ContainsAny(session.id, "\r\n\x00") || len(session.id) > 1024 {
		return mcpSession{}, ErrInvalidResponse
	}
	notification, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if _, _, err := m.send(ctx, headers, session, true, notification, 0); err != nil {
		return mcpSession{}, fmt.Errorf("confirm remote MCP initialization: %w", err)
	}
	return session, nil
}

func (m *MCPClient) post(ctx context.Context, headers http.Header, session mcpSession, method string, params any) (json.RawMessage, error) {
	if params == nil {
		params = map[string]any{}
	}
	id := rpcRequestIDs.Add(1)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	raw, _, err := m.send(ctx, headers, session, true, body, id)
	return raw, err
}

// send posts body and returns the JSON-RPC result of request id (or nothing
// for a notification, id 0).
func (m *MCPClient) send(ctx context.Context, headers http.Header, session mcpSession, versionHeader bool, body []byte, id int64) (json.RawMessage, http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if session.id != "" {
		request.Header.Set("Mcp-Session-Id", session.id)
	}
	if versionHeader {
		protocol := session.protocol
		if protocol == "" {
			protocol = DefaultSessionProtocolVersion
		}
		request.Header.Set("MCP-Protocol-Version", protocol)
	}
	response, err := m.client.http.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxOAuthResponseBytes))
		return nil, response.Header, &StatusError{StatusCode: response.StatusCode}
	}
	if id == 0 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxOAuthResponseBytes))
		return nil, response.Header, nil
	}
	raw, err := readRPCResponse(response.Body, response.Header.Get("Content-Type"), id)
	if err != nil {
		return nil, response.Header, err
	}
	var decoded struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &decoded) != nil || decoded.JSONRPC != "2.0" || !rpcIDEquals(decoded.ID, id) {
		return nil, response.Header, ErrInvalidResponse
	}
	if decoded.Error != nil {
		return nil, response.Header, &RPCError{Code: decoded.Error.Code, Message: decoded.Error.Message}
	}
	if len(decoded.Result) == 0 || string(decoded.Result) == "null" {
		return nil, response.Header, ErrInvalidResponse
	}
	return decoded.Result, response.Header, nil
}

func rpcIDEquals(raw json.RawMessage, id int64) bool {
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return false
		}
		return text == fmt.Sprint(id)
	}
	return number.String() == fmt.Sprint(id)
}

// readRPCResponse returns the JSON body, or the SSE event carrying the
// response to id; it stops there even when the server keeps the stream open.
func readRPCResponse(body io.Reader, contentType string, id int64) ([]byte, error) {
	limited := &io.LimitedReader{R: body, N: MaxResponseBytes + 1}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/event-stream") {
		raw, err := io.ReadAll(limited)
		if err != nil {
			return nil, err
		}
		if len(raw) > MaxResponseBytes {
			return nil, errors.New("remote MCP response exceeds size limit")
		}
		return raw, nil
	}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), MaxResponseBytes+1)
	var data []string
	flush := func() []byte {
		if len(data) == 0 {
			return nil
		}
		payload := []byte(strings.Join(data, "\n"))
		data = nil
		var event struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(payload, &event) == nil && rpcIDEquals(event.ID, id) {
			return payload
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if payload := flush(); payload != nil {
				return payload, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if limited.N <= 0 {
		return nil, errors.New("remote MCP response exceeds size limit")
	}
	if payload := flush(); payload != nil {
		return payload, nil
	}
	return nil, errors.New("remote MCP SSE stream ended without the response")
}

// ListedTool is one tool of a tools/list answer.
type ListedTool struct {
	Name        string
	Description string
	ReadOnly    bool
}

// ListTools pages through tools/list (at most 16 pages) and returns up to
// maxTools tools in server order; truncated reports that more existed.
// ReadOnly is the tool's annotations.readOnlyHint.
func (m *MCPClient) ListTools(ctx context.Context, sessionKey string, headers http.Header, maxTools int) ([]ListedTool, bool, error) {
	tools := []ListedTool{}
	seenNames := map[string]bool{}
	seenCursors := map[string]bool{}
	cursor := ""
	for page := 0; page < 16; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := m.Call(ctx, sessionKey, headers, "tools/list", params)
		if err != nil {
			return nil, false, err
		}
		var list struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Annotations struct {
					ReadOnlyHint *bool `json:"readOnlyHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &list) != nil || list.Tools == nil {
			return nil, false, ErrInvalidResponse
		}
		for _, tool := range list.Tools {
			if strings.TrimSpace(tool.Name) == "" || seenNames[tool.Name] {
				return nil, false, errors.New("remote MCP returned an invalid or duplicate tool name")
			}
			seenNames[tool.Name] = true
			if len(tools) >= maxTools {
				return tools, true, nil
			}
			tools = append(tools, ListedTool{
				Name: tool.Name, Description: tool.Description,
				ReadOnly: tool.Annotations.ReadOnlyHint != nil && *tool.Annotations.ReadOnlyHint,
			})
		}
		if list.NextCursor == "" {
			return tools, false, nil
		}
		if seenCursors[list.NextCursor] {
			return nil, false, errors.New("remote MCP tool list repeated a cursor")
		}
		seenCursors[list.NextCursor] = true
		cursor = list.NextCursor
	}
	return nil, false, errors.New("remote MCP tool list has too many pages")
}
