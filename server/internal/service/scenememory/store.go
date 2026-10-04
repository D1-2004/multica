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

// Get returns the memory of a scene; a scene without memory is
// pgx.ErrNoRows.
func (s *Store) Get(ctx context.Context, sc db.AgentScene) (Memory, error) {
	if !validScene(sc) {
		return Memory{}, ErrInvalidIdentity
	}
	row, err := s.queries.GetAgentSceneMemory(ctx, db.GetAgentSceneMemoryParams{
		SceneID:     sc.ID,
		WorkspaceID: sc.WorkspaceID,
		AgentID:     sc.AgentID,
	})
	if err != nil {
		return Memory{}, err
	}
	return Memory{AgentSceneMemory: row, Scene: sc}, nil
}

// GetByScene loads an agent's scene by scene_id and its memory.
func (s *Store) GetByScene(ctx context.Context, workspaceID, agentID, sceneID pgtype.UUID) (Memory, error) {
	sc, err := s.queries.GetAgentScene(ctx, db.GetAgentSceneParams{ID: sceneID, WorkspaceID: workspaceID, AgentID: agentID})
	if err != nil {
		return Memory{}, err
	}
	return s.Get(ctx, sc)
}

func (s *Store) List(ctx context.Context, workspaceID, agentID pgtype.UUID, limit int32) ([]Memory, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.queries.ListAgentSceneMemoryByAgent(ctx, db.ListAgentSceneMemoryByAgentParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		ListLimit:   limit,
	})
	if err != nil {
		return nil, err
	}
	return s.withScenes(ctx, workspaceID, agentID, rows)
}

// withScenes pairs memory rows with their scenes; a row whose scene is gone
// is dropped.
func (s *Store) withScenes(ctx context.Context, workspaceID, agentID pgtype.UUID, rows []db.AgentSceneMemory) ([]Memory, error) {
	if len(rows) == 0 {
		return []Memory{}, nil
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.SceneID)
	}
	scenes, err := s.queries.ListAgentScenesByIDs(ctx, db.ListAgentScenesByIDsParams{WorkspaceID: workspaceID, AgentID: agentID, Ids: ids})
	if err != nil {
		return nil, err
	}
	byID := make(map[pgtype.UUID]db.AgentScene, len(scenes))
	for _, sc := range scenes {
		byID[sc.ID] = sc
	}
	out := make([]Memory, 0, len(rows))
	for _, row := range rows {
		if sc, ok := byID[row.SceneID]; ok {
			out = append(out, Memory{AgentSceneMemory: row, Scene: sc})
		}
	}
	return out, nil
}

func (s *Store) MarkDirty(ctx context.Context, sc db.AgentScene, trigger DirtyTrigger) (Memory, error) {
	if !validScene(sc) {
		return Memory{}, ErrInvalidIdentity
	}
	if trigger.OccurredAt.IsZero() {
		trigger.OccurredAt = time.Now().UTC()
	}
	row, err := s.queries.MarkAgentSceneMemoryDirty(ctx, db.MarkAgentSceneMemoryDirtyParams{
		SceneID:                   sc.ID,
		WorkspaceID:               sc.WorkspaceID,
		AgentID:                   sc.AgentID,
		DirtyThroughAt:            timestamptz(trigger.OccurredAt),
		DirtyThroughEvidenceID:    strings.TrimSpace(trigger.EvidenceID),
		LastTriggerJobID:          trigger.JobID,
		LastTriggerCoordTraceID:   strings.TrimSpace(trigger.CoordTraceID),
		LastTriggerIdempotencyKey: strings.TrimSpace(trigger.IdempotencyKey),
	})
	if err != nil {
		return Memory{}, err
	}
	return Memory{AgentSceneMemory: db.AgentSceneMemory(row), Scene: sc}, nil
}

// Claim leases the next dirty scene memory and loads its scene.
func (s *Store) Claim(ctx context.Context) (Memory, error) {
	row, err := s.queries.ClaimAgentSceneMemory(ctx)
	if err != nil {
		return Memory{}, err
	}
	sc, err := s.queries.GetAgentScene(ctx, db.GetAgentSceneParams{ID: row.SceneID, WorkspaceID: row.WorkspaceID, AgentID: row.AgentID})
	if err != nil {
		return Memory{AgentSceneMemory: row}, err
	}
	return Memory{AgentSceneMemory: row, Scene: sc}, nil
}

