package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAssocToolsRecallDefaultsConversationID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := assoc.NewMemory()
	svc := assoc.NewService(store)
	agent := testAgentID()
	agentID := util.UUIDToString(agent)
	if _, err := svc.BindOutbound(ctx, assoc.BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        agentID,
		IssueID:        "issue-eat",
		IssueTitle:     "向冬翔确认今天吃什么",
		Purpose:        "向冬翔确认今天吃什么",
		ConversationID: "cid-dongxiang",
		EvidenceID:     "msg-out-1",
		Kind:           "dm",
	}); err != nil {
		t.Fatal(err)
	}
	tools := &AssocTools{Service: svc}
	raw, err := tools.Call(ctx, Turn{
		WorkspaceID:    "ws",
		AgentID:        agent,
		ConversationID: "cid-dongxiang",
	}, toolAssocRecall, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	result := mustCoordinatorRecall(t, raw)
	if len(result.Items) != 1 || result.Items[0].IssueID != "issue-eat" {
		t.Fatalf("items=%+v", result.Items)
	}
	if result.Items[0].Purpose != "向冬翔确认今天吃什么" {
		t.Fatalf("purpose=%q", result.Items[0].Purpose)
	}
}

func TestAssocToolsRecallDoesNotDefaultPersonID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := assoc.NewMemory()
	svc := assoc.NewService(store)
	agent := testAgentID()
	agentID := util.UUIDToString(agent)
	if _, err := svc.BindOutbound(ctx, assoc.BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        agentID,
		IssueID:        "issue-eat",
		IssueTitle:     "向冬翔确认今天吃什么",
		Purpose:        "向冬翔确认今天吃什么",
		ConversationID: "cid-dongxiang",
		EvidenceID:     "msg-out-1",
		Kind:           "dm",
	}); err != nil {
		t.Fatal(err)
	}
	tools := &AssocTools{Service: svc}
	raw, err := tools.Call(ctx, Turn{
		WorkspaceID:    "ws",
		AgentID:        agent,
		ConversationID: "cid-dongxiang",
		PersonID:       "25698887",
	}, toolAssocRecall, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	result := mustCoordinatorRecall(t, raw)
	if len(result.Items) != 1 || result.Items[0].IssueID != "issue-eat" {
		t.Fatalf("default person must not hide cid hit: %+v", result.Items)
	}
}

func TestAssocToolsRecallUsesExplicitConversationID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := assoc.NewMemory()
	svc := assoc.NewService(store)
	agent := testAgentID()
	agentID := util.UUIDToString(agent)
	if _, err := svc.BindOutbound(ctx, assoc.BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        agentID,
		IssueID:        "issue-train",
		IssueTitle:     "向冬翔确认明天去上海是坐高铁还是开车",
		Purpose:        "向冬翔确认明天去上海是坐高铁还是开车",
		ConversationID: "cid+bEFv7ngm9n79Q1vL9HYJw==",
		EvidenceID:     "msg-out-9",
		Kind:           "dm",
	}); err != nil {
		t.Fatal(err)
	}
	tools := &AssocTools{Service: svc}
	raw, err := tools.Call(ctx, Turn{
		WorkspaceID:    "ws",
		AgentID:        agent,
		ConversationID: "cid-robot",
	}, toolAssocRecall, `{"conversation_id":"cid+bEFv7ngm9n79Q1vL9HYJw=="}`)
	if err != nil {
		t.Fatal(err)
	}
	result := mustCoordinatorRecall(t, raw)
	if len(result.Items) != 1 || result.Items[0].IssueID != "issue-train" {
		t.Fatalf("items=%+v", result.Items)
	}
}

