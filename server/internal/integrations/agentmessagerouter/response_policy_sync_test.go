package agentmessagerouter

import (
	"context"
	"encoding/json"
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
	fence db.DingtalkResponsePolicyRollout
}

func (s *responsePolicyFenceTestStore) ObserveResponsePolicyRollout(_ context.Context, p db.ObserveResponsePolicyRolloutParams) (db.DingtalkResponsePolicyRollout, error) {
	if p.Revision > s.fence.Revision {
		s.fence = db.DingtalkResponsePolicyRollout{Revision: p.Revision, Enabled: p.Enabled}
	}
	return s.fence, nil
}
func (s *responsePolicyFenceTestStore) HasResponsePolicySyncTarget(context.Context, string) (bool, error) {
	return false, nil
}

func TestResponsePolicyDefaultGateAndStaleReplicaMakeNoRouterRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "test-service"})
	if err != nil {
		t.Fatal(err)
	}
	store := &responsePolicyFenceTestStore{}
	worker := NewResponsePolicySyncWorker(store, client, ResponsePolicySyncConfig{})
	if err := worker.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.fence = db.DingtalkResponsePolicyRollout{Revision: 2, Enabled: true}
	if err := worker.Reconcile(context.Background()); err == nil {
		t.Fatal("older rollout replica was allowed")
	}
	worker.config.Enabled = func() bool { return true }
	worker.config.RolloutRevision = func() int64 { return 2 }
	store.fence.Enabled = false
	if err := worker.Reconcile(context.Background()); err == nil {
		t.Fatal("same revision with conflicting mode was allowed")
	}
	if requests != 0 {
		t.Fatalf("disabled/stale replica made %d Router requests", requests)
	}
}

func TestResponsePolicyDesiredModeNeedsAllGates(t *testing.T) {
	for _, coordinator := range []bool{false, true} {
		for _, ready := range []bool{false, true} {
			p := desiredResponsePolicy(db.ListResponsePolicySyncCandidatesRow{InboundCoordinator: coordinator, DingtalkShowAiTag: true, DingtalkResponsePolicyRevision: 7}, ready)
			if !p.Valid() || p.Managed() != (coordinator && ready) || !p.ShowAITag || p.Revision != 7 {
				t.Fatalf("unexpected policy=%+v", p)
			}
		}
	}
}
