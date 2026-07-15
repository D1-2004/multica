package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/fdebootstrap"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeFDEBootstrapDirectory struct {
	unionID      string
	clientID     string
	clientSecret string
	staffID      string
}

func (f *fakeFDEBootstrapDirectory) LookupInstallationUserUnionID(_ context.Context, clientID, clientSecret, staffID string) (string, error) {
	f.clientID, f.clientSecret, f.staffID = clientID, clientSecret, staffID
	return f.unionID, nil
}

func TestGetFDEBootstrapIntentStatusHidesUnknownTokens(t *testing.T) {
	req := withURLParam(newRequest(http.MethodGet, "/api/fde/bootstrap/intents/not-a-token", nil), "token", "not-a-token")
	w := httptest.NewRecorder()
	testHandler.GetFDEBootstrapIntentStatus(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "{\"status\":\"expired\"}\n" {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}

func TestCreateFDEBootstrapIntentRejectsHumanActor(t *testing.T) {
	req := newRequest(http.MethodPost, "/api/fde/bootstrap/intents", map[string]any{})
	w := httptest.NewRecorder()
	testHandler.CreateFDEBootstrapIntent(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateFDEBootstrapIntentDerivesIdentityFromDingTalkSingleChat(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "FDE Bootstrap Source Agent", []byte(`{}`))
	box, err := secretbox.New(make([]byte, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	installations, err := dingtalk.NewInstallationService(testHandler.Queries, testPool, box)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := installations.Upsert(ctx, dingtalk.InstallationParams{
		WorkspaceID:     util.MustParseUUID(testWorkspaceID),
		AgentID:         util.MustParseUUID(agentID),
		ClientID:        "fde-bootstrap-client",
		ClientSecret:    "fde-bootstrap-secret",
		InstallerUserID: util.MustParseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("create DingTalk installation: %v", err)
	}
	var chatID, taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status)
		VALUES ($1, $2, $3, 'FDE Bootstrap Chat', 'active') RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&chatID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO channel_chat_session_binding
			(chat_session_id, installation_id, channel_type, channel_chat_id, chat_type, config)
		VALUES ($1, $2, 'dingtalk', 'single-chat-1', 'p2p', '{"sender_staff_id":"staff-123"}'::jsonb)
	`, chatID, util.UUIDToString(inst.ID)); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority)
		VALUES ($1, $2, $3, 'running', 0) RETURNING id
	`, agentID, testRuntimeID, chatID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM fde_bootstrap_intent WHERE source_task_id = $1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, chatID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE id = $1`, util.UUIDToString(inst.ID))
	})

	directory := &fakeFDEBootstrapDirectory{unionID: "union-from-directory"}
	h := *testHandler
	h.cfg.AppURL = "https://multica.example"
	h.DingTalkInstallations = installations
	h.DingTalkBootstrapDirectory = directory
	req := newRequest(http.MethodPost, "/api/fde/bootstrap/intents", map[string]any{})
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	w := httptest.NewRecorder()
	h.CreateFDEBootstrapIntent(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var response fdeBootstrapLinkResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(response.URL, "https://multica.example/fde/bootstrap/fdeb_") {
		t.Fatalf("unexpected bootstrap URL: %q", response.URL)
	}
	if directory.clientID != "fde-bootstrap-client" || directory.clientSecret != "fde-bootstrap-secret" || directory.staffID != "staff-123" {
		t.Fatalf("directory lookup used wrong trusted context: %#v", directory)
	}
	var storedTaskID, storedStatus string
	if err := testPool.QueryRow(ctx, `
		SELECT source_task_id::text, status FROM fde_bootstrap_intent
		WHERE source_task_id = $1 ORDER BY created_at DESC LIMIT 1
	`, taskID).Scan(&storedTaskID, &storedStatus); err != nil {
		t.Fatal(err)
	}
	if storedTaskID != taskID || storedStatus != "pending" {
		t.Fatalf("unexpected intent source/status: %s %s", storedTaskID, storedStatus)
	}
}

func TestCompleteFDEBootstrapIntentIsIdentityBoundAndProductIdempotent(t *testing.T) {
	ctx := context.Background()
	email := "fde-bootstrap-test@multica.ai"
	var userID string
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('FDE Test', $1) RETURNING id`, email).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	userUUID := util.MustParseUUID(userID)
	var createdWorkspaceID string
	var expectedIdentity []byte
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM fde_bootstrap_intent WHERE user_id = $1 OR expected_identity_hmac = $2`, userID, expectedIdentity)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM product_workspace_provisioning WHERE user_id = $1`, userID)
		if createdWorkspaceID != "" {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, createdWorkspaceID)
		}
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})

	expectedIdentity, err := fdebootstrap.IdentityHMAC("union-owner")
	if err != nil {
		t.Fatal(err)
	}
	wrongIdentity, err := fdebootstrap.IdentityHMAC("union-someone-else")
	if err != nil {
		t.Fatal(err)
	}
	wrongProof, err := fdebootstrap.SignIdentityAssertion(userID, wrongIdentity, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	firstToken := createFDEIntentForTest(t, expectedIdentity)
	w := completeFDEIntentForTest(t, firstToken, userID, wrongProof)
	if w.Code != http.StatusForbidden {
		t.Fatalf("identity mismatch should be 403, got %d: %s", w.Code, w.Body.String())
	}
	var workspaceCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM product_workspace_provisioning WHERE user_id = $1`, userID).Scan(&workspaceCount); err != nil {
		t.Fatal(err)
	}
	if workspaceCount != 0 {
		t.Fatalf("identity mismatch created a product row")
	}

	proof, err := fdebootstrap.SignIdentityAssertion(userID, expectedIdentity, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w = completeFDEIntentForTest(t, firstToken, userID, proof)
	if w.Code != http.StatusOK || w.Body.String() != "{\"status\":\"ready\"}\n" {
		t.Fatalf("first completion failed: %d %s", w.Code, w.Body.String())
	}
	product, err := testHandler.Queries.GetProductWorkspaceProvisioning(ctx, db.GetProductWorkspaceProvisioningParams{
		UserID: userUUID, ProductKey: fdebootstrap.ProductKey,
	})
	if err != nil || product.Status != "ready" || !product.WorkspaceID.Valid {
		t.Fatalf("product workspace not ready: %#v err=%v", product, err)
	}
	createdWorkspaceID = util.UUIDToString(product.WorkspaceID)

	secondToken := createFDEIntentForTest(t, expectedIdentity)
	w = completeFDEIntentForTest(t, secondToken, userID, proof)
	if w.Code != http.StatusOK {
		t.Fatalf("second completion failed: %d %s", w.Code, w.Body.String())
	}
	secondProduct, err := testHandler.Queries.GetProductWorkspaceProvisioning(ctx, db.GetProductWorkspaceProvisioningParams{
		UserID: userUUID, ProductKey: fdebootstrap.ProductKey,
	})
	if err != nil || secondProduct.WorkspaceID != product.WorkspaceID {
		t.Fatalf("second completion did not reuse workspace: %#v err=%v", secondProduct, err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM workspace WHERE id = $1`, createdWorkspaceID).Scan(&workspaceCount); err != nil {
		t.Fatal(err)
	}
	if workspaceCount != 1 {
		t.Fatalf("expected exactly one product workspace, got %d", workspaceCount)
	}
}

