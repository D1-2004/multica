package agentmessagerouter

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBeginIdentityBindingUsesUnifiedPageWithoutCreatingMessageInstallation(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	store := &fakeBindingStore{}
	router := &fakeBindingRouter{issued: BindingToken{
		BindingToken: "bat_v1.identity-binding-token",
		ExpiresAt:    now.Add(5 * time.Minute),
	}}
	service := newBindingServiceForTest(t, store, router, now)

	result, err := service.Begin(context.Background(), beginParamsForTest(
		uuidForTest(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		uuidForTest(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"),
		BindingModeIdentity,
	))
	if err != nil {
		t.Fatalf("Begin(identity) error = %v", err)
	}
	if store.row.ID.Valid {
		t.Fatalf("identity binding created message installation: %#v", store.row)
	}
	if result.BindingID != uuidStringForTest(store.identityAttempt.ID) {
		t.Fatalf("binding id = %q, want identity attempt %q", result.BindingID, uuidStringForTest(store.identityAttempt.ID))
	}
	_, rawFragment, ok := strings.Cut(result.QRCodeURL, "#")
	if !ok {
		t.Fatalf("QR code URL has no fragment: %q", result.QRCodeURL)
	}
	fragment, err := url.ParseQuery(rawFragment)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragment) != 10 || fragment.Get("bindingMode") != "identity" ||
		fragment.Get("bindingToken") != router.issued.BindingToken ||
		fragment.Get("agentName") != "Database Agent" ||
		fragment.Get("workspaceId") != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" ||
		fragment.Get("workspaceName") != "Database Workspace" ||
		fragment.Get("dispatchPath") != "/api/webhooks/agent-dispatch/v1_MzMzMzMzMzMzMzMzMzMzMw" ||
		fragment.Get("callbackUrl") != "https://multica.example/api/integrations/dingtalk/account-bindings/22222222-2222-2222-2222-222222222222/callback" {
		t.Fatalf("identity QR fragment = %#v", fragment)
	}
}

func TestMessageBindingCallbackStoresRouteWithoutChangingExecutionIdentity(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-1",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)
	setIdentityForTest(store)
	originalIdentity := store.identity

	binding, err := service.CompleteCallback(context.Background(), CallbackParams{
		BindingID:    store.row.ID,
		BindingMode:  BindingModeMessage,
		CallbackToken: canonicalCallbackToken,
		IdentityBinding: IdentityBindingResult{
			Status: "skipped",
		},
		MessageBinding: MessageBindingResult{
			Status:   "success",
			SourceID: "source-1",
		},
	})
	if err != nil {
		t.Fatalf("CompleteCallback(message) error = %v", err)
	}
	if !store.activated || store.identity != originalIdentity {
		t.Fatalf("route activated=%v identity=%#v", store.activated, store.identity)
	}
	if binding.DWSIdentity.Status != "active" || binding.DWSIdentity.Source != "identity" || binding.MessageRoute.Status != "active" {
		t.Fatalf("binding = %#v", binding)
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{originalIdentity.DwsUid, originalIdentity.OrgID, canonicalCallbackToken, "source-1"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public binding leaked %q: %s", secret, encoded)
		}
	}
}

func TestBeginIdentityBindingAllowsExistingMessageRoute(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	store := activeBindingStoreForUnbind(t, now)
	router := &fakeBindingRouter{issued: BindingToken{
		BindingToken: "bat_v1.identity-binding-token",
		ExpiresAt:    now.Add(5 * time.Minute),
	}}
	service := newBindingServiceForTest(t, store, router, now)

	result, err := service.Begin(context.Background(), beginParamsForTest(
		store.row.WorkspaceID,
		store.row.AgentID,
		uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"),
		BindingModeIdentity,
	))
	if err != nil {
		t.Fatalf("Begin(identity with active message route) error = %v", err)
	}
	if result.BindingID != uuidStringForTest(store.identityAttempt.ID) || !store.identityAttempt.ID.Valid {
		t.Fatalf("identity attempt = %#v result=%#v", store.identityAttempt, result)
	}
}

func TestIdentityBindingCallbackDoesNotCreateMessageRoute(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	store := &fakeBindingStore{}
	store.row.WorkspaceID = uuidForTest(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	store.row.AgentID = uuidForTest(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	store.row.InstallerUserID = uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc")
	store.identityAttempt = identityAttemptForTest(t, store, canonicalCallbackToken, now.Add(time.Minute))
	service := newBindingServiceForTest(t, store, &fakeBindingRouter{}, now)

	binding, err := service.CompleteCallback(context.Background(), CallbackParams{
		BindingID:     store.identityAttempt.ID,
		BindingMode:   BindingModeIdentity,
		CallbackToken: canonicalCallbackToken,
		IdentityBinding: IdentityBindingResult{
			Status:                  "success",
			AccountUID:              "24710833",
			AccountOrgID:            "439446171",
			AccountOrganizationName: "Alibaba Group",
			AccountDisplayName:      "Xu Mo",
		},
		MessageBinding: MessageBindingResult{Status: "skipped"},
	})
	if err != nil {
		t.Fatalf("CompleteCallback(identity) error = %v", err)
	}
	if store.row.ID.Valid || store.activated {
		t.Fatalf("identity callback created or activated message route: row=%#v activated=%v", store.row, store.activated)
	}
	if binding.DWSIdentity.Status != "active" || binding.DWSIdentity.Source != "identity" || binding.MessageRoute.Status != "unbound" {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestMessageRouteQueriesNeverMutateExecutionIdentityStorage(t *testing.T) {
	raw, err := os.ReadFile("../../../pkg/db/queries/dingtalk_account_binding.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	if strings.Contains(sql, "ActivateDingTalkAccountBindingWithIdentity") {
		t.Fatal("message activation still exposes an identity-writing query")
	}
	revokeStart := strings.Index(sql, "-- name: RevokeDingTalkAccountBinding :one")
	revokeEnd := strings.Index(sql, "-- name: GetActiveDingTalkAccountBindingByEndpoint :one")
	if revokeStart < 0 || revokeEnd <= revokeStart {
		t.Fatal("could not isolate message revoke query")
	}
	revokeQuery := sql[revokeStart:revokeEnd]
	if strings.Contains(revokeQuery, "DELETE FROM agent_dingtalk_identity") ||
		strings.Contains(revokeQuery, "INSERT INTO agent_dingtalk_identity") ||
		strings.Contains(revokeQuery, "UPDATE agent_dingtalk_identity") {
		t.Fatalf("message revoke mutates identity storage:\n%s", revokeQuery)
	}
}
