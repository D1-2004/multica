package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// runCardCancelAction is the action discriminator embedded in the run
// card's terminate button. The card-action handler (card_action.go)
// dispatches on it, so renderer and handler must agree on the literal.
const runCardCancelAction = "multica.task.cancel"

// runCardDetailLimit is how many of the newest task_message events the
// collapsible detail panel shows. The full transcript lives in the
// Multica run viewer; the card is a glanceable digest, not a mirror.
const defaultRunCardDetailLimit = 8

// defaultRunCardPatchInterval throttles task:message-driven patches.
// Lark caps card patches at 5 QPS per message; one patch every few
// seconds keeps the card feeling live without ever brushing the limit.
const defaultRunCardPatchInterval = 3 * time.Second

// runCardPreviewRunes caps a single detail line's preview text.
const runCardPreviewRunes = 80

// runCardStatusView maps an agent_task_queue.status to the card's
// visual state. Template colors come from Lark's fixed header palette.
type runCardStatusView struct {
	Label    string
	Template string
	TagColor string
	Active   bool   // still cancellable
	Verb     string // header verb: "{Agent} 正在处理 …" etc.
}

func runCardViewForStatus(status string) runCardStatusView {
	switch status {
	case "queued":
		return runCardStatusView{Label: "排队中", Template: "wathet", TagColor: "wathet", Active: true, Verb: "正在处理"}
	case "dispatched":
		return runCardStatusView{Label: "准备中", Template: "wathet", TagColor: "wathet", Active: true, Verb: "正在处理"}
	case "running":
		return runCardStatusView{Label: "正在工作", Template: "blue", TagColor: "blue", Active: true, Verb: "正在处理"}
	case "waiting_local_directory":
		return runCardStatusView{Label: "等待本地目录", Template: "orange", TagColor: "orange", Active: true, Verb: "正在处理"}
	case "deferred":
		return runCardStatusView{Label: "已延期", Template: "grey", TagColor: "grey", Active: true, Verb: "稍后处理"}
	case "completed":
		return runCardStatusView{Label: "已完成", Template: "green", TagColor: "green", Active: false, Verb: "已完成"}
	case "failed":
		return runCardStatusView{Label: "失败", Template: "red", TagColor: "red", Active: false, Verb: "处理失败"}
	case "cancelled":
		return runCardStatusView{Label: "已取消", Template: "grey", TagColor: "grey", Active: false, Verb: "已取消"}
	default:
		return runCardStatusView{Label: status, Template: "blue", TagColor: "blue", Active: false, Verb: "正在处理"}
	}
}

// runCardTerminal reports whether the task status is a terminal state —
// after the terminal patch the publisher drops its per-task worker.
func runCardTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// RunCardEvent is one line inside the collapsible run-detail panel.
type RunCardEvent struct {
	Seq     int32
	At      time.Time
	AtValid bool
	Label   string
	Preview string
}

// RunCardState is the full render input for one card revision. The
// publisher rebuilds it from fresh DB rows on every patch, so the card
// never depends on in-memory accumulation that could drift from the
// run viewer.
type RunCardState struct {
	TaskID          string
	Status          string
	AgentName       string
	IssueIdentifier string
	IssueTitle      string
	IssueURL        string
	CreatedAt       time.Time
	StartedAt       time.Time
	StartedValid    bool
	CompletedAt     time.Time
	CompletedValid  bool
	Now             time.Time
	TotalEvents     int
	ToolCalls       int
	Events          []RunCardEvent // newest first
	ErrorMessage    string
}

