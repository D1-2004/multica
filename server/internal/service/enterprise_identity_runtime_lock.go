package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type EnterpriseIdentityRuntimeLocker interface {
	LockShared(context.Context, pgtype.UUID) (func(), error)
	LockSharedMany(context.Context, []pgtype.UUID) (func(), error)
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
	return l.LockSharedMany(ctx, []pgtype.UUID{runtimeID})
}

func (l *postgresEnterpriseIdentityRuntimeLocker) LockSharedMany(
	ctx context.Context,
	runtimeIDs []pgtype.UUID,
) (func(), error) {
	if l == nil || l.pool == nil || len(runtimeIDs) == 0 {
		return nil, errors.New("enterprise identity Runtime lock is unavailable")
	}
	ordered := make([]pgtype.UUID, 0, len(runtimeIDs))
	seen := make(map[[16]byte]struct{}, len(runtimeIDs))
	for _, runtimeID := range runtimeIDs {
		if !runtimeID.Valid {
			return nil, errors.New("enterprise identity Runtime lock target is invalid")
		}
		if _, exists := seen[runtimeID.Bytes]; exists {
			continue
		}
		seen[runtimeID.Bytes] = struct{}{}
		ordered = append(ordered, runtimeID)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return bytes.Compare(ordered[i].Bytes[:], ordered[j].Bytes[:]) < 0
	})
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire enterprise identity Runtime lock connection: %w", err)
	}
	keys := make([]int32, 0, len(ordered))
	for _, runtimeID := range ordered {
		key := fcE2BRuntimeLockKey(runtimeID)
		if _, err := conn.Exec(
			ctx,
			"SELECT pg_advisory_lock_shared($1, $2)",
			fcE2BRuntimeLockClass,
			key,
		); err != nil {
			for index := len(keys) - 1; index >= 0; index-- {
				releaseFCE2BAdvisoryLock(
					conn,
					true,
					fcE2BRuntimeLockClass,
					keys[index],
					"enterprise identity Runtime read",
				)
			}
			conn.Release()
			return nil, fmt.Errorf("acquire enterprise identity Runtime read lock: %w", err)
		}
		keys = append(keys, key)
	}
	return func() {
		for index := len(keys) - 1; index >= 0; index-- {
			releaseFCE2BAdvisoryLock(
				conn,
				true,
				fcE2BRuntimeLockClass,
				keys[index],
				"enterprise identity Runtime read",
			)
		}
		conn.Release()
	}, nil
}
