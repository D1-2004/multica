package scenememory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrInvalidIdentity = errors.New("scene memory identity is incomplete")
	ErrStaleRevision   = errors.New("scene memory revision is stale")
	ErrLeaseLost       = errors.New("scene memory lease is no longer owned")
	ErrNotCaughtUp     = errors.New("scene memory cursor has not covered the claimed cutoff")
	ErrMemoryText      = errors.New("scene memory text exceeds the code-point budget")
	ErrFlushMeta       = errors.New("scene memory flush meta exceeds the size budget")
)

type Store struct {
	queries *db.Queries
}

func NewStore(queries *db.Queries) *Store {
	return &Store{queries: queries}
}

func (s *Store) WithTx(tx pgx.Tx) *Store {
	if s == nil || s.queries == nil {
		return s
	}
	return &Store{queries: s.queries.WithTx(tx)}
}

func (s *Store) Get(ctx context.Context, id Identity) (db.SceneMemory, error) {
	id = id.normalized()
	if !id.valid() {
		return db.SceneMemory{}, ErrInvalidIdentity
	}
	return s.queries.GetSceneMemoryByIdentity(ctx, db.GetSceneMemoryByIdentityParams{
		WorkspaceID: id.WorkspaceID,
		AgentID:     id.AgentID,
		Platform:    id.Platform,
		OrgID:       id.OrgID,
		SceneKey:    id.SceneKey,
	})
}

