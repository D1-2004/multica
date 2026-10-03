package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

type employeeContinuationResultFixture struct {
	employeeNoticeFixture
	originalRunID string
	taskID        string
	source        employeeSourceMessage
}

// Admit both messages through the actual ingress. Resume, queue, Run and the
// Host checkpoint commit together, so mutable queue metadata is never the
// fixture's only evidence of authorization.
func employeeContinuationResultDatabase(t *testing.T, router bool) employeeContinuationResultFixture {
	t.Helper()
	ctx := context.Background()
	f := employeeNoticeDatabase(t, "succeeded", router, false, func(f *dingTalkResponseFixture, _ *employeeTestModel) {
		f.command.Event.Data.Messages[0].SenderUID = "same-requester"
		f.command.Event.Data.Messages[0].SenderOpenDingTalkID = "original-open-id"
	})
	request := employeeExecutionReplayRequest(t, f)
	var items []employeeentry.Item
	if err := testPool.QueryRow(ctx, `SELECT items FROM employee_scene_job WHERE id=$1::uuid`, f.jobID).Scan(&items); err != nil {
		t.Fatal(err)
	}
	var env employeeDispatchEnvelope
	if err := json.Unmarshal(items[0].Payload, &env); err != nil {
		t.Fatal(err)
	}
	dc := agentDispatchContext{EndpointID: env.EndpointID, EndpointNamespaceID: parseUUID(env.EndpointNamespaceID), UserID: parseUUID(env.PrincipalID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	f.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "continue-message", Text: "Continue the same analysis with this additional evidence", SenderUID: "same-requester", SenderOpenDingTalkID: "current-open-id"}}
	if router {
		f.command.CompletionCallback = responseTestCommand(f.agentID, testRouterTargetIdentity).CompletionCallback
	}
	if w := employeeHTTP(t, f.dingTalkResponseFixture, dc, uuid.NewString()); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	var jobID string
	if err := testPool.QueryRow(ctx, `SELECT id::text,items FROM employee_scene_job WHERE agent_id=$1::uuid AND id<>$2::uuid`, f.agentID, f.jobID).Scan(&jobID, &items); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(items[0].Payload, &env); err != nil {
		t.Fatal(err)
	}
	source := employeeSourceMessages(items[0], env)[0]
	f.model.dispatch, f.model.quiet = false, true
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	callID := "continue-call"
	key := source.ReceiptID + "/" + callID
	evidence, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(request.Context, &fields); err != nil {
		t.Fatal(err)
	}
	fields["employee_job_id"], fields["employee_source_ref"] = jobID, source.SourceRef
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.h.TaskService.PrepareDirectTask(ctx, service.DirectTaskRequest{Task: request.Task, Source: employeetask.Source{Namespace: "employee_scene", Key: key + "/run"}, Prompt: "Continue the same analysis with the current source evidence", PrincipalID: parseUUID(env.PrincipalID), Context: raw})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	accepted, err := f.h.TaskService.ContinueDirectTaskTx(ctx, tx, prepared, employeetask.ResumeParams{Source: employeetask.Source{Namespace: "employee_scene", Key: key + "/resume"}, ActorRef: source.RequesterRef, Body: string(evidence), ExpectedVersion: request.Task.Version})
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := json.Marshal(map[string]string{"task_id": request.Task.ID, "run_id": accepted.Run.ID, "queue_task_id": uuidToString(accepted.Task.ID)})
	checkpoint, _ := json.Marshal(map[string]any{
		"input":  employeeloop.ToolCall{NativeToolCallID: callID, Name: "continue_task", Arguments: map[string]any{"source_ref": source.SourceRef, "task_ref": "t1"}},
		"result": employeeToolRecord{Result: employeeloop.ToolResult{Content: string(ids), Receipt: accepted.Run.ID, Terminal: &employeeloop.Decision{Kind: employeeloop.Dispatched}}},
	})
	if _, err = tx.Exec(ctx, `UPDATE employee_scene_job SET tool_journal=tool_journal || jsonb_build_object($2::text,$3::jsonb) WHERE id=$1::uuid`, jobID, callID, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	out := employeeContinuationResultFixture{employeeNoticeFixture: f, originalRunID: f.runID, taskID: request.Task.ID, source: source}
	out.runID, out.queueID, out.jobID = accepted.Run.ID, uuidToString(accepted.Task.ID), jobID
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1::uuid`, out.queueID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.TaskService.CompleteTask(ctx, accepted.Task.ID, []byte(`{"output":"continued result"}`), "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEmployeeContinuationResultUsesAcceptedRunSource(t *testing.T) {
	for _, router := range []bool{false, true} {
		t.Run(map[bool]string{false: "scene", true: "callback"}[router], func(t *testing.T) {
			f := employeeContinuationResultDatabase(t, router)
			ctx := context.Background()
			for _, run := range []string{f.originalRunID, f.runID} {
				if created, err := f.h.recordEmployeeExecutionEvent(ctx, testWorkspaceID, run); err != nil || !created {
					t.Fatal(created, err)
				}
				var sourceReceipt string
				if err := testPool.QueryRow(ctx, `SELECT envelope->'payload'->>'source_receipt_id' FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, run).Scan(&sourceReceipt); err != nil {
					t.Error("accepted Run did not produce a terminal fact", err)
				}
				if run == f.runID && sourceReceipt != f.source.ReceiptID {
					t.Error("continuation fact reused old source", sourceReceipt)
				}
				if _, err := f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, run); err != nil {
					t.Fatal(err)
				}
				var raw []byte
				if err := testPool.QueryRow(ctx, `SELECT a.input FROM employee_run_notice n JOIN response_action a ON a.id=n.action_id WHERE n.run_id=$1::uuid AND n.state='enqueued'`, run).Scan(&raw); err != nil {
					t.Fatal("accepted Run did not enqueue its notice", err)
				}
				var input dingtalkresponse.ActionInput
				if err := json.Unmarshal(raw, &input); err != nil {
					t.Fatal(err)
				}
				wantSender, wantMessage := "original-open-id", "message-1"
				if run == f.runID {
					wantSender, wantMessage = "current-open-id", "continue-message"
				}
				if input.SenderOpenDingTalkID != wantSender || (router && input.ReplyToOpenMsgID != wantMessage) {
					t.Fatal("notice did not use this Run's accepted source", input)
				}
				if err := f.h.BeforeEmployeeRunNoticeSend(ctx, input); err != nil {
					t.Fatal("accepted Run notice failed its use-time source check", err)
				}
			}
		})
	}
}

