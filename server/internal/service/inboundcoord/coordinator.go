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
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service/scenememory"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	decisionTimeout         = 45 * time.Second
	dwsHistoryTimeout       = 12 * time.Second
	coordinatorModel        = "qwen3.7-plus"
	historyLimit            = 4
	dingtalkHistoryLimit    = 10
	instructionsBudget      = 400
	personaBudget           = 400
	toneBudget              = 200
	skillSnapshotLimit      = 24
	skillSnapshotNameBudget = 48
	skillSnapshotDescBudget = 80
	skillSnapshotsBudget    = 1200
	titleBudget             = 80
	temperature             = 0.3
	maxCompletionTokens     = 1536
)

// Action is the short-loop verdict.
type Action string

const (
	ActionReply    Action = "reply"
	ActionIssue    Action = "issue"
	ActionContinue Action = "continue"
	ActionSilence  Action = "silence"
	ActionRetry    Action = "retry"
	ActionDeferred Action = "deferred"
)

// Source names the inbound surface that asked for a decision.
type Source string

const (
	SourceWeb             Source = "web"
	SourceDigitalEmployee Source = "digital_employee"
	SourceRobot           Source = "robot"
)

// Loop names which coordinator cycle is running.
type Loop string

const (
	LoopInbound      Loop = "inbound"
	LoopTaskFinished Loop = "task_finished"
)

// Turn is the local context the loop is allowed to see.
type Turn struct {
	Loop                 Loop
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
	Skills               []SkillSnapshot
	Busy                 bool
	HistoryBefore        time.Time
	MessageTimestamp     time.Time
	HistoryStatus        string
	HistoryError         string
	SkillsStatus         string
	SceneMemoryStatus    string
	TaskDeliveryContext  string
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
	SceneMemory          string
	SceneMemoryRevision  int64
	// SceneTitle is the conversation title Scene Memory recorded for this
	// scene (the DM peer's name or the group title). Channel turns often
	// carry only a sender id, so it is the human-readable conversation name
	// for logs and traces when ConversationTitle is empty.
	SceneTitle string
	// ChatSessionID is the web Chat session the turn belongs to; channel
	// turns leave it empty and are grouped by ConversationID instead.
	ChatSessionID string
	TaskResult    string
	IssueID       string
	Utterances    []WindowUtterance
	// AlreadyToldScene is set by Host on task_finished when this sandbox
	// run already sent IM on the inbound conversation. Decide silences.
	AlreadyToldScene bool
}

// HistoryLine is one already-persisted Multica chat message or a DingTalk row.
type HistoryLine struct {
	Role              string
	Content           string
	EvidenceID        string
	Timestamp         time.Time
	TimestampRaw      string
	SenderID          string
	ReplyToEvidenceID string
	ReplyToSenderID   string
	ContentTruncated  bool
}

// Decision is what callers act on.
type Decision struct {
	Action              Action
	UserText            string
	LookInto            string
	IssueID             string
	Purpose             string
	Intent              string
	Reason              string
	ElapsedMs           int64
	Source              Source
	ToolRounds          int
	ToolsUsed           []string
	IssueComment        *IssueCommentEffect
	Items               []WindowItem
	Steps               []protocol.ChatCoordinatorStep
	CompletedActionKeys []string
	PlanVersion         string
	NonWorkRefs         []string
	IssueResults        []protocol.ChatCoordinatorIssueResult
	// TraceID is the coordinator trace id of the Decide call that produced
	// this verdict (coord_trace_id in SLS, the Langfuse trace id).
	TraceID string
	// TraceTags are the Langfuse trace tags of that turn. A task that joins
	// the turn's trace repeats them so the trace keeps one consistent tag set.
	TraceTags []string
}

type decisionObserverKey struct{}

type turnTraceIDKey struct{}

// ContextWithTraceID pins the coordinator trace id of the next Decide call
// that receives a turn without one. Durable dispatch workers pass their job
// id so the coord_trace_id in SLS and Langfuse, the job, and the Scene
// Memory trigger it records all share one identifier.
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return ctx
	}
	return context.WithValue(ctx, turnTraceIDKey{}, traceID)
}

