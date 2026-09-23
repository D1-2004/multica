// Package dshhost coordinates persistent execution sandboxes. Independent
// execution scopes may mount the same employee filesystem concurrently.
// Replacing a scope's writer still requires confirmed destruction because its
// session state must not be opened concurrently by an old and new process.
package dshhost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrPending        = errors.New("DSH host transition requires reconciliation")
	ErrChanged        = errors.New("DSH host generation changed")
	ErrRetireRequired = errors.New("DSH host must be drained and retired before replacement")
	// ErrCreateRejected means FC refused the create; the intent was abandoned.
	ErrCreateRejected = errors.New("DSH host sandbox creation was rejected")
)

var (
	retireStaleAfter = 2 * time.Minute
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
	// AbortRetire restores running if Destroy has not started. Used when a
	// native grant lands after the idle check and before BeginRetire commits.
	AbortRetire(context.Context, Host) error
	// AbandonCreate is a store primitive. Manager never calls it: an empty
	// FindCreated stays pending so listing lag cannot spawn a second writer.
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
	return m.ensure(ctx, key, template, nil, "", false)
}

// EnsurePrivate is the launch fallback while a shared mount cannot be
// confirmed. A running sandbox that still carries any other volume, such as a
// shared mount from an earlier grant, must be rebuilt rather than reused, so a
// revoked or downgraded grant never survives through the fallback.
func (m Manager) EnsurePrivate(ctx context.Context, key Key, template string) (Host, error) {
	return m.ensure(ctx, key, template, nil, "", true)
}

func (m Manager) EnsureWithShared(ctx context.Context, key Key, template string, shared VolumeMountSpec, authRole string) (Host, error) {
	if shared.Name == "" || authRole == "" {
		return Host{}, errors.New("shared mount requires a volume and composite role")
	}
	return m.ensure(ctx, key, template, &shared, authRole, false)
}

func (h Host) stale(after time.Duration) bool {
	return !h.UpdatedAt.IsZero() && after > 0 && time.Since(h.UpdatedAt) >= after
}

func (m Manager) ensure(ctx context.Context, key Key, template string, shared *VolumeMountSpec, authRole string, privateOnly bool) (Host, error) {
	if key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || strings.TrimSpace(template) == "" {
		return Host{}, errors.New("DSH host requires an employee and immutable template ID")
	}
	h, err := m.Store.Get(ctx, key)
	if err != nil {
		return Host{}, err
	}
	if h.State == "creating" {
		// Empty FindCreated is not permission to BeginCreate another sandbox
		// on the same employee volume. Stay pending until ReconcileCreate
		// observes the original intent.
		return Host{}, fmt.Errorf("%w: %s", ErrPending, WaitCreateIntentStale)
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
		if privateOnly {
			if err := m.RequirePrivate(ctx, h); err != nil {
				return Host{}, err
			}
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
		if status, rejected := createRejected(err); rejected {
			// A 4xx answer created nothing, so the intent can be released
			// instead of waiting on a lookup that can never succeed.
			abandonCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if abandonErr := m.Store.AbandonCreate(abandonCtx, h); abandonErr == nil {
				if status == http.StatusTooManyRequests {
					return Host{}, fmt.Errorf("%w: sandbox creation throttled (HTTP %d)", ErrPending, status)
				}
				return Host{}, fmt.Errorf("%w (HTTP %d)", ErrCreateRejected, status)
			}
		}
		return Host{}, fmt.Errorf("%w: sandbox creation outcome is unconfirmed", ErrPending)
	}
	return m.recordCreated(ctx, h, id)
}

// RequirePrivate returns ErrRetireRequired unless the sandbox is confirmed to
// carry exactly the private employee volume. An inspection failure or a
// response without that volume counts as unconfirmed.
// Providers without mount inspection cannot create a shared mount at all.
func (m Manager) RequirePrivate(ctx context.Context, h Host) error {
	inspector, ok := m.Provider.(interface {
		InspectMounts(context.Context, string) ([]VolumeMountSpec, error)
	})
	if !ok {
		return nil
	}
	mounts, err := inspector.InspectMounts(ctx, h.SandboxID)
	if err != nil {
		return fmt.Errorf("%w: sandbox mounts could not be confirmed private", ErrRetireRequired)
	}
	hasPrivate := false
	for _, got := range mounts {
		if got.Path != MountPath || got.Name != h.VolumeName {
			return fmt.Errorf("%w: sandbox still carries a shared mount", ErrRetireRequired)
		}
		hasPrivate = true
	}
	if !hasPrivate {
		// A detail response without the private volume is not a confirmation.
		return fmt.Errorf("%w: sandbox mounts could not be confirmed private", ErrRetireRequired)
	}
	return nil
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
		// Listing lag and transport errors both stay pending. Do not abandon
		// the intent: Create may already have been accepted.
		return Host{}, fmt.Errorf("%w: %s", ErrPending, WaitCreateIntentStale)
	}
	return m.recordCreated(ctx, h, id)
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
		// Caller deadline/cancel must not hide a GET 404: otherwise the host
		// stays retiring and Ensure can never rebuild the employee volume.
		inspectCtx, inspectCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer inspectCancel()
		if m.sandboxGone(inspectCtx, h.SandboxID) && (h.stale(retireStaleAfter) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
			commitCtx, commitCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer commitCancel()
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
	return m.retire(ctx, key, generation, nil)
}

// RetireUnlessBusy begins retire, then re-checks a live hold (native grants).
// Inserts require state=running, so a grant that committed before BeginRetire
// is visible here; a grant after BeginRetire is denied. Crash recovery of an
// already-retiring generation skips the hold and finishes destroy.
func (m Manager) RetireUnlessBusy(ctx context.Context, key Key, generation int64, busy func(Host) (bool, error)) error {
	return m.retire(ctx, key, generation, busy)
}

func (m Manager) retire(ctx context.Context, key Key, generation int64, busy func(Host) (bool, error)) error {
	h, err := m.Store.Get(ctx, key)
	if err != nil {
		return err
	}
	if h.Generation != generation {
		return ErrChanged
	}
	began := false
	if h.State == "running" {
		h, err = m.Store.BeginRetire(ctx, h)
		if err != nil {
			return err
		}
		began = true
	} else if h.State != "retiring" {
		return ErrChanged
	}
	if began && busy != nil {
		blocked, busyErr := busy(h)
		if busyErr != nil {
			return busyErr
		}
		if blocked {
			if abortErr := m.Store.AbortRetire(ctx, h); abortErr != nil {
				return abortErr
			}
			return fmt.Errorf("%w: %s", ErrPending, WaitNativeGrantBusy)
		}
	}
	return m.finishRetire(ctx, h)
}
