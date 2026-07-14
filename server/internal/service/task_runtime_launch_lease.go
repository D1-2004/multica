package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	runtimeLaunchLeaseDuration      = 90 * time.Second
	runtimeLaunchLeaseRenewInterval = 30 * time.Second
	runtimeLaunchLeaseDBTimeout     = 5 * time.Second
)

var errRuntimeLaunchLeaseLost = errors.New("runtime launch lease lost")

type taskRuntimeLaunchLease struct {
	taskID    pgtype.UUID
	token     pgtype.UUID
	expiresAt time.Time
}

type taskRuntimeLaunchLeaseStore interface {
	Acquire(ctx context.Context, taskID pgtype.UUID, ttl time.Duration) (taskRuntimeLaunchLease, bool, error)
	Renew(ctx context.Context, lease taskRuntimeLaunchLease, ttl time.Duration) (time.Time, bool, error)
	Release(ctx context.Context, lease taskRuntimeLaunchLease) error
}

type postgresTaskRuntimeLaunchLeaseStore struct {
	queries *db.Queries
}

func newPostgresTaskRuntimeLaunchLeaseStore(queries *db.Queries) taskRuntimeLaunchLeaseStore {
	if queries == nil {
		return nil
	}
	return &postgresTaskRuntimeLaunchLeaseStore{queries: queries}
}

func (s *postgresTaskRuntimeLaunchLeaseStore) Acquire(ctx context.Context, taskID pgtype.UUID, ttl time.Duration) (taskRuntimeLaunchLease, bool, error) {
	rawToken := uuid.New()
	lease := taskRuntimeLaunchLease{
		taskID: taskID,
		token:  pgtype.UUID{Bytes: [16]byte(rawToken), Valid: true},
	}
	expiresAt, err := s.queries.AcquireAgentTaskRuntimeLaunchLease(ctx, db.AcquireAgentTaskRuntimeLaunchLeaseParams{
		TaskID:       taskID,
		LeaseToken:   lease.token,
		LeaseSeconds: ttl.Seconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return taskRuntimeLaunchLease{}, false, nil
	}
	if err != nil {
		return taskRuntimeLaunchLease{}, false, fmt.Errorf("acquire task runtime launch lease: %w", err)
	}
	lease.expiresAt = expiresAt.Time
	return lease, true, nil
}

func (s *postgresTaskRuntimeLaunchLeaseStore) Renew(ctx context.Context, lease taskRuntimeLaunchLease, ttl time.Duration) (time.Time, bool, error) {
	expiresAt, err := s.queries.RenewAgentTaskRuntimeLaunchLease(ctx, db.RenewAgentTaskRuntimeLaunchLeaseParams{
		TaskID:       lease.taskID,
		LeaseToken:   lease.token,
		LeaseSeconds: ttl.Seconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("renew task runtime launch lease: %w", err)
	}
	return expiresAt.Time, true, nil
}

func (s *postgresTaskRuntimeLaunchLeaseStore) Release(ctx context.Context, lease taskRuntimeLaunchLease) error {
	_, err := s.queries.ReleaseAgentTaskRuntimeLaunchLease(ctx, db.ReleaseAgentTaskRuntimeLaunchLeaseParams{
		TaskID:     lease.taskID,
		LeaseToken: lease.token,
	})
	if err != nil {
		return fmt.Errorf("release task runtime launch lease: %w", err)
	}
	return nil
}
