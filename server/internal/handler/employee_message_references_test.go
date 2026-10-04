package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
)

const (
	quoteRequesterOpenID = "requester-open-id"
	quoteEmployeeOpenID  = "employee-open-id"
)

type employeeQuoteSetup struct {
	notice         employeeNoticeFixture
	conversation   string
	quotedID       string
	text           string
	sender         string
	outerQuotes    string
	outerSender    string
	quotedSender   string
	replicasReady  bool
	anchorScene    string
	beforeAdmitted func(*employeeQuoteSetup)
}

// employeeQuoteHost admits a quote reply after an Employee Direct task exists
// and builds its snapshot with a fake provider standing in for DWS reads.
func employeeQuoteHost(t *testing.T, state, anchor string, configure ...func(*employeeQuoteSetup)) (*employeeQuoteSetup, *employeeSceneHost, employeeloop.Identity, employeeSourceMessage) {
	t.Helper()
	ctx := context.Background()
	s := &employeeQuoteSetup{notice: employeeNoticeDatabase(t, state, anchor == "callback-ack", false), text: "停止这个", outerSender: quoteRequesterOpenID, quotedSender: quoteEmployeeOpenID, replicasReady: true}
	s.conversation = s.notice.command.Event.Data.Conversation.OpenConversationID
	s.anchorScene = s.conversation
	switch anchor {
	case "ack", "callback-ack":
		s.quotedID = "msg-ack"
	case "notice":
		s.quotedID = "msg-notice"
		if _, err := reconcileEmployeeNotice(t, s.notice.h); err != nil {
			t.Fatal(err)
		}
	case "request":
		s.quotedID, s.quotedSender = "message-1", quoteRequesterOpenID
	default:
		s.quotedID = anchor
	}
	s.outerQuotes = s.quotedID
	for _, option := range configure {
		option(s)
	}
	switch anchor {
	case "ack":
		if tag, err := testPool.Exec(ctx, `UPDATE response_action SET state='delivered',provider_conversation_id=$3,provider_message_id='msg-ack' WHERE agent_id=$1::uuid AND input->>'scene_notice_id'=$2`, s.notice.agentID, s.notice.jobID, s.anchorScene); err != nil || tag.RowsAffected() != 1 {
			t.Fatal("acknowledgement anchor", tag.RowsAffected(), err)
		}
	case "notice":
		if tag, err := testPool.Exec(ctx, `UPDATE response_action SET state='delivered',provider_conversation_id=$3,provider_message_id='msg-notice' WHERE agent_id=$1::uuid AND input->>'employee_run_notice_id'=$2`, s.notice.agentID, s.notice.runID, s.anchorScene); err != nil || tag.RowsAffected() != 1 {
			t.Fatal("run notice anchor", tag.RowsAffected(), err)
		}
	case "callback-ack":
		// A native/Router source answers through its completion callback.
		var requestID, reply string
		if err := testPool.QueryRow(ctx, `SELECT request_id,result_message FROM task_completion_outbox WHERE agent_id=$1::uuid AND request_id LIKE 'multica-terminal:sync-completed:%'`, s.notice.agentID).Scan(&requestID, &reply); err != nil {
			t.Fatal(err)
		}
		if _, err := s.notice.h.PrepareExecutionResult(ctx, s.notice.command.CompletionCallback.URL, agentmessagerouter.ExecutionResultRequest{RequestID: requestID, AgentID: s.notice.agentID, ExecutionStatus: "completed", ResultMessage: reply}); err != nil {
			t.Fatal(err)
		}
		if tag, err := testPool.Exec(ctx, `UPDATE response_action SET state='delivered',provider_conversation_id=$2,provider_message_id='msg-ack' WHERE agent_id=$1::uuid AND request_id=$3`, s.notice.agentID, s.anchorScene, requestID); err != nil || tag.RowsAffected() != 1 {
			t.Fatal("callback acknowledgement anchor", tag.RowsAffected(), err)
		}
	}
	var endpoint, namespace string
	if err := testPool.QueryRow(ctx, `SELECT endpoint_id,id::text FROM agent_dispatch_endpoint WHERE agent_id=$1 LIMIT 1`, s.notice.agentID).Scan(&endpoint, &namespace); err != nil {
		t.Fatal(err)
	}
	dc := agentDispatchContext{EndpointID: endpoint, EndpointNamespaceID: parseUUID(namespace), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(s.notice.agentID)}
	message := s.notice.command.Event.Data.Messages[0]
	message.OpenMsgID, message.Text = "msg-quote", s.text
	message.ReferencedMessage = &DispatchReferencedMessage{OpenMsgID: s.quotedID, Text: "被引用的原消息"}
	if s.sender != "" {
		message.SenderOpenDingTalkID = s.sender
	}
	dws := newFakeResourceDWS()
	dws.messages["msg-quote"] = providerMessage(s.conversation, "msg-quote", s.outerSender, fmt.Sprintf(`{"openMessageId":%q}`, s.outerQuotes))
	dws.messages[s.quotedID] = providerMessage(s.conversation, s.quotedID, s.quotedSender, "")
	worker := s.notice.h.EmployeeSceneWorker
	worker.ResourceProvider = dws
	if s.beforeAdmitted != nil {
		s.beforeAdmitted(s)
	}
	host, id, source := employeeMemoryHost(t, s.notice.dingTalkResponseFixture, dc, []DispatchMessage{message})
	if !s.replicasReady {
		worker.ReplicaReady = func(context.Context) error { return errors.New("an older replica is live") }
	}
	input, err := worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	worker.ReplicaReady = func(context.Context) error { return nil }
	raw, _ := json.Marshal(input)
	if _, err = worker.store.SaveInput(ctx, host.job, raw); err != nil {
		t.Fatal(err)
	}
	return s, host, id, source
}

