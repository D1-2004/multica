package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const maxASBAPIKeyLength = 4096

type ASBAPIKeyValidationError struct {
	Rejected bool
	Cause    error
}

func (e *ASBAPIKeyValidationError) Error() string {
	if e != nil && e.Rejected {
		return "ASB API key was rejected"
	}
	return "ASB API key could not be validated"
}

func (e *ASBAPIKeyValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type asbRuntimeCredentialReader interface {
	GetASBRuntimeCredential(context.Context, pgtype.UUID) (db.AsbRuntimeCredential, error)
	ListASBRuntimeCredentials(context.Context) ([]db.AsbRuntimeCredential, error)
}

type asbRuntimeCredentialWriter interface {
	UpsertASBRuntimeCredential(context.Context, db.UpsertASBRuntimeCredentialParams) (db.AsbRuntimeCredential, error)
}

// ASBRuntimeClientProvider resolves the tenant-scoped ASB control-plane
// client for one Runtime. The plaintext key only exists while creating the
// client and is never copied into Runtime metadata or sandbox environments.
type ASBRuntimeClientProvider struct {
	CapacityGate   *ASBCapacityGate
	Store          asbRuntimeCredentialReader
	Secrets        *secretbox.Box
	Config         ASBConfig
	ConfigProvider func() ASBConfig
}

func (p *ASBRuntimeClientProvider) currentConfig() ASBConfig {
	if p != nil && p.ConfigProvider != nil {
		return p.ConfigProvider()
	}
	if p == nil {
		return ASBConfig{}
	}
	return p.Config
}

type ASBTenantCredentialScope struct {
	LockKey    int32
	RuntimeIDs []pgtype.UUID
}

func (scope ASBTenantCredentialScope) Contains(runtimeID pgtype.UUID) bool {
	if !runtimeID.Valid {
		return false
	}
	for _, candidate := range scope.RuntimeIDs {
		if candidate == runtimeID {
			return true
		}
	}
	return false
}

func ValidateASBAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return errors.New("ASB API key is required")
	}
	if len(apiKey) > maxASBAPIKeyLength {
		return errors.New("ASB API key is too long")
	}
	if strings.IndexFunc(apiKey, unicode.IsControl) >= 0 {
		return errors.New("ASB API key contains control characters")
	}
	return nil
}

func NewASBClientForAPIKey(config ASBConfig, apiKey string) (*ASBClient, error) {
	if err := ValidateASBAPIKey(apiKey); err != nil {
		return nil, err
	}
	return NewASBClient(ASBClientConfig{
		BaseURL: config.APIURL,
		APIKey:  strings.TrimSpace(apiKey),
	})
}

func (p *ASBRuntimeClientProvider) ValidateAPIKey(
	ctx context.Context,
	apiKey string,
) error {
	_, err := p.ValidateAPIKeyAndGetQuotas(ctx, apiKey)
	return err
}

func (p *ASBRuntimeClientProvider) ValidateAPIKeyAndGetQuotas(
	ctx context.Context,
	apiKey string,
) ([]ASBSandboxQuota, error) {
	if p == nil {
		return nil, errors.New("ASB Runtime credential service is unavailable")
	}
	client, err := NewASBClientForAPIKey(p.currentConfig(), apiKey)
	if err != nil {
		return nil, err
	}
	quotas, err := client.ListQuotas(ctx)
	if err != nil {
		var httpErr *ASBHTTPError
		rejected := errors.As(err, &httpErr) &&
			(httpErr.StatusCode == 401 || httpErr.StatusCode == 403)
		return nil, &ASBAPIKeyValidationError{Rejected: rejected, Cause: err}
	}
	return quotas, nil
}

func (p *ASBRuntimeClientProvider) ClientForRuntime(
	ctx context.Context,
	runtimeID pgtype.UUID,
) (*ASBClient, error) {
	if p == nil || p.Store == nil || p.Secrets == nil {
		return nil, errors.New("ASB Runtime credential service is unavailable")
	}
	credential, err := p.Store.GetASBRuntimeCredential(ctx, runtimeID)
	if err != nil {
		return nil, fmt.Errorf("load ASB Runtime API key: %w", err)
	}
	return p.clientForCredential(credential)
}

// RuntimeIDsSharingAPIKey returns the Runtime rows whose encrypted credential
// resolves to the same ASB tenant key. ASB quota is key-scoped, so only task
// sandboxes owned by one of these Runtimes may be reclaimed for this request.
func (p *ASBRuntimeClientProvider) RuntimeIDsSharingAPIKey(
	ctx context.Context,
	runtimeID pgtype.UUID,
) ([]pgtype.UUID, error) {
	scope, err := p.RuntimeCredentialScope(ctx, runtimeID)
	if err != nil {
		return nil, err
	}
	return scope.RuntimeIDs, nil
}

