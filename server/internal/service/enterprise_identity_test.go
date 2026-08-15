package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	idemapi "gitlab.alibaba-inc.com/idem/idem-api-client-golang"
	authoidc "gitlab.alibaba-inc.com/koastline/normandy-auth-sdk-golang/auth/oidc"
)

type fakeEnterpriseIdentityStore struct {
	attempt         db.AgentEnterpriseIdentityAttempt
	agent           db.Agent
	current         db.AgentEnterpriseIdentity
	reusable        db.AgentEnterpriseIdentity
	reusableArgs    db.GetReusableAgentEnterpriseIdentitySourceParams
	references      []db.AgentEnterpriseIdentity
	getCurrentErr   error
	createAttempt   db.CreateAgentEnterpriseIdentityAttemptParams
	upsert          db.UpsertAgentEnterpriseIdentityParams
	sourceCAS       db.CompareAndSwapAgentEnterpriseIdentitySourceParams
	cas             db.CompareAndSwapAgentEnterpriseIdentityTokenParams
	markNeedsReauth int
	maintenance     []db.AgentEnterpriseIdentity
	maintenanceArgs db.ListAgentEnterpriseIdentitiesForMaintenanceParams
	activeSessions  []db.FcE2bSandboxSession
	markedStale     []db.MarkCloudSandboxSessionStaleParams
	touchedSources  int
	invalidSources  int
}

func (f *fakeEnterpriseIdentityStore) CreateAgentEnterpriseIdentityAttempt(
	_ context.Context,
	params db.CreateAgentEnterpriseIdentityAttemptParams,
) (db.AgentEnterpriseIdentityAttempt, error) {
	f.createAttempt = params
	return f.attempt, nil
}

func (f *fakeEnterpriseIdentityStore) ConsumeAgentEnterpriseIdentityAttempt(
	_ context.Context,
	stateHash []byte,
) (db.AgentEnterpriseIdentityAttempt, error) {
	if !bytes.Equal(stateHash, f.attempt.StateHash) {
		return db.AgentEnterpriseIdentityAttempt{}, pgx.ErrNoRows
	}
	return f.attempt, nil
}

func (f *fakeEnterpriseIdentityStore) GetAgent(context.Context, pgtype.UUID) (db.Agent, error) {
	return f.agent, nil
}

func (f *fakeEnterpriseIdentityStore) GetAgentEnterpriseIdentity(
	context.Context,
	db.GetAgentEnterpriseIdentityParams,
) (db.AgentEnterpriseIdentity, error) {
	if f.getCurrentErr != nil {
		return db.AgentEnterpriseIdentity{}, f.getCurrentErr
	}
	return f.current, nil
}

func (f *fakeEnterpriseIdentityStore) GetActiveAgentEnterpriseIdentity(
	context.Context,
	db.GetActiveAgentEnterpriseIdentityParams,
) (db.AgentEnterpriseIdentity, error) {
	if f.getCurrentErr != nil {
		return db.AgentEnterpriseIdentity{}, f.getCurrentErr
	}
	return f.current, nil
}

func (f *fakeEnterpriseIdentityStore) GetReusableAgentEnterpriseIdentitySource(
	_ context.Context,
	params db.GetReusableAgentEnterpriseIdentitySourceParams,
) (db.AgentEnterpriseIdentity, error) {
	f.reusableArgs = params
	if f.reusable.ID.Valid {
		return f.reusable, nil
	}
	if f.current.ID.Valid &&
		f.current.Status == "active" &&
		f.current.WorkspaceID == params.WorkspaceID &&
		f.current.BoundBy == params.BoundBy &&
		f.current.RawEmpID == params.RawEmpID &&
		f.current.BucAgentID == params.BucAgentID &&
		containsUUID(params.RuntimeIds, f.current.BucIdentitySourceRuntimeID) &&
		f.current.BucIdentitySourceSandboxID.Valid {
		return f.current, nil
	}
	return db.AgentEnterpriseIdentity{}, pgx.ErrNoRows
}

func (f *fakeEnterpriseIdentityStore) ListActiveAgentEnterpriseIdentitySourceReferences(
	context.Context,
	db.ListActiveAgentEnterpriseIdentitySourceReferencesParams,
) ([]db.AgentEnterpriseIdentity, error) {
	if f.references != nil {
		return f.references, nil
	}
	if f.current.ID.Valid && f.current.Status == "active" {
		return []db.AgentEnterpriseIdentity{f.current}, nil
	}
	return nil, nil
}

func (f *fakeEnterpriseIdentityStore) CountActiveAgentEnterpriseIdentitySourceReferences(
	context.Context,
	db.CountActiveAgentEnterpriseIdentitySourceReferencesParams,
) (int64, error) {
	if f.references != nil {
		var count int64
		for _, identity := range f.references {
			if identity.Status == "active" {
				count++
			}
		}
		return count, nil
	}
	if f.current.ID.Valid && f.current.Status == "active" {
		return 1, nil
	}
	return 0, nil
}

func (f *fakeEnterpriseIdentityStore) UpsertAgentEnterpriseIdentity(
	_ context.Context,
	params db.UpsertAgentEnterpriseIdentityParams,
) (db.AgentEnterpriseIdentity, error) {
	f.upsert = params
	f.current = db.AgentEnterpriseIdentity{
		ID:                         util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		WorkspaceID:                params.WorkspaceID,
		AgentID:                    params.AgentID,
		RawEmpID:                   params.RawEmpID,
		DisplayName:                params.DisplayName,
		BucAgentID:                 params.BucAgentID,
		AgentSpiffeID:              params.AgentSpiffeID,
		AipID:                      params.AipID,
		BucIdentitySourceSandboxID: params.BucIdentitySourceSandboxID,
		BucIdentitySourceRuntimeID: params.BucIdentitySourceRuntimeID,
		BucIdentitySourceUpdatedAt: pgtype.Timestamptz{
			Time:  time.Now(),
			Valid: true,
		},
		AuthxRefreshTokenEncrypted: params.AuthxRefreshTokenEncrypted,
		AuthxRefreshExpiresAt:      params.AuthxRefreshExpiresAt,
		TokenVersion:               1,
		Status:                     "active",
		BoundBy:                    params.BoundBy,
	}
	return f.current, nil
}

func (f *fakeEnterpriseIdentityStore) CompareAndSwapAgentEnterpriseIdentitySource(
	_ context.Context,
	params db.CompareAndSwapAgentEnterpriseIdentitySourceParams,
) (db.AgentEnterpriseIdentity, error) {
	f.sourceCAS = params
	if !f.current.BucIdentitySourceSandboxID.Valid ||
		f.current.BucIdentitySourceSandboxID.String != params.ExpectedSourceSandboxID.String ||
		f.current.BucIdentitySourceRuntimeID != params.ExpectedSourceRuntimeID {
		return db.AgentEnterpriseIdentity{}, pgx.ErrNoRows
	}
	f.current.BucIdentitySourceSandboxID = params.BucIdentitySourceSandboxID
	f.current.BucIdentitySourceUpdatedAt = pgtype.Timestamptz{
		Time:  time.Now(),
		Valid: true,
	}
	return f.current, nil
}

func (f *fakeEnterpriseIdentityStore) TouchActiveAgentEnterpriseIdentitySourceReferences(
	context.Context,
	db.TouchActiveAgentEnterpriseIdentitySourceReferencesParams,
) (int64, error) {
	f.touchedSources++
	f.current.BucIdentitySourceUpdatedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	return 1, nil
}

func (f *fakeEnterpriseIdentityStore) CompareAndSwapAgentEnterpriseIdentityToken(
	_ context.Context,
	params db.CompareAndSwapAgentEnterpriseIdentityTokenParams,
) (db.AgentEnterpriseIdentity, error) {
	f.cas = params
	if params.ExpectedTokenVersion != f.current.TokenVersion {
		return db.AgentEnterpriseIdentity{}, pgx.ErrNoRows
	}
	f.current.AuthxRefreshTokenEncrypted = params.AuthxRefreshTokenEncrypted
	f.current.AuthxRefreshExpiresAt = params.AuthxRefreshExpiresAt
	f.current.TokenVersion++
	return f.current, nil
}

