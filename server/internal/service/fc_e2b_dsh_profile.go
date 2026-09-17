package service

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshprofile"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (l *FCE2BLauncher) RetryDSHEmployeeBuild(ctx context.Context, key dshhost.Key, revision int64, buildID uuid.UUID) error {
	l = l.withCurrentConfig()
	if l == nil || l.ReadDSHProfileSource == nil {
		return errors.New("employee Profile source unavailable")
	}
	return l.withDSHEmployee(ctx, key, func(conn *pgxpool.Conn, _ db.AgentRuntime, template string) error {
		return (dshprofile.Store{DB: conn}).RetryBuild(ctx, key, template, l.ReadDSHProfileSource, revision, buildID)
	})
}

// DSHEmployeeProfile prepares durable build intents only when requested. Reading
// status never starts a sandbox or publishes a new revision. Neither operation
// accepts descriptors, artifacts or credentials from the caller.
func (l *FCE2BLauncher) DSHEmployeeProfile(ctx context.Context, key dshhost.Key, prepare bool) (dshprofile.Status, error) {
	l = l.withCurrentConfig()
	var result dshprofile.Status
	if l == nil || l.ReadDSHProfileSource == nil {
		return result, errors.New("employee Profile source unavailable")
	}
	err := l.withDSHEmployee(ctx, key, func(conn *pgxpool.Conn, _ db.AgentRuntime, template string) error {
		if err := l.syncDSHNativePlugins(ctx, conn, key, template); err != nil {
			return err
		}
		store := dshprofile.Store{DB: conn}
		if prepare {
			if _, err := store.Prepare(ctx, key, template, l.ReadDSHProfileSource); err != nil {
				return err
			}
		}
		// Serialize saved employee settings with the status read so a historical
		// receipt cannot be presented as current after a concurrent config edit.
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		source, err := l.ReadDSHProfileSource(ctx, db.New(tx), key, template)
		if err != nil {
			return err
		}
		result, err = (dshprofile.Store{DB: tx}).Status(ctx, key)
		if err != nil {
			return err
		}
		result, err = result.CompareConfiguration(source)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	return result, err
}
