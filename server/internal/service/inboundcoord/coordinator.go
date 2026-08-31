// Package inboundcoord is the server-side short loop that decides whether an
// inbound user turn can be answered immediately or must become an Issue that
// starts a sandbox. Direct reply is the chat response; Issue is the only
// sandbox path. Decide is one JSON LLM call with no tools. DWS is not a
// coordinator tool; DingTalk history is loaded by the server before Decide.
package inboundcoord

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	decisionTimeout      = 10 * time.Second
	coordinatorModel     = "qwen3.7-plus"
	historyLimit         = 4
	dingtalkHistoryLimit = 10
	instructionsBudget   = 400
	titleBudget          = 40
	temperature          = 0.3
	maxCompletionTokens  = 192
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
	AgentID           pgtype.UUID
	AgentName         string
	Instructions      string
	Busy              bool
	History           []HistoryLine
	DingTalkHistory   []HistoryLine
	IdentityNote      string
	RelatedTasks      string
	WorkspaceID       string
	ConversationID    string
	PersonID          string
	EvidenceID        string
	Kind              string
}

// HistoryLine is one already-persisted Multica chat message.
type HistoryLine struct {
	Role    string
	Content string
}

// Decision is what callers act on.
type Decision struct {
	Action     Action
	UserText   string
	LookInto   string
	Reason     string
	ElapsedMs  int64
	Source     Source
	ToolRounds int
	ToolsUsed  []string
}

type historyReader interface {
	ListChatMessagesPage(ctx context.Context, arg db.ListChatMessagesPageParams) ([]db.ChatMessage, error)
	GetAgent(ctx context.Context, id pgtype.UUID) (db.Agent, error)
	CountRunningTasks(ctx context.Context, agentID pgtype.UUID) (int64, error)
	GetAgentInboundCoordinator(ctx context.Context, id pgtype.UUID) (bool, error)
}

// Coordinator runs one JSON LLM decision with no tools. Tools and Chat remain
// on the struct for the leftover assoc loop in loop.go; Decide does not call them.
type Coordinator struct {
	LLM     *llm.Client
	Queries historyReader
	Tools   Tools
	Chat    Completer
}

// New wires the short loop. assocSvc may be nil; Decide does not call assoc tools.
func New(llmClient *llm.Client, queries historyReader, assocSvc *assoc.Service) *Coordinator {
	c := &Coordinator{LLM: llmClient, Queries: queries}
	if assocSvc != nil {
		c.Tools = &AssocTools{Service: assocSvc}
	}
	return c
}

// Decide returns a verdict. A disabled LLM or any failure continues the
// existing sandbox enqueue so a missing model never silences users.
func (c *Coordinator) Decide(ctx context.Context, turn Turn) Decision {
	if c == nil {
		return Decision{Action: ActionContinue}
	}
	if c.LLM == nil || !c.LLM.Enabled() {
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
	if c.coordinatorOff(ctx, turn) {
		slog.Info("inbound coordinator skipped; agent switch off",
			"event", "inbound_coordinator_decided",
			"source", string(turn.Source),
			"action", string(ActionContinue),
			"fail_open", false,
			"switch_off", true,
		)
		return Decision{Action: ActionContinue}
	}

	loopCtx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()
	started := time.Now()

	raw, err := c.LLM.GenerateJSONFast(
		loopCtx,
		coordinatorModel,
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
			"model", coordinatorModel,
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
		"model", coordinatorModel,
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

// TurnFromChatSession loads agent voice, busy state, and recent history.
// Web Chat reads the last 4 Multica rows into History. Robot and digital-employee
// turns read the last 10 rows into DingTalkHistory before Decide.
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
		AgentID:           session.AgentID,
		WorkspaceID:       util.UUIDToString(session.WorkspaceID),
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
	if turn.Source == SourceWeb {
		turn.History = c.listHistory(ctx, session.ID, historyLimit)
	} else {
		turn.DingTalkHistory = c.listHistory(ctx, session.ID, dingtalkHistoryLimit)
	}
	return turn
}

// FillDingTalkHistory loads the current DingTalk conversation's recent
// messages into the turn. Web Chat must not call this. A lookup failure
// leaves DingTalkHistory empty and Decide still runs.
func (c *Coordinator) FillDingTalkHistory(ctx context.Context, turn Turn, sessionID pgtype.UUID) Turn {
	if turn.Source == SourceWeb {
		return turn
	}
	turn.DingTalkHistory = c.listHistory(ctx, sessionID, dingtalkHistoryLimit)
	return turn
}

// AttachDingTalkConversation is the robot/digital-employee pre-Decide hook:
// prefer the bound session's last 10 rows, else the caller-supplied window.
// Web Chat is a no-op. Empty history is not an error.
func AttachDingTalkConversation(ctx context.Context, c *Coordinator, turn Turn, sessionID pgtype.UUID, window []HistoryLine) Turn {
	if turn.Source == SourceWeb {
		return turn
	}
	if c != nil && sessionID.Valid {
		turn = c.FillDingTalkHistory(ctx, turn, sessionID)
	}
	if len(turn.DingTalkHistory) > 0 {
		return turn
	}
	if len(window) == 0 {
		return turn
	}
	start := 0
	if len(window) > dingtalkHistoryLimit {
		start = len(window) - dingtalkHistoryLimit
	}
	lines := make([]HistoryLine, 0, len(window)-start)
	for _, line := range window[start:] {
		content := clipRunes(strings.TrimSpace(line.Content), 160)
		if content == "" {
			continue
		}
		role := line.Role
		if role == "" {
			role = "user"
		}
		lines = append(lines, HistoryLine{Role: role, Content: content})
	}
	turn.DingTalkHistory = lines
	return turn
}

func (c *Coordinator) listHistory(ctx context.Context, sessionID pgtype.UUID, limit int32) []HistoryLine {
	if c == nil || c.Queries == nil || !sessionID.Valid || limit <= 0 {
		return nil
	}
	page, err := c.Queries.ListChatMessagesPage(ctx, db.ListChatMessagesPageParams{
		ChatSessionID: sessionID,
		Limit:         limit,
	})
	if err != nil {
		return nil
	}
	var lines []HistoryLine
	for i := len(page) - 1; i >= 0; i-- {
		content := strings.TrimSpace(page[i].Content)
		if content == "" {
			continue
		}
		lines = append(lines, HistoryLine{
			Role:    page[i].Role,
			Content: clipRunes(content, 160),
		})
	}
	return lines
}

func (c *Coordinator) coordinatorOff(ctx context.Context, turn Turn) bool {
	if c == nil || c.Queries == nil || !turn.AgentID.Valid {
		return false
	}
	on, err := c.Queries.GetAgentInboundCoordinator(ctx, turn.AgentID)
	if err != nil {
		return false
	}
	return !on
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
	look := strings.TrimSpace(decision.LookInto)
	msg := strings.TrimSpace(message)
	if utf8.RuneCountInString(look) >= 8 {
		return clipRunes(look, titleBudget)
	}
	if utf8.RuneCountInString(msg) >= 8 {
		return clipRunes(msg, titleBudget)
	}
	if look != "" {
		return clipRunes(look, titleBudget)
	}
	if msg != "" {
		return clipRunes(msg, titleBudget)
	}
	return "跟进事项"
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
