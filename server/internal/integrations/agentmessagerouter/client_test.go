package agentmessagerouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestClientTargetIdentityCanonicalizesRouterBase(t *testing.T) {
	var identities []string
	for _, rawURL := range []string{
		"https://ROUTER.internal:443/api/",
		"https://router.internal/api",
	} {
		client, err := NewClient(ClientConfig{
			BaseURL:           rawURL,
			ServiceCredential: "service-secret",
		})
		if err != nil {
			t.Fatal(err)
		}
		identities = append(identities, client.TargetIdentity())
	}
	if identities[0] != identities[1] {
		t.Fatalf("equivalent Router bases have different targets: %q != %q", identities[0], identities[1])
	}
	if !regexp.MustCompile(`^router-target:v1:sha256:[a-f0-9]{64}$`).MatchString(identities[0]) {
		t.Fatalf("target identity = %q", identities[0])
	}

	other, err := NewClient(ClientConfig{
		BaseURL:           "https://router-prepub.internal/api",
		ServiceCredential: "service-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if other.TargetIdentity() == identities[0] {
		t.Fatal("different Router bases share a target identity")
	}
}

func TestClientRejectsAmbiguousRouterBasePath(t *testing.T) {
	for _, rawURL := range []string{
		"https://router.internal/api/../formal",
		"https://router.internal/api%2Fformal",
		"https://router.internal/api//formal",
	} {
		if _, err := NewClient(ClientConfig{
			BaseURL:           rawURL,
			ServiceCredential: "service-secret",
		}); err == nil {
			t.Fatalf("ambiguous Router base %q accepted", rawURL)
		}
	}
}

func TestClientIssuesBindingTokenWithServiceCredential(t *testing.T) {
	expiresAt := time.Date(2026, 7, 14, 10, 5, 0, 0, time.UTC)
	dispatchPath := "/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw"
	descriptor := bindingTokenDescriptorForTest(dispatchPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/account-binding-tokens" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer service-credential" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body) != 4 || body["agentId"] != descriptor.AgentID ||
			body["name"] != descriptor.Name || body["dispatchPath"] != dispatchPath {
			t.Fatalf("request body = %#v", body)
		}
		workspace, ok := body["workspace"].(map[string]any)
		if !ok || len(workspace) != 2 || workspace["id"] != descriptor.Workspace.ID ||
			workspace["name"] != descriptor.Workspace.Name {
			t.Fatalf("request workspace = %#v", body["workspace"])
		}
		if _, found := body["dispatchUrl"]; found {
			t.Fatalf("request must not contain full dispatch URL: %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bindingToken": "bat_v1.secret-value",
			"expiresAt":    expiresAt.Format(time.RFC3339),
		})
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{
		BaseURL:           server.URL,
		ServiceCredential: "service-credential",
		HTTPClient:        server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := client.IssueBindingToken(context.Background(), descriptor)
	if err != nil {
		t.Fatalf("IssueBindingToken: %v", err)
	}
	if got.BindingToken != "bat_v1.secret-value" || !got.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("result = %#v", got)
	}
}

func TestClientChecksDigitalEmployeeBindingsByAccountKey(t *testing.T) {
	requests := []DigitalEmployeeBindingKey{
		{
			AgentID:         "agent-a",
			Platform:        "dingtalk",
			TenantID:        "corp-a",
			AccountID:       "employee-a",
			ExpectedDomains: []string{"channel", "calendar", "approval", "future_domain"},
		},
		{
			AgentID:         "agent-b",
			Platform:        "dingtalk",
			TenantID:        "corp-b",
			AccountID:       "employee-b",
			ExpectedDomains: []string{"channel"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/digital-employee-bindings/check" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Bindings []DigitalEmployeeBindingKey `json:"bindings"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Bindings) != 2 || body.Bindings[0].AccountID != "employee-a" ||
			len(body.Bindings[0].ExpectedDomains) != 4 || body.Bindings[1].AgentID != "agent-b" {
			t.Fatalf("request body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]any{
				"bindings": []map[string]any{
					{
						"agentId": "agent-a", "platform": "dingtalk", "tenantId": "corp-a",
						"accountId": "employee-a", "status": "valid",
					},
					{
						"agentId": "agent-b", "platform": "dingtalk", "tenantId": "corp-b",
						"accountId": "employee-b", "status": "bound_to_other_agent", "currentAgentId": "agent-c",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := mustTestClient(t, server)
	got, err := client.CheckDigitalEmployeeBindings(context.Background(), requests)
	if err != nil {
		t.Fatalf("CheckDigitalEmployeeBindings: %v", err)
	}
	if len(got) != 2 || got[0].Status != "valid" || got[1].Status != "bound_to_other_agent" ||
		got[1].CurrentAgentID != "agent-c" {
		t.Fatalf("result = %#v", got)
	}
}

func TestClientConditionallyUnbindsCompleteAccountKey(t *testing.T) {
	request := DigitalEmployeeBindingKey{
		AgentID: "agent-a", Platform: "dingtalk", TenantID: "corp-a", AccountID: "employee-a",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/digital-employee-bindings/unbind" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 4 || body["agentId"] != "agent-a" || body["platform"] != "dingtalk" ||
			body["tenantId"] != "corp-a" || body["accountId"] != "employee-a" {
			t.Fatalf("request body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data":    map[string]any{"status": "ownership_changed"},
		})
	}))
	defer server.Close()

	got, err := mustTestClient(t, server).UnbindDigitalEmployeeBinding(context.Background(), request)
	if err != nil {
		t.Fatalf("UnbindDigitalEmployeeBinding: %v", err)
	}
	if got.Status != "ownership_changed" {
		t.Fatalf("result = %#v", got)
	}
}

func TestClientLooksUpSourceIdentitiesWithoutBindingRelation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/digital-employee-bindings/source-identities" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string][]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if got := body["sourceIds"]; len(got) != 2 || got[0] != "source-channel" || got[1] != "missing-source" {
			t.Fatalf("request body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]any{
				"sources": []map[string]any{{
					"sourceId": "source-channel", "platform": "dingtalk", "tenantId": "corp-a",
					"accountId": "employee-a", "sourceType": "digital_employee", "domain": "channel",
				}},
				"missingSourceIds": []string{"missing-source"},
			},
		})
	}))
	defer server.Close()

	got, err := mustTestClient(t, server).GetDigitalEmployeeSourceIdentities(
		context.Background(), []string{"source-channel", "missing-source"},
	)
	if err != nil {
		t.Fatalf("GetDigitalEmployeeSourceIdentities: %v", err)
	}
	if len(got.Sources) != 1 || got.Sources[0].AccountID != "employee-a" ||
		len(got.MissingSourceIDs) != 1 || got.MissingSourceIDs[0] != "missing-source" {
		t.Fatalf("result = %#v", got)
	}
}

