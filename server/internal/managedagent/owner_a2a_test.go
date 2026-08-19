package managedagent_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/managedagent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

type managedOwnerFixture struct {
	pool          *pgxpool.Pool
	sourceKey     string
	workspaceID   pgtype.UUID
	runtimeID     pgtype.UUID
	agentID       pgtype.UUID
	ownerA        pgtype.UUID
	ownerB        pgtype.UUID
	endpointID    pgtype.UUID
	publicAgentID string
}

type provisionResult struct {
	agent   db.Agent
	created bool
	err     error
}

func TestManagedOwnerSameOwnerReconciliationPreservesA2AGrants(t *testing.T) {
	fixture := newManagedOwnerFixture(t)
	rawToken, clientID := createManagedOwnerGrant(t, fixture, fixture.pool)
	service := newManagedOwnerService(t, fixture, fixture.pool)

	result := provisionManagedOwner(context.Background(), service, fixture, fixture.ownerA)
	if result.err != nil || result.created {
		t.Fatalf("same-owner reconciliation = created:%v error:%v", result.created, result.err)
	}
	assertManagedOwnerA2AState(t, fixture.pool, fixture.endpointID, clientID, true, "active", "active")
	if _, err := db.New(fixture.pool).GetAgentA2ACredentialByTokenHash(context.Background(), auth.HashToken(rawToken)); err != nil {
		t.Fatalf("same-owner reconciliation revoked credential: %v", err)
	}
}

