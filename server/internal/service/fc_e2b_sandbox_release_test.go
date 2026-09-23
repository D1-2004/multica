package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshschedule"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type terminalRecordingLauncher struct {
	terminal []db.AgentTaskQueue
}

func (l *terminalRecordingLauncher) LaunchTask(context.Context, db.AgentTaskQueue) error { return nil }
func (l *terminalRecordingLauncher) TaskTerminal(task db.AgentTaskQueue) {
	l.terminal = append(l.terminal, task)
}

func TestTaskTerminalObserverSeesOnlyTerminalEvents(t *testing.T) {
	fc := &terminalRecordingLauncher{}
	s := &TaskService{RuntimeLauncher: NewCloudSandboxLauncher(nil, fc, nil)}
	task := db.AgentTaskQueue{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}}
	// An empty route only gates realtime delivery; the runtime still hears it.
	for _, eventType := range []string{protocol.EventTaskQueued, protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled} {
		s.publishTaskEvent(eventType, humanRealtimeRoute{}, task)
	}
	if len(fc.terminal) != 3 || fc.terminal[0].ID != task.ID {
		t.Fatalf("terminal notifications = %d", len(fc.terminal))
	}
	// Terminal writes that only record metrics, such as agent archiving, notify too.
	s.captureTaskCompleted(context.Background(), task)
	if len(fc.terminal) != 4 {
		t.Fatalf("capture path notifications = %d", len(fc.terminal))
	}
}

func TestTaskTerminalRunsOneReleasePerTask(t *testing.T) {
	a, _ := dshLaunchPools(t)
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	l := &FCE2BLauncher{Pool: a, sleep: func(ctx context.Context, _ time.Duration) error {
		entered <- struct{}{}
		<-release
		return context.Canceled
	}}
	task := db.AgentTaskQueue{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, RuntimeID: pgtype.UUID{Bytes: uuid.New(), Valid: true}}
	l.TaskTerminal(task)
	<-entered
	// The event path and the metrics path both notify for one terminal write.
	l.TaskTerminal(task)
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, pending := fcE2BSandboxReleasePending.Load(util.UUIDToString(task.ID)); !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("release never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(entered) != 0 {
		t.Fatal("a duplicate notification started a second release")
	}
}

func TestSandboxOriginIsTheAppHost(t *testing.T) {
	if got := fcE2BSandboxOrigin(FCE2BConfig{DSHNativeAuthority: "https://pre-fde-workbench.dingtalk.com/"}); got != "pre-fde-workbench.dingtalk.com" {
		t.Fatalf("origin = %q", got)
	}
	if got := fcE2BSandboxOrigin(FCE2BConfig{}); got != "" {
		t.Fatalf("empty authority origin = %q", got)
	}
}

func TestDSHScopeIsSingleUseOnlyForOrdinaryUnscopedTasks(t *testing.T) {
	u := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
	key := dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	own := func(task db.AgentTaskQueue) uuid.UUID { return employeeFilesystemScopeID(dshExecutionScope(key, task)) }
	task := db.AgentTaskQueue{ID: u()}
	if !dshScopeIsSingleUse(key, task, own(task)) {
		t.Fatal("a run-only task owns its scope")
	}
	if dshScopeIsSingleUse(key, task, uuid.New()) {
		t.Fatal("a scope bound through another session may be shared")
	}
	scheduled := task
	scheduled.TriggerEvidenceKind = pgtype.Text{String: dshschedule.EvidenceKind, Valid: true}
	if dshScopeIsSingleUse(key, scheduled, own(scheduled)) {
		t.Fatal("scheduled work may continue an earlier conversation")
	}
	issue := task
	issue.IssueID = u()
	chat := task
	chat.ChatSessionID = u()
	if dshScopeIsSingleUse(key, issue, own(issue)) || dshScopeIsSingleUse(key, chat, own(chat)) {
		t.Fatal("issue and chat scopes are reused by later turns")
	}
}

func TestSandboxLifetimeRequestReportsGoneOnlyOutsideDelete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer server.Close()
	l := &FCE2BLauncher{Config: FCE2BConfig{APIURL: server.URL, APIKey: "test-key"}}
	ctx := context.Background()
	if _, err := l.sandboxLifetimeRequest(ctx, http.MethodDelete, "sbx-1", "", nil); err != nil {
		t.Fatalf("DELETE of an absent sandbox is success: %v", err)
	}
	if _, err := l.sandboxLifetimeRequest(ctx, http.MethodPost, "sbx-1", "/timeout", []byte("{}")); !errors.Is(err, errFCE2BSandboxGone) {
		t.Fatalf("POST 404 = %v", err)
	}
}

