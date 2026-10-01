// Package inboundcoord is the server-side short loop that decides whether an
// inbound user turn can be answered immediately or must become an Issue that
// starts a sandbox. Direct reply is the chat response; Issue is the only
// sandbox path. Decide runs a bounded read-and-plan loop with progressive
// context disclosure. Host commits validated finish.items afterwards. History
// reads use an isolated DingTalk identity and the original window watermark.
package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/internal/service/userdecision"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/modelregistry"
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
	ActionReply     Action = "reply"
	ActionAwaitUser Action = "await_user"
	ActionIssue     Action = "issue"
	ActionContinue  Action = "continue"
	ActionSilence   Action = "silence"
	ActionRetry     Action = "retry"
	ActionDeferred  Action = "deferred"
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
	LoopInbound           Loop = "inbound"
	LoopTaskFinished      Loop = "task_finished"
	LoopFinishCheck       Loop = "finish_check"
	LoopConversationReply Loop = "conversation_reply"
)

// Turn is the local context the loop is allowed to see.
type Turn struct {
	UserDecisionEnabled    bool
	UserDecisionRequestID  string
	UserDecisionSubmission *userdecision.Submission

	model                      string
	ProactiveConversation      bool
	OutstandingFollowUps       string
	Loop                       Loop
	FinishCheckAction          Action
	FinishCheckMixedActions    bool
	Source                     Source
	Addressed                  bool
	ChatType                   string
	ConversationTitle          string
	SenderName                 string
	Message                    string
	AgentID                    pgtype.UUID
	UserID                     pgtype.UUID
	AgentName                  string
	EmployeeAccountName        string
	Instructions               string
	InstructionsUnavailable    bool
	CoordinatorContract        *coordinatorcontract.Contract
	CoordinatorContractState   string
	CoordinatorContractHash    string
	Persona                    string
	ReplyTone                  string
	Skills                     []SkillSnapshot
	Busy                       bool
	HistoryBefore              time.Time
	MessageTimestamp           time.Time
	HistoryStatus              string
	HistoryError               string
	SkillsStatus               string
	SceneMemoryStatus          string
	TaskDeliveryContext        string
	History                    []HistoryLine
	DingTalkHistory            []HistoryLine
	CoordinationReads          []CoordinationRead
	CoordinationReadsTruncated bool
	IdentityNote               string
	RelatedTasks               string
	WorkspaceID                string
	ConversationID             string
	PersonID                   string
	DWSUID                     string
	DWSOrgID                   string
	EvidenceID                 string
	Kind                       string
	TraceID                    string
	IssueDispatchContext       []byte
	SceneMemory                string
	SceneMemoryRevision        int64
	// SceneTitle is the conversation title Scene Memory recorded for this
	// scene (the DM peer's name or the group title). Channel turns often
	// carry only a sender id, so it is the human-readable conversation name
	// for logs and traces when ConversationTitle is empty.
	SceneTitle string
	// ChatSessionID is the web Chat session the turn belongs to; channel
	// turns leave it empty and are grouped by ConversationID instead.
	ChatSessionID string
	// recalledIssueIDs are the Issue ids this run has actually recalled; the
	// loop refreshes them each round so tool schemas can list them.
	recalledIssueIDs []string
	TaskResult       string
	IssueID          string
	Utterances       []WindowUtterance
	// AlreadyToldScene is set by Host on task_finished when this sandbox
	// run already sent IM on the inbound conversation. Decide silences.
	AlreadyToldScene bool
	// configLinkOffered is set by runLoop when a capability answer of this
	// turn will end with the Host-minted configuration link (config_link.go).
	configLinkOffered bool
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
	UserDecision *UserDecisionSnapshot `json:"user_decision,omitempty"`

	CoordinationActions []CoordinationAction `json:"coordination_actions,omitempty"`
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
	// configLinkURL is the bearer URL Host appended to the capability answer;
	// logs and traces replace it. It is not serialized: the reply text in the
	// checkpoint is what carries the link.
	configLinkURL string
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
	GetAgentCoordinatorContract(ctx context.Context, id pgtype.UUID) ([]byte, error)
	GetAgentSceneMemoryFlags(ctx context.Context, id pgtype.UUID) (db.AgentSceneMemoryFlags, error)
	GetAgentDingTalkIdentity(ctx context.Context, arg db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error)
	ListEnabledAgentSkillCardMetadata(ctx context.Context, params db.ListEnabledAgentSkillCardMetadataParams) ([]db.ListEnabledAgentSkillCardMetadataRow, error)
}

