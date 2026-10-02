package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const multicaMCPContextConfigLinkTool = "create_context_config_link"

const multicaMCPContextConfigLinkToolDescription = `为当前钉钉会话（群聊或单聊）生成「场域能力配置」链接。Create a link that lets DingTalk users configure this Agent's capabilities (连接器 / 技能 / 能力 / 提示词 / 例行任务) for the current conversation: this group chat (本群) or this 1:1 chat (本单聊).

Call it when a user asks to configure, enable or connect connectors, skills or capabilities here, or asks for the configuration link (场域配置链接). Reply with a Markdown link whose target is the returned dingtalk_url, e.g. [配置本群能力](dingtalk_url) in a group or [配置本单聊能力](dingtalk_url) in a 1:1 chat (scene_kind "group" or "dm"); never show the bare url, and never shorten or rewrite the link target.

The link is bound to the current conversation's scene, the same way in a group and in a 1:1 chat: whoever opens it within 30 minutes can configure that conversation's capabilities.

Requires a task token of a run dispatched from DingTalk. The conversation is taken from the server-side dispatch context, never from arguments.`

type multicaMCPContextConfigLinkArguments struct {
	Tab string `json:"tab"`
}

type multicaMCPContextConfigLinkResult struct {
	URL         string `json:"url"`
	DingTalkURL string `json:"dingtalk_url"`
	Scope       string `json:"scope"`
	// SceneKind is the conversation's kind in the scene directory: "group"
	// or "dm" (a 1:1 chat).
	SceneKind string `json:"scene_kind"`
	ExpiresAt string `json:"expires_at"`
}

// multicaMCPContextConfigLinkVisible lists the tool only for task tokens while
// context_capabilities is on; personal access tokens are refused.
func (h *Handler) multicaMCPContextConfigLinkVisible(r *http.Request) bool {
	return multicaMCPTaskTokenAuthenticated(r)
}

func multicaMCPContextConfigLinkDefinition() map[string]any {
	return map[string]any{
		"name":        multicaMCPContextConfigLinkTool,
		"title":       "Create a capability configuration link",
		"description": multicaMCPContextConfigLinkToolDescription,
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"tab": map[string]any{
					"type":        "string",
					"enum":        contextConfigLinkTabs,
					"description": `Page tab to open: "scope" (场域能力, default) or "routines" (例行任务).`,
				},
			},
		},
		"outputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":          map[string]any{"type": "string"},
				"dingtalk_url": map[string]any{"type": "string"},
				"scope":        map[string]any{"type": "string", "enum": []string{contextcap.ScopeScene}},
				"scene_kind":   map[string]any{"type": "string", "enum": []string{contextcap.SceneKindGroup, contextcap.SceneKindDM}},
				"expires_at":   map[string]any{"type": "string"},
			},
			"required": []string{"url", "dingtalk_url", "scope", "scene_kind", "expires_at"},
		},
		"annotations": map[string]any{
			"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false,
		},
	}
}

func (h *Handler) handleMulticaMCPContextConfigLink(w http.ResponseWriter, r *http.Request, id json.RawMessage, rawArguments json.RawMessage) {
	if !multicaMCPTaskTokenAuthenticated(r) {
		h.writeMulticaMCPToolError(w, id, "create_context_config_link requires a task token")
		return
	}
	var args multicaMCPContextConfigLinkArguments
	if err := decodeMulticaMCPArguments(rawArguments, &args); err != nil {
		h.writeMulticaMCPError(w, id, -32602, "invalid create_context_config_link arguments")
		return
	}
	result, err := h.createContextConfigLink(r, args.Tab)
	if err != nil {
		var toolErr *multicaMCPToolCallError
		if !errors.As(err, &toolErr) {
			slog.ErrorContext(r.Context(), "context capabilities: configuration link mint failed",
				"source_task_id", r.Header.Get("X-Task-ID"), "error", err)
			toolErr = &multicaMCPToolCallError{message: "failed to create the configuration link"}
		}
		h.writeMulticaMCPToolError(w, id, toolErr.message)
		return
	}
	payload, _ := json.Marshal(result)
	h.writeMulticaMCPResult(w, id, multicaMCPToolResult{
		Content:           []multicaMCPContent{{Type: "text", Text: string(payload)}},
		StructuredContent: result,
	})
}

// createContextConfigLink mints a configuration link for the conversation
// scene (group or 1:1 chat) of the authenticated active task. The scene comes
// only from the task's server-written dispatch context.
func (h *Handler) createContextConfigLink(r *http.Request, tab string) (multicaMCPContextConfigLinkResult, error) {
	ctx := r.Context()
	if h.Queries == nil || h.DB == nil {
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "configuration links are not available"}
	}
	tab, err := contextConfigLinkTab(tab)
	if err != nil {
		return multicaMCPContextConfigLinkResult{}, err
	}
	workspaceUUID, err := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-Workspace-ID")))
	if err != nil {
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "authenticated workspace is invalid"}
	}
	taskUUID, err := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-Task-ID")))
	if err != nil {
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "authenticated source task is invalid"}
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: taskUUID, WorkspaceID: workspaceUUID})
	if errors.Is(err, pgx.ErrNoRows) {
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "authenticated source task was not found"}
	}
	if err != nil {
		return multicaMCPContextConfigLinkResult{}, err
	}
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	if agentID == "" || agentID != uuidToString(task.AgentID) {
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "authenticated Agent does not own the source task"}
	}
	if task.Status != "running" && task.Status != "dispatched" {
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "source task is not active"}
	}
	if service.IsSceneRoutineContext(task.Context) {
		// A routine run acts on cron or webhook input with nobody asking in
		// the chat, so it never hands out access to its scene.
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "a routine run cannot issue configuration links; ask in the chat instead"}
	}
	origin, err := h.contextConfigLinkOrigin()
	if err != nil {
		return multicaMCPContextConfigLinkResult{}, err
	}
	return h.mintContextConfigLink(ctx, contextConfigLinkMint{
		WorkspaceID:  uuidToString(workspaceUUID),
		AgentID:      agentID,
		Scope:        h.taskContextScope(ctx, workspaceUUID, task),
		Tab:          tab,
		Origin:       origin,
		SourceTaskID: uuidToString(task.ID),
		Issuer:       contextConfigLinkIssuerTaskTool,
	})
}