// renderRunCard produces the schema-2.0 interactive card JSON for a
// run-bound status card: colored status header, progress summary,
// collapsible event-detail panel, and a terminate button while the run
// is still cancellable. update_multi stays true on every revision —
// the card is sent once and patched through the whole lifecycle (see
// defaultRenderer.Render for why Lark silently drops patches without
// it).
func renderRunCard(st RunCardState) (string, error) {
	view := runCardViewForStatus(st.Status)

	title := fmt.Sprintf("%s %s %s", st.AgentName, view.Verb, st.IssueIdentifier)
	if st.AgentName == "" {
		title = fmt.Sprintf("Multica %s %s", view.Verb, st.IssueIdentifier)
	}

	header := map[string]any{
		"template": view.Template,
		"title":    map[string]any{"tag": "plain_text", "content": title},
		"text_tag_list": []any{
			map[string]any{
				"tag":   "text_tag",
				"text":  map[string]any{"tag": "plain_text", "content": view.Label},
				"color": view.TagColor,
			},
		},
	}
	if sub := truncateRunes(st.IssueTitle, 60); sub != "" {
		header["subtitle"] = map[string]any{"tag": "plain_text", "content": sub}
	}

	elements := []any{
		map[string]any{
			"tag":     "markdown",
			"content": runCardSummaryMarkdown(st, view),
		},
	}

	if len(st.Events) > 0 {
		detail := make([]any, 0, len(st.Events))
		for _, ev := range st.Events {
			detail = append(detail, map[string]any{
				"tag":     "markdown",
				"content": runCardEventLine(ev),
			})
		}
		panelTitle := fmt.Sprintf("运行细节（最近 %d / 共 %d 个事件）", len(st.Events), st.TotalEvents)
		elements = append(elements, map[string]any{
			"tag":      "collapsible_panel",
			"expanded": false,
			"header": map[string]any{
				"title":          map[string]any{"tag": "plain_text", "content": panelTitle},
				"vertical_align": "center",
				"icon": map[string]any{
					"tag":   "standard_icon",
					"token": "down-small-ccm_outlined",
					"color": "grey",
					"size":  "16px 16px",
				},
				"icon_position":       "right",
				"icon_expanded_angle": -180,
			},
			"border":   map[string]any{"color": "grey", "corner_radius": "5px"},
			"elements": detail,
		})
	}

	if buttons := runCardButtons(st, view); len(buttons) > 0 {
		columns := make([]any, 0, len(buttons))
		for _, b := range buttons {
			columns = append(columns, map[string]any{
				"tag":      "column",
				"width":    "auto",
				"elements": []any{b},
			})
		}
		elements = append(elements, map[string]any{
			"tag":                "column_set",
			"horizontal_spacing": "8px",
			"columns":            columns,
		})
	}

	doc := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi": true,
			"width_mode":   "fill",
		},
		"header": header,
		"body": map[string]any{
			"direction": "vertical",
			"elements":  elements,
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func runCardSummaryMarkdown(st RunCardState, view runCardStatusView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**状态** %s", view.Label)

	start := st.CreatedAt
	if st.StartedValid {
		start = st.StartedAt
	}
	if st.CompletedValid && !start.IsZero() {
		fmt.Fprintf(&b, " · 耗时 %s", formatRunDuration(st.CompletedAt.Sub(start)))
	} else if view.Active && st.Status != "queued" && !start.IsZero() && st.Now.After(start) {
		fmt.Fprintf(&b, " · 已运行 %s", formatRunDuration(st.Now.Sub(start)))
	}
	fmt.Fprintf(&b, "\n**进展** %d 个事件 · %d 次工具调用", st.TotalEvents, st.ToolCalls)
	if st.ErrorMessage != "" {
		fmt.Fprintf(&b, "\n**错误** %s", truncateRunes(sanitizeCardText(st.ErrorMessage), 200))
	}
	return b.String()
}

func runCardEventLine(ev RunCardEvent) string {
	ts := "--:--:--"
	if ev.AtValid {
		ts = ev.At.Local().Format("15:04:05")
	}
	line := fmt.Sprintf("`#%d %s` **%s**", ev.Seq, ts, sanitizeCardText(ev.Label))
	if ev.Preview != "" {
		line += " · " + truncateRunes(sanitizeCardText(ev.Preview), runCardPreviewRunes)
	}
	return line
}

func runCardButtons(st RunCardState, view runCardStatusView) []any {
	var buttons []any
	if view.Active {
		buttons = append(buttons, map[string]any{
			"tag":  "button",
			"text": map[string]any{"tag": "plain_text", "content": "终止运行"},
			"type": "danger",
			"behaviors": []any{
				map[string]any{
					"type": "callback",
					"value": map[string]any{
						"action":  runCardCancelAction,
						"task_id": st.TaskID,
					},
				},
			},
		})
	}
	if st.IssueURL != "" {
		buttons = append(buttons, map[string]any{
			"tag":  "button",
			"text": map[string]any{"tag": "plain_text", "content": "在 Multica 中打开"},
			"type": "default",
			"behaviors": []any{
				map[string]any{
					"type":        "open_url",
					"default_url": st.IssueURL,
					"pc_url":      st.IssueURL,
					"ios_url":     st.IssueURL,
					"android_url": st.IssueURL,
				},
			},
		})
	}
	return buttons
}

// taskMessageToEvent flattens one task_message row into a card detail
// line. Labels follow the run viewer's vocabulary: tool calls show the
// tool name, results show the same name with an arrow, agent narration
// shows "Agent".
func taskMessageToEvent(m db.TaskMessage) RunCardEvent {
	ev := RunCardEvent{Seq: m.Seq}
	if m.CreatedAt.Valid {
		ev.At = m.CreatedAt.Time
		ev.AtValid = true
	}
	tool := strings.TrimSpace(m.Tool.String)
	switch m.Type {
	case "tool_use":
		ev.Label = tool
		if ev.Label == "" {
			ev.Label = "工具调用"
		}
		ev.Preview = toolInputPreview(m.Input)
	case "tool_result":
		if tool != "" {
			ev.Label = "↳ " + tool
		} else {
			ev.Label = "↳ 结果"
		}
		ev.Preview = firstLine(m.Output.String)
	case "thinking":
		ev.Label = "思考"
		ev.Preview = firstLine(m.Content.String)
	case "error":
		ev.Label = "错误"
		ev.Preview = firstLine(m.Content.String)
	default: // "text" and future types
		ev.Label = "Agent"
		ev.Preview = firstLine(m.Content.String)
	}
	return ev
}

// toolInputPreview extracts a human-scannable one-liner from a tool_use
// input JSONB. The common daemon shapes carry the interesting part in a
// "command" (exec) or "description" field; anything else falls back to
// the compact JSON itself.
func toolInputPreview(input []byte) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(input, &m); err == nil {
		for _, key := range []string{"command", "description", "path", "file_path", "prompt"} {
			if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
				return firstLine(s)
			}
		}
	}
	return firstLine(string(input))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// sanitizeCardText strips characters that would break out of the
