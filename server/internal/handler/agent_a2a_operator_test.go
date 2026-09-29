package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

func requireAgentA2AOperatorTestSchema(t *testing.T) {
	t.Helper()
	var available bool
	if err := testPool.QueryRow(context.Background(), `SELECT to_regclass('agent_a2a_operator_config') IS NOT NULL`).Scan(&available); err != nil || !available {
		t.Skip("A2A operator schema is not migrated in the handler test database")
	}
}

func configureAgentA2AOperatorTestHandler(t *testing.T, operatorEmails, forwardOrigins []string) {
	t.Helper()
	originalProvider := testHandler.configProvider
	testHandler.SetConfigProvider(func() Config {
		return Config{
			PublicURL:                "http://127.0.0.1:8080",
			A2AOperatorEmails:        operatorEmails,
			A2AForwardAllowedOrigins: forwardOrigins,
		}
	})
	t.Cleanup(func() { testHandler.SetConfigProvider(originalProvider) })
}

func decodeAgentA2AOperatorResponse(t *testing.T, recorder *httptest.ResponseRecorder) AgentA2AOperatorResponse {
	t.Helper()
	var response AgentA2AOperatorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode operator response: %v (%s)", err, recorder.Body.String())
	}
	return response
}

func TestAgentA2AOperatorConfigRequiresOperatorEmail(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	requireAgentA2AOperatorTestSchema(t)
	agentID, _, _ := privateAgentTestFixture(t)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_a2a_operator_config WHERE agent_id = $1`, agentID)
	})
	var operatorEmail string
	if err := testPool.QueryRow(context.Background(), `SELECT email FROM "user" WHERE id = $1`, testUserID).Scan(&operatorEmail); err != nil {
		t.Fatalf("load test user email: %v", err)
	}

	get := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		testHandler.GetAgentA2AOperatorConfig(recorder, withAgentA2AURLParams(
			newRequest(http.MethodGet, "/api/agents/"+agentID+"/a2a/operator", nil), "id", agentID))
		return recorder
	}
	putIdentity := func(body map[string]any) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		testHandler.UpdateAgentA2AOperatorIdentity(recorder, withAgentA2AURLParams(
			newRequest(http.MethodPut, "/api/agents/"+agentID+"/a2a/operator/dws-identity", body), "id", agentID))
		return recorder
	}

	// A workspace owner who is not a deployment operator sees nothing and can
	// change nothing.
	configureAgentA2AOperatorTestHandler(t, []string{"someone-else@multica.test"}, nil)
	if recorder := get(); recorder.Code != http.StatusOK || decodeAgentA2AOperatorResponse(t, recorder).Operator {
		t.Fatalf("non-operator GET = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := putIdentity(map[string]any{"uid": "5550001", "org_id": "7770001"}); recorder.Code != http.StatusForbidden {
		t.Fatalf("non-operator PUT = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}

	configureAgentA2AOperatorTestHandler(t, []string{"  " + operatorEmail + "  "}, nil)
	if recorder := putIdentity(map[string]any{"uid": "0123", "org_id": "7770001"}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("leading-zero uid accepted: %d", recorder.Code)
	}
	recorder := putIdentity(map[string]any{"uid": "5550001", "org_id": "7770001", "deap_agent_uuid": "18265b7f-ed66-42f7-b4be-c99dd20b2b62"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("operator PUT = %d: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeAgentA2AOperatorResponse(t, recorder)
	if !response.Operator || response.DWSIdentity == nil || response.DWSIdentity.UID != "5550001" ||
		response.DWSIdentity.OrgID != "7770001" || response.DWSIdentity.DEAPAgentUUID == nil {
		t.Fatalf("operator identity response = %+v", response)
	}

	recorder = httptest.NewRecorder()
	testHandler.DeleteAgentA2AOperatorIdentity(recorder, withAgentA2AURLParams(
		newRequest(http.MethodDelete, "/api/agents/"+agentID+"/a2a/operator/dws-identity", nil), "id", agentID))
	if recorder.Code != http.StatusOK || decodeAgentA2AOperatorResponse(t, recorder).DWSIdentity != nil {
		t.Fatalf("operator DELETE = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestAgentA2AForwardRoutesOnlyTheBoundClient(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	if testHandler.A2AService == nil {
		t.Skip("A2A service is not configured in the handler test harness")
	}
	requireAgentA2ATestSchema(t)
	requireAgentA2AOperatorTestSchema(t)
	ctx := context.Background()
	agentID, ownerID, _ := privateAgentTestFixture(t)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_a2a_operator_config WHERE agent_id = $1`, agentID)
	})
	var operatorEmail string
	if err := testPool.QueryRow(ctx, `SELECT email FROM "user" WHERE id = $1`, testUserID).Scan(&operatorEmail); err != nil {
		t.Fatalf("load test user email: %v", err)
	}

	targetCalls := 0
	var targetAuthorization string
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls++
		targetAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"forwarded":true}}`))
	}))
	defer target.Close()
	originalTransport := testHandler.a2aForwardTransport
	testHandler.a2aForwardTransport = target.Client().Transport
	t.Cleanup(func() { testHandler.a2aForwardTransport = originalTransport })

	localCalls := 0
	originalProtocol := testHandler.A2AProtocol
	testHandler.A2AProtocol = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		localCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	t.Cleanup(func() { testHandler.A2AProtocol = originalProtocol })

	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("secretbox: %v", err)
	}
	originalSecrets := testHandler.A2AService.PushSecrets
	testHandler.A2AService.PushSecrets = box
	t.Cleanup(func() { testHandler.A2AService.PushSecrets = originalSecrets })

	assignAgentA2ATestRuntime(t, agentID, "cloud", "opencode", agentA2ATestManagedOpenCodeRuntimeMetadata)
	configureAgentA2AOperatorTestHandler(t, []string{operatorEmail}, []string{target.URL})
	endpoint, boundClient, boundSecret := createAgentA2ATestCaller(t, agentID, ownerID)

	response := createAgentA2ATestClient(t, agentID, ownerID, map[string]any{"name": "other-caller"})
	if response.Code != http.StatusCreated {
		t.Fatalf("create second client = %d: %s", response.Code, response.Body.String())
	}
	var otherClient AgentA2AClientResponse
	if err := json.Unmarshal(response.Body.Bytes(), &otherClient); err != nil {
		t.Fatalf("decode second client: %v", err)
	}
	response = httptest.NewRecorder()
	testHandler.CreateAgentA2ACredential(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodPost, "/api/agents/"+agentID+"/a2a/clients/"+otherClient.ID+"/credentials", map[string]any{}),
		"id", agentID, "clientId", otherClient.ID,
	))
	if response.Code != http.StatusCreated {
		t.Fatalf("create second credential = %d: %s", response.Code, response.Body.String())
	}
	var otherSecret AgentA2ACredentialSecretResponse
	if err := json.Unmarshal(response.Body.Bytes(), &otherSecret); err != nil {
		t.Fatalf("decode second credential: %v", err)
	}

	putForward := func(body map[string]any) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		testHandler.UpdateAgentA2AOperatorForward(recorder, withAgentA2AURLParams(
			newRequest(http.MethodPut, "/api/agents/"+agentID+"/a2a/operator/forward", body), "id", agentID))
		return recorder
	}
	targetRPC := target.URL + "/api/a2a/agents/pre-target-agent-0001/v1"
	targetToken := randomAgentA2ATestToken(t)
	if recorder := putForward(map[string]any{"rpc_url": targetRPC, "token": targetToken}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous source client accepted: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := putForward(map[string]any{"rpc_url": targetRPC, "token": targetToken, "source_client_id": boundClient.ID}); recorder.Code != http.StatusOK {
		t.Fatalf("bind forward = %d: %s", recorder.Code, recorder.Body.String())
	}

	call := func(token, method string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/a2a/agents/"+endpoint.PublicAgentID+"/v1",
			strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"`+method+`","params":{"id":"tsk_x"}}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		request = withAgentA2AURLParams(request, "publicAgentId", endpoint.PublicAgentID)
		recorder := httptest.NewRecorder()
		testHandler.HandleAgentA2ARPC(recorder, request)
		return recorder
	}

	if recorder := call(boundSecret.Token, "GetTask", nil); recorder.Code != http.StatusOK || targetCalls != 1 || localCalls != 0 {
		t.Fatalf("bound client was not forwarded: status=%d target=%d local=%d body=%s", recorder.Code, targetCalls, localCalls, recorder.Body.String())
	}
	if targetAuthorization != "Bearer "+targetToken {
		t.Fatalf("target Authorization = %q", targetAuthorization)
	}
	if recorder := call(otherSecret.Token, "GetTask", nil); recorder.Code != http.StatusNoContent || targetCalls != 1 || localCalls != 1 {
		t.Fatalf("other client must stay local: status=%d target=%d local=%d", recorder.Code, targetCalls, localCalls)
	}
	if recorder := call(boundSecret.Token, "GetTask", map[string]string{agentA2AForwardedHeader: "spoofed"}); recorder.Code != http.StatusLoopDetected || targetCalls != 1 || localCalls != 1 {
		t.Fatalf("marked request on a forwarding hop: status=%d target=%d local=%d", recorder.Code, targetCalls, localCalls)
	}

	// An archived Agent starts no new turns through the forward, but its
	// existing forwarded tasks stay readable.
	if _, err := testPool.Exec(ctx, `UPDATE agent SET archived_at = now() WHERE id = $1`, agentID); err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	if recorder := call(boundSecret.Token, "SendMessage", nil); targetCalls != 1 || localCalls != 2 {
		t.Fatalf("archived Agent forwarded a new turn: status=%d target=%d local=%d", recorder.Code, targetCalls, localCalls)
	}
	if recorder := call(boundSecret.Token, "GetTask", nil); recorder.Code != http.StatusOK || targetCalls != 2 {
		t.Fatalf("archived Agent stopped forwarding reads: status=%d target=%d local=%d", recorder.Code, targetCalls, localCalls)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET archived_at = NULL WHERE id = $1`, agentID); err != nil {
		t.Fatalf("unarchive agent: %v", err)
	}

	// A scope the source client lacks is never forwarded; the local SDK
	// rejects it.
	if recorder := updateAgentA2ATestClient(t, agentID, ownerID, boundClient.ID, map[string]any{"scopes": []string{"read"}}); recorder.Code != http.StatusOK {
		t.Fatalf("narrow scopes = %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call(boundSecret.Token, "CancelTask", nil); targetCalls != 2 || localCalls != 3 {
		t.Fatalf("out-of-scope method was forwarded: status=%d target=%d local=%d", recorder.Code, targetCalls, localCalls)
	}

	// The target key stays bound to its first source client, also after the
	// forward is cleared and recreated.
	if recorder := putForward(map[string]any{"rpc_url": targetRPC, "token": targetToken, "source_client_id": otherClient.ID}); recorder.Code != http.StatusConflict {
		t.Fatalf("target key rebound to another source client: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder := httptest.NewRecorder()
	testHandler.DeleteAgentA2AOperatorForward(recorder, withAgentA2AURLParams(
		newRequest(http.MethodDelete, "/api/agents/"+agentID+"/a2a/operator/forward", nil), "id", agentID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("clear forward = %d", recorder.Code)
	}
	if recorder := putForward(map[string]any{"rpc_url": targetRPC, "token": targetToken, "source_client_id": otherClient.ID}); recorder.Code != http.StatusConflict {
		t.Fatalf("target key rebound after clearing: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := putForward(map[string]any{"rpc_url": targetRPC, "token": randomAgentA2ATestToken(t), "source_client_id": otherClient.ID}); recorder.Code != http.StatusOK {
		t.Fatalf("fresh target key for another client refused: %d %s", recorder.Code, recorder.Body.String())
	}
}

// randomAgentA2ATestToken returns a well-formed target key unique to this run.
// Forward token bindings are permanent, so tests must not share a key.
func randomAgentA2ATestToken(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("random token: %v", err)
	}
	token := "mca2a_" + hex.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM a2a_forward_token_binding WHERE token_sha256 = $1`, hex.EncodeToString(digest[:]))
	})
	return token
}

func TestAgentA2AOperatorForwardSealsTokenAndHidesIt(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	if testHandler.A2AService == nil {
		t.Skip("A2A service is not configured in the handler test harness")
	}
	requireAgentA2ATestSchema(t)
	requireAgentA2AOperatorTestSchema(t)
	agentID, ownerID, _ := privateAgentTestFixture(t)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_a2a_operator_config WHERE agent_id = $1`, agentID)
	})
	var operatorEmail string
	if err := testPool.QueryRow(context.Background(), `SELECT email FROM "user" WHERE id = $1`, testUserID).Scan(&operatorEmail); err != nil {
		t.Fatalf("load test user email: %v", err)
	}
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("secretbox: %v", err)
	}
	originalSecrets := testHandler.A2AService.PushSecrets
	testHandler.A2AService.PushSecrets = box
	t.Cleanup(func() { testHandler.A2AService.PushSecrets = originalSecrets })
	var _ *service.A2AService = testHandler.A2AService
	assignAgentA2ATestRuntime(t, agentID, "cloud", "opencode", agentA2ATestManagedOpenCodeRuntimeMetadata)
	configureAgentA2AOperatorTestHandler(t, []string{operatorEmail}, nil)
	_, soleClient, _ := createAgentA2ATestCaller(t, agentID, ownerID)

	putForward := func(body map[string]any) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		testHandler.UpdateAgentA2AOperatorForward(recorder, withAgentA2AURLParams(
			newRequest(http.MethodPut, "/api/agents/"+agentID+"/a2a/operator/forward", body), "id", agentID))
		return recorder
	}
	target := "https://pre-fde-workbench.dingtalk.com/api/a2a/agents/99c83573-1392-4265-87e1-9c10da75f8e4/v1"
	sealToken := randomAgentA2ATestToken(t)

	configureAgentA2AOperatorTestHandler(t, []string{operatorEmail}, nil)
	if recorder := putForward(map[string]any{"rpc_url": target, "token": sealToken}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("forward accepted with forwarding disabled: %d", recorder.Code)
	}

	configureAgentA2AOperatorTestHandler(t, []string{operatorEmail}, []string{"https://pre-fde-workbench.dingtalk.com"})
	if recorder := putForward(map[string]any{"rpc_url": target, "token": "not-a-key"}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid forward token accepted: %d", recorder.Code)
	}
	recorder := putForward(map[string]any{"rpc_url": target, "token": sealToken})
	if recorder.Code != http.StatusOK {
		t.Fatalf("operator forward PUT = %d: %s", recorder.Code, recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte(sealToken)) {
		t.Fatalf("forward token was returned: %s", recorder.Body.String())
	}
	response := decodeAgentA2AOperatorResponse(t, recorder)
	if response.Forward == nil || response.Forward.RPCURL != target || !response.Forward.Active || response.Forward.SourceClientID != soleClient.ID {
		t.Fatalf("forward response = %+v", response.Forward)
	}
	var sealed []byte
	if err := testPool.QueryRow(context.Background(), `SELECT forward_token_encrypted FROM agent_a2a_operator_config WHERE agent_id = $1`, agentID).Scan(&sealed); err != nil {
		t.Fatalf("load sealed token: %v", err)
	}
	if bytes.Contains(sealed, []byte(sealToken)) {
		t.Fatal("forward token is stored in plaintext")
	}
	if opened, err := box.Open(sealed); err != nil || string(opened) != sealToken {
		t.Fatalf("sealed token does not round-trip: %v", err)
	}
}
