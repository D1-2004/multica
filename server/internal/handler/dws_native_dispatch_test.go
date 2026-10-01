package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	nativeUnitAgent = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	nativeUnitUID   = "1001"
	nativeUnitOrg   = "2002"
)

func nativeUnitPolicy() protocol.DingTalkResponsePolicy {
	return protocol.DingTalkResponsePolicy{Version: protocol.DingTalkResponsePolicyVersion, Mode: protocol.DingTalkResponseModeCoordinator, Revision: 3, ShowAITag: true}
}

func nativeUnitMessage() *dwsevents.MessageEvent {
	return &dwsevents.MessageEvent{
		EventHeader:          dwsevents.EventHeader{Timestamp: 1783483236995},
		MessageID:            "msg-1",
		ConversationID:       "cid-1",
		Sender:               "测试用户甲",
		SenderOpenDingTalkID: "open-user-1",
		Content:              "@员工 帮我查一下昨天的日志",
		EventTime:            1783483235983,
	}
}

func nativeUnitInput(key string, m *dwsevents.MessageEvent) nativeMessageInput {
	return nativeMessageInput{AgentID: nativeUnitAgent, UID: nativeUnitUID, OrgID: nativeUnitOrg, EventKey: key, Message: m, Policy: nativeUnitPolicy()}
}

// A group @-mention becomes the managed digital-employee dispatch the Router
// would deliver: the mention of this account is trusted from the event key,
// the sender is identified by openDingTalkId only, and every callback names
// a native dispatch task.
func TestBuildNativeDispatchCommandGroupMention(t *testing.T) {
	command, err := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, nativeUnitMessage()))
	if err != nil {
		t.Fatal(err)
	}
	if err := command.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !managedDingTalkResponse(command) || !isNativeDispatchCommand(command) || commandDWSEnvironment(command) != "production" {
		t.Fatalf("managed=%v native=%v env=%q", managedDingTalkResponse(command), isNativeDispatchCommand(command), commandDWSEnvironment(command))
	}
	data := command.Event.Data
	if data.Conversation.Type != "group" || data.Conversation.OpenConversationID != "cid-1" ||
		data.Sender.OpenDingTalkID != "open-user-1" || data.Sender.UID != "" || data.Sender.DisplayName != "测试用户甲" {
		t.Fatalf("conversation/sender = %+v %+v", data.Conversation, data.Sender)
	}
	message := data.Messages[0]
	if message.OpenMsgID != "msg-1" || message.OccurredAt != 1783483235983 || message.SenderUID != "" ||
		!reflect.DeepEqual(message.Mentions, []DispatchMention{{UID: nativeUnitUID}}) {
		t.Fatalf("message = %+v", message)
	}
	if !dispatchMentionsEmployee(command) {
		t.Fatal("the @-mention of this account is not recognized")
	}
	turn := coordinatorHistoryInputs(command, parseUUID(nativeUnitAgent), unixMillis(command.Event.Data.Messages[0].OccurredAt))
	if !turn.Addressed || turn.ChatType != "group" || turn.DWSUID != nativeUnitUID || turn.PersonID == "" {
		t.Fatalf("turn = %+v", turn)
	}
	for _, callback := range []string{command.CompletionCallback.URL, command.CompletionCallback.UpdateURL, command.CompletionCallback.ResponseURL} {
		if !agentmessagerouter.IsNativeDispatchCallback(callback) {
			t.Fatalf("callback %q is not native", callback)
		}
	}
	if command.CompletionCallback.TelemetryURL != "" || command.ExternalIdentity.ContextToken != "" ||
		command.ExternalIdentity.DWS.UID != nativeUnitUID || command.ExternalIdentity.DWS.OrgID != nativeUnitOrg {
		t.Fatalf("callback/identity = %+v %+v", command.CompletionCallback, command.ExternalIdentity)
	}
}

// A single chat is always addressed to the employee; its explicit empty
// mention list is not a missing one.
func TestBuildNativeDispatchCommandSingleChat(t *testing.T) {
	command, err := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAllSingleChats, nativeUnitMessage()))
	if err != nil {
		t.Fatal(err)
	}
	if err := command.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	message := command.Event.Data.Messages[0]
	if command.Event.Data.Conversation.Type != "single" || message.Mentions == nil || len(message.Mentions) != 0 {
		t.Fatalf("conversation = %+v mentions = %#v", command.Event.Data.Conversation, message.Mentions)
	}
	turn := coordinatorHistoryInputs(command, parseUUID(nativeUnitAgent), unixMillis(message.OccurredAt))
	if turn.ChatType != "p2p" || !turn.Addressed {
		t.Fatalf("turn = %+v", turn)
	}
}

