package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDingTalkAccountUnbindCASDoesNotRevokeConcurrentWinner(t *testing.T) {
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

	_, err := db.New(testPool).RevokeDingTalkAccountBindingByAccountKey(ctx, db.RevokeDingTalkAccountBindingByAccountKeyParams{
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

func TestDingTalkAccountUnbindIntentEnqueueIsIdempotent(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "dingtalk-unbind-enqueue-idempotent", nil)
	installationID := seedAccountKeyProjection(t, ctx, agentID, "source-enqueue", "tenant-enqueue", "account-enqueue")
	queries := db.New(testPool)
	params := db.EnqueueDingTalkBindingUnbindsByAgentIDsParams{
		TargetIdentity: testRouterTargetIdentity,
		AgentIds:       []pgtype.UUID{util.MustParseUUID(agentID)},
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM dingtalk_binding_unbind_outbox WHERE installation_id = $1`, installationID)
	})

	if err := queries.EnqueueDingTalkBindingUnbindsByAgentIDs(ctx, params); err != nil {
		t.Fatal(err)
	}
	if err := queries.EnqueueDingTalkBindingUnbindsByAgentIDs(ctx, params); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM dingtalk_binding_unbind_outbox WHERE installation_id = $1
	`, installationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate enqueue row count = %d", count)
	}
}

func TestDingTalkAccountUnbindIntentFailsClosedWithoutTargetOrCompleteAccountKey(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	queries := db.New(testPool)

	completeAgentID := createHandlerTestAgent(t, "dingtalk-unbind-missing-target", nil)
	seedAccountKeyProjection(t, ctx, completeAgentID, "source-missing-target", "tenant-target", "account-target")
	withoutTarget := &Handler{}
	if err := withoutTarget.enqueueDingTalkBindingUnbinds(
		ctx,
		queries,
		[]pgtype.UUID{util.MustParseUUID(completeAgentID)},
	); err == nil {
		t.Fatal("active account binding was allowed to disappear without a Router target")
	}

	legacyAgentID := createHandlerTestAgent(t, "dingtalk-unbind-missing-account-key", nil)
	seedLegacyAccountProjection(t, ctx, legacyAgentID, "source-missing-account-key")
	withTarget := &Handler{TaskCompletionTargetIdentity: testRouterTargetIdentity}
	if err := withTarget.enqueueDingTalkBindingUnbinds(
		ctx,
		queries,
		[]pgtype.UUID{util.MustParseUUID(legacyAgentID)},
	); err == nil {
		t.Fatal("legacy active projection was allowed to disappear before account-key enrichment")
	}

	noBindingAgentID := createHandlerTestAgent(t, "dingtalk-unbind-no-binding", nil)
	if err := withoutTarget.enqueueDingTalkBindingUnbinds(
		ctx,
		queries,
		[]pgtype.UUID{util.MustParseUUID(noBindingAgentID)},
	); err != nil {
		t.Fatalf("agent without a digital-employee binding was blocked: %v", err)
	}
}

func seedAccountKeyProjection(t *testing.T, ctx context.Context, agentID, sourceID, tenantID, accountID string) pgtype.UUID {
	t.Helper()
	var installationID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO channel_installation (
			workspace_id, agent_id, channel_type, config, installer_user_id, status
		) VALUES (
			$1, $2, 'dingtalk_account',
			jsonb_build_object(
				'router_source_id', $3::text,
				'router_platform', 'dingtalk',
				'router_tenant_id', $4::text,
				'router_account_id', $5::text,
				'dispatch_endpoint_id', $3::text || '-endpoint'
			),
			$6, 'active'
		)
		RETURNING id
	`, testWorkspaceID, agentID, sourceID, tenantID, accountID, testUserID).Scan(&installationID); err != nil {
		t.Fatalf("seed account-key projection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE id = $1`, installationID)
	})
	return installationID
}

func seedLegacyAccountProjection(t *testing.T, ctx context.Context, agentID, sourceID string) pgtype.UUID {
	t.Helper()
	var installationID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO channel_installation (
			workspace_id, agent_id, channel_type, config, installer_user_id, status
		) VALUES (
			$1, $2, 'dingtalk_account',
			jsonb_build_object(
				'router_source_id', $3::text,
				'dispatch_endpoint_id', $3::text || '-endpoint'
			),
			$4, 'active'
		)
		RETURNING id
	`, testWorkspaceID, agentID, sourceID, testUserID).Scan(&installationID); err != nil {
		t.Fatalf("seed legacy account projection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE id = $1`, installationID)
	})
	return installationID
}
