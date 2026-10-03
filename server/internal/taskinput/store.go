package taskinput

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB accepts a pool or an existing transaction. Every *Tx method begins a
// transaction on it, which nests as a savepoint inside an outer transaction,
// so the Host can commit a collection write together with its own records
// (a Task wait, an outbox intent, a TaskWake admission) atomically.
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Store is the collection domain. Lock order in every write transaction:
// workspace (FOR KEY SHARE, fencing workspace deletion) -> employee_task
// (FOR UPDATE, the same mode employeetask's lifecycle writes take) ->
// collection (FOR UPDATE) -> invitation (FOR UPDATE) -> input / ready intent
// rows. A Host transaction that combines these writes with Task lifecycle
// writes (WaitTaskTx, CompleteGoalTx, Stop) therefore never upgrades a shared
// Task lock and cannot deadlock against a second such transaction. External
// I/O never
// happens inside these transactions.
type Store struct{ db DB }

func NewStore(db DB) *Store { return &Store{db: db} }

const (
	sceneKindDM    = "dm"
	sceneKindGroup = "group"

	maxInvitations   = 32
	maxRefBytes      = 512
	maxActorBytes    = 256
	maxReasonRunes   = 1000
	maxAnswerBytes   = 32 * 1024
	maxBodyRefBytes  = 1024
	maxProviderIDLen = 256
	maxAttempts      = 8
	maxVersions      = 64

	// answerClockSkew tolerates provider/DB clock skew when checking that an
	// answer does not predate its invitation.
	answerClockSkew = 2 * time.Minute
)

func validUUID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}

// validRef accepts an opaque, normalized reference: non-empty, at most max
// bytes, no whitespace or control characters.
func validRef(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validateScope(scope Scope) error {
	if !validUUID(scope.WorkspaceID) || !validUUID(scope.AgentID) || !validRef(scope.TenantOrgID, 128) {
		return ErrInvalid
	}
	return nil
}

func validateSource(source Source) error {
	if !validRef(source.Namespace, 128) || source.Key == "" || len(source.Key) > maxRefBytes || source.Key != strings.TrimSpace(source.Key) {
		return fmt.Errorf("%w: source", ErrInvalid)
	}
	return nil
}

func validateAuthority(a Authority) error {
	if !validRef(a.ActorRef, maxActorBytes) || !validUUID(a.SceneID) || !validRef(a.ReceiptRef, maxRefBytes) || a.VerifiedAt.IsZero() {
		return fmt.Errorf("%w: authority evidence", ErrInvalid)
	}
	return nil
}

// validateHostAuthority accepts Host-originated evidence (a sweeper or a
// permission reconciler). Its scene is still a directory scene: the one where
// the Host observed the reason (the origin scene for an origin revocation).
func isHostActor(a Authority) bool {
	return strings.HasPrefix(a.ActorRef, HostActorPrefix) && len(a.ActorRef) > len(HostActorPrefix)
}

func validReason(reason string, required bool) bool {
	trimmed := strings.TrimSpace(reason)
	if required && trimmed == "" {
		return false
	}
	return utf8.ValidString(reason) && utf8.RuneCountInString(reason) <= maxReasonRunes
}

func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pgErr.ConstraintName)
	}
	return err
}

// DeliveryActionID is the stable idempotency key of one delivery attempt.
// The outbox sends with it; an unknown outcome is resolved by querying it.
func DeliveryActionID(invitationID string, attempt int) string {
	return "taskinput.invite/" + invitationID + "/" + strconv.Itoa(attempt)
}

// ReadyEventID is the stable derived event id of a ready intent.
func ReadyEventID(collectionID string, revision int64) string {
	return collectionID + "/" + strconv.FormatInt(revision, 10)
}