// The same message always yields the same command, so a redelivered event
// replays its acceptance instead of conflicting with it.
func TestBuildNativeDispatchCommandIsDeterministic(t *testing.T) {
	first, err := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, nativeUnitMessage()))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, nativeUnitMessage()))
	key := nativeDispatchIdempotencyKey(nativeUnitOrg, "cid-1", "msg-1")
	if dispatchRequestFingerprint(first, key) != dispatchRequestFingerprint(second, key) {
		t.Fatal("fingerprint differs between deliveries of one message")
	}
	other := nativeUnitMessage()
	other.MessageID = "msg-2"
	third, _ := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, other))
	if third.CompletionCallback.URL == first.CompletionCallback.URL || nativeDispatchIdempotencyKey(nativeUnitOrg, "cid-1", "msg-2") == key {
		t.Fatal("different messages share a dispatch task or key")
	}
	// The in-process wire round trip keeps the command the pipeline sees.
	raw, err := json.Marshal(AgentDispatchV2Request{SchemaVersion: first.SchemaVersion, AgentID: first.AgentID, Source: first.Source,
		Event: first.Event, Surface: first.Surface, Outbound: first.Outbound, ResponsePolicy: first.ResponsePolicy,
		ExternalIdentity: first.ExternalIdentity, CompletionCallback: first.CompletionCallback})
	if err != nil {
		t.Fatal(err)
	}
	var request AgentDispatchV2Request
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.DispatchCommand(), first) {
		t.Fatalf("round trip changed the command:\n%+v\n%+v", request.DispatchCommand(), first)
	}
}

// A quote names its author only when proven; otherwise it stays unknown
// instead of becoming "another person".
func TestBuildNativeDispatchCommandQuotedAuthor(t *testing.T) {
	for _, tt := range []struct {
		name      string
		quotedOwn bool
		quoted    string
		want      string
		relation  dispatchQuotedSenderRelation
	}{
		{name: "the employee's own message", quotedOwn: true, quoted: "open-employee", want: nativeUnitUID, relation: dispatchQuotedSenderSelf},
		{name: "the sender's own message", quoted: "open-user-1", want: "open-user-1", relation: dispatchQuotedSenderCurrentSender},
		{name: "someone unproven", quoted: "open-someone", want: "", relation: dispatchQuotedSenderUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := nativeUnitMessage()
			m.QuotedMessage = &dwsevents.MessageContext{MessageID: "quoted-1", SenderOpenDingTalkID: tt.quoted, Content: "原消息", Sender: "null"}
			in := nativeUnitInput(dws.EventIMAt, m)
			in.QuotedOwn = tt.quotedOwn
			command, err := buildNativeDispatchCommand(in)
			if err != nil {
				t.Fatal(err)
			}
			ref := command.Event.Data.Messages[0].ReferencedMessage
			if ref == nil || ref.OpenMsgID != "quoted-1" || ref.Text != "原消息" || ref.SenderUID != tt.want {
				t.Fatalf("referenced = %+v", ref)
			}
			if got := dispatchQuotedSenderRelationOf(ref, dispatchDisplayIdentitiesFrom(command)); got != tt.relation {
				t.Fatalf("relation = %v, want %v", got, tt.relation)
			}
		})
	}
}

func TestBuildNativeDispatchCommandSkips(t *testing.T) {
	for _, tt := range []struct {
		name   string
		key    string
		mutate func(*dwsevents.MessageEvent)
		reason nativeSkip
	}{
		{"media or empty content", dws.EventIMAt, func(m *dwsevents.MessageEvent) { m.Content = "  " }, "empty_content"},
		{"no message id", dws.EventIMAt, func(m *dwsevents.MessageEvent) { m.MessageID = "" }, "missing_message_reference"},
		{"no conversation", dws.EventIMAllSingleChats, func(m *dwsevents.MessageEvent) { m.ConversationID = "" }, "missing_message_reference"},
		{"single chat without sender", dws.EventIMAllSingleChats, func(m *dwsevents.MessageEvent) { m.SenderOpenDingTalkID = "" }, "missing_sender"},
		{"unsubscribed key", dws.EventIMAllGroups, func(*dwsevents.MessageEvent) {}, "unsupported_event_key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := nativeUnitMessage()
			tt.mutate(m)
			_, err := buildNativeDispatchCommand(nativeUnitInput(tt.key, m))
			var skipped nativeSkip
			if !errors.As(err, &skipped) || skipped != tt.reason {
				t.Fatalf("error = %v, want skip %s", err, tt.reason)
			}
		})
	}
	m := nativeUnitMessage()
	m.Sender = "null"
	command, err := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, m))
	if err != nil || command.Event.Data.Sender.DisplayName != "" || command.Event.Data.Messages[0].SenderDisplayName != "" {
		t.Fatalf("literal null sender name kept: %+v %v", command.Event.Data.Sender, err)
	}
}

// Targets are re-derived from the callback URL on every restore: native
// dispatch tasks never fall back to the Router target, and they work on a
// deployment without a Router.
func TestBindDispatchCompletionTargetSelectsNativeTarget(t *testing.T) {
	native, err := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, nativeUnitMessage()))
	if err != nil {
		t.Fatal(err)
	}
	routerTarget := "router-target:v1:sha256:" + strings.Repeat("a", 64)
	routerCallback := DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/router-task/execution-result"}
	native.ExtraCompletionCallbacks = []DispatchCompletionCallback{routerCallback}
	bound, err := bindDispatchCompletionTarget(native, routerTarget)
	if err != nil {
		t.Fatal(err)
	}
	if bound.CompletionCallback.Target != agentmessagerouter.NativeTargetIdentity() || bound.ExtraCompletionCallbacks[0].Target != routerTarget {
		t.Fatalf("targets = %q %q", bound.CompletionCallback.Target, bound.ExtraCompletionCallbacks[0].Target)
	}
	if native.ExtraCompletionCallbacks[0].Target != "" {
		t.Fatal("binding mutated the caller's callbacks")
	}
	native.ExtraCompletionCallbacks = nil
	if bound, err = bindDispatchCompletionTarget(native, ""); err != nil || bound.CompletionCallback.Target != agentmessagerouter.NativeTargetIdentity() {
		t.Fatalf("native without a Router: %+v %v", bound.CompletionCallback, err)
	}
	router := native
	router.CompletionCallback = &routerCallback
	if _, err := bindDispatchCompletionTarget(router, ""); err == nil {
		t.Fatal("Router callback bound without a Router target")
	}
	raw, err := json.Marshal(native)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restoreInboundCoordinatorCommand(raw, pgtype.UUID{}, routerTarget)
	if err != nil || restored.CompletionCallback.Target != agentmessagerouter.NativeTargetIdentity() {
		t.Fatalf("restored target = %+v %v", restored.CompletionCallback, err)
	}
}

