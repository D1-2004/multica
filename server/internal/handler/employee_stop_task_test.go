package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
)

func employeeStopCall(source employeeSourceMessage) employeeloop.ToolCall {
	return employeeloop.ToolCall{Name: "stop_task", NativeToolCallID: "stop-once", Arguments: map[string]any{"source_ref": source.SourceRef, "task_ref": "t1", "read_ref": "task-read", "instruction_quote": "停止这个任务"}}
}

func TestEmployeeStopTaskUsesRealWorkerWithoutSuccessor(t *testing.T) {
	f, host, _, source := employeeCurrentTaskHost(t, "running", "停止这个任务")
	ctx := context.Background()
	calls := 0
	host.worker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		if calls == 1 {
			return employeeReplyCompletion(t, employeeReplyCall(t, "task-read", "read_task", employeeReadCall(source).Arguments)), nil
		}
		if calls == 2 {
			return employeeReplyCompletion(t, employeeReplyCall(t, "stop-once", "stop_task", employeeStopCall(source).Arguments)), nil
		}
		return employeeMemoryAnswer(t), nil
	})
	if err := host.worker.store.Retry(ctx, host.job, "worker begins saved wake"); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE id=$1`, host.job.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := host.worker.ProcessNext(ctx); !worked || err != nil {
		t.Fatal(worked, err)
	}
	var tasks, runs int
	var queueState, taskState string
	var pending bool
	if err := testPool.QueryRow(ctx, `SELECT q.status,COALESCE((q.context->>'process_stop_pending')::boolean,false),t.state,(SELECT count(*) FROM employee_task WHERE agent_id=t.agent_id),(SELECT count(*) FROM employee_task_run WHERE task_id=t.id) FROM agent_task_queue q JOIN employee_task_run r ON r.queue_task_id=q.id JOIN employee_task t ON t.id=r.task_id WHERE q.id=$1`, f.queueID).Scan(&queueState, &pending, &taskState, &tasks, &runs); err != nil {
		t.Fatal(err)
	}
	if queueState != "cancelled" || taskState != "cancelled" || !pending || tasks != 1 || runs != 1 || calls != 2 {
		t.Fatalf("stop queued no cancellation or created work: queue=%s goal=%s pending=%v tasks=%d runs=%d models=%d", queueState, taskState, pending, tasks, runs, calls)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved employeeSavedOutcome
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.Outcome.Reply, "请求停止") || strings.Contains(saved.Outcome.Reply, "已停止") || strings.Contains(saved.Outcome.Reply, "已完成") {
		t.Fatal("stop acknowledgement invented exit evidence", saved.Outcome.Reply)
	}

	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',available_at=now(),outcome=NULL WHERE id=$1`, host.job.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := host.worker.ProcessNext(ctx); !worked || err != nil {
		t.Fatal(worked, err)
	}
	var intents int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_entry WHERE agent_id=$1 AND payload->>'operation'='stop'`, f.agentID).Scan(&intents); err != nil || intents != 1 || calls != 2 {
		t.Fatal("restart repeated model or stop effect", intents, calls, err)
	}
}

func TestEmployeeStopTaskReplayAndObservedExit(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "running", "停止这个任务")
	ctx := context.Background()
	if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	first, err := host.Execute(ctx, id, employeeStopCall(source))
	if err != nil {
		t.Fatal(err)
	}
	again, err := host.Execute(ctx, id, employeeStopCall(source))
	if err != nil || again.Receipt != first.Receipt {
		t.Fatal("stop replay changed receipt", again, err)
	}
	changed := employeeStopCall(source)
	changed.Arguments["instruction_quote"] = "停止"
	if _, err = host.Execute(ctx, id, changed); !errors.Is(err, employeeentry.ErrConflict) {
		t.Fatal("changed stop payload accepted", err)
	}
	readState := func(callID string) string {
		t.Helper()
		call := employeeReadCall(source)
		call.NativeToolCallID = callID
		result, err := host.Execute(ctx, id, call)
		if err != nil {
			t.Fatal(err)
		}
		var view map[string]any
		if json.Unmarshal([]byte(result.Content), &view) != nil {
			t.Fatal("bad current state")
		}
		return fmt.Sprint(view["execution_state"])
	}
	if state := readState("before-ack"); state != "stopping" {
		t.Fatal("logical cancellation claimed exit", state)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET completed_at=now()-interval '10 days' WHERE id=$1`, f.queueID); err != nil {
		t.Fatal(err)
	}
	if state := readState("after-timeout"); state != "stopping" {
		t.Fatal("time elapsed claimed exit", state)
	}
	if err = f.h.TaskService.AcknowledgeTaskProcessStopped(ctx, parseUUID(f.queueID)); err != nil {
		t.Fatal(err)
	}
	if state := readState("after-ack"); state != "stopped" {
		t.Fatal("real exit evidence not reflected", state)
	}
	var inputs, runs int
	if err = testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task_entry WHERE agent_id=$1 AND payload->>'operation'='stop'),(SELECT count(*) FROM employee_task_run WHERE agent_id=$1)`, f.agentID).Scan(&inputs, &runs); err != nil || inputs != 1 || runs != 1 {
		t.Fatal("replay or observation created work", inputs, runs, err)
	}
}

func TestEmployeeStopTaskRejectsMissingOrInvalidSourceRead(t *testing.T) {
	for _, fault := range []string{"no_read", "wrong_source", "wrong_quote", "wrong_task_ref", "revoked_principal", "stale_version"} {
		t.Run(fault, func(t *testing.T) {
			f, host, id, source := employeeCurrentTaskHost(t, "running", "停止这个任务")
			ctx := context.Background()
			if fault != "no_read" {
				if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
					t.Fatal(err)
				}
			}
			call := employeeStopCall(source)
			switch fault {
			case "wrong_source":
				call.Arguments["source_ref"] = "other/source"
			case "wrong_quote":
				call.Arguments["instruction_quote"] = "not in this message"
			case "wrong_task_ref":
				call.Arguments["task_ref"] = "t999"
			case "revoked_principal":
				if _, err := testPool.Exec(ctx, `UPDATE employee_event_consumption SET principal_id=$2::uuid WHERE job_id=$1::uuid`, host.job.ID, uuid.NewString()); err != nil {
					t.Fatal(err)
				}
			case "stale_version":
				if _, err := testPool.Exec(ctx, `UPDATE employee_task SET version=version+1 WHERE agent_id=$1`, f.agentID); err != nil {
					t.Fatal(err)
				}
			}
			result, err := host.Execute(ctx, id, call)
			if err == nil || result.Receipt != "" {
				t.Fatal("invalid stop accepted", fault, result, err)
			}
			var queue string
			var count int
			if err = testPool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM employee_task_entry WHERE agent_id=$2 AND payload->>'operation'='stop') FROM agent_task_queue WHERE id=$1`, f.queueID, f.agentID).Scan(&queue, &count); err != nil || queue != "running" || count != 0 {
				t.Fatal("rejected stop changed execution", queue, count, err)
			}
		})
	}
}

