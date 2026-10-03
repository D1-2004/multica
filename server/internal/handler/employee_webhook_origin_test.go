package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/contextcap"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// employeeWebhookFixture is a webhook scene routine (例行任务) of the ctxcap
// agent in its group scene: the endpoint token, its autopilot and trigger,
// and the handler that serves the ingress and runs the delivery worker.
type employeeWebhookFixture struct {
	f       *ctxcapFixture
	a       contextCapAgent
	token   string
	ap      db.Autopilot
	trigger db.AutopilotTrigger
}

func newEmployeeWebhookFixture(t *testing.T) *employeeWebhookFixture {
	t.Helper()
	f, a := routineFixture(t)
	f.h.WebhookDeliveryWorker = NewWebhookDeliveryWorker(f.h)
	created := createGroupRoutine(t, f, a, sceneRoutineInput{
		Title:        "Deploy hook " + uuid.NewString()[:8],
		Instructions: "Summarize the deploy event for the group.",
		Trigger:      sceneRoutineTrigger{Kind: "webhook"},
	})
	const prefix = "https://multica.example/api/webhooks/autopilots/"
	token := strings.TrimPrefix(created.Routine.Trigger.WebhookURL, prefix)
	if token == "" || token == created.Routine.Trigger.WebhookURL {
		t.Fatalf("routine webhook URL = %q", created.Routine.Trigger.WebhookURL)
	}
	ctx := context.Background()
	ap, err := f.h.Queries.GetAutopilot(ctx, parseUUID(created.Routine.AutopilotID))
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := f.h.Queries.GetWebhookTriggerByToken(ctx, pgtype.Text{String: token, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	full, err := f.h.Queries.GetAutopilotTrigger(ctx, trigger.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &employeeWebhookFixture{f: f, a: a, token: token, ap: ap, trigger: full}
}

func (e *employeeWebhookFixture) post(t *testing.T, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return e.postToken(t, e.token, body, headers)
}

func (e *employeeWebhookFixture) postToken(t *testing.T, token string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/autopilots/"+token, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = withURLParam(req, "token", token)
	rec := httptest.NewRecorder()
	e.f.h.HandleAutopilotWebhook(rec, req)
	return rec
}

func (e *employeeWebhookFixture) setSecret(t *testing.T, secret string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE autopilot_trigger SET signing_secret = $1 WHERE id = $2`, secret, e.trigger.ID); err != nil {
		t.Fatal(err)
	}
}

func (e *employeeWebhookFixture) setProvider(t *testing.T, provider string) {
	t.Helper()
	setTriggerProvider(t, uuidToString(e.trigger.ID), provider)
}

// process drives the durable worker until the delivery leaves queued.
func (e *employeeWebhookFixture) process(t *testing.T, deliveryID string) db.WebhookDelivery {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		delivery, err := e.f.h.Queries.GetWebhookDelivery(ctx, parseUUID(deliveryID))
		if err != nil {
			t.Fatalf("load delivery: %v", err)
		}
		if delivery.Status != deliveryStatusQueued {
			return delivery
		}
		if _, err := e.f.h.WebhookDeliveryWorker.ProcessNext(ctx); err != nil {
			t.Fatalf("process delivery: %v", err)
		}
	}
	t.Fatalf("delivery %s stayed queued", deliveryID)
	return db.WebhookDelivery{}
}

// processOneAttempt drives the worker until it has attempted the delivery
// once (other queued deliveries in the shared database may come first).
func (e *employeeWebhookFixture) processOneAttempt(t *testing.T, id pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		d, err := e.f.h.Queries.GetWebhookDelivery(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.DispatchAttempts > 0 || d.Status != deliveryStatusQueued {
			return
		}
		if _, err := e.f.h.WebhookDeliveryWorker.ProcessNext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("delivery %s was never attempted", uuidToString(id))
}

func (e *employeeWebhookFixture) deliveries(t *testing.T) []db.WebhookDelivery {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT id::text FROM webhook_delivery WHERE trigger_id = $1 ORDER BY created_at`, e.trigger.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]db.WebhookDelivery, 0, len(ids))
	for _, id := range ids {
		d, err := e.f.h.Queries.GetWebhookDelivery(context.Background(), parseUUID(id))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func (e *employeeWebhookFixture) counts(t *testing.T) (runs, tasks int) {
	t.Helper()
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot_run WHERE autopilot_id = $1`, e.ap.ID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue q JOIN autopilot_run r ON r.id = q.autopilot_run_id WHERE r.autopilot_id = $1`, e.ap.ID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	return runs, tasks
}

type employeeWebhookFrozenColumns struct {
	Digest         string
	SecretRevision string
	Binding        WebhookEndpointBinding
}

func frozenColumns(t *testing.T, deliveryID string) employeeWebhookFrozenColumns {
	t.Helper()
	var digest, revision pgtype.Text
	var binding []byte
	if err := testPool.QueryRow(context.Background(), `SELECT source_digest, signing_secret_revision, source_binding FROM webhook_delivery WHERE id = $1`, deliveryID).
		Scan(&digest, &revision, &binding); err != nil {
		t.Fatalf("frozen columns: %v", err)
	}
	out := employeeWebhookFrozenColumns{Digest: digest.String, SecretRevision: revision.String}
	if len(binding) > 0 {
		if err := json.Unmarshal(binding, &out.Binding); err != nil {
			t.Fatalf("binding: %v", err)
		}
	}
	return out
}

func decodeWebhookResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %d response %q: %v", rec.Code, rec.Body.String(), err)
	}
	return resp
}

func requireWebhookStatus(t *testing.T, rec *httptest.ResponseRecorder, code int, status string) map[string]any {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("HTTP %d, want %d: %s", rec.Code, code, rec.Body.String())
	}
	resp := decodeWebhookResponse(t, rec)
	if status != "" && resp["status"] != status {
		t.Fatalf("status %v, want %s: %s", resp["status"], status, rec.Body.String())
	}
	return resp
}

