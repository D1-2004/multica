package agentmessagerouter

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestResponsePolicyReadbackAndRevisionFencing(t *testing.T) {
	for _, tc := range []struct {
		name            string
		initialRevision int64
		drift           bool
		wantPatch       bool
		wantError       bool
	}{
		{"first_activation", 0, false, true, false},
		{"matching_replay", 4, false, false, false},
		{"reject_older_intent", 5, false, false, true},
		{"verify_readback", 3, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desired := protocol.DingTalkResponsePolicy{Version: 1, Mode: "multica_coordinator", Revision: 4}
			var current *protocol.DingTalkResponsePolicy
			if tc.initialRevision != 0 {
				p := desired
				p.Revision = tc.initialRevision
				current = &p
			}
			patches := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				responsePolicy := current
				if r.Method == "PATCH" {
					patches++
					responsePolicy = &desired
					if !tc.drift {
						current = &desired
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "success", "data": Subscription{
					SourceID: "source-1", AgentID: "agent-1", DispatchURL: "https://multica.example/dispatch", Status: "active",
					Surface: SubscriptionSurface{Type: "chat"}, Outbound: SubscriptionOutbound{Mode: "dws", ReplyTo: "latest_message"}, ResponsePolicy: responsePolicy,
				}})
			}))
			defer server.Close()
			client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "test-service"})
			if err != nil {
				t.Fatal(err)
			}
			worker := NewResponsePolicySyncWorker(nil, client, ResponsePolicySyncConfig{})
			err = worker.syncPolicy(context.Background(), "source-1", "agent-1", desired)
			if (err != nil) != tc.wantError || (patches > 0) != tc.wantPatch {
				t.Fatalf("patches=%d err=%v", patches, err)
			}
		})
	}
}

type responsePolicyFenceTestStore struct {
	ResponsePolicySyncStore
	fence      db.DingtalkResponsePolicyRollout
	rows       []db.ListResponsePolicySyncCandidatesRow
	listParams []db.ListResponsePolicySyncCandidatesParams
	upserts    []db.UpsertResponsePolicySyncParams
}

func (s *responsePolicyFenceTestStore) EnsureAgentResponsePolicyTarget(context.Context, string) (db.DingtalkResponsePolicyRollout, error) {
	return s.fence, nil
}
func (s *responsePolicyFenceTestStore) ListResponsePolicySyncCandidates(_ context.Context, p db.ListResponsePolicySyncCandidatesParams) ([]db.ListResponsePolicySyncCandidatesRow, error) {
	s.listParams = append(s.listParams, p)
	return s.rows, nil
}
func (s *responsePolicyFenceTestStore) UpsertResponsePolicySync(_ context.Context, p db.UpsertResponsePolicySyncParams) (db.DingtalkResponsePolicySync, error) {
	s.upserts = append(s.upserts, p)
	return db.DingtalkResponsePolicySync{}, nil
}

func TestResponsePolicyUnmigratedTargetMakesNoRouterRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "test-service"})
	if err != nil {
		t.Fatal(err)
	}
	for _, fence := range []db.DingtalkResponsePolicyRollout{{}, {Revision: 1, Enabled: true}, {Revision: 2, Enabled: false}} {
		store := &responsePolicyFenceTestStore{fence: fence}
		worker := NewResponsePolicySyncWorker(store, client, ResponsePolicySyncConfig{})
		if err := worker.Reconcile(context.Background()); err == nil {
			t.Fatalf("unmigrated target allowed: %+v", fence)
		}
		if len(store.listParams) != 0 || len(store.upserts) != 0 {
			t.Fatal("unmigrated target accessed agent policies")
		}
	}
	if requests != 0 {
		t.Fatalf("unmigrated target made %d Router requests", requests)
	}
}

