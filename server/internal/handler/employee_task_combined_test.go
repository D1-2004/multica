package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	openai "github.com/openai/openai-go/v3"
)

type employeeCombinedRun struct{ Job, Source, DeliveryJob, DeliverySource, Run, Queue, Task string }

func employeeCombinedWake(t *testing.T, f employeeNoticeFixture, text, tool string) employeeCombinedRun {
	t.Helper()
	ctx := context.Background()
	var priorJob, priorSource string
	if err := testPool.QueryRow(ctx, `SELECT q.context->>'employee_job_id',q.context->>'employee_source_ref' FROM employee_task_run r JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE r.agent_id=$1 ORDER BY r.created_at DESC,r.id DESC LIMIT 1`, f.agentID).Scan(&priorJob, &priorSource); err != nil {
		t.Fatal(err)
	}
	var endpoint, namespace string
	if err := testPool.QueryRow(ctx, `SELECT endpoint_id,id::text FROM agent_dispatch_endpoint WHERE agent_id=$1 LIMIT 1`, f.agentID).Scan(&endpoint, &namespace); err != nil {
		t.Fatal(err)
	}
	dc := agentDispatchContext{EndpointID: endpoint, EndpointNamespaceID: parseUUID(namespace), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	msg := f.command.Event.Data.Messages[0]
	msg.OpenMsgID = uuid.NewString()
	msg.Text = text
	f.command.Event.Data.Messages = []DispatchMessage{msg}
	if response := employeeHTTP(t, f.dingTalkResponseFixture, dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var out employeeCombinedRun
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT job_id::text,receipt_id::text FROM employee_event_consumption WHERE agent_id=$1 ORDER BY created_at DESC LIMIT 1`, f.agentID).Scan(&out.Job, &receipt); err != nil {
		t.Fatal(err)
	}
	out.Source = receipt + "/" + msg.OpenMsgID
	calls := 0
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		if tool == "steer_task" {
			return employeeReplyCompletion(t, employeeReplyCall(t, "steer-call", tool, map[string]any{"source_ref": out.Source, "correction": text, "reply": "已按要求调整。"})), nil
		}
		if calls == 1 {
			return employeeReplyCompletion(t, employeeReplyCall(t, "task-read", "read_task", map[string]any{"source_ref": out.Source, "task_ref": "t1"})), nil
		}
		return employeeReplyCompletion(t, employeeReplyCall(t, "continue-call", "continue_task", map[string]any{"source_ref": out.Source, "task_ref": "t1", "read_ref": "task-read", "instruction_quote": text, "prompt": text, "reply": "我来继续。"})), nil
	})
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); !worked || err != nil {
		t.Fatal(worked, err)
	}
	callID := "continue-call"
	if tool == "steer_task" {
		callID = "steer-call"
	}
	var recordRaw []byte
	if err := testPool.QueryRow(ctx, `SELECT tool_journal->$2->'result' FROM employee_scene_job WHERE id=$1`, out.Job, callID).Scan(&recordRaw); err != nil {
		t.Fatal(err)
	}
	var record employeeToolRecord
	if json.Unmarshal(recordRaw, &record) != nil || record.Failure != "" {
		t.Fatal("wake did not accept execution", record.Failure)
	}
	var refs struct {
		Task  string `json:"task_id"`
		Run   string `json:"run_id"`
		Queue string `json:"queue_task_id"`
	}
	if json.Unmarshal([]byte(record.Result.Content), &refs) != nil || refs.Run == "" || record.Result.Receipt != refs.Run {
		t.Fatal("missing accepted execution references", record.Result)
	}
	out.Task, out.Run, out.Queue = refs.Task, refs.Run, refs.Queue
	out.DeliveryJob, out.DeliverySource = out.Job, out.Source
	if tool == "steer_task" {
		out.DeliveryJob, out.DeliverySource = priorJob, priorSource
	}
	var deliveredJob, deliveredSource string
	if err := testPool.QueryRow(ctx, `SELECT context->>'employee_job_id',context->>'employee_source_ref' FROM agent_task_queue WHERE id=$1`, out.Queue).Scan(&deliveredJob, &deliveredSource); err != nil || deliveredJob != out.DeliveryJob || deliveredSource != out.DeliverySource {
		t.Fatal("steer changed delivery anchor", deliveredJob, deliveredSource, err)
	}
	want := 1
	if tool == "continue_task" {
		want = 2
	}
	if calls != want {
		t.Fatal("extra model calls", calls)
	}
	return out
}
func employeeCombinedComplete(t *testing.T, f employeeNoticeFixture, run employeeCombinedRun) {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1`, run.Queue); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.TaskService.CompleteTask(ctx, parseUUID(run.Queue), []byte(`{"output":"组合执行已返回"}`), "", "", false, ""); err != nil {
		t.Fatal(err)
	}
}
func employeeCombinedResultProof(t *testing.T, f employeeNoticeFixture, run employeeCombinedRun) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var job, source, state, action string
	if err := testPool.QueryRow(ctx, `SELECT job_id::text,source_ref,state,action_id FROM employee_run_notice WHERE run_id=$1`, run.Run).Scan(&job, &source, &state, &action); err != nil {
		t.Error("current Run notice missing", err)
		return
	}
	if job != run.DeliveryJob || source != run.DeliverySource || state != "enqueued" || action == "" {
		t.Error("notice missing delivery or follows wrong accepted source", job, source, state, action)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE agent_id=$1 AND source='employee.execution' AND source_event_id=$2`, f.agentID, run.Run).Scan(&count); err != nil || count != 1 {
		t.Error("terminal event missing", count, err)
	}
	var effectJob, effectReceipt string
	if err := testPool.QueryRow(ctx, `SELECT envelope->'payload'->>'job_id',envelope->'payload'->>'source_receipt_id' FROM scene_event_receipt WHERE agent_id=$1 AND source='employee.execution' AND source_event_id=$2`, f.agentID, run.Run).Scan(&effectJob, &effectReceipt); err != nil || effectJob != run.Job || effectReceipt != strings.SplitN(run.Source, "/", 2)[0] {
		t.Error("execution fact followed delivery instead of effect source", effectJob, effectReceipt, err)
	}
}

func TestEmployeeTaskCombinedSteerThenContinuePreservesCorrections(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	correction := strings.Repeat("保留完整人工约束。", 260) + "CORRECTION_TAIL_MUST_SURVIVE"
	steered := employeeCombinedWake(t, f, correction, "steer_task")
	employeeCombinedComplete(t, f, steered)
	employeeCombinedResultProof(t, f, steered)
	// Non-correction ledger activity must not evict an earlier binding correction
	// from the next packet's dedicated current-corrections section.
	scope := employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "456", Kind: employeetask.ScopeScene}
	if err := testPool.QueryRow(context.Background(), `SELECT scene_id::text FROM employee_task WHERE id=$1`, steered.Task).Scan(&scope.Scene.SceneID); err != nil {
		t.Fatal(err)
	}
	store := employeetask.NewStore(testPool)
	for i := 0; i < 22; i++ {
		task, err := store.Get(context.Background(), scope, steered.Task)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = store.AppendInput(context.Background(), scope, task.ID, employeetask.InputParams{Source: employeetask.Source{Namespace: "combined-note", Key: fmt.Sprint(i)}, Body: "informational note", ExpectedVersion: task.Version}); err != nil {
			t.Fatal(err)
		}
	}
	continued := employeeCombinedWake(t, f, "继续产出最终表格", "continue_task")
	var prompt string
	if err := testPool.QueryRow(context.Background(), `SELECT context->>'direct_task_prompt' FROM agent_task_queue WHERE id=$1`, continued.Queue).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "CURRENT CORRECTIONS") || !strings.Contains(prompt, correction) {
		t.Error("continuation dropped complete binding correction")
	}
	if steered.Task != continued.Task {
		t.Error("continuation created different Task")
	}
	employeeCombinedComplete(t, f, continued)
	employeeCombinedResultProof(t, f, continued)
}

func TestEmployeeTaskCombinedContinueThenSteerMergedUsesCurrentSource(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	continued := employeeCombinedWake(t, f, "继续做表格", "continue_task")
	steered := employeeCombinedWake(t, f, "表格只保留已签约客户", "steer_task")
	if steered.Run != continued.Run || steered.Queue != continued.Queue || steered.Task != continued.Task {
		t.Fatal("queued correction created extra writer", continued, steered)
	}
	employeeCombinedComplete(t, f, steered)
	employeeCombinedResultProof(t, f, steered)
	var attempts int
	var journal []employeeentry.ModelTurn
	if err := testPool.QueryRow(context.Background(), `SELECT model_attempts,model_journal FROM employee_scene_job WHERE id=$1`, steered.Job).Scan(&attempts, &journal); err != nil || attempts != 1 || len(journal) != 1 {
		t.Fatal("steer added model passes", attempts, err)
	}
}

func TestEmployeeTaskCombinedClaimedContinuationThenSteerWaitsForExit(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	continued := employeeCombinedWake(t, f, "继续生成表格", "continue_task")
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='running',started_at=now(),dispatched_at=now() WHERE id=$1`, continued.Queue); err != nil {
		t.Fatal(err)
	}
	steered := employeeCombinedWake(t, f, "只保留已签约客户并添加日期", "steer_task")
	if steered.Run == continued.Run || steered.Task != continued.Task {
		t.Fatal("claimed steer did not create same-task successor", continued, steered)
	}
	var state string
	var stopPending bool
	if err := testPool.QueryRow(context.Background(), `SELECT status,COALESCE((context->>'process_stop_pending')::boolean,false) FROM agent_task_queue WHERE id=$1`, continued.Queue).Scan(&state, &stopPending); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" || !stopPending {
		t.Fatal("cancel lost pending exit barrier", state, stopPending)
	}
	agent, err := f.h.Queries.GetAgent(context.Background(), parseUUID(f.agentID))
	if err != nil {
		t.Fatal(err)
	}
	params := db.ClaimAgentTaskParams{AgentID: agent.ID, PrepareLeaseSecs: 60, EmployeeDirectRuntimeIds: []pgtype.UUID{agent.RuntimeID}}
	if _, err = f.h.Queries.ClaimAgentTask(context.Background(), params); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("successor claimed before exit acknowledgement", err)
	}
	if err = f.h.TaskService.AcknowledgeTaskProcessStopped(context.Background(), parseUUID(continued.Queue)); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.h.Queries.ClaimAgentTask(context.Background(), params)
	if err != nil || uuidToString(claimed.ID) != steered.Queue {
		t.Fatal("successor unavailable after exit acknowledgement", claimed.ID, err)
	}
	employeeCombinedComplete(t, f, steered)
	employeeCombinedResultProof(t, f, steered)
	var noticeState, reason string
	if err := testPool.QueryRow(context.Background(), `SELECT state,reason FROM employee_run_notice WHERE run_id=$1`, continued.Run).Scan(&noticeState, &reason); err != nil || noticeState != "suppressed" || reason != "steered" {
		t.Fatal("interrupted predecessor sent cancellation notice", noticeState, reason, err)
	}
}

