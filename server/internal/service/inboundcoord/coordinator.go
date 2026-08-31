// Package inboundcoord is the server-side short loop that decides whether an
// inbound user turn can be answered immediately or must become an Issue that
// starts a sandbox. Direct reply is the chat response; Issue is the only
// sandbox path. The loop does not call DWS.
package inboundcoord

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	decisionTimeout     = 8 * time.Second
	historyLimit        = 8
	instructionsBudget  = 1500
	titleBudget         = 40
	temperature         = 0.7
	maxCompletionTokens = 320
)

// Action is the short-loop verdict.
type Action string

const (
	ActionReply    Action = "reply"
	ActionIssue    Action = "issue"
	ActionContinue Action = "continue"
	ActionSilence  Action = "silence"
)

// Source names the inbound surface that asked for a decision.
type Source string

const (
	SourceWeb             Source = "web"
	SourceDigitalEmployee Source = "digital_employee"
	SourceRobot           Source = "robot"
)

// Turn is the local context the loop is allowed to see.
type Turn struct {
	Source            Source
	Addressed         bool
	ChatType          string
	ConversationTitle string
	SenderName        string
	Message           string
	AgentName         string
	Instructions      string
	Busy              bool
	History           []HistoryLine
}

// HistoryLine is one already-persisted Multica chat message.
type HistoryLine struct {
	Role    string
	Content string
}

// Decision is what callers act on.
type Decision struct {
	Action    Action
	UserText  string
	LookInto  string
	Reason    string
	ElapsedMs int64
	Source    Source
}

type historyReader interface {
	ListChatMessagesPage(ctx context.Context, arg db.ListChatMessagesPageParams) ([]db.ChatMessage, error)
	GetAgent(ctx context.Context, id pgtype.UUID) (db.Agent, error)
	CountRunningTasks(ctx context.Context, agentID pgtype.UUID) (int64, error)
}

// Coordinator runs one bounded LLM JSON decision.
type Coordinator struct {
	LLM     *llm.Client
	Queries historyReader
}

// Decide returns a verdict. A disabled LLM or any failure continues the
// existing sandbox enqueue so a missing model never silences users.
func (c *Coordinator) Decide(ctx context.Context, turn Turn) Decision {
	if c == nil || c.LLM == nil || !c.LLM.Enabled() {
		return Decision{Action: ActionContinue}
	}
	if strings.TrimSpace(turn.Message) == "" {
		if turn.Source == SourceWeb {
			return Decision{Action: ActionContinue}
		}
		return Decision{Action: ActionSilence}
	}
	if turn.Source != SourceWeb && !turn.Addressed && strings.EqualFold(turn.ChatType, "group") {
		return Decision{Action: ActionSilence}
	}

	loopCtx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()
	started := time.Now()

	raw, err := c.LLM.GenerateJSON(
		loopCtx,
		"",
		systemPrompt,
		buildUserPrompt(turn),
		temperature,
		maxCompletionTokens,
	)
	elapsed := time.Since(started)
	if err != nil {
		slog.Warn("inbound coordinator llm failed; continuing sandbox enqueue",
			"event", "inbound_coordinator_decided",
			"source", string(turn.Source),
			"action", string(ActionContinue),
			"fail_open", true,
			"elapsed_ms", elapsed.Milliseconds(),
			"error", err,
		)
		return Decision{Action: ActionContinue}
	}
	decision := parseDecision(raw, turn)
	decision.ElapsedMs = elapsed.Milliseconds()
	decision.Source = turn.Source
	slog.Info("inbound coordinator decided",
		"event", "inbound_coordinator_decided",
		"source", string(turn.Source),
		"action", string(decision.Action),
		"fail_open", decision.Action == ActionContinue,
		"look_into_runes", utf8.RuneCountInString(decision.LookInto),
		"reply_runes", utf8.RuneCountInString(decision.UserText),
		"elapsed_ms", decision.ElapsedMs,
	)
	return decision
}

// Trace is the user-visible short-loop record persisted with the Chat reply.
func (d Decision) Trace() protocol.ChatCoordinatorTrace {
	return protocol.ChatCoordinatorTrace{
		Action:    string(d.Action),
		LookInto:  d.LookInto,
		Reason:    d.Reason,
		ElapsedMs: d.ElapsedMs,
		Source:    string(d.Source),
	}
}

