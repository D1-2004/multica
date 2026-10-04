package taskinput

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeetask"
)

// A Run of the Task finishing (either way) says nothing about the inputs the
// goal waits for: the collection stays open and keeps accepting answers.
func TestCollectionRunSuccessLeavesGoalWaiting(t *testing.T) {
	for _, outcome := range []employeetask.State{employeetask.StateSucceeded, employeetask.StateFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			f := database(t)
			ctx := context.Background()
			task := f.newTask(t, "Collect weekly numbers from C and D")
			run, err := f.tasks.StartRun(ctx, f.taskScope(), task.ID, employeetask.StartRunParams{Source: employeetask.Source{Namespace: "host", Key: "start"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
			if err != nil {
				t.Fatal(err)
			}
			col, invitations := f.create(t, task, spec(f.dmC, participantC, "What is your number?"), spec(f.dmD, participantD, "What is your number?"))
			invitations = f.deliverAll(t, invitations)
			if _, _, err = f.tasks.RecordResult(ctx, f.taskScope(), task.ID, employeetask.ResultParams{Source: employeetask.Source{Namespace: "runtime", Key: "terminal"}, RunID: run.ID, State: outcome, Result: "asked both"}); err != nil {
				t.Fatal(err)
			}
			waits, err := f.store.TaskWaits(ctx, f.scope, task.ID)
			if err != nil || len(waits) != 1 || waits[0].State != CollectionOpen || waits[0].Received != 0 || waits[0].Expected != 2 {
				t.Fatalf("goal must still wait on the open collection: %+v %v", waits, err)
			}
			if n := f.count(t, `SELECT count(*) FROM employee_task_ready_intent`); n != 0 {
				t.Fatalf("a run outcome must not produce a ready intent: %d", n)
			}
			// Inputs still count after the Run ended.
			f.mustAccept(t, answer(col, invitations[0], "c-1", "7"))
			result := f.mustAccept(t, answer(col, invitations[1], "d-1", "11"))
			if result.Ready == nil || result.Collection.State != CollectionReady || result.Collection.ReceivedCount != 2 {
				t.Fatalf("all slots filled after the run: %+v", result)
			}
		})
	}
}

func TestInputCountsInvitationOnlyOnce(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect numbers")
	col, invitations := f.create(t, task, spec(f.dmC, participantC, "Your number?"), spec(f.dmD, participantD, "Your number?"))
	invitations = f.deliverAll(t, invitations)
	c, d := invitations[0], invitations[1]

	first := f.mustAccept(t, answer(col, c, "c-1", "7"))
	if first.Collection.ReceivedCount != 1 || first.Input.Version != 1 || first.Input.Kind != "answer" || first.Ready != nil {
		t.Fatalf("first answer: %+v", first)
	}
	// Technical redelivery of the same source.
	replay, err := f.store.AcceptInputTx(ctx, f.scope, func() AcceptInputParams { p := answer(col, c, "c-1", "7"); p.ExpectedRevision = 1; return p }())
	if err != nil || !replay.Replayed || replay.Input.ID != first.Input.ID || replay.Collection.ReceivedCount != 1 {
		t.Fatalf("source replay must not count again: %+v %v", replay, err)
	}
	// Same source, different content.
	changed := answer(col, c, "c-1", "8")
	changed.ExpectedRevision = first.Collection.Revision
	if _, err := f.store.AcceptInputTx(ctx, f.scope, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same source, different content: %v", err)
	}
	// A second message without an explicit correction never counts twice.
	if _, err := f.accept(t, answer(col, c, "c-2", "7 again")); !errors.Is(err, ErrAlreadyAnswered) {
		t.Fatalf("second answer without correction: %v", err)
	}
	correction := answer(col, c, "c-3", "8 (corrected)")
	correction.Correction = true
	correction.Binding = BindInviteReference
	corrected := f.mustAccept(t, correction)
	if corrected.Collection.ReceivedCount != 1 || corrected.Input.Version != 2 || corrected.Input.Kind != "correction" || corrected.Invitation.EffectiveVersion != 2 {
		t.Fatalf("correction replaces the slot without counting: %+v", corrected)
	}
	history, err := f.store.InputHistory(ctx, f.scope, col.ID, c.ID)
	if err != nil || len(history) != 2 || history[0].Body != "7" || history[1].Body != "8 (corrected)" {
		t.Fatalf("history kept: %+v %v", history, err)
	}
	view, err := f.store.ReadOriginInputs(ctx, f.scope, col.ID, OriginViewer{TaskID: task.ID, SceneID: f.origin})
	if err != nil || view.Slots[0].Answer == nil || view.Slots[0].Answer.Body != "8 (corrected)" || !view.Slots[0].Answer.Corrected || view.Received != 1 {
		t.Fatalf("origin sees the effective version: %+v %v", view, err)
	}
	final := f.mustAccept(t, answer(col, d, "d-1", "11"))
	if final.Collection.ReceivedCount != 2 || final.Collection.State != CollectionReady || final.Ready == nil {
		t.Fatalf("every invitation counted once: %+v", final)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_input WHERE collection_id=$1::uuid`, col.ID); n != 3 {
		t.Fatalf("inputs: %d", n)
	}
}

// One person invited by two Tasks in the same DM: replies quoting the
// invitations bind correctly even out of order; an unquoted message is never
// guessed onto the latest Task.
func TestSamePersonTwoTasksRequiresInviteOrReplyTo(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task1, task2 := f.newTask(t, "Budget"), f.newTask(t, "Headcount")
	col1, inv1 := f.create(t, task1, spec(f.dmC, participantC, "Budget for Q4?"))
	col2, inv2 := f.create(t, task2, spec(f.dmC, participantC, "Headcount for Q4?"))
	i1, i2 := f.deliver(t, inv1[0]), f.deliver(t, inv2[0])

	candidates, err := f.store.BindingCandidates(ctx, f.scope, participantC)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates: %+v %v", candidates, err)
	}
	msg := InboundMessage{MessageID: "m-free", SceneID: f.dmC, SceneKind: "dm", SenderRef: participantC, SenderKind: SenderPerson, MessageKind: MessageText}
	if b := BindAnswer(msg, nil, candidates); b.Outcome != BindAmbiguous || len(b.Candidates) != 2 {
		t.Fatalf("unquoted message with two pending invitations must be ambiguous: %+v", b)
	}
	// The store refuses the guess even if a caller tries it.
	if _, err := f.accept(t, answer(col2, i2, "m-free", "40")); !errors.Is(err, ErrConflict) {
		t.Fatalf("dm_single_pending with two pending must conflict: %v", err)
	}

	// Out of order: the reply to Task 2's invitation arrives first.
	reply2 := msg
	reply2.MessageID, reply2.ReplyToID = "m-r2", i2.ProviderMessageID
	b := BindAnswer(reply2, nil, candidates)
	if b.Outcome != BindBound || b.Kind != BindReplyChain || b.InvitationID != i2.ID {
		t.Fatalf("reply to invitation 2: %+v", b)
	}
	p2 := answer(col2, i2, "m-r2", "40 people")
	p2.Binding = BindReplyChain
	f.mustAccept(t, p2)

	// Then a two-hop chain: quoting the person's own earlier message that
	// quoted Task 1's invitation.
	parents := map[string]string{"m-earlier": i1.ProviderMessageID}
	reply1 := msg
	reply1.MessageID, reply1.ReplyToID = "m-r1", "m-earlier"
	candidates, _ = f.store.BindingCandidates(ctx, f.scope, participantC)
	b = BindAnswer(reply1, parents, candidates)
	if b.Outcome != BindBound || b.InvitationID != i1.ID {
		t.Fatalf("two-hop reply to invitation 1: %+v", b)
	}
	p1 := answer(col1, i1, "m-r1", "1.2M")
	p1.Binding = BindReplyChain
	f.mustAccept(t, p1)

	for _, c := range []struct {
		col  Collection
		task employeetask.Task
		body string
	}{{col1, task1, "1.2M"}, {col2, task2, "40 people"}} {
		view, err := f.store.ReadOriginInputs(ctx, f.scope, c.col.ID, OriginViewer{TaskID: c.task.ID, SceneID: f.origin})
		if err != nil || len(view.Slots) != 1 || view.Slots[0].Answer == nil || view.Slots[0].Answer.Body != c.body {
			t.Fatalf("answers crossed tasks: %+v %v", view, err)
		}
	}

	// With both answered, an unquoted DM message binds to nothing; it lists the
	// sender's own answered invitations so the Loop can ask about a correction.
	candidates, _ = f.store.BindingCandidates(ctx, f.scope, participantC)
	if b := BindAnswer(msg, nil, candidates); b.Outcome != BindUnbound || b.Reason != ReasonNoPendingInvitation || len(b.Candidates) != 2 {
		t.Fatalf("no pending invitation: %+v", b)
	}
	// An explicit invitation reference resolves exactly one.
	ref := msg
	ref.InviteRefs = []string{i1.ID}
	if b := BindAnswer(ref, nil, candidates); b.Outcome != BindBound || b.Kind != BindInviteReference || b.InvitationID != i1.ID {
		t.Fatalf("explicit reference: %+v", b)
	}
}

func TestOriginReadsAuthorizedAnswersParticipantCannotReadOthers(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	const (
		originSecret = "SENTINEL-ORIGIN-private-requester-note"
		questionC    = "SENTINEL-QC how many leads did you close?"
		questionD    = "SENTINEL-QD what is the churn risk for ACME?"
		answerC      = "SENTINEL-AC 13 leads"
		answerD      = "SENTINEL-AD ACME will churn"
	)
	task := f.newTask(t, "Weekly report "+originSecret)
	col, invitations := f.create(t, task, spec(f.dmC, participantC, questionC), spec(f.dmD, participantD, questionD))
	invitations = f.deliverAll(t, invitations)
	c, d := invitations[0], invitations[1]
	f.mustAccept(t, answer(col, c, "c-1", answerC))
	f.mustAccept(t, answer(col, d, "d-1", answerD))

	origin, err := f.store.ReadOriginInputs(ctx, f.scope, col.ID, OriginViewer{TaskID: task.ID, SceneID: f.origin})
	if err != nil {
		t.Fatal(err)
	}
	originJSON, _ := json.Marshal(origin)
	for _, want := range []string{answerC, answerD, questionC, questionD} {
		if !strings.Contains(string(originJSON), want) {
			t.Fatalf("origin view misses %q: %s", want, originJSON)
		}
	}

	viewC := ParticipantViewer{ActorRef: participantC, SceneID: f.dmC}
	own, err := f.store.ReadParticipantInvitation(ctx, f.scope, c.ID, viewC)
	if err != nil || own.Answer == nil || own.Answer.Body != answerC {
		t.Fatalf("participant reads own answer: %+v %v", own, err)
	}
	listed, err := f.store.ListParticipantInvitations(ctx, f.scope, viewC)
	if err != nil || len(listed) != 1 || listed[0].InvitationID != c.ID {
		t.Fatalf("participant lists only its own: %+v %v", listed, err)
	}
	candidates, err := f.store.BindingCandidates(ctx, f.scope, participantC)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates: %+v %v", candidates, err)
	}
	binding := BindAnswer(InboundMessage{SceneID: f.dmC, SceneKind: "dm", SenderRef: participantC, SenderKind: SenderPerson, MessageKind: MessageText}, nil, candidates)
	participantSurface, _ := json.Marshal(struct {
		Own        ParticipantView
		Listed     []ParticipantView
		Candidates []BindingCandidate
		Binding    Binding
	}{own, listed, candidates, binding})
	for _, forbidden := range []string{answerD, questionD, originSecret, requesterRef, participantD, f.origin, col.AuthorityRef} {
		if strings.Contains(string(participantSurface), forbidden) {
			t.Fatalf("participant surface leaks %q: %s", forbidden, participantSurface)
		}
	}
	if !strings.Contains(string(participantSurface), answerC) || !strings.Contains(string(participantSurface), questionC) {
		t.Fatalf("participant surface misses its own content: %s", participantSurface)
	}

	// Cross-participant, cross-scene and origin-from-elsewhere reads fail closed.
	if _, err := f.store.ReadParticipantInvitation(ctx, f.scope, d.ID, viewC); !errors.Is(err, ErrNotFound) {
		t.Fatalf("C reads D's invitation: %v", err)
	}
	if _, err := f.store.ReadParticipantInvitation(ctx, f.scope, c.ID, ParticipantViewer{ActorRef: participantC, SceneID: f.dmD}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("C's invitation read from another scene: %v", err)
	}
	if _, err := f.store.ReadOriginInputs(ctx, f.scope, col.ID, OriginViewer{TaskID: task.ID, SceneID: f.dmC}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("origin view from a participant scene: %v", err)
	}
	other := f.newTask(t, "Other")
	if _, err := f.store.ReadOriginInputs(ctx, f.scope, col.ID, OriginViewer{TaskID: other.ID, SceneID: f.origin}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("origin view for another task: %v", err)
	}
	otherTenant := f.scope
	otherTenant.TenantOrgID = "org-b"
	if _, err := f.store.ReadOriginInputs(ctx, otherTenant, col.ID, OriginViewer{TaskID: task.ID, SceneID: f.origin}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("origin view from another tenant: %v", err)
	}

	// Revoking D's authorization withdraws D's answer from the origin view.
	current, _ := f.store.GetCollection(ctx, f.scope, col.ID)
	_, revoked, err := f.store.RevokeInvitationTx(ctx, f.scope, RevokeInvitationParams{CollectionID: col.ID, InvitationID: d.ID, Reason: "D withdrew consent",
		Source: Source{Namespace: "employee.tool", Key: "revoke/d"}, Authority: requesterAuthority(f.origin), ExpectedRevision: current.Revision})
	if err != nil || revoked.State != InvitationRevoked {
		t.Fatalf("revoke D: %+v %v", revoked, err)
	}
	origin, _ = f.store.ReadOriginInputs(ctx, f.scope, col.ID, OriginViewer{TaskID: task.ID, SceneID: f.origin})
	originJSON, _ = json.Marshal(origin)
	if strings.Contains(string(originJSON), answerD) || origin.Received != 1 || origin.State != CollectionOpen {
		t.Fatalf("revoked answer still authorized: %s", originJSON)
	}
}

// Several participants answer the last slots at the same moment from separate
// connections; exactly one ready intent exists for exactly one revision.
func TestConcurrentFinalRepliesCreateOneReadyIntent(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect six numbers")
	specs := make([]InvitationSpec, 6)
	for i := range specs {
		specs[i] = spec(f.resolveScene(t, "dm", "cid-dm-p"), "dingtalk:org-a:p"+string(rune('0'+i)), "Number?")
	}
	col, invitations := f.create(t, task, specs...)
	invitations = f.deliverAll(t, invitations)

	var wg sync.WaitGroup
	errs := make(chan error, len(invitations)*4)
	for i, inv := range invitations {
		for range 4 {
			// Each answer is also redelivered concurrently by the provider.
			wg.Go(func() {
				if _, err := f.accept(t, answer(col, inv, "p-"+inv.ID, "answer "+string(rune('a'+i)))); err != nil {
					errs <- err
				}
			})
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_ready_intent WHERE collection_id=$1::uuid`, col.ID); n != 1 {
		t.Fatalf("ready intents: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_input WHERE collection_id=$1::uuid`, col.ID); n != len(invitations) {
		t.Fatalf("inputs: %d", n)
	}
	got, err := f.store.GetCollection(ctx, f.scope, col.ID)
	if err != nil || got.State != CollectionReady || got.ReceivedCount != 6 || got.Revision != 7 {
		t.Fatalf("collection: %+v %v", got, err)
	}
	intent, err := f.store.GetReadyIntent(ctx, f.scope, col.ID, got.Revision)
	if err != nil || intent.State != ReadyIntentPending || intent.EventID != col.ID+"/7" || intent.EventType != ReadyEventType || intent.EventSource != ReadyEventSource {
		t.Fatalf("intent: %+v %v", intent, err)
	}
}

// A crash before commit leaves nothing; a crash after commit leaves one
// pending intent whose revision and occurred_at never change on retry,
// replay or reconciliation.
func TestReadyIntentCrashRecoveryKeepsRevisionAndOccurredAt(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect two numbers")
	col, invitations := f.create(t, task, spec(f.dmC, participantC, "Number?"), spec(f.dmD, participantD, "Number?"))
	invitations = f.deliverAll(t, invitations)
	f.mustAccept(t, answer(col, invitations[0], "c-1", "7"))
	final := answer(col, invitations[1], "d-1", "11")
	current, _ := f.store.GetCollection(ctx, f.scope, col.ID)
	final.ExpectedRevision = current.Revision

	// Crash inside the Host's outer transaction, before commit.
	outer, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := NewStore(outer).AcceptInputTx(ctx, f.scope, final); err != nil || res.Ready == nil {
		t.Fatalf("accept in outer tx: %+v %v", res, err)
	}
	_ = outer.Rollback(ctx)
	if n := f.count(t, `SELECT count(*) FROM employee_task_ready_intent`); n != 0 {
		t.Fatalf("rolled back intent survived: %d", n)
	}

	committed, err := f.store.AcceptInputTx(ctx, f.scope, final)
	if err != nil || committed.Ready == nil {
		t.Fatalf("accept: %+v %v", committed, err)
	}
	first := *committed.Ready
	time.Sleep(20 * time.Millisecond)

	// Process restart: the reconciler finds the same intent; a provider replay
	// of the final message returns it unchanged.
	pending, err := f.store.ListPendingReadyIntents(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].CollectionRevision != first.CollectionRevision || !pending[0].OccurredAt.Equal(first.OccurredAt) {
		t.Fatalf("pending after restart: %+v %v", pending, err)
	}
	replay, err := f.store.AcceptInputTx(ctx, f.scope, final)
	if err != nil || !replay.Replayed || replay.Ready == nil || !replay.Ready.OccurredAt.Equal(first.OccurredAt) || replay.Ready.CollectionRevision != first.CollectionRevision {
		t.Fatalf("replay changed the intent: %+v %v", replay, err)
	}

	// Crash during admission: the TaskWake and the mark roll back together.
	outer, err = f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewStore(outer).MarkReadyIntentAdmittedTx(ctx, f.scope, col.ID, first.CollectionRevision, "job/1"); err != nil {
		t.Fatal(err)
	}
	_ = outer.Rollback(ctx)
	if pending, _ := f.store.ListPendingReadyIntents(ctx, 10); len(pending) != 1 {
		t.Fatalf("rolled back admission lost the intent: %+v", pending)
	}
	summarizing, admitted, err := f.store.MarkReadyIntentAdmittedTx(ctx, f.scope, col.ID, first.CollectionRevision, "job/2")
	if err != nil || summarizing.State != CollectionSummarizing || admitted.State != ReadyIntentAdmitted || !admitted.OccurredAt.Equal(first.OccurredAt) {
		t.Fatalf("admit: %+v %+v %v", summarizing, admitted, err)
	}
	if _, again, err := f.store.MarkReadyIntentAdmittedTx(ctx, f.scope, col.ID, first.CollectionRevision, "job/2"); err != nil || again.AdmittedRef != "job/2" {
		t.Fatalf("admission replay: %v", err)
	}
	if _, _, err := f.store.MarkReadyIntentAdmittedTx(ctx, f.scope, col.ID, first.CollectionRevision, "job/3"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second admission: %v", err)
	}
	if pending, _ := f.store.ListPendingReadyIntents(ctx, 10); len(pending) != 0 {
		t.Fatalf("admitted intent still pending: %+v", pending)
	}
	complete := CompleteParams{CollectionID: col.ID, SummaryRef: "outbox/summary-1", Source: Source{Namespace: "employee.wake", Key: "job/2"}, ExpectedRevision: first.CollectionRevision}
	done, err := f.store.CompleteCollectionTx(ctx, f.scope, complete)
	if err != nil || done.State != CollectionCompleted {
		t.Fatalf("complete: %+v %v", done, err)
	}
	if again, err := f.store.CompleteCollectionTx(ctx, f.scope, complete); err != nil || again.State != CollectionCompleted {
		t.Fatalf("complete replay: %v", err)
	}
	// A late correction after the summary never reopens or re-summarizes.
	late := answer(col, invitations[0], "c-late", "9")
	late.Correction, late.Binding = true, BindInviteReference
	if _, err := f.accept(t, late); !errors.Is(err, ErrClosed) {
		t.Fatalf("late correction after completion: %v", err)
	}
}

// A correction that lands while the origin is summarizing moves the revision:
// the old summary can no longer complete and a new intent replaces it.
func TestCorrectionBeforeSummaryInvalidatesOldRevision(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect")
	col, invitations := f.create(t, task, spec(f.dmC, participantC, "Number?"))
	invitations = f.deliverAll(t, invitations)
	ready := f.mustAccept(t, answer(col, invitations[0], "c-1", "7")).Ready
	if _, _, err := f.store.MarkReadyIntentAdmittedTx(ctx, f.scope, col.ID, ready.CollectionRevision, "job/1"); err != nil {
		t.Fatal(err)
	}
	fix := answer(col, invitations[0], "c-2", "8")
	fix.Correction, fix.Binding = true, BindInviteReference
	corrected := f.mustAccept(t, fix)
	if corrected.Ready == nil || corrected.Ready.CollectionRevision != ready.CollectionRevision+1 || corrected.Collection.State != CollectionReady {
		t.Fatalf("correction during summary: %+v", corrected)
	}
	if _, err := f.store.CompleteCollectionTx(ctx, f.scope, CompleteParams{CollectionID: col.ID, SummaryRef: "outbox/old", Source: Source{Namespace: "employee.wake", Key: "job/1"}, ExpectedRevision: ready.CollectionRevision}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale summary must not complete: %v", err)
	}
	if old, _ := f.store.GetReadyIntent(ctx, f.scope, col.ID, ready.CollectionRevision); old.State != ReadyIntentAdmitted {
		t.Fatalf("admitted intent keeps its fact: %+v", old)
	}
	pending, _ := f.store.ListPendingReadyIntents(ctx, 10)
	if len(pending) != 1 || pending[0].CollectionRevision != corrected.Collection.Revision {
		t.Fatalf("new revision pending: %+v", pending)
	}
}

func TestAuthorizationExpiryAndRevocation(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect")
	expires := time.Now().Add(time.Hour)
	specC := spec(f.dmC, participantC, "Number?")
	specC.ExpiresAt = &expires
	params := f.createParams(task, specC, spec(f.dmD, participantD, "Number?"), spec(f.dmDE, participantDE, "Number?"))
	params.Deadline = &Deadline{At: time.Now().Add(2 * time.Hour), TimeZone: "Asia/Shanghai"}
	col, invitations, err := f.store.CreateCollectionTx(ctx, f.scope, params)
	if err != nil {
		t.Fatal(err)
	}
	invitations = f.deliverAll(t, invitations)
	c, d, de := invitations[0], invitations[1], invitations[2]

	late := answer(col, c, "c-late", "7")
	late.OccurredAt = expires.Add(time.Minute)
	if _, err := f.accept(t, late); !errors.Is(err, ErrClosed) {
		t.Fatalf("answer after invitation expiry: %v", err)
	}
	afterDeadline := answer(col, d, "d-late", "11")
	afterDeadline.OccurredAt = params.Deadline.At.Add(time.Minute)
	if _, err := f.accept(t, afterDeadline); !errors.Is(err, ErrClosed) {
		t.Fatalf("answer after the collection deadline: %v", err)
	}
	stale := answer(col, d, "d-stale", "11")
	stale.OccurredAt = time.Now().Add(-time.Hour)
	if _, err := f.accept(t, stale); !errors.Is(err, ErrInvalid) {
		t.Fatalf("message older than the invitation: %v", err)
	}

	revoke := RevokeInvitationParams{CollectionID: col.ID, InvitationID: d.ID, Reason: "requester no longer needs D", Source: Source{Namespace: "employee.tool", Key: "revoke/d"}, ExpectedRevision: col.Revision}
	revoke.Authority = Authority{ActorRef: participantC, SceneID: f.dmC, ReceiptRef: "r", VerifiedAt: time.Now()}
	if _, _, err := f.store.RevokeInvitationTx(ctx, f.scope, revoke); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a participant cannot revoke another: %v", err)
	}
	revoke.Authority = requesterAuthority(f.origin)
	if _, _, err := f.store.RevokeInvitationTx(ctx, f.scope, revoke); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.RevokeInvitationTx(ctx, f.scope, revoke); err != nil {
		t.Fatalf("revoke replay: %v", err)
	}
	if _, err := f.accept(t, answer(col, d, "d-1", "11")); !errors.Is(err, ErrClosed) {
		t.Fatalf("answer after revocation: %v", err)
	}
	// The Host may revoke when the participant's identity or permission is gone.
	current, _ := f.store.GetCollection(ctx, f.scope, col.ID)
	hostRevoke := RevokeInvitationParams{CollectionID: col.ID, InvitationID: de.ID, Reason: "identity bridge revoked", Source: Source{Namespace: "host.permission", Key: "de"},
		Authority: Authority{ActorRef: "host:identity-reconciler", SceneID: f.origin, ReceiptRef: "reconcile/1", VerifiedAt: time.Now()}, ExpectedRevision: current.Revision}
	if _, _, err := f.store.RevokeInvitationTx(ctx, f.scope, hostRevoke); err != nil {
		t.Fatalf("host revoke: %v", err)
	}
	// Expire is refused before the deadline (DB clock), and an all_required
	// collection with revoked slots waits for an explicit decision.
	current, _ = f.store.GetCollection(ctx, f.scope, col.ID)
	if current.State != CollectionOpen {
		t.Fatalf("collection with revoked slots stays open: %+v", current)
	}
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, CloseParams{CollectionID: col.ID, Mode: CloseExpire, Source: Source{Namespace: "host.sweep", Key: "expire"},
		Authority: Authority{ActorRef: "host:deadline-sweeper", SceneID: f.origin, ReceiptRef: "sweep/1", VerifiedAt: time.Now()}, ExpectedRevision: current.Revision}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expire before the deadline: %v", err)
	}
}