// Native and Router deliveries complete to different targets, so a collect
// window never merges them; two native messages still merge.
func TestNativeAndRouterDeliveriesNeverShareAWindow(t *testing.T) {
	native, _ := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, nativeUnitMessage()))
	second := nativeUnitMessage()
	second.MessageID = "msg-2"
	nativeNext, _ := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, second))
	router := native
	router.CompletionCallback = &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/router-task/execution-result"}
	if !sameCoordinatorCollectKind(native, nativeNext) {
		t.Fatal("two native messages do not share a window")
	}
	if sameCoordinatorCollectKind(native, router) || sameCoordinatorCollectKind(router, native) {
		t.Fatal("native and Router deliveries share a window")
	}
}

// A wire delivery cannot claim the native dispatch namespace.
func TestAgentDispatchV2RejectsNativeNamespaceFromTheWire(t *testing.T) {
	command, err := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAllSingleChats, nativeUnitMessage()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(AgentDispatchV2Request{SchemaVersion: command.SchemaVersion, AgentID: command.AgentID, Source: command.Source,
		Event: command.Event, Surface: command.Surface, Outbound: command.Outbound, ResponsePolicy: command.ResponsePolicy,
		ExternalIdentity: command.ExternalIdentity, CompletionCallback: command.CompletionCallback})
	w := httptest.NewRecorder()
	(&Handler{}).handleAgentDispatchV2(w, httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch/k1_x", nil), raw, agentDispatchContext{})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "reserved dispatch task namespace") {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	for _, spoof := range []string{"/api/v1/dispatch-tasks/dwsn-x/execution-result", "/api/v1/dispatch-tasks/dwsn-" + strings.Repeat("A", 40) + "/execution-result"} {
		if !dispatchCommandClaimsNativeNamespace(DispatchCommand{ExtraCompletionCallbacks: []DispatchCompletionCallback{{URL: spoof}}}) {
			t.Fatalf("%s not treated as the native namespace", spoof)
		}
	}
}

func TestNativeReceiptSkipsTheRouter(t *testing.T) {
	sender := RouterResponseReceiptSender{}
	receipt := protocol.DingTalkResponseReceipt{RequestID: "r", AgentID: nativeUnitAgent, ActionID: "a", State: "delivered"}
	command, _ := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, nativeUnitMessage()))
	if err := sender.SendResponseReceipt(context.Background(), command.CompletionCallback.ResponseURL, agentmessagerouter.NativeTargetIdentity(), receipt); err != nil {
		t.Fatalf("native receipt: %v", err)
	}
	if err := sender.SendResponseReceipt(context.Background(), "/api/v1/dispatch-tasks/router-task/response-receipt", agentmessagerouter.NativeTargetIdentity(), receipt); err == nil {
		t.Fatal("native target accepted a Router callback")
	}
	if err := sender.SendResponseReceipt(context.Background(), "/api/v1/dispatch-tasks/router-task/response-receipt", "router-target:v1:sha256:"+strings.Repeat("b", 64), receipt); err == nil {
		t.Fatal("Router receipt sent without a Router client")
	}
}

// The waiting notice of a native job is pinned to production and its native
// target, like the job's replies.
func TestFreezeCoordinatorWaitDeliveryForNativeDispatch(t *testing.T) {
	command, _ := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, nativeUnitMessage()))
	command, err := bindDispatchCompletionTarget(command, "")
	if err != nil {
		t.Fatal(err)
	}
	policy := db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, DingtalkResponseEnabled: true, DingtalkResponsePolicyRevision: 3}
	scope := agentDispatchContext{WorkspaceID: parseUUID(nativeTestWorkspace), AgentID: parseUUID(nativeUnitAgent)}
	delivery := freezeCoordinatorWaitDelivery(command, scope, policy)
	if !delivery.Enabled || delivery.Input.DWSEnvironment != "production" || delivery.Input.CallbackTarget != agentmessagerouter.NativeTargetIdentity() {
		t.Fatalf("delivery = %+v", delivery)
	}
	command.CompletionCallback = &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/router-task/execution-result", Target: "router-target:v1:sha256:" + strings.Repeat("c", 64)}
	if delivery := freezeCoordinatorWaitDelivery(command, scope, policy); delivery.Input.DWSEnvironment != "" {
		t.Fatalf("Router dispatch pinned to %q", delivery.Input.DWSEnvironment)
	}
}

