package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/pkg/llm"
	"github.com/multica-ai/multica/server/pkg/protocol"
	openai "github.com/openai/openai-go/v3"
)

type dingTalkResponseFixture struct {
	h                        *Handler
	agentID, taskID, issueID string
	command                  DispatchCommand
}

func responseTestCommand(agentID, target string) DispatchCommand {
	key := "response-test-" + uuid.NewString()
	return DispatchCommand{
		SchemaVersion: "2.0", AgentID: agentID,
		Source: DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-" + key, Type: "group"},
			Sender:       DispatchSender{DisplayName: "Requester", OpenDingTalkID: "requester-open-id"},
			Messages:     []DispatchMessage{{OpenMsgID: "message-1", Text: "Summarize the findings"}},
		}},
		Surface: DispatchSurface{Type: "issue"}, Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ResponsePolicy:   &protocol.DingTalkResponsePolicy{Version: 1, Mode: protocol.DingTalkResponseModeCoordinator, Revision: 7, ShowAITag: true},
		ExternalIdentity: AgentDispatchExternalIdentity{DWS: &AgentDispatchDWSIdentity{UID: "123", OrgID: "456"}},
		CompletionCallback: &DispatchCompletionCallback{
			URL:         "/api/v1/dispatch-tasks/" + key + "/execution-result",
			UpdateURL:   "/api/v1/dispatch-tasks/" + key + "/execution-update",
			ResponseURL: "/api/v1/dispatch-tasks/" + key + "/response-receipt", Target: target,
		},
	}
}

