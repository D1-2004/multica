package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type coordinationExecutionStub struct {
	coordinationIssueStub
	task     db.GetLatestCoordinatorIssueExecutionRow
	taskErr  error
	taskArgs []db.GetLatestCoordinatorIssueExecutionParams
}

func (s *coordinationExecutionStub) GetLatestCoordinatorIssueExecution(_ context.Context, p db.GetLatestCoordinatorIssueExecutionParams) (db.GetLatestCoordinatorIssueExecutionRow, error) {
	s.taskArgs = append(s.taskArgs, p)
	return s.task, s.taskErr
}

func executionFixture(t *testing.T) (Turn, string, *coordinationExecutionStub) {
	t.Helper()
	agent := testAgentID()
	id, issue := testOwnedIssue(t, agent)
	issue.Status = "in_review"
	taskID, err := util.ParseUUID("09dfa579-1111-4222-8333-444444444444")
	if err != nil {
		t.Fatal(err)
	}
	stamp := func(hour, minute int) pgtype.Timestamptz {
		return pgtype.Timestamptz{Time: time.Date(2026, 9, 8, hour, minute, 0, 0, time.UTC), Valid: true}
	}
	stub := &coordinationExecutionStub{coordinationIssueStub: coordinationIssueStub{issue: issue}, task: db.GetLatestCoordinatorIssueExecutionRow{ID: taskID, Status: "completed", CreatedAt: stamp(13, 17), StartedAt: stamp(13, 18), CompletedAt: stamp(13, 19)}}
	return Turn{WorkspaceID: util.UUIDToString(issue.WorkspaceID), AgentID: agent, SceneID: testSceneID("cid-current"), ConversationID: "cid-current"}, id, stub
}

func TestWorkStateLatestExecutionKeepsIssueReviewSeparateFromCompletedRun(t *testing.T) {
	turn, id, stub := executionFixture(t)
	raw, err := (&AssocTools{Issues: stub}).Call(context.Background(), turn, toolWorkState, `{"issue_id":"`+id+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var view coordinationWorkState
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	latest := view.LatestExecution
	if view.Status != "in_review" || view.StatusSource != "issue_database" || view.TaskStatus != "not_loaded" || latest.ReadStatus != "loaded" || latest.Status != "completed" || latest.StatusSource != "agent_task_database" || latest.CompletedAt != "2026-09-08T13:19:00Z" || latest.DeliveryStatus != "not_loaded" {
		t.Fatalf("state meanings collapsed: %s", raw)
	}
	if latest.TaskID != util.UUIDToString(stub.task.ID) || latest.CreatedAt == "" || latest.StartedAt == "" || latest.Scope != coordinationExecutionScope {
		t.Fatalf("execution metadata missing: %s", raw)
	}
	if len(stub.taskArgs) != 1 || stub.taskArgs[0].WorkspaceID != stub.issue.WorkspaceID || stub.taskArgs[0].IssueID != stub.issue.ID || stub.taskArgs[0].AgentID != turn.AgentID || stub.commentReads != 0 {
		t.Fatalf("query scope/shape changed: %#v", stub.taskArgs)
	}
	if utf8.RuneCountInString(raw) > coordinationWorkStateBudget {
		t.Fatal("execution metadata exceeded work-state budget")
	}
}

func TestWorkStateLatestExecutionUnknownAndAbsentAreExplicit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader bool
		err    error
		want   string
	}{{"missing_reader", false, nil, "not_loaded"}, {"read_failure", true, errors.New("SECRET_FAILURE_BODY"), "unavailable"}, {"no_row", true, pgx.ErrNoRows, "not_found"}} {
		t.Run(tc.name, func(t *testing.T) {
			turn, id, stub := executionFixture(t)
			stub.taskErr = tc.err
			var access IssueAccess = stub
			if !tc.reader {
				access = &stub.coordinationIssueStub
			}
			raw, err := (&AssocTools{Issues: access}).Call(context.Background(), turn, toolWorkState, `{"issue_id":"`+id+`"}`)
			if err != nil {
				t.Fatal(err)
			}
			var view coordinationWorkState
			_ = json.Unmarshal([]byte(raw), &view)
			if view.LatestExecution.ReadStatus != tc.want || view.LatestExecution.TaskID != "" || view.LatestExecution.Status != "" || strings.Contains(raw, "SECRET_FAILURE_BODY") || strings.Contains(raw, `"read_status":"empty"`) {
				t.Fatalf("missing execution misrepresented: %s", raw)
			}
		})
	}
}

