package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

func TestConnectorDiscoveryAvailabilityBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, cursor string
		err          error
		want         bool
	}{
		{"timeout", "", fmt.Errorf("secret upstream URL: %w", context.DeadlineExceeded), true},
		{"network", "", &net.DNSError{Err: "secret", Name: "secret.example"}, true},
		{"http", "", connectorUpstreamStatusError{Code: 502}, true},
		{"reconnect", "", errConnectorReconnectRequired, true},
		{"invalid metadata", "", connectorUpstreamProtocolError{}, true},
		{"reserved collision", "", connectorControlCollisionError{}, true},
		{"unknown error", "", errors.New("secret configuration error"), false},
		{"later page", "page2", context.DeadlineExceeded, true},
		{"canceled", "", context.Canceled, false},
		{"healthy", "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, ok := connectorUnavailableDiscovery(context.Background(), internalConnector{Name: "GitHub"}, tc.cursor, tc.err)
			if ok != tc.want {
				t.Fatalf("diagnostic=%v want=%v", ok, tc.want)
			}
			if !ok {
				return
			}
			encoded, err := json.Marshal(result)
			if err != nil || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "nextCursor") {
				t.Fatalf("unsafe or paginated diagnostic: %s, %v", encoded, err)
			}
			tools := result.(map[string]any)["tools"].([]map[string]any)
			if len(tools) != 3 || tools[0]["name"] != connectorDiscoveryStatusTool || !strings.Contains(tools[0]["description"].(string), "report it blocked") {
				t.Fatalf("business failure not explicit: %#v", tools)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := connectorUnavailableDiscovery(ctx, internalConnector{}, "", context.DeadlineExceeded); ok {
		t.Fatal("parent cancellation was converted to a diagnostic")
	}
}

func TestConnectorDiscoveryDiagnosticIsOnlyAStatusRead(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `{"url":"secret"}`, `{"retry":true}`, `{} {}`} {
		if _, ok := connectorDiscoveryStatusResult(internalConnector{Name: "GitHub"}, json.RawMessage(input)); ok {
			t.Fatalf("accepted diagnostic arguments: %s", input)
		}
	}
	result, ok := connectorDiscoveryStatusResult(internalConnector{Name: "GitHub"}, json.RawMessage(`{}`))
	if !ok || result.IsError || len(result.Content) != 1 {
		t.Fatalf("status read failed: %+v", result)
	}
	var text map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &text); err != nil {
		t.Fatal(err)
	}
	if text["business_execution"] != false || text["recovery_verified"] != false || text["status"] != "discovery_unavailable" {
		t.Fatalf("diagnostic claimed business execution/recovery: %+v", text)
	}
}

func TestConnectorDiscoveryReservedToolCannotReachUpstream(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":%q,"inputSchema":{"type":"object"}}]}}`, connectorDiscoveryStatusTool)
	}))
	defer upstream.Close()
	h := &Handler{InternalConnectorClient: upstream.Client()}
	c := internalConnector{AuthMode: "none", UpstreamURL: upstream.URL}
	_, err := h.callInternalConnectorUpstream(context.Background(), c, "tools/list", connectorRPCParams{})
	if _, ok := connectorUnavailableDiscovery(context.Background(), c, "", err); err == nil || !ok {
		t.Fatalf("reserved upstream definitions were not rejected with explicit diagnostic: %v", err)
	}
}

func TestConnectorDiscoveryRelayIsolationAndRevocation(t *testing.T) {
	f := newCatalogFixture(t)
	c := f.storeTools(t, f.create(t, f.gh))
	f.grantGlobally(t, c.ID)
	f.sealWorkspaceOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "test-access", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	task := f.task(t, nil)
	upstreamRequests := 0
	catalogExternalClient = func(app connectorcatalog.App) *remotemcp.ExternalClient {
		return remotemcp.NewExternalClientWithTransport(app.Hosts, connectorTestRoundTrip(func(r *http.Request) (*http.Response, error) {
			upstreamRequests++
			return nil, fmt.Errorf("do not expose secret endpoint: %w", context.DeadlineExceeded)
		}))
	}
	call := func(method string, params any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
		req := httptest.NewRequest(http.MethodPost, "/api/internal-connectors/"+c.ID+"/mcp", strings.NewReader(string(body)))
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req.Header.Set("X-Agent-ID", f.agentID)
		req.Header.Set("X-Task-ID", task)
		rec := httptest.NewRecorder()
		f.h.CallInternalConnector(rec, withURLParam(req, "connectorId", c.ID))
		return rec
	}
	listed := call("tools/list", map[string]any{})
	var rpc struct {
		Result struct {
			Tools []struct{ Name, Description string }
		} `json:"result"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &rpc); err != nil || len(rpc.Result.Tools) != 3 || rpc.Result.Tools[0].Name != connectorDiscoveryStatusTool || strings.Contains(listed.Body.String(), "secret") {
		t.Fatalf("unavailable discovery did not survive the relay: %d %s (%v)", listed.Code, listed.Body.String(), err)
	}
	t.Logf("discovery wire response: %s", listed.Body.String())
	requests := upstreamRequests
	text, isError := f.relay(t, task, c.ID, connectorDiscoveryStatusTool)
	if isError || !strings.Contains(text, `"business_execution":false`) || upstreamRequests != requests {
		t.Fatalf("diagnostic performed work or lost status: %s error=%v requests=%d/%d", text, isError, upstreamRequests, requests)
	}
	if text, isError := f.relay(t, task, c.ID, "search"); !isError || strings.Contains(text, "secret") {
		t.Fatalf("business failure was hidden: %s error=%v", text, isError)
	}
	// A later failure isolates the server and explicitly marks incompleteness.
	later := call("tools/list", map[string]any{"cursor": "page2"})
	if strings.Contains(later.Body.String(), `"error"`) || !strings.Contains(later.Body.String(), connectorDiscoveryStatusTool) || !strings.Contains(later.Body.String(), `"multica_catalog_complete":false`) {
		t.Fatalf("partial discovery was hidden or blocked the runtime: %s", later.Body.String())
	}
	if _, err := testPool.Exec(context.Background(), `DELETE FROM internal_connector_agent WHERE connector_id = $1 AND agent_id = $2`, c.ID, f.agentID); err != nil {
		t.Fatal(err)
	}
	requests = upstreamRequests
	revoked := call("tools/call", map[string]any{"name": connectorDiscoveryStatusTool, "arguments": map[string]any{}})
	if revoked.Code != http.StatusForbidden || upstreamRequests != requests {
		t.Fatalf("revoked grant reached diagnostic/upstream: %d %s", revoked.Code, revoked.Body.String())
	}
	var failures, diagnostics int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE outcome = 'upstream_error'), count(*) FILTER (WHERE outcome = 'discovery_diagnostic') FROM internal_connector_call_audit WHERE connector_id = $1 AND task_id = $2`, c.ID, task).Scan(&failures, &diagnostics); err != nil || failures < 1 || diagnostics != 1 {
		t.Fatalf("failure/diagnostic audit lost: %d/%d %v", failures, diagnostics, err)
	}
}
