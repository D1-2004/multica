package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeDingTalkBindingTeardownStore struct {
	agents       []db.Agent
	bindings     map[pgtype.UUID]db.ChannelInstallation
	lockErr      error
	backfillErr  error
	deleteErr    error
	backfills    int
	deletions    int
	lockedAgents []pgtype.UUID
}

func (f *fakeDingTalkBindingTeardownStore) LockAgents(
	_ context.Context,
	agentIDs []pgtype.UUID,
) ([]db.Agent, error) {
	f.lockedAgents = append([]pgtype.UUID(nil), agentIDs...)
	return append([]db.Agent(nil), f.agents...), f.lockErr
}

func (f *fakeDingTalkBindingTeardownStore) GetBindingForUpdate(
	_ context.Context,
	agentID pgtype.UUID,
) (db.ChannelInstallation, error) {
	row, ok := f.bindings[agentID]
	if !ok {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeDingTalkBindingTeardownStore) BackfillBindingAccountKey(
	_ context.Context,
	row db.ChannelInstallation,
	identity agentmessagerouter.DigitalEmployeeSourceIdentity,
) (db.ChannelInstallation, error) {
	if f.backfillErr != nil {
		return db.ChannelInstallation{}, f.backfillErr
	}
	f.backfills++
	config, err := agentmessagerouter.ParseDingTalkAccountConfig(row.Config)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	config.RouterPlatform = identity.Platform
	config.RouterTenantID = identity.TenantID
	config.RouterAccountID = identity.AccountID
	row.Config, err = config.Marshal()
	return row, err
}

func (f *fakeDingTalkBindingTeardownStore) DeleteBindingProjection(
	_ context.Context,
	_ db.ChannelInstallation,
	_ agentmessagerouter.DingTalkAccountConfig,
) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletions++
	return nil
}

type fakeDingTalkBindingTeardownRouter struct {
	unbindResult   agentmessagerouter.DigitalEmployeeBindingUnbindResult
	unbindErr      error
	sourceResult   agentmessagerouter.DigitalEmployeeSourceIdentityResult
	sourceErr      error
	subscription   agentmessagerouter.Subscription
	subscribeErr   error
	unbinds        []agentmessagerouter.DigitalEmployeeBindingKey
	sourceIDs      [][]string
	sourcesRead    []string
	unbindStarted  chan struct{}
	unbindContinue chan struct{}
}

func (f *fakeDingTalkBindingTeardownRouter) UnbindDigitalEmployeeBinding(
	_ context.Context,
	binding agentmessagerouter.DigitalEmployeeBindingKey,
) (agentmessagerouter.DigitalEmployeeBindingUnbindResult, error) {
	f.unbinds = append(f.unbinds, binding)
	if f.unbindStarted != nil {
		select {
		case f.unbindStarted <- struct{}{}:
		default:
		}
	}
	if f.unbindContinue != nil {
		<-f.unbindContinue
	}
	return f.unbindResult, f.unbindErr
}

func (f *fakeDingTalkBindingTeardownRouter) GetDigitalEmployeeSourceIdentities(
	_ context.Context,
	sourceIDs []string,
) (agentmessagerouter.DigitalEmployeeSourceIdentityResult, error) {
	f.sourceIDs = append(f.sourceIDs, append([]string(nil), sourceIDs...))
	return f.sourceResult, f.sourceErr
}

func (f *fakeDingTalkBindingTeardownRouter) GetSubscription(
	_ context.Context,
	sourceID string,
) (agentmessagerouter.Subscription, error) {
	f.sourcesRead = append(f.sourcesRead, sourceID)
	return f.subscription, f.subscribeErr
}

