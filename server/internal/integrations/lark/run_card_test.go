package lark

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// --- renderer -------------------------------------------------------------

func runningState() RunCardState {
	base := time.Date(2026, 7, 2, 18, 32, 0, 0, time.UTC)
	return RunCardState{
		TaskID:          "3f6f3c1e-8f5a-4d55-9c60-0a53a67a1111",
		Status:          "running",
		AgentName:       "Codex",
		IssueIdentifier: "FDE-6",
		IssueTitle:      "修复登录问题",
		IssueURL:        "https://multica.example/issues/FDE-6",
		CreatedAt:       base,
		StartedAt:       base.Add(10 * time.Second),
		StartedValid:    true,
		Now:             base.Add(2 * time.Minute),
		TotalEvents:     19,
		ToolCalls:       8,
		Events: []RunCardEvent{
			{Seq: 19, At: base.Add(2 * time.Minute), AtValid: true, Label: "exec_command", Preview: "multica issue comment add"},
			{Seq: 16, At: base.Add(90 * time.Second), AtValid: true, Label: "Agent", Preview: "writing the comment body"},
		},
	}
}

func parseCard(t *testing.T, cardJSON string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(cardJSON), &doc); err != nil {
		t.Fatalf("card JSON does not parse: %v", err)
	}
	return doc
}

func bodyElements(t *testing.T, doc map[string]any) []any {
	t.Helper()
	body, ok := doc["body"].(map[string]any)
	if !ok {
		t.Fatalf("card missing body: %v", doc)
	}
	els, ok := body["elements"].([]any)
	if !ok {
		t.Fatalf("card body missing elements")
	}
	return els
}

// findElement returns the first body element with the given tag.
func findElement(t *testing.T, doc map[string]any, tag string) map[string]any {
	t.Helper()
	for _, el := range bodyElements(t, doc) {
		m, ok := el.(map[string]any)
		if ok && m["tag"] == tag {
			return m
		}
	}
	return nil
}

func TestRenderRunCardRunning(t *testing.T) {
	cardJSON, err := renderRunCard(runningState())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	doc := parseCard(t, cardJSON)

	if doc["schema"] != "2.0" {
		t.Fatalf("schema = %v, want 2.0", doc["schema"])
	}
	cfg := doc["config"].(map[string]any)
	if cfg["update_multi"] != true {
		t.Fatalf("config.update_multi must be true on every revision")
	}
	header := doc["header"].(map[string]any)
	if header["template"] != "blue" {
		t.Fatalf("running header template = %v, want blue", header["template"])
	}
	title := header["title"].(map[string]any)["content"].(string)
	if !strings.Contains(title, "Codex") || !strings.Contains(title, "FDE-6") {
		t.Fatalf("title %q should carry agent and identifier", title)
	}

	panel := findElement(t, doc, "collapsible_panel")
	if panel == nil {
		t.Fatalf("running card must include the run-detail collapsible panel")
	}
	panelTitle := panel["header"].(map[string]any)["title"].(map[string]any)["content"].(string)
	if !strings.Contains(panelTitle, "2") || !strings.Contains(panelTitle, "19") {
		t.Fatalf("panel title %q should show shown/total counts", panelTitle)
	}
	if got := len(panel["elements"].([]any)); got != 2 {
		t.Fatalf("panel has %d detail lines, want 2", got)
	}

	if !strings.Contains(cardJSON, runCardCancelAction) {
		t.Fatalf("running card must carry the cancel callback action")
	}
	if !strings.Contains(cardJSON, runningState().TaskID) {
		t.Fatalf("cancel button value must bind the task id")
	}
	if !strings.Contains(cardJSON, "https://multica.example/issues/FDE-6") {
		t.Fatalf("card must carry the open-in-Multica URL")
	}
}

func TestRenderRunCardTerminalStates(t *testing.T) {
	cases := []struct {
		status   string
		template string
	}{
		{"completed", "green"},
		{"failed", "red"},
		{"cancelled", "grey"},
	}
	for _, tc := range cases {
		st := runningState()
		st.Status = tc.status
		st.CompletedAt = st.Now
		st.CompletedValid = true
		if tc.status == "failed" {
			st.ErrorMessage = "boom `with backticks`"
		}
		cardJSON, err := renderRunCard(st)
		if err != nil {
			t.Fatalf("render %s: %v", tc.status, err)
		}
		doc := parseCard(t, cardJSON)
		header := doc["header"].(map[string]any)
		if header["template"] != tc.template {
			t.Fatalf("%s header template = %v, want %s", tc.status, header["template"], tc.template)
		}
		if strings.Contains(cardJSON, runCardCancelAction) {
			t.Fatalf("%s card must not offer the terminate button", tc.status)
		}
		if tc.status == "failed" {
			if !strings.Contains(cardJSON, "**错误**") {
				t.Fatalf("failed card should surface the error line")
			}
			if strings.Contains(cardJSON, "`with backticks`") {
				t.Fatalf("error text must be sanitized against markdown breakout")
			}
		}
	}
}