func TestEmployeeStopTaskJournalFailureRollsBackCancellation(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "running", "停止这个任务")
	ctx := context.Background()
	if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	name := "stop_journal_failure_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := testPool.Exec(ctx, `CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected stop checkpoint failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DROP TRIGGER IF EXISTS `+name+` ON employee_scene_job`)
		_, _ = testPool.Exec(ctx, `DROP FUNCTION IF EXISTS `+name+`()`)
	})
	if _, err := testPool.Exec(ctx, `CREATE TRIGGER `+name+` BEFORE UPDATE OF tool_journal ON employee_scene_job FOR EACH ROW WHEN (NEW.id='`+host.job.ID+`'::uuid) EXECUTE FUNCTION `+name+`() `); err != nil {
		t.Fatal(err)
	}
	result, err := host.Execute(ctx, id, employeeStopCall(source))
	if err == nil || result.Receipt != "" {
		t.Fatal("failed outer commit advertised stop", result, err)
	}
	var state, goal string
	var count int
	if err = testPool.QueryRow(ctx, `SELECT q.status,t.state,(SELECT count(*) FROM employee_task_entry WHERE task_id=t.id AND payload->>'operation'='stop') FROM agent_task_queue q JOIN employee_task_run r ON r.queue_task_id=q.id JOIN employee_task t ON t.id=r.task_id WHERE q.id=$1`, f.queueID).Scan(&state, &goal, &count); err != nil || state != "running" || goal != "running" || count != 0 {
		t.Fatal("stop escaped outer rollback", state, goal, count, err)
	}
}