func (f *fakeEnterpriseIdentityStore) MarkAgentEnterpriseIdentityNeedsReauth(
	context.Context,
	db.MarkAgentEnterpriseIdentityNeedsReauthParams,
) (int64, error) {
	f.markNeedsReauth++
	f.current.Status = "needs_reauth"
	return 1, nil
}

func (f *fakeEnterpriseIdentityStore) MarkAgentEnterpriseIdentitiesNeedsReauthBySource(
	context.Context,
	db.MarkAgentEnterpriseIdentitiesNeedsReauthBySourceParams,
) ([]db.AgentEnterpriseIdentity, error) {
	f.invalidSources++
	identities := f.references
	if identities == nil && f.current.ID.Valid {
		identities = []db.AgentEnterpriseIdentity{f.current}
	}
	for index := range identities {
		identities[index].Status = "needs_reauth"
	}
	f.current.Status = "needs_reauth"
	return identities, nil
}

func (f *fakeEnterpriseIdentityStore) ListAgentEnterpriseIdentitiesForMaintenance(
	_ context.Context,
	params db.ListAgentEnterpriseIdentitiesForMaintenanceParams,
) ([]db.AgentEnterpriseIdentity, error) {
	f.maintenanceArgs = params
	return f.maintenance, nil
}

func (f *fakeEnterpriseIdentityStore) RevokeAgentEnterpriseIdentity(
	context.Context,
	db.RevokeAgentEnterpriseIdentityParams,
) (db.AgentEnterpriseIdentity, error) {
	f.current.Status = "revoked"
	for index := range f.references {
		if f.references[index].ID == f.current.ID {
			f.references[index].Status = "revoked"
		}
	}
	return f.current, nil
}

func (f *fakeEnterpriseIdentityStore) ListActiveCloudSandboxSessionsByAgentIdentity(
	_ context.Context,
	_ db.ListActiveCloudSandboxSessionsByAgentIdentityParams,
) ([]db.FcE2bSandboxSession, error) {
	return f.activeSessions, nil
}

func (f *fakeEnterpriseIdentityStore) MarkCloudSandboxSessionStale(
	_ context.Context,
	params db.MarkCloudSandboxSessionStaleParams,
) error {
	f.markedStale = append(f.markedStale, params)
	return nil
}

type fakeEnterpriseAuthX struct {
	issuedSSOTicket string
	renewedFrom     string
	issueResult     EnterpriseOIDCToken
	renewResult     EnterpriseOIDCToken
}

func (f *fakeEnterpriseAuthX) IssueFromSSOTicket(
	_ context.Context,
	ssoTicket string,
) (EnterpriseOIDCToken, error) {
	f.issuedSSOTicket = ssoTicket
	return f.issueResult, nil
}

func (f *fakeEnterpriseAuthX) Renew(_ context.Context, token string) (EnterpriseOIDCToken, error) {
	f.renewedFrom = token
	return f.renewResult, nil
}

type fakeNormandyOIDCTokenClient struct {
	issueCalls  int
	renewCalls  int
	issueResult *authoidc.OidcToken
	renewResult *authoidc.OidcToken
}

func (f *fakeNormandyOIDCTokenClient) IssueToken(
	*authoidc.OidcTokenSpec,
) (*authoidc.OidcToken, error) {
	f.issueCalls++
	return f.issueResult, nil
}

func (f *fakeNormandyOIDCTokenClient) RenewToken(
	*authoidc.OidcRenewSpec,
) (*authoidc.OidcToken, error) {
	f.renewCalls++
	return f.renewResult, nil
}

type fakeEnterpriseIdem struct {
	registration   EnterpriseAgentRegistration
	issuedOIDC     string
	issuedAgent    string
	issuedOperator string
	ait            string
}

func (f *fakeEnterpriseIdem) EnsureAgent(
	_ context.Context,
	registration EnterpriseAgentRegistration,
) (string, bool, error) {
	f.registration = registration
	return "aip-1", true, nil
}

func (f *fakeEnterpriseIdem) IssueAIT(
	_ context.Context,
	oidc string,
	agent string,
	operator string,
	_ int64,
) (string, error) {
	f.issuedOIDC = oidc
	f.issuedAgent = agent
	f.issuedOperator = operator
	if f.ait == "" {
		return "", errors.New("missing fake AIT")
	}
	return f.ait, nil
}

func (f *fakeEnterpriseIdem) DeleteAgent(context.Context, string, string) error {
	return nil
}

type fakeEnterpriseSandboxes struct {
	deletedRuntimeIDs []pgtype.UUID
	deletedIDs        []string
}

type fakeEnterpriseIdentityTokenRotationLocker struct {
	mu sync.Mutex
}

type fakeEnterpriseIdentitySourceLocker struct {
	mu sync.RWMutex
}

func (f *fakeEnterpriseIdentitySourceLocker) Lock(
	_ context.Context,
	_ enterpriseIdentitySourceKey,
) (func(), error) {
	f.mu.Lock()
	return f.mu.Unlock, nil
}

func (f *fakeEnterpriseIdentitySourceLocker) LockShared(
	_ context.Context,
	_ enterpriseIdentitySourceKey,
) (func(), error) {
	f.mu.RLock()
	return f.mu.RUnlock, nil
}

func (f *fakeEnterpriseIdentityTokenRotationLocker) Lock(
	_ context.Context,
	_ pgtype.UUID,
) (func(), error) {
	f.mu.Lock()
	return f.mu.Unlock, nil
}

type fakeEnterpriseIdentityRuntimeLocker struct {
	mu sync.Mutex
}

func (f *fakeEnterpriseIdentityRuntimeLocker) LockShared(
	ctx context.Context,
	runtimeID pgtype.UUID,
) (func(), error) {
	return f.LockSharedMany(ctx, []pgtype.UUID{runtimeID})
}

func (f *fakeEnterpriseIdentityRuntimeLocker) LockSharedMany(
	_ context.Context,
	_ []pgtype.UUID,
) (func(), error) {
	f.mu.Lock()
	return f.mu.Unlock, nil
}

type fakeEnterpriseIdentityTenantResolver struct {
	lockKey    int32
	runtimeIDs []pgtype.UUID
}

func (f *fakeEnterpriseIdentityTenantResolver) RuntimeCredentialScope(
	_ context.Context,
	runtimeID pgtype.UUID,
) (ASBTenantCredentialScope, error) {
	lockKey := f.lockKey
	if lockKey == 0 {
		lockKey = 42
	}
	runtimeIDs := f.runtimeIDs
	if len(runtimeIDs) == 0 {
		runtimeIDs = []pgtype.UUID{runtimeID}
	}
	return ASBTenantCredentialScope{LockKey: lockKey, RuntimeIDs: runtimeIDs}, nil
}

