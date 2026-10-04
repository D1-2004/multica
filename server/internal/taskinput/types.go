// Package taskinput is the cross-scene input ledger of an EmployeeTask: a
// collection asks invited participants in other scenes for bounded answers,
// counts each invitation once, and writes one durable ready intent when every
// mandatory slot is filled. The origin scene reads the authorized answers; a
// participant reads only its own question, answer and status.
//
// Callers (the Host) authenticate actors, resolve scenes through the
// directory, and verify permissions before invoking this package. Nothing here
// sends a message, calls a model, or trusts payload text as authority. The
// package owns no execution: the coordinator turns a ready intent into a typed
// TaskWake for the origin scene.
//
// Design references GawkBot at 71e82a1809565281cbd0bf8185d3c125b715d934
// (re-implemented, nothing copied): task addressing by reply chain
// (internal/team/task_addressing.go, notification_context.go
// UltimateThreadRoot), the packer egress tiers and final byte scan
// (internal/packer/policy.go, deliver.go), PENDING-never-resend delivery, and
// inbound guards against self and bot messages (slack_transport.go).
package taskinput

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrInvalid: malformed input or a rule violation the Host should have
	// prevented (wrong binding kind for the scene, missing evidence).
	ErrInvalid = errors.New("task input is invalid")
	// ErrNotFound: the record does not exist in this scope, or the actor or
	// scene presented is not the one it belongs to. Cross-scope access never
	// learns more than this.
	ErrNotFound = errors.New("task input record not found in scope")
	// ErrConflict: an expected revision or attempt is stale, or one source
	// was replayed with different content.
	ErrConflict = errors.New("task input revision or source payload conflict")
	// ErrStaleRevision: the expected collection revision moved (another
	// answer, a correction, a close). It is an ErrConflict; the Host re-reads,
	// re-binds and retries, unlike a source payload conflict.
	ErrStaleRevision = fmt.Errorf("%w: expected revision is stale; re-read and re-bind", ErrConflict)
	// ErrClosed: the collection, invitation or task no longer accepts this
	// write (completed, cancelled, revoked, expired, late). A late answer never
	// creates a new Task.
	ErrClosed = errors.New("task input collection or invitation is closed")
	// ErrAlreadyAnswered: the invitation already has an effective answer and
	// the new input was not marked as an explicit correction.
	ErrAlreadyAnswered = errors.New("invitation already answered; only an explicit correction replaces it")
	// ErrDeliveryPending: an invitation send is in flight or its outcome is
	// unknown. The original action must be queried; it is never resent.
	ErrDeliveryPending = errors.New("invitation delivery is pending; query the original action instead of resending")
	// ErrForbidden: the actor is not allowed to perform this transition (for
	// example a partial close by someone other than the original requester).
	ErrForbidden = errors.New("actor is not authorized for this collection transition")
)

// Scope is the trusted ownership every row carries. A record outside the scope
// is reported as ErrNotFound.
type Scope struct {
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	TenantOrgID string `json:"tenant_org_id"`
}

// Source identifies one exact source action (a message, a tool call, a host
// sweep), never a body hash. Replaying it with identical content is idempotent;
// with different content it conflicts.
type Source struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

// Authority is Host-verified evidence for one write: who acted, in which
// directory scene, through which persisted receipt. The Host builds it from a
// trusted admission and the current directory; model output and message text
// never produce it. VerifiedAt is not part of replay identity.
type Authority struct {
	ActorRef   string    `json:"actor_ref"`
	SceneID    string    `json:"scene_id"`
	ReceiptRef string    `json:"receipt_ref"`
	VerifiedAt time.Time `json:"-"`
}

// HostActorPrefix marks an actor that is the Host itself (a sweeper or a
// permission reconciler), never a person.
const HostActorPrefix = "host:"

type CollectionState string

const (
	CollectionOpen        CollectionState = "open"
	CollectionReady       CollectionState = "ready"
	CollectionSummarizing CollectionState = "summarizing"
	CollectionCompleted   CollectionState = "completed"
	CollectionCancelled   CollectionState = "cancelled"
	CollectionRevoked     CollectionState = "revoked"
	CollectionExpired     CollectionState = "expired"
)

// Active reports whether the collection can still change (accept input,
// corrections, or be summarized).
func (s CollectionState) Active() bool {
	return s == CollectionOpen || s == CollectionReady || s == CollectionSummarizing
}