func TestEmployeeTaskCombinedSteerProofRejectsForgery(t *testing.T) {
	for _, field := range []string{"ledger_actor", "ledger_body", "journal_source", "journal_result", "prompt", "input_boundary"} {
		t.Run(field, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false)
			run := employeeCombinedWake(t, f, "只保留已签约客户", "steer_task")
			employeeCombinedComplete(t, f, run)
			ctx := context.Background()
			var query string
			var args []any
			switch field {
			case "ledger_actor":
				query = `UPDATE employee_task_entry SET actor_ref='someone-else' WHERE task_id=$1 AND kind='steer'`
				args = []any{run.Task}
			case "ledger_body":
				query = `UPDATE employee_task_entry SET body='different correction' WHERE task_id=$1 AND kind='steer'`
				args = []any{run.Task}
			case "journal_source":
				query = `UPDATE employee_scene_job SET tool_journal=jsonb_set(tool_journal,'{steer-call,input,arguments,source_ref}','"other/source"') WHERE id=$1`
				args = []any{run.Job}
			case "journal_result":
				query = `UPDATE employee_scene_job SET tool_journal=jsonb_set(tool_journal,'{steer-call,result,result,Receipt}','"wrong-run"') WHERE id=$1`
				args = []any{run.Job}
			case "prompt":
				query = `UPDATE agent_task_queue SET context=jsonb_set(context,'{direct_task_prompt}','"forged work"') WHERE id=$1`
				args = []any{run.Queue}
			case "input_boundary":
				query = `UPDATE employee_task_run SET input_seq=(SELECT max(seq) FROM employee_task_entry WHERE task_id=$2 AND kind='run_started') WHERE id=$1`
				args = []any{run.Run, run.Task}
			}
			if _, err := testPool.Exec(ctx, query, args...); err != nil {
				t.Fatal(err)
			}
			if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
				t.Fatal(err)
			}
			if _, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil {
				t.Fatal(err)
			}
			var facts, sent int
			if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1),(SELECT count(*) FROM employee_run_notice WHERE run_id=$1::uuid AND state='enqueued')`, run.Run).Scan(&facts, &sent); err != nil || facts != 0 || sent != 0 {
				t.Fatal("forged steer accepted", facts, sent, err)
			}
		})
	}
}

func TestEmployeeTaskCombinedConcurrentContinueAndSteerHaveOneWriter(t *testing.T) {
	f, continued, id, source := employeeCurrentTaskHost(t, "succeeded")
	ctx := context.Background()
	if _, err := continued.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx, `SELECT task_id::text FROM employee_task_run WHERE id=$1`, f.runID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	task, err := employeetask.NewStore(testPool).Get(ctx, continued.taskScope(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	control := service.EmployeeTaskControl{Tasks: f.h.TaskService}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; _, err := continued.Execute(ctx, id, employeeContinueCall(source)); results <- err }()
	go func() {
		<-start
		_, err := control.Steer(ctx, service.EmployeeTaskSteerRequest{Task: task, Source: employeetask.Source{Namespace: "combined-concurrent", Key: uuid.NewString()}, ActorRef: source.RequesterRef, Content: "只保留已签约客户", SameRequester: true})
		results <- err
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil && !strings.Contains(err.Error(), "changed") {
			t.Fatal(err)
		}
	}
	var tasks, runs, active int
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task WHERE agent_id=$1),(SELECT count(*) FROM employee_task_run WHERE agent_id=$1),(SELECT count(*) FROM employee_task_run WHERE agent_id=$1 AND state='running')`, f.agentID).Scan(&tasks, &runs, &active); err != nil || tasks != 1 || runs != 2 || active != 1 {
		t.Fatal("concurrent controls created extra writer", tasks, runs, active, err)
	}
}

