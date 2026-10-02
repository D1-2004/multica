package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeelearning"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
)

type employeeLearningFixture struct {
	response                          *dingTalkResponseFixture
	model                             *employeeTestModel
	dc                                agentDispatchContext
	scope                             employeetask.Scope
	taskID, runID, queueID, requester string
}

func newEmployeeLearningFixture(t *testing.T) employeeLearningFixture {
	t.Helper()
	return newEmployeeLearningFixtureInConversation(t, "group")
}

func newEmployeeLearningFixtureInConversation(t *testing.T, conversationType string) employeeLearningFixture {
	t.Helper()
	f, model, dc := employeeFixture(t)
	f.command.Event.Data.Conversation.Type = conversationType
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1,$2,'123','456',$3)`, f.agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1`, f.agentID)
	})
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	model.dispatch = true
	f.h.EmployeeMemory = employeememory.NewStore(testPool)
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	model.sourceRef = receipt + "/message-1"
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	out := employeeLearningFixture{response: f, model: model, dc: dc, scope: employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "456", Kind: employeetask.ScopeScene}}
	if err := testPool.QueryRow(ctx, `SELECT t.id::text,r.id::text,r.queue_task_id::text,t.scene_id::text,t.requester_ref FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE t.agent_id=$1`, f.agentID).Scan(&out.taskID, &out.runID, &out.queueID, &out.scope.Scene.SceneID, &out.requester); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='completed',result='{"output":"The reported customer trend is increasing."}' WHERE id=$1`, out.queueID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := employeetask.NewStore(testPool).RecordResult(ctx, out.scope, out.taskID, employeetask.ResultParams{Source: employeetask.Source{Namespace: "test_terminal", Key: out.queueID}, RunID: out.runID, State: employeetask.StateSucceeded, Result: "The reported customer trend is increasing.", ResultRef: "agent_task_queue:" + out.queueID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"employee_learning_consumption", "employee_learning", "employee_memory_state"} {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE agent_id=$1`, f.agentID)
		}
	})
	return out
}
func (f employeeLearningFixture) privateScope() employeememory.Scope {
	return employeememory.Scope{WorkspaceID: parseUUID(f.scope.WorkspaceID), AgentID: parseUUID(f.scope.AgentID), TenantOrgID: f.scope.TenantOrgID, Scene: f.scope.Scene, Kind: employeememory.ScopePrivate, PrincipalID: f.requester}
}

