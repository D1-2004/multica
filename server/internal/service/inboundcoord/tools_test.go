package inboundcoord

import (
	"context"
	"encoding/json"
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
	var result assoc.Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Issue != "issue-eat" {
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
	var result assoc.Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Issue != "issue-eat" {
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
	var result assoc.Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Issue != "issue-train" {
		t.Fatalf("items=%+v", result.Items)
	}
}

func TestAssocToolsRecallQOmitsInboundConversation(t *testing.T) {
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
	}, toolAssocRecall, `{"q":"高铁"}`)
	if err != nil {
		t.Fatal(err)
	}
	var result assoc.Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Issue != "issue-train" {
		t.Fatalf("q without cid should list across agent matters, items=%+v", result.Items)
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
	}, toolAssocBind, `{"issue_id":"issue-eat","purpose":"向冬翔确认今天吃什么","intent":"ask"}`)
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
}

func TestAssocToolsBindRequiresConversation(t *testing.T) {
	t.Parallel()
	tools := &AssocTools{Service: assoc.NewService(assoc.NewMemory())}
	_, err := tools.Call(context.Background(), Turn{WorkspaceID: "ws", AgentID: testAgentID()}, toolAssocBind, `{"issue_id":"issue-eat","purpose":"向冬翔确认今天吃什么","intent":"ask"}`)
	if err == nil || !strings.Contains(err.Error(), "conversation_id") {
		t.Fatalf("err=%v", err)
	}
}

func TestAssocToolsBindPendingNewMatter(t *testing.T) {
	t.Parallel()
	tools := &AssocTools{Service: assoc.NewService(assoc.NewMemory())}
	raw, err := tools.Call(context.Background(), Turn{
		WorkspaceID:    "ws",
		AgentID:        testAgentID(),
		ConversationID: "cid-dongxiang",
		SenderName:     "冬翔",
	}, toolAssocBind, `{"purpose":"向冬翔确认今天吃什么","intent":"ask"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"pending":true`) || !strings.Contains(raw, `"intent":"ask"`) {
		t.Fatalf("bind=%s", raw)
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
	var result assoc.Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, event := range result.Events {
		got[event.EvidenceID] = event.Text
	}
	if got["msg-in-7"] != "7点" {
		t.Fatalf("inbound text=%q events=%+v", got["msg-in-7"], result.Events)
	}
	if got["msg-out-1"] != "今晚几点打球" && got["msg-out-1"] != "向须莫v6确认今晚几点打球" {
		t.Fatalf("outbound text=%q events=%+v", got["msg-out-1"], result.Events)
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