func TestWrongActorSceneOrgAndKindRejected(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect")
	col, invitations := f.create(t, task, spec(f.dmC, participantC, "Number?"), spec(f.group, participantD, "Number?"))
	invitations = f.deliverAll(t, invitations)
	c, g := invitations[0], invitations[1]

	wrongActor := answer(col, c, "x-1", "7")
	wrongActor.Authority.ActorRef = participantD
	wrongScene := answer(col, c, "x-2", "7")
	wrongScene.Authority.SceneID = f.dmD
	wrongCollection := answer(col, c, "x-3", "7")
	otherTask := f.newTask(t, "Other")
	otherCol, _ := f.create(t, otherTask, spec(f.dmC, participantD, "?"))
	wrongCollection.CollectionID = otherCol.ID
	for name, p := range map[string]AcceptInputParams{"actor": wrongActor, "scene": wrongScene, "collection": wrongCollection} {
		if _, err := f.accept(t, p); !errors.Is(err, ErrNotFound) {
			t.Fatalf("wrong %s: %v", name, err)
		}
	}
	for name, scope := range map[string]Scope{
		"tenant":    {WorkspaceID: f.scope.WorkspaceID, AgentID: f.scope.AgentID, TenantOrgID: "org-b"},
		"agent":     {WorkspaceID: f.scope.WorkspaceID, AgentID: uuid.NewString(), TenantOrgID: testOrg},
		"workspace": {WorkspaceID: uuid.NewString(), AgentID: f.scope.AgentID, TenantOrgID: testOrg},
	} {
		p := answer(col, c, "x-scope-"+name, "7")
		if _, err := f.store.AcceptInputTx(ctx, scope, p); !errors.Is(err, ErrNotFound) {
			t.Fatalf("wrong %s scope: %v", name, err)
		}
	}
	groupUnquoted := answer(col, g, "x-4", "7")
	groupUnquoted.Binding = BindDMSinglePending
	bot := answer(col, c, "x-5", "7")
	bot.SenderKind = SenderBot
	self := answer(col, c, "x-6", "7")
	self.SenderKind = SenderSelf
	card := answer(col, c, "x-7", "intro card")
	card.MessageKind = MessageCard
	for name, p := range map[string]AcceptInputParams{"group unquoted": groupUnquoted, "bot": bot, "self": self, "card": card} {
		if _, err := f.accept(t, p); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s must never count: %v", name, err)
		}
	}
	// Inviting into a scene outside the directory, or without the requester's authority, fails.
	params := f.createParams(f.newTask(t, "x"), spec(uuid.NewString(), participantC, "?"))
	if _, _, err := f.store.CreateCollectionTx(ctx, f.scope, params); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown target scene: %v", err)
	}
	params = f.createParams(f.newTask(t, "y"), spec(f.dmC, participantC, "?"))
	params.Authority.ActorRef = participantC
	if _, _, err := f.store.CreateCollectionTx(ctx, f.scope, params); !errors.Is(err, ErrForbidden) {
		t.Fatalf("collection authorized by a non-requester: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_input`); n != 0 {
		t.Fatalf("rejected inputs were written: %d", n)
	}
}

