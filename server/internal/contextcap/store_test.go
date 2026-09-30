package contextcap

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// storeFixture holds rows created inside one rolled-back transaction.
type storeFixture struct {
	tx          pgx.Tx
	workspaceID string
	agentID     string
	connectorID string
	skillID     string
	otherConn   string
}

func openStoreTx(t *testing.T) storeFixture {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	var migrated bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('context_capability_binding') IS NOT NULL
		AND to_regclass('context_config_link') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
		t.Skip("context capability tables are not migrated")
	}
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	f := storeFixture{tx: tx}
	bg := context.Background()
	if err := tx.QueryRow(bg, `INSERT INTO workspace (name, slug, description) VALUES ('contextcap', 'contextcap-' || $1, '') RETURNING id::text`,
		uuid.NewString()).Scan(&f.workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(bg, `INSERT INTO agent (workspace_id, name, runtime_mode) VALUES ($1::uuid, 'contextcap agent', 'cloud') RETURNING id::text`,
		f.workspaceID).Scan(&f.agentID); err != nil {
		t.Fatal(err)
	}
	for _, target := range []*string{&f.connectorID, &f.otherConn} {
		id := uuid.NewString()
		if _, err := tx.Exec(bg, `INSERT INTO internal_connector (id, workspace_id, name, upstream_url, credential_ref, allowed_tools, enabled)
			VALUES ($1::uuid, $2::uuid, 'Knowledge', 'https://safe.example.test/mcp', 'REF', '["read"]'::jsonb, true)`, id, f.workspaceID); err != nil {
			t.Fatal(err)
		}
		*target = id
	}
	if err := tx.QueryRow(bg, `INSERT INTO skill (workspace_id, name, description, content) VALUES ($1::uuid, 'contextcap-skill-' || $2, 'desc', 'body') RETURNING id::text`,
		f.workspaceID, uuid.NewString()).Scan(&f.skillID); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestStoreOffersGateTaskBindings(t *testing.T) {
	f := openStoreTx(t)
	ctx := context.Background()
	actor := uuid.NewString()

	if err := ReplaceOffers(ctx, f.tx, f.workspaceID, f.agentID, []string{f.connectorID, f.connectorID}, []string{f.skillID}, actor); err != nil {
		t.Fatal(err)
	}
	offers, err := ListOffers(ctx, f.tx, f.workspaceID, f.agentID)
	if err != nil || len(offers.ConnectorIDs) != 1 || len(offers.SkillIDs) != 1 || !offers.Contains(ResourceConnector, f.connectorID) {
		t.Fatalf("offers = %#v err=%v", offers, err)
	}
	if offered, err := IsOffered(ctx, f.tx, f.workspaceID, f.agentID, ResourceSkill, f.skillID); err != nil || !offered {
		t.Fatalf("skill not offered: %v %v", offered, err)
	}

	scene := BindingWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopeScene, OrgID: "org-1", ScopeKey: "cidGroup",
		ScopeTitle: "Group", ResourceType: ResourceConnector, ResourceID: f.connectorID, Enabled: true, ActorID: actor}
	if _, err := UpsertBinding(ctx, f.tx, scene); err != nil {
		t.Fatal(err)
	}
	person := BindingWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopePerson, OrgID: "org-1", ScopeKey: "staff-1",
		ResourceType: ResourceSkill, ResourceID: f.skillID, Enabled: true}
	if _, err := UpsertBinding(ctx, f.tx, person); err != nil {
		t.Fatal(err)
	}
	notOffered := scene
	notOffered.ResourceID = f.otherConn
	if _, err := UpsertBinding(ctx, f.tx, notOffered); !errors.Is(err, ErrNotOffered) {
		t.Fatalf("non-offered enable: %v", err)
	}
	notOffered.Enabled = false
	if b, err := UpsertBinding(ctx, f.tx, notOffered); err != nil || b.Enabled {
		t.Fatalf("disabling a non-offered binding must be allowed: %#v %v", b, err)
	}
	invalid := scene
	invalid.ScopeKey = "not-a-cid"
	if _, err := UpsertBinding(ctx, f.tx, invalid); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid scene key: %v", err)
	}

	full := Scope{OrgID: "org-1", SceneKey: "cidGroup", PersonKey: "staff-1"}
	bindings, err := TaskBindings(ctx, f.tx, f.workspaceID, f.agentID, full)
	if err != nil || len(bindings) != 2 {
		t.Fatalf("task bindings = %#v err=%v", bindings, err)
	}
	for _, scope := range []Scope{
		{OrgID: "org-2", SceneKey: "cidGroup", PersonKey: "staff-1"},
		{OrgID: "org-1", SceneKey: "cidOther", PersonKey: "staff-2"},
		{OrgID: "org-1"},
	} {
		if got, err := TaskBindings(ctx, f.tx, f.workspaceID, f.agentID, scope); err != nil || len(got) != 0 {
			t.Fatalf("scope %#v leaked bindings %#v err=%v", scope, got, err)
		}
	}
	if got, _ := TaskBindings(ctx, f.tx, f.workspaceID, f.agentID, Scope{OrgID: "org-1", SceneKey: "cidGroup"}); len(got) != 1 || got[0].ResourceID != f.connectorID {
		t.Fatalf("scene-only bindings = %#v", got)
	}

	// Removing the connector offer disables its scene binding at once.
	if err := ReplaceOffers(ctx, f.tx, f.workspaceID, f.agentID, nil, []string{f.skillID}, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := TaskBindings(ctx, f.tx, f.workspaceID, f.agentID, full); len(got) != 1 || got[0].ResourceType != ResourceSkill {
		t.Fatalf("offer removal did not disable binding: %#v", got)
	}
	rows, err := ListScopeBindings(ctx, f.tx, f.workspaceID, f.agentID, ScopeScene, "org-1", "cidGroup")
	if err != nil || len(rows) != 2 {
		t.Fatalf("scope bindings keep raw rows: %#v %v", rows, err)
	}

	if err := ReplaceOffers(ctx, f.tx, f.workspaceID, f.agentID, []string{uuid.NewString()}, nil, ""); !errors.Is(err, ErrUnknownResource) {
		t.Fatalf("unknown connector offered: %v", err)
	}
	if err := ReplaceOffers(ctx, f.tx, uuid.NewString(), f.agentID, nil, nil, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("agent outside workspace: %v", err)
	}
}