// A signature is an HMAC of the exact bytes received: whitespace, a BOM and
// key order are part of what was signed, and a signature over the
// normalized JSON is not a signature of the request. The accepted delivery
// records which secret revision verified it, never the secret itself.
func TestEmployeeWebhookValidSignatureOverRawBytes(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	secret := "f1-secret-" + uuid.NewString()
	e.setSecret(t, secret)

	raw := []byte("\xEF\xBB\xBF  {\"z\":1,  \"event\":\"deploy.finished\",\"eventPayload\":{\"b\":2,\"a\":\"x\"}}\n")
	accepted := requireWebhookStatus(t, e.post(t, raw, map[string]string{"X-Hub-Signature-256": signBody(secret, raw)}), http.StatusOK, "accepted")
	deliveryID := accepted["delivery_id"].(string)
	delivery := e.process(t, deliveryID)
	if delivery.SignatureStatus != sigStatusValid || delivery.Status != deliveryStatusDispatched {
		t.Fatalf("delivery = %s/%s", delivery.SignatureStatus, delivery.Status)
	}
	frozen := frozenColumns(t, deliveryID)
	if frozen.SecretRevision == "" || strings.Contains(frozen.SecretRevision, secret) || frozen.Binding.SignaturePolicy != webhookSignatureHubSHA256 || frozen.Binding.SecretRevision != frozen.SecretRevision {
		t.Fatalf("secret revision = %q binding = %+v", frozen.SecretRevision, frozen.Binding)
	}

	normalized, _ := json.Marshal(map[string]any{"event": "deploy.finished", "eventPayload": map[string]any{"a": "x", "b": 2}})
	rejected := requireWebhookStatus(t, e.post(t, raw, map[string]string{"X-Hub-Signature-256": signBody(secret, normalized)}), http.StatusUnauthorized, "rejected")
	if rejected["reason"] != "invalid_signature" {
		t.Fatalf("rejected = %v", rejected)
	}

	// Rotating the secret changes the recorded revision of later deliveries.
	rotated := "f1-rotated-" + uuid.NewString()
	e.setSecret(t, rotated)
	again := requireWebhookStatus(t, e.post(t, raw, map[string]string{"X-Hub-Signature-256": signBody(rotated, raw)}), http.StatusOK, "accepted")
	if rev := frozenColumns(t, again["delivery_id"].(string)).SecretRevision; rev == "" || rev == frozen.SecretRevision {
		t.Fatalf("rotated revision %q, first %q", rev, frozen.SecretRevision)
	}
}

