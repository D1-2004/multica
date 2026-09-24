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
	ErrCreateRejected = errors.New("DSH host sandbox create was rejected")
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
	// Observed* is the last confirmed shared-mount identity on this generation.
	// It is empty until a detail read succeeds. A later failed read may trust
	// it only when the new grant is not tighter.
	ObservedSharedKnown     bool
	ObservedSharedVolume    string
	ObservedSharedAccess    string
	ObservedGrantGeneration int64
}

// SharedTarget is the grant a running or new sandbox must honor.
// Access empty keeps the historical rule: a different shared volume is wider.
// Access write treats a different shared volume as narrower, so an RO host
// stays up until a normal rebuild. Access none drops any shared volume.
type SharedTarget struct {
	Access          string
	Volume          string
	OtherVolume     string
	RoleARN         string
	GrantGeneration int64
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
	// AbandonCreate releases a creating intent. Manager calls it only after
	// the provider definitively rejected the create request. An empty
	// FindCreated stays pending so listing lag cannot spawn a second writer.
	AbandonCreate(context.Context, Host) error
}

type Provider interface {
	// Create must label the sandbox with the durable CreateIntent before it
	// can mount storage. Errors are ambiguous and must not cause a retry,
	// except an *FCStatusError whose status proves the request was refused.
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
	return m.ensure(ctx, key, template, nil, false)
}

// EnsurePrivate is the launch fallback while a shared mount cannot be
// confirmed. A running sandbox that still carries any other volume, such as a
// shared mount from an earlier grant, must be rebuilt rather than reused, so a
// revoked or downgraded grant never survives through the fallback.
func (m Manager) EnsurePrivate(ctx context.Context, key Key, template string) (Host, error) {
	return m.ensure(ctx, key, template, nil, true)
}

func (m Manager) EnsureWithShared(ctx context.Context, key Key, template string, shared VolumeMountSpec, authRole string) (Host, error) {
	return m.EnsureWithSharedGrant(ctx, key, template, SharedTarget{Volume: shared.Name, RoleARN: authRole})
}

// EnsureWithSharedGrant applies one grant to a new sandbox, or compares it
// with the mount a running sandbox already has.
func (m Manager) EnsureWithSharedGrant(ctx context.Context, key Key, template string, target SharedTarget) (Host, error) {
	if target.Volume == "" || target.RoleARN == "" {
		return Host{}, errors.New("shared mount requires a volume and composite role")
	}
	return m.ensure(ctx, key, template, &target, false)
}

// EnsureConstrained checks a read grant against a sandbox that may already
// have a wider shared volume. A new sandbox stays private: the caller has
// not declared that this image can mount the shared disk.
func (m Manager) EnsureConstrained(ctx context.Context, key Key, template string, shared VolumeMountSpec, authRole string) (Host, error) {
	if shared.Name == "" || authRole == "" {
		return Host{}, errors.New("shared mount requires a volume and composite role")
	}
	h, err := m.Store.Get(ctx, key)
	if err != nil {
		return Host{}, err
	}
	if h.State == "running" {
		return m.ensure(ctx, key, template, &SharedTarget{Access: "read", Volume: shared.Name, RoleARN: authRole}, false)
	}
	return m.ensure(ctx, key, template, nil, false)
}

func (h Host) stale(after time.Duration) bool {
	return !h.UpdatedAt.IsZero() && after > 0 && time.Since(h.UpdatedAt) >= after
}

