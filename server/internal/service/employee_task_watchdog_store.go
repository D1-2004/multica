package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// EmployeeWatchdogDB is satisfied by a pgx pool.
type EmployeeWatchdogDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// EmployeeTaskWaitReader exposes a Task's declared wait from the package that
// owns it. Returning false means the Task has no open wait.
type EmployeeTaskWaitReader interface {
	ReadEmployeeTaskWait(context.Context, pgx.Tx, employeetask.Task) (EmployeeTaskWait, bool, error)
}

// EmployeeWatchdogNoticeRef identifies the Task authority a notice belongs to.
// The resolver derives the delivery anchor from the Task's original accepted
// source; nothing in the ref grants authority.
type EmployeeWatchdogNoticeRef struct {
	NoticeID     string
	Scope        employeetask.Scope
	TaskID       string
	RunID        string
	QueueTaskID  string
	RequesterRef string
	Kind         EmployeeWatchdogStateKind
}

// EmployeeWatchdogHold is a permanent, observable reason not to send a notice
// for this episode (for example a revoked tenant or a missing anchor).
type EmployeeWatchdogHold struct{ Reason string }

func (e *EmployeeWatchdogHold) Error() string { return "employee watchdog notice held: " + e.Reason }

// EmployeeWatchdogTargetResolver resolves the original Task authority's
// delivery anchor under the current permission, identity and tenant fences.
// Any non-hold error is retried by a later scan or BeforeSend.
type EmployeeWatchdogTargetResolver interface {
	ResolveEmployeeWatchdogTarget(context.Context, pgx.Tx, EmployeeWatchdogNoticeRef) (dingtalkresponse.ActionInput, error)
}

// EmployeeWatchdogOutbox is the existing response outbox scene-notice entry.
type EmployeeWatchdogOutbox interface {
	EnqueueSceneNotice(context.Context, dingtalkresponse.DBTX, dingtalkresponse.ActionInput, string) (string, error)
}

// EmployeeWatchdog finds Employee Tasks without real progress, records stall
// episodes and at most one deterministic notice per episode, and fences that
// notice again immediately before the provider send. It never invokes a model
// and never creates Tasks, Runs, queue work or scene jobs.
type EmployeeWatchdog struct {
	DB      EmployeeWatchdogDB
	Config  EmployeeWatchdogConfig
	Waits   EmployeeTaskWaitReader
	Targets EmployeeWatchdogTargetResolver
	Outbox  EmployeeWatchdogOutbox
	// Ready is the producer gate: it must fail until every live replica runs a
	// binary whose outbox BeforeSend understands watchdog notices.
	Ready func(context.Context) error
	// Now is a fixture clock for tests; nil uses the PostgreSQL clock so Host
	// times and the scan clock share one source.
	Now func() time.Time
}

// EmployeeWatchdogScanResult counts one scan's effects. Skipped holds metric
// reasons for states that were not aged (never notices).
type EmployeeWatchdogScanResult struct {
	Candidates int
	Opened     int
	Cleared    int
	Closed     int
	Enqueued   int
	Held       int
	Skipped    map[string]int
}

func (r *EmployeeWatchdogScanResult) add(o EmployeeWatchdogScanResult) {
	r.Opened += o.Opened
	r.Cleared += o.Cleared
	r.Closed += o.Closed
	r.Enqueued += o.Enqueued
	r.Held += o.Held
	for k, v := range o.Skipped {
		if r.Skipped == nil {
			r.Skipped = map[string]int{}
		}
		r.Skipped[k] += v
	}
}

const employeeWatchdogRecipientTaskOrigin = "task_origin"