const collectionColumns = `id::text, workspace_id::text, agent_id::text, tenant_org_id, task_id::text,
 origin_scene_id::text, authority_ref, requester_ref, delivery_anchor_ref, goal_revision, revision, state,
 completion_rule, expected_count, received_count, deadline_at, deadline_tz, close_mode, close_actor_ref,
 close_reason, close_source_namespace, close_source_key, COALESCE(close_payload::text,''), close_revision, summary_ref,
 complete_source_namespace, complete_source_key, created_at, updated_at, closed_at`

// collectionRow keeps the replay columns the public Collection omits.
type collectionRow struct {
	Collection
	closeSource    Source
	closePayload   string
	closeRevision  int64
	completeSource Source
}

func scanCollection(row pgx.Row) (collectionRow, error) {
	var c collectionRow
	var deadlineAt *time.Time
	var deadlineTZ string
	err := row.Scan(&c.ID, &c.Scope.WorkspaceID, &c.Scope.AgentID, &c.Scope.TenantOrgID, &c.TaskID,
		&c.OriginSceneID, &c.AuthorityRef, &c.RequesterRef, &c.DeliveryAnchorRef, &c.GoalRevision, &c.Revision, &c.State,
		&c.CompletionRule, &c.ExpectedCount, &c.ReceivedCount, &deadlineAt, &deadlineTZ, &c.CloseMode, &c.CloseActorRef,
		&c.CloseReason, &c.closeSource.Namespace, &c.closeSource.Key, &c.closePayload, &c.closeRevision, &c.SummaryRef,
		&c.completeSource.Namespace, &c.completeSource.Key, &c.CreatedAt, &c.UpdatedAt, &c.ClosedAt)
	if err != nil {
		return collectionRow{}, mapError(err)
	}
	if deadlineAt != nil {
		c.Deadline = &Deadline{At: *deadlineAt, TimeZone: deadlineTZ}
	}
	return c, nil
}

const invitationColumns = `id::text, workspace_id::text, agent_id::text, tenant_org_id, task_id::text,
 collection_id::text, ordinal, collection_revision, COALESCE(target_scene_id::text,''), target_scene_kind, participant_ref,
 authorization_scope, question, expires_at, delivery_state, delivery_attempt, delivery_action_id,
 delivery_outcome, delivery_error, provider_message_id, rendered_hash, effective_version, end_reason,
 end_source_namespace, end_source_key, delivered_at, answered_at, created_at, updated_at`

type invitationRow struct {
	Invitation
	endSource Source
}

func scanInvitation(row pgx.Row) (invitationRow, error) {
	var i invitationRow
	err := row.Scan(&i.ID, &i.Scope.WorkspaceID, &i.Scope.AgentID, &i.Scope.TenantOrgID, &i.TaskID,
		&i.CollectionID, &i.Ordinal, &i.CollectionRevision, &i.TargetSceneID, &i.TargetSceneKind, &i.ParticipantRef,
		&i.AuthorizationScope, &i.Question, &i.ExpiresAt, &i.State, &i.DeliveryAttempt, &i.DeliveryActionID,
		&i.DeliveryOutcome, &i.DeliveryError, &i.ProviderMessageID, &i.RenderedHash, &i.EffectiveVersion, &i.EndReason,
		&i.endSource.Namespace, &i.endSource.Key, &i.DeliveredAt, &i.AnsweredAt, &i.CreatedAt, &i.UpdatedAt)
	if err != nil {
		return invitationRow{}, mapError(err)
	}
	return i, nil
}

const inputColumns = `id::text, workspace_id::text, agent_id::text, tenant_org_id, task_id::text,
 collection_id::text, invitation_id::text, version, kind, binding, source_namespace, source_key,
 source_scene_id::text, receipt_ref, provider_message_id, actor_ref, occurred_at, body, body_ref,
 collection_revision, created_at`