func TestTaskCancelledAndCollectionClosedRejectLateInput(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect")
	col, invitations := f.create(t, task, spec(f.dmC, participantC, "Number?"), spec(f.dmD, participantD, "Number?"))
	invitations = f.deliverAll(t, invitations)
	if _, _, err := f.tasks.Stop(ctx, f.taskScope(), task.ID, employeetask.StopParams{Source: employeetask.Source{Namespace: "human", Key: "stop"}, ActorRef: requesterRef, Body: "stop", ExpectedVersion: task.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.accept(t, answer(col, invitations[0], "c-1", "7")); !errors.Is(err, ErrClosed) {
		t.Fatalf("answer to a stopped task: %v", err)
	}
	if _, _, err := f.store.CreateCollectionTx(ctx, f.scope, func() CreateCollectionParams {
		p := f.createParams(task, spec(f.dmC, participantC, "?"))
		p.Source.Key = "second"
		return p
	}()); !errors.Is(err, ErrClosed) {
		t.Fatalf("collection on a stopped task: %v", err)
	}
	// The Stop path cancels the collection; nothing reopens it.
	cancel := CloseParams{CollectionID: col.ID, Mode: CloseCancel, Reason: "task stopped", Source: Source{Namespace: "employee.stop", Key: task.ID}, Authority: requesterAuthority(f.origin), ExpectedRevision: col.Revision}
	closed, ready, err := f.store.CloseCollectionTx(ctx, f.scope, cancel)
	if err != nil || closed.State != CollectionCancelled || ready != nil {
		t.Fatalf("cancel: %+v %v", closed, err)
	}
	if again, _, err := f.store.CloseCollectionTx(ctx, f.scope, cancel); err != nil || again.State != CollectionCancelled {
		t.Fatalf("cancel replay: %v", err)
	}
	other := cancel
	other.Source.Key, other.Mode = "other", CloseRevoke
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, other); !errors.Is(err, ErrClosed) {
		t.Fatalf("closing a closed collection: %v", err)
	}
	invs, _ := f.store.ListInvitations(ctx, f.scope, col.ID)
	for _, inv := range invs {
		if inv.State != InvitationRevoked {
			t.Fatalf("cancelled collection leaves open invitations: %+v", inv)
		}
	}
	if _, _, err := f.store.MarkReadyIntentAdmittedTx(ctx, f.scope, col.ID, col.Revision, "job"); err == nil {
		t.Fatal("no wake for a cancelled collection")
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_input`); n != 0 {
		t.Fatalf("late inputs written: %d", n)
	}
}

func TestCollectionCreateReplayAndSourceConflict(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect")
	params := f.createParams(task, spec(f.dmC, participantC, "Number?"), spec(f.dmD, participantD, "Number?"))
	var wg sync.WaitGroup
	ids := make(chan string, 6)
	for range 6 {
		wg.Go(func() {
			col, invs, err := f.store.CreateCollectionTx(ctx, f.scope, params)
			if err != nil || len(invs) != 2 {
				t.Errorf("concurrent create: %v", err)
				return
			}
			ids <- col.ID
		})
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first != "" && id != first {
			t.Fatal("one source created two collections")
		}
		first = id
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_invitation`); n != 2 {
		t.Fatalf("invitations: %d", n)
	}
	changed := params
	changed.Invitations = []InvitationSpec{spec(f.dmC, participantC, "Different question?")}
	if _, _, err := f.store.CreateCollectionTx(ctx, f.scope, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same source, different collection: %v", err)
	}
	dup := params
	dup.Source.Key = "dup"
	dup.Invitations = []InvitationSpec{spec(f.dmC, participantC, "a?"), spec(f.dmD, participantC, "b?")}
	if _, _, err := f.store.CreateCollectionTx(ctx, f.scope, dup); !errors.Is(err, ErrInvalid) {
		t.Fatalf("one person twice in a collection: %v", err)
	}
	stale := params
	stale.Source.Key, stale.GoalRevision = "stale", task.GoalRevision+1
	if _, _, err := f.store.CreateCollectionTx(ctx, f.scope, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale goal revision: %v", err)
	}
	noTZ := params
	noTZ.Source.Key, noTZ.Deadline = "no-tz", &Deadline{At: time.Now().Add(time.Hour)}
	if _, _, err := f.store.CreateCollectionTx(ctx, f.scope, noTZ); !errors.Is(err, ErrInvalid) {
		t.Fatalf("deadline without a time zone: %v", err)
	}
}

// DeleteWorkspace locks the workspace FOR UPDATE and sweeps the tables; a
// concurrent answer waits and then finds nothing, leaving no orphan rows.
func TestWorkspaceDeletionRaceLeavesNoOrphans(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect")
	col, invitations := f.create(t, task, spec(f.dmC, participantC, "Number?"))
	invitations = f.deliverAll(t, invitations)

	teardown, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer teardown.Rollback(ctx)
	if _, err := teardown.Exec(ctx, `SELECT id FROM workspace WHERE id=$1::uuid FOR UPDATE`, f.scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		p := answer(col, invitations[0], "c-1", "7")
		p.ExpectedRevision = col.Revision
		_, err := f.store.AcceptInputTx(ctx, f.scope, p)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for f.count(t, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR KEY SHARE%'`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("answer did not wait for the workspace lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, table := range []string{"employee_task_ready_intent", "employee_task_input", "employee_task_invitation", "employee_task_collection", "employee_task_run", "employee_task_entry", "employee_task", "agent_scene"} {
		if _, err := teardown.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1::uuid`, f.scope.WorkspaceID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := teardown.Exec(ctx, `DELETE FROM workspace WHERE id=$1::uuid`, f.scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := teardown.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("answer after workspace deletion: %v", err)
	}
	for _, table := range []string{"employee_task_ready_intent", "employee_task_input", "employee_task_invitation", "employee_task_collection"} {
		if n := f.count(t, `SELECT count(*) FROM `+table); n != 0 {
			t.Fatalf("orphan rows in %s: %d", table, n)
		}
	}
}

// Group answers count only as replies to the invitation; free group chatter,
// this Agent's own echo, other bots and a digital employee's intro card never
// count, while an invited digital employee's own reply does.
func TestGroupNonReplySelfAndBotMessagesIgnored(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect")
	col, invitations := f.create(t, task, spec(f.group, participantC, "Number?"), spec(f.group, participantDE, "Number?"))
	invitations = f.deliverAll(t, invitations)
	c, de := invitations[0], invitations[1]
	base := InboundMessage{MessageID: "g-1", SceneID: f.group, SceneKind: "group", SenderRef: participantC, SenderKind: SenderPerson, MessageKind: MessageText}
	candidatesC, _ := f.store.BindingCandidates(ctx, f.scope, participantC)
	candidatesDE, _ := f.store.BindingCandidates(ctx, f.scope, participantDE)

	cases := []struct {
		name       string
		msg        InboundMessage
		candidates []BindingCandidate
		outcome    BindOutcome
		reason     string
	}{
		{"group chatter without reply", base, candidatesC, BindIgnored, ReasonGroupWithoutReply},
		{"own invitation echoed back", func() InboundMessage {
			m := base
			m.SenderKind, m.SenderRef, m.ReplyToID = SenderSelf, "", c.ProviderMessageID
			return m
		}(), candidatesC, BindIgnored, ReasonSelfMessage},
		{"other bot answers", func() InboundMessage { m := base; m.SenderKind, m.ReplyToID = SenderBot, c.ProviderMessageID; return m }(), candidatesC, BindIgnored, ReasonBotMessage},
		{"digital employee intro card", func() InboundMessage {
			m := base
			m.SenderRef, m.SenderKind, m.MessageKind, m.ReplyToID = participantDE, SenderDigitalEmployee, MessageCard, de.ProviderMessageID
			return m
		}(), candidatesDE, BindIgnored, ReasonNotAnswerContent},
		{"digital employee card without reply", func() InboundMessage {
			m := base
			m.SenderRef, m.SenderKind, m.MessageKind = participantDE, SenderDigitalEmployee, MessageCard
			return m
		}(), candidatesDE, BindIgnored, ReasonNotAnswerContent},
		{"reply to someone else's invitation", func() InboundMessage { m := base; m.ReplyToID = de.ProviderMessageID; return m }(), candidatesC, BindUnbound, ReasonReplyNotInvitation},
		{"reply to an unrelated message", func() InboundMessage { m := base; m.ReplyToID = "other-msg"; return m }(), candidatesC, BindUnbound, ReasonReplyNotInvitation},
	}
	for _, tc := range cases {
		if b := BindAnswer(tc.msg, nil, tc.candidates); b.Outcome != tc.outcome || b.Reason != tc.reason {
			t.Fatalf("%s: %+v", tc.name, b)
		}
	}
	// The invited digital employee's own text reply counts like a person's.
	deReply := base
	deReply.SenderRef, deReply.SenderKind, deReply.ReplyToID = participantDE, SenderDigitalEmployee, de.ProviderMessageID
	b := BindAnswer(deReply, nil, candidatesDE)
	if b.Outcome != BindBound || b.InvitationID != de.ID {
		t.Fatalf("invited digital employee reply: %+v", b)
	}
	p := answer(col, de, "g-de", "13")
	p.SenderKind = SenderDigitalEmployee
	if res := f.mustAccept(t, p); res.Collection.ReceivedCount != 1 {
		t.Fatalf("digital employee answer: %+v", res)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_input WHERE invitation_id=$1::uuid`, c.ID); n != 0 {
		t.Fatalf("ignored messages were counted: %d", n)
	}
}

func TestPartialCloseRequiresRequesterAndReasonAndIsAudited(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect")
	col, invitations := f.create(t, task, spec(f.dmC, participantC, "Number?"), spec(f.dmD, participantD, "Number?"), spec(f.group, participantDE, "Number?"))
	invitations = f.deliverAll(t, invitations)
	answered := f.mustAccept(t, answer(col, invitations[0], "c-1", "7"))

	partial := CloseParams{CollectionID: col.ID, Mode: ClosePartial, Reason: "D and the DE are on leave; summarize what we have",
		Source: Source{Namespace: "employee.tool", Key: "partial/1"}, Authority: Authority{ActorRef: participantC, SceneID: f.dmC, ReceiptRef: "r", VerifiedAt: time.Now()}, ExpectedRevision: answered.Collection.Revision}
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, partial); !errors.Is(err, ErrForbidden) {
		t.Fatalf("partial close by a participant: %v", err)
	}
	partial.Authority = Authority{ActorRef: requesterRef, SceneID: f.dmC, ReceiptRef: "r", VerifiedAt: time.Now()}
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, partial); !errors.Is(err, ErrForbidden) {
		t.Fatalf("partial close by the requester outside the origin scene: %v", err)
	}
	noReason := partial
	noReason.Reason, noReason.Authority = "  ", requesterAuthority(f.origin)
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, noReason); !errors.Is(err, ErrInvalid) {
		t.Fatalf("partial close without a reason: %v", err)
	}
	partial.Authority = requesterAuthority(f.origin)
	closed, ready, err := f.store.CloseCollectionTx(ctx, f.scope, partial)
	if err != nil || closed.State != CollectionReady || ready == nil || ready.CollectionRevision != closed.Revision {
		t.Fatalf("partial close: %+v %+v %v", closed, ready, err)
	}
	if closed.CloseMode != "partial" || closed.CloseActorRef != requesterRef || closed.CloseReason != partial.Reason || closed.ClosedAt == nil {
		t.Fatalf("partial close is audited: %+v", closed)
	}
	replayed, replayReady, err := f.store.CloseCollectionTx(ctx, f.scope, partial)
	if err != nil || replayed.ID != closed.ID || replayReady == nil || replayReady.CollectionRevision != ready.CollectionRevision {
		t.Fatalf("partial close replay: %+v %v", replayReady, err)
	}
	if _, err := f.accept(t, answer(col, invitations[1], "d-late", "11")); !errors.Is(err, ErrClosed) {
		t.Fatalf("late answer after partial close: %v", err)
	}
	view, err := f.store.ReadOriginInputs(ctx, f.scope, col.ID, OriginViewer{TaskID: task.ID, SceneID: f.origin})
	if err != nil || view.CloseMode != "partial" || view.Received != 1 || view.Expected != 3 {
		t.Fatalf("origin view: %+v %v", view, err)
	}
	for _, slot := range view.Slots[1:] {
		if slot.State != InvitationRevoked || slot.Answer != nil {
			t.Fatalf("missing slots are revoked with no answer: %+v", slot)
		}
	}
	var endReason string
	if err := f.pool.QueryRow(ctx, `SELECT end_reason FROM employee_task_invitation WHERE id=$1::uuid`, invitations[1].ID).Scan(&endReason); err != nil || endReason != "partial_close" {
		t.Fatalf("revocation audit: %q %v", endReason, err)
	}
}

