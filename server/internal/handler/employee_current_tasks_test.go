package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	openai "github.com/openai/openai-go/v3"
)

func TestEmployeeCurrentTaskContinuesSucceededWithoutIssue(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	var taskID, endpointID, endpointNamespace string
	if err := testPool.QueryRow(ctx, `SELECT task_id::text FROM employee_task_run WHERE id=$1`, f.runID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT endpoint_id,id::text FROM agent_dispatch_endpoint WHERE agent_id=$1 LIMIT 1`, f.agentID).Scan(&endpointID, &endpointNamespace); err != nil {
		t.Fatal(err)
	}
	dc := agentDispatchContext{EndpointID: endpointID, EndpointNamespaceID: parseUUID(endpointNamespace), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	f.command.Event.Data.Messages[0].OpenMsgID = "continue-current"
	f.command.Event.Data.Messages[0].Text = "继续这个事项，把已有结果整理为表格"
	if response := employeeHTTP(t, f.dingTalkResponseFixture, dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1 AND job_id<>$2 ORDER BY created_at DESC LIMIT 1`, f.agentID, f.jobID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	source := receipt + "/continue-current"
	calls := 0
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		raw, _ := json.Marshal(p.Messages)
		if calls == 1 {
			if !strings.Contains(string(raw), `t1`) || !strings.Contains(string(raw), `Analyze feedback`) {
				t.Error("new wake has no source-bound current Task candidate")
			}
			return employeeReplyCompletion(t, employeeReplyCall(t, "task-read", "read_task", map[string]any{"source_ref": source, "task_ref": "t1"})), nil
		}
		if calls == 2 {
			if !strings.Contains(string(raw), "真实已存结果") || !strings.Contains(string(raw), "succeeded") {
				t.Error("read did not expose persisted status/report")
			}
			return employeeReplyCompletion(t, employeeReplyCall(t, "continue-once", "continue_task", map[string]any{"source_ref": source, "task_ref": "t1", "read_ref": "task-read", "instruction_quote": "继续这个事项，把已有结果整理为表格", "prompt": "把已有结果整理为表格", "reply": "我来整理为表格。"})), nil
		}
		return employeeMemoryAnswer(t), nil
	})
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); !worked || err != nil {
		t.Fatal(worked, err)
	}
	var tasks, runs, issues int
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task WHERE agent_id=$1),(SELECT count(*) FROM employee_task_run WHERE task_id=$2),(SELECT count(*) FROM employee_task t JOIN issue i ON i.id=t.issue_id WHERE t.agent_id=$1)`, f.agentID, taskID).Scan(&tasks, &runs, &issues); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || runs != 2 || issues != 0 || calls != 2 {
		t.Fatalf("continuation tasks=%d runs=%d issues=%d calls=%d", tasks, runs, issues, calls)
	}
	var prompt string
	if err := testPool.QueryRow(ctx, `SELECT q.context->>'direct_task_prompt' FROM agent_task_queue q JOIN employee_task_run r ON r.queue_task_id=q.id WHERE r.task_id=$1 AND r.id<>$2`, taskID, f.runID).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "继续这个事项，把已有结果整理为表格") || !strings.Contains(prompt, "真实已存结果") {
		t.Fatal("new packet lost current source or previous result report")
	}
	var newJob, newRun, newQueue string
	if err := testPool.QueryRow(ctx, `SELECT q.context->>'employee_job_id',r.id::text,q.id::text FROM employee_task_run r JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE r.task_id=$1 AND r.id<>$2`, taskID, f.runID).Scan(&newJob, &newRun, &newQueue); err != nil {
		t.Fatal(err)
	}
	// A crash after accepted effect but before final outcome must replay the
	// model/read/effect journals, even though the task is now running.
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',available_at=now(),outcome=NULL WHERE id=$1`, newJob); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if calls != 2 {
		t.Fatal("recovery requested model again", calls)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1`, newQueue); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.TaskService.CompleteTask(ctx, parseUUID(newQueue), []byte(`{"output":"续接表格结果"}`), "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var noticeJob, noticeSource, noticeBody string
	if err := testPool.QueryRow(ctx, `SELECT job_id::text,source_ref,body FROM employee_run_notice WHERE run_id=$1`, newRun).Scan(&noticeJob, &noticeSource, &noticeBody); err != nil {
		t.Fatal(err)
	}
	if noticeJob != newJob || noticeSource != source || !strings.Contains(noticeBody, "续接表格结果") {
		t.Fatal("continued result returned to original dispatch", noticeJob, noticeSource, noticeBody)
	}
	var facts int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE agent_id=$1 AND source='employee.execution' AND source_event_id=$2`, f.agentID, newRun).Scan(&facts); err != nil || facts != 1 {
		t.Fatal("continuation terminal fact missing", facts, err)
	}
}

