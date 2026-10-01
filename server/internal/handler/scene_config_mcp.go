package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// config-qwen-tag-scene: the MCP server the claim mounts for a task that has
// a current Agent work scene (a group or a 1:1 chat), next to the skill of
// the same name. Its tools read and change that one scene's configuration —
// prompts, routines, offered skills and connectors, remote MCP servers — and
// take no scene argument.
//
// The claim issues a scene token (auth.IssueSceneToken) binding the task,
// agent, workspace and scene, and puts it in the route path
// (/api/scene-config/mcp/{sct_…}); the Authorization stays the task's task
// token, which the sandbox relay requires. Every call verifies the token,
// requires it to belong to the calling task, requires the task to be active,
// and re-resolves the task's scene, which must still be the token's. A
// routine run (cron or webhook) is read-only: its input may come from
// outside.

const sceneConfigMCPServerName = service.SceneConfigSkillName

const sceneConfigMCPMaxRequestBytes = 64 << 10

// sceneConfigTarget is the scene a task configures.
type sceneConfigTarget struct {
	agent contextCapAgent
	task  db.AgentTaskQueue
	scope contextcap.Scope
	scene contextcap.SceneSummary
	// routineRun: a cron or webhook run; writes are refused.
	routineRun bool
	// senderOpenDingTalkID is the 1:1 counterpart of a dm task (its
	// dispatch sender), "" otherwise.
	senderOpenDingTalkID string
}

// taskConfigScene resolves the scene task may configure: its context scope's
// scene (already fenced to the agent's tenant org by resolveTaskContextScope),
// a group or a 1:1 chat. ok is false when the task has none.
func (h *Handler) taskConfigScene(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue) (sceneConfigTarget, bool, error) {
	if h.Queries == nil || h.DB == nil {
		return sceneConfigTarget{}, false, nil
	}
	scope, skipped := h.resolveTaskContextScope(ctx, workspaceID, task)
	if skipped != "" || scope.SceneID == "" {
		return sceneConfigTarget{}, false, nil
	}
	summary, err := contextcap.GetScene(ctx, h.DB, uuidToString(workspaceID), uuidToString(task.AgentID), scope.OrgID, scope.SceneID)
	if errors.Is(err, contextcap.ErrNotFound) {
		return sceneConfigTarget{}, false, nil
	}
	if err != nil {
		return sceneConfigTarget{}, false, err
	}
	if summary.Kind != scene.KindGroup && summary.Kind != scene.KindDM {
		return sceneConfigTarget{}, false, nil
	}
	target := sceneConfigTarget{
		agent: contextCapAgent{ID: uuidToString(task.AgentID), WorkspaceID: uuidToString(workspaceID), OrgID: scope.OrgID},
		task:  task, scope: scope, scene: summary,
		routineRun: service.IsSceneRoutineContext(task.Context),
	}
	if summary.Kind == scene.KindDM {
		target.senderOpenDingTalkID = dispatchSenderOpenID(task.Context)
	}
	return target, true, nil
}

// taskHasConfigScene reports whether task gets the config-qwen-tag-scene
// skill and server: whether it has a current scene to configure. A lookup
// failure is logged and counts as none.
func (h *Handler) taskHasConfigScene(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue) bool {
	_, ok, err := h.taskConfigScene(ctx, workspaceID, task)
	if err != nil {
		slog.WarnContext(ctx, "scene config: scene lookup failed; no scene configuration for the task",
			"task_id", uuidToString(task.ID), "agent_id", uuidToString(task.AgentID), "error", err)
	}
	return ok
}

