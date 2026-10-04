package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// webhookOccurrenceFixture adds a webhook trigger and accepted deliveries to
// the employee routine fixture.
type webhookOccurrenceFixture struct {
	*employeeRoutineFixture
	hook db.AutopilotTrigger
}

func newWebhookOccurrenceFixture(t *testing.T) *webhookOccurrenceFixture {
	t.Helper()
	f := newEmployeeRoutineFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = f.pool.Exec(bg, `DELETE FROM employee_webhook_occurrence WHERE workspace_id=$1::uuid`, f.ws)
		_, _ = f.pool.Exec(bg, `DELETE FROM webhook_delivery WHERE workspace_id=$1::uuid`, f.ws)
	})
	hook, err := f.q.CreateAutopilotTrigger(ctx, db.CreateAutopilotTriggerParams{AutopilotID: f.ap.ID, Kind: "webhook", Enabled: true,
		WebhookToken: pgtype.Text{String: "awt_" + uuid.NewString(), Valid: true}, Provider: pgtype.Text{String: "generic", Valid: true},
		PublishedByType: pgtype.Text{String: "member", Valid: true}, PublishedByID: f.uuid(f.user)})
	if err != nil {
		t.Fatal(err)
	}
	return &webhookOccurrenceFixture{employeeRoutineFixture: f, hook: hook}
}