// markdown inline-code / bold spans the detail lines are built from.
func sanitizeCardText(s string) string {
	s = strings.NewReplacer("`", "'", "*", "∗", "\n", " ", "\r", " ").Replace(s)
	return strings.TrimSpace(s)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func formatRunDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%d 小时 %d 分", h, m)
	case m > 0:
		return fmt.Sprintf("%d 分 %d 秒", m, s)
	default:
		return fmt.Sprintf("%d 秒", s)
	}
}

// RunCardQueries is the narrow DB surface the publisher needs.
// *ChannelStore satisfies it: the Lark-prefixed methods route to
// channel_* tables, the rest pass through the embedded *db.Queries.
type RunCardQueries interface {
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	GetIssue(ctx context.Context, id pgtype.UUID) (db.Issue, error)
	GetWorkspace(ctx context.Context, id pgtype.UUID) (db.Workspace, error)
	GetAgent(ctx context.Context, id pgtype.UUID) (db.Agent, error)
	ListTaskMessages(ctx context.Context, taskID pgtype.UUID) ([]db.TaskMessage, error)
	GetLarkInstallation(ctx context.Context, id pgtype.UUID) (Installation, error)
	GetLarkChatSessionBindingBySession(ctx context.Context, chatSessionID pgtype.UUID) (ChatSessionBinding, error)
	GetLarkOutboundCardByTask(ctx context.Context, taskID pgtype.UUID) (OutboundCardMessage, error)
	CreateLarkOutboundCardMessage(ctx context.Context, arg CreateOutboundCardMessageParams) (OutboundCardMessage, error)
	UpdateLarkOutboundCardStatus(ctx context.Context, arg UpdateOutboundCardStatusParams) error
}

