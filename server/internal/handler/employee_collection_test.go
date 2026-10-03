package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/taskinput"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	openai "github.com/openai/openai-go/v3"
)

// collectionTestModel scripts every foreground request of a test. respond
// sees the exact marshaled request, so tests can assert what each Loop saw.
type collectionTestModel struct {
	mu       sync.Mutex
	requests []string
	respond  func(request string) (string, map[string]any)
}

func (m *collectionTestModel) Chat(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.requests = append(m.requests, string(raw))
	respond := m.respond
	m.mu.Unlock()
	finish, message := respond(string(raw))
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": message}}})
	var out openai.ChatCompletion
	return &out, json.Unmarshal(body, &out)
}

func (m *collectionTestModel) set(respond func(string) (string, map[string]any)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.respond = respond
}

func (m *collectionTestModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *collectionTestModel) last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[len(m.requests)-1]
}

func collectionQuiet(string) (string, map[string]any) {
	return wakeToolCall("call-quiet", "stay_quiet", map[string]any{})
}

type collectionHarness struct {
	t     *testing.T
	f     *dingTalkResponseFixture
	dc    agentDispatchContext
	model *collectionTestModel
}

const collectionTestOrg = "456"

func newCollectionHarness(t *testing.T) *collectionHarness {
	t.Helper()
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1::uuid,$2::uuid,'123','456',$3::uuid)`, f.agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	ep, err := f.h.Queries.EnsureAgentDispatchEndpoint(ctx, db.EnsureAgentDispatchEndpointParams{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID), ActorUserID: parseUUID(testUserID), EndpointID: "collection-" + uuid.NewString(), DispatchUrl: "https://test.invalid/dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	dc.EndpointID, dc.EndpointNamespaceID = ep.EndpointID, ep.ID
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	model := &collectionTestModel{respond: collectionQuiet}
	f.h.EmployeeSceneWorker.model = model
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_ready_intent", "employee_task_input", "employee_task_invitation", "employee_task_collection", "employee_task_wait", "employee_host_notice", "employee_run_notice"} {
			if _, err := testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE agent_id=$1::uuid`, f.agentID); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, f.agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`, f.agentID)
	})
	return &collectionHarness{t: t, f: f, dc: dc, model: model}
}

type collectionMessage struct {
	conversation, kind, title string
	name, openID              string
	messageID, text           string
	quoted                    string
	mentions                  []DispatchMention
}

// send admits one native-shaped message through the real dispatch handler and
// returns its source_ref.
func (c *collectionHarness) send(m collectionMessage) string {
	c.t.Helper()
	command := c.f.command
	command.Event.Data.Conversation = DispatchConversation{OpenConversationID: m.conversation, Type: m.kind, Title: m.title}
	command.Event.Data.Sender = DispatchSender{DisplayName: m.name, OpenDingTalkID: m.openID}
	message := DispatchMessage{OpenMsgID: m.messageID, Text: m.text, OccurredAt: time.Now().UnixMilli(), Mentions: m.mentions}
	if m.quoted != "" {
		message.ReferencedMessage = &DispatchReferencedMessage{OpenMsgID: m.quoted}
	}
	command.Event.Data.Messages = []DispatchMessage{message}
	c.f.command = command
	if w := employeeHTTP(c.t, c.f, c.dc, uuid.NewString()); w.Code != http.StatusAccepted {
		c.t.Fatalf("admit %s: %d %s", m.messageID, w.Code, w.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(context.Background(), `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1::uuid AND payload#>>'{command,event,data,messages,0,openMsgId}'=$2`, c.f.agentID, m.messageID).Scan(&receipt); err != nil {
		c.t.Fatal(err)
	}
	return receipt + "/" + m.messageID
}

// process runs the scene worker until no job is left.
func (c *collectionHarness) process() {
	c.t.Helper()
	for range 20 {
		worked, err := c.f.h.EmployeeSceneWorker.ProcessNext(context.Background())
		if err != nil {
			c.t.Fatal(err)
		}
		if !worked {
			return
		}
	}
	c.t.Fatal("worker did not drain")
}

func (c *collectionHarness) count(query string, args ...any) int {
	c.t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		c.t.Fatal(err)
	}
	return n
}

// seed makes Carol (DM), Dave (group "B组") and Erin (group only) known.
func (c *collectionHarness) seed() {
	c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-hello", text: "你好"})
	c.send(collectionMessage{conversation: "cid-team-group", kind: "group", title: "B组", name: "Dave", openID: "dave-open", messageID: "dave-hello", text: "大家好"})
	c.send(collectionMessage{conversation: "cid-team-group", kind: "group", title: "B组", name: "Erin", openID: "erin-open", messageID: "erin-hello", text: "hi"})
	c.process()
}

func collectionCall(id, name string, args map[string]any) (string, map[string]any) {
	return wakeToolCall(id, name, args)
}

func collectionCreateArgs(source string, participants ...map[string]any) map[string]any {
	list := make([]any, len(participants))
	for i, p := range participants {
		list[i] = p
	}
	return map[string]any{"source_ref": source, "goal": "Collect this week's signed deals", "question": "本周你签了几单？", "participants": list, "reply": "好的，我去问，收齐后汇总给你。"}
}