func TestNativeManagedResponsePolicy(t *testing.T) {
	agent := db.Agent{ID: parseUUID(nativeUnitAgent), WorkspaceID: parseUUID(nativeTestWorkspace), RuntimeID: parseUUID(nativeTestRuntime)}
	capable := []byte(`{"client_capabilities":["dws_message_policy_v1"]}`)
	for _, tt := range []struct {
		name    string
		row     db.GetAgentDingTalkResponsePolicyRow
		runtime []byte
		noRT    bool
		reason  string
	}{
		{name: "managed", row: db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, DingtalkResponseEnabled: true, DingtalkResponsePolicyRevision: 4, DingtalkShowAiTag: true}, runtime: capable},
		{name: "coordinator off", row: db.GetAgentDingTalkResponsePolicyRow{DingtalkResponseEnabled: true, DingtalkResponsePolicyRevision: 4}, runtime: capable, reason: "inbound_coordinator_off"},
		{name: "response off", row: db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, DingtalkResponsePolicyRevision: 4}, runtime: capable, reason: "dingtalk_response_off"},
		{name: "unversioned", row: db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, DingtalkResponseEnabled: true}, runtime: capable, reason: "response_policy_unversioned"},
		{name: "runtime without the wrapper", row: db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, DingtalkResponseEnabled: true, DingtalkResponsePolicyRevision: 4}, runtime: []byte(`{}`), reason: "runtime_lacks_dws_message_policy"},
		{name: "runtime gone", row: db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, DingtalkResponseEnabled: true, DingtalkResponsePolicyRevision: 4}, noRT: true, reason: "runtime_missing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeEligibilityStore{row: tt.row, runtime: db.AgentRuntime{RuntimeMode: "local", Metadata: tt.runtime}, noRuntime: tt.noRT}
			policy, reason, err := nativeManagedResponsePolicy(context.Background(), store, agent)
			if err != nil || reason != tt.reason {
				t.Fatalf("reason = %q err = %v, want %q", reason, err, tt.reason)
			}
			if reason == "" && (!policy.Managed() || policy.Revision != 4 || !policy.ShowAITag) {
				t.Fatalf("policy = %+v", policy)
			}
		})
	}
}

type fakeEligibilityStore struct {
	row       db.GetAgentDingTalkResponsePolicyRow
	runtime   db.AgentRuntime
	noRuntime bool
}

func (f *fakeEligibilityStore) GetAgentDingTalkResponsePolicy(context.Context, pgtype.UUID) (db.GetAgentDingTalkResponsePolicyRow, error) {
	return f.row, nil
}

func (f *fakeEligibilityStore) GetAgentRuntimeForWorkspace(context.Context, db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error) {
	if f.noRuntime {
		return db.AgentRuntime{}, pgx.ErrNoRows
	}
	return f.runtime, nil
}

// fakeNativeDispatchStore serves acceptNativeMessage's reads; dispatching
// reaches GetAgentDispatchEndpointForDelivery, which reports no endpoint.
type fakeNativeDispatchStore struct {
	fakeEligibilityStore
	ownerAgent    string // "" = the Router owns the account
	selfOpenID    string
	enabledAt     time.Time
	ownMessages   map[string]bool
	recentReplies map[string]bool // conversation + "\x00" + trimmed text
	learned       []string
	endpointReads int
}

func (f *fakeNativeDispatchStore) GetDWSNativeAccountOwner(_ context.Context, p db.GetDWSNativeAccountOwnerParams) (db.GetDWSNativeAccountOwnerRow, error) {
	if f.ownerAgent == "" || p.DwsUid != nativeUnitUID || p.OrgID != nativeUnitOrg {
		return db.GetDWSNativeAccountOwnerRow{}, pgx.ErrNoRows
	}
	return db.GetDWSNativeAccountOwnerRow{
		AgentID: parseUUID(f.ownerAgent), WorkspaceID: parseUUID(nativeTestWorkspace), SelfOpenDingtalkID: f.selfOpenID,
		EnabledAt: pgtype.Timestamptz{Time: f.enabledAt, Valid: !f.enabledAt.IsZero()},
	}, nil
}

func (f *fakeNativeDispatchStore) GetAgent(context.Context, pgtype.UUID) (db.Agent, error) {
	return db.Agent{ID: parseUUID(nativeUnitAgent), WorkspaceID: parseUUID(nativeTestWorkspace), RuntimeID: parseUUID(nativeTestRuntime)}, nil
}

func (f *fakeNativeDispatchStore) GetAgentDispatchEndpointForDelivery(context.Context, db.GetAgentDispatchEndpointForDeliveryParams) (db.AgentDispatchEndpoint, error) {
	f.endpointReads++
	return db.AgentDispatchEndpoint{}, pgx.ErrNoRows
}

func (f *fakeNativeDispatchStore) IsAgentOwnDingTalkMessage(_ context.Context, p db.IsAgentOwnDingTalkMessageParams) (bool, error) {
	return f.ownMessages[p.MessageID], nil
}

func (f *fakeNativeDispatchStore) IsRecentOwnReplyEcho(_ context.Context, p db.IsRecentOwnReplyEchoParams) (bool, error) {
	return f.recentReplies[p.ConversationID+"\x00"+p.Content], nil
}

func (f *fakeNativeDispatchStore) SetDWSNativeSelfOpenDingTalkID(_ context.Context, p db.SetDWSNativeSelfOpenDingTalkIDParams) error {
	f.learned = append(f.learned, p.SelfOpenDingtalkID)
	return nil
}

func nativeUnitStore() *fakeNativeDispatchStore {
	return &fakeNativeDispatchStore{
		fakeEligibilityStore: fakeEligibilityStore{
			row:     db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, DingtalkResponseEnabled: true, DingtalkResponsePolicyRevision: 2},
			runtime: db.AgentRuntime{RuntimeMode: "local", Metadata: []byte(`{"client_capabilities":["dws_message_policy_v1"]}`)},
		},
		ownerAgent: nativeUnitAgent,
	}
}

