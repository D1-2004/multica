package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
)

type observeFixture struct {
	*nativeDBFixture
	cid   string
	ready bool
}

// newObserveFixture is a native employee-mode agent whose replicas all
// support the group transcript, with no group scene yet.
func newObserveFixture(t *testing.T) *observeFixture {
	t.Helper()
	f := newNativeDBFixture(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent SET coordination_mode='employee' WHERE id=$1::uuid`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET runtime_mode='local',provider='codex',metadata='{"client_capabilities":["employee-direct-v1","dws_message_policy_v1"]}'::jsonb WHERE id=(SELECT runtime_id FROM agent WHERE id=$1::uuid)`, f.agentID); err != nil {
		t.Fatal(err)
	}
	o := &observeFixture{nativeDBFixture: f, cid: "cid-observe-" + uuid.NewString(), ready: true}
	f.h.EventRouteConfig = nil
	f.h.EmployeeLoopReady = func(context.Context, pgtype.UUID, pgtype.UUID) error { return nil }
	f.h.TaskService = &service.TaskService{Queries: f.h.Queries, TxStarter: testPool, Bus: f.h.Bus}
	f.h.EmployeeSceneWorker = NewEmployeeSceneWorker(f.h, &employeeTestModel{})
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return nil }
	f.h.EmployeeMemoryObserveReady = func(context.Context) bool { return o.ready }
	t.Cleanup(func() {
		for _, table := range []string{"employee_scene_message", "employee_event_consumption", "employee_scene_job", "scene_event_receipt", "agent_scene"} {
			if _, err := testPool.Exec(ctx, `DELETE FROM `+table+` WHERE agent_id=$1::uuid`, f.agentID); err != nil {
				t.Error(err)
			}
		}
	})
	return o
}

func (o *observeFixture) line(key, sender, content, messageID string, sentAt time.Time) []byte {
	data := fmt.Sprintf(`{"eventId":"ev-%s-%s","eventKey":%q,"occurredAtMs":%d,"subId":"sub-1","payload":{"body":{"sender":"成员%s","openMessageId":%q,"senderOpenDingTalkId":%q,"openConversationId":%q,"content":%q},"event_time":%d}}`,
		key, messageID, key, sentAt.UnixMilli(), sender, messageID, sender, o.cid, content, sentAt.UnixMilli())
	return dwsclient.EventLine(dwsevents.Event{ID: "ev-" + key + "-" + messageID, Key: key, SubscriptionID: "sub-1", Data: json.RawMessage(data)})
}

// at is an @-mention of the employee through the existing @ path.
func (o *observeFixture) at(t *testing.T, sender, content, messageID string, sentAt time.Time) {
	t.Helper()
	nativeClock = func() time.Time { return sentAt.Add(time.Second) }
	if err := o.h.HandleDWSNativeEvent(context.Background(), o.identity, o.line(dws.EventIMAt, sender, content, messageID, sentAt)); err != nil {
		t.Fatal(err)
	}
}

