package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	openai "github.com/openai/openai-go/v3"
)

// employeeWakeTestModel scripts one wake. It records every provider request so
// tests can inspect the exact rendered roles and the request count.
type employeeWakeTestModel struct {
	calls    int
	requests []json.RawMessage
	respond  func(call int) (finish string, message map[string]any)
}

func (m *employeeWakeTestModel) Chat(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	m.calls++
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	m.requests = append(m.requests, raw)
	finish, message := m.respond(m.calls)
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": message}}})
	var out openai.ChatCompletion
	return &out, json.Unmarshal(body, &out)
}

func wakeToolCall(id, name string, args map[string]any) (string, map[string]any) {
	raw, _ := json.Marshal(args)
	return "tool_calls", map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
}

type employeeWakeFixture struct {
	employeeNoticeFixture
	scope employeeentry.Scope
	task  employeetask.Task
	wake  *employeeWakeTestModel
}

// employeeWakeDatabase dispatches a real Employee Task from a router message
// (with its completion callback), lets its Run succeed, then installs a
// scripted wake model. The Task's origin is the real admitted message.
func employeeWakeDatabase(t *testing.T) employeeWakeFixture {
	t.Helper()
	f := employeeNoticeDatabase(t, "succeeded", true, false)
	ctx := context.Background()
	var taskID, sceneID string
	if err := testPool.QueryRow(ctx, `SELECT id::text,scene_id::text FROM employee_task WHERE agent_id=$1::uuid`, f.agentID).Scan(&taskID, &sceneID); err != nil {
		t.Fatal(err)
	}
	scope := employeeentry.Scope{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "456", SceneID: sceneID}
	task, err := employeetask.NewStore(testPool).Get(ctx, employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "456", Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: sceneID}}, taskID)
	if err != nil {
		t.Fatal(err)
	}
	wake := &employeeWakeTestModel{respond: func(int) (string, map[string]any) {
		return wakeToolCall("call-wake-reply", "reply", map[string]any{"reply": "三位同事都回复了，汇总如下。"})
	}}
	f.h.EmployeeSceneWorker.model = wake
	return employeeWakeFixture{f, scope, task, wake}
}

func (f employeeWakeFixture) admission(id string) employeeentry.TaskWakeAdmission {
	return employeeentry.TaskWakeAdmission{Scope: f.scope, Source: "execution", EventID: id, OccurredAt: time.Now().UTC(), Wake: employeeentry.TaskWake{
		SchemaVersion: employeeentry.TaskWakeSchemaVersion, Kind: employeeentry.TaskWakeExecutionFollowUp, TaskID: f.task.ID,
		GoalRevision: f.task.GoalRevision, InputSeq: f.task.LastEntrySeq, AuthorityRef: "task:" + f.task.ID, EvidenceRef: "run:" + f.runID,
	}}
}

