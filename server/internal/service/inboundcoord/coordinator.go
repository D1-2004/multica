// Package inboundcoord is the server-side short loop that decides whether an
// inbound user turn can be answered immediately or must become an Issue that
// starts a sandbox. Direct reply is the chat response; Issue is the only
// sandbox path. Decide runs a bounded tool loop (assoc_recall / assoc_bind /
// finish). DWS is not model-callable; the server reads authoritative DingTalk
// history through an isolated DWS identity before the first model round.
package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
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
	decisionTimeout      = 15 * time.Second
	coordinatorModel     = "qwen3.7-plus"
	historyLimit         = 4
	dingtalkHistoryLimit = 10
	instructionsBudget   = 400
	titleBudget          = 40
	temperature          = 0.3
	maxCompletionTokens  = 512
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
	DWSUID            string
	DWSOrgID          string
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

// Coordinator runs the bounded assoc tool loop in loop.go. Assoc also seeds
// related_tasks for the inbound scene before the first model round.
type Coordinator struct {
	LLM        *llm.Client
	Queries    historyReader
	Tools      Tools
	Chat       Completer
	Assoc      *assoc.Service
	DWSHistory DingTalkHistoryLoader
}

// New wires the loop. assocSvc may be nil; Decide still fail-opens.
func New(llmClient *llm.Client, queries historyReader, assocSvc *assoc.Service) *Coordinator {
	c := &Coordinator{LLM: llmClient, Queries: queries, Assoc: assocSvc}
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
	if turn.Source != SourceWeb {
		if c.DWSHistory == nil {
			slog.Warn("inbound coordinator DWS history unavailable; continuing sandbox enqueue",
				"event", "inbound_coordinator_dws_history_failed",
				"source", string(turn.Source),
				"error_class", "not_configured",
			)
			return Decision{Action: ActionContinue}
		}
		history, err := c.DWSHistory.Load(loopCtx, turn)
		if err != nil {
			slog.Warn("inbound coordinator DWS history failed; continuing sandbox enqueue",
				"event", "inbound_coordinator_dws_history_failed",
				"source", string(turn.Source),
				"error_class", "read_failed",
				"elapsed_ms", time.Since(started).Milliseconds(),
				"error", err,
			)
			return Decision{Action: ActionContinue}
		}
		turn.DingTalkHistory = history
		slog.Info("inbound coordinator DWS history loaded",
			"event", "inbound_coordinator_dws_history_loaded",
			"source", string(turn.Source),
			"message_count", len(history),
			"elapsed_ms", time.Since(started).Milliseconds(),
		)
	}
	turn = c.injectRelatedTasks(loopCtx, turn)

	decision, err := c.runLoop(loopCtx, turn)
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
		return Decision{Action: ActionContinue, ElapsedMs: elapsed.Milliseconds(), Source: turn.Source}
	}
	decision.ElapsedMs = elapsed.Milliseconds()
	decision.Source = turn.Source
	slog.Info("inbound coordinator decided",
		"event", "inbound_coordinator_decided",
		"source", string(turn.Source),
		"action", string(decision.Action),
		"model", coordinatorModel,
		"fail_open", decision.Action == ActionContinue,
		"tool_rounds", decision.ToolRounds,
		"tools_used", decision.ToolsUsed,
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

func (c *Coordinator) injectRelatedTasks(ctx context.Context, turn Turn) Turn {
	if strings.TrimSpace(turn.RelatedTasks) != "" {
		return turn
	}
	if c == nil || c.Assoc == nil || strings.TrimSpace(turn.WorkspaceID) == "" || !turn.AgentID.Valid {
		return turn
	}
	cid := strings.TrimSpace(turn.ConversationID)
	if cid == "" {
		return turn
	}
	now := time.Now().UTC()
	since, err := assoc.ParseSince("48h", now)
	if err != nil {
		return turn
	}
	result, err := c.Assoc.Recall(ctx, assoc.Query{
		WorkspaceID:    turn.WorkspaceID,
		AgentID:        util.UUIDToString(turn.AgentID),
		ConversationID: cid,
		Since:          since,
		Until:          now,
		Limit:          5,
	})
	if err != nil {
		slog.Warn("inbound coordinator related_tasks recall failed",
			"event", "inbound_coordinator_related_tasks_failed",
			"conversation_id", cid,
			"error", err,
		)
		return turn
	}
	if len(result.Items) == 0 {
		return turn
	}
	var b strings.Builder
	for _, item := range result.Items {
		purpose := clipRunes(strings.TrimSpace(item.Purpose), 80)
		if purpose == "" {
			continue
		}
		fmt.Fprintf(&b, "- issue=%s purpose=%s status=%s\n", item.Issue, purpose, item.Status)
	}
	turn.RelatedTasks = strings.TrimSpace(b.String())
	return turn
}

// TurnFromChatSession loads agent voice and busy state. Web Chat also reads the
// last 4 Multica rows. DingTalk turns load real conversation history in Decide.
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
	title := conversationTitle
	if source != SourceWeb {
		title = ""
	}
	turn := Turn{
		Source:            source,
		Addressed:         addressed,
		ChatType:          chatType,
		ConversationTitle: title,
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
	}
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
