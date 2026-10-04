package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/service/employeememory/digest"
)

// employeeSceneFacts adapts M5's scene-shared fact store to the digest
// writer. Every write is CaptureOriginFlush (synthesis, confidence 3,
// untrusted, never pinned) with the writer's own actor; refusals that the
// store raises for one statement become per-operation rejections.
type employeeSceneFacts struct{ store *employeememory.Store }

// NewEmployeeSceneFacts returns nil when the memory store is not wired.
func NewEmployeeSceneFacts(store *employeememory.Store) digest.FactStore {
	if store == nil {
		return nil
	}
	return employeeSceneFacts{store: store}
}

func employeeDigestScope(key digest.SceneKey) employeememory.Scope {
	scope := employeememory.Scope{WorkspaceID: parseUUID(key.WorkspaceID), AgentID: parseUUID(key.AgentID), TenantOrgID: key.TenantOrgID, Kind: employeememory.ScopeScene}
	scope.Scene.SceneID = key.SceneID
	return scope
}

func (f employeeSceneFacts) ActiveFacts(ctx context.Context, tx pgx.Tx, key digest.SceneKey, limit int) ([]digest.Fact, error) {
	records, err := f.store.ActiveSceneFactsTx(ctx, tx, employeeDigestScope(key), limit)
	if err != nil {
		return nil, err
	}
	out := make([]digest.Fact, 0, len(records))
	for _, rec := range records {
		out = append(out, digest.Fact{ID: rec.ID, Type: string(rec.Type), Key: rec.Key, Subject: rec.Subject, Insight: rec.Insight, Source: string(rec.Source), CaptureOrigin: string(rec.CaptureOrigin), CreatedBy: rec.CreatedBy, SpeakerName: rec.SpeakerName, EvidenceID: rec.EvidenceID, ConflictsWith: rec.ConflictsWith, CreatedAt: rec.CreatedAt})
	}
	return out, nil
}

func (employeeSceneFacts) HostKey(kind, subject string) (string, error) {
	return employeememory.HostKey(employeememory.LearningType(kind), subject)
}

func (f employeeSceneFacts) Upsert(ctx context.Context, tx pgx.Tx, key digest.SceneKey, actor string, in digest.FactUpsert) (digest.FactWrite, error) {
	e := in.Evidence
	entry, err := f.store.UpsertSceneFactTx(ctx, tx, employeeDigestScope(key), employeememory.SceneFactInput{
		Type: employeememory.LearningType(in.Kind), Subject: in.Subject, Quote: in.Quote, Origin: employeememory.CaptureOriginFlush, ActorID: actor,
		Grounding: employeememory.SceneFactGrounding{MessageID: e.ProviderMessageID, Text: e.Body, SpeakerRef: e.SenderRef, SpeakerName: e.SenderName, SpeakerClass: e.SenderClass, SaidAt: e.SentAt},
	})
	if err != nil {
		return digest.FactWrite{}, employeeSceneFactRefusal(err)
	}
	return digest.FactWrite{ID: entry.Record.ID, Replayed: entry.Replayed, Changed: entry.Changed}, nil
}

func (f employeeSceneFacts) Retract(ctx context.Context, tx pgx.Tx, key digest.SceneKey, id, actor string) error {
	_, err := f.store.RetractSceneFactTx(ctx, tx, employeeDigestScope(key), id, actor)
	return employeeSceneFactRefusal(err)
}

// employeeSceneFactRefusal keeps store errors that refuse one statement
// local to that operation; anything else aborts the flush commit.
func employeeSceneFactRefusal(err error) error {
	for _, refusal := range []struct {
		err    error
		reason string
	}{
		{employeememory.ErrUngroundedQuote, "ungrounded_quote"},
		{employeememory.ErrPreResetEvidence, "pre_reset_evidence"},
		{employeememory.ErrUntrustedCorrection, "trusted_record_exists"},
		{employeememory.ErrSceneKindNotShared, "scene_kind_not_shared"},
		{employeememory.ErrSceneRetractDenied, "retract_denied"},
		{employeememory.ErrInvalidLearning, "invalid_learning"},
	} {
		if errors.Is(err, refusal.err) {
			return &digest.OpRejectedError{Reason: refusal.reason, Err: err}
		}
	}
	return err
}
