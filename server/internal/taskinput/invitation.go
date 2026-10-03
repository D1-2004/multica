package taskinput

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

var renderedHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DeliveryStep is what the Host may do next for an invitation's delivery.
type DeliveryStep string

const (
	// StepAwaitOutbox: the send intent exists and the outbox owns the attempt.
	// A crashed or in-flight attempt is resolved by querying its action id,
	// never by starting a second send.
	StepAwaitOutbox DeliveryStep = "await_outbox"
	// StepQueryAction: the provider outcome is unknown; query the original
	// action id and record sent or failed.
	StepQueryAction DeliveryStep = "query_action"
	// StepMayRetry: the provider definitely did not deliver; RetryInviteDeliveryTx
	// mints a new action id.
	StepMayRetry DeliveryStep = "may_retry"
	// StepHeld: the egress scan held the bytes; the requester decides.
	StepHeld DeliveryStep = "held"
	// StepResolveScene: sent to a person without a known DM scene; resolve
	// the DM scene from the provider receipt and backfill it. Never resend.
	StepResolveScene DeliveryStep = "resolve_scene"
	// StepNone: delivered, or the invitation ended; nothing is ever sent.
	StepNone DeliveryStep = "none"
)

// NextDeliveryStep applies GawkBot's rule: a SENT or PENDING delivery is never
// sent again; only a definite failure allows a fresh attempt.
func NextDeliveryStep(inv Invitation) DeliveryStep {
	if inv.State == InvitationPendingScene && inv.DeliveryOutcome == DeliverySent {
		return StepResolveScene
	}
	if inv.State != InvitationPendingDelivery && inv.State != InvitationPendingScene {
		return StepNone
	}
	switch inv.DeliveryOutcome {
	case DeliveryPending:
		return StepAwaitOutbox
	case DeliveryUnknown:
		return StepQueryAction
	case DeliveryFailed:
		return StepMayRetry
	case DeliveryHeld:
		return StepHeld
	default:
		return StepNone
	}
}

