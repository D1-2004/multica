package agentidentitygithub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStartOAuthPostsAgentContext(t *testing.T) {
	var gotPath string
	var gotBody OAuthStartRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"state":"state-1","authorizationUrl":"https://github.com/login/oauth/authorize"}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, Timeout: time.Second})
	result, err := client.StartOAuth(context.Background(), OAuthStartRequest{
		WorkspaceID: "workspace-1",
		AgentID:     "agent-1",
		UserID:      "user-1",
		ReturnURL:   "https://app.example.test/ws/agents/agent-1",
	})
	if err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}
	if gotPath != "/api/agent-identity/v1/connections/github/oauth/start" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody.WorkspaceID != "workspace-1" || gotBody.AgentID != "agent-1" || gotBody.UserID != "user-1" {
		t.Fatalf("unexpected body: %#v", gotBody)
	}
	if !result.OK || result.State != "state-1" || !strings.Contains(result.AuthorizationURL, "github.com") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestGetStatusUsesAgentIdentityQueryContract(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if r.URL.Path != "/api/agent-identity/v1/connections/github/status" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"connectionId":"connection-1","accountLogin":"octocat","accountId":"42","status":"ACTIVE","grantedScopes":"repo","refreshExpiresAt":1783665600000}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL})
	result, err := client.GetStatus(context.Background(), "workspace-1", "agent-1", "user-1")
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !strings.Contains(gotQuery, "workspaceId=workspace-1") || !strings.Contains(gotQuery, "agentId=agent-1") || !strings.Contains(gotQuery, "userId=user-1") {
		t.Fatalf("unexpected query = %q", gotQuery)
	}
	if !result.OK || result.ConnectionID != "connection-1" || result.AccountLogin != "octocat" || result.RefreshExpiresAt == nil {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestServiceErrorCarriesAgentIdentityErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"errorCode":"NEEDS_REAUTH","errorMessage":"GitHub needs reauthorization"}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL})
	_, err := client.TestConnection(context.Background(), "connection-1")
	if err == nil {
		t.Fatal("expected error")
	}
	svcErr, ok := err.(*ServiceError)
	if !ok {
		t.Fatalf("error type = %T", err)
	}
	if svcErr.StatusCode != http.StatusUnauthorized || svcErr.Code != "NEEDS_REAUTH" || svcErr.Message != "GitHub needs reauthorization" {
		t.Fatalf("unexpected service error: %#v", svcErr)
	}
}
