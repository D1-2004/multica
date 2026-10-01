package dshhost

import (
	"context"
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/startupobs"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ProvisionSpace = iota
	ProvisionAccessPoint
	ProvisionRole
	ProvisionPolicy
	ProvisionPolicyAttachment
	ProvisionVolume
	provisionStepCount
)

// ProvisionSpec is operator-owned placement, frozen before any cloud write.
// It contains no credentials and must never be populated from task input.
type ProvisionSpec struct {
	AccountID       string   `json:"account_id"`
	Region          string   `json:"region"`
	Zone            string   `json:"zone"`
	TeamID          string   `json:"team_id"`
	FileSystemID    string   `json:"file_system_id"`
	VPCID           string   `json:"vpc_id"`
	SecurityGroupID string   `json:"security_group_id"`
	VSwitchIDs      []string `json:"vswitch_ids"`
	SizeLimit       int64    `json:"size_limit"`
	FileCountLimit  int64    `json:"file_count_limit"`
}

func (s ProvisionSpec) valid() bool {
	return s.AccountID != "" && s.Region != "" && s.Zone != "" && s.TeamID != "" &&
		s.FileSystemID != "" && s.VPCID != "" && s.SecurityGroupID != "" &&
		len(s.VSwitchIDs) > 0 && !slices.Contains(s.VSwitchIDs, "") && s.SizeLimit >= 10<<30 && s.FileCountLimit >= 10000 && strings.HasPrefix(s.Zone, s.Region+"-")
}

func (s ProvisionSpec) Validate() error {
	if !s.valid() {
		return errors.New("incomplete DSH cloud placement or invalid storage quota")
	}
	return nil
}

type Provision struct {
	Key
	Spec   ProvisionSpec
	Intent uuid.UUID
	Step   int
	State  string
	// Resources hold identities in ProvisionSpace through ProvisionVolume order.
	// Role creation, policy creation and attachment have separate crash boundaries.
	// Provider lookup must verify ownership, not just names.
	Resources []string
}

func (p Provision) Path() string {
	return "/multica_" + p.WorkspaceID.String() + "_" + p.AgentID.String() + "/"
}

func (p Provision) StepIntent() string {
	return fmt.Sprintf("%s-%d", p.Intent, p.Step)
}

type ProvisionStore interface {
	BeginProvision(context.Context, Key, ProvisionSpec) (Provision, error)
	ClaimProvisionStep(context.Context, Provision) (Provision, error)
	CompleteProvisionStep(context.Context, Provision, string) (Provision, error)
	FinishProvision(context.Context, Provision, Storage) (Host, error)
}

type StorageProvider interface {
	// Resolve dependencies and readiness before claiming the durable write.
	// The returned closure performs only the prepared mutation. A failed
	// prerequisite must not strand an intent that was never sent to the cloud.
	// Each create must attach StepIntent and employee identity atomically to
	// the object, or use the provider's request idempotency key. Never apply
	// ownership labels in a second request after creation.
	PrepareStorageResource(context.Context, Provision) (func(context.Context) (string, error), error)
	// An empty/eventually consistent listing never permits another create.
	// Require exactly one object matching intent, placement and dependencies.
	FindStorageResource(context.Context, Provision) (string, error)
	// Read back the full chain, including AP RAM enforcement, employee root,
	// UID/GID, volume team and role's exclusive AP permissions. No task may
	// mount the result before this succeeds.
	VerifyStorage(context.Context, Provision) (Storage, error)
}

type Provisioner struct {
	Store    ProvisionStore
	Provider StorageProvider
}

// Ensure advances a durable intent. It never retries an ambiguous cloud write.
// The authenticated caller must first authorize this employee in its workspace.
func (m Provisioner) Ensure(ctx context.Context, key Key, spec ProvisionSpec) (host Host, resultErr error) {
	finish := startupobs.Start(ctx, "nas_provisioning")
	defer func() { finish(resultErr) }()
	if key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || !spec.valid() || m.Store == nil || m.Provider == nil {
		return Host{}, errors.New("invalid DSH storage provisioning configuration")
	}
	p, err := m.Store.BeginProvision(ctx, key, spec)
	if err != nil {
		return Host{}, err
	}
	for p.Step < provisionStepCount {
		if err := ctx.Err(); err != nil {
			return Host{}, err
		}
		var id string
		finishStep := startupobs.Start(ctx, fmt.Sprintf("nas_step_%d", p.Step))
		switch p.State {
		case "planned":
			create, prepareErr := m.Provider.PrepareStorageResource(ctx, p)
			if prepareErr != nil {
				finishStep(prepareErr)
				return Host{}, prepareErr
			}
			if create == nil {
				finishStep(errors.New("missing creation operation"))
				return Host{}, errors.New("missing DSH storage creation operation")
			}
			p, err = m.Store.ClaimProvisionStep(ctx, p)
			if err != nil {
				finishStep(err)
				return Host{}, err
			}
			id, err = create(ctx)
		case "creating":
			id, err = m.Provider.FindStorageResource(ctx, p)
		default:
			finishStep(errors.New("invalid provisioning state"))
			return Host{}, errors.New("invalid DSH storage provisioning state")
		}
		finishStep(err)
		if err != nil || id == "" {
			return Host{}, fmt.Errorf("%w: storage creation outcome is unconfirmed", ErrPending)
		}
		commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		p, err = m.Store.CompleteProvisionStep(commitCtx, p, id)
		cancel()
		if err != nil {
			return Host{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Host{}, err
	}
	storage, err := m.Provider.VerifyStorage(ctx, p)
	if err != nil {
		return Host{}, fmt.Errorf("%w: storage ownership or readiness is unconfirmed", ErrPending)
	}
	if err := validateProvisionStorage(p, storage); err != nil {
		return Host{}, err
	}
	return m.Store.FinishProvision(ctx, p, storage)
}

func validateProvisionStorage(p Provision, s Storage) error {
	if p.Step != provisionStepCount || len(p.Resources) != provisionStepCount || (p.State != "planned" && p.State != "complete") ||
		s.FileSystemID != p.Spec.FileSystemID || s.SpaceID != p.Resources[ProvisionSpace] || s.AccessPointARN != p.Resources[ProvisionAccessPoint] ||
		s.RoleARN != p.Resources[ProvisionRole] || s.VolumeName != p.Resources[ProvisionVolume] || s.VPCID != p.Spec.VPCID ||
		s.SecurityGroupID != p.Spec.SecurityGroupID || !slices.Equal(s.VSwitchIDs, p.Spec.VSwitchIDs) {
		return errors.New("DSH verified storage differs from its provisioning intent")
	}
	return nil
}
