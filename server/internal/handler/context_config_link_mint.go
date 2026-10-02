package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/forwarding"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Configuration links are minted in one place for every issuer: the
// executor's create_context_config_link and scene_connect_link tools (a
// running task) and the Coordinator's capability answer (an inbound turn,
// before any task exists). Scope rules, lifetimes and the audit line stay the
// same.

// Issuer labels in the "configuration link issued" audit line.
const (
	contextConfigLinkIssuerTaskTool    = "task_tool"
	contextConfigLinkIssuerCoordinator = "coordinator"
)

// contextConfigLinkPagePath is the configure page a link opens; the bearer
// token follows it.
const contextConfigLinkPagePath = inboundcoord.ConfigLinkPagePath

// redactContextConfigLinks removes configuration link URLs (plain or
// percent-encoded) from text that is stored beyond the delivery path: a link
// is a bearer token (it hands over its conversation's configuration), so the
// Coordinator transcript, which managers and allow-listed members can read
// and the Coordinator rereads as history, keeps only a placeholder. The
// DingTalk reply keeps the link, and so do the executor task's own messages:
// the delivery fallback and the wrap-up "already delivered" check read them
// back and must see the text that was sent.
func redactContextConfigLinks(text string) string {
	return inboundcoord.RedactConfigLinks(text)
}

// contextConfigLinkMint is one link to mint for a resolved dispatch scope.
type contextConfigLinkMint struct {
	WorkspaceID string
	AgentID     string
	Scope       contextcap.Scope
	// Origin is the app origin without a trailing slash.
	Origin string
	// SourceTaskID is the minting task; the Coordinator has none.
	SourceTaskID string
	Issuer       string
	CoordTraceID string
	// Tab is the configure page tab the link opens ("" for the default).
	Tab string
}

// contextConfigLinkTabs are the configure page tabs a link may open
// (CONTEXT_CONFIG_TABS in packages/views/dingtalk/context-config-page.tsx).
var contextConfigLinkTabs = []string{"scope", "routines"}

// contextConfigLinkTab checks a requested tab: "" or one of
// contextConfigLinkTabs. "public" (the removed 公开能力 tab) still opens the
// default tab, so a run that listed the tools before the change does not fail.
func contextConfigLinkTab(tab string) (string, error) {
	tab = strings.TrimSpace(tab)
	if tab == "public" {
		return "", nil
	}
	if tab == "" || slices.Contains(contextConfigLinkTabs, tab) {
		return tab, nil
	}
	return "", &multicaMCPToolCallError{message: "tab must be one of " + strings.Join(contextConfigLinkTabs, ", ")}
}

// contextConfigLinkOrigin returns the app origin configuration pages are
// served from.
func (h *Handler) contextConfigLinkOrigin() (string, error) {
	if base := h.currentConfig().ForwardPublicBaseURL; base != "" {
		if _, _, err := forwarding.ParsePublicBase(base); err != nil {
			return "", err
		}
		return base, nil
	}
	origin := strings.TrimRight(firstNonEmpty(h.currentConfig().AppURL, h.currentConfig().FrontendOrigin), "/")
	if origin == "" {
		return "", &multicaMCPToolCallError{message: "the app URL is not configured, so no configuration link can be issued"}
	}
	return origin, nil
}