func TestRenderRunCardWithoutAppURL(t *testing.T) {
	st := runningState()
	st.IssueURL = ""
	cardJSON, err := renderRunCard(st)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(cardJSON, "open_url") {
		t.Fatalf("card without AppURL must omit the open button")
	}
}

func TestRunCardEventLineSanitizes(t *testing.T) {
	long := strings.Repeat("字", 100)
	line := runCardEventLine(RunCardEvent{Seq: 3, Label: "exec`cmd`", Preview: "a\nb`c`" + long})
	if strings.Contains(line, "`c`") || strings.Contains(line, "exec`cmd`") {
		t.Fatalf("backticks must not survive into the line: %q", line)
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("newlines must not survive into the line: %q", line)
	}
	if !strings.Contains(line, "…") {
		t.Fatalf("long preview should be truncated with an ellipsis: %q", line)
	}
}

func TestTaskMessageToEvent(t *testing.T) {
	at := pgtype.Timestamptz{Time: time.Date(2026, 7, 2, 18, 34, 59, 0, time.UTC), Valid: true}
	toolUse := taskMessageToEvent(db.TaskMessage{
		Seq: 19, Type: "tool_use",
		Tool:      pgtype.Text{String: "exec_command", Valid: true},
		Input:     []byte(`{"command":"multica issue status abc in_progress"}`),
		CreatedAt: at,
	})
	if toolUse.Label != "exec_command" {
		t.Fatalf("tool_use label = %q", toolUse.Label)
	}
	if toolUse.Preview != "multica issue status abc in_progress" {
		t.Fatalf("tool_use preview = %q", toolUse.Preview)
	}

	result := taskMessageToEvent(db.TaskMessage{
		Seq: 20, Type: "tool_result",
		Tool:   pgtype.Text{String: "exec_command", Valid: true},
		Output: pgtype.Text{String: "ok\nsecond line", Valid: true},
	})
	if result.Label != "↳ exec_command" || result.Preview != "ok" {
		t.Fatalf("tool_result = %q / %q", result.Label, result.Preview)
	}

	text := taskMessageToEvent(db.TaskMessage{
		Seq: 21, Type: "text",
		Content: pgtype.Text{String: "I'm writing the comment", Valid: true},
	})
	if text.Label != "Agent" || text.Preview != "I'm writing the comment" {
		t.Fatalf("text = %q / %q", text.Label, text.Preview)
	}
}

// --- publisher fakes -------------------------------------------------------

type fakeRunCardStore struct {
	mu sync.Mutex

	tasks         map[string]db.AgentTaskQueue
	issues        map[string]db.Issue
	workspaces    map[string]db.Workspace
	agents        map[string]db.Agent
	installations map[string]Installation
	bindings      map[string]ChatSessionBinding // keyed by chat_session_id
	messages      map[string][]db.TaskMessage

	cards map[string]OutboundCardMessage // keyed by task_id

	getIssueCalls  int
	statusUpdates  []UpdateOutboundCardStatusParams
	createdCards   []CreateOutboundCardMessageParams
	nextCardRowSeq int
}

func newFakeRunCardStore() *fakeRunCardStore {
	return &fakeRunCardStore{
		tasks:         map[string]db.AgentTaskQueue{},
		issues:        map[string]db.Issue{},
		workspaces:    map[string]db.Workspace{},
		agents:        map[string]db.Agent{},
		installations: map[string]Installation{},
		bindings:      map[string]ChatSessionBinding{},
		messages:      map[string][]db.TaskMessage{},
		cards:         map[string]OutboundCardMessage{},
	}
}

func (f *fakeRunCardStore) GetAgentTask(_ context.Context, id pgtype.UUID) (db.AgentTaskQueue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[uuidString(id)]
	if !ok {
		return db.AgentTaskQueue{}, pgx.ErrNoRows
	}
	return t, nil
}

