package agentmessagerouter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type fakeDirectBindingRouter struct {
	*fakeBindingRouter
	created      []CreateSubscriptionParams
	createResult Subscription
	createErr    error
}

func (f *fakeDirectBindingRouter) CreateHTTPCallbackSubscription(
	_ context.Context,
	params CreateSubscriptionParams,
) (Subscription, error) {
	f.created = append(f.created, params)
	if f.createErr != nil {
		return Subscription{}, f.createErr
	}
	result := f.createResult
	if result.SourceID == "" {
		result = Subscription{
			SourceID:    "source-channel-1",
			AgentID:     params.AgentID,
			DispatchURL: params.DispatchURL,
			Surface:     params.Surface,
			Outbound:    params.Outbound,
			Status:      "active",
		}
	}
	return result, nil
}

func TestBindDigitalEmployeeConsumesServerTokenAndPersistsCurrentProjection(t *testing.T) {
	now := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)
	store := &fakeBindingStore{}
	agentID := uuidForTest(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	workspaceID := uuidForTest(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	router := &fakeDirectBindingRouter{fakeBindingRouter: &fakeBindingRouter{
		issued: BindingToken{BindingToken: "bat_v1.server-only-secret", ExpiresAt: now.Add(5 * time.Minute)},
		bindingChecks: []DigitalEmployeeBindingCheck{{
			AgentID: uuidStringForTest(agentID), Platform: "dingtalk", TenantID: "tenant-a",
			AccountID: "employee-a", Status: "unbound",
		}},
		unbindResult: DigitalEmployeeBindingUnbindResult{Status: "unbound"},
	}}
	service := newBindingServiceForTest(t, store, router, now)

	result, err := service.BindDigitalEmployee(context.Background(), DirectBindingParams{
		Agent: BeginAgent{
			ID: agentID, Name: "Agent A",
			Workspace: BeginWorkspace{ID: workspaceID, Name: "Workspace A"},
		},
		InitiatorID:       uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"),
		TenantID:          "tenant-a",
		DigitalEmployeeID: "employee-a",
		SurfaceType:       DingTalkSurfaceAuto,
		MessageScope:      DingTalkMessageScopeAll,
		EnabledDomains:    []string{"channel", "approval"},
	})
	if err != nil {
		t.Fatalf("BindDigitalEmployee() error = %v", err)
	}
	if !store.activated || len(router.created) != 1 {
		t.Fatalf("activated=%v creates=%d", store.activated, len(router.created))
	}
	created := router.created[0]
	if created.BindingToken != "bat_v1.server-only-secret" ||
		!strings.HasPrefix(created.DispatchURL, "https://multica.example/api/webhooks/agent-dispatch/") ||
		created.ReplaceExisting || len(created.EnabledDomains) != 2 || created.EnabledDomains[1] != "approval" {
		t.Fatalf("create params = %#v", created)
	}
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	if config.RouterTenantID != "tenant-a" || config.RouterAccountID != "employee-a" ||
		config.RouterSourceID != "source-channel-1" || config.CallbackTokenHash != "" ||
		len(config.EnabledDomains) != 2 || config.EnabledDomains[1] != "approval" {
		t.Fatalf("stored config = %#v", config)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(store.beginConfig), "server-only-secret") ||
		strings.Contains(string(store.row.Config), "server-only-secret") ||
		strings.Contains(string(payload), "server-only-secret") {
		t.Fatalf("binding token escaped persistence or response")
	}
	if result.Status != "active" || result.RouterBindingStatus != "valid" ||
		result.DigitalEmployeeID != "employee-a" {
		t.Fatalf("result = %#v", result)
	}
	descriptorPayload, err := json.Marshal(router.issueAgent)
	if err != nil {
		t.Fatal(err)
	}
	var descriptorFields map[string]any
	if err := json.Unmarshal(descriptorPayload, &descriptorFields); err != nil {
		t.Fatal(err)
	}
	if descriptorFields["agentEnvironment"] != "production" {
		t.Fatalf("Router issue environment = %#v", descriptorFields["agentEnvironment"])
	}
}

func TestUnbindDirectPendingProjectionCallsRouterBeforeLocalCleanup(t *testing.T) {
	now := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	config.RouterPlatform = "dingtalk"
	config.RouterTenantID = "tenant-a"
	config.RouterAccountID = "employee-a"
	config.EnabledDomains = []string{"channel"}
	store.row.Config, err = config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store.row.Status = "pending"
	router := &fakeBindingRouter{unbindResult: DigitalEmployeeBindingUnbindResult{Status: "unbound"}}
	service := newBindingServiceForTest(t, store, router, now)

	_, err = service.Unbind(context.Background(), UnbindParams{
		WorkspaceID: store.row.WorkspaceID,
		AgentID:     store.row.AgentID,
		BindingMode: BindingModeMessage,
	})
	if err != nil {
		t.Fatalf("Unbind() error = %v", err)
	}
	if len(router.unbindRequests) != 1 || router.unbindRequests[0].TenantID != "tenant-a" || !store.revoked {
		t.Fatalf("unbind requests=%#v revoked=%v", router.unbindRequests, store.revoked)
	}
}

func TestBindDigitalEmployeeCompensatesUnexpectedRouterSubscription(t *testing.T) {
	now := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)
	agentID := uuidForTest(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	router := &fakeDirectBindingRouter{
		fakeBindingRouter: &fakeBindingRouter{
			issued: BindingToken{BindingToken: "bat_v1.server-only-secret", ExpiresAt: now.Add(5 * time.Minute)},
			bindingChecks: []DigitalEmployeeBindingCheck{{
				AgentID: uuidStringForTest(agentID), Platform: "dingtalk", TenantID: "tenant-a",
				AccountID: "employee-a", Status: "unbound",
			}},
			unbindResult: DigitalEmployeeBindingUnbindResult{Status: "unbound"},
		},
		createResult: Subscription{
			SourceID: "source-channel-1", AgentID: uuidStringForTest(agentID),
			DispatchURL: "https://attacker.example/dispatch",
			Surface:     SubscriptionSurface{Type: DingTalkSurfaceAuto},
			Outbound:    SubscriptionOutbound{Mode: "dws", ReplyTo: "latest_message"},
			Status:      "active",
		},
	}
	service := newBindingServiceForTest(t, &fakeBindingStore{}, router, now)

	_, err := service.BindDigitalEmployee(context.Background(), DirectBindingParams{
		Agent: BeginAgent{
			ID: agentID, Name: "Agent A",
			Workspace: BeginWorkspace{
				ID: uuidForTest(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), Name: "Workspace A",
			},
		},
		InitiatorID:       uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"),
		TenantID:          "tenant-a",
		DigitalEmployeeID: "employee-a",
	})
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("BindDigitalEmployee() error = %v, want ErrInvalidResult", err)
	}
	if len(router.unbindRequests) != 1 || router.unbindRequests[0].AccountID != "employee-a" {
		t.Fatalf("compensating unbinds=%#v", router.unbindRequests)
	}
}

