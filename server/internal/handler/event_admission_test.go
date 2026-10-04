package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/eventrouter"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
)

func TestProviderEventPayloadAndCategory(t *testing.T) {
	c := DispatchCommand{Event: DispatchEvent{Domain: "channel", Type: "message.created"}}
	raw := []byte(`{"externalIdentity":{"dws":{"token":"do-not-store"}},"completionCallback":{"telemetryToken":"secret"},"event":{"domain":"channel","type":"message.created","new_field":{"keep":true}}}`)
	e, err := dispatchProviderEvent(context.Background(), raw, c, "verified/source", "id1")
	if err != nil || e.Category != eventrouter.UserMessage || !strings.Contains(string(e.Payload), "new_field") || strings.Contains(string(e.Payload), "secret") || strings.Contains(string(e.Payload), "do-not-store") {
		t.Fatalf("unsafe projection: %+v %v", e, err)
	}
	native := dwsevents.Event{ID: "native-id", Key: "user_im_message_receive_at", Data: json.RawMessage(`{"native_field":true}`)}
	e, err = dispatchProviderEvent(withNativeEvent(context.Background(), native), raw, c, "native/source", "dispatch-key")
	if err != nil || e.ID != native.ID || e.Type != native.Key || !strings.Contains(string(e.Payload), "native_field") {
		t.Fatalf("native data lost: %+v %v", e, err)
	}
	c.Event.Type = "emotionReply"
	if dispatchEventCategory(c) != eventrouter.Observation {
		t.Fatal("reaction became human authorization")
	}
	c.Event.Domain = "calendar"
	c.Event.Type = "calendar.started"
	if dispatchEventCategory(c) != eventrouter.Observation {
		t.Fatal("calendar became human authorization")
	}
	c.Control = &DispatchControl{Action: "cancel"}
	if dispatchEventCategory(c) != eventrouter.Control {
		t.Fatal("control became message")
	}
	c.Event.Domain = "channel"
	c.Event.Type = "message.created"
	c.Control.Action = "dispatch"
	if dispatchEventCategory(c) != eventrouter.UserMessage {
		t.Fatal("Router dispatch directive hid the user-message category")
	}
	if eventPayloadFingerprint("base", []byte(`{"a":1,"b":2}`)) != eventPayloadFingerprint("base", []byte(`{ "b":2,"a":1 }`)) {
		t.Fatal("JSON formatting caused conflict")
	}
	if eventPayloadFingerprint("base", []byte(`{"a":1}`)) == eventPayloadFingerprint("base", []byte(`{"a":2}`)) {
		t.Fatal("changed payload did not change fingerprint")
	}
}

func TestEventAdmissionHeldBeforeHandlerDatabase(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "held event")
	f.h.EventRouteConfig = func(string, string, string) (string, string) { return eventrouter.Unified, "test-v1" }
	f.h.TaskCompletionTargetIdentity = testRouterTargetIdentity
	raw := []byte(strings.Replace(string(responseHTTPPayload(uuidToString(f.agent.ID), f.baseKey, "")), `"type":"group"`, `"type":"unrecognized"`, 1))
	request := httptest.NewRequest(http.MethodPost, "/dispatch", nil)
	request.Header.Set("Idempotency-Key", f.baseKey)
	w := httptest.NewRecorder()
	f.h.handleAgentDispatchV2(w, request, raw, f.dc)
	if w.Code != http.StatusAccepted {
		t.Fatalf("unmapped entered handler: %d %s", w.Code, w.Body.String())
	}
	row, err := f.h.Queries.GetSceneEventReceipt(context.Background(), db.GetSceneEventReceiptParams{WorkspaceID: f.dc.WorkspaceID, AgentID: f.dc.AgentID, Source: "messagerouter/" + uuidToString(f.dc.EndpointNamespaceID), SourceEventID: f.baseKey})
	if err != nil || row.State != eventrouter.Unmapped || row.PrincipalID != f.dc.UserID {
		t.Fatalf("receipt: %+v %v", row, err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM scene_event_receipt WHERE agent_id=$1`, f.dc.AgentID)
	})
	f.h.EventRouteConfig = func(string, string, string) (string, string) { return eventrouter.Legacy, "test-v2" }
	// Retry the exact original envelope: a canary change never releases it.
	w = httptest.NewRecorder()
	f.h.handleAgentDispatchV2(w, request, raw, f.dc)
	if w.Code != http.StatusAccepted {
		t.Fatalf("held receipt switched route: %d", w.Code)
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, f.agent.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("held event started tasks: %d %v", count, err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM inbound_coordinator_job WHERE agent_id=$1`, f.agent.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("held event added a job beside the existing fixture job: %d %v", count, err)
	}
}

func TestEventAdmissionSceneRefAndReplicaFenceDatabase(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "known scene")
	f.h.EventRouteConfig = func(string, string, string) (string, string) { return eventrouter.Unified, "snapshot-v1" }
	f.h.EventRouteReady = func(context.Context) (bool, error) { return false, nil }
	f.command.AgentScene = nil
	raw, _ := json.Marshal(f.command)
	req := httptest.NewRequest(http.MethodPost, "/dispatch", nil)
	req.Header.Set("Idempotency-Key", f.baseKey)
	w := httptest.NewRecorder()
	if !f.h.admitDispatchEvent(w, req, raw, &f.command, f.dc) || w.Code != http.StatusServiceUnavailable {
		t.Fatal("mixed replicas accepted")
	}
	f.h.EventRouteReady = func(context.Context) (bool, error) { return true, nil }
	w = httptest.NewRecorder()
	if !f.h.admitDispatchEvent(w, req, raw, &f.command, f.dc) || w.Code != http.StatusAccepted || f.command.AgentScene == nil || f.command.AgentScene.SceneID != uuidToString(f.scene.ID) || f.command.EventReceiptID == "" {
		t.Fatal("resolved scene did not reach entry")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM scene_event_receipt WHERE agent_id=$1`, f.agent.ID)
	})
	encoded, _ := json.Marshal(f.command)
	var restored DispatchCommand
	if err := json.Unmarshal(encoded, &restored); err != nil || restored.EventReceiptID != f.command.EventReceiptID || restored.AgentScene.SceneID != f.command.AgentScene.SceneID {
		t.Fatal("restart lost receipt/ref")
	}
}