func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	traceID, _ := ctx.Value(turnTraceIDKey{}).(string)
	return strings.TrimSpace(traceID)
}

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
	GetAgentSceneMemoryFlags(ctx context.Context, id pgtype.UUID) (db.AgentSceneMemoryFlags, error)
	ListEnabledAgentSkillCardMetadata(ctx context.Context, agentID pgtype.UUID) ([]db.ListEnabledAgentSkillCardMetadataRow, error)
}

// SkillSnapshot is the Coordinator-facing catalog row for one enabled skill.
// Name and description only; never SKILL.md.
type SkillSnapshot struct {
	Name        string
	Description string
}

// Coordinator runs the bounded assoc tool loop in loop.go.
type Coordinator struct {
	Ready       func(context.Context) (bool, error)
	LLM         *llm.Client
	Queries     historyReader
	Tools       Tools
	Chat        Completer
	Assoc       *assoc.Service
	DWSHistory  DingTalkHistoryLoader
	SceneMemory sceneMemoryReader
	// Langfuse exports one trace per Decide call. Nil disables tracing.
	Langfuse *langfuse.Client
}

type sceneMemoryReader interface {
	Get(ctx context.Context, id scenememory.Identity) (db.SceneMemory, error)
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

// FillVoice copies Digital-Employee-tab persona, reply tone, and enabled
// skill snapshots onto the turn. Skill load failures leave Skills empty
// rather than blocking Decide.
func (c *Coordinator) FillVoice(ctx context.Context, turn *Turn) {
	if c == nil || c.Queries == nil || turn == nil || !turn.AgentID.Valid {
		return
	}
	voice, err := c.Queries.GetAgentVoice(ctx, turn.AgentID)
	if err == nil {
		turn.Persona = voice.Persona
		turn.ReplyTone = voice.ReplyTone
	}
	c.FillSkills(ctx, turn)
}

// FillSkills copies enabled skill name+description snapshots onto the turn.
func (c *Coordinator) FillSkills(ctx context.Context, turn *Turn) {
	if c == nil || c.Queries == nil || turn == nil || !turn.AgentID.Valid {
		return
	}
	rows, err := c.Queries.ListEnabledAgentSkillCardMetadata(ctx, turn.AgentID)
	if err != nil {
		turn.SkillsStatus = "unavailable"
		return
	}
	turn.Skills = skillSnapshotsFromRows(rows)
	turn.SkillsStatus = "loaded"
	if len(turn.Skills) == 0 {
		turn.SkillsStatus = "empty"
	}
}

func skillSnapshotsFromRows(rows []db.ListEnabledAgentSkillCardMetadataRow) []SkillSnapshot {
	if len(rows) == 0 {
		return nil
	}
	out := make([]SkillSnapshot, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Name)
		if name == "" {
			continue
		}
		out = append(out, SkillSnapshot{
			Name:        name,
			Description: strings.TrimSpace(row.Description),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func hostSilence(turn Turn, reason string) Decision {
	ensureTurnTraceID(&turn)
	slog.Info("inbound coordinator decided",
		append(coordinatorLogIndex(turn),
			"event", "inbound_coordinator_decided",
			"action", string(ActionSilence),
			"reason", reason,
			"window_items", 0,
		)...)
	return Decision{Action: ActionSilence, Reason: reason}
}

// Decide returns a verdict. A disabled LLM or any failure continues the
// existing sandbox enqueue so a missing model never silences users.
func (c *Coordinator) Decide(ctx context.Context, turn Turn) (decision Decision) {
	if strings.TrimSpace(turn.TraceID) == "" {
		turn.TraceID = TraceIDFromContext(ctx)
	}
	var turnTrace *langfuse.Trace
	var loopErr error
	defer func() {
		if decision.Source == "" {
			decision.Source = turn.Source
		}
		decision.TraceID = strings.TrimSpace(turn.TraceID)
		decision.TraceTags = coordinatorTraceTags(turn)
		finishCoordinatorTrace(turnTrace, decision, loopErr)
		RecordDecision(ctx, decision)
	}()
	if c == nil {
		return Decision{Action: ActionContinue}
	}
	if c.Ready != nil {
		ready, err := c.Ready(ctx)
		if err != nil || !ready {
			return Decision{Action: ActionDeferred, Reason: "coordinator_rollout_wait"}
		}
	}
	if restored, ok := RestoredPlan(ctx); ok {
		return restored
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
	if turn.Loop != LoopTaskFinished && turn.Source != SourceWeb && AllWindowAck(turn) {
		return hostSilence(turn, "window_ack")
	}
	if turn.Loop == LoopTaskFinished && turn.AlreadyToldScene {
		return hostSilence(turn, "already_told_scene")
	}
	if c.Chat == nil && (c.LLM == nil || !c.LLM.Enabled()) {
		return Decision{Action: ActionDeferred, Reason: "coordinator_model_unavailable"}
	}
	ensureTurnTraceID(&turn)
	c.prefetchSceneMemory(ctx, &turn)
	if turn.Loop != LoopTaskFinished && c.coordinatorOff(ctx, turn) {
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
	turnTrace = c.startTurnTrace(ctx, turn, started)
	loopCtx = langfuse.ContextWithTrace(loopCtx, turnTrace)
	if turn.HistoryBefore.IsZero() {
		turn.HistoryBefore = turn.MessageTimestamp
		if turn.HistoryBefore.IsZero() {
			turn.HistoryBefore = time.Now().UTC()
		}
	}
	if turn.Source == SourceWeb && !turn.HistoryBefore.IsZero() {
		kept := make([]HistoryLine, 0, len(turn.History))
		for _, line := range turn.History {
			if line.Timestamp.IsZero() || line.Timestamp.Before(turn.HistoryBefore) {
				kept = append(kept, line)
			}
		}
		turn.History = kept
	}
	if turn.HistoryStatus == "" {
		turn.HistoryStatus = "not_loaded"
		if len(turn.DingTalkHistory) > 0 || len(turn.History) > 0 {
			turn.HistoryStatus = "loaded"
		}
	}
	decision, err := c.runLoop(loopCtx, turn)
	loopErr = err
	elapsed := time.Since(started)
	if turn.Loop == LoopTaskFinished {
		decision = FilterTaskFinishedWrapup(decision)
	}
	if err != nil {
		failOpen := ActionDeferred
		slog.Warn("inbound coordinator llm failed",
			append(coordinatorLogIndex(turn),
				"event", "inbound_coordinator_decided",
				"action", string(failOpen),
				"model", coordinatorModel,
				"fail_open", false,
				"elapsed_ms", elapsed.Milliseconds(),
				"error", err,
			)...)
		decision.Action = failOpen
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
			"window_items", len(decision.Items),
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
	results := d.IssueResults
	if len(results) == 0 && d.IssueComment != nil {
		effect := d.IssueComment
		results = []protocol.ChatCoordinatorIssueResult{{
			Action: "issue_commented", IssueID: effect.IssueID,
			IssueIdentifier: effect.IssueIdentifier,
			CommentID:       effect.CommentID, TaskID: effect.TaskID,
		}}
	}
	return protocol.ChatCoordinatorTrace{
		Action:       string(d.Action),
		LookInto:     d.LookInto,
		Reason:       d.Reason,
		ElapsedMs:    d.ElapsedMs,
		Source:       string(d.Source),
		Steps:        d.Steps,
		IssueResults: results,
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
		ChatSessionID:     util.UUIDToString(session.ID),
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
			Role:             page[i].Role,
			Content:          clipRunes(content, 160),
			EvidenceID:       util.UUIDToString(page[i].ID),
			Timestamp:        page[i].CreatedAt.Time,
			ContentTruncated: utf8.RuneCountInString(content) > 160,
		})
	}
	return lines
}

func (c *Coordinator) prefetchSceneMemory(ctx context.Context, turn *Turn) {
	if c == nil || turn == nil || c.SceneMemory == nil || c.Queries == nil {
		return
	}
	if turn.Source != SourceDigitalEmployee {
		return
	}
	if strings.TrimSpace(turn.ConversationID) == "" || !turn.AgentID.Valid {
		return
	}
	turn.SceneMemoryStatus = "not_loaded"
	flags, err := c.Queries.GetAgentSceneMemoryFlags(ctx, turn.AgentID)
	if err != nil || !flags.RecallEnabled {
		return
	}
	workspaceID, err := util.ParseUUID(turn.WorkspaceID)
	if err != nil {
		return
	}
	kind := scenememory.KindFromChatType(turn.ChatType)
	row, err := c.SceneMemory.Get(ctx, scenememory.Identity{
		WorkspaceID: workspaceID,
		AgentID:     turn.AgentID,
		OrgID:       turn.DWSOrgID,
		SceneKey:    turn.ConversationID,
		SceneKind:   kind,
	})
	if err != nil {
		return
	}
	turn.SceneMemoryStatus = "loaded"
	if strings.TrimSpace(row.MemoryText) == "" {
		turn.SceneMemoryStatus = "empty"
	}
	turn.SceneMemory = scenememory.SanitizeMemoryTextForAgent(row.MemoryText, turn.AgentName)
	turn.SceneMemoryRevision = row.MemoryRevision
	turn.SceneTitle = strings.TrimSpace(row.SceneTitle)
	slog.Info("scene memory injected into coordinator",
		append(coordinatorLogIndex(*turn),
			"event", "scene_memory_recall_injected",
			"scene_key", row.SceneKey,
			"scene_memory_revision", row.MemoryRevision,
			"code_points", utf8.RuneCountInString(row.MemoryText),
		)...)
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
		Purpose   string `json:"purpose"`
		Delegator string `json:"delegator"`
		Intent    string `json:"intent"`
		Place     string `json:"place"`
		Reason    string `json:"reason"`
		Items     []struct {
			Delegator string `json:"delegator"`
			Purpose   string `json:"purpose"`
			Intent    string `json:"intent"`
			LookInto  string `json:"look_into"`
			Place     string `json:"place"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &parsed); err != nil {
		return Decision{Action: ActionContinue}
	}
	action := Action(strings.TrimSpace(parsed.Action))
	text := strings.TrimSpace(parsed.Text)
	look := strings.TrimSpace(parsed.LookInto)
	reason := strings.TrimSpace(parsed.Reason)
	items := parseWindowItems(turn, parsed.Items)
	if len(items) == 0 && action == ActionIssue {
		purpose := strings.TrimSpace(parsed.Purpose)
		if purpose == "" || utf8.RuneCountInString(purpose) < 8 {
			purpose = firstNonEmpty(purpose, look, strings.TrimSpace(turn.Message), text)
		}
		intent := strings.TrimSpace(parsed.Intent)
		if intent == "" {
			intent = "other"
		}
		item, ok := newWindowItem(turn, firstNonEmpty(parsed.Delegator, turn.SenderName), parsed.Place, purpose, intent, look)
		if !ok {
			item, ok = newWindowItem(turn, turn.SenderName, parsed.Place, purpose, intent, look)
		}
		if ok {
			items = []WindowItem{item}
		}
	}
	if len(items) > SceneWindowMaxItems {
		items = items[:SceneWindowMaxItems]
	}
	switch action {
	case ActionReply:
		if len(items) > 0 {
			action = ActionIssue
			break
		}
		if text == "" {
			return Decision{Action: ActionContinue}
		}
		reply := Decision{Action: ActionReply, UserText: text, Reason: reason}
		if turn.Loop == LoopTaskFinished {
			return FilterTaskFinishedWrapup(reply)
		}
		return reply
	case ActionIssue:
		break
	case ActionSilence:
		if len(items) > 0 {
			action = ActionIssue
			break
		}
		if turn.Source == SourceWeb {
			return Decision{Action: ActionContinue}
		}
		return Decision{Action: ActionSilence, Reason: reason}
	default:
		if len(items) == 0 {
			return Decision{Action: ActionContinue}
		}
		action = ActionIssue
	}
	if action != ActionIssue {
		return Decision{Action: ActionContinue}
	}
	if text == "" {
		return Decision{Action: ActionContinue}
	}
	if isMissingPayloadIssue(text) {
		return Decision{Action: ActionReply, UserText: text, Reason: firstNonEmpty(reason, "missing send payload")}
	}
	if len(items) > 0 {
		primary := items[0]
		if look == "" {
			look = primary.LookInto
		}
		return Decision{
			Action:   ActionIssue,
			UserText: text,
			LookInto: look,
			Purpose:  primary.Purpose,
			Intent:   primary.Intent,
			Reason:   reason,
			Items:    items,
		}
	}
	delegator := firstNonEmpty(parsed.Delegator, turn.SenderName)
	if delegator != "" && !validWindowDelegator(turn, delegator) {
		return Decision{Action: ActionContinue, UserText: text, Reason: firstNonEmpty(reason, "invalid window item")}
	}
	purpose, _ := assoc.ComposeCoordinatorPurpose(delegator, parsed.Place, parsed.Purpose)
	intent, _ := assoc.CoordinatorIntent(parsed.Intent)
	if look == "" {
		look = purpose
	}
	if look == "" {
		look = clipRunes(strings.TrimSpace(turn.Message), titleBudget)
	}
	return Decision{Action: ActionIssue, UserText: text, LookInto: look, Purpose: purpose, Intent: intent, Reason: reason}
}

func parseWindowItems(turn Turn, raw []struct {
	Delegator string `json:"delegator"`
	Purpose   string `json:"purpose"`
	Intent    string `json:"intent"`
	LookInto  string `json:"look_into"`
	Place     string `json:"place"`
}) []WindowItem {
	out := make([]WindowItem, 0, SceneWindowMaxItems)
	for _, row := range raw {
		item, ok := newWindowItem(turn, row.Delegator, row.Place, row.Purpose, row.Intent, row.LookInto)
		if !ok {
			continue
		}
		out = append(out, item)
		if len(out) == SceneWindowMaxItems {
			break
		}
	}
	return out
}

func newWindowItem(turn Turn, delegator, place, purpose, intent, lookInto string) (WindowItem, bool) {
	delegator = strings.TrimSpace(delegator)
	if delegator == "" {
		delegator = strings.TrimSpace(turn.SenderName)
	}
	if !validWindowDelegator(turn, delegator) {
		return WindowItem{}, false
	}
	composed, err := assoc.ComposeCoordinatorPurpose(delegator, place, purpose)
	if err != nil {
		return WindowItem{}, false
	}
	gotIntent, ok := assoc.CoordinatorIntent(intent)
	if !ok {
		return WindowItem{}, false
	}
	look := strings.TrimSpace(lookInto)
	if look == "" {
		look = composed
	}
	if cid := strings.TrimSpace(turn.ConversationID); cid != "" {
		look = strings.TrimSpace(look) + "\nscene_cid=" + cid
	}
	return WindowItem{Delegator: delegator, Purpose: composed, Intent: gotIntent, LookInto: look}, true
}

// isMissingPayloadIssue detects an issue ack that is actually asking the user
// what to send. Opening a sandbox for that wastes a Run and the Issue body
// tells the daemon to "process directly".
func isMissingPayloadIssue(text string) bool {
	for _, needle := range []string{"要说什么", "请问要说", "发什么内容", "说什么？", "发什么？"} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// IssueTitle is the Issue row title for a sandbox handoff. It is what humans
// and later models read, so it keeps the deliverable and drops ticket jargon.
func IssueTitle(decision Decision, message string) string {
	candidates := []string{
		DisplayMatterTitle(decision.Purpose),
		DisplayMatterTitle(decision.LookInto),
		DisplayMatterTitle(message),
	}
	for _, title := range candidates {
		if utf8.RuneCountInString(title) >= 8 {
			return clipRunes(title, titleBudget)
		}
	}
	for _, title := range candidates {
		if title != "" {
			return clipRunes(title, titleBudget)
		}
	}
	return "跟进事项"
}

// DisplayMatterTitle is the human-facing form of a purpose or inbound line:
// the deliverable, without 委托人委托 prefixes or <@id> mention tokens.
func DisplayMatterTitle(raw string) string {
	s := stripMentionTokens(strings.TrimSpace(raw))
	s = stripDelegatorPrefix(s)
	return strings.Join(strings.Fields(s), " ")
}

func stripDelegatorPrefix(s string) string {
	for _, sep := range []string{"委托：", "委托:"} {
		idx := strings.Index(s, sep)
		if idx <= 0 || idx > 16 {
			continue
		}
		rest := strings.TrimSpace(s[idx+len(sep):])
		if rest != "" {
			return rest
		}
	}
	return s
}

func stripMentionTokens(s string) string {
	for {
		start := strings.Index(s, "<@")
		if start < 0 {
			return s
		}
		rel := strings.Index(s[start:], ">")
		if rel < 0 {
			return s
		}
		s = strings.TrimSpace(s[:start] + " " + s[start+rel+1:])
	}
}

// IssueDescription is the Issue body the sandbox will see.
func IssueDescription(decision Decision, message string) string {
	var b strings.Builder
	if decision.PlanVersion == WindowPlanVersion {
		b.WriteString("本次子任务只执行这一个交付物：")
		b.WriteString(decision.Purpose)
		b.WriteString("\n下方原始发言用于溯源与理解；其中不属于本交付物的其它工作由各自任务处理，不要重复执行。\n\n")
	}
	b.WriteString(strings.TrimSpace(message))
	if decision.UserText != "" {
		b.WriteString("\n\n本轮拟向用户说明：")
		b.WriteString(decision.UserText)
		b.WriteString("\n这是接待文案，不是完成或送达证据。请直接处理当前交付物，不重复打招呼或复述接待。")
	}
	if decision.LookInto != "" {
		b.WriteString("\n要核对：")
		b.WriteString(decision.LookInto)
	}
	if purpose := strings.TrimSpace(decision.Purpose); purpose != "" {
		b.WriteString("\n事项简报：")
		b.WriteString(purpose)
		if intent := strings.TrimSpace(decision.Intent); intent != "" {
			b.WriteString("\n意图：")
			b.WriteString(intent)
		}
	}
	if len(decision.Items) > 0 {
		b.WriteString("\n窗口事项：")
		for i, item := range decision.Items {
			b.WriteString("\n")
			b.WriteString(fmt.Sprintf("%d. 委托人=%s；%s", i+1, item.Delegator, item.LookInto))
		}
	}
	b.WriteString("\n\n身份与闭环要求：本次委托人是当前可信钉钉派发事件里的发信人；Multica 的 Issue 创建人或评论人只表示谁执行了 Issue 工具，是协助者，不等同于委托人、当前钉钉发信人或消息接收人。数字员工事件的会话和用户身份完整；机器人事件的用户标识可能缺失，此时只能使用事件里已有的发信人名称、会话和原文，不能虚构身份或改用 Issue 署名。如涉及代问或转达，先从当前钉钉消息和关联会话中明确委托人、Agent 转达人、消息接收人和下一位应答人。联系接收人时要说明是谁委托、具体问什么；拿到答复后要注明是谁说了什么，再回给需要结果的人。遇到阻塞时，回复当前能解除阻塞、且正在处理其问题的人，不要固定回复委托人。每次新建 Issue 或 Issue 评论触发的任务，在结束前必须实际给一个明确的人发送进度、阻塞或结果；只在 Issue 中留言不算送达，钉钉发送未成功时不得写“任务完成”。检索听记、文档、消息时只使用本轮 scene_cid / conversation_id，禁止打开或引用其它群的内容。")
	return b.String()
}

func clipRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