func (s *Store) Renew(ctx context.Context, row Memory) error {
	n, err := s.queries.RenewAgentSceneMemoryLease(ctx, db.RenewAgentSceneMemoryLeaseParams{
		SceneID:    row.SceneID,
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

// CommitBatch commits one flushed page. A non-empty batch title becomes the
// scene's title in the directory.
func (s *Store) CommitBatch(ctx context.Context, row Memory, batch CommitBatch) (Memory, error) {
	if batch.ReplaceText && !ValidateMemoryText(batch.MemoryText) {
		return Memory{}, ErrMemoryText
	}
	meta := batch.FlushMeta
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	if !json.Valid(meta) || len(meta) > MaxFlushMetaBytes {
		return Memory{}, ErrFlushMeta
	}
	updated, err := s.queries.CommitAgentSceneMemoryBatch(ctx, db.CommitAgentSceneMemoryBatchParams{
		ReplaceText:            batch.ReplaceText,
		MemoryText:             batch.MemoryText,
		SourceCursorAt:         timestamptz(batch.SourceCursorAt),
		SourceCursorEvidenceID: strings.TrimSpace(batch.SourceCursorEvidenceID),
		LastFlushMeta:          meta,
		SceneID:                row.SceneID,
		LeaseToken:             row.LeaseToken,
		ExpectedMemoryRevision: batch.ExpectedMemoryRevision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Memory{}, s.classifyLeaseWrite(ctx, row, ErrStaleRevision)
	}
	if err != nil {
		return Memory{}, err
	}
	sc := row.Scene
	if title := strings.TrimSpace(batch.SceneTitle); title != "" && title != sc.Title && sc.ID.Valid {
		touched, touchErr := s.queries.TouchAgentScene(ctx, db.TouchAgentSceneParams{
			Title: title, ID: sc.ID, WorkspaceID: sc.WorkspaceID, AgentID: sc.AgentID,
			LastActiveAt: sc.LastActiveAt,
		})
		if touchErr == nil {
			sc = touched
		}
	}
	return Memory{AgentSceneMemory: updated, Scene: sc}, nil
}

func (s *Store) FinishClaim(ctx context.Context, row Memory) error {
	n, err := s.queries.FinishAgentSceneMemoryClaim(ctx, db.FinishAgentSceneMemoryClaimParams{
		SceneID:    row.SceneID,
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

func (s *Store) Retry(ctx context.Context, row Memory, delay time.Duration, code, message string) error {
	if delay < 5*time.Second {
		delay = 5 * time.Second
	}
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	n, err := s.queries.RetryAgentSceneMemoryClaim(ctx, db.RetryAgentSceneMemoryClaimParams{
		DelaySeconds:  delay.Seconds(),
		LastErrorCode: clipErr(code, 64),
		LastError:     clipErr(message, 500),
		SceneID:       row.SceneID,
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

func (s *Store) ReleasePending(ctx context.Context, row Memory) error {
	n, err := s.queries.ReleaseAgentSceneMemoryPending(ctx, db.ReleaseAgentSceneMemoryPendingParams{
		SceneID:    row.SceneID,
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

func (s *Store) Block(ctx context.Context, row Memory, code, message string) error {
	n, err := s.queries.BlockAgentSceneMemory(ctx, db.BlockAgentSceneMemoryParams{
		LastErrorCode: clipErr(code, 64),
		LastError:     clipErr(message, 500),
		SceneID:       row.SceneID,
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

func (s *Store) ReplaceText(ctx context.Context, row Memory, expectedRevision int64, text string) (Memory, error) {
	if !ValidateMemoryText(text) {
		return Memory{}, ErrMemoryText
	}
	updated, err := s.queries.ReplaceAgentSceneMemoryText(ctx, db.ReplaceAgentSceneMemoryTextParams{
		MemoryText:             text,
		SceneID:                row.SceneID,
		WorkspaceID:            row.WorkspaceID,
		AgentID:                row.AgentID,
		ExpectedMemoryRevision: expectedRevision,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Memory{}, ErrStaleRevision
		}
		return Memory{}, err
	}
	return Memory{AgentSceneMemory: updated, Scene: row.Scene}, nil
}

func (s *Store) Reset(ctx context.Context, sc db.AgentScene, cutoff DirtyTrigger) (Memory, error) {
	if !validScene(sc) {
		return Memory{}, ErrInvalidIdentity
	}
	if cutoff.OccurredAt.IsZero() {
		cutoff.OccurredAt = time.Now().UTC()
	}
	row, err := s.queries.ResetAgentSceneMemory(ctx, db.ResetAgentSceneMemoryParams{
		SceneID:                sc.ID,
		WorkspaceID:            sc.WorkspaceID,
		AgentID:                sc.AgentID,
		SourceCursorAt:         timestamptz(cutoff.OccurredAt),
		SourceCursorEvidenceID: strings.TrimSpace(cutoff.EvidenceID),
	})
	if err != nil {
		return Memory{}, err
	}
	return Memory{AgentSceneMemory: row, Scene: sc}, nil
}

func (s *Store) CountValidLeases(ctx context.Context) (int64, error) {
	return s.queries.CountValidAgentSceneMemoryLeases(ctx)
}

func (s *Store) DeleteByWorkspace(ctx context.Context, workspaceID pgtype.UUID) error {
	return s.queries.DeleteAgentSceneMemoryByWorkspace(ctx, workspaceID)
}

func (s *Store) DeleteByAgent(ctx context.Context, workspaceID, agentID pgtype.UUID) error {
	return s.queries.DeleteAgentSceneMemoryByAgent(ctx, db.DeleteAgentSceneMemoryByAgentParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
}

func (s *Store) classifyLeaseWrite(ctx context.Context, row Memory, fallback error) error {
	current, err := s.queries.GetAgentSceneMemory(ctx, db.GetAgentSceneMemoryParams{
		SceneID: row.SceneID, WorkspaceID: row.WorkspaceID, AgentID: row.AgentID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	}
	if !leaseOwned(current, row.AgentSceneMemory) {
		return ErrLeaseLost
	}
	return fallback
}

func leaseOwned(current, claimed db.AgentSceneMemory) bool {
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
	return retryDelayWithCap(attempt, 15*time.Minute)
}

// RetryDelayFor keeps history/evidence gaps on the long backoff, but an LLM
// timeout must not inherit a catch-up attempt_count and park the scene for
// 15 minutes. Successful pages reset attempt_count; consecutive timeouts still
// climb, capped at one minute.
func RetryDelayFor(code string, attempt int32) time.Duration {
	if code == ErrorLLMTimeout {
		return retryDelayWithCap(attempt, time.Minute)
	}
	return RetryDelay(attempt)
}

func retryDelayWithCap(attempt int32, capDelay time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if capDelay < 5*time.Second {
		return 5 * time.Second
	}
	delay := 5 * time.Second
	for i := int32(1); i < attempt; i++ {
		delay *= 2
		if delay >= capDelay {
			return capDelay
		}
	}
	return delay
}

func StatusOf(row Memory) string {
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
