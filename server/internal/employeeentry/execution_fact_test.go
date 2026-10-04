package employeeentry

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
)

func executionFactFixture(t *testing.T, f fixture, held bool, route ...string) ExecutionFact {
	t.Helper()
	a := f.admission
	ws, _ := util.ParseUUID(a.Scope.WorkspaceID)
	agent, _ := util.ParseUUID(a.Scope.AgentID)
	principal, _ := util.ParseUUID(a.Item.PrincipalID)
	e := eventrouter.Event{Version: 1, ID: uuid.NewString(), Source: "employee.execution", Type: "execution.terminal", Category: eventrouter.RunCallback, PayloadSchema: "employee.execution/1", Payload: json.RawMessage(`{"run_id":"recorded-run"}`)}
	h := eventrouter.Host{Owner: scene.Owner{WorkspaceID: ws, AgentID: agent}, PrincipalID: principal, TenantOrgID: a.Scope.TenantOrgID, Locator: scene.DingTalkConversation("org-a", scene.KindGroup, "cid-test"), Route: eventrouter.Unified, ConfigVersion: "execution/1", Fingerprint: e.ID}
	if len(route) > 0 {
		h.Route = route[0]
	}
	state, reason := "completed", "current_goal_revision"
	if held {
		h.UnmappedReason = "scene_unavailable"
		state, reason = "held", h.UnmappedReason
	}
	r, _, err := eventrouter.Admit(context.Background(), f.pool, e, h)
	if err != nil {
		t.Fatal(err)
	}
	a.Scope.SceneID = util.UUIDToString(r.SceneID)
	return ExecutionFact{Scope: a.Scope, ReceiptID: util.UUIDToString(r.ID), PrincipalID: a.Item.PrincipalID, Payload: e.Payload, ConfigRevision: h.ConfigVersion, State: state, Reason: reason}
}

func TestExecutionFactRecordsWithoutJobAndReplays(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "held"}[held], func(t *testing.T) {
			f := database(t)
			fact := executionFactFixture(t, f, held)
			var wg sync.WaitGroup
			errs := make(chan error, 6)
			created := make(chan bool, 6)
			for range 6 {
				wg.Go(func() {
					c, fresh, err := f.store.RecordExecutionFact(context.Background(), fact)
					if err == nil && (c.State != fact.State || c.JobID != "" || c.Owner != Employee || c.Reason != fact.Reason) {
						err = errors.New("fact created work or changed its disposition")
					}
					errs <- err
					created <- fresh
				})
			}
			wg.Wait()
			close(errs)
			close(created)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			n := 0
			for fresh := range created {
				if fresh {
					n++
				}
			}
			var jobs int
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM employee_scene_job`).Scan(&jobs); err != nil || jobs != 0 || n != 1 {
				t.Fatalf("new facts=%d jobs=%d err=%v", n, jobs, err)
			}
			fact.Payload = json.RawMessage(`{"run_id":"changed"}`)
			if _, _, err := f.store.RecordExecutionFact(context.Background(), fact); !errors.Is(err, ErrConflict) {
				t.Fatalf("changed fact accepted: %v", err)
			}
		})
	}
}

func TestExecutionFactRequiresMatchingCallbackReceiptAndOuterCommit(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	fact := executionFactFixture(t, f, false)
	wrong := fact
	wrong.PrincipalID = uuid.NewString()
	if _, _, err := f.store.RecordExecutionFact(ctx, wrong); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign principal accepted: %v", err)
	}
	wrong = fact
	wrong.ReceiptID = f.admission.Item.ReceiptID
	if _, _, err := f.store.RecordExecutionFact(ctx, wrong); !errors.Is(err, ErrInvalid) {
		t.Fatalf("human message consumed as terminal fact: %v", err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, _, err := NewStore(tx).RecordExecutionFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Lookup(ctx, fact.Scope, fact.ReceiptID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fact escaped outer rollback: %v", err)
	}
}

func TestExecutionFactCannotReplaceAdmittedPayload(t *testing.T) {
	f := database(t)
	fact := executionFactFixture(t, f, false)
	fact.Payload = json.RawMessage(`{"run_id":"another-run"}`)
	if _, _, err := f.store.RecordExecutionFact(context.Background(), fact); !errors.Is(err, ErrConflict) {
		t.Fatalf("consumer replaced admitted fact: %v", err)
	}
	if _, err := f.store.Lookup(context.Background(), fact.Scope, fact.ReceiptID); !errors.Is(err, ErrNotFound) {
		t.Fatal("invalid fact persisted", err)
	}
}

func TestExecutionFactAcceptsResolvedLegacyAndRejectsInconsistentReceipt(t *testing.T) {
	for _, kind := range []string{"legacy", "wrong_state", "reason", "unmapped"} {
		t.Run(kind, func(t *testing.T) {
			f := database(t)
			fact := executionFactFixture(t, f, kind == "unmapped", eventrouter.Legacy)
			fact.State = "completed"
			if kind == "wrong_state" {
				if _, err := f.pool.Exec(context.Background(), `UPDATE scene_event_receipt SET state='ready' WHERE id=$1::uuid`, fact.ReceiptID); err == nil {
					t.Fatal("database accepted inconsistent route/state")
				}
				return
			}
			if kind == "reason" {
				if _, err := f.pool.Exec(context.Background(), `UPDATE scene_event_receipt SET reason='unresolved_source' WHERE id=$1::uuid`, fact.ReceiptID); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := f.store.RecordExecutionFact(context.Background(), fact)
			if kind == "legacy" && err != nil {
				t.Fatal("valid legacy fact rejected", err)
			}
			if kind != "legacy" && !errors.Is(err, ErrInvalid) {
				t.Fatal("unresolved/inconsistent receipt admitted", err)
			}
		})
	}
}
