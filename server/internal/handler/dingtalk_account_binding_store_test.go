package handler

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDingTalkAccountTeardownCASDoesNotDeleteConcurrentWinner(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "dingtalk-account-unbind-cas", nil)
	installationID := seedAccountKeyProjection(t, ctx, agentID, "source-cas", "tenant-cas", "account-old")

	if _, err := testPool.Exec(ctx, `
		UPDATE channel_installation
		SET config = jsonb_set(config, '{router_account_id}', to_jsonb('account-winner'::text))
		WHERE id = $1
	`, installationID); err != nil {
		t.Fatalf("install concurrent winner: %v", err)
	}

	_, err := db.New(testPool).DeleteDingTalkAccountBindingProjectionForTeardown(ctx, db.DeleteDingTalkAccountBindingProjectionForTeardownParams{
		ID: installationID, WorkspaceID: util.MustParseUUID(testWorkspaceID), AgentID: util.MustParseUUID(agentID),
		ExpectedRouterPlatform: "dingtalk", ExpectedRouterTenantID: "tenant-cas",
		ExpectedRouterAccountID: "account-old",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale account-key CAS error = %v, want pgx.ErrNoRows", err)
	}

	var status, accountID string
	if err := testPool.QueryRow(ctx, `
		SELECT status, config ->> 'router_account_id'
		FROM channel_installation WHERE id = $1
	`, installationID).Scan(&status, &accountID); err != nil {
		t.Fatal(err)
	}
	if status != "active" || accountID != "account-winner" {
		t.Fatalf("concurrent winner status=%q account=%q", status, accountID)
	}
}