func (f *fakeRunCardStore) GetIssue(_ context.Context, id pgtype.UUID) (db.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getIssueCalls++
	i, ok := f.issues[uuidString(id)]
	if !ok {
		return db.Issue{}, pgx.ErrNoRows
	}
	return i, nil
}

func (f *fakeRunCardStore) GetWorkspace(_ context.Context, id pgtype.UUID) (db.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workspaces[uuidString(id)]
	if !ok {
		return db.Workspace{}, pgx.ErrNoRows
	}
	return w, nil
}

func (f *fakeRunCardStore) GetAgent(_ context.Context, id pgtype.UUID) (db.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[uuidString(id)]
	if !ok {
		return db.Agent{}, pgx.ErrNoRows
	}
	return a, nil
}

func (f *fakeRunCardStore) ListTaskMessages(_ context.Context, taskID pgtype.UUID) ([]db.TaskMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.messages[uuidString(taskID)], nil
}

func (f *fakeRunCardStore) GetLarkInstallation(_ context.Context, id pgtype.UUID) (Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.installations[uuidString(id)]
	if !ok {
		return Installation{}, pgx.ErrNoRows
	}
	return inst, nil
}

func (f *fakeRunCardStore) GetLarkChatSessionBindingBySession(_ context.Context, chatSessionID pgtype.UUID) (ChatSessionBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bindings[uuidString(chatSessionID)]
	if !ok {
		return ChatSessionBinding{}, pgx.ErrNoRows
	}
	return b, nil
}

func (f *fakeRunCardStore) GetLarkOutboundCardByTask(_ context.Context, taskID pgtype.UUID) (OutboundCardMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.cards[uuidString(taskID)]
	if !ok {
		return OutboundCardMessage{}, pgx.ErrNoRows
	}
	return c, nil
}

func (f *fakeRunCardStore) CreateLarkOutboundCardMessage(_ context.Context, arg CreateOutboundCardMessageParams) (OutboundCardMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdCards = append(f.createdCards, arg)
	f.nextCardRowSeq++
	row := OutboundCardMessage{
		ID:                   pgtype.UUID{Bytes: [16]byte{byte(f.nextCardRowSeq)}, Valid: true},
		ChatSessionID:        arg.ChatSessionID,
		TaskID:               arg.TaskID,
		ChannelChatID:        arg.ChannelChatID,
		ChannelCardMessageID: arg.ChannelCardMessageID,
		Status:               arg.Status,
	}
	f.cards[uuidString(arg.TaskID)] = row
	return row, nil
}

func (f *fakeRunCardStore) UpdateLarkOutboundCardStatus(_ context.Context, arg UpdateOutboundCardStatusParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusUpdates = append(f.statusUpdates, arg)
	return nil
}

type fakeCardCredentials struct{}

func (fakeCardCredentials) DecryptAppSecret(inst Installation) (string, error) {
	return "secret", nil
}

type fakeCardClient struct {
	mu      sync.Mutex
	sends   []SendCardParams
	patches []PatchCardParams
}

func (f *fakeCardClient) IsConfigured() bool { return true }
func (f *fakeCardClient) SendInteractiveCard(_ context.Context, p SendCardParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, p)
	return "om_run_card", nil
}
func (f *fakeCardClient) PatchInteractiveCard(_ context.Context, p PatchCardParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches = append(f.patches, p)
	return nil
}
func (f *fakeCardClient) SendTextMessage(_ context.Context, p SendTextParams) (string, error) {
	return "om_text", nil
}
func (f *fakeCardClient) SendMarkdownCard(_ context.Context, p SendMarkdownCardParams) (string, error) {
	return "om_md", nil
}
func (f *fakeCardClient) SendBindingPromptCard(_ context.Context, p BindingPromptParams) error {
	return nil
}
func (f *fakeCardClient) GetBotInfo(_ context.Context, _ InstallationCredentials) (BotInfo, error) {
	return BotInfo{}, nil
}
func (f *fakeCardClient) GetMessage(_ context.Context, _ InstallationCredentials, _ string) ([]LarkMessage, error) {
	return nil, nil
}
func (f *fakeCardClient) ListChatMessages(_ context.Context, _ InstallationCredentials, _ ListMessagesParams) ([]LarkMessage, error) {
	return nil, nil
}
func (f *fakeCardClient) BatchGetUsers(_ context.Context, _ InstallationCredentials, _ []string) (map[string]string, error) {
	return nil, nil
}
func (f *fakeCardClient) AddMessageReaction(_ context.Context, _ AddReactionParams) (string, error) {
	return "", nil
}
func (f *fakeCardClient) DeleteMessageReaction(_ context.Context, _ DeleteReactionParams) error {
	return nil
}