// RunCardPublisherConfig tunes the publisher. Zero values default at
// construction; tests override Now / newTimer / spawn for determinism.
type RunCardPublisherConfig struct {
	// AppURL is the Multica web app origin (MULTICA_APP_URL) used for
	// the card's "open in Multica" button. Empty omits the button.
	AppURL string
	// PatchMinInterval throttles task:message-driven patches per task.
	PatchMinInterval time.Duration
	// DetailEventLimit caps the collapsible panel's event lines.
	DetailEventLimit int
	Logger           *slog.Logger
	Now              func() time.Time

	// newTimer and spawn are test seams. Production defaults are
	// time.AfterFunc and `go fn()`.
	newTimer func(d time.Duration, fn func()) stopTimer
	spawn    func(fn func())
}

// stopTimer is the slice of *time.Timer the throttle depends on.
type stopTimer interface{ Stop() bool }

func (c RunCardPublisherConfig) withDefaults() RunCardPublisherConfig {
	if c.PatchMinInterval <= 0 {
		c.PatchMinInterval = defaultRunCardPatchInterval
	}
	if c.DetailEventLimit <= 0 {
		c.DetailEventLimit = defaultRunCardDetailLimit
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.newTimer == nil {
		c.newTimer = func(d time.Duration, fn func()) stopTimer { return time.AfterFunc(d, fn) }
	}
	if c.spawn == nil {
		c.spawn = func(fn func()) { go fn() }
	}
	return c
}

// RunCardPublisher reacts to task-lifecycle bus events for issue tasks
// whose issue originated from a Lark chat (`/issue` in a group) and
// keeps one interactive "run card" per task in that chat: sent when the
// run is enqueued, patched on every status transition, patched on a
// throttle for task:message progress, and finalized (terminate button
// removed, header recolored) on the terminal event.
//
// It deliberately mirrors the Patcher's structure — narrow query
// interface, per-event bounded context, best-effort logging — but is a
// separate subscriber because the two features have opposite scopes:
// the Patcher handles chat-session tasks and explicitly ignores issue
// tasks; the run card handles issue tasks and ignores chat sessions.
//
// All rendering happens from fresh DB reads inside the worker, so a
// patch can never publish state older than what the run viewer shows.
// Bus callbacks only flip in-memory flags and return — the actual DB
// and Lark I/O runs on per-task worker goroutines, keeping the
// synchronous event bus (and therefore the daemon ingest path that
// publishes on it) unblocked.
type RunCardPublisher struct {
	queries     RunCardQueries
	credentials CredentialsResolver
	client      APIClient
	cfg         RunCardPublisherConfig

	mu      sync.Mutex
	workers map[string]*runCardWorker
	// negative caches tasks proven not to be run-card targets (chat
	// tasks, non-Lark origins, missing bindings) so the task:message
	// firehose costs one map lookup after the first resolution.
	negative map[string]struct{}
}

// runCardNegativeCap bounds the negative cache. On overflow the whole
// map resets — entries are cheap to rebuild (one resolution round).
const runCardNegativeCap = 4096

// NewRunCardPublisher constructs the publisher. Call Register to
// subscribe it to the event bus.
func NewRunCardPublisher(queries RunCardQueries, credentials CredentialsResolver, client APIClient, cfg RunCardPublisherConfig) *RunCardPublisher {
	return &RunCardPublisher{
		queries:     queries,
		credentials: credentials,
		client:      client,
		cfg:         cfg.withDefaults(),
		workers:     make(map[string]*runCardWorker),
		negative:    make(map[string]struct{}),
	}
}

// Register subscribes the publisher to every task-lifecycle event plus
// the task:message stream. Call exactly once at boot.
func (p *RunCardPublisher) Register(bus *events.Bus) {
	for _, t := range []string{
		protocol.EventTaskQueued,
		protocol.EventTaskDispatch,
		protocol.EventTaskRunning,
		protocol.EventTaskWaitingLocalDirectory,
		protocol.EventTaskProgress,
		protocol.EventTaskCompleted,
		protocol.EventTaskFailed,
		protocol.EventTaskCancelled,
		protocol.EventTaskMessage,
	} {
		bus.Subscribe(t, p.handleEvent)
	}
}

func (p *RunCardPublisher) handleEvent(e events.Event) {
	taskID, chatSessionID, ok := taskAndSessionFromEvent(e)
	if !ok {
		return
	}
	if chatSessionID.Valid {
		// Chat-session task — owned by the Patcher's plain-reply flow.
		return
	}
	urgent := e.Type != protocol.EventTaskMessage && e.Type != protocol.EventTaskProgress
	p.schedule(uuidString(taskID), urgent)
}

// RefreshCard re-renders and patches the card for taskID from current
// DB state. Used by the card-action handler after a cancel request so
// the card reflects the outcome even when the task was already
// terminal (no bus event fires in that case).
func (p *RunCardPublisher) RefreshCard(taskID string) {
	p.schedule(taskID, true)
}

func (p *RunCardPublisher) schedule(taskID string, urgent bool) {
	if taskID == "" {
		return
	}
	p.mu.Lock()
	if _, bad := p.negative[taskID]; bad {
		p.mu.Unlock()
		return
	}
	w := p.workers[taskID]
	if w == nil {
		w = &runCardWorker{pub: p, taskID: taskID}
		p.workers[taskID] = w
	}
	p.mu.Unlock()
	w.request(urgent)
}

func (p *RunCardPublisher) markNegative(taskID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.negative) >= runCardNegativeCap {
		p.negative = make(map[string]struct{})
	}
	p.negative[taskID] = struct{}{}
	delete(p.workers, taskID)
}