func TestPreviousAccountProjectionCleanupIsExactAndCrossEnvironmentSafe(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	previousAgentID := createHandlerTestAgent(t, "dingtalk-previous-projection", nil)
	winnerAgentID := createHandlerTestAgent(t, "dingtalk-winner-projection", nil)
	previousInstallationID := seedAccountKeyProjection(t, ctx, previousAgentID, "source-previous", "tenant-shared", "account-shared")
	winnerInstallationID := seedAccountKeyProjection(t, ctx, winnerAgentID, "source-winner", "tenant-shared", "account-shared")
	queries := db.New(testPool)

	removed, err := queries.DeletePreviousDingTalkAccountBindingProjection(ctx, db.DeletePreviousDingTalkAccountBindingProjectionParams{
		CurrentInstallationID: winnerInstallationID,
		PreviousAgentID:       util.MustParseUUID(previousAgentID),
		RouterPlatform:        "dingtalk",
		RouterTenantID:        "tenant-shared",
		RouterAccountID:       "account-shared",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].ID != previousInstallationID {
		t.Fatalf("removed projections = %#v", removed)
	}

	missingEnvironment, err := queries.DeletePreviousDingTalkAccountBindingProjection(ctx, db.DeletePreviousDingTalkAccountBindingProjectionParams{
		CurrentInstallationID: winnerInstallationID,
		PreviousAgentID:       util.MustParseUUID("99999999-9999-9999-9999-999999999999"),
		RouterPlatform:        "dingtalk",
		RouterTenantID:        "tenant-shared",
		RouterAccountID:       "account-shared",
	})
	if err != nil || len(missingEnvironment) != 0 {
		t.Fatalf("cross-environment cleanup rows=%#v error=%v", missingEnvironment, err)
	}

	var winnerCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM channel_installation WHERE id = $1 AND status = 'active'`, winnerInstallationID).Scan(&winnerCount); err != nil {
		t.Fatal(err)
	}
	if winnerCount != 1 {
		t.Fatalf("winner projection count = %d", winnerCount)
	}
}

func TestAccountKeyEnrichmentFullSnapshotCASRejectsConcurrentChange(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "dingtalk-enrichment-cas", nil)
	installationID := seedLegacyAccountProjection(t, ctx, agentID, "source-legacy-cas")

	var originalConfig []byte
	if err := testPool.QueryRow(ctx, `SELECT config FROM channel_installation WHERE id = $1`, installationID).Scan(&originalConfig); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `
		UPDATE channel_installation
		SET config = jsonb_set(config, '{surface_type}', to_jsonb('chat'::text))
		WHERE id = $1
	`, installationID); err != nil {
		t.Fatal(err)
	}

	_, err := db.New(testPool).BackfillDingTalkAccountRouterAccountKey(ctx, db.BackfillDingTalkAccountRouterAccountKeyParams{
		RouterPlatform: "dingtalk", RouterTenantID: "tenant-enriched", RouterAccountID: "account-enriched",
		ID: installationID, WorkspaceID: util.MustParseUUID(testWorkspaceID), AgentID: util.MustParseUUID(agentID),
		ExpectedStatus: "active", ExpectedRouterSourceID: "source-legacy-cas", ExpectedConfig: originalConfig,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("full-snapshot enrichment CAS error = %v, want pgx.ErrNoRows", err)
	}

	var platform *string
	if err := testPool.QueryRow(ctx, `SELECT config ->> 'router_platform' FROM channel_installation WHERE id = $1`, installationID).Scan(&platform); err != nil {
		t.Fatal(err)
	}
	if platform != nil {
		t.Fatalf("stale enrichment wrote router platform %q", *platform)
	}
}

func TestDingTalkAccountBeginWaitsForAgentTeardownLock(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "dingtalk-bind-delete-race", nil)
	agentUUID := util.MustParseUUID(agentID)
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	qtx := db.New(testPool).WithTx(tx)
	locked, err := qtx.LockAgentsForDingTalkBindingTeardown(ctx, []pgtype.UUID{agentUUID})
	if err != nil || len(locked) != 1 {
		t.Fatalf("lock teardown agent rows=%d error=%v", len(locked), err)
	}

	endpointID := dingTalkEndpointID("source-bind-delete-race")
	config := agentmessagerouter.NewPendingDingTalkAccountConfig(
		endpointID,
		"/api/webhooks/agent-dispatch/"+endpointID,
		agentmessagerouter.HashCallbackToken("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		time.Now().Add(time.Minute),
	)
	raw, err := config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	type beginResult struct {
		row db.ChannelInstallation
		err error
	}
	result := make(chan beginResult, 1)
	go func() {
		row, beginErr := db.New(testPool).BeginDingTalkAccountBinding(context.Background(), db.BeginDingTalkAccountBindingParams{
			WorkspaceID: util.MustParseUUID(testWorkspaceID), AgentID: agentUUID,
			Config: raw, InstallerUserID: util.MustParseUUID(testUserID),
		})
		result <- beginResult{row: row, err: beginErr}
	}()

	select {
	case early := <-result:
		t.Fatalf("binding begin escaped teardown lock: row=%s error=%v", early.row.ID.String(), early.err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := qtx.ArchiveAgent(ctx, db.ArchiveAgentParams{ID: agentUUID, ArchivedBy: util.MustParseUUID(testUserID)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case completed := <-result:
		if !errors.Is(completed.err, pgx.ErrNoRows) {
			t.Fatalf("binding begin after archive error=%v, want pgx.ErrNoRows", completed.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("binding begin remained blocked after teardown commit")
	}
	var count int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM channel_installation
		WHERE agent_id = $1 AND channel_type = 'dingtalk_account'
	`, agentUUID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("binding rows after archived Agent = %d", count)
	}
}

func TestDingTalkAccountTeardownRollbackPreservesLocalStateAfterRouterUnbind(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "dingtalk-teardown-rollback", nil)
	installationID := seedAccountKeyProjection(t, ctx, agentID, "source-teardown-rollback", "tenant-rollback", "account-rollback")
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	qtx := db.New(testPool).WithTx(tx)
	router := &fakeDingTalkBindingTeardownRouter{
		unbindResult: agentmessagerouter.DigitalEmployeeBindingUnbindResult{Status: "unbound"},
	}
	if _, err := teardownDingTalkBindingsLocked(
		ctx,
		dbDingTalkBindingTeardownStore{queries: qtx},
		router,
		[]pgtype.UUID{util.MustParseUUID(agentID)},
		"",
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if len(router.unbinds) != 1 {
		t.Fatalf("Router unbind calls=%d, want 1", len(router.unbinds))
	}
	var projectionCount int
	var archived bool
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM channel_installation WHERE id = $1`, installationID).Scan(&projectionCount); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM agent WHERE id = $1`, agentID).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if projectionCount != 1 || archived {
		t.Fatalf("rollback projection count=%d archived=%v, want retained Agent and projection", projectionCount, archived)
	}
}