func acceptNativeUnit(t *testing.T, store *fakeNativeDispatchStore, m *dwsevents.MessageEvent) {
	t.Helper()
	acceptNativeUnitKey(t, store, m, dws.EventIMAt)
}

func acceptNativeUnitKey(t *testing.T, store *fakeNativeDispatchStore, m *dwsevents.MessageEvent, key string) {
	t.Helper()
	acceptNativeUnitAt(t, store, m, key, time.UnixMilli(nativeUnitMessage().EventTime).Add(time.Minute))
}

func acceptNativeUnitAt(t *testing.T, store *fakeNativeDispatchStore, m *dwsevents.MessageEvent, key string, now time.Time) {
	t.Helper()
	nativeDispatchLoops = newNativeLoopBreaker(nativeLoopLimit, nativeLoopWindow)
	nativeClock = func() time.Time { return now }
	t.Cleanup(func() { nativeClock = time.Now })
	h := &Handler{dwsNativeDispatch: store}
	identity := dwsclient.Identity{AgentID: nativeUnitAgent, UID: nativeUnitUID, OrgID: nativeUnitOrg}
	if err := h.acceptNativeMessage(context.Background(), identity, dwsevents.Event{ID: "ev-1", Key: key}, m); err != nil {
		t.Fatalf("error = %v, want acknowledged", err)
	}
}

// A native event is processed iff a native row owns the account; everything
// else is acknowledged without dispatching. A Router binding for the agent no
// longer matters: ownership alone decides. When the account moved to another
// agent before the stream's next sweep, the current owner handles it.
func TestAcceptNativeMessageRequiresOwnership(t *testing.T) {
	for _, tt := range []struct {
		name    string
		mutate  func(*fakeNativeDispatchStore)
		message func(*dwsevents.MessageEvent)
		reaches bool
	}{
		{name: "owned by this agent", reaches: true},
		{name: "owned by nobody (the Router)", mutate: func(f *fakeNativeDispatchStore) { f.ownerAgent = "" }},
		{name: "moved to another agent", mutate: func(f *fakeNativeDispatchStore) { f.ownerAgent = "ffffffff-ffff-4fff-8fff-ffffffffffff" }, reaches: true},
		{name: "no longer managed", mutate: func(f *fakeNativeDispatchStore) { f.row.DingtalkResponseEnabled = false }},
		{name: "empty content", message: func(m *dwsevents.MessageEvent) { m.Content = "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := nativeUnitStore()
			if tt.mutate != nil {
				tt.mutate(store)
			}
			m := nativeUnitMessage()
			if tt.message != nil {
				tt.message(m)
			}
			acceptNativeUnit(t, store, m)
			if (store.endpointReads > 0) != tt.reaches {
				t.Fatalf("endpoint reads = %d, want dispatch attempted %v", store.endpointReads, tt.reaches)
			}
		})
	}
}

// The account's own output never re-enters: a proven own message teaches
// the account's openDingTalkId, which then drops its other messages, and a
// recent managed reply echoed back is dropped by its text.
func TestAcceptNativeMessageSelfLoopGuards(t *testing.T) {
	t.Run("own message is skipped and teaches the self id", func(t *testing.T) {
		store := nativeUnitStore()
		store.ownMessages = map[string]bool{"msg-1": true}
		m := nativeUnitMessage()
		m.SenderOpenDingTalkID = "open-employee"
		acceptNativeUnit(t, store, m)
		if store.endpointReads != 0 || len(store.learned) != 1 || store.learned[0] != "open-employee" {
			t.Fatalf("reads = %d learned = %v", store.endpointReads, store.learned)
		}
	})
	t.Run("a learned self id is not relearned", func(t *testing.T) {
		store := nativeUnitStore()
		store.selfOpenID, store.ownMessages = "open-employee", map[string]bool{"msg-1": true}
		acceptNativeUnit(t, store, nativeUnitMessage())
		if len(store.learned) != 0 {
			t.Fatalf("learned = %v", store.learned)
		}
	})
	t.Run("self sender", func(t *testing.T) {
		store := nativeUnitStore()
		store.selfOpenID = "open-user-1"
		acceptNativeUnit(t, store, nativeUnitMessage())
		if store.endpointReads != 0 {
			t.Fatal("the account's own message was dispatched")
		}
	})
	t.Run("echo of a recent own reply", func(t *testing.T) {
		store := nativeUnitStore()
		m := nativeUnitMessage()
		m.Content = "  好的，这就查  \n"
		store.recentReplies = map[string]bool{"cid-1\x00好的，这就查": true}
		acceptNativeUnitKey(t, store, m, dws.EventIMAllSingleChats)
		if store.endpointReads != 0 {
			t.Fatal("an echo of the agent's own reply was dispatched")
		}
	})
	t.Run("a person repeating the reply in a group is not an echo", func(t *testing.T) {
		store := nativeUnitStore()
		m := nativeUnitMessage()
		m.Content = "好的，这就查"
		store.recentReplies = map[string]bool{"cid-1\x00好的，这就查": true}
		acceptNativeUnit(t, store, m)
		if store.endpointReads != 1 {
			t.Fatal("a group @-mention repeating the reply was dropped")
		}
	})
	t.Run("once the self id is learned another sender is never an echo", func(t *testing.T) {
		store := nativeUnitStore()
		store.selfOpenID = "open-employee"
		m := nativeUnitMessage()
		m.Content = "好的，这就查"
		store.recentReplies = map[string]bool{"cid-1\x00好的，这就查": true}
		acceptNativeUnitKey(t, store, m, dws.EventIMAllSingleChats)
		if store.endpointReads != 1 {
			t.Fatal("a person's message matching a reply was dropped after the self id was learned")
		}
	})
	t.Run("the same text elsewhere is not an echo", func(t *testing.T) {
		store := nativeUnitStore()
		store.recentReplies = map[string]bool{"cid-other\x00@员工 帮我查一下昨天的日志": true}
		acceptNativeUnit(t, store, nativeUnitMessage())
		if store.endpointReads != 1 {
			t.Fatal("a message in another conversation was taken as an echo")
		}
	})
	t.Run("a quote of a proven own message teaches the self id", func(t *testing.T) {
		store := nativeUnitStore()
		store.ownMessages = map[string]bool{"quoted-1": true}
		m := nativeUnitMessage()
		m.QuotedMessage = &dwsevents.MessageContext{MessageID: "quoted-1", SenderOpenDingTalkID: "open-employee", Content: "之前的回复"}
		acceptNativeUnit(t, store, m)
		if store.endpointReads != 1 || len(store.learned) != 1 || store.learned[0] != "open-employee" {
			t.Fatalf("reads = %d learned = %v", store.endpointReads, store.learned)
		}
	})
}

