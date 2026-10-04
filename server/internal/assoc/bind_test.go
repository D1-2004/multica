package assoc

import (
	"context"
	"testing"
	"time"
)

func TestBindOutboundRequiresConversation(t *testing.T) {
	t.Parallel()
	_, err := BindOutbound(context.Background(), NewMemory(), BindOutboundInput{
		WorkspaceID: "ws",
		AgentID:     "ag",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGoldenBookTwoPeople(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	ws := "ws"
	ag := "ag"
	issue := "issue-book"
	in := BindOutboundInput{
		WorkspaceID: ws,
		AgentID:     ag,
		IssueID:     issue,
		IssueTitle:  "预约A与B本周五下午30分钟",
		Intent:      "calendar.book",
	}

	a, err := BindOutbound(ctx, store, BindOutboundInput{
		WorkspaceID: ws,
		AgentID:     ag,
		IssueID:     issue,
		IssueTitle:  in.IssueTitle,
		Scene:       sceneOf("cid-a"),
		EvidenceID:  "msg-out-a",
		Intent:      "calendar.book",
	})
	if err != nil || !a.Linked {
		t.Fatalf("bind A: %+v err=%v", a, err)
	}
	b, err := BindOutbound(ctx, store, BindOutboundInput{
		WorkspaceID: ws,
		AgentID:     ag,
		IssueID:     issue,
		IssueTitle:  in.IssueTitle,
		Scene:       sceneOf("cid-b"),
		EvidenceID:  "msg-out-b",
	})
	if err != nil || b.TaskID != a.TaskID {
		t.Fatalf("bind B should reuse task: %+v vs %+v err=%v", a, b, err)
	}

	if _, err := store.InsertEvent(ctx, Event{
		WorkspaceID: ws,
		AgentID:     ag,
		Source:      "inbound_im",
		Direction:   DirInbound,
		EvidenceID:  "msg-in-a",
		SceneID:     sceneIDOf("cid-a"),
	}); err != nil {
		t.Fatal(err)
	}
	dup, err := store.InsertEvent(ctx, Event{
		WorkspaceID: ws,
		AgentID:     ag,
		Source:      "outbound_im",
		Direction:   DirOutbound,
		EvidenceID:  "msg-out-a",
		SceneID:     sceneIDOf("cid-a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dup.EvidenceID != "msg-out-a" {
		t.Fatalf("dedup lost evidence")
	}

	if err := AssociateIssueConversation(ctx, store, AssociateInput{
		WorkspaceID: ws,
		AgentID:     ag,
		IssueID:     issue,
		IssueTitle:  in.IssueTitle,
		Scene:       sceneOf("cid-a"),
		EvidenceID:  "msg-in-a",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID: ws,
		AgentID:     ag,
		SrcType:     NodeTask,
		SrcID:       a.TaskID,
		DstType:     NodePerson,
		DstID:       "uid-c",
		Rel:         RelTaskPerson,
		Props:       map[string]any{"delegated": true},
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	byA, err := Recall(ctx, store, Query{
		WorkspaceID:    ws,
		AgentID:        ag,
		SceneID:        sceneIDOf("cid-a"),
		ConversationID: "cid-a",
		Since:          now.Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byA.Items) != 1 || byA.Items[0].Purpose != in.IssueTitle {
		t.Fatalf("recall by A: %+v", byA.Items)
	}

	byIssue, err := Recall(ctx, store, Query{
		WorkspaceID: ws,
		AgentID:     ag,
		IssueID:     issue,
		Since:       now.Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byIssue.Items) != 1 {
		t.Fatalf("issue items=%d", len(byIssue.Items))
	}
	cids := map[string]bool{}
	for _, conv := range byIssue.Items[0].Conversations {
		cids[conv.ConversationID] = true
	}
	if !cids["cid-a"] || !cids["cid-b"] {
		t.Fatalf("issue conversations=%+v", byIssue.Items[0].Conversations)
	}
	foundC := false
	for _, p := range byIssue.Items[0].People {
		if p.PersonID == "uid-c" {
			foundC = true
		}
	}
	if !foundC {
		t.Fatalf("delegated person missing: %+v", byIssue.Items[0].People)
	}
}

func TestBindOutboundWithoutIssueRecordsEventOnly(t *testing.T) {
	t.Parallel()
	store := NewMemory()
	got, err := BindOutbound(context.Background(), store, BindOutboundInput{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Scene:       sceneOf("cid-x"),
		EvidenceID:  "msg-x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Linked {
		t.Fatal("expected unlinked bind")
	}
	result, err := Recall(context.Background(), store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		SceneID:        sceneIDOf("cid-x"),
		ConversationID: "cid-x",
		Since:          time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("event-only bind must not create a task: %+v", result.Items)
	}
}

// The Host resolves conversation ids to scenes; a scene node must be a
// scene id, never tool command text or a raw conversation id.
func TestBindOutboundRejectsNonSceneIDs(t *testing.T) {
	t.Parallel()
	for _, id := range []string{`$ dws chat message send --user 103262 --content "hi"`, "cid-a", ""} {
		_, err := BindOutbound(context.Background(), NewMemory(), BindOutboundInput{
			WorkspaceID: "ws",
			AgentID:     "ag",
			IssueID:     "issue-1",
			IssueTitle:  "向冬翔确认今晚吃什么",
			Scene:       SceneNode{SceneID: id, ConversationID: id},
		})
		if err == nil {
			t.Fatalf("expected invalid scene for %q", id)
		}
	}
}

func TestListEventsBySceneFiltersConversation(t *testing.T) {
	t.Parallel()
	store := NewMemory()
	now := time.Now().UTC()
	if _, err := store.InsertEvent(context.Background(), Event{
		WorkspaceID: "ws", AgentID: "ag", Source: "outbound_im", Direction: DirOutbound,
		EvidenceID: "out-1", SceneID: sceneIDOf("cid-a"), OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEvent(context.Background(), Event{
		WorkspaceID: "ws", AgentID: "ag", Source: "inbound_im", Direction: DirInbound,
		EvidenceID: "in-other", SceneID: sceneIDOf("cid-b"), OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListEventsByScene(context.Background(), "ws", "ag", sceneIDOf("cid-a"), now.Add(-time.Hour), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].EvidenceID != "out-1" {
		t.Fatalf("events=%+v", got)
	}
}

func TestBindOutboundUsesPurposeWhenTitleShort(t *testing.T) {
	t.Parallel()
	store := NewMemory()
	got, err := BindOutbound(context.Background(), store, BindOutboundInput{
		WorkspaceID: "ws",
		AgentID:     "ag",
		IssueID:     "issue-eat",
		IssueTitle:  "报名表",
		Purpose:     "向冬翔确认今天吃什么",
		Scene:       sceneOf("cid-dongxiang"),
		EvidenceID:  "msg-out",
	})
	if err != nil || !got.Linked {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	task, err := store.GetOpenTaskByIssue(context.Background(), "ws", "ag", "issue-eat")
	if err != nil {
		t.Fatal(err)
	}
	if task.Purpose != "向冬翔确认今天吃什么" {
		t.Fatalf("purpose=%q", task.Purpose)
	}
}

func TestBindOutboundRejectsVaguePurpose(t *testing.T) {
	t.Parallel()
	_, err := BindOutbound(context.Background(), NewMemory(), BindOutboundInput{
		WorkspaceID: "ws",
		AgentID:     "ag",
		IssueID:     "issue-1",
		IssueTitle:  "帮我看看",
		Scene:       sceneOf("cid-a"),
	})
	if err == nil {
		t.Fatal("expected vague purpose to fail")
	}
}

func TestBindOutboundDedupesConversationsAndUsesEventID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	got, err := BindOutbound(ctx, store, BindOutboundInput{
		WorkspaceID: "ws",
		AgentID:     "ag",
		IssueID:     "issue-1",
		IssueTitle:  "预约A与B本周五下午30分钟",
		Scene:       sceneOf("cid-a"),
		EvidenceID:  "msg-out-a",
	})
	if err != nil || !got.Linked {
		t.Fatalf("bind: %+v err=%v", got, err)
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID: "ws",
		AgentID:     "ag",
		IssueID:     "issue-1",
		Since:       time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Conversations) != 1 {
		t.Fatalf("expected one conversation, got %+v", result.Items)
	}
	conv := result.Items[0].Conversations[0]
	if conv.Rel != RelOutreach {
		t.Fatalf("rel=%q", conv.Rel)
	}
	if !containsRel(conv.Rels, RelOutreach) || !containsRel(conv.Rels, RelTaskScene) {
		t.Fatalf("rels=%v want outreach and task_scene", conv.Rels)
	}
	ev, err := store.GetEventByEvidence(ctx, "ws", "ag", "msg-out-a")
	if err != nil {
		t.Fatal(err)
	}
	edges, err := store.ListEdgesBySrc(ctx, "ws", "ag", NodeEvent, ev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || edges[0].Rel != RelEventOf {
		t.Fatalf("event_of edges=%+v", edges)
	}
}

func TestInboundEventUnlinkedUntilAssociate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	if _, err := BindOutbound(ctx, store, BindOutboundInput{
		WorkspaceID: "ws",
		AgentID:     "ag",
		IssueID:     "issue-1",
		IssueTitle:  "预约A与B本周五下午30分钟",
		Scene:       sceneOf("cid-a"),
		EvidenceID:  "msg-out-a",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEvent(ctx, Event{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Source:      "inbound_im",
		Direction:   DirInbound,
		EvidenceID:  "msg-in-a",
		SceneID:     sceneIDOf("cid-a"),
	}); err != nil {
		t.Fatal(err)
	}
	inbound, err := store.GetEventByEvidence(ctx, "ws", "ag", "msg-in-a")
	if err != nil {
		t.Fatal(err)
	}
	if inbound.TaskID != "" {
		t.Fatalf("inbound task_id should stay empty until associate: %q", inbound.TaskID)
	}
	if err := AssociateIssueConversation(ctx, store, AssociateInput{
		WorkspaceID: "ws",
		AgentID:     "ag",
		IssueID:     "issue-1",
		IssueTitle:  "预约A与B本周五下午30分钟",
		Scene:       sceneOf("cid-a"),
		EvidenceID:  "msg-in-a",
	}); err != nil {
		t.Fatal(err)
	}
	linked, err := store.GetEventByEvidence(ctx, "ws", "ag", "msg-in-a")
	if err != nil {
		t.Fatal(err)
	}
	if linked.TaskID == "" {
		t.Fatal("associate should fill inbound task_id")
	}
	edges, err := store.ListEdgesBySrc(ctx, "ws", "ag", NodeEvent, linked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || edges[0].Rel != RelEventOf || edges[0].SrcID != linked.ID {
		t.Fatalf("event_of=%+v", edges)
	}
}

func TestRecallKeywordWithoutConversation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	hit := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-1",
		Purpose:       "预约A与B本周五下午30分钟",
		LastTouchedAt: now,
	})
	_ = mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-2",
		Purpose:       "整理本周项目周报并发送",
		LastTouchedAt: now,
	})
	result, err := Recall(ctx, store, Query{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Q:           "本周五",
		Since:       now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].TaskID != hit.ID {
		t.Fatalf("q-only recall: %+v", result.Items)
	}
}

func TestInsertEdgeMergesProps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	first, err := store.InsertEdge(ctx, Edge{
		WorkspaceID: "ws",
		AgentID:     "ag",
		SrcType:     NodeTask,
		SrcID:       "task-1",
		DstType:     NodePerson,
		DstID:       "123",
		Rel:         RelTaskPerson,
		Props:       map[string]any{"delegated": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.InsertEdge(ctx, Edge{
		WorkspaceID: "ws",
		AgentID:     "ag",
		SrcType:     NodeTask,
		SrcID:       "task-1",
		DstType:     NodePerson,
		DstID:       "123",
		Rel:         RelTaskPerson,
		Props:       map[string]any{"display_name": "A"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatal("expected upsert")
	}
	if second.Props["delegated"] != true || second.Props["display_name"] != "A" {
		t.Fatalf("props not merged: %+v", second.Props)
	}
}
