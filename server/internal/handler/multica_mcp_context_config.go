package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const multicaMCPContextConfigLinkTool = "create_context_config_link"

const multicaMCPContextConfigLinkToolDescription = `为当前钉钉群或当前私聊用户生成「能力配置」链接。Create a link that lets DingTalk users turn on this Agent's offered connectors and skills (连接器 / 技能 / 能力) for the current group chat or for themselves.

Call it when a user asks to configure, enable or connect connectors, skills or capabilities for this group (本群) or for themselves (我的 / 个人). Post the returned url to the user verbatim in your reply; never shorten, rewrite or paraphrase it.

scope defaults to "scene" in a group chat and to "person" in a 1:1 chat. A scene link lets any member of this group who opens it within 30 minutes configure the group's capabilities. A person link is single-use, valid for 15 minutes, and is only issued in a 1:1 chat; in a group, ask the user to message you privately (私聊) and request it there.

Requires a task token of a run dispatched from DingTalk. Scene and person identities are taken from the server-side dispatch context, never from arguments.`

type multicaMCPContextConfigLinkArguments struct {
	Scope string `json:"scope"`
}

type multicaMCPContextConfigLinkResult struct {
	URL         string `json:"url"`
	DingTalkURL string `json:"dingtalk_url"`
	Scope       string `json:"scope"`
	ExpiresAt   string `json:"expires_at"`
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
				"scope": map[string]any{
					"type":        "string",
					"enum":        []string{contextcap.ScopeScene, contextcap.ScopePerson},
					"description": `"scene" configures the current group chat; "person" configures the current 1:1 sender. Omit to use the current conversation.`,
				},
			},
		},
		"outputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":          map[string]any{"type": "string"},
				"dingtalk_url": map[string]any{"type": "string"},
				"scope":        map[string]any{"type": "string", "enum": []string{contextcap.ScopeScene, contextcap.ScopePerson}},
				"expires_at":   map[string]any{"type": "string"},
			},
			"required": []string{"url", "dingtalk_url", "scope", "expires_at"},
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
	result, err := h.createContextConfigLink(r, strings.TrimSpace(args.Scope))
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

// createContextConfigLink mints a configuration link for the scene or trigger
// person of the authenticated active task. The scope comes only from the
// task's server-written dispatch context.
func (h *Handler) createContextConfigLink(r *http.Request, requestedScope string) (multicaMCPContextConfigLinkResult, error) {
	ctx := r.Context()
	if h.Queries == nil || h.DB == nil {
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "configuration links are not available"}
	}
	if requestedScope != "" && requestedScope != contextcap.ScopeScene && requestedScope != contextcap.ScopePerson {
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: `scope must be "scene" or "person"`}
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
	origin := strings.TrimRight(firstNonEmpty(h.currentConfig().AppURL, h.currentConfig().FrontendOrigin), "/")
	if origin == "" {
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "the app URL is not configured, so no configuration link can be issued"}
	}

	scope := h.taskContextScope(ctx, workspaceUUID, task)
	// A personal link is single use but readable by everyone in the chat it
	// is posted to, so it is only issued into a conversation that is
	// positively 1:1 (the dispatcher's DM allow-list). Empty or unknown
	// conversation types are treated as shared.
	direct := contextcap.IsDirectConversationType(scope.ConversationType)
	scopeType := requestedScope
	if scopeType == "" {
		scopeType = contextcap.ScopeScene
		if direct {
			scopeType = contextcap.ScopePerson
		}
	}
	link := contextcap.Link{
		WorkspaceID:  uuidToString(workspaceUUID),
		AgentID:      agentID,
		ScopeType:    scopeType,
		OrgID:        scope.OrgID,
		SourceTaskID: uuidToString(task.ID),
	}
	switch scopeType {
	case contextcap.ScopeScene:
		if !scope.HasScene() {
			return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "This run did not come from a DingTalk group chat, so there is no group to configure. In a 1:1 chat with the user, use scope=person."}
		}
		link.ScopeKey = scope.SceneKey
		link.ScopeTitle = scope.SceneTitle
		if link.ScopeTitle == "" {
			if title, found, err := h.contextCapGroupSceneTitle(ctx, link.WorkspaceID, agentID, scope.OrgID, scope.SceneKey); err == nil && found {
				link.ScopeTitle = title
			}
		}
	case contextcap.ScopePerson:
		if !direct {
			return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "Personal configuration links are only issued in a 1:1 chat, because everyone in a shared conversation could open them. Ask the user to message you privately (私聊) and request the personal link there."}
		}
		if !scope.HasPerson() {
			return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "This run has no single identifiable DingTalk sender, so no personal configuration link can be issued. Ask the user to message you privately (私聊)."}
		}
		link.ScopeKey = scope.PersonKey
		link.ScopeTitle = scope.PersonName
	}

	token, err := contextcap.NewLinkToken()
	if err != nil {
		return multicaMCPContextConfigLinkResult{}, err
	}
	link.TokenHash = contextcap.HashLinkToken(token)
	stored, err := contextcap.InsertLink(ctx, h.DB, link, contextcap.LinkTTL(scopeType))
	if err != nil {
		return multicaMCPContextConfigLinkResult{}, err
	}
	pageURL := origin + "/dingtalk/configure?link=" + url.QueryEscape(token)
	slog.InfoContext(ctx, "context capabilities: configuration link issued",
		"source_task_id", link.SourceTaskID, "agent_id", agentID, "workspace_id", link.WorkspaceID, "scope_type", scopeType)
	return multicaMCPContextConfigLinkResult{
		URL:         pageURL,
		DingTalkURL: "dingtalk://dingtalkclient/page/link?url=" + url.QueryEscape(pageURL) + "&pc_slide=true",
		Scope:       scopeType,
		ExpiresAt:   stored.ExpiresAt.UTC().Format(time.RFC3339),
	}, nil
}