func (f employeeWakeFixture) admit(t *testing.T, id string) employeeentry.Consumption {
	t.Helper()
	c, err := f.h.EmployeeSceneWorker.AdmitTaskWake(context.Background(), f.admission(id))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func wakeCount(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEmployeeTaskWakeRunsLoopOnceAndDeliversToTaskOrigin(t *testing.T) {
	f := employeeWakeDatabase(t)
	ctx := context.Background()
	// The first completion tries to redirect the reply with an extra target; the
	// Host rejects it as a paired native tool result, and the second call replies.
	f.wake.respond = func(call int) (string, map[string]any) {
		if call == 1 {
			return wakeToolCall("call-redirect", "reply", map[string]any{"reply": "x", "conversation_id": "cid-elsewhere"})
		}
		return wakeToolCall("call-wake-reply", "reply", map[string]any{"reply": "三位同事都回复了，汇总如下。"})
	}
	callbacks := wakeCount(t, `SELECT count(*) FROM task_completion_outbox WHERE agent_id=$1::uuid`, f.agentID)
	actions := wakeCount(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid`, f.agentID)
	wake := f.admit(t, "run-follow-up-1")
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("wake worker: %v %v", worked, err)
	}
	var state, kind string
	var attempts int
	if err := testPool.QueryRow(ctx, `SELECT state,kind,model_attempts FROM employee_scene_job WHERE id=$1::uuid`, wake.JobID).Scan(&state, &kind, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || kind != employeeentry.KindTaskWake || attempts != 2 || f.wake.calls != 2 || f.model.calls != 1 {
		t.Fatalf("wake job state=%s kind=%s attempts=%d wake calls=%d chat calls=%d", state, kind, attempts, f.wake.calls, f.model.calls)
	}
	if n := wakeCount(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid`, f.agentID); n != actions+1 {
		t.Fatalf("wake delivery intents=%d (before %d)", n, actions)
	}
	var input map[string]any
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT input FROM response_action WHERE agent_id=$1::uuid AND input->>'scene_notice_id'=$2`, f.agentID, wake.JobID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if input["conversation_id"] != f.command.Event.Data.Conversation.OpenConversationID || input["scene_id"] != f.scope.SceneID || input["sender_open_dingtalk_id"] != "requester-open-id" || input["text"] != "三位同事都回复了，汇总如下。" || input["callback_url"] != "" || input["dws_uid"] != "123" {
		t.Fatalf("wake did not deliver to the Task origin anchor: %s", raw)
	}
	if n := wakeCount(t, `SELECT count(*) FROM task_completion_outbox WHERE agent_id=$1::uuid`, f.agentID); n != callbacks {
		t.Fatalf("wake reused the origin message callback: %d -> %d", callbacks, n)
	}
	if wakeCount(t, `SELECT count(*) FROM employee_event_consumption WHERE job_id=$1::uuid AND state='completed'`, wake.JobID) != 1 {
		t.Fatal("wake consumption not completed")
	}
	// The wake is data, never a human window; native tool roles stay paired.
	for i, request := range f.wake.requests {
		var body struct {
			Messages []struct {
				Role       string `json:"role"`
				Content    any    `json:"content"`
				ToolCallID string `json:"tool_call_id"`
				ToolCalls  []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(request, &body); err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, tool := range body.Tools {
			names = append(names, tool.Function.Name)
		}
		if strings.Join(names, ",") != "reply,stay_quiet" {
			t.Fatalf("wake tools: %v", names)
		}
		wakeData, pending := 0, map[string]bool{}
		for _, message := range body.Messages {
			text, _ := message.Content.(string)
			if message.Role == "user" && strings.HasPrefix(text, "Current conversation window") {
				t.Fatalf("request %d contains a synthetic human window: %s", i, text)
			}
			if message.Role == "user" && strings.Contains(text, "Task wake from the Host") {
				wakeData++
				if !strings.HasPrefix(text, "Background follow-up (data):") || !strings.Contains(text, "Analyze feedback") || !strings.Contains(text, `"task_ref":"t1"`) {
					t.Fatalf("wake context not rendered as Task data: %s", text)
				}
				// The Task UUID stays Host-private (employeeTaskWakeTarget).
				if strings.Contains(text, f.task.ID) || employeeUUIDPattern.MatchString(text) {
					t.Fatalf("wake context exposes a raw UUID: %s", text)
				}
			}
			for _, call := range message.ToolCalls {
				pending[call.ID] = true
			}
			if message.Role == "tool" {
				if !pending[message.ToolCallID] {
					t.Fatalf("unpaired tool result %q", message.ToolCallID)
				}
				delete(pending, message.ToolCallID)
			}
		}
		if wakeData != 1 || len(pending) != 0 {
			t.Fatalf("request %d: wake data=%d unpaired calls=%v", i, wakeData, pending)
		}
	}
	// A restart after commit replays the outcome: no model call, one intent.
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',available_at=now() WHERE id=$1::uuid`, wake.JobID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("wake recovery: %v %v", worked, err)
	}
	if f.wake.calls != 2 || wakeCount(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid`, f.agentID) != actions+1 {
		t.Fatalf("recovery reran the model (%d) or sent twice", f.wake.calls)
	}
}

func TestEmployeeTaskWakeModelBudgetIsThreeRequests(t *testing.T) {
	f := employeeWakeDatabase(t)
	ctx := context.Background()
	f.wake.respond = func(int) (string, map[string]any) {
		return "length", map[string]any{"role": "assistant", "content": "partial"}
	}
	actions := wakeCount(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid`, f.agentID)
	wake := f.admit(t, "run-follow-up-budget")
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("wake worker: %v %v", worked, err)
	}
	var state string
	var attempts int
	var outcome []byte
	if err := testPool.QueryRow(ctx, `SELECT state,model_attempts,outcome FROM employee_scene_job WHERE id=$1::uuid`, wake.JobID).Scan(&state, &attempts, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || attempts != 3 || f.wake.calls != 3 || !strings.Contains(string(outcome), `"failure"`) {
		t.Fatalf("budget: state=%s attempts=%d calls=%d outcome=%s", state, attempts, f.wake.calls, outcome)
	}
	if n := wakeCount(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid`, f.agentID); n != actions {
		t.Fatalf("failed wake invented a message: %d", n-actions)
	}
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',available_at=now() WHERE id=$1::uuid`, wake.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	if f.wake.calls != 3 {
		t.Fatalf("restart spent a fourth request: %d", f.wake.calls)
	}
}

func TestEmployeeTaskWakeRefetchesAuthorityAndHoldsWithoutModel(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(t *testing.T, f employeeWakeFixture)
	}{
		{"stale_goal", "task_wake_stale_goal_revision", func(t *testing.T, f employeeWakeFixture) {
			if _, err := testPool.Exec(context.Background(), `UPDATE employee_task SET goal_revision=goal_revision+1 WHERE id=$1::uuid`, f.task.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"stopped", "task_stopped", func(t *testing.T, f employeeWakeFixture) {
			if _, err := testPool.Exec(context.Background(), `UPDATE employee_task SET state='cancelled' WHERE id=$1::uuid`, f.task.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"principal", "admission_principal_revoked", func(t *testing.T, f employeeWakeFixture) { revokeEmployeeNoticePrincipal(t) }},
		{"invoke", "admission_principal_revoked", func(t *testing.T, f employeeWakeFixture) {
			if _, err := testPool.Exec(context.Background(), `UPDATE agent SET permission_mode='private',owner_id=NULL WHERE id=$1::uuid`, f.agentID); err != nil {
				t.Fatal(err)
			}
		}},
		{"requester", "task_source_mismatch", func(t *testing.T, f employeeWakeFixture) {
			if _, err := testPool.Exec(context.Background(), `UPDATE employee_task SET requester_ref='dingtalk:456:open_id:someone-else' WHERE id=$1::uuid`, f.task.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"endpoint", "endpoint_revoked", func(t *testing.T, f employeeWakeFixture) {
			if _, err := testPool.Exec(context.Background(), `DELETE FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, f.agentID); err != nil {
				t.Fatal(err)
			}
		}},
		{"payload_target", "task_wake_task_missing", func(t *testing.T, f employeeWakeFixture) {
			// A wake naming another Task cannot borrow this job's authority.
			if _, err := testPool.Exec(context.Background(), `UPDATE employee_scene_job SET items=jsonb_set(items,'{0,payload,task_id}',to_jsonb($2::text)) WHERE agent_id=$1::uuid AND kind='task_wake'`, f.agentID, uuid.NewString()); err != nil {
				t.Fatal(err)
			}
		}},
		{"payload_changed", "task_wake_receipt_mismatch", func(t *testing.T, f employeeWakeFixture) {
			if _, err := testPool.Exec(context.Background(), `UPDATE employee_scene_job SET items=jsonb_set(items,'{0,payload,evidence_ref}','"run:forged"') WHERE agent_id=$1::uuid AND kind='task_wake'`, f.agentID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := employeeWakeDatabase(t)
			ctx := context.Background()
			wake := f.admit(t, "run-follow-up-"+tc.name)
			tc.change(t, f)
			if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
				t.Fatalf("worker: %v %v", worked, err)
			}
			var state, reason, jobState string
			if err := testPool.QueryRow(ctx, `SELECT c.state,c.reason,j.state FROM employee_event_consumption c JOIN employee_scene_job j ON j.id=c.job_id WHERE c.job_id=$1::uuid`, wake.JobID).Scan(&state, &reason, &jobState); err != nil {
				t.Fatal(err)
			}
			if state != "held" || reason != tc.reason || jobState != "completed" || f.wake.calls != 0 {
				t.Fatalf("state=%s reason=%s job=%s model calls=%d", state, reason, jobState, f.wake.calls)
			}
		})
	}
}

func TestEmployeeTaskWakeRecheckBeforeDeliveryHoldsLateStop(t *testing.T) {
	f := employeeWakeDatabase(t)
	ctx := context.Background()
	actions := wakeCount(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid`, f.agentID)
	f.wake.respond = func(int) (string, map[string]any) {
		// The requester stops the Task while the model composes the reply.
		if _, err := testPool.Exec(context.Background(), `UPDATE employee_task SET state='cancelled' WHERE id=$1::uuid`, f.task.ID); err != nil {
			t.Error(err)
		}
		return wakeToolCall("call-late", "reply", map[string]any{"reply": "late"})
	}
	wake := f.admit(t, "run-follow-up-late-stop")
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := testPool.QueryRow(ctx, `SELECT reason FROM employee_event_consumption WHERE job_id=$1::uuid`, wake.JobID).Scan(&reason); err != nil || reason != "task_stopped" {
		t.Fatalf("late stop: %s %v", reason, err)
	}
	if n := wakeCount(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid`, f.agentID); n != actions || f.wake.calls != 1 {
		t.Fatalf("stopped Task still delivered: actions=%d calls=%d", n-actions, f.wake.calls)
	}
}

func TestEmployeeTaskWakeProducerGateAndUnknownKindsHold(t *testing.T) {
	f := employeeWakeDatabase(t)
	ctx := context.Background()
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error {
		return errors.New("live server replicas do not all support employee-loop:13")
	}
	if ready, err := f.h.EmployeeSceneWorker.TaskWakeProducerReady(ctx); ready || err == nil {
		t.Fatalf("mixed replicas reported ready: %v %v", ready, err)
	}
	if _, err := f.h.EmployeeSceneWorker.AdmitTaskWake(ctx, f.admission("gated")); !errors.Is(err, employeeentry.ErrTaskWakeNotReady) {
		t.Fatalf("producer not gated: %v", err)
	}
	if n := wakeCount(t, `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='task_wake'`, f.agentID); n != 0 {
		t.Fatalf("gated producer wrote %d wakes", n)
	}
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return nil }
	if ready, err := f.h.EmployeeSceneWorker.TaskWakeProducerReady(ctx); !ready || err != nil {
		t.Fatalf("ready replicas: %v %v", ready, err)
	}
	for _, tc := range []struct {
		name, reason string
		mutate       func(*employeeentry.Job)
	}{
		{"job_kind", "unsupported_job_kind", func(j *employeeentry.Job) { j.Kind = "approval" }},
		{"wake_kind", "unsupported_task_wake_kind", func(j *employeeentry.Job) {
			j.Items[0].Payload = json.RawMessage(strings.Replace(string(j.Items[0].Payload), employeeentry.TaskWakeExecutionFollowUp, "approval.decision", 1))
		}},
		{"wake_shape", "task_wake_invalid", func(j *employeeentry.Job) { j.Items[0].Payload = json.RawMessage(`{"command":{}}`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wake := f.admit(t, "unknown-"+tc.name)
			job, err := f.h.EmployeeSceneWorker.store.Claim(ctx)
			if err != nil || job.ID != wake.JobID {
				t.Fatalf("claim: %+v %v", job, err)
			}
			// A newer binary's job reached this worker in memory only; the
			// claim filter normally retains such work for a supporting binary.
			tc.mutate(&job)
			if worked, err := f.h.EmployeeSceneWorker.processClaimed(ctx, job); err != nil || !worked {
				t.Fatalf("process: %v %v", worked, err)
			}
			var state, reason string
			if err = testPool.QueryRow(ctx, `SELECT state,reason FROM employee_event_consumption WHERE job_id=$1::uuid`, wake.JobID).Scan(&state, &reason); err != nil || state != "held" || reason != tc.reason || f.wake.calls != 0 {
				t.Fatalf("state=%s reason=%s calls=%d %v", state, reason, f.wake.calls, err)
			}
		})
	}
}

func TestEmployeeSceneEnvelopeMismatchRetryIsBounded(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	if response := employeeHTTP(t, f, dc, uuid.NewString()); response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	// A frozen command whose principal no longer matches its item can never
	// become valid; the original one-second retry must not spin forever.
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET items=jsonb_set(items,'{0,payload,principal_id}',to_jsonb($2::text)) WHERE agent_id=$1::uuid`, f.agentID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= employeePersistedRetryLimit; attempt++ {
		if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
			t.Fatalf("attempt %d: %v %v", attempt, worked, err)
		}
		var state, lastError string
		if err := testPool.QueryRow(ctx, `SELECT state,last_error FROM employee_scene_job WHERE agent_id=$1::uuid`, f.agentID).Scan(&state, &lastError); err != nil {
			t.Fatal(err)
		}
		if attempt < employeePersistedRetryLimit && (state != "pending" || lastError != "persisted employee scope mismatch") {
			t.Fatalf("attempt %d kept original retry? state=%s error=%s", attempt, state, lastError)
		}
		if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE agent_id=$1::uuid AND state='pending'`, f.agentID); err != nil {
			t.Fatal(err)
		}
	}
	var state, reason string
	if err := testPool.QueryRow(ctx, `SELECT state,reason FROM employee_event_consumption WHERE agent_id=$1::uuid`, f.agentID).Scan(&state, &reason); err != nil || state != "held" || reason != "persisted_scope_mismatch" {
		t.Fatalf("mismatch not held after the cap: %s %s %v", state, reason, err)
	}
	if _, err := f.h.EmployeeSceneWorker.store.Claim(ctx); !errors.Is(err, employeeentry.ErrNoJob) {
		t.Fatalf("held job still claimable: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("corrupt command reached the model: %d", model.calls)
	}
}

func TestEmployeeTaskWakeReplyBecomesLaterHumanHistory(t *testing.T) {
	f := employeeWakeDatabase(t)
	ctx := context.Background()
	wake := f.admit(t, "run-follow-up-history")
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("wake worker: %v %v", worked, err)
	}
	var actionID, principal, sourceKind, origin string
	if err := testPool.QueryRow(ctx, `SELECT action_id,principal_id::text,source_kind,COALESCE(origin_receipt_id::text,'') FROM employee_host_notice WHERE source_id=$1`, wake.JobID).Scan(&actionID, &principal, &sourceKind, &origin); err != nil {
		t.Fatalf("wake send has no history fact: %v", err)
	}
	var originReceipt string
	if err := testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE job_id=$1::uuid`, f.jobID).Scan(&originReceipt); err != nil {
		t.Fatal(err)
	}
	if principal != testUserID || sourceKind != "task_wake" || origin != originReceipt {
		t.Fatalf("history fact: principal=%s kind=%s origin=%s", principal, sourceKind, origin)
	}
	// The provider confirms the wake's send before the requester answers it.
	conversation := f.command.Event.Data.Conversation.OpenConversationID
	if _, err := testPool.Exec(ctx, `UPDATE response_action SET state='delivered',provider_message_id='wake-provider-message',provider_conversation_id=$2,updated_at=now()-interval '2 seconds' WHERE id=$1`, actionID, conversation); err != nil {
		t.Fatal(err)
	}
	var endpointID, namespaceID string
	if err := testPool.QueryRow(ctx, `SELECT endpoint_id,id::text FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, f.agentID).Scan(&endpointID, &namespaceID); err != nil {
		t.Fatal(err)
	}
	dc := agentDispatchContext{EndpointID: endpointID, EndpointNamespaceID: parseUUID(namespaceID), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	f.command.CompletionCallback, f.command.ResponsePolicy = nil, nil
	f.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "after-wake", Text: "把李四那条改成周一", SenderUID: "requester", SenderOpenDingTalkID: "requester-open-id"}}
	if response := employeeHTTP(t, f.dingTalkResponseFixture, dc, uuid.NewString()); response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	chat := &employeeWakeTestModel{respond: func(int) (string, map[string]any) {
		return "stop", map[string]any{"role": "assistant", "content": "好的，已改成周一。"}
	}}
	f.h.EmployeeSceneWorker.model = chat
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("human turn: %v %v", worked, err)
	}
	if chat.calls != 1 {
		t.Fatalf("human turn calls=%d", chat.calls)
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(chat.requests[0], &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range body.Messages {
		if text, _ := message.Content.(string); message.Role == "assistant" && strings.Contains(text, "三位同事都回复了，汇总如下。") {
			found = true
		}
	}
	if !found {
		t.Fatalf("wake reply missing from the next turn's history: %s", chat.requests[0])
	}
}

type employeeFixedOriginReader struct{ origin employeeentry.TaskOrigin }

func (r employeeFixedOriginReader) ReadTaskOrigin(context.Context, employeeentry.DB, employeeentry.Scope, employeetask.Task, employeetask.Entry) (employeeentry.TaskOrigin, error) {
	return r.origin, nil
}

func TestEmployeeTaskWakeEnterpriseOriginHasNoConversation(t *testing.T) {
	f := employeeWakeDatabase(t)
	ctx := context.Background()
	var sceneID string
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_scene(workspace_id,agent_id,provider,tenant_org_id,source_namespace,scene_kind,external_scene_id) VALUES($1::uuid,$2::uuid,'dingtalk','456','dingtalk.org','enterprise','456') RETURNING id::text`, testWorkspaceID, f.agentID).Scan(&sceneID); err != nil {
		t.Fatal(err)
	}
	scope := employeeentry.Scope{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "456", SceneID: sceneID}
	task, err := employeetask.NewStore(testPool).Create(ctx, employeetask.CreateParams{Scope: employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "456", Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: sceneID}}, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: "automation:enterprise-digest", Definition: employeetask.Definition{Goal: "Enterprise digest"}, Source: employeetask.Source{Namespace: "test_enterprise_origin", Key: uuid.NewString()}})
	if err != nil {
		t.Fatal(err)
	}
	reader := employeeFixedOriginReader{origin: employeeentry.TaskOrigin{PrincipalID: testUserID, PrincipalKind: "test_automation", History: employeeentry.HistoryNotApplicable, Anchor: employeeentry.DeliveryAnchor{SceneID: sceneID}}}
	if err = f.h.EmployeeSceneWorker.TaskOrigins().Register("test_enterprise_origin", reader); err != nil {
		t.Fatal(err)
	}
	actions := wakeCount(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid`, f.agentID)
	f.wake.respond = func(int) (string, map[string]any) {
		return "stop", map[string]any{"role": "assistant", "content": "无处可发的回复"}
	}
	wake, err := f.h.EmployeeSceneWorker.AdmitTaskWake(ctx, employeeentry.TaskWakeAdmission{Scope: scope, Source: "webhook", EventID: "delivery-1", OccurredAt: time.Now(), Wake: employeeentry.TaskWake{SchemaVersion: 1, Kind: employeeentry.TaskWakeWebhookDecision, TaskID: task.ID, GoalRevision: task.GoalRevision, InputSeq: task.LastEntrySeq, AuthorityRef: "webhook:endpoint", EvidenceRef: "delivery:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("enterprise wake: %v %v", worked, err)
	}
	var state, reason string
	if err = testPool.QueryRow(ctx, `SELECT state,reason FROM employee_event_consumption WHERE job_id=$1::uuid`, wake.JobID).Scan(&state, &reason); err != nil || state != "held" || reason != "task_wake_no_conversation" {
		t.Fatalf("enterprise reply: state=%s reason=%s %v", state, reason, err)
	}
	if f.wake.calls != 1 || wakeCount(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid`, f.agentID) != actions {
		t.Fatalf("enterprise wake sent or reran: calls=%d", f.wake.calls)
	}
	request := string(f.wake.requests[0])
	if !strings.Contains(request, `\"recent_conversation\":\"not_applicable\"`) || !strings.Contains(request, `\"return_target\":\"none\"`) || strings.Contains(request, "Recent conversation") || strings.Contains(request, `"name":"reply"`) {
		t.Fatalf("enterprise wake rendering: %s", request)
	}
}

func TestEmployeeTaskWakeReplySuppressedWhenTaskStopsBeforeSend(t *testing.T) {
	for _, tc := range []struct {
		name, change, want string
	}{{"delivered", "", "delivered"}, {"stopped", "cancelled", "cancelled"}, {"corrected", "corrected", "cancelled"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := employeeWakeDatabase(t)
			ctx := context.Background()
			wake := f.admit(t, "run-follow-up-send-"+tc.name)
			if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
				t.Fatalf("wake worker: %v %v", worked, err)
			}
			var actionID string
			if err := testPool.QueryRow(ctx, `SELECT id FROM response_action WHERE agent_id=$1::uuid AND input->>'scene_notice_id'=$2`, f.agentID, wake.JobID).Scan(&actionID); err != nil {
				t.Fatal(err)
			}
			// The requester stops (or corrects) the Task after the reply was
			// committed to the outbox and before the provider send.
			switch tc.change {
			case "cancelled":
				if _, err := testPool.Exec(ctx, `UPDATE employee_task SET state='cancelled' WHERE id=$1::uuid`, f.task.ID); err != nil {
					t.Fatal(err)
				}
			case "corrected":
				if _, err := testPool.Exec(ctx, `UPDATE employee_task SET goal_revision=goal_revision+1 WHERE id=$1::uuid`, f.task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := testPool.Exec(ctx, `DELETE FROM response_action WHERE agent_id=$1::uuid AND id<>$2`, f.agentID, actionID); err != nil {
				t.Fatal(err)
			}
			provider := &employeeNoticeProvider{}
			svc := dingtalkresponse.NewService(testPool, provider, nil)
			svc.BeforeSend = f.h.BeforeEmployeeRunNoticeSend
			runCtx, cancel := context.WithCancel(context.Background())
			go svc.Run(runCtx)
			t.Cleanup(func() {
				cancel()
				svc.WaitWithTimeout(context.Background(), 5*time.Second)
			})
			svc.Notify()
			waitEmployeeNoticeAction(t, actionID, tc.want)
			if tc.want == "cancelled" {
				var code string
				if err := testPool.QueryRow(ctx, `SELECT error_code FROM response_action WHERE id=$1`, actionID).Scan(&code); err != nil || !strings.Contains(code, "task_") {
					t.Fatalf("suppression reason: %q %v", code, err)
				}
				if provider.sends.Load() != 0 {
					t.Fatal("stopped Task's wake reply reached the provider")
				}
			} else if provider.sends.Load() != 1 {
				t.Fatalf("wake reply sends=%d", provider.sends.Load())
			}
		})
	}
}