func (w *EmployeeWatchdog) now(ctx context.Context) (time.Time, error) {
	if w.Now != nil {
		return w.Now(), nil
	}
	var now time.Time
	err := w.DB.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

// Scan evaluates up to limit candidate Tasks, each in its own transaction.
// Per-Task failures do not stop other Tasks; they are joined into the error.
func (w *EmployeeWatchdog) Scan(ctx context.Context, limit int) (EmployeeWatchdogScanResult, error) {
	var out EmployeeWatchdogScanResult
	if w == nil || w.DB == nil || w.Targets == nil || w.Outbox == nil || w.Ready == nil {
		return out, errors.New("employee watchdog is not configured")
	}
	if limit < 1 || limit > 1000 {
		return out, errors.New("employee watchdog scan limit is invalid")
	}
	if err := w.Config.Validate(); err != nil {
		return out, err
	}
	if err := w.Ready(ctx); err != nil {
		return out, err
	}
	now, err := w.now(ctx)
	if err != nil {
		return out, err
	}
	if err := w.enableAgents(ctx, now); err != nil {
		return out, err
	}
	candidates, err := w.candidates(ctx, now, limit)
	if err != nil {
		return out, err
	}
	out.Candidates = len(candidates)
	var errs []error
	for _, c := range candidates {
		r, err := w.scanTask(ctx, now, c)
		if err != nil {
			errs = append(errs, fmt.Errorf("employee watchdog task %s: %w", c.TaskID, err))
			continue
		}
		out.add(r)
	}
	if out.Enqueued > 0 {
		if n, ok := w.Outbox.(interface{ Notify() }); ok {
			n.Notify()
		}
	}
	reasons := make([]string, 0, len(out.Skipped))
	for k, v := range out.Skipped {
		reasons = append(reasons, fmt.Sprintf("%s=%d", k, v))
	}
	sort.Strings(reasons)
	level := slog.LevelDebug
	if out.Opened+out.Cleared+out.Closed > 0 || len(errs) > 0 {
		level = slog.LevelInfo
	}
	slog.Log(ctx, level, "employee watchdog scan", "event", "employee_watchdog_scan", "candidates", out.Candidates, "opened", out.Opened, "cleared", out.Cleared, "closed", out.Closed, "notices_enqueued", out.Enqueued, "notices_held", out.Held, "skipped", strings.Join(reasons, ","), "failed", len(errs))
	return out, errors.Join(errs...)
}

// enableAgents records the enable watermark of every Employee agent the first
// time it is seen. Each insert holds its workspace key-share lock so workspace
// deletion cannot leave an orphan cursor.
func (w *EmployeeWatchdog) enableAgents(ctx context.Context, now time.Time) error {
	rows, err := w.DB.Query(ctx, `SELECT a.workspace_id::text,a.id::text FROM agent a
 WHERE a.coordination_mode='employee' AND a.archived_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM employee_watchdog_cursor c WHERE c.workspace_id=a.workspace_id AND c.agent_id=a.id)
 ORDER BY a.id LIMIT 1000`)
	if err != nil {
		return err
	}
	type agentRef struct{ workspace, agent string }
	var agents []agentRef
	for rows.Next() {
		var a agentRef
		if err = rows.Scan(&a.workspace, &a.agent); err != nil {
			rows.Close()
			return err
		}
		agents = append(agents, a)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, a := range agents {
		created, err := func() (bool, error) {
			tx, err := w.DB.Begin(ctx)
			if err != nil {
				return false, err
			}
			defer tx.Rollback(context.WithoutCancel(ctx))
			var locked string
			if err = tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, a.workspace).Scan(&locked); errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			} else if err != nil {
				return false, err
			}
			tag, err := tx.Exec(ctx, `INSERT INTO employee_watchdog_cursor(workspace_id,agent_id,enabled_at)
 SELECT $1::uuid,$2::uuid,$3 WHERE EXISTS(SELECT 1 FROM agent WHERE id=$2::uuid AND workspace_id=$1::uuid AND coordination_mode='employee')
 ON CONFLICT (workspace_id,agent_id) DO NOTHING`, a.workspace, a.agent, now)
			if err != nil {
				return false, err
			}
			return tag.RowsAffected() == 1, tx.Commit(ctx)
		}()
		if err != nil {
			return err
		}
		if created {
			slog.InfoContext(ctx, "employee watchdog enabled for agent", "event", "employee_watchdog_enabled", "workspace_id", a.workspace, "agent_id", a.agent, "enabled_at", now)
		}
	}
	return nil
}

type employeeWatchdogCandidate struct {
	TaskID      string
	WorkspaceID string
	AgentID     string
	TenantOrgID string
	SceneID     string
}

func (c employeeWatchdogCandidate) scope() employeetask.Scope {
	s := employeetask.Scope{WorkspaceID: c.WorkspaceID, AgentID: c.AgentID, TenantOrgID: c.TenantOrgID, Kind: employeetask.ScopeScene}
	s.Scene.SceneID = c.SceneID
	return s
}