// A backlog DWS replays once a stream connects is acknowledged unanswered:
// messages sent before this agent subscribed the account, and messages older
// than nativeMessageMaxAge. A short outage's late messages are still answered.
func TestAcceptNativeMessageSkipsReplayedBacklog(t *testing.T) {
	sent := time.UnixMilli(nativeUnitMessage().EventTime)
	for _, tt := range []struct {
		name       string
		subscribed time.Time
		now        time.Time
		eventTime  int64 // 0 keeps the message's own
		reaches    bool
	}{
		{name: "fresh message", subscribed: sent.Add(-time.Hour), now: sent.Add(time.Minute), reaches: true},
		{name: "late after a short outage", subscribed: sent.Add(-time.Hour), now: sent.Add(9 * time.Minute), reaches: true},
		{name: "replayed backlog", subscribed: sent.Add(-time.Hour), now: sent.Add(21 * time.Minute)},
		{name: "sent before the subscription", subscribed: sent.Add(2 * time.Minute), now: sent.Add(3 * time.Minute)},
		{name: "subscribed just after sending (clock skew)", subscribed: sent.Add(20 * time.Second), now: sent.Add(time.Minute), reaches: true},
		{name: "no send time is never stale", eventTime: -1, now: sent.Add(time.Hour), reaches: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := nativeUnitStore()
			store.enabledAt = tt.subscribed
			m := nativeUnitMessage()
			if tt.eventTime < 0 {
				m.EventTime, m.Timestamp = 0, 0
			}
			acceptNativeUnitAt(t, store, m, dws.EventIMAllSingleChats, tt.now)
			if (store.endpointReads > 0) != tt.reaches {
				t.Fatalf("endpoint reads = %d, want dispatch attempted %v", store.endpointReads, tt.reaches)
			}
		})
	}
}

// More than six distinct native dispatches in a minute for one conversation
// is treated as a loop; redeliveries and other conversations do not count.
func TestNativeLoopBreaker(t *testing.T) {
	b := newNativeLoopBreaker(nativeLoopLimit, nativeLoopWindow)
	now := time.Now()
	for i := 0; i < nativeLoopLimit; i++ {
		if !b.admit("agent\x00cid", fmt.Sprintf("m%d", i), now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("dispatch %d refused", i)
		}
	}
	if b.admit("agent\x00cid", "m-extra", now.Add(10*time.Second)) {
		t.Fatal("seventh dispatch within a minute admitted")
	}
	if !b.admit("agent\x00cid", "m0", now.Add(11*time.Second)) {
		t.Fatal("a redelivery was counted as a new dispatch")
	}
	if !b.admit("agent\x00other", "m-extra", now.Add(12*time.Second)) {
		t.Fatal("another conversation was limited")
	}
	if !b.admit("agent\x00cid", "m-late", now.Add(nativeLoopWindow+time.Second)) {
		t.Fatal("the window did not slide")
	}

	store := nativeUnitStore()
	nativeDispatchLoops = newNativeLoopBreaker(nativeLoopLimit, nativeLoopWindow)
	h := &Handler{dwsNativeDispatch: store}
	identity := dwsclient.Identity{AgentID: nativeUnitAgent, UID: nativeUnitUID, OrgID: nativeUnitOrg}
	for i := 0; i <= nativeLoopLimit; i++ {
		m := nativeUnitMessage()
		m.MessageID = fmt.Sprintf("loop-%d", i)
		m.EventTime = time.Now().UnixMilli()
		if err := h.acceptNativeMessage(context.Background(), identity, dwsevents.Event{ID: m.MessageID, Key: dws.EventIMAt}, m); err != nil {
			t.Fatal(err)
		}
	}
	if store.endpointReads != nativeLoopLimit {
		t.Fatalf("dispatches = %d, want %d before the breaker", store.endpointReads, nativeLoopLimit)
	}
}