func employeeCurrentTaskHost(t *testing.T, state string, texts ...string) (employeeNoticeFixture, *employeeSceneHost, employeeloop.Identity, employeeSourceMessage) {
	t.Helper()
	f := employeeNoticeDatabase(t, state, false, false)
	var endpoint, namespace string
	if err := testPool.QueryRow(context.Background(), `SELECT endpoint_id,id::text FROM agent_dispatch_endpoint WHERE agent_id=$1 LIMIT 1`, f.agentID).Scan(&endpoint, &namespace); err != nil {
		t.Fatal(err)
	}
	dc := agentDispatchContext{EndpointID: endpoint, EndpointNamespaceID: parseUUID(namespace), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	sourceMessage := f.command.Event.Data.Messages[0]
	sourceMessage.OpenMsgID = "continuation-source"
	sourceMessage.Text = "继续整理成表格"
	if len(texts) > 0 {
		sourceMessage.Text = texts[0]
	}
	host, id, source := employeeMemoryHost(t, f.dingTalkResponseFixture, dc, []DispatchMessage{sourceMessage})
	input, err := host.worker.buildInput(context.Background(), host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input)
	if _, err = host.worker.store.SaveInput(context.Background(), host.job, raw); err != nil {
		t.Fatal(err)
	}
	return f, host, id, source
}
func employeeContinueCall(source employeeSourceMessage) employeeloop.ToolCall {
	return employeeloop.ToolCall{Name: "continue_task", NativeToolCallID: "continue-once", Arguments: map[string]any{"source_ref": source.SourceRef, "task_ref": "t1", "read_ref": "task-read", "instruction_quote": "继续整理成表格", "prompt": "整理为表格", "reply": "我来继续整理。"}}
}
func employeeReadCall(source employeeSourceMessage) employeeloop.ToolCall {
	return employeeloop.ToolCall{Name: "read_task", NativeToolCallID: "task-read", Arguments: map[string]any{"source_ref": source.SourceRef, "task_ref": "t1"}}
}

func TestEmployeeCurrentTaskReplayKeepsOneRunAndRejectsChangedPayload(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "succeeded")
	ctx := context.Background()
	if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	call := employeeContinueCall(source)
	first, err := host.Execute(ctx, id, call)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := host.Execute(ctx, id, call)
	if err != nil || replay.Receipt != first.Receipt {
		t.Fatal("receipt replay", replay, err)
	}
	call.Arguments["prompt"] = "different work"
	if _, err = host.Execute(ctx, id, call); !errors.Is(err, employeeentry.ErrConflict) {
		t.Fatal("changed source payload accepted", err)
	}
	var runs, resumes int
	if err = testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task_run WHERE agent_id=$1),(SELECT count(*) FROM employee_task_entry WHERE agent_id=$1 AND kind='resumed')`, f.agentID).Scan(&runs, &resumes); err != nil || runs != 2 || resumes != 1 {
		t.Fatal(runs, resumes, err)
	}
}

func TestEmployeeCurrentTaskContinuationRequiresReadAndSuccess(t *testing.T) {
	for _, state := range []string{"running", "failed", "cancelled", "succeeded"} {
		t.Run(state, func(t *testing.T) {
			f, host, id, source := employeeCurrentTaskHost(t, state)
			ctx := context.Background()
			if state != "succeeded" {
				if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
					t.Fatal(err)
				}
			}
			result, err := host.Execute(ctx, id, employeeContinueCall(source))
			if err == nil || result.Receipt != "" {
				t.Fatal("unread or unsafe task restarted", result, err)
			}
			var n int
			if err = testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE agent_id=$1`, f.agentID).Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
		})
	}
}

