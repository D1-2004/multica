package contextcap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSceneKindAndPromptValidation(t *testing.T) {
	for conversationType, want := range map[string]string{
		"group": SceneKindGroup, "single": SceneKindDM, "P2P": SceneKindDM, " direct ": SceneKindDM, "": SceneKindGroup, "channel": SceneKindGroup,
	} {
		if got := SceneKindForConversationType(conversationType); got != want {
			t.Errorf("SceneKindForConversationType(%q)=%q want %q", conversationType, got, want)
		}
	}
	if !ValidScenePrompt("") || !ValidScenePrompt(strings.Repeat("场", MaxScenePrompt)) {
		t.Fatal("valid prompts rejected")
	}
	for _, bad := range []string{strings.Repeat("a", MaxScenePrompt+1), "a\x00b", "\xff"} {
		if ValidScenePrompt(bad) {
			t.Errorf("ValidScenePrompt accepted %q", bad[:min(len(bad), 8)])
		}
	}
}

// Input validation runs before any database access.
func TestSceneStoreValidatesBeforeQuerying(t *testing.T) {
	ctx := context.Background()
	share := true
	for name, in := range map[string]BindingWrite{
		"scene share": {ScopeType: ScopeScene, ScopeKey: "cidGroup", ResourceType: ResourceConnector, ResourceID: uuid.NewString(), ShareInGroups: &share},
		"skill share": {ScopeType: ScopePerson, ScopeKey: "staff-1", ResourceType: ResourceSkill, ResourceID: uuid.NewString(), ShareInGroups: &share},
	} {
		if _, err := UpsertBinding(ctx, nil, in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	hash := strings.Repeat("a", 64)
	for name, link := range map[string]Link{
		"scene link with extra": {TokenHash: hash, ScopeType: ScopeScene, ScopeKey: "cidGroup", ExtraSceneKey: "cidDirect"},
		"malformed extra":       {TokenHash: hash, ScopeType: ScopePerson, ScopeKey: "staff-1", ExtraSceneKey: "not-a-cid"},
	} {
		if _, err := InsertLink(ctx, nil, link, time.Minute); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if err := RegisterDirectScene(ctx, nil, uuid.NewString(), uuid.NewString(), "", "staff-1", "Alice"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("register non-cid: err=%v", err)
	}
	if _, err := GetScene(ctx, nil, uuid.NewString(), uuid.NewString(), "", "", "not-a-cid"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("get non-cid: err=%v", err)
	}
}

func TestStoreDirectSceneShareInGroupsAndDirectLinks(t *testing.T) {
	f := openStoreTx(t)
	ctx := context.Background()
	var migrated bool
	if err := f.tx.QueryRow(ctx, `SELECT to_regclass('context_prompt_component') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
		t.Skip("context_prompt_component is not migrated")
	}
	var actor string
	if err := f.tx.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('Scene Admin', 'scene-admin-' || $1 || '@example.test') RETURNING id::text`,
		uuid.NewString()).Scan(&actor); err != nil {
		t.Fatal(err)
	}

	// A 1:1 link redemption registers the DM scene.
	if err := RegisterDirectScene(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "cidDirect", " Alice "); err != nil {
		t.Fatal(err)
	}
	scene, err := GetScene(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "org-1", "cidDirect")
	if err != nil || scene.Kind != SceneKindDM || scene.ConfigTitle != "Alice" || scene.HasPrompt {
		t.Fatalf("scene=%+v err=%v", scene, err)
	}
	if _, err := GetScene(ctx, f.tx, f.workspaceID, f.agentID, "org-2", "org-1", "cidDirect"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scene under another org: %v", err)
	}
	if _, err := f.tx.Exec(ctx, `INSERT INTO scene_memory (workspace_id, agent_id, platform, org_id, scene_key, scene_kind)
		VALUES ($1::uuid, $2::uuid, 'dingtalk', 'org-1', 'cidMemoryDM', 'dm')`, f.workspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}
	// scene_memory records dm for any chat type that is not "group" (an empty
	// or unknown type included), so only the registered configuration row
	// proves a 1:1 chat.
	kinds, err := SceneKinds(ctx, f.tx, f.workspaceID, f.agentID, "org-1", []string{"cidDirect", "cidMemoryDM", "cidUnknown"})
	if err != nil || len(kinds) != 3 || kinds["cidDirect"] != SceneKindDM || kinds["cidMemoryDM"] != SceneKindGroup || kinds["cidUnknown"] != SceneKindGroup {
		t.Fatalf("kinds=%v err=%v", kinds, err)
	}
	if kinds, err := SceneKinds(ctx, f.tx, f.workspaceID, f.agentID, "org-2", []string{"cidDirect"}); err != nil || kinds["cidDirect"] != SceneKindGroup {
		t.Fatalf("kinds under another org=%v err=%v", kinds, err)
	}
	// Registering again keeps the title; a scene prompt component makes
	// has_prompt true and lists a scene nothing else mentions.
	if err := RegisterDirectScene(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "cidDirect", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplacePromptComponents(ctx, f.tx, PromptComponentsWrite{
		WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopeScene, OrgID: "org-1", ScopeKey: "cidPromptOnly",
		Components: []PromptComponentInput{{Name: "Tone", Text: "Answer briefly."}}, ActorID: actor,
	}); err != nil {
		t.Fatal(err)
	}
	if scene, err := GetScene(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "org-1", "cidPromptOnly"); err != nil || !scene.HasPrompt || scene.Kind != SceneKindGroup {
		t.Fatalf("prompt-only scene=%+v err=%v", scene, err)
	}
	if scene, err := GetScene(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "org-1", "cidDirect"); err != nil || scene.HasPrompt || scene.ConfigTitle != "Alice" {
		t.Fatalf("scene after re-register=%+v err=%v", scene, err)
	}

	// share_in_groups: default false, set, kept when omitted.
	if err := ReplaceOffers(ctx, f.tx, f.workspaceID, f.agentID, []string{f.connectorID}, nil, actor); err != nil {
		t.Fatal(err)
	}
	write := BindingWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopePerson, OrgID: "org-1", ScopeKey: "staff-1",
		ResourceType: ResourceConnector, ResourceID: f.connectorID, Enabled: true, ActorID: actor}
	b, err := UpsertBinding(ctx, f.tx, write)
	if err != nil || b.ShareInGroups || b.UpdatedBy != actor || b.UpdatedAt.IsZero() {
		t.Fatalf("default binding=%+v err=%v", b, err)
	}
	share := true
	write.ShareInGroups = &share
	if b, err = UpsertBinding(ctx, f.tx, write); err != nil || !b.ShareInGroups {
		t.Fatalf("shared binding=%+v err=%v", b, err)
	}
	write.ShareInGroups, write.Enabled = nil, false
	if b, err = UpsertBinding(ctx, f.tx, write); err != nil || !b.ShareInGroups || b.Enabled {
		t.Fatalf("toggle keeps share: %+v err=%v", b, err)
	}
	listed, err := ListScopeBindings(ctx, f.tx, f.workspaceID, f.agentID, ScopePerson, "org-1", "staff-1")
	if err != nil || len(listed) != 1 || !listed[0].ShareInGroups {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}

	// A person link carries the DM scene key through redemption.
	stamp := func() string { return strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")[:64] }
	link, err := InsertLink(ctx, f.tx, Link{TokenHash: stamp(), WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopePerson,
		OrgID: "org-1", ScopeKey: "staff-1", ScopeTitle: "Alice", ExtraSceneKey: "cidDirect"}, LinkTTLPerson)
	if err != nil || link.ExtraSceneKey != "cidDirect" {
		t.Fatalf("link=%+v err=%v", link, err)
	}
	redeemed, err := RedeemLink(ctx, f.tx, link.TokenHash, actor)
	if err != nil || redeemed.ExtraSceneKey != "cidDirect" {
		t.Fatalf("redeemed=%+v err=%v", redeemed, err)
	}
}

// ListAllAgentScenes returns more than one HTTP page from a single
// statement, newest activity first, and reports what the cap cut off.
func TestListAllAgentScenesBeyondOnePage(t *testing.T) {
	f := openStoreTx(t)
	ctx := context.Background()
	var migrated bool
	if err := f.tx.QueryRow(ctx, `SELECT to_regclass('agent_scene_config') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
		t.Skip("agent_scene_config is not migrated")
	}
	const total = maxScenesPerPage + 5
	if _, err := f.tx.Exec(ctx, `INSERT INTO agent_scene_config (workspace_id, agent_id, platform, org_id, scene_key, scene_kind, scene_title, prompt, updated_at)
		SELECT $1::uuid, $2::uuid, 'dingtalk', 'org-1', 'cidAll' || lpad(n::text, 4, '0'), 'group', 'Scene ' || n, '', now() - n * interval '1 minute'
		FROM generate_series(1, $3::int) AS n`, f.workspaceID, f.agentID, total); err != nil {
		t.Fatal(err)
	}
	q := SceneListQuery{WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-1", IdentityOrgID: "org-1", Limit: MaxAllScenes}
	page, more, err := ListAgentScenes(ctx, f.tx, q)
	if err != nil || len(page) != maxScenesPerPage || !more {
		t.Fatalf("paged listing: %d scenes, more=%v, err=%v", len(page), more, err)
	}
	all, more, err := ListAllAgentScenes(ctx, f.tx, q, MaxAllScenes)
	if err != nil || len(all) != total || more {
		t.Fatalf("whole listing: %d scenes, more=%v, err=%v", len(all), more, err)
	}
	if all[0].SceneKey != "cidAll0001" || all[total-1].SceneKey != "cidAll0205" {
		t.Fatalf("order: first=%s last=%s", all[0].SceneKey, all[total-1].SceneKey)
	}
	capped, more, err := ListAllAgentScenes(ctx, f.tx, q, 3)
	if err != nil || len(capped) != 3 || !more || capped[2].SceneKey != "cidAll0003" {
		t.Fatalf("capped listing: %+v more=%v err=%v", capped, more, err)
	}
}

func TestSceneWithOnlyACredentialIsListed(t *testing.T) {
	f := openStoreTx(t)
	ctx := context.Background()
	var migrated bool
	if err := f.tx.QueryRow(ctx, `SELECT to_regclass('context_connector_credential') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
		t.Skip("context_connector_credential is not migrated")
	}
	// A group stored a token without turning anything on: no memory, job,
	// config or binding row. The scene must still be found, so a manager
	// can open it and remove the credential.
	if _, err := f.tx.Exec(ctx, `INSERT INTO context_connector_credential (workspace_id, agent_id, connector_id, scope_type, org_id, scope_key, ciphertext, hint)
		VALUES ($1::uuid, $2::uuid, gen_random_uuid(), 'scene', 'org-1', 'cidCredOnly==', '\x00'::bytea, 'x'),
		       ($1::uuid, $2::uuid, gen_random_uuid(), 'scene', 'org-2', 'cidOtherOrg==', '\x00'::bytea, 'x')`,
		f.workspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}
	scene, err := GetScene(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "org-1", "cidCredOnly==")
	if err != nil || scene.SceneKey != "cidCredOnly==" || scene.Kind != SceneKindGroup {
		t.Fatalf("credential-only scene = %+v, %v", scene, err)
	}
	if _, err := GetScene(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "org-1", "cidOtherOrg=="); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org's credential scene: err = %v, want ErrNotFound", err)
	}
}