func containsUUID(values []pgtype.UUID, target pgtype.UUID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type fakeEnterpriseIdentitySource struct {
	availability EnterpriseIdentitySourceAvailability
	key          enterpriseIdentitySourceKey
	createCalls  int
	employeeID   string
	bucAgentID   string
	tokens       BUCIdentityTokens
	err          error
	prepared     []string
	preparedOn   []pgtype.UUID
	parked       []string
	parkedOn     []pgtype.UUID
	deleted      []string
}

func (f *fakeEnterpriseIdentitySource) Create(
	_ context.Context,
	key enterpriseIdentitySourceKey,
	tokens BUCIdentityTokens,
) (EnterpriseIdentitySourceAvailability, error) {
	f.createCalls++
	f.key = key
	f.employeeID = key.RawEmployeeID
	f.bucAgentID = key.BUCAgentID
	f.tokens = tokens
	if f.err != nil {
		return EnterpriseIdentitySourceAvailability{}, f.err
	}
	if strings.TrimSpace(f.availability.SandboxID) == "" {
		f.availability.SandboxID = "identity-source-1"
	}
	if !f.availability.RuntimeID.Valid {
		f.availability.RuntimeID = key.RuntimeID
	}
	return f.availability, nil
}

func (f *fakeEnterpriseIdentitySource) Prepare(
	_ context.Context,
	runtimeID pgtype.UUID,
	sandboxID string,
	_ string,
	_ string,
) error {
	f.prepared = append(f.prepared, sandboxID)
	f.preparedOn = append(f.preparedOn, runtimeID)
	return f.err
}

func (f *fakeEnterpriseIdentitySource) Park(
	_ context.Context,
	runtimeID pgtype.UUID,
	sandboxID string,
) error {
	f.parked = append(f.parked, sandboxID)
	f.parkedOn = append(f.parkedOn, runtimeID)
	return f.err
}

func (f *fakeEnterpriseIdentitySource) Delete(
	_ context.Context,
	_ pgtype.UUID,
	sandboxID string,
) error {
	f.deleted = append(f.deleted, sandboxID)
	return f.err
}

type synchronizedEnterpriseIdentityStore struct {
	*fakeEnterpriseIdentityStore
	mu sync.Mutex
}

func (s *synchronizedEnterpriseIdentityStore) GetAgentEnterpriseIdentity(
	ctx context.Context,
	params db.GetAgentEnterpriseIdentityParams,
) (db.AgentEnterpriseIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakeEnterpriseIdentityStore.GetAgentEnterpriseIdentity(ctx, params)
}

func (s *synchronizedEnterpriseIdentityStore) CompareAndSwapAgentEnterpriseIdentityToken(
	ctx context.Context,
	params db.CompareAndSwapAgentEnterpriseIdentityTokenParams,
) (db.AgentEnterpriseIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakeEnterpriseIdentityStore.CompareAndSwapAgentEnterpriseIdentityToken(ctx, params)
}

type chainingEnterpriseAuthX struct {
	mu    sync.Mutex
	now   time.Time
	calls []string
}

func (c *chainingEnterpriseAuthX) IssueFromSSOTicket(
	context.Context,
	string,
) (EnterpriseOIDCToken, error) {
	return EnterpriseOIDCToken{}, errors.New("unexpected AuthX token issue")
}

func (c *chainingEnterpriseAuthX) Renew(
	_ context.Context,
	refreshToken string,
) (EnterpriseOIDCToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, refreshToken)
	return EnterpriseOIDCToken{
		IDToken:          "id-" + refreshToken,
		RefreshToken:     refreshToken + "-next",
		ExpiresAt:        c.now.Add(time.Hour),
		RefreshExpiresAt: c.now.Add(7 * 24 * time.Hour),
	}, nil
}

func (f *fakeEnterpriseSandboxes) DeleteRuntimeSandbox(
	_ context.Context,
	runtimeID pgtype.UUID,
	sandboxID string,
) error {
	f.deletedRuntimeIDs = append(f.deletedRuntimeIDs, runtimeID)
	f.deletedIDs = append(f.deletedIDs, sandboxID)
	return nil
}

type fakeBUCOAuthClient struct {
	tokens            BUCIdentityTokens
	refreshResult     BUCIdentityTokens
	claims            bucIDTokenClaims
	verifiedToken     string
	ticket            string
	ticketAccessToken string
	ticketErr         error
	verifyErr         error
}

func (f *fakeBUCOAuthClient) ExchangeCode(context.Context, string) (BUCIdentityTokens, error) {
	return f.tokens, nil
}

func (f *fakeBUCOAuthClient) Refresh(context.Context, string) (BUCIdentityTokens, error) {
	return f.refreshResult, nil
}

func (f *fakeBUCOAuthClient) GenerateSSOTicket(
	_ context.Context,
	accessToken string,
) (string, error) {
	f.ticketAccessToken = accessToken
	if f.ticketErr != nil {
		return "", f.ticketErr
	}
	if f.ticket == "" {
		return "buc-sso-ticket", nil
	}
	return f.ticket, nil
}

func (f *fakeBUCOAuthClient) VerifyIDToken(
	_ context.Context,
	token string,
	_ []byte,
	_ time.Time,
) (bucIDTokenClaims, error) {
	f.verifiedToken = token
	return f.claims, f.verifyErr
}

func TestNormandyAuthXClientIssuesFromSSOTicketAndRotatesRefresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 30, 4, 0, 0, 0, time.UTC)
	sdk := &fakeNormandyOIDCTokenClient{
		issueResult: &authoidc.OidcToken{
			IdToken:               "authx-id-1",
			RefreshToken:          "authx-refresh-1",
			ExpiresAt:             now.Add(time.Hour).Unix(),
			RefreshTokenExpiresAt: now.Add(7 * 24 * time.Hour).Unix(),
		},
		renewResult: &authoidc.OidcToken{
			IdToken:               "authx-id-2",
			RefreshToken:          "authx-refresh-2",
			ExpiresAt:             now.Add(2 * time.Hour).Unix(),
			RefreshTokenExpiresAt: now.Add(8 * 24 * time.Hour).Unix(),
		},
	}
	client := &NormandyAuthXClient{
		client:   sdk,
		audience: "https://authx.alibaba-inc.com",
		ttl:      3600,
	}
	issued, err := client.IssueFromSSOTicket(context.Background(), "buc-sso-ticket")
	if err != nil {
		t.Fatalf("IssueFromSSOTicket: %v", err)
	}
	if sdk.issueCalls != 1 ||
		issued.IDToken != "authx-id-1" ||
		issued.RefreshToken != "authx-refresh-1" ||
		!issued.RefreshExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("issued token = %#v calls=%d", issued, sdk.issueCalls)
	}
	renewed, err := client.Renew(context.Background(), issued.RefreshToken)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if sdk.renewCalls != 1 ||
		renewed.IDToken != "authx-id-2" ||
		renewed.RefreshToken != "authx-refresh-2" ||
		!renewed.RefreshExpiresAt.Equal(now.Add(8*24*time.Hour)) {
		t.Fatalf("renewed token = %#v calls=%d", renewed, sdk.renewCalls)
	}
	if _, err := client.IssueFromSSOTicket(context.Background(), ""); err == nil {
		t.Fatal("IssueFromSSOTicket accepted an empty SSO ticket")
	}
	if sdk.issueCalls != 1 {
		t.Fatalf("Normandy issue calls after invalid input = %d", sdk.issueCalls)
	}
}

