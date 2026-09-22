package userdecision

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresConcurrentAcceptance(t *testing.T) {
	ctx, pool := preproductionTestDB(t)
	store := &Store{DB: pool, Environment: "integration-" + uuid.NewString()}
	p := Proposal{Question: "fixture", Options: []Option{{ID: "new", Label: "new", Kind: "start_work", Plan: json.RawMessage(`{}`)}, {ID: "reply", Label: "reply", Kind: "reply", Plan: json.RawMessage(`{}`)}}}
	r := Request{ID: uuid.NewString(), WorkspaceID: uuid.NewString(), AgentID: uuid.NewString(), JobID: uuid.NewString(), Environment: store.Environment, CorpID: "test-corp", ConversationID: "test-cid", InitiatorID: "initiator", SenderUID: "sender", SenderOrgID: "org", Snapshot: json.RawMessage(`{}`), Proposal: p}
	id, err := store.Prepare(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanup, `DELETE FROM coordinator_user_decision_event WHERE decision_id=$1`, id); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM coordinator_user_decision WHERE id=$1`, id); err != nil {
			t.Error(err)
		}
	})
	_, err = pool.Exec(ctx, `UPDATE coordinator_user_decision SET state='sending' WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ConfirmSent(ctx, id, "transformer_card_"+strings.ReplaceAll(id, "-", "")); err != nil {
		t.Fatal(err)
	}
	if err = store.ConfirmSent(ctx, id, "other-provider-card"); err == nil {
		t.Fatal("confirmed card identity was rebound")
	}
	base := Event{ID: uuid.NewString(), CorpID: r.CorpID, ConversationID: r.ConversationID, CardID: "transformer_card_" + strings.ReplaceAll(id, "-", ""), OperatorID: r.InitiatorID, RequestID: id, Version: Version, Selected: []string{"new"}, Raw: json.RawMessage(`{"fixture":true}`)}
	impostor := base
	impostor.ID = uuid.NewString()
	impostor.OperatorID = "other"
	outcome, err := store.Accept(ctx, impostor)
	if err != nil || outcome != "identity_or_version_mismatch" {
		t.Fatal(outcome, err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event := base
			event.ID = uuid.NewString()
			result, err := store.Accept(ctx, event)
			if err != nil {
				t.Error(err)
			}
			if result == "accepted" {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("got %d winners", wins.Load())
	}
	// The same event ID is idempotent even when the request is already resolved.
	_, err = store.Accept(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err = store.Accept(ctx, base)
	if err != nil || outcome != "duplicate_event" {
		t.Fatal(outcome, err)
	}
	var accepted, events, deliveries int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE outcome='accepted'),count(*),sum(delivery_count) FROM coordinator_user_decision_event WHERE decision_id=$1`, id).Scan(&accepted, &events, &deliveries); err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || events != 18 || deliveries != 19 {
		t.Fatalf("accepted=%d events=%d deliveries=%d", accepted, events, deliveries)
	}
	var before time.Time
	if err = pool.QueryRow(ctx, `SELECT expires_at FROM coordinator_user_decision WHERE id=$1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = store.ConfirmSent(ctx, id, "transformer_card_"+strings.ReplaceAll(id, "-", "")); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	_ = pool.QueryRow(ctx, `SELECT expires_at FROM coordinator_user_decision WHERE id=$1`, id).Scan(&after)
	if !before.Equal(after) {
		t.Fatal("send confirmation extended expiry")
	}
}
