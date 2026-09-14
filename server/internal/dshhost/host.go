// Package dshhost coordinates the sole writable sandbox for an employee Home.
// AgenticFS locks are local to a sandbox. Expiry or a failed health check must
// never authorize another writer; replacement requires confirmed destruction.
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
	ErrPending        = errors.New("DSH host transition requires reconciliation")
	ErrChanged        = errors.New("DSH host generation changed")
	ErrRetireRequired = errors.New("DSH host must be drained and retired before replacement")
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
	State        string
	Generation   int64
	CreateIntent uuid.UUID
	SandboxID    string
	TemplateID   string
}

// Store implementations must perform transitions atomically in shared storage.
// A process-local map or expiring lock is not a production implementation.
type Store interface {
	Get(context.Context, Key) (Host, error)
	BeginCreate(context.Context, Key, int64, uuid.UUID, string) (Host, error)
	CompleteCreate(context.Context, Host, string) (Host, error)
	BeginRetire(context.Context, Host) (Host, error)
	CompleteRetire(context.Context, Host) error
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
	if key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || strings.TrimSpace(template) == "" {
		return Host{}, errors.New("DSH host requires an employee and immutable template ID")
	}
	h, err := m.Store.Get(ctx, key)
	if err != nil {
		return Host{}, err
	}
	switch h.State {
	case "running":
		if h.TemplateID != template {
			return Host{}, ErrRetireRequired
		}
		if err := m.Provider.Healthy(ctx, h.SandboxID); err != nil {
			return Host{}, fmt.Errorf("%w: existing sandbox health could not be confirmed", ErrRetireRequired)
		}
		return h, nil
	case "creating", "retiring":
		return Host{}, ErrPending
	case "offline":
	default:
		return Host{}, errors.New("invalid DSH host state")
	}
	// Commit the intent BEFORE the external create. A crash, timeout, or lost
	// database response leaves this intent blocking all subsequent creators.
	h, err = m.Store.BeginCreate(ctx, key, h.Generation, uuid.New(), template)
	if err != nil {
		return Host{}, err
	}
	id, err := m.Provider.Create(ctx, h)
	if err != nil || strings.TrimSpace(id) == "" {
		// Do not expose provider error bodies, which can contain credentials.
		return Host{}, fmt.Errorf("%w: sandbox creation outcome is unconfirmed", ErrPending)
	}
	return m.recordCreated(ctx, h, id)
}

func (m Manager) recordCreated(ctx context.Context, h Host, id string) (Host, error) {
	// Preserve a known create result even if the request disconnected. If the
	// database is unavailable, reconciliation can recover by CreateIntent.
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return m.Store.CompleteCreate(commitCtx, h, id)
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
		return Host{}, ErrPending
	}
	return m.recordCreated(ctx, h, id)
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
	if err := m.Provider.DestroyAndConfirmAbsent(ctx, h.SandboxID); err != nil {
		return fmt.Errorf("%w: old sandbox destruction is unconfirmed", ErrPending)
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return m.Store.CompleteRetire(commitCtx, h)
}