func (s *Store) GetByID(ctx context.Context, workspaceID, agentID, memoryID pgtype.UUID) (db.SceneMemory, error) {
	return s.queries.GetSceneMemoryByID(ctx, db.GetSceneMemoryByIDParams{
		ID:          memoryID,
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
}

func (s *Store) List(ctx context.Context, workspaceID, agentID pgtype.UUID, limit int32) ([]db.SceneMemory, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.queries.ListSceneMemoryByAgent(ctx, db.ListSceneMemoryByAgentParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		ListLimit:   limit,
	})
}

func (s *Store) MarkDirty(ctx context.Context, id Identity, trigger DirtyTrigger) (db.SceneMemory, error) {
	id = id.normalized()
	if !id.valid() {
		return db.SceneMemory{}, ErrInvalidIdentity
	}
	if trigger.OccurredAt.IsZero() {
		trigger.OccurredAt = time.Now().UTC()
	}
	return s.queries.UpsertSceneMemoryDirty(ctx, db.UpsertSceneMemoryDirtyParams{
		WorkspaceID:               id.WorkspaceID,
		AgentID:                   id.AgentID,
		Platform:                  id.Platform,
		OrgID:                     id.OrgID,
		SceneKey:                  id.SceneKey,
		SceneKind:                 id.SceneKind,
		SceneTitle:                id.SceneTitle,
		DirtyThroughAt:            timestamptz(trigger.OccurredAt),
		DirtyThroughEvidenceID:    strings.TrimSpace(trigger.EvidenceID),
		LastTriggerJobID:          trigger.JobID,
		LastTriggerCoordTraceID:   strings.TrimSpace(trigger.CoordTraceID),
		LastTriggerIdempotencyKey: strings.TrimSpace(trigger.IdempotencyKey),
	})
}

func (s *Store) Claim(ctx context.Context) (db.SceneMemory, error) {
	return s.queries.ClaimSceneMemory(ctx)
}

func (s *Store) Renew(ctx context.Context, row db.SceneMemory) error {
	n, err := s.queries.RenewSceneMemoryLease(ctx, db.RenewSceneMemoryLeaseParams{
		ID:         row.ID,
		LeaseToken: row.LeaseToken,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *Store) CommitBatch(ctx context.Context, row db.SceneMemory, batch CommitBatch) (db.SceneMemory, error) {
	if batch.ReplaceText && !ValidateMemoryText(batch.MemoryText) {
		return db.SceneMemory{}, ErrMemoryText
	}
	meta := batch.FlushMeta
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	if !json.Valid(meta) || len(meta) > MaxFlushMetaBytes {
		return db.SceneMemory{}, ErrFlushMeta
	}
	updated, err := s.queries.CommitSceneMemoryBatch(ctx, db.CommitSceneMemoryBatchParams{
		ReplaceText:            batch.ReplaceText,
		MemoryText:             batch.MemoryText,
		SourceCursorAt:         timestamptz(batch.SourceCursorAt),
		SourceCursorEvidenceID: strings.TrimSpace(batch.SourceCursorEvidenceID),
		LastFlushMeta:          meta,
		ID:                     row.ID,
		LeaseToken:             row.LeaseToken,
		ExpectedMemoryRevision: batch.ExpectedMemoryRevision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.SceneMemory{}, s.classifyLeaseWrite(ctx, row, ErrStaleRevision)
	}
	return updated, err
}

func (s *Store) FinishClaim(ctx context.Context, row db.SceneMemory) error {
	n, err := s.queries.FinishSceneMemoryClaim(ctx, db.FinishSceneMemoryClaimParams{
		ID:         row.ID,
		LeaseToken: row.LeaseToken,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return s.classifyLeaseWrite(ctx, row, ErrNotCaughtUp)
	}
	return nil
}

func (s *Store) Retry(ctx context.Context, row db.SceneMemory, delay time.Duration, code, message string) error {
	if delay < 5*time.Second {
		delay = 5 * time.Second
	}
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	n, err := s.queries.RetrySceneMemoryClaim(ctx, db.RetrySceneMemoryClaimParams{
		DelaySeconds:  delay.Seconds(),
		LastErrorCode: clipErr(code, 64),
		LastError:     clipErr(message, 500),
		ID:            row.ID,
		LeaseToken:    row.LeaseToken,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *Store) ReleasePending(ctx context.Context, row db.SceneMemory) error {
	n, err := s.queries.ReleaseSceneMemoryPending(ctx, db.ReleaseSceneMemoryPendingParams{
		ID:         row.ID,
		LeaseToken: row.LeaseToken,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *Store) Block(ctx context.Context, row db.SceneMemory, code, message string) error {
	n, err := s.queries.BlockSceneMemory(ctx, db.BlockSceneMemoryParams{
		LastErrorCode: clipErr(code, 64),
		LastError:     clipErr(message, 500),
		ID:            row.ID,
		LeaseToken:    row.LeaseToken,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *Store) Reset(ctx context.Context, id Identity, cutoff DirtyTrigger) (db.SceneMemory, error) {
	id = id.normalized()
	if !id.valid() {
		return db.SceneMemory{}, ErrInvalidIdentity
	}
	if cutoff.OccurredAt.IsZero() {
		cutoff.OccurredAt = time.Now().UTC()
	}
	return s.queries.ResetSceneMemory(ctx, db.ResetSceneMemoryParams{
		WorkspaceID:            id.WorkspaceID,
		AgentID:                id.AgentID,
		Platform:               id.Platform,
		OrgID:                  id.OrgID,
		SceneKey:               id.SceneKey,
		SceneKind:              id.SceneKind,
		SceneTitle:             id.SceneTitle,
		SourceCursorAt:         timestamptz(cutoff.OccurredAt),
		SourceCursorEvidenceID: strings.TrimSpace(cutoff.EvidenceID),
	})
}

func (s *Store) CountValidLeases(ctx context.Context) (int64, error) {
	return s.queries.CountValidSceneMemoryLeases(ctx)
}

func (s *Store) DeleteByWorkspace(ctx context.Context, workspaceID pgtype.UUID) error {
	return s.queries.DeleteSceneMemoryByWorkspace(ctx, workspaceID)
}

func (s *Store) DeleteByAgent(ctx context.Context, workspaceID, agentID pgtype.UUID) error {
	return s.queries.DeleteSceneMemoryByAgent(ctx, db.DeleteSceneMemoryByAgentParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
}

func (s *Store) classifyLeaseWrite(ctx context.Context, row db.SceneMemory, fallback error) error {
	current, err := s.GetByID(ctx, row.WorkspaceID, row.AgentID, row.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	}
	if !leaseOwned(current, row) {
		return ErrLeaseLost
	}
	return fallback
}

func leaseOwned(current, claimed db.SceneMemory) bool {
	if !current.LeaseToken.Valid || !claimed.LeaseToken.Valid {
		return false
	}
	if current.LeaseToken != claimed.LeaseToken {
		return false
	}
	if !current.LeaseExpiresAt.Valid || !current.LeaseExpiresAt.Time.After(time.Now().UTC()) {
		return false
	}
	return true
}

func RetryDelay(attempt int32) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 5 * time.Second
	for i := int32(1); i < attempt; i++ {
		delay *= 2
		if delay >= 15*time.Minute {
			return 15 * time.Minute
		}
	}
	return delay
}

func StatusOf(row db.SceneMemory) string {
	if row.BlockedAt.Valid {
		return "blocked"
	}
	if row.LeaseToken.Valid && row.LeaseExpiresAt.Valid && row.LeaseExpiresAt.Time.After(time.Now()) {
		return "running"
	}
	if row.DirtyRevision > row.FlushedRevision {
		if row.LastError != "" {
			return "retrying"
		}
		return "pending"
	}
	return "clean"
}

func clipErr(raw string, n int) string {
	raw = strings.TrimSpace(raw)
	if n <= 0 {
		return ""
	}
	if len(raw) <= n {
		return raw
	}
	for n > 0 && !utf8.RuneStart(raw[n]) {
		n--
	}
	return raw[:n]
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}
