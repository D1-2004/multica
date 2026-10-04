// Package scene is the Agent work scene identity (docs/agent-scene.md).
//
// A scene is one agent in one tenant org in one scene instance: a DingTalk
// group or 1:1 conversation by its stable openConversationId, or the
// enterprise itself. Its scene_id is minted once by the server (agent_scene)
// and is the only scene reference anywhere: association graph, scene
// configuration, scene memory, Coordinator jobs, task context, events and
// outbound targets carry a Ref, and the external locator is read back from
// the directory when a provider call needs it.
//
// Resolve is the single entry that turns a trusted locator into a scene.
// The caller supplies the tenant org from the agent's trusted binding (the
// dispatch's recorded org or the agent's DingTalk identity), never from a
// message body, sender or name. Unknown kinds, missing conversation ids and
// missing orgs are rejected; nothing guesses a dm from a staffId or falls
// back to a wider scope.
package scene

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Scene kinds. Group and dm are conversations; enterprise is the tenant org
// itself, the scene of resource events (documents, approvals) that have no
// conversation.
const (
	KindGroup      = "group"
	KindDM         = "dm"
	KindEnterprise = "enterprise"
)

// ProviderDingTalk is the only provider today.
const ProviderDingTalk = "dingtalk"

// Source namespaces say which issuer and validity range an external scene id
// belongs to. A DingTalk openConversationId is issued per org and is the same
// whether the message arrived through the Router or a DWS native
// subscription, so both resolve to one scene. The enterprise scene uses the
// org id itself.
const (
	NamespaceDingTalkConversation = "dingtalk.open_conversation_id"
	NamespaceDingTalkOrg          = "dingtalk.org"
)

const (
	maxExternalIDBytes = 256
	maxTenantOrgBytes  = 128
	maxTitleRunes      = 256
)

var (
	// ErrUnresolved: the source did not supply what identifies a scene (no
	// conversation id, no trusted tenant org). Callers record the event
	// without a scene and skip every scene-dependent effect.
	ErrUnresolved = errors.New("scene_unresolved")
	// ErrUnknownKind: the conversation type is neither a group nor a 1:1
	// chat. It is never guessed.
	ErrUnknownKind = errors.New("scene kind is unknown")
	// ErrInvalidLocator: a locator field is malformed.
	ErrInvalidLocator = errors.New("scene locator is invalid")
	// ErrKindConflict: the external id is registered under another kind.
	ErrKindConflict = errors.New("scene kind conflicts with the registered scene")
	// ErrNotFound: no scene of this agent matches.
	ErrNotFound = errors.New("scene not found")
	// ErrStaleTenant: the scene belongs to an org the agent no longer
	// serves through its binding; using it would cross enterprises.
	ErrStaleTenant = errors.New("scene belongs to an org the agent is no longer bound to")
)

// Ref is SceneRef v1, the one scene reference every record and event carries.
type Ref struct {
	SceneID string `json:"scene_id"`
}

// RefOf returns the Ref of a directory row.
func RefOf(s db.AgentScene) Ref {
	return Ref{SceneID: util.UUIDToString(s.ID)}
}

// Owner is the workspace and agent a scene belongs to. The agent is the
// employee agent that receives and runs the work, never a Tag template and
// never the speaker.
type Owner struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
}

func (o Owner) valid() bool { return o.WorkspaceID.Valid && o.AgentID.Valid }

// Locator is the typed external identity of a scene.
type Locator struct {
	Provider    string
	TenantOrgID string
	Namespace   string
	Kind        string
	ExternalID  string
}

// DingTalkConversation locates a group or 1:1 conversation by its
// openConversationId in a tenant org.
func DingTalkConversation(tenantOrgID, kind, conversationID string) Locator {
	return Locator{
		Provider:    ProviderDingTalk,
		TenantOrgID: tenantOrgID,
		Namespace:   NamespaceDingTalkConversation,
		Kind:        kind,
		ExternalID:  conversationID,
	}
}

// DingTalkEnterprise locates the enterprise scene of a tenant org.
func DingTalkEnterprise(tenantOrgID string) Locator {
	return Locator{
		Provider:    ProviderDingTalk,
		TenantOrgID: tenantOrgID,
		Namespace:   NamespaceDingTalkOrg,
		Kind:        KindEnterprise,
		ExternalID:  tenantOrgID,
	}
}

// KindFromConversationType maps a dispatch conversation type onto a
// conversation kind: "group" (or DingTalk's "2") is a group; single, p2p,
// private, direct (or "1") is a 1:1 chat. Anything else, empty included, is
// unknown and reported as false.
func KindFromConversationType(conversationType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(conversationType)) {
	case "group", "2":
		return KindGroup, true
	case "single", "p2p", "private", "direct", "dm", "1":
		return KindDM, true
	default:
		return "", false
	}
}

