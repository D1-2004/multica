package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type nativeDBFixture struct {
	h        *Handler
	agentID  string
	identity dwsclient.Identity
}

var nativeTestRouterTarget = "router-target:v1:sha256:" + strings.Repeat("e", 64)

// newNativeDBFixture creates an agent eligible for native subscription: a
// runtime with the managed DWS wrapper, the Coordinator and DingTalk
// response on, a bound identity, native subscription on for that account
// and a dispatch endpoint. The handler also has a Router target, so Router
// deliveries for the same account can be compared.
func newNativeDBFixture(t *testing.T) *nativeDBFixture {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "native-runtime-"+uuid.NewString())
	agentID, _ := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "native-agent-"+uuid.NewString())
	uid := fmt.Sprint(100000 + uuid.New().ID()%900000)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE agent_runtime SET metadata='{"client_capabilities":["dws_message_policy_v1"]}'::jsonb WHERE id=$1`, []any{runtimeID}},
		{`UPDATE agent SET inbound_coordinator=true, dingtalk_response_enabled=true, dingtalk_response_policy_revision=2, dingtalk_show_ai_tag=true WHERE id=$1`, []any{agentID}},
		{`INSERT INTO agent_dingtalk_identity (agent_id, workspace_id, dws_uid, org_id, bound_by) VALUES ($1,$2,$3,'2002',$4)`, []any{agentID, testWorkspaceID, uid, testUserID}},
		{`INSERT INTO agent_dws_native_subscription (agent_id, workspace_id, enabled_by, dws_uid, org_id) VALUES ($1,$2,$3,$4,'2002')`, []any{agentID, testWorkspaceID, testUserID, uid}},
		{`INSERT INTO agent_dispatch_endpoint (workspace_id, agent_id, actor_user_id, endpoint_id, dispatch_url) VALUES ($1,$2,$3,$4,'/api/webhooks/agent-dispatch/native')`, []any{testWorkspaceID, agentID, testUserID, "k1_native" + uuid.NewString()[:8]}},
	} {
		if _, err := testPool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("setup %q: %v", stmt.sql, err)
		}
	}
	t.Cleanup(func() {
		for _, table := range []string{"response_action", "response_route", "task_completion_outbox", "task_execution_update_outbox",
			"inbound_coordinator_job", "agent_dispatch_acceptance", "agent_dispatch_endpoint", "agent_dingtalk_identity", "agent_dws_native_subscription",
			"channel_installation"} {
			if _, err := testPool.Exec(ctx, "DELETE FROM "+table+" WHERE agent_id=$1", agentID); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
		_, _ = testPool.Exec(ctx, `DELETE FROM chat_message WHERE chat_session_id IN (SELECT id FROM chat_session WHERE agent_id=$1)`, agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM chat_session WHERE agent_id=$1`, agentID)
	})
	h := *testHandler
	h.DingTalkResponses = dingtalkresponse.NewService(testPool, nil, nil)
	h.TaskCompletionTargetIdentity = nativeTestRouterTarget
	h.InboundCoordinator = nil
	h.SceneMemoryStore = nil
	h.EventTriggers = nil
	h.InboundCoordinatorWorker = NewInboundCoordinatorJobWorker(&h)
	return &nativeDBFixture{h: &h, agentID: agentID, identity: dwsclient.Identity{AgentID: agentID, UID: uid, OrgID: "2002"}}
}

// nativeTestEventLine is one IM event as the event source hands it over.
func nativeTestEventLine(key, conversationID, messageID, quotedID string) []byte {
	return nativeTestEventLineFrom("open-user-1", "帮我查一下昨天的日志", key, conversationID, messageID, quotedID)
}

func nativeTestEventLineFrom(sender, content, key, conversationID, messageID, quotedID string) []byte {
	quoted := ""
	if quotedID != "" {
		quoted = fmt.Sprintf(`,"quotedMessage":{"openMessageId":%q,"senderOpenDingTalkId":"open-employee","openConversationId":%q,"content":"之前的回复","sender":"null"}`, quotedID, conversationID)
	}
	data := fmt.Sprintf(`{"eventId":"ev-%s","eventKey":%q,"occurredAtMs":1783483236995,"subId":"sub-1","payload":{"body":{"createTime":"2026-07-08 12:00:35","sender":"测试用户甲","openMessageId":%q,"senderOpenDingTalkId":%q,"openConversationId":%q,"content":%q%s},"event_time":1783483235983}}`,
		messageID, key, messageID, sender, conversationID, content, quoted)
	return dwsclient.EventLine(dwsevents.Event{ID: "ev-" + messageID, Key: key, SubscriptionID: "sub-1", Data: json.RawMessage(data)})
}

