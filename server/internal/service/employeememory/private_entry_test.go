package employeememory

import (
	"context"
	"testing"
	"time"
)

func TestPrivateObservationOrderAndLegacyTimestamp(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordered", true: "legacy_without_evidence_time"}[legacy], func(t *testing.T) {
			pool := memoryPool(t)
			scope := memoryScope(t, pool)
			scope.Kind = ScopePrivate
			scope.PrincipalID = "requester"
			store := NewStore(pool)
			ctx := context.Background()
			at := time.Now().UTC()
			write := func(key, content, source string, when time.Time) LearningRecord {
				t.Helper()
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				rec, err := store.RecordPrivateObservationTx(ctx, tx, scope, LearningRecord{Type: LearningTypePreference, Key: key, Insight: content, Source: LearningSourceObserved, Confidence: 4}, TrustedEvidence{SourceID: source, EvidenceID: source, ActorID: "requester", OccurredAt: when})
				if err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				return rec
			}
			current := write("color", "newer-value", "newer-source", at)
			if legacy {
				if _, err := pool.Exec(ctx, `UPDATE employee_learning SET record=record-'evidence_occurred_at' WHERE id=$1`, current.ID); err != nil {
					t.Fatal(err)
				}
			}
			late := write("color", "old-value", "old-source", at.Add(-time.Hour))
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			entry, err := store.PrivateEntryTx(ctx, tx, scope, late.ID)
			if err != nil || entry.State != "superseded" {
				t.Fatalf("late capture overwrote current: %+v %v", entry, err)
			}
			rows, err := store.SearchTx(ctx, tx, scope, "", 8)
			if err != nil || len(rows) != 1 || rows[0].ID != current.ID {
				t.Fatalf("current lost: %+v %v", rows, err)
			}
			_ = tx.Rollback(ctx)
			replay := write("other-key", "different-proposal", "old-source", at.Add(-time.Hour))
			if replay.ID != late.ID || replay.Key != "color" {
				t.Fatal("late evidence escaped its tombstone")
			}
			equal := write("color", "same-time-value", "same-time-source", at)
			tx, _ = pool.Begin(ctx)
			entry, err = store.PrivateEntryTx(ctx, tx, scope, equal.ID)
			_ = tx.Rollback(ctx)
			if err != nil || entry.State != "superseded" {
				t.Fatal("equal-time source replaced current")
			}
			latest := write("color", "future-value", "future-source", at.Add(time.Hour))
			if latest.Supersedes != current.ID {
				t.Fatal("new source did not correct current")
			}
		})
	}
}

func TestPrivateEntryForgetIsTransactionalAndScoped(t *testing.T) {
	pool := memoryPool(t)
	scope := memoryScope(t, pool)
	scope.Kind = ScopePrivate
	scope.PrincipalID = "alice"
	store := NewStore(pool)
	ctx := context.Background()
	record := record(t, store, scope, note("key", "value"), evidence("one"))
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ForgetPrivateTx(ctx, tx, scope, record.ID); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	rows, err := store.Search(ctx, scope, "", 8)
	if err != nil || len(rows) != 1 {
		t.Fatal("rollback lost memory")
	}
	tx, _ = pool.Begin(ctx)
	defer tx.Rollback(ctx)
	wrong := scope
	wrong.PrincipalID = "bob"
	if _, err = store.ForgetPrivateTx(ctx, tx, wrong, record.ID); err != ErrEntryNotFound {
		t.Fatalf("cross-requester forget: %v", err)
	}
	_ = tx.Rollback(ctx)
	tx, _ = pool.Begin(ctx)
	if _, err = store.ForgetPrivateTx(ctx, tx, scope, record.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, _ = pool.Begin(ctx)
	entry, err := store.PrivateEntryTx(ctx, tx, scope, record.ID)
	_ = tx.Rollback(ctx)
	if err != nil || entry.State != "forgotten" {
		t.Fatal("forget tombstone missing")
	}
}

func TestPrivateObservationForgottenNewerRecordStillFencesLateSource(t *testing.T) {
	pool := memoryPool(t)
	scope := memoryScope(t, pool)
	scope.Kind = ScopePrivate
	scope.PrincipalID = "requester"
	store := NewStore(pool)
	ctx := context.Background()
	now := time.Now().UTC()
	tx, _ := pool.Begin(ctx)
	current, err := store.RecordPrivateObservationTx(ctx, tx, scope, LearningRecord{Type: LearningTypePreference, Key: "color", Insight: "GREEN", Source: LearningSourceObserved, Confidence: 4}, TrustedEvidence{SourceID: "new", EvidenceID: "new", ActorID: "requester", OccurredAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ForgetPrivateTx(ctx, tx, scope, current.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, _ = pool.Begin(ctx)
	defer tx.Rollback(ctx)
	late, err := store.RecordPrivateObservationTx(ctx, tx, scope, LearningRecord{Type: LearningTypePreference, Key: "color", Insight: "BLUE", Source: LearningSourceObserved, Confidence: 4}, TrustedEvidence{SourceID: "old", EvidenceID: "old", ActorID: "requester", OccurredAt: now.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.PrivateEntryTx(ctx, tx, scope, late.ID)
	if err != nil || entry.State != "superseded" {
		t.Fatalf("late source resurrected forgotten fact: %+v %v", entry, err)
	}
	rows, err := store.SearchTx(ctx, tx, scope, "", 8)
	if err != nil || len(rows) != 0 {
		t.Fatal("forgotten latest value lost ordering fence")
	}
}