func TestEnterpriseIdentityStartBindingStoresOnlyHashedStateAndNonce(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	store := &fakeEnterpriseIdentityStore{}
	serviceUnderTest := newTestEnterpriseIdentityService(t, store, &fakeBUCOAuthClient{}, &fakeEnterpriseAuthX{}, &fakeEnterpriseIdem{}, &fakeEnterpriseSandboxes{}, now)
	result, err := serviceUnderTest.StartBinding(context.Background(), StartEnterpriseIdentityBindingInput{
		WorkspaceID:  util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentID:      util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		ActorUserID:  util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		RedirectPath: "/agents/222/settings",
	})
	if err != nil {
		t.Fatalf("StartBinding: %v", err)
	}
	authorizeURL, err := url.Parse(result.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	state := authorizeURL.Query().Get("state")
	nonce := authorizeURL.Query().Get("nonce")
	if state == "" || nonce == "" || state == nonce {
		t.Fatalf("OAuth state/nonce = %q/%q", state, nonce)
	}
	if bytes.Equal(store.createAttempt.StateHash, []byte(state)) ||
		bytes.Equal(store.createAttempt.NonceHash, []byte(nonce)) {
		t.Fatal("plaintext OAuth state or nonce was persisted")
	}
	if !bytes.Equal(store.createAttempt.StateHash, sha256Bytes(state)) ||
		!bytes.Equal(store.createAttempt.NonceHash, sha256Bytes(nonce)) {
		t.Fatal("OAuth state or nonce hash mismatch")
	}
	if store.createAttempt.RequestedRawEmpID.Valid {
		t.Fatal("OAuth attempt must not persist an unverified employee ID")
	}
	if authorizeURL.Query().Get("agent_id") != "buc-agent-1" ||
		authorizeURL.Query().Get("authorize_app") != "authorized-app-1,authorized-app-2" ||
		authorizeURL.Query().Get("scope") != "profile openid employee user_authorize" {
		t.Fatalf("authorize query = %v", authorizeURL.Query())
	}
}

func TestEnterpriseIdentityStartBindingWithoutPreauthorizedApplications(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	store := &fakeEnterpriseIdentityStore{}
	serviceUnderTest := newTestEnterpriseIdentityService(t, store, &fakeBUCOAuthClient{}, &fakeEnterpriseAuthX{}, &fakeEnterpriseIdem{}, &fakeEnterpriseSandboxes{}, now)
	serviceUnderTest.Config.BUCAuthorizeApps = nil

	if err := serviceUnderTest.Config.Validate(ASBConfig{}); err != nil {
		t.Fatalf("Validate without preauthorized applications: %v", err)
	}

	result, err := serviceUnderTest.StartBinding(context.Background(), StartEnterpriseIdentityBindingInput{
		WorkspaceID:  util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentID:      util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		ActorUserID:  util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		RedirectPath: "/agents/222/settings",
	})
	if err != nil {
		t.Fatalf("StartBinding: %v", err)
	}
	authorizeURL, err := url.Parse(result.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	if authorizeURL.Query().Get("scope") != "profile openid employee" {
		t.Fatalf("scope = %q", authorizeURL.Query().Get("scope"))
	}
	if _, ok := authorizeURL.Query()["authorize_app"]; ok {
		t.Fatalf("authorize_app must be omitted: %v", authorizeURL.Query())
	}
}

func TestEnterpriseIdentityCompleteBindingRequiresRevokeBeforeChangingEmployee(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	workspaceID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	agentID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	state := "oauth-state"
	store := &fakeEnterpriseIdentityStore{
		attempt: db.AgentEnterpriseIdentityAttempt{
			WorkspaceID:  workspaceID,
			AgentID:      agentID,
			ActorUserID:  util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
			StateHash:    sha256Bytes(state),
			NonceHash:    sha256Bytes("oauth-nonce"),
			RedirectPath: "/agents/222/settings",
		},
		agent: db.Agent{
			ID:          agentID,
			WorkspaceID: workspaceID,
		},
		current: db.AgentEnterpriseIdentity{
			ID:          util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
			WorkspaceID: workspaceID,
			AgentID:     agentID,
			RawEmpID:    "12345",
			Status:      "active",
		},
	}
	buc := &fakeBUCOAuthClient{
		tokens: BUCIdentityTokens{IDToken: "signed-buc-id-token"},
		claims: bucIDTokenClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "openid-2"},
			EmployeeID:       "67890",
			Nonce:            "oauth-nonce",
			Name:             "另一位员工",
		},
	}
	authX := &fakeEnterpriseAuthX{}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		buc,
		authX,
		&fakeEnterpriseIdem{},
		&fakeEnterpriseSandboxes{},
		now,
	)

	_, err := serviceUnderTest.CompleteBinding(context.Background(), state, "oauth-code")
	if !errors.Is(err, ErrEnterpriseIdentityEmployeeConflict) {
		t.Fatalf("CompleteBinding error = %v", err)
	}
	if authX.issuedSSOTicket != "" {
		t.Fatal("AuthX issuance ran before employee conflict validation")
	}
}

func TestValidateExistingIdemAgentRejectsDifferentOwner(t *testing.T) {
	t.Parallel()

	registration := EnterpriseAgentRegistration{
		SPIFFEID:   "spiffe://agents.example/ns/multica/agents/222",
		EmployeeID: "12345",
	}
	profile := idemapi.AgentIdentityProfile{
		Spec: idemapi.AgentIdentityProfileSpec{
			AgentId:      registration.SPIFFEID,
			AgentType:    idemEnterpriseAgentType,
			AipAgentType: "assistant",
			Framework: &idemapi.AgentIdentityProfileFramework{
				Name:    idemEnterpriseFrameworkName,
				Version: idemEnterpriseFrameworkVersion,
			},
			OwnerBinding: &idemapi.OwnerBinding{
				OwnerType:    "user",
				OwnerEmpId:   "67890",
				BindingModel: "server_mediated",
			},
		},
	}
	if err := validateExistingIdemAgent(profile, registration); err == nil {
		t.Fatal("expected a mismatched Idem owner binding to fail")
	}
	profile.Spec.OwnerBinding.OwnerEmpId = registration.EmployeeID
	if err := validateExistingIdemAgent(profile, registration); err != nil {
		t.Fatalf("matching Idem Agent Identity: %v", err)
	}
}

func TestEnterpriseIdentityCompleteBindingPersistsRollingSourceWithoutBUCTokens(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	workspaceID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	agentID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	actorID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	runtimeID := util.MustParseUUID("55555555-5555-5555-5555-555555555555")
	state := "oauth-state"
	nonce := "oauth-nonce"
	bucIDToken := "signed-buc-id-token"
	store := &fakeEnterpriseIdentityStore{
		attempt: db.AgentEnterpriseIdentityAttempt{
			WorkspaceID:  workspaceID,
			AgentID:      agentID,
			ActorUserID:  actorID,
			StateHash:    sha256Bytes(state),
			NonceHash:    sha256Bytes(nonce),
			RedirectPath: "/agents/222/settings",
		},
		agent: db.Agent{
			ID:          agentID,
			WorkspaceID: workspaceID,
			Name:        "A1 Explorer",
			Model:       pgtype.Text{String: "qwen3", Valid: true},
			RuntimeID:   runtimeID,
		},
		getCurrentErr: pgx.ErrNoRows,
	}
	buc := &fakeBUCOAuthClient{
		tokens: BUCIdentityTokens{
			AccessToken:  "buc-access",
			RefreshToken: "buc-refresh",
			IDToken:      bucIDToken,
			ExpiresIn:    3600,
		},
		claims: bucIDTokenClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "openid-1"},
			EmployeeID:       "12345",
			Nonce:            nonce,
			Name:             "测试员工",
		},
	}
	authX := &fakeEnterpriseAuthX{issueResult: EnterpriseOIDCToken{
		IDToken:          "authx-id",
		RefreshToken:     "authx-refresh",
		ExpiresAt:        now.Add(time.Hour),
		RefreshExpiresAt: now.Add(24 * time.Hour),
	}}
	idem := &fakeEnterpriseIdem{ait: "ait-1"}
	sandboxes := &fakeEnterpriseSandboxes{}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		buc,
		authX,
		idem,
		sandboxes,
		now,
	)
	source := serviceUnderTest.Source.(*fakeEnterpriseIdentitySource)

	result, err := serviceUnderTest.CompleteBinding(context.Background(), state, "oauth-code")
	if err != nil {
		t.Fatalf("CompleteBinding: %v", err)
	}
	if result.Identity.Status != "active" || result.RedirectPath != "/agents/222/settings" {
		t.Fatalf("result = %#v", result)
	}
	if buc.verifiedToken != bucIDToken {
		t.Fatal("BUC ID token was not verified before binding")
	}
	if store.upsert.RawEmpID != "12345" ||
		idem.registration.EmployeeID != "12345" {
		t.Fatalf("BUC employee identity was not used for binding")
	}
	if buc.ticketAccessToken != "buc-access" {
		t.Fatalf("BUC SSO ticket access token = %q", buc.ticketAccessToken)
	}
	if authX.issuedSSOTicket != "buc-sso-ticket" {
		t.Fatalf("AuthX SSO ticket = %q", authX.issuedSSOTicket)
	}
	if bytes.Contains(store.upsert.AuthxRefreshTokenEncrypted, []byte("authx-refresh")) ||
		bytes.Contains(store.upsert.AuthxRefreshTokenEncrypted, []byte("buc-refresh")) {
		t.Fatal("plaintext refresh token was persisted")
	}
	opened, err := serviceUnderTest.Secrets.Open(store.upsert.AuthxRefreshTokenEncrypted)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != "authx-refresh" {
		t.Fatalf("stored refresh token = %q", opened)
	}
	if source.tokens.AccessToken != "buc-access" ||
		source.tokens.RefreshToken != "buc-refresh" ||
		source.tokens.IDToken != bucIDToken ||
		source.key.RuntimeID != runtimeID ||
		source.key.BoundBy != store.attempt.ActorUserID ||
		source.employeeID != "12345" {
		t.Fatalf("temporary ASB identity source input = %#v key=%#v", source.tokens, source.key)
	}
	if !store.upsert.BucIdentitySourceSandboxID.Valid ||
		store.upsert.BucIdentitySourceSandboxID.String != "identity-source-1" ||
		store.current.BucIdentitySourceRuntimeID != runtimeID {
		t.Fatalf("persisted paused identity source = %#v", store.current)
	}
	if store.upsert.AgentSpiffeID == "" ||
		store.upsert.AipID != "aip-1" {
		t.Fatalf("persisted identity = %#v", store.upsert)
	}
	if len(sandboxes.deletedIDs) != 0 {
		t.Fatalf("binding allocated or deleted sandbox state: %#v", sandboxes.deletedIDs)
	}
}