func TestDingTalkAccountCallbackAndDeleteSerializeOnProjection(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Run("delete lock first", func(t *testing.T) {
		ctx := context.Background()
		agentID := createHandlerTestAgent(t, "dingtalk-callback-delete-lock-first", nil)
		agentUUID := util.MustParseUUID(agentID)
		installationID, callbackHash, activeConfig := seedPendingDingTalkAccountProjection(t, ctx, agentID, "source-callback-delete-first", "tenant-callback-delete", "account-callback-delete-first")
		tx, err := testPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		qtx := db.New(testPool).WithTx(tx)
		if _, err := qtx.LockAgentsForDingTalkBindingTeardown(ctx, []pgtype.UUID{agentUUID}); err != nil {
			t.Fatal(err)
		}
		if _, err := qtx.GetDingTalkAccountBindingByAgentForUpdate(ctx, agentUUID); err != nil {
			t.Fatal(err)
		}

		activation := make(chan error, 1)
		go func() {
			_, activateErr := db.New(testPool).ActivateDingTalkAccountBinding(context.Background(), db.ActivateDingTalkAccountBindingParams{
				Config: activeConfig, ID: installationID,
				WorkspaceID: util.MustParseUUID(testWorkspaceID), AgentID: agentUUID,
				ExpectedCallbackTokenHash: callbackHash,
			})
			activation <- activateErr
		}()
		select {
		case early := <-activation:
			_ = tx.Rollback(ctx)
			t.Fatalf("callback crossed teardown projection lock: %v", early)
		case <-time.After(100 * time.Millisecond):
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-activation:
			if err != nil {
				t.Fatalf("callback after delete rollback: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("callback remained blocked after delete rollback")
		}
	})

	t.Run("callback commit first", func(t *testing.T) {
		ctx := context.Background()
		agentID := createHandlerTestAgent(t, "dingtalk-callback-delete-callback-first", nil)
		agentUUID := util.MustParseUUID(agentID)
		installationID, callbackHash, activeConfig := seedPendingDingTalkAccountProjection(t, ctx, agentID, "source-callback-first", "tenant-callback-delete", "account-callback-first")
		callbackTx, err := testPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		callbackQueries := db.New(testPool).WithTx(callbackTx)
		if _, err := callbackQueries.ActivateDingTalkAccountBinding(ctx, db.ActivateDingTalkAccountBindingParams{
			Config: activeConfig, ID: installationID,
			WorkspaceID: util.MustParseUUID(testWorkspaceID), AgentID: agentUUID,
			ExpectedCallbackTokenHash: callbackHash,
		}); err != nil {
			_ = callbackTx.Rollback(ctx)
			t.Fatal(err)
		}

		deleteTx, err := testPool.Begin(ctx)
		if err != nil {
			_ = callbackTx.Rollback(ctx)
			t.Fatal(err)
		}
		router := &fakeDingTalkBindingTeardownRouter{
			unbindResult: agentmessagerouter.DigitalEmployeeBindingUnbindResult{Status: "unbound"},
		}
		teardown := make(chan error, 1)
		go func() {
			_, teardownErr := teardownDingTalkBindingsLocked(
				context.Background(),
				dbDingTalkBindingTeardownStore{queries: db.New(testPool).WithTx(deleteTx)},
				router,
				[]pgtype.UUID{agentUUID},
				"",
			)
			teardown <- teardownErr
		}()
		select {
		case early := <-teardown:
			_ = callbackTx.Rollback(ctx)
			_ = deleteTx.Rollback(ctx)
			t.Fatalf("teardown crossed callback projection lock: %v", early)
		case <-time.After(100 * time.Millisecond):
		}
		if err := callbackTx.Commit(ctx); err != nil {
			_ = deleteTx.Rollback(ctx)
			t.Fatal(err)
		}
		select {
		case err := <-teardown:
			if err != nil {
				_ = deleteTx.Rollback(ctx)
				t.Fatalf("teardown after callback commit: %v", err)
			}
		case <-time.After(3 * time.Second):
			_ = deleteTx.Rollback(ctx)
			t.Fatal("teardown remained blocked after callback commit")
		}
		if err := deleteTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var projectionCount int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM channel_installation WHERE id = $1`, installationID).Scan(&projectionCount); err != nil {
			t.Fatal(err)
		}
		if projectionCount != 0 || len(router.unbinds) != 1 {
			t.Fatalf("projection count=%d Router calls=%d", projectionCount, len(router.unbinds))
		}
	})
}

func seedAccountKeyProjection(t *testing.T, ctx context.Context, agentID, sourceID, tenantID, accountID string) pgtype.UUID {
	t.Helper()
	raw := dingTalkAccountProjectionConfig(t, sourceID, tenantID, accountID)
	var installationID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO channel_installation (
			workspace_id, agent_id, channel_type, config, installer_user_id, status
		) VALUES (
			$1, $2, 'dingtalk_account', $3, $4, 'active'
		)
		RETURNING id
	`, testWorkspaceID, agentID, raw, testUserID).Scan(&installationID); err != nil {
		t.Fatalf("seed account-key projection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE id = $1`, installationID)
	})
	return installationID
}

func seedLegacyAccountProjection(t *testing.T, ctx context.Context, agentID, sourceID string) pgtype.UUID {
	t.Helper()
	raw := dingTalkAccountProjectionConfig(t, sourceID, "", "")
	var installationID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO channel_installation (
			workspace_id, agent_id, channel_type, config, installer_user_id, status
		) VALUES (
			$1, $2, 'dingtalk_account', $3, $4, 'active'
		)
		RETURNING id
	`, testWorkspaceID, agentID, raw, testUserID).Scan(&installationID); err != nil {
		t.Fatalf("seed legacy account projection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE id = $1`, installationID)
	})
	return installationID
}

