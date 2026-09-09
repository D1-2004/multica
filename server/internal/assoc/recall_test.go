package assoc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNormalizeConversationID(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`cid74QGZieWQ4ondi1b0m2DtQ==`:       `cid74QGZieWQ4ondi1b0m2DtQ==`,
		`"cid74QGZieWQ4ondi1b0m2DtQ=="`:     `cid74QGZieWQ4ondi1b0m2DtQ==`,
		`'cidviyliGA6bfBKZARuuy0RzA=='`:     `cidviyliGA6bfBKZARuuy0RzA==`,
		`  "cid74QGZieWQ4ondi1b0m2DtQ=="  `: `cid74QGZieWQ4ondi1b0m2DtQ==`,
	}
	for in, want := range cases {
		if got := NormalizeConversationID(in); got != want {
			t.Fatalf("NormalizeConversationID(%q)=%q want %q", in, got, want)
		}
	}
}

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

func TestPurposeNamesEvent(t *testing.T) {
	t.Parallel()
	if PurposeNamesEvent("冬翔委托：向辰驷确认明天几点打球") != true {
		t.Fatal("expected event after 委托：")
	}
	if PurposeNamesEvent("向辰驷确认明天几点打球") != true {
		t.Fatal("expected purpose without 委托 still names an event")
	}
	if PurposeNamesEvent("某人委托：") {
		t.Fatal("empty event after 委托： is not a matter")
	}
	if PurposeNamesEvent("帮我看看") {
		t.Fatal("vague event is not a matter")
	}
}

