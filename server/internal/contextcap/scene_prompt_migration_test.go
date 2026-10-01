package contextcap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Migration 9428 moves each scene prompt to the scope the runtime reads: a
// group's into the group, a 1:1 chat's into its person, resolved like
// DirectScenePerson (an org-less job belongs to the identity org, only live
// grants count), the newest prompt winning when two chats share a person.
func TestScenePromptMigrationFollowsDirectScenePerson(t *testing.T) {
	f := openStoreTx(t)
	tenantMigrated(t, f)
	ctx := context.Background()
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "9428_context_prompt_component_scene_prompt.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	f.bindIdentity(t, "org-home", "")
	sceneConfig := func(orgID, sceneKey, kind, prompt string, age time.Duration) {
		t.Helper()
		if _, err := f.tx.Exec(ctx, `INSERT INTO agent_scene_config (workspace_id, agent_id, platform, org_id, scene_key, scene_kind, prompt, updated_at)
			VALUES ($1::uuid, $2::uuid, 'dingtalk', $3, $4, $5, $6, now() - make_interval(secs => $7::double precision))`,
			f.workspaceID, f.agentID, orgID, sceneKey, kind, prompt, age.Seconds()); err != nil {
			t.Fatal(err)
		}
	}

	sceneConfig("org-home", "cidMigGroup", "group", "group prompt", time.Hour)
	// Two 1:1 chats of the same person (an org-less job and a recorded one):
	// the most recently edited prompt wins.
	f.directJob(t, "cidMigDM1", "staff-mig-x", "X", "", time.Hour)
	f.directJob(t, "cidMigDM2", "staff-mig-x", "X", "org-home", time.Hour)
	sceneConfig("org-home", "cidMigDM1", "dm", "dm prompt one", 2*time.Hour)
	sceneConfig("org-home", "cidMigDM2", "dm", "dm prompt two", time.Hour)
	// An org-less job belongs to the identity org, not to another org.
	f.directJob(t, "cidMigForeign", "staff-mig-y", "Y", "", time.Hour)
	sceneConfig("org-other", "cidMigForeign", "dm", "foreign prompt", time.Hour)
	// A person known only from an expired grant is not the chat's person.
	f.directLink(t, "org-home", "cidMigExpired", "staff-mig-z", "Z", uuid.NewString(), -time.Hour)
	sceneConfig("org-home", "cidMigExpired", "dm", "expired prompt", time.Hour)

	if _, err := f.tx.Exec(ctx, string(raw)); err != nil {
		t.Fatal(err)
	}
	rows, err := f.tx.Query(ctx, `SELECT scope_type, org_id, scope_key, text FROM context_prompt_component
		WHERE agent_id = $1::uuid ORDER BY scope_type, org_id, scope_key`, f.agentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var scopeType, orgID, scopeKey, text string
		if err := rows.Scan(&scopeType, &orgID, &scopeKey, &text); err != nil {
			t.Fatal(err)
		}
		got[scopeType+"/"+orgID+"/"+scopeKey] = text
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"scene/org-home/cidMigGroup":  "group prompt",
		"person/org-home/staff-mig-x": "dm prompt two",
	}
	if len(got) != len(want) {
		t.Fatalf("migrated components = %v, want %v", got, want)
	}
	for key, text := range want {
		if got[key] != text {
			t.Fatalf("migrated components = %v, want %v", got, want)
		}
	}
	// A replay keeps what is there.
	if _, err := f.tx.Exec(ctx, string(raw)); err != nil {
		t.Fatalf("replay: %v", err)
	}
}
