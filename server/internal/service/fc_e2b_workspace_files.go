package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"path"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/wsfs"
)

const (
	workspaceCatalogMaxFiles = 32
	workspaceCatalogMaxBytes = 512 << 10
)

func pgUUID(u pgtype.UUID) uuid.UUID {
	if !u.Valid {
		return uuid.Nil
	}
	return uuid.UUID(u.Bytes)
}

func pyQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func (l *FCE2BLauncher) attachWorkspaceCatalog(ctx context.Context, sandboxID string, workspaceID, agentID pgtype.UUID) string {
	return l.seedWorkspaceCatalog(ctx, sandboxID, workspaceID, agentID)
}

// seedWorkspaceCatalog copies granted catalog files onto an already mounted
// shared volume. It must not mkdir /mnt/workspace — that path exists only when
// FC created the sandbox with volumeMounts.
func (l *FCE2BLauncher) seedWorkspaceCatalog(ctx context.Context, sandboxID string, workspaceID, agentID pgtype.UUID) string {
	if l == nil || l.Pool == nil || l.ObjectStorage == nil || sandboxID == "" {
		return ""
	}
	wsID, agID := pgUUID(workspaceID), pgUUID(agentID)
	if wsID == uuid.Nil || agID == uuid.Nil {
		return ""
	}
	store := wsfs.Store{DB: l.Pool}
	grant, err := store.GetGrant(ctx, wsID, agID)
	if err != nil {
		slog.Warn("workspace filesystem grant unavailable", "error", err)
		return ""
	}
	if grant.Access != wsfs.AccessRead && grant.Access != wsfs.AccessWrite {
		return ""
	}
	files, err := store.ListFiles(ctx, wsID)
	if err != nil {
		slog.Warn("workspace filesystem catalog unavailable", "error", err)
		return grant.Access
	}
	copied := 0
	for _, file := range files {
		if copied >= workspaceCatalogMaxFiles || file.StorageKey == "" || file.RelPath == "" || file.SizeBytes > workspaceCatalogMaxBytes {
			continue
		}
		if _, err := wsfs.JailRelPath(file.RelPath); err != nil {
			continue
		}
		reader, err := l.ObjectStorage.GetReader(ctx, file.StorageKey)
		if err != nil {
			slog.Warn("workspace filesystem read failed", "path", file.RelPath, "error", err)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(reader, workspaceCatalogMaxBytes+1))
		_ = reader.Close()
		if err != nil || int64(len(data)) > workspaceCatalogMaxBytes {
			continue
		}
		dest := path.Join(dshhost.WorkspaceSharedRoot, file.RelPath)
		if !utf8.ValidString(dest) {
			continue
		}
		script := "import base64,pathlib; p=pathlib.Path(" + pyQuote(dest) + "); p.parent.mkdir(parents=True, exist_ok=True); p.write_bytes(base64.standard_b64decode(" + pyQuote(base64.StdEncoding.EncodeToString(data)) + "))"
		if _, err := l.runE2BCommand(ctx, []string{"sandbox", "exec", "--user", "user", sandboxID, "--", "python3", "-c", script}); err != nil {
			slog.Warn("workspace filesystem copy failed", "path", file.RelPath, "error", err)
			continue
		}
		copied++
	}
	slog.Info("workspace filesystem catalog seeded onto mounted volume",
		"sandbox_id", sandboxID,
		"access", grant.Access,
		"copied", copied,
		"catalog", len(files),
	)
	return grant.Access
}
