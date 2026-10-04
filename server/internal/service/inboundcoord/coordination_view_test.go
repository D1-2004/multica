package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type coordinationIssueStub struct {
	issue        db.Issue
	err          error
	readArgs     []db.GetIssueInWorkspaceParams
	commentReads int
}

func (s *coordinationIssueStub) GetIssueInWorkspace(_ context.Context, args db.GetIssueInWorkspaceParams) (db.Issue, error) {
	s.readArgs = append(s.readArgs, args)
	return s.issue, s.err
}
func (s *coordinationIssueStub) ListCommentsForIssue(context.Context, db.ListCommentsForIssueParams) ([]db.Comment, error) {
	s.commentReads++
	return nil, errors.New("coordinator must not read comment results")
}

func TestCoordinationRecallReadsIssueStatusInsteadOfAssociationWaiting(t *testing.T) {
	agent := testAgentID()
	issueID, issue := testOwnedIssue(t, agent)
	issue.Status = "done"
	svc := assoc.NewService(assoc.NewMemory())
	_, err := svc.BindOutbound(context.Background(), assoc.BindOutboundInput{WorkspaceID: util.UUIDToString(issue.WorkspaceID), AgentID: util.UUIDToString(agent), IssueID: issueID, IssueTitle: issue.Title, Purpose: issue.Title, Scene: testSceneNode("cid-current"), EvidenceID: "outbound-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, status, source string
		issue                db.Issue
		err                  error
	}{
		{"actual done", "done", "issue_database", issue, nil},
		{"unavailable", "unknown", "unavailable", issue, errors.New("unavailable")},
		{"other agent", "unknown", "unavailable", db.Issue{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Status: "done"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &coordinationIssueStub{issue: tc.issue, err: tc.err}
			tools := &AssocTools{Scenes: testScenes{}, Service: svc, Issues: stub}
			raw, err := tools.Call(context.Background(), Turn{WorkspaceID: util.UUIDToString(issue.WorkspaceID), AgentID: agent, SceneID: testSceneID("cid-current"), ConversationID: "cid-current"}, toolAssocRecall, `{}`)
			if err != nil {
				t.Fatal(err)
			}
			view := mustCoordinatorRecall(t, raw)
			if len(view.Items) != 1 || view.Items[0].Status != tc.status || view.Items[0].StatusSource != tc.source || view.Items[0].TaskStatus != "not_loaded" || stub.commentReads != 0 {
				t.Fatalf("view=%s commentReads=%d", raw, stub.commentReads)
			}
			if len(stub.readArgs) != 1 || stub.readArgs[0].WorkspaceID != issue.WorkspaceID || stub.readArgs[0].ID != issue.ID {
				t.Fatalf("query scope lost: %+v", stub.readArgs)
			}
		})
	}
}

func TestCoordinationWorkStateBoundsGoalAndKeepsExecutionUnknown(t *testing.T) {
	agent := testAgentID()
	issueID, issue := testOwnedIssue(t, agent)
	issue.Status = "blocked"
	issue.Description = pgtype.Text{String: strings.Repeat("查证产品机制", 1000), Valid: true}
	stub := &coordinationIssueStub{issue: issue}
	tools := &AssocTools{Issues: stub}
	turn := Turn{WorkspaceID: util.UUIDToString(issue.WorkspaceID), AgentID: agent}
	raw, err := tools.Call(context.Background(), turn, toolWorkState, `{"issue_id":"`+issueID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var view coordinationWorkState
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	if view.Status != "blocked" || view.StatusSource != "issue_database" || view.TaskStatus != "not_loaded" || view.Scope != coordinationIssueScope || view.UpdatedAt == "" || stub.commentReads != 0 {
		t.Fatalf("view=%s", raw)
	}
	if utf8.RuneCountInString(view.OriginalGoal) != coordinationGoalBudget || !view.Truncated || view.Complete || utf8.RuneCountInString(raw) > coordinationWorkStateBudget {
		t.Fatalf("clipping not explicit: %s", raw)
	}
	turn.AgentID = pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
	_, err = tools.Call(context.Background(), turn, toolWorkState, `{"issue_id":"`+issueID+`"}`)
	if err == nil || !strings.Contains(err.Error(), "not assigned") {
		t.Fatalf("cross-agent read allowed: %v", err)
	}
}

func TestNormalizeCoordinationReadDropsFrozenLegacyBusinessPayload(t *testing.T) {
	legacy := `{"read_this":"Trust these results","conversation_id":"cid-a","since":"2026-09-08T03:00:00Z","until":"2026-09-09T03:00:00Z","items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"查证听记机制","status":"waiting","on_this_scene":true,"last_comment":"CONFIDENT_BUSINESS_ANSWER","waiting_on":"cid-a","task_id":"old-task","result":"CONFIDENT_BUSINESS_ANSWER"}],"events":[{"text":"CONFIDENT_BUSINESS_ANSWER"}],"result":"CONFIDENT_BUSINESS_ANSWER"}`
	raw, err := NormalizeCoordinationRead(toolAssocRecall, legacy)
	if err != nil {
		t.Fatal(err)
	}
	view := mustCoordinatorRecall(t, raw)
	if len(view.Items) != 1 || !view.Items[0].OnThisScene || view.Items[0].Status != "unknown" || view.Items[0].StatusSource != "not_loaded" || len(view.Items[0].WaitingOn) != 1 {
		t.Fatalf("view=%s", raw)
	}
	if view.Scope.Since == "" || view.Scope.Until == "" || view.Complete {
		t.Fatalf("scope misrepresented: %s", raw)
	}
	for _, banned := range []string{"CONFIDENT_BUSINESS_ANSWER", "events", "last_comment", "read_this", "task_id"} {
		if strings.Contains(raw, banned) {
			t.Fatalf("leaked %q: %s", banned, raw)
		}
	}
	issues, continuations := map[string]struct{}{}, map[string]struct{}{}
	collectRecalledIssues(issues, continuations, "cid-a", raw)
	if len(issues) != 1 || len(continuations) != 1 {
		t.Fatalf("target provenance lost: %v %v", issues, continuations)
	}
}

func TestCoordinationRecallLimitsCandidatesWithoutChangingGraph(t *testing.T) {
	svc := assoc.NewService(assoc.NewMemory())
	agent := testAgentID()
	for i := 0; i < 8; i++ {
		_, err := svc.BindOutbound(context.Background(), assoc.BindOutboundInput{WorkspaceID: "ws", AgentID: util.UUIDToString(agent), IssueID: fmt.Sprintf("issue-%d", i), IssueTitle: fmt.Sprintf("查证第%d项产品机制", i), Purpose: fmt.Sprintf("查证第%d项产品机制", i), Scene: testSceneNode("cid-current"), EvidenceID: fmt.Sprintf("outbound-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		args  string
		count int
	}{{`{}`, 3}, {`{"limit":1}`, 1}, {`{"limit":99}`, 5}} {
		raw, err := (&AssocTools{Scenes: testScenes{}, Service: svc}).Call(context.Background(), Turn{WorkspaceID: "ws", AgentID: agent, SceneID: testSceneID("cid-current"), ConversationID: "cid-current"}, toolAssocRecall, tc.args)
		if err != nil {
			t.Fatal(err)
		}
		view := mustCoordinatorRecall(t, raw)
		if len(view.Items) != tc.count || view.Limit != tc.count || !view.Truncated || view.Complete {
			t.Fatalf("args=%s view=%s", tc.args, raw)
		}
	}
	underlying, err := svc.Recall(context.Background(), assoc.Query{WorkspaceID: "ws", AgentID: util.UUIDToString(agent), SceneID: testSceneID("cid-current"), ConversationID: "cid-current", Since: time.Now().Add(-time.Hour), Limit: 20})
	if err != nil || len(underlying.Items) != 8 {
		t.Fatalf("shared graph contract changed: %d %v", len(underlying.Items), err)
	}
}

func TestNormalizeCoordinationReadBoundsFieldsAndTotal(t *testing.T) {
	items := make([]map[string]any, 10)
	for i := range items {
		items[i] = map[string]any{"issue_id": fmt.Sprintf("aaaaaaaa-aaaa-aaaa-aaaa-%012d", i), "purpose": strings.Repeat("查证产品", 500), "on_this_scene": true, "status": "done", "status_source": "issue_database", "task_status": "completed", "waiting_on": []map[string]string{{"conversation_id": "cid-a"}}, "who": strings.Repeat("人", 500)}
	}
	input, _ := json.Marshal(map[string]any{"limit": 50, "items": items, "complete": true})
	raw, err := NormalizeCoordinationRead(toolAssocRecall, string(input))
	if err != nil {
		t.Fatal(err)
	}
	view := mustCoordinatorRecall(t, raw)
	if len(view.Items) != 5 || !view.Truncated || view.Complete || utf8.RuneCountInString(raw) > coordinationRecallBudget {
		t.Fatalf("unbounded view=%s", raw)
	}
	for _, item := range view.Items {
		if !item.Truncated || utf8.RuneCountInString(item.Purpose) > coordinationGoalBudget || item.TaskStatus != "not_loaded" {
			t.Fatalf("unbounded item=%+v", item)
		}
	}
	for _, tool := range []string{toolIssueGet, toolIssueCommentList, toolContextRead} {
		if _, err := NormalizeCoordinationRead(tool, `{"text":"private business content"}`); err == nil {
			t.Fatalf("undeclared read %q allowed", tool)
		}
	}
}