func newDingTalkResponseFixture(t *testing.T, target string) *dingTalkResponseFixture {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "response-test-runtime-"+uuid.NewString())
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "response-test-agent-"+uuid.NewString())
	taskID := createDispatchedClaimFixtureTask(t, ctx, agentID, runtimeID, issueID, "0 seconds", false)
	h := *testHandler
	h.DingTalkResponses = dingtalkresponse.NewService(testPool, nil, nil)
	h.TaskCompletionTargetIdentity = target
	h.Assoc = assoc.NewService(assoc.NewMemory())
	h.InboundCoordinator = inboundcoord.New(nil, nil, nil)
	f := &dingTalkResponseFixture{h: &h, agentID: agentID, taskID: taskID, issueID: issueID, command: responseTestCommand(agentID, target)}
	f.setContext(t, dispatchRuntimeContext(f.command, "response-test"))
	t.Cleanup(func() {
		for _, table := range []string{"response_action", "response_route", "sandbox_send_receipt", "task_completion_outbox", "task_execution_update_outbox", "inbound_coordinator_job", "task_token"} {
			if _, err := testPool.Exec(ctx, "DELETE FROM "+table+" WHERE agent_id=$1", agentID); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
		_, _ = testPool.Exec(ctx, `DELETE FROM chat_message WHERE chat_session_id IN (SELECT id FROM chat_session WHERE agent_id=$1)`, agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM chat_session WHERE agent_id=$1`, agentID)
	})
	return f
}

func (f *dingTalkResponseFixture) setContext(t *testing.T, raw []byte) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET context=$2::jsonb WHERE id=$1`, f.taskID, raw); err != nil {
		t.Fatal(err)
	}
}

func (f *dingTalkResponseFixture) register(t *testing.T) {
	t.Helper()
	if err := f.h.registerDingTalkResponseRoute(context.Background(), testPool, f.command, agentDispatchContext{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}); err != nil {
		t.Fatal(err)
	}
}

func (f *dingTalkResponseFixture) sandboxInput() dingtalkresponse.ActionInput {
	return dingtalkresponse.ActionInput{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TaskID: f.taskID, IssueID: f.issueID,
		DWSUID: "123", DWSOrgID: "456", ConversationID: f.command.Event.Data.Conversation.OpenConversationID, IsGroup: true, SenderOpenDingTalkID: "requester-open-id"}
}

func (f *dingTalkResponseFixture) pendingReceipt(t *testing.T) {
	t.Helper()
	if err := f.h.DingTalkResponses.RecordSandboxReceipt(context.Background(), f.sandboxInput(), protocol.DingTalkSendReceipt{
		ClientActionID: "sandbox-send", State: "pending", PayloadHash: strings.Repeat("a", 64), OpenConversationID: f.command.Event.Data.Conversation.OpenConversationID,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedDingTalkResponseScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*DispatchCommand)
		want   bool
	}{
		{"coordinator employee", func(*DispatchCommand) {}, true},
		{"legacy missing policy", func(c *DispatchCommand) { c.ResponsePolicy = nil }, false},
		{"legacy explicit", func(c *DispatchCommand) { c.ResponsePolicy.Mode = "legacy" }, false},
		{"robot", func(c *DispatchCommand) { c.Source.Type = "robot" }, false},
		{"SDK", func(c *DispatchCommand) { c.Outbound.Mode = "robot_sdk" }, false},
		{"reaction", func(c *DispatchCommand) { c.Event.Type = "emotionReply" }, false},
		{"calendar", func(c *DispatchCommand) { c.Event.Domain = "calendar" }, false},
		{"cancel", func(c *DispatchCommand) { c.Control = &DispatchControl{Action: "cancel"} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := responseTestCommand(uuid.NewString(), testRouterTargetIdentity)
			tc.mutate(&command)
			if got := managedDingTalkResponse(command); got != tc.want {
				t.Fatalf("managed=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestDingTalkResponseCallbackMustMatchTask(t *testing.T) {
	valid := responseTestCommand(uuid.NewString(), testRouterTargetIdentity)
	if err := valid.validate(); err != nil {
		t.Fatalf("valid fixture: %v", err)
	}
	for _, responseURL := range []string{
		"/api/v1/dispatch-tasks/some-other-task/response-receipt",
		"https://other.example" + valid.CompletionCallback.ResponseURL,
		valid.CompletionCallback.ResponseURL + "?redirect=other",
		strings.Replace(valid.CompletionCallback.ResponseURL, "response-receipt", "execution-result", 1),
		"",
	} {
		command := valid
		cb := *valid.CompletionCallback
		cb.ResponseURL = responseURL
		command.CompletionCallback = &cb
		if err := command.validate(); err == nil {
			t.Fatalf("accepted mismatched response callback %q", responseURL)
		}
	}
}

func TestDingTalkManagedPromptUsesPlatformPolicy(t *testing.T) {
	command := responseTestCommand(uuid.NewString(), testRouterTargetIdentity)
	var stored persistedDispatchContext
	if err := json.Unmarshal(dispatchRuntimeContext(command, "prompt"), &stored); err != nil {
		t.Fatal(err)
	}
	stored.CoordinatorIssueFollowUp = true
	managed := instructionFromSegments(composeDispatchInstructionSegments(dispatchInstructionInputs{Stored: stored, Present: true}))
	for _, forbidden := range []string{"--ai-tag=false", "--ai-tag=true", "remove processing/complete emotions", "remove 处理中"} {
		if strings.Contains(managed, forbidden) {
			t.Fatalf("managed prompt retains %q", forbidden)
		}
	}
	if !strings.Contains(managed, "platform owns") {
		t.Fatalf("managed prompt lacks lifecycle owner: %s", managed)
	}
	stored.ResponsePolicy = nil
	legacy := instructionFromSegments(composeDispatchInstructionSegments(dispatchInstructionInputs{Stored: stored, Present: true}))
	if !strings.Contains(legacy, "--ai-tag=false") || !strings.Contains(legacy, "remove processing/complete emotions") {
		t.Fatal("legacy prompt semantics changed")
	}
}

func TestDingTalkResponseRouteSharesTransactionAndRejectsConflictingSnapshot(t *testing.T) {
	f := newDingTalkResponseFixture(t, testRouterTargetIdentity)
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	scope := agentDispatchContext{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	if err := f.h.registerDingTalkResponseRoute(ctx, tx, f.command, scope); err != nil {
		t.Fatal(err)
	}
	if route, err := f.h.DingTalkResponses.FindRoute(ctx, f.command.CompletionCallback.URL); err != nil || route != nil {
		t.Fatalf("uncommitted route visible: %+v %v", route, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if route, err := f.h.DingTalkResponses.FindRoute(ctx, f.command.CompletionCallback.URL); err != nil || route != nil {
		t.Fatalf("rolled-back route survived: %+v %v", route, err)
	}
	f.register(t)
	f.register(t)
	for _, callback := range []string{f.command.CompletionCallback.URL, f.command.CompletionCallback.UpdateURL} {
		route, err := f.h.DingTalkResponses.FindRoute(ctx, callback)
		if err != nil || route == nil || !route.Input.ShowAITag {
			t.Fatalf("missing committed route: %+v %v", route, err)
		}
	}
	f.command.Event.Data.Conversation.OpenConversationID = "different-conversation"
	if err := f.h.registerDingTalkResponseRoute(ctx, testPool, f.command, scope); err == nil {
		t.Fatal("same callback overwrote its frozen destination")
	}
}

func TestDingTalkPrepareExecutionResultActions(t *testing.T) {
	for _, tc := range []struct {
		name, status, text, closeState, kind string
		suppress                             bool
	}{
		{"send", "completed", "Confirmed meeting time", "", "message.send", false},
		{"silent", "completed", "hidden answer", "silent", "reaction.clear", true},
		{"failed", "failed", "do not send this", "failed", "reaction.clear", false},
		{"cancelled", "cancelled", "do not send this", "cancelled", "reaction.clear", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDingTalkResponseFixture(t, testRouterTargetIdentity)
			f.register(t)
			result := agentmessagerouter.ExecutionResultRequest{RequestID: uuid.NewString(), AgentID: f.agentID, ExternalRunID: f.taskID, ExecutionStatus: tc.status, ResultMessage: tc.text}
			if tc.suppress {
				value := false
				result.ShouldReply = &value
			}
			for range 2 {
				managed, err := f.h.PrepareExecutionResult(context.Background(), f.command.CompletionCallback.URL, result)
				if !managed || err != nil {
					t.Fatalf("prepare managed=%v err=%v", managed, err)
				}
			}
			var count int
			var kind string
			var raw []byte
			if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM response_action WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("duplicate callback created %d actions", count)
			}
			if err := testPool.QueryRow(context.Background(), `SELECT kind,input FROM response_action WHERE agent_id=$1`, f.agentID).Scan(&kind, &raw); err != nil {
				t.Fatal(err)
			}
			var in dingtalkresponse.ActionInput
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Fatal(err)
			}
			if kind != tc.kind || in.CloseState != tc.closeState || (tc.closeState != "" && in.Text != "") || !in.ShowAITag {
				t.Fatalf("wrong action kind=%s input=%+v", kind, in)
			}
			result.AgentID = uuid.NewString()
			if _, err := f.h.PrepareExecutionResult(context.Background(), f.command.CompletionCallback.URL, result); err == nil {
				t.Fatal("foreign agent callback accepted")
			}
		})
	}
}

func TestDingTalkCompletionOutboxWaitsForSandboxVerdict(t *testing.T) {
	var received atomic.Int32
	var replyAllowed atomic.Bool
	var durableAtCallback atomic.Bool
	var f *dingTalkResponseFixture
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result agentmessagerouter.ExecutionResultRequest
		if json.NewDecoder(r.Body).Decode(&result) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received.Add(1)
		replyAllowed.Store(result.ShouldReply == nil || *result.ShouldReply)
		var count int
		_ = testPool.QueryRow(r.Context(), `SELECT count(*) FROM response_action WHERE agent_id=$1 AND kind='message.send'`, f.agentID).Scan(&count)
		durableAtCallback.Store(count == 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "success", "data": map[string]string{"dispatchTaskId": strings.Split(r.URL.Path, "/")[4], "executionStatus": result.ExecutionStatus, "executionReportId": "report-1"}})
	}))
	defer router.Close()
	client, err := agentmessagerouter.NewClient(agentmessagerouter.ClientConfig{BaseURL: router.URL, ServiceCredential: "test-service-credential"})
	if err != nil {
		t.Fatal(err)
	}
	f = newDingTalkResponseFixture(t, client.TargetIdentity())
	f.register(t)
	f.pendingReceipt(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `INSERT INTO task_completion_outbox(root_task_id,terminal_task_id,callback_url,target_identity,request_id,agent_id,execution_status,result_message)
		VALUES($1,$1,$2,$3,$4,$5,'completed','The reviewed schedule is ready.')`, f.taskID, f.command.CompletionCallback.URL, client.TargetIdentity(), "terminal-"+uuid.NewString(), f.agentID); err != nil {
		t.Fatal(err)
	}
	worker := agentmessagerouter.NewCompletionWorker(f.h.Queries, client, nil)
	worker.ResponseActions = f.h
	if worked, err := worker.ProcessNext(ctx); !worked || err != nil {
		t.Fatalf("process pending worked=%v error=%v", worked, err)
	}
	if received.Load() != 0 {
		t.Fatal("pending sandbox receipt allowed a duplicate callback send")
	}
	var state string
	if err := testPool.QueryRow(ctx, `SELECT status FROM task_completion_outbox WHERE agent_id=$1`, f.agentID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("pending completion not retained: %s", state)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM response_action WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("pending send manufactured a platform action")
	}
	// This transition stands in for the provider query worker's verified verdict.
	if _, err := testPool.Exec(ctx, `UPDATE sandbox_send_receipt SET state='failed' WHERE task_id=$1`, f.taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE task_completion_outbox SET available_at=now() WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.ProcessNext(ctx); !worked || err != nil {
		t.Fatalf("process verified failure worked=%v error=%v", worked, err)
	}
	if received.Load() != 1 || replyAllowed.Load() || !durableAtCallback.Load() {
		t.Fatalf("callback order/dedup failed: calls=%d reply=%v durable=%v", received.Load(), replyAllowed.Load(), durableAtCallback.Load())
	}
	if err := testPool.QueryRow(ctx, `SELECT status FROM task_completion_outbox WHERE agent_id=$1`, f.agentID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" {
		t.Fatalf("execution report not acknowledged: %s", state)
	}
}

func (f *dingTalkResponseFixture) insertCoordinatorJob(t *testing.T, command DispatchCommand, status string) string {
	t.Helper()
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO inbound_coordinator_job(id,acceptance_id,workspace_id,agent_id,user_id,endpoint_namespace_id,idempotency_key,command,chat_session_id,user_message_id,status)
		VALUES($1,$2,$3,$4,$5,$3,$6,$7,$8,$9,$10)`, id, uuid.NewString(), testWorkspaceID, f.agentID, testUserID, uuid.NewString(), raw, uuid.NewString(), uuid.NewString(), status); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRouterResponseReceiptClosesCollectedCallbacksOnlyAfterAcknowledgement(t *testing.T) {
	var mode atomic.Int32
	var calls atomic.Int32
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if mode.Load() == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var receipt protocol.DingTalkResponseReceipt
		_ = json.NewDecoder(r.Body).Decode(&receipt)
		if mode.Load() == 1 {
			receipt.ActionID = "wrong-action"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "success", "data": map[string]string{"dispatchTaskId": strings.Split(r.URL.Path, "/")[4], "actionId": receipt.ActionID, "state": receipt.State}})
	}))
	defer router.Close()
	client, err := agentmessagerouter.NewClient(agentmessagerouter.ClientConfig{BaseURL: router.URL, ServiceCredential: "test-service-credential"})
	if err != nil {
		t.Fatal(err)
	}
	f := newDingTalkResponseFixture(t, client.TargetIdentity())
	extra := responseTestCommand(f.agentID, client.TargetIdentity()).CompletionCallback
	f.command.ExtraCompletionCallbacks = []DispatchCompletionCallback{*extra}
	f.insertCoordinatorJob(t, f.command, "completed")
	sender := RouterResponseReceiptSender{Client: client, Handler: f.h}
	receipt := protocol.DingTalkResponseReceipt{RequestID: "request-1", AgentID: f.agentID, ActionID: "action-1", State: "delivered", OccurredAt: time.Now().UnixMilli()}
	ctx := context.Background()
	for _, currentMode := range []int32{0, 1, 2, 2} {
		mode.Store(currentMode)
		err := sender.SendResponseReceipt(ctx, f.command.CompletionCallback.ResponseURL, client.TargetIdentity(), receipt)
		if (err == nil) != (currentMode == 2) {
			t.Fatalf("mode=%d unexpected error=%v", currentMode, err)
		}
		var count int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM task_completion_outbox WHERE agent_id=$1 AND callback_url=$2`, f.agentID, extra.URL).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if currentMode == 2 {
			want = 1
		}
		if count != want {
			t.Fatalf("mode=%d extra callbacks=%d want=%d", currentMode, count, want)
		}
	}
	before := calls.Load()
	if err := sender.SendResponseReceipt(ctx, f.command.CompletionCallback.ResponseURL, "router-target:v1:sha256:"+strings.Repeat("f", 64), receipt); err == nil {
		t.Fatal("changed Router target accepted")
	}
	if calls.Load() != before {
		t.Fatal("target mismatch reached Router")
	}
}

type responseTestCompleter struct{ calls int }

func (c *responseTestCompleter) Chat(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	c.calls++
	return &openai.ChatCompletion{Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{
		Role: "assistant", ToolCalls: []openai.ChatCompletionMessageToolCallUnion{{
			ID: "finish", Type: "function", Function: openai.ChatCompletionMessageFunctionToolCallFunction{
				Name: "finish", Arguments: `{"action":"reply","text":"The reviewed schedule is ready for your decision.","reason":"new result"}`,
			},
		}},
	}}}}, nil
}

