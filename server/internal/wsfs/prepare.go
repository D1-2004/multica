package wsfs

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

// Controller provisions the workspace NAS and composite task roles using the
// same ACS placement as employee DSH storage.
type Controller struct {
	NewAPI func(context.Context) (dshhost.CloudCaller, error)
	Spec   dshhost.ProvisionSpec
}

func (c Controller) provider(ctx context.Context, spec dshhost.ProvisionSpec) (CloudStorageProvider, error) {
	if c.NewAPI == nil {
		return CloudStorageProvider{}, errors.New("workspace filesystem cloud API is not configured")
	}
	api, err := c.NewAPI(ctx)
	if err != nil {
		return CloudStorageProvider{}, err
	}
	return CloudStorageProvider{API: api, Spec: spec}, nil
}

func (c Controller) specFor(ctx context.Context, store Store, workspaceID uuid.UUID) (dshhost.ProvisionSpec, error) {
	spec := c.Spec
	existing, err := store.GetProvision(ctx, workspaceID)
	if err == nil {
		spec.SizeLimit = existing.Spec.SizeLimit
		spec.FileCountLimit = existing.Spec.FileCountLimit
		return spec, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return spec, nil
	}
	return spec, err
}

func (c Controller) EnsureBinding(ctx context.Context, db Database, workspaceID uuid.UUID) (Binding, error) {
	store := Store{DB: db}
	if got, err := store.GetBinding(ctx, workspaceID); err == nil {
		return got, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, err
	}
	spec, err := c.specFor(ctx, store, workspaceID)
	if err != nil {
		return Binding{}, err
	}
	provider, err := c.provider(ctx, spec)
	if err != nil {
		return Binding{}, err
	}
	return (Provisioner{Store: store, Provider: provider}).Ensure(ctx, workspaceID, spec)
}

func (c Controller) PrepareMount(ctx context.Context, db Database, workspaceID, agentID uuid.UUID, employee *dshhost.Host) (MountDecision, error) {
	store := Store{DB: db}
	grant, err := store.GetGrant(ctx, workspaceID, agentID)
	if err != nil {
		return MountDecision{}, err
	}
	if grant.effectiveAccess() == AccessNone {
		return SelectVolumeMounts(employee, grant, nil), nil
	}
	binding, err := c.EnsureBinding(ctx, db, workspaceID)
	if err != nil {
		return MountDecision{}, err
	}
	if employee != nil && employee.AccessPointARN != "" && grant.TaskRoleARN == "" {
		spec, specErr := c.specFor(ctx, store, workspaceID)
		if specErr != nil {
			return MountDecision{}, specErr
		}
		provider, provErr := c.provider(ctx, spec)
		if provErr != nil {
			return MountDecision{}, provErr
		}
		grant, err = provider.EnsureGrantRole(ctx, store, *employee, binding, grant)
		if err != nil {
			return MountDecision{}, err
		}
	}
	return SelectVolumeMounts(employee, grant, &binding), nil
}
