package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestUpdateAgentCoordinationRejectsInvalidMode(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	id := createHandlerTestAgent(t, "coordination-invalid", nil)
	for _, mode := range []any{"future", "", nil, 1, true} {
		response := updateAgentForTest(t, id, map[string]any{"coordination_mode": mode})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("mode %v: %d %s", mode, response.Code, response.Body.String())
		}
	}
}

func TestUpdateAgentCoordinationReadinessAndLegacyWrites(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	id := createHandlerTestAgent(t, "coordination-readiness", nil)
	previous := testHandler.EmployeeLoopReady
	t.Cleanup(func() { testHandler.EmployeeLoopReady = previous })
	testHandler.EmployeeLoopReady = nil
	response := updateAgentForTest(t, id, map[string]any{"coordination_mode": "employee", "inbound_coordinator": true})
	if response.Code != http.StatusConflict {
		t.Fatalf("unready mode accepted: %d %s", response.Code, response.Body.String())
	}
	testHandler.EmployeeLoopReady = func(_ context.Context, workspace, agent pgtype.UUID) error {
		if uuidToString(workspace) != testWorkspaceID || uuidToString(agent) != id {
			return errors.New("wrong scope")
		}
		return nil
	}
	response = updateAgentForTest(t, id, map[string]any{"coordination_mode": "employee"})
	if response.Code != http.StatusOK {
		t.Fatalf("ready mode: %d %s", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["coordination_mode"] != "employee" || result["inbound_coordinator"] != false || result["employee_loop_ready"] != true {
		t.Fatalf("mode switch enabled old disabled agent: %v", result)
	}
	for _, list := range []bool{false, true} {
		read := httptest.NewRecorder()
		if list {
			testHandler.ListAgents(read, newRequest(http.MethodGet, "/api/agents", nil))
		} else {
			testHandler.GetAgent(read, withURLParam(newRequest(http.MethodGet, "/api/agents/"+id, nil), "id", id))
		}
		if read.Code != http.StatusOK {
			t.Fatalf("read mode: %d %s", read.Code, read.Body.String())
		}
		var records []AgentResponse
		if list {
			if err := json.Unmarshal(read.Body.Bytes(), &records); err != nil {
				t.Fatal(err)
			}
		} else {
			var record AgentResponse
			if err := json.Unmarshal(read.Body.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			records = []AgentResponse{record}
		}
		found := false
		for _, record := range records {
			if record.ID == id {
				found = true
				if record.CoordinationMode != "employee" || !record.EmployeeLoopReady || record.InboundCoordinator {
					t.Fatalf("read mode drift: %+v", record)
				}
			}
		}
		if !found {
			t.Fatal("agent missing from response")
		}
	}
	for _, enabled := range []bool{true, false} {
		response = updateAgentForTest(t, id, map[string]any{"inbound_coordinator": enabled})
		if response.Code != http.StatusOK {
			t.Fatalf("legacy enabled write: %d %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result["coordination_mode"] != "employee" || result["inbound_coordinator"] != enabled {
			t.Fatalf("legacy write changed mode: %v", result)
		}
	}
	testHandler.EmployeeLoopReady = nil
	response = updateAgentForTest(t, id, map[string]any{"inbound_coordinator": true})
	if response.Code != http.StatusConflict {
		t.Fatalf("unready old enable accepted: %d %s", response.Code, response.Body.String())
	}
	response = updateAgentForTest(t, id, map[string]any{"coordination_mode": "coordinator"})
	if response.Code != http.StatusOK {
		t.Fatalf("rollback mode: %d %s", response.Code, response.Body.String())
	}
}

func TestUpdateAgentCoordinationProactiveSwitchPreservesMode(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	id := createHandlerTestAgent(t, "coordination-proactive", nil)
	previousReady, previousEvents := testHandler.EmployeeLoopReady, testHandler.EventTriggers
	t.Cleanup(func() { testHandler.EmployeeLoopReady = previousReady; testHandler.EventTriggers = previousEvents })
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_event_trigger WHERE agent_id=$1`, id)
	})
	testHandler.EmployeeLoopReady = func(context.Context, pgtype.UUID, pgtype.UUID) error { return nil }
	testHandler.EventTriggers = service.NewEventTriggerService(testPool, testHandler.AutopilotService)
	response := updateAgentForTest(t, id, map[string]any{"coordination_mode": "employee"})
	if response.Code != http.StatusOK {
		t.Fatalf("set mode: %d %s", response.Code, response.Body.String())
	}
	response = updateAgentForTest(t, id, map[string]any{"event_trigger_enabled": true})
	if response.Code != http.StatusOK {
		t.Fatalf("enable proactive: %d %s", response.Code, response.Body.String())
	}
	var result AgentResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.CoordinationMode != "employee" || !result.EventTriggerEnabled || !result.InboundCoordinator {
		t.Fatalf("proactive switch changed mode: %+v", result)
	}
	testHandler.EmployeeLoopReady = nil
	response = updateAgentForTest(t, id, map[string]any{"event_trigger_enabled": false})
	if response.Code != http.StatusOK {
		t.Fatalf("disable without readiness: %d %s", response.Code, response.Body.String())
	}
	response = updateAgentForTest(t, id, map[string]any{"event_trigger_enabled": true})
	if response.Code != http.StatusConflict {
		t.Fatalf("proactive bypassed readiness: %d %s", response.Code, response.Body.String())
	}
}

func TestUpdateAgentCoordinationRequiresManageAccess(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	id, _, member := privateAgentTestFixture(t)
	response := httptest.NewRecorder()
	request := withURLParam(newRequestAs(member, http.MethodPut, "/api/agents/"+id, map[string]any{"coordination_mode": "employee"}), "id", id)
	testHandler.UpdateAgent(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unauthorized mode edit: %d %s", response.Code, response.Body.String())
	}
}