func TestEmployeeCurrentTaskJournalFailureRollsBackEntireContinuation(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "succeeded")
	ctx := context.Background()
	if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	name := "continuation_journal_failure_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := testPool.Exec(ctx, `CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected continuation checkpoint failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DROP TRIGGER IF EXISTS `+name+` ON employee_scene_job`)
		_, _ = testPool.Exec(ctx, `DROP FUNCTION IF EXISTS `+name+`()`)
	})
	if _, err := testPool.Exec(ctx, `CREATE TRIGGER `+name+` BEFORE UPDATE OF tool_journal ON employee_scene_job FOR EACH ROW WHEN (NEW.id='`+host.job.ID+`'::uuid) EXECUTE FUNCTION `+name+`() `); err != nil {
		t.Fatal(err)
	}
	client, exporter := employeeTraceClient(t)
	trace := employeeTraceStart(ctx, client, host.job)
	ctx = langfuse.ContextWithTrace(ctx, trace)
	defer trace.End(langfuse.EndOptions{})
	result, err := host.Execute(ctx, id, employeeContinueCall(source))
	if err == nil || result.Receipt != "" {
		t.Fatal("rolled back continuation advertised acceptance", result, err)
	}
	var runs, resumes int
	var state string
	if err = testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task_run WHERE agent_id=$1),(SELECT count(*) FROM employee_task_entry WHERE agent_id=$1 AND kind='resumed'),(SELECT state FROM employee_task WHERE agent_id=$1)`, f.agentID).Scan(&runs, &resumes, &state); err != nil || runs != 1 || resumes != 0 || state != "succeeded" {
		t.Fatal("continuation escaped rollback", runs, resumes, state, err)
	}
	spans := employeeTraceKind(exporter, "tool")
	if len(spans) != 1 || employeeTraceAttr(spans[0], "langfuse.observation.metadata.journal_committed") != "false" {
		t.Fatal("rollback trace claimed commit", spans)
	}
}

func TestEmployeeCurrentTaskUsesOnePoolConnection(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "succeeded")
	config, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	view := *f.h
	view.Queries = db.New(pool)
	view.TxStarter = pool
	view.TaskService = &service.TaskService{Queries: view.Queries, TxStarter: pool, Bus: f.h.Bus}
	host.worker = NewEmployeeSceneWorker(&view, host.worker.model)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err = host.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal("read nested a pool connection", err)
	}
	if result, err := host.Execute(ctx, id, employeeContinueCall(source)); err != nil || result.Receipt == "" {
		t.Fatal("continuation nested a pool connection", result, err)
	}
}

func TestEmployeeCurrentTaskRejectsStaleReadAfterConcurrentContinuation(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "succeeded")
	ctx := context.Background()
	if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Execute(ctx, id, employeeContinueCall(source)); err != nil {
		t.Fatal(err)
	}
	call := employeeContinueCall(source)
	call.NativeToolCallID = "another-continuation"
	if result, err := host.Execute(ctx, id, call); err == nil || result.Receipt != "" {
		t.Fatal("stale read started another writer", result, err)
	}
	var n int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE agent_id=$1`, f.agentID).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
}

func TestEmployeeCurrentTaskCandidatesRejectOtherRequesterAndScene(t *testing.T) {
	for _, differentScene := range []bool{false, true} {
		t.Run(fmt.Sprint(differentScene), func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false)
			var endpoint, namespace string
			if err := testPool.QueryRow(context.Background(), `SELECT endpoint_id,id::text FROM agent_dispatch_endpoint WHERE agent_id=$1 LIMIT 1`, f.agentID).Scan(&endpoint, &namespace); err != nil {
				t.Fatal(err)
			}
			dc := agentDispatchContext{EndpointID: endpoint, EndpointNamespaceID: parseUUID(namespace), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
			message := f.command.Event.Data.Messages[0]
			message.OpenMsgID = "foreign-task-reader"
			message.Text = "进度怎样"
			if differentScene {
				f.command.Event.Data.Conversation.OpenConversationID = "cid-other-" + uuid.NewString()
			} else {
				message.SenderUID = "other-person"
				message.SenderStaffID = "other-person"
				message.SenderOpenDingTalkID = "other-person"
			}
			host, id, source := employeeMemoryHost(t, f.dingTalkResponseFixture, dc, []DispatchMessage{message})
			input, err := host.worker.buildInput(context.Background(), host.job, host.envelopes, host.envelopes)
			if err != nil {
				t.Fatal(err)
			}
			if len(input.CurrentTasks) != 0 || input.Input.TaskBrief != "" {
				t.Fatal("foreign task appeared in candidates", input.CurrentTasks)
			}
			raw, _ := json.Marshal(input)
			if _, err = host.worker.store.SaveInput(context.Background(), host.job, raw); err != nil {
				t.Fatal(err)
			}
			if _, err = host.Execute(context.Background(), id, employeeReadCall(source)); err == nil {
				t.Fatal("t1 guessed foreign task")
			}
		})
	}
}