func TestManagedOwnerTransferSeesGrantCommittedWhileWaitingForAgent(t *testing.T) {
	fixture := newManagedOwnerFixture(t)
	ctx := context.Background()

	grantTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin grant transaction: %v", err)
	}
	t.Cleanup(func() { _ = grantTx.Rollback(context.Background()) })
	var grantPID int32
	if err := grantTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&grantPID); err != nil {
		t.Fatalf("load grant backend pid: %v", err)
	}
	rawToken, clientID := createManagedOwnerGrant(t, fixture, grantTx)

	transferPool, transferPID := singleConnectionPool(t, fixture.pool)
	service := newManagedOwnerService(t, fixture, transferPool)
	resultCh := make(chan provisionResult, 1)
	go func() {
		resultCh <- provisionManagedOwner(context.Background(), service, fixture, fixture.ownerB)
	}()

	requireBackendBlockedBy(t, fixture.pool, transferPID, grantPID)
	if err := grantTx.Commit(ctx); err != nil {
		t.Fatalf("commit grant transaction: %v", err)
	}
	result := awaitProvisionResult(t, resultCh)
	if result.err != nil || result.created {
		t.Fatalf("owner A to B transfer = created:%v error:%v", result.created, result.err)
	}
	if result.agent.OwnerID != fixture.ownerB {
		t.Fatalf("owner after A to B transfer = %v, want %v", result.agent.OwnerID, fixture.ownerB)
	}
	assertManagedOwnerA2AState(t, fixture.pool, fixture.endpointID, clientID, false, "revoked", "revoked")

	result = provisionManagedOwner(ctx, service, fixture, fixture.ownerA)
	if result.err != nil || result.created {
		t.Fatalf("owner B to A transfer = created:%v error:%v", result.created, result.err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE agent_a2a_endpoint
		SET enabled = TRUE,
		    delegated_by_user_id = $1,
		    updated_at = now()
		WHERE id = $2
	`, fixture.ownerA, fixture.endpointID); err != nil {
		t.Fatalf("re-enable endpoint after B to A transfer: %v", err)
	}
	assertRevokedCredentialReturnsUnauthorized(t, fixture, rawToken)
}

func TestManagedOwnerTransferWaitsForNewOwnerRemovalAndFailsClosed(t *testing.T) {
	fixture := newManagedOwnerFixture(t)
	ctx := context.Background()

	removalTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin removal transaction: %v", err)
	}
	t.Cleanup(func() { _ = removalTx.Rollback(context.Background()) })
	var removalPID int32
	if err := removalTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&removalPID); err != nil {
		t.Fatalf("load removal backend pid: %v", err)
	}
	var memberID pgtype.UUID
	if err := removalTx.QueryRow(ctx, `
		SELECT id
		FROM member
		WHERE workspace_id = $1 AND user_id = $2
		FOR UPDATE
	`, fixture.workspaceID, fixture.ownerB).Scan(&memberID); err != nil {
		t.Fatalf("lock new owner membership for removal: %v", err)
	}

	transferPool, transferPID := singleConnectionPool(t, fixture.pool)
	service := newManagedOwnerService(t, fixture, transferPool)
	resultCh := make(chan provisionResult, 1)
	go func() {
		resultCh <- provisionManagedOwner(context.Background(), service, fixture, fixture.ownerB)
	}()

	requireBackendBlockedBy(t, fixture.pool, transferPID, removalPID)
	if _, err := removalTx.Exec(ctx, `DELETE FROM member WHERE id = $1`, memberID); err != nil {
		t.Fatalf("delete locked new owner membership: %v", err)
	}
	if err := removalTx.Commit(ctx); err != nil {
		t.Fatalf("commit new owner removal: %v", err)
	}
	result := awaitProvisionResult(t, resultCh)
	if !errors.Is(result.err, pgx.ErrNoRows) {
		t.Fatalf("transfer after new owner removal error = %v, want pgx.ErrNoRows", result.err)
	}

	var ownerID pgtype.UUID
	if err := fixture.pool.QueryRow(ctx, `SELECT owner_id FROM agent WHERE id = $1`, fixture.agentID).Scan(&ownerID); err != nil {
		t.Fatalf("load owner after rejected transfer: %v", err)
	}
	if ownerID != fixture.ownerA {
		t.Fatalf("owner changed after rejected transfer = %v, want %v", ownerID, fixture.ownerA)
	}
}

func newManagedOwnerFixture(t *testing.T) managedOwnerFixture {
	t.Helper()
	pool := managedOwnerTransferTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	suffix := time.Now().UnixNano()
	fixture := managedOwnerFixture{
		pool:          pool,
		sourceKey:     fmt.Sprintf("managed-owner-source-%d", suffix),
		publicAgentID: fmt.Sprintf("managed-owner-agent-%d", suffix),
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('Managed owner A', $1)
		RETURNING id
	`, fmt.Sprintf("managed-owner-a-%d@multica.test", suffix)).Scan(&fixture.ownerA); err != nil {
		t.Fatalf("create owner A: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('Managed owner B', $1)
		RETURNING id
	`, fmt.Sprintf("managed-owner-b-%d@multica.test", suffix)).Scan(&fixture.ownerB); err != nil {
		t.Fatalf("create owner B: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Managed owner transfer', $1, '', 'MOT')
		RETURNING id
	`, fmt.Sprintf("managed-owner-transfer-%d", suffix)).Scan(&fixture.workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner'), ($1, $3, 'member')
	`, fixture.workspaceID, fixture.ownerA, fixture.ownerB); err != nil {
		t.Fatalf("create members: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, NULL, 'Managed owner runtime', 'cloud', $2, 'online', '', '{}'::jsonb, $3, now())
		RETURNING id
	`, fixture.workspaceID, fmt.Sprintf("managed_owner_%d", suffix), fixture.ownerA).Scan(&fixture.runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4)
		RETURNING id
	`, fixture.workspaceID, fmt.Sprintf("managed-owner-agent-%d", suffix), fixture.runtimeID, fixture.ownerA).Scan(&fixture.agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO managed_agent_source_snapshot (source_key, repository_url, ref)
		VALUES ($1, 'https://example.test/managed-agent.git', 'main')
	`, fixture.sourceKey); err != nil {
		t.Fatalf("create managed source snapshot: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_source (
			agent_id, workspace_id, source_type, managed_source_key,
			repo_owner, repo_name, ref, manifest_path, synced_commit_sha,
			sync_status, last_synced_at, created_by
		)
		VALUES (
			$1, $2, 'github', $3,
			'acme', 'managed-agent', 'main', 'agent/agent.md', repeat('a', 40),
			'ready', now(), $4
		)
	`, fixture.agentID, fixture.workspaceID, fixture.sourceKey, fixture.ownerA); err != nil {
		t.Fatalf("create managed agent source: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_a2a_endpoint (
			workspace_id, agent_id, public_agent_id, enabled,
			delegated_by_user_id, card_name
		)
		VALUES ($1, $2, $3, TRUE, $4, 'Managed owner Agent')
		RETURNING id
	`, fixture.workspaceID, fixture.agentID, fixture.publicAgentID, fixture.ownerA).Scan(&fixture.endpointID); err != nil {
		t.Fatalf("create A2A endpoint: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM agent WHERE id = $1`, fixture.agentID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM agent_runtime WHERE id = $1`, fixture.runtimeID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM workspace WHERE id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM managed_agent_source_snapshot WHERE source_key = $1`, fixture.sourceKey)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM "user" WHERE id IN ($1, $2)`, fixture.ownerA, fixture.ownerB)
	})
	return fixture
}