func TestClientReturnsIncompleteSourceIdentityForPerRecordClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]any{
				"sources": []map[string]any{
					{
						"sourceId": "source-valid", "platform": "dingtalk", "tenantId": "corp-a",
						"accountId": "employee-a", "sourceType": "digital_employee", "domain": "channel",
					},
					{
						"sourceId": "source-missing-tenant", "platform": "dingtalk",
						"accountId": "employee-b", "sourceType": "digital_employee", "domain": "channel",
					},
				},
				"missingSourceIds": []string{},
			},
		})
	}))
	defer server.Close()

	got, err := mustTestClient(t, server).GetDigitalEmployeeSourceIdentities(
		context.Background(), []string{"source-valid", "source-missing-tenant"},
	)
	if err != nil {
		t.Fatalf("GetDigitalEmployeeSourceIdentities: %v", err)
	}
	if len(got.Sources) != 2 || got.Sources[0].TenantID != "corp-a" ||
		got.Sources[1].SourceID != "source-missing-tenant" || got.Sources[1].TenantID != "" {
		t.Fatalf("result = %#v", got)
	}
}

func TestNormalizeAgentDescriptorEnforcesBindingMetadataContract(t *testing.T) {
	dispatchPath := "/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw"
	valid := bindingTokenDescriptorForTest(dispatchPath)
	valid.Name = "  " + strings.Repeat("界", 256) + "  "
	valid.Workspace.Name = "  Workspace  "
	normalized, err := normalizeAgentDescriptor(valid)
	if err != nil {
		t.Fatalf("normalize 256-code-point descriptor: %v", err)
	}
	if normalized.Name != strings.Repeat("界", 256) || normalized.Workspace.Name != "Workspace" {
		t.Fatalf("normalized descriptor = %#v", normalized)
	}

	tests := []struct {
		name   string
		mutate func(*AgentDescriptor)
	}{
		{
			name: "workspace id is not standard UUID",
			mutate: func(value *AgentDescriptor) {
				value.Workspace.ID = "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA"
			},
		},
		{
			name: "agent name is blank",
			mutate: func(value *AgentDescriptor) {
				value.Name = " \t "
			},
		},
		{
			name: "agent name exceeds 256 code points",
			mutate: func(value *AgentDescriptor) {
				value.Name = strings.Repeat("A", 257)
			},
		},
		{
			name: "workspace name contains C0 control",
			mutate: func(value *AgentDescriptor) {
				value.Workspace.Name = "Work\nspace"
			},
		},
		{
			name: "workspace name contains C1 control",
			mutate: func(value *AgentDescriptor) {
				value.Workspace.Name = "Work\u009fspace"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			descriptor := bindingTokenDescriptorForTest(dispatchPath)
			tt.mutate(&descriptor)
			if _, err := normalizeAgentDescriptor(descriptor); err == nil {
				t.Fatalf("descriptor accepted: %#v", descriptor)
			}
		})
	}
}

