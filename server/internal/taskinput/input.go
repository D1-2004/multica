package taskinput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

func validateAccept(scope Scope, p AcceptInputParams) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if err := validateSource(p.Source); err != nil {
		return err
	}
	if err := validateAuthority(p.Authority); err != nil {
		return err
	}
	if !validUUID(p.CollectionID) || !validUUID(p.InvitationID) || p.ExpectedRevision <= 0 || p.OccurredAt.IsZero() {
		return ErrInvalid
	}
	// Defense in depth for the binding rules: bots, this Agent, cards and
	// system notices never answer, whatever the caller bound.
	if !p.SenderKind.Answerer() || !p.MessageKind.Answerable() {
		return fmt.Errorf("%w: sender or message kind cannot answer", ErrInvalid)
	}
	switch p.Binding {
	case BindReplyChain, BindDMSinglePending, BindInviteReference:
	default:
		return fmt.Errorf("%w: binding", ErrInvalid)
	}
	if strings.TrimSpace(p.Body) == "" && strings.TrimSpace(p.BodyRef) == "" {
		return fmt.Errorf("%w: empty answer", ErrInvalid)
	}
	if len(p.Body) > maxAnswerBytes || !utf8.ValidString(p.Body) || (p.BodyRef != "" && !validRef(p.BodyRef, maxBodyRefBytes)) {
		return fmt.Errorf("%w: answer size", ErrInvalid)
	}
	if p.ProviderMessageID != "" && !validRef(p.ProviderMessageID, maxProviderIDLen) {
		return ErrInvalid
	}
	return nil
}

