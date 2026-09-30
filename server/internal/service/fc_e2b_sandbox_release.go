package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshschedule"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Every task start renews its sandbox to the full task lifetime (4800s), and
// nothing shortened it afterwards: a one-minute autopilot run kept its sandbox
// for 80 minutes. When a task ends, a sandbox no later task can reuse is
// released, and a reusable one keeps only a short idle window so the next turn
// on the same issue or chat still starts warm. Anything this path skips falls
// back to the provider timeout, which was the only release before.
const (
	fcE2BSandboxIdleRetention = 10 * time.Minute
	// The runner may still flush its last reports right after the server
	// records the terminal state.
	fcE2BSandboxReleaseGrace  = 30 * time.Second
	fcE2BSandboxReleaseBudget = 2 * time.Minute
)

// A cleanup holds one pooled connection for its scope lock (the default pool
// is 25 per pod), so only a few run at once and a bounded number may wait.
// Anything beyond falls back to the provider timeout.
const (
	fcE2BSandboxReleaseConcurrency = 4
	fcE2BSandboxReleaseMaxPending  = 512
)

var (
	fcE2BSandboxReleaseSlots = make(chan struct{}, fcE2BSandboxReleaseConcurrency)
	// Terminal writes notify from both the event and the metrics path; one
	// cleanup per task is in flight at a time.
	fcE2BSandboxReleasePending      sync.Map
	fcE2BSandboxReleasePendingCount atomic.Int64
)

type fcE2BSandboxLifecycleAction string

const (
	fcE2BSandboxCreated       fcE2BSandboxLifecycleAction = "created"
	fcE2BSandboxReused        fcE2BSandboxLifecycleAction = "reused"
	fcE2BSandboxReleased      fcE2BSandboxLifecycleAction = "released"
	fcE2BSandboxIdleTrimmed   fcE2BSandboxLifecycleAction = "idle_trimmed"
	fcE2BSandboxRetained      fcE2BSandboxLifecycleAction = "retained"
	fcE2BSandboxReleaseFailed fcE2BSandboxLifecycleAction = "release_failed"
)

// logFCE2BSandboxLifecycle emits one searchable event per sandbox transition,
// so a sandbox can be followed from creation to release in one query.
func logFCE2BSandboxLifecycle(action fcE2BSandboxLifecycleAction, reason string, task db.AgentTaskQueue, sandboxID, scope string, err error) {
	attrs := []any{"event", "fc_e2b_sandbox_lifecycle", "action", string(action), "reason", reason,
		"sandbox_id", sandboxID, "scope", scope, "task_id", util.UUIDToString(task.ID),
		"agent_id", util.UUIDToString(task.AgentID), "runtime_id", util.UUIDToString(task.RuntimeID)}
	if err != nil {
		slog.Warn("FC/E2B sandbox lifecycle", append(attrs, "error", err.Error())...)
		return
	}
	slog.Info("FC/E2B sandbox lifecycle", attrs...)
}

// fcE2BSandboxOrigin is the deployment host stamped on sandboxes it creates.
// Production and pre-release share one FC account.
func fcE2BSandboxOrigin(cfg FCE2BConfig) string {
	u, err := url.Parse(strings.TrimSpace(cfg.AppOrigin))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// TaskTerminal implements TaskRuntimeTerminalObserver. It returns at once:
// the caller is on the task transition path, while cleanup waits out a grace
// period, takes scope locks and makes provider round trips.
func (l *FCE2BLauncher) TaskTerminal(task db.AgentTaskQueue) {
	if l == nil || l.Pool == nil || !task.ID.Valid || !task.RuntimeID.Valid {
		return
	}
	// A cancelled or failed task's processes end right away; the sandbox
	// itself still follows the release below.
	l.scheduleAbortedTaskStop(task)
	taskKey := util.UUIDToString(task.ID)
	if _, inFlight := fcE2BSandboxReleasePending.LoadOrStore(taskKey, struct{}{}); inFlight {
		return
	}
	if fcE2BSandboxReleasePendingCount.Add(1) > fcE2BSandboxReleaseMaxPending {
		fcE2BSandboxReleasePendingCount.Add(-1)
		fcE2BSandboxReleasePending.Delete(taskKey)
		logFCE2BSandboxLifecycle(fcE2BSandboxRetained, "release_backlog_full", task, "", "", nil)
		return
	}
	go func() {
		defer func() {
			fcE2BSandboxReleasePendingCount.Add(-1)
			fcE2BSandboxReleasePending.Delete(taskKey)
		}()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("FC/E2B sandbox release panicked", "task_id", taskKey, "recovered", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), fcE2BSandboxReleaseGrace+fcE2BSandboxReleaseBudget)
		defer cancel()
		sleep := sleepWithContext
		if l.sleep != nil {
			sleep = l.sleep
		}
		if err := sleep(ctx, fcE2BSandboxReleaseGrace); err != nil {
			return
		}
		select {
		case fcE2BSandboxReleaseSlots <- struct{}{}:
			defer func() { <-fcE2BSandboxReleaseSlots }()
		case <-ctx.Done():
			logFCE2BSandboxLifecycle(fcE2BSandboxRetained, "release_slots_busy", task, "", "", nil)
			return
		}
		l.withCurrentConfig().releaseTaskSandboxes(ctx, task.ID)
	}()
}

