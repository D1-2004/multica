package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/taskinput"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Suppression codes the outbox records for an invitation send refused by
// BeforeCollectionInviteSend; the reconciler maps them back to the invitation.
const (
	employeeInviteSuppressClosed = "invitation_closed"
	employeeInviteSuppressHeld   = "invitation_egress_held"
	employeeInviteSuppressTarget = "invitation_target_revoked"
)

// ---- Outbox fence ----

// BeforeCollectionInviteSend re-checks an invitation immediately before the
// provider submission: the invitation and its collection must still be
// active under this exact action id, the target scene and identity must be
// current, and the exact bytes must pass the final egress scan. Every other
// action falls through to next unchanged.
func (h *Handler) BeforeCollectionInviteSend(next func(context.Context, dingtalkresponse.ActionInput) error) func(context.Context, dingtalkresponse.ActionInput) error {
	return func(ctx context.Context, in dingtalkresponse.ActionInput) error {
		if in.InvitationActionID == "" {
			if next == nil {
				return nil
			}
			return next(ctx, in)
		}
		if h == nil || h.DB == nil {
			return errors.New("collection invitation fence is unavailable")
		}
		var state, collectionState, taskState, tenant, sceneKind, sceneID string
		err := h.DB.QueryRow(ctx, `SELECT i.delivery_state, c.state, t.state, i.tenant_org_id, i.target_scene_kind, COALESCE(i.target_scene_id::text,'')
 FROM employee_task_invitation i
 JOIN employee_task_collection c ON c.id=i.collection_id AND c.workspace_id=i.workspace_id AND c.agent_id=i.agent_id AND c.tenant_org_id=i.tenant_org_id
 JOIN employee_task t ON t.id=i.task_id AND t.workspace_id=i.workspace_id AND t.agent_id=i.agent_id AND t.tenant_org_id=i.tenant_org_id
 WHERE i.delivery_action_id=$1 AND i.workspace_id=$2::uuid AND i.agent_id=$3::uuid`, in.InvitationActionID, in.WorkspaceID, in.AgentID).Scan(&state, &collectionState, &taskState, &tenant, &sceneKind, &sceneID)
		if errors.Is(err, pgx.ErrNoRows) {
			return &dingtalkresponse.SuppressSendError{Reason: employeeInviteSuppressClosed}
		}
		if err != nil {
			return err
		}
		if (state != string(taskinput.InvitationPendingDelivery) && state != string(taskinput.InvitationPendingScene)) || !taskinput.CollectionState(collectionState).Active() || taskState == string(employeetask.StateCancelled) || tenant != in.DWSOrgID {
			return &dingtalkresponse.SuppressSendError{Reason: employeeInviteSuppressClosed}
		}
		if sceneID != in.SceneID || (sceneID != "" && in.ConversationID == "") {
			return &dingtalkresponse.SuppressSendError{Reason: employeeInviteSuppressTarget}
		}
		if sceneID != "" {
			registered, err := employeeSceneFence(ctx, h, employeeentry.Job{Scope: employeeentry.Scope{WorkspaceID: in.WorkspaceID, AgentID: in.AgentID, TenantOrgID: tenant, SceneID: sceneID}})
			if errors.Is(err, scene.ErrNotFound) || errors.Is(err, scene.ErrStaleTenant) || errors.Is(err, scene.ErrUnresolved) {
				return &dingtalkresponse.SuppressSendError{Reason: employeeInviteSuppressTarget}
			}
			if err != nil {
				return err
			}
			if registered.ExternalSceneID != in.ConversationID || registered.SceneKind != sceneKind {
				return &dingtalkresponse.SuppressSendError{Reason: employeeInviteSuppressTarget}
			}
		}
		identity, err := h.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: parseUUID(in.WorkspaceID), AgentID: parseUUID(in.AgentID)})
		if errors.Is(err, pgx.ErrNoRows) {
			return &dingtalkresponse.SuppressSendError{Reason: employeeInviteSuppressTarget}
		}
		if err != nil {
			return err
		}
		if identity.DwsUid != in.DWSUID {
			return &dingtalkresponse.SuppressSendError{Reason: employeeInviteSuppressTarget}
		}
		audience, _ := taskinput.AudienceForSceneKind(sceneKind)
		if check := taskinput.CheckEgress(taskinput.EgressInput{Rendered: in.Text, Audience: audience, TargetSceneKind: sceneKind}); check.Held {
			return &dingtalkresponse.SuppressSendError{Reason: employeeInviteSuppressHeld}
		}
		return nil
	}
}

func employeeRenderedHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// ---- Reconciler ----

// ReconcileEmployeeCollections is the periodic, idempotent PostgreSQL
// consumer of the collection ledger: it copies invitation delivery facts from
// the response outbox (and resolves a pending DM scene from its receipt), then
// admits one collection.ready wake per pending ready intent.
func (w *EmployeeSceneWorker) ReconcileEmployeeCollections(ctx context.Context, limit int) (int, error) {
	if w == nil || w.handler == nil || w.handler.TxStarter == nil {
		return 0, nil
	}
	synced, err := w.syncInvitationDeliveries(ctx, limit)
	if err != nil {
		return synced, err
	}
	admitted, err := w.admitCollectionWakes(ctx, limit)
	return synced + admitted, err
}

type employeeInviteDelivery struct {
	scope                      taskinput.Scope
	invitationID, actionID     string
	state, outcome, sceneID    string
	actionState, messageID     string
	conversationID, code, text string
}

func (w *EmployeeSceneWorker) syncInvitationDeliveries(ctx context.Context, limit int) (int, error) {
	database, ok := employeeEntryDB(w.handler)
	if !ok {
		return 0, errors.New("employee task storage is unavailable")
	}
	rows, err := database.Query(ctx, `SELECT i.workspace_id::text, i.agent_id::text, i.tenant_org_id, i.id::text, i.delivery_action_id, i.delivery_state, i.delivery_outcome,
 COALESCE(i.target_scene_id::text,''), a.state, a.provider_message_id, a.provider_conversation_id, a.error_code, COALESCE(a.input->>'text','')
 FROM employee_task_invitation i JOIN response_action a ON a.id=i.delivery_action_id AND a.workspace_id=i.workspace_id AND a.agent_id=i.agent_id
 WHERE (i.delivery_outcome IN ('pending','unknown') AND a.state IN ('delivered','failed','cancelled','unknown') AND NOT (a.state='unknown' AND i.delivery_outcome='unknown'))
 OR (i.delivery_state='pending_scene' AND i.delivery_outcome='sent' AND a.state='delivered')
 ORDER BY i.updated_at LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	var pending []employeeInviteDelivery
	for rows.Next() {
		var d employeeInviteDelivery
		if err := rows.Scan(&d.scope.WorkspaceID, &d.scope.AgentID, &d.scope.TenantOrgID, &d.invitationID, &d.actionID, &d.state, &d.outcome, &d.sceneID, &d.actionState, &d.messageID, &d.conversationID, &d.code, &d.text); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, d)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	done := 0
	for _, d := range pending {
		if err := w.recordInviteDelivery(ctx, d); err != nil {
			slog.WarnContext(ctx, "collection invitation delivery sync failed", "invitation_id", d.invitationID, "error", err)
			continue
		}
		done++
	}
	return done, nil
}

func (w *EmployeeSceneWorker) recordInviteDelivery(ctx context.Context, d employeeInviteDelivery) error {
	p := taskinput.RecordDeliveryParams{InvitationID: d.invitationID, ActionID: d.actionID, Error: d.code}
	switch d.actionState {
	case "delivered":
		if d.messageID == "" {
			return nil
		}
		p.Outcome, p.ProviderMessageID, p.RenderedHash, p.Error = taskinput.DeliverySent, d.messageID, employeeRenderedHash(d.text), ""
	case "failed":
		p.Outcome = taskinput.DeliveryFailed
	case "cancelled":
		p.Outcome = taskinput.DeliveryFailed
		if strings.HasSuffix(d.code, employeeInviteSuppressHeld) {
			p.Outcome, p.RenderedHash = taskinput.DeliveryHeld, employeeRenderedHash(d.text)
		}
	case "unknown":
		p.Outcome = taskinput.DeliveryUnknown
	default:
		return nil
	}
	tx, err := w.handler.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	q := db.New(tx)
	if p.Outcome == taskinput.DeliverySent && d.sceneID == "" && d.state == string(taskinput.InvitationPendingScene) {
		if d.conversationID == "" {
			return nil
		}
		// The DM scene comes from the conversation the provider delivered to,
		// resolved through the directory like any admitted conversation.
		owner := scene.Owner{WorkspaceID: parseUUID(d.scope.WorkspaceID), AgentID: parseUUID(d.scope.AgentID)}
		registered, err := scene.Resolve(ctx, q, owner, scene.DingTalkConversation(d.scope.TenantOrgID, scene.KindDM, d.conversationID), scene.Observation{KindStated: true})
		if err != nil {
			return err
		}
		p.TargetSceneID = util.UUIDToString(registered.ID)
	}
	inv, err := taskinput.NewStore(tx).RecordInviteDeliveryTx(ctx, d.scope, p)
	if err != nil {
		return err
	}
	if p.TargetSceneID != "" && inv.TargetSceneID == p.TargetSceneID {
		principal, err := employeeHistoryPrincipal(ctx, q, employeeentry.Scope{WorkspaceID: d.scope.WorkspaceID, AgentID: d.scope.AgentID, TenantOrgID: d.scope.TenantOrgID, SceneID: p.TargetSceneID})
		if err != nil {
			return err
		}
		if err = employeeentry.RecordHostNotice(ctx, tx, employeeentry.HostNotice{ActionID: d.actionID, Scope: employeeentry.Scope{WorkspaceID: d.scope.WorkspaceID, AgentID: d.scope.AgentID, TenantOrgID: d.scope.TenantOrgID, SceneID: p.TargetSceneID},
			PrincipalID: principal, SourceKind: employeeentry.HostNoticeInvitation, SourceID: inv.ID}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// admitCollectionWakes turns each pending ready intent into one typed
// collection.ready wake of the origin scene, in the same transaction that
// marks the intent admitted. Its source identity and occurred_at are the
// intent's frozen ones, so a retry or another replica admits the same wake.
func (w *EmployeeSceneWorker) admitCollectionWakes(ctx context.Context, limit int) (int, error) {
	database, ok := employeeEntryDB(w.handler)
	if !ok {
		return 0, errors.New("employee task storage is unavailable")
	}
	intents, err := taskinput.NewStore(database).ListPendingReadyIntents(ctx, limit)
	if err != nil || len(intents) == 0 {
		return 0, err
	}
	if ready, err := w.TaskWakeProducerReady(ctx); err != nil || !ready {
		// Producers wait for every replica; the intent stays pending.
		return 0, nil
	}
	admitted := 0
	for _, intent := range intents {
		ok, err := w.admitCollectionWake(ctx, intent)
		if errors.Is(err, employeeentry.ErrTaskWakeNotReady) {
			return admitted, nil
		}
		if err != nil {
			slog.WarnContext(ctx, "collection ready wake admission failed", "collection_id", intent.CollectionID, "revision", intent.CollectionRevision, "error", err)
			continue
		}
		if ok {
			admitted++
		}
	}
	if admitted > 0 {
		w.Notify()
	}
	return admitted, nil
}

// employeeCollectionWakeSource is the wake producer source of collections.
const employeeCollectionWakeSource = "employee.collection"

func (w *EmployeeSceneWorker) admitCollectionWake(ctx context.Context, intent taskinput.ReadyIntent) (bool, error) {
	tx, err := w.handler.TxStarter.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('taskinput.ready:'||$1, 0))`, intent.CollectionID).Scan(&locked); err != nil || !locked {
		return false, err
	}
	scope := employeeentry.Scope{WorkspaceID: intent.Scope.WorkspaceID, AgentID: intent.Scope.AgentID, TenantOrgID: intent.Scope.TenantOrgID, SceneID: intent.OriginSceneID}
	collections := taskinput.NewStore(tx)
	col, err := collections.GetCollection(ctx, intent.Scope, intent.CollectionID)
	if err != nil {
		return false, err
	}
	admittedRef := employeeentry.TaskWakeSourcePrefix + employeeCollectionWakeSource + ":" + intent.EventID
	// Lock order workspace -> Task (FOR UPDATE) -> collection before the wake
	// admission locks the origin scene; the Task lock is then already held.
	if _, _, err = collections.MarkReadyIntentAdmittedTx(ctx, intent.Scope, intent.CollectionID, intent.CollectionRevision, admittedRef); err != nil {
		if errors.Is(err, taskinput.ErrConflict) || errors.Is(err, taskinput.ErrClosed) {
			return false, nil
		}
		return false, err
	}
	task, err := employeetask.NewStore(tx).Get(ctx, employeetask.Scope{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: scope.SceneID}}, intent.TaskID)
	if err != nil {
		return false, err
	}
	admission := employeeentry.TaskWakeAdmission{Scope: scope, Source: employeeCollectionWakeSource, EventID: intent.EventID, OccurredAt: intent.OccurredAt, Wake: employeeentry.TaskWake{
		SchemaVersion: employeeentry.TaskWakeSchemaVersion, Kind: employeeentry.TaskWakeCollectionReady, TaskID: task.ID, GoalRevision: task.GoalRevision, InputSeq: task.LastEntrySeq,
		AuthorityRef: col.AuthorityRef, EvidenceRef: intent.EvidenceRef()}}
	if _, err = employeeentry.NewStore(tx).AdmitTaskWake(ctx, w, admission); err != nil {
		var hold *employeeentry.TaskOriginHold
		if errors.As(err, &hold) || errors.Is(err, employeeentry.ErrTaskWakeOrigin) || errors.Is(err, employeeentry.ErrTaskWakeStopped) {
			// The origin can never be woken for this collection: end it so the
			// intent is superseded instead of retried forever.
			_ = tx.Rollback(ctx)
			reason := err.Error()
			if hold != nil {
				reason = hold.Reason
			}
			return false, w.revokeCollection(ctx, intent, reason)
		}
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (w *EmployeeSceneWorker) revokeCollection(ctx context.Context, intent taskinput.ReadyIntent, reason string) error {
	database, ok := employeeEntryDB(w.handler)
	if !ok {
		return errors.New("employee task storage is unavailable")
	}
	store := taskinput.NewStore(database)
	col, err := store.GetCollection(ctx, intent.Scope, intent.CollectionID)
	if err != nil || !col.State.Active() {
		return err
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	_, _, err = store.CloseCollectionTx(ctx, intent.Scope, taskinput.CloseParams{CollectionID: col.ID, Mode: taskinput.CloseRevoke, Reason: reason,
		Source:    taskinput.Source{Namespace: "host.collection_wake", Key: intent.EventID},
		Authority: taskinput.Authority{ActorRef: taskinput.HostActorPrefix + "collection-wake", SceneID: col.OriginSceneID, ReceiptRef: intent.EventID, VerifiedAt: time.Now()}, ExpectedRevision: col.Revision})
	if errors.Is(err, taskinput.ErrClosed) || errors.Is(err, taskinput.ErrConflict) {
		return nil
	}
	return err
}

// ---- collection.ready wake ----

// employeeCollectionWakeView is the summary input of a collection.ready wake:
// the origin's authorized answers at the wake's frozen revision, as data.
type employeeCollectionWakeView struct {
	State        string                          `json:"state"`
	Expected     int                             `json:"expected"`
	Received     int                             `json:"received"`
	ClosedEarly  bool                            `json:"closed_early_by_requester,omitempty"`
	CloseReason  string                          `json:"close_reason,omitempty"`
	Responses    []employeeCollectionSlotView    `json:"responses"`
	ProcessFacts *employeeCollectionProcessFacts `json:"process_facts"`
}

// employeeCollectionEvidence parses a ready wake's "collection:<id>/<rev>".
func employeeCollectionEvidence(ref string) (string, int64, bool) {
	rest, ok := strings.CutPrefix(ref, "collection:")
	if !ok {
		return "", 0, false
	}
	id, rev, ok := strings.Cut(rest, "/")
	revision, err := strconv.ParseInt(rev, 10, 64)
	if !ok || err != nil || revision <= 0 {
		return "", 0, false
	}
	if _, err := util.ParseUUID(id); err != nil {
		return "", 0, false
	}
	return id, revision, true
}

func employeeCollectionView(ctx context.Context, database taskinput.DB, job employeeentry.Job, wake employeeentry.TaskWake) (taskinput.OriginView, error) {
	id, revision, ok := employeeCollectionEvidence(wake.EvidenceRef)
	if !ok {
		return taskinput.OriginView{}, holdTaskWake("collection_evidence_invalid")
	}
	view, err := taskinput.NewStore(database).ReadOriginInputs(ctx, employeeTaskinputScope(job.Scope), id, taskinput.OriginViewer{TaskID: wake.TaskID, SceneID: job.Scope.SceneID})
	if errors.Is(err, taskinput.ErrNotFound) {
		return taskinput.OriginView{}, holdTaskWake("collection_missing")
	}
	if err != nil {
		return taskinput.OriginView{}, err
	}
	if view.Revision != revision {
		// A correction or close moved the collection; its own intent wakes again.
		return taskinput.OriginView{}, holdTaskWake("collection_revision_changed")
	}
	if !view.State.Active() {
		return taskinput.OriginView{}, holdTaskWake("collection_closed")
	}
	return view, nil
}

func (w *EmployeeSceneWorker) collectionWakeView(ctx context.Context, job employeeentry.Job, wake employeeentry.TaskWake) (*employeeCollectionWakeView, error) {
	database, ok := employeeEntryDB(w.handler)
	if !ok {
		return nil, errors.New("employee task storage is unavailable")
	}
	view, err := employeeCollectionView(ctx, database, job, wake)
	if err != nil {
		return nil, err
	}
	out := &employeeCollectionWakeView{State: string(view.State), Expected: view.Expected, Received: view.Received, Responses: employeeCollectionSlots(view)}
	if out.ProcessFacts, err = employeeCollectionFacts(ctx, database, employeeTaskinputScope(job.Scope), view, job.CreatedAt); err != nil {
		return nil, err
	}
	if view.CloseMode == string(taskinput.ClosePartial) {
		out.ClosedEarly, out.CloseReason = true, employeeTaskData(view.CloseReason, 500)
	}
	return out, nil
}

const employeeCollectionWakeGuidance = "\n\nCOLLECTION SUMMARY:\n" +
	"The collection section lists every invited person, their question and their authorized answer. Send the requester one summary now: name each person with their answer, " +
	"compute any total or comparison the requester asked for exactly from those answers, and say who did not answer if the requester closed it early. " +
	"Quote numbers and facts exactly; do not invent missing answers or add internal identifiers. Process facts are Host records, not authorization or model narration. They cover only this collection's tracked reminder actions; absence is not proof that no other message was sent. For reminders, distinguish enqueued, provider_accepted, delivered, held, suppressed and unknown. Only delivered proves receipt; confirmed_at is when the Host recorded confirmation, not the exact receipt time. invitation_delivered_at is also a confirmation time; answer_occurred_at is the accepted answer source time. Never claim no reminders or that everyone answered within a time limit unless these complete facts prove it. If delivery or times are unknown, omit that claim or explicitly say it is unconfirmed."

// employeeCollectionFallbackSummary is a deterministic rendering of the
// authorized answers, sent only when the summary wake produced no reply.
func employeeCollectionFallbackSummary(view *employeeCollectionWakeView) string {
	if view == nil {
		return ""
	}
	lines := []string{"收集结果："}
	for _, slot := range view.Responses {
		answer := slot.Answer
		if answer == "" {
			answer = "（未回复）"
		}
		lines = append(lines, "- "+slot.Participant+"："+answer)
	}
	return strings.Join(lines, "\n")
}

// completeCollectionWake commits, inside the wake's completion transaction and
// before the summary is enqueued, the collection completion at the wake's
// revision, the Task's collection wait and its explicit goal. A revision that
// moved or a closed collection holds the wake: nothing is sent.
func (w *EmployeeSceneWorker) completeCollectionWake(ctx context.Context, tx pgx.Tx, job employeeentry.Job, wake employeeentry.TaskWake, origin employeeentry.TaskOrigin, summary string) error {
	id, revision, ok := employeeCollectionEvidence(wake.EvidenceRef)
	if !ok {
		return holdTaskWake("collection_evidence_invalid")
	}
	scopeIn := employeeTaskinputScope(job.Scope)
	_, err := taskinput.NewStore(tx).CompleteCollectionTx(ctx, scopeIn, taskinput.CompleteParams{CollectionID: id, SummaryRef: "employee_scene_job:" + job.ID,
		Source: taskinput.Source{Namespace: employeeentry.TaskWakeSourcePrefix + employeeCollectionWakeSource, Key: job.ID}, ExpectedRevision: revision})
	switch {
	case errors.Is(err, taskinput.ErrStaleRevision), errors.Is(err, taskinput.ErrConflict):
		return holdTaskWake("collection_revision_changed")
	case errors.Is(err, taskinput.ErrClosed):
		return holdTaskWake("collection_closed")
	case err != nil:
		return err
	}
	taskScope := employeetask.Scope{WorkspaceID: job.Scope.WorkspaceID, AgentID: job.Scope.AgentID, TenantOrgID: job.Scope.TenantOrgID, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: job.Scope.SceneID}}
	evidence := "collection:" + id + "/" + strconv.FormatInt(revision, 10)
	// The goal is best effort inside a savepoint: a requester's newer input
	// keeps the goal open, but never blocks the summary they are owed.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	_, _, err = employeetask.ReadyTaskTx(ctx, sp, taskScope, wake.TaskID, employeetask.ReadyParams{Source: employeetask.Source{Namespace: employeeCollectionSourceNamespace, Key: job.ID + "/ready"},
		Kind: employeetask.WaitCollection, RefID: id, Outcome: employeetask.WaitSatisfied, EvidenceRef: evidence, AuthorityRef: wake.AuthorityRef})
	if err == nil || employeetask.ErrorCodeOf(err) != "" {
		var task employeetask.Task
		if task, err = employeetask.NewStore(sp).Get(ctx, taskScope, wake.TaskID); err == nil {
			_, _, err = employeetask.CompleteGoalTx(ctx, sp, taskScope, wake.TaskID, employeetask.CompleteGoalParams{Source: employeetask.Source{Namespace: employeeCollectionSourceNamespace, Key: job.ID + "/complete"},
				GoalRevision: wake.GoalRevision, InputSeq: wake.InputSeq, AuthorityRef: wake.AuthorityRef, EvidenceRef: evidence, Summary: employeeTaskData(summary, 4000), ExpectedVersion: task.Version})
		}
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		if employeetask.ErrorCodeOf(err) == "" && !errors.Is(err, employeetask.ErrConflict) && !errors.Is(err, employeetask.ErrInvalid) {
			return err
		}
		slog.WarnContext(ctx, "collection goal left open after its summary", "job_id", job.ID, "task_id", wake.TaskID, "error", err)
		return nil
	}
	return sp.Commit(ctx)
}
