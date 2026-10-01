package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Configuration links are minted in one place for both issuers: the
// executor's create_context_config_link tool (a running task) and the
// Coordinator's capability answer (an inbound turn, before any task exists).
// Scope rules, lifetimes, single use and the audit line stay the same.

// Issuer labels in the "configuration link issued" audit line.
const (
	contextConfigLinkIssuerTaskTool    = "task_tool"
	contextConfigLinkIssuerCoordinator = "coordinator"
)

// contextConfigLinkPagePath is the configure page a link opens; the bearer
// token follows it.
const contextConfigLinkPagePath = "/dingtalk/configure?link="

// contextConfigLinkPattern matches a configuration link URL (the page path
// and its URL-safe base64 token, contextcap.NewLinkToken).
var contextConfigLinkPattern = regexp.MustCompile(`(?:https?://\S*?)?` + regexp.QuoteMeta(contextConfigLinkPagePath) + `[A-Za-z0-9_%-]+`)

// contextConfigLinkRedacted replaces a configuration link in stored text.
const contextConfigLinkRedacted = "[configuration link]"

// redactContextConfigLinks removes configuration link URLs from text that is
// stored beyond the delivery path: a link is a bearer token (a personal one
// hands over that person's scope), so the Coordinator transcript, which
// managers and allow-listed members can read and the Coordinator rereads as
// history, keeps only a placeholder. The DingTalk reply keeps the link.
func redactContextConfigLinks(text string) string {
	if !strings.Contains(text, contextConfigLinkPagePath) {
		return text
	}
	return contextConfigLinkPattern.ReplaceAllString(text, contextConfigLinkRedacted)
}

// contextConfigLinkMint is one link to mint for a resolved dispatch scope.
type contextConfigLinkMint struct {
	WorkspaceID string
	AgentID     string
	Scope       contextcap.Scope
	// RequestedScope is "", scene or person; "" picks scene in a group and
	// person in a positively 1:1 chat.
	RequestedScope string
	// Origin is the app origin without a trailing slash.
	Origin string
	// SourceTaskID is the minting task; the Coordinator has none.
	SourceTaskID string
	Issuer       string
	CoordTraceID string
}

// contextConfigLinkOrigin returns the app origin configuration pages are
// served from.
func (h *Handler) contextConfigLinkOrigin() (string, error) {
	origin := strings.TrimRight(firstNonEmpty(h.currentConfig().AppURL, h.currentConfig().FrontendOrigin), "/")
	if origin == "" {
		return "", &multicaMCPToolCallError{message: "the app URL is not configured, so no configuration link can be issued"}
	}
	return origin, nil
}

// mintContextConfigLink stores a configuration link for the scene or trigger
// person of a dispatch scope and returns its page URL. The scope must come
// from the server-written dispatch context, never from a model or a prompt.
func (h *Handler) mintContextConfigLink(ctx context.Context, in contextConfigLinkMint) (multicaMCPContextConfigLinkResult, error) {
	scope := in.Scope
	// A personal link is single use but readable by everyone in the chat it
	// is posted to, so it is only issued into a conversation that is
	// positively 1:1 (the dispatcher's DM allow-list). Empty or unknown
	// conversation types are treated as shared.
	direct := contextcap.IsDirectConversationType(scope.ConversationType)
	scopeType := in.RequestedScope
	if scopeType == "" {
		scopeType = contextcap.ScopeScene
		if direct {
			scopeType = contextcap.ScopePerson
		}
	}
	link := contextcap.Link{
		WorkspaceID:  in.WorkspaceID,
		AgentID:      in.AgentID,
		ScopeType:    scopeType,
		OrgID:        scope.OrgID,
		SourceTaskID: in.SourceTaskID,
	}
	switch scopeType {
	case contextcap.ScopeScene:
		if !scope.HasScene() {
			return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: "This run did not come from a DingTalk group chat, so there is no group to configure. In a 1:1 chat with the user, use scope=person."}
		}
		link.ScopeKey = scope.SceneKey
		link.ScopeTitle = scope.SceneTitle
		if link.ScopeTitle == "" {
			if title, found, err := h.contextCapGroupSceneTitle(ctx, link.WorkspaceID, in.AgentID, scope.OrgID, scope.SceneKey); err == nil && found {
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
		// A 1:1 chat is a scene too: redeeming this link also grants the
		// person that DM scene (configuration only this round).
		link.ExtraSceneKey = scope.DirectSceneKey
	default:
		return multicaMCPContextConfigLinkResult{}, &multicaMCPToolCallError{message: `scope must be "scene" or "person"`}
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
	pageURL := in.Origin + contextConfigLinkPagePath + url.QueryEscape(token)
	slog.InfoContext(ctx, "context capabilities: configuration link issued",
		"source_task_id", link.SourceTaskID, "agent_id", in.AgentID, "workspace_id", link.WorkspaceID, "scope_type", scopeType,
		"issuer", in.Issuer, "coord_trace_id", in.CoordTraceID)
	return multicaMCPContextConfigLinkResult{
		URL:         pageURL,
		DingTalkURL: "dingtalk://dingtalkclient/page/link?url=" + url.QueryEscape(pageURL) + "&pc_slide=true",
		Scope:       scopeType,
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

// IssueConfigLink resolves the turn's scene/person exactly as for a task
// carrying the same dispatch context (taskContextScope: A2A, rerun and org
// rules included) and mints the default link for that conversation: the
// scene link in a group, the personal link in a 1:1 chat.
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
		ValidFor:  contextcap.LinkTTL(result.Scope),
		SingleUse: result.Scope == contextcap.ScopePerson,
		ExpiresAt: expiresAt,
	}, nil
}
