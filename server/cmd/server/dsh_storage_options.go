package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/provider"
)

// Aone owns the access package binding and credential rotation. The deployment
// selects one account's provisioning identity; task input cannot select it.
func dshStorageProvisioning(runtime *appRuntimeConfig) (func(context.Context, dshhost.Database, dshhost.Key) (dshhost.Host, error), error) {
	raw := strings.TrimSpace(os.Getenv("MULTICA_DSH_STORAGE_CONFIG"))
	if raw == "" {
		return nil, nil
	}
	var cfg struct {
		Placement          dshhost.ProvisionSpec `json:"placement"`
		CredentialResource string                `json:"credential_resource"`
	}
	if json.Unmarshal([]byte(raw), &cfg) != nil || !strings.HasPrefix(cfg.CredentialResource, "internal:acs:ram:"+cfg.Placement.AccountID+":user/") || !strings.HasSuffix(cfg.CredentialResource, "/accesspack") {
		return nil, errors.New("invalid DSH storage deployment configuration")
	}
	if err := dshStoragePlacement(cfg.Placement, runtime).Validate(); err != nil {
		return nil, err
	}
	managed, err := provider.GetDefaultCredentialProvider()
	if err != nil {
		return nil, errors.New("DSH managed credential provider unavailable")
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
		return nil, err
	}
	return func(ctx context.Context, db dshhost.Database, key dshhost.Key) (dshhost.Host, error) {
		store := dshhost.PostgresStore{DB: db}
		spec := dshStoragePlacement(cfg.Placement, runtime)
		// A durable intent freezes its quota. A live default update must not
		// invalidate an in-flight create or retry it with different parameters.
		existing, err := store.GetProvision(ctx, key)
		if err == nil {
			spec.SizeLimit = existing.Spec.SizeLimit
			spec.FileCountLimit = existing.Spec.FileCountLimit
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return dshhost.Host{}, err
		}
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
