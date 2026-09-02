// Package inboundcoord is the server-side short loop that decides whether an
// inbound user turn can be answered immediately or must become an Issue that
// starts a sandbox. Direct reply is the chat response; Issue is the only
// sandbox path. Decide runs a bounded tool loop (assoc_recall / assoc_bind /
// finish, with issue_comment_add terminal on success). DWS is not model-callable; the server reads authoritative DingTalk
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
	decisionTimeout      = 45 * time.Second
	coordinatorModel     = "qwen3.7-plus"
	historyLimit         = 4
	dingtalkHistoryLimit = 10
	instructionsBudget   = 400
	personaBudget        = 400
	toneBudget           = 200
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
	ActionRetry    Action = "retry"
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
	Source               Source
	Addressed            bool
	ChatType             string
	ConversationTitle    string
	SenderName           string
	Message              string
	AgentID              pgtype.UUID
	UserID               pgtype.UUID
	AgentName            string
	Instructions         string
	Persona              string
	ReplyTone            string
	Busy                 bool
	History              []HistoryLine
	DingTalkHistory      []HistoryLine
	IdentityNote         string
	RelatedTasks         string
	WorkspaceID          string
	ConversationID       string
	PersonID             string
	DWSUID               string
	DWSOrgID             string
	EvidenceID           string
	Kind                 string
	TraceID              string
	IssueDispatchContext []byte
}

// HistoryLine is one already-persisted Multica chat message or a DingTalk row.
type HistoryLine struct {
	Role       string
	Content    string
	EvidenceID string
}

// Decision is what callers act on.
type Decision struct {
	Action       Action
	UserText     string
	LookInto     string
	IssueID      string
	Purpose      string
	Intent       string
	Reason       string
	ElapsedMs    int64
	Source       Source
	ToolRounds   int
	ToolsUsed    []string
	IssueComment *IssueCommentEffect
	Steps        []protocol.ChatCoordinatorStep
}

type decisionObserverKey struct{}

// WithDecisionObserver attaches a request-scoped observer used by durable
// dispatch workers to persist the same verdict regardless of which ingress
// adapter invoked the Coordinator.
func WithDecisionObserver(ctx context.Context, observer func(Decision)) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, decisionObserverKey{}, observer)
}

// RecordDecision publishes one verdict to the request-scoped observer.
func RecordDecision(ctx context.Context, decision Decision) {
	observer, _ := ctx.Value(decisionObserverKey{}).(func(Decision))
	if observer != nil {
		observer(decision)
	}
}

type historyReader interface {
	ListChatMessagesPage(ctx context.Context, arg db.ListChatMessagesPageParams) ([]db.ChatMessage, error)
	GetAgent(ctx context.Context, id pgtype.UUID) (db.Agent, error)
	CountRunningTasks(ctx context.Context, agentID pgtype.UUID) (int64, error)
	GetAgentInboundCoordinator(ctx context.Context, id pgtype.UUID) (bool, error)
	GetAgentVoice(ctx context.Context, id pgtype.UUID) (db.GetAgentVoiceRow, error)
}

// Coordinator runs the bounded assoc tool loop in loop.go.
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
		tools := &AssocTools{Service: assocSvc}
		if issues, ok := queries.(IssueAccess); ok {
			tools.Issues = issues
		}
		c.Tools = tools
	}
	return c
}

// SetIssueCommentWriter wires the normal member-comment path before the
// coordinator is shared by HTTP and channel routers.
func (c *Coordinator) SetIssueCommentWriter(writer IssueCommentWriter) {
	if c == nil {
		return
	}
	if tools, ok := c.Tools.(*AssocTools); ok {
		tools.CommentWriter = writer
	}
}

// FillVoice copies Instructions-tab persona and reply tone onto the turn.
func (c *Coordinator) FillVoice(ctx context.Context, turn *Turn) {
	if c == nil || c.Queries == nil || turn == nil || !turn.AgentID.Valid {
		return
	}
	voice, err := c.Queries.GetAgentVoice(ctx, turn.AgentID)
	if err != nil {
		return
	}
	turn.Persona = voice.Persona
	turn.ReplyTone = voice.ReplyTone
}