// observe is the same group's all-messages frame.
func (o *observeFixture) observe(t *testing.T, sender, content, messageID string, sentAt time.Time) string {
	t.Helper()
	ev, m, err := decodeNativeEvent(o.line(dws.EventIMAllGroups, sender, content, messageID, sentAt))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := o.h.observeNativeGroupMessage(context.Background(), o.identity, ev, m, sentAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

func (o *observeFixture) key(t *testing.T) employeeentry.Scope {
	t.Helper()
	var key employeeentry.Scope
	if err := testPool.QueryRow(context.Background(), `SELECT workspace_id::text,agent_id::text,tenant_org_id,id::text FROM agent_scene WHERE agent_id=$1::uuid AND external_scene_id=$2`, o.agentID, o.cid).Scan(&key.WorkspaceID, &key.AgentID, &key.TenantOrgID, &key.SceneID); err != nil {
		t.Fatal(err)
	}
	return key
}

func (o *observeFixture) rows(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := testPool.Query(context.Background(), `SELECT provider_message_id,proactive_state FROM employee_scene_message WHERE agent_id=$1::uuid`, o.agentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			t.Fatal(err)
		}
		out[id] = state
	}
	return out
}

// A group nobody addressed the employee in gets no scene, receipt or row
// from observations; once the scene exists, observations are stored as
// observation receipts and wake nothing.
func TestObservationLookupOnlyNeverMintsScene(t *testing.T) {
	o := newObserveFixture(t)
	now := time.Now().UTC()
	if outcome := o.observe(t, "user-a", "大家好，周四发版", "m-1", now); outcome != "no_scene" {
		t.Fatalf("outcome = %q", outcome)
	}
	if scenes, receipts, rows := o.count(t, "agent_scene"), o.count(t, "scene_event_receipt"), o.count(t, "employee_scene_message"); scenes+receipts+rows != 0 {
		t.Fatalf("observation minted state: scenes=%d receipts=%d rows=%d", scenes, receipts, rows)
	}
	o.at(t, "user-a", "@员工 你好", "m-2", now.Add(time.Second))
	jobs := o.count(t, "employee_scene_job")
	if jobs != 1 {
		t.Fatalf("@ path jobs = %d", jobs)
	}
	if outcome := o.observe(t, "user-b", "发版定在周四", "m-3", now.Add(2*time.Second)); outcome != "stored" {
		t.Fatalf("outcome = %q", outcome)
	}
	var category, state string
	if err := testPool.QueryRow(context.Background(), `SELECT envelope->>'category',state FROM scene_event_receipt WHERE agent_id=$1::uuid AND source LIKE 'dws-native-group/%'`, o.agentID).Scan(&category, &state); err != nil {
		t.Fatal(err)
	}
	if category != eventrouter.Observation || state != eventrouter.Ready || o.count(t, "employee_scene_job") != jobs || o.count(t, "inbound_coordinator_job") != 0 {
		t.Fatalf("observation woke work: category=%s state=%s jobs=%d", category, state, o.count(t, "employee_scene_job"))
	}
	if o.count(t, "agent_scene") != 1 {
		t.Fatal("observation registered a second scene")
	}
}

// The @-addressed message also arrives as an all-groups frame: one stored
// row, one job, a replayed frame is a duplicate, and its question never
// becomes a proactive wake.
func TestObservationDedupWithAtEvent(t *testing.T) {
	o := newObserveFixture(t)
	now := time.Now().UTC()
	o.at(t, "user-a", "@员工 发版是哪天？", "m-at", now)
	for delivery, want := range []string{"stored", "duplicate"} {
		if outcome := o.observe(t, "user-a", "@员工 发版是哪天？", "m-at", now); outcome != want {
			t.Fatalf("delivery %d outcome = %q", delivery, outcome)
		}
	}
	if rows, jobs := o.count(t, "employee_scene_message"), o.count(t, "employee_scene_job"); rows != 1 || jobs != 1 {
		t.Fatalf("rows=%d jobs=%d", rows, jobs)
	}
	if state := o.rows(t)["m-at"]; state != "" {
		t.Fatalf("@ line became a proactive candidate: %q", state)
	}
	// Even a candidate for an admitted line is skipped as addressed.
	key := o.key(t)
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_scene_message SET proactive_state='pending',proactive_due_at=$2 WHERE agent_id=$1::uuid`, o.agentID, now); err != nil {
		t.Fatal(err)
	}
	admits := 0
	worker := o.proactiveWorker(&admits, now.Add(2*time.Minute))
	if n, err := worker.RunProactiveOnce(context.Background()); err != nil || n != 1 || admits != 0 {
		t.Fatalf("decided=%d admits=%d err=%v", n, admits, err)
	}
	if reason := o.reason(t, key, "m-at"); reason != "addressed" {
		t.Fatalf("reason = %q", reason)
	}
}

func (o *observeFixture) proactiveWorker(admits *int, now time.Time) *EmployeeSceneMessageWorker {
	w := NewEmployeeSceneMessageWorker(o.h)
	w.now = func() time.Time { return now }
	w.admit = func(context.Context, employeeentry.ProactiveCandidate, db.AgentScene) (string, bool, error) {
		*admits++
		return "admitted", false, nil
	}
	return w
}

func (o *observeFixture) reason(t *testing.T, key employeeentry.Scope, messageID string) string {
	t.Helper()
	var reason string
	if err := testPool.QueryRow(context.Background(), `SELECT proactive_state||':'||proactive_reason FROM employee_scene_message WHERE scene_id=$1::uuid AND provider_message_id=$2`, key.SceneID, messageID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(strings.TrimPrefix(reason, "skipped:"), "admitted:")
}

// Self, card, sender-less and coordinator-mode lines are never stored, and
// nothing is stored before every replica supports the transcript.
func TestObservationSkipsSelfCardsSystemAndGates(t *testing.T) {
	o := newObserveFixture(t)
	now := time.Now().UTC()
	o.at(t, "user-a", "@员工 你好", "m-0", now)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_dws_native_subscription SET self_open_dingtalk_id='open-self' WHERE agent_id=$1::uuid`, o.agentID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ sender, content, want string }{
		{"open-self", "我来回答", "self_sender"},
		{"user-b", "[互动卡片]", "card_or_media"},
		{"user-b", "[卡片] 我是数字员工林黛玉，很高兴为您服务", "card_or_media"},
		{"null", "群公告已更新", "no_sender"},
	} {
		if outcome := o.observe(t, tc.sender, tc.content, "m-"+uuid.NewString()[:8], now); outcome != tc.want {
			t.Fatalf("%q: outcome = %q", tc.content, outcome)
		}
	}
	o.ready = false
	if outcome := o.observe(t, "user-b", "发版定在周四", "m-gate", now); outcome != "replicas_not_ready" {
		t.Fatalf("gate outcome = %q", outcome)
	}
	o.ready = true
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET coordination_mode='coordinator' WHERE id=$1::uuid`, o.agentID); err != nil {
		t.Fatal(err)
	}
	if outcome := o.observe(t, "user-b", "发版定在周四", "m-coord", now); outcome != "not_employee_mode" {
		t.Fatalf("coordinator outcome = %q", outcome)
	}
	if rows := o.count(t, "employee_scene_message"); rows != 0 {
		t.Fatalf("stored %d rows", rows)
	}
}

// The all-groups consumer subscribes only Employee-loop identities, and only
// once every live replica supports the transcript (so all replicas build the
// same stream fingerprint).
func TestConsumerOnlyWhenAllReplicasSupport(t *testing.T) {
	o := newObserveFixture(t)
	ctx := context.Background()
	o.ready = false
	if ids, err := o.h.EmployeeObservationIdentities(ctx); err != nil || len(ids) != 0 {
		t.Fatalf("not ready: %v %v", ids, err)
	}
	o.ready = true
	ids, err := o.h.EmployeeObservationIdentities(ctx)
	found := false
	for _, id := range ids {
		found = found || (id.AgentID == o.agentID && id.UID == o.identity.UID && id.OrgID == o.identity.OrgID)
	}
	if err != nil || !found {
		t.Fatalf("ready: %v %v", ids, err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET coordination_mode='coordinator' WHERE id=$1::uuid`, o.agentID); err != nil {
		t.Fatal(err)
	}
	ids, err = o.h.EmployeeObservationIdentities(ctx)
	for _, id := range ids {
		if id.AgentID == o.agentID {
			t.Fatalf("coordinator-mode identity subscribed: %v %v", ids, err)
		}
	}
	o.h.EmployeeMemoryObserveReady = nil
	if ids, _ := o.h.EmployeeObservationIdentities(ctx); len(ids) != 0 {
		t.Fatal("unwired readiness subscribed")
	}
}

