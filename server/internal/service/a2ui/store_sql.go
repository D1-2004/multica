package a2ui

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// New stores interactions in Postgres through the sqlc queries.
func New(q *db.Queries) *Service {
	return &Service{store: sqlStore{q: q}}
}

type sqlStore struct{ q *db.Queries }

func (s sqlStore) Insert(ctx context.Context, row Interaction) error {
	if s.q == nil {
		return errors.New("a2ui store is not configured")
	}
	body, err := json.Marshal(row.spec)
	if err != nil {
		return err
	}
	_, err = s.q.InsertA2UIInteraction(ctx, db.InsertA2UIInteractionParams{
		ID: pgUUID(row.ID), PublicID: row.PublicID, WorkspaceID: pgUUID(row.WorkspaceID), AgentID: pgUUID(row.AgentID),
		SenderUid: row.SenderUID, SenderOrgID: row.SenderOrgID, SceneID: row.SceneID, ConversationID: row.ConversationID,
		MessageID: row.MessageID, ThreadID: row.ThreadID, SourceRef: row.SourceRef,
		Kind: string(row.Kind), Status: string(row.Status), Header: row.Header, Question: row.Question,
		Request: body, IdempotencyKey: row.IdempotencyKey,
	})
	return unique(err)
}

func (s sqlStore) GetByPublicID(ctx context.Context, publicID string) (Interaction, error) {
	if s.q == nil {
		return Interaction{}, errors.New("a2ui store is not configured")
	}
	row, err := s.q.GetA2UIInteractionByPublicID(ctx, publicID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Interaction{}, ErrNotFound
	}
	if err != nil {
		return Interaction{}, err
	}
	return interactionFrom(row)
}

func (s sqlStore) GetByIdempotency(ctx context.Context, agentID uuid.UUID, key string) (Interaction, error) {
	if s.q == nil {
		return Interaction{}, errors.New("a2ui store is not configured")
	}
	row, err := s.q.GetA2UIInteractionByIdempotency(ctx, db.GetA2UIInteractionByIdempotencyParams{
		AgentID: pgUUID(agentID), IdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Interaction{}, ErrNotFound
	}
	if err != nil {
		return Interaction{}, err
	}
	return interactionFrom(row)
}

func (s sqlStore) MarkSent(ctx context.Context, id uuid.UUID, bizID, messageID, conversationID string, status Status) error {
	if s.q == nil {
		return errors.New("a2ui store is not configured")
	}
	return unique(s.q.MarkA2UIInteractionSent(ctx, db.MarkA2UIInteractionSentParams{
		CardBizID: bizID, MessageID: messageID, ConversationID: conversationID, Status: string(status), ID: pgUUID(id),
	}))
}

func (s sqlStore) GetByMessage(ctx context.Context, agentID uuid.UUID, messageID string) (Interaction, error) {
	if s.q == nil {
		return Interaction{}, errors.New("a2ui store is not configured")
	}
	row, err := s.q.GetA2UIInteractionByMessage(ctx, db.GetA2UIInteractionByMessageParams{
		AgentID: pgUUID(agentID), MessageID: messageID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Interaction{}, ErrNotFound
	}
	if err != nil {
		return Interaction{}, err
	}
	return interactionFrom(row)
}

func (s sqlStore) MarkFailed(ctx context.Context, id uuid.UUID) error {
	if s.q == nil {
		return errors.New("a2ui store is not configured")
	}
	return s.q.MarkA2UIInteractionFailed(ctx, pgUUID(id))
}

func (s sqlStore) ListByMessage(ctx context.Context, agentID uuid.UUID, sceneID, messageID string) ([]Interaction, error) {
	if s.q == nil {
		return nil, errors.New("a2ui store is not configured")
	}
	rows, err := s.q.ListA2UIInteractionsBySceneMessage(ctx, db.ListA2UIInteractionsBySceneMessageParams{
		AgentID: pgUUID(agentID), SceneID: sceneID, MessageID: messageID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Interaction, 0, len(rows))
	for _, row := range rows {
		item, err := interactionFrom(row)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func (s sqlStore) Resolve(ctx context.Context, id uuid.UUID, eventID, operator string, status Status, result []byte) (bool, error) {
	if s.q == nil {
		return false, errors.New("a2ui store is not configured")
	}
	_, err := s.q.ResolveA2UIInteraction(ctx, db.ResolveA2UIInteractionParams{
		Status: string(status), EventID: eventID, OperatorUid: operator, Result: result, ID: pgUUID(id),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, unique(err)
	}
	return true, nil
}

func interactionFrom(row db.A2uiInteraction) (Interaction, error) {
	var spec storedRequest
	if len(row.Request) > 0 && string(row.Request) != "null" {
		if err := json.Unmarshal(row.Request, &spec); err != nil {
			return Interaction{}, err
		}
	}
	var result Result
	if len(row.Result) > 0 && string(row.Result) != "{}" && string(row.Result) != "null" {
		if err := json.Unmarshal(row.Result, &result); err != nil {
			return Interaction{}, err
		}
	}
	out := Interaction{
		ID: uuidFrom(row.ID), PublicID: row.PublicID, WorkspaceID: uuidFrom(row.WorkspaceID), AgentID: uuidFrom(row.AgentID),
		SenderUID: row.SenderUid, SenderOrgID: row.SenderOrgID, SceneID: row.SceneID, ConversationID: row.ConversationID,
		MessageID: row.MessageID, ThreadID: row.ThreadID, SourceRef: row.SourceRef,
		Kind: Kind(row.Kind), Status: Status(row.Status), Header: row.Header, Question: row.Question,
		CardBizID: row.CardBizID, EventID: row.EventID, OperatorUID: row.OperatorUid, IdempotencyKey: row.IdempotencyKey,
		Result: result, CreatedAt: timeFrom(row.CreatedAt), ResolvedAt: timeFrom(row.ResolvedAt), spec: spec,
	}
	return out, nil
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	if id == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: [16]byte(id), Valid: true}
}

func uuidFrom(id pgtype.UUID) uuid.UUID {
	if !id.Valid {
		return uuid.Nil
	}
	return uuid.UUID(id.Bytes)
}

func timeFrom(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}

func unique(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}
