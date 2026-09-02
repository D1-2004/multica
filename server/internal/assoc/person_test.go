package assoc

import (
	"context"
	"testing"
	"time"
)

func TestCanonicalPersonKeyPrefersDecimalUID(t *testing.T) {
	t.Parallel()
	key, aliases := CanonicalPersonKey("$:open-id", "123456", "staff-x")
	if key != "123456" {
		t.Fatalf("key=%q", key)
	}
	if len(aliases) != 2 {
		t.Fatalf("aliases=%v", aliases)
	}
}

func TestRecallPersonAliasHitsCanonicalEdge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	task := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-1",
		Purpose:       "预约A与B本周五下午30分钟",
		LastTouchedAt: now,
	})
	key, aliases := CanonicalPersonKey("123456", "$:open-a")
	if err := store.EnsurePerson(ctx, "ws", "ag", key, "", aliases); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID: "ws",
		AgentID:     "ag",
		SrcType:     NodeTask,
		SrcID:       task.ID,
		DstType:     NodePerson,
		DstID:       key,
		Rel:         RelTaskPerson,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID: "ws",
		AgentID:     "ag",
		IssueID:     "issue-1",
		PersonID:    "$:open-a",
		Since:       now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].TaskID != task.ID {
		t.Fatalf("alias recall missed: %+v", result.Items)
	}
}