func (p *RunCardPublisher) dropWorker(taskID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.workers, taskID)
}

// runCardTarget is the resolved routing for one task's card: where it
// goes and the static issue/agent facts that never change over the run.
type runCardTarget struct {
	binding         ChatSessionBinding
	creds           InstallationCredentials
	agentName       string
	issueIdentifier string
	issueTitle      string
	issueURL        string
}

// runCardWorker serializes all I/O for one task's card. dirty/urgent
// coalesce bursts: any number of events between two patches produce
// exactly one re-render from fresh state.
type runCardWorker struct {
	pub    *RunCardPublisher
	taskID string

	mu        sync.Mutex
	dirty     bool
	urgent    bool
	running   bool
	lastPatch time.Time
	timer     stopTimer
	target    *runCardTarget
}

func (w *runCardWorker) request(urgent bool) {
	p := w.pub
	w.mu.Lock()
	w.dirty = true
	if urgent {
		w.urgent = true
	}
	if w.running {
		w.mu.Unlock()
		return
	}
	if !w.urgent {
		if wait := p.cfg.PatchMinInterval - p.cfg.Now().Sub(w.lastPatch); wait > 0 {
			if w.timer == nil {
				w.timer = p.cfg.newTimer(wait, w.timerFired)
			}
			w.mu.Unlock()
			return
		}
	}
	w.begin()
	w.mu.Unlock()
	p.cfg.spawn(w.run)
}

// timerFired is the throttle timer's trailing edge: promote the pending
// dirty flag into a run if nothing else picked it up meanwhile.
func (w *runCardWorker) timerFired() {
	w.mu.Lock()
	w.timer = nil
	if w.running || !w.dirty {
		w.mu.Unlock()
		return
	}
	w.begin()
	w.mu.Unlock()
	w.pub.cfg.spawn(w.run)
}

