package employeememory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPostgresManagedSceneMemoryIsolatedAndRevisionChecked(t *testing.T) {
	pool := memoryPool(t)
	store := NewStore(pool)
	scope := memoryScope(t, pool)
	ctx := context.Background()
	record(t, store, scope, note("shared", "Employee scene note"), evidence("shared"))
	private := scope
	private.Kind = ScopePrivate
	private.PrincipalID = "person-private"
	record(t, store, private, note("private", "PRIVATE_OTHER_PRINCIPAL"), evidence("private"))
	if _, err := pool.Exec(ctx, "INSERT INTO agent_scene_memory(scene_id,workspace_id,agent_id,memory_text) VALUES($1,$2,$3,'Coordinator untouched')", scope.Scene.SceneID, scope.WorkspaceID, scope.AgentID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ManagedScene(ctx, scope)
	if err != nil || snapshot.Revision != 1 || len(snapshot.Learnings) != 1 || snapshot.Learnings[0].Insight != "Employee scene note" {
		t.Fatalf("scene snapshot=%+v err=%v", snapshot, err)
	}
	list, err := store.ManagedScenes(ctx, scope.WorkspaceID, scope.AgentID, []string{scope.TenantOrgID}, 20)
	if err != nil || len(list) != 1 || len(list[0].Learnings) != 1 {
		t.Fatalf("scene list=%+v err=%v", list, err)
	}
	foreign, err := store.ManagedScenes(ctx, scope.WorkspaceID, scope.AgentID, []string{"org-other"}, 20)
	if err != nil || len(foreign) != 0 {
		t.Fatal("another tenant appeared in list")
	}
	if _, err := store.ManagedScene(ctx, private); !errors.Is(err, ErrInvalidScope) {
		t.Fatal("manager queried private namespace")
	}
	stale := int64(0)
	if _, err := store.ResetScene(ctx, scope, &stale); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale reset=%v", err)
	}
	current := snapshot.Revision
	reset, err := store.ResetScene(ctx, scope, &current)
	if err != nil || reset.Revision != 2 || len(reset.Learnings) != 0 {
		t.Fatalf("reset=%+v err=%v", reset, err)
	}
	if _, err := store.ResetScene(ctx, scope, &current); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("repeated reset=%v", err)
	}
	privateBrief, err := store.Brief(ctx, private, "", 10)
	if err != nil || !strings.Contains(privateBrief, "PRIVATE_OTHER_PRINCIPAL") {
		t.Fatal("scene reset touched private memory")
	}
	var old string
	if err := pool.QueryRow(ctx, "SELECT memory_text FROM agent_scene_memory WHERE scene_id=$1", scope.Scene.SceneID).Scan(&old); err != nil || old != "Coordinator untouched" {
		t.Fatalf("old loop altered=%q err=%v", old, err)
	}
	record(t, store, scope, note("shared", "Employee scene note"), evidence("shared"))
	after, err := store.ManagedScene(ctx, scope)
	if err != nil || len(after.Learnings) != 0 {
		t.Fatal("reset replay resurrected learning")
	}
}