// DS-09 context: the duty question nobody answered is admitted once; small
// talk never becomes a candidate; an answered question, a question outside
// the scene's topics, a quiet instruction and the hourly limit skip.
func TestProactiveGateDecisions(t *testing.T) {
	o := newObserveFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-30 * time.Minute)
	o.at(t, "boss", "@员工 你好", "m-hello", base)
	key := o.key(t)
	o.observe(t, "boss", "本场候选编号有 A7、B3、C9。", "ctx-1", base.Add(time.Minute))
	o.observe(t, "peer", "回执：A7 已收到；C9 已收到。", "ctx-2", base.Add(2*time.Minute))

	question := base.Add(3 * time.Minute)
	o.observe(t, "boss", "值班核对：本场候选中，漏了哪一项回执？只回编号。", "q-duty", question)
	o.observe(t, "boss", "中午吃什么？", "q-lunch", question.Add(time.Second))
	o.observe(t, "boss", "今晚的发布方案谁写？", "q-outside", question.Add(2*time.Second))
	if states := o.rows(t); states["q-duty"] != "pending" || states["q-lunch"] != "" || states["q-outside"] != "pending" || states["ctx-1"] != "" {
		t.Fatalf("candidates = %v", states)
	}
	admits := 0
	decideAt := question.Add(employeeProactiveWait + 5*time.Second)
	if n, err := o.proactiveWorker(&admits, question.Add(30*time.Second)).RunProactiveOnce(ctx); err != nil || n != 0 {
		t.Fatalf("decided before the wait: %d %v", n, err)
	}
	if n, err := o.proactiveWorker(&admits, decideAt).RunProactiveOnce(ctx); err != nil || n != 2 || admits != 1 {
		t.Fatalf("decided=%d admits=%d err=%v", n, admits, err)
	}
	if got := o.reason(t, key, "q-duty"); got != "admitted" {
		t.Fatalf("duty question = %q", got)
	}
	if got := o.reason(t, key, "q-outside"); got != "outside_duty" {
		t.Fatalf("outside question = %q", got)
	}
	if n, err := o.proactiveWorker(&admits, decideAt.Add(time.Minute)).RunProactiveOnce(ctx); err != nil || n != 0 || admits != 1 {
		t.Fatalf("admitted twice: decided=%d admits=%d err=%v", n, admits, err)
	}

	// A colleague answers within the wait.
	answered := decideAt.Add(time.Minute)
	o.observe(t, "boss", "刚才那几项候选，哪项缺回执？", "q-answered", answered)
	o.observe(t, "peer", "我核对过了，缺的是 B3。", "a-peer", answered.Add(10*time.Second))
	if n, err := o.proactiveWorker(&admits, answered.Add(employeeProactiveWait+time.Second)).RunProactiveOnce(ctx); err != nil || n != 1 || admits != 1 {
		t.Fatalf("answered: decided=%d admits=%d err=%v", n, admits, err)
	}
	if got := o.reason(t, key, "q-answered"); got != "answered" {
		t.Fatalf("answered question = %q", got)
	}

	// Quiet until told it is over.
	quiet := answered.Add(5 * time.Minute)
	o.at(t, "boss", "@员工 先别在群里说话，等我说结束。", "m-quiet", quiet)
	o.observe(t, "peer", "值班员工请核对刚才三项候选，哪一项没有回执？", "q-quiet", quiet.Add(time.Minute))
	if n, err := o.proactiveWorker(&admits, quiet.Add(time.Minute+employeeProactiveWait+time.Second)).RunProactiveOnce(ctx); err != nil || n != 1 || admits != 1 {
		t.Fatalf("quiet: decided=%d admits=%d err=%v", n, admits, err)
	}
	if got := o.reason(t, key, "q-quiet"); got != "quiet" {
		t.Fatalf("quiet question = %q", got)
	}
	ended := quiet.Add(10 * time.Minute)
	o.at(t, "boss", "@员工 安静结束了。", "m-end", ended)
	o.observe(t, "peer", "值班核对：本场候选哪一项还没有回执？", "q-after", ended.Add(time.Minute))
	if n, err := o.proactiveWorker(&admits, ended.Add(time.Minute+employeeProactiveWait+time.Second)).RunProactiveOnce(ctx); err != nil || n != 1 || admits != 2 {
		t.Fatalf("after quiet: decided=%d admits=%d err=%v", n, admits, err)
	}
}