func TestEmployeeStopTaskSuppressesLateResultWithoutReopening(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "running", "停止这个任务")
	ctx := context.Background()
	if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Execute(ctx, id, employeeStopCall(source)); err != nil {
		t.Fatal(err)
	}
	_, _ = f.h.TaskService.CompleteTask(ctx, parseUUID(f.queueID), []byte(`{"output":"LATE_OUTPUT_AFTER_STOP"}`), "", "", false, "")
	if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var queue, state, reason string
	var actions int
	if err := testPool.QueryRow(ctx, `SELECT q.status,n.state,n.reason,(SELECT count(*) FROM response_action a WHERE a.id=n.action_id) FROM agent_task_queue q JOIN employee_run_notice n ON n.queue_task_id=q.id WHERE q.id=$1`, f.queueID).Scan(&queue, &state, &reason, &actions); err != nil || queue != "cancelled" || state != "suppressed" || reason != "task_stop_requested" || actions != 0 {
		t.Fatal("stopped output was queued", queue, state, reason, actions, err)
	}
	task := employeeCombinedTask(t, f)
	if _, err := (service.EmployeeTaskControl{Tasks: f.h.TaskService}).Steer(ctx, service.EmployeeTaskSteerRequest{Task: task, Source: employeetask.Source{Namespace: "stop-regression", Key: "ordinary-correction"}, ActorRef: source.RequesterRef, Content: "ordinary later correction", SameRequester: true}); !errors.Is(err, employeetask.ErrStopped) {
		t.Fatal("ordinary steer revived a user stop", err)
	}
}

func TestEmployeeStopTaskBeforeSendFenceCancelsPendingOldResult(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "succeeded", "停止这个任务")
	ctx := context.Background()
	if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	stopped, err := host.Execute(ctx, id, employeeStopCall(source))
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Terminal == nil || !strings.Contains(stopped.Terminal.Reply, "完成") {
		t.Fatal("already completed execution was misreported", stopped)
	}
	provider := &employeeNoticeProvider{}
	action := startEmployeeNoticeDelivery(t, f, provider)
	waitEmployeeNoticeAction(t, action, "cancelled")
	if provider.sends.Load() != 0 {
		t.Fatal("old pending result reached provider after stop", provider.sends.Load())
	}
	var state, reason string
	if err := testPool.QueryRow(ctx, `SELECT state,reason FROM employee_run_notice WHERE run_id=$1`, f.runID).Scan(&state, &reason); err != nil || state != "suppressed" || reason != "task_stop_requested" {
		t.Fatal(state, reason, err)
	}
}

