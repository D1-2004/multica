// Package eventrouter admits provider facts and freezes their scene routing.
// It does not execute tasks or decide whether an actor authorized work.
package eventrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	ReplicaMarker   = "[event-router:1]"
	Version         = 1
	UserMessage     = "user_message"
	Observation     = "observation"
	Control         = "control"
	RunCallback     = "run_callback"
	Wake            = "wake"
	Legacy          = "legacy"
	Unified         = "unified"
	Ready           = "ready"
	Unmapped        = "unmapped"
	MaxPayloadBytes = 1 << 20
)

var (
	ErrInvalidEvent = errors.New("invalid event envelope")
	ErrConflict     = errors.New("event identity conflicts with its admission")
)

// Event is a versioned fact with a business payload, not execution authority.
type Event struct {
	Version       int             `json:"version"`
	ID            string          `json:"id"`
	Source        string          `json:"source"`
	Type          string          `json:"type"`
	Category      string          `json:"category"`
	OccurredAt    time.Time       `json:"occurred_at"`
	PayloadSchema string          `json:"payload_schema"`
	Payload       json.RawMessage `json:"payload"`
}

func (e Event) Validate() error {
	if e.Version != Version || !token(e.ID, 512) || !token(e.Source, 512) ||
		!token(e.Type, 256) || !token(e.PayloadSchema, 256) ||
		len(e.Payload) > MaxPayloadBytes || !json.Valid(e.Payload) {
		return ErrInvalidEvent
	}
	switch e.Category {
	case UserMessage, Observation, Control, RunCallback, Wake:
		return nil
	default:
		return ErrInvalidEvent
	}
}

func token(s string, limit int) bool {
	return s != "" && len(s) <= limit && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}

// Host is constructed after authentication and never decoded from caller JSON.
type Host struct {
	Owner          scene.Owner
	PrincipalID    pgtype.UUID
	TenantOrgID    string
	Locator        scene.Locator
	Observation    scene.Observation
	UnmappedReason string
	Route          string
	ConfigVersion  string
	Fingerprint    string
}

type Beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Admit commits the scene and routing receipt together. An existing receipt
// is immutable: retries do not register, touch or remap a scene.
func Admit(ctx context.Context, pool Beginner, e Event, host Host) (db.SceneEventReceipt, bool, error) {
	return AdmitWithHook(ctx, pool, e, host, nil)
}

// AdmitWithHook applies the storage-only hook only to a new receipt. Historical
// replays never acquire a newly installed business consumer implicitly.
func AdmitWithHook(ctx context.Context, pool Beginner, e Event, host Host, hook ReceiptHook) (db.SceneEventReceipt, bool, error) {
	if err := e.Validate(); err != nil {
		return db.SceneEventReceipt{}, false, err
	}
	if pool == nil || !host.Owner.WorkspaceID.Valid || !host.Owner.AgentID.Valid ||
		!host.PrincipalID.Valid || !token(host.Fingerprint, 256) ||
		(host.Route != Legacy && host.Route != Unified) {
		return db.SceneEventReceipt{}, false, ErrInvalidEvent
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return db.SceneEventReceipt{}, false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// Workspace teardown holds FOR UPDATE before sweeping scenes and receipts.
	// Take the parent lock before source or scene locks so an admission either
	// commits before that sweep or observes that the workspace was deleted.
	var workspace pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1 FOR KEY SHARE`, host.Owner.WorkspaceID).Scan(&workspace); err != nil {
		return db.SceneEventReceipt{}, false, err
	}
	key := strings.Join([]string{util.UUIDToString(host.Owner.WorkspaceID),
		util.UUIDToString(host.Owner.AgentID), e.Source, e.ID}, "\x1f")
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 78))`, key); err != nil {
		return db.SceneEventReceipt{}, false, err
	}
	q := db.New(tx)
	old, err := q.GetSceneEventReceipt(ctx, db.GetSceneEventReceiptParams{
		WorkspaceID: host.Owner.WorkspaceID, AgentID: host.Owner.AgentID,
		Source: e.Source, SourceEventID: e.ID,
	})
	if err == nil {
		if old.Fingerprint != host.Fingerprint || old.PrincipalID != host.PrincipalID {
			return db.SceneEventReceipt{}, false, ErrConflict
		}
		if old.TenantOrgID != host.TenantOrgID {
			return db.SceneEventReceipt{}, false, scene.ErrStaleTenant
		}
		return old, true, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.SceneEventReceipt{}, false, err
	}
	state, reason := Legacy, host.UnmappedReason
	var sceneID pgtype.UUID
	if reason == "" {
		if host.Locator.TenantOrgID != host.TenantOrgID {
			return db.SceneEventReceipt{}, false, ErrInvalidEvent
		}
		resolved, resolveErr := scene.Resolve(ctx, q, host.Owner, host.Locator, host.Observation)
		if resolveErr != nil {
			reason = UnmappedReason(resolveErr)
			if reason == "" {
				return db.SceneEventReceipt{}, false, resolveErr
			}
		} else {
			sceneID = resolved.ID
		}
	}
	if host.Route == Unified {
		state = Ready
		if !sceneID.Valid {
			state = Unmapped
		}
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return db.SceneEventReceipt{}, false, fmt.Errorf("encode event: %w", err)
	}
	row, err := q.InsertSceneEventReceipt(ctx, db.InsertSceneEventReceiptParams{
		WorkspaceID: host.Owner.WorkspaceID, AgentID: host.Owner.AgentID,
		PrincipalID: host.PrincipalID, TenantOrgID: host.TenantOrgID,
		Source: e.Source, SourceEventID: e.ID, Fingerprint: host.Fingerprint,
		Envelope: raw, SceneID: sceneID, Route: host.Route, State: state,
		Reason: reason, ConfigVersion: host.ConfigVersion,
	})
	if err != nil {
		return db.SceneEventReceipt{}, false, err
	}
	if hook != nil {
		if err := hook(ctx, tx, row); err != nil {
			return db.SceneEventReceipt{}, false, err
		}
	}
	return row, false, tx.Commit(ctx)
}

// UnmappedReason exposes bounded codes only; storage failures stay retryable.
func UnmappedReason(err error) string {
	switch {
	case errors.Is(err, scene.ErrUnknownKind):
		return "unknown_kind"
	case errors.Is(err, scene.ErrUnresolved):
		return "missing_locator"
	case errors.Is(err, scene.ErrInvalidLocator):
		return "invalid_locator"
	case errors.Is(err, scene.ErrKindConflict):
		return "kind_conflict"
	case errors.Is(err, scene.ErrStaleTenant):
		return "stale_tenant"
	default:
		return ""
	}
}

// ReceiptHook stores application-owned consumption in the receipt transaction.
// It must not perform model, provider or network calls.
type ReceiptHook func(context.Context, pgx.Tx, db.SceneEventReceipt) error