// dispatchSenderOpenID is the dispatch sender's openDingTalkId of a task
// context ("" when absent).
func dispatchSenderOpenID(raw []byte) string {
	var envelope struct {
		EventData *struct {
			Sender struct {
				OpenDingTalkID       string `json:"openDingTalkId"`
				SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
			} `json:"sender"`
		} `json:"dispatch_event_data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.EventData == nil {
		return ""
	}
	return strings.TrimSpace(firstNonEmpty(envelope.EventData.Sender.OpenDingTalkID, envelope.EventData.Sender.SenderOpenDingTalkID))
}

// sceneConfigMCPRoute is the route path of the task's config-qwen-tag-scene
// server, with a freshly issued scene token; ok is false when the task has no
// scene to configure.
func (h *Handler) sceneConfigMCPRoute(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue) (string, bool) {
	target, ok, err := h.taskConfigScene(ctx, workspaceID, task)
	if err != nil {
		slog.WarnContext(ctx, "scene config MCP: scene lookup failed; not mounting the server",
			"task_id", uuidToString(task.ID), "agent_id", uuidToString(task.AgentID), "error", err)
		return "", false
	}
	if !ok {
		return "", false
	}
	token, err := auth.IssueSceneToken(auth.SceneTokenClaims{
		WorkspaceID: uuidToString(workspaceID), AgentID: uuidToString(task.AgentID),
		TaskID: uuidToString(task.ID), SceneID: target.scene.SceneID,
	}, time.Now())
	if err != nil {
		slog.WarnContext(ctx, "scene config MCP: token issue failed; not mounting the server", "task_id", uuidToString(task.ID), "error", err)
		return "", false
	}
	return protocol.SceneConfigMCPPathPrefix + token, true
}

// SceneConfigMCP serves the config-qwen-tag-scene MCP server:
// POST /api/scene-config/mcp/{sceneToken}, task token only.
func (h *Handler) SceneConfigMCP(w http.ResponseWriter, r *http.Request) {
	if !multicaMCPTaskTokenAuthenticated(r) {
		writeError(w, http.StatusForbidden, "the scene configuration server requires a task token")
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
	claims, err := auth.VerifySceneToken(chi.URLParam(r, "sceneToken"), time.Now())
	if err != nil || claims.TaskID != r.Header.Get("X-Task-ID") || claims.AgentID != r.Header.Get("X-Agent-ID") ||
		claims.WorkspaceID != r.Header.Get("X-Workspace-ID") {
		slog.WarnContext(r.Context(), "scene config MCP: scene token refused", "task_id", r.Header.Get("X-Task-ID"),
			"agent_id", r.Header.Get("X-Agent-ID"), "verified", err == nil)
		writeError(w, http.StatusForbidden, "scene token is invalid for this task")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, sceneConfigMCPMaxRequestBytes)
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
		h.writeMulticaMCPResult(w, req.ID, map[string]any{
			"protocolVersion": multicaMCPProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": sceneConfigMCPServerName, "version": "1.0.0"},
			"instructions":    "Tools of the current DingTalk scene's configuration. They act only on this run's scene; read the config-qwen-tag-scene skill before changing anything.",
		})
	case "ping":
		h.writeMulticaMCPResult(w, req.ID, map[string]any{})
	case "tools/list":
		target, ok := h.sceneConfigMCPTarget(w, r, req.ID, claims)
		if !ok {
			return
		}
		h.writeMulticaMCPResult(w, req.ID, map[string]any{"tools": sceneConfigToolDefinitions(target.scene.Kind)})
	case "tools/call":
		target, ok := h.sceneConfigMCPTarget(w, r, req.ID, claims)
		if !ok {
			return
		}
		h.handleSceneConfigToolCall(w, r, req, target)
	default:
		h.writeMulticaMCPError(w, req.ID, -32601, "method not found")
	}
}

// sceneConfigMCPTarget loads the calling task and its scene and checks they
// are still the token's: an active task of the agent whose scene re-resolves
// to the same scene_id.
func (h *Handler) sceneConfigMCPTarget(w http.ResponseWriter, r *http.Request, id json.RawMessage, claims auth.SceneTokenClaims) (sceneConfigTarget, bool) {
	ctx := r.Context()
	refuse := func(message string) (sceneConfigTarget, bool) {
		h.writeMulticaMCPToolError(w, id, message)
		return sceneConfigTarget{}, false
	}
	workspaceID, err := util.ParseUUID(claims.WorkspaceID)
	if err != nil {
		return refuse("scene_token_invalid")
	}
	taskID, err := util.ParseUUID(claims.TaskID)
	if err != nil {
		return refuse("scene_token_invalid")
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return refuse("task_not_found")
	}
	if err != nil {
		slog.ErrorContext(ctx, "scene config MCP: task lookup failed", "task_id", claims.TaskID, "error", err)
		return refuse("scene configuration is unavailable")
	}
	if uuidToString(task.AgentID) != claims.AgentID {
		return refuse("scene_token_invalid")
	}
	if task.Status != "running" && task.Status != "dispatched" {
		return refuse("task_not_active: this run has ended; its scene token no longer works")
	}
	target, ok, err := h.taskConfigScene(ctx, workspaceID, task)
	if err != nil {
		slog.ErrorContext(ctx, "scene config MCP: scene lookup failed", "task_id", claims.TaskID, "error", err)
		return refuse("scene configuration is unavailable")
	}
	if !ok || target.scene.SceneID != claims.SceneID {
		return refuse("scene_changed: this run's scene can no longer be configured")
	}
	return target, true
}

// ── Tools ───────────────────────────────────────────────────────────────────

const (
	sceneConfigToolGet           = "scene_config_get"
	sceneConfigToolPromptUpsert  = "scene_prompt_upsert"
	sceneConfigToolPromptDelete  = "scene_prompt_delete"
	sceneConfigToolMCPUpsert     = "scene_mcp_server_upsert"
	sceneConfigToolMCPDelete     = "scene_mcp_server_delete"
	sceneConfigToolCapabilitySet = "scene_capability_set"
	sceneConfigToolConnectLink   = "scene_connect_link"
	sceneConfigToolRoutineList   = "scene_routine_list"
	sceneConfigToolRoutineCreate = "scene_routine_create"
	sceneConfigToolRoutineUpdate = "scene_routine_update"
	sceneConfigToolRoutineDelete = "scene_routine_delete"
	sceneConfigToolRoutineRun    = "scene_routine_run"
)

// sceneConfigWriteTools change the scene; a routine run may not call them.
var sceneConfigWriteTools = map[string]bool{
	sceneConfigToolPromptUpsert: true, sceneConfigToolPromptDelete: true,
	sceneConfigToolMCPUpsert: true, sceneConfigToolMCPDelete: true,
	sceneConfigToolCapabilitySet: true,
	sceneConfigToolRoutineCreate: true, sceneConfigToolRoutineUpdate: true,
	sceneConfigToolRoutineDelete: true, sceneConfigToolRoutineRun: true,
}

func sceneConfigTool(name, title, description string, properties map[string]any, required []string, readOnly bool) map[string]any {
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name": name, "title": title, "description": description, "inputSchema": schema,
		"annotations": map[string]any{"readOnlyHint": readOnly, "destructiveHint": false, "idempotentHint": readOnly, "openWorldHint": false},
	}
}

func sceneConfigToolDefinitions(kind string) []any {
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	routineID := str("The routine id from scene_routine_list.")
	trigger := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"kind":     map[string]any{"type": "string", "enum": []string{"schedule", "webhook"}},
			"cron":     str(`Five-field cron for a schedule, e.g. "0 9 * * 1-5" (weekdays 09:00). At most every 15 minutes.`),
			"timezone": str("IANA timezone of a schedule; default Asia/Shanghai."),
		},
		"required": []string{"kind"},
	}
	where := "this group chat"
	if kind == scene.KindDM {
		where = "this 1:1 chat"
	}
	return []any{
		sceneConfigTool(sceneConfigToolGet, "Read this scene's configuration",
			"Return the configuration of "+where+": its prompts, routines, offered skills and connectors with their switches, remote MCP servers (header values hidden), and whether this run may change them. Call it before answering or changing anything.",
			map[string]any{}, nil, true),
		sceneConfigTool(sceneConfigToolPromptUpsert, "Add or change a prompt",
			"Add a named prompt to "+where+", or replace the prompt of that name. It is added to every later run here. Never put secrets in it.",
			map[string]any{
				"name":    str("Prompt name, unique in this scene."),
				"text":    str("The prompt text."),
				"order":   map[string]any{"type": "integer", "description": "Position among the prompts; default last."},
				"enabled": map[string]any{"type": "boolean", "description": "false keeps it stored but off; default true."},
			}, []string{"name", "text"}, false),
		sceneConfigTool(sceneConfigToolPromptDelete, "Delete a prompt", "Delete the prompt of that name from "+where+".",
			map[string]any{"name": str("Prompt name.")}, []string{"name"}, false),
		sceneConfigTool(sceneConfigToolMCPUpsert, "Add or change a remote MCP server",
			"Add a remote MCP server to "+where+", or replace the one of that name. http(s) url only; never local commands. Put no secrets in headers: connect accounts through scene_connect_link instead.",
			map[string]any{
				"name":     str("Server name; multica, config-qwen-tag-scene and c<16 hex> are reserved."),
				"url":      str("https URL of the server."),
				"type":     map[string]any{"type": "string", "enum": []string{"http", "sse"}},
				"headers":  map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
				"disabled": map[string]any{"type": "boolean"},
			}, []string{"name", "url"}, false),
		sceneConfigTool(sceneConfigToolMCPDelete, "Delete a remote MCP server", "Remove the MCP server of that name from "+where+".",
			map[string]any{"name": str("Server name.")}, []string{"name"}, false),
		sceneConfigTool(sceneConfigToolCapabilitySet, "Switch an offered skill or connector",
			"Switch a skill or connector the agent's manager offered to scenes (「公开给场域」) on or off in "+where+". The agent's own skills and connectors are always on and cannot be switched here.",
			map[string]any{
				"kind":    map[string]any{"type": "string", "enum": []string{contextcap.ResourceSkill, contextcap.ResourceConnector}},
				"id":      str("The id from scene_config_get offered list."),
				"enabled": map[string]any{"type": "boolean"},
			}, []string{"kind", "id", "enabled"}, false),
		sceneConfigTool(sceneConfigToolConnectLink, "Create a configuration link",
			"Create a configuration link for connecting accounts or managing "+where+" on the configuration page. Post the returned url verbatim. Accounts are never connected in chat.",
			map[string]any{}, nil, false),
		sceneConfigTool(sceneConfigToolRoutineList, "List routines", "List the routines (例行任务) of "+where+" with their schedule, next run and last result.",
			map[string]any{}, nil, true),
		sceneConfigTool(sceneConfigToolRoutineCreate, "Create a routine",
			"Create a routine in "+where+": work you do here on a cron schedule or when a webhook request arrives, with this scene's configuration. The platform posts a start and an end message here. The same purpose and schedule as an existing routine updates it instead and keeps its paused or running state.",
			map[string]any{
				"title":        str("Short name of the routine."),
				"instructions": str("What to do on each run and what the result should contain. Do not ask to post it; the platform does."),
				"trigger":      trigger,
			}, []string{"title", "instructions", "trigger"}, false),
		sceneConfigTool(sceneConfigToolRoutineUpdate, "Change, pause or resume a routine",
			"Change a routine of "+where+". enabled=false pauses it, true resumes it. A schedule can change its cron and timezone; the trigger kind cannot change.",
			map[string]any{
				"routine_id":   routineID,
				"title":        str("New name."),
				"instructions": str("New instructions."),
				"enabled":      map[string]any{"type": "boolean"},
				"cron":         str("New five-field cron."),
				"timezone":     str("New IANA timezone."),
			}, []string{"routine_id"}, false),
		sceneConfigTool(sceneConfigToolRoutineDelete, "Delete a routine", "Delete a routine of "+where+"; its run history stays.",
			map[string]any{"routine_id": routineID}, []string{"routine_id"}, false),
		sceneConfigTool(sceneConfigToolRoutineRun, "Run a routine now", "Start one run of a routine of "+where+" now.",
			map[string]any{"routine_id": routineID}, []string{"routine_id"}, false),
	}
}

type sceneConfigToolError struct{ code, message string }

func (e *sceneConfigToolError) Error() string { return e.code + ": " + e.message }

func toolRefusal(code, message string) error {
	return &sceneConfigToolError{code: code, message: message}
}

func (h *Handler) handleSceneConfigToolCall(w http.ResponseWriter, r *http.Request, req multicaMCPRequest, target sceneConfigTarget) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
		h.writeMulticaMCPError(w, req.ID, -32602, "invalid tools/call parameters")
		return
	}
	if sceneConfigWriteTools[params.Name] && target.routineRun {
		h.writeMulticaMCPToolError(w, req.ID, "routine_run_read_only: a routine run cannot change its scene's configuration")
		return
	}
	ctx := r.Context()
	var (
		result any
		notice string
		err    error
	)
	switch params.Name {
	case sceneConfigToolGet:
		result, err = h.sceneConfigGet(ctx, target)
	case sceneConfigToolPromptUpsert:
		result, notice, err = h.sceneConfigPromptUpsert(ctx, target, params.Arguments)
	case sceneConfigToolPromptDelete:
		result, notice, err = h.sceneConfigPromptDelete(ctx, target, params.Arguments)
	case sceneConfigToolMCPUpsert:
		result, notice, err = h.sceneConfigMCPUpsert(ctx, target, params.Arguments)
	case sceneConfigToolMCPDelete:
		result, notice, err = h.sceneConfigMCPDelete(ctx, target, params.Arguments)
	case sceneConfigToolCapabilitySet:
		result, notice, err = h.sceneConfigCapabilitySet(ctx, target, params.Arguments)
	case sceneConfigToolConnectLink:
		if err = decodeMulticaMCPArguments(params.Arguments, &struct{}{}); err == nil {
			result, err = h.createContextConfigLink(r, "")
		}
	case sceneConfigToolRoutineList:
		if err = decodeMulticaMCPArguments(params.Arguments, &struct{}{}); err == nil {
			var routines []sceneRoutineView
			if routines, err = h.listSceneRoutines(ctx, target.agent, target.scene.SceneID); err == nil {
				result = map[string]any{"routines": routines}
			}
		}
	case sceneConfigToolRoutineCreate:
		result, notice, err = h.sceneConfigRoutineCreate(ctx, target, params.Arguments)
	case sceneConfigToolRoutineUpdate:
		result, notice, err = h.sceneConfigRoutineUpdate(ctx, target, params.Arguments)
	case sceneConfigToolRoutineDelete:
		result, notice, err = h.sceneConfigRoutineDelete(ctx, target, params.Arguments)
	case sceneConfigToolRoutineRun:
		result, notice, err = h.sceneConfigRoutineRun(ctx, target, params.Arguments)
	default:
		h.writeMulticaMCPError(w, req.ID, -32602, "unknown tool")
		return
	}
	if err != nil {
		h.writeSceneConfigToolFailure(w, r, req.ID, params.Name, target, err)
		return
	}
	slog.InfoContext(ctx, "scene config MCP: tool call", "tool", params.Name, "task_id", uuidToString(target.task.ID),
		"agent_id", target.agent.ID, "scene_id", target.scene.SceneID)
	if notice != "" {
		h.postSceneConfigNotice(ctx, target, notice)
	}
	payload, _ := json.Marshal(result)
	h.writeMulticaMCPResult(w, req.ID, multicaMCPToolResult{
		Content:           []multicaMCPContent{{Type: "text", Text: string(payload)}},
		StructuredContent: result,
	})
}

func (h *Handler) writeSceneConfigToolFailure(w http.ResponseWriter, r *http.Request, id json.RawMessage, tool string, target sceneConfigTarget, err error) {
	var (
		toolErr    *sceneConfigToolError
		routineErr *sceneRoutineError
		linkErr    *multicaMCPToolCallError
	)
	switch {
	case errors.As(err, &toolErr):
		h.writeMulticaMCPToolError(w, id, toolErr.code+": "+toolErr.message)
	case errors.As(err, &routineErr):
		h.writeMulticaMCPToolError(w, id, routineErr.Code+": "+routineErr.Message)
	case errors.As(err, &linkErr):
		h.writeMulticaMCPToolError(w, id, linkErr.message)
	case errors.Is(err, errMulticaMCPInvalidArguments):
		h.writeMulticaMCPError(w, id, -32602, "invalid "+tool+" arguments")
	default:
		slog.ErrorContext(r.Context(), "scene config MCP: tool failed", "tool", tool, "task_id", uuidToString(target.task.ID),
			"scene_id", target.scene.SceneID, "error", err)
		h.writeMulticaMCPToolError(w, id, tool+" failed")
	}
}

var errMulticaMCPInvalidArguments = errors.New("invalid tool arguments")

func decodeSceneConfigArguments(raw json.RawMessage, target any) error {
	if err := decodeMulticaMCPArguments(raw, target); err != nil {
		return errMulticaMCPInvalidArguments
	}
	return nil
}

// ── Read ────────────────────────────────────────────────────────────────────

type sceneConfigNamedItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type sceneConfigMCPServerView struct {
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	Type        string   `json:"type,omitempty"`
	Disabled    bool     `json:"disabled"`
	HeaderNames []string `json:"header_names,omitempty"`
}

func (h *Handler) sceneConfigGet(ctx context.Context, target sceneConfigTarget) (any, error) {
	a, sceneID := target.agent, target.scene.SceneID
	prompts, err := contextcap.ListPromptComponents(ctx, h.DB, a.WorkspaceID, a.ID, contextcap.ScopeScene, a.OrgID, sceneID)
	if err != nil {
		return nil, err
	}
	promptViews := make([]map[string]any, 0, len(prompts))
	for _, p := range prompts {
		promptViews = append(promptViews, map[string]any{"name": p.Name, "order": p.Order, "enabled": !p.Disabled, "text": p.Text})
	}
	servers, err := h.sceneConfigMCPServers(ctx, target)
	if err != nil {
		return nil, err
	}
	serverViews := make([]sceneConfigMCPServerView, 0, len(servers))
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var server struct {
			URL      string            `json:"url"`
			Type     string            `json:"type"`
			Headers  map[string]string `json:"headers"`
			Disabled bool              `json:"disabled"`
		}
		_ = json.Unmarshal(servers[name], &server)
		view := sceneConfigMCPServerView{Name: name, URL: server.URL, Type: server.Type, Disabled: server.Disabled}
		for header := range server.Headers {
			view.HeaderNames = append(view.HeaderNames, header)
		}
		sort.Strings(view.HeaderNames)
		serverViews = append(serverViews, view)
	}
	offers, err := contextcap.ListOffers(ctx, h.DB, a.WorkspaceID, a.ID)
	if err != nil {
		return nil, err
	}
	bindings, err := contextcap.ListScopeBindings(ctx, h.DB, a.WorkspaceID, a.ID, contextcap.ScopeScene, a.OrgID, sceneID)
	if err != nil {
		return nil, err
	}
	on := map[string]bool{}
	for _, b := range bindings {
		if b.Enabled {
			on[b.ResourceType+":"+b.ResourceID] = true
		}
	}
	offeredConnectors, err := h.sceneConfigNames(ctx, `SELECT id::text, name FROM internal_connector WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])`, a.WorkspaceID, offers.ConnectorIDs)
	if err != nil {
		return nil, err
	}
	offeredSkills, err := h.sceneConfigNames(ctx, `SELECT id::text, name FROM skill WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])`, a.WorkspaceID, offers.SkillIDs)
	if err != nil {
		return nil, err
	}
	for i := range offeredConnectors {
		offeredConnectors[i].Enabled = on[contextcap.ResourceConnector+":"+offeredConnectors[i].ID]
	}
	for i := range offeredSkills {
		offeredSkills[i].Enabled = on[contextcap.ResourceSkill+":"+offeredSkills[i].ID]
	}
	alwaysConnectors, err := h.sceneConfigNames(ctx, `SELECT ic.id::text, ic.name FROM internal_connector_agent ica
		JOIN internal_connector ic ON ic.id = ica.connector_id
		WHERE ica.workspace_id = $1::uuid AND ica.agent_id = ANY($2::uuid[])`, a.WorkspaceID, []string{a.ID})
	if err != nil {
		return nil, err
	}
	alwaysSkills, err := h.sceneConfigNames(ctx, `SELECT s.id::text, s.name FROM agent_skill ags JOIN skill s ON s.id = ags.skill_id
		WHERE s.workspace_id = $1::uuid AND ags.agent_id = ANY($2::uuid[])`, a.WorkspaceID, []string{a.ID})
	if err != nil {
		return nil, err
	}
	routines, err := h.listSceneRoutines(ctx, a, sceneID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"scene":       map[string]any{"scene_id": sceneID, "kind": target.scene.Kind, "title": target.scene.Title},
		"read_only":   target.routineRun,
		"prompts":     promptViews,
		"mcp_servers": serverViews,
		"capabilities": map[string]any{
			"always_on": map[string]any{"connectors": alwaysConnectors, "skills": alwaysSkills},
			"offered":   map[string]any{"connectors": offeredConnectors, "skills": offeredSkills},
		},
		"routines": routines,
	}, nil
}

func (h *Handler) sceneConfigNames(ctx context.Context, query, workspaceID string, ids []string) ([]sceneConfigNamedItem, error) {
	out := []sceneConfigNamedItem{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := h.DB.Query(ctx, query, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item sceneConfigNamedItem
		if err := rows.Scan(&item.ID, &item.Name); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, rows.Err()
}

func (h *Handler) sceneConfigMCPServers(ctx context.Context, target sceneConfigTarget) (map[string]json.RawMessage, error) {
	a := target.agent
	stored, err := contextcap.GetScopeMCPConfig(ctx, h.DB, a.WorkspaceID, a.ID, contextcap.ScopeScene, a.OrgID, target.scene.SceneID)
	if errors.Is(err, contextcap.ErrNotFound) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(stored.MCPConfig) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	servers, err := contextcap.MCPServers(stored.MCPConfig)
	if err != nil {
		return nil, err
	}
	if servers == nil {
		servers = map[string]json.RawMessage{}
	}
	return servers, nil
}

// ── Prompts ─────────────────────────────────────────────────────────────────

func (h *Handler) sceneConfigPromptUpsert(ctx context.Context, target sceneConfigTarget, raw json.RawMessage) (any, string, error) {
	var args struct {
		Name    string `json:"name"`
		Text    string `json:"text"`
		Order   *int   `json:"order"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeSceneConfigArguments(raw, &args); err != nil {
		return nil, "", err
	}
	name := strings.TrimSpace(args.Name)
	prompts, err := h.sceneConfigPrompts(ctx, target)
	if err != nil {
		return nil, "", err
	}
	added := true
	for i := range prompts {
		if prompts[i].Name == name {
			added = false
			prompts[i].Text = args.Text
			if args.Order != nil {
				prompts[i].Order = *args.Order
			}
			if args.Enabled != nil {
				prompts[i].Disabled = !*args.Enabled
			}
		}
	}
	if added {
		order := len(prompts) + 1
		if args.Order != nil {
			order = *args.Order
		}
		prompts = append(prompts, contextcap.PromptComponentInput{Name: name, Text: args.Text, Order: order, Disabled: args.Enabled != nil && !*args.Enabled})
	}
	stored, err := h.writeSceneConfigPrompts(ctx, target, prompts)
	if err != nil {
		return nil, "", err
	}
	verb := "更新"
	if added {
		verb = "新增"
	}
	return map[string]any{"prompts": stored}, fmt.Sprintf("%s提示词「%s」", verb, name), nil
}