func TestEnterpriseIdentityReusesSharedSourceAcrossAgentsAndRuntimes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 3, 4, 0, 0, 0, time.UTC)
	workspaceID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	boundBy := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	sourceRuntimeID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	targetRuntimeID := util.MustParseUUID("88888888-8888-8888-8888-888888888888")
	reusable := db.AgentEnterpriseIdentity{
		ID:                         util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		WorkspaceID:                workspaceID,
		AgentID:                    util.MustParseUUID("55555555-5555-5555-5555-555555555555"),
		RawEmpID:                   "12345",
		BucAgentID:                 "buc-agent-1",
		BucIdentitySourceSandboxID: pgtype.Text{String: "shared-source-1", Valid: true},
		BucIdentitySourceRuntimeID: sourceRuntimeID,
		BucIdentitySourceUpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		Status:                     "active",
		BoundBy:                    boundBy,
	}
	store := &fakeEnterpriseIdentityStore{reusable: reusable}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		&fakeBUCOAuthClient{},
		&fakeEnterpriseAuthX{},
		&fakeEnterpriseIdem{},
		&fakeEnterpriseSandboxes{},
		now,
	)
	source := serviceUnderTest.Source.(*fakeEnterpriseIdentitySource)
	availability, created, err := serviceUnderTest.reuseOrCreateIdentitySource(
		context.Background(),
		enterpriseIdentitySourceKey{
			WorkspaceID:   workspaceID,
			BoundBy:       boundBy,
			RuntimeID:     targetRuntimeID,
			TenantLockKey: 42,
			RawEmployeeID: "12345",
			BUCAgentID:    "buc-agent-1",
		},
		[]pgtype.UUID{sourceRuntimeID, targetRuntimeID},
		BUCIdentityTokens{},
	)
	if err != nil {
		t.Fatalf("reuseOrCreateIdentitySource: %v", err)
	}
	if created || availability.SandboxID != "shared-source-1" || availability.RuntimeID != sourceRuntimeID {
		t.Fatalf("shared source availability = %#v, created=%t", availability, created)
	}
	if source.createCalls != 0 || strings.Join(source.prepared, ",") != "shared-source-1" ||
		strings.Join(source.parked, ",") != "shared-source-1" || store.touchedSources != 1 {
		t.Fatalf(
			"shared source actions: create=%d prepare=%v park=%v touch=%d",
			source.createCalls,
			source.prepared,
			source.parked,
			store.touchedSources,
		)
	}
	if len(source.preparedOn) != 1 || source.preparedOn[0] != sourceRuntimeID ||
		len(source.parkedOn) != 1 || source.parkedOn[0] != sourceRuntimeID ||
		!containsUUID(store.reusableArgs.RuntimeIds, sourceRuntimeID) ||
		!containsUUID(store.reusableArgs.RuntimeIds, targetRuntimeID) {
		t.Fatalf(
			"cross-Runtime source actions: prepare=%v park=%v query=%v",
			source.preparedOn,
			source.parkedOn,
			store.reusableArgs.RuntimeIds,
		)
	}
}

func TestEnterpriseIdentityReauthorizationCreatesFreshSource(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 15, 1, 0, 0, 0, time.UTC)
	workspaceID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	boundBy := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	runtimeID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	store := &fakeEnterpriseIdentityStore{reusable: db.AgentEnterpriseIdentity{
		ID:                         util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		WorkspaceID:                workspaceID,
		RawEmpID:                   "12345",
		BucAgentID:                 "buc-agent-1",
		BucIdentitySourceSandboxID: pgtype.Text{String: "expired-source-1", Valid: true},
		BucIdentitySourceRuntimeID: runtimeID,
		Status:                     "active",
		BoundBy:                    boundBy,
	}}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		&fakeBUCOAuthClient{},
		&fakeEnterpriseAuthX{},
		&fakeEnterpriseIdem{},
		&fakeEnterpriseSandboxes{},
		now,
	)
	source := serviceUnderTest.Source.(*fakeEnterpriseIdentitySource)
	tokens := BUCIdentityTokens{
		AccessToken:  "fresh-access",
		RefreshToken: "fresh-refresh",
		IDToken:      "fresh-id",
	}
	availability, created, err := serviceUnderTest.provisionIdentitySourceForBinding(
		context.Background(),
		enterpriseIdentitySourceKey{
			WorkspaceID:   workspaceID,
			BoundBy:       boundBy,
			RuntimeID:     runtimeID,
			TenantLockKey: 42,
			RawEmployeeID: "12345",
			BUCAgentID:    "buc-agent-1",
		},
		[]pgtype.UUID{runtimeID},
		tokens,
		true,
	)
	if err != nil {
		t.Fatalf("provisionIdentitySourceForBinding: %v", err)
	}
	if !created || availability.SandboxID != "identity-source-1" ||
		availability.RuntimeID != runtimeID {
		t.Fatalf("fresh source availability = %#v, created=%t", availability, created)
	}
	if source.createCalls != 1 || source.tokens != tokens || source.key.RuntimeID != runtimeID {
		t.Fatalf("fresh source creation = calls:%d tokens:%#v key:%#v", source.createCalls, source.tokens, source.key)
	}
	if len(source.prepared) != 0 || len(source.parked) != 0 {
		t.Fatalf(
			"reauthorization reused stale source: prepare=%v park=%v",
			source.prepared,
			source.parked,
		)
	}
}

func TestEnterpriseIdentitySourceLockUsesSharedTenantAcrossRuntimes(t *testing.T) {
	t.Parallel()

	workspaceID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	boundBy := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	runtimeA := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	runtimeB := util.MustParseUUID("44444444-4444-4444-4444-444444444444")
	keyA := enterpriseIdentitySourceKey{
		WorkspaceID:   workspaceID,
		BoundBy:       boundBy,
		RuntimeID:     runtimeA,
		TenantLockKey: 42,
		RawEmployeeID: "12345",
		BUCAgentID:    "buc-agent-1",
	}
	keyB := keyA
	keyB.RuntimeID = runtimeB
	if enterpriseIdentitySourceLockKey(keyA) != enterpriseIdentitySourceLockKey(keyB) ||
		enterpriseIdentitySourceFingerprint(keyA) != enterpriseIdentitySourceFingerprint(keyB) {
		t.Fatal("same ASB tenant and identity did not resolve to one shared source key")
	}
	keyB.TenantLockKey = 43
	if enterpriseIdentitySourceLockKey(keyA) == enterpriseIdentitySourceLockKey(keyB) ||
		enterpriseIdentitySourceFingerprint(keyA) == enterpriseIdentitySourceFingerprint(keyB) {
		t.Fatal("different ASB tenants resolved to one shared source key")
	}
}