// origin sends the requester's DM request and has the model create the
// collection; it returns the collection.
func (c *collectionHarness) origin(text string, participants ...map[string]any) taskinput.Collection {
	c.t.Helper()
	c.send(collectionMessage{conversation: "cid-origin-dm", kind: "single", name: "Requester", openID: "requester-open-id", messageID: "origin-note-" + uuid.NewString(), text: "我的私人备注 SENTINEL-ORIGIN-NOTE-55"})
	c.process()
	messageID := "origin-request-" + uuid.NewString()
	source := c.send(collectionMessage{conversation: "cid-origin-dm", kind: "single", name: "Requester", openID: "requester-open-id", messageID: messageID, text: text})
	c.model.set(func(string) (string, map[string]any) {
		return collectionCall("call-create", "create_collection", collectionCreateArgs(source, participants...))
	})
	c.process()
	c.model.set(collectionQuiet)
	var id string
	if err := testPool.QueryRow(context.Background(), `SELECT id::text FROM employee_task_collection WHERE agent_id=$1::uuid ORDER BY created_at DESC LIMIT 1`, c.f.agentID).Scan(&id); err != nil {
		var journal, outcome, lastError string
		_ = testPool.QueryRow(context.Background(), `SELECT COALESCE(j.tool_journal::text,''),COALESCE(j.outcome::text,''),COALESCE(j.last_error,'')||' '||COALESCE(c.state,'')||' '||COALESCE(c.reason,'') FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id WHERE c.agent_id=$1::uuid AND c.payload#>>'{command,event,data,messages,0,openMsgId}'=$2`, c.f.agentID, messageID).Scan(&journal, &outcome, &lastError)
		c.t.Fatalf("no collection created: %v\njournal=%s\noutcome=%s\nerr=%s", err, journal, outcome, lastError)
	}
	col, err := taskinput.NewStore(testPool).GetCollection(context.Background(), c.scope(), id)
	if err != nil {
		c.t.Fatal(err)
	}
	return col
}

func (c *collectionHarness) scope() taskinput.Scope {
	return taskinput.Scope{WorkspaceID: testWorkspaceID, AgentID: c.f.agentID, TenantOrgID: collectionTestOrg}
}

func (c *collectionHarness) invitations(collectionID string) map[string]taskinput.Invitation {
	c.t.Helper()
	list, err := taskinput.NewStore(testPool).ListInvitations(context.Background(), c.scope(), collectionID)
	if err != nil {
		c.t.Fatal(err)
	}
	out := map[string]taskinput.Invitation{}
	for _, inv := range list {
		out[inv.ParticipantLabel] = inv
	}
	return out
}

// deliver simulates the provider confirming every pending invitation send.
func (c *collectionHarness) deliver(collectionID string, conversations map[string]string) {
	c.t.Helper()
	for label, inv := range c.invitations(collectionID) {
		conversation := conversations[label]
		if _, err := testPool.Exec(context.Background(), `UPDATE response_action SET state='delivered',provider_task_id='task-'||id,provider_message_id=$2,provider_conversation_id=$3 WHERE id=$1`, inv.DeliveryActionID, "pm-"+label, conversation); err != nil {
			c.t.Fatal(err)
		}
	}
	if _, err := c.f.h.EmployeeSceneWorker.ReconcileEmployeeCollections(context.Background(), 50); err != nil {
		c.t.Fatal(err)
	}
}

func (c *collectionHarness) accept(source, ref string) func(string) (string, map[string]any) {
	return func(string) (string, map[string]any) {
		return collectionCall("call-accept", "accept_collection_input", map[string]any{"source_ref": source, "invitation_ref": ref, "reply": "收到，谢谢！"})
	}
}

func (c *collectionHarness) jobAttempts(messageID string) int {
	return c.count(`SELECT j.model_attempts FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id WHERE c.agent_id=$1::uuid AND c.payload#>>'{command,event,data,messages,0,openMsgId}'=$2`, c.f.agentID, messageID)
}

var collectionParticipants = []map[string]any{
	{"name": "Carol", "channel": "dm"},
	{"name": "Dave", "channel": "group", "group": "B组"},
	{"name": "Erin", "channel": "dm"},
}