func TestClientGetsAgentDeliveryTargetWithServiceCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/agent-delivery-targets/agent-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer service-credential" {
			t.Fatalf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agentId":     "agent-1",
			"dispatchUrl": "https://multica.example.com/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw",
		})
	}))
	defer server.Close()

	client := mustTestClient(t, server)
	target, err := client.GetAgentDeliveryTarget(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("GetAgentDeliveryTarget: %v", err)
	}
	if target.AgentID != "agent-1" || target.DispatchURL == "" {
		t.Fatalf("target = %#v", target)
	}
}

func TestClientMapsOnlyStableDeliveryTarget404ToSentinel(t *testing.T) {
	tests := []struct {
		name         string
		body         map[string]any
		wantNotFound bool
	}{
		{
			name:         "stable not found",
			body:         map[string]any{"code": "delivery_target_not_found", "message": "delivery target not found"},
			wantNotFound: true,
		},
		{
			name: "old router route missing",
			body: map[string]any{"code": "not_found", "message": "not found"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(tt.body)
			}))
			defer server.Close()

			client := mustTestClient(t, server)
			_, err := client.GetAgentDeliveryTarget(context.Background(), "agent-1")
			if got := errors.Is(err, ErrDeliveryTargetNotFound); got != tt.wantNotFound {
				t.Fatalf("errors.Is(ErrDeliveryTargetNotFound) = %v, error = %v", got, err)
			}
		})
	}
}

