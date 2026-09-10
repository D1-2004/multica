package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

func TestMessageAutomationTriggerAPI(t *testing.T) {
	old := testHandler.MessageAutomations
	testHandler.MessageAutomations = &service.MessageAutomationService{Pool: testPool, Autopilot: testHandler.AutopilotService}
	defer func() { testHandler.MessageAutomations = old }()
	agentID := createWebhookTestAgent(t, "Message trigger API")
	apID := createWebhookTestAutopilot(t, agentID, "active", "run_only")
	create := func(minutes int) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := newRequest(http.MethodPost, "/api/autopilots/"+apID+"/triggers", map[string]any{"kind": "dingtalk_message", "merge_interval_minutes": minutes})
		testHandler.CreateAutopilotTrigger(w, withURLParam(r, "id", apID))
		return w
	}
	if w := create(5); w.Code != http.StatusBadRequest {
		t.Fatalf("unbound accepted: %d %s", w.Code, w.Body.String())
	}
	if _, err := testPool.Exec(context.Background(), `INSERT INTO channel_installation(workspace_id,agent_id,installer_user_id,channel_type,config,status)
        VALUES($1,$2,$3,'dingtalk_account','{"router_source_id":"message-api-source"}','active')`, testWorkspaceID, agentID, testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE agent_id=$1`, agentID)
	})
	for _, minutes := range []int{0, 1441} {
		if w := create(minutes); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid interval accepted: %d %s", w.Code, w.Body.String())
		}
	}
	w := create(1)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var trigger AutopilotTriggerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Kind != "dingtalk_message" || trigger.MergeIntervalMinutes == nil || *trigger.MergeIntervalMinutes != 1 {
		t.Fatalf("response missing saved interval: %+v", trigger)
	}
	r := newRequest(http.MethodPatch, "/api/autopilots/"+apID+"/triggers/"+trigger.ID, map[string]any{"merge_interval_minutes": 7})
	r = withURLParams(r, "id", apID, "triggerId", trigger.ID)
	w = httptest.NewRecorder()
	testHandler.UpdateAutopilotTrigger(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var minutes int
	var revision int64
	if err := testPool.QueryRow(context.Background(), `SELECT merge_interval_minutes,message_revision FROM autopilot_trigger WHERE id=$1`, trigger.ID).Scan(&minutes, &revision); err != nil {
		t.Fatal(err)
	}
	if minutes != 7 || revision != 2 {
		t.Fatalf("saved interval/revision=%d/%d", minutes, revision)
	}
	// An active message trigger cannot be silently retargeted to an unbound Agent.
	unbound := createWebhookTestAgent(t, "Unbound message executor")
	r = newRequest(http.MethodPatch, "/api/autopilots/"+apID, map[string]any{"assignee_type": "agent", "assignee_id": unbound})
	w = httptest.NewRecorder()
	testHandler.UpdateAutopilot(w, withURLParam(r, "id", apID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unbound retarget: %d %s", w.Code, w.Body.String())
	}
}

func TestMessageAutomationRetiresHourlySummaryBeforeTaskCreation(t *testing.T) {
	c := conversationSummaryDispatchCommand()
	c.AgentID = ""
	h := &Handler{}
	w := httptest.NewRecorder()
	if !h.handleMessageStatistics(w, newRequest(http.MethodPost, "/dispatch", nil), c, agentDispatchContext{}) {
		t.Fatal("hourly summary escaped retirement gate")
	}
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "hourly_summary_retired") {
		t.Fatalf("retirement receipt: %d %s", w.Code, w.Body.String())
	}
}