func TestEnterpriseIdentityResolveRotatesRefreshAndIssuesTaskAIT(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	sourceRuntimeID := util.MustParseUUID("55555555-5555-5555-5555-555555555555")
	taskRuntimeID := util.MustParseUUID("66666666-6666-6666-6666-666666666666")
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealedRefresh, err := box.Seal([]byte("refresh-old"))
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeEnterpriseIdentityStore{
		current: db.AgentEnterpriseIdentity{
			ID:                         util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
			WorkspaceID:                util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
			AgentID:                    util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
			RawEmpID:                   "12345",
			BucAgentID:                 "buc-agent-1",
			AgentSpiffeID:              "spiffe://agents.example/ns/multica/agents/222",
			AipID:                      "aip-1",
			BucIdentitySourceSandboxID: pgtype.Text{String: "identity-source-1", Valid: true},
			BucIdentitySourceRuntimeID: sourceRuntimeID,
			AuthxRefreshTokenEncrypted: sealedRefresh,
			AuthxRefreshExpiresAt:      pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true},
			TokenVersion:               2,
			Status:                     "active",
		},
	}
	authX := &fakeEnterpriseAuthX{renewResult: EnterpriseOIDCToken{
		IDToken:          "authx-id-new",
		RefreshToken:     "refresh-new",
		ExpiresAt:        now.Add(time.Hour),
		RefreshExpiresAt: now.Add(24 * time.Hour),
	}}
	idem := &fakeEnterpriseIdem{ait: "ait-task"}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		&fakeBUCOAuthClient{},
		authX,
		idem,
		&fakeEnterpriseSandboxes{},
		now,
	)
	serviceUnderTest.Secrets = box
	serviceUnderTest.Tenants = &fakeEnterpriseIdentityTenantResolver{
		runtimeIDs: []pgtype.UUID{sourceRuntimeID, taskRuntimeID},
	}

	resolved, err := serviceUnderTest.ResolveASBTaskIdentity(
		context.Background(),
		store.current.WorkspaceID,
		store.current.AgentID,
		taskRuntimeID,
	)
	if err != nil {
		t.Fatalf("ResolveASBTaskIdentity: %v", err)
	}
	if authX.renewedFrom != "refresh-old" ||
		idem.issuedOIDC != "authx-id-new" ||
		resolved.AgentIdentityToken != "ait-task" ||
		resolved.SourceSandboxID != "identity-source-1" ||
		resolved.SourceRuntimeID != sourceRuntimeID ||
		len(resolved.Fingerprint) != 64 {
		t.Fatalf("resolved = %#v", resolved)
	}
	rotated, err := box.Open(store.cas.AuthxRefreshTokenEncrypted)
	if err != nil {
		t.Fatal(err)
	}
	if string(rotated) != "refresh-new" {
		t.Fatalf("rotation = %q", rotated)
	}
}

func TestEnterpriseIdentityTokenRotationReloadsAfterSharedLock(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	box, err := secretbox.New(bytes.Repeat([]byte{0x52}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte("refresh-0"))
	if err != nil {
		t.Fatal(err)
	}
	store := &synchronizedEnterpriseIdentityStore{
		fakeEnterpriseIdentityStore: &fakeEnterpriseIdentityStore{
			current: db.AgentEnterpriseIdentity{
				ID:                         util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
				WorkspaceID:                util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
				AgentID:                    util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
				AuthxRefreshTokenEncrypted: sealed,
				AuthxRefreshExpiresAt:      pgtype.Timestamptz{Time: now.Add(7 * 24 * time.Hour), Valid: true},
				TokenVersion:               1,
				Status:                     "active",
			},
		},
	}
	authX := &chainingEnterpriseAuthX{now: now}
	locker := &fakeEnterpriseIdentityTokenRotationLocker{}
	newReplica := func() *EnterpriseIdentityService {
		serviceUnderTest := newTestEnterpriseIdentityService(
			t,
			store,
			&fakeBUCOAuthClient{},
			authX,
			&fakeEnterpriseIdem{},
			&fakeEnterpriseSandboxes{},
			now,
		)
		serviceUnderTest.Secrets = box
		serviceUnderTest.TokenLock = locker
		return serviceUnderTest
	}
	replicas := []*EnterpriseIdentityService{newReplica(), newReplica()}
	initial := store.current

	start := make(chan struct{})
	errs := make(chan error, len(replicas))
	var wait sync.WaitGroup
	for _, replica := range replicas {
		wait.Add(1)
		go func(replica *EnterpriseIdentityService) {
			defer wait.Done()
			<-start
			_, _, err := replica.rotateAuthXToken(context.Background(), initial)
			errs <- err
		}(replica)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(authX.calls, ","); got != "refresh-0,refresh-0-next" {
		t.Fatalf("refresh token rotation chain = %q", got)
	}
	finalToken, err := box.Open(store.current.AuthxRefreshTokenEncrypted)
	if err != nil {
		t.Fatal(err)
	}
	if string(finalToken) != "refresh-0-next-next" || store.current.TokenVersion != 3 {
		t.Fatalf("final token/version = %q/%d", finalToken, store.current.TokenVersion)
	}
}

func TestEnterpriseIdentityLeasesSharedSourceAcrossRuntimesWithoutChangingIdentity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 30, 4, 0, 0, 0, time.UTC)
	sourceRuntimeID := util.MustParseUUID("55555555-5555-5555-5555-555555555555")
	taskRuntimeID := util.MustParseUUID("66666666-6666-6666-6666-666666666666")
	identity := db.AgentEnterpriseIdentity{
		ID:                         util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		WorkspaceID:                util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentID:                    util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		RawEmpID:                   "12345",
		BucAgentID:                 "buc-agent-1",
		AgentSpiffeID:              "spiffe://agents.example/ns/multica/agents/222",
		AipID:                      "aip-1",
		BucIdentitySourceSandboxID: pgtype.Text{String: "source-1", Valid: true},
		BucIdentitySourceRuntimeID: sourceRuntimeID,
		TokenVersion:               2,
		Status:                     "active",
		BoundBy:                    util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
	}
	store := &fakeEnterpriseIdentityStore{current: identity}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		&fakeBUCOAuthClient{},
		&fakeEnterpriseAuthX{},
		&fakeEnterpriseIdem{},
		&fakeEnterpriseSandboxes{},
		now,
	)
	serviceUnderTest.Tenants = &fakeEnterpriseIdentityTenantResolver{
		runtimeIDs: []pgtype.UUID{sourceRuntimeID, taskRuntimeID},
	}
	source := serviceUnderTest.Source.(*fakeEnterpriseIdentitySource)

	release, err := serviceUnderTest.AcquireASBTaskIdentitySource(
		context.Background(),
		identity.WorkspaceID,
		identity.AgentID,
		taskRuntimeID,
		"source-1",
	)
	if err != nil {
		t.Fatalf("AcquireASBTaskIdentitySource: %v", err)
	}
	if got := strings.Join(source.prepared, ","); got != "source-1" {
		t.Fatalf("prepared identity source = %q", got)
	}
	if len(source.preparedOn) != 1 || source.preparedOn[0] != sourceRuntimeID {
		t.Fatalf("prepared source Runtime = %v", source.preparedOn)
	}
	if err := release(context.Background()); err != nil {
		t.Fatalf("release ASB identity source: %v", err)
	}
	if got := strings.Join(source.parked, ","); got != "source-1" {
		t.Fatalf("parked identity source = %q", got)
	}
	if len(source.parkedOn) != 1 || source.parkedOn[0] != sourceRuntimeID {
		t.Fatalf("parked source Runtime = %v", source.parkedOn)
	}
	if store.current.BucIdentitySourceSandboxID.String != "source-1" ||
		store.current.TokenVersion != identity.TokenVersion {
		t.Fatalf("identity source coordinate changed during lease: %#v", store.current)
	}
}