func TestClientRegistersRobotWithExplicitDispatchPolicy(t *testing.T) {
	dispatchURL := "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/subscriptions/robots" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer service-credential" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body) != 8 || body["tenantId"] != "tenant-1" ||
			body["robotCode"] != "robot-code-1" || body["agentId"] != "agent-1" ||
			body["clientId"] != "client-id-1" || body["clientSecret"] != "client-secret-1" ||
			body["dispatchUrl"] != dispatchURL {
			t.Fatalf("request body = %#v", body)
		}
		surface, _ := body["surface"].(map[string]any)
		if surface["type"] != "chat" {
			t.Fatalf("surface = %#v", surface)
		}
		outbound, _ := body["outbound"].(map[string]any)
		if outbound["mode"] != "robot_sdk" || outbound["replyTo"] != "latest_message" {
			t.Fatalf("outbound = %#v", outbound)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]any{
				"sourceId":    "source-1",
				"agentId":     "agent-1",
				"dispatchUrl": dispatchURL,
				"surface":     map[string]any{"type": "chat"},
				"outbound":    map[string]any{"mode": "robot_sdk", "replyTo": "latest_message"},
				"status":      "active",
			},
		})
	}))
	defer server.Close()

	client := mustTestClient(t, server)
	got, err := client.RegisterRobot(context.Background(), RobotRegistration{
		TenantID:     "tenant-1",
		RobotCode:    "robot-code-1",
		ClientID:     "client-id-1",
		ClientSecret: "client-secret-1",
		AgentID:      "agent-1",
		DispatchURL:  dispatchURL,
		Surface:      SubscriptionSurface{Type: "chat"},
		Outbound:     SubscriptionOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
	})
	if err != nil {
		t.Fatalf("RegisterRobot: %v", err)
	}
	if got.SourceID != "source-1" || got.AgentID != "agent-1" ||
		got.DispatchURL != dispatchURL || got.Surface.Type != "chat" ||
		got.Outbound.Mode != "robot_sdk" || got.Outbound.ReplyTo != "latest_message" ||
		got.Status != "active" {
		t.Fatalf("subscription = %#v", got)
	}
}

func TestClientRejectsRobotRegistrationWithoutAppCredentials(t *testing.T) {
	base := RobotRegistration{
		RobotCode:    "robot-code-1",
		ClientID:     "client-id-1",
		ClientSecret: "client-secret-1",
		AgentID:      "agent-1",
		DispatchURL:  "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint",
		Surface:      SubscriptionSurface{Type: "chat"},
		Outbound:     SubscriptionOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
	}
	for _, mutate := range []func(*RobotRegistration){
		func(r *RobotRegistration) { r.ClientID = "" },
		func(r *RobotRegistration) { r.ClientSecret = "" },
	} {
		registration := base
		mutate(&registration)
		if _, err := (&Client{}).RegisterRobot(context.Background(), registration); err == nil {
			t.Fatal("expected missing app credential to fail")
		}
	}
}

func TestClientRejectsRobotRegistrationResponseOutsideRequest(t *testing.T) {
	dispatchURL := "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint"
	tests := []struct {
		name string
		data map[string]any
	}{
		{name: "missing source", data: map[string]any{"agentId": "agent-1", "dispatchUrl": dispatchURL, "surface": map[string]any{"type": "chat"}, "outbound": map[string]any{"mode": "robot_sdk", "replyTo": "latest_message"}, "status": "active"}},
		{name: "different agent", data: map[string]any{"sourceId": "source-1", "agentId": "agent-2", "dispatchUrl": dispatchURL, "surface": map[string]any{"type": "chat"}, "outbound": map[string]any{"mode": "robot_sdk", "replyTo": "latest_message"}, "status": "active"}},
		{name: "different endpoint", data: map[string]any{"sourceId": "source-1", "agentId": "agent-1", "dispatchUrl": "https://other.example/dispatch", "surface": map[string]any{"type": "chat"}, "outbound": map[string]any{"mode": "robot_sdk", "replyTo": "latest_message"}, "status": "active"}},
		{name: "different surface", data: map[string]any{"sourceId": "source-1", "agentId": "agent-1", "dispatchUrl": dispatchURL, "surface": map[string]any{"type": "issue"}, "outbound": map[string]any{"mode": "robot_sdk", "replyTo": "latest_message"}, "status": "active"}},
		{name: "different outbound", data: map[string]any{"sourceId": "source-1", "agentId": "agent-1", "dispatchUrl": dispatchURL, "surface": map[string]any{"type": "chat"}, "outbound": map[string]any{"mode": "dws", "replyTo": "latest_message"}, "status": "active"}},
		{name: "inactive", data: map[string]any{"sourceId": "source-1", "agentId": "agent-1", "dispatchUrl": dispatchURL, "surface": map[string]any{"type": "chat"}, "outbound": map[string]any{"mode": "robot_sdk", "replyTo": "latest_message"}, "status": "inactive"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "success", "data": tt.data})
			}))
			defer server.Close()

			client := mustTestClient(t, server)
			_, err := client.RegisterRobot(context.Background(), RobotRegistration{
				RobotCode: "robot-code-1", ClientID: "client-id-1", ClientSecret: "client-secret-1",
				AgentID: "agent-1", DispatchURL: dispatchURL,
				Surface:  SubscriptionSurface{Type: "chat"},
				Outbound: SubscriptionOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
			})
			if err == nil {
				t.Fatal("expected invalid robot registration response")
			}
		})
	}
}