func TestAssocToolsRecallOmitsPurposeWithoutEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := assoc.NewMemory()
	svc := assoc.NewService(store)
	agent := testAgentID()
	agentID := util.UUIDToString(agent)
	now := time.Now().UTC()
	empty, err := store.InsertTask(ctx, assoc.Task{
		WorkspaceID:   "ws",
		AgentID:       agentID,
		IssueID:       "issue-empty",
		Purpose:       "某人委托：",
		Status:        assoc.StatusWaiting,
		LastTouchedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEdge(ctx, assoc.Edge{
		WorkspaceID:   "ws",
		AgentID:       agentID,
		SrcType:       assoc.NodeTask,
		SrcID:         empty.ID,
		DstType:       assoc.NodeScene,
		DstID:         "cid-a",
		Rel:           assoc.RelTaskScene,
		LastTouchedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindOutbound(ctx, assoc.BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        agentID,
		IssueID:        "issue-eat",
		IssueTitle:     "向冬翔确认今天吃什么",
		Purpose:        "向冬翔确认今天吃什么",
		ConversationID: "cid-a",
		EvidenceID:     "msg-out-1",
		Kind:           "dm",
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := (&AssocTools{Service: svc}).Call(ctx, Turn{
		WorkspaceID:    "ws",
		AgentID:        agent,
		ConversationID: "cid-a",
	}, toolAssocRecall, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	result := mustCoordinatorRecall(t, raw)
	if len(result.Items) != 1 || result.Items[0].IssueID != "issue-eat" {
		t.Fatalf("empty-event cards must be omitted, items=%+v", result.Items)
	}
}

func TestAssocToolsRecallQKeepsInboundConversation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := assoc.NewMemory()
	svc := assoc.NewService(store)
	agent := testAgentID()
	agentID := util.UUIDToString(agent)
	if _, err := svc.BindOutbound(ctx, assoc.BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        agentID,
		IssueID:        "issue-train",
		IssueTitle:     "向冬翔确认明天去上海是坐高铁还是开车",
		Purpose:        "向冬翔确认明天去上海是坐高铁还是开车",
		ConversationID: "cid+bEFv7ngm9n79Q1vL9HYJw==",
		EvidenceID:     "msg-out-9",
		Kind:           "dm",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindOutbound(ctx, assoc.BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        agentID,
		IssueID:        "issue-eat",
		IssueTitle:     "向冬翔确认今天吃什么",
		Purpose:        "向冬翔确认今天吃什么",
		ConversationID: "cid-robot",
		EvidenceID:     "msg-out-eat",
		Kind:           "dm",
	}); err != nil {
		t.Fatal(err)
	}
	tools := &AssocTools{Service: svc}
	raw, err := tools.Call(ctx, Turn{
		WorkspaceID:    "ws",
		AgentID:        agent,
		ConversationID: "cid-robot",
	}, toolAssocRecall, `{"q":"高铁"}`)
	if err != nil {
		t.Fatal(err)
	}
	result := mustCoordinatorRecall(t, raw)
	if result.ConversationID != "cid-robot" {
		t.Fatalf("conversation_id=%q", result.ConversationID)
	}
	if len(result.Items) != 0 {
		t.Fatalf("q must not drop inbound cid and hit another scene, items=%+v", result.Items)
	}
}

func TestAssocToolsRecallQWithoutInboundSceneStillSearchesWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := assoc.NewMemory()
	svc := assoc.NewService(store)
	agent := testAgentID()
	agentID := util.UUIDToString(agent)
	if _, err := svc.BindOutbound(ctx, assoc.BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        agentID,
		IssueID:        "issue-train",
		IssueTitle:     "向冬翔确认明天去上海是坐高铁还是开车",
		Purpose:        "向冬翔确认明天去上海是坐高铁还是开车",
		ConversationID: "cid+bEFv7ngm9n79Q1vL9HYJw==",
		EvidenceID:     "msg-out-9",
		Kind:           "dm",
	}); err != nil {
		t.Fatal(err)
	}
	tools := &AssocTools{Service: svc}
	raw, err := tools.Call(ctx, Turn{
		WorkspaceID: "ws",
		AgentID:     agent,
	}, toolAssocRecall, `{"q":"高铁"}`)
	if err != nil {
		t.Fatal(err)
	}
	result := mustCoordinatorRecall(t, raw)
	if len(result.Items) != 1 || result.Items[0].IssueID != "issue-train" {
		t.Fatalf("web q without inbound cid still searches the window, items=%+v", result.Items)
	}
}

func TestDefaultRecallConversationIDFillsInbound(t *testing.T) {
	t.Parallel()
	got := defaultRecallConversationID(`{"q":"辰驷"}`, "cid+bEFv7ngm9n79Q1vL9HYJw==")
	var args recallArgs
	if err := json.Unmarshal([]byte(got), &args); err != nil {
		t.Fatal(err)
	}
	if args.ConversationID != "cid+bEFv7ngm9n79Q1vL9HYJw==" || args.Q != "辰驷" {
		t.Fatalf("got %+v from %s", args, got)
	}
	kept := defaultRecallConversationID(`{"conversation_id":"cid-other","q":"辰驷"}`, "cid-inbound")
	if err := json.Unmarshal([]byte(kept), &args); err != nil {
		t.Fatal(err)
	}
	if args.ConversationID != "cid-other" {
		t.Fatalf("explicit cid overwritten: %+v", args)
	}
}

func TestAssocToolsBindAssociatesIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := assoc.NewMemory()
	svc := assoc.NewService(store)
	agent := testAgentID()
	agentID := util.UUIDToString(agent)
	tools := &AssocTools{Service: svc}
	raw, err := tools.Call(ctx, Turn{
		WorkspaceID:    "ws",
		AgentID:        agent,
		ConversationID: "cid-new",
		PersonID:       "123456",
		EvidenceID:     "msg-in-1",
		Kind:           "dm",
		SenderName:     "冬翔",
	}, toolAssocBind, `{"issue_id":"issue-eat","purpose":"向冬翔确认今天吃什么","intent":"ask","delegator":"冬翔"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"linked":true`) {
		t.Fatalf("bind=%s", raw)
	}
	got, err := svc.Recall(ctx, assoc.Query{
		WorkspaceID:    "ws",
		AgentID:        agentID,
		ConversationID: "cid-new",
		Since:          mustSince48h(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Issue != "issue-eat" {
		t.Fatalf("recall=%+v", got.Items)
	}
	if got.Items[0].Intent != assoc.IntentAsk {
		t.Fatalf("intent=%q", got.Items[0].Intent)
	}
	if got.Items[0].Purpose != "冬翔委托：向冬翔确认今天吃什么" {
		t.Fatalf("purpose=%q", got.Items[0].Purpose)
	}
}

func TestAssocToolsBindRequiresConversation(t *testing.T) {
	t.Parallel()
	tools := &AssocTools{Service: assoc.NewService(assoc.NewMemory())}
	_, err := tools.Call(context.Background(), Turn{WorkspaceID: "ws", AgentID: testAgentID()}, toolAssocBind, `{"issue_id":"issue-eat","purpose":"向冬翔确认今天吃什么","intent":"ask"}`)
	if err == nil || !strings.Contains(err.Error(), "conversation_id") {
		t.Fatalf("err=%v", err)
	}
}

func TestAssocToolsBindRequiresIssueID(t *testing.T) {
	t.Parallel()
	tools := &AssocTools{Service: assoc.NewService(assoc.NewMemory())}
	_, err := tools.Call(context.Background(), Turn{
		WorkspaceID:    "ws",
		AgentID:        testAgentID(),
		ConversationID: "cid-dongxiang",
		SenderName:     "冬翔",
	}, toolAssocBind, `{"purpose":"向冬翔确认今天吃什么","intent":"ask","delegator":"冬翔"}`)
	if err == nil || !strings.Contains(err.Error(), "issue_id is required") {
		t.Fatalf("err=%v", err)
	}
	var h hinter
	if !errors.As(err, &h) || !strings.Contains(h.Hint(), "finish action=issue") {
		t.Fatalf("missing bind hint: %v", err)
	}
}

func TestAssocToolsBindRejectsToolingPurpose(t *testing.T) {
	t.Parallel()
	tools := &AssocTools{Service: assoc.NewService(assoc.NewMemory())}
	_, err := tools.Call(context.Background(), Turn{
		WorkspaceID:    "ws",
		AgentID:        testAgentID(),
		ConversationID: "cid-dongxiang",
		SenderName:     "须莫🥥",
	}, toolAssocBind, `{"issue_id":"issue-meet","delegator":"须莫🥥","intent":"ask","purpose":"向须莫v6询问明早有没有会议，dws要用dws chat data-auth cross-org去找须莫v6"}`)
	if err == nil || !strings.Contains(err.Error(), "tooling") {
		t.Fatalf("err=%v", err)
	}
	var h hinter
	if !errors.As(err, &h) || !strings.Contains(h.Hint(), "dws") {
		t.Fatalf("missing purpose hint: %v", err)
	}
}

func TestMarshalCoordinatorRecallIsSlim(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	commentAt := now.Add(-32 * time.Minute)
	raw, err := marshalCoordinatorRecall(assoc.Result{
		ReadThis:       assoc.RecallReadThis,
		Since:          now.Add(-48 * time.Hour),
		Until:          now,
		ConversationID: `"cid74QGZieWQ4ondi1b0m2DtQ=="`,
		Items: []assoc.Item{{
			Issue:          "0f45c389-d67c-4ebd-b524-97d95177b1c9",
			IssueID:        "0f45c389-d67c-4ebd-b524-97d95177b1c9",
			TaskID:         "12cf095d-abae-4a60-9f17-b15c0dc3cb9e",
			Purpose:        "须莫🥥委托：向须莫v6询问晚上有没有会议",
			Intent:         "ask",
			IntentLabel:    "向某人询问一件事",
			Status:         "waiting",
			OnThisScene:    true,
			WhyListed:      "本会话事项",
			LastTouchedAt:  now.Add(-32 * time.Minute),
			LastTouchedAge: "32分钟前",
			AgeSeconds:     1924,
			MatchedVia:     "both",
			LastComment:    "已向须莫v6发送消息询问今晚是否有会议安排。\n\n发送详情：\n- 目标会话：须莫v6（openConversationId: cidviyliGA6bfBKZARuuy0RzA==）\n- 发送状态：成功（openTaskId: abc）\n等待须莫v6回复。",
			LastCommentAge: "32分钟前",
			LastCommentAt:  &commentAt,
			Conversations: []assoc.ConversationRef{
				{ConversationID: `"cidviyliGA6bfBKZARuuy0RzA=="`, Kind: "dm", Rel: "outreach", Rels: []string{"outreach", "task_scene"}},
			},
			People:    []assoc.PersonRef{{PersonID: "uid-v6", DisplayName: "须莫🥥", Name: "须莫🥥"}},
			WaitingOn: []assoc.WaitingRef{{ConversationID: `"cidviyliGA6bfBKZARuuy0RzA=="`}},
		}},
		Events: []assoc.EventRef{{
			ID: "evt-1", Direction: "inbound", Source: "inbound_im", EvidenceID: "msg-1",
			Text: "问一下晚上有没有会议", When: "刚刚", Age: "刚刚", AgeSeconds: 12,
		}},
		EventsNote: "scene IM evidence, not the matter index.",
	})
	if err != nil {
		t.Fatal(err)
	}
	var view coordinatorRecallView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	if view.ConversationID != "cid74QGZieWQ4ondi1b0m2DtQ==" {
		t.Fatalf("conversation_id=%q", view.ConversationID)
	}
	if len(view.Items) != 1 {
		t.Fatalf("items=%+v", view.Items)
	}
	item := view.Items[0]
	if item.IssueID != "0f45c389-d67c-4ebd-b524-97d95177b1c9" || item.Who != "须莫🥥" || item.WaitingOn != "cidviyliGA6bfBKZARuuy0RzA==" {
		t.Fatalf("item=%+v", item)
	}
	if item.LastComment != "已向须莫v6发送消息询问今晚是否有会议安排。 等待须莫v6回复。" && !strings.Contains(item.LastComment, "已向须莫v6发送消息询问今晚是否有会议安排") {
		t.Fatalf("last_comment=%q", item.LastComment)
	}
	if strings.Contains(item.LastComment, "openTaskId") || strings.Contains(item.LastComment, "发送详情") {
		t.Fatalf("last_comment still has DWS dump: %q", item.LastComment)
	}
	for _, banned := range []string{
		`"task_id"`, `"intent_label"`, `"matched_via"`, `"conversations"`,
		`"last_touched_at"`, `"age_seconds"`, `"last_comment_at"`, `"events_note"`,
		`"since"`, `"until"`, `"rels"`,
	} {
		if strings.Contains(raw, banned) {
			t.Fatalf("slim recall leaked %s: %s", banned, raw)
		}
	}
}

func TestSanitizeRecallCommentDropsDWS(t *testing.T) {
	t.Parallel()
	got := sanitizeRecallComment("已向须莫v6发送消息询问今晚是否有会议安排。\n发送详情：\n- 目标会话：须莫v6（openConversationId: cidx）\n- 发送状态：成功（openTaskId: abc）\n等待回复。")
	if strings.Contains(got, "openTaskId") || strings.Contains(got, "发送详情") || strings.Contains(strings.ToLower(got), "dws") {
		t.Fatalf("got=%q", got)
	}
	if !strings.Contains(got, "已向须莫v6发送消息询问今晚是否有会议安排") {
		t.Fatalf("got=%q", got)
	}
}

func TestAssocToolsUnknownName(t *testing.T) {
	t.Parallel()
	tools := &AssocTools{Service: assoc.NewService(assoc.NewMemory())}
	_, err := tools.Call(context.Background(), Turn{}, "dws_send", `{}`)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("err=%v", err)
	}
}

func mustCoordinatorRecall(t *testing.T, raw string) coordinatorRecallView {
	t.Helper()
	var view coordinatorRecallView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func mustSince48h(t *testing.T) time.Time {
	t.Helper()
	parsed, err := assoc.ParseSince("48h", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestAssocToolsRecallOverlaysEventText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := assoc.NewMemory()
	svc := assoc.NewService(store)
	agent := testAgentID()
	agentID := util.UUIDToString(agent)
	if _, err := svc.BindOutbound(ctx, assoc.BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        agentID,
		IssueID:        "issue-ball",
		IssueTitle:     "向须莫v6确认今晚几点打球",
		Purpose:        "向须莫v6确认今晚几点打球",
		ConversationID: "cid-v6",
		EvidenceID:     "msg-out-1",
		Kind:           "dm",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEvent(ctx, assoc.Event{
		WorkspaceID: "ws",
		AgentID:     agentID,
		Source:      "inbound_im",
		Direction:   assoc.DirInbound,
		EvidenceID:  "msg-in-7",
		SceneKey:    "cid-v6",
	}); err != nil {
		t.Fatal(err)
	}
	tools := &AssocTools{Service: svc}
	raw, err := tools.Call(ctx, Turn{
		WorkspaceID:    "ws",
		AgentID:        agent,
		ConversationID: "cid-v6",
		EvidenceID:     "msg-in-7",
		Message:        "7点",
		DingTalkHistory: []HistoryLine{
			{Role: "须莫", Content: "今晚几点打球", EvidenceID: "msg-out-1"},
		},
	}, toolAssocRecall, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var view coordinatorRecallView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	got := map[string]struct{}{}
	for _, event := range view.Events {
		got[event.Text] = struct{}{}
	}
	if _, ok := got["7点"]; !ok {
		t.Fatalf("inbound text missing events=%+v", view.Events)
	}
	if _, ok := got["今晚几点打球"]; !ok {
		if _, ok := got["向须莫v6确认今晚几点打球"]; !ok {
			t.Fatalf("outbound text missing events=%+v", view.Events)
		}
	}
}

type issueStub struct {
	issue    db.Issue
	comments []db.Comment
}

type issueCommentWriterStub struct {
	effect  IssueCommentEffect
	turn    Turn
	issue   db.Issue
	content string
	parent  pgtype.UUID
	err     error
}

func (s *issueCommentWriterStub) AddMemberComment(_ context.Context, turn Turn, issue db.Issue, content string, parentID pgtype.UUID) (IssueCommentEffect, error) {
	s.turn = turn
	s.issue = issue
	s.content = content
	s.parent = parentID
	return s.effect, s.err
}

func (s *issueStub) GetIssueInWorkspace(context.Context, db.GetIssueInWorkspaceParams) (db.Issue, error) {
	return s.issue, nil
}

func (s *issueStub) ListCommentsForIssue(context.Context, db.ListCommentsForIssueParams) ([]db.Comment, error) {
	return s.comments, nil
}

func testOwnedIssue(t *testing.T, agent pgtype.UUID) (string, db.Issue) {
	t.Helper()
	ws := "22222222-2222-2222-2222-222222222222"
	issueID := "33333333-3333-3333-3333-333333333333"
	wsUUID, err := util.ParseUUID(ws)
	if err != nil {
		t.Fatal(err)
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		t.Fatal(err)
	}
	return issueID, db.Issue{
		ID:           id,
		WorkspaceID:  wsUUID,
		Title:        "向须莫v6确认今晚几点打球",
		Status:       "in_progress",
		Description:  pgtype.Text{String: "确认今晚打球时间并回原发起人", Valid: true},
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   agent,
		UpdatedAt:    pgtype.Timestamptz{Time: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Valid: true},
	}
}

func TestIssueGetReturnsClippedCard(t *testing.T) {
	t.Parallel()
	agent := testAgentID()
	issueID, issue := testOwnedIssue(t, agent)
	tools := &AssocTools{Issues: &issueStub{issue: issue}}
	raw, err := tools.Call(context.Background(), Turn{
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		AgentID:     agent,
	}, toolIssueGet, `{"issue_id":"`+issueID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"title":"向须莫v6确认今晚几点打球"`) || !strings.Contains(raw, "回原发起人") {
		t.Fatalf("issue_get=%s", raw)
	}
}

func TestIssueCommentListAndAdd(t *testing.T) {
	t.Parallel()
	agent := testAgentID()
	issueID, issue := testOwnedIssue(t, agent)
	commentID, err := util.ParseUUID("44444444-4444-4444-4444-444444444444")
	if err != nil {
		t.Fatal(err)
	}
	stub := &issueStub{
		issue: issue,
		comments: []db.Comment{{
			ID:         commentID,
			AuthorType: "agent",
			Content:    "我去问须莫v6今晚几点",
			CreatedAt:  pgtype.Timestamptz{Time: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Valid: true},
		}},
	}
	writer := &issueCommentWriterStub{effect: IssueCommentEffect{
		IssueID: issueID, CommentID: "44444444-4444-4444-4444-444444444444", TaskID: "55555555-5555-5555-5555-555555555555",
	}}
	tools := &AssocTools{Issues: stub, CommentWriter: writer}
	userID, err := util.ParseUUID("66666666-6666-6666-6666-666666666666")
	if err != nil {
		t.Fatal(err)
	}
	turn := Turn{
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		AgentID:     agent,
		UserID:      userID,
	}
	listed, err := tools.Call(context.Background(), turn, toolIssueCommentList, `{"issue_id":"`+issueID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed, "我去问须莫v6今晚几点") {
		t.Fatalf("list=%s", listed)
	}
	added, err := tools.Call(context.Background(), turn, toolIssueCommentAdd, `{"issue_id":"`+issueID+`","content":"须莫v6回7点"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(added, `"task_id":"55555555-5555-5555-5555-555555555555"`) {
		t.Fatalf("add=%s", added)
	}
	if writer.content != "须莫v6回7点" || writer.turn.UserID != userID {
		t.Fatalf("writer content=%q turn=%+v", writer.content, writer.turn)
	}
}

func TestIssueGetRejectsOtherAgent(t *testing.T) {
	t.Parallel()
	_, issue := testOwnedIssue(t, testAgentID())
	tools := &AssocTools{Issues: &issueStub{issue: issue}}
	other := pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
	_, err := tools.Call(context.Background(), Turn{
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		AgentID:     other,
	}, toolIssueGet, `{"issue_id":"33333333-3333-3333-3333-333333333333"}`)
	if err == nil || !strings.Contains(err.Error(), "not assigned") {
		t.Fatalf("err=%v", err)
	}
}