type releaseFCCall struct {
	method, path, body string
}

type releaseProvider struct {
	mu        sync.Mutex
	destroyed []string
}

func (p *releaseProvider) Create(context.Context, dshhost.Host) (string, error) {
	return "", errors.New("release must never create")
}
func (p *releaseProvider) Healthy(context.Context, string) error { return nil }
func (p *releaseProvider) DestroyAndConfirmAbsent(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.destroyed = append(p.destroyed, id)
	return nil
}
func (p *releaseProvider) FindCreated(context.Context, dshhost.Host) (string, error) {
	return "", dshhost.ErrPending
}

type releaseFixture struct {
	l        *FCE2BLauncher
	rt       db.AgentRuntime
	key      dshhost.Key
	pool     *pgxpool.Pool
	other    *pgxpool.Pool
	provider *releaseProvider
	mu       sync.Mutex
	calls    []releaseFCCall
	// onTimeout runs inside the provider's POST /timeout handler.
	onTimeout func(body string)
	// endAt is each sandbox's provider lifetime. Unset sandboxes were last
	// renewed ten minutes ago, before any fixture task started.
	endAt map[string]time.Time
}

func newReleaseFixture(t *testing.T) *releaseFixture {
	t.Helper()
	a, b := dshLaunchPools(t)
	ctx := context.Background()
	if _, err := a.Exec(ctx, `ALTER TABLE agent_task_queue ADD COLUMN runtime_id uuid, ADD COLUMN issue_id uuid, ADD COLUMN chat_session_id uuid;
 CREATE TABLE fc_e2b_sandbox_session (runtime_id uuid, sandbox_id text, sandbox_backend text DEFAULT 'aliyun_fc',
 status text DEFAULT 'running', expires_at timestamptz, updated_at timestamptz DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	f := &releaseFixture{pool: a, other: b, provider: &releaseProvider{}, key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, endAt: map[string]time.Time{}}
	if _, err := (dshhost.PostgresStore{DB: a}).BindStorage(ctx, f.key, dshhost.Storage{
		FileSystemID: "fs-test", SpaceID: uuid.NewString(), VolumeName: uuid.NewString(), AccessPointARN: "acs:nas:cn-beijing:123:accesspoint/ap-test", RoleARN: "role-test", VPCID: "vpc-test", SecurityGroupID: "sg-test", VSwitchIDs: []string{"vsw-test"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, `INSERT INTO workspace VALUES($1)`, f.key.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, `INSERT INTO agent(id,workspace_id) VALUES ($1,$2)`, f.key.AgentID, f.key.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/sandboxes/"), "/timeout")
		f.mu.Lock()
		f.calls = append(f.calls, releaseFCCall{r.Method, r.URL.Path, string(body)})
		hook := f.onTimeout
		endAt, renewed := f.endAt[id]
		if !renewed {
			endAt = time.Now().Add(4800*time.Second - 10*time.Minute)
		}
		var timeout struct {
			Timeout int64 `json:"timeout"`
		}
		if r.Method == http.MethodPost && json.Unmarshal(body, &timeout) == nil {
			f.endAt[id] = time.Now().Add(time.Duration(timeout.Timeout) * time.Second)
		}
		f.mu.Unlock()
		if strings.Contains(r.URL.Path, "expired") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"sandboxID": id, "state": "running", "endAt": endAt})
			return
		}
		if hook != nil && strings.HasSuffix(r.URL.Path, "/timeout") {
			hook(string(body))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	f.rt = db.AgentRuntime{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, WorkspaceID: pgtype.UUID{Bytes: f.key.WorkspaceID, Valid: true}}
	f.l = &FCE2BLauncher{Pool: a, Config: FCE2BConfig{Enabled: true, APIURL: server.URL, APIKey: "test-key", DSHNativeAuthority: "https://pre.multica.test"},
		dshProvider: func(dshhost.Storage) (dshhost.Provider, error) { return f.provider, nil }}
	return f
}

// task inserts a task and one start attempt per sandbox it resolved.
func (f *releaseFixture) task(t *testing.T, issue, chat pgtype.UUID, status string, sandboxes ...string) db.AgentTaskQueue {
	t.Helper()
	ctx := context.Background()
	task := db.AgentTaskQueue{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, AgentID: pgtype.UUID{Bytes: f.key.AgentID, Valid: true},
		RuntimeID: f.rt.ID, IssueID: issue, ChatSessionID: chat, Status: status,
		StartedAt: pgtype.Timestamptz{Time: time.Now().Add(-5 * time.Minute), Valid: true}}
	if _, err := f.pool.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,status,runtime_id,issue_id,chat_session_id) VALUES ($1,$2,$3,$4,$5,$6)`,
		task.ID, task.AgentID, status, task.RuntimeID, issue, chat); err != nil {
		t.Fatal(err)
	}
	for _, sandbox := range sandboxes {
		if _, err := f.pool.Exec(ctx, `INSERT INTO agent_task_runtime_start_attempt(task_id,sandbox_id,status) VALUES ($1,$2,'claimed')`, task.ID, sandbox); err != nil {
			t.Fatal(err)
		}
	}
	return task
}