func TestClientGetsUpdatesAndDeletesSubscription(t *testing.T) {
	var deleteCalls int
	var patchCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer service-credential" {
			t.Fatal("missing service credential")
		}
		if r.URL.Path != "/api/subscriptions/source-1" &&
			r.URL.Path != "/api/subscriptions/source-1/surface" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"code":    "success",
				"data": map[string]any{
					"sourceId":    "source-1",
					"agentId":     "agent-1",
					"dispatchUrl": "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint",
					"surface":     map[string]any{"type": "issue"},
					"outbound":    map[string]any{"mode": "dws", "replyTo": "latest_message"},
					"status":      "active",
				},
			})
		case http.MethodDelete:
			deleteCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"code":    "success",
				"data": map[string]any{
					"sourceId":    "source-1",
					"agentId":     nil,
					"dispatchUrl": nil,
					"status":      "inactive",
				},
			})
		case http.MethodPatch:
			patchCalls++
			var body struct {
				AgentID string              `json:"agentId"`
				Surface SubscriptionSurface `json:"surface"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode patch: %v", err)
			}
			if body.AgentID != "agent-1" || body.Surface.Type != "chat" {
				t.Fatalf("patch body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"code":    "success",
				"data": map[string]any{
					"sourceId":    "source-1",
					"agentId":     "agent-1",
					"dispatchUrl": "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint",
					"surface":     map[string]any{"type": "chat"},
					"outbound":    map[string]any{"mode": "dws", "replyTo": "latest_message"},
					"status":      "active",
				},
			})
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{
		BaseURL:           server.URL,
		ServiceCredential: "service-credential",
		HTTPClient:        server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	subscription, err := client.GetSubscription(context.Background(), "source-1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if subscription.SourceID != "source-1" || subscription.AgentID != "agent-1" ||
		subscription.Surface.Type != "issue" || subscription.Outbound.Mode != "dws" ||
		subscription.Status != "active" {
		t.Fatalf("subscription = %#v", subscription)
	}
	updated, err := client.UpdateSubscriptionSurface(context.Background(), "source-1", "agent-1", "chat")
	if err != nil {
		t.Fatalf("UpdateSubscriptionSurface: %v", err)
	}
	if updated.Surface.Type != "chat" || updated.AgentID != "agent-1" || updated.Outbound.Mode != "dws" {
		t.Fatalf("updated subscription = %#v", updated)
	}
	if err := client.DeleteSubscription(context.Background(), "source-1"); err != nil {
		t.Fatalf("DeleteSubscription should treat missing as success: %v", err)
	}
	if deleteCalls != 1 || patchCalls != 1 {
		t.Fatalf("delete calls = %d patch calls = %d", deleteCalls, patchCalls)
	}
}

func TestClientUpdateSubscriptionSurfaceAcceptsAuto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AgentID string              `json:"agentId"`
			Surface SubscriptionSurface `json:"surface"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode patch: %v", err)
		}
		if body.AgentID != "agent-1" || body.Surface.Type != "auto" {
			t.Fatalf("patch body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]any{
				"sourceId":    "source-1",
				"agentId":     "agent-1",
				"dispatchUrl": "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint",
				"surface":     map[string]any{"type": "auto"},
				"outbound":    map[string]any{"mode": "dws", "replyTo": "latest_message"},
				"status":      "active",
			},
		})
	}))
	defer server.Close()

	client := mustTestClient(t, server)
	updated, err := client.UpdateSubscriptionSurface(
		context.Background(),
		"source-1",
		"agent-1",
		"auto",
	)
	if err != nil {
		t.Fatalf("UpdateSubscriptionSurface: %v", err)
	}
	if updated.Surface.Type != "auto" {
		t.Fatalf("surface type = %q, want auto", updated.Surface.Type)
	}
}

