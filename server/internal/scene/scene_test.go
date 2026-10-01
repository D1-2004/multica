package scene

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestKindFromConversationType(t *testing.T) {
	for raw, want := range map[string]string{
		"group": KindGroup, "GROUP": KindGroup, "2": KindGroup,
		"single": KindDM, "p2p": KindDM, "private": KindDM, "direct": KindDM, "1": KindDM,
	} {
		got, ok := KindFromConversationType(raw)
		if !ok || got != want {
			t.Errorf("KindFromConversationType(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "channel", "unknown", "3"} {
		if got, ok := KindFromConversationType(raw); ok {
			t.Errorf("KindFromConversationType(%q) = %q; want unknown", raw, got)
		}
	}
}

func TestLocatorNormalize(t *testing.T) {
	if _, err := DingTalkConversation("", KindGroup, "cidA").Normalize(); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("missing org: %v", err)
	}
	if _, err := DingTalkConversation("org1", KindDM, "").Normalize(); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("missing conversation: %v", err)
	}
	if _, err := DingTalkConversation("org1", "", "cidA").Normalize(); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("unknown kind: %v", err)
	}
	if _, err := DingTalkConversation("org1", KindGroup, "staff-123").Normalize(); !errors.Is(err, ErrInvalidLocator) {
		t.Fatalf("a staffId is not a conversation: %v", err)
	}
	if _, err := DingTalkConversation("org1", KindGroup, "cid with space").Normalize(); !errors.Is(err, ErrInvalidLocator) {
		t.Fatalf("whitespace: %v", err)
	}
	enterprise := DingTalkEnterprise("org1")
	enterprise.ExternalID = "org2"
	if _, err := enterprise.Normalize(); !errors.Is(err, ErrInvalidLocator) {
		t.Fatalf("enterprise of another org: %v", err)
	}
	got, err := DingTalkConversation(" org1 ", KindGroup, " cidA ").Normalize()
	if err != nil || got.TenantOrgID != "org1" || got.ExternalID != "cidA" {
		t.Fatalf("trimmed locator = %+v, %v", got, err)
	}
}

func TestCheckTenant(t *testing.T) {
	s := db.AgentScene{TenantOrgID: "org1"}
	if err := CheckTenant(s, "org1"); err != nil {
		t.Fatalf("same org: %v", err)
	}
	if err := CheckTenant(s, "org2"); !errors.Is(err, ErrStaleTenant) {
		t.Fatalf("rebound org: %v", err)
	}
	if err := CheckTenant(s, ""); !errors.Is(err, ErrStaleTenant) {
		t.Fatalf("unbound agent: %v", err)
	}
}

func testPool(t *testing.T) *pgxpool.Pool {
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
	if err := pool.QueryRow(ctx, `SELECT to_regclass('agent_scene_locator_idx') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
		t.Skip("agent_scene is not migrated")
	}
	return pool
}

func newOwner() Owner {
	return Owner{WorkspaceID: util.MustParseUUID(uuid.NewString()), AgentID: util.MustParseUUID(uuid.NewString())}
}

func TestResolveOneIDPerAgentTenantAndConversation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	agentA := newOwner()
	agentB := Owner{WorkspaceID: agentA.WorkspaceID, AgentID: util.MustParseUUID(uuid.NewString())}
	group := DingTalkConversation("org1", KindGroup, "cidGroup1")

	first, err := Resolve(ctx, q, agentA, group, Observation{Title: "项目群"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := Resolve(ctx, q, agentA, group, Observation{ActiveAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Title != "项目群" {
		t.Fatalf("same agent, org and conversation must keep scene %v (title kept), got %v %q", first.ID, again.ID, again.Title)
	}

	// The same group seen by another agent is that agent's own scene.
	other, err := Resolve(ctx, q, agentB, group, Observation{})
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Fatal("two agents in one group must not share a scene")
	}
	// The same conversation id in another org never resolves to org1's scene.
	otherOrg, err := Resolve(ctx, q, agentA, DingTalkConversation("org2", KindGroup, "cidGroup1"), Observation{})
	if err != nil {
		t.Fatal(err)
	}
	if otherOrg.ID == first.ID {
		t.Fatal("the same conversation id in another org must be another scene")
	}

	// Two 1:1 chats are two scenes, whoever the counterpart is.
	dm1, err := Resolve(ctx, q, agentA, DingTalkConversation("org1", KindDM, "cidDM1"), Observation{Title: "冬翔"})
	if err != nil {
		t.Fatal(err)
	}
	dm2, err := Resolve(ctx, q, agentA, DingTalkConversation("org1", KindDM, "cidDM2"), Observation{Title: "冬翔"})
	if err != nil {
		t.Fatal(err)
	}
	if dm1.ID == dm2.ID {
		t.Fatal("two 1:1 conversations must be two scenes")
	}

	// A conversation registered as a group is never also a dm.
	if _, err := Resolve(ctx, q, agentA, DingTalkConversation("org1", KindDM, "cidGroup1"), Observation{}); !errors.Is(err, ErrKindConflict) {
		t.Fatalf("kind conflict: %v", err)
	}

	// Lookup without a kind finds the registered scene; it never registers.
	found, err := Lookup(ctx, q, agentA, DingTalkConversation("org1", "", "cidGroup1"))
	if err != nil || found.ID != first.ID {
		t.Fatalf("lookup = %v, %v", found.ID, err)
	}
	if _, err := Lookup(ctx, q, agentA, DingTalkConversation("org1", "", "cidNever")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup of an unseen conversation: %v", err)
	}

	// Get is scoped to the owner.
	if _, err := Get(ctx, q, agentB, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another agent's scene: %v", err)
	}
	if got, err := Get(ctx, q, agentA, first.ID); err != nil || got.ExternalSceneID != "cidGroup1" {
		t.Fatalf("own scene: %+v, %v", got, err)
	}

	// The enterprise scene is the tenant org itself.
	ent, err := Resolve(ctx, q, agentA, DingTalkEnterprise("org1"), Observation{})
	if err != nil || ent.SceneKind != KindEnterprise || ent.ExternalSceneID != "org1" {
		t.Fatalf("enterprise scene: %+v, %v", ent, err)
	}
}

func TestResolveConcurrentFirstSightingReturnsOneID(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	owner := newOwner()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_scene WHERE workspace_id = $1 AND agent_id = $2`, owner.WorkspaceID, owner.AgentID)
	})
	loc := DingTalkConversation("org1", KindGroup, "cidConcurrent")
	const n = 8
	ids := make([]pgtype.UUID, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tx, err := pool.Begin(ctx)
			if err != nil {
				errs[i] = err
				return
			}
			s, err := Resolve(ctx, db.New(tx), owner, loc, Observation{})
			if err != nil {
				_ = tx.Rollback(ctx)
				errs[i] = err
				return
			}
			ids[i] = s.ID
			errs[i] = tx.Commit(ctx)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("resolver %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("concurrent first sightings returned %v and %v", ids[0], ids[i])
		}
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_scene WHERE workspace_id = $1 AND agent_id = $2`, owner.WorkspaceID, owner.AgentID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("directory rows = %d, want 1", rows)
	}
}