func TestTeardownDingTalkBindingsSynchronouslyUnbindsExactOwner(t *testing.T) {
	agentID := parseUUID("11111111-1111-4111-8111-111111111111")
	row := dingTalkBindingTeardownRow(t, agentID, "active", true)
	store := &fakeDingTalkBindingTeardownStore{
		agents:   []db.Agent{{ID: agentID}},
		bindings: map[pgtype.UUID]db.ChannelInstallation{agentID: row},
	}
	router := &fakeDingTalkBindingTeardownRouter{
		unbindResult: agentmessagerouter.DigitalEmployeeBindingUnbindResult{Status: "unbound"},
	}

	if err := teardownDingTalkBindings(context.Background(), store, router, []pgtype.UUID{agentID}); err != nil {
		t.Fatalf("teardownDingTalkBindings: %v", err)
	}
	if len(store.lockedAgents) != 1 || store.lockedAgents[0] != agentID {
		t.Fatalf("locked agents = %+v, want only %s", store.lockedAgents, agentID.String())
	}
	if len(router.unbinds) != 1 {
		t.Fatalf("unbind calls = %d, want 1", len(router.unbinds))
	}
	got := router.unbinds[0]
	if got.AgentID != agentID.String() || got.Platform != "dingtalk" ||
		got.TenantID != "tenant-1" || got.AccountID != "account-1" {
		t.Fatalf("unbind key = %+v", got)
	}
	if store.deletions != 1 {
		t.Fatalf("projection deletions = %d, want 1", store.deletions)
	}
}

func TestTeardownDingTalkBindingsAllowsAgentWithoutLocalBindingAndNoRouter(t *testing.T) {
	agentID := parseUUID("99999999-9999-4999-8999-999999999999")
	store := &fakeDingTalkBindingTeardownStore{
		agents:   []db.Agent{{ID: agentID}},
		bindings: map[pgtype.UUID]db.ChannelInstallation{},
	}
	if err := teardownDingTalkBindings(context.Background(), store, nil, []pgtype.UUID{agentID}); err != nil {
		t.Fatalf("teardown agent without binding: %v", err)
	}
}

func TestTeardownDingTalkBindingsFailsClosed(t *testing.T) {
	agentID := parseUUID("22222222-2222-4222-8222-222222222222")
	tests := []struct {
		name       string
		status     string
		unbindErr  error
		deleteErr  error
		wantUnbind int
	}{
		{name: "Router inconsistent", status: "inconsistent", wantUnbind: 1},
		{name: "Router unavailable", unbindErr: errors.New("timeout"), wantUnbind: 1},
		{name: "invalid Router response", status: "unexpected", wantUnbind: 1},
		{name: "local projection delete fails", status: "unbound", deleteErr: errors.New("database failed"), wantUnbind: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeDingTalkBindingTeardownStore{
				agents:    []db.Agent{{ID: agentID}},
				bindings:  map[pgtype.UUID]db.ChannelInstallation{agentID: dingTalkBindingTeardownRow(t, agentID, "active", true)},
				deleteErr: tt.deleteErr,
			}
			router := &fakeDingTalkBindingTeardownRouter{
				unbindResult: agentmessagerouter.DigitalEmployeeBindingUnbindResult{Status: tt.status},
				unbindErr:    tt.unbindErr,
			}

			if err := teardownDingTalkBindings(context.Background(), store, router, []pgtype.UUID{agentID}); err == nil {
				t.Fatal("teardownDingTalkBindings succeeded, want fail-closed error")
			}
			if len(router.unbinds) != tt.wantUnbind || store.deletions != 0 {
				t.Fatalf("unbinds=%d deletions=%d", len(router.unbinds), store.deletions)
			}
		})
	}
}

func TestTeardownDingTalkBindingsTreatsOwnershipChangeAsSafe(t *testing.T) {
	agentID := parseUUID("33333333-3333-4333-8333-333333333333")
	store := &fakeDingTalkBindingTeardownStore{
		agents:   []db.Agent{{ID: agentID}},
		bindings: map[pgtype.UUID]db.ChannelInstallation{agentID: dingTalkBindingTeardownRow(t, agentID, "active", true)},
	}
	router := &fakeDingTalkBindingTeardownRouter{
		unbindResult: agentmessagerouter.DigitalEmployeeBindingUnbindResult{Status: "ownership_changed"},
	}

	if err := teardownDingTalkBindings(context.Background(), store, router, []pgtype.UUID{agentID}); err != nil {
		t.Fatalf("teardownDingTalkBindings: %v", err)
	}
	if store.deletions != 1 {
		t.Fatalf("projection deletions = %d, want 1", store.deletions)
	}
}