func TestClientDeletesAllDigitalEmployeeSubscriptionsForAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete ||
			r.URL.Path != "/api/subscriptions/digital-employees/agent-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer service-credential" {
			t.Fatal("missing service credential")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": []map[string]any{
				{"sourceId": "source-channel", "agentId": "agent-1", "status": "inactive"},
				{"sourceId": "source-calendar", "agentId": "agent-1", "status": "inactive"},
			},
		})
	}))
	defer server.Close()

	client := mustTestClient(t, server)
	if err := client.DeleteDigitalEmployeeSubscriptions(context.Background(), "agent-1"); err != nil {
		t.Fatalf("DeleteDigitalEmployeeSubscriptions: %v", err)
	}
}

func TestClientCreatesHTTPCallbackSubscriptionWithoutInventingTenant(t *testing.T) {
	dispatchURL := "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/subscriptions" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		source := body["source"].(map[string]any)
		if source["accountId"] != "robot-code-1" || source["tenantId"] != "" {
			t.Fatalf("source = %#v", source)
		}
		config := source["subscriptionConfig"].(map[string]any)
		if config["upstreamMode"] != "HTTP_CALLBACK" {
			t.Fatalf("subscription config = %#v", config)
		}
		if body["replaceExistingBinding"] != true {
			t.Fatalf("replaceExistingBinding = %#v", body["replaceExistingBinding"])
		}
		surface, _ := body["surface"].(map[string]any)
		outbound, _ := body["outbound"].(map[string]any)
		if surface["type"] != "chat" || outbound["mode"] != "robot_sdk" || outbound["replyTo"] != "latest_message" {
			t.Fatalf("dispatch policy = %#v %#v", surface, outbound)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "code": "success", "data": map[string]any{
				"sourceId": "source-robot", "agentId": "agent-1",
				"dispatchUrl": dispatchURL,
				"surface":     map[string]any{"type": "chat"},
				"outbound":    map[string]any{"mode": "robot_sdk", "replyTo": "latest_message"},
				"status":      "active",
			},
		})
	}))
	defer server.Close()

	client := mustTestClient(t, server)
	result, err := client.CreateHTTPCallbackSubscription(context.Background(), CreateSubscriptionParams{
		AccountID: "robot-code-1", AgentID: "agent-1", DispatchURL: dispatchURL,
		BindingToken:       "bat_v1.token",
		SubscriptionConfig: map[string]any{"upstreamMode": "HTTP_CALLBACK"},
		Surface:            SubscriptionSurface{Type: "chat"},
		Outbound:           SubscriptionOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
		ReplaceExisting:    true,
	})
	if err != nil {
		t.Fatalf("CreateHTTPCallbackSubscription: %v", err)
	}
	if result.SourceID != "source-robot" {
		t.Fatalf("source = %#v", result)
	}
}

func TestClientMapsHTTP200SubscriptionNotFoundToSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"code":    "business_error",
			"message": "subscription_not_found",
			"data":    nil,
		})
	}))
	defer server.Close()

	client := mustTestClient(t, server)
	_, err := client.GetSubscription(context.Background(), "source-1")
	if !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("error = %v, want ErrSubscriptionNotFound", err)
	}
	var routerError *RouterAPIError
	if !errors.As(err, &routerError) || routerError.Code != "business_error" {
		t.Fatalf("error = %#v, want typed RouterAPIError", err)
	}
}