// A missing or wrong signature is decided before event identity: an
// unauthenticated request reusing a known event id is rejected, never
// answered as duplicate (which would leak the run) or conflict, and it does
// not touch the accepted delivery.
func TestEmployeeWebhookMissingOrWrongSignatureNeverDedupes(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	secret := "f1-secret-" + uuid.NewString()
	e.setSecret(t, secret)
	body := []byte(`{"event":"deploy.finished","eventPayload":{"build":41}}`)
	key := map[string]string{"Idempotency-Key": "evt-" + uuid.NewString()}

	signed := map[string]string{"X-Hub-Signature-256": signBody(secret, body)}
	for k, v := range key {
		signed[k] = v
	}
	first := requireWebhookStatus(t, e.post(t, body, signed), http.StatusOK, "accepted")

	missing := requireWebhookStatus(t, e.post(t, body, key), http.StatusUnauthorized, "rejected")
	if missing["reason"] != "missing_signature" || missing["run_id"] != nil {
		t.Fatalf("missing signature = %v", missing)
	}
	other := []byte(`{"event":"deploy.finished","eventPayload":{"build":42}}`)
	wrong := map[string]string{"X-Hub-Signature-256": signBody("not-the-secret-"+uuid.NewString(), other)}
	for k, v := range key {
		wrong[k] = v
	}
	invalid := requireWebhookStatus(t, e.post(t, other, wrong), http.StatusUnauthorized, "rejected")
	if invalid["reason"] != "invalid_signature" || invalid["run_id"] != nil {
		t.Fatalf("wrong signature = %v", invalid)
	}

	original, err := e.f.h.Queries.GetWebhookDelivery(context.Background(), parseUUID(first["delivery_id"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	if original.AttemptCount != 1 {
		t.Fatalf("unauthenticated retries bumped the accepted delivery: attempts=%d", original.AttemptCount)
	}
	if runs, _ := e.counts(t); runs != 1 {
		t.Fatalf("runs = %d, want 1", runs)
	}
}

// The supported signature protocols (GitHub and generic X-Hub-Signature-256)
// carry no timestamp, so no expiry is invented: an old event still verifies.
// Replay protection is the provider event id: a GitHub redelivery with the
// same X-GitHub-Delivery returns the first receipt and runs nothing new.
func TestEmployeeWebhookReplayUsesEventIDNotInventedExpiry(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	e.setProvider(t, "github")
	secret := "f1-secret-" + uuid.NewString()
	e.setSecret(t, secret)
	body := []byte(`{"action":"completed","timestamp":"2001-01-01T00:00:00Z","workflow_run":{"id":7}}`)
	headers := map[string]string{
		"X-GitHub-Event":      "workflow_run",
		"X-GitHub-Delivery":   uuid.NewString(),
		"X-Hub-Signature-256": signBody(secret, body),
	}
	first := requireWebhookStatus(t, e.post(t, body, headers), http.StatusOK, "accepted")
	e.process(t, first["delivery_id"].(string))

	replay := requireWebhookStatus(t, e.post(t, body, headers), http.StatusOK, "duplicate")
	if replay["delivery_id"] != first["delivery_id"] || replay["run_id"] != first["run_id"] {
		t.Fatalf("replay = %v, first = %v", replay, first)
	}
	if runs, tasks := e.counts(t); runs != 1 || tasks != 1 {
		t.Fatalf("replay ran again: runs=%d tasks=%d", runs, tasks)
	}
	frozen := frozenColumns(t, first["delivery_id"].(string))
	if frozen.Binding.Provider != "github" || frozen.Digest == "" {
		t.Fatalf("frozen = %+v", frozen)
	}
}

// Body limits: exactly the cap is accepted, one byte more is 413; corrupt
// JSON, a scalar body and an unusable event id are refused before anything
// is persisted.
func TestEmployeeWebhookBodyAndIdentityBoundaries(t *testing.T) {
	e := newEmployeeWebhookFixture(t)

	prefix, suffix := []byte(`{"pad":"`), []byte(`"}`)
	atCap := make([]byte, 0, maxWebhookBodyBytes)
	atCap = append(atCap, prefix...)
	atCap = append(atCap, bytes.Repeat([]byte("x"), maxWebhookBodyBytes-len(prefix)-len(suffix))...)
	atCap = append(atCap, suffix...)
	if len(atCap) != maxWebhookBodyBytes {
		t.Fatalf("fixture size %d", len(atCap))
	}
	requireWebhookStatus(t, e.post(t, atCap, nil), http.StatusOK, "accepted")

	over := append(append([]byte{}, atCap[:len(atCap)-2]...), []byte(`x"}`)...)
	if rec := e.post(t, over, nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over cap: %d %s", rec.Code, rec.Body.String())
	}
	for name, body := range map[string][]byte{"corrupt": []byte(`{"event":`), "scalar": []byte(`"hello"`), "empty": nil} {
		if rec := e.post(t, body, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	for name, key := range map[string]string{"too long": strings.Repeat("k", 256), "control": "evt\x01one"} {
		if rec := e.post(t, []byte(`{"event":"x"}`), map[string]string{"Idempotency-Key": key}); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s key: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if got := len(e.deliveries(t)); got != 1 {
		t.Fatalf("refused requests persisted deliveries: %d rows", got)
	}
}

// A wrong or rotated token finds no endpoint and leaves no trace.
func TestEmployeeWebhookWrongTokenLeavesNoTrace(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	if rec := e.postToken(t, webhookTokenPrefix+"definitely-not-a-token", []byte(`{"event":"x"}`), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", rec.Code)
	}
	old := e.token
	if _, err := e.f.h.rotateSceneRoutineWebhook(context.Background(), mustRoutineByAutopilot(t, e), routineMember()); err != nil {
		t.Fatal(err)
	}
	if rec := e.postToken(t, old, []byte(`{"event":"x"}`), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("rotated token: %d", rec.Code)
	}
	if got := len(e.deliveries(t)); got != 0 {
		t.Fatalf("deliveries = %d", got)
	}
}

func mustRoutineByAutopilot(t *testing.T, e *employeeWebhookFixture) contextcap.Routine {
	t.Helper()
	routine, err := contextcap.GetRoutineByAutopilot(context.Background(), testPool, uuidToString(e.ap.ID))
	if err != nil {
		t.Fatal(err)
	}
	return routine
}

// The endpoint's tenant binding is checked when the event is admitted: if
// the agent no longer serves the routine's org, the run is recorded as
// skipped and nothing runs anywhere else.
func TestEmployeeWebhookTenantBindingFailureRunsNowhere(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_dingtalk_identity SET org_id = 'org-elsewhere' WHERE agent_id = $1`, e.a.ID); err != nil {
		t.Fatal(err)
	}
	skipped := requireWebhookStatus(t, e.post(t, []byte(`{"event":"deploy.finished","org":"org-elsewhere"}`), nil), http.StatusOK, "skipped")
	if reason, _ := skipped["reason"].(string); !strings.Contains(reason, "no longer bound") {
		t.Fatalf("skipped = %v", skipped)
	}
	e.process(t, skipped["delivery_id"].(string))
	if runs, tasks := e.counts(t); runs != 1 || tasks != 0 {
		t.Fatalf("runs=%d tasks=%d", runs, tasks)
	}
}

// Actor, org, scene, agent and mode in the body are data: the run goes to the
// routine's scene and agent recorded on the endpoint, and the frozen binding
// names that route.
func TestEmployeeWebhookPayloadCannotChooseRoute(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	forged := fmt.Sprintf(`{"event":"deploy.finished","eventPayload":{"scene_id":%q,"agent_id":%q,"org_id":"org-forged","workspace_id":%q,"actor":{"staff_id":"boss"},"mode":"employee"}}`,
		ctxcapOtherScene, uuid.NewString(), uuid.NewString())
	accepted := requireWebhookStatus(t, e.post(t, []byte(forged), nil), http.StatusOK, "accepted")
	deliveryID := accepted["delivery_id"].(string)
	e.process(t, deliveryID)

	run, err := e.f.h.Queries.GetAutopilotRun(context.Background(), parseUUID(accepted["run_id"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	var runtime map[string]json.RawMessage
	_ = json.Unmarshal(run.RuntimeContext, &runtime)
	if !strings.Contains(string(runtime[protocol.AgentSceneContextKey]), ctxcapScene) || strings.Contains(string(run.RuntimeContext), ctxcapOtherScene) {
		t.Fatalf("runtime context = %s", run.RuntimeContext)
	}
	task, err := e.f.h.Queries.GetAgentTask(context.Background(), run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if uuidToString(task.AgentID) != e.a.ID || strings.Contains(string(task.Context), ctxcapOtherScene) {
		t.Fatalf("task agent=%s context=%s", uuidToString(task.AgentID), task.Context)
	}
	b := frozenColumns(t, deliveryID).Binding
	if b.SceneID != ctxcapScene || b.TenantOrgID != ctxcapOrg || b.AssigneeID != e.a.ID || b.WorkspaceID != testWorkspaceID ||
		b.SceneKind != "group" || b.RoutineID == "" || b.CreatorType != "member" || b.Dispatch != webhookDispatchAutopilotRunOnly {
		t.Fatalf("binding = %+v", b)
	}
}

// Same event id and same effective payload (key order and whitespace do not
// matter) is the same receipt: same delivery, same run, one task.
func TestEmployeeWebhookSameIDSameContentSameReceipt(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	key := map[string]string{"Idempotency-Key": "evt-" + uuid.NewString()}
	first := requireWebhookStatus(t, e.post(t, []byte(`{"event":"deploy.finished","eventPayload":{"a":1,"b":[1,2]}}`), key), http.StatusOK, "accepted")
	e.process(t, first["delivery_id"].(string))
	again := requireWebhookStatus(t, e.post(t, []byte("{ \"eventPayload\": {\"b\":[1,2], \"a\":1},\n \"event\":\"deploy.finished\" }"), key), http.StatusOK, "duplicate")
	if again["delivery_id"] != first["delivery_id"] || again["run_id"] != first["run_id"] {
		t.Fatalf("duplicate = %v first = %v", again, first)
	}
	if runs, tasks := e.counts(t); runs != 1 || tasks != 1 {
		t.Fatalf("runs=%d tasks=%d", runs, tasks)
	}
	ds := e.deliveries(t)
	if len(ds) != 1 || ds[0].AttemptCount != 2 {
		t.Fatalf("deliveries = %d attempts = %d", len(ds), ds[0].AttemptCount)
	}
}

// Same event id with a different effective payload is a conflict: 409, the
// accepted delivery and its run stay as they were, and the refused attempt
// is kept as a rejected delivery for the operator.
func TestEmployeeWebhookSameIDChangedContentConflicts(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	key := map[string]string{"Idempotency-Key": "evt-" + uuid.NewString()}
	first := requireWebhookStatus(t, e.post(t, []byte(`{"event":"deploy.finished","eventPayload":{"build":1}}`), key), http.StatusOK, "accepted")

	conflict := requireWebhookStatus(t, e.post(t, []byte(`{"event":"deploy.finished","eventPayload":{"build":2}}`), key), http.StatusConflict, "conflict")
	if conflict["reason"] != "event_id_conflict" || conflict["run_id"] != nil || conflict["delivery_id"] == first["delivery_id"] {
		t.Fatalf("conflict = %v", conflict)
	}
	// A different event name under the same id is a different payload too.
	requireWebhookStatus(t, e.post(t, []byte(`{"event":"deploy.started","eventPayload":{"build":1}}`), key), http.StatusConflict, "conflict")

	e.process(t, first["delivery_id"].(string))
	if runs, tasks := e.counts(t); runs != 1 || tasks != 1 {
		t.Fatalf("runs=%d tasks=%d", runs, tasks)
	}
	original, err := e.f.h.Queries.GetWebhookDelivery(context.Background(), parseUUID(first["delivery_id"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	if original.AttemptCount != 1 || original.Status != deliveryStatusDispatched {
		t.Fatalf("original = attempts %d status %s", original.AttemptCount, original.Status)
	}
	audit, err := e.f.h.Queries.GetWebhookDelivery(context.Background(), parseUUID(conflict["delivery_id"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	if audit.Status != deliveryStatusRejected || audit.Error.String != "event_id_conflict" || audit.AutopilotRunID.Valid || audit.ResponseStatus.Int32 != http.StatusConflict {
		t.Fatalf("audit = %s %q run=%v http=%d", audit.Status, audit.Error.String, audit.AutopilotRunID.Valid, audit.ResponseStatus.Int32)
	}
}

// A new event id about the same resource is new work, and without any event
// id every request is its own event (the per_request policy): identical
// bodies are not swallowed by a content hash.
func TestEmployeeWebhookNewIDSameResourceAccepted(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	body := []byte(`{"event":"deploy.finished","eventPayload":{"service":"api","build":9}}`)
	a := requireWebhookStatus(t, e.post(t, body, map[string]string{"Idempotency-Key": "evt-" + uuid.NewString()}), http.StatusOK, "accepted")
	b := requireWebhookStatus(t, e.post(t, body, map[string]string{"Idempotency-Key": "evt-" + uuid.NewString()}), http.StatusOK, "accepted")
	c := requireWebhookStatus(t, e.post(t, body, nil), http.StatusOK, "accepted")
	d := requireWebhookStatus(t, e.post(t, body, nil), http.StatusOK, "accepted")
	seen := map[any]bool{}
	for _, r := range []map[string]any{a, b, c, d} {
		if seen[r["run_id"]] {
			t.Fatalf("run reused: %v", r)
		}
		seen[r["run_id"]] = true
		e.process(t, r["delivery_id"].(string))
	}
	if runs, tasks := e.counts(t); runs != 4 || tasks != 4 {
		t.Fatalf("runs=%d tasks=%d", runs, tasks)
	}
	ctx := context.Background()
	for _, r := range []map[string]any{a, c} {
		d, err := e.f.h.Queries.GetWebhookDelivery(ctx, parseUUID(r["delivery_id"].(string)))
		if err != nil {
			t.Fatal(err)
		}
		src, err := e.f.h.loadWebhookFrozenSource(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		want := webhookIdentityProviderEventID
		if r["delivery_id"] == c["delivery_id"] {
			want = webhookIdentityPerRequest
		}
		if src.IdentityPolicy != want || (want == webhookIdentityPerRequest) != (src.EventID == "") {
			t.Fatalf("identity = %s %q", src.IdentityPolicy, src.EventID)
		}
	}
}

// A disabled (paused) endpoint accepts nothing new, and a delivery that was
// persisted but never admitted before the pause runs nothing either.
func TestEmployeeWebhookDisabledEndpointCreatesNoTask(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	ctx := context.Background()

	// A delivery persisted in the pre-admission crash window.
	failAdmission := installAutopilotRunInsertFailure(t, e.ap.ID)
	if rec := e.post(t, []byte(`{"event":"deploy.finished"}`), nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("forced admission failure: %d %s", rec.Code, rec.Body.String())
	}
	failAdmission()
	pending := e.deliveries(t)
	if len(pending) != 1 || pending[0].Status != deliveryStatusQueued {
		t.Fatalf("pending = %+v", pending)
	}

	paused := false
	if _, err := e.f.h.updateSceneRoutine(ctx, e.a, mustRoutineByAutopilot(t, e), routineMember(), sceneRoutinePatch{Enabled: &paused}); err != nil {
		t.Fatal(err)
	}
	ignored := requireWebhookStatus(t, e.post(t, []byte(`{"event":"deploy.finished"}`), nil), http.StatusOK, "ignored")
	if ignored["reason"] != "autopilot_paused" || ignored["run_id"] != nil {
		t.Fatalf("ignored = %v", ignored)
	}
	recovered := e.process(t, uuidToString(pending[0].ID))
	if recovered.Status != deliveryStatusIgnored || recovered.AutopilotRunID.Valid {
		t.Fatalf("recovered = %s run=%v", recovered.Status, recovered.AutopilotRunID.Valid)
	}
	if runs, tasks := e.counts(t); runs != 0 || tasks != 0 {
		t.Fatalf("runs=%d tasks=%d", runs, tasks)
	}
}

// installAutopilotRunInsertFailure makes run admission for one autopilot
// fail, holding a delivery in the pre-admission crash window. The returned
// func removes it.
func installAutopilotRunInsertFailure(t *testing.T, autopilotID pgtype.UUID) func() {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	fn, trg := "f1_fail_run_fn_"+suffix, "f1_fail_run_"+suffix
	drop := func() {
		testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON autopilot_run`, trg))
		testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, fn))
	}
	t.Cleanup(drop)
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.autopilot_id = '%s'::uuid THEN
		RAISE EXCEPTION 'forced admission failure';
	END IF;
	RETURN NEW;
END;
$$;`, fn, uuidToString(autopilotID))); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON autopilot_run FOR EACH ROW EXECUTE FUNCTION %s()`, trg, fn)); err != nil {
		t.Fatal(err)
	}
	return drop
}