// SkillSnapshot is the Coordinator-facing catalog row for one enabled skill.
// Name and description only; never SKILL.md.
type SkillSnapshot struct {
	Name        string
	Description string
}

// Coordinator runs the bounded assoc tool loop in loop.go.
type Coordinator struct {
	RouteProvider func(context.Context) (*modelregistry.Route, error)
	// ModelProvider is sampled once per decision, including all finish reviews.
	ModelProvider func() string
	model         string
	Ready         func(context.Context) (bool, error)
	LLM           *llm.Client
	Queries       historyReader
	Tools         Tools
	Chat          Completer
	Assoc         *assoc.Service
	DWSHistory    DingTalkHistoryLoader
	SceneMemory   sceneMemoryReader
	// ConfigLinks mints the context configuration link that ends a capability
	// answer on inbound DingTalk turns. Nil leaves capability answers as the
	// model wrote them.
	ConfigLinks ConfigLinkIssuer
	// Langfuse exports one trace per Decide call. Nil disables tracing.
	Langfuse *langfuse.Client
}

type sceneMemoryReader interface {
	Get(ctx context.Context, id scenememory.Identity) (db.SceneMemory, error)
}

// New wires the loop. assocSvc may be nil; missing required evidence defers
// enabled Coordinator work without creating an unverified sandbox request.
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
// skill snapshots onto the turn. Skill load failures are marked unavailable
// rather than being treated as an empty installed catalog.
func (c *Coordinator) FillVoice(ctx context.Context, turn *Turn) {
	c.fillCoordinatorContract(ctx, turn)
	if c == nil || c.Queries == nil || turn == nil || !turn.AgentID.Valid {
		return
	}
	voice, err := c.Queries.GetAgentVoice(ctx, turn.AgentID)
	if err == nil {
		turn.Persona = voice.Persona
		turn.ReplyTone = voice.ReplyTone
	}
	c.fillReceivingIdentity(ctx, turn)
	c.FillSkills(ctx, turn)
}

