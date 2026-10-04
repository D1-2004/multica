package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// employeeWebhookRoutineFixture is a webhook scene routine of an agent in
// employee mode whose runtime is a local daemon advertising Employee Direct,
// with every live replica reading the webhook origin.
type employeeWebhookRoutineFixture struct {
	*employeeWebhookFixture
	runtime  db.AgentRuntime
	daemonID string
	routine  string
}

func newEmployeeWebhookRoutineFixture(t *testing.T, instructions string) *employeeWebhookRoutineFixture {
	t.Helper()
	f, a := routineFixture(t)
	ctx := context.Background()
	x := &employeeWebhookRoutineFixture{daemonID: "webhook-daemon-" + uuid.NewString()}
	runtimeID := uuid.NewString()
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_runtime(id,workspace_id,daemon_id,name,runtime_mode,provider,status,owner_id,metadata,last_seen_at) VALUES($1::uuid,$2::uuid,$3,'Webhook claim runtime','local','codex','online',$4::uuid,'{"client_capabilities":["employee-direct-v1"]}',now())`, runtimeID, testWorkspaceID, x.daemonID, testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, statement := range []string{
			`DELETE FROM employee_webhook_occurrence WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_learning_consumption WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_learning WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_run_notice WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_task_entry WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_task_run WHERE agent_id=$1::uuid`,
			`DELETE FROM employee_task WHERE agent_id=$1::uuid`,
			`DELETE FROM scene_event_receipt WHERE agent_id=$1::uuid`,
			`DELETE FROM task_usage WHERE task_id IN (SELECT id FROM agent_task_queue WHERE agent_id=$1::uuid)`,
			`DELETE FROM task_message WHERE task_id IN (SELECT id FROM agent_task_queue WHERE agent_id=$1::uuid)`,
			`DELETE FROM agent_task_queue WHERE agent_id=$1::uuid`,
			`UPDATE agent SET runtime_id=$2::uuid, coordination_mode='coordinator' WHERE id=$1::uuid`,
		} {
			args := []any{a.ID}
			if strings.Contains(statement, "$2") {
				args = append(args, testRuntimeID)
			}
			if _, err := testPool.Exec(bg, statement, args...); err != nil {
				t.Error(statement, err)
			}
		}
		_, _ = testPool.Exec(bg, `DELETE FROM agent_runtime WHERE id=$1::uuid`, runtimeID)
	})
	if _, err := testPool.Exec(ctx, `UPDATE agent SET runtime_id=$2::uuid,runtime_mode='local',coordination_mode='employee' WHERE id=$1::uuid`, a.ID, runtimeID); err != nil {
		t.Fatal(err)
	}
	var err error
	if x.runtime, err = f.h.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID)); err != nil {
		t.Fatal(err)
	}
	f.h.WebhookSourceReady = func(context.Context) error { return nil }
	f.h.EmployeeSceneWorker = &EmployeeSceneWorker{ReplicaReady: func(context.Context) error { return nil }}
	f.h.WebhookDeliveryWorker = NewWebhookDeliveryWorker(f.h)
	created := createGroupRoutine(t, f, a, sceneRoutineInput{Title: "Deploy watcher", Instructions: instructions, Trigger: sceneRoutineTrigger{Kind: "webhook"}})
	const prefix = "https://multica.example/api/webhooks/autopilots/"
	token := strings.TrimPrefix(created.Routine.Trigger.WebhookURL, prefix)
	ap, err := f.h.Queries.GetAutopilot(ctx, parseUUID(created.Routine.AutopilotID))
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.h.Queries.GetWebhookTriggerByToken(ctx, pgtype.Text{String: token, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := f.h.Queries.GetAutopilotTrigger(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	x.employeeWebhookFixture = &employeeWebhookFixture{f: f, a: a, token: token, ap: ap, trigger: trigger}
	x.routine = created.Routine.ID
	return x
}

func (x *employeeWebhookRoutineFixture) noticeID(runID pgtype.UUID, phase string) string {
	return dingtalkresponse.StableActionID(testWorkspaceID, x.a.ID, dingtalkresponse.RoutineNoticeRequestID(uuidToString(runID), phase), "message.send")
}

func (x *employeeWebhookRoutineFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(sql, err)
	}
	return n
}

// A webhook delivery of an employee-mode routine runs as one EmployeeTask
// Direct execution: dispatch frozen at the ingress, the allowlisted payload
// (not the body) in the packet, a duplicate delivery runs nothing, the claim
// runs the frozen packet with the routine output rule, and the routine's
// result notice is the only sender, without an admission announcement.
func TestEmployeeWebhookRoutineRunsAsEmployeeTask(t *testing.T) {
	x := newEmployeeWebhookRoutineFixture(t, "WEBHOOK_ROUTINE_V1: report the deploy marker.")
	f, ctx := x.f, context.Background()
	previousResponses := testHandler.DingTalkResponses
	testHandler.DingTalkResponses = f.h.DingTalkResponses
	t.Cleanup(func() { testHandler.DingTalkResponses = previousResponses })

	fields := []string{"/event", "/eventPayload/marker", "/eventPayload/missing"}
	if _, err := f.h.updateSceneRoutine(ctx, x.a, mustRoutine(t, f, x.a, x.routine), routineMember(), sceneRoutinePatch{PayloadFields: &fields}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"event":"deploy.finished","eventPayload":{"marker":"HOOK-F2-MARKER","note":"NOT_SELECTED_FIELD","scene_id":"` + ctxcapOtherScene + `","actor":"forged boss"}}`)
	key := map[string]string{"Idempotency-Key": "evt-" + uuid.NewString()}
	accepted := requireWebhookStatus(t, x.post(t, body, key), http.StatusOK, "accepted")
	deliveryID := accepted["delivery_id"].(string)
	binding := frozenColumns(t, deliveryID).Binding
	if binding.Dispatch != webhookDispatchEmployeeDirect || binding.PayloadPolicy != webhookPayloadFieldsV1 || strings.Join(binding.PayloadFields, ",") != strings.Join(fields, ",") {
		t.Fatalf("frozen binding = %+v", binding)
	}

	delivery := x.process(t, deliveryID)
	if delivery.Status != deliveryStatusDispatched || uuidToString(delivery.AutopilotRunID) != accepted["run_id"] {
		t.Fatalf("delivery = %s run=%s err=%q", delivery.Status, uuidToString(delivery.AutopilotRunID), delivery.Error.String)
	}
	run, err := f.h.Queries.GetAutopilotRun(ctx, delivery.AutopilotRunID)
	if err != nil || run.Status != "running" || !run.TaskID.Valid || run.PlannedAt.Valid || run.Source != "webhook" {
		t.Fatalf("run = %+v err=%v", run, err)
	}
	queue, err := f.h.Queries.GetAgentTask(ctx, run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	direct, ok := service.ParseDirectTaskContext(queue)
	if !ok || direct.AutomationOrigin == nil || direct.AutomationOrigin.Kind != service.AutomationOriginSceneRoutineWebhook {
		t.Fatalf("queue context = %s", queue.Context)
	}
	if !strings.Contains(direct.Prompt, "HOOK-F2-MARKER") || strings.Contains(direct.Prompt, "NOT_SELECTED_FIELD") || strings.Contains(direct.Prompt, "forged boss") ||
		!strings.Contains(direct.Prompt, "untrusted data") || !strings.Contains(direct.Prompt, "WEBHOOK_ROUTINE_V1") {
		t.Fatalf("packet = %s", direct.Prompt)
	}
	origin, err := service.LoadAutomationOrigin(ctx, testPool, queue)
	if err != nil || origin.Source().Namespace != "scene.routine.webhook" || origin.Source().Key != "delivery/"+deliveryID ||
		origin.RequesterRef() != "routine:"+x.routine || origin.Scope().Scene.SceneID != ctxcapScene {
		t.Fatalf("origin = %+v err=%v", origin, err)
	}
	if got, ok := service.WebhookDeliveryOf(origin); !ok || got != deliveryID {
		t.Fatalf("origin delivery = %q", got)
	}
	taskOrigin, err := service.LoadAutomationTaskOrigin(ctx, testPool, origin.Scope(), origin.EmployeeTaskID())
	if err != nil || taskOrigin.HistoryPolicy.Kind != service.AutomationHistorySceneEndpoint || taskOrigin.DeliveryAnchor.RoutineID != x.routine ||
		taskOrigin.DeliveryAnchor.Owner != service.AutomationDeliveryOwnerSceneRoutine {
		t.Fatalf("task origin = %+v err=%v", taskOrigin, err)
	}
	if n := x.count(t, `SELECT count(*) FROM response_action WHERE id=$1`, x.noticeID(run.ID, dingtalkresponse.RoutineNoticeStart)); n != 0 {
		t.Fatalf("webhook admission announced itself: %d start notices", n)
	}

	// The provider retries: same receipt, nothing new.
	requireWebhookStatus(t, x.post(t, body, key), http.StatusOK, "duplicate")
	if n := x.count(t, `SELECT count(*) FROM employee_webhook_occurrence WHERE autopilot_id=$1`, x.ap.ID); n != 1 {
		t.Fatalf("occurrences = %d", n)
	}
	if n := x.count(t, `SELECT count(*) FROM agent_task_queue WHERE autopilot_run_id=$1`, run.ID); n != 1 {
		t.Fatalf("queue rows = %d", n)
	}

	auth := service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{x.runtime.ID}}
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, x.runtime.ID, auth)
	if err != nil || claimed == nil || claimed.ID != run.TaskID {
		t.Fatal(claimed, err)
	}
	req := newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, x.daemonID)
	req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityEmployeeDirectV1)
	resp, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, claimed, x.runtime, "", uuidToString(x.runtime.ID), testWorkspaceID)
	if failure != nil {
		t.Fatal(failure.message)
	}
	if !strings.Contains(resp.DirectTaskPrompt, "HOOK-F2-MARKER") || !strings.Contains(resp.DirectTaskPrompt, employeeRoutineOutputInstruction) ||
		strings.Contains(resp.DirectTaskPrompt, employeeDirectOutputInstruction) || resp.AutopilotDescription != "" || strings.Contains(resp.DirectTaskPrompt, "NOT_SELECTED_FIELD") {
		t.Fatalf("claim prompt=%q autopilot=%q", resp.DirectTaskPrompt, resp.AutopilotDescription)
	}
	wire, _ := json.Marshal(resp)
	var task daemon.Task
	if err := json.Unmarshal(wire, &task); err != nil {
		t.Fatal(err)
	}
	if modelInput := daemon.BuildPrompt(task, "codex"); strings.Contains(modelInput, "NOT_SELECTED_FIELD") || strings.Contains(modelInput, "Trigger payload") {
		t.Fatalf("model input carries more than the allowlist: %s", modelInput)
	}

	queueID := uuidToString(claimed.ID)
	w := httptest.NewRecorder()
	testHandler.StartTask(w, withURLParam(newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, x.daemonID), "taskId", queueID))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	testHandler.CompleteTask(w, withURLParam(newDaemonTokenRequest(http.MethodPost, "/", map[string]any{"output": "HOOK-OK HOOK-F2-MARKER", "session_id": "webhook-session"}, testWorkspaceID, x.daemonID), "taskId", queueID))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var endText string
	if err := testPool.QueryRow(ctx, `SELECT input->>'text' FROM response_action WHERE id=$1`, x.noticeID(run.ID, dingtalkresponse.RoutineNoticeEnd)).Scan(&endText); err != nil || endText != "HOOK-OK HOOK-F2-MARKER" {
		t.Fatalf("end notice %q err=%v", endText, err)
	}
	if settled, err := f.h.ReconcileEmployeeRoutineRuns(ctx, 100); err != nil || settled < 1 {
		t.Fatal("reconcile", settled, err)
	}
	if again, err := f.h.ReconcileEmployeeRoutineRuns(ctx, 100); err != nil || again != 0 {
		t.Fatal("settled twice", again, err)
	}
	if apRun, err := f.h.Queries.GetAutopilotRun(ctx, run.ID); err != nil || apRun.Status != "completed" {
		t.Fatal(apRun.Status, err)
	}
	if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	f.h.EmployeeMemory = employeememory.NewStore(testPool)
	for range 20 {
		n, err := f.h.ReconcileEmployeeLearnings(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if n := x.count(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input->>'routine_run_id'=$2`, x.a.ID, uuidToString(run.ID)); n != 1 {
		t.Fatal("routine did not deliver exactly one result notice", n)
	}
	if n := x.count(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input ? 'employee_run_notice_id'`, x.a.ID); n != 0 {
		t.Fatal("a second sender enqueued a notice", n)
	}
	if n := x.count(t, `SELECT count(*) FROM employee_learning WHERE agent_id=$1::uuid`, x.a.ID); n != 0 {
		t.Fatal("automation produced a private learning", n)
	}
	var state string
	if err := testPool.QueryRow(ctx, `SELECT t.state FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE r.queue_task_id=$1::uuid`, queueID).Scan(&state); err != nil || state != string(employeetask.StateSucceeded) {
		t.Fatal("task state", state, err)
	}
}

// While a live replica cannot read the webhook origin, the ingress freezes
// the Autopilot path for that delivery and it runs as before.
func TestEmployeeWebhookReplicaGateKeepsAutopilotPath(t *testing.T) {
	x := newEmployeeWebhookRoutineFixture(t, "GATED_WEBHOOK: say hello.")
	x.f.h.EmployeeSceneWorker = &EmployeeSceneWorker{ReplicaReady: func(context.Context) error { return errEmployeeRoutineTestGate }}
	accepted := requireWebhookStatus(t, x.post(t, []byte(`{"event":"deploy.finished"}`), nil), http.StatusOK, "accepted")
	if b := frozenColumns(t, accepted["delivery_id"].(string)).Binding; b.Dispatch != webhookDispatchAutopilotRunOnly || len(b.PayloadFields) != 0 {
		t.Fatalf("binding = %+v", b)
	}
	delivery := x.process(t, accepted["delivery_id"].(string))
	run, err := x.f.h.Queries.GetAutopilotRun(context.Background(), delivery.AutopilotRunID)
	if err != nil || !run.TaskID.Valid {
		t.Fatal(run, err)
	}
	queue, err := x.f.h.Queries.GetAgentTask(context.Background(), run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.ParseDirectTaskContext(queue); ok {
		t.Fatalf("gated delivery produced a Direct execution: %s", queue.Context)
	}
	if n := x.count(t, `SELECT count(*) FROM employee_webhook_occurrence WHERE autopilot_id=$1`, x.ap.ID); n != 0 {
		t.Fatal("gated delivery recorded an Employee occurrence", n)
	}
}

// A delivery frozen for the Employee path never switches producer: if the
// readers disappear before the worker runs it (a rollback), it waits without
// spending an attempt, and runs on the Employee path once they are back.
func TestEmployeeWebhookFrozenEmployeePathWaitsForReaders(t *testing.T) {
	x := newEmployeeWebhookRoutineFixture(t, "WAITING_WEBHOOK: say hello.")
	ctx := context.Background()
	accepted := requireWebhookStatus(t, x.post(t, []byte(`{"event":"deploy.finished","eventPayload":{"n":1}}`), nil), http.StatusOK, "accepted")
	deliveryID := accepted["delivery_id"].(string)
	x.f.h.EmployeeSceneWorker = &EmployeeSceneWorker{ReplicaReady: func(context.Context) error { return errEmployeeRoutineTestGate }}
	x.processOneLease(t, deliveryID)
	held, err := x.f.h.Queries.GetWebhookDelivery(ctx, parseUUID(deliveryID))
	if err != nil || held.Status != deliveryStatusFrozenQueued || held.DispatchAttempts != 0 || !held.AvailableAt.Time.After(held.ReceivedAt.Time) {
		t.Fatalf("held = %s attempts=%d err=%v", held.Status, held.DispatchAttempts, err)
	}
	if n := x.count(t, `SELECT count(*) FROM agent_task_queue WHERE autopilot_run_id=$1::uuid`, accepted["run_id"]); n != 0 {
		t.Fatal("a task was produced while readers were missing", n)
	}
	x.f.h.EmployeeSceneWorker = &EmployeeSceneWorker{ReplicaReady: func(context.Context) error { return nil }}
	if _, err := testPool.Exec(ctx, `UPDATE webhook_delivery SET available_at=now() WHERE id=$1`, deliveryID); err != nil {
		t.Fatal(err)
	}
	done := x.process(t, deliveryID)
	run, err := x.f.h.Queries.GetAutopilotRun(ctx, done.AutopilotRunID)
	if err != nil || !run.TaskID.Valid {
		t.Fatal(run, err)
	}
	queue, _ := x.f.h.Queries.GetAgentTask(ctx, run.TaskID)
	if direct, ok := service.ParseDirectTaskContext(queue); !ok || direct.AutomationOrigin == nil {
		t.Fatalf("resumed delivery left the Employee path: %s", queue.Context)
	}
}

// processOneLease drives the worker until it has leased the delivery once.
func (x *employeeWebhookRoutineFixture) processOneLease(t *testing.T, deliveryID string) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		d, err := x.f.h.Queries.GetWebhookDelivery(ctx, parseUUID(deliveryID))
		if err != nil {
			t.Fatal(err)
		}
		if d.AvailableAt.Time.After(d.ReceivedAt.Time.Add(employeeWebhookReadinessDelay/2)) || !webhookDeliveryPending(d.Status) {
			return
		}
		if _, err := x.f.h.WebhookDeliveryWorker.ProcessNext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("delivery was never leased")
}

// The routine's payload allowlist is validated and stored per webhook
// trigger; a schedule has none; a chat cannot set it.
func TestEmployeeWebhookPayloadFieldsConfiguration(t *testing.T) {
	x := newEmployeeWebhookRoutineFixture(t, "CONFIG_WEBHOOK: say hello.")
	ctx := context.Background()
	routine := mustRoutine(t, x.f, x.a, x.routine)
	for name, fields := range map[string][]string{
		"not a pointer":  {"eventPayload"},
		"bad escape":     {"/a~2b"},
		"too many":       make([]string, maxWebhookPayloadFields+1),
		"control in key": {"/a\x01"},
	} {
		if name == "too many" {
			for i := range fields {
				fields[i] = "/f" + string(rune('a'+i))
			}
		}
		fields := fields
		if _, err := x.f.h.updateSceneRoutine(ctx, x.a, routine, routineMember(), sceneRoutinePatch{PayloadFields: &fields}); routineCode(err) != "invalid_routine" {
			t.Errorf("%s: %v", name, err)
		}
	}
	ok := []string{"/eventPayload/build/status", "/eventPayload/repo~1name"}
	result, err := x.f.h.updateSceneRoutine(ctx, x.a, routine, routineMember(), sceneRoutinePatch{PayloadFields: &ok})
	if err != nil || strings.Join(result.Routine.Trigger.PayloadFields, ",") != strings.Join(ok, ",") {
		t.Fatalf("set = %+v %v", result.Routine.Trigger, err)
	}
	empty := []string{}
	result, err = x.f.h.updateSceneRoutine(ctx, x.a, routine, routineMember(), sceneRoutinePatch{PayloadFields: &empty})
	if err != nil || strings.Join(result.Routine.Trigger.PayloadFields, ",") != strings.Join(defaultWebhookPayloadFields, ",") {
		t.Fatalf("reset = %+v %v", result.Routine.Trigger, err)
	}
	sc, _ := x.f.h.sceneRoutineScene(ctx, x.a, ctxcapScene)
	if _, err := x.f.h.createSceneRoutine(ctx, x.a, sc, routineMember(), sceneRoutineCounterpart{}, sceneRoutineInput{
		Title: "Cron with fields", Instructions: "x", Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "0 9 * * *", PayloadFields: []string{"/event"}},
	}); routineCode(err) != "invalid_routine" {
		t.Fatalf("schedule with payload fields: %v", err)
	}

	selected, problem, err := selectWebhookPayload(WebhookEnvelope{Event: "e", EventPayload: json.RawMessage(`{"repo/name":"r","build":{"status":"ok","log":"LONG"},"list":[1,2]}`)},
		[]string{"/eventPayload/repo~1name", "/eventPayload/build/status", "/eventPayload/list/1", "/eventPayload/list/01", "/nope"})
	if err != nil || problem != "" || string(selected) != `{"/eventPayload/build/status":"ok","/eventPayload/list/1":2,"/eventPayload/repo~1name":"r"}` {
		t.Fatalf("selected = %s %q %v", selected, problem, err)
	}
	big := WebhookEnvelope{Event: "e", EventPayload: json.RawMessage(`{"blob":"` + strings.Repeat("x", service.MaxWebhookSelectedBytes) + `"}`)}
	if _, problem, err := selectWebhookPayload(big, defaultWebhookPayloadFields); err != nil || problem == "" {
		t.Fatalf("oversized selection: %q %v", problem, err)
	}
}

