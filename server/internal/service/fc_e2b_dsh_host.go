package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshprofile"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/wsfs"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const DSHEmployeeHostCapability = "dsh_employee_host_v1"
const dshEmployeeLockClass int32 = 0x44534831
const employeeFilesystemLockClass int32 = 0x46535331

// Keep the platform deadline outside the Runtime's bounded readiness window.
// Runtime initialization defers history IO; independent session hosts start concurrently.
const dshNativeStartupTimeout = 320 * time.Second

var errDSHHostWaiting = errors.New("DSH employee host is awaiting reconciliation or task drain")
var errDSHHostStartup = errors.New("DSH employee native startup failed")

type dshHostWaitError struct {
	reason string
}

func (e dshHostWaitError) Error() string {
	if e.reason == "" {
		return errDSHHostWaiting.Error()
	}
	return errDSHHostWaiting.Error() + ": " + e.reason
}

func (e dshHostWaitError) Unwrap() error { return errDSHHostWaiting }

func waitDSHHost(reason string) error {
	return dshHostWaitError{reason: reason}
}

func (l *FCE2BLauncher) deferDSHHostWaiting(ctx context.Context, exec interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, task db.AgentTaskQueue, attempt db.AgentTaskRuntimeStartAttempt, waitErr error) (fcE2BLaunchSubmission, bool, error) {
	if _, recordErr := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "dsh_host_waiting"); recordErr != nil {
		return fcE2BLaunchSubmission{}, false, recordErr
	}
	reason := dshWaitReason(waitErr)
	if exec != nil {
		if _, err := exec.Exec(ctx, `UPDATE agent_task_queue SET wait_reason=$2 WHERE id=$1 AND status='queued'`, task.ID, reason); err != nil {
			slog.Warn("dsh host wait_reason write failed", "task_id", util.UUIDToString(task.ID), "error", err)
		}
	}
	slog.Info("FC/E2B launch deferred by DSH host wait",
		"event", "fc_e2b_launch_deferred",
		"task_id", util.UUIDToString(task.ID),
		"runtime_id", util.UUIDToString(task.RuntimeID),
		"reason", reason,
	)
	return fcE2BLaunchSubmission{}, true, nil
}

func dshWaitReason(err error) string {
	var wait dshHostWaitError
	if errors.As(err, &wait) && wait.reason != "" {
		return wait.reason
	}
	switch {
	case strings.Contains(err.Error(), dshhost.WaitSandboxUnhealthy):
		return dshhost.WaitSandboxUnhealthy
	case strings.Contains(err.Error(), dshhost.WaitDestroyUnconfirmed):
		return dshhost.WaitDestroyUnconfirmed
	case strings.Contains(err.Error(), dshhost.WaitCreateIntentStale):
		return dshhost.WaitCreateIntentStale
	case strings.Contains(err.Error(), dshhost.WaitNativeGrantBusy):
		return dshhost.WaitNativeGrantBusy
	case strings.Contains(err.Error(), dshhost.WaitTaskDrainBusy):
		return dshhost.WaitTaskDrainBusy
	}
	return "dsh_host_waiting"
}

// sharedLaunchKeep reuses the historical private ensure path.
// sharedLaunchOffer may attach a shared mount only when the sandbox is created.
// sharedLaunchRevoke drops a shared mount after an explicit none grant.
// sharedLaunchConstrain compares a read grant with a mount the sandbox already
// has. It must not attach a shared volume to a newly created sandbox.
type sharedLaunchMode int

const (
	sharedLaunchKeep sharedLaunchMode = iota
	sharedLaunchOffer
	sharedLaunchRevoke
	sharedLaunchConstrain
)

// classifySharedLaunch separates "not capable", "not ready", and "revoked".
// A prepare failure must not retire a healthy sandbox. An explicit revoke must.
// A read grant still has to be compared with an existing mount when the image
// has not declared shared disk; that flag only blocks a new mount.
// Unknown create results are handled by the creating fence, not by this mode.
func classifySharedLaunch(capable bool, decision wsfs.MountDecision, notReady bool) sharedLaunchMode {
	if decision.Revoked {
		return sharedLaunchRevoke
	}
	if notReady || decision.Shared == nil {
		return sharedLaunchKeep
	}
	if !capable {
		if decision.Access == wsfs.AccessRead {
			return sharedLaunchConstrain
		}
		return sharedLaunchKeep
	}
	return sharedLaunchOffer
}

func sharedTarget(decision wsfs.MountDecision) dshhost.SharedTarget {
	volume := ""
	if decision.Shared != nil {
		volume = decision.Shared.Name
	}
	other := decision.RWVolume
	if decision.Access == wsfs.AccessWrite {
		other = decision.ROVolume
	}
	return dshhost.SharedTarget{
		Access:          decision.Access,
		Volume:          volume,
		OtherVolume:     other,
		RoleARN:         decision.RoleARN,
		GrantGeneration: decision.GrantGeneration,
	}
}

