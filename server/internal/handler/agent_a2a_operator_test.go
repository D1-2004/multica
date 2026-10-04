package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/forwarding"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

func hashAgentA2ATestToken(token string) string { return auth.HashToken(token) }

const agentA2AForwardTestSecret = "forward-registration-secret-0123456789abcdef"

func requireAgentA2AOperatorTestSchema(t *testing.T) {
	t.Helper()
	var available bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT to_regclass('agent_a2a_operator_config') IS NOT NULL
		   AND to_regclass('a2a_forward_registration') IS NOT NULL
		   AND to_regclass('agent_a2a_forward_registrant') IS NOT NULL
		   AND to_regclass('agent_a2a_forward_client') IS NOT NULL
		   AND to_regclass('a2a_forward_token_binding') IS NOT NULL`).Scan(&available); err != nil || !available {
		t.Skip("A2A operator schema is not migrated in the handler test database")
	}
}

func configureAgentA2AOperatorTestHandler(t *testing.T, config Config) {
	t.Helper()
	if config.PublicURL == "" {
		config.PublicURL = "http://127.0.0.1:8080"
	}
	originalProvider := testHandler.configProvider
	testHandler.SetConfigProvider(func() Config { return config })
	t.Cleanup(func() { testHandler.SetConfigProvider(originalProvider) })
}

func useAgentA2AForwardTestSecrets(t *testing.T) *secretbox.Box {
	t.Helper()
	if testHandler.A2AService == nil {
		t.Skip("A2A service is not configured in the handler test harness")
	}
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("secretbox: %v", err)
	}
	original := testHandler.A2AService.PushSecrets
	testHandler.A2AService.PushSecrets = box
	t.Cleanup(func() { testHandler.A2AService.PushSecrets = original })
	return box
}

func agentA2AOperatorTestEmail(t *testing.T) string {
	t.Helper()
	var email string
	if err := testPool.QueryRow(context.Background(), `SELECT email FROM "user" WHERE id = $1`, testUserID).Scan(&email); err != nil {
		t.Fatalf("load test user email: %v", err)
	}
	return email
}

func cleanupAgentA2AOperatorRows(t *testing.T, agentID string, identities ...[2]string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_a2a_operator_config WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_dingtalk_identity WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM a2a_forward_token_binding WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_a2a_forward_registrant WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_a2a_forward_client WHERE agent_id = $1`, agentID)
		for _, identity := range identities {
			_, _ = testPool.Exec(ctx, `DELETE FROM a2a_forward_registration WHERE dws_uid = $1 AND org_id = $2`, identity[0], identity[1])
		}
	})
}

func decodeAgentA2AOperatorResponse(t *testing.T, recorder *httptest.ResponseRecorder) AgentA2AOperatorResponse {
	t.Helper()
	var response AgentA2AOperatorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode operator response: %v (%s)", err, recorder.Body.String())
	}
	return response
}

func putAgentA2AOperatorIdentity(agentID string, body map[string]any) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	testHandler.UpdateAgentA2AOperatorIdentity(recorder, withAgentA2AURLParams(
		newRequest(http.MethodPut, "/api/agents/"+agentID+"/a2a/operator/dws-identity", body), "id", agentID))
	return recorder
}

// randomAgentA2ATestToken returns a well-formed A2A key unique to this run.
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

// randomAgentA2ATestIdentity returns a digital employee identity unique to
// this run so registrations of parallel or earlier runs never collide.
func randomAgentA2ATestIdentity(t *testing.T) (string, string) {
	t.Helper()
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("random identity: %v", err)
	}
	n := int64(raw[0])<<24 | int64(raw[1])<<16 | int64(raw[2])<<8 | int64(raw[3])
	return strconv.FormatInt(9_000_000_000+n, 10), strconv.FormatInt(8_000_000_000+n, 10)
}

