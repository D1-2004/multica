package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

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

// Invitation reminders (05-watchdog.md C3). Only an invitation whose original
// requester explicitly authorized reminders can be reminded, at a frequency
// and count frozen with the invitation. A reminder repeats only that
// participant's own question into the scene the invitation was delivered to;
// it never carries another participant's answer, the Task, or the origin
// conversation. It is a Host scene notice: no Task, Run, queue work or model
// call. Answered, revoked, expired or closed invitations and stopped Tasks are
// never reminded.

var (
	ErrReminderPolicyInvalid   = errors.New("invitation reminder policy is invalid")
	ErrReminderPolicyForbidden = errors.New("only the original requester can authorize invitation reminders when the invitation is created")
	ErrReminderPolicyConflict  = errors.New("invitation reminder policy is frozen with different values")
)

// InvitationReminderPolicy is the requester's explicit authorization, frozen
// with the invitation. MaxCount defaults to 1.
type InvitationReminderPolicy struct {
	MaxCount   int
	FirstAfter time.Duration
	Interval   time.Duration
	// AuthorityActorRef must be the collection's requester_ref.
	AuthorityActorRef string
	// Source is the requester's authorizing source action and InstructionQuote
	// its exact words asking for reminders (audit only, never sent).
	Source           taskinput.Source
	InstructionQuote string
}

func (p InvitationReminderPolicy) normalized() (InvitationReminderPolicy, error) {
	if p.MaxCount == 0 {
		p.MaxCount = 1
	}
	p.AuthorityActorRef = strings.TrimSpace(p.AuthorityActorRef)
	p.InstructionQuote = strings.TrimSpace(p.InstructionQuote)
	switch {
	case p.MaxCount < 1 || p.MaxCount > 3,
		p.FirstAfter < time.Minute || p.FirstAfter > 7*24*time.Hour || p.FirstAfter%time.Second != 0,
		p.Interval < 10*time.Minute || p.Interval > 7*24*time.Hour || p.Interval%time.Second != 0,
		p.AuthorityActorRef == "" || len(p.AuthorityActorRef) > 256,
		strings.TrimSpace(p.Source.Namespace) == "" || len(p.Source.Namespace) > 128,
		strings.TrimSpace(p.Source.Key) == "" || len(p.Source.Key) > 512,
		p.InstructionQuote == "" || utf8.RuneCountInString(p.InstructionQuote) > 500:
		return p, ErrReminderPolicyInvalid
	}
	return p, nil
}