// A delivery's identity, payload and route are fixed by its first
// acceptance. The ingress envelope uses the delivery row's received_at, a
// worker retry rebuilds the identical bytes later, a rebuild that would
// differ fails closed, and a route that changed in the crash window runs
// nowhere instead of somewhere new.
func TestEmployeeWebhookRetryKeepsFirstReceivedAtPayloadAndRoute(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	ctx := context.Background()

	t.Run("accepted envelope is the frozen source", func(t *testing.T) {
		accepted := requireWebhookStatus(t, e.post(t, []byte(`{"event":"deploy.finished","eventPayload":{"n":1}}`), nil), http.StatusOK, "accepted")
		d, err := e.f.h.Queries.GetWebhookDelivery(ctx, parseUUID(accepted["delivery_id"].(string)))
		if err != nil {
			t.Fatal(err)
		}
		run, err := e.f.h.Queries.GetAutopilotRun(ctx, parseUUID(accepted["run_id"].(string)))
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond)
		src, err := e.f.h.loadWebhookFrozenSource(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		// trigger_payload is JSONB, so compare canonical forms.
		rebuilt, err := canonicalWebhookJSON(src.Payload)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := canonicalWebhookJSON(run.TriggerPayload)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(rebuilt, stored) {
			t.Fatalf("rebuilt source differs from admitted run payload:\n%s\n%s", rebuilt, stored)
		}
		if !src.ReceivedAt.Equal(d.ReceivedAt.Time) || !strings.Contains(string(run.TriggerPayload), d.ReceivedAt.Time.UTC().Format(time.RFC3339)) {
			t.Fatalf("receivedAt %v / payload %s / delivery %v", src.ReceivedAt, run.TriggerPayload, d.ReceivedAt.Time)
		}
		if src.Digest == "" || src.Digest != frozenColumns(t, uuidToString(d.ID)).Digest {
			t.Fatalf("digest %q", src.Digest)
		}
		e.process(t, uuidToString(d.ID))
	})

	t.Run("crash window retry keeps first receivedAt and route", func(t *testing.T) {
		restore := installAutopilotRunInsertFailure(t, e.ap.ID)
		rec := e.post(t, []byte(`{"event":"deploy.finished","eventPayload":{"n":2}}`), map[string]string{"Idempotency-Key": "evt-" + uuid.NewString()})
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("forced admission failure: %d", rec.Code)
		}
		pending := latestDelivery(t, e)
		// The first worker attempt fails too and is retried later.
		e.processOneAttempt(t, pending.ID)
		restore()
		time.Sleep(1100 * time.Millisecond)
		if _, err := testPool.Exec(ctx, `UPDATE webhook_delivery SET available_at = now() WHERE id = $1`, pending.ID); err != nil {
			t.Fatal(err)
		}
		done := e.process(t, uuidToString(pending.ID))
		if done.Status != deliveryStatusDispatched || done.DispatchAttempts < 2 {
			t.Fatalf("done = %s attempts=%d err=%q", done.Status, done.DispatchAttempts, done.Error.String)
		}
		run, err := e.f.h.Queries.GetAutopilotRun(ctx, done.AutopilotRunID)
		if err != nil {
			t.Fatal(err)
		}
		var env WebhookEnvelope
		if err := json.Unmarshal(run.TriggerPayload, &env); err != nil {
			t.Fatal(err)
		}
		if env.Request.ReceivedAt != pending.ReceivedAt.Time.UTC().Format(time.RFC3339) {
			t.Fatalf("receivedAt %s, first %s", env.Request.ReceivedAt, pending.ReceivedAt.Time.UTC().Format(time.RFC3339))
		}
		if !strings.Contains(string(run.RuntimeContext), ctxcapScene) {
			t.Fatalf("route = %s", run.RuntimeContext)
		}
	})

	t.Run("a rebuild that differs fails closed", func(t *testing.T) {
		restore := installAutopilotRunInsertFailure(t, e.ap.ID)
		if rec := e.post(t, []byte(`{"event":"deploy.finished","eventPayload":{"n":3}}`), nil); rec.Code != http.StatusInternalServerError {
			t.Fatalf("forced admission failure: %d", rec.Code)
		}
		restore()
		pending := latestDelivery(t, e)
		// A normalizer that would now read the body differently (simulated by
		// a changed stored body) must not run a different payload.
		if _, err := testPool.Exec(ctx, `UPDATE webhook_delivery SET raw_body = convert_to('{"event":"deploy.finished","eventPayload":{"n":33}}', 'UTF8') WHERE id = $1`, pending.ID); err != nil {
			t.Fatal(err)
		}
		done := e.process(t, uuidToString(pending.ID))
		if done.Status != deliveryStatusFailed || done.AutopilotRunID.Valid || done.Error.String != "source_digest_mismatch" {
			t.Fatalf("drift = %s run=%v err=%q", done.Status, done.AutopilotRunID.Valid, done.Error.String)
		}
	})

	t.Run("a changed route runs nowhere", func(t *testing.T) {
		restore := installAutopilotRunInsertFailure(t, e.ap.ID)
		if rec := e.post(t, []byte(`{"event":"deploy.finished","eventPayload":{"n":4}}`), nil); rec.Code != http.StatusInternalServerError {
			t.Fatalf("forced admission failure: %d", rec.Code)
		}
		restore()
		pending := latestDelivery(t, e)
		if _, err := testPool.Exec(ctx, `UPDATE context_scope_routine SET scene_id = $1 WHERE autopilot_id = $2`, ctxcapOtherScene, e.ap.ID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `UPDATE context_scope_routine SET scene_id = $1 WHERE autopilot_id = $2`, ctxcapScene, e.ap.ID)
		})
		done := e.process(t, uuidToString(pending.ID))
		if done.Status != deliveryStatusIgnored || done.AutopilotRunID.Valid || done.Error.String != "binding_changed" {
			t.Fatalf("changed route = %s run=%v err=%q", done.Status, done.AutopilotRunID.Valid, done.Error.String)
		}
	})
}

