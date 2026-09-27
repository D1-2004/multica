package handler

import (
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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/redis/go-redis/v9"
)

type semanticaMCPRelayStatus struct {
	Available bool     `json:"available"`
	AgentID   *string  `json:"agent_id"`
	AgentName *string  `json:"agent_name"`
	Tools     []string `json:"tools"`
}

// GetSemanticaMCPRelayStatus is behind the workspace membership gate. It
// reports deployment state without exposing the upstream URL or credentials.
func (h *Handler) GetSemanticaMCPRelayStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace_id")
	if !ok {
		return
	}
	status := semanticaMCPRelayStatus{Tools: []string{}}
	relay := h.SemanticaMCPRelay
	if relay == nil {
		writeJSON(w, http.StatusOK, status)
		return
	}
	agentID, err := util.ParseUUID(relay.targetAgentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Semantica relay configuration is invalid")
		return
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, status)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Semantica relay status is unavailable")
		return
	}
	workspace := uuidToString(workspaceID)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, workspace)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspace) ||
		!h.canInvokeAgent(r.Context(), agent, actorType, actorID, userID, workspace) {
		writeJSON(w, http.StatusOK, status)
		return
	}
	status.AgentID = &relay.targetAgentID
	status.AgentName = &agent.Name
	status.Available = !agent.ArchivedAt.Valid && agent.RuntimeID.Valid && relay.redis != nil &&
		featureflags.SemanticaMCPRelayEnabled(r.Context(), h.FeatureFlags)
	if status.Available {
		status.Tools = []string{"get_knowledge_graph_schema", "get_knowledge_node_schema", "query_knowledge_cypher", "search_knowledge"}
	}
	writeJSON(w, http.StatusOK, status)
}

const semanticaRelayTool = "semantica_mcp_relay"
const semanticaMaxResponse = internalMCPMaxResponse

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
	// Authorization and rate checks have finished. Commit JSON headers before
	// the slow upstream call so the public relay's header deadline does not
	// cancel an otherwise valid MCP request. Body completion remains bounded
	// by ctx, and no success result is emitted before the upstream finishes.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(w).Flush()
	result, err := relay.call(ctx, args)
	outcome = "ok"
	if err != nil {
		outcome = "upstream_error"
		result = multicaMCPToolResult{IsError: true, Content: []multicaMCPContent{{Type: "text", Text: "Semantica upstream unavailable"}}}
	} else if result.IsError {
		outcome = "tool_error"
	}
	_ = json.NewEncoder(w).Encode(multicaMCPResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func semanticaJSONObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return internalMCPJSONObject(raw)
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

func semanticaReadRPC(body io.Reader, contentType string) ([]byte, error) {
	return readInternalMCPResponse(body, contentType)
}