// TraceJSON is stored on chat_message.source_payload for coordinator rows.
func (d Decision) TraceJSON() []byte {
	raw, err := json.Marshal(d.Trace())
	if err != nil {
		return nil
	}
	return raw
}

// TurnFromChatSession loads agent voice, busy state, and recent Multica history.
func (c *Coordinator) TurnFromChatSession(
	ctx context.Context,
	session db.ChatSession,
	source Source,
	addressed bool,
	chatType string,
	conversationTitle string,
	senderName string,
	message string,
) Turn {
	turn := Turn{
		Source:            source,
		Addressed:         addressed,
		ChatType:          chatType,
		ConversationTitle: conversationTitle,
		SenderName:        senderName,
		Message:           message,
	}
	if c == nil || c.Queries == nil {
		return turn
	}
	if agent, err := c.Queries.GetAgent(ctx, session.AgentID); err == nil {
		turn.AgentName = agent.Name
		turn.Instructions = agent.Instructions
	}
	if n, err := c.Queries.CountRunningTasks(ctx, session.AgentID); err == nil && n > 0 {
		turn.Busy = true
	}
	page, err := c.Queries.ListChatMessagesPage(ctx, db.ListChatMessagesPageParams{
		ChatSessionID: session.ID,
		Limit:         historyLimit,
	})
	if err != nil {
		return turn
	}
	for i := len(page) - 1; i >= 0; i-- {
		content := strings.TrimSpace(page[i].Content)
		if content == "" {
			continue
		}
		turn.History = append(turn.History, HistoryLine{
			Role:    page[i].Role,
			Content: clipRunes(content, 400),
		})
	}
	return turn
}

func parseDecision(raw string, turn Turn) Decision {
	var parsed struct {
		Action   string `json:"action"`
		Text     string `json:"text"`
		LookInto string `json:"look_into"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &parsed); err != nil {
		return Decision{Action: ActionContinue}
	}
	action := Action(strings.TrimSpace(parsed.Action))
	text := strings.TrimSpace(parsed.Text)
	look := strings.TrimSpace(parsed.LookInto)
	reason := strings.TrimSpace(parsed.Reason)
	switch action {
	case ActionReply:
		if text == "" {
			return Decision{Action: ActionContinue}
		}
		return Decision{Action: ActionReply, UserText: text, Reason: reason}
	case ActionIssue:
		if look == "" {
			look = clipRunes(strings.TrimSpace(turn.Message), titleBudget)
		}
		if text == "" {
			text = issueAckFallback(look)
		}
		return Decision{Action: ActionIssue, UserText: text, LookInto: look, Reason: reason}
	case ActionSilence:
		if turn.Source == SourceWeb {
			return Decision{Action: ActionContinue}
		}
		return Decision{Action: ActionSilence, Reason: reason}
	default:
		return Decision{Action: ActionContinue}
	}
}

func issueAckFallback(look string) string {
	target := strings.TrimSpace(look)
	if target == "" {
		return "我先把这件事单独跟一下，核对完马上回你。"
	}
	return "我先去核对「" + target + "」，跟完马上回你。"
}

// IssueTitle is the Issue row title for a sandbox handoff.
func IssueTitle(decision Decision, message string) string {
	if strings.TrimSpace(decision.LookInto) != "" {
		return clipRunes(decision.LookInto, titleBudget)
	}
	title := clipRunes(strings.TrimSpace(message), titleBudget)
	if title == "" {
		return "跟进事项"
	}
	return title
}

// IssueDescription is the Issue body the sandbox will see.
func IssueDescription(decision Decision, message string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(message))
	if decision.UserText != "" {
		b.WriteString("\n\n前台已对用户说：")
		b.WriteString(decision.UserText)
		b.WriteString("\n请直接处理，不要重复打招呼或复述这句前台回复。")
	}
	if decision.LookInto != "" {
		b.WriteString("\n要核对：")
		b.WriteString(decision.LookInto)
	}
	return b.String()
}

func clipRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