// PENDING or unknown deliveries are never resent; only a definite failure
// mints a new action id, and a send racing a close is kept as a fact.
func TestInviteDeliveryNeverResendsPending(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect")
	col, invitations := f.create(t, task, spec(f.dmC, participantC, "Number?"), spec(f.dmD, participantD, "Number?"))
	c, d := invitations[0], invitations[1]
	if c.DeliveryActionID != DeliveryActionID(c.ID, 1) || NextDeliveryStep(c) != StepAwaitOutbox {
		t.Fatalf("first attempt: %+v", c)
	}
	retry := RetryDeliveryParams{InvitationID: c.ID, ExpectedAttempt: 1}
	if _, err := f.store.RetryInviteDeliveryTx(ctx, f.scope, col.ID, retry); !errors.Is(err, ErrDeliveryPending) {
		t.Fatalf("retry while pending: %v", err)
	}
	unknown, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, RecordDeliveryParams{InvitationID: c.ID, ActionID: c.DeliveryActionID, Outcome: DeliveryUnknown, Error: "timeout"})
	if err != nil || NextDeliveryStep(unknown) != StepQueryAction {
		t.Fatalf("unknown: %+v %v", unknown, err)
	}
	if _, err := f.store.RetryInviteDeliveryTx(ctx, f.scope, col.ID, retry); !errors.Is(err, ErrDeliveryPending) {
		t.Fatalf("retry while unknown: %v", err)
	}
	// The query found no message: a definite failure.
	failed, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, RecordDeliveryParams{InvitationID: c.ID, ActionID: c.DeliveryActionID, Outcome: DeliveryFailed, Error: "not delivered"})
	if err != nil || NextDeliveryStep(failed) != StepMayRetry {
		t.Fatalf("failed: %+v %v", failed, err)
	}
	second, err := f.store.RetryInviteDeliveryTx(ctx, f.scope, col.ID, retry)
	if err != nil || second.DeliveryAttempt != 2 || second.DeliveryActionID != DeliveryActionID(c.ID, 2) {
		t.Fatalf("retry: %+v %v", second, err)
	}
	if again, err := f.store.RetryInviteDeliveryTx(ctx, f.scope, col.ID, retry); err != nil || again.DeliveryActionID != second.DeliveryActionID {
		t.Fatalf("retry replay: %+v %v", again, err)
	}
	hash := renderedHash(c.Question)
	if _, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, RecordDeliveryParams{InvitationID: c.ID, ActionID: c.DeliveryActionID, Outcome: DeliverySent, ProviderMessageID: "m1", RenderedHash: hash}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale action: %v", err)
	}
	sent := RecordDeliveryParams{InvitationID: c.ID, ActionID: second.DeliveryActionID, Outcome: DeliverySent, ProviderMessageID: "m2", RenderedHash: hash}
	delivered, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, sent)
	if err != nil || delivered.State != InvitationDelivered || delivered.ProviderMessageID != "m2" || NextDeliveryStep(delivered) != StepNone {
		t.Fatalf("sent: %+v %v", delivered, err)
	}
	if _, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, sent); err != nil {
		t.Fatalf("sent replay: %v", err)
	}
	other := sent
	other.ProviderMessageID = "m3"
	if _, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("a delivered action changing message id: %v", err)
	}
	// Held by the egress scan: nothing is sent, no retry without the requester.
	held, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, RecordDeliveryParams{InvitationID: d.ID, ActionID: d.DeliveryActionID, Outcome: DeliveryHeld, RenderedHash: hash, Error: HoldPrivateFragment})
	if err != nil || NextDeliveryStep(held) != StepHeld {
		t.Fatalf("held: %+v %v", held, err)
	}
	if _, err := f.store.RetryInviteDeliveryTx(ctx, f.scope, col.ID, RetryDeliveryParams{InvitationID: d.ID, ExpectedAttempt: 1}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("retry a held delivery: %v", err)
	}

	// A send that raced a cancel is recorded without reopening anything.
	task2 := f.newTask(t, "Other")
	col2, inv2 := f.create(t, task2, spec(f.dmC, participantD, "?"))
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, CloseParams{CollectionID: col2.ID, Mode: CloseCancel, Source: Source{Namespace: "x", Key: "y"}, Authority: requesterAuthority(f.origin), ExpectedRevision: col2.Revision}); err != nil {
		t.Fatal(err)
	}
	late, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, RecordDeliveryParams{InvitationID: inv2[0].ID, ActionID: inv2[0].DeliveryActionID, Outcome: DeliverySent, ProviderMessageID: "m-late", RenderedHash: hash})
	if err != nil || late.State != InvitationRevoked || late.ProviderMessageID != "m-late" {
		t.Fatalf("late send fact: %+v %v", late, err)
	}
}

