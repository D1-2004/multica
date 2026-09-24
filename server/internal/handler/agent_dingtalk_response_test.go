package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAgentDingTalkResponsePolicyDefaultsAndHydration(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dingtalk-policy-defaults", nil)

	get := func() AgentResponse {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodGet, "/api/agents/"+agentID, nil), "id", agentID)
		testHandler.GetAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("get agent: %d: %s", w.Code, w.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if _, present := raw["dingtalk_response_enabled"]; !present {
			t.Fatal("detail omitted the explicit default-off switch")
		}
		var response AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	initial := get()
	if initial.DingTalkResponseEnabled || initial.DingTalkShowAITag || initial.InboundCoordinator || initial.DingTalkResponsePolicyRevision != 1 {
		t.Fatalf("unexpected initial response policy: %+v", initial)
	}
	w := updateAgentForTest(t, agentID, map[string]any{"dingtalk_show_ai_tag": true, "dingtalk_response_enabled": true})
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d: %s", w.Code, w.Body.String())
	}
	detail := get()
	if !detail.DingTalkResponseEnabled || !detail.DingTalkShowAITag || detail.InboundCoordinator || detail.DingTalkResponsePolicyRevision != 2 {
		t.Fatalf("unexpected updated detail response policy: %+v", detail)
	}

	w = httptest.NewRecorder()
	testHandler.ListAgents(w, newRequest(http.MethodGet, "/api/agents", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d: %s", w.Code, w.Body.String())
	}
	var agents []AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &agents); err != nil {
		t.Fatal(err)
	}
	for _, agent := range agents {
		if agent.ID == agentID {
			if !agent.DingTalkResponseEnabled || !agent.DingTalkShowAITag || agent.InboundCoordinator || agent.DingTalkResponsePolicyRevision != 2 {
				t.Fatalf("list response policy differs from detail: %+v", agent)
			}
			return
		}
	}
	t.Fatal("agent missing from list")
}

func TestUpdateAgentDingTalkResponsePolicyRevision(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dingtalk-policy-revision", nil)
	cases := []struct {
		name            string
		body            map[string]any
		revision        int64
		showAITag       bool
		coordinator     bool
		responseEnabled bool
	}{
		{"unchanged defaults", map[string]any{"dingtalk_show_ai_tag": false, "inbound_coordinator": false, "dingtalk_response_enabled": false}, 1, false, false, false},
		{"response switch independent", map[string]any{"dingtalk_response_enabled": true}, 2, false, false, true},
		{"other switches change once", map[string]any{"dingtalk_show_ai_tag": true, "inbound_coordinator": true}, 3, true, true, true},
		{"duplicate settings", map[string]any{"dingtalk_show_ai_tag": true, "inbound_coordinator": true, "dingtalk_response_enabled": true}, 3, true, true, true},
		{"unrelated edit", map[string]any{"description": "Changed description"}, 3, true, true, true},
		{"AI switch only", map[string]any{"dingtalk_show_ai_tag": false}, 4, false, true, true},
		{"response switch off", map[string]any{"dingtalk_response_enabled": false}, 5, false, true, false},
		{"coordinator switch only", map[string]any{"inbound_coordinator": false}, 6, false, false, false},
		{"all switches change once", map[string]any{"dingtalk_show_ai_tag": true, "inbound_coordinator": true, "dingtalk_response_enabled": true}, 7, true, true, true},
		{"client cannot set revision", map[string]any{"dingtalk_response_policy_revision": 99}, 7, true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := updateAgentForTest(t, agentID, tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("update: %d: %s", w.Code, w.Body.String())
			}
			var response AgentResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.DingTalkResponseEnabled != tc.responseEnabled || response.DingTalkShowAITag != tc.showAITag || response.InboundCoordinator != tc.coordinator || response.DingTalkResponsePolicyRevision != tc.revision {
				t.Fatalf("unexpected response policy: %+v", response)
			}
		})
	}
}

