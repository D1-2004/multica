package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/util"
)

// --- fixture ---------------------------------------------------------------

type watchdogClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *watchdogClock) Now() time.Time                     { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *watchdogClock) Set(t time.Time)                    { c.mu.Lock(); c.now = t; c.mu.Unlock() }
func (c *watchdogClock) At(d time.Duration, base time.Time) { c.Set(base.Add(d)) }

type watchdogTargets struct {
	mu        sync.Mutex
	hold      string
	cid       string
	principal string
}

func (r *watchdogTargets) set(hold, cid string) {
	r.mu.Lock()
	r.hold, r.cid = hold, cid
	r.mu.Unlock()
}

func (r *watchdogTargets) ResolveEmployeeWatchdogTarget(_ context.Context, _ pgx.Tx, ref EmployeeWatchdogNoticeRef) (EmployeeWatchdogTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hold != "" {
		return EmployeeWatchdogTarget{}, &EmployeeWatchdogHold{Reason: r.hold}
	}
	cid := r.cid
	if cid == "" {
		cid = "cid-watchdog-" + ref.Scope.Scene.SceneID
	}
	return EmployeeWatchdogTarget{Input: dingtalkresponse.ActionInput{WorkspaceID: ref.Scope.WorkspaceID, AgentID: ref.Scope.AgentID, DWSUID: "dws-watchdog", DWSOrgID: ref.Scope.TenantOrgID, SceneID: ref.Scope.Scene.SceneID, ConversationID: cid, IsGroup: true},
		HistoryPrincipalID: r.principal}, nil
}

type watchdogProvider struct {
	ws      string
	mu      sync.Mutex
	sends   map[string]int
	texts   map[string]string
	sendErr error
}

func (p *watchdogProvider) Send(_ context.Context, in dingtalkresponse.ActionInput, key string) (dwsclient.SendResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sends == nil {
		p.sends, p.texts = map[string]int{}, map[string]string{}
	}
	if in.WorkspaceID != p.ws {
		// A leftover action from another test in this database.
		return dwsclient.SendResult{OpenTaskID: "open-" + key}, nil
	}
	p.sends[in.SceneNoticeID]++
	p.texts[in.SceneNoticeID] = in.Text
	if p.sendErr != nil {
		return dwsclient.SendResult{}, p.sendErr
	}
	return dwsclient.SendResult{OpenTaskID: "open-" + key}, nil
}

func (p *watchdogProvider) Query(_ context.Context, in dingtalkresponse.ActionInput, _ string) (dwsclient.SendStatus, error) {
	return dwsclient.SendStatus{State: "delivered", OpenConversationID: in.ConversationID, OpenMessageID: "msg-" + in.SceneNoticeID}, nil
}

func (p *watchdogProvider) sent(noticeID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sends[noticeID]
}

func (p *watchdogProvider) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.sends {
		n += c
	}
	return n
}

type watchdogFixture struct {
	directFixture
	t        *testing.T
	ctx      context.Context
	ws       string
	agent    string
	task     employeetask.Task
	queueID  string
	runID    string
	base     time.Time
	clock    *watchdogClock
	targets  *watchdogTargets
	provider *watchdogProvider
	outbox   *dingtalkresponse.Service
	w        *EmployeeWatchdog
}