// begin transitions to running. Caller holds w.mu.
func (w *runCardWorker) begin() {
	w.running = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

func (w *runCardWorker) run() {
	p := w.pub
	for {
		w.mu.Lock()
		if !w.dirty {
			w.running = false
			w.mu.Unlock()
			return
		}
		w.dirty = false
		w.urgent = false
		w.mu.Unlock()

		outcome := p.publish(w)

		w.mu.Lock()
		w.lastPatch = p.cfg.Now()
		switch outcome {
		case runCardOutcomeNegative:
			w.running = false
			w.mu.Unlock()
			p.markNegative(w.taskID)
			return
		case runCardOutcomeTerminal:
			if !w.dirty {
				w.running = false
				w.mu.Unlock()
				p.dropWorker(w.taskID)
				return
			}
		}
		if !w.dirty {
			w.running = false
			w.mu.Unlock()
			return
		}
		// More work queued while we were publishing. Urgent requests
		// loop immediately; message-driven ones respect the throttle
		// via a trailing-edge timer.
		if !w.urgent {
			if wait := p.cfg.PatchMinInterval - p.cfg.Now().Sub(w.lastPatch); wait > 0 {
				if w.timer == nil {
					w.timer = p.cfg.newTimer(wait, w.timerFired)
				}
				w.running = false
				w.mu.Unlock()
				return
			}
		}
		w.mu.Unlock()
	}
}

type runCardOutcome int

const (
	runCardOutcomeOK runCardOutcome = iota
	// runCardOutcomeRetry: transient failure — do not cache anything;
	// the next task event retries resolution naturally.
	runCardOutcomeRetry
	// runCardOutcomeNegative: proven non-target — cache and stop.
	runCardOutcomeNegative
	// runCardOutcomeTerminal: card finalized — drop the worker.
	runCardOutcomeTerminal
)

// publish re-renders the card for the worker's task from fresh DB state
// and sends or patches it. One bounded context per attempt, mirroring
// the Patcher's budget so a stuck Lark call cannot pile up goroutines
// unboundedly (the worker serializes attempts per task).
func (p *RunCardPublisher) publish(w *runCardWorker) runCardOutcome {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log := p.cfg.Logger
	taskUUID, err := util.ParseUUID(w.taskID)
	if err != nil {
		log.Warn("lark run card: invalid task id", "task_id", w.taskID, "error", err)
		return runCardOutcomeNegative
	}

	task, err := p.queries.GetAgentTask(ctx, taskUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Enqueue commit may not be visible yet; the dispatch /
			// running events that follow re-trigger us.
			return runCardOutcomeRetry
		}
		log.Warn("lark run card: load task failed", "task_id", w.taskID, "error", err)
		return runCardOutcomeRetry
	}
	if task.ChatSessionID.Valid || !task.IssueID.Valid {
		return runCardOutcomeNegative
	}

	target, outcome := p.resolveTarget(ctx, w, task)
	if outcome != runCardOutcomeOK {
		return outcome
	}

	st, err := p.buildState(ctx, task, target)
	if err != nil {
		log.Warn("lark run card: build state failed", "task_id", w.taskID, "error", err)
		return runCardOutcomeRetry
	}
	cardJSON, err := renderRunCard(st)
	if err != nil {
		log.Warn("lark run card: render failed", "task_id", w.taskID, "error", err)
		return runCardOutcomeRetry
	}

	terminal := runCardTerminal(task.Status)
	if err := p.deliver(ctx, task, target, cardJSON, terminal); err != nil {
		log.Warn("lark run card: deliver failed", "task_id", w.taskID, "error", err)
		return runCardOutcomeRetry
	}
	if terminal {
		return runCardOutcomeTerminal
	}
	return runCardOutcomeOK
}