func createManagedOwnerGrant(t *testing.T, fixture managedOwnerFixture, executor db.DBTX) (string, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	queries := db.New(executor)
	client, err := queries.CreateAgentA2AClientForOwner(ctx, db.CreateAgentA2AClientForOwnerParams{
		Name:        "owner-transfer-caller",
		Scopes:      []string{"send", "read"},
		OwnerUserID: fixture.ownerA,
		WorkspaceID: fixture.workspaceID,
		AgentID:     fixture.agentID,
	})
	if err != nil {
		t.Fatalf("create A2A client: %v", err)
	}
	rawToken := fmt.Sprintf("mca2a_%040x", time.Now().UnixNano())
	if _, err := queries.CreateAgentA2ACredentialForOwner(ctx, db.CreateAgentA2ACredentialForOwnerParams{
		KeyID:       fmt.Sprintf("managed_owner_key_%d", time.Now().UnixNano()),
		TokenHash:   auth.HashToken(rawToken),
		TokenPrefix: rawToken[:12],
		OwnerUserID: fixture.ownerA,
		WorkspaceID: fixture.workspaceID,
		AgentID:     fixture.agentID,
		ClientID:    client.ID,
	}); err != nil {
		t.Fatalf("create A2A credential: %v", err)
	}
	return rawToken, client.ID
}

func newManagedOwnerService(t *testing.T, fixture managedOwnerFixture, pool *pgxpool.Pool) *managedagent.Service {
	t.Helper()
	service, err := managedagent.New(db.New(pool), pool, managedagent.Config{
		SourceKey:     fixture.sourceKey,
		RepositoryURL: "https://example.test/managed-agent.git",
		Ref:           "main",
		SyncInterval:  time.Hour,
		BatchSize:     1,
	}, nil)
	if err != nil {
		t.Fatalf("create managed Agent service: %v", err)
	}
	return service
}

func provisionManagedOwner(ctx context.Context, service *managedagent.Service, fixture managedOwnerFixture, ownerID pgtype.UUID) provisionResult {
	agent, created, err := service.Provision(ctx, fixture.workspaceID, ownerID, fixture.runtimeID, "cloud", "managed_owner_test", "")
	return provisionResult{agent: agent, created: created, err: err}
}