// candidates lists Employee Direct scene Tasks that can be in a watched state,
// plus any Task holding an open episode so it can be closed.
func (w *EmployeeWatchdog) candidates(ctx context.Context, now time.Time, limit int) ([]employeeWatchdogCandidate, error) {
	rows, err := w.DB.Query(ctx, `SELECT t.id::text,t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scene_id::text FROM employee_task t
 WHERE t.owner_loop='employee' AND t.dispatch_mode='direct' AND t.scope_kind='scene' AND t.issue_id IS NULL
 AND EXISTS(SELECT 1 FROM employee_watchdog_cursor c WHERE c.workspace_id=t.workspace_id AND c.agent_id=t.agent_id)
 AND (t.active_run_id IS NOT NULL
  OR t.state NOT IN ('ready','running','succeeded','failed','cancelled')
  OR (t.state='cancelled' AND t.updated_at > $1)
  OR EXISTS(SELECT 1 FROM employee_watchdog_episode e WHERE e.task_id=t.id AND e.state='open'))
 ORDER BY t.id LIMIT $2`, now.Add(-w.Config.stoppingWindow()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []employeeWatchdogCandidate
	for rows.Next() {
		var c employeeWatchdogCandidate
		if err := rows.Scan(&c.TaskID, &c.WorkspaceID, &c.AgentID, &c.TenantOrgID, &c.SceneID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// employeeWatchdogDelivery is the accepted delivery contract of the execution.
type employeeWatchdogDelivery struct {
	known bool
	owner string
	quiet bool
}

type employeeWatchdogLoaded struct {
	task     employeetask.Task
	facts    EmployeeWatchdogFacts
	delivery employeeWatchdogDelivery
}

// lockEmployeeWatchdogTask fences workspace deletion (same parent lock as Task
// writers) and serializes watchdog evaluation of one Task across replicas.
func lockEmployeeWatchdogTask(ctx context.Context, tx pgx.Tx, workspaceID, taskID string) (bool, error) {
	var locked string
	err := tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, workspaceID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('employee-watchdog:' || $1, 0))`, taskID)
	return err == nil, err
}

func (w *EmployeeWatchdog) loadFacts(ctx context.Context, tx pgx.Tx, scope employeetask.Scope, taskID string) (employeeWatchdogLoaded, error) {
	var out employeeWatchdogLoaded
	task, err := employeetask.NewStore(tx).Get(ctx, scope, taskID)
	if err != nil {
		return out, err
	}
	out.task = task
	out.facts = EmployeeWatchdogFacts{TaskState: task.State, ActiveRunID: task.ActiveRunID}
	var run employeetask.Run
	switch {
	case task.ActiveRunID != "":
		err = tx.QueryRow(ctx, `SELECT id::text,queue_task_id::text,state,created_at FROM employee_task_run
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND id=$5::uuid`,
			scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, task.ID, task.ActiveRunID).Scan(&run.ID, &run.QueueTaskID, &run.State, &run.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
	case task.State == employeetask.StateCancelled:
		run, err = employeetask.NewStore(tx).LatestRun(ctx, scope, task.ID)
		if errors.Is(err, employeetask.ErrNotFound) {
			err = nil
		}
	}
	if err != nil {
		return out, err
	}
	var queue db.AgentTaskQueue
	if run.ID != "" {
		e := &EmployeeWatchdogExecution{RunID: run.ID, RunState: run.State, RunCreatedAt: run.CreatedAt, QueueTaskID: run.QueueTaskID}
		var started, fireAt, runtimeChanged *time.Time
		var queueContext []byte
		var runtimeStatus, runtimeMode, runtimeKind string
		var attempt bool
		err = tx.QueryRow(ctx, `SELECT q.status,q.started_at,q.fire_at,q.context,COALESCE(rt.status,''),COALESCE(rt.runtime_mode,''),COALESCE(rt.metadata->>'kind',''),rt.updated_at,
 EXISTS(SELECT 1 FROM agent_task_runtime_start_attempt a WHERE a.task_id=q.id AND a.backend IN ('aliyun_fc','asb'))
 FROM agent_task_queue q LEFT JOIN agent_runtime rt ON rt.id=q.runtime_id
 WHERE q.id=$1::uuid AND q.agent_id=$2::uuid`, run.QueueTaskID, scope.AgentID).Scan(&e.QueueStatus, &started, &fireAt, &queueContext, &runtimeStatus, &runtimeMode, &runtimeKind, &runtimeChanged, &attempt)
		if errors.Is(err, pgx.ErrNoRows) {
			// A Run whose queue row is gone cannot be observed.
			e = nil
			err = nil
		} else if err != nil {
			return out, err
		} else {
			if started != nil {
				e.QueueStartedAt = *started
			}
			if fireAt != nil {
				e.QueueFireAt = *fireAt
			}
			cloud := runtimeMode == "cloud" || runtimeKind == "fc-e2b"
			// Cloud runtimes are not heartbeat-managed; only a local daemon's
			// offline row means its executions cannot report.
			e.RuntimeOffline = runtimeStatus != "" && runtimeStatus != "online" && !cloud
			if runtimeChanged != nil {
				e.RuntimeChangedAt = *runtimeChanged
			}
			e.CredentialBound = attempt || runtimeKind == "fc-e2b"
			out.delivery = employeeWatchdogDeliveryContract(queueContext)
			if task.State == employeetask.StateCancelled {
				queue, err = db.New(tx).GetAgentTask(ctx, util.MustParseUUID(run.QueueTaskID))
				if err != nil {
					return out, err
				}
			}
		}
		out.facts.Execution = e
	}
	if task.State == employeetask.StateCancelled {
		stop := &EmployeeWatchdogStop{}
		err = tx.QueryRow(ctx, `SELECT seq,created_at FROM employee_task_entry
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND kind='input' AND payload->>'operation'='stop'
 ORDER BY seq DESC LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, task.ID).Scan(&stop.Seq, &stop.At)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			stop, err = nil, nil
		case err != nil:
			return out, err
		case out.facts.Execution == nil:
			stop.ExitConfirmed = true
		default:
			// Same projection read_task uses, including steer predecessors.
			projection, _, perr := directStopProjection(ctx, tx, task, run, queue)
			if perr != nil {
				if !errors.Is(perr, ErrDirectTaskAccessDenied) && !errors.Is(perr, employeetask.ErrInvalid) {
					return out, perr
				}
				// An unverifiable execution is not aged.
				stop = nil
			} else {
				stop.ExitConfirmed = projection.ExitConfirmed
			}
		}
		out.facts.Stop = stop
	}
	if w.Waits != nil {
		wait, ok, err := w.Waits.ReadEmployeeTaskWait(ctx, tx, task)
		if err != nil {
			return out, err
		}
		if ok {
			out.facts.Wait = &wait
		}
	}
	queueID := ""
	if out.facts.Execution != nil {
		queueID = out.facts.Execution.QueueTaskID
	}
	out.facts.Progress, err = loadEmployeeTaskProgress(ctx, tx, task, queueID)
	if err != nil {
		return out, err
	}
	if out.facts.Wait != nil {
		out.facts.Progress = out.facts.Progress.Merge(out.facts.Wait.Progress)
	}
	return out, nil
}

// employeeWatchdogDeliveryContract reads the frozen delivery contract of the
// execution. An unknown completion notice mode is treated as quiet: a
// proactive notice must never override a contract the Host does not know.
func employeeWatchdogDeliveryContract(raw []byte) employeeWatchdogDelivery {
	var c struct {
		Owner  string `json:"employee_delivery_owner"`
		Policy *struct {
			Mode string `json:"mode"`
		} `json:"employee_completion_notice_policy"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return employeeWatchdogDelivery{known: true, quiet: true}
	}
	quiet := c.Policy != nil && c.Policy.Mode != "" && c.Policy.Mode != string(employeetask.CompletionNoticeAlways)
	return employeeWatchdogDelivery{known: true, owner: c.Owner, quiet: quiet}
}

func loadOpenEmployeeWatchdogEpisode(ctx context.Context, tx pgx.Tx, taskID string) (*employeeWatchdogOpen, error) {
	var e employeeWatchdogOpen
	var at *time.Time
	var source string
	err := tx.QueryRow(ctx, `SELECT id::text,state_kind,boundary_key,since_at,watermark_at,watermark_source,watermark_ref FROM employee_watchdog_episode
 WHERE task_id=$1::uuid AND state='open' FOR UPDATE`, taskID).Scan(&e.ID, &e.Kind, &e.Boundary, &e.Since, &at, &source, &e.Watermark.Ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if at != nil {
		e.Watermark.At, e.Watermark.Source = *at, EmployeeActivitySource(source)
	}
	return &e, nil
}

func closeEmployeeWatchdogEpisode(ctx context.Context, tx pgx.Tx, id, state, reason string, watermark EmployeeActivityWatermark, now time.Time) error {
	var at *time.Time
	if !watermark.IsZero() {
		at = &watermark.At
	}
	tag, err := tx.Exec(ctx, `UPDATE employee_watchdog_episode SET state=$2,close_reason=$3,closed_at=$4,
 watermark_source=CASE WHEN $5::timestamptz > COALESCE(watermark_at,'-infinity') THEN $6 ELSE watermark_source END,
 watermark_ref=CASE WHEN $5::timestamptz > COALESCE(watermark_at,'-infinity') THEN $7 ELSE watermark_ref END,
 watermark_at=CASE WHEN $5::timestamptz > COALESCE(watermark_at,'-infinity') THEN $5::timestamptz ELSE watermark_at END,
 updated_at=now() WHERE id=$1::uuid AND state='open'`, id, state, reason, now, at, string(watermark.Source), watermark.Ref)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("employee watchdog episode is no longer open")
	}
	return nil
}

type employeeWatchdogEvent struct {
	name   string
	fields []any
}

func (w *EmployeeWatchdog) scanTask(ctx context.Context, now time.Time, c employeeWatchdogCandidate) (EmployeeWatchdogScanResult, error) {
	var out EmployeeWatchdogScanResult
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if ok, err := lockEmployeeWatchdogTask(ctx, tx, c.WorkspaceID, c.TaskID); err != nil || !ok {
		return out, err
	}
	var enabledAt time.Time
	err = tx.QueryRow(ctx, `SELECT enabled_at FROM employee_watchdog_cursor WHERE workspace_id=$1::uuid AND agent_id=$2::uuid`, c.WorkspaceID, c.AgentID).Scan(&enabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	loaded, err := w.loadFacts(ctx, tx, c.scope(), c.TaskID)
	if errors.Is(err, employeetask.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	state := ClassifyEmployeeWatchdogState(loaded.facts, now, w.Config.credentialTTL())
	open, err := loadOpenEmployeeWatchdogEpisode(ctx, tx, c.TaskID)
	if err != nil {
		return out, err
	}
	threshold := w.Config.Threshold(c.AgentID, state.Kind)
	d := decideEmployeeWatchdog(open, state, loaded.facts.Progress, enabledAt, threshold, now)
	base := []any{"workspace_id", c.WorkspaceID, "agent_id", c.AgentID, "tenant_org_id", c.TenantOrgID, "scene_id", c.SceneID, "task_id", c.TaskID}
	var events []employeeWatchdogEvent
	if d.Close {
		if err = closeEmployeeWatchdogEpisode(ctx, tx, open.ID, d.CloseState, d.CloseReason, d.CloseWatermark, now); err != nil {
			return out, err
		}
		if d.CloseState == "cleared" {
			out.Cleared++
		} else {
			out.Closed++
		}
		events = append(events, employeeWatchdogEvent{"employee_watchdog_episode_closed", append(append([]any{}, base...), "episode_id", open.ID, "state_kind", string(open.Kind), "state", d.CloseState, "close_reason", d.CloseReason, "progress_source", string(d.CloseWatermark.Source), "progress_ref", d.CloseWatermark.Ref)})
	}
	if d.Skip != "" {
		out.Skipped = map[string]int{d.Skip: 1}
	}
	if d.Open {
		episodeID, noticeEvents, r, err := w.openEpisode(ctx, tx, loaded, state, d, now)
		if err != nil {
			return out, err
		}
		out.add(r)
		if episodeID != "" {
			events = append(events, employeeWatchdogEvent{"employee_watchdog_episode_opened", append(append([]any{}, base...), "episode_id", episodeID, "run_id", state.RunID, "state_kind", string(state.Kind), "state_reason", state.Reason, "boundary", state.Boundary, "since", d.Since, "silent_seconds", int(now.Sub(d.Since).Seconds()), "progress_source", string(d.Watermark.Source), "progress_ref", d.Watermark.Ref)})
			events = append(events, noticeEvents...)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return EmployeeWatchdogScanResult{}, err
	}
	for _, e := range events {
		slog.InfoContext(ctx, "employee watchdog", append([]any{"event", e.name}, e.fields...)...)
	}
	return out, nil
}

// openEpisode inserts the episode and its single notice intent. The intent is
// enqueued into the response outbox in the same transaction, so a crash after
// commit is recovered by the outbox poll and a crash before commit leaves
// nothing behind.
func (w *EmployeeWatchdog) openEpisode(ctx context.Context, tx pgx.Tx, loaded employeeWatchdogLoaded, state EmployeeWatchdogState, d employeeWatchdogDecision, now time.Time) (string, []employeeWatchdogEvent, EmployeeWatchdogScanResult, error) {
	var out EmployeeWatchdogScanResult
	task := loaded.task
	var watermarkAt *time.Time
	if !d.Watermark.IsZero() {
		watermarkAt = &d.Watermark.At
	}
	var runID *string
	if state.RunID != "" {
		runID = &state.RunID
	}
	var episodeID string
	err := tx.QueryRow(ctx, `INSERT INTO employee_watchdog_episode
 (workspace_id,agent_id,tenant_org_id,scene_id,task_id,run_id,state_kind,state_reason,boundary_key,since_at,watermark_at,watermark_source,watermark_ref,opened_at)
 VALUES ($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6::uuid,$7,$8,$9,$10,$11,$12,$13,$14)
 ON CONFLICT DO NOTHING RETURNING id::text`,
		task.Scope.WorkspaceID, task.Scope.AgentID, task.Scope.TenantOrgID, task.Scope.Scene.SceneID, task.ID, runID,
		string(state.Kind), state.Reason, state.Boundary, d.Since, watermarkAt, string(d.Watermark.Source), d.Watermark.Ref, now).Scan(&episodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		// The same silence was already recorded (for example the state flipped
		// back to an identical boundary): never notify it twice.
		out.Skipped = map[string]int{"duplicate_episode": 1}
		return "", nil, out, nil
	}
	if err != nil {
		return "", nil, out, err
	}
	out.Opened++
	noticeID := uuid.NewString()
	body := ComposeEmployeeWatchdogNotice(state, task.Definition.Goal, now.Sub(d.Since))
	noticeState, reason, actionID := "", "", ""
	switch {
	case !loaded.delivery.known && state.RunID != "":
		noticeState, reason = "held", "delivery_contract_unknown"
	case loaded.delivery.known && loaded.delivery.owner != "employee":
		noticeState, reason = "held", "delivery_not_host_owned"
	case loaded.delivery.quiet:
		noticeState, reason = "held", "delivery_contract_quiet"
	}
	if noticeState == "" {
		var sent int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM employee_watchdog_notice WHERE task_id=$1::uuid AND boundary_key=$2 AND state='enqueued'`, task.ID, state.Boundary).Scan(&sent); err != nil {
			return "", nil, out, err
		}
		if sent >= w.Config.maxNotices() {
			noticeState, reason = "held", "notice_cap"
		}
	}
	if noticeState == "" {
		ref := EmployeeWatchdogNoticeRef{NoticeID: noticeID, Scope: task.Scope, TaskID: task.ID, RunID: state.RunID, RequesterRef: task.RequesterRef, Kind: state.Kind}
		if loaded.facts.Execution != nil {
			ref.QueueTaskID = loaded.facts.Execution.QueueTaskID
		}
		target, err := w.Targets.ResolveEmployeeWatchdogTarget(ctx, tx, ref)
		var hold *EmployeeWatchdogHold
		switch {
		case errors.As(err, &hold):
			noticeState, reason = "held", hold.Reason
		case err != nil:
			return "", nil, out, err
		default:
			target.Text = body
			actionID, err = w.Outbox.EnqueueSceneNotice(ctx, tx, target, noticeID)
			if err != nil {
				return "", nil, out, err
			}
			noticeState = "enqueued"
		}
	}
	var action *string
	if actionID != "" {
		action = &actionID
	}
	if _, err = tx.Exec(ctx, `INSERT INTO employee_watchdog_notice
 (id,episode_id,workspace_id,agent_id,tenant_org_id,scene_id,task_id,run_id,kind,recipient_key,boundary_key,body,state,reason,action_id)
 VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6::uuid,$7::uuid,$8::uuid,$9,$10,$11,$12,$13,$14,$15)`,
		noticeID, episodeID, task.Scope.WorkspaceID, task.Scope.AgentID, task.Scope.TenantOrgID, task.Scope.Scene.SceneID, task.ID, runID,
		string(state.Kind), employeeWatchdogRecipientTaskOrigin, state.Boundary, body, noticeState, reason, action); err != nil {
		return "", nil, out, err
	}
	if noticeState == "enqueued" {
		out.Enqueued++
	} else {
		out.Held++
	}
	event := employeeWatchdogEvent{"employee_watchdog_notice_recorded", []any{"workspace_id", task.Scope.WorkspaceID, "agent_id", task.Scope.AgentID, "scene_id", task.Scope.Scene.SceneID, "task_id", task.ID, "run_id", state.RunID, "episode_id", episodeID, "notice_id", noticeID, "kind", string(state.Kind), "state", noticeState, "reason", reason, "action_id", actionID}}
	return episodeID, []employeeWatchdogEvent{event}, out, nil
}

