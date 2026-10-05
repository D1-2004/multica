package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/taskinput"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	reminderParticipantC = "dingtalk:direct-org:staff-c"
	reminderParticipantD = "dingtalk:direct-org:staff-d"
	reminderParticipantE = "dingtalk:direct-org:staff-e"
	reminderDWSUID       = "dws-reminder"
)

type reminderScene struct {
	id, external, kind string
}

// collectionFixture is a lifecycle v2 goal in the fixture's origin scene that
// waits on a real taskinput collection with three invitations (two 1:1, one
// group). The fixture's v1 Direct run is finished so only the goal is watched.
type collectionFixture struct {
	*watchdogFixture
	inputs    *taskinput.Store
	scope     taskinput.Scope
	goal      employeetask.Task
	requester string
	principal string
	scenes    map[string]reminderScene
	col       taskinput.Collection
	invs      []taskinput.Invitation
	start     time.Time
}

func newCollectionFixture(t *testing.T, specs ...func(*taskinput.InvitationSpec)) *collectionFixture {
	t.Helper()
	f := newWatchdogFixture(t)
	c := &collectionFixture{watchdogFixture: f, inputs: taskinput.NewStore(f.pool), scope: taskinput.Scope{WorkspaceID: f.ws, AgentID: f.agent, TenantOrgID: f.task.Scope.TenantOrgID}, scenes: map[string]reminderScene{}}
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM employee_task_invitation_reminder WHERE workspace_id=$1::uuid`, `DELETE FROM employee_task_invitation_reminder_policy WHERE workspace_id=$1::uuid`,
			`DELETE FROM employee_task_input WHERE workspace_id=$1::uuid`, `DELETE FROM employee_task_ready_intent WHERE workspace_id=$1::uuid`, `DELETE FROM employee_task_invitation WHERE workspace_id=$1::uuid`,
			`DELETE FROM employee_task_collection WHERE workspace_id=$1::uuid`, `DELETE FROM employee_task_wait WHERE workspace_id=$1::uuid`, `DELETE FROM employee_host_notice WHERE workspace_id=$1::uuid`,
			`DELETE FROM agent_dingtalk_identity WHERE workspace_id=$1::uuid`} {
			if _, err := f.pool.Exec(context.Background(), q, f.ws); err != nil {
				t.Error(err)
			}
		}
	})
	// Retire the fixture's v1 run: this fixture watches the goal only.
	f.exec(`UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1::uuid`, f.queueID)
	f.exec(`UPDATE employee_task_run SET state='succeeded',finished_at=now() WHERE id=$1::uuid`, f.runID)
	f.exec(`UPDATE employee_task SET state='ready',active_run_id=NULL WHERE id=$1::uuid`, f.task.ID)
	f.exec(`INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid)`, f.agent, f.ws, reminderDWSUID, f.task.Scope.TenantOrgID, util.UUIDToString(f.request.PrincipalID))
	c.principal = util.UUIDToString(f.request.PrincipalID)
	for name, kind := range map[string]string{"dmC": scene.KindDM, "dmD": scene.KindDM, "group": scene.KindGroup, "dmE": scene.KindDM} {
		c.scenes[name] = c.resolveScene(kind)
	}
	goal, err := employeetask.NewStore(f.pool).Create(f.ctx, employeetask.CreateParams{Scope: f.task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect,
		RequesterRef: f.task.RequesterRef, Definition: employeetask.Definition{Goal: "收集三位同事的数字并汇总"}, Source: employeetask.Source{Namespace: "test", Key: "goal-" + uuid.NewString()},
		Input: "收集三位同事的数字并汇总", Lifecycle: employeetask.LifecycleV2, CompletionMode: employeetask.CompletionExplicitGoal})
	if err != nil {
		t.Fatal(err)
	}
	c.goal, c.requester = goal, goal.RequesterRef
	all := []taskinput.InvitationSpec{
		{TargetSceneID: c.scenes["dmC"].id, ParticipantRef: reminderParticipantC, Question: "C 同学，请回复你本周的数字。"},
		{TargetSceneID: c.scenes["dmD"].id, ParticipantRef: reminderParticipantD, Question: "D 同学，请回复你本周的数字。"},
		{TargetSceneID: c.scenes["group"].id, ParticipantRef: reminderParticipantE, Question: "E 同学，请在群里回复你本周的数字。"},
	}
	for i, apply := range specs {
		apply(&all[i])
	}
	c.col, c.invs = c.create(t, goal, all, nil)
	return c
}

func (c *collectionFixture) resolveScene(kind string) reminderScene {
	c.t.Helper()
	ws, _ := util.ParseUUID(c.ws)
	agent, _ := util.ParseUUID(c.agent)
	external := "cid-reminder-" + uuid.NewString()
	row, err := scene.Resolve(c.ctx, db.New(c.pool), scene.Owner{WorkspaceID: ws, AgentID: agent}, scene.DingTalkConversation(c.task.Scope.TenantOrgID, kind, external), scene.Observation{KindStated: true})
	if err != nil {
		c.t.Fatal(err)
	}
	return reminderScene{id: scene.RefOf(row).SceneID, external: external, kind: kind}
}

// create runs CreateCollectionTx in an outer transaction; inTx, when set, runs
// in that same transaction (for example to record reminder authorization).
func (c *collectionFixture) create(t *testing.T, goal employeetask.Task, specs []taskinput.InvitationSpec, inTx func(pgx.Tx, []taskinput.Invitation)) (taskinput.Collection, []taskinput.Invitation) {
	t.Helper()
	tx, err := c.pool.Begin(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(c.ctx)
	col, invs, err := taskinput.NewStore(tx).CreateCollectionTx(c.ctx, c.scope, taskinput.CreateCollectionParams{TaskID: goal.ID, OriginSceneID: goal.Scope.Scene.SceneID, AuthorityRef: "task-authority/" + goal.ID,
		RequesterRef: goal.RequesterRef, DeliveryAnchorRef: "origin-message/1", GoalRevision: goal.GoalRevision, Invitations: specs,
		Source: taskinput.Source{Namespace: "employee.tool", Key: "create_collection/" + uuid.NewString()}, Authority: taskinput.Authority{ActorRef: goal.RequesterRef, SceneID: goal.Scope.Scene.SceneID, ReceiptRef: "receipt/origin/" + uuid.NewString(), VerifiedAt: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if inTx != nil {
		inTx(tx, invs)
	}
	if _, _, err := employeetask.WaitTaskTx(c.ctx, tx, goal.Scope, goal.ID, employeetask.WaitParams{Source: employeetask.Source{Namespace: "employee.tool", Key: "wait/" + col.ID}, Kind: employeetask.WaitCollection, RefID: col.ID, Mandatory: true, AuthorityRef: "task-authority/" + goal.ID}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(c.ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.pool.QueryRow(c.ctx, `SELECT created_at FROM employee_task_wait WHERE task_id=$1::uuid AND ref_id=$2`, goal.ID, col.ID).Scan(&c.start); err != nil {
		t.Fatal(err)
	}
	return col, invs
}

func (c *collectionFixture) sceneOf(inv taskinput.Invitation) reminderScene {
	for _, s := range c.scenes {
		if s.id == inv.TargetSceneID {
			return s
		}
	}
	c.t.Fatalf("unknown target scene %s", inv.TargetSceneID)
	return reminderScene{}
}

// deliver records the invitation send the way the A2 contract does: a
// response action with the invitation's delivery action id (already
// delivered), its host notice, and the provider fact.
func (c *collectionFixture) deliver(t *testing.T, inv taskinput.Invitation) taskinput.Invitation {
	t.Helper()
	target := c.sceneOf(inv)
	in := dingtalkresponse.ActionInput{WorkspaceID: c.ws, AgentID: c.agent, RequestID: "invitation:" + inv.ID, DWSUID: reminderDWSUID, DWSOrgID: c.scope.TenantOrgID, SceneID: inv.TargetSceneID,
		ConversationID: target.external, IsGroup: target.kind == scene.KindGroup, Text: inv.Question, CallbackTarget: "invitation"}
	if target.kind == scene.KindDM {
		in.SenderOpenDingTalkID = "odt-" + inv.ParticipantRef
	}
	raw, _ := json.Marshal(in)
	c.exec(`INSERT INTO response_action(id,workspace_id,agent_id,request_id,kind,input,state,provider_message_id) VALUES($1,$2::uuid,$3::uuid,$4,'message.send',$5,'delivered',$6)`, inv.DeliveryActionID, c.ws, c.agent, in.RequestID, raw, "msg-"+inv.ID)
	if err := employeeentry.RecordHostNotice(c.ctx, c.pool, employeeentry.HostNotice{ActionID: inv.DeliveryActionID, Scope: employeeentry.Scope{WorkspaceID: c.ws, AgentID: c.agent, TenantOrgID: c.scope.TenantOrgID, SceneID: inv.TargetSceneID},
		PrincipalID: c.principal, SourceKind: employeeentry.HostNoticeInvitation, SourceID: inv.ID}); err != nil {
		t.Fatal(err)
	}
	audience, _ := taskinput.AudienceForSceneKind(target.kind)
	hash := taskinput.CheckEgress(taskinput.EgressInput{Rendered: inv.Question, Audience: audience, TargetSceneKind: target.kind}).RenderedHash
	out, err := c.inputs.RecordInviteDeliveryTx(c.ctx, c.scope, taskinput.RecordDeliveryParams{InvitationID: inv.ID, ActionID: inv.DeliveryActionID, Outcome: taskinput.DeliverySent, ProviderMessageID: "msg-" + inv.ID, RenderedHash: hash})
	if err != nil || out.State != taskinput.InvitationDelivered {
		t.Fatal(out.State, err)
	}
	return out
}

func (c *collectionFixture) deliverAll(t *testing.T) {
	for i := range c.invs {
		c.invs[i] = c.deliver(t, c.invs[i])
	}
	if err := c.pool.QueryRow(c.ctx, `SELECT max(delivered_at) FROM employee_task_invitation WHERE collection_id=$1::uuid`, c.col.ID).Scan(&c.start); err != nil {
		t.Fatal(err)
	}
}

func (c *collectionFixture) answer(t *testing.T, inv taskinput.Invitation, body string) {
	t.Helper()
	binding := taskinput.BindDMSinglePending
	if inv.TargetSceneKind == scene.KindGroup {
		binding = taskinput.BindReplyChain
	}
	key := "answer-" + uuid.NewString()
	for range 8 {
		col, err := c.inputs.GetCollection(c.ctx, c.scope, c.col.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.inputs.AcceptInputTx(c.ctx, c.scope, taskinput.AcceptInputParams{CollectionID: c.col.ID, InvitationID: inv.ID, Source: taskinput.Source{Namespace: "dingtalk.message", Key: key},
			Authority:  taskinput.Authority{ActorRef: inv.ParticipantRef, SceneID: inv.TargetSceneID, ReceiptRef: "receipt/" + key, VerifiedAt: time.Now()},
			SenderKind: taskinput.SenderPerson, MessageKind: taskinput.MessageText, Binding: binding, ProviderMessageID: key, OccurredAt: time.Now().UTC(), Body: body, ExpectedRevision: col.Revision})
		if errors.Is(err, taskinput.ErrStaleRevision) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("answer: revision kept moving")
}

func (c *collectionFixture) clockAt(d time.Duration) { c.clock.Set(c.start.Add(d)) }

// --- C2: wait facts ----------------------------------------------------------

func TestWatchdogWaitReaderCountsCollectionWithoutAnswers(t *testing.T) {
	c := newCollectionFixture(t)
	c.deliverAll(t)
	c.answer(t, c.invs[0], "SENTINEL-C-ANSWER-7")
	var w EmployeeTaskWait
	c.withTx(t, func(tx pgx.Tx) {
		goal, err := employeetask.NewStore(tx).Get(c.ctx, c.goal.Scope, c.goal.ID)
		if err != nil || goal.State != employeetask.StateWaiting {
			t.Fatal("goal is not waiting", goal.State, err)
		}
		var ok bool
		w, ok, err = EmployeeTaskWaitFacts{}.ReadEmployeeTaskWait(c.ctx, tx, goal)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
	})
	if w.Kind != EmployeeTaskWaitInputs || w.Expected != 3 || w.Received != 1 || w.Progress.Source != EmployeeActivityTaskInput || w.Progress.IsZero() || w.Key == "" {
		t.Fatalf("wait=%+v", w)
	}
	// The watchdog uses the PostgreSQL facts by default (no Waits override).
	c.exec(`UPDATE employee_task_input SET created_at=$2 WHERE collection_id=$1::uuid`, c.col.ID, c.start.Add(time.Minute))
	c.clockAt(30 * time.Minute)
	if got := c.scan(nil); got.Opened != 0 {
		t.Fatal("input wait aged before its threshold", got)
	}
	c.clockAt(62 * time.Minute)
	if got := c.scan(nil); got.Opened != 1 || got.Enqueued != 1 {
		t.Fatal("long input wait did not open", got)
	}
	n := onlyNotice(t, c.notices(c.goal.ID))
	if n.Kind != "waiting_inputs" || !strings.Contains(n.Body, "1/3") || strings.Contains(n.Body, "SENTINEL") || strings.Contains(n.Body, "还在执行") {
		t.Fatalf("notice=%+v", n)
	}
	// A second accepted answer is progress: the episode clears silently.
	c.answer(t, c.invs[1], "SENTINEL-D-ANSWER-11")
	c.exec(`UPDATE employee_task_input SET created_at=$2 WHERE invitation_id=$1::uuid`, c.invs[1].ID, c.start.Add(63*time.Minute))
	c.clockAt(64 * time.Minute)
	if got := c.scan(nil); got.Cleared != 1 || got.Enqueued != 0 {
		t.Fatal("accepted answer did not clear the wait", got)
	}
	// The last answer fills every slot: the ready wake owns the next step, so
	// the filled collection is no longer an input wait.
	c.answer(t, c.invs[2], "SENTINEL-E-ANSWER-13")
	c.withTx(t, func(tx pgx.Tx) {
		goal, err := employeetask.NewStore(tx).Get(c.ctx, c.goal.Scope, c.goal.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := (EmployeeTaskWaitFacts{}).ReadEmployeeTaskWait(c.ctx, tx, goal); err != nil || ok {
			t.Fatal("a filled collection is still reported as an input wait", ok, err)
		}
	})
}

func (c *collectionFixture) withTx(t *testing.T, fn func(pgx.Tx)) {
	t.Helper()
	tx, err := c.pool.Begin(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(c.ctx)
	fn(tx)
}

func TestWatchdogWaitReaderKinds(t *testing.T) {
	c := newCollectionFixture(t)
	store := employeetask.NewStore(c.pool)
	newGoal := func() employeetask.Task {
		g, err := store.Create(c.ctx, employeetask.CreateParams{Scope: c.task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: c.task.RequesterRef,
			Definition: employeetask.Definition{Goal: "等待依赖"}, Source: employeetask.Source{Namespace: "test", Key: "goal-" + uuid.NewString()}, Input: "等待依赖",
			Lifecycle: employeetask.LifecycleV2, CompletionMode: employeetask.CompletionExplicitGoal})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	open := func(g employeetask.Task, kind employeetask.WaitKind, ref string, mandatory bool) employeetask.Task {
		task, _, err := store.WaitTask(c.ctx, g.Scope, g.ID, employeetask.WaitParams{Source: employeetask.Source{Namespace: "test", Key: "wait-" + uuid.NewString()}, Kind: kind, RefID: ref, Mandatory: mandatory, AuthorityRef: "task-authority/" + g.ID})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	read := func(g employeetask.Task) (EmployeeTaskWait, bool) {
		var w EmployeeTaskWait
		var ok bool
		c.withTx(t, func(tx pgx.Tx) {
			fresh, err := store.Get(c.ctx, g.Scope, g.ID)
			if err != nil {
				t.Fatal(err)
			}
			w, ok, err = EmployeeTaskWaitFacts{}.ReadEmployeeTaskWait(c.ctx, tx, fresh)
			if err != nil {
				t.Fatal(err)
			}
		})
		return w, ok
	}
	human := open(newGoal(), employeetask.WaitHumanInput, "requester-reply", true)
	if w, ok := read(human); !ok || w.Kind != EmployeeTaskWaitInputs || w.Expected != 0 {
		t.Fatalf("human input wait=%+v ok=%v", w, ok)
	}
	due := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	timed := open(newGoal(), employeetask.WaitSchedule, due.Format(time.RFC3339), true)
	if w, ok := read(timed); !ok || w.Kind != EmployeeTaskWaitSchedule || !w.DueAt.Equal(due) {
		t.Fatalf("schedule wait=%+v ok=%v", w, ok)
	}
	// A routine trigger's scheduler-owned next run is its due time.
	var autopilotID, triggerID string
	if err := c.pool.QueryRow(c.ctx, `INSERT INTO autopilot(workspace_id,title,assignee_id,status,execution_mode,created_by_type,created_by_id) VALUES($1::uuid,'wd routine',$2::uuid,'active','run_only','member',$3::uuid) RETURNING id::text`, c.ws, c.agent, c.principal).Scan(&autopilotID); err != nil {
		t.Fatal("autopilot fixture: ", err)
	}
	t.Cleanup(func() {
		_, _ = c.pool.Exec(context.Background(), `DELETE FROM autopilot WHERE id=$1::uuid`, autopilotID)
	})
	if err := c.pool.QueryRow(c.ctx, `INSERT INTO autopilot_trigger(autopilot_id,kind,enabled,cron_expression,timezone,next_run_at) VALUES($1::uuid,'schedule',true,'0 * * * *','Asia/Shanghai',$2) RETURNING id::text`, autopilotID, due.Add(time.Hour)).Scan(&triggerID); err != nil {
		t.Fatal(err)
	}
	routine := open(newGoal(), employeetask.WaitSchedule, employeeScheduleRoutineTrigger+triggerID, true)
	if w, ok := read(routine); !ok || !w.DueAt.Equal(due.Add(time.Hour)) {
		t.Fatalf("routine schedule wait=%+v ok=%v", w, ok)
	}
	unknown := open(newGoal(), employeetask.WaitSchedule, "whenever-it-feels-right", true)
	if _, ok := read(unknown); ok {
		t.Fatal("a schedule ref without a known due time was aged")
	}
	upstream := open(newGoal(), employeetask.WaitUpstreamTask, c.goal.ID, true)
	if w, ok := read(upstream); !ok || w.Kind != EmployeeTaskWaitDependency || w.Reason != EmployeeWatchdogUpstreamTask {
		t.Fatalf("upstream wait=%+v ok=%v", w, ok)
	}
	external := open(newGoal(), employeetask.WaitExternal, "webhook:payment-confirmed", true)
	if w, ok := read(external); !ok || w.Reason != EmployeeWatchdogExternalEvent {
		t.Fatalf("external wait=%+v ok=%v", w, ok)
	}
	optional := open(newGoal(), employeetask.WaitExternal, "nice-to-have", false)
	if _, ok := read(optional); ok {
		t.Fatal("an optional wait does not block the goal and is not aged")
	}
	// Dependency notices never name the dependency.
	c.clockAt(0)
	c.clock.Set(time.Now().Add(2 * time.Hour))
	if _, err := c.w.Scan(c.ctx, 200); err != nil {
		t.Fatal(err)
	}
	for _, g := range []employeetask.Task{upstream, external} {
		n := onlyNotice(t, c.notices(g.ID))
		if n.Kind != "waiting_dependency" || strings.Contains(n.Body, c.goal.ID) || strings.Contains(n.Body, "payment") || strings.Contains(n.Body, "还在执行") {
			t.Fatalf("dependency notice=%+v", n)
		}
	}
	if n := onlyNotice(t, c.notices(human.ID)); !strings.Contains(n.Body, "收到后会继续") || strings.Contains(n.Body, "/") {
		t.Fatalf("uncounted input notice=%+v", n)
	}
}

// A dismissed card mutes only its own reminder, including an already queued
// notice, without satisfying a wait or hiding another question's notification.
func TestWatchdogDismissedHumanQuestionNotification(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		want         bool
	}{
		{"dismiss", "", false},
		{"defer", `WITH changed AS (UPDATE employee_human_question SET state='deferred' WHERE id=$1::uuid) UPDATE employee_human_response SET body='{"intent":"defer"}' WHERE question_id=$1::uuid`, false},
		{"answer", `UPDATE employee_human_response SET body='{"intent":"answer"}' WHERE question_id=$1::uuid`, true},
		{"open", `UPDATE employee_human_question SET state='open' WHERE id=$1::uuid`, true},
		{"wrong_scene", `UPDATE employee_human_question SET scene_id=gen_random_uuid() WHERE id=$1::uuid`, true},
		{"wrong_revision", `UPDATE employee_human_question SET goal_revision=goal_revision+1 WHERE id=$1::uuid`, true},
		{"wrong_tenant", `UPDATE employee_human_question SET tenant_org_id='other' WHERE id=$1::uuid`, true},
		{"wrong_task", `UPDATE employee_human_question SET task_id=gen_random_uuid() WHERE id=$1::uuid`, true},
		{"missing_terminal_response", `UPDATE employee_human_question SET response_id=gen_random_uuid() WHERE id=$1::uuid`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, g, q := dismissedHumanWaitFixture(t)
			if tc.change != "" {
				f.exec(tc.change, q)
			}
			before := humanWaitLifecycleSnapshot(t, f)
			tx, err := f.pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			_, ok, err := (EmployeeTaskWaitFacts{}).ReadEmployeeTaskWait(f.ctx, tx, g)
			if err != nil || ok != tc.want {
				t.Fatal(ok, err)
			}
			if !tc.want {
				f.at(62 * time.Minute)
				f.scan(nil)
				if len(f.notices(g.ID)) != 0 {
					t.Fatal("dismissed question opened a fresh notice")
				}
			}
			if before != humanWaitLifecycleSnapshot(t, f) {
				t.Fatal("reader mutated lifecycle")
			}
		})
	}
	t.Run("next_open_question", func(t *testing.T) {
		f, g, _ := dismissedHumanWaitFixture(t)
		_, wait, err := employeetask.NewStore(f.pool).WaitTask(f.ctx, g.Scope, g.ID, employeetask.WaitParams{Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, Kind: employeetask.WaitHumanInput, RefID: uuid.NewString(), Mandatory: true, AuthorityRef: "test"})
		if err != nil {
			t.Fatal(err)
		}
		tx, err := f.pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(f.ctx)
		w, ok, err := (EmployeeTaskWaitFacts{}).ReadEmployeeTaskWait(f.ctx, tx, g)
		if err != nil || !ok || w.Key != wait.ID {
			t.Fatal("later question hidden", w, ok, err)
		}
	})
	t.Run("queued_notice", func(t *testing.T) {
		f, g, q := dismissedHumanWaitFixture(t)
		f.exec(`UPDATE employee_human_question SET state='open',response_id=NULL WHERE id=$1::uuid`, q)
		f.at(62 * time.Minute)
		f.scan(nil)
		n := onlyNotice(t, f.notices(g.ID))
		f.exec(`UPDATE employee_human_question SET state='answered',response_id=(SELECT id FROM employee_human_response WHERE question_id=$1::uuid) WHERE id=$1::uuid`, q)
		before := humanWaitLifecycleSnapshot(t, f)
		_, code := f.runOutbox(f.pool, f.w, n.ActionID, "cancelled")
		if !strings.HasSuffix(code, "state_changed") || f.provider.total() != 0 {
			t.Fatal(code, f.provider.total())
		}
		f.scan(nil)
		if before != humanWaitLifecycleSnapshot(t, f) {
			t.Fatal("watchdog mutated lifecycle")
		}
		if n = onlyNotice(t, f.notices(g.ID)); n.State != "suppressed" {
			t.Fatal(n)
		}
	})
	t.Run("queued_deferred_notice", func(t *testing.T) {
		f, g, q := dismissedHumanWaitFixture(t)
		f.exec(`UPDATE employee_human_question SET state='open',response_id=NULL WHERE id=$1::uuid`, q)
		f.at(62 * time.Minute)
		f.scan(nil)
		n := onlyNotice(t, f.notices(g.ID))
		f.exec(`WITH changed AS (UPDATE employee_human_question SET state='deferred',response_id=(SELECT id FROM employee_human_response WHERE question_id=$1::uuid) WHERE id=$1::uuid) UPDATE employee_human_response SET body='{"intent":"defer"}' WHERE question_id=$1::uuid`, q)
		before := humanWaitLifecycleSnapshot(t, f)
		_, code := f.runOutbox(f.pool, f.w, n.ActionID, "cancelled")
		if !strings.HasSuffix(code, "state_changed") || f.provider.total() != 0 {
			t.Fatal(code, f.provider.total())
		}
		f.scan(nil)
		if before != humanWaitLifecycleSnapshot(t, f) {
			t.Fatal("deferred notification mutated lifecycle")
		}
	})
}

func dismissedHumanWaitFixture(t *testing.T) (*watchdogFixture, employeetask.Task, string) {
	t.Helper()
	f := newWatchdogFixture(t)
	f.exec(`UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1::uuid`, f.queueID)
	f.exec(`UPDATE employee_task_run SET state='succeeded',finished_at=now() WHERE id=$1::uuid`, f.runID)
	f.exec(`UPDATE employee_task SET state='succeeded',active_run_id=NULL WHERE id=$1::uuid`, f.task.ID)
	t.Cleanup(func() {
		for _, sql := range []string{`DELETE FROM employee_human_response WHERE question_id IN (SELECT id FROM employee_human_question WHERE workspace_id=$1::uuid)`, `DELETE FROM employee_human_question WHERE workspace_id=$1::uuid`, `DELETE FROM employee_task_wait WHERE workspace_id=$1::uuid`} {
			if _, err := f.pool.Exec(context.Background(), sql, f.ws); err != nil {
				t.Error(err)
			}
		}
	})
	store := employeetask.NewStore(f.pool)
	g, err := store.Create(f.ctx, employeetask.CreateParams{Scope: f.task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: f.task.RequesterRef, Definition: employeetask.Definition{Goal: "human decision"}, Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, Input: "human decision", Lifecycle: employeetask.LifecycleV2, CompletionMode: employeetask.CompletionExplicitGoal})
	if err != nil {
		t.Fatal(err)
	}
	q, r := uuid.NewString(), uuid.NewString()
	g, _, err = store.WaitTask(f.ctx, g.Scope, g.ID, employeetask.WaitParams{Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, Kind: employeetask.WaitHumanInput, RefID: q, Mandatory: true, AuthorityRef: "human-question:" + q})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE employee_task_wait SET created_at=$2 WHERE task_id=$1::uuid`, g.ID, f.base.Add(time.Minute))
	f.exec(`INSERT INTO employee_human_question(id,workspace_id,agent_id,tenant_org_id,scene_id,principal_id,source_job_id,source_receipt_id,source_ref,requester_ref,operator_open_id,task_id,run_id,goal_revision,summary,choice,card_public_id,state,response_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,gen_random_uuid(),gen_random_uuid(),'test',$7,'operator',$8::uuid,$9::uuid,$10,'question','{}','test','answered',$11::uuid)`, q, f.ws, f.agent, g.Scope.TenantOrgID, g.Scope.Scene.SceneID, util.UUIDToString(f.request.PrincipalID), g.RequesterRef, g.ID, f.runID, g.GoalRevision, r)
	f.exec(`INSERT INTO employee_human_response(id,question_id,event_id,input_surface,requester_ref,body) VALUES($1::uuid,$2::uuid,'dismiss-test','chat_text',$3,'{"intent":"dismiss"}')`, r, q, g.RequesterRef)
	return f, g, q
}

func humanWaitLifecycleSnapshot(t *testing.T, f *watchdogFixture) string {
	t.Helper()
	var raw string
	err := f.pool.QueryRow(f.ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM employee_task t WHERE workspace_id=$1::uuid),(SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM employee_task_wait w WHERE workspace_id=$1::uuid),(SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM employee_task_run r WHERE workspace_id=$1::uuid),(SELECT jsonb_agg(to_jsonb(q) ORDER BY q.id) FROM agent_task_queue q WHERE agent_id=$2::uuid))::text`, f.ws, f.agent).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
