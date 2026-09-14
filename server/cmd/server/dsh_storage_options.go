package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/dshhost"
	"gitlab.alibaba-inc.com/koastline/normandy-credential-sdk-golang/credential/provider"
)

// Aone owns the access package binding and credential rotation. The deployment
// selects one account's provisioning identity; task input cannot select it.
func dshStorageProvisioning(db dshhost.Database) (func(context.Context, dshhost.Key) (dshhost.Host, error), error) {
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
	if err := cfg.Placement.Validate(); err != nil {
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
	m := dshhost.Provisioner{Store: dshhost.PostgresStore{DB: db}, Provider: dshhost.CloudStorageProvider{API: api, Spec: cfg.Placement}}
	return func(ctx context.Context, key dshhost.Key) (dshhost.Host, error) {
		return m.Ensure(ctx, key, cfg.Placement)
	}, nil
}