// A person who never DM'd the agent has no DM scene. The invitation waits in
// pending_scene, accepts nothing, and gets its scene exactly once from the
// send's provider receipt; answers then bind in that DM like any other.
func TestPendingSceneInvitationResolvesFromDeliveryReceipt(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	const participantE = "dingtalk:org-a:staff-e"
	task := f.newTask(t, "Collect")

	// A kind is never guessed, and a group target needs its scene up front.
	for name, s := range map[string]InvitationSpec{
		"no kind":          {ParticipantRef: participantE, Question: "?"},
		"group, no scene":  {ParticipantRef: participantE, Question: "?", TargetKind: "group"},
		"kind disagreeing": {TargetSceneID: f.group, ParticipantRef: participantE, Question: "?", TargetKind: "dm"},
	} {
		p := f.createParams(task, s)
		p.Source.Key = "invalid/" + name
		if _, _, err := f.store.CreateCollectionTx(ctx, f.scope, p); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	col, invitations := f.create(t, task, InvitationSpec{ParticipantRef: participantE, Question: "Your number?", TargetKind: "dm"}, spec(f.dmC, participantC, "Your number?"))
	e := invitations[0]
	if e.State != InvitationPendingScene || e.TargetSceneID != "" || e.TargetSceneKind != "dm" || NextDeliveryStep(e) != StepAwaitOutbox {
		t.Fatalf("pending scene invitation: %+v", e)
	}
	audience, ok := AudienceForSceneKind(e.TargetSceneKind)
	if !ok || audience != AudienceDirect {
		t.Fatalf("pending scene egress tier: %v %v", audience, ok)
	}
	// The first message from E creates E's DM scene on admission.
	dmE := f.resolveScene(t, "dm", "cid-dm-e")
	early := answer(col, e, "e-early", "5")
	early.Authority.SceneID, early.Binding = dmE, BindDMSinglePending
	if _, err := f.accept(t, early); !errors.Is(err, ErrDeliveryPending) {
		t.Fatalf("input before the scene is resolved: %v", err)
	}
	if candidates, _ := f.store.BindingCandidates(ctx, f.scope, participantE); len(candidates) != 0 {
		t.Fatalf("unresolved invitation offered for binding: %+v", candidates)
	}

	hash := renderedHash(e.Question)
	sent := RecordDeliveryParams{InvitationID: e.ID, ActionID: e.DeliveryActionID, Outcome: DeliverySent, ProviderMessageID: "msg-e", RenderedHash: hash}
	noScene, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, sent)
	if err != nil || noScene.State != InvitationPendingScene || noScene.ProviderMessageID != "msg-e" || NextDeliveryStep(noScene) != StepResolveScene {
		t.Fatalf("sent without a scene: %+v %v", noScene, err)
	}
	for name, sceneID := range map[string]string{"group scene": f.group, "unknown scene": uuid.NewString()} {
		bad := sent
		bad.TargetSceneID = sceneID
		if _, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, bad); err == nil {
			t.Fatalf("backfill to %s accepted", name)
		}
	}
	backfill := sent
	backfill.TargetSceneID = dmE
	resolved, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, backfill)
	if err != nil || resolved.State != InvitationDelivered || resolved.TargetSceneID != dmE || NextDeliveryStep(resolved) != StepNone {
		t.Fatalf("backfill: %+v %v", resolved, err)
	}
	if again, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, backfill); err != nil || again.TargetSceneID != dmE {
		t.Fatalf("backfill replay: %+v %v", again, err)
	}
	other := backfill
	other.TargetSceneID = f.dmD
	if _, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("second, different backfill: %v", err)
	}
	// A known-scene invitation cannot be re-pointed either.
	c := f.deliver(t, invitations[1])
	moved := RecordDeliveryParams{InvitationID: c.ID, ActionID: c.DeliveryActionID, Outcome: DeliverySent, ProviderMessageID: c.ProviderMessageID, RenderedHash: c.RenderedHash, TargetSceneID: f.dmD}
	if _, err := f.store.RecordInviteDeliveryTx(ctx, f.scope, moved); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-pointing a known scene: %v", err)
	}

	// Answers in the resolved DM now bind and count.
	candidates, err := f.store.BindingCandidates(ctx, f.scope, participantE)
	if err != nil || len(candidates) != 1 || candidates[0].TargetSceneID != dmE {
		t.Fatalf("resolved candidates: %+v %v", candidates, err)
	}
	msg := InboundMessage{MessageID: "e-1", SceneID: dmE, SceneKind: "dm", SenderRef: participantE, SenderKind: SenderPerson, MessageKind: MessageText}
	if b := BindAnswer(msg, nil, candidates); b.Outcome != BindBound || b.Kind != BindDMSinglePending || b.InvitationID != e.ID {
		t.Fatalf("binding in the resolved DM: %+v", b)
	}
	f.mustAccept(t, answer(col, resolved, "e-1", "5"))
	view, err := f.store.ReadParticipantInvitation(ctx, f.scope, e.ID, ParticipantViewer{ActorRef: participantE, SceneID: dmE})
	if err != nil || view.Answer == nil || view.Answer.Body != "5" {
		t.Fatalf("participant view after resolution: %+v %v", view, err)
	}

	// A pending_scene invitation that is cancelled before resolution keeps no scene.
	task2 := f.newTask(t, "Other")
	col2, inv2 := f.create(t, task2, InvitationSpec{ParticipantRef: participantE, Question: "?", TargetKind: "dm"})
	if _, _, err := f.store.CloseCollectionTx(ctx, f.scope, CloseParams{CollectionID: col2.ID, Mode: CloseCancel, Source: Source{Namespace: "x", Key: "cancel"}, Authority: requesterAuthority(f.origin), ExpectedRevision: col2.Revision}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.GetInvitation(ctx, f.scope, inv2[0].ID); got.State != InvitationRevoked || got.TargetSceneID != "" {
		t.Fatalf("cancelled pending scene invitation: %+v", got)
	}
}