func TestStoreCredentialsAndSummaries(t *testing.T) {
	f := openStoreTx(t)
	ctx := context.Background()
	key := CredentialBinding{WorkspaceID: f.workspaceID, AgentID: f.agentID, ConnectorID: f.connectorID, ScopeType: ScopePerson, OrgID: "org-1", ScopeKey: "staff-1"}
	stored, err := UpsertCredential(ctx, f.tx, key, []byte("sealed-1"), "••••1111", "")
	if err != nil || stored.Hint != "••••1111" || stored.UpdatedAt.IsZero() {
		t.Fatalf("upsert: %#v %v", stored, err)
	}
	if _, err := UpsertCredential(ctx, f.tx, key, []byte("sealed-2"), "••••2222", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	got, err := GetCredential(ctx, f.tx, key)
	if err != nil || string(got.Ciphertext) != "sealed-2" || got.Hint != "••••2222" {
		t.Fatalf("get: %#v %v", got, err)
	}
	sceneKey := key
	sceneKey.ScopeType, sceneKey.ScopeKey = ScopeScene, "cidGroup"
	if _, err := UpsertCredential(ctx, f.tx, sceneKey, []byte("sealed-scene"), "••••", ""); err != nil {
		t.Fatal(err)
	}
	list, err := ListScopeCredentials(ctx, f.tx, f.workspaceID, f.agentID, ScopePerson, "org-1", "staff-1")
	if err != nil || len(list) != 1 || list[0].Ciphertext != nil {
		t.Fatalf("list must not load ciphertext: %#v %v", list, err)
	}
	task, err := TaskCredentials(ctx, f.tx, f.workspaceID, f.agentID, Scope{OrgID: "org-1", SceneKey: "cidGroup", PersonKey: "staff-1"})
	if err != nil || len(task) != 2 {
		t.Fatalf("task credentials: %#v %v", task, err)
	}
	if task, _ := TaskCredentials(ctx, f.tx, f.workspaceID, f.agentID, Scope{OrgID: "org-1", PersonKey: "staff-2"}); len(task) != 0 {
		t.Fatalf("other person reached credential: %#v", task)
	}

	if err := ReplaceOffers(ctx, f.tx, f.workspaceID, f.agentID, []string{f.connectorID}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertBinding(ctx, f.tx, BindingWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopeScene, OrgID: "org-1",
		ScopeKey: "cidGroup", ScopeTitle: "Group title", ResourceType: ResourceConnector, ResourceID: f.connectorID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	summaries, err := ListScopeSummaries(ctx, f.tx, f.workspaceID, f.agentID)
	if err != nil || len(summaries) != 2 {
		t.Fatalf("summaries: %#v %v", summaries, err)
	}
	if summaries[0].ScopeType != ScopePerson || summaries[0].CredentialCount != 1 || len(summaries[0].Bindings) != 0 ||
		summaries[1].ScopeType != ScopeScene || summaries[1].ScopeTitle != "Group title" || summaries[1].CredentialCount != 1 || len(summaries[1].Bindings) != 1 {
		t.Fatalf("unexpected summaries: %#v", summaries)
	}

	if deleted, err := DeleteCredential(ctx, f.tx, key); err != nil || !deleted {
		t.Fatalf("delete: %v %v", deleted, err)
	}
	if deleted, _ := DeleteCredential(ctx, f.tx, key); deleted {
		t.Fatal("second delete reported a row")
	}
	if _, err := GetCredential(ctx, f.tx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted credential: %v", err)
	}
}

func TestStoreGrantsAndLinks(t *testing.T) {
	f := openStoreTx(t)
	ctx := context.Background()
	user := uuid.NewString()

	grant := Grant{UserID: user, WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopeScene, OrgID: "org-1",
		ScopeKey: "cidGroup", ScopeTitle: "Group", Source: GrantSourceAgentLink}
	long, err := UpsertGrant(ctx, f.tx, grant, GrantTTLScene)
	if err != nil || long.ExpiresAt.Before(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("grant: %#v %v", long, err)
	}
	grant.ScopeTitle = ""
	grant.Source = GrantSourceJSAPI
	renewed, err := UpsertGrant(ctx, f.tx, grant, time.Minute)
	if err != nil || !renewed.ExpiresAt.Equal(long.ExpiresAt) || renewed.ScopeTitle != "Group" || renewed.Source != GrantSourceJSAPI {
		t.Fatalf("renewal shortened expiry or lost title: %#v %v", renewed, err)
	}
	if _, err := GetLiveGrant(ctx, f.tx, user, f.agentID, ScopeScene, "org-1", "cidGroup"); err != nil {
		t.Fatal(err)
	}
	if _, err := GetLiveGrant(ctx, f.tx, user, f.agentID, ScopeScene, "org-2", "cidGroup"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("grant crossed org: %v", err)
	}
	if list, err := ListLiveGrantsForUser(ctx, f.tx, user, ""); err != nil || len(list) != 1 {
		t.Fatalf("live grants: %#v %v", list, err)
	}
	if list, _ := ListLiveGrantsForUser(ctx, f.tx, user, uuid.NewString()); len(list) != 0 {
		t.Fatalf("agent filter ignored: %#v", list)
	}
	if _, err := f.tx.Exec(ctx, `UPDATE context_config_grant SET expires_at = now() - interval '1 second' WHERE user_id = $1::uuid`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := GetLiveGrant(ctx, f.tx, user, f.agentID, ScopeScene, "org-1", "cidGroup"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired grant is live: %v", err)
	}

	personToken, _ := NewLinkToken()
	personLink, err := InsertLink(ctx, f.tx, Link{TokenHash: HashLinkToken(personToken), WorkspaceID: f.workspaceID, AgentID: f.agentID,
		ScopeType: ScopePerson, OrgID: "org-1", ScopeKey: "staff-1", ScopeTitle: "Alice"}, LinkTTLPerson)
	if err != nil || personLink.ExpiresAt.After(time.Now().Add(16*time.Minute)) || personLink.SourceTaskID != "" {
		t.Fatalf("insert person link: %#v %v", personLink, err)
	}
	if redeemed, err := RedeemLink(ctx, f.tx, HashLinkToken(personToken), user); err != nil || redeemed.ScopeKey != "staff-1" {
		t.Fatalf("person redeem: %#v %v", redeemed, err)
	}
	if _, err := RedeemLink(ctx, f.tx, HashLinkToken(personToken), uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("person link redeemed twice: %v", err)
	}

	sceneToken, _ := NewLinkToken()
	sourceTask := uuid.NewString()
	if _, err := InsertLink(ctx, f.tx, Link{TokenHash: HashLinkToken(sceneToken), WorkspaceID: f.workspaceID, AgentID: f.agentID,
		ScopeType: ScopeScene, OrgID: "org-1", ScopeKey: "cidGroup", SourceTaskID: sourceTask}, LinkTTLScene); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if redeemed, err := RedeemLink(ctx, f.tx, HashLinkToken(sceneToken), uuid.NewString()); err != nil || redeemed.SourceTaskID != sourceTask {
			t.Fatalf("scene redeem %d: %#v %v", i, redeemed, err)
		}
	}
	if _, err := f.tx.Exec(ctx, `UPDATE context_config_link SET expires_at = now() - interval '1 second' WHERE token_hash = $1`, HashLinkToken(sceneToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := RedeemLink(ctx, f.tx, HashLinkToken(sceneToken), user); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired scene link redeemed: %v", err)
	}
	if _, err := RedeemLink(ctx, f.tx, HashLinkToken("unknown"), user); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown link redeemed: %v", err)
	}
	if _, err := InsertLink(ctx, f.tx, Link{TokenHash: HashLinkToken("x"), WorkspaceID: f.workspaceID, AgentID: f.agentID,
		ScopeType: ScopeOffer, ScopeKey: "k"}, LinkTTLScene); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("offer link accepted: %v", err)
	}
}