func TestWorkStateLatestExecutionNeverInfersExecutionOrDeliveryFromIssueStatus(t *testing.T) {
	for _, tc := range []struct{ issue, task string }{{"done", "queued"}, {"blocked", "failed"}, {"in_review", "waiting_local_directory"}} {
		t.Run(tc.issue+"_"+tc.task, func(t *testing.T) {
			turn, id, stub := executionFixture(t)
			stub.issue.Status, stub.task.Status = tc.issue, tc.task
			// A newly queued retry or incomplete legacy row must not inherit a
			// previous run's start/completion time from the Issue's status.
			stub.task.StartedAt, stub.task.CompletedAt = pgtype.Timestamptz{}, pgtype.Timestamptz{}
			raw, err := (&AssocTools{Issues: stub}).Call(context.Background(), turn, toolWorkState, `{"issue_id":"`+id+`"}`)
			if err != nil {
				t.Fatal(err)
			}
			var view coordinationWorkState
			if err := json.Unmarshal([]byte(raw), &view); err != nil {
				t.Fatal(err)
			}
			latest := view.LatestExecution
			if view.Status != tc.issue || latest.Status != tc.task || latest.ReadStatus != "loaded" || latest.StartedAt != "" || latest.CompletedAt != "" || latest.DeliveryStatus != "not_loaded" {
				t.Fatalf("independent lifecycle facts were inferred or collapsed: %s", raw)
			}
		})
	}
}

func TestWorkStateLatestExecutionCancelledReadPreservesOnlyAvailableIssueEvidence(t *testing.T) {
	for _, readErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(readErr.Error(), func(t *testing.T) {
			turn, id, stub := executionFixture(t)
			stub.taskErr = readErr // A driver may return a partial row with its error.
			raw, err := (&AssocTools{Issues: stub}).Call(context.Background(), turn, toolWorkState, `{"issue_id":"`+id+`"}`)
			if err != nil {
				t.Fatal(err)
			}
			var view coordinationWorkState
			if err := json.Unmarshal([]byte(raw), &view); err != nil {
				t.Fatal(err)
			}
			if view.Status != "in_review" || view.StatusSource != "issue_database" || view.LatestExecution.ReadStatus != "unavailable" || view.LatestExecution.TaskID != "" || view.LatestExecution.Status != "" || view.LatestExecution.CompletedAt != "" || strings.Contains(raw, readErr.Error()) {
				t.Fatalf("cancelled task read became execution evidence: %s", raw)
			}
		})
	}
}

func TestWorkStateLatestExecutionRejectsMalformedDatabaseMetadata(t *testing.T) {
	for _, kind := range []string{"missing_task_id", "unsupported_status"} {
		t.Run(kind, func(t *testing.T) {
			turn, id, stub := executionFixture(t)
			if kind == "missing_task_id" {
				stub.task.ID = pgtype.UUID{}
			} else {
				stub.task.Status = "externally_delivered"
			}
			raw, err := (&AssocTools{Issues: stub}).Call(context.Background(), turn, toolWorkState, `{"issue_id":"`+id+`"}`)
			if err == nil || raw != "" {
				t.Fatalf("database adapter bypassed metadata validation: raw=%q err=%v", raw, err)
			}
		})
	}
}

func TestWorkStateExecutionReadOnlyAfterIssueOwnershipAndWorkspaceGate(t *testing.T) {
	for _, kind := range []string{"other_agent", "other_workspace", "invalid_workspace", "missing_agent"} {
		t.Run(kind, func(t *testing.T) {
			turn, id, stub := executionFixture(t)
			switch kind {
			case "other_agent":
				stub.issue.AssigneeID = pgtype.UUID{Bytes: [16]byte{91}, Valid: true}
			case "other_workspace":
				stub.issue.WorkspaceID = pgtype.UUID{Bytes: [16]byte{92}, Valid: true}
			case "invalid_workspace":
				turn.WorkspaceID = "invalid"
			case "missing_agent":
				turn.AgentID = pgtype.UUID{}
			}
			if _, err := (&AssocTools{Issues: stub}).Call(context.Background(), turn, toolWorkState, `{"issue_id":"`+id+`"}`); err == nil {
				t.Fatal("unauthorized Issue read accepted")
			}
			if len(stub.taskArgs) != 0 {
				t.Fatal("execution query ran before authorization gate")
			}
		})
	}
}