// CompletionAllRequired is the only rule in v1: every invitation must be
// answered. "Enough" is never guessed from a message; an early close is an
// explicit, audited partial close by the original requester.
const CompletionAllRequired = "all_required"

type InvitationState string

const (
	// InvitationPendingScene: a 1:1 invitation to a person who has no DM scene
	// yet. The send goes to the person; the DM scene is backfilled from the
	// provider receipt (RecordDeliveryParams.TargetSceneID). No input is
	// accepted until then, and no scene is ever minted from a person.
	InvitationPendingScene    InvitationState = "pending_scene"
	InvitationPendingDelivery InvitationState = "pending_delivery"
	InvitationDelivered       InvitationState = "delivered"
	InvitationAnswered        InvitationState = "answered"
	InvitationRevoked         InvitationState = "revoked"
	InvitationExpired         InvitationState = "expired"
)

// DeliveryOutcome is the last known provider fact for the current attempt.
type DeliveryOutcome string

const (
	// DeliveryPending: the send intent exists; the outbox owns the attempt.
	DeliveryPending DeliveryOutcome = "pending"
	// DeliveryUnknown: the provider call did not return a definite result.
	// The original action must be queried; it is never resent.
	DeliveryUnknown DeliveryOutcome = "unknown"
	// DeliveryFailed: the provider definitely did not deliver; a new attempt
	// with a new action id is allowed.
	DeliveryFailed DeliveryOutcome = "failed"
	// DeliveryHeld: the final egress scan refused the rendered bytes; nothing
	// was sent and the requester decides.
	DeliveryHeld DeliveryOutcome = "held"
	// DeliverySent: the provider accepted the message and returned its id.
	DeliverySent DeliveryOutcome = "sent"
)

// BindingKind records how an inbound message was bound to an invitation.
type BindingKind string

const (
	// BindReplyChain: the provider reply/quote chain reaches the invitation
	// message within MaxReplyHops. The only binding valid in a group.
	BindReplyChain BindingKind = "reply_chain"
	// BindDMSinglePending: no quote, in a 1:1 scene where the sender has
	// exactly one delivered, unanswered invitation.
	BindDMSinglePending BindingKind = "dm_single_pending"
	// BindInviteReference: an explicit invitation reference the Host resolved
	// (for example a clarification choice it offered), only in a 1:1 scene.
	BindInviteReference BindingKind = "invite_reference"
)

// SenderKind is the Host's classification of an inbound sender.
type SenderKind string

const (
	// SenderPerson is a human account.
	SenderPerson SenderKind = "person"
	// SenderDigitalEmployee is another digital employee's own account (for
	// example a DEAP actor). It answers like a person when explicitly invited.
	SenderDigitalEmployee SenderKind = "digital_employee"
	// SenderBot is a robot or app sender. Bots never answer an invitation.
	SenderBot SenderKind = "bot"
	// SenderSelf is this Agent (any of its identities). Its own messages,
	// including echoed invitations, never count.
	SenderSelf SenderKind = "self"
)

// MessageKind is the Host's classification of an inbound message body.
type MessageKind string

const (
	MessageText     MessageKind = "text"
	MessageRichText MessageKind = "rich_text"
	MessageImage    MessageKind = "image"
	MessageFile     MessageKind = "file"
	MessageAudio    MessageKind = "audio"
	MessageVideo    MessageKind = "video"
	// MessageCard covers interactive/intro/placeholder cards, including the
	// cards digital employees auto-post when they join a group. Never an answer.
	MessageCard MessageKind = "card"
	// MessageSystem covers join/leave/recall notices. Never an answer.
	MessageSystem MessageKind = "system"
)

// Answerable reports whether a message of this kind can carry an answer.
func (k MessageKind) Answerable() bool {
	switch k {
	case MessageText, MessageRichText, MessageImage, MessageFile, MessageAudio, MessageVideo:
		return true
	default:
		return false
	}
}

// Answerer reports whether a sender of this kind can answer an invitation.
func (k SenderKind) Answerer() bool {
	return k == SenderPerson || k == SenderDigitalEmployee
}

// Deadline is an explicit collection deadline in the requester's time zone.
type Deadline struct {
	At       time.Time `json:"at"`
	TimeZone string    `json:"time_zone"`
}

