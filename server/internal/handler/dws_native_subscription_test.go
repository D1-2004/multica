package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	nativeTestWorkspace = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	nativeTestUser      = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	nativeTestAgent     = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	nativeTestRuntime   = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
)

type fakeNativeStore struct {
	enabled       map[string]bool
	identity      bool
	messageStatus string // "" = no message binding
	enables       int
	disables      int
	staleDeletes  int
	// reboundUID moves the agents' current identity to another account.
	reboundUID string
	// notManaged turns the agent's DingTalk response off, so native
	// dispatches could not carry a managed response policy.
	notManaged bool
	// streaming lists other agents' native subscriptions.
	streaming []db.ListActiveDWSNativeSubscriptionsRow
	// ownerAgent is the agent whose native subscription owns the account
	// ("" = the Router owns it).
	ownerAgent string
	// accountRouted: some agent's active message binding routes the account.
	accountRouted bool
	enableErr     error
	disableErr    error
	enabledParams db.EnableAgentDWSNativeSubscriptionParams
}

func (f *fakeNativeStore) GetDWSNativeAccountOwner(context.Context, db.GetDWSNativeAccountOwnerParams) (db.GetDWSNativeAccountOwnerRow, error) {
	if f.ownerAgent == "" {
		return db.GetDWSNativeAccountOwnerRow{}, pgx.ErrNoRows
	}
	return db.GetDWSNativeAccountOwnerRow{AgentID: parseUUID(f.ownerAgent)}, nil
}

func (f *fakeNativeStore) HasActiveDingTalkMessageRouteForAccount(context.Context, db.HasActiveDingTalkMessageRouteForAccountParams) (bool, error) {
	return f.accountRouted, nil
}

func (f *fakeNativeStore) GetAgentDingTalkResponsePolicy(context.Context, pgtype.UUID) (db.GetAgentDingTalkResponsePolicyRow, error) {
	return db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, DingtalkResponseEnabled: !f.notManaged,
		DingtalkResponsePolicyRevision: 3, DingtalkShowAiTag: true}, nil
}

func (f *fakeNativeStore) GetAgentRuntimeForWorkspace(context.Context, db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error) {
	return db.AgentRuntime{RuntimeMode: "local", Metadata: []byte(`{"client_capabilities":["dws_message_policy_v1"]}`)}, nil
}

func (f *fakeNativeStore) ListActiveDWSNativeSubscriptions(context.Context) ([]db.ListActiveDWSNativeSubscriptionsRow, error) {
	return f.streaming, nil
}

// fakeDispatchEndpoints records native subscription's endpoint ensure.
type fakeDispatchEndpoints struct {
	calls int
	err   error
}

func (f *fakeDispatchEndpoints) Ensure(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID) (agentmessagerouter.DispatchEndpoint, error) {
	f.calls++
	return agentmessagerouter.DispatchEndpoint{EndpointID: "k1_endpoint"}, f.err
}

func (f *fakeNativeStore) GetAgentDWSNativeSubscription(_ context.Context, p db.GetAgentDWSNativeSubscriptionParams) (db.AgentDwsNativeSubscription, error) {
	if !f.enabled[util.UUIDToString(p.AgentID)] {
		return db.AgentDwsNativeSubscription{}, pgx.ErrNoRows
	}
	return db.AgentDwsNativeSubscription{AgentID: p.AgentID, WorkspaceID: p.WorkspaceID}, nil
}

func (f *fakeNativeStore) EnableAgentDWSNativeSubscription(_ context.Context, p db.EnableAgentDWSNativeSubscriptionParams) (db.AgentDwsNativeSubscription, error) {
	f.enabledParams = p
	if f.enableErr != nil {
		return db.AgentDwsNativeSubscription{}, f.enableErr
	}
	f.enables++
	f.enabled[util.UUIDToString(p.AgentID)] = true
	return db.AgentDwsNativeSubscription{AgentID: p.AgentID, WorkspaceID: p.WorkspaceID}, nil
}