func TestClientMapsHTTP404SubscriptionNotFoundToSentinelWithoutLeakingBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "subscription_not_found",
			"message": "Bearer service-credential callback-credential-secret",
		})
	}))
	defer server.Close()

	client := mustTestClient(t, server)
	_, err := client.GetSubscription(context.Background(), "source-1")
	if !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("error = %v, want ErrSubscriptionNotFound", err)
	}
	if containsAny(err.Error(), "service-credential", "callback-credential-secret") {
		t.Fatalf("unsafe error = %q", err)
	}
}

func TestClientClassifiesMalformedBackfillLookupResponses(t *testing.T) {
	tests := []struct {
		name string
		path string
		data map[string]any
		lookup func(*Client) error
	}{
		{
			name: "subscription",
			path: "/api/subscriptions/source-1",
			data: map[string]any{"sourceId": "source-1", "status": "active"},
			lookup: func(client *Client) error {
				_, err := client.GetSubscription(context.Background(), "source-1")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.path {
					t.Fatalf("path = %q, want %q", r.URL.Path, test.path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"code": "success",
					"data": test.data,
				})
			}))
			defer server.Close()

			err := test.lookup(mustTestClient(t, server))
			if !errors.Is(err, ErrRouterInvalidResponse) {
				t.Fatalf("error = %v, want ErrRouterInvalidResponse", err)
			}
		})
	}
}

func TestClientPreservesTypedBindingTokenBusinessErrorWithoutLeakingMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    "service_auth_not_configured",
			"message": "Bearer service-credential bat_v1.secret-value",
		})
	}))
	defer server.Close()

	client := mustTestClient(t, server)
	_, err := client.IssueBindingToken(context.Background(), bindingTokenDescriptorForTest("/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw"))
	var routerError *RouterAPIError
	if !errors.As(err, &routerError) || routerError.Code != "service_auth_not_configured" {
		t.Fatalf("error = %#v, want service_auth_not_configured", err)
	}
	if containsAny(err.Error(), "service-credential", "bat_v1.secret-value") {
		t.Fatalf("unsafe error = %q", err)
	}
}

func TestClientRejectsInvalidSuccessfulSubscriptionPayloads(t *testing.T) {
	tests := []struct {
		name      string
		data      map[string]any
		wantDrift bool
	}{
		{name: "different source", data: map[string]any{"sourceId": "source-2", "agentId": "agent-1", "dispatchUrl": "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint", "surface": map[string]any{"type": "auto"}, "outbound": map[string]any{"mode": "dws", "replyTo": "latest_message"}, "status": "active"}, wantDrift: true},
		{name: "inactive", data: map[string]any{"sourceId": "source-1", "agentId": "agent-1", "dispatchUrl": "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint", "surface": map[string]any{"type": "auto"}, "outbound": map[string]any{"mode": "dws", "replyTo": "latest_message"}, "status": "inactive"}, wantDrift: true},
		{name: "missing agent", data: map[string]any{"sourceId": "source-1", "dispatchUrl": "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint", "status": "active"}},
		{name: "missing dispatch url", data: map[string]any{"sourceId": "source-1", "agentId": "agent-1", "status": "active"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "success", "data": tt.data})
			}))
			defer server.Close()

			client := mustTestClient(t, server)
			result, err := client.GetSubscription(context.Background(), "source-1")
			if err == nil {
				t.Fatal("expected invalid subscription response")
			}
			if errors.Is(err, ErrSubscriptionDrift) != tt.wantDrift {
				t.Fatalf("error = %v, want drift=%v", err, tt.wantDrift)
			}
			if tt.wantDrift && result.SourceID != tt.data["sourceId"] {
				t.Fatalf("result = %#v, want mismatched snapshot preserved", result)
			}
		})
	}
}

