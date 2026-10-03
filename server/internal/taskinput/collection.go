package taskinput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func validateCreate(scope Scope, p CreateCollectionParams) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if err := validateSource(p.Source); err != nil {
		return err
	}
	if err := validateAuthority(p.Authority); err != nil {
		return err
	}
	if !validUUID(p.TaskID) || !validUUID(p.OriginSceneID) || p.GoalRevision <= 0 ||
		!validRef(p.AuthorityRef, maxRefBytes) || !validRef(p.RequesterRef, maxActorBytes) || !validRef(p.DeliveryAnchorRef, maxRefBytes) {
		return ErrInvalid
	}
	// The requester authorized the outreach in the origin scene; the Host
	// presents that evidence, not a payload actor.
	if p.Authority.ActorRef != p.RequesterRef || p.Authority.SceneID != p.OriginSceneID {
		return fmt.Errorf("%w: authority is not the requester in the origin scene", ErrForbidden)
	}
	if p.Deadline != nil {
		if p.Deadline.At.IsZero() || strings.TrimSpace(p.Deadline.TimeZone) == "" || p.Deadline.TimeZone != strings.TrimSpace(p.Deadline.TimeZone) {
			return fmt.Errorf("%w: deadline needs an explicit time zone", ErrInvalid)
		}
		if _, err := time.LoadLocation(p.Deadline.TimeZone); err != nil || p.Deadline.TimeZone == "Local" {
			return fmt.Errorf("%w: deadline time zone", ErrInvalid)
		}
	}
	if len(p.Invitations) == 0 || len(p.Invitations) > maxInvitations {
		return fmt.Errorf("%w: 1-%d invitations", ErrInvalid, maxInvitations)
	}
	participants := make(map[string]struct{}, len(p.Invitations))
	for _, spec := range p.Invitations {
		if !validRef(spec.ParticipantRef, maxActorBytes) {
			return ErrInvalid
		}
		switch {
		case spec.TargetSceneID == "":
			if spec.TargetKind != sceneKindDM {
				return fmt.Errorf("%w: only a 1:1 invitation may wait for its scene, and it must say so", ErrInvalid)
			}
		case !validUUID(spec.TargetSceneID):
			return ErrInvalid
		case spec.TargetKind != "" && spec.TargetKind != sceneKindDM && spec.TargetKind != sceneKindGroup:
			return ErrInvalid
		}
		question := strings.TrimSpace(spec.Question)
		if question == "" || question != spec.Question || !utf8.ValidString(question) || utf8.RuneCountInString(question) > MaxQuestionRunes {
			return fmt.Errorf("%w: bounded question", ErrInvalid)
		}
		if spec.ExpiresAt != nil && spec.ExpiresAt.IsZero() {
			return ErrInvalid
		}
		// One slot per person: a second invitation would count them twice.
		if _, dup := participants[spec.ParticipantRef]; dup {
			return fmt.Errorf("%w: participant invited twice", ErrInvalid)
		}
		participants[spec.ParticipantRef] = struct{}{}
	}
	return nil
}