// COL-01 shape on real PG: the origin creates a collection; invitations go
// out through the outbox with the invitation's own action id; each
// participant's Loop sees only its own question; answers bind by DM or by a
// group reply; non-reply group chatter and cards never count; the last answer
// writes one ready intent; one wake sends one summary and completes the goal.
func TestCollectionEndToEndOriginInvitationsAnswersOneSummary(t *testing.T) {
	c := newCollectionHarness(t)
	ctx := context.Background()
	c.seed()
	col := c.origin("帮我问一下 Carol、Dave 和 Erin 本周签了几单 SENTINEL-ORIGIN-REQUEST-99", collectionParticipants...)

	if col.State != taskinput.CollectionOpen || col.ExpectedCount != 3 || col.RequesterRef != "dingtalk:456:open_id:requester-open-id" {
		t.Fatalf("collection: %+v", col)
	}
	var lifecycle int
	var completion, state string
	if err := testPool.QueryRow(ctx, `SELECT lifecycle_version,completion_mode,state FROM employee_task WHERE id=$1::uuid`, col.TaskID).Scan(&lifecycle, &completion, &state); err != nil || lifecycle != 2 || completion != "explicit_goal" || state != "waiting" {
		t.Fatalf("collection Task lifecycle=%d mode=%s state=%s err=%v", lifecycle, completion, state, err)
	}
	invites := c.invitations(col.ID)
	if invites["Carol"].State != taskinput.InvitationPendingDelivery || invites["Dave"].TargetSceneKind != "group" || invites["Erin"].State != taskinput.InvitationPendingScene || invites["Erin"].TargetSceneID != "" {
		t.Fatalf("invitations: %+v", invites)
	}
	for label, inv := range invites {
		var raw []byte
		if err := testPool.QueryRow(ctx, `SELECT input FROM response_action WHERE id=$1`, inv.DeliveryActionID).Scan(&raw); err != nil {
			t.Fatalf("%s: invitation send must use the invitation action id: %v", label, err)
		}
		var in dingtalkresponse.ActionInput
		_ = json.Unmarshal(raw, &in)
		if in.InvitationActionID != inv.DeliveryActionID || !strings.Contains(in.Text, "本周你签了几单") || strings.Contains(in.Text, "SENTINEL") || in.DWSUID != "123" {
			t.Fatalf("%s invitation action: %s", label, raw)
		}
		switch label {
		case "Dave":
			if !in.IsGroup || in.SenderOpenDingTalkID != "dave-open" || in.ConversationID != "cid-team-group" {
				t.Fatalf("group invitation target: %s", raw)
			}
		case "Erin":
			if in.IsGroup || in.ConversationID != "" || in.SenderOpenDingTalkID != "erin-open" {
				t.Fatalf("pending-scene DM target: %s", raw)
			}
		}
	}
	if n := c.count(`SELECT count(*) FROM employee_host_notice WHERE agent_id=$1::uuid AND source_kind='invitation'`, c.f.agentID); n != 2 {
		t.Fatalf("history facts before delivery: %d", n)
	}

	c.deliver(col.ID, map[string]string{"Carol": "cid-carol-dm", "Dave": "cid-team-group", "Erin": "cid-erin-dm"})
	invites = c.invitations(col.ID)
	for label, inv := range invites {
		if inv.State != taskinput.InvitationDelivered || inv.ProviderMessageID != "pm-"+label || inv.TargetSceneID == "" {
			t.Fatalf("%s after delivery: %+v", label, inv)
		}
	}
	if n := c.count(`SELECT count(*) FROM employee_host_notice WHERE agent_id=$1::uuid AND source_kind='invitation' AND action_id=$2 AND scene_id=$3::uuid`, c.f.agentID, invites["Erin"].DeliveryActionID, invites["Erin"].TargetSceneID); n != 1 {
		t.Fatalf("Erin's resolved DM history fact: %d", n)
	}

	// Carol answers in her DM without quoting: the only pending invitation.
	carol := c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-answer", text: "7 单 SENTINEL-C-SEVEN"})
	c.model.set(c.accept(carol, "i1"))
	before := c.model.count()
	c.process()
	carolRequest := c.model.last()
	if c.model.count() != before+1 || !strings.Contains(carolRequest, "本周你签了几单") || strings.Contains(carolRequest, "SENTINEL-ORIGIN") || strings.Contains(carolRequest, "Dave") {
		t.Fatalf("Carol's Loop input leaks or misses her own question: %s", carolRequest)
	}

	// Dave: group chatter and a card are never answers; a quote reply is.
	c.model.set(collectionQuiet)
	before = c.model.count()
	c.send(collectionMessage{conversation: "cid-team-group", kind: "group", title: "B组", name: "Dave", openID: "dave-open", messageID: "dave-chatter", text: "我晚点再说"})
	c.send(collectionMessage{conversation: "cid-team-group", kind: "group", title: "B组", name: "Dave", openID: "dave-open", messageID: "dave-card", text: "[互动卡片]", quoted: "pm-Dave"})
	c.process()
	if c.model.count() == before {
		t.Fatal("chatter and card were not processed")
	}
	for _, request := range c.model.requests[before:] {
		if strings.Contains(request, "invitation_context") {
			t.Fatalf("chatter or card got an invitation binding: %s", request)
		}
	}
	dave := c.send(collectionMessage{conversation: "cid-team-group", kind: "group", title: "B组", name: "Dave", openID: "dave-open", messageID: "dave-answer", text: "11 单 SENTINEL-D-ELEVEN", quoted: "pm-Dave"})
	c.model.set(c.accept(dave, "i1"))
	c.process()
	daveRequest := c.model.last()
	if !strings.Contains(daveRequest, `\"bound_to_this_message\":true`) || strings.Contains(daveRequest, "SENTINEL-C") || strings.Contains(daveRequest, "SENTINEL-ORIGIN") {
		t.Fatalf("Dave's Loop input: %s", daveRequest)
	}
	if n := c.count(`SELECT count(*) FROM employee_task_ready_intent WHERE collection_id=$1::uuid`, col.ID); n != 0 {
		t.Fatalf("ready before the last answer: %d", n)
	}

	// Erin answers in the DM the provider created for her invitation.
	erin := c.send(collectionMessage{conversation: "cid-erin-dm", kind: "single", name: "Erin", openID: "erin-open", messageID: "erin-answer", text: "13"})
	c.model.set(c.accept(erin, "i1"))
	c.process()
	got, _ := taskinput.NewStore(testPool).GetCollection(ctx, c.scope(), col.ID)
	if got.State != taskinput.CollectionReady || got.ReceivedCount != 3 {
		t.Fatalf("collection after three answers: %+v", got)
	}
	for _, id := range []string{"carol-answer", "dave-answer", "erin-answer"} {
		if attempts := c.jobAttempts(id); attempts < 1 || attempts > 3 {
			t.Fatalf("%s model attempts=%d", id, attempts)
		}
	}

	// The reconciler admits one wake; it summarizes once.
	wakeCalls := 0
	c.model.set(func(request string) (string, map[string]any) {
		wakeCalls++
		if !strings.Contains(request, "SENTINEL-C-SEVEN") || !strings.Contains(request, "SENTINEL-D-ELEVEN") || !strings.Contains(request, `\"answer\":\"13\"`) {
			t.Errorf("summary wake misses authorized answers: %s", request)
		}
		return collectionCall("call-summary", "reply", map[string]any{"reply": "本周合计 31 单：Carol 7、Dave 11、Erin 13。"})
	})
	for range 3 {
		if _, err := c.f.h.EmployeeSceneWorker.ReconcileEmployeeCollections(ctx, 50); err != nil {
			t.Fatal(err)
		}
	}
	if n := c.count(`SELECT count(*) FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='task_wake'`, c.f.agentID); n != 1 {
		t.Fatalf("wake jobs: %d", n)
	}
	c.process()
	if wakeCalls != 1 || c.count(`SELECT model_attempts FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='task_wake'`, c.f.agentID) != 1 {
		t.Fatalf("summary model calls: %d", wakeCalls)
	}
	got, _ = taskinput.NewStore(testPool).GetCollection(ctx, c.scope(), col.ID)
	if got.State != taskinput.CollectionCompleted {
		t.Fatalf("collection after summary: %+v", got)
	}
	if err := testPool.QueryRow(ctx, `SELECT state FROM employee_task WHERE id=$1::uuid`, col.TaskID).Scan(&state); err != nil || state != "succeeded" {
		t.Fatalf("goal after summary: %s %v", state, err)
	}
	if n := c.count(`SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input->>'conversation_id'='cid-origin-dm' AND input->>'text' LIKE '%合计 31 单%'`, c.f.agentID); n != 1 {
		t.Fatalf("origin summaries: %d", n)
	}
	if n := c.count(`SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input->>'conversation_id'<>'cid-origin-dm' AND input->>'text' LIKE '%31%'`, c.f.agentID); n != 0 {
		t.Fatalf("summary leaked to participants: %d", n)
	}
	// Replays admit and send nothing more.
	if _, err := c.f.h.EmployeeSceneWorker.ReconcileEmployeeCollections(ctx, 50); err != nil {
		t.Fatal(err)
	}
	c.process()
	if n := c.count(`SELECT count(*) FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='task_wake'`, c.f.agentID); n != 1 || wakeCalls != 1 {
		t.Fatalf("replay created more wakes: %d calls=%d", n, wakeCalls)
	}
}