func TestAgentA2AOperatorIdentitySharesIntegrationsBinding(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	requireAgentA2AOperatorTestSchema(t)
	ctx := context.Background()
	agentID, _, _ := privateAgentTestFixture(t)
	cleanupAgentA2AOperatorRows(t, agentID)
	operatorEmail := agentA2AOperatorTestEmail(t)

	get := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		testHandler.GetAgentA2AOperatorConfig(recorder, withAgentA2AURLParams(
			newRequest(http.MethodGet, "/api/agents/"+agentID+"/a2a/operator", nil), "id", agentID))
		return recorder
	}

	configureAgentA2AOperatorTestHandler(t, Config{A2AOperatorEmails: []string{"someone-else@multica.test"}})
	if recorder := get(); recorder.Code != http.StatusOK || decodeAgentA2AOperatorResponse(t, recorder).Operator {
		t.Fatalf("non-operator GET = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := putAgentA2AOperatorIdentity(agentID, map[string]any{"uid": "5550001", "org_id": "7770001"}); recorder.Code != http.StatusForbidden {
		t.Fatalf("non-operator PUT = %d, want 403", recorder.Code)
	}

	configureAgentA2AOperatorTestHandler(t, Config{A2AOperatorEmails: []string{"  " + operatorEmail + " "}})
	if recorder := putAgentA2AOperatorIdentity(agentID, map[string]any{"uid": "0123", "org_id": "7"}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("leading-zero uid accepted: %d", recorder.Code)
	}
	recorder := putAgentA2AOperatorIdentity(agentID, map[string]any{
		"uid": "5550001", "org_id": "7770001", "display_name": "Tagggg",
		"organization_name": "钉钉", "deap_agent_uuid": "18265b7f-ed66-42f7-b4be-c99dd20b2b62",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("operator PUT = %d: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeAgentA2AOperatorResponse(t, recorder)
	if response.DWSIdentity == nil || !response.DWSIdentity.A2AEnabled || response.DWSIdentity.DisplayName != "Tagggg" {
		t.Fatalf("identity response = %+v", response.DWSIdentity)
	}
	if response.ProdForward != nil || response.ForwardTarget != nil {
		t.Fatalf("forward sections shown without forwarding configuration: %+v", response)
	}

	// The operator binding is the Integrations DingTalk identity row.
	var uid, orgID, name string
	if err := testPool.QueryRow(ctx, `SELECT dws_uid, org_id, account_display_name FROM agent_dingtalk_identity WHERE agent_id = $1`, agentID).Scan(&uid, &orgID, &name); err != nil {
		t.Fatalf("load shared identity: %v", err)
	}
	if uid != "5550001" || orgID != "7770001" || name != "Tagggg" {
		t.Fatalf("shared identity = %s/%s/%s", uid, orgID, name)
	}

	// A binding made in Integrations shows up here, still flagged for A2A.
	if _, err := testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET dws_uid = '5550002', account_display_name = 'Scanned' WHERE agent_id = $1`, agentID); err != nil {
		t.Fatalf("simulate Integrations rebinding: %v", err)
	}
	if response := decodeAgentA2AOperatorResponse(t, get()); response.DWSIdentity == nil || response.DWSIdentity.UID != "5550002" || !response.DWSIdentity.A2AEnabled {
		t.Fatalf("Integrations rebinding not visible: %+v", response.DWSIdentity)
	}

	recorder = httptest.NewRecorder()
	testHandler.DeleteAgentA2AOperatorIdentity(recorder, withAgentA2AURLParams(
		newRequest(http.MethodDelete, "/api/agents/"+agentID+"/a2a/operator/dws-identity", nil), "id", agentID))
	if recorder.Code != http.StatusOK || decodeAgentA2AOperatorResponse(t, recorder).DWSIdentity != nil {
		t.Fatalf("operator DELETE = %d %s", recorder.Code, recorder.Body.String())
	}
	var remaining int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_dingtalk_identity WHERE agent_id = $1`, agentID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("clearing here must clear the Integrations identity too: rows=%d err=%v", remaining, err)
	}

	// An Integrations-only binding is visible but not enabled for A2A.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_dingtalk_identity (agent_id, workspace_id, dws_uid, org_id, bound_by)
		SELECT id, workspace_id, '5550003', '7770003', $2 FROM agent WHERE id = $1`, agentID, testUserID); err != nil {
		t.Fatalf("simulate Integrations binding: %v", err)
	}
	if response := decodeAgentA2AOperatorResponse(t, get()); response.DWSIdentity == nil || response.DWSIdentity.A2AEnabled {
		t.Fatalf("Integrations-only binding = %+v", response.DWSIdentity)
	}
}

func signedAgentA2AForwardRegistration(t *testing.T, secret string, body map[string]any, at time.Time) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode registration: %v", err)
	}
	timestamp := strconv.FormatInt(at.UnixMilli(), 10)
	request := httptest.NewRequest(http.MethodPost, agentA2AForwardRegistrationPath, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(agentA2AForwardTimestampHeader, timestamp)
	request.Header.Set(agentA2AForwardSignatureHeader, forwarding.SignRegistration([]byte(secret), timestamp, raw))
	return request
}

func TestA2AForwardRegistrationEndpointRequiresSignature(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2AOperatorTestSchema(t)
	box := useAgentA2AForwardTestSecrets(t)
	uid, orgID := randomAgentA2ATestIdentity(t)
	cleanupAgentA2AOperatorRows(t, "00000000-0000-0000-0000-000000000000", [2]string{uid, orgID})
	configureAgentA2AOperatorTestHandler(t, Config{
		A2AForwardAllowedOrigins:     []string{"https://pre.example.test"},
		A2AForwardRegistrationSecret: agentA2AForwardTestSecret,
	})
	token := randomAgentA2ATestToken(t)
	rpcURL := "https://pre.example.test/api/a2a/agents/pre-target-agent-0001/v1"
	targetClientID := uuid.NewString()
	register := map[string]any{"action": "register", "dws_uid": uid, "org_id": orgID, "rpc_url": rpcURL, "target_client_id": targetClientID, "token": token, "target_agent_name": "Pre QwenTag"}
	call := func(request *http.Request) int {
		recorder := httptest.NewRecorder()
		testHandler.HandleA2AForwardRegistration(recorder, request)
		return recorder.Code
	}

	if code := call(signedAgentA2AForwardRegistration(t, "wrong-secret-wrong-secret-wrong-secret", register, time.Now())); code != http.StatusUnauthorized {
		t.Fatalf("wrong secret = %d, want 401", code)
	}
	if code := call(signedAgentA2AForwardRegistration(t, agentA2AForwardTestSecret, register, time.Now().Add(-10*time.Minute))); code != http.StatusUnauthorized {
		t.Fatalf("stale signature = %d, want 401", code)
	}
	outside := map[string]any{"action": "register", "dws_uid": uid, "org_id": orgID, "rpc_url": "https://evil.example/api/a2a/agents/pre-target-agent-0001/v1", "token": token}
	if code := call(signedAgentA2AForwardRegistration(t, agentA2AForwardTestSecret, outside, time.Now())); code != http.StatusBadRequest {
		t.Fatalf("origin outside the allow-list = %d, want 400", code)
	}
	withoutClient := map[string]any{"action": "register", "dws_uid": uid, "org_id": orgID, "rpc_url": rpcURL, "token": token}
	if code := call(signedAgentA2AForwardRegistration(t, agentA2AForwardTestSecret, withoutClient, time.Now())); code != http.StatusBadRequest {
		t.Fatalf("registration without target client = %d, want 400", code)
	}
	registeredAt := time.Now()
	if code := call(signedAgentA2AForwardRegistration(t, agentA2AForwardTestSecret, register, registeredAt)); code != http.StatusOK {
		t.Fatalf("valid registration = %d", code)
	}
	var sealed []byte
	var stored, storedClient string
	if err := testPool.QueryRow(context.Background(), `SELECT token_encrypted, rpc_url, target_client_id::text FROM a2a_forward_registration WHERE dws_uid = $1 AND org_id = $2`, uid, orgID).Scan(&sealed, &stored, &storedClient); err != nil {
		t.Fatalf("load registration: %v", err)
	}
	if opened, err := box.Open(sealed); err != nil || string(opened) != token || stored != rpcURL || storedClient != targetClientID || bytes.Contains(sealed, []byte(token)) {
		t.Fatalf("registration not stored correctly (rpc=%s client=%s err=%v)", stored, storedClient, err)
	}

	count := func() int {
		var n int
		_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM a2a_forward_registration WHERE dws_uid = $1 AND org_id = $2 AND token_encrypted IS NOT NULL`, uid, orgID).Scan(&n)
		return n
	}

	// A replay, or any registration signed before the stored one, changes nothing.
	older := map[string]any{"action": "register", "dws_uid": uid, "org_id": orgID, "rpc_url": "https://pre.example.test/api/a2a/agents/older-target-agent-01/v1", "target_client_id": uuid.NewString(), "token": randomAgentA2ATestToken(t)}
	if code := call(signedAgentA2AForwardRegistration(t, agentA2AForwardTestSecret, older, registeredAt.Add(-time.Minute))); code != http.StatusConflict {
		t.Fatalf("older registration = %d, want 409", code)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT rpc_url FROM a2a_forward_registration WHERE dws_uid = $1 AND org_id = $2`, uid, orgID).Scan(&stored); err != nil || stored != rpcURL {
		t.Fatalf("older registration replaced the newer one: %s %v", stored, err)
	}

	tokenSHA := agentA2AForwardTokenSHA256(token)
	// Withdrawal needs the registrant's own URL and the exact key.
	for name, body := range map[string]map[string]any{
		"foreign url": {"action": "revoke", "dws_uid": uid, "org_id": orgID, "rpc_url": "https://pre.example.test/api/a2a/agents/someone-else-agent-01/v1", "token_sha256": tokenSHA},
		"other key":   {"action": "revoke", "dws_uid": uid, "org_id": orgID, "rpc_url": rpcURL, "token_sha256": agentA2AForwardTokenSHA256("mca2a_other")},
	} {
		if code := call(signedAgentA2AForwardRegistration(t, agentA2AForwardTestSecret, body, time.Now())); code != http.StatusOK || count() != 1 {
			t.Fatalf("%s revoke = %d, rows=%d", name, code, count())
		}
	}
	if code := call(signedAgentA2AForwardRegistration(t, agentA2AForwardTestSecret, map[string]any{"action": "revoke", "dws_uid": uid, "org_id": orgID, "rpc_url": rpcURL}, time.Now())); code != http.StatusBadRequest {
		t.Fatalf("revoke without key digest = %d, want 400", code)
	}
	ownRevoke := map[string]any{"action": "revoke", "dws_uid": uid, "org_id": orgID, "rpc_url": rpcURL, "token_sha256": tokenSHA}
	// A withdrawal signed before the registration is a replay and keeps it.
	if code := call(signedAgentA2AForwardRegistration(t, agentA2AForwardTestSecret, ownRevoke, registeredAt.Add(-time.Second))); code != http.StatusOK || count() != 1 {
		t.Fatalf("earlier-signed revoke = %d, rows=%d", code, count())
	}
	if code := call(signedAgentA2AForwardRegistration(t, agentA2AForwardTestSecret, ownRevoke, time.Now())); code != http.StatusOK || count() != 0 {
		t.Fatalf("own revoke = %d, rows=%d", code, count())
	}
	// The withdrawal stays as a tombstone, so replaying the original
	// registration cannot bring it back.
	if code := call(signedAgentA2AForwardRegistration(t, agentA2AForwardTestSecret, register, registeredAt)); code != http.StatusConflict || count() != 0 {
		t.Fatalf("replayed registration after withdrawal = %d, rows=%d", code, count())
	}
}

func TestAgentA2AForwardUsesRegistrationForBoundIdentity(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	requireAgentA2AOperatorTestSchema(t)
	ctx := context.Background()
	box := useAgentA2AForwardTestSecrets(t)
	agentID, ownerID, _ := privateAgentTestFixture(t)
	uid, orgID := randomAgentA2ATestIdentity(t)
	cleanupAgentA2AOperatorRows(t, agentID, [2]string{uid, orgID})
	operatorEmail := agentA2AOperatorTestEmail(t)

	targetCalls := 0
	targetStatus := http.StatusOK
	rejectToken := ""
	var onReject func()
	var targetAuthorization string
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls++
		targetAuthorization = r.Header.Get("Authorization")
		if rejectToken != "" && targetAuthorization == "Bearer "+rejectToken {
			if onReject != nil {
				onReject()
			}
			w.Header().Set(agentA2AForwardKeyRejectedHeader, "revoked")
			writeAgentA2AUnauthorized(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(targetStatus)
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

	assignAgentA2ATestRuntime(t, agentID, "cloud", "opencode", agentA2ATestManagedOpenCodeRuntimeMetadata)
	configureAgentA2AOperatorTestHandler(t, Config{
		PublicURL:                    "https://multica-prod.example.test",
		A2AOperatorEmails:            []string{operatorEmail},
		A2AForwardAllowedOrigins:     []string{target.URL},
		A2AForwardRegistrationSecret: agentA2AForwardTestSecret,
	})
	endpoint, boundClient, boundSecret := createAgentA2ATestCaller(t, agentID, ownerID)
	response := createAgentA2ATestClient(t, agentID, ownerID, map[string]any{"name": "other-caller"})
	var otherClient AgentA2AClientResponse
	_ = json.Unmarshal(response.Body.Bytes(), &otherClient)
	response = httptest.NewRecorder()
	testHandler.CreateAgentA2ACredential(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodPost, "/api/agents/"+agentID+"/a2a/clients/"+otherClient.ID+"/credentials", map[string]any{}),
		"id", agentID, "clientId", otherClient.ID,
	))
	var otherSecret AgentA2ACredentialSecretResponse
	if err := json.Unmarshal(response.Body.Bytes(), &otherSecret); err != nil || otherSecret.Token == "" {
		t.Fatalf("create second credential: %v %s", err, response.Body.String())
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

	// No identity yet: served locally.
	if recorder := call(boundSecret.Token, "GetTask", nil); recorder.Code != http.StatusNoContent || targetCalls != 0 {
		t.Fatalf("unbound Agent forwarded: status=%d target=%d", recorder.Code, targetCalls)
	}
	if recorder := putAgentA2AOperatorIdentity(agentID, map[string]any{"uid": uid, "org_id": orgID}); recorder.Code != http.StatusOK {
		t.Fatalf("bind identity = %d %s", recorder.Code, recorder.Body.String())
	}
	// Bound but nothing registered: still local.
	if recorder := call(boundSecret.Token, "GetTask", nil); recorder.Code != http.StatusNoContent || targetCalls != 0 || localCalls != 2 {
		t.Fatalf("unregistered identity forwarded: status=%d target=%d local=%d", recorder.Code, targetCalls, localCalls)
	}

	registrationToken := randomAgentA2ATestToken(t)
	sealed, err := box.Seal([]byte(registrationToken))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO a2a_forward_registration (dws_uid, org_id, rpc_url, target_client_id, token_encrypted, token_sha256, target_agent_name, signed_at_ms)
		VALUES ($1, $2, $3, $4, $5, $6, 'Pre QwenTag', 1)`,
		uid, orgID, target.URL+"/api/a2a/agents/pre-target-agent-0001/v1", uuid.NewString(), sealed, agentA2AForwardTokenSHA256(registrationToken)); err != nil {
		t.Fatalf("insert registration: %v", err)
	}

	recorder := httptest.NewRecorder()
	testHandler.GetAgentA2AOperatorConfig(recorder, withAgentA2AURLParams(
		newRequest(http.MethodGet, "/api/agents/"+agentID+"/a2a/operator", nil), "id", agentID))
	if forward := decodeAgentA2AOperatorResponse(t, recorder).ForwardTarget; forward == nil || forward.AgentName != "Pre QwenTag" {
		t.Fatalf("production forward target = %+v", forward)
	}

	if recorder := call(boundSecret.Token, "GetTask", nil); recorder.Code != http.StatusOK || targetCalls != 1 {
		t.Fatalf("registered identity not forwarded: status=%d target=%d local=%d", recorder.Code, targetCalls, localCalls)
	}
	if targetAuthorization != "Bearer "+registrationToken {
		t.Fatalf("target Authorization = %q", targetAuthorization)
	}
	// The registration key now belongs to the first client; a second client
	// of the same Agent runs locally rather than sharing its tasks.
	if recorder := call(otherSecret.Token, "GetTask", nil); recorder.Code != http.StatusNoContent || targetCalls != 1 || localCalls != 3 {
		t.Fatalf("second client must stay local: status=%d target=%d local=%d", recorder.Code, targetCalls, localCalls)
	}
	if recorder := call(boundSecret.Token, "GetTask", map[string]string{agentA2AForwardedHeader: "spoofed"}); recorder.Code != http.StatusLoopDetected || targetCalls != 1 {
		t.Fatalf("marked request on a forwarding hop: status=%d target=%d", recorder.Code, targetCalls)
	}

	if _, err := testPool.Exec(ctx, `UPDATE agent SET archived_at = now() WHERE id = $1`, agentID); err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	if recorder := call(boundSecret.Token, "SendMessage", nil); targetCalls != 1 || localCalls != 4 {
		t.Fatalf("archived Agent forwarded a new turn: status=%d target=%d local=%d", recorder.Code, targetCalls, localCalls)
	}
	if recorder := call(boundSecret.Token, "GetTask", nil); recorder.Code != http.StatusOK || targetCalls != 2 {
		t.Fatalf("archived Agent stopped forwarding reads: status=%d target=%d", recorder.Code, targetCalls)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET archived_at = NULL WHERE id = $1`, agentID); err != nil {
		t.Fatalf("unarchive agent: %v", err)
	}

	// A registration that points back at this Agent never forwards.
	if _, err := testPool.Exec(ctx, `UPDATE a2a_forward_registration SET rpc_url = $3 WHERE dws_uid = $1 AND org_id = $2`,
		uid, orgID, "https://multica-prod.example.test/api/a2a/agents/"+endpoint.PublicAgentID+"/v1"); err != nil {
		t.Fatalf("point registration at self: %v", err)
	}
	if recorder := call(boundSecret.Token, "GetTask", nil); recorder.Code != http.StatusNoContent || targetCalls != 2 {
		t.Fatalf("self registration forwarded: status=%d target=%d", recorder.Code, targetCalls)
	}

	if _, err := testPool.Exec(ctx, `UPDATE a2a_forward_registration SET rpc_url = $3 WHERE dws_uid = $1 AND org_id = $2`,
		uid, orgID, target.URL+"/api/a2a/agents/pre-target-agent-0001/v1"); err != nil {
		t.Fatalf("restore registration: %v", err)
	}
	active := func() int {
		var n int
		_ = testPool.QueryRow(ctx, `SELECT count(*) FROM a2a_forward_registration WHERE dws_uid = $1 AND org_id = $2 AND token_encrypted IS NOT NULL`, uid, orgID).Scan(&n)
		return n
	}

	// Any other 401 from the target passes through; the registration stays.
	targetStatus = http.StatusUnauthorized
	if recorder := call(boundSecret.Token, "GetTask", nil); recorder.Code != http.StatusUnauthorized || targetCalls != 3 || active() != 1 {
		t.Fatalf("plain target 401: status=%d target=%d active=%d", recorder.Code, targetCalls, active())
	}
	targetStatus = http.StatusOK

	// A key rotation racing the call: the target rejects the old key after
	// the registry already holds the new one, and the call follows it.
	rotatedToken := randomAgentA2ATestToken(t)
	rotatedSealed, err := box.Seal([]byte(rotatedToken))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	var rotateErr error
	rejectToken = registrationToken
	onReject = func() {
		_, rotateErr = testPool.Exec(context.Background(), `
			UPDATE a2a_forward_registration SET token_encrypted = $3, token_sha256 = $4, signed_at_ms = 2
			WHERE dws_uid = $1 AND org_id = $2`, uid, orgID, rotatedSealed, agentA2AForwardTokenSHA256(rotatedToken))
	}
	if recorder := call(boundSecret.Token, "GetTask", nil); recorder.Code != http.StatusOK || targetCalls != 5 || rotateErr != nil {
		t.Fatalf("rotation race: status=%d target=%d err=%v", recorder.Code, targetCalls, rotateErr)
	}
	if targetAuthorization != "Bearer "+rotatedToken || active() != 1 {
		t.Fatalf("rotation race used %q, active=%d", targetAuthorization, active())
	}

	// A target that rejects the current key ran nothing: the call is served
	// here and the registration is retired (kept as a tombstone).
	rejectToken = rotatedToken
	onReject = nil
	localBefore := localCalls
	if recorder := call(boundSecret.Token, "GetTask", nil); recorder.Code != http.StatusNoContent || targetCalls != 6 || localCalls != localBefore+1 {
		t.Fatalf("rejected key not served locally: status=%d target=%d local=%d", recorder.Code, targetCalls, localCalls)
	}
	if active() != 0 {
		t.Fatal("rejected registration still live")
	}
	if recorder := call(boundSecret.Token, "GetTask", nil); recorder.Code != http.StatusNoContent || targetCalls != 6 {
		t.Fatalf("retired registration still forwarded: status=%d target=%d", recorder.Code, targetCalls)
	}
	_ = boundClient
}

type recordedAgentA2ARegistration struct {
	body  agentA2AForwardRegistrationRequest
	valid bool
}

func TestAgentA2AForwardRegistrantRegistersAndWithdraws(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	requireAgentA2AOperatorTestSchema(t)
	ctx := context.Background()
	agentID, ownerID, _ := privateAgentTestFixture(t)
	uid, orgID := randomAgentA2ATestIdentity(t)
	cleanupAgentA2AOperatorRows(t, agentID, [2]string{uid, orgID})
	operatorEmail := agentA2AOperatorTestEmail(t)

	var mu sync.Mutex
	var received []recordedAgentA2ARegistration
	registryStatus := http.StatusOK
	registry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		timestamp := r.Header.Get(agentA2AForwardTimestampHeader)
		expected := forwarding.SignRegistration([]byte(agentA2AForwardTestSecret), timestamp, body)
		var request agentA2AForwardRegistrationRequest
		_ = json.Unmarshal(body, &request)
		mu.Lock()
		received = append(received, recordedAgentA2ARegistration{body: request, valid: expected == r.Header.Get(agentA2AForwardSignatureHeader) && r.URL.Path == agentA2AForwardRegistrationPath})
		status := registryStatus
		mu.Unlock()
		writeJSON(w, status, map[string]string{"status": request.Action})
	}))
	defer registry.Close()
	originalClient := agentA2AForwardRegistrationHTTPClient
	agentA2AForwardRegistrationHTTPClient = registry.Client()
	t.Cleanup(func() { agentA2AForwardRegistrationHTTPClient = originalClient })
	requests := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(received)
	}
	lastRequest := func(t *testing.T, want int) agentA2AForwardRegistrationRequest {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(received) != want || !received[want-1].valid {
			t.Fatalf("registry requests = %+v, want %d", received, want)
		}
		return received[want-1].body
	}
	nthRequest := func(t *testing.T, n int) agentA2AForwardRegistrationRequest {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(received) < n || !received[n-1].valid {
			t.Fatalf("registry requests = %+v, want at least %d", received, n)
		}
		return received[n-1].body
	}
	setRegistry := func(status int) {
		mu.Lock()
		registryStatus = status
		mu.Unlock()
	}

	localCalls := 0
	originalProtocol := testHandler.A2AProtocol
	testHandler.A2AProtocol = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		localCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	t.Cleanup(func() { testHandler.A2AProtocol = originalProtocol })

	assignAgentA2ATestRuntime(t, agentID, "cloud", "opencode", agentA2ATestManagedOpenCodeRuntimeMetadata)
	configureAgentA2AOperatorTestHandler(t, Config{
		A2AOperatorEmails:            []string{operatorEmail},
		A2AForwardRegistryURLs:       []string{registry.URL},
		A2AForwardRegistrationSecret: agentA2AForwardTestSecret,
	})
	endpoint, _, _ := createAgentA2ATestCaller(t, agentID, ownerID)
	rpcURL := "http://127.0.0.1:8080/api/a2a/agents/" + endpoint.PublicAgentID + "/v1"

	keyStatus := func(token string) (string, string) {
		t.Helper()
		var credentialStatus, clientID string
		if err := testPool.QueryRow(ctx, `
			SELECT credential.status, client.id::text FROM a2a_client_credential credential
			JOIN a2a_client client ON client.id = credential.client_id
			WHERE credential.token_hash = $1`, hashAgentA2ATestToken(token)).Scan(&credentialStatus, &clientID); err != nil {
			t.Fatalf("load key: %v", err)
		}
		return credentialStatus, clientID
	}
	activeKeys := func(clientID string) int {
		t.Helper()
		var n int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM a2a_client_credential WHERE client_id = $1 AND status = 'active'`, clientID).Scan(&n); err != nil {
			t.Fatalf("count keys: %v", err)
		}
		return n
	}
	rpc := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/a2a/agents/"+endpoint.PublicAgentID+"/v1",
			strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"GetTask","params":{"id":"tsk_x"}}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		request = withAgentA2AURLParams(request, "publicAgentId", endpoint.PublicAgentID)
		recorder := httptest.NewRecorder()
		testHandler.HandleAgentA2ARPC(recorder, request)
		return recorder
	}
	prodForward := func(accept bool) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		testHandler.UpdateAgentA2AProdForward(recorder, withAgentA2AURLParams(
			newRequest(http.MethodPut, "/api/agents/"+agentID+"/a2a/operator/prod-forward", map[string]any{"accept": accept}), "id", agentID))
		return recorder
	}
	registration := func(recorder *httptest.ResponseRecorder) AgentA2AProdForwardRegistrationResponse {
		t.Helper()
		state := decodeAgentA2AOperatorResponse(t, recorder).ProdForward
		if state == nil || len(state.Registrations) != 1 {
			t.Fatalf("pre-release forward state = %+v", state)
		}
		return state.Registrations[0]
	}

	// Binding registers a key of a dedicated client that works here.
	recorder := putAgentA2AOperatorIdentity(agentID, map[string]any{"uid": uid, "org_id": orgID})
	if recorder.Code != http.StatusOK {
		t.Fatalf("bind identity = %d %s", recorder.Code, recorder.Body.String())
	}
	if state := registration(recorder); state.RegisteredAt == nil || !state.Current || state.Error != "" {
		t.Fatalf("registration state = %+v", state)
	}
	first := lastRequest(t, 1)
	_, forwardClient := keyStatus(first.Token)
	if first.Action != "register" || first.DWSUID != uid || first.RPCURL != rpcURL || first.TargetClientID != forwardClient || !validAgentAccessToken(first.Token) {
		t.Fatalf("registration = %+v (client %s)", first, forwardClient)
	}
	if recorder := rpc(first.Token); recorder.Code != http.StatusNoContent {
		t.Fatalf("registered key rejected: %d", recorder.Code)
	}

	// Re-registering rotates the key on the same client, so forwarded tasks
	// stay reachable, and retires the old key only once the registry took
	// the new one.
	prodForward(true)
	second := lastRequest(t, 2)
	if status, client := keyStatus(second.Token); status != "active" || client != forwardClient || second.TargetClientID != forwardClient {
		t.Fatalf("rotated key %s on %s", status, client)
	}
	if status, _ := keyStatus(first.Token); status != "revoked" {
		t.Fatalf("replaced key status = %s", status)
	}

	// A registry refusal (4xx, before any write) keeps the key it already
	// holds working.
	setRegistry(http.StatusConflict)
	if state := registration(prodForward(true)); state.RegisteredAt == nil || !state.Current || !strings.Contains(state.Error, "register:") {
		t.Fatalf("refused rotation state = %+v", state)
	}
	refused := lastRequest(t, 3)
	if status, _ := keyStatus(second.Token); status != "active" {
		t.Fatalf("held key revoked by a refused rotation: %s", status)
	}
	if status, _ := keyStatus(refused.Token); status != "revoked" {
		t.Fatalf("refused key left active: %s", status)
	}

	// A server error may come after the registry stored the key, so both
	// keys stay valid; the next registration retires the rest.
	setRegistry(http.StatusInternalServerError)
	if state := registration(prodForward(true)); !strings.Contains(state.Error, "unconfirmed") {
		t.Fatalf("unanswered rotation state = %+v", state)
	}
	setRegistry(http.StatusOK)
	unanswered := lastRequest(t, 4)
	if activeKeys(forwardClient) != 2 {
		t.Fatalf("active keys after an unanswered rotation = %d, want 2", activeKeys(forwardClient))
	}
	if state := registration(prodForward(true)); state.Error != "" || !state.Current {
		t.Fatalf("recovered state = %+v", state)
	}
	fifth := lastRequest(t, 5)
	if activeKeys(forwardClient) != 1 {
		t.Fatalf("active keys after recovery = %d, want 1", activeKeys(forwardClient))
	}
	if status, _ := keyStatus(unanswered.Token); status != "revoked" {
		t.Fatalf("unanswered key left active after recovery: %s", status)
	}

	// Rebinding on the Integrations page makes the key stale at once, even
	// before any sync; the marked 401 lets production retire it.
	if _, err := testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET dws_uid = '5550009' WHERE agent_id = $1`, agentID); err != nil {
		t.Fatalf("simulate Integrations rebinding: %v", err)
	}
	if recorder := rpc(fifth.Token); recorder.Code != http.StatusUnauthorized || recorder.Header().Get(agentA2AForwardKeyRejectedHeader) != "stale" {
		t.Fatalf("stale key: %d %q", recorder.Code, recorder.Header().Get(agentA2AForwardKeyRejectedHeader))
	}
	recorder = httptest.NewRecorder()
	testHandler.GetAgentA2AOperatorConfig(recorder, withAgentA2AURLParams(
		newRequest(http.MethodGet, "/api/agents/"+agentID+"/a2a/operator", nil), "id", agentID))
	if state := registration(recorder); state.RegisteredAt == nil || state.Current {
		t.Fatalf("stale registration shown as current: %+v", state)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET dws_uid = $2 WHERE agent_id = $1`, agentID, uid); err != nil {
		t.Fatalf("restore identity: %v", err)
	}
	if recorder := rpc(fifth.Token); recorder.Code != http.StatusNoContent {
		t.Fatalf("current key rejected: %d", recorder.Code)
	}
	// An unknown key gets a plain 401 that production passes through.
	if recorder := rpc("mca2a_" + strings.Repeat("0", 40)); recorder.Code != http.StatusUnauthorized || recorder.Header().Get(agentA2AForwardKeyRejectedHeader) != "" {
		t.Fatalf("unknown key: %d %q", recorder.Code, recorder.Header().Get(agentA2AForwardKeyRejectedHeader))
	}

	// Turning the switch off withdraws exactly that registration; its key is
	// then answered as revoked.
	recorder = prodForward(false)
	if state := decodeAgentA2AOperatorResponse(t, recorder).ProdForward; state == nil || state.Accept || state.Registrations[0].RegisteredAt != nil {
		t.Fatalf("switched-off state = %+v", state)
	}
	withdraw := lastRequest(t, 6)
	if withdraw.Action != "revoke" || withdraw.DWSUID != uid || withdraw.RPCURL != rpcURL || withdraw.TokenSHA256 != agentA2AForwardTokenSHA256(fifth.Token) {
		t.Fatalf("withdrawal = %+v", withdraw)
	}
	if recorder := rpc(fifth.Token); recorder.Code != http.StatusUnauthorized || recorder.Header().Get(agentA2AForwardKeyRejectedHeader) != "revoked" {
		t.Fatalf("withdrawn key: %d %q", recorder.Code, recorder.Header().Get(agentA2AForwardKeyRejectedHeader))
	}

	// Switching back on registers a new key on the same client.
	prodForward(true)
	if seventh := lastRequest(t, 7); seventh.Action != "register" || seventh.TargetClientID != forwardClient {
		t.Fatalf("re-enabled registration = %+v, want client %s", seventh, forwardClient)
	}

	// Concurrent re-registrations are serialized: exactly one key survives,
	// and it is the one the registry accepted last.
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = prodForward(true)
		}()
	}
	wg.Wait()
	survivor := lastRequest(t, 10)
	if activeKeys(forwardClient) != 1 {
		t.Fatalf("active forward keys after concurrent syncs = %d", activeKeys(forwardClient))
	}
	if status, _ := keyStatus(survivor.Token); status != "active" {
		t.Fatalf("last registered key status = %s", status)
	}

	// Another employee gets a new client: the old one, with its tasks, is
	// retired after its registration is withdrawn.
	otherUID, otherOrg := randomAgentA2ATestIdentity(t)
	cleanupAgentA2AOperatorRows(t, agentID, [2]string{otherUID, otherOrg})
	if recorder := putAgentA2AOperatorIdentity(agentID, map[string]any{"uid": otherUID, "org_id": otherOrg}); recorder.Code != http.StatusOK {
		t.Fatalf("rebind identity = %d %s", recorder.Code, recorder.Body.String())
	}
	if withdraw := nthRequest(t, 11); withdraw.Action != "revoke" || withdraw.DWSUID != uid || withdraw.TokenSHA256 != agentA2AForwardTokenSHA256(survivor.Token) {
		t.Fatalf("withdrawal on rebinding = %+v", withdraw)
	}
	moved := lastRequest(t, 12)
	if _, client := keyStatus(moved.Token); moved.DWSUID != otherUID || client == forwardClient || moved.TargetClientID != client {
		t.Fatalf("new employee registration = %+v on client %s (old %s)", moved, client, forwardClient)
	}
	var oldClientStatus string
	if err := testPool.QueryRow(ctx, `SELECT status FROM a2a_client WHERE id = $1`, forwardClient).Scan(&oldClientStatus); err != nil || oldClientStatus != "revoked" {
		t.Fatalf("previous employee's client = %s (%v)", oldClientStatus, err)
	}
	// Even if revoking it had failed, the retired client's key is refused.
	if _, err := testPool.Exec(ctx, `UPDATE a2a_client SET status = 'active', revoked_at = NULL WHERE id = $1`, forwardClient); err != nil {
		t.Fatalf("simulate failed client revocation: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE a2a_client_credential SET status = 'active', revoked_at = NULL WHERE token_hash = $1`, hashAgentA2ATestToken(survivor.Token)); err != nil {
		t.Fatalf("simulate failed key revocation: %v", err)
	}
	if recorder := rpc(survivor.Token); recorder.Code != http.StatusUnauthorized || recorder.Header().Get(agentA2AForwardKeyRejectedHeader) != "stale" {
		t.Fatalf("retired client's key: %d %q", recorder.Code, recorder.Header().Get(agentA2AForwardKeyRejectedHeader))
	}

	// Unpublishing keeps the registration, so forwarded tasks stay readable
	// and cancellable; the endpoint itself refuses new turns.
	if response := putAgentA2ATestConfig(t, agentID, ownerID, false); response.Code != http.StatusOK {
		t.Fatalf("disable A2A = %d %s", response.Code, response.Body.String())
	}
	if requests() != 12 {
		t.Fatalf("unpublishing sent %d registry requests", requests()-12)
	}
	if status, _ := keyStatus(moved.Token); status != "active" {
		t.Fatalf("key of an unpublished endpoint = %s", status)
	}
	if recorder := rpc(moved.Token); recorder.Code == http.StatusUnauthorized {
		t.Fatal("unpublished endpoint rejected the forward key itself")
	}
	_ = localCalls
}