func TestEnterpriseIdentityMaintenanceRefreshesPlatformCredentials(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealedRefresh, err := box.Seal([]byte("authx-refresh-old"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := util.MustParseUUID("55555555-5555-5555-5555-555555555555")
	identity := db.AgentEnterpriseIdentity{
		ID:                         util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		WorkspaceID:                util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentID:                    util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		RawEmpID:                   "12345",
		BucAgentID:                 "buc-agent-1",
		BucIdentitySourceSandboxID: pgtype.Text{String: "source-1", Valid: true},
		BucIdentitySourceRuntimeID: runtimeID,
		BucIdentitySourceUpdatedAt: pgtype.Timestamptz{
			Time:  now.Add(-13 * time.Hour),
			Valid: true,
		},
		AuthxRefreshTokenEncrypted: sealedRefresh,
		AuthxRefreshExpiresAt:      pgtype.Timestamptz{Time: now.Add(10 * time.Minute), Valid: true},
		TokenVersion:               2,
		Status:                     "active",
		BoundBy:                    util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
	}
	store := &fakeEnterpriseIdentityStore{
		current:     identity,
		maintenance: []db.AgentEnterpriseIdentity{identity},
	}
	authX := &fakeEnterpriseAuthX{renewResult: EnterpriseOIDCToken{
		IDToken:          "authx-id-new",
		RefreshToken:     "authx-refresh-new",
		ExpiresAt:        now.Add(time.Hour),
		RefreshExpiresAt: now.Add(24 * time.Hour),
	}}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		&fakeBUCOAuthClient{},
		authX,
		&fakeEnterpriseIdem{},
		&fakeEnterpriseSandboxes{},
		now,
	)
	serviceUnderTest.Secrets = box
	source := serviceUnderTest.Source.(*fakeEnterpriseIdentitySource)

	if err := serviceUnderTest.maintainActiveIdentities(context.Background()); err != nil {
		t.Fatalf("maintainActiveIdentities: %v", err)
	}
	if authX.renewedFrom != "authx-refresh-old" {
		t.Fatalf("renewed_from=%q", authX.renewedFrom)
	}
	rotated, err := box.Open(store.cas.AuthxRefreshTokenEncrypted)
	if err != nil {
		t.Fatal(err)
	}
	if string(rotated) != "authx-refresh-new" {
		t.Fatalf("rotated refresh token = %q", rotated)
	}
	if !store.maintenanceArgs.RotateBefore.Time.Equal(now.Add(defaultEnterpriseRefreshBefore)) ||
		!store.maintenanceArgs.SourceCheckBefore.Time.Equal(
			now.Add(-defaultEnterpriseSourceRefreshInterval),
		) ||
		store.maintenanceArgs.BatchSize != defaultEnterpriseMaintenanceBatch {
		t.Fatalf("maintenance query = %#v", store.maintenanceArgs)
	}
	if got := strings.Join(source.prepared, ","); got != "source-1" {
		t.Fatalf("maintained identity source = %q", got)
	}
	if got := strings.Join(source.parked, ","); got != "source-1" {
		t.Fatalf("parked maintained identity source = %q", got)
	}
}

func TestEnterpriseIdentityRevokeRetiresActiveASBSandboxes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	workspaceID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	agentID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	runtimeID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	scopeID := util.MustParseUUID("55555555-5555-5555-5555-555555555555")
	identity := db.AgentEnterpriseIdentity{
		ID:            util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		WorkspaceID:   workspaceID,
		AgentID:       agentID,
		RawEmpID:      "12345",
		BucAgentID:    "buc-agent-1",
		AgentSpiffeID: "spiffe://agents.example/ns/multica/agents/222",
		AipID:         "aip-1",
		Status:        "active",
	}
	store := &fakeEnterpriseIdentityStore{
		current: identity,
		activeSessions: []db.FcE2bSandboxSession{{
			WorkspaceID:         workspaceID,
			RuntimeID:           runtimeID,
			ScopeType:           "chat",
			ScopeID:             scopeID,
			SandboxID:           "task-sandbox-1",
			SandboxBackend:      string(SandboxBackendASB),
			IdentityFingerprint: enterpriseIdentityFingerprint(identity),
			Status:              "running",
		}},
	}
	sandboxes := &fakeEnterpriseSandboxes{}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		&fakeBUCOAuthClient{},
		&fakeEnterpriseAuthX{},
		&fakeEnterpriseIdem{},
		sandboxes,
		now,
	)

	if err := serviceUnderTest.Revoke(context.Background(), workspaceID, agentID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if len(store.markedStale) != 1 {
		t.Fatalf("marked stale sessions = %#v", store.markedStale)
	}
	marked := store.markedStale[0]
	if marked.RuntimeID != runtimeID ||
		marked.ScopeID != scopeID ||
		marked.SandboxID != "task-sandbox-1" ||
		marked.IdentityFingerprint != enterpriseIdentityFingerprint(identity) {
		t.Fatalf("marked stale session = %#v", marked)
	}
	if got := strings.Join(sandboxes.deletedIDs, ","); got != "task-sandbox-1" {
		t.Fatalf("deleted sandboxes = %q", got)
	}
	if len(sandboxes.deletedRuntimeIDs) != 1 || sandboxes.deletedRuntimeIDs[0] != runtimeID {
		t.Fatalf("deleted sandbox Runtime IDs = %#v", sandboxes.deletedRuntimeIDs)
	}
	if store.current.Status != "revoked" {
		t.Fatalf("identity status = %q", store.current.Status)
	}
}

func TestEnterpriseIdentityRevokeRetainsSharedSourceReferencedByAnotherAgent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 3, 4, 0, 0, 0, time.UTC)
	workspaceID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	boundBy := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	runtimeID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	first := db.AgentEnterpriseIdentity{
		ID:                         util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		WorkspaceID:                workspaceID,
		AgentID:                    util.MustParseUUID("55555555-5555-5555-5555-555555555555"),
		RawEmpID:                   "12345",
		BucAgentID:                 "buc-agent-1",
		AgentSpiffeID:              "spiffe://agents.example/ns/multica/agents/555",
		AipID:                      "aip-1",
		BucIdentitySourceSandboxID: pgtype.Text{String: "shared-source-1", Valid: true},
		BucIdentitySourceRuntimeID: runtimeID,
		Status:                     "active",
		BoundBy:                    boundBy,
	}
	second := first
	second.ID = util.MustParseUUID("66666666-6666-6666-6666-666666666666")
	second.AgentID = util.MustParseUUID("77777777-7777-7777-7777-777777777777")
	second.AgentSpiffeID = "spiffe://agents.example/ns/multica/agents/777"
	second.AipID = "aip-2"
	store := &fakeEnterpriseIdentityStore{
		current:    first,
		references: []db.AgentEnterpriseIdentity{first, second},
	}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		&fakeBUCOAuthClient{},
		&fakeEnterpriseAuthX{},
		&fakeEnterpriseIdem{},
		&fakeEnterpriseSandboxes{},
		now,
	)
	source := serviceUnderTest.Source.(*fakeEnterpriseIdentitySource)
	if err := serviceUnderTest.Revoke(context.Background(), workspaceID, first.AgentID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if len(source.deleted) != 0 {
		t.Fatalf("shared source deleted while referenced: %v", source.deleted)
	}
	if store.references[0].Status != "revoked" || store.references[1].Status != "active" {
		t.Fatalf("shared source references = %#v", store.references)
	}
}

func TestHTTPBUCOAuthClientRefreshUsesDocumentedEndpointAndResponse(t *testing.T) {
	t.Parallel()

	var requestSeen bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen = true
		if r.Method != http.MethodPost || r.URL.Path != "/rpc/oauth2/refresh_token.json" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" ||
			r.Form.Get("refresh_token") != "buc-refresh-old" {
			t.Errorf("refresh request form = %#v", r.Form)
		}
		if r.Form.Get("client_id") != "" || r.Form.Get("client_secret") != "" {
			t.Errorf("undocumented client credentials were sent: %#v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(
			w,
			`{"access_token":"buc-access-new","expires_in":259200,"refresh_token":"buc-refresh-new"}`,
		)
	}))
	defer server.Close()

	client, err := NewHTTPBUCOAuthClient(
		server.URL+"/rpc/oauth2/access_token.json",
		server.URL+"/oauth2",
		server.URL+"/oauth2/v1/keys",
		"buc-client-1",
		"buc-secret",
		server.URL+"/callback",
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := client.Refresh(context.Background(), "buc-refresh-old")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !requestSeen {
		t.Fatal("BUC refresh request was not sent")
	}
	if tokens.AccessToken != "buc-access-new" ||
		tokens.RefreshToken != "buc-refresh-new" ||
		tokens.IDToken != "" ||
		tokens.ExpiresIn != 259200 {
		t.Fatalf("refreshed BUC tokens = %#v", tokens)
	}
}

func TestHTTPBUCOAuthClientRefreshClassifiesNumericProviderError(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(
			w,
			`{"error_code":240115,"error":"invalid_refresh_token"}`,
		)
	}))
	defer server.Close()

	client, err := NewHTTPBUCOAuthClient(
		server.URL+"/rpc/oauth2/access_token.json",
		server.URL+"/oauth2",
		server.URL+"/oauth2/v1/keys",
		"buc-client-1",
		"buc-secret",
		"https://multica.example/api/agent-enterprise-identity/buc/callback",
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Refresh(context.Background(), "buc-refresh-old")
	var providerErr *enterpriseIdentityProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("Refresh error = %v", err)
	}
	if providerErr.Code != "240115" ||
		providerErr.Class != "buc_oauth_error" ||
		providerErr.StatusCode != http.StatusOK {
		t.Fatalf("provider error = %#v", providerErr)
	}
}