func latestDelivery(t *testing.T, e *employeeWebhookFixture) db.WebhookDelivery {
	t.Helper()
	ds := e.deliveries(t)
	if len(ds) == 0 {
		t.Fatal("no delivery")
	}
	return ds[len(ds)-1]
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Logs carry ids and outcomes only: no body, signing secret or token on any
// path, including rejections, conflicts and worker failures.
func TestEmployeeWebhookLogsCarryNoBodySecretOrToken(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	sink := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	bodySentinel := "BODY-SENTINEL-" + uuid.NewString()
	secret := "SECRET-SENTINEL-" + uuid.NewString()
	e.setSecret(t, secret)
	body := []byte(fmt.Sprintf(`{"event":"deploy.finished","eventPayload":{"note":%q}}`, bodySentinel))
	changed := []byte(fmt.Sprintf(`{"event":"deploy.finished","eventPayload":{"note":%q,"v":2}}`, bodySentinel))
	key := "evt-" + uuid.NewString()
	sign := func(b []byte) map[string]string {
		return map[string]string{"X-Hub-Signature-256": signBody(secret, b), "Idempotency-Key": key}
	}

	accepted := requireWebhookStatus(t, e.post(t, body, sign(body)), http.StatusOK, "accepted")
	e.post(t, body, map[string]string{"X-Hub-Signature-256": signBody("wrong-secret-0123456789", body)})
	e.post(t, changed, sign(changed))
	e.post(t, body, sign(body))
	e.post(t, append([]byte(`{"x":"`+bodySentinel), bytes.Repeat([]byte("y"), maxWebhookBodyBytes)...), nil)
	e.post(t, []byte(`{"broken":"`+bodySentinel), nil)
	e.process(t, accepted["delivery_id"].(string))

	restore := installAutopilotRunInsertFailure(t, e.ap.ID)
	e.post(t, body, map[string]string{"X-Hub-Signature-256": signBody(secret, body)})
	restore()
	pending := latestDelivery(t, e)
	testPool.Exec(context.Background(), `UPDATE webhook_delivery SET raw_body = convert_to('{"event":"x","eventPayload":"`+bodySentinel+`"}', 'UTF8') WHERE id = $1`, pending.ID)
	e.process(t, uuidToString(pending.ID))

	logs := sink.String()
	for name, sentinel := range map[string]string{"body": bodySentinel, "secret": secret, "token": e.token, "event id": key} {
		if strings.Contains(logs, sentinel) {
			t.Fatalf("logs contain the %s:\n%s", name, logs)
		}
	}
	// The answers are still observable by id: accepted, rejected, conflict
	// and duplicate each leave one outcome line, and the worker refusal is
	// logged too.
	for _, want := range []string{
		"outcome=accepted delivery_id=" + accepted["delivery_id"].(string),
		"outcome=rejected", "reason=invalid_signature",
		"outcome=conflict", "reason=event_id_conflict",
		"outcome=duplicate delivery_id=" + accepted["delivery_id"].(string),
		"secret_revision=sha256:", "routine_id=", "scene_id=" + ctxcapScene,
		"stored delivery no longer rebuilds its accepted input",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs miss %q:\n%s", want, logs)
		}
	}
}