// newWatchdogFixture builds a real Employee Direct Task with a claimed queue
// execution that started at base. The watchdog is enabled an hour earlier.
func newWatchdogFixture(t *testing.T) *watchdogFixture {
	t.Helper()
	d := directDatabase(t)
	ctx := context.Background()
	f := &watchdogFixture{directFixture: d, t: t, ctx: ctx, ws: d.request.Task.Scope.WorkspaceID, agent: d.request.Task.Scope.AgentID, task: d.request.Task}
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM employee_watchdog_notice WHERE workspace_id=$1::uuid AND $2::uuid IS NOT NULL`, `DELETE FROM employee_watchdog_episode WHERE workspace_id=$1::uuid AND $2::uuid IS NOT NULL`, `DELETE FROM employee_watchdog_cursor WHERE workspace_id=$1::uuid AND $2::uuid IS NOT NULL`, `DELETE FROM response_action WHERE workspace_id=$1::uuid AND $2::uuid IS NOT NULL`, `DELETE FROM employee_host_notice WHERE workspace_id=$1::uuid AND $2::uuid IS NOT NULL`, `DELETE FROM agent_task_runtime_start_attempt WHERE $1::uuid IS NOT NULL AND task_id IN (SELECT id FROM agent_task_queue WHERE agent_id=$2::uuid)`} {
			if _, err := d.pool.Exec(ctx, q, f.ws, f.agent); err != nil {
				t.Error(err)
			}
		}
	})
	f.exec(`UPDATE agent SET coordination_mode='employee' WHERE id=$1::uuid`, f.agent)
	r, err := d.service.EnqueueDirectTask(ctx, d.request)
	if err != nil {
		t.Fatal(err)
	}
	f.queueID, f.runID = util.UUIDToString(r.Task.ID), r.Run.ID
	if err := d.pool.QueryRow(ctx, `SELECT date_trunc('second', now())`).Scan(&f.base); err != nil {
		t.Fatal(err)
	}
	f.startQueue(f.queueID, f.runID, f.base)
	f.exec(`INSERT INTO employee_watchdog_cursor(workspace_id,agent_id,enabled_at) VALUES($1::uuid,$2::uuid,$3)`, f.ws, f.agent, f.base.Add(-time.Hour))
	f.clock = &watchdogClock{now: f.base}
	f.targets = &watchdogTargets{principal: util.UUIDToString(d.request.PrincipalID)}
	f.provider = &watchdogProvider{ws: f.ws}
	f.outbox = dingtalkresponse.NewService(d.pool, f.provider, nil)
	f.w = f.watchdog(d.pool, f.outbox)
	return f
}

func (f *watchdogFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

// startQueue marks an execution as claimed at the given Host time, owned by
// the Employee delivery path, as the Direct claim does.
func (f *watchdogFixture) startQueue(queueID, runID string, at time.Time) {
	f.exec(`UPDATE agent_task_queue SET status='running',dispatched_at=$2,started_at=$2,context=context||'{"employee_delivery_owner":"employee"}'::jsonb WHERE id=$1::uuid`, queueID, at)
	f.exec(`UPDATE employee_task_run SET created_at=$2 WHERE id=$1::uuid`, runID, at.Add(-time.Second))
}

func (f *watchdogFixture) watchdog(db EmployeeWatchdogDB, outbox EmployeeWatchdogOutbox) *EmployeeWatchdog {
	return &EmployeeWatchdog{DB: db, Config: DefaultEmployeeWatchdogConfig(), Targets: f.targets, Outbox: outbox, Ready: func(context.Context) error { return nil }, Now: f.clock.Now}
}

func (f *watchdogFixture) scan(w *EmployeeWatchdog) EmployeeWatchdogScanResult {
	f.t.Helper()
	if w == nil {
		w = f.w
	}
	got, err := w.Scan(f.ctx, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

func (f *watchdogFixture) at(d time.Duration) { f.clock.At(d, f.base) }

type watchdogEpisodeRow struct {
	ID, Kind, Reason, Boundary, State, CloseReason, WatermarkSource, WatermarkRef, RunID string
	Since                                                                                time.Time
	Watermark                                                                            *time.Time
}

func (f *watchdogFixture) episodes(taskID string) []watchdogEpisodeRow {
	f.t.Helper()
	rows, err := f.pool.Query(f.ctx, `SELECT id::text,state_kind,state_reason,boundary_key,state,close_reason,watermark_source,watermark_ref,COALESCE(run_id::text,''),since_at,watermark_at FROM employee_watchdog_episode WHERE task_id=$1::uuid ORDER BY created_at,since_at`, taskID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []watchdogEpisodeRow
	for rows.Next() {
		var e watchdogEpisodeRow
		if err := rows.Scan(&e.ID, &e.Kind, &e.Reason, &e.Boundary, &e.State, &e.CloseReason, &e.WatermarkSource, &e.WatermarkRef, &e.RunID, &e.Since, &e.Watermark); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

type watchdogNoticeRow struct {
	ID, EpisodeID, Kind, State, Reason, Body, ActionID string
}

func (f *watchdogFixture) notices(taskID string) []watchdogNoticeRow {
	f.t.Helper()
	rows, err := f.pool.Query(f.ctx, `SELECT id::text,episode_id::text,kind,state,reason,body,COALESCE(action_id,'') FROM employee_watchdog_notice WHERE task_id=$1::uuid ORDER BY created_at`, taskID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []watchdogNoticeRow
	for rows.Next() {
		var n watchdogNoticeRow
		if err := rows.Scan(&n.ID, &n.EpisodeID, &n.Kind, &n.State, &n.Reason, &n.Body, &n.ActionID); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func (f *watchdogFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *watchdogFixture) actions() int {
	return f.count(`SELECT count(*) FROM response_action WHERE workspace_id=$1::uuid`, f.ws)
}

func (f *watchdogFixture) actionState(id string) (string, string) {
	f.t.Helper()
	var state, code string
	if err := f.pool.QueryRow(f.ctx, `SELECT state,error_code FROM response_action WHERE id=$1`, id).Scan(&state, &code); err != nil {
		f.t.Fatal(err)
	}
	return state, code
}

// runOutbox runs the real response outbox worker with the watchdog as its
// BeforeSend fence until the action reaches one of the wanted states.
func (f *watchdogFixture) runOutbox(pool *pgxpool.Pool, w *EmployeeWatchdog, actionID string, want ...string) (string, string) {
	f.t.Helper()
	svc := dingtalkresponse.NewService(pool, f.provider, nil)
	svc.BeforeSend = func(ctx context.Context, in dingtalkresponse.ActionInput) error {
		if handled, err := w.BeforeSend(ctx, in); handled {
			return err
		}
		return nil
	}
	ctx, cancel := context.WithCancel(f.ctx)
	go svc.Run(ctx)
	defer func() {
		cancel()
		if !svc.WaitWithTimeout(context.Background(), 5*time.Second) {
			f.t.Error("outbox worker did not stop")
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state, code := f.actionState(actionID)
		for _, s := range want {
			if state == s {
				return state, code
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	state, code := f.actionState(actionID)
	f.t.Fatalf("action %s stayed %s/%s, want %v", actionID, state, code, want)
	return "", ""
}

// addMessage records a daemon-reported task message at a Host time.
func (f *watchdogFixture) addMessage(queueID, kind string, at time.Time) {
	f.exec(`INSERT INTO task_message(task_id,seq,type,content,created_at) VALUES($1::uuid,COALESCE((SELECT max(seq) FROM task_message WHERE task_id=$1::uuid),0)+1,$2,'x',$3)`, queueID, kind, at)
}

func (f *watchdogFixture) stopTask() {
	f.t.Helper()
	store := employeetask.NewStore(f.pool)
	task, err := store.Get(f.ctx, f.task.Scope, f.task.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, _, err := store.Stop(f.ctx, task.Scope, task.ID, employeetask.StopParams{Source: employeetask.Source{Namespace: "test", Key: "stop-" + uuid.NewString()}, ActorRef: task.RequesterRef, Body: "停下", RunID: f.runID, QueueTaskID: f.queueID, ExpectedVersion: task.Version}); err != nil {
		f.t.Fatal(err)
	}
	// The Host cancels the exact queue; the daemon has not acknowledged exit.
	f.exec(`UPDATE agent_task_queue SET status='cancelled',completed_at=now(),context=context||'{"process_stop_pending":true}'::jsonb WHERE id=$1::uuid`, f.queueID)
	f.exec(`UPDATE employee_task_run SET state='cancelled',finished_at=now() WHERE id=$1::uuid`, f.runID)
}

func onlyNotice(t *testing.T, rows []watchdogNoticeRow) watchdogNoticeRow {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("notices=%d, want exactly one: %+v", len(rows), rows)
	}
	return rows[0]
}

// --- tests -----------------------------------------------------------------

func TestWatchdogIgnoresHeartbeatBookkeepingAndOwnNotice(t *testing.T) {
	f := newWatchdogFixture(t)
	// Only system activity follows the claim.
	f.addMessage(f.queueID, "status", f.base.Add(5*time.Minute))
	f.addMessage(f.queueID, "log", f.base.Add(10*time.Minute))
	f.addMessage(f.queueID, "error", f.base.Add(11*time.Minute))
	f.exec(`UPDATE agent_runtime SET last_seen_at=$2 WHERE workspace_id=$1::uuid`, f.ws, f.base.Add(14*time.Minute))
	f.exec(`UPDATE employee_task SET version=version+1,updated_at=$2 WHERE id=$1::uuid`, f.task.ID, f.base.Add(14*time.Minute))
	f.exec(`INSERT INTO employee_task_entry(workspace_id,agent_id,tenant_org_id,task_id,seq,kind,source_namespace,source_key,goal_revision,run_id,payload,created_at)
 SELECT workspace_id,agent_id,tenant_org_id,id,last_entry_seq+1,'writer_fenced','test','fence',goal_revision,$2::uuid,'{}'::jsonb,$3 FROM employee_task WHERE id=$1::uuid`, f.task.ID, f.runID, f.base.Add(13*time.Minute))
	f.exec(`UPDATE employee_task SET last_entry_seq=last_entry_seq+1 WHERE id=$1::uuid`, f.task.ID)
	// Another Host notice in the same scene is not execution progress either.
	if _, err := f.outbox.EnqueueSceneNotice(f.ctx, f.pool, dingtalkresponse.ActionInput{WorkspaceID: f.ws, AgentID: f.agent, DWSUID: "u", DWSOrgID: f.task.Scope.TenantOrgID, SceneID: f.task.Scope.Scene.SceneID, ConversationID: "cid", IsGroup: true, Text: "场域配置已更新"}, uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	f.at(14 * time.Minute)
	if got := f.scan(nil); got.Opened != 0 {
		t.Fatal("opened before the threshold", got)
	}
	f.at(16 * time.Minute)
	if got := f.scan(nil); got.Opened != 1 || got.Enqueued != 1 {
		t.Fatal("system-only activity hid a stall", got)
	}
	episodes := f.episodes(f.task.ID)
	if len(episodes) != 1 || episodes[0].Kind != "execution_running" || episodes[0].State != "open" || !episodes[0].Since.Equal(f.base) || episodes[0].Watermark != nil || episodes[0].RunID != f.runID {
		t.Fatalf("episode=%+v", episodes)
	}
	n := onlyNotice(t, f.notices(f.task.ID))
	if n.State != "enqueued" || n.ActionID == "" || !strings.Contains(n.Body, "还在执行") || !strings.Contains(n.Body, "16 分钟") {
		t.Fatalf("notice=%+v", n)
	}

	// More heartbeats, logs, the watchdog's own queued notice and its own
	// bookkeeping must neither clear the episode nor create a second notice.
	f.addMessage(f.queueID, "status", f.base.Add(20*time.Minute))
	f.exec(`UPDATE agent_runtime SET last_seen_at=$2 WHERE workspace_id=$1::uuid`, f.ws, f.base.Add(24*time.Minute))
	f.at(25 * time.Minute)
	if got := f.scan(nil); got.Cleared != 0 || got.Opened != 0 || got.Enqueued != 0 {
		t.Fatal("system activity changed the episode", got)
	}
	if e := f.episodes(f.task.ID); len(e) != 1 || e[0].State != "open" {
		t.Fatalf("episode changed: %+v", e)
	}
	onlyNotice(t, f.notices(f.task.ID))
}

func TestWatchdogRealProgressClearsEpisodeOlderEventsDoNot(t *testing.T) {
	f := newWatchdogFixture(t)
	f.at(16 * time.Minute)
	f.scan(nil)
	if e := f.episodes(f.task.ID); len(e) != 1 || e[0].State != "open" {
		t.Fatalf("precondition: %+v", e)
	}
	actions := f.actions()

	// Output older than the silence start (reported late, before the claim)
	// does not clear the episode.
	f.addMessage(f.queueID, "text", f.base.Add(-time.Minute))
	f.at(17 * time.Minute)
	if got := f.scan(nil); got.Cleared != 0 {
		t.Fatal("an older event cleared the episode", got)
	}

	// A real tool result clears it silently: no "recovered" message.
	f.addMessage(f.queueID, "tool_result", f.base.Add(17*time.Minute))
	f.at(17*time.Minute + 30*time.Second)
	if got := f.scan(nil); got.Cleared != 1 || got.Enqueued != 0 {
		t.Fatal("fresh progress did not clear the episode", got)
	}
	episodes := f.episodes(f.task.ID)
	if len(episodes) != 1 || episodes[0].State != "cleared" || episodes[0].CloseReason != "progress" || episodes[0].WatermarkSource != "task_message" || episodes[0].Watermark == nil || !episodes[0].Watermark.Equal(f.base.Add(17*time.Minute)) {
		t.Fatalf("cleared episode=%+v", episodes)
	}
	if f.actions() != actions {
		t.Fatal("clearing sent a message")
	}

	// A late row that is older than the clearing progress never moves the
	// watermark back: the next independent stall starts at 17m, not 16m30s.
	f.addMessage(f.queueID, "text", f.base.Add(16*time.Minute+30*time.Second))
	f.at(31 * time.Minute)
	if got := f.scan(nil); got.Opened != 0 {
		t.Fatal("silence was measured from an older event", got)
	}
	f.at(32*time.Minute + time.Second)
	if got := f.scan(nil); got.Opened != 1 || got.Enqueued != 1 {
		t.Fatal("next independent stall did not open", got)
	}
	episodes = f.episodes(f.task.ID)
	if len(episodes) != 2 || episodes[1].State != "open" || !episodes[1].Since.Equal(f.base.Add(17*time.Minute)) || episodes[1].Watermark == nil || !episodes[1].Watermark.Equal(f.base.Add(17*time.Minute)) {
		t.Fatalf("second episode=%+v", episodes)
	}
	if len(f.notices(f.task.ID)) != 2 {
		t.Fatal("independent stall should have its own notice")
	}
}

func TestWatchdogConcurrentScanRestartCreatesOneNotice(t *testing.T) {
	f := newWatchdogFixture(t)
	f.at(16 * time.Minute)
	pools := make([]*pgxpool.Pool, 2)
	for i := range pools {
		pool, err := pgxpool.NewWithConfig(f.ctx, f.pool.Config().Copy())
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		pools[i] = pool
	}
	var wg sync.WaitGroup
	var opened, enqueued atomic.Int32
	errs := make(chan error, 8)
	for i := range 6 {
		pool := pools[i%2]
		w := f.watchdog(pool, dingtalkresponse.NewService(pool, f.provider, nil))
		wg.Go(func() {
			got, err := w.Scan(f.ctx, 100)
			if err != nil {
				errs <- err
				return
			}
			opened.Add(int32(got.Opened))
			enqueued.Add(int32(got.Enqueued))
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if opened.Load() != 1 || enqueued.Load() != 1 {
		t.Fatalf("opened=%d enqueued=%d across scanners", opened.Load(), enqueued.Load())
	}
	// A restarted process with no local state finds the same committed intent.
	restarted, err := pgxpool.NewWithConfig(f.ctx, f.pool.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	w := f.watchdog(restarted, dingtalkresponse.NewService(restarted, f.provider, nil))
	f.at(18 * time.Minute)
	if got := f.scan(w); got.Opened != 0 || got.Enqueued != 0 {
		t.Fatal("restart created another notice", got)
	}
	n := onlyNotice(t, f.notices(f.task.ID))
	if f.actions() != 1 || len(f.episodes(f.task.ID)) != 1 {
		t.Fatal("duplicate episode or action")
	}
	f.runOutbox(restarted, w, n.ActionID, "delivered")
	if f.provider.sent(n.ID) != 1 || f.provider.total() != 1 {
		t.Fatal("notice was not sent exactly once", f.provider.sent(n.ID), f.provider.total())
	}
}

type fixedWaitReader struct {
	mu   sync.Mutex
	wait *EmployeeTaskWait
}

func (r *fixedWaitReader) ReadEmployeeTaskWait(context.Context, pgx.Tx, employeetask.Task) (EmployeeTaskWait, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wait == nil {
		return EmployeeTaskWait{}, false, nil
	}
	return *r.wait, true, nil
}

func TestWatchdogWaitingAndStoppingAreNotExecutionStalls(t *testing.T) {
	t.Run("waiting_inputs", func(t *testing.T) {
		f := newWatchdogFixture(t)
		// A participant answer stored before the wait; it must never be quoted.
		store := employeetask.NewStore(f.pool)
		task, err := store.Get(f.ctx, f.task.Scope, f.task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = store.AppendInput(f.ctx, task.Scope, task.ID, employeetask.InputParams{Source: employeetask.Source{Namespace: "test", Key: "answer"}, ActorRef: task.RequesterRef, Body: "SENTINEL-OTHER-PERSON-ANSWER", ExpectedVersion: task.Version}); err != nil {
			t.Fatal(err)
		}
		f.exec(`UPDATE employee_task_entry SET created_at=$2 WHERE task_id=$1::uuid AND kind='input'`, f.task.ID, f.base.Add(-time.Minute))
		waits := &fixedWaitReader{wait: &EmployeeTaskWait{Kind: EmployeeTaskWaitInputs, Key: "collection:c1:r1", Since: f.base, Expected: 3, Received: 2}}
		f.w.Waits = waits
		f.at(20 * time.Minute)
		if got := f.scan(nil); got.Opened != 0 {
			t.Fatal("a wait for colleagues was reported as an execution stall", got, f.notices(f.task.ID))
		}
		f.at(61 * time.Minute)
		if got := f.scan(nil); got.Opened != 1 || got.Enqueued != 1 {
			t.Fatal("long input wait did not open", got)
		}
		n := onlyNotice(t, f.notices(f.task.ID))
		if n.Kind != "waiting_inputs" || !strings.Contains(n.Body, "2/3") || strings.Contains(n.Body, "还在执行") || strings.Contains(n.Body, "SENTINEL") {
			t.Fatalf("waiting notice=%+v", n)
		}
		// An accepted answer is progress for the wait and clears it silently.
		waits.mu.Lock()
		waits.wait.Received, waits.wait.Progress = 3, EmployeeActivityWatermark{At: f.base.Add(62 * time.Minute), Source: EmployeeActivityTaskInput, Ref: "answer-3"}
		waits.mu.Unlock()
		f.at(63 * time.Minute)
		if got := f.scan(nil); got.Cleared != 1 || got.Enqueued != 0 {
			t.Fatal("accepted input did not clear the wait episode", got)
		}
	})
	t.Run("stopping", func(t *testing.T) {
		f := newWatchdogFixture(t)
		f.w.Config.StoppingUnconfirmedSeconds = 1800
		f.at(16 * time.Minute)
		f.scan(nil)
		running := onlyNotice(t, f.notices(f.task.ID))
		f.stopTask()
		f.exec(`UPDATE employee_task_entry SET created_at=$2 WHERE task_id=$1::uuid AND payload->>'operation'='stop'`, f.task.ID, f.base.Add(17*time.Minute))
		f.at(20 * time.Minute)
		if got := f.scan(nil); got.Closed != 1 || got.Opened != 0 {
			t.Fatal("stop did not supersede the running episode without a new stall", got)
		}
		if e := f.episodes(f.task.ID); e[0].State != "closed" || e[0].CloseReason != "superseded" {
			t.Fatalf("running episode=%+v", e)
		}
		// The queued running notice is now obsolete and must not be sent.
		f.runOutbox(f.pool, f.w, running.ActionID, "cancelled")
		if f.provider.sent(running.ID) != 0 {
			t.Fatal("obsolete running notice was sent after stop")
		}
		f.at(46 * time.Minute)
		if got := f.scan(nil); got.Opened != 0 {
			t.Fatal("stopping is not a stall before its own threshold", got)
		}
		f.at(48 * time.Minute)
		if got := f.scan(nil); got.Opened != 1 || got.Enqueued != 1 {
			t.Fatal("unconfirmed stop did not open its own episode", got)
		}
		rows := f.notices(f.task.ID)
		if len(rows) != 2 || rows[1].Kind != "stopping" || !strings.Contains(rows[1].Body, "尚未确认退出") || strings.Contains(rows[1].Body, "还在执行") {
			t.Fatalf("stopping notice=%+v", rows)
		}
		// Process exit acknowledgement closes the stopping episode.
		f.exec(`UPDATE agent_task_queue SET context=(context-'process_stop_pending')||jsonb_build_object('process_stopped_at',to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')) WHERE id=$1::uuid`, f.queueID)
		f.at(50 * time.Minute)
		if got := f.scan(nil); got.Closed != 1 {
			t.Fatal("confirmed exit did not close the stopping episode", got)
		}
	})
}