// CreateCollectionTx freezes the expected slots and creates one invitation per
// participant in pending_delivery, each with its first delivery action id. The
// Host writes the matching outbox intents (and the Task wait) in the same outer
// transaction, then sends outside it. A source replay returns the stored
// collection; the same source with other content conflicts.
func (s *Store) CreateCollectionTx(ctx context.Context, scope Scope, p CreateCollectionParams) (Collection, []Invitation, error) {
	if err := validateCreate(scope, p); err != nil {
		return Collection{}, nil, err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return Collection{}, nil, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Collection{}, nil, err
	}
	defer tx.Rollback(ctx)
	if err := lockWorkspace(ctx, tx, scope.WorkspaceID); err != nil {
		return Collection{}, nil, err
	}
	task, err := lockTask(ctx, tx, scope, p.TaskID)
	if err != nil {
		return Collection{}, nil, err
	}
	if task.scopeKind != "scene" || task.sceneID != p.OriginSceneID {
		return Collection{}, nil, ErrNotFound
	}
	if task.ownerLoop != "employee" {
		return Collection{}, nil, fmt.Errorf("%w: collections belong to EmployeeLoop tasks", ErrInvalid)
	}
	if existing, ok, err := s.createReplay(ctx, tx, scope, p, payload); err != nil || ok {
		if err != nil {
			return Collection{}, nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return Collection{}, nil, err
		}
		return existing.col, existing.invitations, nil
	}
	if task.stopped() || task.state == "succeeded" || task.state == "failed" {
		return Collection{}, nil, ErrClosed
	}
	if task.goalRevision != p.GoalRevision {
		return Collection{}, nil, fmt.Errorf("%w: goal revision", ErrConflict)
	}
	if _, err := sceneKind(ctx, tx, scope, p.OriginSceneID); err != nil {
		return Collection{}, nil, err
	}
	kinds := make([]string, len(p.Invitations))
	for i, spec := range p.Invitations {
		kind := sceneKindDM
		if spec.TargetSceneID != "" {
			kind, err = sceneKind(ctx, tx, scope, spec.TargetSceneID)
			if err != nil {
				return Collection{}, nil, err
			}
			if kind != sceneKindDM && kind != sceneKindGroup {
				return Collection{}, nil, fmt.Errorf("%w: invitations go to a dm or group scene", ErrInvalid)
			}
			if spec.TargetKind != "" && spec.TargetKind != kind {
				return Collection{}, nil, fmt.Errorf("%w: target kind disagrees with the directory", ErrInvalid)
			}
		}
		kinds[i] = kind
		if spec.ExpiresAt != nil {
			if err := requireFuture(ctx, tx, *spec.ExpiresAt); err != nil {
				return Collection{}, nil, err
			}
		}
	}
	var deadlineAt *time.Time
	deadlineTZ := ""
	if p.Deadline != nil {
		if err := requireFuture(ctx, tx, p.Deadline.At); err != nil {
			return Collection{}, nil, err
		}
		deadlineAt, deadlineTZ = &p.Deadline.At, p.Deadline.TimeZone
	}
	row, err := scanCollection(tx.QueryRow(ctx, `INSERT INTO employee_task_collection
 (workspace_id,agent_id,tenant_org_id,task_id,origin_scene_id,authority_ref,requester_ref,delivery_anchor_ref,goal_revision,expected_count,deadline_at,deadline_tz,source_namespace,source_key,create_payload)
 VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb)
 ON CONFLICT (task_id, source_namespace, source_key) DO NOTHING RETURNING `+collectionColumns,
		scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, p.TaskID, p.OriginSceneID, p.AuthorityRef, p.RequesterRef,
		p.DeliveryAnchorRef, p.GoalRevision, len(p.Invitations), deadlineAt, deadlineTZ, p.Source.Namespace, p.Source.Key, payload))
	if errors.Is(err, ErrNotFound) {
		// A concurrent create with the same source committed first.
		existing, ok, err := s.createReplay(ctx, tx, scope, p, payload)
		if err != nil {
			return Collection{}, nil, err
		}
		if !ok {
			return Collection{}, nil, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return Collection{}, nil, err
		}
		return existing.col, existing.invitations, nil
	}
	if err != nil {
		return Collection{}, nil, err
	}
	invitations := make([]Invitation, 0, len(p.Invitations))
	for i, spec := range p.Invitations {
		id := uuid.NewString()
		state := InvitationPendingDelivery
		if spec.TargetSceneID == "" {
			state = InvitationPendingScene
		}
		inv, err := scanInvitation(tx.QueryRow(ctx, `INSERT INTO employee_task_invitation
 (id,workspace_id,agent_id,tenant_org_id,task_id,collection_id,ordinal,collection_revision,target_scene_id,target_scene_kind,participant_ref,question,expires_at,delivery_action_id,delivery_state)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,$7,$8,NULLIF($9,'')::uuid,$10,$11,$12,$13,$14,$15) RETURNING `+invitationColumns,
			id, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, p.TaskID, row.ID, i+1, row.Revision, spec.TargetSceneID,
			kinds[i], spec.ParticipantRef, spec.Question, spec.ExpiresAt, DeliveryActionID(id, 1), string(state)))
		if err != nil {
			return Collection{}, nil, err
		}
		invitations = append(invitations, inv.Invitation)
	}
	if err = tx.Commit(ctx); err != nil {
		return Collection{}, nil, err
	}
	return row.Collection, invitations, nil
}