func (f *fakeNativeStore) DisableAgentDWSNativeSubscription(_ context.Context, p db.DisableAgentDWSNativeSubscriptionParams) error {
	if f.disableErr != nil {
		return f.disableErr
	}
	f.disables++
	delete(f.enabled, util.UUIDToString(p.AgentID))
	return nil
}

func (f *fakeNativeStore) ListWorkspaceDWSNativeSubscriptions(context.Context, pgtype.UUID) ([]db.AgentDwsNativeSubscription, error) {
	var rows []db.AgentDwsNativeSubscription
	for agentID := range f.enabled {
		rows = append(rows, db.AgentDwsNativeSubscription{AgentID: parseUUID(agentID), DwsUid: "1001", OrgID: "2002"})
	}
	return rows, nil
}

// ListAgentDingTalkIdentities returns each enabled agent's current identity;
// reboundUID simulates an identity moved to another account.
func (f *fakeNativeStore) ListAgentDingTalkIdentities(context.Context, pgtype.UUID) ([]db.AgentDingtalkIdentity, error) {
	uid := "1001"
	if f.reboundUID != "" {
		uid = f.reboundUID
	}
	var identities []db.AgentDingtalkIdentity
	for agentID := range f.enabled {
		identities = append(identities, db.AgentDingtalkIdentity{AgentID: parseUUID(agentID), DwsUid: uid, OrgID: "2002"})
	}
	return identities, nil
}

func (f *fakeNativeStore) DeleteStaleDWSNativeSubscriptionsForAccount(context.Context, db.DeleteStaleDWSNativeSubscriptionsForAccountParams) error {
	f.staleDeletes++
	return nil
}

func (f *fakeNativeStore) GetAgentDingTalkIdentity(context.Context, db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error) {
	if !f.identity {
		return db.AgentDingtalkIdentity{}, pgx.ErrNoRows
	}
	return db.AgentDingtalkIdentity{DwsUid: "1001", OrgID: "2002"}, nil
}

func (f *fakeNativeStore) GetDingTalkAccountBindingByAgent(context.Context, db.GetDingTalkAccountBindingByAgentParams) (db.ChannelInstallation, error) {
	if f.messageStatus == "" {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	return db.ChannelInstallation{Status: f.messageStatus}, nil
}

// fakeDirectBindingService adds the direct digital-employee binding.
type fakeDirectBindingService struct {
	fakeDingTalkAccountBindingService
	bindCalls  int
	bindParams agentmessagerouter.DirectBindingParams
}

func (f *fakeDirectBindingService) BindDigitalEmployee(_ context.Context, p agentmessagerouter.DirectBindingParams) (agentmessagerouter.DirectDigitalEmployeeBinding, error) {
	f.bindCalls++
	f.bindParams = p
	return agentmessagerouter.DirectDigitalEmployeeBinding{Status: "active"}, nil
}

type fakeOperatorUsers struct{ email string }

func (f fakeOperatorUsers) GetUser(context.Context, pgtype.UUID) (db.User, error) {
	return db.User{Email: f.email}, nil
}

func nativeTestHandler(store *fakeNativeStore, service dingTalkAccountBindingService, operator bool) *Handler {
	metadata := &dingTalkOwnershipMetadataDB{
		agents: map[string]db.Agent{
			nativeTestAgent: {ID: parseUUID(nativeTestAgent), WorkspaceID: parseUUID(nativeTestWorkspace),
				OwnerID: parseUUID(nativeTestUser), Name: "Native Agent", RuntimeID: parseUUID(nativeTestRuntime)},
		},
		workspace: db.Workspace{ID: parseUUID(nativeTestWorkspace), Name: "Native Workspace"},
	}
	h := &Handler{DingTalkAccountBindings: service, dingTalkAccountBindingMetadata: metadata,
		dingTalkAccountBindingPermissions: &fakeDingTalkAccountBindingPermissionStore{
			member: db.Member{UserID: parseUUID(nativeTestUser), Role: "member"},
		},
		dwsNativeSubscriptions: store, operatorUsers: fakeOperatorUsers{email: "operator@multica.test"}, Bus: events.New(),
		DispatchEndpoints: &fakeDispatchEndpoints{}, nativeSourceActive: func() bool { return true }}
	if operator {
		h.SetConfigProvider(func() Config { return Config{A2AOperatorEmails: []string{"Operator@multica.test"}} })
	}
	return h
}

func nativeRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-User-ID", nativeTestUser)
	return withURLParams(req, "id", nativeTestWorkspace, "agentId", nativeTestAgent)
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return body.Code
}