func adoptedSharedTarget(mode sharedLaunchMode, decision wsfs.MountDecision) *dshhost.SharedTarget {
	switch mode {
	case sharedLaunchRevoke:
		target := dshhost.SharedTarget{Access: "none", GrantGeneration: decision.GrantGeneration}
		return &target
	case sharedLaunchOffer, sharedLaunchConstrain:
		if decision.Shared == nil || decision.Shared.Name == "" || decision.RoleARN == "" {
			return nil
		}
		target := sharedTarget(decision)
		if mode == sharedLaunchConstrain {
			target.Access = wsfs.AccessRead
		}
		return &target
	default:
		return nil
	}
}

func retireUnusedSharedCandidate(ctx context.Context, conn *pgxpool.Conn, manager dshhost.Manager, key dshhost.Key, host dshhost.Host, workspaceID, excludeTask pgtype.UUID, scopeID uuid.UUID) error {
	var taskBusy, nativeBusy bool
	err := conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM agent_task_queue t JOIN agent a ON a.id=t.agent_id
 WHERE a.workspace_id=$1 AND t.agent_id=$2 AND t.id IS DISTINCT FROM $3::uuid
 AND ((t.status IN ('dispatched','running','waiting_local_directory') AND ($5::uuid='00000000-0000-0000-0000-000000000000'::uuid OR EXISTS
 (SELECT 1 FROM agent_task_runtime_start_attempt active WHERE active.task_id=t.id AND active.sandbox_id=$4))) OR
 (t.status='queued' AND EXISTS (SELECT 1 FROM agent_task_runtime_start_attempt s
 WHERE s.task_id=t.id AND s.sandbox_id=$4 AND s.status IN ('starting','claimed')))))`,
		workspaceID, pgtype.UUID{Bytes: key.AgentID, Valid: true}, excludeTask, host.SandboxID, scopeID).Scan(&taskBusy)
	if err != nil {
		return err
	}
	if taskBusy {
		return waitDSHHost(dshhost.WaitTaskDrainBusy)
	}
	err = conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM dsh_native_access n
 WHERE n.workspace_id=$1 AND n.agent_id=$2 AND n.sandbox_id=$3 AND n.generation=$4
 AND n.parent_access_id IS NULL AND n.kind IN ('entry','session') AND n.expires_at>now())`,
		workspaceID, pgtype.UUID{Bytes: key.AgentID, Valid: true}, host.SandboxID, host.Generation).Scan(&nativeBusy)
	if err != nil {
		return err
	}
	if nativeBusy {
		return waitDSHHost(dshhost.WaitNativeGrantBusy)
	}
	if err = manager.RetireUnlessBusy(ctx, key, host.Generation, func(retired dshhost.Host) (bool, error) {
		var granted bool
		qErr := conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM dsh_native_access n
 WHERE n.workspace_id=$1 AND n.agent_id=$2 AND n.sandbox_id=$3 AND n.generation=$4
 AND n.parent_access_id IS NULL AND n.kind IN ('entry','session') AND n.expires_at>now())`,
			workspaceID, pgtype.UUID{Bytes: key.AgentID, Valid: true}, retired.SandboxID, retired.Generation).Scan(&granted)
		return granted, qErr
	}); err != nil {
		reason := dshWaitReason(err)
		if reason == "dsh_host_waiting" {
			reason = dshhost.WaitDestroyUnconfirmed
		}
		return waitDSHHost(reason)
	}
	return nil
}

func (l *FCE2BLauncher) workspaceMountDecision(ctx context.Context, conn wsfs.Database, key dshhost.Key, before *dshhost.Host, metadata []byte) (decision wsfs.MountDecision, mode sharedLaunchMode) {
	decision = wsfs.MountDecision{Private: before, RoleARN: before.RoleARN}
	if l.ReadWorkspaceMount == nil {
		return decision, sharedLaunchKeep
	}
	// Always read the grant. The capability flag may block a new shared offer.
	// It must not hide an explicit revoke or a narrower grant.
	got, err := l.ReadWorkspaceMount(ctx, conn, key.WorkspaceID, key.AgentID, before)
	if err != nil {
		slog.Warn("workspace filesystem mount not ready; launching without the shared mount",
			"error", err,
			"workspace_id", key.WorkspaceID,
			"agent_id", key.AgentID,
		)
		return decision, sharedLaunchKeep
	}
	if got.Private == nil {
		got.Private = before
	}
	if got.RoleARN == "" {
		got.RoleARN = before.RoleARN
	}
	return got, classifySharedLaunch(wsfs.DeclaresSharedDisk(metadata), got, false)
}

var dshAccessPointPattern = regexp.MustCompile(`^acs:nas:[a-z0-9-]+:[0-9]+:accesspoint/(ap-[a-z0-9]+)$`)

func employeeFilesystemScopeID(scope dshhost.SessionScope) uuid.UUID {
	return uuid.NewSHA1(scope.AgentID, []byte(scope.Kind+":"+scope.ID.String()))
}

func lockEmployeeFilesystemScope(ctx context.Context, conn *pgxpool.Conn, identity dshhost.Key, scopeID uuid.UUID) (func(), error) {
	if conn == nil || identity.WorkspaceID == uuid.Nil || identity.AgentID == uuid.Nil || scopeID == uuid.Nil {
		return nil, errors.New("filesystem scope coordination requires a database and identity")
	}
	key := dshEmployeeLockKey(pgtype.UUID{Bytes: identity.WorkspaceID, Valid: true}, pgtype.UUID{Bytes: scopeID, Valid: true})
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1,$2)", employeeFilesystemLockClass, key); err != nil {
		return nil, fmt.Errorf("lock filesystem sandbox scope: %w", err)
	}
	return func() {
		releaseFCE2BAdvisoryLock(conn, false, employeeFilesystemLockClass, key, "filesystem sandbox scope")
	}, nil
}

func dshExecutionScope(key dshhost.Key, task db.AgentTaskQueue) dshhost.SessionScope {
	// Match task serialization precedence. Unscoped/autopilot tasks get their
	// own Session instead of accidentally joining an unrelated conversation.
	if task.IssueID.Valid {
		return dshhost.SessionScope{Key: key, Kind: "issue", ID: uuid.UUID(task.IssueID.Bytes)}
	}
	if task.ChatSessionID.Valid {
		return dshhost.SessionScope{Key: key, Kind: "chat", ID: uuid.UUID(task.ChatSessionID.Bytes)}
	}
	return dshhost.SessionScope{Key: key, Kind: "task", ID: uuid.UUID(task.ID.Bytes)}
}

func dshEmployeeLockKey(workspace, agent pgtype.UUID) int32 {
	h := fnv.New32a()
	_, _ = h.Write(workspace.Bytes[:])
	_, _ = h.Write(agent.Bytes[:])
	return int32(h.Sum32())
}

func lockDSHEmployee(ctx context.Context, conn *pgxpool.Conn, workspace, agent pgtype.UUID) (func(), error) {
	if conn == nil || !workspace.Valid || !agent.Valid {
		return nil, errors.New("DSH employee coordination requires a database connection and identity")
	}
	key := dshEmployeeLockKey(workspace, agent)
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1,$2)", dshEmployeeLockClass, key); err != nil {
		return nil, fmt.Errorf("lock DSH employee: %w", err)
	}
	return func() { releaseFCE2BAdvisoryLock(conn, false, dshEmployeeLockClass, key, "DSH employee") }, nil
}

// EnsureDSHEmployeeHost starts or recovers the employee writer for a human
// native entry. The caller checks management permission before this operation
// and again when issuing access. No task or runner is fabricated for UI startup.
func (l *FCE2BLauncher) EnsureDSHEmployeeHost(ctx context.Context, key dshhost.Key) (dshhost.Host, error) {
	l = l.withCurrentConfig()
	var host dshhost.Host
	err := l.withDSHEmployee(ctx, key, func(conn *pgxpool.Conn, runtime db.AgentRuntime, template string) error {
		// Prefer the most recently used session host, so an entry opened after
		// a platform task observes the same native process and live events.
		var scopeID uuid.UUID
		err := conn.QueryRow(ctx, `SELECT scope_id FROM employee_filesystem_sandbox
 WHERE workspace_id=$1 AND agent_id=$2 ORDER BY updated_at DESC,scope_id LIMIT 1`, key.WorkspaceID, key.AgentID).Scan(&scopeID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if scopeID != uuid.Nil {
			release, err := lockEmployeeFilesystemScope(ctx, conn, key, scopeID)
			if err != nil {
				return err
			}
			defer release()
		}
		host, _, err = l.resolveFilesystemScopeSandbox(ctx, key, scopeID, pgtype.UUID{}, runtime, template, conn, chattrace.New("dsh_native_entry"))
		return err
	})
	return host, err
}

// All human Home/Profile operations use the same Runtime-to-employee lock order
// as task admission. The operation runs against the reloaded current binding.
func (l *FCE2BLauncher) withDSHEmployee(ctx context.Context, key dshhost.Key, operation func(*pgxpool.Conn, db.AgentRuntime, string) error) error {
	if l == nil || l.Queries == nil || l.Pool == nil || !l.Config.Enabled || l.nativeAuthority == nil || key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil {
		return errors.New("DSH employee startup is unavailable")
	}
	if err := l.Config.Validate(); err != nil {
		return err
	}
	params := db.GetAgentInWorkspaceParams{ID: pgtype.UUID{Bytes: key.AgentID, Valid: true}, WorkspaceID: pgtype.UUID{Bytes: key.WorkspaceID, Valid: true}}
	agent, err := l.Queries.GetAgentInWorkspace(ctx, params)
	if err != nil || !agent.RuntimeID.Valid {
		return errors.New("DSH employee runtime is unavailable")
	}
	// Use the platform launch lock order: shared Runtime, then employee. Reload
	// the binding on this connection so a waiting entry cannot select stale state.
	conn, releaseRuntime, err := l.lockRuntimeShared(ctx, agent.RuntimeID)
	if err != nil {
		return err
	}
	defer releaseRuntime()
	releaseEmployee, err := lockDSHEmployee(ctx, conn, params.WorkspaceID, params.ID)
	if err != nil {
		return err
	}
	defer releaseEmployee()
	queries := db.New(conn)
	current, err := queries.GetAgentInWorkspace(ctx, params)
	if err != nil || current.RuntimeID != agent.RuntimeID || current.ArchivedAt.Valid || current.RuntimeMode != "cloud" {
		return errors.New("DSH employee binding changed")
	}
	runtime, err := queries.GetAgentRuntime(ctx, current.RuntimeID)
	if err != nil || runtime.WorkspaceID != params.WorkspaceID || runtime.Provider != "dsh" || !IsFCE2BRuntime(runtime) || !runtime.OwnerID.Valid || !runtime.DaemonID.Valid || strings.TrimSpace(runtime.DaemonID.String) == "" {
		return errors.New("DSH employee requires an FC DSH runtime")
	}
	template, err := fcE2BTemplateForRuntime(runtime)
	if err != nil {
		return err
	}
	return operation(conn, runtime, template)
}

// The caller holds the execution-scope admission lock through runner submission.
// Native entries pass no excluded task, so every admitted task blocks retirement.
// Storage operations reuse that connection, even when the pool has size one.
func (l *FCE2BLauncher) resolveEmployeeFilesystemSandbox(ctx context.Context, key dshhost.Key, excludeTask pgtype.UUID, rt db.AgentRuntime, template string, conn *pgxpool.Conn, trace chattrace.Trace) (dshhost.Host, bool, error) {
	return l.resolveFilesystemScopeSandbox(ctx, key, uuid.Nil, excludeTask, rt, template, conn, trace)
}

func (l *FCE2BLauncher) resolveFilesystemScopeSandbox(ctx context.Context, key dshhost.Key, scopeID uuid.UUID, excludeTask pgtype.UUID, rt db.AgentRuntime, template string, conn *pgxpool.Conn, trace chattrace.Trace) (dshhost.Host, bool, error) {
	if conn == nil || !rt.WorkspaceID.Valid || key.WorkspaceID != uuid.UUID(rt.WorkspaceID.Bytes) || key.AgentID == uuid.Nil {
		return dshhost.Host{}, false, errors.New("invalid DSH employee launch identity")
	}
	isDSH := FCE2BRuntimeProvider(rt) == "dsh"
	var catalog, profileDigest string
	var store dshhost.Store = dshhost.PostgresStore{DB: conn}
	if scopeID != uuid.Nil {
		scopedStore := dshhost.FilesystemSandboxStore{DB: conn, ScopeID: scopeID}
		if _, err := scopedStore.Bind(ctx, key); err != nil {
			return dshhost.Host{}, false, fmt.Errorf("bind employee filesystem sandbox: %w", err)
		}
		store = scopedStore
	}
	before, err := store.Get(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return dshhost.Host{}, false, errors.New("DSH employee storage has not been provisioned")
	}
	if err != nil {
		return dshhost.Host{}, false, err
	}
	profiles := dshprofile.Store{DB: conn}
	var revision dshprofile.Revision
	if isDSH {
		catalog, profileDigest, err = dshManagedCatalog(l.Config.LLMModels)
		if err != nil {
			return dshhost.Host{}, false, err
		}
		if l.ReadDSHProfileSource == nil {
			err = errors.New("DSH employee Profile source unavailable")
		} else {
			revision, err = profiles.Prepare(ctx, key, template, l.ReadDSHProfileSource)
		}
		if err != nil {
			if before.State != "creating" && before.State != "retiring" {
				return dshhost.Host{}, false, err
			}
			// Invalid new settings cannot strand an uncertain create or prevent
			// confirmed retirement. An empty descriptor forbids any new admission.
			revision = dshprofile.Revision{}
		}
		if revision.Descriptor == "" && before.State != "creating" && before.State != "retiring" {
			return dshhost.Host{}, false, errDSHHostWaiting
		}
	}
	provider, err := l.dshHostProvider(before.Storage)
	if err != nil {
		return dshhost.Host{}, false, err
	}
	manager := dshhost.Manager{Store: store, Provider: provider}
	decision, launchMode := l.workspaceMountDecision(ctx, conn, key, &before, rt.Metadata)
	ensureHost := func() (dshhost.Host, error) {
		switch launchMode {
		case sharedLaunchRevoke:
			return manager.EnsurePrivate(ctx, key, template)
		case sharedLaunchOffer:
			if decision.Shared != nil && decision.RoleARN != "" {
				return manager.EnsureWithSharedGrant(ctx, key, template, sharedTarget(decision))
			}
		case sharedLaunchConstrain:
			if decision.Shared != nil && decision.Shared.Name != "" && decision.RoleARN != "" {
				target := sharedTarget(decision)
				target.Access = wsfs.AccessRead
				h, err := manager.Store.Get(ctx, key)
				if err != nil {
					return dshhost.Host{}, err
				}
				if h.State == "running" {
					return manager.EnsureWithSharedGrant(ctx, key, template, target)
				}
				return manager.Ensure(ctx, key, template)
			}
		}
		return manager.Ensure(ctx, key, template)
	}
	ensureWithFallback := func() (dshhost.Host, error) {
		host, err := ensureHost()
		if errors.Is(err, dshhost.ErrCreateRejected) && decision.Shared != nil {
			// A shared-mount create FC refuses must not cost the task: the
			// rejected intent is already released, so create private-only.
			slog.Warn("shared-mount sandbox create rejected; launching without the shared mount",
				"error", err,
				"workspace_id", key.WorkspaceID,
				"agent_id", key.AgentID,
			)
			decision = wsfs.MountDecision{Private: &before, RoleARN: before.RoleARN}
			launchMode = sharedLaunchKeep
			return manager.Ensure(ctx, key, template)
		}
		return host, err
	}
	var host dshhost.Host
	if before.State == "creating" {
		host, err = manager.ReconcileCreate(ctx, key)
		if err != nil {
			slog.Info("dsh host waiting", "reason", dshhost.WaitCreateIntentStale, "workspace_id", key.WorkspaceID, "agent_id", key.AgentID, "generation", before.Generation, "sandbox_id", before.SandboxID)
			return dshhost.Host{}, false, waitDSHHost(dshhost.WaitCreateIntentStale)
		}
		host, err = manager.AuthorizeRunning(ctx, host, adoptedSharedTarget(launchMode, decision))
	} else if before.State == "retiring" {
		err = dshhost.ErrRetireRequired
	} else {
		host, err = ensureWithFallback()
	}
	// A creating host adopted above already has its sandbox. Drain checks and
	// logs must use it, not the empty pre-adoption sandbox ID.
	retireSandboxID := before.SandboxID
	if before.State == "creating" && host.SandboxID != "" {
		retireSandboxID = host.SandboxID
	}
	if errors.Is(err, dshhost.ErrRetireRequired) || (err == nil && host.TemplateID != template) {
		if errors.Is(err, dshhost.ErrRetireRequired) && strings.Contains(err.Error(), dshhost.WaitSandboxUnhealthy) {
			slog.Info("dsh host waiting", "reason", dshhost.WaitSandboxUnhealthy, "workspace_id", key.WorkspaceID, "agent_id", key.AgentID, "generation", before.Generation, "sandbox_id", retireSandboxID)
		}
		var taskBusy, nativeBusy bool
		// A submitted background runner can still be queued before claiming.
		// Treat its persisted sandbox receipt as active admission, too.
		// Live native grants also reserve a healthy generation until revoked or
		// expired. Advisory-lock waiters are not retire signals.
		err = conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM agent_task_queue t JOIN agent a ON a.id=t.agent_id
 WHERE a.workspace_id=$1 AND t.agent_id=$2 AND t.id IS DISTINCT FROM $3::uuid
 AND ((t.status IN ('dispatched','running','waiting_local_directory') AND ($5::uuid='00000000-0000-0000-0000-000000000000'::uuid OR EXISTS
 (SELECT 1 FROM agent_task_runtime_start_attempt active WHERE active.task_id=t.id AND active.sandbox_id=$4))) OR
 (t.status='queued' AND EXISTS (SELECT 1 FROM agent_task_runtime_start_attempt s
 WHERE s.task_id=t.id AND s.sandbox_id=$4 AND s.status IN ('starting','claimed')))))`,
			rt.WorkspaceID, pgtype.UUID{Bytes: key.AgentID, Valid: true}, excludeTask, retireSandboxID, scopeID).Scan(&taskBusy)
		if err != nil {
			return dshhost.Host{}, false, err
		}
		if before.State == "running" {
			err = conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM dsh_native_access n
 WHERE n.workspace_id=$1 AND n.agent_id=$2 AND n.sandbox_id=$3 AND n.generation=$4
 AND n.parent_access_id IS NULL AND n.kind IN ('entry','session') AND n.expires_at>now())`,
				rt.WorkspaceID, pgtype.UUID{Bytes: key.AgentID, Valid: true}, before.SandboxID, before.Generation).Scan(&nativeBusy)
			if err != nil {
				return dshhost.Host{}, false, err
			}
		}
		if nativeBusy {
			slog.Info("dsh host waiting", "reason", dshhost.WaitNativeGrantBusy, "workspace_id", key.WorkspaceID, "agent_id", key.AgentID, "generation", before.Generation, "sandbox_id", retireSandboxID)
			return dshhost.Host{}, false, waitDSHHost(dshhost.WaitNativeGrantBusy)
		}
		if taskBusy {
			slog.Info("dsh host waiting", "reason", dshhost.WaitTaskDrainBusy, "workspace_id", key.WorkspaceID, "agent_id", key.AgentID, "generation", before.Generation, "sandbox_id", retireSandboxID)
			return dshhost.Host{}, false, waitDSHHost(dshhost.WaitTaskDrainBusy)
		}
		current, loadErr := store.Get(ctx, key)
		if loadErr != nil {
			return dshhost.Host{}, false, loadErr
		}
		if err = manager.RetireUnlessBusy(ctx, key, current.Generation, func(retired dshhost.Host) (bool, error) {
			var granted bool
			qErr := conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM dsh_native_access n
 WHERE n.workspace_id=$1 AND n.agent_id=$2 AND n.sandbox_id=$3 AND n.generation=$4
 AND n.parent_access_id IS NULL AND n.kind IN ('entry','session') AND n.expires_at>now())`,
				rt.WorkspaceID, pgtype.UUID{Bytes: key.AgentID, Valid: true}, retired.SandboxID, retired.Generation).Scan(&granted)
			return granted, qErr
		}); err != nil {
			reason := dshWaitReason(err)
			if reason == "dsh_host_waiting" {
				reason = dshhost.WaitDestroyUnconfirmed
			}
			slog.Info("dsh host waiting", "reason", reason, "workspace_id", key.WorkspaceID, "agent_id", key.AgentID, "generation", current.Generation, "sandbox_id", current.SandboxID)
			return dshhost.Host{}, false, waitDSHHost(reason)
		}
		if isDSH && revision.Descriptor == "" {
			return dshhost.Host{}, false, errDSHHostWaiting
		}
		host, err = ensureWithFallback()
	}
	if errors.Is(err, dshhost.ErrPending) || errors.Is(err, dshhost.ErrChanged) {
		reason := dshWaitReason(err)
		// The error names the provider outcome (an HTTP status, never a body),
		// which is the only record of why a create was rejected.
		slog.Info("dsh host waiting", "reason", reason, "workspace_id", key.WorkspaceID, "agent_id", key.AgentID, "error", err)
		return dshhost.Host{}, false, waitDSHHost(reason)
	}
	if err != nil {
		return dshhost.Host{}, false, err
	}
	if isDSH && revision.Descriptor == "" {
		return dshhost.Host{}, false, errDSHHostWaiting
	}
	cold := before.State != "running" || host.SandboxID != before.SandboxID
	if cold {
		err = l.waitSandboxReady(ctx, host.SandboxID)
	} else {
		err = l.checkSandboxReady(ctx, host.SandboxID)
	}
	if err != nil && cold && launchMode == sharedLaunchOffer && decision.Shared != nil {
		// The shared candidate was created for this call and is not executable.
		// Confirm it is unused and gone, then create one private sandbox.
		// A healthy host is not on this path. An unconfirmed destroy does not
		// start a second sandbox.
		if retireErr := retireUnusedSharedCandidate(ctx, conn, manager, key, host, rt.WorkspaceID, excludeTask, scopeID); retireErr != nil {
			return dshhost.Host{}, true, retireErr
		}
		var privateHost dshhost.Host
		privateHost, err = manager.Ensure(ctx, key, template)
		if err != nil {
			return dshhost.Host{}, true, err
		}
		host = privateHost
		err = l.waitSandboxReady(ctx, host.SandboxID)
	}
	if err != nil {
		return dshhost.Host{}, cold, err
	}
	if _, err = l.renewEmployeeHostSandbox(ctx, host.SandboxID, trace); err != nil {
		return dshhost.Host{}, cold, err
	}
	args, err := dshHomePrepareArgs(host)
	if err != nil {
		return dshhost.Host{}, cold, err
	}
	out, err := l.runE2BCommand(ctx, args)
	if err != nil {
		slog.Warn("DSH employee Home initialization command failed",
			"sandbox_id", host.SandboxID,
			"workspace_id", host.WorkspaceID,
			"agent_id", host.AgentID,
			"generation", host.Generation,
			"error", err,
			"output", strings.TrimSpace(out),
		)
		return dshhost.Host{}, cold, fmt.Errorf("DSH employee Home initialization failed: %w", err)
	}
	if err = validateDSHHomeReceipt(out, host); err != nil {
		return dshhost.Host{}, cold, err
	}
	if !isDSH {
		chattrace.LogStage(slog.Default(), trace, "employee_filesystem", "ready", "sandbox_id", host.SandboxID, "generation", host.Generation, "agent_id", host.AgentID.String(), "provider", FCE2BRuntimeProvider(rt))
		return host, cold, nil
	}
	// Plugin snapshots are imported by the configuration worker and settings
	// endpoints. Task admission consumes a durable revision, never another
	// session's live Host availability.
	origin, authority, err := dshNativeGatewayAddress(l.Config, host)
	if err != nil {
		return dshhost.Host{}, cold, err
	}
	// An unchanged healthy host already owns this exact immutable Profile.
	// Reopening it needs a live receipt, not another artifact delivery/staging pass.
	status, statusErr := profiles.Status(ctx, key)
	reusedProfile := statusErr == nil && status.Current && status.AppliedSandboxID == host.SandboxID && status.AppliedGeneration == host.Generation && status.AppliedRevision == strconv.FormatInt(revision.ID, 10)
	if !reusedProfile && !cold {
		// The employee acknowledgement records only the last sandbox. Another
		// session may have acknowledged the same revision since this Host did.
		// Prove this Host's live composition before treating its UI grant as a
		// pending configuration change. This probe never starts or stages DSH.
		reusedProfile = l.dshNativeHostHasProfile(ctx, host, profileDigest, revision)
	}
	if !reusedProfile {
		// Native market edits are already hot-loaded in this Host. Importing
		// them publishes the next immutable Profile for new task sandboxes;
		// it must not restart the browser's Host underneath its live grant.
		// --ensure rejects a changed Profile, so waiting must happen before
		// staging/ensure rather than turning that expected change into retirement.
		var nativeActive bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM dsh_native_access
 WHERE workspace_id=$1 AND agent_id=$2 AND sandbox_id=$3 AND generation=$4
 AND parent_access_id IS NULL AND kind IN ('entry','session') AND expires_at>now())`,
			key.WorkspaceID, key.AgentID, host.SandboxID, host.Generation).Scan(&nativeActive); err != nil {
			return dshhost.Host{}, cold, err
		}
		if nativeActive {
			return dshhost.Host{}, cold, errDSHHostWaiting
		}
		err = l.deliverDSHProfile(ctx, profiles, host, revision)
		if err == nil {
			err = l.stageDSHProfile(ctx, host, revision)
		}
	}
	if err == nil {
		// The execution-scope admission lock already protects this session's
		// writer. An employee-wide startup lock makes unrelated sessions wait
		// behind one slow cold start and imposes the reconciliation retry delay
		// on healthy hosts too. Preserve task-level concurrency here.
		out, err = l.runE2BCommandWithTimeout(ctx, dshNativeStartupTimeout, dshNativeHostEnsureArgs(host, catalog, authority, origin, l.nativeAuthority.publicKey(), revision))
	}
	if err == nil {
		err = validateDSHNativeHostReceipt(out, host, profileDigest, revision)
	}
	if err != nil {
		// A crashed supervisor can leave a native child writing the Home.
		// Stop admissions now; the next reconciliation drains other tasks and
		// confirms destruction of this entire sandbox before a replacement.
		if _, transitionErr := store.BeginRetire(ctx, host); transitionErr != nil && !errors.Is(transitionErr, dshhost.ErrChanged) {
			return dshhost.Host{}, cold, transitionErr
		}
		chattrace.LogStage(slog.Default(), trace, "dsh_employee_host", "waiting", "reason", "native_host_unavailable", "sandbox_id", host.SandboxID, "generation", host.Generation)
		return dshhost.Host{}, cold, errors.Join(errDSHHostWaiting, errDSHHostStartup)
	}
	// The process has proved its exact composition. A database outage or a
	// concurrent config edit is a reconciliation retry, not a dead Host.
	if err = profiles.Acknowledge(ctx, host, revision, l.ReadDSHProfileSource); err != nil {
		chattrace.LogStage(slog.Default(), trace, "dsh_employee_host", "waiting", "reason", "profile_ack_pending", "sandbox_id", host.SandboxID, "generation", host.Generation)
		return dshhost.Host{}, cold, errDSHHostWaiting
	}
	chattrace.LogStage(slog.Default(), trace, "dsh_employee_host", "ready", "sandbox_id", host.SandboxID, "generation", host.Generation, "agent_id", host.AgentID.String(), "managed_profile_digest", profileDigest, "employee_profile_revision", revision.ID, "employee_profile_digest", revision.Digest)
	return host, cold, nil
}

// dshHostProvider builds the FC client for one employee's storage placement.
func (l *FCE2BLauncher) dshHostProvider(storage dshhost.Storage) (dshhost.Provider, error) {
	if l.dshProvider != nil {
		return l.dshProvider(storage)
	}
	return dshhost.NewFCProvider(dshhost.FCConfig{
		APIURL: l.Config.APIURL, APIKey: l.Config.APIKey, TimeoutSeconds: l.Config.TimeoutSeconds,
		VPCID: storage.VPCID, SecurityGroupID: storage.SecurityGroupID, VSwitchIDs: storage.VSwitchIDs,
		Origin: fcE2BSandboxOrigin(l.Config),
	})
}

// This digest covers the image-owned managed overlay and its model catalog.
// It is not the employee's editable Profile revision, which has its own lifecycle.
func dshManagedCatalog(models []string) (string, string, error) {
	invalid := errors.New("invalid DSH managed model catalog")
	if len(models) == 0 || len(models) > 256 {
		return "", "", invalid
	}
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		if model == "" || strings.TrimSpace(model) != model || !utf8.ValidString(model) || utf8.RuneCountInString(model) > 512 || seen[model] {
			return "", "", invalid
		}
		for _, char := range model {
			if char < 32 {
				return "", "", invalid
			}
		}
		seen[model] = true
	}
	encoded, err := json.Marshal(models)
	if err != nil || len(encoded) > 65536 {
		return "", "", invalid
	}
	raw := string(encoded)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("multica-native-profile-v2\n"+raw)))
	return raw, digest, nil
}

func dshNativeHostEnsureArgs(host dshhost.Host, catalog, authority, origin, publicKey string, revision dshprofile.Revision) []string {
	return []string{"sandbox", "exec", "--user", "user", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=",
		"-e", "DSH_HOME=" + dshhost.MountPath + "/home", "-e", "MULTICA_DSH_WORKSPACE_ID=" + host.WorkspaceID.String(),
		"-e", "MULTICA_DSH_AGENT_ID=" + host.AgentID.String(), "-e", "MULTICA_DSH_HOST_GENERATION=" + strconv.FormatInt(host.Generation, 10),
		"-e", "MULTICA_DSH_MODEL_CATALOG_JSON=" + catalog,
		"-e", "MULTICA_DSH_EMPLOYEE_PROFILE_FILE=" + dshProfileInputPath(revision),
		"-e", "MULTICA_DSH_NATIVE_AUTHORITY=" + authority,
		"-e", "MULTICA_DSH_NATIVE_PUBLIC_KEY=" + publicKey,
		"-e", "MULTICA_DSH_NATIVE_ORIGIN=" + origin,
		"-e", "MULTICA_DSH_SANDBOX_ID=" + host.SandboxID,
		host.SandboxID, "--", "/usr/local/libexec/multica-dsh-host", "--ensure"}
}

func validateDSHNativeHostReceipt(out string, host dshhost.Host, profileDigest string, revision dshprofile.Revision) error {
	var receipt struct {
		Version         int    `json:"version"`
		WorkspaceID     string `json:"workspace_id"`
		AgentID         string `json:"agent_id"`
		Generation      int64  `json:"generation"`
		ProfileDigest   string `json:"managed_profile_digest"`
		EmployeeProfile struct {
			Revision string `json:"revision"`
			Digest   string `json:"digest"`
		} `json:"employee_profile"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &receipt); err != nil || receipt.Version != 1 || receipt.WorkspaceID != host.WorkspaceID.String() || receipt.AgentID != host.AgentID.String() || receipt.Generation != host.Generation || profileDigest == "" || receipt.ProfileDigest != profileDigest {
		return errors.New("DSH native Host did not confirm the expected employee, generation and managed profile")
	}
	if revision.ID < 1 || revision.Descriptor == "" || revision.Digest != fmt.Sprintf("%x", sha256.Sum256([]byte(revision.Descriptor))) || receipt.EmployeeProfile.Revision != strconv.FormatInt(revision.ID, 10) || receipt.EmployeeProfile.Digest != revision.Digest {
		return errors.New("DSH native Host did not confirm the exact employee Profile revision")
	}
	return nil
}