// RecordInvitationReminderPolicyTx freezes the requester's reminder
// authorization for one invitation. It must run in the transaction that
// created the invitation (taskinput.CreateCollectionTx); a later call cannot
// add authority to an existing invitation. Replaying the same policy is a
// no-op; different values conflict.
func RecordInvitationReminderPolicyTx(ctx context.Context, tx pgx.Tx, scope taskinput.Scope, invitationID string, p InvitationReminderPolicy) error {
	if tx == nil {
		return ErrReminderPolicyInvalid
	}
	p, err := p.normalized()
	if err != nil {
		return err
	}
	if parsed, err := uuid.Parse(invitationID); err != nil || parsed.String() != invitationID {
		return ErrReminderPolicyInvalid
	}
	var taskID, collectionID, requester string
	var createdNow bool
	err = tx.QueryRow(ctx, `SELECT i.task_id::text,i.collection_id::text,c.requester_ref,i.created_at=now()
 FROM employee_task_invitation i JOIN employee_task_collection c ON c.id=i.collection_id AND c.workspace_id=i.workspace_id AND c.agent_id=i.agent_id AND c.tenant_org_id=i.tenant_org_id AND c.task_id=i.task_id
 WHERE i.id=$1::uuid AND i.workspace_id=$2::uuid AND i.agent_id=$3::uuid AND i.tenant_org_id=$4`,
		invitationID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID).Scan(&taskID, &collectionID, &requester, &createdNow)
	if errors.Is(err, pgx.ErrNoRows) {
		return taskinput.ErrNotFound
	}
	if err != nil {
		return err
	}
	var existing InvitationReminderPolicy
	var first, interval int
	err = tx.QueryRow(ctx, `SELECT max_count,first_after_seconds,interval_seconds,authority_actor_ref,source_namespace,source_key,instruction_quote
 FROM employee_task_invitation_reminder_policy WHERE invitation_id=$1::uuid`, invitationID).
		Scan(&existing.MaxCount, &first, &interval, &existing.AuthorityActorRef, &existing.Source.Namespace, &existing.Source.Key, &existing.InstructionQuote)
	if err == nil {
		existing.FirstAfter, existing.Interval = time.Duration(first)*time.Second, time.Duration(interval)*time.Second
		if existing != p {
			return ErrReminderPolicyConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if p.AuthorityActorRef != requester || !createdNow {
		return ErrReminderPolicyForbidden
	}
	_, err = tx.Exec(ctx, `INSERT INTO employee_task_invitation_reminder_policy
 (invitation_id,workspace_id,agent_id,tenant_org_id,task_id,collection_id,max_count,first_after_seconds,interval_seconds,authority_actor_ref,source_namespace,source_key,instruction_quote)
 VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,$7,$8,$9,$10,$11,$12,$13)`,
		invitationID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID, collectionID, p.MaxCount,
		int(p.FirstAfter/time.Second), int(p.Interval/time.Second), p.AuthorityActorRef, p.Source.Namespace, p.Source.Key, p.InstructionQuote)
	return err
}

// ComposeInvitationReminder renders the deterministic reminder: the
// participant's own question and how to answer it in that scene, nothing else.
func ComposeInvitationReminder(question, sceneKind string) string {
	question = strings.TrimSpace(question)
	hint := "直接在这里回复即可。"
	if sceneKind == scene.KindGroup {
		hint = "请引用回复最初的提问消息作答。"
	}
	return "提醒：还在等你回复这个问题——\n「" + question + "」\n" + hint
}

type employeeReminderCandidate struct {
	invitationID string
	scope        taskinput.Scope
}

// employeeReminderFacts is what one reminder decision reads, all from PG.
type employeeReminderFacts struct {
	policy     InvitationReminderPolicy
	invitation taskinput.Invitation
	collection taskinput.Collection
	task       employeetask.Task
	sent       int
	lastAt     time.Time
}

// stopReason reports why no reminder may be sent now; "" means the
// invitation is still waiting for this participant.
func (f employeeReminderFacts) stopReason(now time.Time) string {
	inv, col := f.invitation, f.collection
	switch {
	case inv.State == taskinput.InvitationAnswered || inv.EffectiveVersion > 0:
		return "invitation_answered"
	case inv.State == taskinput.InvitationRevoked:
		return "invitation_revoked"
	case inv.State == taskinput.InvitationExpired || (inv.ExpiresAt != nil && !now.Before(*inv.ExpiresAt)):
		return "invitation_expired"
	case inv.State != taskinput.InvitationDelivered || inv.DeliveredAt == nil || inv.TargetSceneID == "":
		return "invitation_not_delivered"
	case col.State != taskinput.CollectionOpen:
		return "collection_" + string(col.State)
	case col.Deadline != nil && !now.Before(col.Deadline.At):
		return "collection_deadline_passed"
	case f.task.State == employeetask.StateCancelled:
		return "task_stopped"
	case f.task.State == employeetask.StateSucceeded || f.task.State == employeetask.StateFailed:
		return "task_closed"
	case f.task.GoalRevision != col.GoalRevision:
		return "goal_revised"
	}
	return ""
}

// due reports whether the next ordinal is due under the frozen policy.
func (f employeeReminderFacts) due(now time.Time) bool {
	if f.sent >= f.policy.MaxCount || f.invitation.DeliveredAt == nil {
		return false
	}
	if f.sent == 0 {
		return !now.Before(f.invitation.DeliveredAt.Add(f.policy.FirstAfter))
	}
	return !now.Before(f.lastAt.Add(f.policy.Interval))
}

func loadEmployeeReminderFacts(ctx context.Context, tx pgx.Tx, scope taskinput.Scope, invitationID string) (employeeReminderFacts, bool, error) {
	var f employeeReminderFacts
	var first, interval int
	err := tx.QueryRow(ctx, `SELECT max_count,first_after_seconds,interval_seconds,authority_actor_ref,source_namespace,source_key,instruction_quote
 FROM employee_task_invitation_reminder_policy WHERE invitation_id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4`,
		invitationID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID).
		Scan(&f.policy.MaxCount, &first, &interval, &f.policy.AuthorityActorRef, &f.policy.Source.Namespace, &f.policy.Source.Key, &f.policy.InstructionQuote)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, false, nil
	}
	if err != nil {
		return f, false, err
	}
	f.policy.FirstAfter, f.policy.Interval = time.Duration(first)*time.Second, time.Duration(interval)*time.Second
	inputs := taskinput.NewStore(tx)
	if f.invitation, err = inputs.GetInvitation(ctx, scope, invitationID); errors.Is(err, taskinput.ErrNotFound) {
		return f, false, nil
	} else if err != nil {
		return f, false, err
	}
	if f.collection, err = inputs.GetCollection(ctx, scope, f.invitation.CollectionID); errors.Is(err, taskinput.ErrNotFound) {
		return f, false, nil
	} else if err != nil {
		return f, false, err
	}
	taskScope := employeetask.Scope{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID, Kind: employeetask.ScopeScene}
	taskScope.Scene.SceneID = f.collection.OriginSceneID
	if f.task, err = employeetask.NewStore(tx).Get(ctx, taskScope, f.collection.TaskID); errors.Is(err, employeetask.ErrNotFound) {
		return f, false, nil
	} else if err != nil {
		return f, false, err
	}
	var last *time.Time
	if err = tx.QueryRow(ctx, `SELECT count(*),max(created_at) FROM employee_task_invitation_reminder WHERE invitation_id=$1::uuid`, invitationID).Scan(&f.sent, &last); err != nil {
		return f, false, err
	}
	if last != nil {
		f.lastAt = *last
	}
	return f, true, nil
}

// employeeReminderTarget is the scene notice address and its history fact.
type employeeReminderTarget struct {
	input       dingtalkresponse.ActionInput
	principalID string
}

// resolveEmployeeReminderTarget re-derives where a reminder goes from the
// invitation's own delivered send: the response action with the invitation's
// delivery action id holds the provider address (including a DM peer), and
// its host notice the scene history principal. The scene directory, tenant
// fence and the agent's current DWS identity are re-checked, so a revoked or
// changed target is held, never guessed.
func resolveEmployeeReminderTarget(ctx context.Context, tx pgx.Tx, f employeeReminderFacts) (employeeReminderTarget, error) {
	var out employeeReminderTarget
	inv := f.invitation
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT input FROM response_action WHERE id=$1 AND workspace_id=$2::uuid AND agent_id=$3::uuid`,
		inv.DeliveryActionID, inv.Scope.WorkspaceID, inv.Scope.AgentID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, &EmployeeWatchdogHold{Reason: "invitation_action_missing"}
	}
	if err != nil {
		return out, err
	}
	var sent dingtalkresponse.ActionInput
	if json.Unmarshal(raw, &sent) != nil {
		return out, &EmployeeWatchdogHold{Reason: "invitation_action_invalid"}
	}
	q := db.New(tx)
	sceneID, err := scene.ParseID(inv.TargetSceneID)
	if err != nil {
		return out, &EmployeeWatchdogHold{Reason: "target_scene_removed"}
	}
	ws, err1 := util.ParseUUID(inv.Scope.WorkspaceID)
	agentID, err2 := util.ParseUUID(inv.Scope.AgentID)
	if err1 != nil || err2 != nil {
		return out, &EmployeeWatchdogHold{Reason: "invitation_scope_invalid"}
	}
	owner := scene.Owner{WorkspaceID: ws, AgentID: agentID}
	target, err := scene.Get(ctx, q, owner, sceneID)
	if errors.Is(err, scene.ErrNotFound) {
		return out, &EmployeeWatchdogHold{Reason: "target_scene_removed"}
	}
	if err != nil {
		return out, err
	}
	if scene.CheckTenant(target, inv.Scope.TenantOrgID) != nil {
		return out, &EmployeeWatchdogHold{Reason: "tenant_revoked"}
	}
	if target.SceneKind != scene.KindGroup && target.SceneKind != scene.KindDM {
		return out, &EmployeeWatchdogHold{Reason: "unsupported_scene_kind"}
	}
	isGroup := target.SceneKind == scene.KindGroup
	if sent.SceneID != inv.TargetSceneID || sent.ConversationID != target.ExternalSceneID || sent.IsGroup != isGroup || sent.DWSOrgID != inv.Scope.TenantOrgID {
		return out, &EmployeeWatchdogHold{Reason: "target_scene_changed"}
	}
	if !isGroup && sent.SenderOpenDingTalkID == "" {
		return out, &EmployeeWatchdogHold{Reason: "participant_target_unresolved"}
	}
	agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: owner.AgentID, WorkspaceID: owner.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, &EmployeeWatchdogHold{Reason: "agent_removed"}
	}
	if err != nil {
		return out, err
	}
	if agent.ArchivedAt.Valid {
		return out, &EmployeeWatchdogHold{Reason: "agent_archived"}
	}
	identity, err := q.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, &EmployeeWatchdogHold{Reason: "identity_removed"}
	}
	if err != nil {
		return out, err
	}
	if identity.DwsUid != sent.DWSUID {
		return out, &EmployeeWatchdogHold{Reason: "identity_changed"}
	}
	err = tx.QueryRow(ctx, `SELECT principal_id::text FROM employee_host_notice WHERE action_id=$1 AND source_kind=$2
 AND workspace_id=$3::uuid AND agent_id=$4::uuid AND tenant_org_id=$5 AND scene_id=$6::uuid`,
		inv.DeliveryActionID, employeeentry.HostNoticeInvitation, inv.Scope.WorkspaceID, inv.Scope.AgentID, inv.Scope.TenantOrgID, inv.TargetSceneID).Scan(&out.principalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, &EmployeeWatchdogHold{Reason: "history_principal_unknown"}
	}
	if err != nil {
		return out, err
	}
	out.input = dingtalkresponse.ActionInput{WorkspaceID: inv.Scope.WorkspaceID, AgentID: inv.Scope.AgentID, DWSUID: sent.DWSUID, DWSOrgID: sent.DWSOrgID,
		SceneID: inv.TargetSceneID, ConversationID: sent.ConversationID, SenderOpenDingTalkID: sent.SenderOpenDingTalkID, IsGroup: isGroup,
		ShowAITag: sent.ShowAITag, DWSEnvironment: sent.DWSEnvironment}
	return out, nil
}

// employeeReminderEgress scans the exact reminder bytes against the other
// participants' answers in the same collection (never allowed to leave).
func employeeReminderEgress(ctx context.Context, tx pgx.Tx, f employeeReminderFacts, sceneKind, body string) (string, error) {
	audience, ok := taskinput.AudienceForSceneKind(sceneKind)
	if !ok {
		return "unsupported_scene_kind", nil
	}
	rows, err := tx.Query(ctx, `SELECT body FROM employee_task_input WHERE collection_id=$1::uuid AND invitation_id<>$2::uuid
 AND workspace_id=$3::uuid AND agent_id=$4::uuid AND tenant_org_id=$5`, f.collection.ID, f.invitation.ID, f.invitation.Scope.WorkspaceID, f.invitation.Scope.AgentID, f.invitation.Scope.TenantOrgID)
	if err != nil {
		return "", err
	}
	private := []taskinput.PrivateFragment{}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			rows.Close()
			return "", err
		}
		private = append(private, taskinput.PrivateFragment{Text: text})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	result := taskinput.CheckEgress(taskinput.EgressInput{Rendered: body, Audience: audience, TargetSceneKind: sceneKind, Private: private})
	if result.Held {
		return "egress_" + strings.Join(result.Reasons, "_"), nil
	}
	return "", nil
}

// scanReminders records at most one due reminder per invitation per scan.
func (w *EmployeeWatchdog) scanReminders(ctx context.Context, now time.Time, limit int) (EmployeeWatchdogScanResult, []error) {
	var out EmployeeWatchdogScanResult
	rows, err := w.DB.Query(ctx, `SELECT p.invitation_id::text,p.workspace_id::text,p.agent_id::text,p.tenant_org_id
 FROM employee_task_invitation_reminder_policy p
 JOIN employee_task_invitation i ON i.id=p.invitation_id AND i.workspace_id=p.workspace_id AND i.agent_id=p.agent_id AND i.tenant_org_id=p.tenant_org_id
 WHERE i.delivery_state='delivered' AND i.delivered_at IS NOT NULL
 AND i.delivered_at + make_interval(secs => p.first_after_seconds) <= $1
 AND (SELECT count(*) FROM employee_task_invitation_reminder r WHERE r.invitation_id=p.invitation_id) < p.max_count
 ORDER BY i.delivered_at,p.invitation_id LIMIT $2`, now, limit)
	if err != nil {
		return out, []error{err}
	}
	var candidates []employeeReminderCandidate
	for rows.Next() {
		var c employeeReminderCandidate
		if err := rows.Scan(&c.invitationID, &c.scope.WorkspaceID, &c.scope.AgentID, &c.scope.TenantOrgID); err != nil {
			rows.Close()
			return out, []error{err}
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, []error{err}
	}
	var errs []error
	for _, c := range candidates {
		r, err := w.remindOne(ctx, now, c)
		if err != nil {
			errs = append(errs, fmt.Errorf("employee invitation reminder %s: %w", c.invitationID, err))
			continue
		}
		out.add(r)
	}
	return out, errs
}

func lockEmployeeReminder(ctx context.Context, tx pgx.Tx, workspaceID, invitationID string) (bool, error) {
	var locked string
	err := tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, workspaceID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('employee-invitation-reminder:' || $1, 0))`, invitationID)
	return err == nil, err
}