func TestHTTPBUCOAuthClientGeneratesRepeatableSSOTicket(t *testing.T) {
	t.Parallel()

	var requestSeen bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen = true
		if r.Method != http.MethodPost || r.URL.Path != enterpriseBUCSSOTicketPath {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.Form.Get("access_token") != "buc-access" ||
			r.Form.Get("expires_type") != "repeatable" ||
			r.Form.Get("expires_in") != "300" {
			t.Errorf("ticket request form = %#v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(
			w,
			`{"success":true,"error":"0","content":{"data":{"ssoTicket":"buc-sso-ticket","expiresIn":300}}}`,
		)
	}))
	defer server.Close()

	client, err := NewHTTPBUCOAuthClient(
		server.URL+"/rpc/oauth2/access_token.json",
		server.URL+"/oauth2",
		server.URL+"/oauth2/v1/keys",
		"buc-client-1",
		"buc-secret",
		"https://multica.example/api/agent-enterprise-identity/buc/callback",
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := client.GenerateSSOTicket(context.Background(), "buc-access")
	if err != nil {
		t.Fatalf("GenerateSSOTicket: %v", err)
	}
	if !requestSeen || ticket != "buc-sso-ticket" {
		t.Fatalf("request seen=%v ticket=%q", requestSeen, ticket)
	}
}

func TestHTTPBUCOAuthClientReportsSSOTicketProviderError(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(
			w,
			`{"success":false,"errorCode":"240117","errorMsg":"access token expired"}`,
		)
	}))
	defer server.Close()

	client, err := NewHTTPBUCOAuthClient(
		server.URL+"/rpc/oauth2/access_token.json",
		server.URL+"/oauth2",
		server.URL+"/oauth2/v1/keys",
		"buc-client-1",
		"buc-secret",
		"https://multica.example/api/agent-enterprise-identity/buc/callback",
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GenerateSSOTicket(context.Background(), "expired-buc-access")
	var providerErr *enterpriseIdentityProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("GenerateSSOTicket error = %v", err)
	}
	if providerErr.Provider != "buc" ||
		providerErr.Operation != "generate_sso_ticket" ||
		providerErr.Class != "buc_ticket_error" ||
		providerErr.StatusCode != http.StatusOK ||
		providerErr.Code != "240117" {
		t.Fatalf("provider error = %#v", providerErr)
	}
}

func TestHTTPBUCOAuthClientVerifyIDTokenRejectsForgedSignatureAndMissingIdentityClaims(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	client, err := NewHTTPBUCOAuthClient(
		defaultBUCTokenURL,
		defaultBUCIssuer,
		defaultBUCJWKSURL,
		"buc-client-1",
		"test-only-key",
		"https://multica.example/api/agent-enterprise-identity/buc/callback",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	validToken := signedTestBUCHMACToken(t, now, "nonce-1", "openid-1", []byte("test-only-key"))
	if _, err := client.VerifyIDToken(
		context.Background(),
		validToken,
		sha256Bytes("nonce-1"),
		now,
	); err != nil {
		t.Fatalf("verify valid HMAC token: %v", err)
	}
	missingSubjectToken := signedTestBUCHMACToken(t, now, "nonce-1", "", []byte("test-only-key"))
	if _, err := client.VerifyIDToken(
		context.Background(),
		missingSubjectToken,
		sha256Bytes("nonce-1"),
		now,
	); err == nil {
		t.Fatal("expected missing subject to fail")
	}
	missingEmployeeClaims := testBUCIDTokenClaims(now, defaultBUCIssuer, "nonce-1")
	missingEmployeeClaims.EmployeeID = ""
	missingEmployeeToken, err := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		missingEmployeeClaims,
	).SignedString([]byte("test-only-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.VerifyIDToken(
		context.Background(),
		missingEmployeeToken,
		sha256Bytes("nonce-1"),
		now,
	); err == nil {
		t.Fatal("expected missing employee identity to fail")
	}
	forgedToken := signedTestBUCHMACToken(t, now, "nonce-1", "openid-1", []byte("attacker-key"))
	if _, err := client.VerifyIDToken(
		context.Background(),
		forgedToken,
		sha256Bytes("nonce-1"),
		now,
	); err == nil {
		t.Fatal("expected forged token signature to fail")
	}
}

func TestHTTPBUCOAuthClientVerifyIDTokenUsesBUCJWKSForRSA(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "buc-key-1"
	var issuer string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jwks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"kid": keyID,
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes()),
			}},
		}); err != nil {
			t.Errorf("encode JWKS: %v", err)
		}
	}))
	defer server.Close()
	issuer = server.URL + "/issuer"
	client, err := NewHTTPBUCOAuthClient(
		server.URL+"/token",
		issuer,
		server.URL+"/jwks",
		"buc-client-1",
		"unused-for-rsa",
		server.URL+"/callback",
		server.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}
	token := signedTestBUCRSAToken(t, now, issuer, "nonce-1", keyID, privateKey)
	claims, err := client.VerifyIDToken(
		context.Background(),
		token,
		sha256Bytes("nonce-1"),
		now,
	)
	if err != nil {
		t.Fatalf("verify RSA token: %v", err)
	}
	if claims.Name != "测试员工" {
		t.Fatalf("verified claims = %#v", claims)
	}
}

func newTestEnterpriseIdentityService(
	t *testing.T,
	store enterpriseIdentityStore,
	buc BUCOAuthClient,
	authX EnterpriseAuthX,
	idem EnterpriseIdem,
	sandboxes EnterpriseIdentitySandboxController,
	now time.Time,
) *EnterpriseIdentityService {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{0x24}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	serviceUnderTest, err := NewEnterpriseIdentityService(
		store,
		EnterpriseIdentityConfig{
			Enabled:             true,
			BUCAuthorizeURL:     defaultBUCAuthorizeURL,
			BUCTokenURL:         defaultBUCTokenURL,
			BUCIssuer:           defaultBUCIssuer,
			BUCJWKSURL:          defaultBUCJWKSURL,
			BUCClientID:         "buc-client-1",
			BUCClientSecret:     "buc-secret",
			BUCAgentID:          "buc-agent-1",
			BUCRedirectURL:      "https://multica.example/api/agent-enterprise-identity/buc/callback",
			BUCAuthorizeApps:    []string{"authorized-app-1", "authorized-app-2"},
			AuthXServiceID:      "multica",
			AuthXAudience:       "https://authx.alibaba-inc.com",
			AuthXTTL:            3600,
			AuthXEnvironment:    "production",
			IdemBaseURL:         "http://id-api.alibaba-inc.com",
			IdemTimeout:         time.Second,
			OperatorTrustDomain: "operators.example",
			AgentTrustDomain:    "agents.example",
			AgentNamespace:      "multica",
			AITTTL:              900,
			OAuthAttemptTTL:     10 * time.Minute,
		},
		buc,
		authX,
		idem,
		sandboxes,
		&fakeEnterpriseIdentityTenantResolver{},
		&fakeEnterpriseIdentitySource{},
		box,
		&fakeEnterpriseIdentityTokenRotationLocker{},
		&fakeEnterpriseIdentitySourceLocker{},
		&fakeEnterpriseIdentityRuntimeLocker{},
	)
	if err != nil {
		t.Fatal(err)
	}
	serviceUnderTest.Now = func() time.Time { return now }
	return serviceUnderTest
}

func testBUCIDTokenClaims(now time.Time, issuer, nonce string) bucIDTokenClaims {
	return bucIDTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   "openid-1",
			Audience:  jwt.ClaimStrings{"buc-client-1"},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
		EmployeeID: "12345",
		Nonce:      nonce,
		Name:       "测试员工",
	}
}

func signedTestBUCHMACToken(t *testing.T, now time.Time, nonce, subject string, key []byte) string {
	t.Helper()
	claims := testBUCIDTokenClaims(now, defaultBUCIssuer, nonce)
	claims.Subject = subject
	token, err := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		claims,
	).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func signedTestBUCRSAToken(
	t *testing.T,
	now time.Time,
	issuer string,
	nonce string,
	keyID string,
	privateKey *rsa.PrivateKey,
) string {
	t.Helper()
	token := jwt.NewWithClaims(
		jwt.SigningMethodRS256,
		testBUCIDTokenClaims(now, issuer, nonce),
	)
	token.Header["kid"] = keyID
	signed, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
