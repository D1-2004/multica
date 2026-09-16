package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshprofile"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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
	host, err := (dshhost.PostgresStore{DB: conn}).Get(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return l.syncDSHNativePluginsOnHost(ctx, conn, key, template, host)
}

func (l *FCE2BLauncher) syncDSHNativePluginsOnHost(ctx context.Context, conn *pgxpool.Conn, key dshhost.Key, template string, host dshhost.Host) error {
	if host.State != "running" || host.SandboxID == "" {
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	// Older images remain readable during a rolling update. A present helper
	// failing is not equivalent to an empty plugin set and must not delete bindings.
	const command = `if [ -f /opt/multica-dsh/employee-profile-snapshot.mjs ]; then /usr/local/libexec/multica-dsh-host --plugin-snapshot || printf '{"version":1,"error":"snapshot_unavailable"}\n'; else printf '{"version":1,"available":false}\n'; fi`
	out, err := l.runE2BCommand(readCtx, []string{"sandbox", "exec", "--user", "user", "-e", "LD_PRELOAD=", "-e", "LD_LIBRARY_PATH=", "-e", "PYTHONPATH=", "-e", "PYTHONHOME=",
		"-e", "DSH_HOME=" + dshhost.MountPath + "/home", "-e", "MULTICA_DSH_WORKSPACE_ID=" + key.WorkspaceID.String(), "-e", "MULTICA_DSH_AGENT_ID=" + key.AgentID.String(),
		"-e", "MULTICA_DSH_HOST_GENERATION=" + strconv.FormatInt(host.Generation, 10), host.SandboxID, "--", "/bin/sh", "-c", command})
	if err != nil {
		// An expired sandbox must still reach ordinary lifecycle recovery. The
		// persisted pointer is checked again once its replacement has mounted.
		return nil
	}
	var availability struct {
		Version   int    `json:"version"`
		Error     string `json:"error"`
		Available *bool  `json:"available"`
	}
	if len(out) > 2<<20 || json.Unmarshal([]byte(out), &availability) != nil {
		return errors.New("native plugin synchronization unavailable")
	}
	if availability.Error != "" {
		return errors.New("native plugin changes are not ready for synchronization")
	}
	if availability.Version == 1 && availability.Available != nil && !*availability.Available {
		return nil
	}
	var snapshot dshprofile.NativeSnapshot
	if json.Unmarshal([]byte(out), &snapshot) != nil || snapshot.Validate(key.WorkspaceID.String(), key.AgentID.String()) != nil {
		return errors.New("invalid native plugin snapshot")
	}
	return l.SyncDSHProfileSource(ctx, conn, key, template, snapshot)
}