func (h *Handler) sceneConfigPromptDelete(ctx context.Context, target sceneConfigTarget, raw json.RawMessage) (any, string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := decodeSceneConfigArguments(raw, &args); err != nil {
		return nil, "", err
	}
	name := strings.TrimSpace(args.Name)
	prompts, err := h.sceneConfigPrompts(ctx, target)
	if err != nil {
		return nil, "", err
	}
	kept := prompts[:0]
	for _, p := range prompts {
		if p.Name != name {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(prompts) {
		return nil, "", toolRefusal("prompt_not_found", "this scene has no prompt named "+name)
	}
	stored, err := h.writeSceneConfigPrompts(ctx, target, kept)
	if err != nil {
		return nil, "", err
	}
	return map[string]any{"prompts": stored}, fmt.Sprintf("删除提示词「%s」", name), nil
}

func (h *Handler) sceneConfigPrompts(ctx context.Context, target sceneConfigTarget) ([]contextcap.PromptComponentInput, error) {
	a := target.agent
	stored, err := contextcap.ListPromptComponents(ctx, h.DB, a.WorkspaceID, a.ID, contextcap.ScopeScene, a.OrgID, target.scene.SceneID)
	if err != nil {
		return nil, err
	}
	out := make([]contextcap.PromptComponentInput, 0, len(stored))
	for _, p := range stored {
		out = append(out, contextcap.PromptComponentInput{Name: p.Name, Order: p.Order, Text: p.Text, Disabled: p.Disabled})
	}
	return out, nil
}

func (h *Handler) writeSceneConfigPrompts(ctx context.Context, target sceneConfigTarget, prompts []contextcap.PromptComponentInput) ([]map[string]any, error) {
	if _, err := contextcap.NormalizePromptComponents(prompts); err != nil {
		if errors.Is(err, contextcap.ErrDuplicatePromptName) {
			return nil, toolRefusal("invalid_prompts", "two prompts have the same name")
		}
		return nil, toolRefusal("invalid_prompts", "a prompt needs a short name without control characters and a non-empty text, and a scene holds at most "+fmt.Sprint(contextcap.MaxPromptComponents)+" prompts")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	a := target.agent
	stored, err := contextcap.ReplacePromptComponents(ctx, tx, contextcap.PromptComponentsWrite{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: contextcap.ScopeScene, OrgID: a.OrgID,
		ScopeKey: target.scene.SceneID, Components: prompts,
	})
	if errors.Is(err, contextcap.ErrInvalidInput) {
		return nil, toolRefusal("invalid_prompts", "the prompts are invalid")
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(stored))
	for _, p := range stored {
		out = append(out, map[string]any{"name": p.Name, "order": p.Order, "enabled": !p.Disabled})
	}
	return out, nil
}

// ── MCP servers ─────────────────────────────────────────────────────────────

func (h *Handler) sceneConfigMCPUpsert(ctx context.Context, target sceneConfigTarget, raw json.RawMessage) (any, string, error) {
	var args struct {
		Name     string            `json:"name"`
		URL      string            `json:"url"`
		Type     string            `json:"type"`
		Headers  map[string]string `json:"headers"`
		Disabled bool              `json:"disabled"`
	}
	if err := decodeSceneConfigArguments(raw, &args); err != nil {
		return nil, "", err
	}
	name := strings.TrimSpace(args.Name)
	servers, err := h.sceneConfigMCPServers(ctx, target)
	if err != nil {
		return nil, "", err
	}
	server := map[string]any{"url": strings.TrimSpace(args.URL)}
	if args.Type != "" {
		server["type"] = args.Type
	}
	if len(args.Headers) > 0 {
		server["headers"] = args.Headers
	}
	if args.Disabled {
		server["disabled"] = true
	}
	_, existed := servers[name]
	encoded, _ := json.Marshal(server)
	servers[name] = encoded
	if err := h.writeSceneConfigMCPServers(ctx, target, servers); err != nil {
		return nil, "", err
	}
	verb := "更新"
	if !existed {
		verb = "新增"
	}
	return map[string]any{"name": name, "url": server["url"], "disabled": args.Disabled},
		fmt.Sprintf("%s MCP 服务器「%s」", verb, name), nil
}

func (h *Handler) sceneConfigMCPDelete(ctx context.Context, target sceneConfigTarget, raw json.RawMessage) (any, string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := decodeSceneConfigArguments(raw, &args); err != nil {
		return nil, "", err
	}
	name := strings.TrimSpace(args.Name)
	servers, err := h.sceneConfigMCPServers(ctx, target)
	if err != nil {
		return nil, "", err
	}
	if _, ok := servers[name]; !ok {
		return nil, "", toolRefusal("mcp_server_not_found", "this scene has no MCP server named "+name)
	}
	delete(servers, name)
	if err := h.writeSceneConfigMCPServers(ctx, target, servers); err != nil {
		return nil, "", err
	}
	return map[string]any{"deleted": name}, fmt.Sprintf("删除 MCP 服务器「%s」", name), nil
}

func (h *Handler) writeSceneConfigMCPServers(ctx context.Context, target sceneConfigTarget, servers map[string]json.RawMessage) error {
	document, _ := json.Marshal(map[string]any{"mcpServers": servers})
	if len(servers) == 0 {
		document = json.RawMessage("null")
	}
	config, reason := normalizeRemoteScopeMCPConfig(document)
	if reason != "" {
		return toolRefusal("invalid_mcp_config", reason)
	}
	a := target.agent
	_, err := contextcap.PutScopeMCPConfig(ctx, h.DB, contextcap.ScopeMCPConfigWrite{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: contextcap.ScopeScene, OrgID: a.OrgID,
		ScopeKey: target.scene.SceneID, MCPConfig: config,
	})
	if errors.Is(err, contextcap.ErrInvalidInput) {
		return toolRefusal("invalid_mcp_config", "the MCP server configuration is invalid")
	}
	return err
}

// ── Skills and connectors ───────────────────────────────────────────────────

func (h *Handler) sceneConfigCapabilitySet(ctx context.Context, target sceneConfigTarget, raw json.RawMessage) (any, string, error) {
	var args struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := decodeSceneConfigArguments(raw, &args); err != nil {
		return nil, "", err
	}
	if args.Kind != contextcap.ResourceSkill && args.Kind != contextcap.ResourceConnector {
		return nil, "", toolRefusal("invalid_kind", `kind must be "skill" or "connector"`)
	}
	a := target.agent
	binding, err := contextcap.UpsertBinding(ctx, h.DB, contextcap.BindingWrite{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: contextcap.ScopeScene, OrgID: a.OrgID,
		ScopeKey: target.scene.SceneID, ScopeTitle: target.scene.Title,
		ResourceType: args.Kind, ResourceID: strings.TrimSpace(args.ID), Enabled: args.Enabled,
	})
	switch {
	case errors.Is(err, contextcap.ErrNotOffered):
		return nil, "", toolRefusal("not_offered", "this "+args.Kind+" is not offered to scenes; only items in scene_config_get offered lists can be switched")
	case errors.Is(err, contextcap.ErrInvalidInput):
		return nil, "", toolRefusal("invalid_id", "the id is not a valid "+args.Kind+" id")
	case err != nil:
		return nil, "", err
	}
	label := "Skill"
	if args.Kind == contextcap.ResourceConnector {
		label = "连接器"
	}
	state := "关闭"
	if binding.Enabled {
		state = "开启"
	}
	name := h.sceneConfigResourceName(ctx, a.WorkspaceID, args.Kind, binding.ResourceID)
	return map[string]any{"kind": args.Kind, "id": binding.ResourceID, "enabled": binding.Enabled},
		fmt.Sprintf("%s%s「%s」", state, label, name), nil
}

func (h *Handler) sceneConfigResourceName(ctx context.Context, workspaceID, kind, id string) string {
	query := `SELECT name FROM skill WHERE workspace_id = $1::uuid AND id = $2::uuid`
	if kind == contextcap.ResourceConnector {
		query = `SELECT name FROM internal_connector WHERE workspace_id = $1::uuid AND id = $2::uuid`
	}
	var name string
	if err := h.DB.QueryRow(ctx, query, workspaceID, id).Scan(&name); err != nil || name == "" {
		return id
	}
	return name
}

// ── Routines ────────────────────────────────────────────────────────────────

func sceneConfigActor(target sceneConfigTarget) sceneRoutineActor {
	return sceneRoutineActor{Type: contextcap.RoutineCreatedByAgent, AgentID: target.task.AgentID, TaskID: target.task.ID}
}

// sceneConfigRoutineView keeps a full webhook URL out of the conversation:
// it is shown once on the configuration page only.
func sceneConfigRoutineView(view sceneRoutineView) sceneRoutineView {
	view.Trigger.WebhookURL = ""
	return view
}

func (h *Handler) sceneConfigRoutine(ctx context.Context, target sceneConfigTarget, id string) (contextcap.Routine, error) {
	routine, err := h.loadSceneRoutine(ctx, target.agent, strings.TrimSpace(id))
	if err == nil && routine.SceneID != target.scene.SceneID {
		err = routineRefusal(http.StatusNotFound, "routine_not_found", "this scene has no such routine")
	}
	return routine, err
}

func (h *Handler) sceneConfigRoutineCreate(ctx context.Context, target sceneConfigTarget, raw json.RawMessage) (any, string, error) {
	var args sceneRoutineInput
	if err := decodeSceneConfigArguments(raw, &args); err != nil {
		return nil, "", err
	}
	sc, err := h.sceneRoutineScene(ctx, target.agent, target.scene.SceneID)
	if err != nil {
		return nil, "", err
	}
	var cp sceneRoutineCounterpart
	if sc.SceneKind == scene.KindDM {
		cp = sceneRoutineCounterpart{OpenDingTalkID: target.senderOpenDingTalkID, StaffID: target.scope.PersonKey}
	}
	result, err := h.createSceneRoutine(ctx, target.agent, sc, sceneConfigActor(target), cp, args)
	if err != nil {
		return nil, "", err
	}
	result.Routine = sceneConfigRoutineView(result.Routine)
	verb := "新增"
	if result.Updated {
		verb = "更新"
	}
	return result, fmt.Sprintf("%s例行任务「%s」", verb, result.Routine.Title), nil
}

func (h *Handler) sceneConfigRoutineUpdate(ctx context.Context, target sceneConfigTarget, raw json.RawMessage) (any, string, error) {
	var args struct {
		RoutineID string `json:"routine_id"`
		sceneRoutinePatch
	}
	if err := decodeSceneConfigArguments(raw, &args); err != nil {
		return nil, "", err
	}
	routine, err := h.sceneConfigRoutine(ctx, target, args.RoutineID)
	if err != nil {
		return nil, "", err
	}
	result, err := h.updateSceneRoutine(ctx, target.agent, routine, sceneConfigActor(target), args.sceneRoutinePatch)
	if err != nil {
		return nil, "", err
	}
	result.Routine = sceneConfigRoutineView(result.Routine)
	change := "修改"
	if args.Enabled != nil && args.Title == nil && args.Instructions == nil && args.Cron == nil && args.Timezone == nil {
		change = map[bool]string{true: "恢复", false: "暂停"}[*args.Enabled]
	}
	return result, fmt.Sprintf("%s例行任务「%s」", change, result.Routine.Title), nil
}

func (h *Handler) sceneConfigRoutineDelete(ctx context.Context, target sceneConfigTarget, raw json.RawMessage) (any, string, error) {
	var args struct {
		RoutineID string `json:"routine_id"`
	}
	if err := decodeSceneConfigArguments(raw, &args); err != nil {
		return nil, "", err
	}
	routine, err := h.sceneConfigRoutine(ctx, target, args.RoutineID)
	if err != nil {
		return nil, "", err
	}
	title := ""
	if ap, _, err := h.loadRoutineAutopilot(ctx, routine); err == nil {
		title = ap.Title
	}
	if err := h.deleteSceneRoutine(ctx, target.agent, routine, sceneConfigActor(target)); err != nil {
		return nil, "", err
	}
	return map[string]any{"deleted": routine.ID}, fmt.Sprintf("删除例行任务「%s」", title), nil
}

func (h *Handler) sceneConfigRoutineRun(ctx context.Context, target sceneConfigTarget, raw json.RawMessage) (any, string, error) {
	var args struct {
		RoutineID string `json:"routine_id"`
	}
	if err := decodeSceneConfigArguments(raw, &args); err != nil {
		return nil, "", err
	}
	routine, err := h.sceneConfigRoutine(ctx, target, args.RoutineID)
	if err != nil {
		return nil, "", err
	}
	run, err := h.runSceneRoutine(ctx, routine, sceneConfigActor(target))
	if err != nil {
		return nil, "", err
	}
	// The run posts its own start notice; no change notice.
	return map[string]any{"run": run}, "", nil
}

// ── Change notices ──────────────────────────────────────────────────────────

// postSceneConfigNotice tells the scene a configuration change was made from
// a conversation (best effort): members see every change, whatever the
// model says about it.
func (h *Handler) postSceneConfigNotice(ctx context.Context, target sceneConfigTarget, change string) {
	if h.DingTalkResponses == nil || h.TxStarter == nil {
		return
	}
	owner := scene.Owner{WorkspaceID: parseUUID(target.agent.WorkspaceID), AgentID: parseUUID(target.agent.ID)}
	identity, err := h.routineIdentity(ctx, h.Queries, owner.WorkspaceID, owner.AgentID, target.agent.OrgID)
	if err != nil {
		return
	}
	in := dingtalkresponse.ActionInput{
		WorkspaceID: target.agent.WorkspaceID, AgentID: target.agent.ID, DWSUID: identity.DwsUid, DWSOrgID: identity.OrgID,
		SceneID: target.scene.SceneID, ConversationID: target.scene.ConversationID, IsGroup: target.scene.Kind == scene.KindGroup,
		Text:           "场域配置已更新：" + change + "。可以在配置页的「QwenTag配置」里查看。",
		DWSEnvironment: h.routineDWSEnvironment(ctx, h.Queries, owner, identity),
	}
	if !in.IsGroup {
		if target.senderOpenDingTalkID == "" {
			return
		}
		in.SenderOpenDingTalkID = target.senderOpenDingTalkID
	}
	if policy, err := h.Queries.GetAgentDingTalkResponsePolicy(ctx, owner.AgentID); err == nil {
		in.ShowAITag = policy.DingtalkShowAiTag
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	if _, err := h.DingTalkResponses.EnqueueSceneNotice(ctx, tx, in, uuid.NewString()); err != nil {
		slog.WarnContext(ctx, "scene config MCP: change notice not enqueued", "scene_id", target.scene.SceneID, "error", err)
		return
	}
	if tx.Commit(ctx) == nil {
		h.DingTalkResponses.Notify()
	}
}