// fakeTimer records the scheduled delay and lets the test fire it.
type fakeTimer struct {
	fn      func()
	stopped bool
}

func (f *fakeTimer) Stop() bool { f.stopped = true; return true }

// runCardFixture wires a publisher with fakes and one fully-bound
// lark_chat issue task.
type runCardFixture struct {
	store  *fakeRunCardStore
	client *fakeCardClient
	pub    *RunCardPublisher

	now    time.Time
	timers []*fakeTimer

	taskID    string
	issueID   string
	sessionID string
}

func newRunCardFixture(t *testing.T) *runCardFixture {
	t.Helper()
	fx := &runCardFixture{
		store:     newFakeRunCardStore(),
		client:    &fakeCardClient{},
		now:       time.Date(2026, 7, 2, 18, 0, 0, 0, time.UTC),
		taskID:    "11111111-1111-1111-1111-111111111111",
		issueID:   "22222222-2222-2222-2222-222222222222",
		sessionID: "33333333-3333-3333-3333-333333333333",
	}
	workspaceID := "44444444-4444-4444-4444-444444444444"
	agentID := "55555555-5555-5555-5555-555555555555"
	installationID := "66666666-6666-6666-6666-666666666666"

	fx.store.tasks[fx.taskID] = db.AgentTaskQueue{
		ID:      uuidFromString(t, fx.taskID),
		AgentID: uuidFromString(t, agentID),
		IssueID: uuidFromString(t, fx.issueID),
		Status:  "queued",
		CreatedAt: pgtype.Timestamptz{
			Time: fx.now.Add(-5 * time.Second), Valid: true,
		},
	}
	fx.store.issues[fx.issueID] = db.Issue{
		ID:          uuidFromString(t, fx.issueID),
		WorkspaceID: uuidFromString(t, workspaceID),
		Title:       "修复登录问题",
		Number:      6,
		OriginType:  pgtype.Text{String: "lark_chat", Valid: true},
		OriginID:    uuidFromString(t, fx.sessionID),
	}
	fx.store.workspaces[workspaceID] = db.Workspace{
		ID:          uuidFromString(t, workspaceID),
		IssuePrefix: "FDE",
	}
	fx.store.agents[agentID] = db.Agent{
		ID:   uuidFromString(t, agentID),
		Name: "Codex",
	}
	fx.store.installations[installationID] = Installation{
		ID:          uuidFromString(t, installationID),
		WorkspaceID: uuidFromString(t, workspaceID),
		AppID:       "cli_test",
		Status:      string(InstallationActive),
	}
	fx.store.bindings[fx.sessionID] = ChatSessionBinding{
		ChatSessionID:  uuidFromString(t, fx.sessionID),
		InstallationID: uuidFromString(t, installationID),
		ChannelChatID:  "oc_test_chat",
	}

	fx.pub = NewRunCardPublisher(fx.store, fakeCardCredentials{}, fx.client, RunCardPublisherConfig{
		AppURL:           "https://multica.example",
		PatchMinInterval: 3 * time.Second,
		Logger:           newDiscardLogger(),
		Now:              func() time.Time { return fx.now },
		newTimer: func(d time.Duration, fn func()) stopTimer {
			ft := &fakeTimer{fn: fn}
			fx.timers = append(fx.timers, ft)
			return ft
		},
		spawn: func(fn func()) { fn() },
	})
	return fx
}

func (fx *runCardFixture) setTaskStatus(t *testing.T, status string) {
	t.Helper()
	fx.store.mu.Lock()
	task := fx.store.tasks[fx.taskID]
	task.Status = status
	if status == "running" {
		task.StartedAt = pgtype.Timestamptz{Time: fx.now, Valid: true}
	}
	if runCardTerminal(status) {
		task.CompletedAt = pgtype.Timestamptz{Time: fx.now, Valid: true}
	}
	fx.store.tasks[fx.taskID] = task
	fx.store.mu.Unlock()
}

func (fx *runCardFixture) event(eventType string) events.Event {
	return events.Event{
		Type:   eventType,
		TaskID: fx.taskID,
		Payload: map[string]any{
			"task_id":  fx.taskID,
			"issue_id": fx.issueID,
		},
	}
}