func TestWatchdogProgressBeforeSendSuppressesNotice(t *testing.T) {
	f := newWatchdogFixture(t)
	f.at(16 * time.Minute)
	f.scan(nil)
	n := onlyNotice(t, f.notices(f.task.ID))
	// Progress lands after the intent commit but before the provider send.
	f.addMessage(f.queueID, "tool_use", f.base.Add(16*time.Minute))
	_, code := f.runOutbox(f.pool, f.w, n.ActionID, "cancelled")
	if !strings.HasSuffix(code, "newer_progress") || f.provider.total() != 0 {
		t.Fatal("stale notice was not suppressed", code, f.provider.total())
	}
	n = onlyNotice(t, f.notices(f.task.ID))
	if n.State != "suppressed" || n.Reason != "newer_progress" {
		t.Fatalf("notice=%+v", n)
	}
	if e := f.episodes(f.task.ID); len(e) != 1 || e[0].State != "cleared" {
		t.Fatalf("episode=%+v", e)
	}
}

func TestWatchdogCancelledOrUnboundBeforeSendSuppresses(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(f *watchdogFixture)
		reason string
	}{
		{"task_cancelled", func(f *watchdogFixture) { f.stopTask() }, "state_changed"},
		{"tenant_unbound", func(f *watchdogFixture) { f.targets.set("tenant_revoked", "") }, "tenant_revoked"},
		{"target_changed", func(f *watchdogFixture) { f.targets.set("", "cid-other") }, "notice_target_changed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newWatchdogFixture(t)
			f.at(16 * time.Minute)
			f.scan(nil)
			n := onlyNotice(t, f.notices(f.task.ID))
			c.change(f)
			_, code := f.runOutbox(f.pool, f.w, n.ActionID, "cancelled")
			if !strings.HasSuffix(code, c.reason) || f.provider.total() != 0 {
				t.Fatal("send was not suppressed", code, f.provider.total())
			}
			if n = onlyNotice(t, f.notices(f.task.ID)); n.State != "suppressed" || n.Reason != c.reason {
				t.Fatalf("notice=%+v", n)
			}
		})
	}
}