// resolveTarget resolves (and caches on the worker) the Lark routing
// for the task's issue. Only definitive misses are negative; transient
// DB errors return retry so a hiccup can't permanently silence a card.
func (p *RunCardPublisher) resolveTarget(ctx context.Context, w *runCardWorker, task db.AgentTaskQueue) (*runCardTarget, runCardOutcome) {
	w.mu.Lock()
	cached := w.target
	w.mu.Unlock()
	if cached != nil {
		return cached, runCardOutcomeOK
	}

	log := p.cfg.Logger
	issue, err := p.queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, runCardOutcomeNegative
		}
		log.Warn("lark run card: load issue failed", "task_id", w.taskID, "error", err)
		return nil, runCardOutcomeRetry
	}
	if !issue.OriginType.Valid || issue.OriginType.String != "lark_chat" || !issue.OriginID.Valid {
		return nil, runCardOutcomeNegative
	}

	binding, err := p.queries.GetLarkChatSessionBindingBySession(ctx, issue.OriginID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, runCardOutcomeNegative
		}
		log.Warn("lark run card: load chat binding failed", "task_id", w.taskID, "error", err)
		return nil, runCardOutcomeRetry
	}
	inst, err := p.queries.GetLarkInstallation(ctx, binding.InstallationID)
	if err != nil {
		log.Warn("lark run card: load installation failed", "task_id", w.taskID, "error", err)
		return nil, runCardOutcomeRetry
	}
	if InstallationStatus(inst.Status) != InstallationActive {
		return nil, runCardOutcomeNegative
	}
	if p.credentials == nil {
		log.Warn("lark run card: credentials resolver missing")
		return nil, runCardOutcomeNegative
	}
	secret, err := p.credentials.DecryptAppSecret(inst)
	if err != nil {
		log.Warn("lark run card: decrypt app secret failed", "task_id", w.taskID, "error", err)
		return nil, runCardOutcomeRetry
	}
	creds := InstallationCredentials{
		AppID:     inst.AppID,
		AppSecret: secret,
		Region:    RegionOrDefault(inst.Region),
	}
	if inst.TenantKey.Valid {
		creds.TenantKey = inst.TenantKey.String
	}

	identifier := fmt.Sprintf("#%d", issue.Number)
	issueURL := ""
	if ws, werr := p.queries.GetWorkspace(ctx, issue.WorkspaceID); werr == nil && ws.IssuePrefix != "" {
		identifier = fmt.Sprintf("%s-%d", ws.IssuePrefix, issue.Number)
	}
	if p.cfg.AppURL != "" {
		issueURL = strings.TrimRight(p.cfg.AppURL, "/") + "/issues/" + identifier
	}

	agentName := ""
	if agent, aerr := p.queries.GetAgent(ctx, task.AgentID); aerr == nil {
		agentName = agent.Name
	}

	target := &runCardTarget{
		binding:         binding,
		creds:           creds,
		agentName:       agentName,
		issueIdentifier: identifier,
		issueTitle:      issue.Title,
		issueURL:        issueURL,
	}
	w.mu.Lock()
	w.target = target
	w.mu.Unlock()
	return target, runCardOutcomeOK
}