func TestEmployeeLearningCaptureIsPrivateInferredAndIdempotent(t *testing.T) {
	f := newEmployeeLearningFixture(t)
	ctx := context.Background()
	calls := f.model.calls
	if n, err := f.response.h.ReconcileEmployeeLearnings(ctx, 100); err != nil || n != 1 {
		t.Fatalf("capture=%d %v", n, err)
	}
	var raw []byte
	var kind, principal string
	if err := testPool.QueryRow(ctx, `SELECT record,scope_kind,principal_id FROM employee_learning WHERE agent_id=$1`, f.scope.AgentID).Scan(&raw, &kind, &principal); err != nil {
		t.Fatal(err)
	}
	var learning employeememory.LearningRecord
	if err := json.Unmarshal(raw, &learning); err != nil {
		t.Fatal(err)
	}
	if kind != "private" || principal != f.requester || learning.Trusted || learning.Source != employeememory.LearningSourceInferred || learning.Confidence > 3 || learning.SourceID != "employee-run:"+f.runID || learning.EvidenceID != "agent_task_queue:"+f.queueID {
		t.Fatalf("runtime claim was promoted or mis-scoped: %s %s %s", kind, principal, raw)
	}
	if _, err := f.response.h.ReconcileEmployeeLearnings(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_learning WHERE agent_id=$1`, f.scope.AgentID).Scan(&count); err != nil || count != 1 || f.model.calls != calls {
		t.Fatalf("duplicate/model capture: records=%d calls=%d %v", count, f.model.calls, err)
	}
	if err := f.response.h.EmployeeMemory.Reset(ctx, f.privateScope()); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM employee_learning_consumption WHERE run_id=$1`, f.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.response.h.ReconcileEmployeeLearnings(ctx, 100); err != nil {
		t.Fatal(err)
	}
	brief, err := f.response.h.EmployeeMemory.Brief(ctx, f.privateScope(), "", 8)
	if err != nil || brief != "" {
		t.Fatalf("reset resurrected old evidence: %q %v", brief, err)
	}
}

func TestEmployeeLearningCaptureSkipsStaleRevisionDurably(t *testing.T) {
	f := newEmployeeLearningFixture(t)
	ctx := context.Background()
	task, err := employeetask.NewStore(testPool).Get(ctx, f.scope, f.taskID)
	if err != nil {
		t.Fatal(err)
	}
	corrected := employeetask.Definition{Goal: "Use a different reporting interval"}
	if _, _, err = employeetask.NewStore(testPool).AppendInput(ctx, f.scope, f.taskID, employeetask.InputParams{Source: employeetask.Source{Namespace: "correction", Key: "new"}, ActorRef: f.requester, Body: corrected.Goal, Correction: &corrected, ExpectedVersion: task.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.response.h.ReconcileEmployeeLearnings(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err = testPool.QueryRow(ctx, `SELECT state,reason FROM employee_learning_consumption WHERE run_id=$1`, f.runID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "skipped" || reason != "stale_goal_revision" {
		t.Fatalf("stale capture: %s %s", state, reason)
	}
	if n, err := f.response.h.ReconcileEmployeeLearnings(ctx, 100); err != nil || n != 0 {
		t.Fatalf("permanent skip retried: %d %v", n, err)
	}
}

func TestEmployeePrivateBriefOnlyForOneKnownRequester(t *testing.T) {
	f := newEmployeeLearningFixtureInConversation(t, "single")
	ctx := context.Background()
	if _, err := f.response.h.ReconcileEmployeeLearnings(ctx, 100); err != nil {
		t.Fatal(err)
	}
	f.model.dispatch = false
	f.response.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "next-message", Text: "What did you learn?"}}
	if response := employeeHTTP(t, f.response, f.dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	if _, err := f.response.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT input_snapshot FROM employee_scene_job WHERE agent_id=$1 ORDER BY created_at DESC LIMIT 1`, f.scope.AgentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var single employeeSavedInput
	if err := json.Unmarshal(raw, &single); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(single.Input.Memory, "customer trend") {
		t.Fatalf("captured private brief is never consumed: %q", single.Input.Memory)
	}
	f.response.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "alice-message", SenderOpenDingTalkID: "requester-open-id", Text: "My request"}, {OpenMsgID: "bob-message", SenderOpenDingTalkID: "other-person", Text: "My separate request"}}
	if response := employeeHTTP(t, f.response, f.dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	if _, err := f.response.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT input_snapshot FROM employee_scene_job WHERE agent_id=$1 ORDER BY created_at DESC LIMIT 1`, f.scope.AgentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var mixed employeeSavedInput
	if err := json.Unmarshal(raw, &mixed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mixed.Input.Memory, "customer trend") {
		t.Fatal("mixed window exposed private requester memory")
	}
}

func TestEmployeeLearningCaptureConcurrentResetRejectsFirstOldEvidence(t *testing.T) {
	f := newEmployeeLearningFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	scope := f.privateScope()
	if _, err := testPool.Exec(ctx, `INSERT INTO employee_memory_state(workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id) VALUES($1,$2,$3,$4,'private',$5)`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.Scene.SceneID, scope.PrincipalID); err != nil {
		t.Fatal(err)
	}
	reset, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reset.Rollback(context.Background())
	var resetPID int
	if err = reset.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&resetPID); err != nil {
		t.Fatal(err)
	}
	if _, err = reset.Exec(ctx, `UPDATE employee_memory_state SET reset_at=clock_timestamp(),revision=revision+1 WHERE agent_id=$1 AND principal_id=$2`, scope.AgentID, scope.PrincipalID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := f.response.h.ReconcileEmployeeLearnings(ctx, 100); done <- err }()
	blocked := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err = testPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, resetPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("capture did not wait on real reset transaction")
	}
	if err = reset.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	brief, err := f.response.h.EmployeeMemory.Brief(ctx, scope, "", 8)
	if err != nil || brief != "" {
		t.Fatalf("first old evidence resurrected after concurrent reset: %q %v", brief, err)
	}
	var reason string
	if err = testPool.QueryRow(ctx, `SELECT reason FROM employee_learning_consumption WHERE run_id=$1`, f.runID).Scan(&reason); err != nil || reason != "pre_reset_outcome" {
		t.Fatalf("reset skip=%q %v", reason, err)
	}
}

func TestEmployeeLearningCaptureRejectsPermanentInputsOnce(t *testing.T) {
	cases := []struct{ name, sql, reason string }{
		{"rebound tenant", `UPDATE agent_dingtalk_identity SET org_id='another-org' WHERE agent_id=$1`, "stale_tenant"},
		{"archived", `UPDATE agent SET archived_at=now() WHERE id=$1`, "agent_archived"},
		{"missing requester", `UPDATE employee_task SET requester_ref='' WHERE agent_id=$1`, "missing_requester"},
		{"oversize requester", `UPDATE employee_task SET requester_ref=repeat('x',257) WHERE agent_id=$1`, "missing_requester"},
		{"empty result", `UPDATE employee_task_run SET result='' WHERE agent_id=$1`, "no_result_content"},
		{"execution mismatch", `UPDATE agent_task_queue SET status='cancelled' WHERE agent_id=$1`, "execution_state_mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newEmployeeLearningFixture(t)
			ctx := context.Background()
			if _, err := testPool.Exec(ctx, tc.sql, f.scope.AgentID); err != nil {
				t.Fatal(err)
			}
			if n, err := f.response.h.ReconcileEmployeeLearnings(ctx, 100); err != nil || n != 1 {
				t.Fatalf("first=%d %v", n, err)
			}
			if n, err := f.response.h.ReconcileEmployeeLearnings(ctx, 100); err != nil || n != 0 {
				t.Fatalf("replay=%d %v", n, err)
			}
			var reason string
			var learningCount int
			if err := testPool.QueryRow(ctx, `SELECT reason FROM employee_learning_consumption WHERE run_id=$1`, f.runID).Scan(&reason); err != nil || reason != tc.reason {
				t.Fatalf("reason=%s %v", reason, err)
			}
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_learning WHERE agent_id=$1`, f.scope.AgentID).Scan(&learningCount); err != nil || learningCount != 0 {
				t.Fatalf("private data captured after reject: %d %v", learningCount, err)
			}
		})
	}
}
func TestEmployeeLearningCaptureConcurrentConsumersCommitOnce(t *testing.T) {
	f := newEmployeeLearningFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan int, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := f.response.h.ReconcileEmployeeLearnings(ctx, 100)
			results <- n
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	total := 0
	for n := range results {
		total += n
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var records int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_learning WHERE agent_id=$1`, f.scope.AgentID).Scan(&records); err != nil || records != 1 || total != 1 {
		t.Fatalf("records=%d consumed=%d %v", records, total, err)
	}
}
func TestEmployeeLearningCaptureFailureRollsBackLearningWithReceipt(t *testing.T) {
	f := newEmployeeLearningFixture(t)
	ctx := context.Background()
	interrupted := errors.New("consumer interrupted before receipt")
	_, err := employeelearning.NewStore(testPool).Process(ctx, 100, func(ctx context.Context, tx pgx.Tx, c employeelearning.Candidate) (employeelearning.Result, error) {
		result, err := f.response.h.captureEmployeeRunCandidate(ctx, tx, c)
		if err != nil {
			return result, err
		}
		return result, interrupted
	})
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	var records, receipts int
	if err = testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_learning WHERE agent_id=$1),(SELECT count(*) FROM employee_learning_consumption WHERE agent_id=$1)`, f.scope.AgentID).Scan(&records, &receipts); err != nil || records != 0 || receipts != 0 {
		t.Fatalf("partial capture commit: %d %d %v", records, receipts, err)
	}
	if n, err := f.response.h.ReconcileEmployeeLearnings(ctx, 100); err != nil || n != 1 {
		t.Fatalf("recover=%d %v", n, err)
	}
}
