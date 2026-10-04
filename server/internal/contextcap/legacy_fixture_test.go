package contextcap

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Fixtures of data written before scene ids (docs/agent-scene.md), for the
// tests of migrations that read it.

// directJob plants one inbound Coordinator job of conversation cid whose
// dispatch sender is (staffID, name), recorded under dispatchOrg, created
// age ago.
func (f storeFixture) directJob(t *testing.T, cid, staffID, name, dispatchOrg string, age time.Duration) {
	t.Helper()
	command, err := json.Marshal(map[string]any{
		"source": map[string]any{"platform": "dingtalk", "type": "digital_employee"},
		"event": map[string]any{"data": map[string]any{
			"conversation": map[string]any{"openConversationId": cid, "type": "single"},
			"sender":       map[string]any{"staffId": staffID, "displayName": name},
		}},
		"externalIdentity": map[string]any{"dws": map[string]any{"orgId": dispatchOrg}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.tx.Exec(context.Background(), `INSERT INTO inbound_coordinator_job
		(acceptance_id, workspace_id, agent_id, user_id, endpoint_namespace_id, idempotency_key, command, chat_session_id, user_message_id, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'completed', now() - make_interval(secs => $10::double precision))`,
		uuid.NewString(), f.workspaceID, f.agentID, uuid.NewString(), uuid.NewString(), uuid.NewString(), command,
		uuid.NewString(), uuid.NewString(), age.Seconds()); err != nil {
		t.Fatal(err)
	}
}

// directLink plants a redeemed personal link minted in 1:1 chat cid for
// staffID (its extra scene still a raw conversation id, as stored before
// scene ids), and userID's person grant living ttl.
func (f storeFixture) directLink(t *testing.T, orgID, cid, staffID, title, userID string, ttl time.Duration) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.tx.Exec(ctx, `INSERT INTO context_config_link
		(token_hash, workspace_id, agent_id, scope_type, org_id, scope_key, scope_title, extra_scene_key, expires_at, consumed_at, consumed_by)
		VALUES ($1, $2::uuid, $3::uuid, 'person', $4, $5, $6, $7, now() + interval '15 minutes', now(), $8::uuid)`,
		HashLinkToken(uuid.NewString()), f.workspaceID, f.agentID, orgID, staffID, title, cid, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tx.Exec(ctx, `INSERT INTO context_config_grant
		(user_id, workspace_id, agent_id, scope_type, org_id, scope_key, scope_title, source, expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'person', $4, $5, $6, 'agent_link', now() + make_interval(secs => $7::double precision))`,
		userID, f.workspaceID, f.agentID, orgID, staffID, title, ttl.Seconds()); err != nil {
		t.Fatal(err)
	}
}

// directSceneJob plants a 1:1 Coordinator job like directJob whose command
// carries the dispatch's SceneRef, as jobs do since scene ids.
func (f storeFixture) directSceneJob(t *testing.T, cid, sceneID, staffID, name, dispatchOrg string, age time.Duration) {
	t.Helper()
	command, err := json.Marshal(map[string]any{
		"source":      map[string]any{"platform": "dingtalk", "type": "digital_employee"},
		"agent_scene": map[string]any{"scene_id": sceneID},
		"event": map[string]any{"data": map[string]any{
			"conversation": map[string]any{"openConversationId": cid, "type": "single"},
			"sender":       map[string]any{"staffId": staffID, "displayName": name},
		}},
		"externalIdentity": map[string]any{"dws": map[string]any{"orgId": dispatchOrg}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.tx.Exec(context.Background(), `INSERT INTO inbound_coordinator_job
		(acceptance_id, workspace_id, agent_id, user_id, endpoint_namespace_id, idempotency_key, command, chat_session_id, user_message_id, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'completed', now() - make_interval(secs => $10::double precision))`,
		uuid.NewString(), f.workspaceID, f.agentID, uuid.NewString(), uuid.NewString(), uuid.NewString(), command,
		uuid.NewString(), uuid.NewString(), age.Seconds()); err != nil {
		t.Fatal(err)
	}
}