func TestClientDeleteRequiresSuccessfulInactiveEnvelope(t *testing.T) {
	tests := []struct {
		name     string
		response map[string]any
		wantAPI  bool
	}{
		{
			name: "http 200 business error",
			response: map[string]any{
				"success": false,
				"code":    "internal_error",
				"message": "Bearer service-credential bat_v1.secret-value",
				"data":    nil,
			},
			wantAPI: true,
		},
		{
			name: "different source",
			response: map[string]any{
				"success": true,
				"code":    "success",
				"data":    map[string]any{"sourceId": "source-2", "agentId": nil, "dispatchUrl": nil, "status": "inactive"},
			},
		},
		{
			name: "still active",
			response: map[string]any{
				"success": true,
				"code":    "success",
				"data":    map[string]any{"sourceId": "source-1", "agentId": "agent-1", "dispatchUrl": "https://multica.example.com/dispatch", "status": "active"},
			},
		},
		{
			name: "partially null target",
			response: map[string]any{
				"success": true,
				"code":    "success",
				"data":    map[string]any{"sourceId": "source-1", "agentId": "agent-1", "dispatchUrl": nil, "status": "inactive"},
			},
		},
		{
			name: "blank target values",
			response: map[string]any{
				"success": true,
				"code":    "success",
				"data":    map[string]any{"sourceId": "source-1", "agentId": " ", "dispatchUrl": "\t", "status": "inactive"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := mustTestClient(t, server)
			err := client.DeleteSubscription(context.Background(), "source-1")
			if err == nil {
				t.Fatal("expected delete response rejection")
			}
			if containsAny(err.Error(), "service-credential", "bat_v1.secret-value") {
				t.Fatalf("unsafe error = %q", err)
			}
			if tt.wantAPI {
				var routerError *RouterAPIError
				if !errors.As(err, &routerError) || routerError.Code != "internal_error" {
					t.Fatalf("error = %#v, want internal_error", err)
				}
			}
		})
	}
}

func TestClientRejectsTrailingAndOversizedRouterResponses(t *testing.T) {
	valid := `{"bindingToken":"bat_v1.secret-value","expiresAt":"2026-07-14T10:05:00Z"}`
	tests := []struct {
		name string
		body string
	}{
		{name: "trailing json", body: valid + ` {"second":true}`},
		{name: "oversized trailing whitespace", body: valid + strings.Repeat(" ", maxRouterResponseBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := mustTestClient(t, server)
			if _, err := client.IssueBindingToken(context.Background(), bindingTokenDescriptorForTest("/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw")); err == nil {
				t.Fatal("expected malformed router response")
			}
		})
	}
}

func TestClientRejectsInvalidOrFailedResponsesWithoutLeakingCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"Bearer service-credential bat_v1.secret-value"}`))
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{
		BaseURL:           server.URL,
		ServiceCredential: "service-credential",
		HTTPClient:        server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.IssueBindingToken(context.Background(), bindingTokenDescriptorForTest("/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw"))
	if err == nil {
		t.Fatal("expected upstream error")
	}
	if got := err.Error(); got == "" || containsAny(got, "service-credential", "bat_v1.secret-value") {
		t.Fatalf("unsafe error = %q", got)
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if len(candidate) > 0 && len(value) >= len(candidate) {
			for i := 0; i+len(candidate) <= len(value); i++ {
				if value[i:i+len(candidate)] == candidate {
					return true
				}
			}
		}
	}
	return false
}

func mustTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		BaseURL:           server.URL,
		ServiceCredential: "service-credential",
		HTTPClient:        server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func bindingTokenDescriptorForTest(dispatchPath string) AgentDescriptor {
	return AgentDescriptor{
		AgentID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		Name:    "Database Agent",
		Workspace: WorkspaceDescriptor{
			ID:   "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			Name: "Database Workspace",
		},
		DispatchPath: dispatchPath,
	}
}
