package assoc

import (
	"context"
	"testing"
	"time"
)

func TestCloseSceneAssociationsDropsRecallAndKeepsEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	svc := NewService(store)

	a, err := BindOutbound(ctx, store, BindOutboundInput{
		WorkspaceID: "ws",
		AgentID:     "ag",
		IssueID:     "issue-a",
		IssueTitle:  "向冬翔确认今天吃什么",
		Scene:       sceneOf("cid-a"),
		EvidenceID:  "msg-out-a",
		Purpose:     "向冬翔确认今天吃什么",
	})
	if err != nil || !a.Linked {
		t.Fatalf("bind A: %+v err=%v", a, err)
	}
	b, err := BindOutbound(ctx, store, BindOutboundInput{
		WorkspaceID: "ws",
		AgentID:     "ag",
		IssueID:     "issue-b",
		IssueTitle:  "向须莫确认明天打球",
		Scene:       sceneOf("cid-b"),
		EvidenceID:  "msg-out-b",
		Purpose:     "向须莫确认明天打球",
	})
	if err != nil || !b.Linked {
		t.Fatalf("bind B: %+v err=%v", b, err)
	}
	if err := AssociateIssueConversation(ctx, store, AssociateInput{
		WorkspaceID: "ws",
		AgentID:     "ag",
		IssueID:     "issue-a",
		IssueTitle:  "向冬翔确认今天吃什么",
		Scene:       sceneOf("cid-a"),
		EvidenceID:  "msg-in-a",
		PersonID:    "uid-a",
		Purpose:     "向冬翔确认今天吃什么",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEvent(ctx, Event{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Source:      "inbound_im",
		Direction:   DirInbound,
		EvidenceID:  "msg-in-a",
		Body:        "7点",
		OccurredAt:  now,
		SceneID:     sceneIDOf("cid-a"),
		PersonKey:   "uid-a",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateEventTask(ctx, "ws", "ag", "msg-in-a", a.TaskID); err != nil {
		t.Fatal(err)
	}

	before, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		SceneID:        sceneIDOf("cid-a"),
		ConversationID: "cid-a",
		Since:          now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) == 0 {
		t.Fatal("expected cid-a to recall a matter before reset")
	}

	got, err := svc.CloseSceneAssociations(ctx, "ws", "ag", sceneIDOf("cid-a"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ClosedEdges == 0 {
		t.Fatalf("closed edges = %d", got.ClosedEdges)
	}
	if got.UnlinkedEvents == 0 {
		t.Fatalf("unlinked events = %d", got.UnlinkedEvents)
	}

	after, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		SceneID:        sceneIDOf("cid-a"),
		ConversationID: "cid-a",
		Since:          now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 0 {
		t.Fatalf("cid-a should recall no matters, items=%+v", after.Items)
	}
	if len(after.Events) == 0 {
		t.Fatal("events must remain after reset")
	}
	inbound, err := store.GetEventByEvidence(ctx, "ws", "ag", "msg-in-a")
	if err != nil {
		t.Fatal(err)
	}
	if inbound.TaskID != "" {
		t.Fatalf("inbound event still linked to task %q", inbound.TaskID)
	}
	if inbound.Body != "7点" {
		t.Fatalf("inbound body=%q", inbound.Body)
	}

	other, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		SceneID:        sceneIDOf("cid-b"),
		ConversationID: "cid-b",
		Since:          now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Items) != 1 || other.Items[0].Issue != "issue-b" {
		t.Fatalf("cid-b should still recall, items=%+v", other.Items)
	}
}

func TestCloseSceneAssociationsRequiresConversation(t *testing.T) {
	t.Parallel()
	svc := NewService(NewMemory())
	_, err := svc.CloseSceneAssociations(context.Background(), "ws", "ag", "")
	if err == nil {
		t.Fatal("expected error")
	}
	_, err = svc.CloseSceneAssociations(context.Background(), "ws", "ag", "$ dws chat message send")
	if err == nil {
		t.Fatal("expected invalid scene id")
	}
}