// An allowlist that selects more than the execution bound fails the
// occurrence visibly: a failed run and delivery, no task, no notice.
func TestEmployeeWebhookOversizedSelectionFailsClosed(t *testing.T) {
	x := newEmployeeWebhookRoutineFixture(t, "BIG_WEBHOOK: say hello.")
	body := bytes.Join([][]byte{[]byte(`{"event":"deploy.finished","eventPayload":{"blob":"`), bytes.Repeat([]byte("y"), service.MaxWebhookSelectedBytes), []byte(`"}}`)}, nil)
	accepted := requireWebhookStatus(t, x.post(t, body, nil), http.StatusOK, "accepted")
	delivery := x.process(t, accepted["delivery_id"].(string))
	run, err := x.f.h.Queries.GetAutopilotRun(context.Background(), delivery.AutopilotRunID)
	if err != nil || delivery.Status != deliveryStatusFailed || run.Status != "failed" || run.TaskID.Valid || !strings.Contains(run.FailureReason.String, "payload fields") {
		t.Fatalf("delivery=%s run=%+v err=%v", delivery.Status, run, err)
	}
	if n := x.count(t, `SELECT count(*) FROM employee_webhook_occurrence WHERE autopilot_id=$1 AND state='failed'`, x.ap.ID); n != 1 {
		t.Fatal("failed occurrence not recorded", n)
	}
	if n := x.count(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input->>'routine_run_id'=$2`, x.a.ID, uuidToString(run.ID)); n != 0 {
		t.Fatal("a refused occurrence posted a notice", n)
	}
}

var _ = scene.KindGroup