// AcceptInputTx records one answer from an invited participant, bound by the
// Host with BindAnswer, in one transaction with the slot count and, when the
// last mandatory slot fills, the unique ready intent.
//
// Contract:
//   - The same source with identical content returns the stored result
//     (Replayed); with different content it is ErrConflict.
//   - The actor and scene of Authority must be the invitation's participant and
//     target scene, in this tenant; otherwise ErrNotFound.
//   - A second answer to an answered invitation is ErrAlreadyAnswered unless it
//     is an explicit Correction, which adds a version, replaces the effective
//     slot, keeps the history and never counts the participant twice.
//   - A stopped Task, a closed collection, a revoked or expired invitation, or
//     an answer after the deadline/expiry is ErrClosed. Nothing reopens and no
//     new Task is created.
//   - ExpectedRevision must equal the collection revision the Host bound
//     against; a concurrent change is ErrConflict, and the Host re-binds.
func (s *Store) AcceptInputTx(ctx context.Context, scope Scope, p AcceptInputParams) (AcceptResult, error) {
	if err := validateAccept(scope, p); err != nil {
		return AcceptResult{}, err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return AcceptResult{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return AcceptResult{}, err
	}
	defer tx.Rollback(ctx)
	col, task, err := lockCollectionChain(ctx, tx, scope, p.CollectionID)
	if err != nil {
		return AcceptResult{}, err
	}
	if replay, ok, err := acceptReplay(ctx, tx, scope, p, payload); err != nil || ok {
		if err != nil {
			return AcceptResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return AcceptResult{}, err
		}
		return replay, nil
	}
	if task.stopped() || !col.State.Active() {
		return AcceptResult{}, ErrClosed
	}
	inv, err := loadInvitation(ctx, tx, scope, p.InvitationID, true)
	if err != nil {
		return AcceptResult{}, err
	}
	if inv.CollectionID != col.ID || inv.ParticipantRef != p.Authority.ActorRef {
		return AcceptResult{}, ErrNotFound
	}
	switch inv.State {
	case InvitationRevoked, InvitationExpired:
		return AcceptResult{}, ErrClosed
	case InvitationPendingDelivery, InvitationPendingScene:
		return AcceptResult{}, ErrDeliveryPending
	}
	if inv.TargetSceneID != p.Authority.SceneID {
		return AcceptResult{}, ErrNotFound
	}
	if inv.ExpiresAt != nil && !p.OccurredAt.Before(*inv.ExpiresAt) {
		return AcceptResult{}, fmt.Errorf("%w: invitation expired", ErrClosed)
	}
	if col.Deadline != nil && !p.OccurredAt.Before(col.Deadline.At) {
		return AcceptResult{}, fmt.Errorf("%w: collection deadline passed", ErrClosed)
	}
	if p.OccurredAt.Before(inv.CreatedAt.Add(-answerClockSkew)) {
		return AcceptResult{}, fmt.Errorf("%w: message predates the invitation", ErrInvalid)
	}
	// A group answer is only ever a reply to the invitation; an unquoted
	// binding is valid only in a 1:1 scene.
	if inv.TargetSceneKind == sceneKindGroup && p.Binding != BindReplyChain {
		return AcceptResult{}, fmt.Errorf("%w: group answers must reply to the invitation", ErrInvalid)
	}
	if p.Binding == BindReplyChain && inv.ProviderMessageID == "" {
		return AcceptResult{}, ErrInvalid
	}
	if inv.State == InvitationAnswered && !p.Correction {
		return AcceptResult{}, ErrAlreadyAnswered
	}
	if col.Revision != p.ExpectedRevision {
		return AcceptResult{}, ErrStaleRevision
	}
	if p.Binding == BindDMSinglePending {
		if inv.State != InvitationDelivered {
			return AcceptResult{}, fmt.Errorf("%w: an unquoted correction needs a reference", ErrInvalid)
		}
		if err := requireSinglePending(ctx, tx, scope, inv.Invitation); err != nil {
			return AcceptResult{}, err
		}
	}
	version := inv.EffectiveVersion + 1
	if version > maxVersions {
		return AcceptResult{}, fmt.Errorf("%w: too many corrections", ErrInvalid)
	}
	kind := "correction"
	received := col.ReceivedCount
	state := col.State
	writeIntent := false
	if inv.State == InvitationDelivered {
		kind = "answer"
		received++
		if state == CollectionOpen && received == col.ExpectedCount {
			state, writeIntent = CollectionReady, true
		}
	} else if state == CollectionReady || state == CollectionSummarizing {
		// A correction before the summary commits moves the revision: the old
		// summary fails its revision check and a new ready intent replaces it.
		state, writeIntent = CollectionReady, true
	}
	revision := col.Revision + 1
	input, err := scanInput(tx.QueryRow(ctx, `INSERT INTO employee_task_input
 (workspace_id,agent_id,tenant_org_id,task_id,collection_id,invitation_id,version,kind,binding,source_namespace,source_key,
  source_scene_id,receipt_ref,provider_message_id,actor_ref,occurred_at,body,body_ref,collection_revision,payload)
 VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6::uuid,$7,$8,$9,$10,$11,$12::uuid,$13,$14,$15,$16,$17,$18,$19,$20::jsonb)
 RETURNING `+inputColumns,
		scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, col.TaskID, col.ID, inv.ID, version, kind, string(p.Binding),
		p.Source.Namespace, p.Source.Key, p.Authority.SceneID, p.Authority.ReceiptRef, p.ProviderMessageID, p.Authority.ActorRef,
		p.OccurredAt, p.Body, p.BodyRef, revision, payload))
	if err != nil {
		return AcceptResult{}, err
	}
	updatedInv, err := scanInvitation(tx.QueryRow(ctx, `UPDATE employee_task_invitation SET delivery_state='answered',
 effective_version=$5, answered_at=COALESCE(answered_at, now()), updated_at=now()
 WHERE `+scopeWhere+` AND id=$4::uuid RETURNING `+invitationColumns, scopeArgs(scope, inv.ID, version)...))
	if err != nil {
		return AcceptResult{}, err
	}
	updated, err := scanCollection(tx.QueryRow(ctx, `UPDATE employee_task_collection SET state=$5, revision=$6, received_count=$7, updated_at=now()
 WHERE `+scopeWhere+` AND id=$4::uuid AND revision=$8 RETURNING `+collectionColumns,
		scopeArgs(scope, col.ID, state, revision, received, col.Revision)...))
	if err != nil {
		return AcceptResult{}, err
	}
	result := AcceptResult{Collection: updated.Collection, Invitation: updatedInv.Invitation, Input: input}
	if writeIntent {
		intent, err := writeReadyIntent(ctx, tx, updated.Collection, revision)
		if err != nil {
			return AcceptResult{}, err
		}
		result.Ready = &intent
	}
	if err = tx.Commit(ctx); err != nil {
		return AcceptResult{}, err
	}
	return result, nil
}

// requireSinglePending re-checks, inside the write, that the invitation is
// still the participant's only delivered, unanswered invitation in that 1:1
// scene. Another pending one makes the unquoted message ambiguous: re-bind.
func requireSinglePending(ctx context.Context, tx pgx.Tx, scope Scope, inv Invitation) error {
	if inv.TargetSceneKind != sceneKindDM {
		return fmt.Errorf("%w: unquoted answers are only bound in a 1:1 scene", ErrInvalid)
	}
	var count int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM employee_task_invitation i
 JOIN employee_task_collection c ON c.id=i.collection_id AND c.workspace_id=i.workspace_id AND c.agent_id=i.agent_id AND c.tenant_org_id=i.tenant_org_id
 WHERE i.workspace_id=$1::uuid AND i.agent_id=$2::uuid AND i.tenant_org_id=$3 AND i.participant_ref=$4 AND i.target_scene_id=$5::uuid
 AND i.delivery_state='delivered' AND c.state IN ('open','ready','summarizing') AND (i.expires_at IS NULL OR i.expires_at > now())`,
		scopeArgs(scope, inv.ParticipantRef, inv.TargetSceneID)...).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: the participant has %d pending invitations here", ErrConflict, count)
	}
	return nil
}

func acceptReplay(ctx context.Context, tx pgx.Tx, scope Scope, p AcceptInputParams, payload []byte) (AcceptResult, bool, error) {
	var stored string
	input, err := scanInputWithPayload(tx.QueryRow(ctx, `SELECT `+inputColumns+`, payload::text FROM employee_task_input
 WHERE `+scopeWhere+` AND source_namespace=$4 AND source_key=$5`, scopeArgs(scope, p.Source.Namespace, p.Source.Key)...), &stored)
	if errors.Is(err, ErrNotFound) {
		return AcceptResult{}, false, nil
	}
	if err != nil {
		return AcceptResult{}, false, err
	}
	equal, err := jsonEqual(ctx, tx, stored, payload)
	if err != nil {
		return AcceptResult{}, false, err
	}
	if !equal {
		return AcceptResult{}, false, ErrConflict
	}
	col, err := loadCollection(ctx, tx, scope, input.CollectionID, false)
	if err != nil {
		return AcceptResult{}, false, err
	}
	inv, err := loadInvitation(ctx, tx, scope, input.InvitationID, false)
	if err != nil {
		return AcceptResult{}, false, err
	}
	result := AcceptResult{Collection: col.Collection, Invitation: inv.Invitation, Input: input, Replayed: true}
	if intent, err := getIntent(ctx, tx, scope, input.CollectionID, input.CollectionRevision, false); err == nil {
		result.Ready = &intent
	} else if !errors.Is(err, ErrNotFound) {
		return AcceptResult{}, false, err
	}
	return result, true, nil
}

func scanInputWithPayload(row pgx.Row, payload *string) (Input, error) {
	var in Input
	err := row.Scan(&in.ID, &in.Scope.WorkspaceID, &in.Scope.AgentID, &in.Scope.TenantOrgID, &in.TaskID,
		&in.CollectionID, &in.InvitationID, &in.Version, &in.Kind, &in.Binding, &in.Source.Namespace, &in.Source.Key,
		&in.SourceSceneID, &in.ReceiptRef, &in.ProviderMessageID, &in.ActorRef, &in.OccurredAt, &in.Body, &in.BodyRef,
		&in.CollectionRevision, &in.CreatedAt, payload)
	if err != nil {
		return Input{}, mapError(err)
	}
	return in, nil
}

// ReadOriginInputs is the origin scene's view of its Task's collection: every
// slot with its participant, question and status, and the effective answer of
// each still-authorized slot. Any other viewer gets ErrNotFound.
func (s *Store) ReadOriginInputs(ctx context.Context, scope Scope, collectionID string, viewer OriginViewer) (OriginView, error) {
	if err := validateScope(scope); err != nil {
		return OriginView{}, err
	}
	if !validUUID(collectionID) || !validUUID(viewer.TaskID) || !validUUID(viewer.SceneID) {
		return OriginView{}, ErrInvalid
	}
	col, err := loadCollection(ctx, s.db, scope, collectionID, false)
	if err != nil {
		return OriginView{}, err
	}
	if col.TaskID != viewer.TaskID || col.OriginSceneID != viewer.SceneID {
		return OriginView{}, ErrNotFound
	}
	invitations, err := listInvitations(ctx, s.db, scope, col.ID)
	if err != nil {
		return OriginView{}, err
	}
	now, err := dbNow(ctx, s.db)
	if err != nil {
		return OriginView{}, err
	}
	view := OriginView{CollectionID: col.ID, TaskID: col.TaskID, State: col.State, Revision: col.Revision, GoalRevision: col.GoalRevision,
		CompletionRule: col.CompletionRule, Expected: col.ExpectedCount, Received: col.ReceivedCount, Deadline: col.Deadline,
		CloseMode: col.CloseMode, CloseReason: col.CloseReason, Slots: make([]OriginSlot, 0, len(invitations))}
	for _, inv := range invitations {
		answer, err := effectiveAnswer(ctx, s.db, scope, inv)
		if err != nil {
			return OriginView{}, err
		}
		view.Slots = append(view.Slots, OriginSlot{InvitationID: inv.ID, Ordinal: inv.Ordinal, ParticipantRef: inv.ParticipantRef,
			TargetSceneID: inv.TargetSceneID, Question: inv.Question, State: effectiveState(inv, now), Answer: answer})
	}
	return view, nil
}

// InputHistory returns every accepted version of one invitation's answer, for
// audit by the Host. It is not a participant view.
func (s *Store) InputHistory(ctx context.Context, scope Scope, collectionID, invitationID string) ([]Input, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	if !validUUID(collectionID) || !validUUID(invitationID) {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT `+inputColumns+` FROM employee_task_input WHERE `+scopeWhere+`
 AND collection_id=$4::uuid AND invitation_id=$5::uuid ORDER BY version`, scopeArgs(scope, collectionID, invitationID)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Input{}
	for rows.Next() {
		in, err := scanInput(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
