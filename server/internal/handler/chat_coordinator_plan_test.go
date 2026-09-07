package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func webCoordinatorFixture(t *testing.T) (*Handler, db.ChatSession) {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("isolated test database unavailable")
	}
	agentID := createHandlerTestAgent(t, t.Name(), []byte(`[]`))
	sessionID := createHandlerTestChatSession(t, agentID)
	session, err := testHandler.Queries.GetChatSession(context.Background(), util.MustParseUUID(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, query := range []string{
			`DELETE FROM agent_task_queue WHERE agent_id = $1`,
			`DELETE FROM comment WHERE issue_id IN (SELECT id FROM issue WHERE assignee_id = $1)`,
			`DELETE FROM issue WHERE assignee_id = $1`,
		} {
			if _, err := testPool.Exec(ctx, query, agentID); err != nil {
				t.Errorf("cleanup coordinator plan: %v", err)
			}
		}
	})
	tasks := &service.TaskService{Queries: testHandler.Queries, TxStarter: testPool, Bus: events.New()}
	return &Handler{Queries: testHandler.Queries, TaskService: tasks, Bus: events.New(), Analytics: testHandler.Analytics}, session
}

func webCoordinatorExistingIssue(t *testing.T, session db.ChatSession, title string) db.Issue {
	t.Helper()
	number, err := testHandler.Queries.IncrementIssueCounter(context.Background(), session.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := testHandler.Queries.CreateIssue(context.Background(), db.CreateIssueParams{
		WorkspaceID: session.WorkspaceID, Title: title, Status: "todo", Priority: "none", Number: number,
		AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: session.AgentID,
		CreatorType: "member", CreatorID: util.MustParseUUID(testUserID),
	})
	if err != nil {
		t.Fatal(err)
	}
	return issue
}

func assertWebCoordinatorCounts(t *testing.T, session db.ChatSession, issues, tasks, comments, messages int) {
	t.Helper()
	var gotIssues, gotTasks, gotComments, gotMessages int
	if err := testPool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM issue WHERE assignee_id = $1),
		(SELECT count(*) FROM agent_task_queue WHERE agent_id = $1),
		(SELECT count(*) FROM comment WHERE issue_id IN (SELECT id FROM issue WHERE assignee_id = $1)),
		(SELECT count(*) FROM chat_message WHERE chat_session_id = $2)`, session.AgentID, session.ID).Scan(&gotIssues, &gotTasks, &gotComments, &gotMessages); err != nil {
		t.Fatal(err)
	}
	if gotIssues != issues || gotTasks != tasks || gotComments != comments || gotMessages != messages {
		t.Fatalf("issues/tasks/comments/messages=%d/%d/%d/%d, want %d/%d/%d/%d", gotIssues, gotTasks, gotComments, gotMessages, issues, tasks, comments, messages)
	}
}

func TestWebCoordinatorPlanCommitsMixedItemsAndCompleteTranscript(t *testing.T) {
	h, session := webCoordinatorFixture(t)
	issue := webCoordinatorExistingIssue(t, session, "确认周五会议时间")
	decision := inboundcoord.Decision{
		Action: inboundcoord.ActionIssue, PlanVersion: "window-plan-v1", UserText: "三点的回复我会带回去，也会查预算和准备议程。",
		Items: []inboundcoord.WindowItem{
			{IssueID: util.UUIDToString(issue.ID), ActionKey: "item-1", Basis: "change", Content: "周五改成三点", Purpose: "确认周五会议改到下午三点", Intent: "confirm", SourceRefs: []string{"u1"}},
			{ActionKey: "item-2", Basis: "new_request", Content: "帮我查预算", Purpose: "查询本周会议活动预算", Intent: "lookup", SourceRefs: []string{"u1"}},
			{ActionKey: "item-3", Basis: "new_request", Content: "再准备议程", Purpose: "准备周五下午会议议程", Intent: "other", SourceRefs: []string{"u1"}},
		},
	}
	var broadcasts atomic.Int32
	h.Bus.SubscribeAll(func(event events.Event) {
		broadcasts.Add(1)
		var count int
		if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM chat_message WHERE chat_session_id = $1`, session.ID).Scan(&count); err != nil || count != 2 {
			t.Errorf("event preceded complete transcript: count=%d err=%v", count, err)
		}
	})
	turn, committed, err := h.persistWebCoordinatorPlan(context.Background(), session, util.MustParseUUID(testUserID), "改三点，查预算，再准备议程", decision, chattrace.New("web"))
	if err != nil {
		t.Fatal(err)
	}
	if turn == nil || len(committed.IssueResults) != 3 || len(committed.CompletedActionKeys) != 3 {
		t.Fatalf("missing committed outcomes: %#v", committed.IssueResults)
	}
	if committed.IssueResults[0].Action != "issue_commented" || committed.IssueResults[0].IssueID != util.UUIDToString(issue.ID) || committed.IssueResults[0].CommentID == "" {
		t.Fatalf("continuation became a new issue: %#v", committed.IssueResults[0])
	}
	for _, result := range committed.IssueResults {
		if result.TaskID == "" {
			t.Fatalf("result did not record its real task: %#v", result)
		}
	}
	var stored protocol.ChatCoordinatorTrace
	if err := json.Unmarshal(turn.AssistantMessage.SourcePayload, &stored); err != nil || len(stored.IssueResults) != 3 {
		t.Fatalf("persisted transcript lost outcomes: trace=%#v err=%v", stored, err)
	}
	assertWebCoordinatorCounts(t, session, 3, 3, 1, 2)
	if broadcasts.Load() == 0 {
		t.Fatal("committed events were not released")
	}
}