type collectionWithInvitations struct {
	col         Collection
	invitations []Invitation
}

func (s *Store) createReplay(ctx context.Context, tx pgx.Tx, scope Scope, p CreateCollectionParams, payload []byte) (collectionWithInvitations, bool, error) {
	var id, stored string
	err := tx.QueryRow(ctx, `SELECT id::text, create_payload::text FROM employee_task_collection WHERE `+scopeWhere+`
 AND task_id=$4::uuid AND source_namespace=$5 AND source_key=$6`, scopeArgs(scope, p.TaskID, p.Source.Namespace, p.Source.Key)...).Scan(&id, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return collectionWithInvitations{}, false, nil
	}
	if err != nil {
		return collectionWithInvitations{}, false, err
	}
	equal, err := jsonEqual(ctx, tx, stored, payload)
	if err != nil {
		return collectionWithInvitations{}, false, err
	}
	if !equal {
		return collectionWithInvitations{}, false, ErrConflict
	}
	col, err := loadCollection(ctx, tx, scope, id, false)
	if err != nil {
		return collectionWithInvitations{}, false, err
	}
	invitations, err := listInvitations(ctx, tx, scope, id)
	if err != nil {
		return collectionWithInvitations{}, false, err
	}
	return collectionWithInvitations{col.Collection, invitations}, true, nil
}

func requireFuture(ctx context.Context, tx pgx.Tx, at time.Time) error {
	var future bool
	if err := tx.QueryRow(ctx, `SELECT $1::timestamptz > now()`, at).Scan(&future); err != nil {
		return err
	}
	if !future {
		return fmt.Errorf("%w: deadline or expiry is not in the future", ErrInvalid)
	}
	return nil
}