func TestTeardownDingTalkBindingsEnrichesLegacyRowBeforeUnbind(t *testing.T) {
	agentID := parseUUID("44444444-4444-4444-8444-444444444444")
	store := &fakeDingTalkBindingTeardownStore{
		agents:   []db.Agent{{ID: agentID}},
		bindings: map[pgtype.UUID]db.ChannelInstallation{agentID: dingTalkBindingTeardownRow(t, agentID, "active", false)},
	}
	router := &fakeDingTalkBindingTeardownRouter{
		unbindResult: agentmessagerouter.DigitalEmployeeBindingUnbindResult{Status: "unbound"},
		sourceResult: agentmessagerouter.DigitalEmployeeSourceIdentityResult{Sources: []agentmessagerouter.DigitalEmployeeSourceIdentity{{
			SourceID: "source-1", Platform: "dingtalk", TenantID: "tenant-1", AccountID: "account-1",
			SourceType: "digital_employee", Domain: "channel",
		}}},
		subscription: agentmessagerouter.Subscription{
			SourceID: "source-1", AgentID: agentID.String(),
			DispatchURL: "/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw", Status: "active",
		},
	}

	if err := teardownDingTalkBindings(context.Background(), store, router, []pgtype.UUID{agentID}); err != nil {
		t.Fatalf("teardownDingTalkBindings: %v", err)
	}
	if store.backfills != 1 || len(router.sourceIDs) != 1 || len(router.sourcesRead) != 0 || len(router.unbinds) != 1 {
		t.Fatalf("backfills=%d identity lookups=%d subscription lookups=%d unbinds=%d", store.backfills, len(router.sourceIDs), len(router.sourcesRead), len(router.unbinds))
	}
}

func TestTeardownDingTalkBindingsAcceptsConfiguredPublicDispatchURLForLegacyRow(t *testing.T) {
	agentID := parseUUID("88888888-8888-4888-8888-888888888888")
	store := &fakeDingTalkBindingTeardownStore{
		agents:   []db.Agent{{ID: agentID}},
		bindings: map[pgtype.UUID]db.ChannelInstallation{agentID: dingTalkBindingTeardownRow(t, agentID, "active", false)},
	}
	router := &fakeDingTalkBindingTeardownRouter{
		unbindResult: agentmessagerouter.DigitalEmployeeBindingUnbindResult{Status: "unbound"},
		sourceResult: agentmessagerouter.DigitalEmployeeSourceIdentityResult{Sources: []agentmessagerouter.DigitalEmployeeSourceIdentity{{
			SourceID: "source-1", Platform: "dingtalk", TenantID: "tenant-1", AccountID: "account-1",
			SourceType: "digital_employee", Domain: "channel",
		}}},
		subscription: agentmessagerouter.Subscription{
			SourceID: "source-1", AgentID: agentID.String(),
			DispatchURL: "https://multica.example/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw", Status: "active",
		},
	}

	if _, err := teardownDingTalkBindingsLocked(
		context.Background(), store, router, []pgtype.UUID{agentID}, "https://multica.example",
	); err != nil {
		t.Fatalf("teardownDingTalkBindingsLocked: %v", err)
	}
}

func TestTeardownDingTalkBindingsDoesNotRequireLegacyActiveSubscriptionOwner(t *testing.T) {
	agentID := parseUUID("66666666-6666-4666-8666-666666666666")
	store := &fakeDingTalkBindingTeardownStore{
		agents:   []db.Agent{{ID: agentID}},
		bindings: map[pgtype.UUID]db.ChannelInstallation{agentID: dingTalkBindingTeardownRow(t, agentID, "active", false)},
	}
	router := &fakeDingTalkBindingTeardownRouter{
		unbindResult: agentmessagerouter.DigitalEmployeeBindingUnbindResult{Status: "unbound"},
		sourceResult: agentmessagerouter.DigitalEmployeeSourceIdentityResult{Sources: []agentmessagerouter.DigitalEmployeeSourceIdentity{{
			SourceID: "source-1", Platform: "dingtalk", TenantID: "tenant-1", AccountID: "account-1",
			SourceType: "digital_employee", Domain: "channel",
		}}},
		subscribeErr: errors.New("subscription disabled"),
		subscription: agentmessagerouter.Subscription{
			SourceID: "source-1", AgentID: "77777777-7777-4777-8777-777777777777",
			DispatchURL: "/api/webhooks/agent-dispatch/v1_AQIDBAUGBwgJCgsMDQ4PEA", Status: "inactive",
		},
	}

	if err := teardownDingTalkBindings(context.Background(), store, router, []pgtype.UUID{agentID}); err != nil {
		t.Fatalf("teardownDingTalkBindings: %v", err)
	}
	if store.backfills != 1 || store.deletions != 1 || len(router.sourcesRead) != 0 || len(router.unbinds) != 1 {
		t.Fatalf("backfills=%d deletions=%d subscription reads=%d unbinds=%d", store.backfills, store.deletions, len(router.sourcesRead), len(router.unbinds))
	}
}