// remindOne decides and records one reminder ordinal in its own transaction.
// The scene notice and its history fact commit with the reminder row.
func (w *EmployeeWatchdog) remindOne(ctx context.Context, now time.Time, c employeeReminderCandidate) (EmployeeWatchdogScanResult, error) {
	var out EmployeeWatchdogScanResult
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if ok, err := lockEmployeeReminder(ctx, tx, c.scope.WorkspaceID, c.invitationID); err != nil || !ok {
		return out, err
	}
	f, ok, err := loadEmployeeReminderFacts(ctx, tx, c.scope, c.invitationID)
	if err != nil || !ok {
		return out, err
	}
	if reason := f.stopReason(now); reason != "" {
		if out.Skipped == nil {
			out.Skipped = map[string]int{}
		}
		out.Skipped["reminder_"+reason]++
		return out, nil
	}
	if !f.due(now) {
		return out, nil
	}
	ordinal := f.sent + 1
	reminderID := uuid.NewString()
	body := ComposeInvitationReminder(f.invitation.Question, f.invitation.TargetSceneKind)
	state, reason, actionID := "", "", ""
	target, err := resolveEmployeeReminderTarget(ctx, tx, f)
	var hold *EmployeeWatchdogHold
	switch {
	case errors.As(err, &hold):
		state, reason = "held", hold.Reason
	case err != nil:
		return out, err
	}
	if state == "" {
		kind := scene.KindDM
		if target.input.IsGroup {
			kind = scene.KindGroup
		}
		held, err := employeeReminderEgress(ctx, tx, f, kind, body)
		if err != nil {
			return out, err
		}
		if held != "" {
			state, reason = "held", held
		}
	}
	if state == "" {
		in := target.input
		in.Text = body
		if actionID, err = w.Outbox.EnqueueSceneNotice(ctx, tx, in, reminderID); err != nil {
			return out, err
		}
		// The reminder becomes later dialogue of the participant's scene.
		if err = employeeentry.RecordHostNotice(ctx, tx, employeeentry.HostNotice{ActionID: actionID, Scope: employeeentry.Scope{WorkspaceID: c.scope.WorkspaceID, AgentID: c.scope.AgentID, TenantOrgID: c.scope.TenantOrgID, SceneID: f.invitation.TargetSceneID},
			PrincipalID: target.principalID, SourceKind: employeeentry.HostNoticeWatchdog, SourceID: reminderID}); err != nil {
			return out, err
		}
		state = "enqueued"
	}
	var action *string
	if actionID != "" {
		action = &actionID
	}
	if _, err = tx.Exec(ctx, `INSERT INTO employee_task_invitation_reminder
 (id,workspace_id,agent_id,tenant_org_id,task_id,collection_id,invitation_id,target_scene_id,ordinal,body,state,reason,action_id)
 VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,$7::uuid,$8::uuid,$9,$10,$11,$12,$13)`,
		reminderID, c.scope.WorkspaceID, c.scope.AgentID, c.scope.TenantOrgID, f.collection.TaskID, f.collection.ID, f.invitation.ID, f.invitation.TargetSceneID, ordinal, body, state, reason, action); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return EmployeeWatchdogScanResult{}, err
	}
	if state == "enqueued" {
		out.Enqueued++
	} else {
		out.Held++
	}
	slog.InfoContext(ctx, "employee invitation reminder recorded", "event", "employee_invitation_reminder_recorded", "workspace_id", c.scope.WorkspaceID, "agent_id", c.scope.AgentID,
		"task_id", f.collection.TaskID, "collection_id", f.collection.ID, "invitation_id", f.invitation.ID, "target_scene_id", f.invitation.TargetSceneID,
		"reminder_id", reminderID, "ordinal", ordinal, "max_count", f.policy.MaxCount, "state", state, "reason", reason, "action_id", actionID)
	return out, nil
}

