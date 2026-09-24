package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/wsfs"
)

const (
	wsfsWriteLockClass  int32 = 0x57534653
	wsfsWriteChunkBytes       = 32 << 10
	wsfsBackfillFiles         = 32
	wsfsBackfillBytes         = 512 << 10
)

// SyncSharedFile writes one shared-catalog file onto the workspace NAS volume
// through the wsfs-write host. A workspace without a provisioned binding is a
// no-op so the OSS catalog can still exist before NAS is ready.
func (l *FCE2BLauncher) SyncSharedFile(ctx context.Context, workspaceID uuid.UUID, rel string, data []byte) error {
	if l == nil || l.Pool == nil || workspaceID == uuid.Nil {
		return nil
	}
	cleaned, err := wsfs.JailRelPath(rel)
	if err != nil || cleaned == "." {
		return fmt.Errorf("shared file path: %w", err)
	}
	store := wsfs.Store{DB: l.Pool}
	if _, err := store.GetBinding(ctx, workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	sandboxID, err := l.ensureWorkspaceWriteHost(ctx, workspaceID)
	if err != nil {
		return err
	}
	return l.writeSharedNASFile(ctx, sandboxID, cleaned, data)
}

// SyncSharedRename moves one shared path on NAS. A missing source is left
// alone. An existing destination is not overwritten.
func (l *FCE2BLauncher) SyncSharedRename(ctx context.Context, workspaceID uuid.UUID, oldRel, newRel string) error {
	oldClean, err := wsfs.JailRelPath(oldRel)
	if err != nil || oldClean == "." {
		return fmt.Errorf("shared rename source: %w", err)
	}
	newClean, err := wsfs.JailRelPath(newRel)
	if err != nil || newClean == "." {
		return fmt.Errorf("shared rename destination: %w", err)
	}
	return l.withSharedNAS(ctx, workspaceID, func(sandboxID string) error {
		_, err := l.runE2BCommand(ctx, sharedNASExec(sandboxID, nasRenameScript(
			path.Join(dshhost.WorkspaceSharedRoot, oldClean),
			path.Join(dshhost.WorkspaceSharedRoot, newClean),
		)))
		return err
	})
}

// SyncSharedDelete removes one shared path on NAS, including a directory tree.
// A path that is already absent is success.
func (l *FCE2BLauncher) SyncSharedDelete(ctx context.Context, workspaceID uuid.UUID, rel string) error {
	cleaned, err := wsfs.JailRelPath(rel)
	if err != nil || cleaned == "." {
		return fmt.Errorf("shared delete path: %w", err)
	}
	return l.withSharedNAS(ctx, workspaceID, func(sandboxID string) error {
		_, err := l.runE2BCommand(ctx, sharedNASExec(sandboxID, nasDeleteScript(path.Join(dshhost.WorkspaceSharedRoot, cleaned))))
		return err
	})
}

func sharedNASExec(sandboxID, script string) []string {
	return []string{"sandbox", "exec", "--user", "user", sandboxID, "--", "python3", "-c", script}
}

func nasRenameScript(oldPath, newPath string) string {
	return "import pathlib,sys; s=pathlib.Path(" + pyQuote(oldPath) + "); d=pathlib.Path(" + pyQuote(newPath) + ");\n" +
		"if not s.exists():\n sys.exit(0)\n" +
		"d.parent.mkdir(parents=True, exist_ok=True)\n" +
		"if d.exists():\n sys.exit(2)\n" +
		"s.rename(d)\n"
}

func nasDeleteScript(dest string) string {
	return "import pathlib,shutil; p=pathlib.Path(" + pyQuote(dest) + ")\n" +
		"if not p.exists():\n raise SystemExit\n" +
		"shutil.rmtree(p) if p.is_dir() else p.unlink()\n"
}

func (l *FCE2BLauncher) withSharedNAS(ctx context.Context, workspaceID uuid.UUID, fn func(string) error) error {
	if l == nil || l.Pool == nil || workspaceID == uuid.Nil {
		return nil
	}
	store := wsfs.Store{DB: l.Pool}
	if _, err := store.GetBinding(ctx, workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	sandboxID, err := l.ensureWorkspaceWriteHost(ctx, workspaceID)
	if err != nil {
		return err
	}
	return fn(sandboxID)
}

// SyncSharedCatalog copies the existing shared catalog onto the NAS volume.
// It is used when an agent is granted access, so a read-only mount is not
// asked to create the files itself.
func (l *FCE2BLauncher) SyncSharedCatalog(ctx context.Context, workspaceID uuid.UUID) error {
	if l == nil || l.Pool == nil || l.ObjectStorage == nil || workspaceID == uuid.Nil {
		return nil
	}
	store := wsfs.Store{DB: l.Pool}
	if _, err := store.GetBinding(ctx, workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	files, err := store.ListFiles(ctx, workspaceID)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	sandboxID, err := l.ensureWorkspaceWriteHost(ctx, workspaceID)
	if err != nil {
		return err
	}
	copied := 0
	for _, file := range files {
		if copied >= wsfsBackfillFiles || file.StorageKey == "" || file.SizeBytes > wsfsBackfillBytes {
			continue
		}
		reader, err := l.ObjectStorage.GetReader(ctx, file.StorageKey)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, wsfsBackfillBytes+1))
		_ = reader.Close()
		if readErr != nil || int64(len(data)) > wsfsBackfillBytes {
			continue
		}
		if err := l.writeSharedNASFile(ctx, sandboxID, file.RelPath, data); err != nil {
			return err
		}
		copied++
	}
	slog.Info("workspace shared catalog written to NAS", "workspace_id", workspaceID, "copied", copied, "catalog", len(files))
	return nil
}

// retireWriteHost drops the write-host row only after the sandbox is confirmed
// absent. Releasing first would let BeginWriteHost create a second sandbox on
// the same read-write volume.
func retireWriteHost(ctx context.Context, provider *dshhost.FCProvider, store wsfs.Store, host wsfs.WriteHost) error {
	return releaseWriteHostAfterDestroy(host.SandboxID, func(id string) error {
		return provider.DestroyAndConfirmAbsent(ctx, id)
	}, func() error {
		return store.ReleaseWriteHost(ctx, host)
	})
}

// lookupWriteHostTemplate uses the DSH stable image, then an employee host in
// this workspace. Shared uploads must not require an employee host row.
func lookupWriteHostTemplate(ctx context.Context, conn *pgxpool.Conn, workspaceID uuid.UUID) (string, error) {
	var stable, employee string
	err := conn.QueryRow(ctx, `SELECT current_template_id FROM fc_e2b_stable_channel
 WHERE sandbox_backend='aliyun_fc' AND channel IN ('stable:dsh','stable') AND btrim(current_template_id)<>''
 ORDER BY (channel='stable:dsh') DESC LIMIT 1`).Scan(&stable)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("workspace write host template: %w", err)
	}
	err = conn.QueryRow(ctx, `SELECT template_id FROM dsh_employee_host
 WHERE workspace_id=$1 AND btrim(template_id)<>'' ORDER BY updated_at DESC LIMIT 1`, workspaceID).Scan(&employee)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("workspace write host template: %w", err)
	}
	return preferWriteHostTemplate(stable, employee)
}