func (m Manager) ensure(ctx context.Context, key Key, template string, target *SharedTarget, privateOnly bool) (Host, error) {
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
		// A concurrent launch must not finish a retire. The holder of the
		// retire checks native grants before destroy; completing it here can
		// delete a sandbox that check still has to see.
		return Host{}, fmt.Errorf("%w: %s", ErrPending, WaitDestroyUnconfirmed)
	}
	if h.State == "running" {
		if h.TemplateID != template {
			return Host{}, ErrRetireRequired
		}
		if privateOnly {
			revoked := SharedTarget{Access: "none"}
			if target != nil {
				revoked.GrantGeneration = target.GrantGeneration
			}
			return m.AuthorizeRunning(ctx, h, &revoked)
		}
		return m.AuthorizeRunning(ctx, h, target)
	}
	if h.State != "offline" {
		return Host{}, errors.New("invalid DSH host state")
	}
	h, err = m.Store.BeginCreate(ctx, key, h.Generation, uuid.New(), template)
	if err != nil {
		return Host{}, err
	}
	if !privateOnly && target != nil && target.Volume != "" && target.Access != "none" {
		h.ExtraMounts = []VolumeMountSpec{{Name: target.Volume, Path: WorkspaceSharedRoot}}
		h.AuthRoleARN = target.RoleARN
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

// SandboxDetail is one provider read of a sandbox's state and volumes.
type SandboxDetail struct {
	State  string
	Mounts []VolumeMountSpec
}

// sandboxInspector is implemented by providers that can read a sandbox's
// volumes. Providers without it cannot create a shared mount at all.
type sandboxInspector interface {
	InspectSandbox(context.Context, string) (SandboxDetail, error)
}

// RequirePrivate returns ErrRetireRequired unless the sandbox is confirmed to
// carry exactly the private employee volume. An inspection failure or a
// response without that volume counts as unconfirmed.
func (m Manager) RequirePrivate(ctx context.Context, h Host) error {
	inspector, ok := m.Provider.(sandboxInspector)
	if !ok {
		return nil
	}
	detail, err := inspector.InspectSandbox(ctx, h.SandboxID)
	if err != nil {
		return fmt.Errorf("%w: sandbox mounts could not be confirmed private", ErrRetireRequired)
	}
	return requirePrivateMounts(h, detail.Mounts)
}

func requirePrivateMounts(h Host, mounts []VolumeMountSpec) error {
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

// AuthorizeRunning compares a live sandbox with the grant. Adding a share or
// widening read to write does not retire a healthy host. A tighter grant or
// an explicit none enters the drain path. A failed detail read waits, unless
// a recorded mount shows the grant was not tightened.
func (m Manager) AuthorizeRunning(ctx context.Context, h Host, target *SharedTarget) (Host, error) {
	if h.SandboxID == "" {
		return Host{}, fmt.Errorf("%w: shared mount inspection is unconfirmed", ErrPending)
	}
	if target == nil {
		if err := m.Provider.Healthy(ctx, h.SandboxID); err != nil {
			return Host{}, fmt.Errorf("%w: %s", ErrRetireRequired, WaitSandboxUnhealthy)
		}
		return h, nil
	}
	inspector, ok := m.Provider.(sandboxInspector)
	if !ok {
		if err := m.Provider.Healthy(ctx, h.SandboxID); err != nil {
			return Host{}, fmt.Errorf("%w: %s", ErrRetireRequired, WaitSandboxUnhealthy)
		}
		return h, nil
	}
	detail, err := inspector.InspectSandbox(ctx, h.SandboxID)
	if err != nil {
		return m.reuseRecordedMount(h, *target)
	}
	if detail.State != "running" {
		return Host{}, fmt.Errorf("%w: %s", ErrRetireRequired, WaitSandboxUnhealthy)
	}
	if target.Access == "none" {
		if err := requirePrivateMounts(h, detail.Mounts); err != nil {
			return Host{}, err
		}
		m.rememberShared(ctx, h, "", "none", target.GrantGeneration)
		h.ExtraMounts = nil
		h.AuthRoleARN = ""
		return h, nil
	}
	live := sharedVolumeName(detail.Mounts)
	switch relateLiveMount(live, *target) {
	case "absent":
		m.rememberShared(ctx, h, "", "none", target.GrantGeneration)
		h.ExtraMounts = nil
		h.AuthRoleARN = ""
		return h, nil
	case "match":
		recorded := target.Access
		if recorded == "" {
			recorded = "read"
		}
		m.rememberShared(ctx, h, target.Volume, recorded, target.GrantGeneration)
		h.ExtraMounts = []VolumeMountSpec{{Name: target.Volume, Path: WorkspaceSharedRoot}}
		h.AuthRoleARN = target.RoleARN
		return h, nil
	case "narrower":
		recorded := "read"
		if target.OtherVolume != "" && live == target.OtherVolume && target.Access == "read" {
			recorded = "write"
		}
		m.rememberShared(ctx, h, live, recorded, target.GrantGeneration)
		h.ExtraMounts = []VolumeMountSpec{{Name: live, Path: WorkspaceSharedRoot}}
		h.AuthRoleARN = ""
		return h, nil
	default:
		return Host{}, fmt.Errorf("%w: shared mount is wider than the grant", ErrRetireRequired)
	}
}

func sharedVolumeName(mounts []VolumeMountSpec) string {
	for _, got := range mounts {
		if got.Path == WorkspaceSharedRoot && got.Name != "" {
			return got.Name
		}
	}
	return ""
}

// relateLiveMount classifies the volume already mounted at the shared path.
// A write grant never treats a different volume as wider: write is the
// maximum, and the RO host keeps running until a normal rebuild.
func relateLiveMount(live string, target SharedTarget) string {
	if live == "" {
		return "absent"
	}
	if target.Volume != "" && live == target.Volume {
		return "match"
	}
	if target.Access == "write" {
		return "narrower"
	}
	return "wider"
}

func grantTightened(targetAccess, observedAccess string) bool {
	rank := func(access string) int {
		switch access {
		case "write":
			return 2
		case "read":
			return 1
		default:
			return 0
		}
	}
	return rank(targetAccess) < rank(observedAccess)
}

func (m Manager) reuseRecordedMount(h Host, target SharedTarget) (Host, error) {
	if !h.ObservedSharedKnown || grantTightened(target.Access, h.ObservedSharedAccess) {
		return Host{}, fmt.Errorf("%w: shared mount inspection is unconfirmed", ErrPending)
	}
	if h.ObservedSharedVolume != "" {
		h.ExtraMounts = []VolumeMountSpec{{Name: h.ObservedSharedVolume, Path: WorkspaceSharedRoot}}
		h.AuthRoleARN = ""
	}
	return h, nil
}

func (m Manager) rememberShared(ctx context.Context, h Host, volume, access string, grantGeneration int64) {
	recorder, ok := m.Store.(interface {
		RecordSharedObservation(context.Context, Key, int64, string, string, int64) error
	})
	if !ok {
		return
	}
	_ = recorder.RecordSharedObservation(ctx, h.Key, h.Generation, volume, access, grantGeneration)
}

func (m Manager) sandboxHasMount(ctx context.Context, sandboxID string, want VolumeMountSpec) bool {
	inspector, ok := m.Provider.(sandboxInspector)
	if !ok || sandboxID == "" {
		return false
	}
	detail, err := inspector.InspectSandbox(ctx, sandboxID)
	if err != nil {
		return false
	}
	for _, got := range detail.Mounts {
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
		if inspector, ok := m.Provider.(sandboxInspector); ok {
			detail, inspectErr := inspector.InspectSandbox(commitCtx, id)
			if inspectErr == nil {
				for _, got := range detail.Mounts {
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
// is visible here; a grant after BeginRetire is denied. A failed hold query
// aborts a retire this call started and, on a later retry, is checked again
// before destroy. An already-retiring host is not destroyed while that query
// fails or a hold is present.
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
	if busy != nil {
		blocked, busyErr := busy(h)
		if busyErr != nil {
			if began {
				if abortErr := m.Store.AbortRetire(ctx, h); abortErr != nil {
					return errors.Join(busyErr, abortErr)
				}
			}
			return busyErr
		}
		if blocked {
			// Destroy has not been sent only when this call moved the host to
			// retiring. An earlier attempt may already have asked FC to delete
			// the sandbox, so restoring running would hand out access to it.
			if began {
				if abortErr := m.Store.AbortRetire(ctx, h); abortErr != nil {
					return abortErr
				}
			}
			return fmt.Errorf("%w: %s", ErrPending, WaitNativeGrantBusy)
		}
	}
	return m.finishRetire(ctx, h)
}
