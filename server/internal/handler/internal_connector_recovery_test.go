package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

func recoveryFixtureRPC(t *testing.T, f *catalogFixture, task, connector, method string, params any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/internal-connectors/"+connector+"/mcp", strings.NewReader(string(body)))
	for key, value := range map[string]string{"X-Actor-Source": "task_token", "X-Workspace-ID": testWorkspaceID, "X-Agent-ID": f.agentID, "X-Task-ID": task} {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	f.h.CallInternalConnector(rec, withURLParam(req, "connectorId", connector))
	return rec
}

func TestConnectorRecoveryInSameTaskPreservesBusinessGuards(t *testing.T) {
	f := newCatalogFixture(t)
	c := f.storeTools(t, f.create(t, f.gh))
	f.grantGlobally(t, c.ID)
	f.sealWorkspaceOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "recovery-token", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	f.provider.mu.Lock()
	f.provider.valid["recovery-token"] = true
	f.provider.mu.Unlock()
	task := f.task(t, nil)
	outage, requests := true, 0
	base := f.provider.Client().Transport
	catalogExternalClient = func(app connectorcatalog.App) *remotemcp.ExternalClient {
		return remotemcp.NewExternalClientWithTransport(app.Hosts, connectorTestRoundTrip(func(r *http.Request) (*http.Response, error) {
			requests++
			if outage {
				return nil, context.DeadlineExceeded
			}
			return base.RoundTrip(r)
		}))
	}
	call := func(name string, args any) *httptest.ResponseRecorder {
		return recoveryFixtureRPC(t, f, task, c.ID, "tools/call", map[string]any{"name": name, "arguments": args})
	}
	initial := recoveryFixtureRPC(t, f, task, c.ID, "tools/list", map[string]any{})
	if !strings.Contains(initial.Body.String(), connectorRecoverTool) || !strings.Contains(initial.Body.String(), connectorRecoveredCallTool) {
		t.Fatalf("current-Run recovery tools absent: %s", initial.Body.String())
	}
	t.Logf("startup wire response: %s", initial.Body.String())
	unavailable := call(connectorRecoverTool, map[string]any{})
	if !strings.Contains(unavailable.Body.String(), `\"status\":\"unavailable\"`) || strings.Contains(unavailable.Body.String(), `"tools"`) {
		t.Fatalf("failed recovery exposed a usable catalog: %s", unavailable.Body.String())
	}
	outage = false
	healthyCatalog := recoveryFixtureRPC(t, f, task, c.ID, "tools/list", map[string]any{})
	if strings.Contains(healthyCatalog.Body.String(), connectorRecoverTool) || !strings.Contains(healthyCatalog.Body.String(), `"name":"search"`) {
		t.Fatalf("healthy native catalog changed: %s", healthyCatalog.Body.String())
	}
	recovered := call(connectorRecoverTool, map[string]any{})
	var rpc struct {
		Result multicaMCPToolResult `json:"result"`
	}
	if err := json.Unmarshal(recovered.Body.Bytes(), &rpc); err != nil || rpc.Result.IsError {
		t.Fatalf("recovery RPC failed: %s %v", recovered.Body.String(), err)
	}
	var status struct {
		Status            string           `json:"status"`
		BusinessExecution bool             `json:"business_execution"`
		Tools             []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &status); err != nil || status.Status != "available" || status.BusinessExecution || len(status.Tools) != 1 || status.Tools[0]["name"] != "search" {
		t.Fatalf("recovery lost pinning or claimed execution: %+v %v", status, err)
	}
	t.Logf("recovery wire response: %s", recovered.Body.String())
	f.provider.mu.Lock()
	if len(f.provider.toolCalls) != 0 {
		t.Fatal("read-only recovery executed a business tool")
	}
	f.provider.mu.Unlock()
	invoked := call(connectorRecoveredCallTool, map[string]any{"tool_name": "search", "arguments": map[string]any{}})
	if !strings.Contains(invoked.Body.String(), "ok:search") {
		t.Fatalf("same-task recovered tool failed: %s", invoked.Body.String())
	}
	t.Logf("business wire response: %s", invoked.Body.String())
	// A failed business request is still one attempt, never recovery plus replay.
	outage = true
	beforeFailure := requests
	failedCall := call(connectorRecoveredCallTool, map[string]any{"tool_name": "search", "arguments": map[string]any{}})
	if !strings.Contains(failedCall.Body.String(), `"isError":true`) || requests != beforeFailure+1 {
		t.Fatalf("business error was hidden or retried: %s requests=%d/%d", failedCall.Body.String(), requests, beforeFailure)
	}
	outage = false
	before := requests
	for _, name := range []string{"create_issue", connectorRecoveredCallTool, connectorRecoverTool} {
		denied := call(connectorRecoveredCallTool, map[string]any{"tool_name": name, "arguments": map[string]any{}})
		if !strings.Contains(denied.Body.String(), `"error"`) || requests != before {
			t.Fatalf("recovery bypassed pinning or recursed: %s", denied.Body.String())
		}
	}
	f.provider.mu.Lock()
	if len(f.provider.toolCalls) != 1 || f.provider.toolCalls[0] != "search" {
		t.Fatalf("business work was replayed: %v", f.provider.toolCalls)
	}
	f.provider.mu.Unlock()
	if _, err := testPool.Exec(context.Background(), `DELETE FROM internal_connector_agent WHERE connector_id=$1 AND agent_id=$2`, c.ID, f.agentID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{connectorRecoverTool, connectorRecoveredCallTool} {
		denied := call(name, map[string]any{"tool_name": "search", "arguments": map[string]any{}})
		if denied.Code != http.StatusForbidden || requests != before {
			t.Fatalf("revoked recovery invoked upstream: %d %s", denied.Code, denied.Body.String())
		}
	}
}

func TestConnectorRecoveryRefusesPartialAndOversizedCatalog(t *testing.T) {
	for _, mode := range []string{"later-failure", "cursor-loop", "invalid-schema", "invalid-name", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			h := &Handler{InternalConnectorRedis: &semanticaTestRates{n: 1}, InternalConnectorClient: &http.Client{Transport: connectorTestRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				if mode == "later-failure" && calls == 2 {
					return nil, context.DeadlineExceeded
				}
				tool := map[string]any{"name": "read", "inputSchema": map[string]any{"type": "object"}}
				list := map[string]any{"tools": []any{tool}}
				if mode == "invalid-schema" {
					tool["inputSchema"] = nil
				} else if mode == "invalid-name" {
					tool["name"] = "%read"
				} else if mode == "oversized" {
					tool["description"] = strings.Repeat("x", connectorRecoveryMaxBytes)
				} else {
					list["nextCursor"] = "page2"
					if calls == 2 {
						tool["name"] = "read2"
					}
				}
				body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": list})
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}}
			result, err := h.recoverConnectorTools(context.Background(), internalConnector{Name: "Fixture", AuthMode: "none", UpstreamURL: "https://safe.example.test/mcp"}, "task", "agent", "ws")
			encoded, _ := json.Marshal(result)
			if err == nil || strings.Contains(string(encoded), `"tools"`) || !strings.Contains(string(encoded), `\"catalog_complete\":false`) {
				t.Fatalf("partial catalog escaped: %s %v", encoded, err)
			}
		})
	}
}