func TestAssocRecallDoesNotQueryLatestExecution(t *testing.T) {
	turn, id, stub := executionFixture(t)
	svc := assoc.NewService(assoc.NewMemory())
	if _, err := svc.BindOutbound(context.Background(), assoc.BindOutboundInput{WorkspaceID: turn.WorkspaceID, AgentID: util.UUIDToString(turn.AgentID), IssueID: id, IssueTitle: stub.issue.Title, Purpose: stub.issue.Title, Scene: testSceneNode(turn.ConversationID), EvidenceID: "outbound"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&AssocTools{Scenes: testScenes{}, Service: svc, Issues: stub}).Call(context.Background(), turn, toolAssocRecall, `{}`); err != nil {
		t.Fatal(err)
	}
	if len(stub.taskArgs) != 0 || stub.commentReads != 0 {
		t.Fatal("prefetch/recall added an execution or comment query")
	}
}

func TestNormalizeWorkStateExecutionValidatesProvenanceAndDropsBusinessPayload(t *testing.T) {
	base := map[string]any{"issue_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "status": "in_review", "status_source": "issue_database", "scope": coordinationIssueScope, "title": "旧日志整理任务", "original_goal": strings.Repeat("业务背景", 1000), "task_status": "completed"}
	good := func() map[string]any {
		return map[string]any{"read_status": "loaded", "status_source": coordinationExecutionSource, "scope": coordinationExecutionScope, "task_id": "09dfa579-1111-4222-8333-444444444444", "status": "completed", "created_at": "2026-09-08T21:17:00+08:00", "started_at": "2026-09-08T21:18:00+08:00", "completed_at": "2026-09-08T21:19:00+08:00", "delivery_status": "delivered", "failure_reason": "SECRET_FAILURE_BODY", "context": "SECRET_CONTEXT", "result": "OLD_REPORT_BODY"}
	}
	base["latest_execution"] = good()
	input, _ := json.Marshal(base)
	raw, err := NormalizeCoordinationRead(toolWorkState, string(input))
	if err != nil {
		t.Fatal(err)
	}
	var view coordinationWorkState
	_ = json.Unmarshal([]byte(raw), &view)
	if view.LatestExecution.Status != "completed" || view.LatestExecution.CompletedAt != "2026-09-08T13:19:00Z" || view.LatestExecution.DeliveryStatus != "not_loaded" || view.TaskStatus != "not_loaded" || !view.Truncated || utf8.RuneCountInString(raw) > coordinationWorkStateBudget {
		t.Fatalf("normalization lost bounded execution meaning: %s", raw)
	}
	for _, sentinel := range []string{"SECRET_FAILURE_BODY", "SECRET_CONTEXT", "OLD_REPORT_BODY"} {
		if strings.Contains(raw, sentinel) {
			t.Fatal("business payload leaked into execution metadata")
		}
	}
	for _, bad := range []struct {
		key   string
		value any
	}{{"status", "done"}, {"status", "COMPLETED"}, {"status", 7}, {"task_id", "not-a-uuid"}, {"task_id", ""}, {"status_source", "business_output"}, {"scope", "other_agent"}, {"read_status", "empty"}, {"completed_at", "not-a-time"}, {"read_status", "not_loaded"}} {
		latest := good()
		latest[bad.key] = bad.value
		base["latest_execution"] = latest
		input, _ := json.Marshal(base)
		if _, err := NormalizeCoordinationRead(toolWorkState, string(input)); err == nil {
			t.Fatalf("invalid latest execution accepted: %s=%v", bad.key, bad.value)
		}
	}
	delete(base, "latest_execution")
	base["result"] = "completed"
	base["latest_task_status"] = "completed"
	input, _ = json.Marshal(base)
	raw, err = NormalizeCoordinationRead(toolWorkState, string(input))
	if err != nil {
		t.Fatal(err)
	}
	view = coordinationWorkState{}
	_ = json.Unmarshal([]byte(raw), &view)
	if view.LatestExecution.ReadStatus != "not_loaded" || view.LatestExecution.Status != "" {
		t.Fatal("business/legacy output was promoted into database execution evidence")
	}
}

var _ coordinatorIssueExecutionReader = (*db.Queries)(nil)