func TestBindDigitalEmployeeDoesNotCompensateSupersededLocalAttempt(t *testing.T) {
	now := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)
	store := &fakeBindingStore{activateErr: pgx.ErrNoRows}
	store.activateHook = func(current *fakeBindingStore) {
		config, err := ParseDingTalkAccountConfig(current.row.Config)
		if err != nil {
			t.Fatal(err)
		}
		config.CallbackTokenHash = strings.Repeat("a", 64)
		config.CallbackExpiresAt = now.Add(10 * time.Minute)
		current.row.Config, err = config.Marshal()
		if err != nil {
			t.Fatal(err)
		}
	}
	agentID := uuidForTest(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	router := &fakeDirectBindingRouter{fakeBindingRouter: &fakeBindingRouter{
		issued: BindingToken{BindingToken: "bat_v1.server-only-secret", ExpiresAt: now.Add(5 * time.Minute)},
		bindingChecks: []DigitalEmployeeBindingCheck{{
			AgentID: uuidStringForTest(agentID), Platform: "dingtalk", TenantID: "tenant-a",
			AccountID: "employee-a", Status: "unbound",
		}},
		unbindResult: DigitalEmployeeBindingUnbindResult{Status: "unbound"},
	}}
	service := newBindingServiceForTest(t, store, router, now)

	_, err := service.BindDigitalEmployee(context.Background(), DirectBindingParams{
		Agent: BeginAgent{
			ID: agentID, Name: "Agent A",
			Workspace: BeginWorkspace{
				ID: uuidForTest(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), Name: "Workspace A",
			},
		},
		InitiatorID:       uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"),
		TenantID:          "tenant-a",
		DigitalEmployeeID: "employee-a",
	})
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("BindDigitalEmployee() error = %v, want ErrBindingConflict", err)
	}
	if len(router.unbindRequests) != 0 {
		t.Fatalf("superseded attempt compensated newer ownership: %#v", router.unbindRequests)
	}
}