func (p *RunCardPublisher) buildState(ctx context.Context, task db.AgentTaskQueue, target *runCardTarget) (RunCardState, error) {
	msgs, err := p.queries.ListTaskMessages(ctx, task.ID)
	if err != nil {
		return RunCardState{}, fmt.Errorf("list task messages: %w", err)
	}
	toolCalls := 0
	for _, m := range msgs {
		if m.Type == "tool_use" {
			toolCalls++
		}
	}
	limit := p.cfg.DetailEventLimit
	tail := msgs
	if len(tail) > limit {
		tail = tail[len(tail)-limit:]
	}
	// Newest first, matching the run viewer's default ordering.
	events := make([]RunCardEvent, 0, len(tail))
	for i := len(tail) - 1; i >= 0; i-- {
		events = append(events, taskMessageToEvent(tail[i]))
	}

	st := RunCardState{
		TaskID:          uuidString(task.ID),
		Status:          task.Status,
		AgentName:       target.agentName,
		IssueIdentifier: target.issueIdentifier,
		IssueTitle:      target.issueTitle,
		IssueURL:        target.issueURL,
		Now:             p.cfg.Now(),
		TotalEvents:     len(msgs),
		ToolCalls:       toolCalls,
		Events:          events,
		ErrorMessage:    task.Error.String,
	}
	if task.CreatedAt.Valid {
		st.CreatedAt = task.CreatedAt.Time
	}
	if task.StartedAt.Valid {
		st.StartedAt = task.StartedAt.Time
		st.StartedValid = true
	}
	if task.CompletedAt.Valid {
		st.CompletedAt = task.CompletedAt.Time
		st.CompletedValid = true
	}
	return st, nil
}

// deliver sends the first revision (and records the outbound row) or
// patches the existing card. The unique index on
// channel_outbound_card_message.task_id makes the send race safe: a
// concurrent first-send loses the insert and converges via patch on
// the next revision.
func (p *RunCardPublisher) deliver(ctx context.Context, task db.AgentTaskQueue, target *runCardTarget, cardJSON string, terminal bool) error {
	card, err := p.queries.GetLarkOutboundCardByTask(ctx, task.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		if terminal {
			// The run ended before we ever sent a card (e.g. instant
			// failure while Lark was unreachable). A dead run's first
			// card is still worth sending — it carries the outcome.
			p.cfg.Logger.Info("lark run card: first send is terminal", "task_id", uuidString(task.ID), "status", task.Status)
		}
		var messageID string
		sendErr := sendWithThreadFallback(p.cfg.Logger, "send run card", threadReplyTarget(target.binding), func(t ReplyTarget) error {
			var serr error
			messageID, serr = p.client.SendInteractiveCard(ctx, SendCardParams{
				InstallationID: target.creds,
				ChatID:         ChatID(target.binding.ChannelChatID),
				CardJSON:       cardJSON,
				ReplyTarget:    t,
			})
			return serr
		})
		if sendErr != nil {
			return sendErr
		}
		status := CardStatusStreaming
		if terminal {
			status = cardStatusForTask(task.Status)
		}
		if _, cerr := p.queries.CreateLarkOutboundCardMessage(ctx, CreateOutboundCardMessageParams{
			ChatSessionID:        target.binding.ChatSessionID,
			TaskID:               task.ID,
			ChannelChatID:        target.binding.ChannelChatID,
			ChannelCardMessageID: messageID,
			Status:               string(status),
		}); cerr != nil {
			// Row insert failed after a successful send (or a racer
			// inserted first). Log loudly: without the row the next
			// revision would send a duplicate card.
			p.cfg.Logger.Error("lark run card: record outbound card failed",
				"task_id", uuidString(task.ID), "message_id", messageID, "error", cerr)
			return cerr
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("load outbound card: %w", err)
	}

	if perr := p.client.PatchInteractiveCard(ctx, PatchCardParams{
		InstallationID:    target.creds,
		LarkCardMessageID: card.ChannelCardMessageID,
		CardJSON:          cardJSON,
	}); perr != nil {
		return perr
	}
	if terminal {
		if uerr := p.queries.UpdateLarkOutboundCardStatus(ctx, UpdateOutboundCardStatusParams{
			ID:     card.ID,
			Status: string(cardStatusForTask(task.Status)),
		}); uerr != nil {
			p.cfg.Logger.Warn("lark run card: update card row status failed",
				"task_id", uuidString(task.ID), "error", uerr)
		}
	}
	return nil
}

func cardStatusForTask(taskStatus string) CardStatus {
	switch taskStatus {
	case "failed":
		return CardStatusError
	case "completed", "cancelled":
		return CardStatusFinal
	default:
		return CardStatusStreaming
	}
}