// delivery stores an accepted delivery row and returns its frozen view.
func (f *webhookOccurrenceFixture) delivery(t *testing.T, marker string) WebhookRoutineDelivery {
	t.Helper()
	body := []byte(`{"event":"deploy.finished","eventPayload":{"marker":"` + marker + `"}}`)
	d, err := f.q.CreateWebhookDelivery(context.Background(), db.CreateWebhookDeliveryParams{
		WorkspaceID: f.uuid(f.ws), AutopilotID: f.ap.ID, TriggerID: f.hook.ID, Provider: "generic", Event: "deploy.finished",
		SignatureStatus: "not_required", Status: "queued", SelectedHeaders: []byte(`{}`), RawBody: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	return WebhookRoutineDelivery{
		DeliveryID: d.ID, TriggerID: f.hook.ID, IdentityPolicy: "per_request", ReceivedAt: d.ReceivedAt.Time,
		SourceDigest: "sha256:test-" + marker, Event: "deploy.finished", Envelope: body,
		PayloadFields: []string{"/event", "/eventPayload"}, Payload: json.RawMessage(`{"/event":"deploy.finished","/eventPayload":{"marker":"` + marker + `"}}`),
		RoutineID: f.routine.ID, SceneID: f.routine.SceneID, TenantOrgID: f.routine.TenantOrgID,
	}
}

func (f *webhookOccurrenceFixture) occurrences(t *testing.T, state string) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM employee_webhook_occurrence WHERE workspace_id=$1::uuid AND state=$2`, f.ws, state)
}

// One delivery admits one Direct execution through the routine core; a replay
// of the same delivery returns the same run and repeats only the wakeups.
func TestEmployeeWebhookOccurrenceAdmitsOnceAndReplays(t *testing.T) {
	f := newWebhookOccurrenceFixture(t)
	ctx := context.Background()
	d := f.delivery(t, "M1")
	run, err := f.svc.DispatchEmployeeWebhookRoutine(ctx, f.ap, d)
	if err != nil || run.Status != "running" || !run.TaskID.Valid || run.PlannedAt.Valid || run.Source != "webhook" || run.WebhookDeliveryID != d.DeliveryID {
		t.Fatalf("run = %+v err=%v", run, err)
	}
	queue, err := f.q.GetAgentTask(ctx, run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := LoadAutomationOrigin(ctx, f.pool, queue)
	if err != nil || origin.Kind() != AutomationOriginSceneRoutineWebhook || origin.Principal().ID != f.user || origin.Scope().Scene.SceneID != f.sceneID {
		t.Fatalf("origin = %+v err=%v", origin, err)
	}
	if len(f.host.notices) != 1 {
		t.Fatalf("start notices = %d", len(f.host.notices))
	}
	notified := f.host.notified
	again, err := f.svc.DispatchEmployeeWebhookRoutine(ctx, f.ap, d)
	if err != nil || again.ID != run.ID {
		t.Fatalf("replay = %+v err=%v", again, err)
	}
	if f.occurrences(t, "accepted") != 1 || f.count(t, `SELECT count(*) FROM agent_task_queue WHERE autopilot_run_id=$1`, run.ID) != 1 ||
		f.count(t, `SELECT count(*) FROM employee_task WHERE workspace_id=$1::uuid`, f.ws) != 1 || len(f.host.notices) != 1 || f.host.notified <= notified {
		t.Fatal("replay admitted again or skipped the wakeups")
	}
}

// The ingress admitted the run: the producer uses that exact run.
func TestEmployeeWebhookOccurrenceUsesTheAdmittedRun(t *testing.T) {
	f := newWebhookOccurrenceFixture(t)
	ctx := context.Background()
	d := f.delivery(t, "M2")
	admitted, err := f.q.CreateAutopilotRun(ctx, db.CreateAutopilotRunParams{AutopilotID: f.ap.ID, TriggerID: f.hook.ID, Source: "webhook", Status: "running", TriggerPayload: d.Envelope, WebhookDeliveryID: d.DeliveryID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.svc.DispatchEmployeeWebhookRoutine(ctx, f.ap, d)
	if err != nil || run.ID != admitted.ID || !run.TaskID.Valid {
		t.Fatalf("run = %+v err=%v", run, err)
	}
	if f.count(t, `SELECT count(*) FROM autopilot_run WHERE autopilot_id=$1`, f.ap.ID) != 1 {
		t.Fatal("a second run was created")
	}
}

// Two workers on the same delivery, each on its own connection, admit one
// execution.
func TestEmployeeWebhookOccurrenceConcurrentWorkersAdmitOnce(t *testing.T) {
	f := newWebhookOccurrenceFixture(t)
	d := f.delivery(t, "M3")
	var wg sync.WaitGroup
	runs := make([]*db.AutopilotRun, 4)
	errs := make([]error, 4)
	for i := range runs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runs[i], errs[i] = f.svc.DispatchEmployeeWebhookRoutine(context.Background(), f.ap, d)
		}(i)
	}
	wg.Wait()
	var runID pgtype.UUID
	for i := range runs {
		if errs[i] != nil {
			// A loser of the run insert race fails cleanly and its worker
			// retries; it must never admit a second execution.
			continue
		}
		if runID.Valid && runs[i].ID != runID {
			t.Fatalf("different runs: %v %v", runID, runs[i].ID)
		}
		runID = runs[i].ID
	}
	if !runID.Valid {
		t.Fatalf("no worker admitted: %v", errs)
	}
	if _, err := f.svc.DispatchEmployeeWebhookRoutine(context.Background(), f.ap, d); err != nil {
		t.Fatal("retry after the race", err)
	}
	if f.occurrences(t, "accepted") != 1 || f.count(t, `SELECT count(*) FROM agent_task_queue WHERE autopilot_run_id=$1`, runID) != 1 ||
		f.count(t, `SELECT count(*) FROM autopilot_run WHERE autopilot_id=$1`, f.ap.ID) != 1 {
		t.Fatal("concurrent workers admitted more than once")
	}
}

// A failure at any write boundary rolls the whole admission back, run
// included; the next attempt admits exactly once.
func TestEmployeeWebhookOccurrenceRollsBackEveryBoundary(t *testing.T) {
	for _, stage := range []string{"employee_task", "queue", "run", "autopilot_run_task", "occurrence"} {
		t.Run(stage, func(t *testing.T) {
			f := newWebhookOccurrenceFixture(t)
			d := f.delivery(t, "R-"+stage)
			f.svc.employeeRoutineFault = func(s string) error {
				if s == stage {
					return errors.New("injected " + s)
				}
				return nil
			}
			if _, err := f.svc.DispatchEmployeeWebhookRoutine(context.Background(), f.ap, d); err == nil {
				t.Fatal("fault not surfaced")
			}
			if f.count(t, `SELECT count(*) FROM autopilot_run WHERE autopilot_id=$1`, f.ap.ID) != 0 || f.occurrences(t, "accepted") != 0 ||
				f.count(t, `SELECT count(*) FROM employee_task WHERE workspace_id=$1::uuid`, f.ws) != 0 || len(f.host.notices) != 0 {
				t.Fatal("partial admission survived")
			}
			f.svc.employeeRoutineFault = nil
			run, err := f.svc.DispatchEmployeeWebhookRoutine(context.Background(), f.ap, d)
			if err != nil || !run.TaskID.Valid || f.occurrences(t, "accepted") != 1 {
				t.Fatalf("retry = %+v err=%v", run, err)
			}
		})
	}
}

// Refusals are recorded, never run elsewhere: a revoked creator skips, a
// selection error fails, a changed binding runs nowhere; a pause after
// acceptance does not drop the accepted delivery.
func TestEmployeeWebhookOccurrenceRefusals(t *testing.T) {
	t.Run("creator revoked", func(t *testing.T) {
		f := newWebhookOccurrenceFixture(t)
		if _, err := f.pool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id=$1::uuid AND user_id=$2::uuid`, f.ws, f.user); err != nil {
			t.Fatal(err)
		}
		run, err := f.svc.DispatchEmployeeWebhookRoutine(context.Background(), f.ap, f.delivery(t, "C1"))
		if err != nil || run.Status != "skipped" || run.TaskID.Valid || f.occurrences(t, "skipped") != 1 {
			t.Fatalf("run = %+v err=%v", run, err)
		}
	})
	t.Run("selection error", func(t *testing.T) {
		f := newWebhookOccurrenceFixture(t)
		d := f.delivery(t, "S1")
		d.SelectionError = "selected webhook payload is too large"
		run, err := f.svc.DispatchEmployeeWebhookRoutine(context.Background(), f.ap, d)
		if err != nil || run.Status != "failed" || f.occurrences(t, "failed") != 1 || len(f.host.notices) != 0 {
			t.Fatalf("run = %+v err=%v", run, err)
		}
	})
	t.Run("binding changed", func(t *testing.T) {
		f := newWebhookOccurrenceFixture(t)
		d := f.delivery(t, "B1")
		d.SceneID = uuid.NewString()
		if _, err := f.svc.DispatchEmployeeWebhookRoutine(context.Background(), f.ap, d); !errors.Is(err, ErrWebhookRoutineGone) {
			t.Fatalf("err = %v", err)
		}
		if f.count(t, `SELECT count(*) FROM autopilot_run WHERE autopilot_id=$1`, f.ap.ID) != 0 || f.occurrences(t, "accepted") != 0 {
			t.Fatal("a changed binding produced work")
		}
	})
	t.Run("paused after acceptance", func(t *testing.T) {
		f := newWebhookOccurrenceFixture(t)
		d := f.delivery(t, "P1")
		if _, err := f.pool.Exec(context.Background(), `UPDATE autopilot SET status='paused' WHERE id=$1`, f.ap.ID); err != nil {
			t.Fatal(err)
		}
		run, err := f.svc.DispatchEmployeeWebhookRoutine(context.Background(), f.ap, d)
		if err != nil || run.Status != "running" || !run.TaskID.Valid {
			t.Fatalf("accepted delivery dropped by a later pause: %+v %v", run, err)
		}
	})
	t.Run("no overlap rule", func(t *testing.T) {
		f := newWebhookOccurrenceFixture(t)
		first, err := f.svc.DispatchEmployeeWebhookRoutine(context.Background(), f.ap, f.delivery(t, "O1"))
		if err != nil || !first.TaskID.Valid {
			t.Fatal(first, err)
		}
		second, err := f.svc.DispatchEmployeeWebhookRoutine(context.Background(), f.ap, f.delivery(t, "O2"))
		if err != nil || !second.TaskID.Valid || second.ID == first.ID {
			t.Fatalf("second delivery while the first runs: %+v %v", second, err)
		}
	})
}

