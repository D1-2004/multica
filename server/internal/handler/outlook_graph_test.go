package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/connectorconfig"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

func TestContextConfigAppSetupDoesNotAskForOutlookOrAgentMailKeys(t *testing.T) {
	if contextConfigAppSetup("outlook") != "automatic" || contextConfigAppSetup("agentmail") != "automatic" {
		t.Fatalf("outlook=%s agentmail=%s", contextConfigAppSetup("outlook"), contextConfigAppSetup("agentmail"))
	}
}

func TestEnvOutlookOAuthClientUsesDeploymentSecret(t *testing.T) {
	t.Setenv("OUTLOOK_CLIENT_ID", "  azure-id  ")
	t.Setenv("OUTLOOK_CLIENT_SECRET", "azure-secret")
	app, ok := connectorcatalog.Default().Lookup("outlook")
	if !ok {
		t.Fatal("outlook missing from the catalog")
	}
	client := envOutlookOAuthClient(app)
	if client.Source != connectorconfig.SourceEnv || client.ClientID != "azure-id" || client.ClientSecret != "azure-secret" ||
		client.CallbackMode != connectorconfig.CallbackProductionForward || client.Scopes != app.Scope ||
		client.AuthorizationEndpoint != app.AuthorizationEndpoint || client.TokenEndpoint != app.TokenEndpoint {
		t.Fatalf("source=%s callback=%s scopes=%q", client.Source, client.CallbackMode, client.Scopes)
	}
	t.Setenv("OUTLOOK_CLIENT_SECRET", "")
	if outlookEnvConfigured() {
		t.Fatal("an empty secret must not count as configured")
	}
}

func TestConnectorOAuthAccountLabelPrefersLoginThenMail(t *testing.T) {
	if got := connectorOAuthAccountLabel("octocat", "ada@outlook.com", "", "", "ada@outlook.com"); got != "octocat" {
		t.Fatalf("login label = %q", got)
	}
	if got := connectorOAuthAccountLabel("  ", "", "", "", "ada@outlook.com"); got != "ada@outlook.com" {
		t.Fatalf("graph label = %q", got)
	}
	if got := connectorOAuthAccountLabel("", "ada@outlook.com", "other@outlook.com", "pref@outlook.com", "upn@outlook.com"); got != "ada@outlook.com" {
		t.Fatalf("mail label = %q", got)
	}
	if connectorAccountGitHubHeader("https://api.github.com/user") != true ||
		connectorAccountGitHubHeader("https://graph.microsoft.com/v1.0/me") != false {
		t.Fatal("github header host check")
	}
}

func TestConnectorOAuthAccountReadsGraphWithoutTheGitHubHeader(t *testing.T) {
	const token = "graph-access-token"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-GitHub-Api-Version") != "" {
			t.Errorf("github header = %q", r.Header.Get("X-GitHub-Api-Version"))
		}
		_, _ = w.Write([]byte(`{"userPrincipalName":"ada@outlook.com","mail":"ada@outlook.com","accessToken":"` + token + `"}`))
	}))
	t.Cleanup(srv.Close)
	previous := catalogExternalClient
	catalogExternalClient = func(app connectorcatalog.App) *remotemcp.ExternalClient {
		return remotemcp.NewExternalClientWithTransport([]string{srv.Listener.Addr().String()}, srv.Client().Transport)
	}
	t.Cleanup(func() { catalogExternalClient = previous })
	app := connectorcatalog.App{Slug: "outlook", AccountURL: srv.URL + "/v1.0/me", Hosts: []string{srv.Listener.Addr().String()}}
	got := (&Handler{}).connectorOAuthAccount(context.Background(), app, token)
	if got != "ada@outlook.com" {
		t.Fatalf("account = %q", got)
	}
}

func TestOutlookGraphToolsCallGraphAndRetryUnauthorized(t *testing.T) {
	const stale = "stale-token"
	const fresh = "fresh-token"
	var calls int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.Contains(r.Header.Get("Authorization"), stale) && r.URL.Path == "/v1.0/me" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"InvalidAuthenticationToken","message":"secret ` + stale + `"}}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+fresh {
			t.Errorf("authorization = %q path=%s", r.Header.Get("Authorization"), r.URL.Path)
		}
		switch r.URL.Path {
		case "/v1.0/me":
			if r.URL.Query().Get("$select") == "" {
				t.Errorf("whoami query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"id":"1","displayName":"Ada","mail":"ada@outlook.com","userPrincipalName":"ada@outlook.com","accessToken":"` + fresh + `"}`))
		case "/v1.0/me/messages":
			if r.URL.Query().Get("$top") != "10" {
				t.Errorf("top = %q", r.URL.Query().Get("$top"))
			}
			_, _ = w.Write([]byte(`{"value":[{"subject":"Hello","isRead":false,"from":{"emailAddress":{"name":"Bob","address":"bob@outlook.com"}},"receivedDateTime":"2026-10-04T00:00:00Z","body":{"content":"` + fresh + `"}}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	previousOrigin := outlookGraphOrigin
	outlookGraphOrigin = srv.URL
	t.Cleanup(func() { outlookGraphOrigin = previousOrigin })
	client := remotemcp.NewExternalClientWithTransport([]string{srv.Listener.Addr().String()}, srv.Client().Transport)

	refreshes := 0
	raw, err := callOutlookGraph(context.Background(), client, "tools/call", map[string]any{"name": "whoami"}, func(rejected string) (string, error) {
		if rejected == "" {
			return stale, nil
		}
		if rejected != stale {
			t.Errorf("retry rejected = %q", rejected)
		}
		refreshes++
		return fresh, nil
	})
	if err != nil || refreshes != 1 || strings.Contains(string(raw), stale) || strings.Contains(string(raw), fresh) || !strings.Contains(string(raw), "ada@outlook.com") {
		t.Fatalf("whoami raw=%s refreshes=%d err=%v", raw, refreshes, err)
	}
	var parsed multicaMCPToolResult
	if json.Unmarshal(raw, &parsed) != nil || parsed.IsError || len(parsed.Content) != 1 || parsed.Content[0].Text == "" {
		t.Fatalf("whoami result = %+v", parsed)
	}

	messages, err := callOutlookGraph(context.Background(), client, "tools/call", map[string]any{
		"name": "list_messages", "arguments": json.RawMessage(`{"top":99}`),
	}, func(string) (string, error) { return fresh, nil })
	if err != nil || strings.Contains(string(messages), fresh) || !strings.Contains(string(messages), "Hello") || strings.Contains(string(messages), "body") {
		t.Fatalf("messages = %s %v", messages, err)
	}

	listed, err := callOutlookGraph(context.Background(), client, "tools/list", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := connectorRPCResult("tools/list", listed)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, item := range tools.(map[string]any)["tools"].([]map[string]any) {
		names = append(names, item["name"].(string))
	}
	if strings.Join(names, ",") != "whoami,list_messages,list_events,list_contacts" {
		t.Fatalf("tools = %v", names)
	}
	if _, err := connectorRPCResult("tools/call", messages); err != nil {
		t.Fatal(err)
	}

	unknown, status, err := outlookToolResult(context.Background(), client, fresh, map[string]any{"name": "send_mail"})
	if err != nil || status != 0 || !strings.Contains(string(unknown), "unknown Outlook tool") || strings.Contains(string(unknown), fresh) {
		t.Fatalf("unknown = %s %v", unknown, err)
	}
	if calls < 2 {
		t.Fatalf("graph calls = %d", calls)
	}
}