func TestProactiveGateRateLimit(t *testing.T) {
	o := newObserveFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-50 * time.Minute)
	o.at(t, "boss", "@员工 你好", "m-hello", base)
	key := o.key(t)
	o.observe(t, "boss", "本场候选编号有 A7、B3、C9。", "ctx-1", base.Add(time.Second))
	admits := 0
	at := base.Add(time.Minute)
	for i := 0; i <= employeeProactivePerHour; i++ {
		id := fmt.Sprintf("q-%d", i)
		o.observe(t, "boss", fmt.Sprintf("第%d轮：本场候选还缺哪项？", i), id, at)
		if _, err := o.proactiveWorker(&admits, at.Add(employeeProactiveWait+time.Second)).RunProactiveOnce(ctx); err != nil {
			t.Fatal(err)
		}
		at = at.Add(3 * time.Minute)
	}
	if admits != employeeProactivePerHour {
		t.Fatalf("admits = %d", admits)
	}
	if got := o.reason(t, key, fmt.Sprintf("q-%d", employeeProactivePerHour)); got != "rate_limited" {
		t.Fatalf("last = %q", got)
	}
}

// The real admission: a proactive wake becomes a normal employee job whose
// line carries a known empty mention list, so the Loop sees it unaddressed,
// is told Quiet is correct, and a retry replays the same admission.
func TestProactiveWakeAdmitsUnaddressedEmployeeJob(t *testing.T) {
	o := newObserveFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-10 * time.Minute)
	o.at(t, "boss", "@员工 你好", "m-hello", base)
	o.observe(t, "boss", "值班核对：本场候选中，漏了哪一项回执？只回编号。", "q-duty", base.Add(time.Minute))
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := employeeentry.ClaimDueProactive(ctx, tx, base.Add(time.Hour), 10)
	_ = tx.Rollback(ctx)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %+v %v", candidates, err)
	}
	registered, err := scene.Get(ctx, o.h.Queries, scene.Owner{WorkspaceID: parseUUID(candidates[0].Key.WorkspaceID), AgentID: parseUUID(o.agentID)}, parseUUID(candidates[0].Key.SceneID))
	if err != nil {
		t.Fatal(err)
	}
	nativeClock = func() time.Time { return base.Add(3 * time.Minute) }
	for attempt := 0; attempt < 2; attempt++ {
		outcome, retry, err := o.h.admitEmployeeProactiveWake(ctx, candidates[0], registered)
		if err != nil || retry || outcome != "admitted" {
			t.Fatalf("attempt %d: %q retry=%v err=%v", attempt, outcome, retry, err)
		}
	}
	// The replayed admission adds nothing; the line joins the scene's
	// queued window (or starts one) like any other admitted message.
	if consumptions := o.count(t, "employee_event_consumption"); consumptions != 2 {
		t.Fatalf("consumptions = %d, want the @ line and one proactive line", consumptions)
	}
	var raw []byte
	var state string
	var queued bool
	if err := testPool.QueryRow(ctx, `SELECT c.payload,c.state,c.job_id IS NOT NULL FROM employee_event_consumption c JOIN scene_event_receipt r ON r.id=c.receipt_id
 WHERE c.agent_id=$1::uuid AND r.source_event_id LIKE 'dws-native-proactive:v1:%' AND r.envelope->>'category'='user_message'`, o.agentID).Scan(&raw, &state, &queued); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || !queued {
		t.Fatalf("proactive line state=%s queued=%v", state, queued)
	}
	var env employeeDispatchEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	messages := env.Command.Event.Data.Messages
	if len(messages) != 1 || messages[0].Mentions == nil || len(messages[0].Mentions) != 0 || messages[0].OpenMsgID != "q-duty" ||
		env.Command.Event.Data.Conversation.Type != "group" || employeeWindowAddressed([]employeeDispatchEnvelope{env}) {
		t.Fatalf("proactive command = %+v", env.Command.Event.Data)
	}
	if note := employeeUnaddressedWindowNote([]employeeDispatchEnvelope{env}); !strings.Contains(note, "Quiet") {
		t.Fatalf("note = %q", note)
	}
	addressed := env
	addressed.Command.Event.Data.Messages = []DispatchMessage{{Mentions: []DispatchMention{{UID: o.identity.UID}}, OpenMsgID: "x"}}
	if note := employeeUnaddressedWindowNote([]employeeDispatchEnvelope{addressed}); note != "" {
		t.Fatalf("addressed window got a note: %q", note)
	}
}

