package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshprofile"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Restart is an explicit user operation, never part of ordinary task admission.
// Persist the exact browser Host's native edits before allowing any process exit.
func (l *FCE2BLauncher) PrepareDSHNativeRestart(ctx context.Context, host dshhost.Host) error {
	l = l.withCurrentConfig()
	if l == nil || l.SyncDSHProfileSource == nil {
		return errors.New("native plugin synchronization unavailable")
	}
	return l.withDSHEmployee(ctx, host.Key, func(conn *pgxpool.Conn, _ db.AgentRuntime, template string) error {
		probe, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, err := l.runE2BCommand(probe, []string{"sandbox", "exec", "--user", "user", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=", host.SandboxID,
			"--", "/opt/task-python/bin/python3", "-c", dshManagedRestartCapabilityCommand})
		if err != nil || strings.TrimSpace(out) != "managed-restart-v1" {
			return errors.New("this Runtime does not support managed DSH restart; upgrade the Runtime before restarting")
		}
		if err := l.syncDSHNativePluginsOnHost(ctx, conn, host.Key, template, host, true); err != nil {
			return errors.New("native plugin changes could not be saved; DSH was not restarted")
		}
		return nil
	})
}

// Probe the immutable installed adapter, not a user-controlled plugin or a
// Runtime label. Older images must never receive the marketplace restart route.
const dshManagedRestartCapabilityCommand = `import sys;sys.path.insert(0,"/opt/multica-dsh");from multica_dsh_host import NativeHost;print("managed-restart-v1" if callable(getattr(NativeHost,"request_restart",None)) else "unsupported")`

// Both profile reconciliation and the settings list use the existing employee
// lock. A snapshot can never select another sandbox or employee's filesystem.
func (l *FCE2BLauncher) SyncDSHNativePlugins(ctx context.Context, key dshhost.Key) error {
	l = l.withCurrentConfig()
	if l == nil || l.SyncDSHProfileSource == nil {
		return nil
	}
	return l.withDSHEmployee(ctx, key, func(conn *pgxpool.Conn, _ db.AgentRuntime, template string) error {
		return l.syncDSHNativePlugins(ctx, conn, key, template)
	})
}