func TestEmployeeCurrentTaskThanksCreatesNoEffect(t *testing.T) {
	f, host, _, _ := employeeCurrentTaskHost(t, "succeeded", "谢谢，辛苦了")
	ctx := context.Background()
	// A natural text response remains a one-call path even with current tasks.
	calls := 0
	host.worker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		return employeeMemoryAnswer(t), nil
	})
	if err := host.worker.store.Retry(ctx, host.job, "resume worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE id=$1`, host.job.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := host.worker.ProcessNext(ctx); !worked || err != nil {
		t.Fatal(worked, err)
	}
	var tasks, runs, resumed int
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task WHERE agent_id=$1),(SELECT count(*) FROM employee_task_run WHERE agent_id=$1),(SELECT count(*) FROM employee_task_entry WHERE agent_id=$1 AND kind='resumed')`, f.agentID).Scan(&tasks, &runs, &resumed); err != nil || tasks != 1 || runs != 1 || resumed != 0 || calls != 1 {
		t.Fatal(tasks, runs, resumed, calls, err)
	}
}

func TestEmployeeCurrentTaskBlockedContinuationExplainsState(t *testing.T) {
	for _, state := range []string{"running", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			_, host, _, source := employeeCurrentTaskHost(t, state)
			calls := 0
			host.worker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				calls++
				if calls == 1 {
					return employeeReplyCompletion(t, employeeReplyCall(t, "task-read", "read_task", employeeReadCall(source).Arguments)), nil
				}
				return employeeReplyCompletion(t, employeeReplyCall(t, "continue-once", "continue_task", employeeContinueCall(source).Arguments)), nil
			})
			ctx := context.Background()
			if err := host.worker.store.Retry(ctx, host.job, "worker recovery"); err != nil {
				t.Fatal(err)
			}
			if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE id=$1`, host.job.ID); err != nil {
				t.Fatal(err)
			}
			if worked, err := host.worker.ProcessNext(ctx); !worked || err != nil {
				t.Fatal(worked, err)
			}
			var raw []byte
			if err := testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var outcome employeeSavedOutcome
			if err := json.Unmarshal(raw, &outcome); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"running": "仍在执行", "failed": "执行失败", "cancelled": "取消"}[state]
			if !strings.Contains(outcome.Outcome.Reply, want) || !strings.Contains(outcome.Outcome.Reply, "未启动续接") || strings.Contains(outcome.Outcome.Reply, "已停止") {
				t.Fatal("continuation refusal hid actual state", outcome.Outcome.Reply)
			}
		})
	}
}

// A finished candidate's result reaches new work through builds_on, also when
// it is the only candidate (R1003-P1G5b: the single-upstream retrospective
// copied the numbers from chat instead).
func TestEmployeeCurrentTasksGuidanceRequiresBuildsOn(t *testing.T) {
	for _, want := range []string{"dispatch_task builds_on", "even when it is the only one", "do not copy its numbers"} {
		if !strings.Contains(employeeCurrentTaskGuidance, want) {
			t.Fatalf("candidate guidance lacks %q", want)
		}
	}
}

// The guidance is frozen into the wake's input snapshot: a replayed wake sends
// the bytes it was admitted with, never the current text.
func TestEmployeeCurrentTasksFrozenGuidanceReplaysStoredBytes(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	source := employeeSecondRequest(t, f, "frozen-guidance", "基于刚才的结果写一段复盘")
	var requests []string
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		raw, _ := json.Marshal(p.Messages)
		requests = append(requests, string(raw))
		return employeeReplyCompletion(t, employeeReplyCall(t, "quiet-"+strconv.Itoa(len(requests)), "stay_quiet", map[string]any{})), nil
	})
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); !worked || err != nil {
		t.Fatal(worked, err)
	}
	var jobID, brief string
	if err := testPool.QueryRow(ctx, `SELECT id::text,input_snapshot#>>'{input,TaskBrief}' FROM employee_scene_job WHERE agent_id=$1::uuid AND id<>$2::uuid ORDER BY created_at DESC LIMIT 1`, f.agentID, f.jobID).Scan(&jobID, &brief); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || !strings.Contains(brief, "even when it is the only one") || !strings.Contains(requests[0], "even when it is the only one") || !strings.Contains(brief, source[:8]) {
		t.Fatalf("new wake did not freeze the current guidance: requests=%d brief=%q", len(requests), brief)
	}
	// Recover the same wake from an older snapshot whose guidance predates builds_on.
	var prior map[string]any
	if err := json.Unmarshal([]byte(brief), &prior); err != nil {
		t.Fatal(err)
	}
	prior["guidance"] = "Previously frozen candidate rules; keep this request unchanged."
	encoded, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	old := string(encoded)
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET input_snapshot=jsonb_set(input_snapshot,'{input,TaskBrief}',to_jsonb($2::text)),state='pending',outcome=NULL,model_journal='[]',model_attempts=0,tool_journal='{}',available_at=now() WHERE id=$1::uuid`, jobID, old); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if len(requests) != 2 || strings.Contains(requests[1], "even when it is the only one") || !strings.Contains(requests[1], "Previously frozen candidate rules; keep this request unchanged.") {
		t.Fatalf("replayed wake did not send its frozen guidance bytes")
	}
}
