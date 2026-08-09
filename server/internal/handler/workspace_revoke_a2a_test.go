package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestRevokeMemberPermanentlyRevokesOwnedAgentA2AAccess covers the important
// non-runtime-owner case: an Agent can be owned by the departing member while
// running on a shared runtime owned by somebody else. Removing and later
// re-inviting that member must not resurrect its endpoint, caller principal,
// credential, or the credential's ability to read an existing A2A task.
func TestRevokeMemberPermanentlyRevokesOwnedAgentA2AAccess(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	withFeatureFlag(t, testHandler, featureflags.AgentA2AInbound, true)
	allowUnsafeLocalAgentA2ARuntimeForTest(t)
	originalProvider := testHandler.configProvider
	testHandler.SetConfigProvider(func() Config {
		return Config{PublicURL: "http://127.0.0.1:8080"}
	})
	t.Cleanup(func() { testHandler.SetConfigProvider(originalProvider) })

	ctx := context.Background()
	agentID, ownerID, _ := privateAgentTestFixture(t)
	rawToken, err := auth.GenerateA2AToken()
	if err != nil {
		t.Fatalf("generate A2A token: %v", err)
	}
	publicAgentID := "agent_" + strings.ReplaceAll(agentID, "-", "")
	keyID := "key_" + strings.ReplaceAll(agentID, "-", "")

	var endpointID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_a2a_endpoint (
			workspace_id, agent_id, public_agent_id, enabled,
			delegated_by_user_id, card_name, card_description, card_version
		)
		VALUES ($1, $2, $3, TRUE, $4, 'Revocation Test Agent', '', '1.0.0')
		RETURNING id
	`, testWorkspaceID, agentID, publicAgentID, ownerID).Scan(&endpointID); err != nil {
		t.Fatalf("create A2A endpoint: %v", err)
	}

	var clientID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO a2a_client (
			endpoint_id, name, status, scopes, created_by, updated_by
		)
		VALUES ($1, 'revoke-test-client', 'active', ARRAY['send', 'read']::text[], $2, $2)
		RETURNING id
	`, endpointID, ownerID).Scan(&clientID); err != nil {
		t.Fatalf("create A2A client: %v", err)
	}

	var credentialID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO a2a_client_credential (
			client_id, key_id, token_hash, token_prefix, status, created_by
		)
		VALUES ($1, $2, $3, $4, 'active', $5)
		RETURNING id
	`, clientID, keyID, auth.HashToken(rawToken), rawToken[:12], ownerID).Scan(&credentialID); err != nil {
		t.Fatalf("create A2A credential: %v", err)
	}

	if _, err := testHandler.Queries.GetAgentA2ACredentialByTokenHash(ctx, auth.HashToken(rawToken)); err != nil {
		t.Fatalf("credential should authenticate before member removal: %v", err)
	}

	originalProtocol := testHandler.A2AProtocol
	protocolCalls := 0
	testHandler.A2AProtocol = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		protocolCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	t.Cleanup(func() { testHandler.A2AProtocol = originalProtocol })

	getTask := func() *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/a2a/agents/"+publicAgentID+"/v1",
			strings.NewReader(`{"jsonrpc":"2.0","id":"read","method":"GetTask","params":{"id":"tsk_existing"}}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+rawToken)
		request.RemoteAddr = "127.0.0.1:49152"
		request = withAgentA2AURLParams(request, "publicAgentId", publicAgentID)
		response := httptest.NewRecorder()
		testHandler.HandleAgentA2ARPC(response, request)
		return response
	}

	if response := getTask(); response.Code != http.StatusNoContent {
		t.Fatalf("GetTask credential before removal status = %d, want 204: %s", response.Code, response.Body.String())
	}

	var memberRowID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM member WHERE workspace_id = $1 AND user_id = $2
	`, testWorkspaceID, ownerID).Scan(&memberRowID); err != nil {
		t.Fatalf("load departing member row: %v", err)
	}

	result, err := testHandler.revokeAndRemoveMember(
		ctx,
		util.MustParseUUID(testWorkspaceID),
		util.MustParseUUID(ownerID),
		util.MustParseUUID(memberRowID),
		util.MustParseUUID(testUserID),
	)
	if err != nil {
		t.Fatalf("revokeAndRemoveMember: %v", err)
	}
	if len(result.Runtimes) != 0 {
		t.Fatalf("fixture owner unexpectedly owned %d runtime(s); regression must cover shared-runtime Agent ownership", len(result.Runtimes))
	}
	if result.A2AEndpointsDisabled != 1 || result.A2AClientsRevoked != 1 || result.A2ACredentialsRevoked != 1 {
		t.Fatalf("A2A revocation counts = endpoint:%d client:%d credential:%d, want 1/1/1",
			result.A2AEndpointsDisabled, result.A2AClientsRevoked, result.A2ACredentialsRevoked)
	}

	var endpointEnabled bool
	var clientStatus, credentialStatus, clientRevokedBy, credentialRevokedBy string
	if err := testPool.QueryRow(ctx, `
		SELECT endpoint.enabled, client.status, credential.status,
		       client.revoked_by, credential.revoked_by
		FROM agent_a2a_endpoint endpoint
		JOIN a2a_client client ON client.endpoint_id = endpoint.id
		JOIN a2a_client_credential credential ON credential.client_id = client.id
		WHERE endpoint.id = $1 AND client.id = $2 AND credential.id = $3
	`, endpointID, clientID, credentialID).Scan(
		&endpointEnabled,
		&clientStatus,
		&credentialStatus,
		&clientRevokedBy,
		&credentialRevokedBy,
	); err != nil {
		t.Fatalf("load revoked A2A state: %v", err)
	}
	if endpointEnabled || clientStatus != "revoked" || credentialStatus != "revoked" {
		t.Fatalf("revoked A2A state = enabled:%v client:%s credential:%s, want false/revoked/revoked",
			endpointEnabled, clientStatus, credentialStatus)
	}
	if clientRevokedBy != testUserID || credentialRevokedBy != testUserID {
		t.Fatalf("revocation actor = client:%s credential:%s, want %s", clientRevokedBy, credentialRevokedBy, testUserID)
	}

	// Re-inviting the exact same user is the regression boundary. Membership
	// alone must not restore either discovery or the old read credential.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')
	`, testWorkspaceID, ownerID); err != nil {
		t.Fatalf("re-invite removed Agent owner: %v", err)
	}
	if _, err := testHandler.Queries.GetPublishedAgentA2AEndpointByPublicID(ctx, publicAgentID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("endpoint lookup after re-invite error = %v, want pgx.ErrNoRows", err)
	}
	if _, err := testHandler.Queries.GetAgentA2ACredentialByTokenHash(ctx, auth.HashToken(rawToken)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old credential lookup after re-invite error = %v, want pgx.ErrNoRows", err)
	}
	if response := getTask(); response.Code != http.StatusUnauthorized {
		t.Fatalf("GetTask with old credential after re-invite status = %d, want 401: %s", response.Code, response.Body.String())
	}
	if protocolCalls != 1 {
		t.Fatalf("revoked GetTask reached protocol handler; total calls = %d, want 1 pre-removal call only", protocolCalls)
	}
}