func TestConnectorDiscoveryBudgetStopsWaitWithoutChangingHealthyResult(t *testing.T) {
	block := false
	h := &Handler{InternalConnectorClient: &http.Client{Transport: connectorTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		if block {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read","inputSchema":{"type":"object"}}]}}`))}, nil
	})}}
	c := internalConnector{AuthMode: "none", UpstreamURL: "https://safe.example.test/mcp"}
	baseline, err := h.callInternalConnectorUpstream(context.Background(), c, "tools/list", connectorRPCParams{})
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := h.callConnectorDiscovery(context.Background(), c, connectorRPCParams{}, 20*time.Millisecond)
	a, _ := json.Marshal(baseline)
	b, _ := json.Marshal(bounded)
	if err != nil || string(a) != string(b) {
		t.Fatalf("healthy tools changed: %s / %s %v", a, b, err)
	}
	block = true
	baselineContext, stopBaseline := context.WithTimeout(context.Background(), 100*time.Millisecond)
	baselineStarted := time.Now()
	_, baselineErr := h.callInternalConnectorUpstream(baselineContext, c, "tools/list", connectorRPCParams{})
	baselineWait := time.Since(baselineStarted)
	stopBaseline()
	started := time.Now()
	_, err = h.callConnectorDiscovery(context.Background(), c, connectorRPCParams{}, 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("discovery budget did not stop wait: %v", err)
	}
	if !errors.Is(baselineErr, context.DeadlineExceeded) {
		t.Fatalf("baseline did not exercise the same outage: %v", baselineErr)
	}
	baselineStatus, _ := connectorUnavailableDiscovery(context.Background(), c, "", baselineErr)
	candidateStatus, _ := connectorUnavailableDiscovery(context.Background(), c, "", err)
	a, _ = json.Marshal(baselineStatus)
	b, _ = json.Marshal(candidateStatus)
	if string(a) != string(b) {
		t.Fatal("bounded discovery changed outage quality/status")
	}
	t.Logf("scaled outage baseline=%s candidate=%s; availability status and healthy catalog unchanged; not a real-cloud latency benchmark", baselineWait, time.Since(started))
}

