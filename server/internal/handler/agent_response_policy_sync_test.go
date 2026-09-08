package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// These checks exercise the SQL boundary: untouched employees never gain a
// Router policy, while an employee switched off still gets a durable rollback.
func TestAgentResponsePolicyCandidateScopeAndLegacyRollback(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	queries := db.New(testPool)
	target := "employee-toggle-test:" + uuid.NewString()
	otherTarget := "other:" + target
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM dingtalk_response_policy_sync WHERE target_identity IN ($1,$2)`, target, otherTarget)
		_, _ = testPool.Exec(ctx, `DELETE FROM dingtalk_response_policy_rollout WHERE target_identity IN ($1,$2)`, target, otherTarget)
	})
	fence, err := queries.EnsureAgentResponsePolicyTarget(ctx, target)
	if err != nil || fence.Revision != 2 || !fence.Enabled {
		t.Fatalf("initialize employee policy epoch: %+v %v", fence, err)
	}

	agentID := createHandlerTestAgent(t, "response-policy-candidate", nil)
	sourceID := "source:" + uuid.NewString()
	var installationID string
	err = testPool.QueryRow(ctx, `INSERT INTO channel_installation (workspace_id, agent_id, channel_type, config, status, installer_user_id)
VALUES ($1,$2,'dingtalk_account',jsonb_build_object('router_source_id',$3::text),'active',$4) RETURNING id`, testWorkspaceID, agentID, sourceID, testUserID).Scan(&installationID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM channel_installation WHERE id=$1`, installationID) })

	candidate := func(targetIdentity string) *db.ListResponsePolicySyncCandidatesRow {
		t.Helper()
		rows, err := queries.ListResponsePolicySyncCandidates(ctx, db.ListResponsePolicySyncCandidatesParams{TargetIdentity: targetIdentity, AfterID: pgtype.UUID{Valid: true}, BatchSize: 1000})
		if err != nil {
			t.Fatal(err)
		}
		for i := range rows {
			if rows[i].SourceID == sourceID {
				return &rows[i]
			}
		}
		return nil
	}
	if candidate(target) != nil {
		t.Fatal("default-off employee was selected before any policy was synchronized")
	}
	if _, err := queries.UpdateAgentDingTalkResponsePolicy(ctx, db.UpdateAgentDingTalkResponsePolicyParams{ID: parseUUID(agentID), ResponseEnabled: pgtype.Bool{Valid: true, Bool: true}}); err != nil {
		t.Fatal(err)
	}
	row := candidate(target)
	if row == nil || !row.DingtalkResponseEnabled {
		t.Fatal("enabled employee missing from candidates")
	}
	params := db.UpsertResponsePolicySyncParams{TargetIdentity: target, SourceID: sourceID, InstallationID: row.InstallationID, WorkspaceID: row.WorkspaceID, AgentID: row.AgentID, AgentRevision: row.DingtalkResponsePolicyRevision, RolloutRevision: fence.Revision, SourceUpdatedAt: row.SourceUpdatedAt, DesiredMode: "multica_coordinator"}
	if _, err := queries.UpsertResponsePolicySync(ctx, params); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.UpdateAgentDingTalkResponsePolicy(ctx, db.UpdateAgentDingTalkResponsePolicyParams{ID: parseUUID(agentID), ResponseEnabled: pgtype.Bool{Valid: true, Bool: false}}); err != nil {
		t.Fatal(err)
	}
	row = candidate(target)
	if row == nil || row.DingtalkResponseEnabled {
		t.Fatal("disabled employee lost its existing Router policy cleanup obligation")
	}
	if candidate(otherTarget) != nil {
		t.Fatal("sync record in another Router target selected an untouched employee")
	}
	if _, err := queries.UpsertResponsePolicySync(ctx, params); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale enabled snapshot survived switch-off: %v", err)
	}
	params.AgentRevision = row.DingtalkResponsePolicyRevision
	params.SourceUpdatedAt = row.SourceUpdatedAt
	params.DesiredMode = "legacy"
	rollback, err := queries.UpsertResponsePolicySync(ctx, params)
	if err != nil || rollback.DesiredMode != "legacy" || rollback.PolicyRevision < 3 {
		t.Fatalf("rollback not persisted: %+v %v", rollback, err)
	}
}

func TestAgentResponsePolicyEpochRejectsOldConfigurationReplica(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	queries := db.New(testPool)
	target := "employee-epoch-test:" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM dingtalk_response_policy_rollout WHERE target_identity=$1`, target)
	})
	// Model a migrated target which previously used Diamond revision 9. The
	// migration advances it once; subsequent worker restarts must not advance it.
	if _, err := testPool.Exec(ctx, `INSERT INTO dingtalk_response_policy_rollout (target_identity,revision,enabled) VALUES ($1,10,true)`, target); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		fence, err := queries.EnsureAgentResponsePolicyTarget(ctx, target)
		if err != nil || fence.Revision != 10 || !fence.Enabled {
			t.Fatalf("restart changed durable epoch: %+v %v", fence, err)
		}
	}
	for _, oldEnabled := range []bool{false, true} {
		fence, err := queries.ObserveResponsePolicyRollout(ctx, db.ObserveResponsePolicyRolloutParams{TargetIdentity: target, Revision: 9, Enabled: oldEnabled})
		if err != nil || fence.Revision != 10 || !fence.Enabled {
			t.Fatalf("old configuration replica overrode employee epoch: %+v %v", fence, err)
		}
	}
	// The new worker must not silently enable a fence deliberately left at the
	// old epoch; migration is the boundary, not a process-local default.
	if _, err := testPool.Exec(ctx, `UPDATE dingtalk_response_policy_rollout SET revision=1,enabled=false WHERE target_identity=$1`, target); err != nil {
		t.Fatal(err)
	}
	fence, err := queries.EnsureAgentResponsePolicyTarget(ctx, target)
	if err != nil || fence.Revision != 1 || fence.Enabled {
		t.Fatalf("ensure mutated an existing old target: %+v %v", fence, err)
	}
}