// BeforeSend is the outbox fence for watchdog notices. It reports handled=false
// for any action that is not a watchdog notice, so callers can chain it before
// other guards. A handled notice is sent only if its episode is still open,
// the Task is in the same state boundary, no newer progress arrived, the
// delivery contract still allows it and the delivery anchor is unchanged.
// Suppression is permanent; other errors defer the send.
func (w *EmployeeWatchdog) BeforeSend(ctx context.Context, in dingtalkresponse.ActionInput) (bool, error) {
	if w == nil || w.DB == nil || in.SceneNoticeID == "" {
		return false, nil
	}
	if _, err := uuid.Parse(in.SceneNoticeID); err != nil {
		return false, nil
	}
	var workspaceID, taskID string
	err := w.DB.QueryRow(ctx, `SELECT workspace_id::text,task_id::text FROM employee_watchdog_notice WHERE id=$1::uuid`, in.SceneNoticeID).Scan(&workspaceID, &taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if w.Targets == nil {
		return true, errors.New("employee watchdog notice target resolver is unavailable")
	}
	now, err := w.now(ctx)
	if err != nil {
		return true, err
	}
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	suppress := func(reason string, closeEpisode *employeeWatchdogOpen, closeState, closeReason string, watermark EmployeeActivityWatermark) (bool, error) {
		if _, err := tx.Exec(ctx, `UPDATE employee_watchdog_notice SET state='suppressed',reason=$2,updated_at=now() WHERE id=$1::uuid AND state='enqueued'`, in.SceneNoticeID, reason); err != nil {
			return true, err
		}
		if closeEpisode != nil {
			if err := closeEmployeeWatchdogEpisode(ctx, tx, closeEpisode.ID, closeState, closeReason, watermark, now); err != nil {
				return true, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return true, err
		}
		slog.InfoContext(ctx, "employee watchdog notice suppressed", "event", "employee_watchdog_notice_suppressed", "workspace_id", workspaceID, "task_id", taskID, "notice_id", in.SceneNoticeID, "action_id", in.ActionID, "reason", reason)
		return true, &dingtalkresponse.SuppressSendError{Reason: reason}
	}
	locked, err := lockEmployeeWatchdogTask(ctx, tx, workspaceID, taskID)
	if err != nil {
		return true, err
	}
	if !locked {
		return suppress("workspace_removed", nil, "", "", EmployeeActivityWatermark{})
	}
	var n struct {
		episodeID, state, reason, body, actionID, kind, boundary string
		scope                                                    employeetask.Scope
	}
	n.scope.Kind = employeetask.ScopeScene
	err = tx.QueryRow(ctx, `SELECT episode_id::text,state,reason,body,COALESCE(action_id,''),kind,boundary_key,agent_id::text,tenant_org_id,scene_id::text
 FROM employee_watchdog_notice WHERE id=$1::uuid FOR UPDATE`, in.SceneNoticeID).Scan(&n.episodeID, &n.state, &n.reason, &n.body, &n.actionID, &n.kind, &n.boundary, &n.scope.AgentID, &n.scope.TenantOrgID, &n.scope.Scene.SceneID)
	if errors.Is(err, pgx.ErrNoRows) {
		return suppress("notice_removed", nil, "", "", EmployeeActivityWatermark{})
	}
	if err != nil {
		return true, err
	}
	n.scope.WorkspaceID = workspaceID
	if n.state != "enqueued" {
		reason := n.reason
		if reason == "" {
			reason = "notice_" + n.state
		}
		return suppress(reason, nil, "", "", EmployeeActivityWatermark{})
	}
	if n.actionID != in.ActionID || in.Text != n.body {
		return suppress("notice_action_mismatch", nil, "", "", EmployeeActivityWatermark{})
	}
	var episodeState string
	if err = tx.QueryRow(ctx, `SELECT state FROM employee_watchdog_episode WHERE id=$1::uuid FOR UPDATE`, n.episodeID).Scan(&episodeState); errors.Is(err, pgx.ErrNoRows) {
		return suppress("episode_removed", nil, "", "", EmployeeActivityWatermark{})
	} else if err != nil {
		return true, err
	}
	if episodeState != "open" {
		return suppress("episode_"+episodeState, nil, "", "", EmployeeActivityWatermark{})
	}
	open, err := loadOpenEmployeeWatchdogEpisode(ctx, tx, taskID)
	if err != nil {
		return true, err
	}
	if open == nil || open.ID != n.episodeID {
		return suppress("episode_superseded", nil, "", "", EmployeeActivityWatermark{})
	}
	loaded, err := w.loadFacts(ctx, tx, n.scope, taskID)
	if errors.Is(err, employeetask.ErrNotFound) {
		return suppress("task_removed", open, "closed", "state_ended", EmployeeActivityWatermark{})
	}
	if err != nil {
		return true, err
	}
	state := ClassifyEmployeeWatchdogState(loaded.facts, now, w.Config.credentialTTL())
	if state.Kind != open.Kind || state.Boundary != open.Boundary {
		return suppress("state_changed", open, "closed", "superseded", EmployeeActivityWatermark{})
	}
	progress := loaded.facts.Progress.Merge(open.Watermark)
	if since := state.SilentSince(progress); since.After(open.Since) {
		closeState, closeReason := "closed", "superseded"
		if threshold := w.Config.Threshold(n.scope.AgentID, state.Kind); threshold <= 0 || now.Sub(since) < threshold {
			closeState, closeReason = "cleared", "progress"
		}
		return suppress("newer_progress", open, closeState, closeReason, progress)
	}
	if loaded.delivery.quiet {
		return suppress("delivery_contract_quiet", nil, "", "", EmployeeActivityWatermark{})
	}
	ref := EmployeeWatchdogNoticeRef{NoticeID: in.SceneNoticeID, Scope: loaded.task.Scope, TaskID: taskID, RunID: state.RunID, RequesterRef: loaded.task.RequesterRef, Kind: state.Kind}
	if loaded.facts.Execution != nil {
		ref.QueueTaskID = loaded.facts.Execution.QueueTaskID
	}
	target, err := w.Targets.ResolveEmployeeWatchdogTarget(ctx, tx, ref)
	var hold *EmployeeWatchdogHold
	if errors.As(err, &hold) {
		return suppress(hold.Reason, nil, "", "", EmployeeActivityWatermark{})
	}
	if err != nil {
		return true, err
	}
	if !employeeWatchdogTargetMatches(in, target) {
		return suppress("notice_target_changed", nil, "", "", EmployeeActivityWatermark{})
	}
	return true, tx.Commit(ctx)
}

func employeeWatchdogTargetMatches(in, want dingtalkresponse.ActionInput) bool {
	return in.WorkspaceID == want.WorkspaceID && in.AgentID == want.AgentID && in.DWSUID == want.DWSUID && in.DWSOrgID == want.DWSOrgID &&
		in.SceneID == want.SceneID && in.ConversationID == want.ConversationID && in.IsGroup == want.IsGroup &&
		in.SenderOpenDingTalkID == want.SenderOpenDingTalkID && in.ShowAITag == want.ShowAITag && in.DWSEnvironment == want.DWSEnvironment
}