func TestConnectorDiscoveryCancelledWaiterDoesNotBreakSharedRefresh(t *testing.T) {
	f := newCatalogFixture(t)
	c := f.create(t, f.gh)
	f.sealWorkspaceOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(10 * time.Second).Unix()})
	f.provider.mu.Lock()
	f.provider.refreshDelay = 250 * time.Millisecond
	f.provider.mu.Unlock()
	c, err := f.h.loadInternalConnector(context.Background(), testWorkspaceID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.WithValue(context.Background(), connectorDiscoveryWaitKey{}, true), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	first := c
	_, err = f.h.freshConnectorToken(short, &first, "")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 200*time.Millisecond {
		t.Fatalf("short discovery waiter did not leave: %v", err)
	}
	second := c
	token, err := f.h.freshConnectorToken(context.Background(), &second, "")
	if err != nil || token == "old-access" || token == "" {
		t.Fatalf("shared refresh failed after cancellation: %v", err)
	}
	if _, _, refreshes, _ := f.provider.counts(); refreshes != 1 {
		t.Fatalf("cancelled waiter duplicated refresh: %d", refreshes)
	}
}

func TestConnectorRecoveryCustomNamesDoNotNeedAliasRediscovery(t *testing.T) {
	f := newCatalogFixture(t)
	c := f.create(t, f.gh)
	f.grantGlobally(t, c.ID)
	if _, err := testPool.Exec(context.Background(), `UPDATE internal_connector SET catalog_slug='', auth_mode='none', upstream_url='https://safe.example.test/mcp' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{`DELETE FROM internal_connector_call_audit WHERE connector_id=$1`, `DELETE FROM internal_connector_agent WHERE connector_id=$1`, `DELETE FROM internal_connector WHERE id=$1`} {
			_, _ = testPool.Exec(context.Background(), query, c.ID)
		}
	})
	names := []string{strings.Repeat("long_name_", 6), "t_0123456789abcdef"}
	lists, calls := 0, 0
	f.h.InternalConnectorClient = &http.Client{Transport: connectorTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		var rpc struct {
			Method string             `json:"method"`
			Params connectorRPCParams `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			t.Fatal(err)
		}
		var result any
		if rpc.Method == "tools/list" {
			lists++
			tools := []map[string]any{}
			for _, name := range names {
				tools = append(tools, map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}})
			}
			result = map[string]any{"tools": tools}
		} else {
			calls++
			if rpc.Params.Name != names[calls-1] {
				t.Fatalf("recovered call lost the upstream name: %q", rpc.Params.Name)
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok:original-name"}}}
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	task := f.task(t, nil)
	recovered := recoveryFixtureRPC(t, f, task, c.ID, "tools/call", map[string]any{"name": connectorRecoverTool, "arguments": map[string]any{}})
	if !strings.Contains(recovered.Body.String(), names[0]) || !strings.Contains(recovered.Body.String(), `"status":"available"`) {
		t.Fatalf("custom catalog did not preserve original names: %s", recovered.Body.String())
	}
	for _, name := range names {
		invoked := recoveryFixtureRPC(t, f, task, c.ID, "tools/call", map[string]any{"name": connectorRecoveredCallTool, "arguments": map[string]any{"tool_name": name, "arguments": map[string]any{}}})
		if !strings.Contains(invoked.Body.String(), "ok:original-name") {
			t.Fatalf("original name call failed: %s", invoked.Body.String())
		}
	}
	if lists != 1 || calls != 2 {
		t.Fatalf("recovery added alias discovery or replay: lists=%d calls=%d", lists, calls)
	}
}
