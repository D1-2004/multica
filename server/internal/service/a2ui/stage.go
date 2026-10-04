package a2ui

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
)

// Stage persists the exact projection without contacting a provider. Hosts call
// it using a transaction-bound Service alongside their question and outbox.
func (s *Service) Stage(ctx context.Context, id uuid.UUID, req OpenRequest) (Interaction, []string, error) {
	if s == nil || s.store == nil || id == uuid.Nil {
		return Interaction{}, nil, ErrInvalid
	}
	row, err := normalize(req)
	if err != nil {
		return row, nil, err
	}
	row.ID = id
	ref := Ref{Family: row.Kind.Family(), ID: id}
	row.PublicID = ref.PublicID()
	row.spec.SurfaceID = ref.SurfaceID()
	row.Status = StatusOpen
	old, e := s.store.GetByPublicID(ctx, row.PublicID)
	if e == nil {
		a, _ := json.Marshal(row.spec)
		b, _ := json.Marshal(old.spec)
		if string(a) != string(b) || old.AgentID != row.AgentID || old.WorkspaceID != row.WorkspaceID || old.SceneID != row.SceneID || old.SourceRef != row.SourceRef || old.SenderOrgID != row.SenderOrgID || old.SenderUID != row.SenderUID || old.ConversationID != row.ConversationID || old.Question != row.Question || old.Header != row.Header {
			return row, nil, ErrConflict
		}
		row = old
	} else if !errors.Is(e, ErrNotFound) {
		return row, nil, e
	} else if err = s.store.Insert(ctx, row); err != nil {
		return row, nil, err
	}
	messages, err := projectCard(row.PublicID, row.spec.SurfaceID, row.Kind, row.Header, row.Question, row.spec)
	return row, messages, err
}

// NativeAnswer is parsed evidence, not task-routing authority.
type NativeAnswer struct {
	PublicID, EventID, Operator string
	Selected                    []string
	Custom, Outcome             string
}

func NativeReference(line []byte) (string, bool) {
	got, e := parseClick(line)
	return got.PublicID, e == nil
}
func (s *Service) InspectNativeAnswer(ctx context.Context, actor Actor, line []byte) (NativeAnswer, error) {
	got, err := parseClick(line)
	if err != nil {
		return NativeAnswer{}, err
	}
	row, err := s.store.GetByPublicID(ctx, got.PublicID)
	if err != nil {
		return NativeAnswer{}, err
	}
	if !allows(actor, row, got) {
		return NativeAnswer{}, ErrInvalid
	}
	if got.Outcome != "answered" && got.Outcome != "skipped" {
		return NativeAnswer{}, ErrInvalid
	}
	return NativeAnswer{got.PublicID, got.EventID, got.Operator, got.Selected, got.Custom, got.Outcome}, nil
}
