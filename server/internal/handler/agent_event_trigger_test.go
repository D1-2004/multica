package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

func TestEventTriggerAgentConfiguration(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	old := testHandler.EventTriggers
	testHandler.EventTriggers = service.NewEventTriggerService(testPool, testHandler.AutopilotService)
	defer func() { testHandler.EventTriggers = old }()
	id := createHandlerTestAgent(t, "event-config", nil)
	for _, enabled := range []bool{true, false} {
		w := updateAgentForTest(t, id, map[string]any{"event_trigger_enabled": enabled})
		if w.Code != http.StatusOK {
			t.Fatalf("update: %d %s", w.Code, w.Body.String())
		}
		var response AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.EventTriggerEnabled != enabled {
			t.Fatal("toggle missing from update response")
		}
		w = httptest.NewRecorder()
		testHandler.ListAgents(w, newRequest(http.MethodGet, "/api/agents", nil))
		var agents []AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &agents); err != nil {
			t.Fatal(err)
		}
		for _, agent := range agents {
			if agent.ID == id && agent.EventTriggerEnabled != enabled {
				t.Fatal("list/detail toggle mismatch")
			}
		}
	}
	if w := updateAgentForTest(t, id, map[string]any{"event_trigger_enabled": "true"}); w.Code != http.StatusBadRequest {
		t.Fatal("malformed boolean accepted")
	}
}

func TestEventTriggerObservedCommandSelfLoopAndValidation(t *testing.T) {
	c := DispatchCommand{SchemaVersion: "2.0", AgentID: "agent", Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"}, Event: DispatchEvent{Domain: "channel", Type: "message.observed", Data: DispatchEventData{Conversation: DispatchConversation{OpenConversationID: "group", Type: "group"}, Sender: DispatchSender{UID: "123"}, Messages: []DispatchMessage{{OpenMsgID: "m1", Text: "self"}}}}, Surface: DispatchSurface{Type: "auto"}, Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"}, ExternalIdentity: AgentDispatchExternalIdentity{DWS: &AgentDispatchDWSIdentity{UID: "123", OrgID: "456"}}}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if !dispatchIsAgentSelfMessage(c) {
		t.Fatal("observed self message can loop")
	}
	c.Event.Data.Sender.UID = "789"
	if dispatchIsAgentSelfMessage(c) {
		t.Fatal("human message ignored")
	}
	c.ExternalIdentity.DWS = nil
	if err := c.validate(); err == nil {
		t.Fatal("missing event identity accepted")
	}
}

func TestEventTriggerDisabledHTTPDoesNotPersist(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	id := createHandlerTestAgent(t, "event-disabled", nil)
	agent, err := testHandler.Queries.GetAgent(context.Background(), parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	h := *testHandler
	h.EventTriggers = service.NewEventTriggerService(testPool, h.AutopilotService)
	c := DispatchCommand{AgentID: id, Source: DispatchSource{Type: "digital_employee"}, Event: DispatchEvent{Domain: "channel", Type: "message.observed"}}
	w := httptest.NewRecorder()
	if !h.handleObservedEvent(w, newRequest(http.MethodPost, "/dispatch", nil), &c, agentDispatchContext{AgentID: agent.ID, WorkspaceID: agent.WorkspaceID}) {
		t.Fatal("observed event fell through to coordinator")
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("disabled acknowledgment=%d", w.Code)
	}
	var count int
	if err = testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_event_stream WHERE agent_id=$1`, agent.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("disabled inbox count=%d %v", count, err)
	}
}

func TestProactiveConversationDoesNotCreateAutopilot(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	old := testHandler.EventTriggers
	testHandler.EventTriggers = service.NewEventTriggerService(testPool, testHandler.AutopilotService)
	defer func() { testHandler.EventTriggers = old }()
	id := createHandlerTestAgent(t, "proactive-config", nil)
	for _, body := range []map[string]any{{"inbound_coordinator": false}, {"event_trigger_enabled": true}, {"event_trigger_enabled": false}, {"event_trigger_enabled": true}, {"inbound_coordinator": false}} {
		w := updateAgentForTest(t, id, body)
		if w.Code != 200 {
			t.Fatalf("update=%d %s", w.Code, w.Body.String())
		}
		var a AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		if a.EventTriggerEnabled && !a.InboundCoordinator {
			t.Fatal("proactive processing without coordinator")
		}
		if on, ok := body["event_trigger_enabled"]; ok && on == true && (!a.EventTriggerEnabled || !a.InboundCoordinator) {
			t.Fatal("enabling proactive did not enable judging")
		}
	}
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM autopilot WHERE assignee_id=$1`, parseUUID(id)).Scan(&n); err != nil || n != 0 {
		t.Fatalf("created autopilots=%d err=%v", n, err)
	}
}