// The quoted author can change between deliveries of one message (its
// receipt or the self id arrives in between); the fingerprint ignores it so
// the redelivery replays instead of conflicting.
func TestNativeFingerprintIgnoresQuotedAuthor(t *testing.T) {
	m := nativeUnitMessage()
	m.QuotedMessage = &dwsevents.MessageContext{MessageID: "quoted-1", SenderOpenDingTalkID: "open-employee", Content: "之前的回复"}
	first, _ := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, m))
	in := nativeUnitInput(dws.EventIMAt, m)
	in.QuotedOwn = true
	second, _ := buildNativeDispatchCommand(in)
	key := nativeDispatchIdempotencyKey(nativeUnitOrg, "cid-1", "msg-1")
	if first.Event.Data.Messages[0].ReferencedMessage.SenderUID == second.Event.Data.Messages[0].ReferencedMessage.SenderUID {
		t.Fatal("fixture does not change the quoted author")
	}
	if dispatchRequestFingerprint(first, key) != dispatchRequestFingerprint(second, key) {
		t.Fatal("quoted author changed the native fingerprint")
	}
	if second.Event.Data.Messages[0].ReferencedMessage.SenderUID != nativeUnitUID {
		t.Fatal("fingerprinting mutated the command")
	}
	router := second
	router.CompletionCallback = &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/router-task/execution-result"}
	routerFirst := first
	routerFirst.CompletionCallback = router.CompletionCallback
	if dispatchRequestFingerprint(routerFirst, key) == dispatchRequestFingerprint(router, key) {
		t.Fatal("Router fingerprints stopped covering the quoted author")
	}
}

type fakeNativeOwnership struct {
	owner string
	err   error
}

func (f fakeNativeOwnership) GetDWSNativeAccountOwner(_ context.Context, p db.GetDWSNativeAccountOwnerParams) (db.GetDWSNativeAccountOwnerRow, error) {
	if f.err != nil {
		return db.GetDWSNativeAccountOwnerRow{}, f.err
	}
	if f.owner == "" || p.DwsUid != nativeUnitUID || p.OrgID != nativeUnitOrg {
		return db.GetDWSNativeAccountOwnerRow{}, pgx.ErrNoRows
	}
	return db.GetDWSNativeAccountOwnerRow{AgentID: parseUUID(f.owner)}, nil
}

// A Router digital-employee channel delivery for a native-owned account is
// answered silently before anything else; other deliveries pass, and an
// unresolvable owner makes the Router retry.
func TestRouterDeliveryForNativeOwnedAccountIsDropped(t *testing.T) {
	routerCommand := func() DispatchCommand {
		command, _ := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, nativeUnitMessage()))
		command.CompletionCallback = &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/router-task/execution-result"}
		return command
	}
	request := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch/k1_x", nil)
	}
	for _, tt := range []struct {
		name    string
		store   fakeNativeOwnership
		mutate  func(*DispatchCommand)
		native  bool
		dropped bool
		status  int
	}{
		{name: "native owns the account", store: fakeNativeOwnership{owner: nativeUnitAgent}, dropped: true, status: http.StatusAccepted},
		{name: "the Router owns the account", store: fakeNativeOwnership{}},
		{name: "owner unknown", store: fakeNativeOwnership{err: errors.New("database down")}, dropped: true, status: http.StatusServiceUnavailable},
		{name: "robot delivery stays with the Router", store: fakeNativeOwnership{owner: nativeUnitAgent}, mutate: func(c *DispatchCommand) { c.Source.Type = "robot" }},
		{name: "calendar stays with the Router", store: fakeNativeOwnership{owner: nativeUnitAgent}, mutate: func(c *DispatchCommand) { c.Event.Domain = "calendar" }},
		{name: "the native path itself", store: fakeNativeOwnership{owner: nativeUnitAgent}, native: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			command := routerCommand()
			if tt.mutate != nil {
				tt.mutate(&command)
			}
			r := request()
			if tt.native {
				r = r.WithContext(withNativeDispatch(r.Context()))
			}
			w := httptest.NewRecorder()
			h := &Handler{dwsNativeOwnership: tt.store}
			if dropped := h.dropNativeOwnedDelivery(w, r, command, agentDispatchContext{}); dropped != tt.dropped {
				t.Fatalf("dropped = %v, want %v", dropped, tt.dropped)
			}
			if tt.dropped && w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
		})
	}
}