func dshHomePrepareArgs(host dshhost.Host) ([]string, error) {
	ap := dshAccessPointPattern.FindStringSubmatch(host.AccessPointARN)
	if len(ap) != 2 || host.WorkspaceID == uuid.Nil || host.AgentID == uuid.Nil || host.Generation < 1 {
		return nil, errors.New("invalid DSH Home storage identity")
	}
	return []string{"sandbox", "exec", "--user", "root", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=", host.SandboxID, "--", "/usr/local/libexec/multica-dsh-home",
		"--workspace", host.WorkspaceID.String(), "--agent", host.AgentID.String(), "--generation", strconv.FormatInt(host.Generation, 10), "--access-point", ap[1]}, nil
}

func validateDSHHomeReceipt(out string, host dshhost.Host) error {
	var receipt struct {
		Version     int    `json:"version"`
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		Generation  int64  `json:"generation"`
		Mount       string `json:"mount"`
		Home        string `json:"dsh_home"`
		UID         int    `json:"uid"`
		GID         int    `json:"gid"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &receipt); err != nil || receipt.Version != 1 || receipt.WorkspaceID != host.WorkspaceID.String() || receipt.AgentID != host.AgentID.String() || receipt.Generation != host.Generation || receipt.Mount != dshhost.MountPath || receipt.Home != dshhost.MountPath+"/home" || receipt.UID != 1000 || receipt.GID != 1000 {
		return errors.New("DSH Home initialization did not confirm the expected employee, generation, mount and task user")
	}
	return nil
}

// Reuse the durable provisioning workflow and the admission connection. Unknown
// cloud outcomes are reconciled on the next admission, never bypassed by a
// temporary sandbox or retried as a second resource creation.
func (l *FCE2BLauncher) prepareDSHTaskFilesystem(ctx context.Context, db dshhost.Database, key dshhost.Key) error {
	if l.ProvisionDSHStorage == nil {
		return errors.New("DSH storage provisioning is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	_, err := l.ProvisionDSHStorage(ctx, db, key)
	if errors.Is(err, dshhost.ErrPending) || errors.Is(err, dshhost.ErrChanged) || errors.Is(err, context.DeadlineExceeded) {
		return errDSHHostWaiting
	}
	if err != nil {
		return fmt.Errorf("initialize DSH employee filesystem: %w", err)
	}
	return nil
}