func TestWebCoordinatorPlanCommitsTwoContinuationsWithoutCreatingIssues(t *testing.T) {
	h, session := webCoordinatorFixture(t)
	one := webCoordinatorExistingIssue(t, session, "第一件会务")
	two := webCoordinatorExistingIssue(t, session, "第二件会务")
	decision := inboundcoord.Decision{Action: inboundcoord.ActionIssue, PlanVersion: "window-plan-v1", UserText: "两处改动我一起更新。", Items: []inboundcoord.WindowItem{
		{IssueID: util.UUIDToString(one.ID), ActionKey: "item-1", Content: "第一件改为三点", Purpose: "把第一件会务时间改到下午三点", Intent: "confirm"},
		{IssueID: util.UUIDToString(two.ID), ActionKey: "item-2", Content: "第二件地点改为上海", Purpose: "把第二件会务地点改为上海", Intent: "confirm"},
	}}
	_, committed, err := h.persistWebCoordinatorPlan(context.Background(), session, util.MustParseUUID(testUserID), "两件都改一下", decision, chattrace.New("web"))
	if err != nil || len(committed.IssueResults) != 2 {
		t.Fatalf("both continuations must commit: results=%#v err=%v", committed.IssueResults, err)
	}
	for _, result := range committed.IssueResults {
		if result.Action != "issue_commented" {
			t.Fatalf("continuation created a new issue: %#v", result)
		}
	}
	assertWebCoordinatorCounts(t, session, 2, 2, 2, 2)
}

func TestWebCoordinatorPlanRollsBackEarlierNewItemWhenContinuationBusy(t *testing.T) {
	h, session := webCoordinatorFixture(t)
	issue := webCoordinatorExistingIssue(t, session, "正在执行的工作")
	if _, err := h.TaskService.EnqueueTaskForIssue(context.Background(), issue); err != nil {
		t.Fatal(err)
	}
	var broadcasts atomic.Int32
	h.Bus.SubscribeAll(func(event events.Event) { broadcasts.Add(1) })
	decision := inboundcoord.Decision{Action: inboundcoord.ActionIssue, PlanVersion: "window-plan-v1", UserText: "两件一起办。", Items: []inboundcoord.WindowItem{
		{ActionKey: "item-1", Content: "先查预算", Purpose: "查询本周的会务活动预算", Intent: "lookup"},
		{IssueID: util.UUIDToString(issue.ID), ActionKey: "item-2", Content: "时间改为四点", Purpose: "把正在执行会务改为四点", Intent: "confirm"},
	}}
	turn, _, err := h.persistWebCoordinatorPlan(context.Background(), session, util.MustParseUUID(testUserID), "查预算、改四点", decision, chattrace.New("web"))
	if !errors.Is(err, service.ErrIssueDispatchPending) || turn != nil {
		t.Fatalf("busy continuation must reject the complete uncommitted plan: turn=%#v err=%v", turn, err)
	}
	assertWebCoordinatorCounts(t, session, 1, 1, 0, 0)
	if broadcasts.Load() != 0 {
		t.Fatalf("rollback emitted %d product events", broadcasts.Load())
	}
}

func TestSendChatMessageCoordinatorDeferredDoesNotExecute(t *testing.T) {
	_, session := webCoordinatorFixture(t)
	original := testHandler.InboundCoordinator
	testHandler.InboundCoordinator = &inboundcoord.Coordinator{}
	t.Cleanup(func() { testHandler.InboundCoordinator = original })
	req := newRequest("POST", "/api/chat-sessions/"+util.UUIDToString(session.ID)+"/messages", map[string]any{"content": "帮我处理这件事"})
	req = withURLParam(req, "sessionId", util.UUIDToString(session.ID))
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.SendChatMessage(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("deferred coordinator verdict must not enqueue a chat task: status=%d body=%s", w.Code, w.Body.String())
	}
	assertWebCoordinatorCounts(t, session, 0, 0, 0, 0)
}
