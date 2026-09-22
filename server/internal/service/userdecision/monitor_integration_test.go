package userdecision

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresMonitorAlertsAndMetrics(t *testing.T) {
	ctx, pool := preproductionTestDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(c)
	})
	store := &Store{DB: tx, Environment: "integration-" + uuid.NewString()}
	var agent, workspace, owner string
	err = tx.QueryRow(ctx, `SELECT a.id::text,a.workspace_id::text,a.owner_id::text FROM agent a JOIN member m ON m.workspace_id=a.workspace_id AND m.user_id=a.owner_id WHERE a.archived_at IS NULL LIMIT 1`).Scan(&agent, &workspace, &owner)
	if err != nil {
		t.Fatal(err)
	}
	var waitingID string
	for _, state := range []string{"send_unknown", "waiting", "accepted", "dispatched", "healthy"} {
		r := integrationRequest(store.Environment)
		r.AgentID = agent
		r.WorkspaceID = workspace
		r.SenderUID = "fixture-" + r.ID
		if _, err = store.Prepare(ctx, r); err != nil {
			t.Fatal(err)
		}
		actual := state
		if state == "healthy" {
			actual = "waiting"
		}
		_, err = tx.Exec(ctx, `UPDATE coordinator_user_decision SET state=$2,created_at=now()-interval '10 minutes',updated_at=now()-interval '10 minutes',sent_at=now()-interval '10 minutes',accepted_at=CASE WHEN $2::text='accepted' THEN now()-interval '6 minutes' ELSE NULL END,card_update_pending=($2::text='dispatched') WHERE id=$1`, r.ID, actual)
		if err != nil {
			t.Fatal(err)
		}
		if state == "waiting" {
			waitingID = r.ID
		}
		if state == "accepted" || state == "healthy" {
			_, err = tx.Exec(ctx, `INSERT INTO coordinator_user_decision_consumer(environment,sender_uid,sender_org_id,owner,lease_expires_at,ready) VALUES($1,$2,$3,$4,now()+interval '1 hour',true)`, store.Environment, r.SenderUID, r.SenderOrgID, uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	alerts, err := store.Monitor(ctx)
	if err != nil || len(alerts) != 4 {
		t.Fatalf("alerts=%d err=%v", len(alerts), err)
	}
	want := map[string]bool{"consumer_unavailable": false, "send_unknown": false, "resume_delayed": false, "card_update_delayed": false}
	for _, alert := range alerts {
		if _, ok := want[alert.Reason]; !ok {
			t.Fatalf("unexpected alert %s", alert.Reason)
		}
		want[alert.Reason] = true
		var item map[string]any
		if err = json.Unmarshal(alert.Item, &item); err != nil {
			t.Fatal(err)
		}
		if item["recipient_id"] != owner || item["workspace_id"] != workspace || item["severity"] != "attention" {
			t.Fatal("incorrect alert recipient/scope")
		}
	}
	for reason, found := range want {
		if !found {
			t.Fatalf("missing %s", reason)
		}
	}
	// Another service instance observes the same authoritative notification gate.
	restarted := &Store{DB: tx, Environment: store.Environment}
	if again, err := restarted.Monitor(ctx); err != nil || len(again) != 0 {
		t.Fatalf("duplicate alerts=%d err=%v", len(again), err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO coordinator_user_decision_event(decision_id,environment,event_id,operator_id,outcome,payload,delivery_count) VALUES($1,$2,$3,'other','identity_mismatch','{}',3)`, waitingID, store.Environment, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Metrics(ctx, workspace, agent)
	if err != nil {
		t.Fatal(err)
	}
	var metrics struct {
		Waiting    int            `json:"waiting"`
		Duplicates int            `json:"duplicate_deliveries_total"`
		Outcomes   map[string]int `json:"outcomes_24h"`
	}
	if err = json.Unmarshal(raw, &metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.Waiting != 2 || metrics.Duplicates != 2 || metrics.Outcomes["identity_mismatch"] != 1 {
		t.Fatalf("wrong metrics %+v", metrics)
	}
	// The same database and agent cannot expose another environment's metrics.
	other := &Store{DB: tx, Environment: "other-" + store.Environment}
	raw, err = other.Metrics(ctx, workspace, agent)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.Waiting != 0 || metrics.Duplicates != 0 {
		t.Fatal("metrics crossed environments")
	}
}
