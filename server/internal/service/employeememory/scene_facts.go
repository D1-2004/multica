package employeememory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"golang.org/x/text/unicode/norm"
)

// CaptureOrigin records which frozen evidence a statement was grounded in.
type CaptureOrigin string

const (
	// CaptureOriginWindow is a message of the current wake window.
	CaptureOriginWindow CaptureOrigin = "window"
	// CaptureOriginTranscript is a frozen group transcript line (g<N>).
	CaptureOriginTranscript CaptureOrigin = "transcript"
	// CaptureOriginFlush is written by the Host flush writer, not on request.
	CaptureOriginFlush CaptureOrigin = "flush"
)

// MaxSubjectRunes bounds the topic a Host key is derived from.
const MaxSubjectRunes = 40

// SceneFactSourcePrefix namespaces scene-fact replay identities. A provider
// message yields at most one scene fact, whichever writer grounds it first,
// so a forgotten statement can never be recorded again from the same message.
const SceneFactSourcePrefix = "dingtalk-message:"

var (
	// ErrSceneForgetDenied: only the recorder or the quoted speaker may forget
	// a shared scene record from a conversation; others use the management page.
	ErrSceneForgetDenied = errors.New("employee memory: only the recorder or the quoted speaker can forget this shared record")
	// ErrSceneRetractDenied: a writer may retract only its own outputs.
	ErrSceneRetractDenied = errors.New("employee memory: a writer can retract only its own records")
	// ErrSceneKindNotShared: shared statements exist only in group and DM scenes.
	ErrSceneKindNotShared = errors.New("employee memory: shared scene memory is available only in group and direct-message scenes")
	// ErrUngroundedQuote: the quote is not an exact excerpt of frozen human evidence.
	ErrUngroundedQuote = errors.New("employee memory: quote is not an exact excerpt of the grounded human message")
)

// SceneFactGrounding is the frozen evidence a statement is quoted from. The
// Host fills it from the wake window or the frozen transcript, never from
// model arguments.
type SceneFactGrounding struct {
	// MessageID is the provider message id; it is also the replay evidence.
	MessageID string
	// Text is the exact frozen body the quote must be a substring of.
	Text string
	// SpeakerRef is org-qualified: dingtalk:<scope tenant org>:uid|open_id|staff_id:<v>.
	SpeakerRef   string
	SpeakerName  string
	SpeakerClass string
	// SaidAt is the original message time and the reset fence time.
	SaidAt time.Time
}

// SceneFactInput is one statement for the scene-shared layer. Type, Subject
// and Quote may originate from a model; everything else is Host authority.
type SceneFactInput struct {
	Type      LearningType
	Subject   string
	Quote     string
	Origin    CaptureOrigin
	ActorID   string
	Grounding SceneFactGrounding
	// CaptureSourceID is "employee-message:<receipt_id>" of the admitted
	// message that asked for the capture; empty for the flush writer.
	CaptureSourceID string
}

// SceneEntry reports the durable state of one scene record. Replayed means the
// grounding message was already consumed and nothing new was written.
type SceneEntry struct {
	Record    LearningRecord   `json:"record"`
	State     string           `json:"state"`
	Changed   bool             `json:"changed,omitempty"`
	Replayed  bool             `json:"replayed,omitempty"`
	Conflicts []LearningRecord `json:"conflicts,omitempty"`
}

