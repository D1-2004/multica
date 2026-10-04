package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/deploymentfence"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWebhookFrozenSourceAdmissionNeedsEveryReader(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	ctx := context.Background()
	currentID, oldID := uuid.NewString(), uuid.NewString()
	fence, err := deploymentfence.New(ctx, testPool, currentID, "test "+WebhookSourceReplicaMarker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM deployment_fence_replica_ack WHERE instance_id=ANY($1::text[])`, []string{currentID, oldID})
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO deployment_fence_replica_ack(instance_id,build_id,state,revision,last_seen_at) VALUES($1,'old-reader','normal',1,now())`, oldID); err != nil {
		t.Fatal(err)
	}
	e.f.h.WebhookSourceReady = func(ctx context.Context) error {
		ready, err := fence.AllLiveReplicasSupport(ctx, WebhookSourceReplicaMarker)
		if err != nil {
			return err
		}
		if !ready {
			return errors.New("mixed reader set")
		}
		return nil
	}
	rec := e.post(t, []byte(`{"event":"release.finished"}`), nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("mixed readers response=%d %s", rec.Code, rec.Body.String())
	}
	if len(e.deliveries(t)) != 0 {
		t.Fatal("mixed reader admission persisted a receipt")
	}
	if runs, tasks := e.counts(t); runs != 0 || tasks != 0 {
		t.Fatalf("mixed reader created runs=%d tasks=%d", runs, tasks)
	}
	ready := e.f.h.WebhookSourceReady
	e.f.h.WebhookSourceReady = nil
	if rec := e.post(t, []byte(`{"event":"release.finished"}`), nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing reader verifier response=%d", rec.Code)
	}
	e.f.h.WebhookSourceReady = ready
	if _, err := testPool.Exec(ctx, `UPDATE deployment_fence_replica_ack SET build_id=$2 WHERE instance_id=$1`, oldID, "upgraded "+WebhookSourceReplicaMarker); err != nil {
		t.Fatal(err)
	}
	accepted := requireWebhookStatus(t, e.post(t, []byte(`{"event":"release.finished"}`), nil), http.StatusOK, "accepted")
	d, err := e.f.h.Queries.GetWebhookDelivery(ctx, parseUUID(accepted["delivery_id"].(string)))
	if err != nil || d.Status != deliveryStatusFrozenQueued {
		t.Fatalf("compatible acceptance state=%s err=%v", d.Status, err)
	}
	e.process(t, uuidToString(d.ID))
}

func TestWebhookFrozenQueueProtectsOldProducerAndConsumer(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	ctx := context.Background()
	binding, problem, err := e.f.h.resolveWebhookEndpointBinding(ctx, e.ap, e.trigger.ID, "generic", "")
	if err != nil || problem != "" {
		t.Fatalf("binding: %s %v", problem, err)
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	// Emulate the old producer: queued INSERT, then v1 binding UPDATE in one tx.
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	d, err := e.f.h.Queries.WithTx(tx).CreateWebhookDelivery(ctx, db.CreateWebhookDeliveryParams{
		WorkspaceID: e.ap.WorkspaceID, AutopilotID: e.ap.ID, TriggerID: e.trigger.ID, Provider: "generic", Event: "release.finished",
		SignatureStatus: sigStatusNotRequired, Status: deliveryStatusQueued, SelectedHeaders: []byte(`{}`), RawBody: []byte(`{"event":"release.finished"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE webhook_delivery SET source_binding=$2::jsonb,source_digest='test-digest',available_at='1970-01-01' WHERE id=$1`, d.ID, encoded); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	d, err = e.f.h.Queries.GetWebhookDelivery(ctx, d.ID)
	if err != nil || d.Status != deliveryStatusFrozenQueued {
		t.Fatalf("old producer escaped guard: status=%s err=%v", d.Status, err)
	}
	// Execute the old claim predicate against this source. It cannot acquire a lease.
	var oldClaim pgtype.UUID
	err = testPool.QueryRow(ctx, `WITH candidate AS (SELECT id FROM webhook_delivery WHERE status='queued' AND available_at<=now() AND trigger_id=$1 AND (lease_expires_at IS NULL OR lease_expires_at<=now()) FOR UPDATE SKIP LOCKED LIMIT 1)
        UPDATE webhook_delivery d SET lease_token=gen_random_uuid(),lease_expires_at=now()+interval '2 minutes' FROM candidate WHERE d.id=candidate.id RETURNING d.id`, e.trigger.ID).Scan(&oldClaim)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old consumer claimed frozen source: %v", err)
	}
	fenceID := uuid.NewString()
	fence, err := deploymentfence.New(ctx, testPool, fenceID, "test "+WebhookSourceReplicaMarker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM deployment_fence_replica_ack WHERE instance_id=$1`, fenceID)
	})
	before, err := fence.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := e.f.h.Queries.ClaimQueuedWebhookDelivery(ctx)
	if err != nil || claimed.ID != d.ID || claimed.Status != deliveryStatusFrozenQueued {
		t.Fatalf("compatible claim: %+v %v", claimed, err)
	}
	after, err := fence.Status(ctx)
	if err != nil || after.Work.WebhookLeases != before.Work.WebhookLeases+1 {
		t.Fatalf("deployment drain missed frozen lease: before=%d after=%d err=%v", before.Work.WebhookLeases, after.Work.WebhookLeases, err)
	}
	tag, err := testPool.Exec(ctx, `UPDATE webhook_delivery SET status='dispatched' WHERE id=$1 AND lease_token=$2 AND status='queued'`, claimed.ID, claimed.LeaseToken)
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("old lease mutation touched frozen row: %v %v", tag, err)
	}
	deferred, err := e.f.h.Queries.DeferClaimedWebhookDelivery(ctx, db.DeferClaimedWebhookDeliveryParams{ID: claimed.ID, LeaseToken: claimed.LeaseToken, AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true}})
	if err != nil || deferred.Status != deliveryStatusFrozenQueued || deferred.LeaseToken.Valid {
		t.Fatalf("defer lost isolation: %+v %v", deferred, err)
	}
	if deliveryToResponse(deferred, false).Status != deliveryStatusQueued {
		t.Fatal("queue isolation broke the public status enum")
	}
	// A downgrade must neither relabel it nor remove the DB protection.
	if _, err := testPool.Exec(ctx, `UPDATE webhook_delivery SET status='queued' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	d, err = e.f.h.Queries.GetWebhookDelivery(ctx, d.ID)
	if err != nil || d.Status != deliveryStatusFrozenQueued {
		t.Fatalf("downgrade exposed source: %s %v", d.Status, err)
	}
	down, err := os.ReadFile("../../migrations/9997_webhook_frozen_queue.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, err = testPool.Exec(ctx, string(down))
	var dbErr *pgconn.PgError
	if !errors.As(err, &dbErr) || dbErr.Code != "55000" {
		t.Fatalf("down did not refuse pending frozen source: %v", err)
	}
}