// A Task created by a webhook occurrence loads its origin for a later wake;
// a forged locator on a queue row is refused.
func TestEmployeeWebhookOccurrenceTaskOriginAndForgery(t *testing.T) {
	f := newWebhookOccurrenceFixture(t)
	ctx := context.Background()
	run, err := f.svc.DispatchEmployeeWebhookRoutine(ctx, f.ap, f.delivery(t, "T1"))
	if err != nil {
		t.Fatal(err)
	}
	queue, err := f.q.GetAgentTask(ctx, run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := LoadAutomationOrigin(ctx, f.pool, queue)
	if err != nil {
		t.Fatal(err)
	}
	taskOrigin, err := LoadAutomationTaskOrigin(ctx, f.pool, origin.Scope(), origin.EmployeeTaskID())
	if err != nil || taskOrigin.Creator.ID != f.user || taskOrigin.HistoryPolicy.Kind != AutomationHistorySceneEndpoint || taskOrigin.DeliveryAnchor.RoutineID != f.routine.ID {
		t.Fatalf("task origin = %+v err=%v", taskOrigin, err)
	}
	var namespaces = map[string]bool{}
	for _, ns := range AutomationTaskSourceNamespaces {
		namespaces[ns] = true
	}
	if !namespaces[origin.Source().Namespace] {
		t.Fatalf("namespace %s not registered", origin.Source().Namespace)
	}
	// A routine occurrence locator pointed at this webhook receipt is refused.
	forged := queue
	var ctxMap map[string]any
	_ = json.Unmarshal(queue.Context, &ctxMap)
	ctxMap[AutomationOriginContextKey] = AutomationOriginRef{Kind: AutomationOriginSceneRoutine, ReceiptID: origin.ReceiptID(), AutopilotRunID: util.UUIDToString(run.ID)}
	forged.Context, _ = json.Marshal(ctxMap)
	if _, err := LoadAutomationOrigin(ctx, f.pool, forged); !errors.Is(err, ErrAutomationOriginInvalid) {
		t.Fatalf("forged kind: %v", err)
	}
	_ = time.Now
}