// runningScope puts the task's own execution scope on sandboxID through the
// real store transitions.
func (f *releaseFixture) runningScope(t *testing.T, task db.AgentTaskQueue, sandboxID string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	scopeID := employeeFilesystemScopeID(dshExecutionScope(f.key, task))
	store := dshhost.FilesystemSandboxStore{DB: f.pool, ScopeID: scopeID}
	h, err := store.Bind(ctx, f.key)
	if err != nil {
		t.Fatal(err)
	}
	if h, err = store.BeginCreate(ctx, f.key, h.Generation, uuid.New(), "template-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteCreate(ctx, h, sandboxID); err != nil {
		t.Fatal(err)
	}
	return scopeID
}

func (f *releaseFixture) release(t *testing.T, task db.AgentTaskQueue, sandboxID string) (fcE2BSandboxLifecycleAction, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	ids, err := taskSandboxIDs(ctx, conn, task.ID)
	if err != nil || len(ids) != 1 || ids[0] != sandboxID {
		t.Fatalf("task sandboxes = %v err=%v", ids, err)
	}
	action, reason, _, err := f.l.releaseTaskSandbox(ctx, conn, task, f.rt, sandboxID)
	if err != nil {
		t.Fatal(err)
	}
	return action, reason
}

func (f *releaseFixture) scopeState(t *testing.T, scopeID uuid.UUID) (string, string) {
	t.Helper()
	var state, sandbox string
	if err := f.pool.QueryRow(context.Background(), `SELECT state,sandbox_id FROM employee_filesystem_sandbox WHERE scope_id=$1`, scopeID).Scan(&state, &sandbox); err != nil {
		t.Fatal(err)
	}
	return state, sandbox
}

func (f *releaseFixture) fcCalls() []releaseFCCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]releaseFCCall(nil), f.calls...)
}

func TestSandboxReleaseRetiresSingleUseEmployeeScope(t *testing.T) {
	f := newReleaseFixture(t)
	task := f.task(t, pgtype.UUID{}, pgtype.UUID{}, "completed", "sbx-run-only")
	scopeID := f.runningScope(t, task, "sbx-run-only")
	if action, reason := f.release(t, task, "sbx-run-only"); action != fcE2BSandboxReleased || reason != "single_use_scope" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
	if state, sandbox := f.scopeState(t, scopeID); state != "offline" || sandbox != "" {
		t.Fatalf("scope state=%s sandbox=%s", state, sandbox)
	}
	if len(f.provider.destroyed) != 1 || f.provider.destroyed[0] != "sbx-run-only" {
		t.Fatalf("destroyed = %v", f.provider.destroyed)
	}
	if calls := f.fcCalls(); len(calls) != 0 {
		t.Fatalf("employee sandboxes retire through the scope store, not a bare DELETE: %v", calls)
	}
}

func TestSandboxReleaseOnlyTrimsSingleUseDSHHost(t *testing.T) {
	f := newReleaseFixture(t)
	// Native entry may have renewed this most recent scope and still be
	// starting its gateway; nothing records that, so the host is not retired.
	f.rt.Provider = "dsh"
	task := f.task(t, pgtype.UUID{}, pgtype.UUID{}, "completed", "sbx-dsh-run-only")
	scopeID := f.runningScope(t, task, "sbx-dsh-run-only")
	if action, reason := f.release(t, task, "sbx-dsh-run-only"); action != fcE2BSandboxIdleTrimmed || reason != "idle_window" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
	if state, _ := f.scopeState(t, scopeID); state != "running" || len(f.provider.destroyed) != 0 {
		t.Fatalf("retired a DSH host: state=%s", state)
	}
}

