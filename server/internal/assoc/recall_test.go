package assoc

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestValidatePurpose(t *testing.T) {
	t.Parallel()
	if err := ValidatePurpose("帮我看看"); err == nil {
		t.Fatal("expected vague purpose to fail")
	}
	if err := ValidatePurpose("短"); err == nil {
		t.Fatal("expected short purpose to fail")
	}
	if err := ValidatePurpose("预约A与B本周五下午30分钟"); err != nil {
		t.Fatalf("precise purpose rejected: %v", err)
	}
}

func TestResolvePurposeFallsBackToUserMessage(t *testing.T) {
	t.Parallel()
	got, err := ResolvePurpose("报名表", "问一下冬翔，今天想吃什么")
	if err != nil {
		t.Fatal(err)
	}
	if got != "问一下冬翔，今天想吃什么" {
		t.Fatalf("got %q", got)
	}
}

func TestRecallRequiresSinceAndAnchor(t *testing.T) {
	t.Parallel()
	store := NewMemory()
	_, err := Recall(context.Background(), store, Query{
		WorkspaceID: "ws",
		AgentID:     "ag",
	})
	if err == nil || !strings.Contains(err.Error(), "since") {
		t.Fatalf("want since required, got %v", err)
	}
	_, err = Recall(context.Background(), store, Query{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Since:       time.Now().Add(-time.Hour),
	})
	if err == nil || !strings.Contains(err.Error(), "conversation_id, issue, or q") {
		t.Fatalf("want conversation, issue, or q required, got %v", err)
	}
}

func TestRecallByConversationFindsOutreach(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	task := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-1",
		Purpose:       "预约A与B本周五下午30分钟",
		Intent:        "calendar.book",
		Status:        StatusWaiting,
		LastTouchedAt: now.Add(-30 * time.Minute),
	})
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		SrcType:       NodeTask,
		SrcID:         task.ID,
		DstType:       NodeScene,
		DstID:         "cid-a",
		Rel:           RelOutreach,
		Props:         map[string]any{"kind": "dm"},
		LastTouchedAt: now.Add(-30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		SrcType:       NodeTask,
		SrcID:         task.ID,
		DstType:       NodeScene,
		DstID:         "cid-a",
		Rel:           RelWaitingOn,
		LastTouchedAt: now.Add(-30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	result, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-a",
		Since:          now.Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %d, want 1: %+v", len(result.Items), result.Items)
	}
	item := result.Items[0]
	if item.Purpose != task.Purpose {
		t.Fatalf("purpose = %q", item.Purpose)
	}
	if item.Issue != "issue-1" {
		t.Fatalf("issue = %q", item.Issue)
	}
	if len(item.Conversations) == 0 || item.Conversations[0].ConversationID != "cid-a" {
		t.Fatalf("conversations = %+v", item.Conversations)
	}
}

func TestRecallExpiredOutreachMisses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	task := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-1",
		Purpose:       "预约A与B本周五下午30分钟",
		LastTouchedAt: now.Add(-72 * time.Hour),
	})
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		SrcType:       NodeTask,
		SrcID:         task.ID,
		DstType:       NodeScene,
		DstID:         "cid-a",
		Rel:           RelOutreach,
		LastTouchedAt: now.Add(-72 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-a",
		Since:          now.Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("expected no items, got %+v", result.Items)
	}
}

func TestRecallIsolatesAgents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	task := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "other",
		IssueID:       "issue-1",
		Purpose:       "预约A与B本周五下午30分钟",
		LastTouchedAt: now,
	})
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID: "ws",
		AgentID:     "other",
		SrcType:     NodeTask,
		SrcID:       task.ID,
		DstType:     NodeScene,
		DstID:       "cid-a",
		Rel:         RelOutreach,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-a",
		Since:          now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("cross-agent leak: %+v", result.Items)
	}
}

