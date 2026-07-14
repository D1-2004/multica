package dingtalk

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// sessionRecord is the observable state of an install session — everything a
// status poll needs, and nothing more. The device code and the polling
// goroutine deliberately stay in the memory of the pod that began the session
// (see migration 181).
type sessionRecord struct {
	ID             string
	WorkspaceID    pgtype.UUID
	Status         RegistrationSessionStatus
	InstallationID pgtype.UUID
	ErrorReason    string
	ErrorMessage   string
	ExpiresAt      time.Time
}

// sessionStore persists that state so a status poll served by a different
// replica than the one that began the session still finds it. An in-process
// map cannot do this: the browser's polls are load-balanced across pods, so
// every poll that misses the originating pod 404s.
type sessionStore interface {
	Create(ctx context.Context, rec sessionRecord, agentID pgtype.UUID) error
	Get(ctx context.Context, id string) (sessionRecord, error)
	FinishSuccess(ctx context.Context, id string, installationID pgtype.UUID, gcAfter time.Time) error
	FinishError(ctx context.Context, id, reason, message string, gcAfter time.Time) error
	Sweep(ctx context.Context, now time.Time) error
}

// dbSessionStore is the production store.
type dbSessionStore struct {
	q *db.Queries
}

func (s *dbSessionStore) Create(ctx context.Context, rec sessionRecord, agentID pgtype.UUID) error {
	return s.q.CreateDingTalkInstallSession(ctx, db.CreateDingTalkInstallSessionParams{
		ID:          rec.ID,
		WorkspaceID: rec.WorkspaceID,
		AgentID:     agentID,
		ExpiresAt:   pgtype.Timestamptz{Time: rec.ExpiresAt, Valid: true},
	})
}

func (s *dbSessionStore) Get(ctx context.Context, id string) (sessionRecord, error) {
	row, err := s.q.GetDingTalkInstallSession(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sessionRecord{}, ErrRegistrationSessionNotFound
		}
		return sessionRecord{}, err
	}
	return sessionRecord{
		ID:             row.ID,
		WorkspaceID:    row.WorkspaceID,
		Status:         RegistrationSessionStatus(row.Status),
		InstallationID: row.InstallationID,
		ErrorReason:    row.ErrorReason,
		ErrorMessage:   row.ErrorMessage,
		ExpiresAt:      row.ExpiresAt.Time,
	}, nil
}

func (s *dbSessionStore) FinishSuccess(ctx context.Context, id string, installationID pgtype.UUID, gcAfter time.Time) error {
	return s.q.FinishDingTalkInstallSessionSuccess(ctx, db.FinishDingTalkInstallSessionSuccessParams{
		ID:             id,
		InstallationID: installationID,
		GcAfter:        pgtype.Timestamptz{Time: gcAfter, Valid: true},
	})
}

func (s *dbSessionStore) FinishError(ctx context.Context, id, reason, message string, gcAfter time.Time) error {
	return s.q.FinishDingTalkInstallSessionError(ctx, db.FinishDingTalkInstallSessionErrorParams{
		ID:           id,
		ErrorReason:  reason,
		ErrorMessage: message,
		GcAfter:      pgtype.Timestamptz{Time: gcAfter, Valid: true},
	})
}

func (s *dbSessionStore) Sweep(ctx context.Context, now time.Time) error {
	return s.q.SweepDingTalkInstallSessions(ctx, pgtype.Timestamptz{Time: now, Valid: true})
}

// memSessionStore keeps sessions in process. It is only for in-package tests
// and single-process runs: production wires dbSessionStore, without which a
// multi-replica deployment 404s every cross-pod poll.
type memSessionStore struct {
	mu   sync.Mutex
	rows map[string]sessionRecord
	gc   map[string]time.Time
}

func newMemSessionStore() *memSessionStore {
	return &memSessionStore{rows: map[string]sessionRecord{}, gc: map[string]time.Time{}}
}

func (s *memSessionStore) Create(_ context.Context, rec sessionRecord, _ pgtype.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[rec.ID] = rec
	return nil
}

func (s *memSessionStore) Get(_ context.Context, id string) (sessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.rows[id]
	if !ok {
		return sessionRecord{}, ErrRegistrationSessionNotFound
	}
	return rec, nil
}

func (s *memSessionStore) FinishSuccess(_ context.Context, id string, installationID pgtype.UUID, gcAfter time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.rows[id]
	if !ok || rec.Status != RegistrationStatusPending {
		return nil
	}
	rec.Status = RegistrationStatusSuccess
	rec.InstallationID = installationID
	s.rows[id] = rec
	s.gc[id] = gcAfter
	return nil
}

func (s *memSessionStore) FinishError(_ context.Context, id, reason, message string, gcAfter time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.rows[id]
	if !ok || rec.Status != RegistrationStatusPending {
		return nil
	}
	rec.Status = RegistrationStatusError
	rec.ErrorReason = reason
	rec.ErrorMessage = message
	s.rows[id] = rec
	s.gc[id] = gcAfter
	return nil
}

func (s *memSessionStore) Sweep(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, at := range s.gc {
		if at.Before(now) {
			delete(s.rows, id)
			delete(s.gc, id)
		}
	}
	return nil
}
