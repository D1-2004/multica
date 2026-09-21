// Package dshhost coordinates persistent execution sandboxes. Independent
// execution scopes may mount the same employee filesystem concurrently.
// Replacing a scope's writer still requires confirmed destruction because its
// session state must not be opened concurrently by an old and new process.
package dshhost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrPending         = errors.New("DSH host transition requires reconciliation")
	ErrChanged         = errors.New("DSH host generation changed")
	ErrRetireRequired  = errors.New("DSH host must be drained and retired before replacement")
	ErrCreateAbandoned = errors.New("DSH create intent was abandoned")
)

var (
	createIntentStaleAfter = 5 * time.Minute
	retireStaleAfter       = 2 * time.Minute
)

const (
	WaitSandboxUnhealthy   = "sandbox_unhealthy"
	WaitDestroyUnconfirmed = "destroy_unconfirmed"
	WaitCreateIntentStale  = "create_intent_stale"
	WaitNativeGrantBusy    = "native_grant_busy"
	WaitTaskDrainBusy      = "task_drain_busy"
)

type Key struct {
	WorkspaceID uuid.UUID
	AgentID     uuid.UUID
}

type Storage struct {
	FileSystemID    string
	SpaceID         string
	VolumeName      string
	AccessPointARN  string
	RoleARN         string
	VPCID           string
	SecurityGroupID string
	VSwitchIDs      []string
}

type Host struct {
	Key
	Storage
	ScopeID      uuid.UUID
	State        string
	Generation   int64
	CreateIntent uuid.UUID
	SandboxID    string
	TemplateID   string
	UpdatedAt    time.Time
	// ExtraMounts and AuthRoleARN are launch overlays, not persisted on the
	// employee host row. They exist so a granted shared volume can be added at
	// Create without changing grant-none DSH sandboxes.
	ExtraMounts []VolumeMountSpec
	AuthRoleARN string
}

// Store implementations must perform transitions atomically in shared storage.
// A process-local map or expiring lock is not a production implementation.
type Store interface {
	Get(context.Context, Key) (Host, error)
	BeginCreate(context.Context, Key, int64, uuid.UUID, string) (Host, error)
	CompleteCreate(context.Context, Host, string) (Host, error)
	BeginRetire(context.Context, Host) (Host, error)
	CompleteRetire(context.Context, Host) error
	AbandonCreate(context.Context, Host) error
}

type Provider interface {
	// Create must label the sandbox with the durable CreateIntent before it
	// can mount storage. Errors are ambiguous and must not cause a retry.
	Create(context.Context, Host) (string, error)
	Healthy(context.Context, string) error
	// DestroyAndConfirmAbsent succeeds only after the provider confirms that
	// the specific sandbox can no longer write, never merely on HTTP 202.
	DestroyAndConfirmAbsent(context.Context, string) error
	// FindCreated returns exactly the sandbox bearing this intent, employee,
	// generation and storage identity. An empty/eventually consistent listing
	// is an error, not permission to create a new sandbox.
	FindCreated(context.Context, Host) (string, error)
}

type Manager struct {
	Store    Store
	Provider Provider
}

func (m Manager) Ensure(ctx context.Context, key Key, template string) (Host, error) {
	return m.ensure(ctx, key, template, nil, "")
}

func (m Manager) EnsureWithShared(ctx context.Context, key Key, template string, shared VolumeMountSpec, authRole string) (Host, error) {
	if shared.Name == "" || authRole == "" {
		return Host{}, errors.New("shared mount requires a volume and composite role")
	}
	return m.ensure(ctx, key, template, &shared, authRole)
}

func (h Host) stale(after time.Duration) bool {
	return !h.UpdatedAt.IsZero() && after > 0 && time.Since(h.UpdatedAt) >= after
}

func (m Manager) ensure(ctx context.Context, key Key, template string, shared *VolumeMountSpec, authRole string) (Host, error) {
	if key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || strings.TrimSpace(template) == "" {
		return Host{}, errors.New("DSH host requires an employee and immutable template ID")
	}
	h, err := m.Store.Get(ctx, key)
	if err != nil {
		return Host{}, err
	}
	if h.State == "creating" {
		if abandoned, abandonErr := m.abandonStaleCreate(ctx, h); abandonErr != nil {
			return Host{}, abandonErr
		} else if abandoned {
			h, err = m.Store.Get(ctx, key)
			if err != nil {
				return Host{}, err
			}
		} else {
			return Host{}, fmt.Errorf("%w: %s", ErrPending, WaitCreateIntentStale)
		}
	}
	if h.State == "retiring" {
		if err := m.finishRetire(ctx, h); err != nil {
			return Host{}, err
		}
		h, err = m.Store.Get(ctx, key)
		if err != nil {
			return Host{}, err
		}
	}
	if h.State == "running" {
		if h.TemplateID != template {
			return Host{}, ErrRetireRequired
		}
		if shared != nil && !m.sandboxHasMount(ctx, h.SandboxID, *shared) {
			return Host{}, ErrRetireRequired
		}
		if err := m.Provider.Healthy(ctx, h.SandboxID); err != nil {
			return Host{}, fmt.Errorf("%w: %s", ErrRetireRequired, WaitSandboxUnhealthy)
		}
		if shared != nil {
			h.ExtraMounts = []VolumeMountSpec{*shared}
			h.AuthRoleARN = authRole
		}
		return h, nil
	}
	if h.State != "offline" {
		return Host{}, errors.New("invalid DSH host state")
	}
	h, err = m.Store.BeginCreate(ctx, key, h.Generation, uuid.New(), template)
	if err != nil {
		return Host{}, err
	}
	if shared != nil {
		h.ExtraMounts = []VolumeMountSpec{*shared}
		h.AuthRoleARN = authRole
	}
	id, err := m.Provider.Create(ctx, h)
	if err != nil || strings.TrimSpace(id) == "" {
		return Host{}, fmt.Errorf("%w: sandbox creation outcome is unconfirmed", ErrPending)
	}
	return m.recordCreated(ctx, h, id)
}