func TestTaskFinishedManagedPendingParksThenVerifiedFailureResumes(t *testing.T) {
	f := newDingTalkResponseFixture(t, testRouterTargetIdentity)
	ctx := context.Background()
	var payload map[string]any
	if err := json.Unmarshal(dispatchRuntimeContext(f.command, "finished"), &payload); err != nil {
		t.Fatal(err)
	}
	payload["coordinator_issue_follow_up"] = true
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	f.setContext(t, raw)
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='completed',result='{"output":"Schedule reviewed"}'::jsonb WHERE id=$1`, f.taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET task_finished_loop_enabled=true WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	f.pendingReceipt(t)
	chat := &taskFinishedCompletionProbe{reply: "The reviewed schedule is ready for your decision."}
	f.h.InboundCoordinator = &inboundcoord.Coordinator{LLM: llm.New(llm.Config{APIKey: "test", BaseURL: "http://127.0.0.1:1"}), Chat: chat}
	if err := f.h.runPersistedTaskFinishedLoop(ctx, f.taskID); !errors.Is(err, errTaskFinishedResponsePending) {
		t.Fatalf("pending should defer: %v", err)
	}
	task, err := f.h.Queries.GetAgentTask(ctx, parseUUID(f.taskID))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.h.Queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if f.h.taskFinishedSceneAlreadyTold(ctx, &task, agent, f.command.Event.Data.Conversation.OpenConversationID) {
		t.Fatal("pending receipt counted as already told")
	}
	command := f.command
	command.TaskFinishedTaskID = f.taskID
	jobID := f.insertCoordinatorJob(t, command, "pending")
	worker := NewInboundCoordinatorJobWorker(f.h)
	if worked, err := worker.ProcessNext(ctx); !worked || err != nil {
		t.Fatalf("park worked=%v err=%v", worked, err)
	}
	var status, lastError string
	var future bool
	if err := testPool.QueryRow(ctx, `SELECT status,COALESCE(last_error,''),available_at>now() FROM inbound_coordinator_job WHERE id=$1`, jobID).Scan(&status, &lastError, &future); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || lastError != "response_receipt_pending" || !future || chat.calls != 0 {
		t.Fatalf("pending was not parked: status=%s error=%s future=%v calls=%d", status, lastError, future, chat.calls)
	}
	if _, err := testPool.Exec(ctx, `UPDATE sandbox_send_receipt SET state='failed' WHERE task_id=$1`, f.taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET available_at=now() WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.ProcessNext(ctx); !worked || err != nil {
		t.Fatalf("resume worked=%v err=%v", worked, err)
	}
	if err := testPool.QueryRow(ctx, `SELECT status FROM inbound_coordinator_job WHERE id=$1`, jobID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || chat.calls != 2 {
		t.Fatalf("verified failed send lost follow-up: status=%s calls=%d", status, chat.calls)
	}
	var text string
	if err := testPool.QueryRow(ctx, `SELECT result_message FROM task_completion_outbox WHERE agent_id=$1 AND result_message<>''`, f.agentID).Scan(&text); err != nil {
		t.Fatalf("follow-up did not enter delivery outbox: %v", err)
	}
	if text != "The reviewed schedule is ready for your decision." {
		t.Fatalf("follow-up text=%q", text)
	}
}
