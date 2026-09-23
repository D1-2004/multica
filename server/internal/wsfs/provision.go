package wsfs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

const (
	ProvisionSpace = iota
	ProvisionAccessPointRO
	ProvisionAccessPointRW
	ProvisionRoleRO
	ProvisionRoleRW
	ProvisionPolicyRO
	ProvisionPolicyRW
	ProvisionAttachRO
	ProvisionAttachRW
	ProvisionVolumeRO
	ProvisionVolumeRW
	provisionStepCount
)

// WorkspaceProvision is one durable intent for the workspace shared Space.
// AgentID is never stored; the path is /multica_{workspaceId}/.
type WorkspaceProvision struct {
	WorkspaceID uuid.UUID
	Spec        dshhost.ProvisionSpec
	Intent      uuid.UUID
	Step        int
	State       string
	Resources   []string
}

func (p WorkspaceProvision) Path() string {
	return "/multica_" + p.WorkspaceID.String() + "/"
}

func (p WorkspaceProvision) StepIntent() string {
	return fmt.Sprintf("%s-%d", p.Intent, p.Step)
}

var provisionStepNames = [provisionStepCount]string{
	"space", "access_point_ro", "access_point_rw", "role_ro", "role_rw",
	"policy_ro", "policy_rw", "attach_ro", "attach_rw", "volume_ro", "volume_rw",
}

func provisionStepName(step int) string {
	if step < 0 || step >= provisionStepCount {
		return fmt.Sprintf("step_%d", step)
	}
	return provisionStepNames[step]
}

// causeText keeps a pending error diagnosable. Cloud errors carry only the
// action, HTTP status and error code, never a response body.
func causeText(err error) string {
	if err == nil {
		return "no resource identity returned"
	}
	return err.Error()
}

type ProvisionStore interface {
	BeginProvision(context.Context, uuid.UUID, dshhost.ProvisionSpec) (WorkspaceProvision, error)
	ClaimProvisionStep(context.Context, WorkspaceProvision) (WorkspaceProvision, error)
	CompleteProvisionStep(context.Context, WorkspaceProvision, string) (WorkspaceProvision, error)
	FinishProvision(context.Context, WorkspaceProvision, Binding) (Binding, error)
}

type StorageProvider interface {
	PrepareStorageResource(context.Context, WorkspaceProvision) (func(context.Context) (string, error), error)
	FindStorageResource(context.Context, WorkspaceProvision) (string, error)
	VerifyStorage(context.Context, WorkspaceProvision) (Binding, error)
}

type Provisioner struct {
	Store    ProvisionStore
	Provider StorageProvider
}

func (m Provisioner) Ensure(ctx context.Context, workspaceID uuid.UUID, spec dshhost.ProvisionSpec) (Binding, error) {
	if workspaceID == uuid.Nil || spec.Validate() != nil || m.Store == nil || m.Provider == nil {
		return Binding{}, errors.New("invalid workspace filesystem provisioning configuration")
	}
	p, err := m.Store.BeginProvision(ctx, workspaceID, spec)
	if err != nil {
		return Binding{}, err
	}
	for p.Step < provisionStepCount {
		if err := ctx.Err(); err != nil {
			return Binding{}, err
		}
		var id string
		switch p.State {
		case "planned":
			create, prepareErr := m.Provider.PrepareStorageResource(ctx, p)
			if prepareErr != nil {
				return Binding{}, prepareErr
			}
			if create == nil {
				return Binding{}, errors.New("missing workspace filesystem creation operation")
			}
			p, err = m.Store.ClaimProvisionStep(ctx, p)
			if err != nil {
				return Binding{}, err
			}
			id, err = create(ctx)
		case "creating":
			id, err = m.Provider.FindStorageResource(ctx, p)
			if (err != nil || id == "") && namedRAMStep(p.Step) {
				id, err = m.retryNamedCreate(ctx, p, err)
			}
		default:
			return Binding{}, errors.New("invalid workspace filesystem provisioning state")
		}
		if err != nil || id == "" {
			return Binding{}, fmt.Errorf("%w: storage step %s creation outcome is unconfirmed: %s", dshhost.ErrPending, provisionStepName(p.Step), causeText(err))
		}
		commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		p, err = m.Store.CompleteProvisionStep(commitCtx, p, id)
		cancel()
		if err != nil {
			return Binding{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Binding{}, err
	}
	binding, err := m.Provider.VerifyStorage(ctx, p)
	if err != nil {
		return Binding{}, fmt.Errorf("%w: storage ownership or readiness is unconfirmed: %s", dshhost.ErrPending, causeText(err))
	}
	if err := validateWorkspaceStorage(p, binding); err != nil {
		return Binding{}, err
	}
	return m.Store.FinishProvision(ctx, p, binding)
}

// namedRAMStep reports steps whose resources are named and account-unique, so
// repeating the create cannot duplicate them. A rejected create (for example a
// missing RAM permission) is retried instead of waiting on a lookup that can
// never succeed. NAS and FC volume steps keep lookup-only reconciliation.
func namedRAMStep(step int) bool {
	switch step {
	case ProvisionRoleRO, ProvisionRoleRW, ProvisionPolicyRO, ProvisionPolicyRW, ProvisionAttachRO, ProvisionAttachRW:
		return true
	}
	return false
}

func (m Provisioner) retryNamedCreate(ctx context.Context, p WorkspaceProvision, lookupErr error) (string, error) {
	create, err := m.Provider.PrepareStorageResource(ctx, p)
	if err != nil {
		return "", fmt.Errorf("lookup: %s; retry prepare: %w", causeText(lookupErr), err)
	}
	if create == nil {
		return "", errors.New("missing workspace filesystem creation operation")
	}
	id, createErr := create(ctx)
	if createErr == nil && id != "" {
		return id, nil
	}
	// An existing resource answers the create with an error; ownership is
	// proven by the same lookup the planned path relies on.
	id, err = m.Provider.FindStorageResource(ctx, p)
	if err != nil || id == "" {
		return "", fmt.Errorf("retry create: %s; lookup: %s", causeText(createErr), causeText(err))
	}
	return id, nil
}

func validateWorkspaceStorage(p WorkspaceProvision, b Binding) error {
	if p.Step != provisionStepCount || len(p.Resources) != provisionStepCount || (p.State != "planned" && p.State != "complete") ||
		b.WorkspaceID != p.WorkspaceID ||
		b.ROAccessPoint != p.Resources[ProvisionAccessPointRO] || b.RWAccessPoint != p.Resources[ProvisionAccessPointRW] ||
		b.RORoleARN != p.Resources[ProvisionRoleRO] || b.RWRoleARN != p.Resources[ProvisionRoleRW] ||
		b.ROVolumeName != p.Resources[ProvisionVolumeRO] || b.RWVolumeName != p.Resources[ProvisionVolumeRW] {
		return errors.New("workspace filesystem verified storage differs from its provisioning intent")
	}
	if b.ROVolumeName == "" || b.RWVolumeName == "" || b.ROVolumeName == b.RWVolumeName ||
		b.ROAccessPoint == b.RWAccessPoint || b.RORoleARN == b.RWRoleARN {
		return errors.New("workspace filesystem RO and RW lanes must be distinct")
	}
	return nil
}
