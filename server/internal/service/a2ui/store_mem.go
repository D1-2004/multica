package a2ui

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
)

type memStore struct {
	mu       sync.Mutex
	rows     map[uuid.UUID]Interaction
	byPublic map[string]uuid.UUID
	byKey    map[string]uuid.UUID
}

func newMemStore() *memStore {
	return &memStore{rows: map[uuid.UUID]Interaction{}, byPublic: map[string]uuid.UUID{}, byKey: map[string]uuid.UUID{}}
}

func (s *memStore) Insert(_ context.Context, row Interaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byPublic[row.PublicID]; ok {
		return ErrConflict
	}
	key := idempotencyKey(row.AgentID, row.IdempotencyKey)
	if key != "" {
		if _, ok := s.byKey[key]; ok {
			return ErrConflict
		}
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	}
	s.rows[row.ID] = row.clone()
	s.byPublic[row.PublicID] = row.ID
	if key != "" {
		s.byKey[key] = row.ID
	}
	return nil
}

func (s *memStore) GetByPublicID(_ context.Context, publicID string) (Interaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byPublic[publicID]
	if !ok {
		return Interaction{}, ErrNotFound
	}
	return s.rows[id].clone(), nil
}

func (s *memStore) GetByIdempotency(_ context.Context, agentID uuid.UUID, key string) (Interaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byKey[idempotencyKey(agentID, key)]
	if !ok {
		return Interaction{}, ErrNotFound
	}
	return s.rows[id].clone(), nil
}

func (s *memStore) MarkSent(_ context.Context, id uuid.UUID, bizID, messageID, conversationID string, status Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok {
		return ErrNotFound
	}
	if row.Status != StatusOpen && row.Status != StatusFailed {
		return nil
	}
	if messageID != "" {
		for _, other := range s.rows {
			if other.ID != id && other.AgentID == row.AgentID && other.MessageID == messageID {
				return ErrConflict
			}
		}
		row.MessageID = messageID
	}
	if conversationID != "" {
		row.ConversationID = conversationID
	}
	row.CardBizID = bizID
	row.Status = status
	s.rows[id] = row
	return nil
}

func (s *memStore) GetByMessage(_ context.Context, agentID uuid.UUID, messageID string) (Interaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.AgentID == agentID && row.MessageID == messageID && messageID != "" {
			return row.clone(), nil
		}
	}
	return Interaction{}, ErrNotFound
}

func (s *memStore) MarkFailed(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok {
		return ErrNotFound
	}
	if row.CardBizID == "" && row.Status == StatusOpen {
		row.Status = StatusFailed
		s.rows[id] = row
	}
	return nil
}

func (s *memStore) Resolve(_ context.Context, id uuid.UUID, eventID, operator string, status Status, result []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.EventID == eventID && row.ID != id {
			return false, ErrConflict
		}
	}
	row, ok := s.rows[id]
	if !ok {
		return false, ErrNotFound
	}
	if row.Status != StatusOpen {
		return false, nil
	}
	var decoded Result
	if json.Unmarshal(result, &decoded) != nil {
		return false, errMalformed
	}
	row.Status = status
	row.EventID = eventID
	row.OperatorUID = operator
	row.Result = decoded
	row.ResolvedAt = time.Now().UTC()
	s.rows[id] = row
	return true, nil
}

func (s *memStore) ListByMessage(_ context.Context, agentID uuid.UUID, sceneID, messageID string) ([]Interaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Interaction, 0)
	for _, row := range s.rows {
		if row.AgentID == agentID && row.SceneID == sceneID && row.MessageID == messageID && row.MessageID != "" {
			out = append(out, row.clone())
		}
	}
	sortInteractions(out)
	if len(out) > 50 {
		out = out[:50]
	}
	return out, nil
}

func sortInteractions(rows []Interaction) {
	for i := 1; i < len(rows); i++ {
		item := rows[i]
		j := i
		for j > 0 && rows[j-1].CreatedAt.Before(item.CreatedAt) {
			rows[j] = rows[j-1]
			j--
		}
		rows[j] = item
	}
}

func idempotencyKey(agentID uuid.UUID, key string) string {
	if key == "" {
		return ""
	}
	return agentID.String() + "\x00" + key
}