func preferWriteHostTemplate(stable, employee string) (string, error) {
	if template := strings.TrimSpace(stable); template != "" {
		return template, nil
	}
	if template := strings.TrimSpace(employee); template != "" {
		return template, nil
	}
	return "", errors.New("workspace write host template is not ready")
}

func releaseWriteHostAfterDestroy(sandboxID string, destroy func(string) error, release func() error) error {
	if sandboxID != "" {
		if err := destroy(sandboxID); err != nil {
			return fmt.Errorf("workspace write host destroy unconfirmed: %w", err)
		}
	}
	return release()
}

func (l *FCE2BLauncher) ensureWorkspaceWriteHost(ctx context.Context, workspaceID uuid.UUID) (string, error) {
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()
	key := int32(workspaceID[0])<<24 | int32(workspaceID[1])<<16 | int32(workspaceID[2])<<8 | int32(workspaceID[3])
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1,$2)", wsfsWriteLockClass, key); err != nil {
		return "", err
	}
	defer releaseFCE2BAdvisoryLock(conn, false, wsfsWriteLockClass, key, "workspace write host")
	return l.ensureWorkspaceWriteHostLocked(ctx, conn, workspaceID, false)
}

func (l *FCE2BLauncher) ensureWorkspaceWriteHostLocked(ctx context.Context, conn *pgxpool.Conn, workspaceID uuid.UUID, replaced bool) (string, error) {
	store := wsfs.Store{DB: conn}
	binding, err := store.GetBinding(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	if binding.RWVolumeName == "" || binding.RWRoleARN == "" {
		return "", errors.New("workspace shared write volume is not ready")
	}
	net, err := store.WriteNetwork(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	template, err := lookupWriteHostTemplate(ctx, conn, workspaceID)
	if err != nil {
		return "", err
	}
	provider, err := dshhost.NewFCProvider(dshhost.FCConfig{
		APIURL: l.Config.APIURL, APIKey: l.Config.APIKey, TimeoutSeconds: dshhost.SandboxTaskTimeoutSeconds(l.Config.TimeoutSeconds),
		VPCID: net.VPCID, SecurityGroupID: net.SecurityGroupID, VSwitchIDs: net.VSwitchIDs,
	})
	if err != nil {
		return "", err
	}
	host, err := store.GetWriteHost(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	if host.State == "running" && (host.VolumeName != binding.RWVolumeName || host.RoleARN != binding.RWRoleARN || host.TemplateID != template) {
		if err := retireWriteHost(ctx, provider, store, host); err != nil {
			return "", err
		}
		host.State = "offline"
	}
	if host.State == "running" {
		if err := provider.Healthy(ctx, host.SandboxID); err == nil {
			_, _ = l.renewSandboxForTask(ctx, host.SandboxID, chattrace.New("wsfs_write"))
			return host.SandboxID, nil
		}
		if !replaced {
			if err := retireWriteHost(ctx, provider, store, host); err != nil {
				return "", err
			}
			return l.ensureWorkspaceWriteHostLocked(ctx, conn, workspaceID, true)
		}
		return "", errors.New("workspace write host is unhealthy")
	}
	if host.State == "creating" {
		spec, specErr := dshhost.WorkspaceWriteSpec(workspaceID, host.CreateIntent, host.Generation, host.TemplateID, host.VolumeName, host.RoleARN)
		if specErr != nil {
			return "", specErr
		}
		id, findErr := provider.FindCreatedSpec(ctx, spec)
		if findErr != nil || id == "" {
			return "", fmt.Errorf("%w: workspace write host create is unconfirmed", dshhost.ErrPending)
		}
		if err := store.CompleteWriteHost(ctx, host, id); err != nil {
			return "", err
		}
		return id, nil
	}
	if host.State != "offline" {
		return "", fmt.Errorf("%w: workspace write host is %s", dshhost.ErrPending, host.State)
	}
	intent := uuid.New()
	nextGen := host.Generation + 1
	created, err := store.BeginWriteHost(ctx, workspaceID, intent, nextGen, template, binding.RWVolumeName, binding.RWRoleARN)
	if err != nil {
		return "", err
	}
	spec, err := dshhost.WorkspaceWriteSpec(workspaceID, created.CreateIntent, created.Generation, created.TemplateID, created.VolumeName, created.RoleARN)
	if err != nil {
		return "", err
	}
	id, err := provider.CreateSpec(ctx, spec)
	if err != nil || id == "" {
		var status *dshhost.FCStatusError
		if errors.As(err, &status) && status.Status >= 400 && status.Status < 500 && status.Status != 408 {
			_ = store.ReleaseWriteHost(ctx, created)
		}
		return "", fmt.Errorf("workspace write host create: %w", err)
	}
	if err := store.CompleteWriteHost(ctx, created, id); err != nil {
		return "", err
	}
	return id, nil
}

func (l *FCE2BLauncher) writeSharedNASFile(ctx context.Context, sandboxID, rel string, data []byte) error {
	dest := path.Join(dshhost.WorkspaceSharedRoot, rel)
	if len(data) == 0 {
		return l.execSharedNASWrite(ctx, sandboxID, dest, nil, false)
	}
	for offset := 0; offset < len(data); offset += wsfsWriteChunkBytes {
		end := offset + wsfsWriteChunkBytes
		if end > len(data) {
			end = len(data)
		}
		if err := l.execSharedNASWrite(ctx, sandboxID, dest, data[offset:end], offset > 0); err != nil {
			return err
		}
	}
	return nil
}

func (l *FCE2BLauncher) execSharedNASWrite(ctx context.Context, sandboxID, dest string, chunk []byte, append bool) error {
	mode := "wb"
	if append {
		mode = "ab"
	}
	script := "import base64,pathlib; p=pathlib.Path(" + pyQuote(dest) + "); p.parent.mkdir(parents=True, exist_ok=True); p.open(" + pyQuote(mode) + ").write(base64.standard_b64decode(" + pyQuote(base64.StdEncoding.EncodeToString(chunk)) + "))"
	_, err := l.runE2BCommand(ctx, []string{"sandbox", "exec", "--user", "user", sandboxID, "--", "python3", "-c", script})
	return err
}