// Decide returns a verdict. A disabled LLM or any failure continues the
// existing sandbox enqueue so a missing model never silences users.
func (c *Coordinator) Decide(ctx context.Context, turn Turn) (decision Decision) {
	defer func() {
		if decision.Source == "" {
			decision.Source = turn.Source
		}
		RecordDecision(ctx, decision)
	}()
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
	ensureTurnTraceID(&turn)
	if c.coordinatorOff(ctx, turn) {
		slog.Info("inbound coordinator skipped; agent switch off",
			append(coordinatorLogIndex(turn),
				"event", "inbound_coordinator_decided",
				"action", string(ActionContinue),
				"fail_open", false,
				"switch_off", true,
			)...)
		return Decision{Action: ActionContinue}
	}

	loopCtx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()
	started := time.Now()
	var preflightSteps []protocol.ChatCoordinatorStep
	if turn.Source != SourceWeb {
		if c.DWSHistory == nil {
			preflightSteps = coordinatorDWSHistorySteps(turn, nil, fmt.Errorf("DWS history is not configured"))
			slog.Warn("inbound coordinator DWS history unavailable; continuing sandbox enqueue",
				append(coordinatorLogIndex(turn),
					"event", "inbound_coordinator_dws_history_failed",
					"error_class", "not_configured",
				)...)
			return Decision{
				Action: ActionContinue, ElapsedMs: time.Since(started).Milliseconds(), Source: turn.Source,
				Steps: preflightSteps,
			}
		}
		history, err := c.DWSHistory.Load(loopCtx, turn)
		if err != nil {
			preflightSteps = coordinatorDWSHistorySteps(turn, nil, err)
			slog.Warn("inbound coordinator DWS history failed; continuing sandbox enqueue",
				append(coordinatorLogIndex(turn),
					"event", "inbound_coordinator_dws_history_failed",
					"error_class", "read_failed",
					"elapsed_ms", time.Since(started).Milliseconds(),
					"error", err,
				)...)
			return Decision{
				Action: ActionContinue, ElapsedMs: time.Since(started).Milliseconds(), Source: turn.Source,
				Steps: preflightSteps,
			}
		}
		turn.DingTalkHistory = history
		preflightSteps = coordinatorDWSHistorySteps(turn, history, nil)
		slog.Info("inbound coordinator DWS history loaded",
			append(coordinatorLogIndex(turn),
				"event", "inbound_coordinator_dws_history_loaded",
				"message_count", len(history),
				"elapsed_ms", time.Since(started).Milliseconds(),
			)...)
	}
	decision, err := c.runLoop(loopCtx, turn)
	elapsed := time.Since(started)
	decision.Steps = mergeCoordinatorSteps(preflightSteps, decision.Steps)
	if err != nil {
		slog.Warn("inbound coordinator llm failed; continuing sandbox enqueue",
			append(coordinatorLogIndex(turn),
				"event", "inbound_coordinator_decided",
				"action", string(ActionContinue),
				"model", coordinatorModel,
				"fail_open", true,
				"elapsed_ms", elapsed.Milliseconds(),
				"error", err,
			)...)
		decision.Action = ActionContinue
		decision.ElapsedMs = elapsed.Milliseconds()
		decision.Source = turn.Source
		return decision
	}
	decision.ElapsedMs = elapsed.Milliseconds()
	decision.Source = turn.Source
	slog.Info("inbound coordinator decided",
		append(coordinatorLogIndex(turn),
			"event", "inbound_coordinator_decided",
			"action", string(decision.Action),
			"model", coordinatorModel,
			"fail_open", decision.Action == ActionContinue,
			"tool_rounds", decision.ToolRounds,
			"tools_used", decision.ToolsUsed,
			"issue_id", strings.TrimSpace(decision.IssueID),
			"text", clipRunes(strings.TrimSpace(decision.UserText), llmLogFieldBudget),
			"look_into", clipRunes(strings.TrimSpace(decision.LookInto), llmLogFieldBudget),
			"look_into_runes", utf8.RuneCountInString(decision.LookInto),
			"reply_runes", utf8.RuneCountInString(decision.UserText),
			"elapsed_ms", decision.ElapsedMs,
		)...)
	return decision
}