// RuntimeCredentialScope resolves the stable tenant lock key and every
// Runtime currently using the exact same ASB API key. Sandboxes created by
// those Runtimes belong to one ASB tenant and may inherit one identity source.
func (p *ASBRuntimeClientProvider) RuntimeCredentialScope(
	ctx context.Context,
	runtimeID pgtype.UUID,
) (ASBTenantCredentialScope, error) {
	if p == nil || p.Store == nil || p.Secrets == nil {
		return ASBTenantCredentialScope{}, errors.New("ASB Runtime credential service is unavailable")
	}
	current, err := p.Store.GetASBRuntimeCredential(ctx, runtimeID)
	if err != nil {
		return ASBTenantCredentialScope{}, fmt.Errorf("load ASB Runtime API key: %w", err)
	}
	currentPlain, err := p.Secrets.Open(current.ApiKeyEncrypted)
	if err != nil {
		return ASBTenantCredentialScope{}, errors.New("decrypt ASB Runtime API key")
	}
	defer clear(currentPlain)
	lockKey := asbAPIKeyCapacityLockKey(string(currentPlain))

	credentials, err := p.Store.ListASBRuntimeCredentials(ctx)
	if err != nil {
		return ASBTenantCredentialScope{}, fmt.Errorf("list ASB Runtime API keys: %w", err)
	}
	runtimeIDs := make([]pgtype.UUID, 0, len(credentials))
	for _, credential := range credentials {
		plain, err := p.Secrets.Open(credential.ApiKeyEncrypted)
		if err != nil {
			return ASBTenantCredentialScope{}, errors.New("decrypt ASB Runtime API key")
		}
		matches := len(plain) == len(currentPlain) &&
			subtle.ConstantTimeCompare(plain, currentPlain) == 1
		clear(plain)
		if matches {
			runtimeIDs = append(runtimeIDs, credential.RuntimeID)
		}
	}
	if len(runtimeIDs) == 0 {
		return ASBTenantCredentialScope{}, errors.New("ASB Runtime credential ownership is inconsistent")
	}
	return ASBTenantCredentialScope{
		LockKey:    lockKey,
		RuntimeIDs: runtimeIDs,
	}, nil
}

func (p *ASBRuntimeClientProvider) clientForCredential(
	credential db.AsbRuntimeCredential,
) (*ASBClient, error) {
	if p == nil || p.Secrets == nil {
		return nil, errors.New("ASB Runtime credential service is unavailable")
	}
	plain, err := p.Secrets.Open(credential.ApiKeyEncrypted)
	if err != nil {
		return nil, errors.New("decrypt ASB Runtime API key")
	}
	defer clear(plain)
	client, err := NewASBClientForAPIKey(p.currentConfig(), string(plain))
	if err != nil {
		return nil, fmt.Errorf("build ASB Runtime client: %w", err)
	}
	client.CapacityGate = p.CapacityGate
	return client, nil
}

func (p *ASBRuntimeClientProvider) StoreRuntimeAPIKey(
	ctx context.Context,
	store asbRuntimeCredentialWriter,
	runtimeID pgtype.UUID,
	apiKey string,
) (db.AsbRuntimeCredential, error) {
	if p == nil || p.Secrets == nil || store == nil {
		return db.AsbRuntimeCredential{}, errors.New("ASB Runtime credential service is unavailable")
	}
	apiKey = strings.TrimSpace(apiKey)
	if err := ValidateASBAPIKey(apiKey); err != nil {
		return db.AsbRuntimeCredential{}, err
	}
	if _, err := NewASBClientForAPIKey(p.currentConfig(), apiKey); err != nil {
		return db.AsbRuntimeCredential{}, err
	}
	sealed, err := p.Secrets.Seal([]byte(apiKey))
	if err != nil {
		return db.AsbRuntimeCredential{}, errors.New("encrypt ASB Runtime API key")
	}
	hint := apiKey
	if len(hint) > 4 {
		hint = hint[len(hint)-4:]
	}
	credential, err := store.UpsertASBRuntimeCredential(
		ctx,
		db.UpsertASBRuntimeCredentialParams{
			RuntimeID:       runtimeID,
			ApiKeyEncrypted: sealed,
			ApiKeyHint:      hint,
		},
	)
	if err != nil {
		return db.AsbRuntimeCredential{}, fmt.Errorf("persist ASB Runtime API key: %w", err)
	}
	return credential, nil
}

func (p *ASBRuntimeClientProvider) DeleteRuntimeSandbox(
	ctx context.Context,
	runtimeID pgtype.UUID,
	sandboxID string,
) error {
	client, err := p.ClientForRuntime(ctx, runtimeID)
	if err != nil {
		return err
	}
	return client.DeleteSandbox(ctx, sandboxID)
}