func (l *FCE2BLauncher) syncDSHNativePlugins(ctx context.Context, conn *pgxpool.Conn, key dshhost.Key, template string) error {
	if l.SyncDSHProfileSource == nil {
		return nil
	}
	host, err := nativePluginSnapshotHost(ctx, conn, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return l.syncDSHNativePluginsOnHost(ctx, conn, key, template, host, false)
}

func (l *FCE2BLauncher) syncDSHNativePluginsOnHost(ctx context.Context, conn *pgxpool.Conn, key dshhost.Key, template string, host dshhost.Host, restart bool) error {
	if host.State != "running" || host.SandboxID == "" {
		if restart {
			return errors.New("native restart Host unavailable")
		}
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	// Older images remain readable during a rolling update. A present helper
	// failing is not equivalent to an empty plugin set and must not delete bindings.
	const command = dshNativePluginSnapshotCommand
	var appliedRevision int64
	if err := conn.QueryRow(ctx, `SELECT applied_revision FROM dsh_employee_profile WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID).Scan(&appliedRevision); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	out, err := l.runE2BCommand(readCtx, []string{"sandbox", "exec", "--user", "user", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=",
		"-e", "DSH_HOME=" + dshhost.MountPath + "/home", "-e", "MULTICA_DSH_WORKSPACE_ID=" + key.WorkspaceID.String(), "-e", "MULTICA_DSH_AGENT_ID=" + key.AgentID.String(),
		"-e", "MULTICA_DSH_SNAPSHOT_REVISION=" + strconv.FormatInt(appliedRevision, 10),
		"-e", "MULTICA_DSH_HOST_GENERATION=" + strconv.FormatInt(host.Generation, 10), host.SandboxID, "--", "/bin/sh", "-c", command})
	if err != nil {
		// An expired sandbox must still reach ordinary lifecycle recovery. The
		// persisted pointer is checked again once its replacement has mounted.
		if !restart && l.checkSandboxReady(readCtx, host.SandboxID) != nil {
			return nil
		}
		return errors.New("native plugin snapshot command failed on a running sandbox")
	}
	var availability struct {
		Version   int    `json:"version"`
		Busy      bool   `json:"busy"`
		Error     string `json:"error"`
		Available *bool  `json:"available"`
	}
	if len(out) > 2<<20 || json.Unmarshal([]byte(out), &availability) != nil {
		return errors.New("native plugin synchronization unavailable")
	}
	if availability.Busy {
		return errDSHHostWaiting
	}
	if availability.Error != "" {
		return errors.New("native plugin changes are not ready for synchronization")
	}
	if availability.Version == 1 && availability.Available != nil && !*availability.Available {
		if restart {
			return errors.New("native restart snapshot unavailable")
		}
		return nil
	}
	var snapshot dshprofile.NativeSnapshot
	if json.Unmarshal([]byte(out), &snapshot) != nil || snapshot.Validate(key.WorkspaceID.String(), key.AgentID.String()) != nil {
		return errors.New("invalid native plugin snapshot")
	}
	if err := l.SyncDSHProfileSource(ctx, conn, key, template, snapshot); err != nil {
		return err
	}
	if restart {
		// Background import deliberately ignores stale native revisions. That
		// no-op is not a saved-change receipt authorizing a user restart.
		var eligible bool
		base, err := strconv.ParseInt(snapshot.BaseRevision, 10, 64)
		if err != nil {
			return err
		}
		err = conn.QueryRow(ctx, `SELECT applied_revision=$3 OR native_sync_revision=$3
 FROM dsh_employee_profile WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID, base).Scan(&eligible)
		if err != nil {
			return err
		}
		if !eligible {
			return errors.New("native configuration revision is stale; reopen the current DSH entry")
		}
	}
	return nil
}

// A failed helper may already have printed a diagnostic object. Emit exactly
// one receipt, rather than concatenating that object with the error receipt.
const dshNativePluginSnapshotCommand = `if [ -f /opt/multica-dsh/employee-profile-snapshot.mjs ]; then if snapshot="$(/usr/local/libexec/multica-dsh-host --plugin-snapshot)"; then printf '%s\n' "$snapshot"; else printf '{"version":1,"error":"snapshot_unavailable"}\n'; fi; else printf '{"version":1,"available":false}\n'; fi`

// Human entries and tasks may now use session-scoped hosts. Reading only the
// legacy employee row silently skips native edits for those existing agents.
func nativePluginSnapshotHost(ctx context.Context, db dshhost.Database, key dshhost.Key) (dshhost.Host, error) {
	host := dshhost.Host{Key: key}
	err := db.QueryRow(ctx, `SELECT h.state,h.generation,h.sandbox_id,h.template_id
 FROM employee_filesystem_host h LEFT JOIN dsh_employee_profile p
 ON p.workspace_id=h.workspace_id AND p.agent_id=h.agent_id
 WHERE h.workspace_id=$1 AND h.agent_id=$2 AND h.state='running' AND h.sandbox_id<>''
 ORDER BY EXISTS (SELECT 1 FROM dsh_native_access n WHERE n.workspace_id=h.workspace_id
 AND n.agent_id=h.agent_id AND n.sandbox_id=h.sandbox_id AND n.generation=h.generation
 AND n.parent_access_id IS NULL AND n.kind IN ('entry','session') AND n.expires_at>now()) DESC,
 (h.sandbox_id=COALESCE(p.applied_sandbox_id,'')) DESC,h.generation DESC,h.sandbox_id LIMIT 1`, key.WorkspaceID, key.AgentID).Scan(&host.State, &host.Generation, &host.SandboxID, &host.TemplateID)
	return host, err
}