// releaseTaskSandboxes handles every sandbox the task's start attempts used.
// It rereads the task: the event payload is not proof of a terminal state.
func (l *FCE2BLauncher) releaseTaskSandboxes(ctx context.Context, taskID pgtype.UUID) {
	if l == nil || l.Queries == nil || l.Pool == nil || !l.Config.Enabled {
		return
	}
	task, err := l.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		slog.Warn("FC/E2B sandbox release could not load task", "task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	if !fcE2BTaskIsTerminal(task.Status) {
		return
	}
	runtime, err := l.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil || !IsFCE2BRuntime(runtime) {
		return
	}
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		slog.Warn("FC/E2B sandbox release could not acquire a connection", "task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	defer conn.Release()
	sandboxes, err := taskSandboxIDs(ctx, conn, task.ID)
	if err != nil {
		slog.Warn("FC/E2B sandbox release could not list sandboxes", "task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	for _, sandboxID := range sandboxes {
		action, reason, scope, err := l.releaseTaskSandbox(ctx, conn, task, runtime, sandboxID)
		if err != nil {
			action = fcE2BSandboxReleaseFailed
		}
		logFCE2BSandboxLifecycle(action, reason, task, sandboxID, scope, err)
	}
}

// taskSandboxIDs lists every sandbox a start attempt of the task resolved.
// A retried launch can hold more than one.
func taskSandboxIDs(ctx context.Context, conn *pgxpool.Conn, taskID pgtype.UUID) ([]string, error) {
	rows, err := conn.Query(ctx, `SELECT DISTINCT sandbox_id FROM agent_task_runtime_start_attempt
 WHERE task_id=$1 AND sandbox_id<>''`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func fcE2BTaskIsTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

func (l *FCE2BLauncher) releaseTaskSandbox(ctx context.Context, conn *pgxpool.Conn, task db.AgentTaskQueue, rt db.AgentRuntime, sandboxID string) (fcE2BSandboxLifecycleAction, string, string, error) {
	key := dshhost.Key{WorkspaceID: uuid.UUID(rt.WorkspaceID.Bytes), AgentID: uuid.UUID(task.AgentID.Bytes)}
	var scopeID uuid.UUID
	err := conn.QueryRow(ctx, `SELECT scope_id FROM employee_filesystem_sandbox
 WHERE workspace_id=$1 AND agent_id=$2 AND sandbox_id=$3`, key.WorkspaceID, key.AgentID, sandboxID).Scan(&scopeID)
	if err == nil {
		return l.releaseEmployeeScopeSandbox(ctx, conn, task, rt, key, scopeID, sandboxID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", "employee", err
	}
	// The employee-wide host predates per-scope sandboxes and outlives tasks.
	var employeeHost bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM dsh_employee_host
 WHERE workspace_id=$1 AND agent_id=$2 AND sandbox_id=$3)`, key.WorkspaceID, key.AgentID, sandboxID).Scan(&employeeHost); err != nil {
		return "", "", "employee", err
	}
	if employeeHost {
		return fcE2BSandboxRetained, "employee_host", "employee", nil
	}
	return l.releaseEphemeralSandbox(ctx, conn, task, rt, sandboxID)
}

// releaseEmployeeScopeSandbox runs under the scope admission lock, which task
// launches hold from resolution until the sandbox is recorded on their start
// attempt. A launch that already reached this sandbox is therefore visible as
// unfinished work, and a later one reads the state this leaves behind.
func (l *FCE2BLauncher) releaseEmployeeScopeSandbox(ctx context.Context, conn *pgxpool.Conn, task db.AgentTaskQueue, rt db.AgentRuntime, key dshhost.Key, scopeID uuid.UUID, sandboxID string) (fcE2BSandboxLifecycleAction, string, string, error) {
	scope := "employee_" + dshExecutionScope(key, task).Kind
	lockKey := dshEmployeeLockKey(pgtype.UUID{Bytes: key.WorkspaceID, Valid: true}, pgtype.UUID{Bytes: scopeID, Valid: true})
	release, locked, err := tryFCE2BAdvisoryLock(ctx, conn, employeeFilesystemLockClass, lockKey, "filesystem sandbox scope")
	if err != nil {
		return "", "", scope, err
	}
	if !locked {
		return fcE2BSandboxRetained, "scope_admitting", scope, nil
	}
	defer release()
	store := dshhost.FilesystemSandboxStore{DB: conn, ScopeID: scopeID}
	host, err := store.Get(ctx, key)
	if err != nil {
		return "", "", scope, err
	}
	if host.State != "running" || host.SandboxID != sandboxID {
		return fcE2BSandboxRetained, "scope_changed", scope, nil
	}
	var taskBusy bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM agent_task_queue t JOIN agent a ON a.id=t.agent_id
 WHERE a.workspace_id=$1 AND t.agent_id=$2 AND t.id<>$3
 AND t.status IN ('queued','dispatched','running','waiting_local_directory')
 AND EXISTS (SELECT 1 FROM agent_task_runtime_start_attempt s WHERE s.task_id=t.id AND s.sandbox_id=$4))`,
		key.WorkspaceID, key.AgentID, task.ID, sandboxID).Scan(&taskBusy); err != nil {
		return "", "", scope, err
	}
	if taskBusy {
		return fcE2BSandboxRetained, "in_use", scope, nil
	}
	// DSH tasks retain their existing idle window for follow-up execution.
	if FCE2BRuntimeProvider(rt) != "dsh" && dshScopeIsSingleUse(key, task, scopeID) {
		provider, err := l.dshHostProvider(host.Storage)
		if err != nil {
			return "", "single_use_scope", scope, err
		}
		if err := (dshhost.Manager{Store: store, Provider: provider}).Retire(ctx, key, host.Generation); err != nil {
			return "", "single_use_scope", scope, err
		}
		return fcE2BSandboxReleased, "single_use_scope", scope, nil
	}
	action, reason, _, err := l.trimSandboxIdleLifetime(ctx, conn, pgtype.UUID{}, sandboxID, scope)
	return action, reason, scope, err
}

// dshScopeIsSingleUse: only an ordinary task with neither an issue nor a chat
// owns a scope that no later task joins. Scheduled work may continue the scope
// of an earlier native conversation, so it keeps the idle window instead.
func dshScopeIsSingleUse(key dshhost.Key, task db.AgentTaskQueue, scopeID uuid.UUID) bool {
	scope := dshExecutionScope(key, task)
	return scope.Kind == "task" && task.TriggerEvidenceKind.String != dshschedule.EvidenceKind &&
		scopeID == employeeFilesystemScopeID(scope)
}

// releaseEphemeralSandbox covers sandboxes outside the employee filesystem.
// A launch holds the scope lock from session lookup through renewal but
// records the sandbox on its attempt only afterwards, so unfinished tasks on
// the same scope count as users even before that record exists.
func (l *FCE2BLauncher) releaseEphemeralSandbox(ctx context.Context, conn *pgxpool.Conn, task db.AgentTaskQueue, rt db.AgentRuntime, sandboxID string) (fcE2BSandboxLifecycleAction, string, string, error) {
	scope, scoped := fcE2BScopeForTask(task)
	scopeName := "task"
	if scoped {
		scopeName = scope.typ
		release, locked, err := tryFCE2BAdvisoryLock(ctx, conn, fcE2BSandboxLockClass, fcE2BScopeLockKey(rt.ID, scope), "sandbox")
		if err != nil {
			return "", "", scopeName, err
		}
		if !locked {
			return fcE2BSandboxRetained, "scope_admitting", scopeName, nil
		}
		defer release()
	}
	var busy, cached bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent_task_queue t
 WHERE t.runtime_id=$1 AND t.id<>$2 AND t.status IN ('queued','dispatched','running','waiting_local_directory')
 AND (EXISTS (SELECT 1 FROM agent_task_runtime_start_attempt s WHERE s.task_id=t.id AND s.sandbox_id=$3)
 OR ($4='chat' AND t.chat_session_id=$5)
 OR ($4='issue' AND t.chat_session_id IS NULL AND t.issue_id=$5))),
 EXISTS (SELECT 1 FROM fc_e2b_sandbox_session
 WHERE runtime_id=$1 AND sandbox_id=$3 AND sandbox_backend='aliyun_fc' AND status='running')`,
		rt.ID, task.ID, sandboxID, scope.typ, scope.id).Scan(&busy, &cached); err != nil {
		return "", "", scopeName, err
	}
	if busy {
		return fcE2BSandboxRetained, "in_use", scopeName, nil
	}
	if !scoped && !cached {
		// Without an issue or chat nothing caches this sandbox for reuse.
		if _, err := l.sandboxLifetimeRequest(ctx, http.MethodDelete, sandboxID, "", nil); err != nil {
			return "", "single_use_scope", scopeName, err
		}
		return fcE2BSandboxReleased, "single_use_scope", scopeName, nil
	}
	return l.trimSandboxIdleLifetime(ctx, conn, rt.ID, sandboxID, scopeName)
}

// trimSandboxIdleLifetime shortens the remaining lifetime to the idle window.
// A launch that reuses the sandbox renews it to the full task lifetime again.
func (l *FCE2BLauncher) trimSandboxIdleLifetime(ctx context.Context, conn *pgxpool.Conn, runtimeID pgtype.UUID, sandboxID, scope string) (fcE2BSandboxLifecycleAction, string, string, error) {
	body, err := json.Marshal(map[string]int64{"timeout": int64(fcE2BSandboxIdleRetention / time.Second)})
	if err != nil {
		return "", "idle_window", scope, err
	}
	if _, err := l.sandboxLifetimeRequest(ctx, http.MethodPost, sandboxID, "/timeout", body); err != nil {
		if errors.Is(err, errFCE2BSandboxGone) {
			return fcE2BSandboxReleased, "already_gone", scope, nil
		}
		return "", "idle_window", scope, err
	}
	if runtimeID.Valid {
		// Keep the reuse cache consistent with the provider, so a launch after
		// the window creates a replacement instead of probing a dead sandbox.
		// Reuse writes the renewed expiry back, so this never hides a live one.
		if _, err := conn.Exec(ctx, `UPDATE fc_e2b_sandbox_session
 SET expires_at=LEAST(expires_at, now()+make_interval(secs => $3)), updated_at=now()
 WHERE runtime_id=$1 AND sandbox_id=$2 AND sandbox_backend='aliyun_fc' AND status='running'`,
			runtimeID, sandboxID, fcE2BSandboxIdleRetention.Seconds()); err != nil {
			return "", "idle_window", scope, err
		}
	}
	return fcE2BSandboxIdleTrimmed, "idle_window", scope, nil
}

// tryFCE2BAdvisoryLock takes a session lock only when it is free. Release
// never waits behind a launch: a launch holding the scope renews the sandbox
// itself, so skipping is always safe.
func tryFCE2BAdvisoryLock(ctx context.Context, conn *pgxpool.Conn, class, key int32, name string) (func(), bool, error) {
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1,$2)", class, key).Scan(&locked); err != nil {
		return nil, false, err
	}
	if !locked {
		return nil, false, nil
	}
	return func() { releaseFCE2BAdvisoryLock(conn, false, class, key, name) }, true, nil
}