func TestWatchdogProviderUnknownIsNeverResent(t *testing.T) {
	f := newWatchdogFixture(t)
	f.provider.sendErr = errors.New("connection reset by peer")
	f.at(16 * time.Minute)
	f.scan(nil)
	n := onlyNotice(t, f.notices(f.task.ID))
	if state, code := f.runOutbox(f.pool, f.w, n.ActionID, "unknown"); code != "send_result_unknown" {
		t.Fatal(state, code)
	}
	// Make the action due again: the outbox may only query, never resubmit.
	f.exec(`UPDATE response_action SET next_attempt_at=now(),lease_until=NULL WHERE id=$1`, n.ActionID)
	var attempts int
	if err := f.pool.QueryRow(f.ctx, `SELECT attempts FROM response_action WHERE id=$1`, n.ActionID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	svc := dingtalkresponse.NewService(f.pool, f.provider, nil)
	svc.BeforeSend = func(ctx context.Context, in dingtalkresponse.ActionInput) error {
		_, err := f.w.BeforeSend(ctx, in)
		return err
	}
	ctx, cancel := context.WithCancel(f.ctx)
	go svc.Run(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && f.count(`SELECT attempts FROM response_action WHERE id=$1`, n.ActionID) == attempts {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	svc.WaitWithTimeout(context.Background(), 5*time.Second)
	if f.count(`SELECT attempts FROM response_action WHERE id=$1`, n.ActionID) == attempts {
		t.Fatal("outbox did not revisit the unknown action")
	}
	// Further scans never create a replacement intent or action.
	for _, d := range []time.Duration{20 * time.Minute, 40 * time.Minute, 90 * time.Minute} {
		f.at(d)
		f.scan(nil)
	}
	if f.provider.sent(n.ID) != 1 || f.actions() != 1 || len(f.notices(f.task.ID)) != 1 {
		t.Fatal("unknown provider outcome was resent", f.provider.sent(n.ID), f.actions())
	}
	if state, _ := f.actionState(n.ActionID); state != "unknown" {
		t.Fatal("unknown action changed state without a provider fact", state)
	}
}

type failingAfterEnqueue struct {
	EmployeeWatchdogOutbox
}

func (o failingAfterEnqueue) EnqueueSceneNotice(ctx context.Context, tx dingtalkresponse.DBTX, in dingtalkresponse.ActionInput, id string) (string, error) {
	if _, err := o.EmployeeWatchdogOutbox.EnqueueSceneNotice(ctx, tx, in, id); err != nil {
		return "", err
	}
	return "", errors.New("process died before commit")
}

// noNotify hides Notify so a committed intent is found only by polling.
type noNotify struct{ inner *dingtalkresponse.Service }

func (o noNotify) EnqueueSceneNotice(ctx context.Context, tx dingtalkresponse.DBTX, in dingtalkresponse.ActionInput, id string) (string, error) {
	return o.inner.EnqueueSceneNotice(ctx, tx, in, id)
}

func TestWatchdogCrashAfterCommitRecovers(t *testing.T) {
	f := newWatchdogFixture(t)
	f.at(16 * time.Minute)
	// A crash before commit leaves no partial episode, intent or action.
	crashing := f.watchdog(f.pool, failingAfterEnqueue{f.outbox})
	if _, err := crashing.Scan(f.ctx, 100); err == nil {
		t.Fatal("injected failure was swallowed")
	}
	if len(f.episodes(f.task.ID)) != 0 || len(f.notices(f.task.ID)) != 0 || f.actions() != 0 {
		t.Fatal("rolled-back scan left partial state")
	}
	// The intent commits; the process dies before any wake-up.
	committed := f.watchdog(f.pool, noNotify{f.outbox})
	if got := f.scan(committed); got.Enqueued != 1 {
		t.Fatal(got)
	}
	n := onlyNotice(t, f.notices(f.task.ID))
	// A fresh process (new pool, no local state) delivers exactly once and a
	// fresh scanner does not record it again.
	pool, err := pgxpool.NewWithConfig(f.ctx, f.pool.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fresh := f.watchdog(pool, dingtalkresponse.NewService(pool, f.provider, nil))
	f.runOutbox(pool, fresh, n.ActionID, "delivered")
	f.at(20 * time.Minute)
	f.scan(fresh)
	if f.provider.sent(n.ID) != 1 || f.actions() != 1 || len(f.notices(f.task.ID)) != 1 {
		t.Fatal("recovery duplicated the notice", f.provider.sent(n.ID), f.actions())
	}
}

func TestWatchdogEnableWatermarkSkipsHistory(t *testing.T) {
	f := newWatchdogFixture(t)
	f.exec(`DELETE FROM employee_watchdog_cursor WHERE workspace_id=$1::uuid`, f.ws)
	// First scan after rollout: this run started before enablement.
	f.at(16 * time.Minute)
	if got := f.scan(nil); got.Opened != 0 {
		t.Fatal("historical run replayed on rollout", got)
	}
	var enabled time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT enabled_at FROM employee_watchdog_cursor WHERE workspace_id=$1::uuid AND agent_id=$2::uuid`, f.ws, f.agent).Scan(&enabled); err != nil || !enabled.Equal(f.base.Add(16*time.Minute)) {
		t.Fatal("enable watermark", enabled, err)
	}
	// A second Task whose run starts after enablement is covered.
	store := employeetask.NewStore(f.pool)
	second, err := store.Create(f.ctx, employeetask.CreateParams{Scope: f.task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: f.task.RequesterRef, Definition: employeetask.Definition{Goal: "Second goal"}, Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, Input: "Second goal"})
	if err != nil {
		t.Fatal(err)
	}
	req := f.request
	req.Task, req.Source, req.Prompt = second, employeetask.Source{Namespace: "execute", Key: "second"}, "Second goal"
	r, err := f.service.EnqueueDirectTask(f.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	f.startQueue(util.UUIDToString(r.Task.ID), r.Run.ID, f.base.Add(17*time.Minute))
	f.at(33 * time.Minute)
	if got := f.scan(nil); got.Opened != 1 {
		t.Fatal("post-enable run not covered", got)
	}
	if len(f.episodes(f.task.ID)) != 0 || len(f.episodes(second.ID)) != 1 {
		t.Fatal("enable watermark applied to the wrong run")
	}
	// Later scans never move the watermark.
	f.at(2 * time.Hour)
	f.scan(nil)
	var again time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT enabled_at FROM employee_watchdog_cursor WHERE workspace_id=$1::uuid AND agent_id=$2::uuid`, f.ws, f.agent).Scan(&again); err != nil || !again.Equal(enabled) {
		t.Fatal("enable watermark moved", again, err)
	}
}

func TestWatchdogCredentialExpiryIsUnreachableNotRunning(t *testing.T) {
	f := newWatchdogFixture(t)
	var runtimeID string
	if err := f.pool.QueryRow(f.ctx, `SELECT runtime_id::text FROM agent_task_queue WHERE id=$1::uuid`, f.queueID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO agent_task_runtime_start_attempt(id,task_id,runtime_id,backend,protocol,status,last_stage,created_at,updated_at) VALUES($1::uuid,$2::uuid,$3::uuid,'aliyun_fc','legacy-v1','claimed','claim_finalized',$4,$4)`, uuid.NewString(), f.queueID, runtimeID, f.base.Add(-2*time.Minute))
	f.at(16 * time.Minute)
	f.scan(nil)
	// The claim outlived its credential: the running episode is superseded at
	// once, but the unreachable notice waits for its grace period.
	f.at(61 * time.Minute)
	if got := f.scan(nil); got.Opened != 0 || got.Closed != 1 {
		t.Fatal("credential expiry did not supersede the running episode", got)
	}
	f.at(63 * time.Minute)
	if got := f.scan(nil); got.Opened != 1 || got.Enqueued != 1 {
		t.Fatal("unreachable episode did not open after its grace", got)
	}
	episodes := f.episodes(f.task.ID)
	if len(episodes) != 2 || episodes[1].Kind != "execution_unreachable" || episodes[1].Reason != "credential_expired" || !episodes[1].Since.Equal(f.base.Add(time.Hour)) {
		t.Fatalf("episodes=%+v", episodes)
	}
	rows := f.notices(f.task.ID)
	if len(rows) != 2 || strings.Contains(rows[1].Body, "还在执行") || !strings.Contains(rows[1].Body, "无法确认") {
		t.Fatalf("unreachable notice=%+v", rows)
	}
}

func TestWatchdogNoticeCreatesNoTaskRunOrGeneration(t *testing.T) {
	f := newWatchdogFixture(t)
	counts := func() [6]int {
		return [6]int{
			f.count(`SELECT count(*) FROM employee_task WHERE workspace_id=$1::uuid`, f.ws),
			f.count(`SELECT count(*) FROM employee_task_entry WHERE workspace_id=$1::uuid`, f.ws),
			f.count(`SELECT count(*) FROM employee_task_run WHERE workspace_id=$1::uuid`, f.ws),
			f.count(`SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid`, f.agent),
			f.count(`SELECT count(*) FROM employee_scene_job WHERE workspace_id=$1::uuid`, f.ws),
			f.count(`SELECT version::int FROM employee_task WHERE id=$1::uuid`, f.task.ID),
		}
	}
	before := counts()
	f.at(16 * time.Minute)
	f.scan(nil)
	n := onlyNotice(t, f.notices(f.task.ID))
	f.runOutbox(f.pool, f.w, n.ActionID, "delivered")
	f.at(40 * time.Minute)
	f.scan(nil)
	if after := counts(); after != before {
		t.Fatalf("notice created work: before=%v after=%v", before, after)
	}
	var taskID *string
	var text, request string
	if err := f.pool.QueryRow(f.ctx, `SELECT task_id::text,input->>'text',request_id FROM response_action WHERE id=$1`, n.ActionID).Scan(&taskID, &text, &request); err != nil {
		t.Fatal(err)
	}
	if taskID != nil || text != n.Body || request != "scene-notice:"+n.ID {
		t.Fatal("notice action carries task or wrong provenance", taskID, request)
	}
	// The notice is part of the requester's scene dialogue for later turns.
	var kind, source, principal string
	if err := f.pool.QueryRow(f.ctx, `SELECT source_kind,source_id,principal_id::text FROM employee_host_notice WHERE action_id=$1 AND scene_id=$2::uuid`, n.ActionID, f.task.Scope.Scene.SceneID).Scan(&kind, &source, &principal); err != nil ||
		kind != "watchdog" || source != n.ID || principal != util.UUIDToString(f.request.PrincipalID) {
		t.Fatal("stall notice is not in the scene history", kind, source, principal, err)
	}
}

func TestWatchdogQuietDeliveryContractHoldsNotice(t *testing.T) {
	f := newWatchdogFixture(t)
	f.exec(`UPDATE agent_task_queue SET context=context||'{"employee_completion_notice_policy":{"mode":"if_not_delivered","require_delivery":"file","source_ref":"s1","instruction_quote":"只发文件"}}'::jsonb WHERE id=$1::uuid`, f.queueID)
	f.at(16 * time.Minute)
	if got := f.scan(nil); got.Opened != 1 || got.Held != 1 || got.Enqueued != 0 {
		t.Fatal(got)
	}
	n := onlyNotice(t, f.notices(f.task.ID))
	if n.State != "held" || n.Reason != "delivery_contract_quiet" || n.ActionID != "" || f.actions() != 0 {
		t.Fatalf("quiet contract overridden: %+v", n)
	}
}

func TestWatchdogNoticeCapPerBoundary(t *testing.T) {
	f := newWatchdogFixture(t)
	f.w.Config.MaxNoticesPerBoundary = 1
	f.at(16 * time.Minute)
	f.scan(nil)
	f.addMessage(f.queueID, "text", f.base.Add(17*time.Minute))
	f.at(18 * time.Minute)
	f.scan(nil)
	f.at(33 * time.Minute)
	if got := f.scan(nil); got.Opened != 1 || got.Held != 1 {
		t.Fatal(got)
	}
	rows := f.notices(f.task.ID)
	if len(rows) != 2 || rows[1].State != "held" || rows[1].Reason != "notice_cap" || f.actions() != 1 {
		t.Fatalf("cap not applied: %+v", rows)
	}
}

func TestWatchdogLiveConfigAndScopedOverride(t *testing.T) {
	f := newWatchdogFixture(t)
	var mu sync.Mutex
	live := DefaultEmployeeWatchdogConfig()
	live.RunningSilenceSeconds = 1200
	f.w.LoadConfig = func() EmployeeWatchdogConfig { mu.Lock(); defer mu.Unlock(); return live }
	f.at(16 * time.Minute)
	if got := f.scan(nil); got.Opened != 0 {
		t.Fatal("live configuration was ignored", got)
	}
	// Shortening one test agent's threshold leaves the shared default alone.
	mu.Lock()
	live.Agents = map[string]EmployeeWatchdogThresholds{f.agent: {RunningSilenceSeconds: 120}}
	mu.Unlock()
	f.at(17 * time.Minute)
	if got := f.scan(nil); got.Opened != 1 {
		t.Fatal("scoped override was ignored", got)
	}
	if live.Threshold(uuid.NewString(), EmployeeWatchdogExecutionRunning) != 20*time.Minute {
		t.Fatal("override leaked into other agents")
	}
	// An invalid live snapshot stops the scan instead of using guessed values.
	mu.Lock()
	live.RunningSilenceSeconds = 5
	mu.Unlock()
	if _, err := f.w.Scan(f.ctx, 100); err == nil {
		t.Fatal("invalid live configuration accepted")
	}
}