// FillSkills copies enabled skill name+description snapshots onto the turn.
func (c *Coordinator) FillSkills(ctx context.Context, turn *Turn) {
	if c == nil || c.Queries == nil || turn == nil || !turn.AgentID.Valid {
		return
	}
	rows, err := c.Queries.ListEnabledAgentSkillCardMetadata(ctx, db.ListEnabledAgentSkillCardMetadataParams{AgentID: turn.AgentID, IncludeFrontmatter: true})
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
			Description: coordinatorSkillDescription(row.Description, row.ContentHead),
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

// Decide returns a verdict or an explicit deferred state. Model/evidence
// failures do not expand authorization by bypassing the validated plan.
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
	snapshot := *c
	snapshot.model = c.configuredModel()
	snapshot.ModelProvider = nil
	if c.RouteProvider != nil {
		route, err := c.RouteProvider(ctx)
		if err != nil {
			return Decision{Action: ActionDeferred, Reason: "model_configuration_unavailable"}
		}
		snapshot.Chat = route
		snapshot.model = route.Model()
		snapshot.RouteProvider = nil
	}
	c = &snapshot
	turn.model = c.model
	if c.Ready != nil {
		ready, err := c.Ready(ctx)
		if err != nil || !ready {
			return Decision{Action: ActionDeferred, Reason: "coordinator_rollout_wait"}
		}
	}
	if restored, ok := RestoredPlan(ctx); ok {
		NormalizeWorkReceipts(turn, &restored)
		return restored
	}
	if strings.TrimSpace(turn.Message) == "" {
		if turn.Source == SourceWeb {
			return Decision{Action: ActionContinue}
		}
		return Decision{Action: ActionSilence}
	}
	if turn.Source != SourceWeb && !turn.Addressed && !turn.ProactiveConversation && strings.EqualFold(turn.ChatType, "group") {
		return Decision{Action: ActionSilence}
	}
	// The owner switch skips the whole short loop, including Host ACK
	// silence. Auto-mode sandbox still has to see the original IM.
	if turn.Loop != LoopTaskFinished && !turn.ProactiveConversation && c.coordinatorOff(ctx, turn) {
		slog.Info("inbound coordinator skipped; agent switch off",
			append(coordinatorLogIndex(turn),
				"event", "inbound_coordinator_decided",
				"action", string(ActionContinue),
				"fail_open", false,
				"switch_off", true,
			)...)
		return Decision{Action: ActionContinue}
	}
	if turn.Loop == LoopTaskFinished && turn.AlreadyToldScene {
		return hostSilence(turn, "already_told_scene")
	}
	if c.Chat == nil && (c.LLM == nil || !c.LLM.Enabled()) {
		return Decision{Action: ActionDeferred, Reason: "coordinator_model_unavailable"}
	}
	ensureTurnTraceID(&turn)
	c.prefetchSceneMemory(ctx, &turn)

	timeout := decisionTimeout
	if turn.UserDecisionEnabled {
		timeout = 90 * time.Second
	}
	loopCtx, cancel := context.WithTimeout(ctx, timeout)
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
		// A deterministic stop would fail identically on every redelivery,
		// so it ends here with a fixed reply or silence instead of six job
		// retries that leave the person with nothing.
		fallback, handled := loopStopFallback(turn, decision)
		if turn.UserDecisionEnabled {
			handled = false
		}
		if handled {
			// Checkpoint the verdict like any window plan: a redelivered job
			// restores it instead of reasoning again and possibly proposing
			// work after the fallback reply went out.
			if saveErr := SavePlan(ctx, fallback); saveErr != nil {
				handled = false
				err = fmt.Errorf("%w; fallback checkpoint: %v", err, saveErr)
			}
		}
		if handled {
			decision = fallback
		} else {
			decision.Action = ActionDeferred
		}
		if turnTrace != nil && handled {
			turnTrace.AddMetadata(map[string]any{"loop_stop_fallback": string(decision.Action)})
		}
		slog.Warn("inbound coordinator llm failed",
			append(coordinatorLogIndex(turn),
				"event", "inbound_coordinator_decided",
				"action", string(decision.Action),
				"model", c.configuredModel(),
				"fail_open", false,
				"loop_stop_reason", decision.Reason,
				"loop_stop_fallback", handled,
				"tool_rounds", decision.ToolRounds,
				"elapsed_ms", elapsed.Milliseconds(),
				"error", err,
			)...)
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
			"model", c.configuredModel(),
			"fail_open", decision.Action == ActionContinue,
			"tool_rounds", decision.ToolRounds,
			"tools_used", decision.ToolsUsed,
			"issue_id", strings.TrimSpace(decision.IssueID),
			"text", clipRunes(strings.TrimSpace(redactConfigLink(decision.UserText, decision.configLinkURL)), llmLogFieldBudget),
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
// last 4 Multica rows. DingTalk history is read on demand inside the loop.
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
	} else {
		turn.InstructionsUnavailable = true
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
	names := []string{turn.AgentName}
	if ident, identErr := c.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
		WorkspaceID: workspaceID,
		AgentID:     turn.AgentID,
	}); identErr == nil {
		names = append(names, ident.AccountDisplayName)
	}
	turn.SceneMemory = scenememory.SanitizeMemoryTextForAgent(row.MemoryText, names...)
	turn.SceneMemoryStatus = "loaded"
	if strings.TrimSpace(turn.SceneMemory) == "" {
		turn.SceneMemoryStatus = "empty"
	}
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

func newWindowItem(turn Turn, delegator, place, purpose, intent, lookInto string) (WindowItem, bool) {
	item, err := composeWindowItem(turn, delegator, place, purpose, intent, lookInto)
	return item, err == nil
}