func TestEmployeeTaskCombinedCorrectionAfterReadRejectsOldVersion(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "succeeded")
	ctx := context.Background()
	if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx, `SELECT task_id::text FROM employee_task_run WHERE id=$1`, f.runID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	task, err := employeetask.NewStore(testPool).Get(ctx, host.taskScope(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (service.EmployeeTaskControl{Tasks: f.h.TaskService}).Steer(ctx, service.EmployeeTaskSteerRequest{Task: task, Source: employeetask.Source{Namespace: "combined-after-read", Key: uuid.NewString()}, ActorRef: source.RequesterRef, Content: "先应用这个新约束", SameRequester: true}); err != nil {
		t.Fatal(err)
	}
	if result, err := host.Execute(ctx, id, employeeContinueCall(source)); err == nil || !strings.Contains(err.Error(), "changed") || result.Receipt != "" {
		t.Fatal("old read ignored committed correction", result, err)
	}
	var count int
	if err = testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil || count != 2 {
		t.Fatal("old version created another execution", count, err)
	}
}

func TestEmployeeTaskCombinedCorrectionOverflowRejectsContinuation(t *testing.T) {
	for _, kind := range []string{"count", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			f, host, id, source := employeeCurrentTaskHost(t, "succeeded")
			ctx := context.Background()
			var taskID string
			if err := testPool.QueryRow(ctx, `SELECT task_id::text FROM employee_task_run WHERE id=$1`, f.runID).Scan(&taskID); err != nil {
				t.Fatal(err)
			}
			task, err := employeetask.NewStore(testPool).Get(ctx, host.taskScope(), taskID)
			if err != nil {
				t.Fatal(err)
			}
			count, body := 101, "preserve this condition"
			if kind == "bytes" {
				count = 5
				body = strings.Repeat("constraint ", 1400)
			}
			// Recovered pre-bound data remains readable, but cannot be continued
			// after silently trimming an accepted constraint.
			store := employeetask.NewStore(testPool)
			for i := 0; i < count; i++ {
				task, _, err = store.Steer(ctx, task.Scope, task.ID, employeetask.SteerParams{Source: employeetask.Source{Namespace: "legacy-overflow", Key: fmt.Sprint(i)}, ActorRef: source.RequesterRef, Body: body, ExpectedVersion: task.Version})
				if err != nil {
					t.Fatal(err)
				}
			}
			accepted, err := f.h.TaskService.EnqueueDirectTask(ctx, service.DirectTaskRequest{Task: task, Source: employeetask.Source{Namespace: "legacy-overflow", Key: "run"}, Prompt: "legacy accepted execution", PrincipalID: parseUUID(testUserID)})
			if err != nil {
				t.Fatal(err)
			}
			employeeCombinedComplete(t, f, employeeCombinedRun{Queue: uuidToString(accepted.Task.ID)})
			if _, err = host.Execute(ctx, id, employeeReadCall(source)); err != nil {
				t.Fatal(err)
			}
			if result, err := host.Execute(ctx, id, employeeContinueCall(source)); err == nil || !strings.Contains(err.Error(), "corrections exceed") || result.Receipt != "" {
				t.Fatal("partial corrections silently accepted", result, err)
			}
			var runs int
			if err = testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE agent_id=$1`, f.agentID).Scan(&runs); err != nil || runs != 2 {
				t.Fatal("overflow created additional execution", runs, err)
			}
		})
	}
}

func TestEmployeeTaskCombinedSteerKeepsTwentyFirstCorrection(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	task := employeeCombinedTask(t, f)
	control := service.EmployeeTaskControl{Tasks: f.h.TaskService}
	var result service.EmployeeTaskSteerResult
	for i := 0; i < 21; i++ {
		body := fmt.Sprintf("binding correction %d", i)
		if i == 0 {
			body = "FIRST_BINDING_CONSTRAINT"
		}
		var err error
		result, err = control.Steer(ctx, service.EmployeeTaskSteerRequest{Task: task, Source: employeetask.Source{Namespace: "combined-all", Key: fmt.Sprint(i)}, ActorRef: task.RequesterRef, Content: body, SameRequester: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	var prompt string
	if err := testPool.QueryRow(ctx, `SELECT context->>'direct_task_prompt' FROM agent_task_queue WHERE id=$1`, result.Queue.ID).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "FIRST_BINDING_CONSTRAINT") || !strings.Contains(prompt, "binding correction 20") {
		t.Fatal("steer silently dropped an accepted constraint")
	}
}
func employeeCombinedTask(t *testing.T, f employeeNoticeFixture) employeetask.Task {
	t.Helper()
	ctx := context.Background()
	var taskID, sceneID string
	if err := testPool.QueryRow(ctx, `SELECT t.id::text,t.scene_id::text FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE r.id=$1`, f.runID).Scan(&taskID, &sceneID); err != nil {
		t.Fatal(err)
	}
	scope := employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "456", Kind: employeetask.ScopeScene}
	scope.Scene.SceneID = sceneID
	task, err := employeetask.NewStore(testPool).Get(ctx, scope, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

type employeeCorrectionFailStarter struct{}

func (employeeCorrectionFailStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := testPool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return employeeCorrectionFailTx{tx}, nil
}

type employeeCorrectionFailTx struct{ pgx.Tx }

func (tx employeeCorrectionFailTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "kind='steer'") {
		return nil, errors.New("injected corrections read failure")
	}
	return tx.Tx.Query(ctx, sql, args...)
}
func TestEmployeeTaskCombinedSteerReadFailureKeepsWriter(t *testing.T) {
	f := employeeNoticeDatabase(t, "running", false, false)
	task := employeeCombinedTask(t, f)
	failed := &service.TaskService{Queries: f.h.Queries, TxStarter: employeeCorrectionFailStarter{}, Bus: f.h.Bus}
	_, err := (service.EmployeeTaskControl{Tasks: failed}).Steer(context.Background(), service.EmployeeTaskSteerRequest{Task: task, Source: employeetask.Source{Namespace: "combined-read-failure", Key: "once"}, ActorRef: task.RequesterRef, Content: "new condition", SameRequester: true})
	if err == nil || !strings.Contains(err.Error(), "injected corrections read failure") {
		t.Error("correction read failure was swallowed", err)
	}
	var state string
	var runs int
	if err = testPool.QueryRow(context.Background(), `SELECT status,(SELECT count(*) FROM employee_task_run WHERE task_id=$2) FROM agent_task_queue WHERE id=$1`, f.queueID, task.ID).Scan(&state, &runs); err != nil || state != "running" || runs != 1 {
		t.Error("failed correction stopped or replaced writer", state, runs, err)
	}
}

func TestEmployeeTaskCombinedSteerOverflowDoesNotCancelWriter(t *testing.T) {
	for _, kind := range []string{"count", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false)
			task := employeeCombinedTask(t, f)
			ctx := context.Background()
			control := service.EmployeeTaskControl{Tasks: f.h.TaskService}
			count, body := 100, "preserve accepted constraint"
			if kind == "bytes" {
				count = 4
				body = strings.Repeat("constraint ", 1400)
			}
			var accepted service.EmployeeTaskSteerResult
			for i := 0; i < count; i++ {
				var err error
				accepted, err = control.Steer(ctx, service.EmployeeTaskSteerRequest{Task: task, Source: employeetask.Source{Namespace: "before-overflow", Key: fmt.Sprint(i)}, ActorRef: task.RequesterRef, Content: body, SameRequester: true})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now(),dispatched_at=now() WHERE id=$1`, accepted.Queue.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := control.Steer(ctx, service.EmployeeTaskSteerRequest{Task: task, Source: employeetask.Source{Namespace: "before-overflow", Key: "too-much"}, ActorRef: task.RequesterRef, Content: body, SameRequester: true}); !errors.Is(err, employeetask.ErrCorrectionBounds) {
				t.Fatal("overflow not rejected", err)
			}
			var state string
			var runs int
			if err := testPool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM employee_task_run WHERE task_id=$2) FROM agent_task_queue WHERE id=$1`, accepted.Queue.ID, task.ID).Scan(&state, &runs); err != nil || state != "running" || runs != 2 {
				t.Fatal("overflow cancelled or replaced accepted writer", state, runs, err)
			}
		})
	}
}

func TestEmployeeTaskCombinedSteerWaitsForEffectCheckpoint(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	run := employeeCombinedWake(t, f, "只保留已签约客户", "steer_task")
	employeeCombinedComplete(t, f, run)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='running' WHERE id=$1`, run.Job); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var notice, fact int
	var skipped bool
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_run_notice WHERE run_id=$1),(SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$2),context ? 'employee_execution_event_skip' FROM agent_task_queue WHERE id=$3`, run.Run, run.Run, run.Queue).Scan(&notice, &fact, &skipped); err != nil || notice != 0 || fact != 0 || skipped {
		t.Fatal("uncommitted effect was permanently settled", notice, fact, skipped, err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='completed' WHERE id=$1`, run.Job); err != nil {
		t.Fatal(err)
	}
	employeeCombinedResultProof(t, f, run)
}