func scanInput(row pgx.Row) (Input, error) {
	var in Input
	err := row.Scan(&in.ID, &in.Scope.WorkspaceID, &in.Scope.AgentID, &in.Scope.TenantOrgID, &in.TaskID,
		&in.CollectionID, &in.InvitationID, &in.Version, &in.Kind, &in.Binding, &in.Source.Namespace, &in.Source.Key,
		&in.SourceSceneID, &in.ReceiptRef, &in.ProviderMessageID, &in.ActorRef, &in.OccurredAt, &in.Body, &in.BodyRef,
		&in.CollectionRevision, &in.CreatedAt)
	if err != nil {
		return Input{}, mapError(err)
	}
	return in, nil
}

const intentColumns = `workspace_id::text, agent_id::text, tenant_org_id, task_id::text, collection_id::text,
 collection_revision, goal_revision, origin_scene_id::text, event_source, event_type, event_id, occurred_at,
 state, admitted_ref, created_at`

func scanIntent(row pgx.Row) (ReadyIntent, error) {
	var r ReadyIntent
	err := row.Scan(&r.Scope.WorkspaceID, &r.Scope.AgentID, &r.Scope.TenantOrgID, &r.TaskID, &r.CollectionID,
		&r.CollectionRevision, &r.GoalRevision, &r.OriginSceneID, &r.EventSource, &r.EventType, &r.EventID, &r.OccurredAt,
		&r.State, &r.AdmittedRef, &r.CreatedAt)
	if err != nil {
		return ReadyIntent{}, mapError(err)
	}
	return r, nil
}

const scopeWhere = `workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3`

func scopeArgs(scope Scope, extra ...any) []any {
	return append([]any{scope.WorkspaceID, scope.AgentID, scope.TenantOrgID}, extra...)
}

// lockWorkspace fences writes against DeleteWorkspace's FOR UPDATE lock: once
// the workspace row is gone, every write here reports ErrNotFound instead of
// leaving orphan rows.
func lockWorkspace(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	var id string
	return mapError(tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, workspaceID).Scan(&id))
}

// taskFacts is the read-only view of employee_task this domain needs. The
// Task lifecycle itself belongs to employeetask; this package only fences on it.
type taskFacts struct {
	state        string
	scopeKind    string
	sceneID      string
	ownerLoop    string
	goalRevision int64
}

// lockTask takes FOR UPDATE on the Task row, the mode every employeetask
// lifecycle write takes. A share lock here would deadlock two Host
// transactions that each add a collection and then write the Task (both hold
// SHARE, both wait to upgrade). A concurrent stop either commits first and is
// observed, or waits until this write commits.
func lockTask(ctx context.Context, tx pgx.Tx, scope Scope, taskID string) (taskFacts, error) {
	var t taskFacts
	err := tx.QueryRow(ctx, `SELECT state, scope_kind, COALESCE(scene_id::text,''), owner_loop, goal_revision
 FROM employee_task WHERE `+scopeWhere+` AND id=$4::uuid FOR UPDATE`, scopeArgs(scope, taskID)...).
		Scan(&t.state, &t.scopeKind, &t.sceneID, &t.ownerLoop, &t.goalRevision)
	return t, mapError(err)
}

// taskStopped reports the human stop fence. Run outcomes (succeeded/failed)
// never close a collection: an open collection keeps waiting regardless.
func (t taskFacts) stopped() bool { return t.state == "cancelled" }

func collectionTaskID(ctx context.Context, tx pgx.Tx, scope Scope, collectionID string) (string, error) {
	var taskID string
	err := tx.QueryRow(ctx, `SELECT task_id::text FROM employee_task_collection WHERE `+scopeWhere+` AND id=$4::uuid`, scopeArgs(scope, collectionID)...).Scan(&taskID)
	return taskID, mapError(err)
}

func loadCollection(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, scope Scope, id string, lock bool) (collectionRow, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanCollection(db.QueryRow(ctx, `SELECT `+collectionColumns+` FROM employee_task_collection WHERE `+scopeWhere+` AND id=$4::uuid`+suffix, scopeArgs(scope, id)...))
}

func loadInvitation(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, scope Scope, id string, lock bool) (invitationRow, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanInvitation(db.QueryRow(ctx, `SELECT `+invitationColumns+` FROM employee_task_invitation WHERE `+scopeWhere+` AND id=$4::uuid`+suffix, scopeArgs(scope, id)...))
}