// beforeReminderSend fences a reminder immediately before the provider send:
// the reminder must still be the enqueued intent for this action, the
// invitation still waiting for this participant, and the target unchanged.
func (w *EmployeeWatchdog) beforeReminderSend(ctx context.Context, in dingtalkresponse.ActionInput) (bool, error) {
	var workspaceID, agentID, tenant, invitationID string
	err := w.DB.QueryRow(ctx, `SELECT workspace_id::text,agent_id::text,tenant_org_id,invitation_id::text FROM employee_task_invitation_reminder WHERE id=$1::uuid`, in.SceneNoticeID).
		Scan(&workspaceID, &agentID, &tenant, &invitationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	now, err := w.now(ctx)
	if err != nil {
		return true, err
	}
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	suppress := func(reason string) (bool, error) {
		if _, err := tx.Exec(ctx, `UPDATE employee_task_invitation_reminder SET state='suppressed',reason=$2,updated_at=now() WHERE id=$1::uuid AND state='enqueued'`, in.SceneNoticeID, reason); err != nil {
			return true, err
		}
		if err := tx.Commit(ctx); err != nil {
			return true, err
		}
		slog.InfoContext(ctx, "employee invitation reminder suppressed", "event", "employee_invitation_reminder_suppressed", "workspace_id", workspaceID, "agent_id", agentID,
			"invitation_id", invitationID, "reminder_id", in.SceneNoticeID, "action_id", in.ActionID, "reason", reason)
		return true, &dingtalkresponse.SuppressSendError{Reason: reason}
	}
	locked, err := lockEmployeeReminder(ctx, tx, workspaceID, invitationID)
	if err != nil {
		return true, err
	}
	if !locked {
		return suppress("workspace_removed")
	}
	var state, reason, body, action string
	if err = tx.QueryRow(ctx, `SELECT state,reason,body,COALESCE(action_id,'') FROM employee_task_invitation_reminder WHERE id=$1::uuid FOR UPDATE`, in.SceneNoticeID).
		Scan(&state, &reason, &body, &action); err != nil {
		return true, err
	}
	if state != "enqueued" {
		if reason == "" {
			reason = "reminder_" + state
		}
		return suppress(reason)
	}
	if action != in.ActionID || body != in.Text {
		return suppress("reminder_action_mismatch")
	}
	scope := taskinput.Scope{WorkspaceID: workspaceID, AgentID: agentID, TenantOrgID: tenant}
	f, ok, err := loadEmployeeReminderFacts(ctx, tx, scope, invitationID)
	if err != nil {
		return true, err
	}
	if !ok {
		return suppress("reminder_authority_removed")
	}
	if stop := f.stopReason(now); stop != "" {
		return suppress(stop)
	}
	target, err := resolveEmployeeReminderTarget(ctx, tx, f)
	var hold *EmployeeWatchdogHold
	if errors.As(err, &hold) {
		return suppress(hold.Reason)
	}
	if err != nil {
		return true, err
	}
	if !employeeWatchdogTargetMatches(in, target.input) {
		return suppress("reminder_target_changed")
	}
	return true, tx.Commit(ctx)
}
