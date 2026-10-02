package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// A capability answer ends with the conversation's configuration link
// (COORD.F04). describe_capabilities stays the model's own answer; after the
// plan passed review and before it is checkpointed, Host runs the
// context_config_link effect once and appends the link as the answer's last
// line: the conversation's scene link, minted the same way for a group chat
// and a 1:1 chat (keyed by the scene_id of its openConversationId, never by
// the sender). The model never writes or sees the link, so the review judges
// only the model's words, like the Host-owned work receipt.
//
// The issuer derives the scene only from the server-written dispatch context
// and reuses the executor tool's minting (TTL, audit).
// Every failure is closed: no issuer, no trusted scope, an error, a timeout or
// a malformed result leaves the reviewed answer exactly as it was.

const (
	toolContextConfigLink = "context_config_link"
	configLinkTimeout     = 2 * time.Second
	configLinkOrigin      = "host_effect"
)

// ConfigLinkRequest identifies the inbound turn a configuration link is for.
// DispatchContext is the server-written dispatch envelope of the turn (the
// same bytes an executor task would carry), never model output.
type ConfigLinkRequest struct {
	WorkspaceID     string
	AgentID         string
	DispatchContext []byte
	TraceID         string
}

// ConfigLink is one minted configuration link. URL carries a bearer token:
// it is delivered in the reply and never logged or traced.
type ConfigLink struct {
	URL   string
	Scope string // "scene"
	// SceneKind is the conversation's kind in the scene directory: "group"
	// or "dm" (a 1:1 chat). It only picks the link's label.
	SceneKind string
	ValidFor  time.Duration
	ExpiresAt time.Time
}

// ConfigLinkIssuer mints the context configuration link for the conversation
// of an inbound DingTalk turn. It returns an error whenever the turn has no
// trusted conversation scene (a group or a 1:1 chat) to configure.
type ConfigLinkIssuer interface {
	IssueConfigLink(ctx context.Context, req ConfigLinkRequest) (ConfigLink, error)
}

// configLinkEligible reports whether this turn's capability answer may carry a
// configuration link. Only inbound DingTalk turns of a Coordinator with an
// issuer qualify; web chat, task_finished and A2UI choice cards never do.
func (c *Coordinator) configLinkEligible(turn Turn) bool {
	return c != nil && c.ConfigLinks != nil &&
		(turn.Loop == "" || turn.Loop == LoopInbound) &&
		(turn.Source == SourceDigitalEmployee || turn.Source == SourceRobot) &&
		!turn.UserDecisionEnabled &&
		len(turn.IssueDispatchContext) > 0 &&
		turn.AgentID.Valid && strings.TrimSpace(turn.WorkspaceID) != ""
}

// attachConfigLink appends the configuration link to the last
// describe_capabilities reply of a reviewed plan and returns the Host steps it
// ran. A plan without describe_capabilities is untouched and mints nothing.
func (c *Coordinator) attachConfigLink(ctx context.Context, turn Turn, decision *Decision) []protocol.ChatCoordinatorStep {
	if decision == nil || !turn.configLinkOffered || c == nil || c.ConfigLinks == nil {
		return nil
	}
	target := -1
	for i, action := range decision.CoordinationActions {
		if action.Kind == "describe_capabilities" {
			target = i
		}
	}
	if target < 0 {
		return nil
	}
	input := `{"action":"describe_capabilities","origin":"` + configLinkOrigin + `"}`
	started := time.Now()
	lt := langfuse.TraceFromContext(ctx)
	var observation *langfuse.Observation
	if lt != nil {
		observation = lt.StartObservation(langfuse.ObservationOptions{Type: langfuse.TypeTool, Name: toolContextConfigLink,
			Input: toolPayload(input), Metadata: map[string]any{"origin": configLinkOrigin, "timeout_ms": configLinkTimeout.Milliseconds()}})
	}
	linkCtx, cancel := context.WithTimeout(ctx, configLinkTimeout)
	link, err := issueConfigLink(linkCtx, c.ConfigLinks, ConfigLinkRequest{
		WorkspaceID:     strings.TrimSpace(turn.WorkspaceID),
		AgentID:         util.UUIDToString(turn.AgentID),
		DispatchContext: turn.IssueDispatchContext,
		TraceID:         strings.TrimSpace(turn.TraceID),
	})
	cancel()
	line, deepLink := "", ""
	if err == nil {
		line, deepLink = configLinkLine(workReceiptLanguage(turn, decision.CoordinationActions[target]), link)
	}
	elapsed := time.Since(started).Milliseconds()
	status := "issued"
	summary := map[string]any{"status": status}
	if err != nil {
		status = "unavailable"
		summary = map[string]any{"status": status, "error": clipRunes(err.Error(), 300)}
	} else {
		summary["scope"] = link.Scope
		summary["expires_at"] = link.ExpiresAt.UTC().Format(time.RFC3339)
		actions := append([]CoordinationAction(nil), decision.CoordinationActions...)
		actions[target].Reply = strings.TrimSpace(actions[target].Reply) + "\n\n" + line
		decision.CoordinationActions = actions
		decision.UserText = ComposeDecisionReplies(actions)
		// The reply carries the link only inside the DingTalk deep link, so
		// that exact string is what logs and traces replace.
		decision.configLinkURL = deepLink
	}
	output, _ := json.Marshal(summary)
	if lt != nil {
		lt.AddMetadata(map[string]any{"config_link_status": status, "config_link_scope": link.Scope, "config_link_elapsed_ms": elapsed})
	}
	traceToolEnd(observation, string(output), err, configLinkOrigin)
	logArgs := append(coordinatorLogIndex(turn), "event", "inbound_coordinator_config_link", "origin", configLinkOrigin,
		"status", status, "scope", link.Scope, "elapsed_ms", elapsed)
	if err != nil {
		logArgs = append(logArgs, "error", clipRunes(err.Error(), 300))
	}
	slog.Info("inbound coordinator config link", logArgs...)
	return []protocol.ChatCoordinatorStep{
		{Type: "tool_use", Tool: toolContextConfigLink, Input: input, Content: "Host effect after review"},
		{Type: "tool_result", Tool: toolContextConfigLink, Output: string(output), Error: err != nil, Content: "Host effect after review"},
	}
}