func (f *nativeDBFixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" WHERE agent_id=$1", f.agentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A native @-mention is accepted through the Router pipeline under the
// native target: one acceptance and Coordinator job, managed routes pinned
// to production, a redelivery that replays instead of duplicating, and a
// reply that goes to the managed outbox without any Router.
func TestNativeMessageRunsTheRouterPipeline(t *testing.T) {
	f := newNativeDBFixture(t)
	ctx := context.Background()
	cid := "cid-native-" + uuid.NewString()
	line := nativeTestEventLine(dws.EventIMAt, cid, "msg-1", "")
	for delivery := 1; delivery <= 2; delivery++ {
		if err := f.h.HandleDWSNativeEvent(ctx, f.identity, line); err != nil {
			t.Fatalf("delivery %d: %v", delivery, err)
		}
	}
	if acceptances, jobs := f.count(t, "agent_dispatch_acceptance"), f.count(t, "inbound_coordinator_job"); acceptances != 1 || jobs != 1 {
		t.Fatalf("acceptances = %d jobs = %d, want one of each after a redelivery", acceptances, jobs)
	}
	var target, status string
	if err := testPool.QueryRow(ctx, `SELECT target_identity, status FROM agent_dispatch_acceptance WHERE agent_id=$1`, f.agentID).Scan(&target, &status); err != nil {
		t.Fatal(err)
	}
	if target != agentmessagerouter.NativeTargetIdentity() || status != "accepted" {
		t.Fatalf("acceptance target = %q status = %q", target, status)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT command FROM inbound_coordinator_job WHERE agent_id=$1`, f.agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var job struct {
		DispatchCommand
		Wait coordinatorWaitDelivery `json:"_coordinator_wait_delivery"`
	}
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	command := job.DispatchCommand
	if !isNativeDispatchCommand(command) || !managedDingTalkResponse(command) || command.Event.Data.Conversation.Type != "group" {
		t.Fatalf("job command = %+v", command)
	}
	if !job.Wait.Enabled || job.Wait.Input.DWSEnvironment != "production" || job.Wait.Input.CallbackTarget != agentmessagerouter.NativeTargetIdentity() {
		t.Fatalf("wait delivery = %+v", job.Wait)
	}
	restored, err := restoreInboundCoordinatorCommand(raw, parseUUID(uuid.NewString()), "router-target:v1:sha256:"+fmt.Sprintf("%064d", 0))
	if err != nil || restored.CompletionCallback.Target != agentmessagerouter.NativeTargetIdentity() {
		t.Fatalf("restored target = %+v %v", restored.CompletionCallback, err)
	}
	for _, callback := range []string{command.CompletionCallback.URL, command.CompletionCallback.UpdateURL} {
		route, err := f.h.DingTalkResponses.FindRoute(ctx, callback)
		if err != nil || route == nil {
			t.Fatalf("route for %s: %+v %v", callback, route, err)
		}
		in := route.Input
		if in.DWSEnvironment != "production" || in.CallbackTarget != agentmessagerouter.NativeTargetIdentity() ||
			in.ReplyToOpenMsgID != "msg-1" || !in.IsGroup || in.SenderOpenDingTalkID != "open-user-1" ||
			in.DWSUID != f.identity.UID || in.DWSOrgID != "2002" || in.CallbackURL != command.CompletionCallback.ResponseURL {
			t.Fatalf("route input = %+v", in)
		}
	}

	// The work receipt a Coordinator job enqueues drains through the native
	// worker into the managed outbox, pinned to production.
	if err := f.h.TaskService.EnqueueSynchronousWorkReceipt(ctx, command.CompletionCallback.URL, agentmessagerouter.NativeTargetIdentity(), parseUUID(f.agentID), "收到，我来查"); err != nil {
		t.Fatal(err)
	}
	worker := agentmessagerouter.NewCompletionWorker(f.h.Queries, agentmessagerouter.NewNativeCallbackClient(func(ctx context.Context, callback string) (bool, error) {
		route, err := f.h.DingTalkResponses.FindRoute(ctx, callback)
		return route != nil, err
	}), nil)
	worker.ResponseActions = f.h
	if worked, err := worker.ProcessNext(ctx); !worked || err != nil {
		t.Fatalf("native worker worked=%v err=%v", worked, err)
	}
	var completion string
	if err := testPool.QueryRow(ctx, `SELECT status FROM task_completion_outbox WHERE agent_id=$1`, f.agentID).Scan(&completion); err != nil {
		t.Fatal(err)
	}
	var actionInput []byte
	if err := testPool.QueryRow(ctx, `SELECT input FROM response_action WHERE agent_id=$1 AND kind='message.send'`, f.agentID).Scan(&actionInput); err != nil {
		t.Fatalf("managed response action: %v", err)
	}
	var action dingtalkresponse.ActionInput
	if err := json.Unmarshal(actionInput, &action); err != nil {
		t.Fatal(err)
	}
	if completion != "delivered" || action.Text != "收到，我来查" || action.DWSEnvironment != "production" || action.ReplyToOpenMsgID != "msg-1" {
		t.Fatalf("completion = %q action = %+v", completion, action)
	}

	// The receipt of a native response closes the window without a Router.
	receipt := protocol.DingTalkResponseReceipt{RequestID: action.RequestID, AgentID: f.agentID, ActionID: "action-1", State: "delivered"}
	if err := (RouterResponseReceiptSender{Handler: f.h}).SendResponseReceipt(ctx, command.CompletionCallback.ResponseURL, agentmessagerouter.NativeTargetIdentity(), receipt); err != nil {
		t.Fatalf("native receipt: %v", err)
	}
}

// The employee's own messages never enter the pipeline, and a quote of one
// is attributed to the employee itself.
func TestNativeMessageOwnOutputAndQuotes(t *testing.T) {
	f := newNativeDBFixture(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `INSERT INTO response_action (id, workspace_id, agent_id, request_id, kind, input, state, provider_message_id)
		VALUES ($1,$2,$3,'request-own','message.send','{}'::jsonb,'delivered','own-msg')`, uuid.NewString(), testWorkspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}
	cid := "cid-native-dm-" + uuid.NewString()
	// DWS hands the account's own message back: it is dropped and teaches
	// the account's openDingTalkId.
	if err := f.h.HandleDWSNativeEvent(ctx, f.identity, nativeTestEventLineFrom("open-employee", "我来查", dws.EventIMAllSingleChats, cid, "own-msg", "")); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, "agent_dispatch_acceptance"); n != 0 {
		t.Fatalf("own message accepted %d times", n)
	}
	var self string
	if err := testPool.QueryRow(ctx, `SELECT self_open_dingtalk_id FROM agent_dws_native_subscription WHERE agent_id=$1`, f.agentID).Scan(&self); err != nil || self != "open-employee" {
		t.Fatalf("learned self id = %q err = %v", self, err)
	}
	// Any later message from that openDingTalkId is the account's own.
	if err := f.h.HandleDWSNativeEvent(ctx, f.identity, nativeTestEventLineFrom("open-employee", "另一条", dws.EventIMAllSingleChats, cid, "own-unrecorded", "")); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, "agent_dispatch_acceptance"); n != 0 {
		t.Fatalf("self sender accepted %d times", n)
	}
	if err := f.h.HandleDWSNativeEvent(ctx, f.identity, nativeTestEventLine(dws.EventIMAllSingleChats, cid, "reply-msg", "own-msg")); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT command FROM inbound_coordinator_job WHERE agent_id=$1`, f.agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var command DispatchCommand
	if err := json.Unmarshal(raw, &command); err != nil {
		t.Fatal(err)
	}
	ref := command.Event.Data.Messages[0].ReferencedMessage
	if command.Event.Data.Conversation.Type != "single" || ref == nil || ref.OpenMsgID != "own-msg" || ref.SenderUID != f.identity.UID {
		t.Fatalf("quote = %+v conversation = %+v", ref, command.Event.Data.Conversation)
	}
	// A stream still dialled for a previous account is never spoken for.
	stale := f.identity
	stale.UID = "999"
	if err := f.h.HandleDWSNativeEvent(ctx, stale, nativeTestEventLine(dws.EventIMAllSingleChats, cid, "stale-msg", "")); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, "agent_dispatch_acceptance"); n != 1 {
		t.Fatalf("acceptances = %d after a stale-account event, want 1", n)
	}
}

// routerDelivery posts the Router digital-employee delivery of a message for
// the fixture's account over the webhook path and returns its status.
func (f *nativeDBFixture) routerDelivery(t *testing.T, conversationID, messageID string) int {
	t.Helper()
	ctx := context.Background()
	command, err := buildNativeDispatchCommand(nativeMessageInput{
		AgentID: f.agentID, UID: f.identity.UID, OrgID: f.identity.OrgID, EventKey: dws.EventIMAt,
		Message: &dwsevents.MessageEvent{MessageID: messageID, ConversationID: conversationID, Sender: "测试用户甲",
			SenderOpenDingTalkID: "open-user-1", Content: "帮我查一下昨天的日志", EventTime: 1783483235983},
		Policy: protocol.DingTalkResponsePolicy{Version: protocol.DingTalkResponsePolicyVersion, Mode: protocol.DingTalkResponseModeCoordinator, Revision: 2, ShowAITag: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/dispatch-tasks/router-" + messageID + "-" + uuid.NewString()[:8]
	command.CompletionCallback = &DispatchCompletionCallback{URL: base + "/execution-result", UpdateURL: base + "/execution-update", ResponseURL: base + "/response-receipt"}
	raw, err := json.Marshal(AgentDispatchV2Request{SchemaVersion: command.SchemaVersion, AgentID: command.AgentID, Source: command.Source,
		Event: command.Event, Surface: command.Surface, Outbound: command.Outbound, ResponsePolicy: command.ResponsePolicy,
		ExternalIdentity: command.ExternalIdentity, CompletionCallback: command.CompletionCallback})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := f.h.Queries.GetAgentDispatchEndpointForDelivery(ctx, db.GetAgentDispatchEndpointForDeliveryParams{AgentID: parseUUID(f.agentID), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch/"+endpoint.EndpointID, nil)
	req.Header.Set("Idempotency-Key", "router-"+messageID)
	w := httptest.NewRecorder()
	f.h.handleAgentDispatchV2(w, req, raw, agentDispatchContext{EndpointID: endpoint.EndpointID, EndpointNamespaceID: endpoint.ID,
		UserID: endpoint.ActorUserID, WorkspaceID: endpoint.WorkspaceID, AgentID: endpoint.AgentID})
	return w.Code
}

func (f *nativeDBFixture) acceptances(t *testing.T, target string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_dispatch_acceptance WHERE agent_id=$1 AND target_identity=$2`, f.agentID, target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *nativeDBFixture) silences(t *testing.T) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM task_completion_outbox WHERE agent_id=$1 AND request_id LIKE 'multica-terminal:sync-silence:router-%'`, f.agentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *nativeDBFixture) native(t *testing.T, conversationID, messageID string) {
	t.Helper()
	if err := f.h.HandleDWSNativeEvent(context.Background(), f.identity, nativeTestEventLine(dws.EventIMAt, conversationID, messageID, "")); err != nil {
		t.Fatal(err)
	}
}

// One owner per account and message. While native owns the account the
// Router's delivery of a message is closed silently and only the native
// event is processed, even with a Router message binding for the account in
// place; once native is switched off, the Router's deliveries are processed
// and native events are dropped.
func TestNativeOwnershipIsExclusivePerMessage(t *testing.T) {
	f := newNativeDBFixture(t)
	ctx := context.Background()
	native := agentmessagerouter.NativeTargetIdentity()
	cid := "cid-owner-" + uuid.NewString()

	// (a) Native owns: the same message from both paths is processed once.
	if status := f.routerDelivery(t, cid, "m1"); status != http.StatusAccepted {
		t.Fatalf("Router delivery status = %d", status)
	}
	f.native(t, cid, "m1")
	if r, n, s := f.acceptances(t, nativeTestRouterTarget), f.acceptances(t, native), f.silences(t); r != 0 || n != 1 || s != 1 {
		t.Fatalf("owned by native: Router acceptances = %d native = %d silent closes = %d", r, n, s)
	}
	if jobs := f.count(t, "inbound_coordinator_job"); jobs != 1 {
		t.Fatalf("jobs = %d, want only the native one", jobs)
	}

	// (c) A Router binding for the account slipped in: ownership still decides.
	if _, err := testPool.Exec(ctx, `INSERT INTO channel_installation (workspace_id, agent_id, channel_type, config, status, installer_user_id)
		VALUES ($1,$2,'dingtalk_account',jsonb_build_object('router_tenant_id','2002','router_account_id',$3::text),'active',$4)`,
		testWorkspaceID, f.agentID, f.identity.UID, testUserID); err != nil {
		t.Fatal(err)
	}
	routed, err := f.h.Queries.HasActiveDingTalkMessageRouteForAccount(ctx, db.HasActiveDingTalkMessageRouteForAccountParams{OrgID: "2002", DwsUid: f.identity.UID})
	if err != nil || !routed {
		t.Fatalf("account route = %v err = %v", routed, err)
	}
	streams, err := f.h.Queries.ListActiveDWSNativeSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, row := range streams {
		listed = listed || util.UUIDToString(row.AgentID) == f.agentID
	}
	if !listed {
		t.Fatal("a Router binding took the account's native stream away")
	}
	f.routerDelivery(t, cid, "m2")
	f.native(t, cid, "m2")
	if r, n := f.acceptances(t, nativeTestRouterTarget), f.acceptances(t, native); r != 0 || n != 2 {
		t.Fatalf("binding race: Router acceptances = %d native = %d", r, n)
	}

	// (b) Switching ownership: with native off the Router owns the account.
	if _, err := testPool.Exec(ctx, `DELETE FROM agent_dws_native_subscription WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	other := "cid-router-" + uuid.NewString()
	if status := f.routerDelivery(t, other, "m3"); status < 200 || status >= 300 {
		t.Fatalf("Router delivery after switch-off status = %d", status)
	}
	f.native(t, other, "m3")
	if r, n := f.acceptances(t, nativeTestRouterTarget), f.acceptances(t, native); r != 1 || n != 2 {
		t.Fatalf("owned by the Router: Router acceptances = %d native = %d", r, n)
	}

	// Switching back: a rebound identity does not own the account until
	// native is enabled again for it.
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dws_native_subscription (agent_id, workspace_id, enabled_by, dws_uid, org_id) VALUES ($1,$2,$3,'424242','2002')`,
		f.agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	f.routerDelivery(t, other, "m4")
	if r := f.acceptances(t, nativeTestRouterTarget); r != 2 {
		t.Fatalf("a row for another account took this one: Router acceptances = %d", r)
	}
	if _, err := f.h.Queries.EnableAgentDWSNativeSubscription(ctx, db.EnableAgentDWSNativeSubscriptionParams{
		AgentID: parseUUID(f.agentID), WorkspaceID: parseUUID(testWorkspaceID), EnabledBy: parseUUID(testUserID), DwsUid: f.identity.UID, OrgID: "2002",
	}); err != nil {
		t.Fatal(err)
	}
	f.routerDelivery(t, other, "m5")
	f.native(t, other, "m5")
	if r, n := f.acceptances(t, nativeTestRouterTarget), f.acceptances(t, native); r != 2 || n != 3 {
		t.Fatalf("re-enabled: Router acceptances = %d native = %d", r, n)
	}
}

// The database holds one native row per account, and re-enabling after a
// rebind moves the row and forgets the old account's learned self id.
func TestNativeSubscriptionAccountIsUnique(t *testing.T) {
	f := newNativeDBFixture(t)
	ctx := context.Background()
	second := newNativeDBFixture(t)
	_, err := second.h.Queries.EnableAgentDWSNativeSubscription(ctx, db.EnableAgentDWSNativeSubscriptionParams{
		AgentID: parseUUID(second.agentID), WorkspaceID: parseUUID(testWorkspaceID), EnabledBy: parseUUID(testUserID), DwsUid: f.identity.UID, OrgID: "2002",
	})
	if !isUniqueViolation(err) {
		t.Fatalf("a second agent enabled the same account: err = %v", err)
	}
	if err := f.h.Queries.SetDWSNativeSelfOpenDingTalkID(ctx, db.SetDWSNativeSelfOpenDingTalkIDParams{
		SelfOpenDingtalkID: "open-employee", AgentID: parseUUID(f.agentID), OrgID: "2002", DwsUid: f.identity.UID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Queries.SetDWSNativeSelfOpenDingTalkID(ctx, db.SetDWSNativeSelfOpenDingTalkIDParams{
		SelfOpenDingtalkID: "open-overwrite", AgentID: parseUUID(f.agentID), OrgID: "2002", DwsUid: f.identity.UID,
	}); err != nil {
		t.Fatal(err)
	}
	row, err := f.h.Queries.EnableAgentDWSNativeSubscription(ctx, db.EnableAgentDWSNativeSubscriptionParams{
		AgentID: parseUUID(f.agentID), WorkspaceID: parseUUID(testWorkspaceID), EnabledBy: parseUUID(testUserID), DwsUid: f.identity.UID, OrgID: "2002",
	})
	if err != nil || row.SelfOpenDingtalkID != "open-employee" {
		t.Fatalf("re-enable same account: %+v %v", row, err)
	}
	row, err = f.h.Queries.EnableAgentDWSNativeSubscription(ctx, db.EnableAgentDWSNativeSubscriptionParams{
		AgentID: parseUUID(f.agentID), WorkspaceID: parseUUID(testWorkspaceID), EnabledBy: parseUUID(testUserID), DwsUid: "777777", OrgID: "2002",
	})
	if err != nil || row.DwsUid != "777777" || row.SelfOpenDingtalkID != "" {
		t.Fatalf("re-enable for a rebound account: %+v %v", row, err)
	}
	if _, owned, err := nativeAccountOwner(ctx, f.h.Queries, f.identity.UID, "2002"); err != nil || owned {
		t.Fatalf("old account still owned: %v %v", owned, err)
	}
	if _, owned, err := nativeAccountOwner(ctx, f.h.Queries, "777777", "2002"); err != nil || owned {
		t.Fatalf("an account the agent's identity is not bound to is owned: %v %v", owned, err)
	}
}

// A managed reply the agent sent to the conversation in the last ten minutes
// and echoed back by DWS is not taken as inbound; an older one is.
func TestNativeEchoOfOwnReplyIsDropped(t *testing.T) {
	f := newNativeDBFixture(t)
	ctx := context.Background()
	cid := "cid-echo-" + uuid.NewString()
	insert := func(age string) {
		t.Helper()
		if _, err := testPool.Exec(ctx, `INSERT INTO response_action (id, workspace_id, agent_id, request_id, kind, input, state, created_at)
			VALUES ($1,$2,$3,'request-echo','message.send',jsonb_build_object('conversation_id',$4::text,'text',E'好的，我来查\n'),'delivered',now() - $5::interval)`,
			uuid.NewString(), testWorkspaceID, f.agentID, cid, age); err != nil {
			t.Fatal(err)
		}
	}
	insert("11 minutes")
	if err := f.h.HandleDWSNativeEvent(ctx, f.identity, nativeTestEventLineFrom("open-user-1", " 好的，我来查 ", dws.EventIMAllSingleChats, cid, "old-echo", "")); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, "agent_dispatch_acceptance"); n != 1 {
		t.Fatalf("a match older than the window was dropped: acceptances = %d", n)
	}
	insert("1 minute")
	if err := f.h.HandleDWSNativeEvent(ctx, f.identity, nativeTestEventLineFrom("open-user-1", " 好的，我来查 ", dws.EventIMAllSingleChats, cid, "fresh-echo", "")); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, "agent_dispatch_acceptance"); n != 1 {
		t.Fatalf("echo of a recent own reply accepted: acceptances = %d", n)
	}
	if err := f.h.HandleDWSNativeEvent(ctx, f.identity, nativeTestEventLineFrom("open-user-1", "好的，我来查 then more", dws.EventIMAllSingleChats, cid, "not-echo", "")); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, "agent_dispatch_acceptance"); n != 2 {
		t.Fatalf("a different message was dropped as an echo: acceptances = %d", n)
	}
}
