package agentmessagerouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestHTTPCallbackRouterRegisterUsesTrustedRobotAPI(t *testing.T) {
	const endpointID = "v1_AAECAwQFBgcICQoLDA0ODw"
	const dispatchPath = "/api/webhooks/agent-dispatch/" + endpointID
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/subscriptions/robots" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"code":"success","data":{"sourceId":"source-1","agentId":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","dispatchUrl":"/api/webhooks/agent-dispatch/v1_AAECAwQFBgcICQoLDA0ODw","surface":{"type":"chat"},"outbound":{"mode":"robot_sdk","replyTo":"latest_message"},"status":"active"}}`))
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	service := &HTTPCallbackRouterService{client: client}
	agentID := util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	sourceID, err := service.Register(context.Background(), dingtalk.HTTPCallbackEndpoint{
		EndpointID:  endpointID,
		DispatchURL: "https://multica.example" + dispatchPath,
	}, agentID, "robot-code-1", "client-id-1", "client-secret-1")
	if err != nil {
		t.Fatal(err)
	}
	if sourceID != "source-1" {
		t.Fatalf("source id = %q", sourceID)
	}
	surface, _ := body["surface"].(map[string]any)
	outbound, _ := body["outbound"].(map[string]any)
	if body["robotCode"] != "robot-code-1" || body["clientId"] != "client-id-1" ||
		body["clientSecret"] != "client-secret-1" || body["agentId"] != util.UUIDToString(agentID) ||
		body["dispatchUrl"] != dispatchPath ||
		body["replaceExistingBinding"] != true || surface["type"] != "chat" ||
		outbound["mode"] != "robot_sdk" || outbound["replyTo"] != "latest_message" {
		t.Fatalf("registration = %#v", body)
	}
}

func TestHTTPCallbackRouterRegisterRejectsInvalidEndpointID(t *testing.T) {
	service := &HTTPCallbackRouterService{}
	_, err := service.Register(context.Background(), dingtalk.HTTPCallbackEndpoint{
		EndpointID:  "invalid",
		DispatchURL: "https://multica.example/api/webhooks/agent-dispatch/invalid",
	}, util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"), "robot-code-1", "client-id", "client-secret")
	if err == nil {
		t.Fatal("expected invalid endpoint id to fail")
	}
}

func TestHTTPCallbackRouterRegisterRequiresRobotCode(t *testing.T) {
	service := &HTTPCallbackRouterService{}
	if _, err := service.Register(context.Background(), dingtalk.HTTPCallbackEndpoint{}, util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"), " ", "client-id", "client-secret"); err == nil {
		t.Fatal("expected missing robot code to fail")
	}
}

func TestNewHTTPCallbackRouterServiceRequiresDependencies(t *testing.T) {
	if _, err := NewHTTPCallbackRouterService(nil, nil); err == nil {
		t.Fatal("expected missing dependencies to fail")
	}
}