func TestEmployeeTaskCombinedLegacySteerRenderingRemainsVerifiable(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false)
			var run employeeCombinedRun
			for i := 0; i < 21; i++ {
				run = employeeCombinedWake(t, f, fmt.Sprintf("保存第%d项约束", i), "steer_task")
			}
			task := employeeCombinedTask(t, f)
			entries, err := employeetask.NewStore(testPool).Corrections(context.Background(), task.Scope, task.ID, 20)
			if err != nil {
				t.Fatal(err)
			}
			var corrections []employeetask.SteerCorrection
			for _, e := range entries {
				corrections = append(corrections, employeetask.SteerCorrection{Ref: fmt.Sprintf("employee_task_entry:%s/%d", e.TaskID, e.Seq), ActorRef: e.ActorRef, Body: e.Body})
			}
			var base string
			if err = testPool.QueryRow(context.Background(), `SELECT context->>'direct_steer_base_prompt' FROM agent_task_queue WHERE id=$1`, run.Queue).Scan(&base); err != nil {
				t.Fatal(err)
			}
			prompt := employeetask.WithCorrections(base, corrections)
			if _, err = testPool.Exec(context.Background(), `UPDATE agent_task_queue SET context=jsonb_set(CASE WHEN $3 THEN context-'direct_steer_corrections_version' ELSE context END,'{direct_task_prompt}',to_jsonb($2::text)) WHERE id=$1`, run.Queue, prompt, legacy); err != nil {
				t.Fatal(err)
			}
			employeeCombinedComplete(t, f, run)
			if legacy {
				employeeCombinedResultProof(t, f, run)
				return
			}
			if _, err = f.h.ReconcileEmployeeExecutionEvents(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			var n int
			if err = testPool.QueryRow(context.Background(), `SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, run.Run).Scan(&n); err != nil || n != 0 {
				t.Fatal("new full-render proof accepted omitted constraint", n, err)
			}
		})
	}
}

func TestEmployeeTaskCombinedFastRunWaitsForDeliveryJobCommit(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "succeeded")
	ctx := context.Background()
	if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	accepted, err := host.Execute(ctx, id, employeeContinueCall(source))
	if err != nil {
		t.Fatal(err)
	}
	var refs struct {
		Run   string `json:"run_id"`
		Queue string `json:"queue_task_id"`
	}
	if err = json.Unmarshal([]byte(accepted.Content), &refs); err != nil {
		t.Fatal(err)
	}
	employeeCombinedComplete(t, f, employeeCombinedRun{Queue: refs.Queue})
	if _, err = f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// The scanner excludes active delivery jobs; the final target fence must
	// also defer rather than persist a tombstone when called at this boundary.
	if created, err := f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, refs.Run); err != nil || created {
		t.Fatal("active delivery job was permanently settled", created, err)
	}
	var n int
	if err = testPool.QueryRow(ctx, `SELECT count(*) FROM employee_run_notice WHERE run_id=$1`, refs.Run).Scan(&n); err != nil || n != 0 {
		t.Fatal("premature notice", n, err)
	}
	saved := employeeSavedOutcome{Outcome: employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Dispatched, Reply: "已受理。"}}}
	raw, _ := json.Marshal(saved)
	if err = host.worker.store.SaveOutcome(ctx, host.job, raw); err != nil {
		t.Fatal(err)
	}
	if err = host.worker.complete(ctx, host.job, host.envelopes, saved); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	var actions int
	if err = testPool.QueryRow(ctx, `SELECT n.state,(SELECT count(*) FROM response_action a WHERE a.id=n.action_id) FROM employee_run_notice n WHERE n.run_id=$1`, refs.Run).Scan(&state, &actions); err != nil || state != "enqueued" || actions != 1 {
		t.Fatal("notice did not converge once after commit", state, actions, err)
	}
}