// mintContextConfigLink stores a configuration link for the conversation
// scene of a dispatch scope and returns its page URL. A group and a 1:1 chat
// are minted the same way: the link is keyed by the scene_id the dispatch
// resolved from the conversation's openConversationId (docs/agent-scene.md
// §1, §5), never by the sender, so it needs no staffId. The scope must come
// from the server-written dispatch context, never from a model or a prompt,
// and its scene has already passed the use-time fence (taskContextScope).
func (h *Handler) mintContextConfigLink(ctx context.Context, in contextConfigLinkMint) (multicaMCPContextConfigLinkResult, error) {
	scope := in.Scope
	if !scope.HasScene() {
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "This run did not come from a DingTalk group or 1:1 chat of this agent, so there is no conversation to configure."}
	}
	summary, err := contextcap.GetScene(ctx, h.DB, in.WorkspaceID, in.AgentID, scope.OrgID, scope.SceneID)
	if errors.Is(err, contextcap.ErrNotFound) || errors.Is(err, contextcap.ErrInvalidInput) {
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "This conversation is no longer one of this agent's scenes, so there is nothing to configure."}
	}
	if err != nil {
		return multicaMCPContextConfigLinkResult{}, err
	}
	link := contextcap.Link{
		WorkspaceID:  in.WorkspaceID,
		AgentID:      in.AgentID,
		ScopeType:    contextcap.ScopeScene,
		OrgID:        scope.OrgID,
		ScopeKey:     scope.SceneID,
		ScopeTitle:   firstNonEmpty(scope.SceneTitle, agentSceneTitle(summary)),
		SourceTaskID: in.SourceTaskID,
	}

	token, err := contextcap.NewLinkToken()
	if err != nil {
		return multicaMCPContextConfigLinkResult{}, err
	}
	link.TokenHash = contextcap.HashLinkToken(token)
	stored, err := contextcap.InsertLink(ctx, h.DB, link, contextcap.LinkTTL(link.ScopeType))
	if err != nil {
		return multicaMCPContextConfigLinkResult{}, err
	}
	pageURL := in.Origin + contextConfigLinkPagePath + url.QueryEscape(token)
	if in.Tab != "" {
		// After the token, so link redaction (the path prefix + token)
		// still matches.
		pageURL += "&tab=" + url.QueryEscape(in.Tab)
	}
	slog.InfoContext(ctx, "context capabilities: configuration link issued",
		"source_task_id", link.SourceTaskID, "agent_id", in.AgentID, "workspace_id", link.WorkspaceID, "scope_type", link.ScopeType,
		"scene_id", link.ScopeKey, "scene_kind", summary.Kind, "issuer", in.Issuer, "coord_trace_id", in.CoordTraceID)
	return multicaMCPContextConfigLinkResult{
		URL:         pageURL,
		DingTalkURL: inboundcoord.ConfigLinkDeepLink(pageURL),
		Scope:       link.ScopeType,
		SceneKind:   summary.Kind,
		ExpiresAt:   stored.ExpiresAt.UTC().Format(time.RFC3339),
	}, nil
}

// coordinatorConfigLinkIssuer mints the configuration link the Coordinator
// appends to its capability answer on an inbound DingTalk turn.
type coordinatorConfigLinkIssuer struct{ h *Handler }

// NewCoordinatorConfigLinkIssuer wires the Coordinator to the same link
// minting the executor's create_context_config_link tool uses.
func NewCoordinatorConfigLinkIssuer(h *Handler) inboundcoord.ConfigLinkIssuer {
	return coordinatorConfigLinkIssuer{h: h}
}

// IssueConfigLink resolves the turn's scene exactly as for a task carrying
// the same dispatch context (taskContextScope: A2A, rerun, org and scene
// fence rules included) and mints that conversation's scene link, in a group
// and in a 1:1 chat alike.
func (i coordinatorConfigLinkIssuer) IssueConfigLink(ctx context.Context, req inboundcoord.ConfigLinkRequest) (inboundcoord.ConfigLink, error) {
	h := i.h
	if h == nil || h.Queries == nil || h.DB == nil {
		return inboundcoord.ConfigLink{}, errors.New("configuration links are not available")
	}
	if len(req.DispatchContext) == 0 {
		return inboundcoord.ConfigLink{}, errors.New("the turn has no dispatch context")
	}
	workspaceUUID, err := util.ParseUUID(strings.TrimSpace(req.WorkspaceID))
	if err != nil {
		return inboundcoord.ConfigLink{}, errors.New("workspace is invalid")
	}
	agentUUID, err := util.ParseUUID(strings.TrimSpace(req.AgentID))
	if err != nil {
		return inboundcoord.ConfigLink{}, errors.New("agent is invalid")
	}
	origin, err := h.contextConfigLinkOrigin()
	if err != nil {
		return inboundcoord.ConfigLink{}, err
	}
	// The inbound turn is the task-to-be: the same dispatch context, created
	// now, with no rerun source.
	scope := h.taskContextScope(ctx, workspaceUUID, db.AgentTaskQueue{
		AgentID:   agentUUID,
		Context:   req.DispatchContext,
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	result, err := h.mintContextConfigLink(ctx, contextConfigLinkMint{
		WorkspaceID:  uuidToString(workspaceUUID),
		AgentID:      uuidToString(agentUUID),
		Scope:        scope,
		Origin:       origin,
		Issuer:       contextConfigLinkIssuerCoordinator,
		CoordTraceID: strings.TrimSpace(req.TraceID),
	})
	if err != nil {
		var toolErr *multicaMCPToolCallError
		if errors.As(err, &toolErr) {
			return inboundcoord.ConfigLink{}, errors.New(toolErr.message)
		}
		return inboundcoord.ConfigLink{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339, result.ExpiresAt)
	if err != nil {
		return inboundcoord.ConfigLink{}, err
	}
	return inboundcoord.ConfigLink{
		URL:       result.URL,
		Scope:     result.Scope,
		SceneKind: result.SceneKind,
		ValidFor:  contextcap.LinkTTL(result.Scope),
		ExpiresAt: expiresAt,
	}, nil
}