func TestUpdateAgentDingTalkResponsePolicyConcurrentPartialUpdates(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := parseUUID(createHandlerTestAgent(t, "dingtalk-policy-concurrent", nil))
	params := []db.UpdateAgentDingTalkResponsePolicyParams{
		{ID: agentID, InboundCoordinator: pgtype.Bool{Bool: true, Valid: true}},
		{ID: agentID, ShowAiTag: pgtype.Bool{Bool: true, Valid: true}},
		{ID: agentID, ResponseEnabled: pgtype.Bool{Bool: true, Valid: true}},
	}
	queries := make([]*db.Queries, len(params))
	for i := range params {
		conn, err := testPool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(conn.Release)
		queries[i] = db.New(conn)
	}
	errors := make(chan error, len(params))
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i, update := range params {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := queries[i].UpdateAgentDingTalkResponsePolicy(ctx, update)
			errors <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	policy, err := queries[0].GetAgentDingTalkResponsePolicy(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.DingtalkResponseEnabled || !policy.InboundCoordinator || !policy.DingtalkShowAiTag || policy.DingtalkResponsePolicyRevision != 4 {
		t.Fatalf("concurrent updates lost a setting or revision: %+v", policy)
	}
}

func TestUpdateAgentDingTalkResponsePolicyRejectsMalformedBoolean(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dingtalk-policy-malformed", nil)
	for _, field := range []string{"dingtalk_response_enabled", "dingtalk_show_ai_tag", "inbound_coordinator"} {
		for _, value := range []any{"false", 1, []bool{true}} {
			w := updateAgentForTest(t, agentID, map[string]any{field: value})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected malformed %s=%v to fail: %d: %s", field, value, w.Code, w.Body.String())
			}
		}
	}
}

func TestUpdateAgentDingTalkResponsePolicyRequiresManageAccess(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID, ownerID, memberID := privateAgentTestFixture(t)
	for _, tc := range []struct {
		name   string
		userID string
		status int
	}{
		{"unrelated member", memberID, http.StatusForbidden},
		{"agent owner", ownerID, http.StatusOK},
		{"workspace owner", testUserID, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := withURLParam(newRequestAs(tc.userID, http.MethodPut, "/api/agents/"+agentID, map[string]any{
				"dingtalk_response_enabled": true,
			}), "id", agentID)
			testHandler.UpdateAgent(w, req)
			if w.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, w.Code, w.Body.String())
			}
		})
	}
}

func TestUpdateAgentUserDecisionNamesRoundTrip(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "decision-names", nil)
	for _, tc := range []struct {
		body     map[string]any
		names    []string
		revision int64
		enabled  bool
	}{
		{map[string]any{"inbound_coordinator": true, "inbound_coordinator_user_decision": true, "inbound_coordinator_user_decision_names": []string{" 冬翔 ", "", "冬翔", "Alice"}}, []string{"冬翔", "Alice"}, 2, true},
		{map[string]any{"description": "Unrelated update"}, []string{"冬翔", "Alice"}, 2, true},
		{map[string]any{"inbound_coordinator_user_decision_names": []string{"冬翔", "Alice"}}, []string{"冬翔", "Alice"}, 2, true},
		{map[string]any{"inbound_coordinator_user_decision_names": []string{}}, []string{}, 3, true},
		{map[string]any{"inbound_coordinator": false}, []string{}, 4, false},
	} {
		w := updateAgentForTest(t, agentID, tc.body)
		if w.Code != http.StatusOK {
			t.Fatalf("update: %d %s", w.Code, w.Body.String())
		}
		var response AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(response.InboundCoordinatorUserDecisionNames, tc.names) || response.DingTalkResponsePolicyRevision != tc.revision || response.InboundCoordinatorUserDecision != tc.enabled {
			t.Fatalf("unexpected response: names=%v revision=%d enabled=%v", response.InboundCoordinatorUserDecisionNames, response.DingTalkResponsePolicyRevision, response.InboundCoordinatorUserDecision)
		}
	}
	for _, value := range []any{"冬翔", []any{"冬翔", 1}, 1} {
		w := updateAgentForTest(t, agentID, map[string]any{"inbound_coordinator_user_decision_names": value})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("malformed names accepted: %d", w.Code)
		}
	}
}