func employeeQuotedBindings(t *testing.T, host *employeeSceneHost) []employeeCurrentTaskBinding {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT input_snapshot FROM employee_scene_job WHERE id=$1::uuid`, host.job.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved employeeSavedInput
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	var out []employeeCurrentTaskBinding
	for _, binding := range saved.CurrentTasks {
		if binding.Origin == employeeQuoteOrigin {
			out = append(out, binding)
		}
	}
	return out
}

func employeeQuoteCall(name, id string, source employeeSourceMessage, ref string, extra map[string]any) employeeloop.ToolCall {
	args := map[string]any{"source_ref": source.SourceRef, "task_ref": ref}
	for key, value := range extra {
		args[key] = value
	}
	return employeeloop.ToolCall{Name: name, NativeToolCallID: id, Arguments: args}
}

func employeeTaskOf(t *testing.T, s *employeeQuoteSetup) string {
	t.Helper()
	var task string
	if err := testPool.QueryRow(context.Background(), `SELECT task_id::text FROM employee_task_run WHERE id=$1::uuid`, s.notice.runID).Scan(&task); err != nil {
		t.Fatal(err)
	}
	return task
}

// Quoting the employee's acknowledgement of a running task and asking to stop
// it stops exactly that task through the normal read/stop contract.
func TestEmployeeReferenceQuotedAcknowledgementStopsRunningTask(t *testing.T) {
	s, host, id, source := employeeQuoteHost(t, "running", "ack")
	ctx := context.Background()
	quoted := employeeQuotedBindings(t, host)
	if len(quoted) != 1 || quoted[0].Ref != "q1" || quoted[0].TaskID != employeeTaskOf(t, s) || quoted[0].SourceRef != source.SourceRef {
		t.Fatalf("quoted bindings = %+v", quoted)
	}
	if _, err := host.Execute(ctx, id, employeeQuoteCall("read_task", "quote-read", source, "q1", nil)); err != nil {
		t.Fatal(err)
	}
	result, err := host.Execute(ctx, id, employeeQuoteCall("stop_task", "quote-stop", source, "q1", map[string]any{"read_ref": "quote-read", "instruction_quote": "停止这个"}))
	if err != nil || result.Receipt == "" {
		t.Fatal("quoted stop", result, err)
	}
	var queue, task string
	var tasks int
	if err = testPool.QueryRow(ctx, `SELECT q.status,t.state,(SELECT count(*) FROM employee_task WHERE agent_id=t.agent_id) FROM agent_task_queue q JOIN employee_task_run r ON r.queue_task_id=q.id JOIN employee_task t ON t.id=r.task_id WHERE q.id=$1::uuid`, s.notice.queueID).Scan(&queue, &task, &tasks); err != nil {
		t.Fatal(err)
	}
	if queue != "cancelled" || task != "cancelled" || tasks != 1 {
		t.Fatalf("queue=%s task=%s tasks=%d", queue, task, tasks)
	}
}

// A native source's acknowledgement goes through its completion callback;
// quoting it locates the same Task (pre REF-01a found this path missing).
func TestEmployeeReferenceQuotedCallbackAcknowledgementLocatesTask(t *testing.T) {
	s, host, id, source := employeeQuoteHost(t, "running", "callback-ack")
	quoted := employeeQuotedBindings(t, host)
	if len(quoted) != 1 || quoted[0].TaskID != employeeTaskOf(t, s) {
		t.Fatalf("quoted bindings = %+v", quoted)
	}
	ctx := context.Background()
	if _, err := host.Execute(ctx, id, employeeQuoteCall("read_task", "quote-read", source, "q1", nil)); err != nil {
		t.Fatal(err)
	}
	if result, err := host.Execute(ctx, id, employeeQuoteCall("stop_task", "quote-stop", source, "q1", map[string]any{"read_ref": "quote-read", "instruction_quote": "停止这个"})); err != nil || result.Receipt == "" {
		t.Fatal("quoted stop", result, err)
	}
}

// Quoting a delivered result notice continues the same Task with a new Run.
func TestEmployeeReferenceQuotedNoticeContinuesSameTask(t *testing.T) {
	s, host, id, source := employeeQuoteHost(t, "succeeded", "notice", func(s *employeeQuoteSetup) { s.text = "继续整理成表格" })
	ctx := context.Background()
	if quoted := employeeQuotedBindings(t, host); len(quoted) != 1 || quoted[0].TaskID != employeeTaskOf(t, s) {
		t.Fatalf("quoted bindings = %+v", quoted)
	}
	if _, err := host.Execute(ctx, id, employeeQuoteCall("read_task", "quote-read", source, "q1", nil)); err != nil {
		t.Fatal(err)
	}
	result, err := host.Execute(ctx, id, employeeQuoteCall("continue_task", "quote-continue", source, "q1", map[string]any{"read_ref": "quote-read", "instruction_quote": "继续整理成表格", "prompt": "整理为表格", "reply": "我来继续整理。"}))
	if err != nil || result.Receipt == "" {
		t.Fatal("quoted continuation", result, err)
	}
	var tasks, runs, resumes int
	if err = testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task WHERE agent_id=$1),(SELECT count(*) FROM employee_task_run WHERE agent_id=$1),(SELECT count(*) FROM employee_task_entry WHERE agent_id=$1 AND kind='resumed')`, s.notice.agentID).Scan(&tasks, &runs, &resumes); err != nil || tasks != 1 || runs != 2 || resumes != 1 {
		t.Fatal(tasks, runs, resumes, err)
	}
}

