package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/provider"
)

// Aone owns the access package binding and credential rotation. The deployment
// selects one account's provisioning identity; task input cannot select it.
func dshStorageProvisioning(runtime *appRuntimeConfig) (func(context.Context, dshhost.Database, dshhost.Key) (dshhost.Host, error), error) {
	cfg, source, err := readDSHStorageConfig(runtime)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	slog.Info("DSH storage configuration loaded", "source", source, "size_limit", cfg.Placement.SizeLimit, "file_count_limit", cfg.Placement.FileCountLimit)
	managed, err := provider.GetDefaultCredentialProvider()
	if err != nil {
		return nil, errors.New("DSH managed credential provider unavailable")
	}
	return func(ctx context.Context, db dshhost.Database, key dshhost.Key) (dshhost.Host, error) {
		cfg, source, err := readDSHStorageConfig(runtime)
		if err != nil {
			return dshhost.Host{}, err
		}
		if cfg == nil {
			return dshhost.Host{}, errors.New("DSH storage configuration unavailable")
		}
		store := dshhost.PostgresStore{DB: db}
		spec := cfg.Placement
		// A durable intent freezes its quota. A live default update must not
		// invalidate an in-flight create or retry it with different parameters.
		existing, err := store.GetProvision(ctx, key)
		if err == nil {
			spec.SizeLimit = existing.Spec.SizeLimit
			spec.FileCountLimit = existing.Spec.FileCountLimit
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return dshhost.Host{}, err
		}
		api, err := dshhost.NewACSClient(cfg.Placement.Region, func(ctx context.Context) (dshhost.CloudCredentials, error) {
			if err := ctx.Err(); err != nil {
				return dshhost.CloudCredentials{}, err
			}
			cred, err := managed.GetCredential(cfg.CredentialResource)
			if err != nil || cred == nil {
				return dshhost.CloudCredentials{}, errors.New("DSH provisioning access package unavailable")
			}
			return dshhost.CloudCredentials{AccessKeyID: cred.AccessKeyId, AccessKeySecret: cred.AccessKeySecret, SecurityToken: cred.SecurityToken}, nil
		})
		if err != nil {
			return dshhost.Host{}, err
		}
		slog.Info("DSH storage provisioning configuration selected", "source", source, "agent_id", key.AgentID, "size_limit", spec.SizeLimit, "file_count_limit", spec.FileCountLimit)
		storageProvider := dshhost.CloudStorageProvider{API: api, Spec: spec}
		m := dshhost.Provisioner{Store: store, Provider: storageProvider}
		return m.Ensure(ctx, key, spec)
	}, nil
}

func dshStoragePlacement(base dshhost.ProvisionSpec, runtime *appRuntimeConfig) dshhost.ProvisionSpec {
	if runtime != nil && runtime.remote != nil {
		quota := runtime.current().Runtime.AgenticFS.Defaults()
		base.SizeLimit = quota.SizeLimit
		base.FileCountLimit = quota.FileCountLimit
	}
	return base
}

type dshStorageConfig struct {
	Placement          dshhost.ProvisionSpec `json:"placement"`
	CredentialResource string                `json:"credential_resource"`
}

func readDSHStorageConfig(runtime *appRuntimeConfig) (*dshStorageConfig, string, error) {
	var cfg dshStorageConfig
	source := "environment"
	current := runtime.current().Runtime.AgenticFS
	if current.Placement != nil {
		p := current.Placement
		cfg.Placement = dshhost.ProvisionSpec{AccountID: p.AccountID, Region: p.Region, Zone: p.Zone, TeamID: p.TeamID, FileSystemID: p.FileSystemID, VPCID: p.VPCID, SecurityGroupID: p.SecurityGroupID, VSwitchIDs: append([]string(nil), p.VSwitchIDs...), SizeLimit: current.SizeLimit, FileCountLimit: current.FileCountLimit}
		cfg.CredentialResource = current.CredentialResource
		source = "diamond"
	} else {
		// Retain the deployment-mode path during the initial rolling migration.
		// Once placement is in Diamond, the legacy environment value is not read.
		raw := strings.TrimSpace(os.Getenv("MULTICA_DSH_STORAGE_CONFIG"))
		if raw == "" {
			return nil, source, nil
		}
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, source, errors.New("invalid DSH storage deployment configuration")
		}
		cfg.Placement = dshStoragePlacement(cfg.Placement, runtime)
	}
	if !strings.HasPrefix(cfg.CredentialResource, "internal:acs:ram:"+cfg.Placement.AccountID+":user/") || !strings.HasSuffix(cfg.CredentialResource, "/accesspack") {
		return nil, source, errors.New("invalid DSH storage credential resource")
	}
	if err := cfg.Placement.Validate(); err != nil {
		return nil, source, err
	}
	return &cfg, source, nil
}