// Concurrent requests with one event id race on separate connections: one
// delivery is created and admitted; the same payload elsewhere is a
// duplicate of it, a different payload is a conflict, and one run exists.
func TestEmployeeWebhookConcurrentSameIDAdmitsOnce(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	key := map[string]string{"Idempotency-Key": "evt-" + uuid.NewString()}
	same := []byte(`{"event":"deploy.finished","eventPayload":{"build":5}}`)
	other := []byte(`{"event":"deploy.finished","eventPayload":{"build":6}}`)

	const n = 8
	codes := make([]int, n)
	statuses := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			body := same
			if i%2 == 1 {
				body = other
			}
			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/autopilots/"+e.token, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", key["Idempotency-Key"])
			req = withURLParam(req, "token", e.token)
			rec := httptest.NewRecorder()
			e.f.h.HandleAutopilotWebhook(rec, req)
			codes[i] = rec.Code
			var resp map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			statuses[i], _ = resp["status"].(string)
		}(i)
	}
	close(start)
	wg.Wait()

	accepted, duplicates, conflicts := 0, 0, 0
	for i := range codes {
		switch {
		case codes[i] == http.StatusOK && statuses[i] == "accepted":
			accepted++
		case codes[i] == http.StatusOK && statuses[i] == "duplicate":
			duplicates++
		case codes[i] == http.StatusConflict && statuses[i] == "conflict":
			conflicts++
		default:
			t.Fatalf("request %d: %d %q", i, codes[i], statuses[i])
		}
	}
	// Whichever payload wins, the other half conflicts and the rest of its
	// own half are duplicates.
	if accepted != 1 || conflicts != n/2 || duplicates != n/2-1 {
		t.Fatalf("accepted=%d duplicates=%d conflicts=%d", accepted, duplicates, conflicts)
	}
	var live int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM webhook_delivery WHERE trigger_id = $1 AND status <> 'rejected'`, e.trigger.ID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if runs, _ := e.counts(t); runs != 1 || live != 1 {
		t.Fatalf("runs=%d live deliveries=%d", runs, live)
	}
}

// A routine whose row disagrees with its autopilot (here the autopilot was
// reassigned to another agent) is an unusable endpoint: it accepts nothing
// and never runs the routine's scene on someone else.
func TestEmployeeWebhookInvalidBindingAcceptsNothing(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	other := createWebhookTestAgent(t, "F1 other agent "+uuid.NewString()[:8])
	if _, err := testPool.Exec(context.Background(), `UPDATE autopilot SET assignee_id = $1 WHERE id = $2`, other, e.ap.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE autopilot SET assignee_id = $1 WHERE id = $2`, e.a.ID, e.ap.ID)
	})
	ignored := requireWebhookStatus(t, e.post(t, []byte(`{"event":"deploy.finished"}`), nil), http.StatusOK, "ignored")
	if ignored["reason"] != webhookBindingInvalid || ignored["run_id"] != nil {
		t.Fatalf("ignored = %v", ignored)
	}
	if runs, tasks := e.counts(t); runs != 0 || tasks != 0 {
		t.Fatalf("runs=%d tasks=%d", runs, tasks)
	}
}

