package taskinput

import (
	"context"
	"errors"
	"testing"
	"time"
)

// partiallyClosed answers one of three invitations and partially closes the
// collection, leaving one pending ready intent at the close revision.
func partiallyClosed(t *testing.T, f fixture) (Collection, ReadyIntent, CloseParams) {
	t.Helper()
	ctx := context.Background()
	task := f.newTask(t, "Collect")
	col, invitations := f.create(t, task, spec(f.dmC, participantC, "Number?"), spec(f.dmD, participantD, "Number?"), spec(f.group, participantDE, "Number?"))
	invitations = f.deliverAll(t, invitations)
	answered := f.mustAccept(t, answer(col, invitations[0], "c-1", "7"))
	partial := CloseParams{CollectionID: col.ID, Mode: ClosePartial, Reason: "summarize what arrived",
		Source: Source{Namespace: "employee.tool", Key: "partial/" + col.ID}, Authority: requesterAuthority(f.origin), ExpectedRevision: answered.Collection.Revision}
	closed, ready, err := f.store.CloseCollectionTx(ctx, f.scope, partial)
	if err != nil || ready == nil || closed.State != CollectionReady {
		t.Fatalf("partial close: %+v %+v %v", closed, ready, err)
	}
	return closed, *ready, partial
}

func (f fixture) priorClose(t *testing.T, collectionID string) (mode, actor, reason string) {
	t.Helper()
	err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(close_payload->'prior'->>'mode',''),
 COALESCE(close_payload->'prior'->'authority'->>'actor_ref',''), COALESCE(close_payload->'prior'->>'reason','')
 FROM employee_task_collection WHERE id=$1::uuid`, collectionID).Scan(&mode, &actor, &reason)
	if err != nil {
		t.Fatal(err)
	}
	return mode, actor, reason
}

// After a partial close the requester or the Host can still stop the summary
// before it is sent: the collection ends, the pending ready intent is
// superseded and can no longer be admitted, and the partial-close audit is
// kept.
func TestCancelAfterPartialCloseInvalidatesReadyIntent(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	closed, ready, partial := partiallyClosed(t, f)

	cancel := CloseParams{CollectionID: closed.ID, Mode: CloseCancel, Reason: "no longer needed", Source: Source{Namespace: "employee.tool", Key: "cancel/" + closed.ID},
		Authority: Authority{ActorRef: participantC, SceneID: f.dmC, ReceiptRef: "r", VerifiedAt: time.Now()}, ExpectedRevision: closed.Revision}
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, cancel); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a participant cancels after a partial close: %v", err)
	}
	cancel.Authority = requesterAuthority(f.origin)
	stale := cancel
	stale.ExpectedRevision = closed.Revision - 1
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, stale); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale cancel: %v", err)
	}
	cancelled, none, err := f.store.CloseCollectionTx(ctx, f.scope, cancel)
	if err != nil || none != nil {
		t.Fatalf("cancel after partial close: %+v %v", cancelled, err)
	}
	if cancelled.State != CollectionCancelled || cancelled.Revision != closed.Revision+1 || cancelled.CloseMode != "cancel" || cancelled.CloseReason != "no longer needed" {
		t.Fatalf("cancelled collection: %+v", cancelled)
	}
	if mode, actor, reason := f.priorClose(t, closed.ID); mode != "partial" || actor != requesterRef || reason != partial.Reason {
		t.Fatalf("partial close audit lost: %q %q %q", mode, actor, reason)
	}
	intent, err := f.store.GetReadyIntent(ctx, f.scope, closed.ID, ready.CollectionRevision)
	if err != nil || intent.State != ReadyIntentSuperseded || !intent.OccurredAt.Equal(ready.OccurredAt) {
		t.Fatalf("ready intent after cancel: %+v %v", intent, err)
	}
	if pending, _ := f.store.ListPendingReadyIntents(ctx, 10); len(pending) != 0 {
		t.Fatalf("cancelled collection still pending: %+v", pending)
	}
	if _, _, err := f.store.MarkReadyIntentAdmittedTx(ctx, f.scope, closed.ID, ready.CollectionRevision, "job/1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("admitting a superseded intent: %v", err)
	}
	if _, err := f.store.CompleteCollectionTx(ctx, f.scope, CompleteParams{CollectionID: closed.ID, SummaryRef: "outbox/1", Source: Source{Namespace: "employee.wake", Key: "job/1"}, ExpectedRevision: ready.CollectionRevision}); !errors.Is(err, ErrClosed) {
		t.Fatalf("summary of a cancelled collection: %v", err)
	}
	if waits, _ := f.store.TaskWaits(ctx, f.scope, closed.TaskID); len(waits) != 0 {
		t.Fatalf("cancelled collection is still a wait: %+v", waits)
	}

	// Replays of both closes are idempotent; nothing reopens.
	if again, r, err := f.store.CloseCollectionTx(ctx, f.scope, cancel); err != nil || r != nil || again.State != CollectionCancelled || again.Revision != cancelled.Revision {
		t.Fatalf("cancel replay: %+v %v", again, err)
	}
	if again, r, err := f.store.CloseCollectionTx(ctx, f.scope, partial); err != nil || r != nil || again.State != CollectionCancelled {
		t.Fatalf("partial close replay after cancel: %+v %+v %v", again, r, err)
	}
	changed := partial
	changed.Reason = "different reason"
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("partial close source replayed with other content: %v", err)
	}
	for _, mode := range []CloseMode{ClosePartial, CloseRevoke} {
		next := cancel
		next.Mode, next.Source.Key, next.ExpectedRevision = mode, "after-cancel/"+string(mode), cancelled.Revision
		if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, next); !errors.Is(err, ErrClosed) {
			t.Fatalf("%s after cancel: %v", mode, err)
		}
	}
}

// An origin authority withdrawn while the summary wake is already admitted
// ends the collection; the admitted wake can no longer complete it.
func TestRevokeAfterPartialCloseWhileSummarizing(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	closed, ready, _ := partiallyClosed(t, f)
	summarizing, _, err := f.store.MarkReadyIntentAdmittedTx(ctx, f.scope, closed.ID, ready.CollectionRevision, "job/1")
	if err != nil || summarizing.State != CollectionSummarizing {
		t.Fatalf("admit: %+v %v", summarizing, err)
	}
	// A second partial close is still refused.
	again := CloseParams{CollectionID: closed.ID, Mode: ClosePartial, Reason: "again", Source: Source{Namespace: "employee.tool", Key: "partial/again"}, Authority: requesterAuthority(f.origin), ExpectedRevision: summarizing.Revision}
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, again); !errors.Is(err, ErrClosed) {
		t.Fatalf("second partial close: %v", err)
	}
	revoke := CloseParams{CollectionID: closed.ID, Mode: CloseRevoke, Reason: "origin authority withdrawn", Source: Source{Namespace: "host.permission", Key: "revoke/" + closed.ID},
		Authority: Authority{ActorRef: "host:permission-reconciler", SceneID: f.origin, ReceiptRef: "reconcile/1", VerifiedAt: time.Now()}, ExpectedRevision: summarizing.Revision}
	revoked, _, err := f.store.CloseCollectionTx(ctx, f.scope, revoke)
	if err != nil || revoked.State != CollectionRevoked || revoked.Revision != summarizing.Revision+1 {
		t.Fatalf("revoke while summarizing: %+v %v", revoked, err)
	}
	if intent, _ := f.store.GetReadyIntent(ctx, f.scope, closed.ID, ready.CollectionRevision); intent.State != ReadyIntentAdmitted || intent.AdmittedRef != "job/1" {
		t.Fatalf("the admission fact is kept: %+v", intent)
	}
	if _, err := f.store.CompleteCollectionTx(ctx, f.scope, CompleteParams{CollectionID: closed.ID, SummaryRef: "outbox/1", Source: Source{Namespace: "employee.wake", Key: "job/1"}, ExpectedRevision: ready.CollectionRevision}); !errors.Is(err, ErrClosed) {
		t.Fatalf("summary after revoke: %v", err)
	}
	if mode, _, _ := f.priorClose(t, closed.ID); mode != "partial" {
		t.Fatalf("partial close audit lost: %q", mode)
	}
}

// After a partial close the deadline sweeper can still expire the collection
// once its explicit deadline passed (DB clock), never before.
func TestExpireAfterPartialCloseRequiresPassedDeadline(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect")
	params := f.createParams(task, spec(f.dmC, participantC, "Number?"), spec(f.dmD, participantD, "Number?"))
	params.Deadline = &Deadline{At: time.Now().Add(time.Hour), TimeZone: "Asia/Shanghai"}
	col, invitations, err := f.store.CreateCollectionTx(ctx, f.scope, params)
	if err != nil {
		t.Fatal(err)
	}
	invitations = f.deliverAll(t, invitations)
	answered := f.mustAccept(t, answer(col, invitations[0], "c-1", "7"))
	closed, ready, err := f.store.CloseCollectionTx(ctx, f.scope, CloseParams{CollectionID: col.ID, Mode: ClosePartial, Reason: "go ahead",
		Source: Source{Namespace: "employee.tool", Key: "partial"}, Authority: requesterAuthority(f.origin), ExpectedRevision: answered.Collection.Revision})
	if err != nil || ready == nil {
		t.Fatalf("partial close: %v", err)
	}
	sweep := CloseParams{CollectionID: col.ID, Mode: CloseExpire, Source: Source{Namespace: "host.sweep", Key: "expire/" + col.ID},
		Authority: Authority{ActorRef: "host:deadline-sweeper", SceneID: f.origin, ReceiptRef: "sweep/1", VerifiedAt: time.Now()}, ExpectedRevision: closed.Revision}
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, sweep); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expire before the deadline: %v", err)
	}
	// Time passes: the deadline is now behind the DB clock.
	if _, err := f.pool.Exec(ctx, `UPDATE employee_task_collection SET deadline_at=now()-interval '1 second' WHERE id=$1::uuid`, col.ID); err != nil {
		t.Fatal(err)
	}
	expired, _, err := f.store.CloseCollectionTx(ctx, f.scope, sweep)
	if err != nil || expired.State != CollectionExpired || expired.Revision != closed.Revision+1 {
		t.Fatalf("expire after partial close: %+v %v", expired, err)
	}
	if intent, _ := f.store.GetReadyIntent(ctx, f.scope, col.ID, ready.CollectionRevision); intent.State != ReadyIntentSuperseded {
		t.Fatalf("pending intent survives expiry: %+v", intent)
	}
	if inv, _ := f.store.GetInvitation(ctx, f.scope, invitations[0].ID); inv.State != InvitationAnswered {
		t.Fatalf("an answered slot keeps its fact: %+v", inv)
	}
}
