package employeememory

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var ErrEntryNotFound = errors.New("employee memory record not found in this namespace")

// PrivateEntry reports the current state, including replay tombstones. A prior
// capture receipt is not evidence that a forgotten or superseded record is active.
type PrivateEntry struct {
	Record  LearningRecord `json:"record"`
	State   string         `json:"state"`
	Changed bool           `json:"changed,omitempty"`
}

func (s *Store) PrivateEntryTx(ctx context.Context, tx pgx.Tx, scope Scope, id string) (PrivateEntry, error) {
	if tx == nil || scope.Kind != ScopePrivate {
		return PrivateEntry{}, ErrInvalidScope
	}
	if _, err := uuid.Parse(id); err != nil {
		return PrivateEntry{}, ErrInvalidLearning
	}
	if err := authorize(ctx, db.New(tx), scope); err != nil {
		return PrivateEntry{}, err
	}
	return readPrivateEntry(ctx, tx, scope, id)
}
func readPrivateEntry(ctx context.Context, tx pgx.Tx, scope Scope, id string) (PrivateEntry, error) {
	var raw []byte
	entry := PrivateEntry{}
	err := tx.QueryRow(ctx, `SELECT record,CASE WHEN forgotten_at IS NOT NULL THEN 'forgotten' WHEN superseded_by IS NOT NULL THEN 'superseded' ELSE 'active' END FROM employee_learning WHERE `+scopePredicate+` AND id=$7::uuid`, append(scope.args(), id)...).Scan(&raw, &entry.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return PrivateEntry{}, ErrEntryNotFound
	}
	if err != nil {
		return PrivateEntry{}, err
	}
	err = json.Unmarshal(raw, &entry.Record)
	return entry, err
}

// ForgetPrivateTx forgets one exact record, never its replacement. Namespace
// locking serializes correction/reset/deletion; the replay row stays in place.
func (s *Store) ForgetPrivateTx(ctx context.Context, tx pgx.Tx, scope Scope, id string) (PrivateEntry, error) {
	if tx == nil || scope.Kind != ScopePrivate {
		return PrivateEntry{}, ErrInvalidScope
	}
	if _, err := uuid.Parse(id); err != nil {
		return PrivateEntry{}, ErrInvalidLearning
	}
	if err := lockWriteScope(ctx, tx, scope); err != nil {
		return PrivateEntry{}, err
	}
	entry, err := readPrivateEntry(ctx, tx, scope, id)
	if err != nil || entry.State == "forgotten" {
		return entry, err
	}
	if _, err = tx.Exec(ctx, `UPDATE employee_learning SET forgotten_at=now() WHERE `+scopePredicate+` AND id=$7::uuid`, append(scope.args(), id)...); err != nil {
		return PrivateEntry{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE employee_memory_state SET revision=revision+1,updated_at=now() WHERE `+scopePredicate, scope.args()...); err != nil {
		return PrivateEntry{}, err
	}
	entry.State, entry.Changed = "forgotten", true
	return entry, nil
}

// RecordPrivateObservationTx is the ordered capture policy for admitted messages.
// Late/equal evidence receives an inactive receipt rather than replacing newer
// memory; its replay identity stays consumed even if the proposal changes keys.
// Background Record/RecordTx callers retain their existing correction policy.
func (s *Store) RecordPrivateObservationTx(ctx context.Context, tx pgx.Tx, scope Scope, rec LearningRecord, e TrustedEvidence) (LearningRecord, error) {
	if tx == nil || scope.Kind != ScopePrivate {
		return LearningRecord{}, ErrInvalidScope
	}
	if e.HumanStated || e.VerifiedExecution || e.OccurredAt.IsZero() || rec.Source != LearningSourceObserved {
		return LearningRecord{}, ErrInvalidLearning
	}
	normalized, err := normalizeRecord(rec, scope, e)
	if err != nil {
		return LearningRecord{}, err
	}
	normalized.EvidenceOccurredAt = e.OccurredAt
	if err = lockWriteScope(ctx, tx, scope); err != nil {
		return LearningRecord{}, err
	}
	return recordLocked(ctx, tx, scope, normalized, e, true)
}