// ValidConversationID accepts a DingTalk openConversationId: "cid…", at most
// 256 bytes, no whitespace or control characters.
func ValidConversationID(id string) bool {
	return strings.HasPrefix(id, "cid") && validToken(id, maxExternalIDBytes)
}

func validToken(s string, maxBytes int) bool {
	if s == "" || len(s) > maxBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Normalize trims the locator and checks it. A missing conversation id or
// org is ErrUnresolved, an unknown kind ErrUnknownKind, anything else
// malformed ErrInvalidLocator.
func (l Locator) Normalize() (Locator, error) {
	l.Provider = strings.TrimSpace(l.Provider)
	l.TenantOrgID = strings.TrimSpace(l.TenantOrgID)
	l.Namespace = strings.TrimSpace(l.Namespace)
	l.Kind = strings.TrimSpace(l.Kind)
	l.ExternalID = strings.TrimSpace(l.ExternalID)
	if l.Provider != ProviderDingTalk {
		return Locator{}, fmt.Errorf("%w: provider %q", ErrInvalidLocator, l.Provider)
	}
	if l.TenantOrgID == "" || l.ExternalID == "" {
		return Locator{}, ErrUnresolved
	}
	if !validToken(l.TenantOrgID, maxTenantOrgBytes) {
		return Locator{}, fmt.Errorf("%w: tenant org", ErrInvalidLocator)
	}
	switch l.Kind {
	case KindGroup, KindDM:
		if l.Namespace != NamespaceDingTalkConversation {
			return Locator{}, fmt.Errorf("%w: namespace %q for a conversation", ErrInvalidLocator, l.Namespace)
		}
		if !ValidConversationID(l.ExternalID) {
			return Locator{}, fmt.Errorf("%w: conversation id", ErrInvalidLocator)
		}
	case KindEnterprise:
		if l.Namespace != NamespaceDingTalkOrg || l.ExternalID != l.TenantOrgID {
			return Locator{}, fmt.Errorf("%w: enterprise scene must be its tenant org", ErrInvalidLocator)
		}
	default:
		return Locator{}, ErrUnknownKind
	}
	return l, nil
}

// Kind sources (agent_scene.kind_source): a kind observed from a trusted
// source, or one migration 9510 assigned without evidence (docs/agent-scene.md
// §8).
const (
	KindSourceObserved = "observed"
	KindSourceMigrated = "migrated"
)

// Observation is what an event says about the scene besides its identity.
type Observation struct {
	// KindStated: the locator's kind is the conversation type a trusted
	// inbound event states (a Router or DWS dispatch, a channel callback, a
	// send to a person). Only such an observation settles a migrated kind;
	// a kind a model or a client names never does.
	KindStated bool
	Title      string
	ActiveAt   time.Time
}

// Resolve returns the scene of owner at loc, registering it on first sight.
// Concurrent first sightings return the same scene_id. A registered scene of
// another kind at the same external id is ErrKindConflict, except that an
// observation with KindStated settles a kind migration 9510 assigned
// without evidence (KindSourceMigrated). obs updates the stored title (when
// non-empty) and moves last_active_at forward.
func Resolve(ctx context.Context, q *db.Queries, owner Owner, loc Locator, obs Observation) (db.AgentScene, error) {
	if q == nil || !owner.valid() {
		return db.AgentScene{}, ErrUnresolved
	}
	loc, err := loc.Normalize()
	if err != nil {
		return db.AgentScene{}, err
	}
	title := clipTitle(obs.Title)
	activeAt := obs.ActiveAt
	if activeAt.IsZero() {
		activeAt = time.Now()
	}
	existing, err := find(ctx, q, owner, loc)
	if errors.Is(err, ErrNotFound) {
		inserted, insertErr := q.InsertAgentScene(ctx, db.InsertAgentSceneParams{
			WorkspaceID:     owner.WorkspaceID,
			AgentID:         owner.AgentID,
			Provider:        loc.Provider,
			TenantOrgID:     loc.TenantOrgID,
			SourceNamespace: loc.Namespace,
			SceneKind:       loc.Kind,
			ExternalSceneID: loc.ExternalID,
			Title:           title,
			LastActiveAt:    pgtype.Timestamptz{Time: activeAt.UTC(), Valid: true},
		})
		if insertErr == nil {
			return inserted, nil
		}
		if !errors.Is(insertErr, pgx.ErrNoRows) {
			return db.AgentScene{}, insertErr
		}
		// A concurrent registration won; read it back.
		existing, err = find(ctx, q, owner, loc)
	}
	if err != nil {
		return db.AgentScene{}, err
	}
	if existing.KindSource == KindSourceMigrated && obs.KindStated {
		settled, settleErr := q.SettleAgentSceneKind(ctx, db.SettleAgentSceneKindParams{
			SceneKind: loc.Kind, ID: existing.ID, WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID,
		})
		switch {
		case settleErr == nil:
			existing = settled
		case errors.Is(settleErr, pgx.ErrNoRows):
			// A concurrent observation settled it first; compare with that.
			if existing, err = find(ctx, q, owner, loc); err != nil {
				return db.AgentScene{}, err
			}
		default:
			return db.AgentScene{}, settleErr
		}
	}
	if existing.SceneKind != loc.Kind {
		return db.AgentScene{}, ErrKindConflict
	}
	return q.TouchAgentScene(ctx, db.TouchAgentSceneParams{
		Title:        title,
		LastActiveAt: pgtype.Timestamptz{Time: activeAt.UTC(), Valid: true},
		ID:           existing.ID,
		WorkspaceID:  owner.WorkspaceID,
		AgentID:      owner.AgentID,
	})
}

// Lookup returns the registered scene of owner at loc without registering
// one. loc.Kind may be empty when the caller only has the external id (a
// tool argument); a non-empty kind must match.
func Lookup(ctx context.Context, q *db.Queries, owner Owner, loc Locator) (db.AgentScene, error) {
	if q == nil || !owner.valid() {
		return db.AgentScene{}, ErrUnresolved
	}
	wantKind := strings.TrimSpace(loc.Kind)
	if wantKind == "" {
		// Normalize needs a kind to check the namespace; the stored kind is
		// compared below.
		loc.Kind = KindGroup
		if strings.TrimSpace(loc.Namespace) == NamespaceDingTalkOrg {
			loc.Kind = KindEnterprise
		}
	}
	loc, err := loc.Normalize()
	if err != nil {
		return db.AgentScene{}, err
	}
	found, err := find(ctx, q, owner, loc)
	if err != nil {
		return db.AgentScene{}, err
	}
	if wantKind != "" && found.SceneKind != wantKind {
		return db.AgentScene{}, ErrKindConflict
	}
	return found, nil
}

// Get returns owner's scene by id; another agent's or workspace's scene is
// ErrNotFound.
func Get(ctx context.Context, q *db.Queries, owner Owner, sceneID pgtype.UUID) (db.AgentScene, error) {
	if q == nil || !owner.valid() || !sceneID.Valid {
		return db.AgentScene{}, ErrNotFound
	}
	row, err := q.GetAgentScene(ctx, db.GetAgentSceneParams{ID: sceneID, WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentScene{}, ErrNotFound
	}
	return row, err
}

// ParseID parses a scene_id string; malformed ids are ErrNotFound so callers
// answer them like unknown scenes.
func ParseID(raw string) (pgtype.UUID, error) {
	id, err := util.ParseUUID(strings.TrimSpace(raw))
	if err != nil || !id.Valid {
		return pgtype.UUID{}, ErrNotFound
	}
	return id, nil
}

// CheckTenant is the use-time fence: a scene may be used only while the
// agent still serves its tenant org through the binding the caller read
// (currentOrgID). It is checked before reading private state or sending.
func CheckTenant(s db.AgentScene, currentOrgID string) error {
	if strings.TrimSpace(currentOrgID) == "" || s.TenantOrgID != strings.TrimSpace(currentOrgID) {
		return ErrStaleTenant
	}
	return nil
}

func find(ctx context.Context, q *db.Queries, owner Owner, loc Locator) (db.AgentScene, error) {
	row, err := q.FindAgentSceneByLocator(ctx, db.FindAgentSceneByLocatorParams{
		WorkspaceID:     owner.WorkspaceID,
		AgentID:         owner.AgentID,
		Provider:        loc.Provider,
		TenantOrgID:     loc.TenantOrgID,
		SourceNamespace: loc.Namespace,
		ExternalSceneID: loc.ExternalID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentScene{}, ErrNotFound
	}
	return row, err
}

func clipTitle(title string) string {
	title = strings.TrimSpace(strings.ToValidUTF8(title, ""))
	title = strings.Map(func(r rune) rune {
		if r == 0 || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	if utf8.RuneCountInString(title) <= maxTitleRunes {
		return title
	}
	return strings.TrimSpace(string([]rune(title)[:maxTitleRunes]))
}