func TestSetDWSNativeSubscription(t *testing.T) {
	path := "/api/workspaces/" + nativeTestWorkspace + "/dingtalk/account-bindings/" + nativeTestAgent + "/native-subscription"
	for _, tt := range []struct {
		name          string
		body          string
		identity      bool
		messageStatus string
		enabledBefore bool
		want          int
		code          string
		enables       int
		disables      int
	}{
		{name: "enable with a bound identity", body: `{"enabled":true}`, identity: true, want: 200, enables: 1},
		{name: "enable without identity", body: `{"enabled":true}`, want: 409, code: "native_subscription_requires_identity"},
		{name: "enable next to an active message binding", body: `{"enabled":true}`, identity: true, messageStatus: "active",
			want: 409, code: "native_subscription_conflicts_with_message_binding"},
		{name: "an inactive message binding does not block", body: `{"enabled":true}`, identity: true, messageStatus: "revoked", want: 200, enables: 1},
		{name: "disable", body: `{"enabled":false}`, enabledBefore: true, want: 200, disables: 1},
		{name: "enabled is required", body: `{}`, want: 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeNativeStore{enabled: map[string]bool{}, identity: tt.identity, messageStatus: tt.messageStatus}
			if tt.enabledBefore {
				store.enabled[nativeTestAgent] = true
			}
			h := nativeTestHandler(store, &fakeDingTalkAccountBindingService{}, false)
			w := httptest.NewRecorder()
			h.SetDWSNativeSubscription(w, nativeRequest(http.MethodPut, path, tt.body))
			if w.Code != tt.want || (tt.code != "" && errorCode(t, w) != tt.code) {
				t.Fatalf("status = %d body = %s, want %d %s", w.Code, w.Body.String(), tt.want, tt.code)
			}
			if store.enables != tt.enables || store.disables != tt.disables {
				t.Fatalf("enables = %d disables = %d, want %d %d", store.enables, store.disables, tt.enables, tt.disables)
			}
		})
	}
}

// A digital-employee message binding cannot start while native subscription
// is on; an identity binding can.
func TestBeginMessageBindingBlockedByNativeSubscription(t *testing.T) {
	path := "/api/workspaces/" + nativeTestWorkspace + "/dingtalk/account-bindings/begin"
	for _, mode := range []string{"message", "identity"} {
		store := &fakeNativeStore{enabled: map[string]bool{nativeTestAgent: true}}
		service := &fakeDingTalkAccountBindingService{}
		h := nativeTestHandler(store, service, false)
		w := httptest.NewRecorder()
		h.BeginDingTalkAccountBinding(w, nativeRequest(http.MethodPost, path, `{"agent_id":"`+nativeTestAgent+`","binding_mode":"`+mode+`"}`))
		if mode == "message" {
			if w.Code != 409 || errorCode(t, w) != "message_binding_conflicts_with_native_subscription" || service.beginCalls != 0 {
				t.Fatalf("message: status = %d body = %s begin calls = %d", w.Code, w.Body.String(), service.beginCalls)
			}
		} else if w.Code != 200 || service.beginCalls != 1 {
			t.Fatalf("identity: status = %d body = %s begin calls = %d", w.Code, w.Body.String(), service.beginCalls)
		}
	}
}