func TestResponsePolicyDesiredModeNeedsEmployeeCoordinatorAndRuntime(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, coordinator := range []bool{false, true} {
			for _, ready := range []bool{false, true} {
				p := desiredResponsePolicy(db.ListResponsePolicySyncCandidatesRow{DingtalkResponseEnabled: enabled, InboundCoordinator: coordinator, DingtalkShowAiTag: true, DingtalkResponsePolicyRevision: 7}, ready)
				if !p.Valid() || p.Managed() != (enabled && coordinator && ready) || !p.ShowAITag || p.Revision != 7 {
					t.Fatalf("enabled=%v coordinator=%v runtime=%v policy=%+v", enabled, coordinator, ready, p)
				}
			}
		}
	}
}

func TestResponsePolicyReconcileEmployeeSwitchAndCapabilityGates(t *testing.T) {
	for _, tc := range []struct {
		name                                          string
		enabled, coordinator, runtimeID, runtimeReady bool
		resolverErr                                   bool
		wantResolve, wantUpsert                       bool
		wantMode                                      string
	}{
		{"disabled previous subscriber rolls back", false, true, true, true, false, false, true, "legacy"},
		{"coordinator off", true, false, true, true, false, false, true, "legacy"},
		{"no runtime", true, true, false, true, false, false, true, "legacy"},
		{"old runtime", true, true, true, false, false, true, true, "legacy"},
		{"ready", true, true, true, true, false, true, true, "multica_coordinator"},
		{"unresolved runtime preserves current policy", true, true, true, false, true, true, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/subscriptions/response-policy-capabilities" {
					t.Errorf("unexpected Router write: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "success", "data": map[string]any{"versions": []int{1}, "modes": []string{"legacy", "multica_coordinator"}}})
			}))
			defer server.Close()
			client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "test-service"})
			if err != nil {
				t.Fatal(err)
			}
			row := db.ListResponsePolicySyncCandidatesRow{SourceID: "source-1", DingtalkResponseEnabled: tc.enabled, InboundCoordinator: tc.coordinator, RuntimeID: pgtype.UUID{Valid: tc.runtimeID}, DingtalkResponsePolicyRevision: 7}
			store := &responsePolicyFenceTestStore{fence: db.DingtalkResponsePolicyRollout{Revision: 13, Enabled: true}, rows: []db.ListResponsePolicySyncCandidatesRow{row}}
			resolved := false
			worker := NewResponsePolicySyncWorker(store, client, ResponsePolicySyncConfig{RuntimeSupportsPolicy: func(_ context.Context, agent db.Agent) (bool, error) {
				resolved = true
				if agent.RuntimeID != row.RuntimeID {
					t.Fatal("runtime resolver received wrong trusted identity")
				}
				if tc.resolverErr {
					return false, errors.New("temporary resolver outage")
				}
				return tc.runtimeReady, nil
			}})
			if err := worker.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if resolved != tc.wantResolve || (len(store.upserts) != 0) != tc.wantUpsert {
				t.Fatalf("resolved=%v upserts=%+v", resolved, store.upserts)
			}
			if len(store.listParams) != 1 || store.listParams[0].TargetIdentity != client.TargetIdentity() {
				t.Fatalf("candidate selection lost target scope: %+v", store.listParams)
			}
			if tc.wantUpsert && (store.upserts[0].DesiredMode != tc.wantMode || store.upserts[0].RolloutRevision != 13) {
				t.Fatalf("desired policy did not use employee mode and persisted epoch: %+v", store.upserts[0])
			}
		})
	}
}

func TestResponsePolicyOldRouterDoesNotQueueEmployeePolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/subscriptions/response-policy-capabilities" {
			t.Errorf("unexpected request against old Router: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "test-service"})
	if err != nil {
		t.Fatal(err)
	}
	store := &responsePolicyFenceTestStore{fence: db.DingtalkResponsePolicyRollout{Revision: 2, Enabled: true}}
	worker := NewResponsePolicySyncWorker(store, client, ResponsePolicySyncConfig{})
	if err := worker.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.listParams) != 0 || len(store.upserts) != 0 {
		t.Fatal("old Router capability gate allowed policy materialization")
	}
}