// RecordInviteDeliveryTx records the provider fact for the invitation's
// current action. Sent moves pending_delivery to delivered with the provider
// message id (the reply-chain anchor) and the rendered hash that passed the
// egress scan. Unknown keeps the attempt pending for a query. A send that
// raced with a close is still recorded as a fact without reopening anything.
// A replay of the same fact is a no-op; a different fact for a settled action
// conflicts.
func (s *Store) RecordInviteDeliveryTx(ctx context.Context, scope Scope, p RecordDeliveryParams) (Invitation, error) {
	if err := validateScope(scope); err != nil {
		return Invitation{}, err
	}
	if !validUUID(p.InvitationID) || !validRef(p.ActionID, 128) || !validReason(p.Error, false) {
		return Invitation{}, ErrInvalid
	}
	if p.TargetSceneID != "" && (!validUUID(p.TargetSceneID) || p.Outcome != DeliverySent) {
		return Invitation{}, fmt.Errorf("%w: only a sent delivery can name the resolved scene", ErrInvalid)
	}
	switch p.Outcome {
	case DeliverySent:
		if !validRef(p.ProviderMessageID, maxProviderIDLen) || !renderedHashPattern.MatchString(p.RenderedHash) {
			return Invitation{}, fmt.Errorf("%w: sent needs the provider message id and rendered hash", ErrInvalid)
		}
	case DeliveryHeld:
		if !renderedHashPattern.MatchString(p.RenderedHash) || p.ProviderMessageID != "" {
			return Invitation{}, ErrInvalid
		}
	case DeliveryFailed, DeliveryUnknown:
		if p.ProviderMessageID != "" {
			return Invitation{}, ErrInvalid
		}
	default:
		return Invitation{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Invitation{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockWorkspace(ctx, tx, scope.WorkspaceID); err != nil {
		return Invitation{}, err
	}
	inv, err := loadInvitation(ctx, tx, scope, p.InvitationID, true)
	if err != nil {
		return Invitation{}, err
	}
	if inv.DeliveryActionID != p.ActionID {
		return Invitation{}, fmt.Errorf("%w: stale delivery action", ErrConflict)
	}
	backfill := false
	if p.TargetSceneID != "" && p.TargetSceneID != inv.TargetSceneID {
		if inv.TargetSceneID != "" {
			return Invitation{}, fmt.Errorf("%w: the invitation scene is already resolved", ErrConflict)
		}
		// The resolved scene comes from the directory, as a 1:1 scene of
		// this agent and tenant; it is set once.
		kind, err := sceneKind(ctx, tx, scope, p.TargetSceneID)
		if err != nil {
			return Invitation{}, err
		}
		if kind != sceneKindDM || inv.TargetSceneKind != sceneKindDM {
			return Invitation{}, fmt.Errorf("%w: a pending scene resolves to a 1:1 scene", ErrInvalid)
		}
		backfill = true
	}
	switch inv.DeliveryOutcome {
	case DeliverySent:
		if p.Outcome != DeliverySent || p.ProviderMessageID != inv.ProviderMessageID || p.RenderedHash != inv.RenderedHash {
			return Invitation{}, ErrConflict
		}
		if !backfill {
			return inv.Invitation, tx.Commit(ctx)
		}
	case DeliveryHeld:
		if p.Outcome == DeliveryHeld && p.RenderedHash == inv.RenderedHash {
			return inv.Invitation, tx.Commit(ctx)
		}
		return Invitation{}, ErrConflict
	case DeliveryFailed:
		if p.Outcome == DeliveryFailed {
			return inv.Invitation, tx.Commit(ctx)
		}
		return Invitation{}, ErrConflict
	}
	var updated invitationRow
	switch p.Outcome {
	case DeliverySent:
		// Right-hand expressions read the old row: a pending_scene invitation
		// becomes delivered only once its scene is known.
		updated, err = scanInvitation(tx.QueryRow(ctx, `UPDATE employee_task_invitation SET delivery_outcome='sent',
 provider_message_id=$5, rendered_hash=$6, delivery_error='', delivered_at=COALESCE(delivered_at, now()), updated_at=now(),
 target_scene_id=COALESCE(target_scene_id, NULLIF($7,'')::uuid),
 delivery_state=CASE
   WHEN delivery_state='pending_delivery' THEN 'delivered'
   WHEN delivery_state='pending_scene' AND COALESCE(target_scene_id, NULLIF($7,'')::uuid) IS NOT NULL THEN 'delivered'
   ELSE delivery_state END
 WHERE `+scopeWhere+` AND id=$4::uuid RETURNING `+invitationColumns, scopeArgs(scope, inv.ID, p.ProviderMessageID, p.RenderedHash, p.TargetSceneID)...))
	case DeliveryHeld:
		updated, err = scanInvitation(tx.QueryRow(ctx, `UPDATE employee_task_invitation SET delivery_outcome='held',
 rendered_hash=$5, delivery_error=$6, updated_at=now() WHERE `+scopeWhere+` AND id=$4::uuid RETURNING `+invitationColumns,
			scopeArgs(scope, inv.ID, p.RenderedHash, p.Error)...))
	default:
		updated, err = scanInvitation(tx.QueryRow(ctx, `UPDATE employee_task_invitation SET delivery_outcome=$5,
 delivery_error=$6, updated_at=now() WHERE `+scopeWhere+` AND id=$4::uuid RETURNING `+invitationColumns,
			scopeArgs(scope, inv.ID, string(p.Outcome), p.Error)...))
	}
	if err != nil {
		return Invitation{}, err
	}
	return updated.Invitation, tx.Commit(ctx)
}

// RetryInviteDeliveryTx starts a new delivery attempt with a new action id,
// only after a definite failure. A pending or unknown attempt is
// ErrDeliveryPending (query it); a held one needs the requester. Replaying the
// same retry returns the already-minted attempt.
func (s *Store) RetryInviteDeliveryTx(ctx context.Context, scope Scope, collectionID string, p RetryDeliveryParams) (Invitation, error) {
	if err := validateScope(scope); err != nil {
		return Invitation{}, err
	}
	if !validUUID(collectionID) || !validUUID(p.InvitationID) || p.ExpectedAttempt < 1 {
		return Invitation{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Invitation{}, err
	}
	defer tx.Rollback(ctx)
	col, _, err := lockCollectionChain(ctx, tx, scope, collectionID)
	if err != nil {
		return Invitation{}, err
	}
	inv, err := loadInvitation(ctx, tx, scope, p.InvitationID, true)
	if err != nil {
		return Invitation{}, err
	}
	if inv.CollectionID != col.ID {
		return Invitation{}, ErrNotFound
	}
	pending := inv.State == InvitationPendingDelivery || inv.State == InvitationPendingScene
	if inv.DeliveryAttempt == p.ExpectedAttempt+1 && inv.DeliveryOutcome == DeliveryPending && pending {
		return inv.Invitation, tx.Commit(ctx)
	}
	if !col.State.Active() || inv.State == InvitationRevoked || inv.State == InvitationExpired {
		return Invitation{}, ErrClosed
	}
	if !pending || inv.DeliveryAttempt != p.ExpectedAttempt {
		return Invitation{}, ErrConflict
	}
	switch inv.DeliveryOutcome {
	case DeliveryFailed:
	case DeliveryHeld:
		return Invitation{}, ErrForbidden
	default:
		return Invitation{}, ErrDeliveryPending
	}
	if inv.DeliveryAttempt >= maxAttempts {
		return Invitation{}, fmt.Errorf("%w: delivery attempts exhausted", ErrClosed)
	}
	next := inv.DeliveryAttempt + 1
	updated, err := scanInvitation(tx.QueryRow(ctx, `UPDATE employee_task_invitation SET delivery_attempt=$5, delivery_action_id=$6,
 delivery_outcome='pending', delivery_error='', updated_at=now() WHERE `+scopeWhere+` AND id=$4::uuid RETURNING `+invitationColumns,
		scopeArgs(scope, inv.ID, next, DeliveryActionID(inv.ID, next))...))
	if err != nil {
		return Invitation{}, err
	}
	return updated.Invitation, tx.Commit(ctx)
}

// GetInvitation returns an invitation with its delivery facts for the Host
// (outbox, reconciler). It is not a participant view.
func (s *Store) GetInvitation(ctx context.Context, scope Scope, id string) (Invitation, error) {
	if err := validateScope(scope); err != nil {
		return Invitation{}, err
	}
	if !validUUID(id) {
		return Invitation{}, ErrInvalid
	}
	inv, err := loadInvitation(ctx, s.db, scope, id, false)
	return inv.Invitation, err
}

func dbNow(ctx context.Context, db DB) (time.Time, error) {
	var now time.Time
	err := db.QueryRow(ctx, `SELECT now()`).Scan(&now)
	return now, err
}

// effectiveState reports an elapsed explicit expiry without waiting for a sweep.
func effectiveState(inv Invitation, now time.Time) InvitationState {
	if (inv.State == InvitationPendingDelivery || inv.State == InvitationDelivered) && inv.ExpiresAt != nil && !now.Before(*inv.ExpiresAt) {
		return InvitationExpired
	}
	return inv.State
}

// effectiveAnswer loads the authorized effective answer of an answered
// invitation; revoked or expired invitations expose no answer.
func effectiveAnswer(ctx context.Context, db DB, scope Scope, inv Invitation) (*Answer, error) {
	if inv.State != InvitationAnswered || inv.EffectiveVersion == 0 {
		return nil, nil
	}
	in, err := scanInput(db.QueryRow(ctx, `SELECT `+inputColumns+` FROM employee_task_input WHERE `+scopeWhere+`
 AND collection_id=$4::uuid AND invitation_id=$5::uuid AND version=$6`, scopeArgs(scope, inv.CollectionID, inv.ID, inv.EffectiveVersion)...))
	if err != nil {
		return nil, err
	}
	return &Answer{Version: in.Version, Corrected: in.Version > 1, Body: in.Body, BodyRef: in.BodyRef, ProviderMessageID: in.ProviderMessageID, OccurredAt: in.OccurredAt}, nil
}

func validateParticipantViewer(v ParticipantViewer) error {
	if !validRef(v.ActorRef, maxActorBytes) || !validUUID(v.SceneID) {
		return ErrInvalid
	}
	return nil
}

func (s *Store) participantView(ctx context.Context, scope Scope, inv Invitation, now time.Time) (ParticipantView, error) {
	answer, err := effectiveAnswer(ctx, s.db, scope, inv)
	if err != nil {
		return ParticipantView{}, err
	}
	view := ParticipantView{InvitationID: inv.ID, Question: inv.Question, State: effectiveState(inv, now), ExpiresAt: inv.ExpiresAt, Answer: answer}
	var deadlineAt *time.Time
	var tz string
	err = s.db.QueryRow(ctx, `SELECT deadline_at, deadline_tz FROM employee_task_collection WHERE `+scopeWhere+` AND id=$4::uuid`, scopeArgs(scope, inv.CollectionID)...).Scan(&deadlineAt, &tz)
	if err != nil {
		return ParticipantView{}, mapError(err)
	}
	if deadlineAt != nil {
		view.Deadline = &Deadline{At: *deadlineAt, TimeZone: tz}
	}
	return view, nil
}

// ReadParticipantInvitation is the participant's own view: its question, its
// effective answer and status. Another participant, another scene, or another
// tenant gets ErrNotFound; the Task, origin and other slots are never exposed.
func (s *Store) ReadParticipantInvitation(ctx context.Context, scope Scope, invitationID string, viewer ParticipantViewer) (ParticipantView, error) {
	if err := validateScope(scope); err != nil {
		return ParticipantView{}, err
	}
	if !validUUID(invitationID) {
		return ParticipantView{}, ErrInvalid
	}
	if err := validateParticipantViewer(viewer); err != nil {
		return ParticipantView{}, err
	}
	inv, err := loadInvitation(ctx, s.db, scope, invitationID, false)
	if err != nil {
		return ParticipantView{}, err
	}
	if inv.ParticipantRef != viewer.ActorRef || inv.TargetSceneID != viewer.SceneID || inv.State == InvitationPendingDelivery || inv.State == InvitationPendingScene {
		return ParticipantView{}, ErrNotFound
	}
	now, err := dbNow(ctx, s.db)
	if err != nil {
		return ParticipantView{}, err
	}
	return s.participantView(ctx, scope, inv.Invitation, now)
}

// ListParticipantInvitations returns the viewer's own delivered invitations in
// its scene for active collections: what a B-scene wake may show.
func (s *Store) ListParticipantInvitations(ctx context.Context, scope Scope, viewer ParticipantViewer) ([]ParticipantView, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	if err := validateParticipantViewer(viewer); err != nil {
		return nil, err
	}
	invitations, err := s.queryInvitations(ctx, `SELECT `+invitationColumns+` FROM employee_task_invitation
 WHERE `+scopeWhere+` AND participant_ref=$4 AND target_scene_id=$5::uuid AND delivery_state IN ('delivered','answered')
 AND collection_id IN (SELECT id FROM employee_task_collection WHERE `+scopeWhere+` AND state IN ('open','ready','summarizing'))
 ORDER BY created_at, id LIMIT $6`, scopeArgs(scope, viewer.ActorRef, viewer.SceneID, maxInvitations)...)
	if err != nil {
		return nil, err
	}
	now, err := dbNow(ctx, s.db)
	if err != nil {
		return nil, err
	}
	out := make([]ParticipantView, 0, len(invitations))
	for _, inv := range invitations {
		view, err := s.participantView(ctx, scope, inv, now)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, nil
}

// BindingCandidates returns the participant's own invitations that can still
// receive an answer or correction (delivered or answered, active collection,
// not past an explicit expiry), across scenes, for BindAnswer.
func (s *Store) BindingCandidates(ctx context.Context, scope Scope, participantRef string) ([]BindingCandidate, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	if !validRef(participantRef, maxActorBytes) {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT i.id::text, i.collection_id::text, i.task_id::text, i.target_scene_id::text, i.participant_ref,
 i.provider_message_id, i.delivery_state, c.revision, i.question
 FROM employee_task_invitation i
 JOIN employee_task_collection c ON c.id=i.collection_id AND c.workspace_id=i.workspace_id AND c.agent_id=i.agent_id AND c.tenant_org_id=i.tenant_org_id
 WHERE i.workspace_id=$1::uuid AND i.agent_id=$2::uuid AND i.tenant_org_id=$3 AND i.participant_ref=$4
 AND i.delivery_state IN ('delivered','answered') AND c.state IN ('open','ready','summarizing')
 AND (i.expires_at IS NULL OR i.expires_at > now() OR i.delivery_state='answered')
 ORDER BY i.created_at, i.id LIMIT 64`, scopeArgs(scope, participantRef)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BindingCandidate{}
	for rows.Next() {
		var c BindingCandidate
		if err := rows.Scan(&c.InvitationID, &c.CollectionID, &c.TaskID, &c.TargetSceneID, &c.ParticipantRef, &c.ProviderMessageID, &c.State, &c.Revision, &c.Question); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) queryInvitations(ctx context.Context, sql string, args ...any) ([]Invitation, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		inv, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv.Invitation)
	}
	return out, rows.Err()
}
