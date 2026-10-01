package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
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
// line: a group chat gets the scene link, a 1:1 chat the personal link (which
// also grants that 1:1 scene). The model never writes or sees the link, so the
// review judges only the model's words, like the Host-owned work receipt.
//
// The issuer derives scene and person only from the server-written dispatch
// context and reuses the executor tool's minting (TTL, single use, audit).
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
	URL       string
	Scope     string // "scene" or "person"
	ValidFor  time.Duration
	SingleUse bool
	ExpiresAt time.Time
}

// ConfigLinkIssuer mints the context configuration link for the conversation
// of an inbound DingTalk turn. It returns an error whenever the turn has no
// trusted scene (group) or person (1:1) to configure.
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
	line := ""
	if err == nil {
		line, err = configLinkLine(workReceiptLanguage(turn, decision.CoordinationActions[target]), link)
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
		decision.configLinkURL = link.URL
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
	if link.Scope != "scene" && link.Scope != "person" {
		return ConfigLink{}, fmt.Errorf("configuration link has unknown scope %q", link.Scope)
	}
	if link.ValidFor <= 0 {
		return ConfigLink{}, errors.New("configuration link has no lifetime")
	}
	return link, nil
}

// configLinkLine is the fixed Host line that ends the capability answer. The
// URL comes last so the answer ends with the link itself.
func configLinkLine(language string, link ConfigLink) (string, error) {
	if link.Scope == "person" && !link.SingleUse {
		return "", errors.New("personal configuration link must be single use")
	}
	minutes := int(math.Round(link.ValidFor.Minutes()))
	if minutes < 1 {
		minutes = 1
	}
	var format string
	switch language {
	case "en":
		format = "Configure this group's capabilities (valid for %d min): %s"
		if link.Scope == "person" {
			format = "Configure your personal capabilities (valid for %d min, single use): %s"
		}
	case "ja":
		format = "このグループの機能設定（%d 分間有効）：%s"
		if link.Scope == "person" {
			format = "あなた個人の機能設定（%d 分間有効、1 回限り）：%s"
		}
	case "ko":
		format = "이 그룹의 기능 설정 (%d분간 유효): %s"
		if link.Scope == "person" {
			format = "개인 기능 설정 (%d분간 유효, 1회용): %s"
		}
	default:
		format = "本群能力配置（%d 分钟内有效）：%s"
		if link.Scope == "person" {
			format = "你的个人能力配置（%d 分钟内有效，限用一次）：%s"
		}
	}
	return fmt.Sprintf(format, minutes, link.URL), nil
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