// The list flags identities with native subscription and tells operators
// they may bind manually.
func TestListMarksNativeSubscriptionAndManualBinding(t *testing.T) {
	store := &fakeNativeStore{enabled: map[string]bool{nativeTestAgent: true}}
	service := &fakeDingTalkAccountBindingService{listResult: []agentmessagerouter.PublicDingTalkAccountBinding{{
		AgentID:     nativeTestAgent,
		DWSIdentity: agentmessagerouter.PublicDingTalkBindingOutcome{Status: "active"},
	}}}
	for _, operator := range []bool{false, true} {
		h := nativeTestHandler(store, service, operator)
		w := httptest.NewRecorder()
		h.ListDingTalkAccountBindings(w, nativeRequest(http.MethodGet, "/api/workspaces/"+nativeTestWorkspace+"/dingtalk/account-bindings", ""))
		var body struct {
			Bindings []struct {
				DWSIdentity struct {
					NativeSubscription bool `json:"native_subscription"`
				} `json:"dws_identity"`
			} `json:"bindings"`
			ManualBindingAllowed bool `json:"manual_binding_allowed"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Bindings) != 1 {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		if !body.Bindings[0].DWSIdentity.NativeSubscription || body.ManualBindingAllowed != operator {
			t.Fatalf("operator=%v: body = %s", operator, w.Body.String())
		}
	}
}

// A row left for the account the identity was bound to before is not
// reported as on: the rebound identity is not subscribed until enabled again.
func TestListDoesNotMarkNativeSubscriptionOfARebindIdentity(t *testing.T) {
	store := &fakeNativeStore{enabled: map[string]bool{nativeTestAgent: true}, reboundUID: "9999"}
	service := &fakeDingTalkAccountBindingService{listResult: []agentmessagerouter.PublicDingTalkAccountBinding{{
		AgentID:     nativeTestAgent,
		DWSIdentity: agentmessagerouter.PublicDingTalkBindingOutcome{Status: "active"},
	}}}
	h := nativeTestHandler(store, service, false)
	w := httptest.NewRecorder()
	h.ListDingTalkAccountBindings(w, nativeRequest(http.MethodGet, "/api/workspaces/"+nativeTestWorkspace+"/dingtalk/account-bindings", ""))
	if w.Code != 200 || strings.Contains(w.Body.String(), `"native_subscription":true`) {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestBindDingTalkMessageRouteManually(t *testing.T) {
	path := "/api/workspaces/" + nativeTestWorkspace + "/dingtalk/account-bindings/" + nativeTestAgent + "/message-route/manual"
	for _, tt := range []struct {
		name     string
		body     string
		operator bool
		native   bool
		want     int
		code     string
	}{
		{name: "operator", body: `{"org_id":"439446171","uid":"123456"}`, operator: true, want: 200},
		{name: "not an operator", body: `{"org_id":"439446171","uid":"123456"}`, want: 403, code: "operator_only"},
		{name: "native subscription on", body: `{"org_id":"439446171","uid":"123456"}`, operator: true, native: true,
			want: 409, code: "message_binding_conflicts_with_native_subscription"},
		{name: "ids must be decimal", body: `{"org_id":"ding123","uid":"123456"}`, operator: true, want: 400, code: "invalid_identity"},
		{name: "scope is checked", body: `{"org_id":"439446171","uid":"123456","message_scope":"custom"}`, operator: true,
			want: 400, code: "invalid_message_scope"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeNativeStore{enabled: map[string]bool{}}
			if tt.native {
				store.enabled[nativeTestAgent] = true
			}
			service := &fakeDirectBindingService{}
			h := nativeTestHandler(store, service, tt.operator)
			w := httptest.NewRecorder()
			h.BindDingTalkMessageRouteManually(w, nativeRequest(http.MethodPost, path, tt.body))
			if w.Code != tt.want || (tt.code != "" && errorCode(t, w) != tt.code) {
				t.Fatalf("status = %d body = %s, want %d %s", w.Code, w.Body.String(), tt.want, tt.code)
			}
			if tt.want != 200 {
				if service.bindCalls != 0 {
					t.Fatal("binding reached the router despite a guard")
				}
				return
			}
			p := service.bindParams
			if service.bindCalls != 1 || p.TenantID != "439446171" || p.DigitalEmployeeID != "123456" ||
				p.MessageScope != agentmessagerouter.DingTalkMessageScopeAll || p.InitiatorID != parseUUID(nativeTestUser) ||
				p.Agent.Name != "Native Agent" || p.Agent.Workspace.Name != "Native Workspace" {
				t.Fatalf("bind params = %#v", p)
			}
		})
	}
}
