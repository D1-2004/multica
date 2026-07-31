package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type EnterpriseIdentityRuntimeLocker interface {
	LockShared(context.Context, pgtype.UUID) (func(), error)
}

type postgresEnterpriseIdentityRuntimeLocker struct {
	pool *pgxpool.Pool
}

func newPostgresEnterpriseIdentityRuntimeLocker(
	pool *pgxpool.Pool,
) *postgresEnterpriseIdentityRuntimeLocker {
	return &postgresEnterpriseIdentityRuntimeLocker{pool: pool}
}

func (l *postgresEnterpriseIdentityRuntimeLocker) LockShared(
	ctx context.Context,
	runtimeID pgtype.UUID,
) (func(), error) {
	if l == nil || l.pool == nil || !runtimeID.Valid {
		return nil, errors.New("enterprise identity Runtime lock is unavailable")
	}
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire enterprise identity Runtime lock connection: %w", err)
	}
	key := fcE2BRuntimeLockKey(runtimeID)
	if _, err := conn.Exec(
		ctx,
		"SELECT pg_advisory_lock_shared($1, $2)",
		fcE2BRuntimeLockClass,
		key,
	); err != nil {
		conn.Release()
		return nil, fmt.Errorf("acquire enterprise identity Runtime read lock: %w", err)
	}
	return func() {
		releaseFCE2BAdvisoryLock(
			conn,
			true,
			fcE2BRuntimeLockClass,
			key,
			"enterprise identity Runtime read",
		)
		conn.Release()
	}, nil
}