func seedPendingDingTalkAccountProjection(
	t *testing.T,
	ctx context.Context,
	agentID, sourceID, tenantID, accountID string,
) (pgtype.UUID, string, []byte) {
	t.Helper()
	endpointID := dingTalkEndpointID(sourceID)
	callbackToken := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	callbackHash := agentmessagerouter.HashCallbackToken(callbackToken)
	pending := agentmessagerouter.NewPendingDingTalkAccountConfig(
		endpointID,
		"/api/webhooks/agent-dispatch/"+endpointID,
		callbackHash,
		time.Now().Add(time.Minute),
	)
	pendingRaw, err := pending.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var installationID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO channel_installation (
			workspace_id, agent_id, channel_type, config, installer_user_id, status
		) VALUES ($1, $2, 'dingtalk_account', $3, $4, 'pending')
		RETURNING id
	`, testWorkspaceID, agentID, pendingRaw, testUserID).Scan(&installationID); err != nil {
		t.Fatalf("seed pending account projection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE id = $1`, installationID)
	})
	boundAt := time.Unix(1_700_000_000, 0).UTC()
	active := pending
	active.RouterSourceID = sourceID
	active.RouterPlatform = "dingtalk"
	active.RouterTenantID = tenantID
	active.RouterAccountID = accountID
	active.BoundAt = &boundAt
	activeRaw, err := active.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return installationID, callbackHash, activeRaw
}

func dingTalkAccountProjectionConfig(t *testing.T, sourceID, tenantID, accountID string) []byte {
	t.Helper()
	endpointID := dingTalkEndpointID(sourceID)
	boundAt := time.Unix(1_700_000_000, 0).UTC()
	config := agentmessagerouter.DingTalkAccountConfig{
		SchemaVersion: 1, DispatchEndpointID: endpointID, DispatchKeyID: "v1",
		DispatchURL: "/api/webhooks/agent-dispatch/" + endpointID, RouterSourceID: sourceID,
		RouterPlatform: "dingtalk", RouterTenantID: tenantID, RouterAccountID: accountID,
		MessageScope: agentmessagerouter.DingTalkMessageScopeDirectOnly, BoundAt: &boundAt,
	}
	if tenantID == "" && accountID == "" {
		config.RouterPlatform = ""
	}
	raw, err := config.Marshal()
	if err != nil {
		t.Fatalf("marshal dingtalk account projection: %v", err)
	}
	return raw
}

func dingTalkEndpointID(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return "v1_" + base64.RawURLEncoding.EncodeToString(digest[:16])
}
