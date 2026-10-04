package a2ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

// ResolvedProjection derives the closed display from the persisted candidates.
// The caller owns answer admission and durable update retries; this method has
// no write effects and never trusts caller-supplied labels or task routing.
func (s *Service) ResolvedProjection(ctx context.Context, publicID string, result Result) ([]string, error) {
	row, err := s.Get(ctx, publicID)
	if err != nil {
		return nil, err
	}
	if row.Kind != KindConfirm && row.Kind != KindChoose && row.Kind != KindApproval {
		return nil, fmt.Errorf("%w: resolved projection requires frozen choices", ErrInvalid)
	}
	ref, ok := ParseRef(row.PublicID)
	if !ok || ref.ID != row.ID || ref.Family != row.Kind.Family() {
		return nil, fmt.Errorf("%w: resolved projection reference", ErrInvalid)
	}
	surfaceID := row.spec.SurfaceID
	if surfaceID == "" {
		surfaceID = ref.SurfaceID()
	}
	if surfaceID != ref.SurfaceID() {
		return nil, fmt.Errorf("%w: resolved projection surface", ErrInvalid)
	}
	if result.Outcome == string(StatusSkipped) || result.Outcome == "disabled" {
		return projectResolvedAsk(surfaceID, row.Question, nil, "", result.Outcome)
	}
	switch result.Outcome {
	case string(StatusAnswered):
	case string(StatusApproved), string(StatusRejected):
		if row.Kind != KindApproval {
			return nil, fmt.Errorf("%w: resolved projection outcome", ErrInvalid)
		}
	default:
		return nil, fmt.Errorf("%w: resolved projection outcome", ErrInvalid)
	}
	if !row.spec.Multiple && len(result.Selected) > 1 {
		return nil, fmt.Errorf("%w: resolved projection selection count", ErrInvalid)
	}
	byID := make(map[string]string, len(row.spec.Options))
	for _, option := range row.spec.Options {
		byID[option.ID] = compactOptionLabel(option)
	}
	labels := make([]string, 0, len(result.Selected))
	seen := make(map[string]bool, len(result.Selected))
	for _, id := range result.Selected {
		label, found := byID[id]
		if !found || seen[id] {
			return nil, fmt.Errorf("%w: resolved projection option", ErrInvalid)
		}
		seen[id] = true
		labels = append(labels, label)
	}
	custom := strings.TrimSpace(result.Custom)
	if result.Outcome == string(StatusApproved) || result.Outcome == string(StatusRejected) {
		index := 0
		if result.Outcome == string(StatusRejected) {
			index = 1
		}
		if len(row.spec.Options) < 2 || len(result.Selected) != 1 || result.Selected[0] != row.spec.Options[index].ID {
			return nil, fmt.Errorf("%w: resolved projection approval decision", ErrInvalid)
		}
	}
	if len(labels) == 0 && custom == "" {
		return nil, fmt.Errorf("%w: resolved projection empty answer", ErrInvalid)
	}
	return projectResolvedAsk(surfaceID, row.Question, labels, custom, result.Outcome)
}