// Two final answers committed by two concurrent workers produce one ready
// intent, one wake and one summary, also with concurrent reconcilers.
func TestCollectionConcurrentFinalAnswersOneWakeOneSummary(t *testing.T) {
	c := newCollectionHarness(t)
	ctx := context.Background()
	c.seed()
	col := c.origin("问 Carol 和 Dave 本周签了几单", collectionParticipants[0], collectionParticipants[1])
	c.deliver(col.ID, map[string]string{"Carol": "cid-carol-dm", "Dave": "cid-team-group"})
	carol := c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-answer", text: "7"})
	dave := c.send(collectionMessage{conversation: "cid-team-group", kind: "group", title: "B组", name: "Dave", openID: "dave-open", messageID: "dave-answer", text: "11", quoted: "pm-Dave"})
	c.model.set(func(request string) (string, map[string]any) {
		if strings.Contains(request, carol) {
			return c.accept(carol, "i1")(request)
		}
		return c.accept(dave, "i1")(request)
	})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := c.f.h.EmployeeSceneWorker.ProcessNext(ctx)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	c.process()
	if n := c.count(`SELECT count(*) FROM employee_task_input WHERE collection_id=$1::uuid`, col.ID); n != 2 {
		t.Fatalf("inputs: %d", n)
	}
	if n := c.count(`SELECT count(*) FROM employee_task_ready_intent WHERE collection_id=$1::uuid`, col.ID); n != 1 {
		t.Fatalf("ready intents: %d", n)
	}
	c.model.set(func(string) (string, map[string]any) {
		return collectionCall("call-summary", "reply", map[string]any{"reply": "合计 18 单。"})
	})
	wg = sync.WaitGroup{}
	for range 3 {
		wg.Go(func() { _, _ = c.f.h.EmployeeSceneWorker.ReconcileEmployeeCollections(ctx, 50) })
	}
	wg.Wait()
	if n := c.count(`SELECT count(*) FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='task_wake'`, c.f.agentID); n != 1 {
		t.Fatalf("wake jobs after concurrent reconcilers: %d", n)
	}
	c.process()
	if n := c.count(`SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input->>'text' LIKE '%合计 18 单%'`, c.f.agentID); n != 1 {
		t.Fatalf("summaries: %d", n)
	}
}