// --- publisher tests --------------------------------------------------------

func TestRunCardPublisherSendsOnQueued(t *testing.T) {
	fx := newRunCardFixture(t)
	fx.pub.handleEvent(fx.event(protocol.EventTaskQueued))

	if len(fx.client.sends) != 1 {
		t.Fatalf("sends = %d, want 1", len(fx.client.sends))
	}
	send := fx.client.sends[0]
	if string(send.ChatID) != "oc_test_chat" {
		t.Fatalf("card sent to %q", send.ChatID)
	}
	if !strings.Contains(send.CardJSON, runCardCancelAction) {
		t.Fatalf("first revision must carry the terminate button")
	}
	if !strings.Contains(send.CardJSON, "FDE-6") {
		t.Fatalf("card must carry the issue identifier, got %s", send.CardJSON)
	}
	if len(fx.store.createdCards) != 1 {
		t.Fatalf("outbound card rows = %d, want 1", len(fx.store.createdCards))
	}
	created := fx.store.createdCards[0]
	if uuidString(created.ChatSessionID) != fx.sessionID {
		t.Fatalf("card row chat_session = %s", uuidString(created.ChatSessionID))
	}
	if created.Status != string(CardStatusStreaming) {
		t.Fatalf("card row status = %s", created.Status)
	}
}

func TestRunCardPublisherPatchesOnStatusTransition(t *testing.T) {
	fx := newRunCardFixture(t)
	fx.pub.handleEvent(fx.event(protocol.EventTaskQueued))
	fx.now = fx.now.Add(500 * time.Millisecond)

	fx.setTaskStatus(t, "running")
	fx.pub.handleEvent(fx.event(protocol.EventTaskRunning))

	if len(fx.client.sends) != 1 {
		t.Fatalf("sends = %d, want 1 (second revision must patch)", len(fx.client.sends))
	}
	if len(fx.client.patches) != 1 {
		t.Fatalf("patches = %d, want 1", len(fx.client.patches))
	}
	patch := fx.client.patches[0]
	if patch.LarkCardMessageID != "om_run_card" {
		t.Fatalf("patch targets %q", patch.LarkCardMessageID)
	}
	if !strings.Contains(patch.CardJSON, "正在工作") {
		t.Fatalf("running revision should carry the running status label")
	}
}

func TestRunCardPublisherSkipsChatSessionTasks(t *testing.T) {
	fx := newRunCardFixture(t)
	fx.pub.handleEvent(events.Event{
		Type:   protocol.EventTaskQueued,
		TaskID: fx.taskID,
		Payload: map[string]any{
			"task_id":         fx.taskID,
			"chat_session_id": fx.sessionID,
		},
	})
	if len(fx.client.sends)+len(fx.client.patches) != 0 {
		t.Fatalf("chat-session tasks must not produce run cards")
	}
}

func TestRunCardPublisherNegativeCachesNonLarkOrigins(t *testing.T) {
	fx := newRunCardFixture(t)
	fx.store.mu.Lock()
	issue := fx.store.issues[fx.issueID]
	issue.OriginType = pgtype.Text{String: "quick_create", Valid: true}
	fx.store.issues[fx.issueID] = issue
	fx.store.mu.Unlock()

	fx.pub.handleEvent(fx.event(protocol.EventTaskQueued))
	fx.pub.handleEvent(fx.event(protocol.EventTaskRunning))

	if len(fx.client.sends) != 0 {
		t.Fatalf("non-lark origin must not send cards")
	}
	fx.store.mu.Lock()
	calls := fx.store.getIssueCalls
	fx.store.mu.Unlock()
	if calls != 1 {
		t.Fatalf("issue lookups = %d, want 1 (negative cache must absorb the second event)", calls)
	}
}

