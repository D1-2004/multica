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
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const DSHEmployeeHostCapability = "dsh_employee_host_v1"
const dshEmployeeLockClass int32 = 0x44534831

var errDSHHostWaiting = errors.New("DSH employee host is awaiting reconciliation or task drain")
var dshAccessPointPattern = regexp.MustCompile(`^acs:nas:[a-z0-9-]+:[0-9]+:accesspoint/(ap-[a-z0-9]+)$`)

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
	if l == nil || l.Queries == nil || l.Pool == nil || !l.Config.Enabled || l.nativeAuthority == nil || key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil {
		return dshhost.Host{}, errors.New("DSH employee startup is unavailable")
	}
	if err := l.Config.Validate(); err != nil {
		return dshhost.Host{}, err
	}
	params := db.GetAgentInWorkspaceParams{ID: pgtype.UUID{Bytes: key.AgentID, Valid: true}, WorkspaceID: pgtype.UUID{Bytes: key.WorkspaceID, Valid: true}}
	agent, err := l.Queries.GetAgentInWorkspace(ctx, params)
	if err != nil || !agent.RuntimeID.Valid {
		return dshhost.Host{}, errors.New("DSH employee runtime is unavailable")
	}
	// Use the platform launch lock order: shared Runtime, then employee. Reload
	// the binding on this connection so a waiting entry cannot select stale state.
	conn, releaseRuntime, err := l.lockRuntimeShared(ctx, agent.RuntimeID)
	if err != nil {
		return dshhost.Host{}, err
	}
	defer releaseRuntime()
	releaseEmployee, err := lockDSHEmployee(ctx, conn, params.WorkspaceID, params.ID)
	if err != nil {
		return dshhost.Host{}, err
	}
	defer releaseEmployee()
	queries := db.New(conn)
	current, err := queries.GetAgentInWorkspace(ctx, params)
	if err != nil || current.RuntimeID != agent.RuntimeID || current.ArchivedAt.Valid || current.RuntimeMode != "cloud" {
		return dshhost.Host{}, errors.New("DSH employee binding changed")
	}
	runtime, err := queries.GetAgentRuntime(ctx, current.RuntimeID)
	if err != nil || runtime.WorkspaceID != params.WorkspaceID || runtime.Provider != "dsh" || !IsFCE2BRuntime(runtime) || !runtime.OwnerID.Valid || !runtime.DaemonID.Valid || strings.TrimSpace(runtime.DaemonID.String) == "" {
		return dshhost.Host{}, errors.New("DSH employee requires an FC DSH runtime")
	}
	template, err := fcE2BTemplateForRuntime(runtime)
	if err != nil {
		return dshhost.Host{}, err
	}
	host, _, err := l.resolveDSHEmployeeSandbox(ctx, key, pgtype.UUID{}, runtime, template, conn, chattrace.New("dsh_native_entry"))
	return host, err
}