func assertManagedOwnerA2AState(t *testing.T, pool *pgxpool.Pool, endpointID, clientID pgtype.UUID, wantEnabled bool, wantClientStatus, wantCredentialStatus string) {
	t.Helper()
	var enabled bool
	var clientStatus, credentialStatus string
	if err := pool.QueryRow(context.Background(), `
		SELECT endpoint.enabled, client.status, credential.status
		FROM agent_a2a_endpoint AS endpoint
		JOIN a2a_client AS client ON client.endpoint_id = endpoint.id
		JOIN a2a_client_credential AS credential ON credential.client_id = client.id
		WHERE endpoint.id = $1 AND client.id = $2
	`, endpointID, clientID).Scan(&enabled, &clientStatus, &credentialStatus); err != nil {
		t.Fatalf("load A2A grant state: %v", err)
	}
	if enabled != wantEnabled || clientStatus != wantClientStatus || credentialStatus != wantCredentialStatus {
		t.Fatalf(
			"A2A grant state = enabled:%v client:%s credential:%s, want enabled:%v client:%s credential:%s",
			enabled, clientStatus, credentialStatus, wantEnabled, wantClientStatus, wantCredentialStatus,
		)
	}
}

func assertRevokedCredentialReturnsUnauthorized(t *testing.T, fixture managedOwnerFixture, rawToken string) {
	t.Helper()
	provider := featureflag.NewStaticProvider()
	protocolCalls := 0
	h := &handler.Handler{
		Queries:      db.New(fixture.pool),
		FeatureFlags: featureflag.NewService(provider),
		A2AProtocol: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			protocolCalls++
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	h.SetConfigProvider(func() handler.Config {
		return handler.Config{PublicURL: "http://127.0.0.1:8080"}
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/a2a/agents/"+fixture.publicAgentID+"/v1",
		strings.NewReader(`{"jsonrpc":"2.0","id":"read","method":"GetTask","params":{"id":"tsk_existing"}}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+rawToken)
	request.RemoteAddr = "127.0.0.1:49152"
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("publicAgentId", fixture.publicAgentID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	response := httptest.NewRecorder()
	h.HandleAgentA2ARPC(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked credential status = %d, want 401: %s", response.Code, response.Body.String())
	}
	if protocolCalls != 0 {
		t.Fatalf("revoked credential reached protocol handler %d time(s)", protocolCalls)
	}
}

func singleConnectionPool(t *testing.T, base *pgxpool.Pool) (*pgxpool.Pool, int32) {
	t.Helper()
	config, err := pgxpool.ParseConfig(base.Config().ConnString())
	if err != nil {
		t.Fatalf("parse transfer pool config: %v", err)
	}
	config.MaxConns = 1
	config.MinConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("create transfer pool: %v", err)
	}
	t.Cleanup(pool.Close)
	var backendPID int32
	if err := pool.QueryRow(context.Background(), `SELECT pg_backend_pid()`).Scan(&backendPID); err != nil {
		t.Fatalf("load transfer backend pid: %v", err)
	}
	return pool, backendPID
}

func requireBackendBlockedBy(t *testing.T, observer *pgxpool.Pool, blockedPID, blockerPID int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		var blocked bool
		err := observer.QueryRow(ctx, `SELECT $2::integer = ANY(pg_blocking_pids($1::integer))`, blockedPID, blockerPID).Scan(&blocked)
		if err != nil {
			t.Fatalf("inspect backend blockers: %v", err)
		}
		if blocked {
			return
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("backend %d did not block behind %d: %v", blockedPID, blockerPID, err)
		}
	}
}

func awaitProvisionResult(t *testing.T, resultCh <-chan provisionResult) provisionResult {
	t.Helper()
	select {
	case result := <-resultCh:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("managed owner transfer did not finish")
		return provisionResult{}
	}
}

func managedOwnerTransferTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	var schemaReady bool
	if err := pool.QueryRow(context.Background(), `
		SELECT to_regclass('agent_a2a_endpoint') IS NOT NULL
		   AND to_regclass('managed_agent_source_snapshot') IS NOT NULL
	`).Scan(&schemaReady); err != nil || !schemaReady {
		t.Skip("managed Agent A2A schema is not migrated")
	}
	return pool
}