// lockCollectionChain runs the standard prefix of a collection write: the
// workspace fence, the Task stop fence and the collection row lock.
func lockCollectionChain(ctx context.Context, tx pgx.Tx, scope Scope, collectionID string) (collectionRow, taskFacts, error) {
	if err := lockWorkspace(ctx, tx, scope.WorkspaceID); err != nil {
		return collectionRow{}, taskFacts{}, err
	}
	taskID, err := collectionTaskID(ctx, tx, scope, collectionID)
	if err != nil {
		return collectionRow{}, taskFacts{}, err
	}
	task, err := lockTask(ctx, tx, scope, taskID)
	if err != nil {
		return collectionRow{}, taskFacts{}, err
	}
	col, err := loadCollection(ctx, tx, scope, collectionID, true)
	return col, task, err
}

func sceneKind(ctx context.Context, tx pgx.Tx, scope Scope, sceneID string) (string, error) {
	var kind string
	err := tx.QueryRow(ctx, `SELECT scene_kind FROM agent_scene WHERE `+scopeWhere+` AND id=$4::uuid`, scopeArgs(scope, sceneID)...).Scan(&kind)
	return kind, mapError(err)
}

// supersedePending marks pending intents of older revisions stale. Admitted
// intents stay admitted; their wake fails the revision check at completion.
func supersedePending(ctx context.Context, tx pgx.Tx, scope Scope, collectionID string, below int64) error {
	_, err := tx.Exec(ctx, `UPDATE employee_task_ready_intent SET state='superseded', updated_at=now()
 WHERE `+scopeWhere+` AND collection_id=$4::uuid AND state='pending' AND collection_revision<$5`, scopeArgs(scope, collectionID, below)...)
	return err
}

// writeReadyIntent inserts the unique intent for (collection, revision) with a
// frozen occurred_at, and returns the stored row (the first one on a retry).
func writeReadyIntent(ctx context.Context, tx pgx.Tx, col Collection, revision int64) (ReadyIntent, error) {
	if err := supersedePending(ctx, tx, col.Scope, col.ID, revision); err != nil {
		return ReadyIntent{}, err
	}
	_, err := tx.Exec(ctx, `INSERT INTO employee_task_ready_intent
 (workspace_id,agent_id,tenant_org_id,task_id,collection_id,collection_revision,goal_revision,origin_scene_id,event_source,event_type,event_id,occurred_at)
 VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6,$7,$8::uuid,$9,$10,$11,now())
 ON CONFLICT (collection_id, collection_revision) DO NOTHING`,
		col.Scope.WorkspaceID, col.Scope.AgentID, col.Scope.TenantOrgID, col.TaskID, col.ID, revision, col.GoalRevision,
		col.OriginSceneID, ReadyEventSource, ReadyEventType, ReadyEventID(col.ID, revision))
	if err != nil {
		return ReadyIntent{}, mapError(err)
	}
	return getIntent(ctx, tx, col.Scope, col.ID, revision, false)
}

func getIntent(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, scope Scope, collectionID string, revision int64, lock bool) (ReadyIntent, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanIntent(db.QueryRow(ctx, `SELECT `+intentColumns+` FROM employee_task_ready_intent
 WHERE `+scopeWhere+` AND collection_id=$4::uuid AND collection_revision=$5`+suffix, scopeArgs(scope, collectionID, revision)...))
}

// jsonEqual compares two JSON documents semantically in PostgreSQL, the same
// way the stored payload was normalized.
func jsonEqual(ctx context.Context, tx pgx.Tx, stored string, data []byte) (bool, error) {
	if stored == "" {
		return false, nil
	}
	var equal bool
	err := tx.QueryRow(ctx, `SELECT $1::jsonb=$2::jsonb`, stored, string(data)).Scan(&equal)
	return equal, err
}