// TestRevokeMemberWinsAgainstStaleA2AClientCreate controls the dangerous
// interleaving explicitly: member removal owns the member-row UPDATE lock but
// is paused on a runtime lock while a client INSERT starts from a snapshot in
// which that member still exists. The INSERT must wait on the member KEY SHARE
// lock and return no row after removal commits, rather than creating an active
// caller behind the completed revocation pass.
func TestRevokeMemberWinsAgainstStaleA2AClientCreate(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)

	ctx := context.Background()
	agentID, ownerID, _ := privateAgentTestFixture(t)
	publicAgentID := "agent_race_" + strings.ReplaceAll(agentID, "-", "")

	var endpointID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_a2a_endpoint (
			workspace_id, agent_id, public_agent_id, enabled,
			delegated_by_user_id, card_name, card_description, card_version
		)
		VALUES ($1, $2, $3, TRUE, $4, 'Revocation Race Agent', '', '1.0.0')
		RETURNING id
	`, testWorkspaceID, agentID, publicAgentID, ownerID).Scan(&endpointID); err != nil {
		t.Fatalf("create A2A endpoint: %v", err)
	}

	var memberRowID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM member WHERE workspace_id = $1 AND user_id = $2
	`, testWorkspaceID, ownerID).Scan(&memberRowID); err != nil {
		t.Fatalf("load departing member row: %v", err)
	}

	// Give the member one otherwise-unused runtime so revokeAndRemoveMember can
	// be paused after taking the member lock but before scanning A2A children.
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, NULL, $2, 'cloud', 'handler_test_runtime', 'online',
		        'revocation race runtime', '{}'::jsonb, $3, now())
		RETURNING id
	`, testWorkspaceID, "a2a-revoke-race-"+agentID, ownerID).Scan(&runtimeID); err != nil {
		t.Fatalf("create member runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	blocker, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin runtime blocker: %v", err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT id FROM agent_runtime WHERE id = $1 FOR UPDATE`, runtimeID); err != nil {
		t.Fatalf("lock member runtime: %v", err)
	}

	type revokeOutcome struct {
		result revocationResult
		err    error
	}
	revokeDone := make(chan revokeOutcome, 1)
	go func() {
		result, revokeErr := testHandler.revokeAndRemoveMember(
			ctx,
			util.MustParseUUID(testWorkspaceID),
			util.MustParseUUID(ownerID),
			util.MustParseUUID(memberRowID),
			util.MustParseUUID(testUserID),
		)
		revokeDone <- revokeOutcome{result: result, err: revokeErr}
	}()

	// Observe the member UPDATE lock without relying on a sleep. A KEY SHARE
	// probe timing out proves removal has crossed its linearization point and is
	// now waiting on the runtime blocker.
	memberLocked := false
	lockDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(lockDeadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		var ignored string
		probeErr := testPool.QueryRow(probeCtx, `
			SELECT id FROM member WHERE id = $1 FOR KEY SHARE
		`, memberRowID).Scan(&ignored)
		probeTimedOut := errors.Is(probeCtx.Err(), context.DeadlineExceeded)
		cancel()
		if probeTimedOut {
			memberLocked = true
			break
		}
		if probeErr != nil {
			t.Fatalf("probe member revocation lock: %v", probeErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !memberLocked {
		t.Fatal("member removal did not acquire its outer row lock")
	}

	createDone := make(chan error, 1)
	go func() {
		_, createErr := testHandler.Queries.CreateAgentA2AClientForOwner(ctx, db.CreateAgentA2AClientForOwnerParams{
			Name:               "stale-snapshot-client",
			Scopes:             []string{"read"},
			RateLimitPerMinute: pgtype.Int4{},
			MaxConcurrentTasks: pgtype.Int4{},
			OwnerUserID:        util.MustParseUUID(ownerID),
			WorkspaceID:        util.MustParseUUID(testWorkspaceID),
			AgentID:            util.MustParseUUID(agentID),
		})
		createDone <- createErr
	}()

	select {
	case createErr := <-createDone:
		t.Fatalf("A2A client creation crossed a locked member removal early: %v", createErr)
	case <-time.After(150 * time.Millisecond):
		// Expected: the durable-grant query is waiting for member KEY SHARE.
	}

	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release runtime blocker: %v", err)
	}

	var revoked revokeOutcome
	select {
	case revoked = <-revokeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("member removal did not complete after releasing runtime blocker")
	}
	if revoked.err != nil {
		t.Fatalf("revokeAndRemoveMember: %v", revoked.err)
	}
	if revoked.result.A2AEndpointsDisabled != 1 {
		t.Fatalf("disabled endpoint count = %d, want 1", revoked.result.A2AEndpointsDisabled)
	}

	select {
	case createErr := <-createDone:
		if !errors.Is(createErr, pgx.ErrNoRows) {
			t.Fatalf("stale A2A client creation error = %v, want pgx.ErrNoRows", createErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked A2A client creation did not resume after member removal")
	}

	var clientCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM a2a_client WHERE endpoint_id = $1
	`, endpointID).Scan(&clientCount); err != nil {
		t.Fatalf("count A2A clients after removal race: %v", err)
	}
	if clientCount != 0 {
		t.Fatalf("stale A2A client survived removal race: count = %d", clientCount)
	}
}