// HostKey derives the learning key from a subject, so corrections reuse a key
// without the model choosing one: <type initial>-<sha256 prefix> over the
// NFKC-normalized, lowercased subject without whitespace and punctuation.
func HostKey(kind LearningType, subject string) (string, error) {
	if !validLearningType(kind) || !validSubject(subject) {
		return "", fmt.Errorf("%w: invalid type or subject", ErrInvalidLearning)
	}
	var b strings.Builder
	for _, r := range strings.ToLower(norm.NFKC.String(subject)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("%w: subject has no letters or digits", ErrInvalidLearning)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return string(kind)[:1] + "-" + hex.EncodeToString(sum[:])[:20], nil
}

func validLearningType(kind LearningType) bool {
	for _, v := range ValidLearningTypes() {
		if v == kind {
			return true
		}
	}
	return false
}

func validSubject(subject string) bool {
	return validText(subject, 4*MaxSubjectRunes) && strings.TrimSpace(subject) == subject && utf8.RuneCountInString(subject) <= MaxSubjectRunes && !strings.ContainsAny(subject, "\r\n")
}

// ValidSpeakerRef accepts only refs qualified by the scope tenant org, in the
// employeeRequesterRef form. A bare staffId or another org's ref is rejected.
func ValidSpeakerRef(ref, tenantOrgID string) bool {
	rest, ok := strings.CutPrefix(ref, "dingtalk:"+tenantOrgID+":")
	if !ok || tenantOrgID == "" || len(ref) > 256 || !utf8.ValidString(ref) {
		return false
	}
	kind, value, ok := strings.Cut(rest, ":")
	return ok && (kind == "uid" || kind == "open_id" || kind == "staff_id") && strings.TrimSpace(value) == value && value != "" && !strings.ContainsAny(value, " \t\r\n\x00")
}

func validateAttribution(rec LearningRecord, scope Scope) error {
	if rec.Subject != "" && !validSubject(rec.Subject) {
		return errors.New("invalid subject")
	}
	if rec.SpeakerRef != "" && !ValidSpeakerRef(rec.SpeakerRef, scope.TenantOrgID) {
		return errors.New("speaker_ref must be qualified by the scope tenant org")
	}
	if len(rec.SpeakerName) > 128 || !utf8.ValidString(rec.SpeakerName) || strings.ContainsRune(rec.SpeakerName, 0) {
		return errors.New("invalid speaker name")
	}
	if rec.CaptureSourceID != "" {
		receipt, ok := strings.CutPrefix(rec.CaptureSourceID, "employee-message:")
		if _, err := uuid.Parse(receipt); !ok || err != nil {
			return errors.New("invalid capture source")
		}
	}
	switch rec.CaptureOrigin {
	case "", CaptureOriginWindow, CaptureOriginTranscript, CaptureOriginFlush:
		return nil
	}
	return errors.New("invalid capture origin")
}

// sceneFactRecord validates the Host input and returns the record and the
// replay evidence. A flush statement is a synthesis candidate; a requested
// capture is an observed statement. Neither is trusted.
func sceneFactRecord(scope Scope, in SceneFactInput) (LearningRecord, TrustedEvidence, error) {
	bad := func(err error, message string) (LearningRecord, TrustedEvidence, error) {
		return LearningRecord{}, TrustedEvidence{}, fmt.Errorf("%w: %s", err, message)
	}
	if scope.Kind != ScopeScene || scope.PrincipalID != "" {
		return LearningRecord{}, TrustedEvidence{}, ErrInvalidScope
	}
	g := in.Grounding
	if g.SpeakerClass != "human" {
		return bad(ErrUngroundedQuote, "only a human statement can be recorded")
	}
	if !ValidSpeakerRef(g.SpeakerRef, scope.TenantOrgID) {
		return bad(ErrUngroundedQuote, "speaker is unknown or not qualified by this tenant")
	}
	quote := strings.TrimSpace(in.Quote)
	if !validText(g.MessageID, 256) || g.SaidAt.IsZero() || quote == "" || !strings.Contains(g.Text, quote) {
		return bad(ErrUngroundedQuote, "the quote must be an exact excerpt of that message")
	}
	key, err := HostKey(in.Type, in.Subject)
	if err != nil {
		return LearningRecord{}, TrustedEvidence{}, err
	}
	rec := LearningRecord{Type: in.Type, Key: key, Insight: quote, Subject: in.Subject, SpeakerRef: g.SpeakerRef, SpeakerName: strings.TrimSpace(g.SpeakerName), SaidAt: g.SaidAt.UTC(), CaptureOrigin: in.Origin, CaptureSourceID: in.CaptureSourceID, Source: LearningSourceObserved, Confidence: 4}
	switch in.Origin {
	case CaptureOriginWindow, CaptureOriginTranscript:
	case CaptureOriginFlush:
		rec.Source, rec.Confidence = LearningSourceSynthesis, 3
	default:
		return bad(ErrInvalidLearning, "invalid capture origin")
	}
	return rec, TrustedEvidence{SourceID: SceneFactSourcePrefix + scope.Scene.SceneID, EvidenceID: g.MessageID, ActorID: in.ActorID, OccurredAt: g.SaidAt}, nil
}

// UpsertSceneFactTx records one grounded statement in the scene-shared layer
// within the caller's transaction (tool journal or flush run). It takes the
// workspace and namespace locks, applies the reset fence and orders by SaidAt:
//   - the same author (recorder, or the quoted speaker correcting themself)
//     supersedes their own current record for the Host key;
//   - another author's current record stays active and the new record names
//     it in conflicts_with, so both are shown as conflicting candidates;
//   - an untrusted statement never supersedes a trusted record;
//   - a message that was already grounded returns its record (Replayed), even
//     when that record was forgotten: tombstones are never revived.
func (s *Store) UpsertSceneFactTx(ctx context.Context, tx pgx.Tx, scope Scope, in SceneFactInput) (SceneEntry, error) {
	if tx == nil {
		return SceneEntry{}, ErrInvalidScope
	}
	rec, e, err := sceneFactRecord(scope, in)
	if err != nil {
		return SceneEntry{}, err
	}
	normalized, err := normalizeRecord(rec, scope, e)
	if err != nil {
		return SceneEntry{}, err
	}
	normalized.EvidenceOccurredAt = e.OccurredAt
	if err = lockWriteScope(ctx, tx, scope); err != nil {
		return SceneEntry{}, err
	}
	if err = requireSharedSceneKind(ctx, tx, scope); err != nil {
		return SceneEntry{}, err
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT id::text FROM employee_learning WHERE `+scopePredicate+` AND replay_key=$7`, append(scope.args(), replayKey(e))...).Scan(&existing)
	if err == nil {
		entry, readErr := s.sceneEntry(ctx, tx, scope, existing)
		entry.Replayed = true
		return entry, readErr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SceneEntry{}, err
	}
	saved, err := recordLocked(ctx, tx, scope, normalized, e, true)
	if err != nil {
		return SceneEntry{}, err
	}
	entry, err := s.sceneEntry(ctx, tx, scope, saved.ID)
	entry.Changed = err == nil && entry.State == "active"
	return entry, err
}

// requireSharedSceneKind reads the trusted directory row: enterprise scenes and
// unknown kinds have no conversation audience to share a statement with.
func requireSharedSceneKind(ctx context.Context, tx pgx.Tx, scope Scope) error {
	id, err := scene.ParseID(scope.Scene.SceneID)
	if err != nil {
		return ErrInvalidScope
	}
	row, err := scene.Get(ctx, db.New(tx), scene.Owner{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID}, id)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidScope, err)
	}
	if row.SceneKind != scene.KindGroup && row.SceneKind != scene.KindDM {
		return ErrSceneKindNotShared
	}
	return nil
}

// SceneEntryTx reports one scene record's current state, including tombstones.
func (s *Store) SceneEntryTx(ctx context.Context, tx pgx.Tx, scope Scope, id string) (SceneEntry, error) {
	if tx == nil || scope.Kind != ScopeScene || scope.PrincipalID != "" {
		return SceneEntry{}, ErrInvalidScope
	}
	if _, err := uuid.Parse(id); err != nil {
		return SceneEntry{}, ErrInvalidLearning
	}
	if err := authorize(ctx, db.New(tx), scope); err != nil {
		return SceneEntry{}, err
	}
	return s.sceneEntry(ctx, tx, scope, id)
}

// sceneEntry reads one record and, while it is active, the other authors'
// active records under the same type and key.
func (s *Store) sceneEntry(ctx context.Context, tx pgx.Tx, scope Scope, id string) (SceneEntry, error) {
	entry, err := readPrivateEntry(ctx, tx, scope, id)
	if err != nil {
		return SceneEntry{}, err
	}
	out := SceneEntry{Record: entry.Record, State: entry.State}
	if out.State != "active" {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT record FROM employee_learning WHERE `+scopePredicate+` AND forgotten_at IS NULL AND superseded_by IS NULL AND record->>'type'=$7 AND record->>'key'=$8 AND id<>$9::uuid ORDER BY created_at DESC,id DESC LIMIT 8`, append(scope.args(), string(entry.Record.Type), entry.Record.Key, id)...)
	if err != nil {
		return SceneEntry{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var peer LearningRecord
		if err = rows.Scan(&raw); err != nil {
			return SceneEntry{}, err
		}
		if err = json.Unmarshal(raw, &peer); err != nil {
			return SceneEntry{}, err
		}
		out.Conflicts = append(out.Conflicts, peer)
	}
	return out, rows.Err()
}

// ForgetSceneTx forgets one exact shared record for a requester who recorded
// it or is its quoted speaker. Replacements and other authors' records stay;
// the replay row stays as a tombstone so the same message is never re-recorded.
func (s *Store) ForgetSceneTx(ctx context.Context, tx pgx.Tx, scope Scope, id, requester string) (SceneEntry, error) {
	return s.tombstoneScene(ctx, tx, scope, id, func(rec LearningRecord) error {
		if requester == "" || (rec.CreatedBy != requester && rec.SpeakerRef != requester) {
			return ErrSceneForgetDenied
		}
		return nil
	})
}

// RetractSceneFactTx lets a writer withdraw its own output, never another
// author's record. The tombstone keeps the message consumed.
func (s *Store) RetractSceneFactTx(ctx context.Context, tx pgx.Tx, scope Scope, id, actor string) (SceneEntry, error) {
	return s.tombstoneScene(ctx, tx, scope, id, func(rec LearningRecord) error {
		if actor == "" || rec.CreatedBy != actor {
			return ErrSceneRetractDenied
		}
		return nil
	})
}

func (s *Store) tombstoneScene(ctx context.Context, tx pgx.Tx, scope Scope, id string, allowed func(LearningRecord) error) (SceneEntry, error) {
	if tx == nil || scope.Kind != ScopeScene || scope.PrincipalID != "" {
		return SceneEntry{}, ErrInvalidScope
	}
	if _, err := uuid.Parse(id); err != nil {
		return SceneEntry{}, ErrInvalidLearning
	}
	if err := lockWriteScope(ctx, tx, scope); err != nil {
		return SceneEntry{}, err
	}
	entry, err := readPrivateEntry(ctx, tx, scope, id)
	if err != nil {
		return SceneEntry{}, err
	}
	if err = allowed(entry.Record); err != nil {
		return SceneEntry{}, err
	}
	if entry.State == "forgotten" {
		return SceneEntry{Record: entry.Record, State: entry.State}, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE employee_learning SET forgotten_at=now() WHERE `+scopePredicate+` AND id=$7::uuid`, append(scope.args(), id)...); err != nil {
		return SceneEntry{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE employee_memory_state SET revision=revision+1,updated_at=now() WHERE `+scopePredicate, scope.args()...); err != nil {
		return SceneEntry{}, err
	}
	return SceneEntry{Record: entry.Record, State: "forgotten", Changed: true}, nil
}

// ForgetSceneByAuthorTx forgets every scene record the author recorded, one
// tombstone per record. It never touches other authors' records and never
// moves the scene reset fence, so other people's evidence stays recordable.
func (s *Store) ForgetSceneByAuthorTx(ctx context.Context, tx pgx.Tx, scope Scope, author string) (int64, error) {
	if tx == nil || scope.Kind != ScopeScene || scope.PrincipalID != "" || !validText(author, 128) {
		return 0, ErrInvalidScope
	}
	if err := lockWriteScope(ctx, tx, scope); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE employee_learning SET forgotten_at=now() WHERE `+scopePredicate+` AND forgotten_at IS NULL AND record->>'created_by'=$7`, append(scope.args(), author)...)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() > 0 {
		if _, err = tx.Exec(ctx, `UPDATE employee_memory_state SET revision=revision+1,updated_at=now() WHERE `+scopePredicate, scope.args()...); err != nil {
			return 0, err
		}
	}
	return tag.RowsAffected(), nil
}

// ActiveSceneFactsTx lists the scene layer's active records, newest first, for
// writers that must see existing statements (attribution, conflicts) before
// proposing changes. It reads inside the caller's transaction without locking.
func (s *Store) ActiveSceneFactsTx(ctx context.Context, tx pgx.Tx, scope Scope, limit int) ([]LearningRecord, error) {
	if tx == nil || scope.Kind != ScopeScene || scope.PrincipalID != "" {
		return nil, ErrInvalidScope
	}
	if err := authorize(ctx, db.New(tx), scope); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := tx.Query(ctx, `SELECT record FROM employee_learning WHERE `+scopePredicate+` AND forgotten_at IS NULL AND superseded_by IS NULL ORDER BY created_at DESC,id DESC LIMIT $7`, append(scope.args(), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LearningRecord{}
	for rows.Next() {
		var raw []byte
		var rec LearningRecord
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