func composeWindowItem(turn Turn, delegator, place, purpose, intent, lookInto string) (WindowItem, error) {
	delegator = strings.TrimSpace(delegator)
	if delegator == "" {
		delegator = strings.TrimSpace(turn.SenderName)
	}
	if !validWindowDelegator(turn, delegator) {
		return WindowItem{}, hintErr("delegator must copy the utterance sender", "Host binds the speaker from source_refs; do not invent a different delegator.")
	}
	composed, err := assoc.ComposeCoordinatorPurpose(delegator, place, purpose)
	if err != nil {
		return WindowItem{}, hintWrap("invalid deliverable purpose", hintPurposeRepair, err)
	}
	gotIntent, ok := assoc.CoordinatorIntent(intent)
	if !ok {
		return WindowItem{}, hintErr("invalid work intent", hintIntent)
	}
	look := strings.TrimSpace(lookInto)
	if look == "" {
		look = composed
	}
	if cid := strings.TrimSpace(turn.ConversationID); cid != "" {
		look = strings.TrimSpace(look) + "\nscene_cid=" + cid
	}
	return WindowItem{Delegator: delegator, Purpose: composed, Intent: gotIntent, LookInto: look}, nil
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
	purpose := strings.TrimSpace(decision.Purpose)
	lookInto := strings.TrimSpace(decision.LookInto)
	deliverable := firstNonEmpty(purpose, lookInto)
	if decision.PlanVersion == WindowPlanVersion {
		b.WriteString("本次子任务只执行这一个交付物：")
		b.WriteString(deliverable)
		b.WriteString("\n下方原始发言用于溯源与理解；其中不属于本交付物的其它工作由各自任务处理，不要重复执行。\n\n")
	}
	b.WriteString(strings.TrimSpace(message))
	if decision.UserText != "" {
		b.WriteString("\n\n本轮拟向用户说明：")
		b.WriteString(decision.UserText)
		b.WriteString("\n这是接待文案，不是完成或送达证据；接待由 Host 负责发给委托人。请直接处理当前交付物，不重复打招呼或复述接待，也不再向委托人发送确认或进度消息；只在有结果或失败时回报。")
	}
	if purpose != "" && decision.PlanVersion != WindowPlanVersion {
		b.WriteString("\n事项简报：")
		b.WriteString(purpose)
	}
	// composeWindowItem uses purpose as its default context, optionally followed
	// by scene_cid. Keep that scope and any independent context without repeating
	// the goal; never deduplicate or summarize the original message/handoff.
	context := lookInto
	if purpose != "" {
		context = strings.TrimSpace(strings.TrimPrefix(lookInto, purpose+"\n"))
		if context == purpose {
			context = ""
		}
	} else if decision.PlanVersion == WindowPlanVersion {
		context = ""
	}
	if context != "" {
		b.WriteString("\n执行上下文：")
		b.WriteString(context)
	}
	if intent := strings.TrimSpace(decision.Intent); intent != "" {
		b.WriteString("\n意图：")
		b.WriteString(intent)
	}
	if len(decision.Items) == 1 {
		item := decision.Items[0]
		if item.Delegator != "" {
			b.WriteString("\n委托人=")
			b.WriteString(item.Delegator)
		}
		if extra := strings.TrimSpace(item.LookInto); extra != "" && extra != lookInto && extra != deliverable {
			b.WriteString("\n补充范围：")
			b.WriteString(extra)
		}
	} else if len(decision.Items) > 1 {
		b.WriteString("\n窗口事项：")
		for i, item := range decision.Items {
			b.WriteString("\n")
			b.WriteString(fmt.Sprintf("%d. 委托人=%s；%s", i+1, item.Delegator, item.LookInto))
		}
	}
	b.WriteString("\n\n身份与交付：Issue 创建人或评论人只表示谁操作了 Issue，不自动等同于本次委托人或消息接收人。")
	switch decision.Source {
	case SourceWeb:
		b.WriteString("本任务来自 Web 会话。通过当前任务结果或已有会话回传渠道返回结果、阻塞说明；无需为此查找钉钉发信人或收件人。")
	case SourceDigitalEmployee, SourceRobot:
		b.WriteString("本任务来自钉钉，委托人采用当前可信派发事件里的发信人。只使用事件已提供的身份、名称和会话；缺少用户标识时不虚构身份或改用 Issue 署名。按已有交付上下文回复原会话；上下文要求直接发送时，核验实际发送结果。")
	default:
		b.WriteString("本任务来源未确认。使用任务中已有的来源和交付上下文，并记录任务结果；不推定钉钉发信人、接收人或 Web 回传渠道。")
	}
	b.WriteString("当前请求明确授权外发、代问或转达时，仍按原文指定的对象、渠道和范围执行，并保留委托与答复来源；没有外发要求时不另找接收人。派工和接待文案不证明已执行或送达，未确认发送成功不得声称已送达。涉及钉钉听记、文档、消息的检索时只使用本轮 scene_cid / conversation_id，禁止打开或引用其它群的内容。")
	b.WriteString("\n执行范围：以当前任务的最新约束和原始请求为准，只执行本交付物所需的动作。遇到依赖故障时记录已完成步骤、原始错误和阻塞，不自行寻找或修改凭证，也不扩展为未经授权的登录或环境维修。")
	return b.String()
}

func clipRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// configuredModel retains the previous model for snapshots predating this field.
func (c *Coordinator) configuredModel() string {
	if c != nil {
		if c.model != "" {
			return c.model
		}
		if c.ModelProvider != nil {
			if model := strings.TrimSpace(c.ModelProvider()); model != "" {
				return model
			}
		}
	}
	return coordinatorModel
}

func (turn Turn) modelName() string {
	if turn.model != "" {
		return turn.model
	}
	return coordinatorModel
}

// CurrentModel reports the Diamond default before a per-decision route is resolved.
func (c *Coordinator) CurrentModel() string { return c.configuredModel() }