func TestCompleteFDEBootstrapIntentConcurrentTokensCreateOneWorkspace(t *testing.T) {
	ctx := context.Background()
	var userID string
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('FDE Concurrent', $1) RETURNING id`, "fde-bootstrap-concurrent@multica.ai").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	identity, err := fdebootstrap.IdentityHMAC("union-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := fdebootstrap.SignIdentityAssertion(userID, identity, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tokens := []string{createFDEIntentForTest(t, identity), createFDEIntentForTest(t, identity)}
	var createdWorkspaceID string
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM fde_bootstrap_intent WHERE user_id = $1 OR expected_identity_hmac = $2`, userID, identity)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM product_workspace_provisioning WHERE user_id = $1`, userID)
		if createdWorkspaceID != "" {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, createdWorkspaceID)
		}
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})

	start := make(chan struct{})
	responses := make([]*httptest.ResponseRecorder, len(tokens))
	var wg sync.WaitGroup
	for i, token := range tokens {
		wg.Add(1)
		go func(index int, rawToken string) {
			defer wg.Done()
			<-start
			req := newRequestAs(userID, http.MethodPost, "/api/fde/bootstrap/intents/"+rawToken+"/complete", map[string]any{})
			req.AddCookie(&http.Cookie{Name: auth.FDEBootstrapIdentityCookieName, Value: proof})
			req = withURLParam(req, "token", rawToken)
			responses[index] = httptest.NewRecorder()
			testHandler.CompleteFDEBootstrapIntent(responses[index], req)
		}(i, token)
	}
	close(start)
	wg.Wait()
	for i, response := range responses {
		if response.Code != http.StatusOK {
			t.Fatalf("completion %d failed: %d %s", i, response.Code, response.Body.String())
		}
	}

	product, err := testHandler.Queries.GetProductWorkspaceProvisioning(ctx, db.GetProductWorkspaceProvisioningParams{
		UserID: util.MustParseUUID(userID), ProductKey: fdebootstrap.ProductKey,
	})
	if err != nil || !product.WorkspaceID.Valid {
		t.Fatalf("missing product workspace: %#v err=%v", product, err)
	}
	createdWorkspaceID = util.UUIDToString(product.WorkspaceID)
	var count int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM workspace w
		JOIN member m ON m.workspace_id = w.id
		WHERE m.user_id = $1 AND w.description = 'Workspace created by the Multica FDE bootstrap flow.'
	`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent completions created %d FDE workspaces", count)
	}
}

func createFDEIntentForTest(t *testing.T, identity []byte) string {
	t.Helper()
	token, err := fdebootstrap.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	randomUUID := func() pgtype.UUID { return util.MustParseUUID("00000000-0000-4000-8000-" + randomID()[:12]) }
	_, err = testHandler.Queries.CreateFDEBootstrapIntent(context.Background(), db.CreateFDEBootstrapIntentParams{
		TokenHash:            fdebootstrap.HashToken(token),
		ExpectedIdentityHmac: identity,
		SourceWorkspaceID:    randomUUID(),
		SourceAgentID:        randomUUID(),
		SourceTaskID:         randomUUID(),
		SourceChatSessionID:  randomUUID(),
		SourceInstallationID: randomUUID(),
		ExpiresAt:            pgtype.Timestamptz{Time: time.Now().Add(fdebootstrap.IntentTTL), Valid: true},
	})
	if err != nil {
		t.Fatalf("create intent: %v", err)
	}
	return token
}

func completeFDEIntentForTest(t *testing.T, token, userID, proof string) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequestAs(userID, http.MethodPost, "/api/fde/bootstrap/intents/"+token+"/complete", map[string]any{})
	req.AddCookie(&http.Cookie{Name: auth.FDEBootstrapIdentityCookieName, Value: proof})
	req = withURLParam(req, "token", token)
	w := httptest.NewRecorder()
	testHandler.CompleteFDEBootstrapIntent(w, req)
	return w
}