// Stopping the collection Task cancels the collection in the stop
// transaction; queued invitations are suppressed before sending and a late
// answer gets no binding.
func TestCollectionStopClosesCollectionAndSuppressesSends(t *testing.T) {
	c := newCollectionHarness(t)
	ctx := context.Background()
	c.seed()
	col := c.origin("问 Carol 和 Dave 本周签了几单", collectionParticipants[0], collectionParticipants[1])
	invites := c.invitations(col.ID)
	scope := employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: c.f.agentID, TenantOrgID: collectionTestOrg, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: col.OriginSceneID}}
	task, err := employeetask.NewStore(testPool).Get(ctx, scope, col.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.f.h.TaskService.StopDirectTaskTx(ctx, tx, service.DirectTaskStopRequest{Task: task, Source: employeetask.Source{Namespace: "test", Key: "stop"}, ActorRef: task.RequesterRef, Body: "别问了", PrincipalID: parseUUID(testUserID)}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := taskinput.NewStore(testPool).GetCollection(ctx, c.scope(), col.ID)
	if got.State != taskinput.CollectionCancelled {
		t.Fatalf("collection after stop: %+v", got)
	}
	for label, inv := range c.invitations(col.ID) {
		if inv.State != taskinput.InvitationRevoked {
			t.Fatalf("%s after stop: %+v", label, inv)
		}
	}
	var raw []byte
	if err = testPool.QueryRow(ctx, `SELECT input FROM response_action WHERE id=$1`, invites["Carol"].DeliveryActionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var in dingtalkresponse.ActionInput
	_ = json.Unmarshal(raw, &in)
	in.ActionID = invites["Carol"].DeliveryActionID
	var suppressed *dingtalkresponse.SuppressSendError
	if err = c.f.h.BeforeCollectionInviteSend(nil)(ctx, in); !errors.As(err, &suppressed) || suppressed.Reason != employeeInviteSuppressClosed {
		t.Fatalf("send after stop: %v", err)
	}
	c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-late", text: "7"})
	c.process()
	if strings.Contains(c.model.last(), "invitation_context") || c.count(`SELECT count(*) FROM employee_task_input WHERE collection_id=$1::uuid`, col.ID) != 0 {
		t.Fatal("a late answer was offered or recorded after stop")
	}
}

// Every create_collection effect commits with the tool journal or not at
// all: an unknown participant, a lost outbox or a lost lease leave no Task,
// collection, wait, invitation or send behind.
func TestCollectionCreateRollsBackWithToolJournal(t *testing.T) {
	cases := map[string]func(c *collectionHarness){
		"unknown participant": func(c *collectionHarness) {},
		"outbox unavailable":  func(c *collectionHarness) { c.f.h.DingTalkResponses = nil },
		"lease lost":          func(c *collectionHarness) {},
	}
	for name, configure := range cases {
		t.Run(name, func(t *testing.T) {
			c := newCollectionHarness(t)
			c.seed()
			configure(c)
			participants := []map[string]any{collectionParticipants[0]}
			if name == "unknown participant" {
				participants = append(participants, map[string]any{"name": "Nobody", "channel": "dm"})
			}
			messageID := "origin-" + uuid.NewString()
			source := c.send(collectionMessage{conversation: "cid-origin-dm", kind: "single", name: "Requester", openID: "requester-open-id", messageID: messageID, text: "帮我问一下"})
			calls := 0
			c.model.set(func(string) (string, map[string]any) {
				calls++
				if calls > 1 {
					return collectionCall("call-reply", "reply", map[string]any{"source_ref": source, "reply": "这个人我联系不上。"})
				}
				if name == "lease lost" {
					if _, err := testPool.Exec(context.Background(), `UPDATE employee_scene_job SET lease_token=gen_random_uuid() WHERE agent_id=$1::uuid AND state='running'`, c.f.agentID); err != nil {
						t.Fatal(err)
					}
				}
				return collectionCall("call-create", "create_collection", collectionCreateArgs(source, participants...))
			})
			for range 5 {
				if worked, _ := c.f.h.EmployeeSceneWorker.ProcessNext(context.Background()); !worked {
					break
				}
			}
			for _, table := range []string{"employee_task_collection", "employee_task_invitation", "employee_task_wait"} {
				if n := c.count(`SELECT count(*) FROM `+table+` WHERE agent_id=$1::uuid`, c.f.agentID); n != 0 {
					t.Fatalf("%s rows left after a failed create: %d", table, n)
				}
			}
			if n := c.count(`SELECT count(*) FROM employee_task WHERE agent_id=$1::uuid AND lifecycle_version=2`, c.f.agentID); n != 0 {
				t.Fatalf("v2 Task left after a failed create: %d", n)
			}
			if n := c.count(`SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input->>'invitation_action_id'<>''`, c.f.agentID); n != 0 {
				t.Fatalf("invitation sends left after a failed create: %d", n)
			}
			if name != "lease lost" {
				var failure string
				if err := testPool.QueryRow(context.Background(), `SELECT COALESCE(tool_journal->'call-create'->'result'->>'failure','') FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id WHERE c.agent_id=$1::uuid AND c.payload#>>'{command,event,data,messages,0,openMsgId}'=$2`, c.f.agentID, messageID).Scan(&failure); err != nil || failure == "" {
					t.Fatalf("failure not journaled: %q %v", failure, err)
				}
			}
		})
	}
}

// The Host, not the model, decides which invitation a message can answer:
// a source outside the window, another source's invitation, an unbound group
// message and a second, conflicting record of the same source are refused.
func TestCollectionAcceptRefusesUnboundSourcesAndConflicts(t *testing.T) {
	c := newCollectionHarness(t)
	c.seed()
	col := c.origin("问 Carol 和 Dave", collectionParticipants[0], collectionParticipants[1])
	c.deliver(col.ID, map[string]string{"Carol": "cid-carol-dm", "Dave": "cid-team-group"})

	// Dave's group message without a quote gets no binding and cannot be recorded.
	dave := c.send(collectionMessage{conversation: "cid-team-group", kind: "group", title: "B组", name: "Dave", openID: "dave-open", messageID: "dave-unquoted", text: "11"})
	c.model.set(func(string) (string, map[string]any) {
		return collectionCall("call-accept", "accept_collection_input", map[string]any{"source_ref": dave, "invitation_ref": "i1", "reply": "收到"})
	})
	before := c.model.count()
	c.process()
	if c.model.count() == before || strings.Contains(c.model.requests[before], "invitation_context") {
		t.Fatal("an unquoted group message was bound to an invitation")
	}
	// A batch that records Carol's message twice with different content.
	carol := c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-answer", text: "7"})
	c.model.set(func(string) (string, map[string]any) {
		first, _ := json.Marshal(map[string]any{"source_ref": carol, "invitation_ref": "i1", "reply": "收到"})
		second, _ := json.Marshal(map[string]any{"source_ref": carol, "invitation_ref": "i1", "correction": true, "reply": "收到"})
		return "tool_calls", map[string]any{"role": "assistant", "tool_calls": []any{
			map[string]any{"id": "call-a", "type": "function", "function": map[string]any{"name": "accept_collection_input", "arguments": string(first)}},
			map[string]any{"id": "call-b", "type": "function", "function": map[string]any{"name": "accept_collection_input", "arguments": string(second)}},
		}}
	})
	c.process()
	if n := c.count(`SELECT count(*) FROM employee_task_input WHERE collection_id=$1::uuid`, col.ID); n != 1 {
		t.Fatalf("inputs after unbound and conflicting records: %d", n)
	}
	var source string
	if err := testPool.QueryRow(context.Background(), `SELECT source_key FROM employee_task_input WHERE collection_id=$1::uuid`, col.ID).Scan(&source); err != nil || source != carol {
		t.Fatalf("recorded source: %s %v", source, err)
	}
	// Carol cannot record against Dave's source from her own window.
	c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-other", text: "再补充一下"})
	c.model.set(func(string) (string, map[string]any) {
		return collectionCall("call-accept", "accept_collection_input", map[string]any{"source_ref": dave, "invitation_ref": "i1", "reply": "收到"})
	})
	c.process()
	if n := c.count(`SELECT count(*) FROM employee_task_input WHERE collection_id=$1::uuid`, col.ID); n != 1 {
		t.Fatalf("a foreign source was recorded: %d", n)
	}
}

// A group request must address this employee before it can make it contact
// people on the requester's behalf.
func TestCollectionCreateRequiresAddressedGroupSource(t *testing.T) {
	c := newCollectionHarness(t)
	c.seed()
	source := c.send(collectionMessage{conversation: "cid-origin-group", kind: "group", title: "A组", name: "Requester", openID: "requester-open-id", messageID: "group-request", text: "问一下 Carol", mentions: []DispatchMention{{UID: "someone-else"}}})
	c.model.set(func(request string) (string, map[string]any) {
		if strings.Contains(request, "does not address") {
			return collectionQuiet(request)
		}
		return collectionCall("call-create", "create_collection", collectionCreateArgs(source, collectionParticipants[0]))
	})
	c.process()
	if n := c.count(`SELECT count(*) FROM employee_task_collection WHERE agent_id=$1::uuid`, c.f.agentID); n != 0 {
		t.Fatalf("an unaddressed group message created a collection: %d", n)
	}
}

// A requester's request for reminders without a reminder service is refused
// instead of silently dropped; with one, the plan is recorded in the same
// transaction with the requester's exact words.
func TestCollectionRemindersNeedRequesterWordsAndRecorder(t *testing.T) {
	c := newCollectionHarness(t)
	c.seed()
	text := "问一下 Carol 本周签了几单，没回就每天提醒一次"
	var recorded []CollectionReminderPolicy
	c.f.h.EmployeeSceneWorker.CollectionReminders = func(ctx context.Context, tx pgx.Tx, scope taskinput.Scope, p CollectionReminderPolicy) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_task_collection WHERE id=$1::uuid)`, p.CollectionID).Scan(&exists); err != nil || !exists {
			return fmt.Errorf("collection not visible in the creating transaction: %v", err)
		}
		recorded = append(recorded, p)
		return nil
	}
	source := c.send(collectionMessage{conversation: "cid-origin-dm", kind: "single", name: "Requester", openID: "requester-open-id", messageID: "origin-remind", text: text})
	c.model.set(func(string) (string, map[string]any) {
		args := collectionCreateArgs(source, collectionParticipants[0])
		args["reminders"] = map[string]any{"instruction_quote": "没回就每天提醒一次", "max_count": 1, "interval_minutes": 1440}
		return collectionCall("call-create", "create_collection", args)
	})
	c.process()
	if len(recorded) != 1 || recorded[0].InstructionQuote != "没回就每天提醒一次" || recorded[0].RequesterRef != "dingtalk:456:open_id:requester-open-id" || recorded[0].MaxCount != 1 {
		t.Fatalf("reminder plan: %+v", recorded)
	}
}

// The participant's acknowledgement never reveals progress or totals: the
// tool result the model sees has no counts.
func TestCollectionAcceptResultHidesProgress(t *testing.T) {
	c := newCollectionHarness(t)
	c.seed()
	col := c.origin("问 Carol 和 Dave", collectionParticipants[0], collectionParticipants[1])
	c.deliver(col.ID, map[string]string{"Carol": "cid-carol-dm", "Dave": "cid-team-group"})
	carol := c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-answer", text: "7"})
	c.model.set(c.accept(carol, "i1"))
	c.process()
	var raw string
	if err := testPool.QueryRow(context.Background(), `SELECT j.tool_journal->'call-accept'->'result'->'result'->>'Content' FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id WHERE c.agent_id=$1::uuid AND c.payload#>>'{command,event,data,messages,0,openMsgId}'='carol-answer'`, c.f.agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"recorded":true`) || strings.Contains(raw, "received") || strings.Contains(raw, "expected") || strings.Contains(raw, "Dave") {
		t.Fatalf("participant tool result: %s", raw)
	}
}

