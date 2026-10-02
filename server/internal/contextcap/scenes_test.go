package contextcap

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestScenePromptValidation(t *testing.T) {
	if !ValidScenePrompt("") || !ValidScenePrompt(strings.Repeat("场", MaxScenePrompt)) {
		t.Fatal("valid prompts rejected")
	}
	for _, bad := range []string{strings.Repeat("a", MaxScenePrompt+1), "a\x00b", "\xff"} {
		if ValidScenePrompt(bad) {
			t.Errorf("ValidScenePrompt accepted %q", bad[:min(len(bad), 8)])
		}
	}
}

// A scene scope key is a scene_id; a conversation id is not.
func TestValidSceneID(t *testing.T) {
	for id, want := range map[string]bool{
		"aaaaaaaa-0000-4000-8000-000000000003": true,
		"AAAAAAAA-0000-4000-8000-000000000003": false,
		"00000000-0000-0000-0000-000000000000": false,
		"cidVaO557dsSgYcgnvRNbwY4g==":          false,
		"":                                     false,
	} {
		if got := ValidSceneID(id); got != want {
			t.Errorf("ValidSceneID(%q)=%v want %v", id, got, want)
		}
		if got := ValidScopeKey(ScopeScene, id); got != want {
			t.Errorf("ValidScopeKey(scene, %q)=%v want %v", id, got, want)
		}
	}
}

