package service

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/wsfs"
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

// effectiveWorkspaceFSAccess is the shared-disk permission of the volume
// mounted for this launch. A desired write grant does not make a reused
// read-only volume writable.
func effectiveWorkspaceFSAccess(host *dshhost.Host) string {
	if host == nil || len(host.ExtraMounts) != 1 {
		return ""
	}
	if host.SharedAccess == wsfs.AccessRead || host.SharedAccess == wsfs.AccessWrite {
		return host.SharedAccess
	}
	return wsfs.AccessRead
}

// seedWorkspaceCatalog reports the grant access for env injection. Catalog
// bytes are written by the wsfs-write host at upload or grant time. Copying
// them here would hit a read-only shared volume and leave the disk empty.
func (l *FCE2BLauncher) seedWorkspaceCatalog(ctx context.Context, sandboxID string, workspaceID, agentID pgtype.UUID) string {
	if l == nil || l.Pool == nil || sandboxID == "" {
		return ""
	}
	wsID, agID := pgUUID(workspaceID), pgUUID(agentID)
	if wsID == uuid.Nil || agID == uuid.Nil {
		return ""
	}
	grant, err := (wsfs.Store{DB: l.Pool}).GetGrant(ctx, wsID, agID)
	if err != nil {
		slog.Warn("workspace filesystem grant unavailable", "error", err)
		return ""
	}
	if grant.Access != wsfs.AccessRead && grant.Access != wsfs.AccessWrite {
		return ""
	}
	return grant.Access
}