func TestRecallByIssueListsConversations(t *testing.T) {
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
	for _, cid := range []string{"cid-a", "cid-b"} {
		if _, err := store.InsertEdge(ctx, Edge{
			WorkspaceID: "ws",
			AgentID:     "ag",
			SrcType:     NodeTask,
			SrcID:       task.ID,
			DstType:     NodeScene,
			DstID:       cid,
			Rel:         RelOutreach,
			Props:       map[string]any{"kind": "dm"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID: "ws",
		AgentID:     "ag",
		IssueID:     "issue-1",
		Since:       now.Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %d", len(result.Items))
	}
	if len(result.Items[0].Conversations) != 2 {
		t.Fatalf("conversations = %+v", result.Items[0].Conversations)
	}
}

func TestRecallKeywordFiltersPurpose(t *testing.T) {
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
	miss := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-1",
		Purpose:       "整理本周项目周报并发送",
		LastTouchedAt: now,
	})
	for _, task := range []Task{hit, miss} {
		if _, err := store.InsertEdge(ctx, Edge{
			WorkspaceID: "ws",
			AgentID:     "ag",
			SrcType:     NodeTask,
			SrcID:       task.ID,
			DstType:     NodeScene,
			DstID:       "cid-a",
			Rel:         RelTaskScene,
		}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-a",
		Q:              "本周五",
		Since:          now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].TaskID != hit.ID {
		t.Fatalf("keyword miss: %+v", result.Items)
	}
}

func TestRecallRecentFirstAndExtraPersonIsNewEdge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	older := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-old",
		Purpose:       "上周已完成的预约日程跟进",
		LastTouchedAt: now.Add(-10 * time.Hour),
	})
	newer := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-new",
		Purpose:       "预约A与B本周五下午30分钟",
		Status:        StatusWaiting,
		LastTouchedAt: now.Add(-5 * time.Minute),
	})
	for _, pair := range []struct {
		task Task
		age  time.Duration
	}{{older, 10 * time.Hour}, {newer, 5 * time.Minute}} {
		if _, err := store.InsertEdge(ctx, Edge{
			WorkspaceID:   "ws",
			AgentID:       "ag",
			SrcType:       NodeTask,
			SrcID:         pair.task.ID,
			DstType:       NodeScene,
			DstID:         "cid-a",
			Rel:           RelOutreach,
			LastTouchedAt: now.Add(-pair.age),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID: "ws",
		AgentID:     "ag",
		SrcType:     NodeTask,
		SrcID:       newer.ID,
		DstType:     NodePerson,
		DstID:       "uid-c",
		Rel:         RelTaskPerson,
		Props:       map[string]any{"delegated": true, "display_name": "C"},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-a",
		Since:          now.Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items = %d", len(result.Items))
	}
	if result.Items[0].TaskID != newer.ID {
		t.Fatalf("want newest first, got %s then %s", result.Items[0].TaskID, result.Items[1].TaskID)
	}
	if len(result.Items[0].People) != 1 || result.Items[0].People[0].PersonID != "uid-c" {
		t.Fatalf("delegated person missing: %+v", result.Items[0].People)
	}
}

func TestRecallConversationHitsWhenPersonDoesNotMatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	task := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-eat",
		Purpose:       "向冬翔确认今晚吃什么",
		LastTouchedAt: now,
	})
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID: "ws",
		AgentID:     "ag",
		SrcType:     NodeTask,
		SrcID:       task.ID,
		DstType:     NodeScene,
		DstID:       "cid-dongxiang",
		Rel:         RelOutreach,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-dongxiang",
		PersonID:       "25698887",
		Since:          now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Issue != "issue-eat" {
		t.Fatalf("cid hit must survive person mismatch: %+v", result.Items)
	}
}

func TestRecallPersonOnlyStillFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	hit := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-hit",
		Purpose:       "向冬翔确认今晚吃什么",
		LastTouchedAt: now,
	})
	miss := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-miss",
		Purpose:       "向李四确认今晚吃什么",
		LastTouchedAt: now,
	})
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID: "ws",
		AgentID:     "ag",
		SrcType:     NodeTask,
		SrcID:       hit.ID,
		DstType:     NodePerson,
		DstID:       "25698887",
		Rel:         RelTaskPerson,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID: "ws",
		AgentID:     "ag",
		SrcType:     NodeTask,
		SrcID:       miss.ID,
		DstType:     NodeScene,
		DstID:       "cid-other",
		Rel:         RelTaskScene,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID: "ws",
		AgentID:     "ag",
		PersonID:    "25698887",
		Q:           "今晚吃什么",
		Since:       now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Issue != "issue-hit" {
		t.Fatalf("person-only recall: %+v", result.Items)
	}
}

func TestRecallPersonMatchRanksAboveSceneOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	sceneOnly := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-news",
		Purpose:       "搜索并整理今天的热点新闻",
		Status:        StatusWaiting,
		LastTouchedAt: now,
	})
	withPerson := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-eat",
		Purpose:       "向冬翔确认今晚吃什么",
		Status:        StatusWaiting,
		LastTouchedAt: now,
	})
	for _, task := range []Task{sceneOnly, withPerson} {
		if _, err := store.InsertEdge(ctx, Edge{
			WorkspaceID: "ws",
			AgentID:     "ag",
			SrcType:     NodeTask,
			SrcID:       task.ID,
			DstType:     NodeScene,
			DstID:       "cid-shared",
			Rel:         RelOutreach,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID: "ws",
		AgentID:     "ag",
		SrcType:     NodeTask,
		SrcID:       withPerson.ID,
		DstType:     NodePerson,
		DstID:       "25698887",
		Rel:         RelTaskPerson,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-shared",
		PersonID:       "25698887",
		Since:          now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items=%d", len(result.Items))
	}
	if result.Items[0].Issue != "issue-eat" {
		t.Fatalf("want person-matched first, got %+v", result.Items)
	}
}

func TestInsertEventDedupsEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	first, err := store.InsertEvent(ctx, Event{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Source:      "outbound_im",
		Direction:   DirOutbound,
		EvidenceID:  "msg-1",
		SceneKey:    "cid-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.InsertEvent(ctx, Event{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Source:      "inbound_im",
		Direction:   DirInbound,
		EvidenceID:  "msg-1",
		SceneKey:    "cid-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected same event, got %s and %s", first.ID, second.ID)
	}
}

func mustInsertTask(t *testing.T, store *Memory, task Task) Task {
	t.Helper()
	out, err := store.InsertTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