func TestEmployeeStopTaskCommittedProviderActionIsNotResubmitted(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "succeeded", "停止这个任务")
	ctx := context.Background()
	if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var actionID string
	if err := testPool.QueryRow(ctx, `SELECT action_id FROM employee_run_notice WHERE run_id=$1`, f.runID).Scan(&actionID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE response_action SET state='provider_accepted',provider_task_id='already-submitted' WHERE id=$1`, actionID); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Execute(ctx, id, employeeStopCall(source)); err != nil {
		t.Fatal(err)
	}
	provider := &employeeNoticeProvider{}
	startEmployeeNoticeDelivery(t, f, provider)
	waitEmployeeNoticeAction(t, actionID, "delivered")
	if provider.sends.Load() != 0 || provider.queries.Load() == 0 {
		t.Fatal("already submitted message resent or falsely revoked", provider.sends.Load(), provider.queries.Load())
	}
}

func TestEmployeeStopTaskSingleConnectionAndCachedReceipt(t *testing.T) {
	f, host, id, source := employeeCurrentTaskHost(t, "running", "停止这个任务")
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	view := *f.h
	view.Queries = db.New(pool)
	view.TxStarter = pool
	view.TaskService = &service.TaskService{Queries: view.Queries, TxStarter: pool, Bus: f.h.Bus}
	host.worker = NewEmployeeSceneWorker(&view, host.worker.model)
	bound, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err = host.Execute(bound, id, employeeReadCall(source)); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Execute(bound, id, employeeStopCall(source)); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Execute(bound, id, employeeStopCall(source)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_entry WHERE agent_id=$1 AND payload->>'operation'='stop'`, f.agentID).Scan(&n); err != nil || n != 1 {
		t.Fatal("recovery repeated stop intent", n, err)
	}
}

func TestEmployeeStopTaskGroupRequiresAddressedSource(t *testing.T) {
	for _, tc := range []struct {
		name               string
		mentions           []DispatchMention
		proactive, allowed bool
	}{
		{name: "known_other", mentions: []DispatchMention{{UID: "another-employee"}}},
		{name: "known_empty", mentions: []DispatchMention{}},
		{name: "mention_self", mentions: []DispatchMention{{UID: "123"}}, allowed: true},
		{name: "unknown_mentions", allowed: true},
		{name: "proactive_other", mentions: []DispatchMention{{UID: "another-employee"}}, proactive: true, allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "running", false, false)
			ctx := context.Background()
			// Participation is admitted from trusted configuration, not the request
			// flag that admission deliberately overwrites.
			f.h.EventTriggers = service.NewEventTriggerService(testPool, nil)
			agent, err := f.h.Queries.GetAgent(ctx, parseUUID(f.agentID))
			if err != nil {
				t.Fatal(err)
			}
			if err = f.h.EventTriggers.SetEnabled(ctx, agent, parseUUID(testUserID), tc.proactive); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_event_trigger WHERE agent_id=$1`, f.agentID)
			})
			var endpoint, namespace string
			if err := testPool.QueryRow(ctx, `SELECT endpoint_id,id::text FROM agent_dispatch_endpoint WHERE agent_id=$1 LIMIT 1`, f.agentID).Scan(&endpoint, &namespace); err != nil {
				t.Fatal(err)
			}
			dc := agentDispatchContext{EndpointID: endpoint, EndpointNamespaceID: parseUUID(namespace), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
			message := f.command.Event.Data.Messages[0]
			message.OpenMsgID = uuid.NewString()
			message.Text = "停止这个任务"
			message.Mentions = tc.mentions
			host, id, source := employeeMemoryHost(t, f.dingTalkResponseFixture, dc, []DispatchMessage{message})
			input, err := host.worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(input)
			if _, err = host.worker.store.SaveInput(ctx, host.job, raw); err != nil {
				t.Fatal(err)
			}
			if _, err = host.Execute(ctx, id, employeeReadCall(source)); err != nil {
				t.Fatal(err)
			}
			result, err := host.Execute(ctx, id, employeeStopCall(source))
			if tc.allowed {
				if err != nil || result.Receipt == "" {
					t.Fatal("addressed/proactive/unknown source rejected", result, err)
				}
			} else if err == nil || result.Receipt != "" {
				t.Error("unaddressed source obtained stop receipt", result, err)
			}
			var state string
			var intents int
			if err = testPool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM employee_task_entry WHERE agent_id=$2 AND payload->>'operation'='stop') FROM agent_task_queue WHERE id=$1`, f.queueID, f.agentID).Scan(&state, &intents); err != nil {
				t.Fatal(err)
			}
			if tc.allowed && (state != "cancelled" || intents != 1) || !tc.allowed && (state != "running" || intents != 0) {
				t.Fatal("wrong scope mutated execution", state, intents)
			}
		})
	}
}