var _ = employeeentry.HostNoticeInvitation

// One person asked by two collections in the same DM: an unquoted answer is
// ambiguous. The model cannot pick one without the sender's own words naming
// it; with them, the answer is recorded as an explicit invitation reference.
func TestCollectionAmbiguousDMNeedsSendersOwnReference(t *testing.T) {
	c := newCollectionHarness(t)
	c.seed()
	first := c.origin("问 Carol 本周签了几单", collectionParticipants[0])
	c.deliver(first.ID, map[string]string{"Carol": "cid-carol-dm"})
	second := c.origin("再问 Carol 下周计划拜访几家", collectionParticipants[0])
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_task_invitation SET question='下周计划拜访几家客户？' WHERE collection_id=$1::uuid`, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE response_action SET state='delivered',provider_task_id='task-'||id,provider_message_id='pm-Carol-2',provider_conversation_id='cid-carol-dm' WHERE id=$1`, c.invitations(second.ID)["Carol"].DeliveryActionID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.f.h.EmployeeSceneWorker.ReconcileEmployeeCollections(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	unclear := c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-unclear", text: "7"})
	c.model.set(c.accept(unclear, "i1"))
	before := c.model.count()
	c.process()
	if !strings.Contains(c.model.requests[before], `\"binding\":\"ambiguous\"`) {
		t.Fatalf("two pending invitations must be ambiguous: %s", c.model.requests[before])
	}
	if n := c.count(`SELECT count(*) FROM employee_task_input WHERE agent_id=$1::uuid`, c.f.agentID); n != 0 {
		t.Fatalf("an ambiguous answer was recorded without the sender naming the question: %d", n)
	}
	// 预发 COL-02: the model picked one and passed the whole message as the
	// quote. That names no question; it is refused and the model clarifies.
	guess := c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-guess", text: "COL02 3"})
	calls := 0
	c.model.set(func(string) (string, map[string]any) {
		calls++
		if calls == 1 {
			return collectionCall("call-guess", "accept_collection_input", map[string]any{"source_ref": guess, "invitation_ref": "i2", "reference_quote": "COL02 3", "reply": "收到"})
		}
		return collectionCall("call-ask", "reply", map[string]any{"source_ref": guess, "reply": "你说的 3 是哪一个问题？"})
	})
	c.process()
	if n := c.count(`SELECT count(*) FROM employee_task_input WHERE agent_id=$1::uuid`, c.f.agentID); n != 0 || calls != 2 {
		t.Fatalf("a guessed quote was recorded or ended the turn: inputs=%d calls=%d", n, calls)
	}
	if n := c.count(`SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input->>'text'='你说的 3 是哪一个问题？'`, c.f.agentID); n != 1 {
		t.Fatalf("clarification after the refusal: %d", n)
	}
	named := c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-named", text: "本周签单是 7 单"})
	c.model.set(func(request string) (string, map[string]any) {
		// i1 is the first-created collection's question in this listing.
		return collectionCall("call-accept", "accept_collection_input", map[string]any{"source_ref": named, "invitation_ref": "i1", "reference_quote": "本周签单", "reply": "收到"})
	})
	c.process()
	var binding, collection string
	if err := testPool.QueryRow(context.Background(), `SELECT binding,collection_id::text FROM employee_task_input WHERE agent_id=$1::uuid`, c.f.agentID).Scan(&binding, &collection); err != nil || binding != string(taskinput.BindInviteReference) || collection != first.ID {
		t.Fatalf("named answer: binding=%s collection=%s err=%v", binding, collection, err)
	}
}

// A summary wake whose model never produces a reply spends at most three
// requests and still delivers the authorized answers, rendered by the Host.
func TestCollectionSummaryFallsBackWithoutAnotherModelRequest(t *testing.T) {
	c := newCollectionHarness(t)
	c.seed()
	col := c.origin("问 Carol 本周签了几单", collectionParticipants[0])
	c.deliver(col.ID, map[string]string{"Carol": "cid-carol-dm"})
	carol := c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-answer", text: "7 单"})
	c.model.set(c.accept(carol, "i1"))
	c.process()
	failures := 0
	c.model.set(func(string) (string, map[string]any) {
		failures++
		return "stop", map[string]any{"role": "assistant", "content": ""}
	})
	if _, err := c.f.h.EmployeeSceneWorker.ReconcileEmployeeCollections(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	c.process()
	if attempts := c.count(`SELECT model_attempts FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='task_wake'`, c.f.agentID); attempts > 3 || failures > 3 {
		t.Fatalf("summary wake exceeded its budget: attempts=%d calls=%d", attempts, failures)
	}
	if n := c.count(`SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input->>'conversation_id'='cid-origin-dm' AND input->>'text' LIKE '%收集结果：%Carol：7 单%'`, c.f.agentID); n != 1 {
		t.Fatalf("fallback summaries: %d", n)
	}
	got, _ := taskinput.NewStore(testPool).GetCollection(context.Background(), c.scope(), col.ID)
	if got.State != taskinput.CollectionCompleted {
		t.Fatalf("collection after fallback summary: %+v", got)
	}
}

// A shortened name resolves only when exactly one known sender's display
// name contains it; two candidates are refused for clarification.
func TestCollectionParticipantPartialNameMustBeUnique(t *testing.T) {
	c := newCollectionHarness(t)
	c.send(collectionMessage{conversation: "cid-director-dm", kind: "single", name: "DingTalk-FDE Director", openID: "director-open", messageID: "director-hello", text: "你好"})
	c.send(collectionMessage{conversation: "cid-team-group", kind: "group", title: "B组", name: "Sales Lead A", openID: "lead-a-open", messageID: "lead-a-hello", text: "hi"})
	c.send(collectionMessage{conversation: "cid-team-group", kind: "group", title: "B组", name: "Sales Lead B", openID: "lead-b-open", messageID: "lead-b-hello", text: "hi"})
	c.process()
	col := c.origin("帮我问一下 Director 这周签了几单", map[string]any{"name": "Director", "channel": "dm"})
	invites := c.invitations(col.ID)
	if len(invites) != 1 || invites["DingTalk-FDE Director"].ParticipantRef != "dingtalk:456:open_id:director-open" {
		t.Fatalf("partial name: %+v", invites)
	}
	source := c.send(collectionMessage{conversation: "cid-origin-dm", kind: "single", name: "Requester", openID: "requester-open-id", messageID: "ambiguous-lead", text: "问一下 Sales Lead"})
	c.model.set(func(string) (string, map[string]any) {
		return collectionCall("call-lead", "create_collection", collectionCreateArgs(source, map[string]any{"name": "Sales Lead", "channel": "group", "group": "B组"}))
	})
	c.process()
	if n := c.count(`SELECT count(*) FROM employee_task_collection WHERE agent_id=$1::uuid`, c.f.agentID); n != 1 {
		t.Fatalf("an ambiguous partial name created a collection: %d", n)
	}
}

// A late answer after the collection closed has no invitation context, so the
// record tool is not offered and the model replies normally (预发 COL-03).
func TestCollectionLateAnswerIsNotOfferedTheRecordTool(t *testing.T) {
	c := newCollectionHarness(t)
	c.seed()
	col := c.origin("问 Carol 本周签了几单", collectionParticipants[0])
	c.deliver(col.ID, map[string]string{"Carol": "cid-carol-dm"})
	if _, _, err := taskinput.NewStore(testPool).CloseCollectionTx(context.Background(), c.scope(), taskinput.CloseParams{CollectionID: col.ID, Mode: taskinput.CloseCancel,
		Source: taskinput.Source{Namespace: "test", Key: "cancel"}, Authority: taskinput.Authority{ActorRef: col.RequesterRef, SceneID: col.OriginSceneID, ReceiptRef: "r", VerifiedAt: time.Now()}, ExpectedRevision: col.Revision}); err != nil {
		t.Fatal(err)
	}
	c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "carol-late", text: "7 单", quoted: "pm-Carol"})
	c.model.set(collectionQuiet)
	before := c.model.count()
	c.process()
	request := c.model.requests[before]
	if strings.Contains(request, `"name":"accept_collection_input"`) || strings.Contains(request, "invitation_context") {
		t.Fatal("a late answer was offered the record tool or an invitation binding")
	}
}