func (m Manager) sandboxHasMount(ctx context.Context, sandboxID string, want VolumeMountSpec) bool {
	inspector, ok := m.Provider.(interface {
		InspectMounts(context.Context, string) ([]VolumeMountSpec, error)
	})
	if !ok || sandboxID == "" {
		return false
	}
	mounts, err := inspector.InspectMounts(ctx, sandboxID)
	if err != nil {
		return false
	}
	for _, got := range mounts {
		if got.Path == want.Path && got.Name == want.Name {
			return true
		}
	}
	return false
}

func (m Manager) recordCreated(ctx context.Context, h Host, id string) (Host, error) {
	// Preserve a known create result even if the request disconnected. If the
	// database is unavailable, reconciliation can recover by CreateIntent.
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	created, err := m.Store.CompleteCreate(commitCtx, h, id)
	if err != nil {
		return Host{}, err
	}
	created.ExtraMounts = h.ExtraMounts
	created.AuthRoleARN = h.AuthRoleARN
	if len(created.ExtraMounts) == 0 {
		if inspector, ok := m.Provider.(interface {
			InspectMounts(context.Context, string) ([]VolumeMountSpec, error)
		}); ok {
			mounts, inspectErr := inspector.InspectMounts(commitCtx, id)
			if inspectErr == nil {
				for _, got := range mounts {
					if got.Path == WorkspaceSharedRoot && got.Name != "" && got.Name != created.VolumeName {
						created.ExtraMounts = []VolumeMountSpec{got}
					}
				}
			}
		}
	}
	return created, nil
}

func (m Manager) ReconcileCreate(ctx context.Context, key Key) (Host, error) {
	h, err := m.Store.Get(ctx, key)
	if err != nil {
		return Host{}, err
	}
	if h.State != "creating" {
		return Host{}, ErrChanged
	}
	id, err := m.Provider.FindCreated(ctx, h)
	if err != nil || strings.TrimSpace(id) == "" {
		if abandoned, abandonErr := m.abandonStaleCreate(ctx, h); abandonErr != nil {
			return Host{}, abandonErr
		} else if abandoned {
			return Host{}, ErrCreateAbandoned
		}
		return Host{}, fmt.Errorf("%w: %s", ErrPending, WaitCreateIntentStale)
	}
	return m.recordCreated(ctx, h, id)
}

func (m Manager) abandonStaleCreate(ctx context.Context, h Host) (bool, error) {
	if h.State != "creating" || !h.stale(createIntentStaleAfter) {
		return false, nil
	}
	id, err := m.Provider.FindCreated(ctx, h)
	if strings.TrimSpace(id) != "" {
		return false, nil
	}
	// Empty listing (ErrPending or blank id) after the stale window may be
	// abandoned. Transport/API errors stay pending so a lost create is not
	// replaced with a second writer on the same volume.
	if err != nil && !errors.Is(err, ErrPending) {
		return false, nil
	}
	if err := m.Store.AbandonCreate(ctx, h); err != nil {
		return false, err
	}
	return true, nil
}

func (m Manager) sandboxGone(ctx context.Context, id string) bool {
	if id == "" {
		return true
	}
	if inspector, ok := m.Provider.(interface {
		SandboxAbsent(context.Context, string) (bool, error)
	}); ok {
		gone, err := inspector.SandboxAbsent(ctx, id)
		return err == nil && gone
	}
	return false
}

func (m Manager) finishRetire(ctx context.Context, h Host) error {
	err := m.Provider.DestroyAndConfirmAbsent(ctx, h.SandboxID)
	if err != nil {
		if m.sandboxGone(ctx, h.SandboxID) && (h.stale(retireStaleAfter) || errors.Is(err, context.DeadlineExceeded)) {
			commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			return m.Store.CompleteRetire(commitCtx, h)
		}
		return fmt.Errorf("%w: %s", ErrPending, WaitDestroyUnconfirmed)
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return m.Store.CompleteRetire(commitCtx, h)
}

// Retire is invoked after admissions are stopped and current tasks are drained.
// Repeating it for the SAME generation recovers a crashed retirement. A stale
// caller can never retire a newer generation.
func (m Manager) Retire(ctx context.Context, key Key, generation int64) error {
	h, err := m.Store.Get(ctx, key)
	if err != nil {
		return err
	}
	if h.Generation != generation {
		return ErrChanged
	}
	if h.State == "running" {
		h, err = m.Store.BeginRetire(ctx, h)
		if err != nil {
			return err
		}
	} else if h.State != "retiring" {
		return ErrChanged
	}
	return m.finishRetire(ctx, h)
}
