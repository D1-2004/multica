package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"

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
	var template string
	if err := conn.QueryRow(ctx, `SELECT template_id FROM dsh_employee_host
 WHERE workspace_id=$1 AND template_id<>'' ORDER BY updated_at DESC LIMIT 1`, workspaceID).Scan(&template); err != nil {
		return "", fmt.Errorf("workspace write host template: %w", err)
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
		_ = provider.DestroyAndConfirmAbsent(ctx, host.SandboxID)
		if err := store.ReleaseWriteHost(ctx, host); err != nil {
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
			_ = provider.DestroyAndConfirmAbsent(ctx, host.SandboxID)
			if err := store.ReleaseWriteHost(ctx, host); err != nil {
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