// Quoting the requester's own original request also locates its Task.
func TestEmployeeReferenceQuotedRequestLocatesTask(t *testing.T) {
	s, host, _, _ := employeeQuoteHost(t, "running", "request")
	if quoted := employeeQuotedBindings(t, host); len(quoted) != 1 || quoted[0].TaskID != employeeTaskOf(t, s) {
		t.Fatalf("quoted bindings = %+v", quoted)
	}
}

// Without a Host fact, a provider-confirmed quote of this scene, the frozen
// requester's own task and a ready replica set, a quote reply locates nothing
// and cannot control any task; the older refusal of quoted control holds.
func TestEmployeeReferenceForgedOrForeignQuoteCannotControl(t *testing.T) {
	for name, tc := range map[string]struct {
		anchor string
		option func(*employeeQuoteSetup)
	}{
		"provider quotes another message":   {"ack", func(s *employeeQuoteSetup) { s.outerQuotes = "msg-other" }},
		"outer sender is not the requester": {"ack", func(s *employeeQuoteSetup) { s.outerSender = "open-intruder" }},
		"quoted message is the requester's": {"ack", func(s *employeeQuoteSetup) { s.quotedSender = quoteRequesterOpenID }},
		"anchor sent in another scene":      {"ack", func(s *employeeQuoteSetup) { s.anchorScene = "cid-other-scene" }},
		"another requester quotes it": {"ack", func(s *employeeQuoteSetup) {
			s.sender, s.outerSender = "open-colleague", "open-colleague"
		}},
		"typed task id without anchor": {"msg-plain", func(s *employeeQuoteSetup) {
			s.quotedSender = quoteRequesterOpenID
			s.beforeAdmitted = func(s *employeeQuoteSetup) {}
		}},
		"older replica live": {"ack", func(s *employeeQuoteSetup) { s.replicasReady = false }},
	} {
		t.Run(name, func(t *testing.T) {
			s, host, id, source := employeeQuoteHost(t, "running", tc.anchor, tc.option)
			ctx := context.Background()
			if quoted := employeeQuotedBindings(t, host); len(quoted) != 0 {
				t.Fatalf("unverified quote located %+v", quoted)
			}
			if name == "typed task id without anchor" && !strings.Contains(source.Message.Text, "停止") {
				t.Fatal(source.Message.Text)
			}
			if _, err := host.Execute(ctx, id, employeeQuoteCall("stop_task", "quote-stop", source, "q1", map[string]any{"read_ref": "quote-read", "instruction_quote": "停止这个"})); err == nil {
				t.Fatal("stop of an unlocated quoted task accepted")
			}
			// A t-ref candidate of the same requester stays refused for a quote.
			if _, err := host.Execute(ctx, id, employeeQuoteCall("read_task", "plain-read", source, "t1", nil)); err == nil {
				if _, err = host.Execute(ctx, id, employeeQuoteCall("stop_task", "plain-stop", source, "t1", map[string]any{"read_ref": "plain-read", "instruction_quote": "停止这个"})); err == nil {
					t.Fatal("quote reply stopped a non-quoted candidate")
				}
			}
			var queue string
			var intents int
			if err := testPool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM employee_task_entry WHERE agent_id=$2::uuid AND payload->>'operation'='stop') FROM agent_task_queue WHERE id=$1::uuid`, s.notice.queueID, s.notice.agentID).Scan(&queue, &intents); err != nil || queue != "running" || intents != 0 {
				t.Fatal("forged quote changed the task", queue, intents, err)
			}
		})
	}
}

// One quoted message can anchor several tasks; all are listed for the model
// to clarify, and each still needs its own read and the quoting source.
func TestEmployeeReferenceSeveralQuotedTasksAreListedForClarification(t *testing.T) {
	_, host, _, _ := employeeQuoteHost(t, "running", "ack", func(s *employeeQuoteSetup) {
		s.beforeAdmitted = func(s *employeeQuoteSetup) {
			ctx := context.Background()
			var requester, sceneID string
			if err := testPool.QueryRow(ctx, `SELECT requester_ref,scene_id::text FROM employee_task WHERE agent_id=$1::uuid`, s.notice.agentID).Scan(&requester, &sceneID); err != nil {
				t.Fatal(err)
			}
			scope := employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: s.notice.agentID, TenantOrgID: "456", Kind: employeetask.ScopeScene}
			scope.Scene.SceneID = sceneID
			second, err := employeetask.NewStore(testPool).Create(ctx, employeetask.CreateParams{Scope: scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: requester, Definition: employeetask.Definition{Goal: "Second task"}, Source: employeetask.Source{Namespace: "employee_scene", Key: uuid.NewString()}, Input: "{}"})
			if err != nil {
				t.Fatal(err)
			}
			entry, _ := json.Marshal(map[string]any{"input": map[string]any{"name": "dispatch_task", "native_tool_call_id": "call-second", "arguments": map[string]any{"source_ref": "x"}}, "result": map[string]any{"result": map[string]any{"Content": `{"task_id":"` + second.ID + `"}`, "Receipt": "run-second"}}})
			if _, err = testPool.Exec(ctx, `UPDATE employee_scene_job SET tool_journal=COALESCE(tool_journal,'{}'::jsonb)||jsonb_build_object('call-second',$2::jsonb) WHERE id=$1::uuid`, s.notice.jobID, entry); err != nil {
				t.Fatal(err)
			}
		}
	})
	quoted := employeeQuotedBindings(t, host)
	if len(quoted) != 2 || quoted[0].Ref == quoted[1].Ref || quoted[0].TaskID == quoted[1].TaskID {
		t.Fatalf("quoted bindings = %+v", quoted)
	}
	var brief string
	if err := testPool.QueryRow(context.Background(), `SELECT input_snapshot#>>'{input,TaskBrief}' FROM employee_scene_job WHERE id=$1::uuid`, host.job.ID).Scan(&brief); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, "quoted_task_candidates") || !strings.Contains(brief, "with several q-refs ask which one") {
		t.Fatalf("brief lacks quoted candidates guidance: %s", brief)
	}
}

// A reaction on an old 「停止这个任务」 message carries that old text but is
// never an instruction: stop, continue, steer, dispatch and memory all refuse
// it, nothing changes, and the snapshot tells the model to stay quiet.
func TestEmployeeReactionOnOldStopCommandHasNoEffect(t *testing.T) {
	f := employeeNoticeDatabase(t, "running", false, false)
	ctx := context.Background()
	var endpoint, namespace string
	if err := testPool.QueryRow(ctx, `SELECT endpoint_id,id::text FROM agent_dispatch_endpoint WHERE agent_id=$1 LIMIT 1`, f.agentID).Scan(&endpoint, &namespace); err != nil {
		t.Fatal(err)
	}
	dc := agentDispatchContext{EndpointID: endpoint, EndpointNamespaceID: parseUUID(namespace), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	message := f.command.Event.Data.Messages[0]
	message.OpenMsgID, message.Text = "msg-old-stop", "停止这个任务"
	message.Reaction = &DispatchMessageReaction{EmotionName: "OK", Action: "add"}
	f.h.EmployeeMemory = employeememory.NewStore(testPool)
	host, id, source := employeeMemoryHost(t, f.dingTalkResponseFixture, dc, []DispatchMessage{message})
	input, err := host.worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.Config.Persona.Instructions, "REACTIONS:") {
		t.Fatal("reaction guidance missing from the snapshot")
	}
	raw, _ := json.Marshal(input)
	if _, err = host.worker.store.SaveInput(ctx, host.job, raw); err != nil {
		t.Fatal(err)
	}
	for _, call := range []employeeloop.ToolCall{
		employeeQuoteCall("stop_task", "reaction-stop", source, "t1", map[string]any{"read_ref": "reaction-read", "instruction_quote": "停止这个任务"}),
		employeeQuoteCall("continue_task", "reaction-continue", source, "t1", map[string]any{"read_ref": "reaction-read", "instruction_quote": "停止这个任务", "prompt": "继续", "reply": "好"}),
		{Name: "steer_task", NativeToolCallID: "reaction-steer", Arguments: map[string]any{"source_ref": source.SourceRef, "correction": "停止这个任务", "reply": "好"}},
		{Name: "dispatch_task", NativeToolCallID: "reaction-dispatch", Arguments: map[string]any{"source_ref": source.SourceRef, "goal": "停止", "prompt": "停止这个任务", "reply": "好"}},
		employeeCaptureCall(source, "stop-preference", "停止这个任务"),
	} {
		if call.Name != "stop_task" && call.Name != "continue_task" {
			if _, err = host.Execute(ctx, id, call); err == nil {
				t.Fatalf("%s accepted a reaction source", call.Name)
			}
			continue
		}
		if _, err = host.Execute(ctx, id, employeeQuoteCall("read_task", "reaction-read", source, "t1", nil)); err != nil {
			t.Fatal(err)
		}
		if _, err = host.Execute(ctx, id, call); err == nil {
			t.Fatalf("%s accepted a reaction source", call.Name)
		}
	}
	var queue string
	var tasks, runs, intents, memories int
	if err = testPool.QueryRow(ctx, `SELECT (SELECT status FROM agent_task_queue WHERE id=$1::uuid),(SELECT count(*) FROM employee_task WHERE agent_id=$2::uuid),(SELECT count(*) FROM employee_task_run WHERE agent_id=$2::uuid),(SELECT count(*) FROM employee_task_entry WHERE agent_id=$2::uuid AND (payload->>'operation'='stop' OR kind IN ('steer','resumed'))),(SELECT count(*) FROM employee_learning WHERE agent_id=$2::uuid)`, f.queueID, f.agentID).Scan(&queue, &tasks, &runs, &intents, &memories); err != nil {
		t.Fatal(err)
	}
	if queue != "running" || tasks != 1 || runs != 1 || intents != 0 || memories != 0 {
		t.Fatalf("reaction had effects: queue=%s tasks=%d runs=%d intents=%d memories=%d", queue, tasks, runs, intents, memories)
	}
}

// The journal reader keeps only committed task effects of the asked source.
func TestEmployeeReferenceCommittedEffectsFromJournal(t *testing.T) {
	journal, _ := json.Marshal(map[string]any{
		"a": map[string]any{"input": map[string]any{"name": "dispatch_task", "arguments": map[string]any{"source_ref": "r/m1"}}, "result": map[string]any{"result": map[string]any{"Content": `{"task_id":"t-a"}`, "Receipt": "run-a"}}},
		"b": map[string]any{"input": map[string]any{"name": "stop_task", "arguments": map[string]any{"source_ref": "r/m2"}}, "result": map[string]any{"result": map[string]any{"Content": `{"task_id":"t-b"}`, "Receipt": "employee-stop:t-b:3"}}},
		"c": map[string]any{"input": map[string]any{"name": "dispatch_task", "arguments": map[string]any{"source_ref": "r/m1"}}, "result": map[string]any{"failure": "refused", "result": map[string]any{"Content": `{"task_id":"t-c"}`, "Receipt": "run-c"}}},
		"d": map[string]any{"input": map[string]any{"name": "read_task", "arguments": map[string]any{"source_ref": "r/m1"}}, "result": map[string]any{"result": map[string]any{"Content": `{"task_id":"t-d"}`, "Receipt": "x"}}},
		"e": map[string]any{"input": map[string]any{"name": "steer_task", "arguments": map[string]any{"source_ref": "r/m1"}}, "result": map[string]any{"result": map[string]any{"Content": `{"task_id":"t-e"}`}}},
	})
	got := strings.Join(sortedStrings(employeeCommittedTaskEffects(journal, "")), ",")
	if got != "t-a,t-b" {
		t.Fatalf("all effects = %s", got)
	}
	if got = strings.Join(employeeCommittedTaskEffects(journal, "r/m1"), ","); got != "t-a" {
		t.Fatalf("source effects = %s", got)
	}
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