// Input validation runs before any database access.
func TestSceneStoreValidatesBeforeQuerying(t *testing.T) {
	ctx := context.Background()
	share := true
	sceneID := uuid.NewString()
	for name, in := range map[string]BindingWrite{
		"scene share": {ScopeType: ScopeScene, ScopeKey: sceneID, ResourceType: ResourceConnector, ResourceID: uuid.NewString(), ShareInGroups: &share},
		"skill share": {ScopeType: ScopePerson, ScopeKey: "staff-1", ResourceType: ResourceSkill, ResourceID: uuid.NewString(), ShareInGroups: &share},
		"cid scene":   {ScopeType: ScopeScene, ScopeKey: "cidGroup", ResourceType: ResourceConnector, ResourceID: uuid.NewString()},
	} {
		if _, err := UpsertBinding(ctx, nil, in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	hash := strings.Repeat("a", 64)
	for name, link := range map[string]Link{
		"scene link with extra": {TokenHash: hash, ScopeType: ScopeScene, ScopeKey: sceneID, ExtraSceneID: uuid.NewString()},
		"conversation extra":    {TokenHash: hash, ScopeType: ScopePerson, ScopeKey: "staff-1", ExtraSceneID: "cidDirect"},
	} {
		if _, err := InsertLink(ctx, nil, link, time.Minute); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if _, err := GetScene(ctx, nil, uuid.NewString(), uuid.NewString(), "org-1", "cidGroup"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("get by conversation id: err=%v", err)
	}
}

// insertScene registers one agent_scene row of the fixture agent and
// returns its scene_id.
func (f storeFixture) insertScene(t *testing.T, orgID, kind, external, title string, age time.Duration) string {
	t.Helper()
	namespace := "dingtalk.open_conversation_id"
	if kind == "enterprise" {
		namespace = "dingtalk.org"
	}
	var id string
	if err := f.tx.QueryRow(context.Background(), `INSERT INTO agent_scene
		(workspace_id, agent_id, provider, tenant_org_id, source_namespace, scene_kind, external_scene_id, title, last_active_at)
		VALUES ($1::uuid, $2::uuid, 'dingtalk', $3, $4, $5, $6, $7, now() - make_interval(secs => $8::double precision))
		RETURNING id::text`, f.workspaceID, f.agentID, orgID, namespace, kind, external, title, age.Seconds()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func requireSceneTables(t *testing.T, f storeFixture) {
	t.Helper()
	var migrated bool
	if err := f.tx.QueryRow(context.Background(), `SELECT to_regclass('agent_scene_locator_idx') IS NOT NULL
		AND to_regclass('agent_scene_memory') IS NOT NULL AND to_regclass('context_prompt_component') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
		t.Skip("agent_scene is not migrated")
	}
}

// Scenes are listed from the scene directory only: memory, prompt and
// Coordinator facts are joined by scene_id, never discovered from them.
func TestListAgentScenesReadsTheSceneDirectory(t *testing.T) {
	f := openStoreTx(t)
	requireSceneTables(t, f)
	ctx := context.Background()
	group := f.insertScene(t, "org-1", "group", "cidGroup==", "项目群", time.Minute)
	dm := f.insertScene(t, "org-1", "dm", "cidDirect==", "冬翔", 2*time.Minute)
	other := f.insertScene(t, "org-2", "group", "cidGroup==", "Other org", 0)
	f.insertScene(t, "org-1", "enterprise", "org-1", "", 0)

	if _, err := f.tx.Exec(ctx, `INSERT INTO agent_scene_memory (scene_id, workspace_id, agent_id, memory_text)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'remembered')`, dm, f.workspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}
	var actor string
	if err := f.tx.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('Scene Admin', 'scene-admin-' || $1 || '@example.test') RETURNING id::text`,
		uuid.NewString()).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplacePromptComponents(ctx, f.tx, PromptComponentsWrite{
		WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopeScene, OrgID: "org-1", ScopeKey: group,
		Components: []PromptComponentInput{{Name: "Tone", Text: "Answer briefly."}}, ActorID: actor,
	}); err != nil {
		t.Fatal(err)
	}
	// A prompt stored under a scene id the directory does not know lists
	// nothing.
	if _, err := ReplacePromptComponents(ctx, f.tx, PromptComponentsWrite{
		WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopeScene, OrgID: "org-1", ScopeKey: uuid.NewString(),
		Components: []PromptComponentInput{{Name: "Tone", Text: "Orphan."}}, ActorID: actor,
	}); err != nil {
		t.Fatal(err)
	}
	command, err := json.Marshal(map[string]any{
		"source":      map[string]any{"platform": "dingtalk", "type": "digital_employee"},
		"agent_scene": map[string]any{"scene_id": group},
		"event": map[string]any{"data": map[string]any{
			"conversation": map[string]any{"openConversationId": "cidGroup==", "type": "group"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sessionID string
	if err := f.tx.QueryRow(ctx, `INSERT INTO chat_session (workspace_id, agent_id, creator_id, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'inbound') RETURNING id::text`, f.workspaceID, f.agentID, actor).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tx.Exec(ctx, `INSERT INTO inbound_coordinator_job
		(acceptance_id, workspace_id, agent_id, user_id, endpoint_namespace_id, idempotency_key, command, chat_session_id, user_message_id, status)
		VALUES (gen_random_uuid(), $1::uuid, $2::uuid, $3::uuid, gen_random_uuid(), gen_random_uuid()::text, $4, $5::uuid, gen_random_uuid(), 'completed')`,
		f.workspaceID, f.agentID, actor, command, sessionID); err != nil {
		t.Fatal(err)
	}

	scenes, more, err := ListAgentScenes(ctx, f.tx, SceneListQuery{WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-1"})
	if err != nil || more || len(scenes) != 2 {
		t.Fatalf("scenes=%+v more=%v err=%v", scenes, more, err)
	}
	if scenes[0].SceneID != group || scenes[0].Kind != SceneKindGroup || scenes[0].ConversationID != "cidGroup==" ||
		!scenes[0].HasPrompt || scenes[0].HasMemory || scenes[0].InboundSessionID != sessionID || scenes[0].InboundCount != 1 {
		t.Fatalf("group scene=%+v", scenes[0])
	}
	if scenes[1].SceneID != dm || scenes[1].Kind != SceneKindDM || !scenes[1].HasMemory || scenes[1].MemoryText != "remembered" ||
		scenes[1].HasPrompt || scenes[1].Title != "冬翔" {
		t.Fatalf("dm scene=%+v", scenes[1])
	}
	groups, _, err := ListAgentScenes(ctx, f.tx, SceneListQuery{WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-1", GroupsOnly: true})
	if err != nil || len(groups) != 1 || groups[0].SceneID != group {
		t.Fatalf("groups only=%+v err=%v", groups, err)
	}
	// The same conversation id in another org is another scene.
	if got, err := GetScene(ctx, f.tx, f.workspaceID, f.agentID, "org-2", other); err != nil || got.Title != "Other org" {
		t.Fatalf("other org scene=%+v err=%v", got, err)
	}
	if _, err := GetScene(ctx, f.tx, f.workspaceID, f.agentID, "org-2", group); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org-1 scene read under org-2: %v", err)
	}
	// An agent without a tenant org has no conversation scenes.
	if none, _, err := ListAgentScenes(ctx, f.tx, SceneListQuery{WorkspaceID: f.workspaceID, AgentID: f.agentID}); err != nil || len(none) != 0 {
		t.Fatalf("orgless listing=%+v err=%v", none, err)
	}
}

// ListAllAgentScenes returns more than one HTTP page from a single
// statement, newest activity first, and reports what the cap cut off.
func TestListAllAgentScenesBeyondOnePage(t *testing.T) {
	f := openStoreTx(t)
	requireSceneTables(t, f)
	ctx := context.Background()
	const total = maxScenesPerPage + 5
	if _, err := f.tx.Exec(ctx, `INSERT INTO agent_scene (workspace_id, agent_id, provider, tenant_org_id, source_namespace, scene_kind, external_scene_id, title, last_active_at)
		SELECT $1::uuid, $2::uuid, 'dingtalk', 'org-1', 'dingtalk.open_conversation_id', 'group', 'cidAll' || lpad(n::text, 4, '0'), 'Scene ' || n, now() - n * interval '1 minute'
		FROM generate_series(1, $3::int) AS n`, f.workspaceID, f.agentID, total); err != nil {
		t.Fatal(err)
	}
	q := SceneListQuery{WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-1", Limit: MaxAllScenes}
	page, more, err := ListAgentScenes(ctx, f.tx, q)
	if err != nil || len(page) != maxScenesPerPage || !more {
		t.Fatalf("paged listing: %d scenes, more=%v, err=%v", len(page), more, err)
	}
	all, more, err := ListAllAgentScenes(ctx, f.tx, q, MaxAllScenes)
	if err != nil || len(all) != total || more {
		t.Fatalf("whole listing: %d scenes, more=%v, err=%v", len(all), more, err)
	}
	if all[0].ConversationID != "cidAll0001" || all[total-1].ConversationID != "cidAll0205" {
		t.Fatalf("order: first=%s last=%s", all[0].ConversationID, all[total-1].ConversationID)
	}
	capped, more, err := ListAllAgentScenes(ctx, f.tx, q, 3)
	if err != nil || len(capped) != 3 || !more || capped[2].ConversationID != "cidAll0003" {
		t.Fatalf("capped listing: %+v more=%v err=%v", capped, more, err)
	}
}

func TestPersonBindingShareAndDirectLinkScene(t *testing.T) {
	f := openStoreTx(t)
	requireSceneTables(t, f)
	ctx := context.Background()
	var actor string
	if err := f.tx.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('Scene Admin', 'scene-admin-' || $1 || '@example.test') RETURNING id::text`,
		uuid.NewString()).Scan(&actor); err != nil {
		t.Fatal(err)
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

	// A person link minted in a 1:1 chat carries that chat's scene_id.
	dm := f.insertScene(t, "org-1", "dm", "cidDirect==", "Alice", 0)
	stamp := func() string { return strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")[:64] }
	link, err := InsertLink(ctx, f.tx, Link{TokenHash: stamp(), WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopePerson,
		OrgID: "org-1", ScopeKey: "staff-1", ScopeTitle: "Alice", ExtraSceneID: dm}, LinkTTLPerson)
	if err != nil || link.ExtraSceneID != dm {
		t.Fatalf("link=%+v err=%v", link, err)
	}
	redeemed, err := RedeemLink(ctx, f.tx, link.TokenHash, actor)
	if err != nil || redeemed.ExtraSceneID != dm {
		t.Fatalf("redeemed=%+v err=%v", redeemed, err)
	}
}