// [O] recall: older human lines sharing two topical units with the current
// message (one for a short single-topic question), excluding frozen lines,
// fenced to the scene, bounded and rendered as material.
func TestRecallMinOverlapTwo(t *testing.T) {
	o := newObserveFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-48 * time.Hour)
	o.at(t, "boss", "@员工 你好", "m-hello", base)
	key := o.key(t)
	o.observe(t, "boss", "发版定在周四", "say-release", base.Add(time.Minute))
	o.observe(t, "peer", "周四下午开会", "say-meeting", base.Add(2*time.Minute))
	for i := 0; i < 40; i++ {
		o.observe(t, "peer", fmt.Sprintf("填充消息 %d 号，今天的流水", i), fmt.Sprintf("fill-%d", i), base.Add(time.Duration(3+i)*time.Minute))
	}
	now := time.Now().UTC()
	recall, err := o.h.employeeSceneVerbatimRecall(ctx, key, scene.KindGroup, "发版哪天？", nil, now)
	if err != nil || len(recall.MessageIDs) != 1 || recall.MessageIDs[0] != "say-release" || !strings.Contains(recall.Section, "发版定在周四") ||
		!strings.Contains(recall.Section, "不是对你的请求") || len(recall.Section) > employeeRecallBytes {
		t.Fatalf("recall = %+v %v", recall, err)
	}
	if recall, _ := o.h.employeeSceneVerbatimRecall(ctx, key, scene.KindGroup, "发版哪天？", []string{"say-release"}, now); recall.Section != "" {
		t.Fatalf("frozen line recalled again: %+v", recall)
	}
	// Two topical units are required for a longer query: one shared word
	// ("周四") is not enough.
	if recall, _ := o.h.employeeSceneVerbatimRecall(ctx, key, scene.KindGroup, "周四晚上的聚餐安排", nil, now); recall.Section != "" {
		t.Fatalf("single shared word recalled: %+v", recall)
	}
	if recall, _ := o.h.employeeSceneVerbatimRecall(ctx, key, scene.KindDM, "发版哪天？", nil, now); recall.Section != "" {
		t.Fatal("recall outside a group")
	}
	// Past retention nothing is recalled.
	if recall, _ := o.h.employeeSceneVerbatimRecall(ctx, key, scene.KindGroup, "发版哪天？", nil, now.Add(15*24*time.Hour)); recall.Section != "" {
		t.Fatalf("expired line recalled: %+v", recall)
	}
	if got := employeeRecallQuery([]employeeSourceMessage{{Message: DispatchMessage{Text: "@员工 发版哪天？ https://x.example/a"}}}); got != "发版哪天？" {
		t.Fatalf("query = %q", got)
	}
	// Frozen into a new group snapshot's memory, never for lines already in
	// front of the model.
	registered, err := scene.Get(ctx, o.h.Queries, scene.Owner{WorkspaceID: parseUUID(key.WorkspaceID), AgentID: parseUUID(key.AgentID)}, parseUUID(key.SceneID))
	if err != nil {
		t.Fatal(err)
	}
	window := []employeeSourceMessage{{Message: DispatchMessage{OpenMsgID: "m-now", Text: "@员工 发版哪天？"}}}
	input := employeeSavedInput{}
	input.Input.Memory = "brief"
	o.h.appendEmployeeVerbatimRecall(ctx, &input, employeeentry.Job{Scope: key, CreatedAt: now}, registered, window)
	if !strings.HasPrefix(input.Input.Memory, "brief\n[O] ") || !strings.Contains(input.Input.Memory, "发版定在周四") {
		t.Fatalf("memory = %q", input.Input.Memory)
	}
	input.Input.Memory = "brief"
	input.Input.RecentConversation = `{"messages":[{"role":"user","text":"发版定在周四","message_id":"say-release"}]}`
	o.h.appendEmployeeVerbatimRecall(ctx, &input, employeeentry.Job{Scope: key, CreatedAt: now}, registered, window)
	if input.Input.Memory != "brief" {
		t.Fatalf("recent line recalled again: %q", input.Input.Memory)
	}
}