func listInvitations(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, scope Scope, collectionID string) ([]Invitation, error) {
	rows, err := db.Query(ctx, `SELECT `+invitationColumns+` FROM employee_task_invitation WHERE `+scopeWhere+` AND collection_id=$4::uuid ORDER BY ordinal`, scopeArgs(scope, collectionID)...)
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

// GetCollection returns one collection in scope (Host use; not a participant view).
func (s *Store) GetCollection(ctx context.Context, scope Scope, id string) (Collection, error) {
	if err := validateScope(scope); err != nil {
		return Collection{}, err
	}
	if !validUUID(id) {
		return Collection{}, ErrInvalid
	}
	row, err := loadCollection(ctx, s.db, scope, id, false)
	return row.Collection, err
}

// ListInvitations returns a collection's invitations with delivery facts, for
// the Host's outbox and reconciliation. It is not a participant view.
func (s *Store) ListInvitations(ctx context.Context, scope Scope, collectionID string) ([]Invitation, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	if !validUUID(collectionID) {
		return nil, ErrInvalid
	}
	return listInvitations(ctx, s.db, scope, collectionID)
}

// TaskWaits lists the Task's collections that are still active. An open
// collection is an unsatisfied mandatory wait: the goal cannot complete. A
// ready/summarizing one is satisfied up to its Revision; completing the goal
// must complete that collection at that revision in the same transaction.
func (s *Store) TaskWaits(ctx context.Context, scope Scope, taskID string) ([]TaskWait, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	if !validUUID(taskID) {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, state, revision, expected_count, received_count FROM employee_task_collection
 WHERE `+scopeWhere+` AND task_id=$4::uuid AND state IN ('open','ready','summarizing') ORDER BY created_at, id`, scopeArgs(scope, taskID)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskWait{}
	for rows.Next() {
		var w TaskWait
		if err := rows.Scan(&w.CollectionID, &w.State, &w.Revision, &w.Expected, &w.Received); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

type closePayload struct {
	Operation string `json:"operation"`
	CloseParams
}

// CloseCollectionTx ends waiting. ClosePartial is the audited early close:
// only the original requester, in the origin scene, with a reason; unanswered
// invitations are revoked and the collection becomes ready with a new ready
// intent so the origin summarizes what arrived. Cancel/revoke/expire end the
// collection; nothing reopens it, and later answers report ErrClosed.
func (s *Store) CloseCollectionTx(ctx context.Context, scope Scope, p CloseParams) (Collection, *ReadyIntent, error) {
	if err := validateScope(scope); err != nil {
		return Collection{}, nil, err
	}
	if err := validateSource(p.Source); err != nil {
		return Collection{}, nil, err
	}
	if err := validateAuthority(p.Authority); err != nil {
		return Collection{}, nil, err
	}
	if !validUUID(p.CollectionID) || p.ExpectedRevision <= 0 {
		return Collection{}, nil, ErrInvalid
	}
	switch p.Mode {
	case ClosePartial:
		if !validReason(p.Reason, true) {
			return Collection{}, nil, fmt.Errorf("%w: a partial close needs a reason", ErrInvalid)
		}
	case CloseCancel, CloseRevoke, CloseExpire:
		if !validReason(p.Reason, false) {
			return Collection{}, nil, ErrInvalid
		}
	default:
		return Collection{}, nil, ErrInvalid
	}
	payload, err := json.Marshal(closePayload{Operation: "close", CloseParams: p})
	if err != nil {
		return Collection{}, nil, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Collection{}, nil, err
	}
	defer tx.Rollback(ctx)
	col, _, err := lockCollectionChain(ctx, tx, scope, p.CollectionID)
	if err != nil {
		return Collection{}, nil, err
	}
	if col.closeSource == p.Source {
		equal, err := jsonEqual(ctx, tx, col.closePayload, payload)
		if err != nil {
			return Collection{}, nil, err
		}
		if !equal {
			return Collection{}, nil, ErrConflict
		}
		var ready *ReadyIntent
		if p.Mode == ClosePartial {
			intent, err := getIntent(ctx, tx, scope, col.ID, col.closeRevision, false)
			if err != nil {
				return Collection{}, nil, err
			}
			ready = &intent
		}
		if err = tx.Commit(ctx); err != nil {
			return Collection{}, nil, err
		}
		return col.Collection, ready, nil
	}
	if !col.State.Active() {
		return Collection{}, nil, ErrClosed
	}
	if col.CloseMode != "" {
		// A partial close already decided this collection's inputs.
		return Collection{}, nil, ErrClosed
	}
	if col.Revision != p.ExpectedRevision {
		return Collection{}, nil, ErrStaleRevision
	}
	requester := p.Authority.ActorRef == col.RequesterRef && p.Authority.SceneID == col.OriginSceneID
	switch p.Mode {
	case ClosePartial:
		if !requester {
			return Collection{}, nil, ErrForbidden
		}
		if col.State != CollectionOpen {
			return Collection{}, nil, fmt.Errorf("%w: every slot is already filled", ErrConflict)
		}
	case CloseCancel, CloseRevoke:
		if !requester && !isHostActor(p.Authority) {
			return Collection{}, nil, ErrForbidden
		}
	case CloseExpire:
		var due bool
		if err := tx.QueryRow(ctx, `SELECT deadline_at IS NOT NULL AND deadline_at <= now() FROM employee_task_collection WHERE id=$1::uuid`, col.ID).Scan(&due); err != nil {
			return Collection{}, nil, err
		}
		if !due {
			return Collection{}, nil, fmt.Errorf("%w: the deadline has not passed", ErrInvalid)
		}
	}
	revision := col.Revision + 1
	state, invitationEnd := CollectionReady, InvitationRevoked
	switch p.Mode {
	case CloseCancel:
		state = CollectionCancelled
	case CloseRevoke:
		state = CollectionRevoked
	case CloseExpire:
		state, invitationEnd = CollectionExpired, InvitationExpired
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_task_invitation SET delivery_state=$5, end_reason=$6,
 end_source_namespace=$7, end_source_key=$8, updated_at=now()
 WHERE `+scopeWhere+` AND collection_id=$4::uuid AND delivery_state IN ('pending_scene','pending_delivery','delivered')`,
		scopeArgs(scope, col.ID, invitationEnd, string(p.Mode)+"_close", p.Source.Namespace, p.Source.Key)...); err != nil {
		return Collection{}, nil, err
	}
	updated, err := scanCollection(tx.QueryRow(ctx, `UPDATE employee_task_collection SET state=$5, revision=$6, close_mode=$7,
 close_actor_ref=$8, close_reason=$9, close_source_namespace=$10, close_source_key=$11, close_payload=$12::jsonb,
 close_revision=$6, closed_at=now(), updated_at=now() WHERE `+scopeWhere+` AND id=$4::uuid AND revision=$13 RETURNING `+collectionColumns,
		scopeArgs(scope, col.ID, state, revision, string(p.Mode), p.Authority.ActorRef, p.Reason, p.Source.Namespace, p.Source.Key, payload, col.Revision)...))
	if err != nil {
		return Collection{}, nil, err
	}
	var ready *ReadyIntent
	if state == CollectionReady {
		intent, err := writeReadyIntent(ctx, tx, updated.Collection, revision)
		if err != nil {
			return Collection{}, nil, err
		}
		ready = &intent
	} else if err := supersedePending(ctx, tx, scope, col.ID, revision+1); err != nil {
		return Collection{}, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Collection{}, nil, err
	}
	return updated.Collection, ready, nil
}

// RevokeInvitationTx withdraws one participant's authorization (the requester
// stops asking them, or the Host lost the identity/permission). A revoked
// answer is no longer authorized for the origin and stops counting. Under
// all_required the collection then waits for an explicit partial close; after
// a partial close it becomes ready again at a new revision.
func (s *Store) RevokeInvitationTx(ctx context.Context, scope Scope, p RevokeInvitationParams) (Collection, Invitation, error) {
	if err := validateScope(scope); err != nil {
		return Collection{}, Invitation{}, err
	}
	if err := validateSource(p.Source); err != nil {
		return Collection{}, Invitation{}, err
	}
	if err := validateAuthority(p.Authority); err != nil {
		return Collection{}, Invitation{}, err
	}
	if !validUUID(p.CollectionID) || !validUUID(p.InvitationID) || p.ExpectedRevision <= 0 || !validReason(p.Reason, true) {
		return Collection{}, Invitation{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Collection{}, Invitation{}, err
	}
	defer tx.Rollback(ctx)
	col, _, err := lockCollectionChain(ctx, tx, scope, p.CollectionID)
	if err != nil {
		return Collection{}, Invitation{}, err
	}
	inv, err := loadInvitation(ctx, tx, scope, p.InvitationID, true)
	if err != nil {
		return Collection{}, Invitation{}, err
	}
	if inv.CollectionID != col.ID {
		return Collection{}, Invitation{}, ErrNotFound
	}
	if inv.endSource == p.Source {
		if inv.State != InvitationRevoked || inv.EndReason != p.Reason {
			return Collection{}, Invitation{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return Collection{}, Invitation{}, err
		}
		return col.Collection, inv.Invitation, nil
	}
	if !(p.Authority.ActorRef == col.RequesterRef && p.Authority.SceneID == col.OriginSceneID) && !isHostActor(p.Authority) {
		return Collection{}, Invitation{}, ErrForbidden
	}
	if !col.State.Active() || inv.State == InvitationRevoked || inv.State == InvitationExpired {
		return Collection{}, Invitation{}, ErrClosed
	}
	if col.Revision != p.ExpectedRevision {
		return Collection{}, Invitation{}, ErrStaleRevision
	}
	revision := col.Revision + 1
	received := col.ReceivedCount
	state := col.State
	writeIntent := false
	if inv.State == InvitationAnswered {
		received--
		if col.CloseMode == string(ClosePartial) {
			state, writeIntent = CollectionReady, true
		} else {
			state = CollectionOpen
		}
	}
	updatedInv, err := scanInvitation(tx.QueryRow(ctx, `UPDATE employee_task_invitation SET delivery_state='revoked', end_reason=$5,
 end_source_namespace=$6, end_source_key=$7, updated_at=now() WHERE `+scopeWhere+` AND id=$4::uuid RETURNING `+invitationColumns,
		scopeArgs(scope, inv.ID, p.Reason, p.Source.Namespace, p.Source.Key)...))
	if err != nil {
		return Collection{}, Invitation{}, err
	}
	updated, err := scanCollection(tx.QueryRow(ctx, `UPDATE employee_task_collection SET state=$5, revision=$6, received_count=$7, updated_at=now()
 WHERE `+scopeWhere+` AND id=$4::uuid AND revision=$8 RETURNING `+collectionColumns,
		scopeArgs(scope, col.ID, state, revision, received, col.Revision)...))
	if err != nil {
		return Collection{}, Invitation{}, err
	}
	if writeIntent {
		if _, err := writeReadyIntent(ctx, tx, updated.Collection, revision); err != nil {
			return Collection{}, Invitation{}, err
		}
	} else if err := supersedePending(ctx, tx, scope, col.ID, revision+1); err != nil {
		return Collection{}, Invitation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Collection{}, Invitation{}, err
	}
	return updated.Collection, updatedInv.Invitation, nil
}

// ListPendingReadyIntents is the reconciler's scan across workspaces: each
// pending intent becomes one typed collection.ready TaskWake for its origin
// scene. Admission and MarkReadyIntentAdmittedTx commit together.
func (s *Store) ListPendingReadyIntents(ctx context.Context, limit int) ([]ReadyIntent, error) {
	if limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT `+intentColumns+` FROM employee_task_ready_intent
 WHERE state='pending' ORDER BY created_at, collection_id, collection_revision LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReadyIntent{}
	for rows.Next() {
		intent, err := scanIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, intent)
	}
	return out, rows.Err()
}

// GetReadyIntent returns the intent of one collection revision.
func (s *Store) GetReadyIntent(ctx context.Context, scope Scope, collectionID string, revision int64) (ReadyIntent, error) {
	if err := validateScope(scope); err != nil {
		return ReadyIntent{}, err
	}
	if !validUUID(collectionID) || revision <= 0 {
		return ReadyIntent{}, ErrInvalid
	}
	return getIntent(ctx, s.db, scope, collectionID, revision, false)
}

// MarkReadyIntentAdmittedTx records that the origin wake for this exact
// revision was admitted (admittedRef: the receipt/job the Host created) and
// moves the collection to summarizing. Run it in the same transaction as the
// TaskWake admission. A replay with the same ref is a no-op; a stale revision
// (a correction or close happened) is ErrConflict and must not be admitted.
func (s *Store) MarkReadyIntentAdmittedTx(ctx context.Context, scope Scope, collectionID string, revision int64, admittedRef string) (Collection, ReadyIntent, error) {
	if err := validateScope(scope); err != nil {
		return Collection{}, ReadyIntent{}, err
	}
	if !validUUID(collectionID) || revision <= 0 || !validRef(admittedRef, maxRefBytes) {
		return Collection{}, ReadyIntent{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Collection{}, ReadyIntent{}, err
	}
	defer tx.Rollback(ctx)
	col, task, err := lockCollectionChain(ctx, tx, scope, collectionID)
	if err != nil {
		return Collection{}, ReadyIntent{}, err
	}
	intent, err := getIntent(ctx, tx, scope, collectionID, revision, true)
	if err != nil {
		return Collection{}, ReadyIntent{}, err
	}
	switch intent.State {
	case ReadyIntentAdmitted:
		if intent.AdmittedRef != admittedRef {
			return Collection{}, ReadyIntent{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return Collection{}, ReadyIntent{}, err
		}
		return col.Collection, intent, nil
	case ReadyIntentSuperseded:
		return Collection{}, ReadyIntent{}, ErrConflict
	}
	if task.stopped() || !col.State.Active() {
		return Collection{}, ReadyIntent{}, ErrClosed
	}
	if col.Revision != revision || col.State != CollectionReady {
		return Collection{}, ReadyIntent{}, ErrStaleRevision
	}
	intent, err = scanIntent(tx.QueryRow(ctx, `UPDATE employee_task_ready_intent SET state='admitted', admitted_ref=$6,
 admitted_at=now(), updated_at=now() WHERE `+scopeWhere+` AND collection_id=$4::uuid AND collection_revision=$5 RETURNING `+intentColumns,
		scopeArgs(scope, collectionID, revision, admittedRef)...))
	if err != nil {
		return Collection{}, ReadyIntent{}, err
	}
	updated, err := scanCollection(tx.QueryRow(ctx, `UPDATE employee_task_collection SET state='summarizing', updated_at=now()
 WHERE `+scopeWhere+` AND id=$4::uuid AND revision=$5 RETURNING `+collectionColumns, scopeArgs(scope, collectionID, revision)...))
	if err != nil {
		return Collection{}, ReadyIntent{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Collection{}, ReadyIntent{}, err
	}
	return updated.Collection, intent, nil
}

// CompleteCollectionTx records that the origin summary for ExpectedRevision
// was committed for delivery. Run it in the same transaction as the goal
// completion and the summary outbox intent: a correction that landed after the
// summary was built moved the revision, so this fails and nothing is sent.
func (s *Store) CompleteCollectionTx(ctx context.Context, scope Scope, p CompleteParams) (Collection, error) {
	if err := validateScope(scope); err != nil {
		return Collection{}, err
	}
	if err := validateSource(p.Source); err != nil {
		return Collection{}, err
	}
	if !validUUID(p.CollectionID) || p.ExpectedRevision <= 0 || !validRef(p.SummaryRef, maxRefBytes) {
		return Collection{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Collection{}, err
	}
	defer tx.Rollback(ctx)
	col, task, err := lockCollectionChain(ctx, tx, scope, p.CollectionID)
	if err != nil {
		return Collection{}, err
	}
	if col.State == CollectionCompleted {
		if col.completeSource == p.Source && col.SummaryRef == p.SummaryRef && col.Revision == p.ExpectedRevision {
			if err = tx.Commit(ctx); err != nil {
				return Collection{}, err
			}
			return col.Collection, nil
		}
		return Collection{}, ErrClosed
	}
	if task.stopped() || !col.State.Active() {
		return Collection{}, ErrClosed
	}
	if col.Revision != p.ExpectedRevision || col.State != CollectionSummarizing {
		return Collection{}, ErrStaleRevision
	}
	updated, err := scanCollection(tx.QueryRow(ctx, `UPDATE employee_task_collection SET state='completed', summary_ref=$5,
 complete_source_namespace=$6, complete_source_key=$7, completed_at=now(), updated_at=now()
 WHERE `+scopeWhere+` AND id=$4::uuid AND revision=$8 RETURNING `+collectionColumns,
		scopeArgs(scope, col.ID, p.SummaryRef, p.Source.Namespace, p.Source.Key, p.ExpectedRevision)...))
	if err != nil {
		return Collection{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Collection{}, err
	}
	return updated.Collection, nil
}