func TestComposeCoordinatorPurposeRequiresDelegatorAndPlace(t *testing.T) {
	t.Parallel()
	got, err := ComposeCoordinatorPurpose("冬翔", "", "向辰驷确认明天几点打球")
	if err != nil {
		t.Fatal(err)
	}
	if got != "冬翔委托：向辰驷确认明天几点打球" {
		t.Fatalf("got %q", got)
	}
	got, err = ComposeCoordinatorPurpose("冬翔", "公司楼下球场", "向辰驷确认明天几点打球")
	if err != nil {
		t.Fatal(err)
	}
	if got != "冬翔委托：向辰驷确认明天几点打球（地点：公司楼下球场）" {
		t.Fatalf("got %q", got)
	}
	if _, err := ComposeCoordinatorPurpose("", "", "向辰驷确认明天几点打球"); err == nil {
		t.Fatal("expected delegator required")
	}
	if err := ValidateCoordinatorPurpose("须莫🥥 在钉钉会话中的消息：你看看你联系人里有须莫v6吗"); err == nil {
		t.Fatal("expected envelope purpose to fail")
	}
	if err := ValidateCoordinatorPurpose("冬翔委托：向须莫v6询问明早有没有会议，dws要用dws chat data-auth"); err == nil {
		t.Fatal("expected tooling purpose to fail")
	}
	if err := ValidateCoordinatorPurpose("106201委托：向本群汇报当前运行环境的关键配置（环境变量/DWS身份/MCP/Skills）简略状态"); err != nil {
		t.Fatalf("DWS身份 as the requested object must pass: %v", err)
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
	if !item.OnThisScene || item.IssueID != "issue-1" {
		t.Fatalf("on_this_scene=%v issue_id=%q", item.OnThisScene, item.IssueID)
	}
	if result.ReadThis == "" || item.WhyListed != "本会话事项" {
		t.Fatalf("read_this=%q why_listed=%q", result.ReadThis, item.WhyListed)
	}
}

func TestRecallMergesQuotedConversationIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	task := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-meet",
		Purpose:       "须莫🥥委托：向须莫v6询问晚上有没有会议",
		Status:        StatusWaiting,
		LastTouchedAt: now.Add(-30 * time.Minute),
	})
	for _, dst := range []string{`"cid74QGZieWQ4ondi1b0m2DtQ=="`, "cid74QGZieWQ4ondi1b0m2DtQ=="} {
		if _, err := store.InsertEdge(ctx, Edge{
			WorkspaceID:   "ws",
			AgentID:       "ag",
			SrcType:       NodeTask,
			SrcID:         task.ID,
			DstType:       NodeScene,
			DstID:         dst,
			Rel:           RelTaskScene,
			LastTouchedAt: now.Add(-30 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		SrcType:       NodeTask,
		SrcID:         task.ID,
		DstType:       NodeScene,
		DstID:         `"cidviyliGA6bfBKZARuuy0RzA=="`,
		Rel:           RelOutreach,
		LastTouchedAt: now.Add(-30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: `"cid74QGZieWQ4ondi1b0m2DtQ=="`,
		Since:          now.Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ConversationID != "cid74QGZieWQ4ondi1b0m2DtQ==" {
		t.Fatalf("conversation_id=%q", result.ConversationID)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items=%+v", result.Items)
	}
	ids := map[string]string{}
	for _, conv := range result.Items[0].Conversations {
		if strings.Contains(conv.ConversationID, `"`) {
			t.Fatalf("quoted cid leaked: %+v", conv)
		}
		ids[conv.ConversationID] = conv.Rel
	}
	if ids["cid74QGZieWQ4ondi1b0m2DtQ=="] == "" || ids["cidviyliGA6bfBKZARuuy0RzA=="] != RelOutreach {
		t.Fatalf("conversations=%+v", result.Items[0].Conversations)
	}
	if !result.Items[0].OnThisScene {
		t.Fatal("expected on_this_scene")
	}
}

func TestRecallWaitingOnForeignSceneIsNotOnThisScene(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	task := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-schedule",
		Purpose:       "冬翔委托：向 dxxh 确认下周排期",
		Status:        StatusWaiting,
		LastTouchedAt: now.Add(-20 * time.Minute),
	})
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		SrcType:       NodeTask,
		SrcID:         task.ID,
		DstType:       NodeScene,
		DstID:         "cid-g2",
		Rel:           RelTaskScene,
		LastTouchedAt: now.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		SrcType:       NodeTask,
		SrcID:         task.ID,
		DstType:       NodeScene,
		DstID:         "cid-r9b",
		Rel:           RelWaitingOn,
		LastTouchedAt: now.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	foreign, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-r9b",
		Since:          now.Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign.Items) != 1 {
		t.Fatalf("r9b items=%+v", foreign.Items)
	}
	if foreign.Items[0].OnThisScene {
		t.Fatalf("waiting_on R9B must not mark G2 排期 as R9B's matter: %+v", foreign.Items[0])
	}
	if foreign.Items[0].WhyListed != "其他会话" {
		t.Fatalf("why_listed=%q", foreign.Items[0].WhyListed)
	}

	home, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-g2",
		Since:          now.Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(home.Items) != 1 || !home.Items[0].OnThisScene {
		t.Fatalf("g2 should still own the 排期: %+v", home.Items)
	}
}

func TestRecallWindowKeywordIsNotOnThisScene(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	task := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-wash",
		Purpose:       "向辰驷确认明天什么时候去洗脚",
		Status:        StatusWaiting,
		LastTouchedAt: now.Add(-time.Hour),
	})
	if _, err := store.InsertEdge(ctx, Edge{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		SrcType:       NodeTask,
		SrcID:         task.ID,
		DstType:       NodeScene,
		DstID:         "cid-chenshi",
		Rel:           RelOutreach,
		LastTouchedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Q:           "辰驷",
		Since:       now.Add(-48 * time.Hour),
		Until:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items=%+v", result.Items)
	}
	item := result.Items[0]
	if item.MatchedVia != "window" || item.OnThisScene {
		t.Fatalf("matched_via=%q on_this_scene=%v", item.MatchedVia, item.OnThisScene)
	}
	if item.WhyListed != "关键词命中，不是本会话" {
		t.Fatalf("why_listed=%q", item.WhyListed)
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"score"`) {
		t.Fatalf("score must not leak into recall JSON: %s", raw)
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

func TestInsertEventKeepsBodyAndRecallExposesText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	if _, err := BindOutbound(ctx, store, BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		IssueID:        "issue-1",
		IssueTitle:     "向须莫v6确认今晚几点打球",
		Purpose:        "向须莫v6确认今晚几点打球",
		ConversationID: "cid-a",
		EvidenceID:     "msg-out",
		Kind:           "dm",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEvent(ctx, Event{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Source:      "inbound_im",
		Direction:   DirInbound,
		EvidenceID:  "msg-in",
		Body:        "7点",
		SceneKey:    "cid-a",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-a",
		Since:          time.Now().UTC().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, event := range result.Events {
		got[event.EvidenceID] = event.Text
	}
	if got["msg-in"] != "7点" {
		t.Fatalf("inbound text=%q events=%+v", got["msg-in"], result.Events)
	}
	if got["msg-out"] != "向须莫v6确认今晚几点打球" {
		t.Fatalf("outbound text=%q events=%+v", got["msg-out"], result.Events)
	}
}

func TestRecallInboundOnPreviousOutboundUnionsRelsAndEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	if _, err := BindOutbound(ctx, store, BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		IssueID:        "issue-1",
		IssueTitle:     "预约A与B本周五下午30分钟",
		ConversationID: "cid-a",
		EvidenceID:     "msg-out-a",
		Kind:           "dm",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEvent(ctx, Event{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Source:      "inbound_im",
		Direction:   DirInbound,
		EvidenceID:  "msg-in-a",
		SceneKey:    "cid-a",
	}); err != nil {
		t.Fatal(err)
	}
	if err := AssociateIssueConversation(ctx, store, AssociateInput{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		IssueID:        "issue-1",
		IssueTitle:     "预约A与B本周五下午30分钟",
		ConversationID: "cid-a",
		EvidenceID:     "msg-in-a",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-a",
		Since:          time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items=%d want 1: %+v", len(result.Items), result.Items)
	}
	conv := result.Items[0].Conversations
	if len(conv) != 1 || conv[0].ConversationID != "cid-a" {
		t.Fatalf("conversations=%+v", conv)
	}
	if conv[0].Rel != RelOutreach {
		t.Fatalf("primary rel=%q", conv[0].Rel)
	}
	for _, want := range []string{RelOutreach, RelTaskScene, RelSpawnedFrom} {
		if !containsRel(conv[0].Rels, want) {
			t.Fatalf("rels=%v missing %s", conv[0].Rels, want)
		}
	}
	if result.Items[0].Origin == nil || result.Items[0].Origin.Rel != RelSpawnedFrom {
		t.Fatalf("origin=%+v", result.Items[0].Origin)
	}
	if len(result.Events) != 2 {
		t.Fatalf("events=%+v want outbound+inbound", result.Events)
	}
	seen := map[string]string{}
	for _, event := range result.Events {
		seen[event.EvidenceID] = event.Direction
	}
	if seen["msg-out-a"] != DirOutbound || seen["msg-in-a"] != DirInbound {
		t.Fatalf("event directions=%v", seen)
	}
}

func TestRecallInboundOnPreviousOutboundKeepsSeparateIssues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	if _, err := BindOutbound(ctx, store, BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		IssueID:        "issue-out",
		IssueTitle:     "向冬翔确认今晚高铁还是开车",
		ConversationID: "cid-a",
		EvidenceID:     "msg-out-a",
	}); err != nil {
		t.Fatal(err)
	}
	if err := AssociateIssueConversation(ctx, store, AssociateInput{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		IssueID:        "issue-in",
		IssueTitle:     "冬翔回复后跟进订票",
		ConversationID: "cid-a",
		EvidenceID:     "msg-in-a",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEvent(ctx, Event{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Source:      "inbound_im",
		Direction:   DirInbound,
		EvidenceID:  "msg-in-a",
		SceneKey:    "cid-a",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := Recall(ctx, store, Query{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		ConversationID: "cid-a",
		Since:          time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	issues := map[string]Item{}
	for _, item := range result.Items {
		issues[item.Issue] = item
	}
	if len(issues) != 2 {
		t.Fatalf("issues=%v want outbound+inbound matters", issues)
	}
	if !containsRel(issues["issue-out"].Conversations[0].Rels, RelOutreach) {
		t.Fatalf("outbound rels=%v", issues["issue-out"].Conversations[0].Rels)
	}
	if !containsRel(issues["issue-in"].Conversations[0].Rels, RelSpawnedFrom) &&
		!containsRel(issues["issue-in"].Conversations[0].Rels, RelTaskScene) {
		t.Fatalf("inbound rels=%v", issues["issue-in"].Conversations[0].Rels)
	}
}

func TestRecallDedupesEventEvidenceAndEventLinkedTasks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	task := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-1",
		Purpose:       "向冬翔确认今晚高铁还是开车",
		LastTouchedAt: now,
	})
	first, err := store.InsertEvent(ctx, Event{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Source:      "outbound_im",
		Direction:   DirOutbound,
		EvidenceID:  "msg-1",
		SceneKey:    "cid-a",
		TaskID:      task.ID,
		OccurredAt:  now,
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
		OccurredAt:  now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected evidence dedup, got %s and %s", first.ID, second.ID)
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
	if len(result.Items) != 1 || result.Items[0].TaskID != task.ID {
		t.Fatalf("event-linked task missing: %+v", result.Items)
	}
	if len(result.Events) != 1 || result.Events[0].EvidenceID != "msg-1" {
		t.Fatalf("events=%+v", result.Events)
	}
}

func TestRecallEventOnlyMarksMatchedViaEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	task := mustInsertTask(t, store, Task{
		WorkspaceID:   "ws",
		AgentID:       "ag",
		IssueID:       "issue-event",
		Purpose:       "向须莫v6确认今天晚饭吃什么",
		Status:        StatusWaiting,
		LastTouchedAt: now.Add(-2 * time.Hour),
	})
	if _, err := store.InsertEvent(ctx, Event{
		WorkspaceID: "ws",
		AgentID:     "ag",
		Source:      "inbound_im",
		Direction:   DirInbound,
		EvidenceID:  "msg-in-event",
		Body:        "晚饭想吃什么",
		OccurredAt:  now.Add(-2 * time.Hour),
		SceneKey:    "cid-a",
		TaskID:      task.ID,
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
		t.Fatalf("items=%+v", result.Items)
	}
	if result.Items[0].MatchedVia != "event" {
		t.Fatalf("matched_via=%q", result.Items[0].MatchedVia)
	}
	if result.Items[0].LastTouchedAge == "" || result.Items[0].AgeSeconds <= 0 {
		t.Fatalf("age missing: %+v", result.Items[0])
	}
	if len(result.Events) != 1 || result.Events[0].Age == "" {
		t.Fatalf("events=%+v", result.Events)
	}
}

func containsRel(rels []string, want string) bool {
	for _, rel := range rels {
		if rel == want {
			return true
		}
	}
	return false
}

func mustInsertTask(t *testing.T, store *Memory, task Task) Task {
	t.Helper()
	out, err := store.InsertTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
