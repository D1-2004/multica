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
	for name, in := range map[string]SceneConfigWrite{
		"bad key":  {SceneKey: "group-1", SceneKind: SceneKindGroup, ActorID: uuid.NewString()},
		"bad kind": {SceneKey: "cidGroup", SceneKind: "channel", ActorID: uuid.NewString()},
		"too long": {SceneKey: "cidGroup", SceneKind: SceneKindGroup, Prompt: strings.Repeat("a", MaxScenePrompt+1), ActorID: uuid.NewString()},
		"no actor": {SceneKey: "cidGroup", SceneKind: SceneKindGroup},
	} {
		if _, err := UpsertScenePrompt(ctx, nil, in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if err := RegisterDirectScene(ctx, nil, uuid.NewString(), uuid.NewString(), "", "staff-1", "Alice"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("register non-cid: err=%v", err)
	}
	if _, err := GetScene(ctx, nil, uuid.NewString(), uuid.NewString(), "", "not-a-cid"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("get non-cid: err=%v", err)
	}
}

func TestStoreScenePromptShareInGroupsAndDirectLinks(t *testing.T) {
	f := openStoreTx(t)
	ctx := context.Background()
	var migrated bool
	if err := f.tx.QueryRow(ctx, `SELECT to_regclass('agent_scene_config') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
		t.Skip("agent_scene_config is not migrated")
	}
	var actor string
	if err := f.tx.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('Scene Admin', 'scene-admin-' || $1 || '@example.test') RETURNING id::text`,
		uuid.NewString()).Scan(&actor); err != nil {
		t.Fatal(err)
	}

	// A 1:1 link redemption registers the DM scene without a prompt write.
	if err := RegisterDirectScene(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "cidDirect", " Alice "); err != nil {
		t.Fatal(err)
	}
	registered, err := GetSceneConfig(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "cidDirect")
	if err != nil || registered.SceneKind != SceneKindDM || registered.SceneTitle != "Alice" || registered.Prompt != "" || registered.UpdatedBy != "" {
		t.Fatalf("registered=%+v err=%v", registered, err)
	}
	scene, err := GetScene(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "cidDirect")
	if err != nil || scene.Kind != SceneKindDM || scene.ConfigTitle != "Alice" || scene.HasPrompt {
		t.Fatalf("scene=%+v err=%v", scene, err)
	}
	if _, err := GetScene(ctx, f.tx, f.workspaceID, f.agentID, "org-2", "cidDirect"); !errors.Is(err, ErrNotFound) {
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

	stored, err := UpsertScenePrompt(ctx, f.tx, SceneConfigWrite{
		WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-1", SceneKey: "cidDirect", SceneKind: SceneKindDM,
		Prompt: "Answer briefly.", ActorID: actor,
	})
	if err != nil || stored.Prompt != "Answer briefly." || stored.UpdatedBy != actor || stored.UpdatedByName != "Scene Admin" || stored.SceneTitle != "Alice" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	// Registering again keeps the prompt and its update stamp.
	if err := RegisterDirectScene(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "cidDirect", "Renamed"); err != nil {
		t.Fatal(err)
	}
	kept, err := GetSceneConfig(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "cidDirect")
	if err != nil || kept.Prompt != "Answer briefly." || kept.UpdatedBy != actor || kept.SceneTitle != "Alice" {
		t.Fatalf("after re-register=%+v err=%v", kept, err)
	}
	if scene, err := GetScene(ctx, f.tx, f.workspaceID, f.agentID, "org-1", "cidDirect"); err != nil || !scene.HasPrompt {
		t.Fatalf("scene after prompt=%+v err=%v", scene, err)
	}
	// Another workspace never overwrites the row.
	if _, err := UpsertScenePrompt(ctx, f.tx, SceneConfigWrite{
		WorkspaceID: uuid.NewString(), AgentID: f.agentID, OrgID: "org-1", SceneKey: "cidDirect", SceneKind: SceneKindDM,
		Prompt: "hijack", ActorID: actor,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign workspace prompt write: %v", err)
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