func TestTeardownDingTalkBindingsRejectsInvalidLegacySourceIdentity(t *testing.T) {
	agentID := parseUUID("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	tests := []struct {
		name     string
		identity agentmessagerouter.DigitalEmployeeSourceIdentity
	}{
		{name: "wrong platform", identity: agentmessagerouter.DigitalEmployeeSourceIdentity{
			SourceID: "source-1", Platform: "lark", TenantID: "tenant-1", AccountID: "account-1",
			SourceType: "digital_employee", Domain: "channel",
		}},
		{name: "wrong domain", identity: agentmessagerouter.DigitalEmployeeSourceIdentity{
			SourceID: "source-1", Platform: "dingtalk", TenantID: "tenant-1", AccountID: "account-1",
			SourceType: "digital_employee", Domain: "calendar",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeDingTalkBindingTeardownStore{
				agents:   []db.Agent{{ID: agentID}},
				bindings: map[pgtype.UUID]db.ChannelInstallation{agentID: dingTalkBindingTeardownRow(t, agentID, "active", false)},
			}
			router := &fakeDingTalkBindingTeardownRouter{
				sourceResult: agentmessagerouter.DigitalEmployeeSourceIdentityResult{Sources: []agentmessagerouter.DigitalEmployeeSourceIdentity{tt.identity}},
			}
			if err := teardownDingTalkBindings(context.Background(), store, router, []pgtype.UUID{agentID}); err == nil {
				t.Fatal("teardownDingTalkBindings succeeded for invalid source identity")
			}
			if store.backfills != 0 || len(router.unbinds) != 0 {
				t.Fatalf("backfills=%d unbinds=%d, want no mutation", store.backfills, len(router.unbinds))
			}
		})
	}
}

func TestTeardownDingTalkBindingsRejectsPendingAndMissingAgentLock(t *testing.T) {
	agentID := parseUUID("55555555-5555-4555-8555-555555555555")
	router := &fakeDingTalkBindingTeardownRouter{}

	for _, store := range []*fakeDingTalkBindingTeardownStore{
		{agents: nil, bindings: map[pgtype.UUID]db.ChannelInstallation{}},
		{agents: []db.Agent{{ID: agentID}}, bindings: map[pgtype.UUID]db.ChannelInstallation{
			agentID: dingTalkBindingTeardownRow(t, agentID, "pending", false),
		}},
	} {
		if err := teardownDingTalkBindings(context.Background(), store, router, []pgtype.UUID{agentID}); err == nil {
			t.Fatal("teardownDingTalkBindings succeeded, want fail-closed error")
		}
	}
	if len(router.unbinds) != 0 {
		t.Fatalf("unbind calls = %d, want 0", len(router.unbinds))
	}
}

func dingTalkBindingTeardownRow(
	t *testing.T,
	agentID pgtype.UUID,
	status string,
	withAccountKey bool,
) db.ChannelInstallation {
	t.Helper()
	boundAt := time.Unix(1_700_000_000, 0).UTC()
	config := agentmessagerouter.DingTalkAccountConfig{
		SchemaVersion: 1, DispatchEndpointID: "v1_AAECAwQFBgcICQoLDA0ODw", DispatchKeyID: "v1",
		DispatchURL: "/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw", RouterSourceID: "source-1",
		MessageScope: agentmessagerouter.DingTalkMessageScopeDirectOnly, BoundAt: &boundAt,
	}
	if withAccountKey {
		config.RouterPlatform = "dingtalk"
		config.RouterTenantID = "tenant-1"
		config.RouterAccountID = "account-1"
	}
	raw, err := config.Marshal()
	if err != nil {
		t.Fatalf("Marshal config: %v", err)
	}
	return db.ChannelInstallation{
		ID: parseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), AgentID: agentID,
		WorkspaceID: parseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		ChannelType: agentmessagerouter.ChannelTypeDingTalkAccount, Config: raw, Status: status,
	}
}
