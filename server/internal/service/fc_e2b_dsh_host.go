package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

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

func lockDSHEmployee(ctx context.Context, conn *pgxpool.Conn, workspace, agent pgtype.UUID) (func(), error) {
	if conn == nil || !workspace.Valid || !agent.Valid {
		return nil, errors.New("DSH employee coordination requires a database connection and identity")
	}
	h := fnv.New32a()
	_, _ = h.Write(workspace.Bytes[:])
	_, _ = h.Write(agent.Bytes[:])
	key := int32(h.Sum32())
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1,$2)", dshEmployeeLockClass, key); err != nil {
		return nil, fmt.Errorf("lock DSH employee: %w", err)
	}
	return func() { releaseFCE2BAdvisoryLock(conn, false, dshEmployeeLockClass, key, "DSH employee") }, nil
}

// The caller holds the employee admission lock through runner submission.
// Storage operations reuse that connection, even when the pool has size one.
func (l *FCE2BLauncher) resolveDSHEmployeeSandbox(ctx context.Context, task db.AgentTaskQueue, rt db.AgentRuntime, template string, conn *pgxpool.Conn, trace chattrace.Trace) (dshhost.Host, bool, error) {
	if conn == nil || !rt.WorkspaceID.Valid || !task.AgentID.Valid {
		return dshhost.Host{}, false, errors.New("invalid DSH employee launch identity")
	}
	key := dshhost.Key{WorkspaceID: uuid.UUID(rt.WorkspaceID.Bytes), AgentID: uuid.UUID(task.AgentID.Bytes)}
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
		err = conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM agent_task_queue t JOIN agent a ON a.id=t.agent_id
 WHERE a.workspace_id=$1 AND t.agent_id=$2 AND t.id<>$3
 AND (t.status IN ('dispatched','running','waiting_local_directory') OR
 (t.status='queued' AND EXISTS (SELECT 1 FROM agent_task_runtime_start_attempt s
 WHERE s.task_id=t.id AND s.sandbox_id=$4 AND s.status IN ('starting','claimed')))))`,
			rt.WorkspaceID, task.AgentID, task.ID, before.SandboxID).Scan(&busy)
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
	out, err = l.runE2BCommand(ctx, dshNativeHostEnsureArgs(host))
	if err == nil {
		err = validateDSHNativeHostReceipt(out, host)
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
	chattrace.LogStage(slog.Default(), trace, "dsh_employee_host", "ready", "sandbox_id", host.SandboxID, "generation", host.Generation, "agent_id", host.AgentID.String())
	return host, cold, nil
}

func dshNativeHostEnsureArgs(host dshhost.Host) []string {
	return []string{"sandbox", "exec", "--user", "user", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=",
		"-e", "DSH_HOME=" + dshhost.MountPath + "/home", "-e", "MULTICA_DSH_WORKSPACE_ID=" + host.WorkspaceID.String(),
		"-e", "MULTICA_DSH_AGENT_ID=" + host.AgentID.String(), "-e", "MULTICA_DSH_HOST_GENERATION=" + strconv.FormatInt(host.Generation, 10),
		host.SandboxID, "--", "/usr/local/libexec/multica-dsh-host", "--ensure"}
}

func validateDSHNativeHostReceipt(out string, host dshhost.Host) error {
	var receipt struct {
		Version     int    `json:"version"`
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		Generation  int64  `json:"generation"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &receipt); err != nil || receipt.Version != 1 || receipt.WorkspaceID != host.WorkspaceID.String() || receipt.AgentID != host.AgentID.String() || receipt.Generation != host.Generation {
		return errors.New("DSH native Host did not confirm the expected employee and generation")
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