type Collection struct {
	ID                string          `json:"id"`
	Scope             Scope           `json:"scope"`
	TaskID            string          `json:"task_id"`
	OriginSceneID     string          `json:"origin_scene_id"`
	AuthorityRef      string          `json:"authority_ref"`
	RequesterRef      string          `json:"requester_ref"`
	DeliveryAnchorRef string          `json:"delivery_anchor_ref"`
	GoalRevision      int64           `json:"goal_revision"`
	Revision          int64           `json:"revision"`
	State             CollectionState `json:"state"`
	CompletionRule    string          `json:"completion_rule"`
	ExpectedCount     int             `json:"expected_count"`
	ReceivedCount     int             `json:"received_count"`
	Deadline          *Deadline       `json:"deadline,omitempty"`
	CloseMode         string          `json:"close_mode,omitempty"`
	CloseActorRef     string          `json:"close_actor_ref,omitempty"`
	CloseReason       string          `json:"close_reason,omitempty"`
	SummaryRef        string          `json:"summary_ref,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	ClosedAt          *time.Time      `json:"closed_at,omitempty"`
}

type Invitation struct {
	ID                 string          `json:"id"`
	Scope              Scope           `json:"scope"`
	TaskID             string          `json:"task_id"`
	CollectionID       string          `json:"collection_id"`
	Ordinal            int             `json:"ordinal"`
	CollectionRevision int64           `json:"collection_revision"`
	TargetSceneID      string          `json:"target_scene_id"`
	TargetSceneKind    string          `json:"target_scene_kind"`
	ParticipantRef     string          `json:"participant_ref"`
	ParticipantLabel   string          `json:"participant_label,omitempty"`
	AuthorizationScope string          `json:"authorization_scope"`
	Question           string          `json:"question"`
	ExpiresAt          *time.Time      `json:"expires_at,omitempty"`
	State              InvitationState `json:"state"`
	DeliveryAttempt    int             `json:"delivery_attempt"`
	DeliveryActionID   string          `json:"delivery_action_id"`
	DeliveryOutcome    DeliveryOutcome `json:"delivery_outcome"`
	DeliveryError      string          `json:"delivery_error,omitempty"`
	ProviderMessageID  string          `json:"provider_message_id,omitempty"`
	RenderedHash       string          `json:"rendered_hash,omitempty"`
	EffectiveVersion   int             `json:"effective_version"`
	EndReason          string          `json:"end_reason,omitempty"`
	DeliveredAt        *time.Time      `json:"delivered_at,omitempty"`
	AnsweredAt         *time.Time      `json:"answered_at,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// Input is one accepted answer version. The source message's receipt stays in
// its own (B) scene; ReceiptRef only points at it.
type Input struct {
	ID                 string      `json:"id"`
	Scope              Scope       `json:"scope"`
	TaskID             string      `json:"task_id"`
	CollectionID       string      `json:"collection_id"`
	InvitationID       string      `json:"invitation_id"`
	Version            int         `json:"version"`
	Kind               string      `json:"kind"`
	Binding            BindingKind `json:"binding"`
	Source             Source      `json:"source"`
	SourceSceneID      string      `json:"source_scene_id"`
	ReceiptRef         string      `json:"receipt_ref"`
	ProviderMessageID  string      `json:"provider_message_id,omitempty"`
	ActorRef           string      `json:"actor_ref"`
	OccurredAt         time.Time   `json:"occurred_at"`
	Body               string      `json:"body,omitempty"`
	BodyRef            string      `json:"body_ref,omitempty"`
	CollectionRevision int64       `json:"collection_revision"`
	CreatedAt          time.Time   `json:"created_at"`
}

// ReadyIntent is the durable, unique-per-revision request to wake the origin
// scene with kind collection.ready. Its event identity and OccurredAt are
// frozen at first insert, so a retry or crash recovery never re-times it.
type ReadyIntent struct {
	Scope              Scope     `json:"scope"`
	TaskID             string    `json:"task_id"`
	CollectionID       string    `json:"collection_id"`
	CollectionRevision int64     `json:"collection_revision"`
	GoalRevision       int64     `json:"goal_revision"`
	OriginSceneID      string    `json:"origin_scene_id"`
	EventSource        string    `json:"event_source"`
	EventType          string    `json:"event_type"`
	EventID            string    `json:"event_id"`
	OccurredAt         time.Time `json:"occurred_at"`
	State              string    `json:"state"`
	AdmittedRef        string    `json:"admitted_ref,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

const (
	ReadyEventSource = "employee.collection"
	ReadyEventType   = "collection.ready"

	ReadyIntentPending    = "pending"
	ReadyIntentAdmitted   = "admitted"
	ReadyIntentSuperseded = "superseded"
)

// EvidenceRef is the reference a TaskWake carries for this intent.
func (r ReadyIntent) EvidenceRef() string { return "collection:" + r.EventID }

// InvitationSpec is one participant to ask. TargetSceneID is a dm or group
// scene of the same agent and tenant from the directory. It may be empty only
// for a 1:1 invitation to a person without a DM scene yet, and then TargetKind
// must say "dm" explicitly: a kind is never guessed, and a group target always
// needs its scene up front. ParticipantRef is the normalized identity from the
// Host's trusted identity bridge.
type InvitationSpec struct {
	TargetSceneID  string `json:"target_scene_id,omitempty"`
	TargetKind     string `json:"target_kind,omitempty"`
	ParticipantRef string `json:"participant_ref"`
	// ParticipantLabel is the provider display name, for views only; it is
	// never an identity.
	ParticipantLabel string     `json:"participant_label,omitempty"`
	Question         string     `json:"question"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

type CreateCollectionParams struct {
	TaskID            string           `json:"task_id"`
	OriginSceneID     string           `json:"origin_scene_id"`
	AuthorityRef      string           `json:"authority_ref"`
	RequesterRef      string           `json:"requester_ref"`
	DeliveryAnchorRef string           `json:"delivery_anchor_ref"`
	GoalRevision      int64            `json:"goal_revision"`
	Deadline          *Deadline        `json:"deadline,omitempty"`
	Invitations       []InvitationSpec `json:"invitations"`
	Source            Source           `json:"source"`
	Authority         Authority        `json:"authority"`
}

// RecordDeliveryParams reports the provider fact for the invitation's current
// delivery action.
type RecordDeliveryParams struct {
	InvitationID      string          `json:"invitation_id"`
	ActionID          string          `json:"action_id"`
	Outcome           DeliveryOutcome `json:"outcome"`
	ProviderMessageID string          `json:"provider_message_id,omitempty"`
	RenderedHash      string          `json:"rendered_hash,omitempty"`
	Error             string          `json:"error,omitempty"`
	// TargetSceneID backfills the DM scene of a pending_scene invitation,
	// resolved by the Host (scene.Resolve, kind dm) from the send's provider
	// receipt. Set once: the same value replays, a different one conflicts.
	TargetSceneID string `json:"target_scene_id,omitempty"`
}

type RetryDeliveryParams struct {
	InvitationID    string `json:"invitation_id"`
	ExpectedAttempt int    `json:"expected_attempt"`
}

type AcceptInputParams struct {
	CollectionID      string      `json:"collection_id"`
	InvitationID      string      `json:"invitation_id"`
	Source            Source      `json:"source"`
	Authority         Authority   `json:"authority"`
	SenderKind        SenderKind  `json:"sender_kind"`
	MessageKind       MessageKind `json:"message_kind"`
	Binding           BindingKind `json:"binding"`
	ProviderMessageID string      `json:"provider_message_id,omitempty"`
	OccurredAt        time.Time   `json:"occurred_at"`
	Body              string      `json:"body,omitempty"`
	BodyRef           string      `json:"body_ref,omitempty"`
	// Correction is an explicit, Host-confirmed replacement of the effective
	// answer. Without it a second answer to the same invitation is refused.
	Correction       bool  `json:"correction,omitempty"`
	ExpectedRevision int64 `json:"-"`
}

type AcceptResult struct {
	Collection Collection   `json:"collection"`
	Invitation Invitation   `json:"invitation"`
	Input      Input        `json:"input"`
	Replayed   bool         `json:"replayed"`
	Ready      *ReadyIntent `json:"ready,omitempty"`
}

type CloseMode string

const (
	// ClosePartial: the original requester stops waiting and asks for a
	// summary of what has arrived. Requires the requester and a reason.
	ClosePartial CloseMode = "partial"
	// CloseCancel: the task or collection was cancelled.
	CloseCancel CloseMode = "cancel"
	// CloseRevoke: the origin authority was withdrawn.
	CloseRevoke CloseMode = "revoke"
	// CloseExpire: the explicit deadline passed (checked with the DB clock).
	CloseExpire CloseMode = "expire"
)

type CloseParams struct {
	CollectionID     string    `json:"collection_id"`
	Mode             CloseMode `json:"mode"`
	Reason           string    `json:"reason"`
	Source           Source    `json:"source"`
	Authority        Authority `json:"authority"`
	ExpectedRevision int64     `json:"-"`
}

type RevokeInvitationParams struct {
	CollectionID     string    `json:"collection_id"`
	InvitationID     string    `json:"invitation_id"`
	Reason           string    `json:"reason"`
	Source           Source    `json:"source"`
	Authority        Authority `json:"authority"`
	ExpectedRevision int64     `json:"-"`
}

type CompleteParams struct {
	CollectionID     string `json:"collection_id"`
	SummaryRef       string `json:"summary_ref"`
	Source           Source `json:"source"`
	ExpectedRevision int64  `json:"-"`
}

// Answer is an effective, authorized answer as a reader sees it.
type Answer struct {
	Version           int       `json:"version"`
	Corrected         bool      `json:"corrected"`
	Body              string    `json:"body,omitempty"`
	BodyRef           string    `json:"body_ref,omitempty"`
	ProviderMessageID string    `json:"provider_message_id,omitempty"`
	OccurredAt        time.Time `json:"occurred_at"`
}

// OriginViewer is the origin scene asking for its Task's answers.
type OriginViewer struct {
	TaskID  string `json:"task_id"`
	SceneID string `json:"scene_id"`
}

type OriginSlot struct {
	InvitationID     string          `json:"invitation_id"`
	Ordinal          int             `json:"ordinal"`
	ParticipantRef   string          `json:"participant_ref"`
	ParticipantLabel string          `json:"participant_label,omitempty"`
	TargetSceneID    string          `json:"target_scene_id"`
	Question         string          `json:"question"`
	State            InvitationState `json:"state"`
	Answer           *Answer         `json:"answer,omitempty"`
}

type OriginView struct {
	CollectionID   string          `json:"collection_id"`
	TaskID         string          `json:"task_id"`
	State          CollectionState `json:"state"`
	Revision       int64           `json:"revision"`
	GoalRevision   int64           `json:"goal_revision"`
	CompletionRule string          `json:"completion_rule"`
	Expected       int             `json:"expected"`
	Received       int             `json:"received"`
	Deadline       *Deadline       `json:"deadline,omitempty"`
	CloseMode      string          `json:"close_mode,omitempty"`
	CloseReason    string          `json:"close_reason,omitempty"`
	Slots          []OriginSlot    `json:"slots"`
}

// ParticipantViewer is an invited participant in its own scene.
type ParticipantViewer struct {
	ActorRef string `json:"actor_ref"`
	SceneID  string `json:"scene_id"`
}

// ParticipantView deliberately omits the Task, origin scene, requester, other
// slots and counts: a participant sees only its question, answer and status.
type ParticipantView struct {
	InvitationID string          `json:"invitation_id"`
	Question     string          `json:"question"`
	State        InvitationState `json:"state"`
	ExpiresAt    *time.Time      `json:"expires_at,omitempty"`
	Deadline     *Deadline       `json:"deadline,omitempty"`
	Answer       *Answer         `json:"answer,omitempty"`
}

// BindingCandidate is one of the sender's own open invitations, as the
// binding rules see it. Question is the sender's own question, safe to show
// back to that sender when asking which one they answered.
type BindingCandidate struct {
	InvitationID      string          `json:"invitation_id"`
	CollectionID      string          `json:"collection_id"`
	TaskID            string          `json:"task_id"`
	TargetSceneID     string          `json:"target_scene_id"`
	ParticipantRef    string          `json:"participant_ref"`
	ProviderMessageID string          `json:"provider_message_id,omitempty"`
	State             InvitationState `json:"state"`
	Revision          int64           `json:"revision"`
	Question          string          `json:"question"`
}

// TaskWait summarizes one non-terminal collection of a Task for lifecycle
// checks: an open collection is an unsatisfied mandatory wait.
type TaskWait struct {
	CollectionID string          `json:"collection_id"`
	State        CollectionState `json:"state"`
	Revision     int64           `json:"revision"`
	Expected     int             `json:"expected"`
	Received     int             `json:"received"`
}