// The wake-time read by-product stores human lines only and is idempotent
// with the native observation of the same message.
func TestWakeReadByProductStoresHumanLines(t *testing.T) {
	o := newObserveFixture(t)
	now := time.Now().UTC()
	o.at(t, "boss", "@员工 你好", "m-hello", now.Add(-time.Hour))
	key := o.key(t)
	o.h.recordEmployeeSceneHistory(context.Background(), key, []employeeentry.SceneMessageInput{
		{ProviderMessageID: "w-1", SentAt: now.Add(-50 * time.Minute), SenderClass: employeeentry.SceneSenderHuman, SenderRef: "dingtalk:2002:open_id:boss", SenderName: "主管", Body: "发版定在周四"},
		{ProviderMessageID: "w-2", SentAt: now.Add(-49 * time.Minute), SenderClass: employeeentry.SceneSenderBot, Body: "我是机器人"},
		{ProviderMessageID: "w-3", SentAt: now.Add(-48 * time.Minute), SenderClass: employeeentry.SceneSenderSelf, Body: "好的"},
	})
	if states := o.rows(t); len(states) != 1 {
		t.Fatalf("rows = %v", states)
	}
	if outcome := o.observe(t, "boss", "发版定在周四", "w-1", now.Add(-50*time.Minute)); outcome != "stored" {
		t.Fatalf("outcome = %q", outcome)
	}
	var receipt pgtype.UUID
	if err := testPool.QueryRow(context.Background(), `SELECT receipt_id FROM employee_scene_message WHERE agent_id=$1::uuid AND provider_message_id='w-1'`, o.agentID).Scan(&receipt); err != nil || !receipt.Valid {
		t.Fatalf("receipt not attached: %v %v", receipt, err)
	}
	if rows := o.count(t, "employee_scene_message"); rows != 1 {
		t.Fatalf("rows = %d", rows)
	}
}
