package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const enterpriseIdentitySourceLockClass = int32(0x4549534b) // "EISK"

type enterpriseIdentitySourceKey struct {
	WorkspaceID   pgtype.UUID
	BoundBy       pgtype.UUID
	RuntimeID     pgtype.UUID
	TenantLockKey int32
	RawEmployeeID string
	BUCAgentID    string
}

func (k enterpriseIdentitySourceKey) validate() error {
	if !k.WorkspaceID.Valid || !k.BoundBy.Valid || !k.RuntimeID.Valid {
		return errors.New("enterprise identity source key is incomplete")
	}
	if strings.TrimSpace(k.RawEmployeeID) == "" || strings.TrimSpace(k.BUCAgentID) == "" {
		return errors.New("enterprise identity source principal is incomplete")
	}
	return nil
}

func enterpriseIdentitySourceKeyFromIdentity(
	identity db.AgentEnterpriseIdentity,
	tenantLockKey int32,
) (enterpriseIdentitySourceKey, error) {
	key := enterpriseIdentitySourceKey{
		WorkspaceID:   identity.WorkspaceID,
		BoundBy:       identity.BoundBy,
		RuntimeID:     identity.BucIdentitySourceRuntimeID,
		TenantLockKey: tenantLockKey,
		RawEmployeeID: identity.RawEmpID,
		BUCAgentID:    identity.BucAgentID,
	}
	return key, key.validate()
}

type EnterpriseIdentitySourceLocker interface {
	Lock(context.Context, enterpriseIdentitySourceKey) (func(), error)
	LockShared(context.Context, enterpriseIdentitySourceKey) (func(), error)
}

type postgresEnterpriseIdentitySourceLocker struct {
	pool *pgxpool.Pool
}

func newPostgresEnterpriseIdentitySourceLocker(
	pool *pgxpool.Pool,
) *postgresEnterpriseIdentitySourceLocker {
	return &postgresEnterpriseIdentitySourceLocker{pool: pool}
}

func (l *postgresEnterpriseIdentitySourceLocker) Lock(
	ctx context.Context,
	key enterpriseIdentitySourceKey,
) (func(), error) {
	return l.lock(ctx, key, false)
}

func (l *postgresEnterpriseIdentitySourceLocker) LockShared(
	ctx context.Context,
	key enterpriseIdentitySourceKey,
) (func(), error) {
	return l.lock(ctx, key, true)
}

func (l *postgresEnterpriseIdentitySourceLocker) lock(
	ctx context.Context,
	key enterpriseIdentitySourceKey,
	shared bool,
) (func(), error) {
	if l == nil || l.pool == nil {
		return nil, errors.New("enterprise identity source lock is unavailable")
	}
	if err := key.validate(); err != nil {
		return nil, err
	}
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire enterprise identity source lock connection: %w", err)
	}
	lockKey := enterpriseIdentitySourceLockKey(key)
	lockFunction := "pg_advisory_lock"
	unlockFunction := "pg_advisory_unlock"
	if shared {
		lockFunction = "pg_advisory_lock_shared"
		unlockFunction = "pg_advisory_unlock_shared"
	}
	if _, err := conn.Exec(
		ctx,
		fmt.Sprintf("SELECT %s($1, $2)", lockFunction),
		enterpriseIdentitySourceLockClass,
		lockKey,
	); err != nil {
		conn.Release()
		return nil, fmt.Errorf("acquire enterprise identity source lock: %w", err)
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		err := conn.QueryRow(
			unlockCtx,
			fmt.Sprintf("SELECT %s($1, $2)", unlockFunction),
			enterpriseIdentitySourceLockClass,
			lockKey,
		).Scan(&unlocked)
		if err != nil || !unlocked {
			slog.Error(
				"enterprise identity source lock release failed",
				"error", err,
				"unlocked", unlocked,
				"shared", shared,
			)
			_ = conn.Conn().Close(unlockCtx)
		}
		conn.Release()
	}, nil
}

func enterpriseIdentitySourceLockKey(key enterpriseIdentitySourceKey) int32 {
	hasher := fnv.New32a()
	for _, value := range []string{
		util.UUIDToString(key.WorkspaceID),
		util.UUIDToString(key.BoundBy),
		strconv.FormatInt(int64(key.TenantLockKey), 10),
		strings.TrimSpace(key.RawEmployeeID),
		strings.TrimSpace(key.BUCAgentID),
	} {
		_, _ = hasher.Write([]byte(value))
		_, _ = hasher.Write([]byte{0})
	}
	return int32(hasher.Sum32())
}

func enterpriseIdentitySourceFingerprint(key enterpriseIdentitySourceKey) string {
	hasher := sha256.New()
	for _, value := range []string{
		util.UUIDToString(key.WorkspaceID),
		util.UUIDToString(key.BoundBy),
		strconv.FormatInt(int64(key.TenantLockKey), 10),
		strings.TrimSpace(key.RawEmployeeID),
		strings.TrimSpace(key.BUCAgentID),
	} {
		_, _ = hasher.Write([]byte(value))
		_, _ = hasher.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

func sameEnterpriseIdentitySourceSnapshot(
	left db.AgentEnterpriseIdentity,
	right db.AgentEnterpriseIdentity,
) bool {
	if left.ID != right.ID ||
		left.BucIdentitySourceSandboxID.Valid != right.BucIdentitySourceSandboxID.Valid ||
		left.BucIdentitySourceRuntimeID.Valid != right.BucIdentitySourceRuntimeID.Valid {
		return false
	}
	if !left.BucIdentitySourceSandboxID.Valid ||
		!left.BucIdentitySourceRuntimeID.Valid {
		return true
	}
	if strings.TrimSpace(left.BucIdentitySourceSandboxID.String) !=
		strings.TrimSpace(right.BucIdentitySourceSandboxID.String) {
		return false
	}
	return left.WorkspaceID == right.WorkspaceID &&
		left.BoundBy == right.BoundBy &&
		left.RawEmpID == right.RawEmpID &&
		left.BucAgentID == right.BucAgentID &&
		left.BucIdentitySourceRuntimeID == right.BucIdentitySourceRuntimeID
}