// issueConfigLink calls the issuer and validates what it returns. A panic in
// the issuer is an error too: a capability answer never fails because of it.
func issueConfigLink(ctx context.Context, issuer ConfigLinkIssuer, req ConfigLinkRequest) (link ConfigLink, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			link, err = ConfigLink{}, fmt.Errorf("configuration link issuer panicked: %v", recovered)
		}
	}()
	link, err = issuer.IssueConfigLink(ctx, req)
	if err != nil {
		return ConfigLink{}, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ConfigLink{}, ctxErr
	}
	link.URL = strings.TrimSpace(link.URL)
	if (!strings.HasPrefix(link.URL, "https://") && !strings.HasPrefix(link.URL, "http://")) || strings.ContainsAny(link.URL, " \t\r\n") {
		return ConfigLink{}, errors.New("configuration link has no usable url")
	}
	if link.Scope != "scene" {
		return ConfigLink{}, fmt.Errorf("configuration link has unknown scope %q", link.Scope)
	}
	if link.ValidFor <= 0 {
		return ConfigLink{}, errors.New("configuration link has no lifetime")
	}
	return link, nil
}

// ConfigLinkDeepLink wraps a configuration page URL in the DingTalk client
// link that opens it inside DingTalk: the side panel on desktop
// (pc_slide=true), the in-app browser on mobile.
func ConfigLinkDeepLink(pageURL string) string {
	return "dingtalk://dingtalkclient/page/link?url=" + url.QueryEscape(pageURL) + "&pc_slide=true"
}

// configLinkLine is the fixed Host line that ends the capability answer: a
// Markdown link to the DingTalk deep link (the reply is sent as Markdown, so
// people see the label, never the bearer URL), then its lifetime. The label
// names the conversation the link configures: this group or this 1:1 chat.
// It returns the line and the deep link it carries.
func configLinkLine(language string, link ConfigLink) (string, string) {
	minutes := int(math.Round(link.ValidFor.Minutes()))
	if minutes < 1 {
		minutes = 1
	}
	direct := link.SceneKind == "dm"
	var format string
	switch language {
	case "en":
		format = "[Configure this group's capabilities](%s) (valid for %d min)"
		if direct {
			format = "[Configure this chat's capabilities](%s) (valid for %d min)"
		}
	case "ja":
		format = "[このグループの機能設定](%s)（%d 分間有効）"
		if direct {
			format = "[この個別チャットの機能設定](%s)（%d 分間有効）"
		}
	case "ko":
		format = "[이 그룹의 기능 설정](%s) (%d분간 유효)"
		if direct {
			format = "[이 1:1 채팅의 기능 설정](%s) (%d분간 유효)"
		}
	default:
		format = "[本群能力配置](%s)（%d 分钟内有效）"
		if direct {
			format = "[本单聊能力配置](%s)（%d 分钟内有效）"
		}
	}
	deepLink := ConfigLinkDeepLink(link.URL)
	return fmt.Sprintf(format, deepLink, minutes), deepLink
}

// redactConfigLink removes the bearer URL of an attached configuration link
// from text bound for logs and traces. The reply itself keeps it.
func redactConfigLink(text, url string) string {
	if url == "" || !strings.Contains(text, url) {
		return text
	}
	return strings.ReplaceAll(text, url, "[configuration link]")
}

func redactConfigLinkActions(actions []CoordinationAction, url string) []CoordinationAction {
	if url == "" {
		return actions
	}
	var out []CoordinationAction
	for i := range actions {
		if redacted := redactConfigLink(actions[i].Reply, url); redacted != actions[i].Reply {
			if out == nil {
				out = append([]CoordinationAction(nil), actions...)
			}
			out[i].Reply = redacted
		}
	}
	if out == nil {
		return actions
	}
	return out
}
