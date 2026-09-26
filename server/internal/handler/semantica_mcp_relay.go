package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/redis/go-redis/v9"
)

const semanticaRelayTool = "semantica_mcp_relay"
const semanticaMaxResponse = 2 << 20

var semanticaTools = map[string]bool{
	"search_knowledge": true, "query_knowledge_cypher": true,
	"get_knowledge_graph_schema": true, "get_knowledge_node_schema": true,
}

type semanticaTaskStore interface {
	GetAgentTaskInWorkspace(context.Context, db.GetAgentTaskInWorkspaceParams) (db.AgentTaskQueue, error)
}
type semanticaRateStore interface {
	Eval(context.Context, string, []string, ...interface{}) *redis.Cmd
}

// SemanticaMCPRelay owns only upstream credentials and transport. Task identity
// is always resolved by Auth and checked against the database on each call.
type SemanticaMCPRelay struct {
	targetAgentID string
	endpoint      string
	bearer        string
	client        *http.Client
	redis         semanticaRateStore
	tasks         semanticaTaskStore
	environment   string
}

func NewSemanticaMCPRelayFromEnv(rdb *redis.Client) (*SemanticaMCPRelay, error) {
	endpoint := strings.TrimSpace(os.Getenv("MULTICA_SEMANTICA_MCP_URL"))
	bearer := strings.TrimSpace(os.Getenv("MULTICA_SEMANTICA_MCP_BEARER_TOKEN"))
	agent := strings.TrimSpace(os.Getenv("MULTICA_SEMANTICA_MCP_TARGET_AGENT_ID"))
	if endpoint == "" && bearer == "" && agent == "" {
		return nil, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("Semantica requires a fixed HTTPS endpoint")
	}
	agentID, err := uuid.Parse(agent)
	if err != nil || agentID == uuid.Nil {
		return nil, errors.New("Semantica requires an authorized Agent UUID")
	}
	if bearer == "" || strings.ContainsAny(bearer, "\r\n") {
		return nil, errors.New("Semantica upstream credential is missing or invalid")
	}
	if rdb == nil {
		return nil, errors.New("Semantica requires the shared Tair client")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &SemanticaMCPRelay{
		endpoint: endpoint, bearer: bearer, targetAgentID: agentID.String(), redis: rdb,
		environment: featureflags.SemanticaEnvironment(),
		client:      &http.Client{Timeout: 45 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (h *Handler) semanticaRelayVisible(r *http.Request) bool {
	return h.SemanticaMCPRelay != nil && h.SemanticaMCPRelay.redis != nil &&
		featureflags.SemanticaMCPRelayEnabled(r.Context(), h.FeatureFlags) &&
		multicaMCPTaskTokenAuthenticated(r) && r.Header.Get("X-Agent-ID") == h.SemanticaMCPRelay.targetAgentID
}

func semanticaRelayDefinition() map[string]any {
	return map[string]any{
		"name":        semanticaRelayTool,
		"description": "Query the authorized internal Semantica knowledge MCP. First use method=tools/list to discover the four read-only tools and their argument schemas, then tools/call with tool_name and arguments. For knowledge queries inspect get_knowledge_graph_schema before search_knowledge; use approved-current data only. The server owns upstream credentials and URL.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"method":    map[string]any{"type": "string", "enum": []string{"tools/list", "tools/call"}},
				"tool_name": map[string]any{"type": "string", "enum": []string{"search_knowledge", "query_knowledge_cypher", "get_knowledge_graph_schema", "get_knowledge_node_schema"}},
				"arguments": map[string]any{"type": "object"},
			}, "required": []string{"method"}},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": true},
	}
}

type semanticaRelayArguments struct {
	Method    string          `json:"method"`
	ToolName  string          `json:"tool_name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func (h *Handler) handleSemanticaRelayCall(w http.ResponseWriter, r *http.Request, id, jsonArgs json.RawMessage) {
	start := time.Now()
	outcome, tool := "denied", ""
	defer func() {
		slog.InfoContext(r.Context(), "semantica_mcp_relay_call", "task_id", r.Header.Get("X-Task-ID"), "agent_id", r.Header.Get("X-Agent-ID"), "workspace_id", r.Header.Get("X-Workspace-ID"), "tool", tool, "outcome", outcome, "duration_ms", time.Since(start).Milliseconds())
	}()
	fail := func(message string) { h.writeMulticaMCPToolError(w, id, message) }
	if !h.semanticaRelayVisible(r) {
		fail("Semantica relay unavailable")
		return
	}
	var args semanticaRelayArguments
	if decodeMulticaMCPArguments(jsonArgs, &args) != nil ||
		(args.Method != "tools/list" && args.Method != "tools/call") ||
		(args.Method == "tools/call" && (!semanticaTools[args.ToolName] || !semanticaJSONObject(args.Arguments))) ||
		(args.Method == "tools/list" && (args.ToolName != "" || len(args.Arguments) != 0)) {
		fail("Invalid Semantica method or tool arguments")
		return
	}
	tool = args.ToolName
	relay := h.SemanticaMCPRelay
	taskID, err := util.ParseUUID(r.Header.Get("X-Task-ID"))
	if err != nil {
		fail("Task is not authorized")
		return
	}
	wsID, err := util.ParseUUID(r.Header.Get("X-Workspace-ID"))
	if err != nil {
		fail("Task is not authorized")
		return
	}
	store := relay.tasks
	if store == nil {
		store = h.Queries
	}
	if store == nil {
		fail("Task store unavailable")
		return
	}
	task, err := store.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: wsID})
	if err != nil || task.ID != taskID || util.UUIDToString(task.AgentID) != relay.targetAgentID || (task.Status != "running" && task.Status != "dispatched") {
		fail("Task is not active or authorized")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := relay.limit(ctx, r.Header.Get("X-Task-ID"), relay.targetAgentID, r.Header.Get("X-Workspace-ID")); err != nil {
		outcome = "rate_unavailable"
		fail("Semantica rate limit or shared store unavailable")
		return
	}
	result, err := relay.call(ctx, args)
	if err != nil {
		outcome = "upstream_error"
		fail("Semantica upstream unavailable")
		return
	}
	outcome = "ok"
	if result.IsError {
		outcome = "tool_error"
	}
	h.writeMulticaMCPResult(w, id, result)
}

func semanticaJSONObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 1 && raw[0] == '{' && raw[len(raw)-1] == '}' && json.Valid(raw)
}

func (s *SemanticaMCPRelay) limit(ctx context.Context, task, agent, ws string) error {
	const script = `local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], 120000) end
return n`
	for _, scope := range []struct {
		label, id string
		max       int64
	}{{"task", task, 30}, {"agent", agent, 120}, {"workspace", ws, 600}} {
		key := "mcpconn:" + s.environment + ":semantica:rate:" + scope.label + ":" + scope.id + ":" + time.Now().UTC().Format("200601021504")
		n, err := s.redis.Eval(ctx, script, []string{key}).Int64()
		if err != nil {
			return err
		}
		if n > scope.max {
			return errors.New("rate limited")
		}
	}
	return nil
}

func (s *SemanticaMCPRelay) call(ctx context.Context, args semanticaRelayArguments) (multicaMCPToolResult, error) {
	params := map[string]any{}
	if args.Method == "tools/call" {
		params["name"] = args.ToolName
		params["arguments"] = args.Arguments
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": args.Method, "params": params})
	if err != nil {
		return multicaMCPToolResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return multicaMCPToolResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+s.bearer)
	resp, err := s.client.Do(req)
	if err != nil {
		return multicaMCPToolResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return multicaMCPToolResult{}, errors.New("upstream HTTP error")
	}
	raw, err := semanticaReadRPC(resp.Body, resp.Header.Get("Content-Type"))
	if err != nil {
		return multicaMCPToolResult{}, err
	}
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &rpc) != nil || rpc.JSONRPC != "2.0" || string(rpc.ID) != "1" || (len(rpc.Error) > 0 && string(rpc.Error) != "null") || !semanticaJSONObject(rpc.Result) {
		return multicaMCPToolResult{}, errors.New("invalid upstream RPC")
	}
	if args.Method == "tools/list" {
		var list struct {
			Tools []map[string]any `json:"tools"`
		}
		if json.Unmarshal(rpc.Result, &list) != nil || list.Tools == nil {
			return multicaMCPToolResult{}, errors.New("invalid tool list")
		}
		allowed := []map[string]any{}
		for _, t := range list.Tools {
			name, _ := t["name"].(string)
			if semanticaTools[name] {
				allowed = append(allowed, t)
			}
		}
		data := map[string]any{"tools": allowed}
		text, _ := json.Marshal(data)
		return multicaMCPToolResult{Content: []multicaMCPContent{{Type: "text", Text: string(text)}}, StructuredContent: data}, nil
	}
	var result multicaMCPToolResult
	if json.Unmarshal(rpc.Result, &result) != nil || len(result.Content) == 0 {
		return result, errors.New("invalid tool result")
	}
	return result, nil
}

// Stop at the matching SSE response, even when the server keeps the stream open.
func semanticaReadRPC(body io.Reader, contentType string) ([]byte, error) {
	limited := &io.LimitedReader{R: body, N: semanticaMaxResponse + 1}
	if !strings.HasPrefix(contentType, "text/event-stream") {
		raw, err := io.ReadAll(limited)
		if err != nil || len(raw) > semanticaMaxResponse {
			return nil, errors.New("response read limit")
		}
		return raw, nil
	}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), semanticaMaxResponse+1)
	var data []string
	for scanner.Scan() {
		if limited.N == 0 {
			return nil, errors.New("response read limit")
		}
		line := scanner.Text()
		if line == "" {
			payload := []byte(strings.Join(data, "\n"))
			data = nil
			var event struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(payload, &event) == nil && string(event.ID) == "1" {
				return payload, nil
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return nil, errors.New("missing upstream RPC event")
}