func TestEmployeeContinuationResultRejectsUnprovenSource(t *testing.T) {
	cases := map[string]string{
		"missing_resume":       `DELETE FROM employee_task_entry WHERE task_id=$1::uuid AND kind='resumed'`,
		"other_actor":          `UPDATE employee_task_entry SET actor_ref='another-requester' WHERE task_id=$1::uuid AND kind='resumed'`,
		"other_evidence":       `UPDATE employee_task_entry SET body='{}' WHERE task_id=$1::uuid AND kind='resumed'`,
		"other_revision":       `UPDATE employee_task_entry SET goal_revision=goal_revision+1 WHERE task_id=$1::uuid AND kind='resumed'`,
		"other_native_call":    `UPDATE employee_task_entry SET source_key=source_key || '-forged' WHERE task_id=$1::uuid AND kind='resumed'`,
		"other_input_boundary": `UPDATE employee_task_run SET input_seq=1 WHERE queue_task_id=$2::uuid`,
		"nonterminal_journal":  `UPDATE employee_scene_job SET tool_journal=tool_journal #- '{continue-call,result,result,Terminal}' WHERE id=$3::uuid`,
		"other_source":         `UPDATE employee_scene_job SET tool_journal=jsonb_set(tool_journal,'{continue-call,input,arguments,source_ref}','"forged"') WHERE id=$3::uuid`,
		"changed_frozen_input": `UPDATE agent_task_queue SET context=jsonb_set(context,'{employee_direct_input,employee_source_ref}','"forged"') WHERE id=$2::uuid`,
		"retargeted_context_and_frozen_input": `UPDATE agent_task_queue q SET context=q.context ||
 jsonb_build_object('employee_job_id',old.context->'employee_job_id','employee_source_ref',old.context->'employee_source_ref',
 'employee_direct_input',(q.context->'employee_direct_input') || jsonb_build_object('employee_job_id',old.context->'employee_job_id','employee_source_ref',old.context->'employee_source_ref'))
 FROM agent_task_queue old JOIN employee_task_run r ON r.queue_task_id=old.id WHERE q.id=$2::uuid AND r.task_id=$1::uuid AND old.id<>$2::uuid`,
	}
	for _, field := range []string{"task_id", "run_id", "queue_task_id"} {
		cases["journal_"+field] = `UPDATE employee_scene_job SET tool_journal=jsonb_set(tool_journal,'{continue-call,result,result,Content}',to_jsonb(((tool_journal #>> '{continue-call,result,result,Content}')::jsonb || '{"` + field + `":"wrong"}'::jsonb)::text)) WHERE id=$3::uuid`
	}
	for name, mutation := range cases {
		t.Run(name, func(t *testing.T) {
			f := employeeContinuationResultDatabase(t, false)
			ctx := context.Background()
			// All placeholders have explicit types, including unused ones, so the
			// adversarial mutations share the same fixture argument order.
			mutation = `WITH fixture AS (SELECT $1::uuid,$2::uuid,$3::uuid) ` + mutation
			if _, err := testPool.Exec(ctx, mutation, f.taskID, f.queueID, f.jobID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.h.recordEmployeeExecutionEvent(ctx, testWorkspaceID, f.runID); err != nil {
				t.Fatal(err)
			}
			var receipts int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, f.runID).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatal("unproven source produced a fact", receipts, err)
			}
			if _, err := f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, f.runID); err != nil {
				t.Fatal(err)
			}
			var state string
			var action bool
			if err := testPool.QueryRow(ctx, `SELECT state,action_id IS NOT NULL FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&state, &action); err != nil || state != "suppressed" || action {
				t.Fatal("unproven source authorized a notice", state, action, err)
			}
		})
	}
}

func TestEmployeeContinuationResultReassessesOnlyOlderProof(t *testing.T) {
	for _, proof := range []int{employeeExecutionProofVersion - 1, employeeExecutionProofVersion, employeeExecutionProofVersion + 1} {
		t.Run(strconv.Itoa(proof), func(t *testing.T) {
			f := employeeContinuationResultDatabase(t, false)
			ctx := context.Background()
			if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context=context || jsonb_build_object('employee_execution_event_skip',jsonb_build_object('version',1,'proof_version',$3::int,'run_id',$2::text,'reason','source_dispatch_missing')) WHERE id=$1::uuid`, f.queueID, f.runID, proof); err != nil {
				t.Fatal(err)
			}
			if created, err := f.h.recordEmployeeExecutionEvent(ctx, testWorkspaceID, f.runID); err != nil || created != (proof < employeeExecutionProofVersion) {
				t.Fatalf("proof %d: created=%v err=%v", proof, created, err)
			}
			var receipts, preserved int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, f.runID).Scan(&receipts); err != nil || (receipts == 1) != (proof < employeeExecutionProofVersion) {
				t.Fatal("proof upgrade lost continuation or reopened settled rejection", proof, receipts, err)
			}
			if err := testPool.QueryRow(ctx, `SELECT (context->'employee_execution_event_skip'->>'proof_version')::int FROM agent_task_queue WHERE id=$1::uuid`, f.queueID).Scan(&preserved); err != nil || preserved != proof {
				t.Fatal("proof downgraded", proof, preserved, err)
			}
		})
	}
}
