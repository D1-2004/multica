package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeASBRuntimeCredentialStore struct {
	credential  db.AsbRuntimeCredential
	credentials []db.AsbRuntimeCredential
	upsert      db.UpsertASBRuntimeCredentialParams
}

func (store *fakeASBRuntimeCredentialStore) GetASBRuntimeCredential(
	context.Context,
	pgtype.UUID,
) (db.AsbRuntimeCredential, error) {
	return store.credential, nil
}

func (store *fakeASBRuntimeCredentialStore) ListASBRuntimeCredentials(
	context.Context,
) ([]db.AsbRuntimeCredential, error) {
	if store.credentials != nil {
		return append([]db.AsbRuntimeCredential(nil), store.credentials...), nil
	}
	if store.credential.RuntimeID.Valid {
		return []db.AsbRuntimeCredential{store.credential}, nil
	}
	return nil, nil
}

func (store *fakeASBRuntimeCredentialStore) UpsertASBRuntimeCredential(
	_ context.Context,
	params db.UpsertASBRuntimeCredentialParams,
) (db.AsbRuntimeCredential, error) {
	store.upsert = params
	store.credential = db.AsbRuntimeCredential{
		RuntimeID:       params.RuntimeID,
		ApiKeyEncrypted: append([]byte(nil), params.ApiKeyEncrypted...),
		ApiKeyHint:      params.ApiKeyHint,
		CreatedAt:       pgtype.Timestamptz{Time: time.Now(), Valid: true},
		UpdatedAt:       pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	return store.credential, nil
}

func TestASBRuntimeCredentialIsEncryptedAndResolvedPerRuntime(t *testing.T) {
	t.Parallel()

	const apiKey = "tenant-api-key-5678"
	var receivedHeader string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/sandboxes/quotas" {
			http.NotFound(response, request)
			return
		}
		receivedHeader = request.Header.Get(asbAPIKeyHeader)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`[]`))
	}))
	defer server.Close()

	box, err := secretbox.New(bytes.Repeat([]byte{0x31}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeASBRuntimeCredentialStore{}
	provider := &ASBRuntimeClientProvider{
		Store:   store,
		Secrets: box,
		Config:  ASBConfig{APIURL: server.URL},
	}
	runtimeID := pgtype.UUID{
		Bytes: [16]byte{0x11, 0x22, 0x33},
		Valid: true,
	}
	credential, err := provider.StoreRuntimeAPIKey(
		context.Background(),
		store,
		runtimeID,
		apiKey,
	)
	if err != nil {
		t.Fatalf("StoreRuntimeAPIKey: %v", err)
	}
	if credential.ApiKeyHint != "5678" {
		t.Fatalf("API key hint = %q, want %q", credential.ApiKeyHint, "5678")
	}
	if bytes.Contains(credential.ApiKeyEncrypted, []byte(apiKey)) {
		t.Fatal("encrypted credential contains the plaintext API key")
	}
	plaintext, err := box.Open(credential.ApiKeyEncrypted)
	if err != nil {
		t.Fatalf("decrypt stored API key: %v", err)
	}
	if string(plaintext) != apiKey {
		t.Fatalf("decrypted API key = %q", plaintext)
	}
	clear(plaintext)

	client, err := provider.ClientForRuntime(context.Background(), runtimeID)
	if err != nil {
		t.Fatalf("ClientForRuntime: %v", err)
	}
	if err := client.ValidateCredential(context.Background()); err != nil {
		t.Fatalf("ValidateCredential: %v", err)
	}
	if receivedHeader != apiKey {
		t.Fatalf("ASB API key header = %q, want configured Runtime key", receivedHeader)
	}
}

func TestASBRuntimeCredentialFindsRuntimesSharingExactAPIKey(t *testing.T) {
	t.Parallel()

	box, err := secretbox.New(bytes.Repeat([]byte{0x41}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	runtimeA := pgtype.UUID{Bytes: [16]byte{0x11}, Valid: true}
	runtimeB := pgtype.UUID{Bytes: [16]byte{0x22}, Valid: true}
	runtimeOther := pgtype.UUID{Bytes: [16]byte{0x33}, Valid: true}
	seal := func(value string) []byte {
		sealed, sealErr := box.Seal([]byte(value))
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		return sealed
	}
	store := &fakeASBRuntimeCredentialStore{
		credential: db.AsbRuntimeCredential{
			RuntimeID:       runtimeA,
			ApiKeyEncrypted: seal("shared-tenant-key"),
			ApiKeyHint:      "-key",
		},
		credentials: []db.AsbRuntimeCredential{
			{
				RuntimeID:       runtimeA,
				ApiKeyEncrypted: seal("shared-tenant-key"),
				ApiKeyHint:      "-key",
			},
			{
				RuntimeID:       runtimeB,
				ApiKeyEncrypted: seal("shared-tenant-key"),
				ApiKeyHint:      "-key",
			},
			{
				RuntimeID:       runtimeOther,
				ApiKeyEncrypted: seal("other-tenant-key"),
				ApiKeyHint:      "-key",
			},
		},
	}
	provider := &ASBRuntimeClientProvider{Store: store, Secrets: box}
	runtimeIDs, err := provider.RuntimeIDsSharingAPIKey(
		context.Background(),
		runtimeA,
	)
	if err != nil {
		t.Fatalf("RuntimeIDsSharingAPIKey: %v", err)
	}
	if len(runtimeIDs) != 2 ||
		runtimeIDs[0] != runtimeA ||
		runtimeIDs[1] != runtimeB {
		t.Fatalf("matching Runtime IDs = %#v", runtimeIDs)
	}
}

func TestASBRuntimeCredentialValidationRejectsUnauthorizedKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = response.Write([]byte(`{"code":"unauthorized","message":"invalid key"}`))
	}))
	defer server.Close()

	provider := &ASBRuntimeClientProvider{
		Config: ASBConfig{APIURL: server.URL},
	}
	err := provider.ValidateAPIKey(context.Background(), "wrong-key")
	var validationErr *ASBAPIKeyValidationError
	if !errors.As(err, &validationErr) || !validationErr.Rejected {
		t.Fatalf("ValidateAPIKey error = %#v, want rejected validation error", err)
	}
}

