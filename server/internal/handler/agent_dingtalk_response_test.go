package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		var response AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	initial := get()
	if initial.DingTalkShowAITag || initial.InboundCoordinator || initial.DingTalkResponsePolicyRevision != 1 {
		t.Fatalf("unexpected initial response policy: %+v", initial)
	}
	w := updateAgentForTest(t, agentID, map[string]any{"dingtalk_show_ai_tag": true})
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d: %s", w.Code, w.Body.String())
	}
	detail := get()
	if !detail.DingTalkShowAITag || detail.InboundCoordinator || detail.DingTalkResponsePolicyRevision != 2 {
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
			if !agent.DingTalkShowAITag || agent.InboundCoordinator || agent.DingTalkResponsePolicyRevision != 2 {
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
		name        string
		body        map[string]any
		revision    int64
		showAITag   bool
		coordinator bool
	}{
		{"unchanged defaults", map[string]any{"dingtalk_show_ai_tag": false, "inbound_coordinator": false}, 1, false, false},
		{"both switches change once", map[string]any{"dingtalk_show_ai_tag": true, "inbound_coordinator": true}, 2, true, true},
		{"duplicate settings", map[string]any{"dingtalk_show_ai_tag": true, "inbound_coordinator": true}, 2, true, true},
		{"unrelated edit", map[string]any{"description": "Changed description"}, 2, true, true},
		{"AI switch only", map[string]any{"dingtalk_show_ai_tag": false}, 3, false, true},
		{"coordinator switch only", map[string]any{"inbound_coordinator": false}, 4, false, false},
		{"client cannot set revision", map[string]any{"dingtalk_response_policy_revision": 99}, 4, false, false},
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
			if response.DingTalkShowAITag != tc.showAITag || response.InboundCoordinator != tc.coordinator || response.DingTalkResponsePolicyRevision != tc.revision {
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
	if !policy.InboundCoordinator || !policy.DingtalkShowAiTag || policy.DingtalkResponsePolicyRevision != 3 {
		t.Fatalf("concurrent updates lost a setting or revision: %+v", policy)
	}
}

func TestUpdateAgentDingTalkResponsePolicyRejectsMalformedBoolean(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dingtalk-policy-malformed", nil)
	w := updateAgentForTest(t, agentID, map[string]any{"dingtalk_show_ai_tag": "false"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected malformed boolean to fail: %d: %s", w.Code, w.Body.String())
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
				"dingtalk_show_ai_tag": true,
			}), "id", agentID)
			testHandler.UpdateAgent(w, req)
			if w.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, w.Code, w.Body.String())
			}
		})
	}
}