func TestRunCardPublisherThrottlesMessageEvents(t *testing.T) {
	fx := newRunCardFixture(t)
	fx.pub.handleEvent(fx.event(protocol.EventTaskQueued)) // send

	// First message right after the send: inside the min interval, so
	// it must arm a timer instead of patching.
	fx.now = fx.now.Add(1 * time.Second)
	fx.pub.handleEvent(events.Event{Type: protocol.EventTaskMessage, TaskID: fx.taskID})
	if len(fx.client.patches) != 0 {
		t.Fatalf("message inside the throttle window must not patch immediately")
	}
	if len(fx.timers) != 1 {
		t.Fatalf("timers armed = %d, want 1", len(fx.timers))
	}

	// A second message inside the window must not arm a second timer.
	fx.pub.handleEvent(events.Event{Type: protocol.EventTaskMessage, TaskID: fx.taskID})
	if len(fx.timers) != 1 {
		t.Fatalf("burst must coalesce into the single armed timer")
	}

	// Trailing edge fires: exactly one patch for the burst.
	fx.now = fx.now.Add(3 * time.Second)
	fx.timers[0].fn()
	if len(fx.client.patches) != 1 {
		t.Fatalf("patches after timer = %d, want 1", len(fx.client.patches))
	}

	// A message after the window patches immediately.
	fx.now = fx.now.Add(5 * time.Second)
	fx.pub.handleEvent(events.Event{Type: protocol.EventTaskMessage, TaskID: fx.taskID})
	if len(fx.client.patches) != 2 {
		t.Fatalf("patches = %d, want 2", len(fx.client.patches))
	}
}

func TestRunCardPublisherStatusEventBypassesThrottle(t *testing.T) {
	fx := newRunCardFixture(t)
	fx.pub.handleEvent(fx.event(protocol.EventTaskQueued))

	fx.now = fx.now.Add(200 * time.Millisecond)
	fx.setTaskStatus(t, "running")
	fx.pub.handleEvent(fx.event(protocol.EventTaskRunning))
	if len(fx.client.patches) != 1 {
		t.Fatalf("status transitions must patch immediately, got %d patches", len(fx.client.patches))
	}
}

func TestRunCardPublisherFinalizesOnTerminal(t *testing.T) {
	fx := newRunCardFixture(t)
	fx.pub.handleEvent(fx.event(protocol.EventTaskQueued))

	fx.now = fx.now.Add(90 * time.Second)
	fx.setTaskStatus(t, "cancelled")
	fx.pub.handleEvent(fx.event(protocol.EventTaskCancelled))

	if len(fx.client.patches) != 1 {
		t.Fatalf("patches = %d, want 1", len(fx.client.patches))
	}
	final := fx.client.patches[0].CardJSON
	if strings.Contains(final, runCardCancelAction) {
		t.Fatalf("terminal card must drop the terminate button")
	}
	if !strings.Contains(final, "已取消") {
		t.Fatalf("terminal card should show 已取消, got %s", final)
	}
	if len(fx.store.statusUpdates) != 1 || fx.store.statusUpdates[0].Status != string(CardStatusFinal) {
		t.Fatalf("outbound row must be finalized, got %+v", fx.store.statusUpdates)
	}
	fx.pub.mu.Lock()
	workers := len(fx.pub.workers)
	fx.pub.mu.Unlock()
	if workers != 0 {
		t.Fatalf("terminal task must drop its worker, %d left", workers)
	}
}

func TestRunCardPublisherRendersDetailFromMessages(t *testing.T) {
	fx := newRunCardFixture(t)
	fx.store.mu.Lock()
	for i := 1; i <= 12; i++ {
		fx.store.messages[fx.taskID] = append(fx.store.messages[fx.taskID], db.TaskMessage{
			Seq:       int32(i),
			Type:      "tool_use",
			Tool:      pgtype.Text{String: "exec_command", Valid: true},
			Input:     []byte(`{"command":"echo step"}`),
			CreatedAt: pgtype.Timestamptz{Time: fx.now, Valid: true},
		})
	}
	fx.store.mu.Unlock()

	fx.pub.handleEvent(fx.event(protocol.EventTaskQueued))
	if len(fx.client.sends) != 1 {
		t.Fatalf("sends = %d", len(fx.client.sends))
	}
	doc := parseCard(t, fx.client.sends[0].CardJSON)
	panel := findElement(t, doc, "collapsible_panel")
	if panel == nil {
		t.Fatalf("card must include detail panel when messages exist")
	}
	lines := panel["elements"].([]any)
	if len(lines) != defaultRunCardDetailLimit {
		t.Fatalf("detail lines = %d, want %d", len(lines), defaultRunCardDetailLimit)
	}
	// Newest first: the first line must carry the highest seq.
	first := lines[0].(map[string]any)["content"].(string)
	if !strings.Contains(first, "#12") {
		t.Fatalf("first detail line should be the newest event, got %q", first)
	}
	if !strings.Contains(first, "echo step") {
		t.Fatalf("detail line should include the command preview, got %q", first)
	}
}