func TestASBRuntimeCredentialValidationReturnsQuota(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet ||
			request.URL.Path != "/v1/sandboxes/quotas" ||
			request.Header.Get(asbAPIKeyHeader) != "tenant-key" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`[{
			"networkZone":"ALITest",
			"region":"cn-zhangjiakou",
			"quota":5,
			"usage":1,
			"alertPercentage":80,
			"volumeSizeQuotaGib":100,
			"volumeUsageGib":4
		}]`))
	}))
	defer server.Close()

	provider := &ASBRuntimeClientProvider{
		Config: ASBConfig{APIURL: server.URL},
	}
	quotas, err := provider.ValidateAPIKeyAndGetQuotas(
		context.Background(),
		"tenant-key",
	)
	if err != nil {
		t.Fatalf("ValidateAPIKeyAndGetQuotas: %v", err)
	}
	if len(quotas) != 1 {
		t.Fatalf("quotas = %#v", quotas)
	}
	quota := quotas[0]
	if quota.NetworkZone != "ALITest" ||
		quota.Region != "cn-zhangjiakou" ||
		quota.Quota != 5 ||
		quota.Usage != 1 ||
		quota.AlertPercentage == nil ||
		*quota.AlertPercentage != 80 ||
		quota.VolumeSizeQuotaGiB == nil ||
		*quota.VolumeSizeQuotaGiB != 100 ||
		quota.VolumeUsageGiB != 4 {
		t.Fatalf("quota = %#v", quota)
	}
}

func TestValidateASBAPIKeyRejectsUnsafeInput(t *testing.T) {
	t.Parallel()

	for _, apiKey := range []string{"", " \t", "valid\ninjected"} {
		if err := ValidateASBAPIKey(apiKey); err == nil {
			t.Fatalf("ValidateASBAPIKey(%q) unexpectedly succeeded", apiKey)
		}
	}
}
