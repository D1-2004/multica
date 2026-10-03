package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
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
	// requester is the display name of the dispatch sender who asked, for
	// the change notices ("" when the context names none).
	requester string
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
	target.requester = firstNonEmpty(scope.PersonName, dispatchSenderName(task.Context))
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

// dispatchSenderName is the dispatch sender's display name of a task context
// ("" when absent).
func dispatchSenderName(raw []byte) string {
	var envelope struct {
		EventData *struct {
			Sender struct {
				DisplayName string `json:"displayName"`
			} `json:"sender"`
		} `json:"dispatch_event_data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.EventData == nil {
		return ""
	}
	return strings.TrimSpace(envelope.EventData.Sender.DisplayName)
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
		target, ok := h.sceneConfigMCPTarget(w, r, req.ID, claims, false)
		if !ok {
			return
		}
		h.writeMulticaMCPResult(w, req.ID, map[string]any{"tools": sceneConfigToolDefinitions(target.scene.Kind)})
	case "tools/call":
		target, ok := h.sceneConfigMCPTarget(w, r, req.ID, claims, true)
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
// to the same scene_id. A refusal answers a tools/call with a readable
// ok=false result and tools/list with a JSON-RPC error.
func (h *Handler) sceneConfigMCPTarget(w http.ResponseWriter, r *http.Request, id json.RawMessage, claims auth.SceneTokenClaims, call bool) (sceneConfigTarget, bool) {
	ctx := r.Context()
	refuse := func(code, message string) (sceneConfigTarget, bool) {
		if call {
			h.writeSceneConfigRefusal(w, id, code, message)
		} else {
			h.writeMulticaMCPError(w, id, -32000, code+": "+message)
		}
		return sceneConfigTarget{}, false
	}
	unavailable := func() (sceneConfigTarget, bool) {
		if call {
			h.writeMulticaMCPToolError(w, id, "scene configuration is unavailable")
		} else {
			h.writeMulticaMCPError(w, id, -32603, "scene configuration is unavailable")
		}
		return sceneConfigTarget{}, false
	}
	workspaceID, err := util.ParseUUID(claims.WorkspaceID)
	if err != nil {
		return refuse("scene_token_invalid", "the scene token is invalid")
	}
	taskID, err := util.ParseUUID(claims.TaskID)
	if err != nil {
		return refuse("scene_token_invalid", "the scene token is invalid")
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return refuse("task_not_found", "this run was not found")
	}
	if err != nil {
		slog.ErrorContext(ctx, "scene config MCP: task lookup failed", "task_id", claims.TaskID, "error", err)
		return unavailable()
	}
	if uuidToString(task.AgentID) != claims.AgentID {
		return refuse("scene_token_invalid", "the scene token is invalid")
	}
	if task.Status != "running" && task.Status != "dispatched" {
		return refuse("task_not_active", "this run has ended; its scene token no longer works")
	}
	target, ok, err := h.taskConfigScene(ctx, workspaceID, task)
	if err != nil {
		slog.ErrorContext(ctx, "scene config MCP: scene lookup failed", "task_id", claims.TaskID, "error", err)
		return unavailable()
	}
	if !ok || target.scene.SceneID != claims.SceneID {
		return refuse("scene_changed", "this run's scene can no longer be configured")
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

// sceneConfigWriteTools change the scene or hand out access to it; a routine
// run (cron or webhook input, nobody asking in the chat) may not call them.
var sceneConfigWriteTools = map[string]bool{
	sceneConfigToolPromptUpsert: true, sceneConfigToolPromptDelete: true,
	sceneConfigToolMCPUpsert: true, sceneConfigToolMCPDelete: true,
	sceneConfigToolCapabilitySet: true, sceneConfigToolConnectLink: true,
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
	where, linkLabel := "this group chat", "配置本群能力"
	if kind == scene.KindDM {
		where, linkLabel = "this 1:1 chat", "配置本单聊能力"
	}
	mcpUpsertTitle := "Add or change a remote MCP server"
	mcpUpsertDescription := "Add a remote MCP server to " + where + ", or change the fields you pass of the one of that name (omitted fields keep their values); disabled switches it off or on. http(s) url only; never local commands. Every run here calls it, so restate the address and wait for confirmation first. Put no secrets in headers: connect accounts through scene_connect_link instead."
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
		sceneConfigTool(sceneConfigToolMCPUpsert, mcpUpsertTitle, mcpUpsertDescription,
			map[string]any{
				"name":     str("Server name; multica, config-qwen-tag-scene and c<16 hex> are reserved."),
				"url":      str("https URL of the server; required to add one, omit to keep the stored one."),
				"type":     map[string]any{"type": "string", "enum": []string{"http", "sse"}},
				"headers":  map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Replaces all stored headers; omit to keep them."},
				"disabled": map[string]any{"type": "boolean"},
			}, []string{"name"}, false),
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
			"Create the configuration link of "+where+" (场域配置链接), for connecting accounts or managing it on the configuration page; whoever opens it within 30 minutes can configure "+where+". Reply with a Markdown link whose target is the returned dingtalk_url, e.g. ["+linkLabel+"](dingtalk_url); never show the bare url. Accounts are never connected in chat.",
			map[string]any{"tab": map[string]any{"type": "string", "enum": contextConfigLinkTabs,
				"description": `Page tab to open: "scope" (场域能力, default) or "routines" (例行任务).`}}, nil, false),
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
		h.writeSceneConfigRefusal(w, req.ID, "routine_run_read_only", "a routine run cannot change its scene's configuration or issue configuration links")
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
		var args struct {
			Tab string `json:"tab"`
		}
		if err = decodeSceneConfigArguments(params.Arguments, &args); err == nil {
			result, err = h.createContextConfigLink(r, args.Tab)
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

// writeSceneConfigToolFailure answers a failed call. Refusals (a code and a
// message written for the agent to relay) are ordinary results with
// ok=false: the sandbox MCP bridge replaces the text of an isError result
// with a generic line, so an isError refusal would never reach the agent.
// Only unexpected failures stay isError, with a generic text.
func (h *Handler) writeSceneConfigToolFailure(w http.ResponseWriter, r *http.Request, id json.RawMessage, tool string, target sceneConfigTarget, err error) {
	var (
		toolErr    *sceneConfigToolError
		routineErr *sceneRoutineError
		linkErr    *multicaMCPToolCallError
	)
	switch {
	case errors.As(err, &toolErr):
		h.writeSceneConfigRefusal(w, id, toolErr.code, toolErr.message)
	case errors.As(err, &routineErr):
		h.writeSceneConfigRefusal(w, id, routineErr.Code, routineErr.Message)
	case errors.As(err, &linkErr):
		h.writeSceneConfigRefusal(w, id, "link_refused", linkErr.message)
	case errors.Is(err, errMulticaMCPInvalidArguments):
		h.writeSceneConfigRefusal(w, id, "invalid_arguments", "the arguments do not match the input schema of "+tool)
	default:
		slog.ErrorContext(r.Context(), "scene config MCP: tool failed", "tool", tool, "task_id", uuidToString(target.task.ID),
			"scene_id", target.scene.SceneID, "error", err)
		h.writeMulticaMCPToolError(w, id, tool+" failed")
	}
}

// sceneConfigRefusal is the result of a refused call.
type sceneConfigRefusal struct {
	OK      bool   `json:"ok"`
	Refused string `json:"refused"`
	Message string `json:"message"`
}

func (h *Handler) writeSceneConfigRefusal(w http.ResponseWriter, id json.RawMessage, code, message string) {
	refusal := sceneConfigRefusal{Refused: code, Message: message}
	payload, _ := json.Marshal(refusal)
	h.writeMulticaMCPResult(w, id, multicaMCPToolResult{
		Content:           []multicaMCPContent{{Type: "text", Text: string(payload)}},
		StructuredContent: refusal,
	})
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
		view := sceneConfigMCPServerView{Name: name, URL: maskMCPServerURL(server.URL), Type: server.Type, Disabled: server.Disabled}
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
		JOIN internal_connector ic ON ic.id = ica.connector_id AND ic.workspace_id = ica.workspace_id
		WHERE ica.workspace_id = $1::uuid AND ica.agent_id = ANY($2::uuid[]) AND ic.enabled`, a.WorkspaceID, []string{a.ID})
	if err != nil {
		return nil, err
	}
	alwaysSkills, err := h.sceneConfigNames(ctx, `SELECT s.id::text, s.name FROM agent_skill ags JOIN skill s ON s.id = ags.skill_id
		WHERE s.workspace_id = $1::uuid AND ags.agent_id = ANY($2::uuid[]) AND ags.enabled`, a.WorkspaceID, []string{a.ID})
	if err != nil {
		return nil, err
	}
	for i := range alwaysConnectors {
		alwaysConnectors[i].Enabled = true
	}
	for i := range alwaysSkills {
		alwaysSkills[i].Enabled = true
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

// sceneConfigMCPUpsert adds a remote MCP server or changes the fields the
// call passes; omitted fields (headers above all, whose values the agent
// never sees) keep their stored values. A group may add and re-point servers
// from the chat (冬翔's call, 2026-10-02): the guards are remote URLs only,
// masked secrets, a change notice naming who asked and the address, and
// switching off or deleting any time; a routine run changes nothing.
func (h *Handler) sceneConfigMCPUpsert(ctx context.Context, target sceneConfigTarget, raw json.RawMessage) (any, string, error) {
	var args struct {
		Name     string             `json:"name"`
		URL      *string            `json:"url"`
		Type     *string            `json:"type"`
		Headers  *map[string]string `json:"headers"`
		Disabled *bool              `json:"disabled"`
	}
	if err := decodeSceneConfigArguments(raw, &args); err != nil {
		return nil, "", err
	}
	name := strings.TrimSpace(args.Name)
	servers, err := h.sceneConfigMCPServers(ctx, target)
	if err != nil {
		return nil, "", err
	}
	stored, existed := servers[name]
	server := map[string]any{}
	if existed {
		if err := json.Unmarshal(stored, &server); err != nil || server == nil {
			server = map[string]any{}
		}
	}
	storedURL, _ := server["url"].(string)
	if args.URL != nil {
		server["url"] = strings.TrimSpace(*args.URL)
	}
	if args.Type != nil {
		if strings.TrimSpace(*args.Type) == "" {
			delete(server, "type")
		} else {
			server["type"] = strings.TrimSpace(*args.Type)
		}
	}
	if args.Headers != nil {
		if len(*args.Headers) == 0 {
			delete(server, "headers")
		} else {
			server["headers"] = *args.Headers
		}
	}
	if args.Disabled != nil {
		if *args.Disabled {
			server["disabled"] = true
		} else {
			delete(server, "disabled")
		}
	}
	url, _ := server["url"].(string)
	if url == "" {
		return nil, "", toolRefusal("invalid_mcp_config", "url is required to add a server")
	}
	encoded, _ := json.Marshal(server)
	servers[name] = encoded
	if err := h.writeSceneConfigMCPServers(ctx, target, servers); err != nil {
		return nil, "", err
	}
	disabled, _ := server["disabled"].(bool)
	masked := maskMCPServerURL(url)
	var notice string
	switch {
	case !existed:
		notice = fmt.Sprintf("新增 MCP 服务器「%s」，地址 %s。如需撤销，可以让我停用或删除它", name, masked)
	case args.URL == nil && args.Headers == nil && args.Type == nil && args.Disabled != nil:
		notice = fmt.Sprintf("%s MCP 服务器「%s」（%s）", map[bool]string{true: "停用", false: "启用"}[disabled], name, masked)
	case url != storedURL:
		notice = fmt.Sprintf("修改 MCP 服务器「%s」的地址：%s → %s", name, maskMCPServerURL(storedURL), masked)
	default:
		notice = fmt.Sprintf("修改 MCP 服务器「%s」（%s）", name, masked)
	}
	return map[string]any{"name": name, "url": masked, "disabled": disabled}, notice, nil
}

// maskMCPServerURL hides what may carry a credential in a server URL (user
// info, query and fragment) before it is shown in a conversation.
func maskMCPServerURL(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" {
		return "[hidden]"
	}
	masked := u.Scheme + "://" + u.Host + u.EscapedPath()
	if u.RawQuery != "" || u.Fragment != "" {
		masked += "?…"
	}
	return masked
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
	stored, ok := servers[name]
	if !ok {
		return nil, "", toolRefusal("mcp_server_not_found", "this scene has no MCP server named "+name)
	}
	var server struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(stored, &server)
	delete(servers, name)
	if err := h.writeSceneConfigMCPServers(ctx, target, servers); err != nil {
		return nil, "", err
	}
	return map[string]any{"deleted": name}, fmt.Sprintf("删除 MCP 服务器「%s」（%s）", name, maskMCPServerURL(server.URL)), nil
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
	// A chat never widens what a webhook run reads; managers set it.
	args.Trigger.PayloadFields = nil
	sc, err := h.sceneRoutineScene(ctx, target.agent, target.scene.SceneID)
	if err != nil {
		return nil, "", err
	}
	var cp sceneRoutineCounterpart
	if sc.SceneKind == scene.KindDM {
		cp = sceneRoutineCounterpart{OpenDingTalkID: target.senderOpenDingTalkID}
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
	args.PayloadFields = nil
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
	// Anyone in the chat can ask for a run, so a run from the chat keeps the
	// routines' minimum interval after the previous run of any kind.
	if runs, err := h.Queries.ListAutopilotRuns(ctx, db.ListAutopilotRunsParams{AutopilotID: parseUUID(routine.AutopilotID), Limit: 1}); err != nil {
		return nil, "", err
	} else if len(runs) > 0 && runs[0].CreatedAt.Valid && time.Since(runs[0].CreatedAt.Time) < sceneRoutineMinInterval {
		return nil, "", toolRefusal("routine_run_too_soon", "this routine ran less than 15 minutes ago; run it from the configuration page if it is needed sooner")
	}
	run, err := h.runSceneRoutine(ctx, routine, sceneConfigActor(target))
	if err != nil {
		return nil, "", err
	}
	// The run posts its own start notice; no change notice.
	return map[string]any{"run": run}, "", nil
}

// ── Change notices ──────────────────────────────────────────────────────────

// sceneConfigNoticeText is a change notice: who asked (the dispatch sender)
// and what changed.
func sceneConfigNoticeText(requester, change string) string {
	by := ""
	if requester != "" {
		by = "（" + requester + " 提出）"
	}
	return "场域配置已更新" + by + "：" + change + "。可以在配置页的「QwenTag配置」里查看。"
}

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
		Text:           sceneConfigNoticeText(target.requester, change),
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
