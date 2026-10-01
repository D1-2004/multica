package contextcap

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Migration 9510 registers every conversation scene configuration was stored
// for and re-keys the rows to the scene_id; ReconcileSceneCredentials then
// reseals the credentials. Nothing without a tenant org or a conversation id
// is migrated, and a second run changes nothing.
func TestSceneScopeMigrationAndCredentialRekey(t *testing.T) {
	f := openStoreTx(t)
	requireSceneTables(t, f)
	ctx := context.Background()
	migration, err := os.ReadFile("../../migrations/9510_scene_scope_to_scene_id.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	box := testBox(t)
	user := uuid.NewString()

	// Stored before scene ids: a group binding, a group credential, a DM
	// grant and the personal link that names that DM, an orgless binding
	// and a malformed key.
	if _, err := f.tx.Exec(ctx, `INSERT INTO context_capability_binding
		(workspace_id, agent_id, scope_type, org_id, scope_key, scope_title, resource_type, resource_id, enabled)
		VALUES ($1::uuid, $2::uuid, 'scene', 'org-1', 'cidLegacyGroup==', '老群', 'skill', $3::uuid, true),
		       ($1::uuid, $2::uuid, 'scene', '', 'cidOrgless==', '', 'skill', $3::uuid, true),
		       ($1::uuid, $2::uuid, 'scene', 'org-1', 'not a cid', '', 'skill', $3::uuid, true)`,
		f.workspaceID, f.agentID, f.skillID); err != nil {
		t.Fatal(err)
	}
	// Sealed as an earlier release did, under the conversation id.
	old := CredentialBinding{WorkspaceID: f.workspaceID, AgentID: f.agentID, ConnectorID: f.connectorID, ScopeType: ScopeScene, OrgID: "org-1", ScopeKey: "cidLegacyGroup=="}
	payload, err := json.Marshal(sealedCredential{WorkspaceID: old.WorkspaceID, AgentID: old.AgentID, ConnectorID: old.ConnectorID,
		ScopeType: old.ScopeType, OrgID: old.OrgID, ScopeKey: old.ScopeKey, Bearer: "token-1234567"})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.tx.Exec(ctx, `INSERT INTO context_connector_credential (workspace_id, agent_id, connector_id, scope_type, org_id, scope_key, ciphertext, hint)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'scene', 'org-1', 'cidLegacyGroup==', $4, '••••4567')`,
		f.workspaceID, f.agentID, f.connectorID, sealed); err != nil {
		t.Fatal(err)
	}
	f.directLink(t, "org-1", "cidLegacyDirect==", "staff-1", "Alice", user, time.Hour)
	if _, err := f.tx.Exec(ctx, `INSERT INTO context_config_grant (user_id, workspace_id, agent_id, scope_type, org_id, scope_key, scope_title, source, expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'scene', 'org-1', 'cidLegacyDirect==', 'Alice', 'agent_link', now() + interval '1 day')`,
		user, f.workspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}

	for run := 0; run < 2; run++ {
		if _, err := f.tx.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	scenes := map[string]string{}
	rows, err := f.tx.Query(ctx, `SELECT external_scene_id, scene_kind || ':' || id::text FROM agent_scene
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND tenant_org_id = 'org-1'`, f.workspaceID, f.agentID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, entry string
		if err := rows.Scan(&cid, &entry); err != nil {
			t.Fatal(err)
		}
		scenes[cid] = entry
	}
	rows.Close()
	if len(scenes) != 2 || scenes["cidLegacyGroup=="][:6] != "group:" || scenes["cidLegacyDirect=="][:3] != "dm:" {
		t.Fatalf("registered scenes=%v", scenes)
	}
	groupID := scenes["cidLegacyGroup=="][len("group:"):]
	dmID := scenes["cidLegacyDirect=="][len("dm:"):]

	var bindingKey string
	if err := f.tx.QueryRow(ctx, `SELECT scope_key FROM context_capability_binding
		WHERE agent_id = $1::uuid AND scope_type = 'scene' AND org_id = 'org-1' AND scope_title = '老群'`, f.agentID).Scan(&bindingKey); err != nil || bindingKey != groupID {
		t.Fatalf("binding key=%q err=%v", bindingKey, err)
	}
	var grantKey, linkExtra string
	if err := f.tx.QueryRow(ctx, `SELECT scope_key FROM context_config_grant WHERE user_id = $1::uuid AND scope_type = 'scene'`, user).Scan(&grantKey); err != nil || grantKey != dmID {
		t.Fatalf("grant key=%q err=%v", grantKey, err)
	}
	if err := f.tx.QueryRow(ctx, `SELECT extra_scene_key FROM context_config_link WHERE consumed_by = $1::uuid`, user).Scan(&linkExtra); err != nil || linkExtra != dmID {
		t.Fatalf("link extra=%q err=%v", linkExtra, err)
	}
	var untouched int
	if err := f.tx.QueryRow(ctx, `SELECT count(*) FROM context_capability_binding
		WHERE agent_id = $1::uuid AND scope_key IN ('cidOrgless==', 'not a cid')`, f.agentID).Scan(&untouched); err != nil || untouched != 2 {
		t.Fatalf("unmigratable rows changed: %d err=%v", untouched, err)
	}

	// The credential still names the conversation until the server reseals it.
	moved, err := ReconcileSceneCredentials(ctx, f.tx, box)
	if err != nil || moved != 1 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
	if moved, err := ReconcileSceneCredentials(ctx, f.tx, box); err != nil || moved != 0 {
		t.Fatalf("second run moved=%d err=%v", moved, err)
	}
	next := old
	next.ScopeKey = groupID
	credential, err := GetCredential(ctx, f.tx, next)
	if err != nil {
		t.Fatal(err)
	}
	if bearer, err := OpenCredential(box, next, credential.Ciphertext); err != nil || bearer != "token-1234567" {
		t.Fatalf("resealed bearer=%q err=%v", bearer, err)
	}
	if _, err := OpenCredential(box, old, credential.Ciphertext); err == nil {
		t.Fatal("the resealed credential still opens under the conversation key")
	}
}