func coordinatorDWSHistorySteps(turn Turn, history []HistoryLine, loadErr error) []protocol.ChatCoordinatorStep {
	input, _ := json.Marshal(map[string]any{
		"conversation_id": strings.TrimSpace(turn.ConversationID),
		"limit":           dingtalkHistoryLimit,
	})
	steps := []protocol.ChatCoordinatorStep{{
		Seq: 1, Type: "tool_use", Tool: "dws_chat_history", Input: string(input),
	}}
	if loadErr != nil {
		steps = append(steps, protocol.ChatCoordinatorStep{
			Seq: 2, Type: "tool_result", Tool: "dws_chat_history",
			Output: clipRunes(loadErr.Error(), 800), Error: true,
		})
		return steps
	}
	type visibleHistoryLine struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	visible := make([]visibleHistoryLine, 0, len(history))
	for _, line := range history {
		visible = append(visible, visibleHistoryLine{
			Role: clipRunes(strings.TrimSpace(line.Role), 80), Content: clipRunes(strings.TrimSpace(line.Content), 800),
		})
	}
	output, _ := json.Marshal(map[string]any{"message_count": len(history), "messages": visible})
	steps = append(steps, protocol.ChatCoordinatorStep{
		Seq: 2, Type: "tool_result", Tool: "dws_chat_history", Output: clipRunes(string(output), 4000),
	})
	return steps
}

func mergeCoordinatorSteps(groups ...[]protocol.ChatCoordinatorStep) []protocol.ChatCoordinatorStep {
	count := 0
	for _, group := range groups {
		count += len(group)
	}
	steps := make([]protocol.ChatCoordinatorStep, 0, count)
	for _, group := range groups {
		for _, step := range group {
			step.Seq = len(steps) + 1
			steps = append(steps, step)
		}
	}
	return steps
}

// Trace is the user-visible short-loop record persisted with the Chat reply.
func (d Decision) Trace() protocol.ChatCoordinatorTrace {
	return protocol.ChatCoordinatorTrace{
		Action:    string(d.Action),
		LookInto:  d.LookInto,
		Reason:    d.Reason,
		ElapsedMs: d.ElapsedMs,
		Source:    string(d.Source),
		Steps:     d.Steps,
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
	c.FillVoice(ctx, &turn)
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
		return true
	}
	return !on
}

func parseDecision(raw string, turn Turn) Decision {
	var parsed struct {
		Action    string `json:"action"`
		Text      string `json:"text"`
		LookInto  string `json:"look_into"`
		IssueID   string `json:"issue_id"`
		Purpose   string `json:"purpose"`
		Delegator string `json:"delegator"`
		Intent    string `json:"intent"`
		Place     string `json:"place"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &parsed); err != nil {
		return Decision{Action: ActionContinue}
	}
	action := Action(strings.TrimSpace(parsed.Action))
	text := strings.TrimSpace(parsed.Text)
	look := strings.TrimSpace(parsed.LookInto)
	issueID := strings.TrimSpace(parsed.IssueID)
	reason := strings.TrimSpace(parsed.Reason)
	switch action {
	case ActionReply:
		if text == "" {
			return Decision{Action: ActionContinue}
		}
		return Decision{Action: ActionReply, UserText: text, Reason: reason}
	case ActionIssue:
		purpose, _ := assoc.ComposeCoordinatorPurpose(firstNonEmpty(parsed.Delegator, turn.SenderName), parsed.Place, parsed.Purpose)
		intent, _ := assoc.CoordinatorIntent(parsed.Intent)
		if look == "" {
			look = purpose
		}
		if look == "" {
			look = clipRunes(strings.TrimSpace(turn.Message), titleBudget)
		}
		if text == "" {
			text = issueAckFallback(look)
		}
		return Decision{Action: ActionIssue, UserText: text, LookInto: look, IssueID: issueID, Purpose: purpose, Intent: intent, Reason: reason}
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
	if purpose := strings.TrimSpace(decision.Purpose); utf8.RuneCountInString(purpose) >= 8 {
		return clipRunes(purpose, titleBudget)
	}
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
	b.WriteString("\n\n身份与闭环要求：本次委托人是当前可信钉钉派发事件里的发信人；Multica 的 Issue 创建人或评论人只表示谁执行了 Issue 工具，是协助者，不等同于委托人、当前钉钉发信人或消息接收人。数字员工事件的会话和用户身份完整；机器人事件的用户标识可能缺失，此时只能使用事件里已有的发信人名称、会话和原文，不能虚构身份或改用 Issue 署名。如涉及代问或转达，先从当前钉钉消息和关联会话中明确委托人、Agent 转达人、消息接收人和下一位应答人。联系接收人时要说明是谁委托、具体问什么；拿到答复后要注明是谁说了什么，再回给需要结果的人。遇到阻塞时，回复当前能解除阻塞、且正在处理其问题的人，不要固定回复委托人。每次新建 Issue 或 Issue 评论触发的任务，在结束前必须实际给一个明确的人发送进度、阻塞或结果；只在 Issue 中留言不算送达，钉钉发送未成功时不得写“任务完成”。")
	return b.String()
}

func clipRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