func TestSandboxReleaseKeepsEmployeeSandboxAnotherTaskUses(t *testing.T) {
	f := newReleaseFixture(t)
	task := f.task(t, pgtype.UUID{}, pgtype.UUID{}, "completed", "sbx-shared")
	scopeID := f.runningScope(t, task, "sbx-shared")
	f.task(t, pgtype.UUID{}, pgtype.UUID{}, "running", "sbx-shared")
	if action, reason := f.release(t, task, "sbx-shared"); action != fcE2BSandboxRetained || reason != "in_use" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
	if state, _ := f.scopeState(t, scopeID); state != "running" || len(f.provider.destroyed) != 0 || len(f.fcCalls()) != 0 {
		t.Fatalf("busy sandbox touched: state=%s destroyed=%v", state, f.provider.destroyed)
	}
}

func TestSandboxReleaseKeepsEmployeeSandboxWithNativeGrant(t *testing.T) {
	f := newReleaseFixture(t)
	task := f.task(t, pgtype.UUID{}, pgtype.UUID{}, "completed", "sbx-native")
	scopeID := f.runningScope(t, task, "sbx-native")
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO dsh_native_access (id,workspace_id,agent_id,user_id,generation,sandbox_id,kind,token_hash,expires_at)
 VALUES ($1,$2,$3,$4,1,'sbx-native','session',repeat('a',64),now()+interval '10 minutes')`, uuid.New(), f.key.WorkspaceID, f.key.AgentID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if action, reason := f.release(t, task, "sbx-native"); action != fcE2BSandboxRetained || reason != "native_grant" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
	if state, _ := f.scopeState(t, scopeID); state != "running" || len(f.provider.destroyed) != 0 {
		t.Fatalf("browser session lost its sandbox: state=%s", state)
	}
}

func TestSandboxReleaseTrimsReusableEmployeeScope(t *testing.T) {
	f := newReleaseFixture(t)
	task := f.task(t, pgtype.UUID{Bytes: uuid.New(), Valid: true}, pgtype.UUID{}, "completed", "sbx-issue")
	scopeID := f.runningScope(t, task, "sbx-issue")
	if action, reason := f.release(t, task, "sbx-issue"); action != fcE2BSandboxIdleTrimmed || reason != "idle_window" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
	calls := f.fcCalls()
	if len(calls) != 1 || calls[0] != (releaseFCCall{http.MethodPost, "/sandboxes/sbx-issue/timeout", `{"timeout":600}`}) {
		t.Fatalf("calls = %v", calls)
	}
	if state, sandbox := f.scopeState(t, scopeID); state != "running" || sandbox != "sbx-issue" || len(f.provider.destroyed) != 0 {
		t.Fatal("a reusable scope must stay adoptable during its idle window")
	}
}

func TestSandboxReleaseRestoresLifetimeWhenANativeGrantLandsDuringTrim(t *testing.T) {
	f := newReleaseFixture(t)
	task := f.task(t, pgtype.UUID{Bytes: uuid.New(), Valid: true}, pgtype.UUID{}, "completed", "sbx-racing")
	f.runningScope(t, task, "sbx-racing")
	// A browser entry issued between the grant check and the trim: its host was
	// renewed before this release took the scope lock.
	f.onTimeout = func(body string) {
		if body != `{"timeout":600}` {
			return
		}
		if _, err := f.other.Exec(context.Background(), `INSERT INTO dsh_native_access (id,workspace_id,agent_id,user_id,generation,sandbox_id,kind,token_hash,expires_at)
 VALUES ($1,$2,$3,$4,1,'sbx-racing','entry',repeat('b',64),now()+interval '1 minute')`, uuid.New(), f.key.WorkspaceID, f.key.AgentID, uuid.New()); err != nil {
			t.Error(err)
		}
	}
	if action, reason := f.release(t, task, "sbx-racing"); action != fcE2BSandboxRetained || reason != "native_grant" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
	calls := f.fcCalls()
	last := calls[len(calls)-2]
	if last.method != http.MethodPost || last.body != `{"timeout":4800}` {
		t.Fatalf("lifetime was not restored for the browser session: %v", calls)
	}
}

func TestSandboxReleaseSkipsEmployeeScopeHeldByALaunch(t *testing.T) {
	f := newReleaseFixture(t)
	task := f.task(t, pgtype.UUID{}, pgtype.UUID{}, "completed", "sbx-admitting")
	scopeID := f.runningScope(t, task, "sbx-admitting")
	ctx := context.Background()
	conn, err := f.other.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// The launch path's own lock: this proves both sides compute the same key.
	unlock, err := lockEmployeeFilesystemScope(ctx, conn, f.key, scopeID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if action, reason := f.release(t, task, "sbx-admitting"); action != fcE2BSandboxRetained || reason != "scope_admitting" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
	if len(f.provider.destroyed) != 0 {
		t.Fatal("released a sandbox a launch was adopting")
	}
}

func TestSandboxReleaseDeletesUnscopedEphemeralSandbox(t *testing.T) {
	f := newReleaseFixture(t)
	task := f.task(t, pgtype.UUID{}, pgtype.UUID{}, "failed", "sbx-plain")
	if action, reason := f.release(t, task, "sbx-plain"); action != fcE2BSandboxReleased || reason != "single_use_scope" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
	if calls := f.fcCalls(); len(calls) != 1 || calls[0].method != http.MethodDelete || calls[0].path != "/sandboxes/sbx-plain" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestSandboxReleaseWaitsForUnstartedTaskOnSameIssue(t *testing.T) {
	f := newReleaseFixture(t)
	issue := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	task := f.task(t, issue, pgtype.UUID{}, "completed", "sbx-issue-plain")
	// Renewed under the scope lock but not yet recorded on its attempt.
	f.task(t, issue, pgtype.UUID{}, "queued")
	if action, reason := f.release(t, task, "sbx-issue-plain"); action != fcE2BSandboxRetained || reason != "in_use" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
	if calls := f.fcCalls(); len(calls) != 0 {
		t.Fatalf("shortened a sandbox a queued turn may be using: %v", calls)
	}
}

func TestSandboxReleaseSkipsEphemeralScopeHeldByALaunch(t *testing.T) {
	f := newReleaseFixture(t)
	chat := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	task := f.task(t, pgtype.UUID{}, chat, "completed", "sbx-chat-locked")
	launch := *f.l
	launch.Pool = f.other
	scope, _ := fcE2BScopeForTask(task)
	unlock, err := launch.lockSandboxScope(context.Background(), f.rt, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if action, reason := f.release(t, task, "sbx-chat-locked"); action != fcE2BSandboxRetained || reason != "scope_admitting" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
}

func TestSandboxReleaseTrimsEphemeralSandboxAndItsReuseCache(t *testing.T) {
	f := newReleaseFixture(t)
	chat := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	task := f.task(t, pgtype.UUID{}, chat, "completed", "sbx-chat")
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO fc_e2b_sandbox_session(runtime_id,sandbox_id,expires_at) VALUES ($1,'sbx-chat',now()+interval '70 minutes')`, f.rt.ID); err != nil {
		t.Fatal(err)
	}
	if action, reason := f.release(t, task, "sbx-chat"); action != fcE2BSandboxIdleTrimmed || reason != "idle_window" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
	if calls := f.fcCalls(); len(calls) != 1 || calls[0] != (releaseFCCall{http.MethodPost, "/sandboxes/sbx-chat/timeout", `{"timeout":600}`}) {
		t.Fatalf("calls = %v", calls)
	}
	var withinWindow bool
	if err := f.pool.QueryRow(ctx, `SELECT expires_at <= now()+interval '11 minutes' FROM fc_e2b_sandbox_session WHERE sandbox_id='sbx-chat'`).Scan(&withinWindow); err != nil || !withinWindow {
		t.Fatalf("reuse cache still promises the old lifetime: %v", err)
	}
}

func TestSandboxReleaseTreatsExpiredSandboxAsReleased(t *testing.T) {
	f := newReleaseFixture(t)
	task := f.task(t, pgtype.UUID{Bytes: uuid.New(), Valid: true}, pgtype.UUID{}, "cancelled", "sbx-expired")
	if action, reason := f.release(t, task, "sbx-expired"); action != fcE2BSandboxReleased || reason != "already_gone" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
}