// The caller holds the employee admission lock through runner submission.
// Native entries pass no excluded task, so every admitted task blocks retirement.
// Storage operations reuse that connection, even when the pool has size one.
func (l *FCE2BLauncher) resolveDSHEmployeeSandbox(ctx context.Context, key dshhost.Key, excludeTask pgtype.UUID, rt db.AgentRuntime, template string, conn *pgxpool.Conn, trace chattrace.Trace) (dshhost.Host, bool, error) {
	if conn == nil || !rt.WorkspaceID.Valid || key.WorkspaceID != uuid.UUID(rt.WorkspaceID.Bytes) || key.AgentID == uuid.Nil {
		return dshhost.Host{}, false, errors.New("invalid DSH employee launch identity")
	}
	catalog, profileDigest, err := dshManagedCatalog(l.Config.LLMModels)
	if err != nil {
		return dshhost.Host{}, false, err
	}
	store := dshhost.PostgresStore{DB: conn}
	before, err := store.Get(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return dshhost.Host{}, false, errors.New("DSH employee storage has not been provisioned")
	}
	if err != nil {
		return dshhost.Host{}, false, err
	}
	var provider dshhost.Provider
	if l.dshProvider != nil {
		provider, err = l.dshProvider(before.Storage)
	} else {
		provider, err = dshhost.NewFCProvider(dshhost.FCConfig{
			APIURL: l.Config.APIURL, APIKey: l.Config.APIKey, TimeoutSeconds: l.Config.TimeoutSeconds,
			VPCID: before.VPCID, SecurityGroupID: before.SecurityGroupID, VSwitchIDs: before.VSwitchIDs,
		})
	}
	if err != nil {
		return dshhost.Host{}, false, err
	}
	manager := dshhost.Manager{Store: store, Provider: provider}
	var host dshhost.Host
	if before.State == "creating" {
		host, err = manager.ReconcileCreate(ctx, key)
		if err != nil {
			return dshhost.Host{}, false, errDSHHostWaiting
		}
	} else if before.State == "retiring" {
		err = dshhost.ErrRetireRequired
	} else {
		host, err = manager.Ensure(ctx, key, template)
	}
	if errors.Is(err, dshhost.ErrRetireRequired) || (err == nil && host.TemplateID != template) {
		var busy bool
		// A submitted background runner can still be queued before claiming.
		// Treat its persisted sandbox receipt as active admission, too.
		// Live native grants also reserve a healthy generation until revoked or
		// expired. A retiring generation already rejects those grants; its
		// remaining task writers must still drain before destruction.
		err = conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM agent_task_queue t JOIN agent a ON a.id=t.agent_id
 WHERE a.workspace_id=$1 AND t.agent_id=$2 AND t.id IS DISTINCT FROM $3::uuid
 AND (t.status IN ('dispatched','running','waiting_local_directory') OR
 (t.status='queued' AND EXISTS (SELECT 1 FROM agent_task_runtime_start_attempt s
 WHERE s.task_id=t.id AND s.sandbox_id=$4 AND s.status IN ('starting','claimed')))))
 OR ($5 AND EXISTS (SELECT 1 FROM dsh_native_access n
 WHERE n.workspace_id=$1 AND n.agent_id=$2 AND n.sandbox_id=$4 AND n.generation=$6
 AND n.kind IN ('entry','session') AND n.expires_at>now()))`,
			rt.WorkspaceID, pgtype.UUID{Bytes: key.AgentID, Valid: true}, excludeTask, before.SandboxID, before.State == "running", before.Generation).Scan(&busy)
		if err != nil {
			return dshhost.Host{}, false, err
		}
		if busy {
			return dshhost.Host{}, false, errDSHHostWaiting
		}
		current, loadErr := store.Get(ctx, key)
		if loadErr != nil {
			return dshhost.Host{}, false, loadErr
		}
		if err = manager.Retire(ctx, key, current.Generation); err != nil {
			return dshhost.Host{}, false, errDSHHostWaiting
		}
		host, err = manager.Ensure(ctx, key, template)
	}
	if errors.Is(err, dshhost.ErrPending) || errors.Is(err, dshhost.ErrChanged) {
		return dshhost.Host{}, false, errDSHHostWaiting
	}
	if err != nil {
		return dshhost.Host{}, false, err
	}
	cold := before.State != "running" || host.SandboxID != before.SandboxID
	if cold {
		err = l.waitSandboxReady(ctx, host.SandboxID)
	} else {
		err = l.checkSandboxReady(ctx, host.SandboxID)
	}
	if err != nil {
		return dshhost.Host{}, cold, err
	}
	if _, err = l.renewSandboxForTask(ctx, host.SandboxID, trace); err != nil {
		return dshhost.Host{}, cold, err
	}
	args, err := dshHomePrepareArgs(host)
	if err != nil {
		return dshhost.Host{}, cold, err
	}
	out, err := l.runE2BCommand(ctx, args)
	if err != nil {
		return dshhost.Host{}, cold, errors.New("DSH employee Home initialization failed")
	}
	if err = validateDSHHomeReceipt(out, host); err != nil {
		return dshhost.Host{}, cold, err
	}
	origin, authority, err := dshNativeGatewayAddress(l.Config, host)
	if err != nil {
		return dshhost.Host{}, cold, err
	}
	out, err = l.runE2BCommand(ctx, dshNativeHostEnsureArgs(host, catalog, authority, origin, l.nativeAuthority.publicKey()))
	if err == nil {
		err = validateDSHNativeHostReceipt(out, host, profileDigest)
	}
	if err != nil {
		// A crashed supervisor can leave a native child writing the Home.
		// Stop admissions now; the next reconciliation drains other tasks and
		// confirms destruction of this entire sandbox before a replacement.
		if _, transitionErr := store.BeginRetire(ctx, host); transitionErr != nil && !errors.Is(transitionErr, dshhost.ErrChanged) {
			return dshhost.Host{}, cold, transitionErr
		}
		chattrace.LogStage(slog.Default(), trace, "dsh_employee_host", "waiting", "reason", "native_host_unavailable", "sandbox_id", host.SandboxID, "generation", host.Generation)
		return dshhost.Host{}, cold, errDSHHostWaiting
	}
	chattrace.LogStage(slog.Default(), trace, "dsh_employee_host", "ready", "sandbox_id", host.SandboxID, "generation", host.Generation, "agent_id", host.AgentID.String(), "managed_profile_digest", profileDigest)
	return host, cold, nil
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

func dshNativeHostEnsureArgs(host dshhost.Host, catalog, authority, origin, publicKey string) []string {
	return []string{"sandbox", "exec", "--user", "user", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=",
		"-e", "DSH_HOME=" + dshhost.MountPath + "/home", "-e", "MULTICA_DSH_WORKSPACE_ID=" + host.WorkspaceID.String(),
		"-e", "MULTICA_DSH_AGENT_ID=" + host.AgentID.String(), "-e", "MULTICA_DSH_HOST_GENERATION=" + strconv.FormatInt(host.Generation, 10),
		"-e", "MULTICA_DSH_MODEL_CATALOG_JSON=" + catalog,
		"-e", "MULTICA_DSH_NATIVE_AUTHORITY=" + authority,
		"-e", "MULTICA_DSH_NATIVE_PUBLIC_KEY=" + publicKey,
		"-e", "MULTICA_DSH_NATIVE_ORIGIN=" + origin,
		"-e", "MULTICA_DSH_SANDBOX_ID=" + host.SandboxID,
		host.SandboxID, "--", "/usr/local/libexec/multica-dsh-host", "--ensure"}
}

func validateDSHNativeHostReceipt(out string, host dshhost.Host, profileDigest string) error {
	var receipt struct {
		Version       int    `json:"version"`
		WorkspaceID   string `json:"workspace_id"`
		AgentID       string `json:"agent_id"`
		Generation    int64  `json:"generation"`
		ProfileDigest string `json:"managed_profile_digest"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &receipt); err != nil || receipt.Version != 1 || receipt.WorkspaceID != host.WorkspaceID.String() || receipt.AgentID != host.AgentID.String() || receipt.Generation != host.Generation || profileDigest == "" || receipt.ProfileDigest != profileDigest {
		return errors.New("DSH native Host did not confirm the expected employee, generation and managed profile")
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