// Enabling native subscription needs a running source, managed responses,
// an account no other agent holds and no Router binding routes, and a
// dispatch endpoint; the row records the account.
func TestSetDWSNativeSubscriptionPreconditions(t *testing.T) {
	path := "/api/workspaces/" + nativeTestWorkspace + "/dingtalk/account-bindings/" + nativeTestAgent + "/native-subscription"
	other := parseUUID("ffffffff-ffff-4fff-8fff-ffffffffffff")
	for _, tt := range []struct {
		name       string
		store      fakeNativeStore
		endpoints  DispatchEndpointEnsurer
		stopped    bool
		want       int
		code       string
		ensures    int
		enables    int
		noEnsurers bool
	}{
		{name: "eligible", want: 200, ensures: 1, enables: 1},
		{name: "source not running", stopped: true, want: 409, code: "native_subscription_unavailable"},
		{name: "not managed", store: fakeNativeStore{notManaged: true}, want: 409, code: "native_subscription_requires_managed_response"},
		{name: "account streamed by another agent", store: fakeNativeStore{streaming: []db.ListActiveDWSNativeSubscriptionsRow{{AgentID: other, DwsUid: "1001", OrgID: "2002"}}},
			want: 409, code: "native_subscription_account_in_use"},
		{name: "account held by another agent's row (unique index)", store: fakeNativeStore{enableErr: &pgconn.PgError{Code: "23505"}},
			want: 409, code: "native_subscription_account_in_use", ensures: 1},
		{name: "account routed by another agent's message binding", store: fakeNativeStore{accountRouted: true},
			want: 409, code: "native_subscription_conflicts_with_message_binding"},
		{name: "own row does not block", store: fakeNativeStore{streaming: []db.ListActiveDWSNativeSubscriptionsRow{{AgentID: parseUUID(nativeTestAgent), DwsUid: "1001", OrgID: "2002"}}},
			want: 200, ensures: 1, enables: 1},
		{name: "same uid in another org does not block", store: fakeNativeStore{streaming: []db.ListActiveDWSNativeSubscriptionsRow{{AgentID: other, DwsUid: "1001", OrgID: "3003"}}},
			want: 200, ensures: 1, enables: 1},
		{name: "endpoint unavailable", endpoints: &fakeDispatchEndpoints{err: errors.New("keyring")}, want: 503, code: "native_subscription_unavailable", ensures: 1},
		{name: "endpoints not configured", noEnsurers: true, want: 503, code: "native_subscription_unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			store.enabled, store.identity = map[string]bool{}, true
			h := nativeTestHandler(&store, &fakeDingTalkAccountBindingService{}, false)
			endpoints, _ := tt.endpoints.(*fakeDispatchEndpoints)
			if endpoints == nil {
				endpoints = &fakeDispatchEndpoints{}
			}
			h.DispatchEndpoints = endpoints
			if tt.noEnsurers {
				h.DispatchEndpoints = nil
			}
			if tt.stopped {
				h.nativeSourceActive = func() bool { return false }
			}
			w := httptest.NewRecorder()
			h.SetDWSNativeSubscription(w, nativeRequest(http.MethodPut, path, `{"enabled":true}`))
			if w.Code != tt.want || (tt.code != "" && errorCode(t, w) != tt.code) {
				t.Fatalf("status = %d body = %s, want %d %s", w.Code, w.Body.String(), tt.want, tt.code)
			}
			if endpoints.calls != tt.ensures || store.enables != tt.enables {
				t.Fatalf("ensures = %d enables = %d, want %d %d", endpoints.calls, store.enables, tt.ensures, tt.enables)
			}
			if tt.enables == 1 && (store.enabledParams.DwsUid != "1001" || store.enabledParams.OrgID != "2002") {
				t.Fatalf("enabled account = %+v", store.enabledParams)
			}
		})
	}
}

// The manual Router binding refuses an account native subscription owns,
// whichever agent owns it.
func TestManualMessageBindingRefusesNativeOwnedAccount(t *testing.T) {
	path := "/api/workspaces/" + nativeTestWorkspace + "/dingtalk/account-bindings/" + nativeTestAgent + "/message-route/manual"
	for _, owned := range []bool{false, true} {
		store := &fakeNativeStore{enabled: map[string]bool{}, uidOwned: owned}
		service := &fakeDirectBindingService{}
		h := nativeTestHandler(store, service, true)
		w := httptest.NewRecorder()
		h.BindDingTalkMessageRouteManually(w, nativeRequest(http.MethodPost, path, `{"corp_id":"ding8196cd9a2b2405da24f2f5cc6abecb85","uid":"1001"}`))
		if !owned {
			if w.Code != http.StatusOK || service.bindCalls != 1 {
				t.Fatalf("unowned: status = %d body = %s binds = %d", w.Code, w.Body.String(), service.bindCalls)
			}
			continue
		}
		if w.Code != http.StatusConflict || errorCode(t, w) != "message_binding_conflicts_with_native_subscription" || service.bindCalls != 0 {
			t.Fatalf("owned: status = %d body = %s binds = %d", w.Code, w.Body.String(), service.bindCalls)
		}
	}
}

// Unbinding the identity turns native subscription off first; if that fails
// the identity stays bound and the caller sees the failure.
func TestUnbindIdentityClearsNativeSubscriptionFirst(t *testing.T) {
	path := "/api/workspaces/" + nativeTestWorkspace + "/dingtalk/account-bindings/" + nativeTestAgent + "?binding_mode=identity"
	for _, failing := range []bool{false, true} {
		store := &fakeNativeStore{enabled: map[string]bool{nativeTestAgent: true}}
		if failing {
			store.disableErr = errors.New("database down")
		}
		service := &fakeDingTalkAccountBindingService{}
		h := nativeTestHandler(store, service, false)
		w := httptest.NewRecorder()
		h.UnbindDingTalkAccountBinding(w, nativeRequest(http.MethodDelete, path, ""))
		if failing {
			if w.Code != http.StatusInternalServerError || errorCode(t, w) != "native_subscription_cleanup_failed" || service.unbindCalls != 0 {
				t.Fatalf("failing cleanup: status = %d body = %s unbinds = %d", w.Code, w.Body.String(), service.unbindCalls)
			}
			continue
		}
		if service.unbindCalls != 1 || store.disables != 1 {
			t.Fatalf("status = %d body = %s unbinds = %d disables = %d", w.Code, w.Body.String(), service.unbindCalls, store.disables)
		}
	}
}