// An admitted run goes only to the target it was accepted for. Changing the
// ordinary autopilot's assignee or mode between acceptance and dispatch
// settles the run as skipped and ignores the delivery: nothing runs on agent
// B, nothing runs in the other mode.
func TestEmployeeWebhookAdmittedRunNeverReroutes(t *testing.T) {
	ctx := context.Background()
	for name, change := range map[string]func(t *testing.T, apID, other string){
		"assignee": func(t *testing.T, apID, other string) {
			if _, err := testPool.Exec(ctx, `UPDATE autopilot SET assignee_id = $1 WHERE id = $2`, other, apID); err != nil {
				t.Fatal(err)
			}
		},
		"execution mode": func(t *testing.T, apID, other string) {
			if _, err := testPool.Exec(ctx, `UPDATE autopilot SET execution_mode = 'create_issue' WHERE id = $1`, apID); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			agentA := createWebhookTestAgent(t, "F1 frozen A "+uuid.NewString()[:8])
			agentB := createWebhookTestAgent(t, "F1 frozen B "+uuid.NewString()[:8])
			apID := createWebhookTestAutopilot(t, agentA, "active", "run_only")
			trig := createWebhookTriggerViaHandler(t, apID)
			t.Cleanup(func() {
				testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE agent_id IN ($1, $2)`, agentA, agentB)
				testPool.Exec(context.Background(), `DELETE FROM issue WHERE origin_type = 'autopilot' AND origin_id IN (SELECT id FROM autopilot_run WHERE autopilot_id = $1)`, apID)
				testPool.Exec(context.Background(), `DELETE FROM autopilot_run WHERE autopilot_id = $1`, apID)
			})

			deliveryID := requireAcceptedWebhookResponse(t, postWebhook(t, *trig.WebhookToken, map[string]any{"event": "frozen.target"}, nil))
			change(t, apID, agentB)
			delivery := processQueuedWebhookDelivery(t, deliveryID)
			if delivery.Status != deliveryStatusIgnored || delivery.Error.String != webhookBindingChanged || !delivery.AutopilotRunID.Valid {
				t.Fatalf("delivery = %s %q run=%v", delivery.Status, delivery.Error.String, delivery.AutopilotRunID.Valid)
			}
			run, err := testHandler.Queries.GetAutopilotRun(ctx, delivery.AutopilotRunID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != "skipped" || run.TaskID.Valid || run.IssueID.Valid || !strings.Contains(run.FailureReason.String, webhookBindingChanged) {
				t.Fatalf("run = %s task=%v issue=%v reason=%q", run.Status, run.TaskID.Valid, run.IssueID.Valid, run.FailureReason.String)
			}
			var tasks int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE agent_id IN ($1, $2) OR autopilot_run_id = $3`, agentA, agentB, run.ID).Scan(&tasks); err != nil {
				t.Fatal(err)
			}
			if tasks != 0 {
				t.Fatalf("tasks = %d", tasks)
			}
		})
	}
}

// A routine run admitted before its agent left the routine's org fails the
// use-time tenant fence: skipped, never dispatched with the old scene.
func TestEmployeeWebhookAdmittedRoutineRunFencesTenant(t *testing.T) {
	e := newEmployeeWebhookFixture(t)
	accepted := requireWebhookStatus(t, e.post(t, []byte(`{"event":"deploy.finished"}`), nil), http.StatusOK, "accepted")
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_dingtalk_identity SET org_id = 'org-elsewhere' WHERE agent_id = $1`, e.a.ID); err != nil {
		t.Fatal(err)
	}
	delivery := e.process(t, accepted["delivery_id"].(string))
	if delivery.Status != deliveryStatusIgnored || delivery.Error.String != webhookSceneUnusable {
		t.Fatalf("delivery = %s %q", delivery.Status, delivery.Error.String)
	}
	if runs, tasks := e.counts(t); runs != 1 || tasks != 0 {
		t.Fatalf("runs=%d tasks=%d", runs, tasks)
	}
}
