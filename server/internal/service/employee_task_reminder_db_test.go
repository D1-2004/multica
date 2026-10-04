package service

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/taskinput"
)

func (c *collectionFixture) reminders(invitationID string) []struct{ ID, State, Reason, Body, ActionID string } {
	c.t.Helper()
	rows, err := c.pool.Query(c.ctx, `SELECT id::text,state,reason,body,COALESCE(action_id,'') FROM employee_task_invitation_reminder WHERE invitation_id=$1::uuid ORDER BY ordinal`, invitationID)
	if err != nil {
		c.t.Fatal(err)
	}
	defer rows.Close()
	var out []struct{ ID, State, Reason, Body, ActionID string }
	for rows.Next() {
		var r struct{ ID, State, Reason, Body, ActionID string }
		if err := rows.Scan(&r.ID, &r.State, &r.Reason, &r.Body, &r.ActionID); err != nil {
			c.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func defaultReminderPolicy(requester string) InvitationReminderPolicy {
	return InvitationReminderPolicy{FirstAfter: 30 * time.Minute, Interval: time.Hour, AuthorityActorRef: requester, Source: taskinput.Source{Namespace: "dingtalk.message", Key: "origin-msg-reminders"}, InstructionQuote: "半小时没回就提醒他们一次"}
}

// withPolicy rebuilds the collection with a reminder policy on every invitation.
func newReminderFixture(t *testing.T, policy func(requester string) InvitationReminderPolicy) *collectionFixture {
	t.Helper()
	c := newCollectionFixture(t)
	// Replace the policy-less collection with an authorized one on a new goal.
	goal, err := employeetask.NewStore(c.pool).Create(c.ctx, employeetask.CreateParams{Scope: c.task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect,
		RequesterRef: c.task.RequesterRef, Definition: employeetask.Definition{Goal: "收集数字（允许提醒）"}, Source: employeetask.Source{Namespace: "test", Key: "goal-" + uuid.NewString()},
		Input: "收集数字（允许提醒）", Lifecycle: employeetask.LifecycleV2, CompletionMode: employeetask.CompletionExplicitGoal})
	if err != nil {
		t.Fatal(err)
	}
	p := policy(goal.RequesterRef)
	specs := []taskinput.InvitationSpec{
		{TargetSceneID: c.scenes["dmC"].id, ParticipantRef: reminderParticipantC, Question: "C 同学，请回复你本周的数字。"},
		{TargetSceneID: c.scenes["dmD"].id, ParticipantRef: reminderParticipantD, Question: "D 同学，请回复你本周的数字。"},
		{TargetSceneID: c.scenes["group"].id, ParticipantRef: reminderParticipantE, Question: "E 同学，请在群里回复你本周的数字。"},
	}
	// Close the first goal's collection so only the authorized one is open.
	c.exec(`UPDATE employee_task_collection SET state='cancelled',close_mode='cancel',closed_at=now() WHERE id=$1::uuid`, c.col.ID)
	c.goal = goal
	c.col, c.invs = c.create(t, goal, specs, c.withPolicy(p))
	c.deliverAll(t)
	return c
}

// withPolicy records reminder authorization for every invitation in the
// creation transaction, as the A2 contract requires.
func (c *collectionFixture) withPolicy(p InvitationReminderPolicy) func(pgx.Tx, []taskinput.Invitation) {
	return func(tx pgx.Tx, invs []taskinput.Invitation) {
		for _, inv := range invs {
			if err := RecordInvitationReminderPolicyTx(c.ctx, tx, c.scope, inv.ID, p); err != nil {
				c.t.Fatal(err)
			}
		}
	}
}

// --- C3: invitation reminders ---------------------------------------------------

func TestWatchdogReminderRequiresExplicitAuthorization(t *testing.T) {
	c := newCollectionFixture(t)
	c.deliverAll(t)
	for _, d := range []time.Duration{time.Hour, 6 * time.Hour, 48 * time.Hour} {
		c.clockAt(d)
		if _, err := c.w.Scan(c.ctx, 200); err != nil {
			t.Fatal(err)
		}
	}
	if n := c.count(`SELECT count(*) FROM employee_task_invitation_reminder WHERE workspace_id=$1::uuid`, c.ws); n != 0 {
		t.Fatal("reminded without an explicit authorization", n)
	}
	// Stall notices to the requester go to the origin scene; nothing may reach
	// a participant's scene without an authorization.
	if n := c.count(`SELECT count(*) FROM employee_host_notice WHERE workspace_id=$1::uuid AND source_kind='watchdog' AND scene_id<>$2::uuid`, c.ws, c.goal.Scope.Scene.SceneID); n != 0 {
		t.Fatal("reminder history without authorization", n)
	}
}

func TestWatchdogReminderPolicyIsFrozenWithTheInvitation(t *testing.T) {
	c := newCollectionFixture(t)
	p := defaultReminderPolicy(c.requester)
	// After creation (another transaction), nobody can add authority.
	later, err := c.pool.Begin(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = RecordInvitationReminderPolicyTx(c.ctx, later, c.scope, c.invs[0].ID, p)
	_ = later.Rollback(c.ctx)
	if !errors.Is(err, ErrReminderPolicyForbidden) {
		t.Fatal("a later transaction added reminder authority", err)
	}
	// In the creation transaction: only the requester, with a bounded policy.
	goal, err := employeetask.NewStore(c.pool).Create(c.ctx, employeetask.CreateParams{Scope: c.task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect,
		RequesterRef: c.task.RequesterRef, Definition: employeetask.Definition{Goal: "policy"}, Source: employeetask.Source{Namespace: "test", Key: "goal-" + uuid.NewString()},
		Input: "policy", Lifecycle: employeetask.LifecycleV2, CompletionMode: employeetask.CompletionExplicitGoal})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.pool.Begin(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(c.ctx)
	_, invs, err := taskinput.NewStore(tx).CreateCollectionTx(c.ctx, c.scope, taskinput.CreateCollectionParams{TaskID: goal.ID, OriginSceneID: goal.Scope.Scene.SceneID, AuthorityRef: "auth", RequesterRef: goal.RequesterRef,
		DeliveryAnchorRef: "anchor", GoalRevision: goal.GoalRevision, Invitations: []taskinput.InvitationSpec{{TargetSceneID: c.scenes["dmE"].id, ParticipantRef: reminderParticipantE, Question: "E 请回复"}},
		Source: taskinput.Source{Namespace: "employee.tool", Key: uuid.NewString()}, Authority: taskinput.Authority{ActorRef: goal.RequesterRef, SceneID: goal.Scope.Scene.SceneID, ReceiptRef: "r", VerifiedAt: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	other := p
	other.AuthorityActorRef = reminderParticipantC
	if err := RecordInvitationReminderPolicyTx(c.ctx, tx, c.scope, invs[0].ID, other); !errors.Is(err, ErrReminderPolicyForbidden) {
		t.Fatal("a non-requester authorized reminders", err)
	}
	for _, bad := range []InvitationReminderPolicy{{MaxCount: 4, FirstAfter: time.Hour, Interval: time.Hour}, {FirstAfter: time.Second, Interval: time.Hour}, {FirstAfter: time.Hour, Interval: time.Minute}} {
		bad.AuthorityActorRef, bad.Source, bad.InstructionQuote = p.AuthorityActorRef, p.Source, p.InstructionQuote
		if err := RecordInvitationReminderPolicyTx(c.ctx, tx, c.scope, invs[0].ID, bad); !errors.Is(err, ErrReminderPolicyInvalid) {
			t.Fatal("unbounded policy accepted", bad, err)
		}
	}
	if err := RecordInvitationReminderPolicyTx(c.ctx, tx, c.scope, invs[0].ID, p); err != nil {
		t.Fatal(err)
	}
	if err := RecordInvitationReminderPolicyTx(c.ctx, tx, c.scope, invs[0].ID, p); err != nil {
		t.Fatal("identical replay refused", err)
	}
	changed := p
	changed.MaxCount = 2
	if err := RecordInvitationReminderPolicyTx(c.ctx, tx, c.scope, invs[0].ID, changed); !errors.Is(err, ErrReminderPolicyConflict) {
		t.Fatal("frozen policy changed", err)
	}
	var max int
	if err := tx.QueryRow(c.ctx, `SELECT max_count FROM employee_task_invitation_reminder_policy WHERE invitation_id=$1::uuid`, invs[0].ID).Scan(&max); err != nil || max != 1 {
		t.Fatal("default max count", max, err)
	}
}

func TestWatchdogReminderSendsOnlyOwnQuestionOnce(t *testing.T) {
	c := newReminderFixture(t, defaultReminderPolicy)
	c.answer(t, c.invs[1], "SENTINEL-D-ANSWER-11")
	c.clockAt(29 * time.Minute)
	if got := c.scan(nil); got.Enqueued != 0 {
		t.Fatal("reminded before the frozen delay", got)
	}
	c.clockAt(31 * time.Minute)
	c.scan(nil)
	// C and E are still waiting; D answered.
	if r := c.reminders(c.invs[1].ID); len(r) != 0 {
		t.Fatal("an answered participant was reminded", r)
	}
	var cReminder struct{ ID, State, Reason, Body, ActionID string }
	for i, inv := range []taskinput.Invitation{c.invs[0], c.invs[2]} {
		rows := c.reminders(inv.ID)
		if len(rows) != 1 || rows[0].State != "enqueued" || rows[0].ActionID == "" {
			t.Fatalf("reminder for %s: %+v", inv.ParticipantRef, rows)
		}
		body := rows[0].Body
		if !strings.Contains(body, inv.Question) || strings.Contains(body, "SENTINEL") || strings.Contains(body, c.goal.Definition.Goal) {
			t.Fatalf("reminder carries more than the participant's own question: %q", body)
		}
		for j, other := range c.invs {
			if other.ID != inv.ID && strings.Contains(body, other.Question) {
				t.Fatalf("reminder %d carries question %d", i, j)
			}
		}
		var conversation, sender, sceneID string
		if err := c.pool.QueryRow(c.ctx, `SELECT input->>'conversation_id',COALESCE(input->>'sender_open_dingtalk_id',''),input->>'scene_id' FROM response_action WHERE id=$1`, rows[0].ActionID).Scan(&conversation, &sender, &sceneID); err != nil {
			t.Fatal(err)
		}
		target := c.sceneOf(inv)
		if conversation != target.external || sceneID != inv.TargetSceneID || (target.kind == scene.KindDM && sender != "odt-"+inv.ParticipantRef) {
			t.Fatal("reminder went somewhere other than the invitation's scene", conversation, sender)
		}
		var kind, principal string
		if err := c.pool.QueryRow(c.ctx, `SELECT source_kind,principal_id::text FROM employee_host_notice WHERE action_id=$1 AND scene_id=$2::uuid`, rows[0].ActionID, inv.TargetSceneID).Scan(&kind, &principal); err != nil || kind != employeeentry.HostNoticeWatchdog || principal != c.principal {
			t.Fatal("reminder is not in the participant scene's history", kind, principal, err)
		}
		if i == 0 {
			cReminder = rows[0]
		}
	}
	// Default max count is one: no further reminders, ever.
	for _, d := range []time.Duration{2 * time.Hour, 24 * time.Hour} {
		c.clockAt(d)
		c.scan(nil)
		if n := c.count(`SELECT count(*) FROM employee_task_invitation_reminder WHERE collection_id=$1::uuid`, c.col.ID); n != 2 {
			t.Fatal("reminded beyond the frozen count", n)
		}
	}
	// Delivered exactly once through the real outbox and its fence.
	c.runOutbox(c.pool, c.w, cReminder.ActionID, "delivered")
	if c.provider.sent(cReminder.ID) != 1 {
		t.Fatal("reminder send count", c.provider.sent(cReminder.ID))
	}
	if c.count(`SELECT count(*) FROM employee_task_run WHERE task_id=$1::uuid`, c.goal.ID) != 0 || c.count(`SELECT count(*) FROM employee_scene_job WHERE workspace_id=$1::uuid`, c.ws) != 0 {
		t.Fatal("a reminder created execution or a foreground job")
	}
}

func TestWatchdogReminderStopsWhenAnsweredRevokedExpiredOrCancelled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(c *collectionFixture)
	}{
		{"answered", func(c *collectionFixture) { c.answer(t, c.invs[0], "7") }},
		{"revoked", func(c *collectionFixture) {
			col, _ := c.inputs.GetCollection(c.ctx, c.scope, c.col.ID)
			if _, _, err := c.inputs.RevokeInvitationTx(c.ctx, c.scope, taskinput.RevokeInvitationParams{CollectionID: c.col.ID, InvitationID: c.invs[0].ID, Reason: "requester withdrew", Source: taskinput.Source{Namespace: "test", Key: uuid.NewString()},
				Authority: taskinput.Authority{ActorRef: c.requester, SceneID: c.goal.Scope.Scene.SceneID, ReceiptRef: "r", VerifiedAt: time.Now()}, ExpectedRevision: col.Revision}); err != nil {
				t.Fatal(err)
			}
		}},
		{"expired", func(c *collectionFixture) {
			c.exec(`UPDATE employee_task_invitation SET expires_at=$2 WHERE id=$1::uuid`, c.invs[0].ID, c.start.Add(20*time.Minute))
		}},
		{"collection_cancelled", func(c *collectionFixture) {
			col, _ := c.inputs.GetCollection(c.ctx, c.scope, c.col.ID)
			if _, _, err := c.inputs.CloseCollectionTx(c.ctx, c.scope, taskinput.CloseParams{CollectionID: c.col.ID, Mode: taskinput.CloseCancel, Reason: "task cancelled", Source: taskinput.Source{Namespace: "test", Key: uuid.NewString()},
				Authority: taskinput.Authority{ActorRef: c.requester, SceneID: c.goal.Scope.Scene.SceneID, ReceiptRef: "r", VerifiedAt: time.Now()}, ExpectedRevision: col.Revision}); err != nil {
				t.Fatal(err)
			}
		}},
		{"task_stopped", func(c *collectionFixture) {
			c.exec(`UPDATE employee_task SET state='cancelled' WHERE id=$1::uuid`, c.goal.ID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newReminderFixture(t, defaultReminderPolicy)
			tc.change(c)
			c.clockAt(31 * time.Minute)
			if _, err := c.w.Scan(c.ctx, 200); err != nil {
				t.Fatal(err)
			}
			if r := c.reminders(c.invs[0].ID); len(r) != 0 {
				t.Fatalf("%s invitation was reminded: %+v", tc.name, r)
			}
		})
	}
}

func TestWatchdogReminderBeforeSendSuppressesAfterAnswer(t *testing.T) {
	c := newReminderFixture(t, defaultReminderPolicy)
	c.clockAt(31 * time.Minute)
	c.scan(nil)
	r := c.reminders(c.invs[0].ID)
	if len(r) != 1 || r[0].State != "enqueued" {
		t.Fatal(r)
	}
	// The participant answers before the outbox sends the reminder.
	c.answer(t, c.invs[0], "7")
	_, code := c.runOutbox(c.pool, c.w, r[0].ActionID, "cancelled")
	if !strings.HasSuffix(code, "invitation_answered") || c.provider.sent(r[0].ID) != 0 {
		t.Fatal("stale reminder was sent", code)
	}
	if r = c.reminders(c.invs[0].ID); r[0].State != "suppressed" || r[0].Reason != "invitation_answered" {
		t.Fatal(r)
	}
}

func TestWatchdogReminderSamePersonTwoInvitationsDoNotCross(t *testing.T) {
	c := newReminderFixture(t, defaultReminderPolicy)
	// A second Task invites the same participant C in the same DM.
	goal, err := employeetask.NewStore(c.pool).Create(c.ctx, employeetask.CreateParams{Scope: c.task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect,
		RequesterRef: c.task.RequesterRef, Definition: employeetask.Definition{Goal: "第二件事"}, Source: employeetask.Source{Namespace: "test", Key: "goal-" + uuid.NewString()},
		Input: "第二件事", Lifecycle: employeetask.LifecycleV2, CompletionMode: employeetask.CompletionExplicitGoal})
	if err != nil {
		t.Fatal(err)
	}
	p := defaultReminderPolicy(goal.RequesterRef)
	_, second := c.create(t, goal, []taskinput.InvitationSpec{{TargetSceneID: c.scenes["dmC"].id, ParticipantRef: reminderParticipantC, Question: "C 同学，第二件事：请回复你的城市。"}}, c.withPolicy(p))
	c.deliver(t, second[0])
	c.exec(`UPDATE employee_task_invitation SET delivered_at=$2 WHERE id=ANY($1::uuid[])`, []string{c.invs[0].ID, second[0].ID}, c.start)
	c.clockAt(31 * time.Minute)
	c.scan(nil)
	first, other := c.reminders(c.invs[0].ID), c.reminders(second[0].ID)
	if len(first) != 1 || len(other) != 1 {
		t.Fatal(first, other)
	}
	if !strings.Contains(first[0].Body, "本周的数字") || strings.Contains(first[0].Body, "城市") || !strings.Contains(other[0].Body, "城市") || strings.Contains(other[0].Body, "本周的数字") {
		t.Fatal("reminders crossed between two invitations of one person", first[0].Body, other[0].Body)
	}
}

func TestWatchdogReminderFrequencyAndCountBound(t *testing.T) {
	c := newReminderFixture(t, func(requester string) InvitationReminderPolicy {
		p := defaultReminderPolicy(requester)
		p.MaxCount, p.Interval = 2, time.Hour
		return p
	})
	scanBoth := func() {
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for range 4 {
			pool, err := pgxpool.NewWithConfig(c.ctx, c.pool.Config().Copy())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			w := c.watchdog(pool, dingtalkresponse.NewService(pool, c.provider, nil))
			wg.Go(func() { _, err := w.Scan(c.ctx, 200); errs <- err })
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	c.clockAt(31 * time.Minute)
	scanBoth()
	if n := len(c.reminders(c.invs[0].ID)); n != 1 {
		t.Fatal("concurrent scanners did not record exactly one reminder", n)
	}
	var first time.Time
	if err := c.pool.QueryRow(c.ctx, `SELECT created_at FROM employee_task_invitation_reminder WHERE invitation_id=$1::uuid`, c.invs[0].ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	c.clock.Set(first.Add(59 * time.Minute))
	scanBoth()
	if n := len(c.reminders(c.invs[0].ID)); n != 1 {
		t.Fatal("frozen interval not respected", n)
	}
	c.clock.Set(first.Add(61 * time.Minute))
	scanBoth()
	if n := len(c.reminders(c.invs[0].ID)); n != 2 {
		t.Fatal("second reminder missing", n)
	}
	c.clock.Set(first.Add(30 * time.Hour))
	scanBoth()
	if n := len(c.reminders(c.invs[0].ID)); n != 2 {
		t.Fatal("frozen count exceeded", n)
	}
}

func TestWatchdogReminderHeldWithoutInvitationContract(t *testing.T) {
	c := newReminderFixture(t, defaultReminderPolicy)
	c.exec(`DELETE FROM response_action WHERE id=$1`, c.invs[0].DeliveryActionID)
	c.exec(`DELETE FROM employee_host_notice WHERE action_id=$1`, c.invs[1].DeliveryActionID)
	c.exec(`UPDATE agent_scene SET tenant_org_id='other-org' WHERE id=$1::uuid`, c.invs[2].TargetSceneID)
	c.clockAt(31 * time.Minute)
	c.scan(nil)
	for inv, reason := range map[string]string{c.invs[0].ID: "invitation_action_missing", c.invs[1].ID: "history_principal_unknown", c.invs[2].ID: "tenant_revoked"} {
		r := c.reminders(inv)
		if len(r) != 1 || r[0].State != "held" || r[0].Reason != reason || r[0].ActionID != "" {
			t.Fatalf("want held %s, got %+v", reason, r)
		}
	}
	if n := c.count(`SELECT count(*) FROM employee_host_notice WHERE workspace_id=$1::uuid AND source_kind='watchdog' AND scene_id<>$2::uuid`, c.ws, c.goal.Scope.Scene.SceneID); n != 0 {
		t.Fatal("held reminders were written to history", n)
	}
}
