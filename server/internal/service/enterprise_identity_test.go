package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	idemapi "gitlab.alibaba-inc.com/idem/idem-api-client-golang"
)

type fakeEnterpriseIdentityStore struct {
	attempt         db.AgentEnterpriseIdentityAttempt
	agent           db.Agent
	current         db.AgentEnterpriseIdentity
	getCurrentErr   error
	createAttempt   db.CreateAgentEnterpriseIdentityAttemptParams
	upsert          db.UpsertAgentEnterpriseIdentityParams
	cas             db.CompareAndSwapAgentEnterpriseIdentityTokenParams
	markNeedsReauth int
	maintenance     []db.AgentEnterpriseIdentity
	maintenanceArgs db.ListAgentEnterpriseIdentitiesForMaintenanceParams
	maintenanceHits int
	activeSessions  []db.FcE2bSandboxSession
	markedStale     []db.MarkCloudSandboxSessionStaleParams
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
		BucAnchorSandboxID:         params.BucAnchorSandboxID,
		AuthxRefreshTokenEncrypted: params.AuthxRefreshTokenEncrypted,
		AuthxRefreshExpiresAt:      params.AuthxRefreshExpiresAt,
		TokenVersion:               1,
		Status:                     "active",
		BoundBy:                    params.BoundBy,
	}
	return f.current, nil
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
	return 1, nil
}

func (f *fakeEnterpriseIdentityStore) ListAgentEnterpriseIdentitiesForMaintenance(
	_ context.Context,
	params db.ListAgentEnterpriseIdentitiesForMaintenanceParams,
) ([]db.AgentEnterpriseIdentity, error) {
	f.maintenanceArgs = params
	return f.maintenance, nil
}

func (f *fakeEnterpriseIdentityStore) TouchAgentEnterpriseIdentityMaintenance(
	context.Context,
	db.TouchAgentEnterpriseIdentityMaintenanceParams,
) (int64, error) {
	f.maintenanceHits++
	return 1, nil
}

func (f *fakeEnterpriseIdentityStore) RevokeAgentEnterpriseIdentity(
	context.Context,
	db.RevokeAgentEnterpriseIdentityParams,
) (db.AgentEnterpriseIdentity, error) {
	f.current.Status = "revoked"
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
	issuedFrom  string
	renewedFrom string
	issueResult EnterpriseOIDCToken
	renewResult EnterpriseOIDCToken
}

func (f *fakeEnterpriseAuthX) IssueFromBUCIDToken(_ context.Context, token string) (EnterpriseOIDCToken, error) {
	f.issuedFrom = token
	return f.issueResult, nil
}

func (f *fakeEnterpriseAuthX) Renew(_ context.Context, token string) (EnterpriseOIDCToken, error) {
	f.renewedFrom = token
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

type fakeEnterpriseAnchor struct {
	createdEmployee string
	createdTokens   BUCIdentityTokens
	availableID     string
	deletedID       string
	deletedIDs      []string
}

func (f *fakeEnterpriseAnchor) Create(
	_ context.Context,
	_ pgtype.UUID,
	_ pgtype.UUID,
	employeeID string,
	tokens BUCIdentityTokens,
) (string, error) {
	f.createdEmployee = employeeID
	f.createdTokens = tokens
	return "anchor-1", nil
}

func (f *fakeEnterpriseAnchor) EnsureAvailable(_ context.Context, sandboxID string) error {
	f.availableID = sandboxID
	return nil
}

func (f *fakeEnterpriseAnchor) Delete(_ context.Context, sandboxID string) error {
	f.deletedID = sandboxID
	f.deletedIDs = append(f.deletedIDs, sandboxID)
	return nil
}

type fakeBUCOAuthClient struct {
	tokens        BUCIdentityTokens
	claims        bucIDTokenClaims
	verifiedToken string
	verifyErr     error
}

func (f *fakeBUCOAuthClient) ExchangeCode(context.Context, string) (BUCIdentityTokens, error) {
	return f.tokens, nil
}

func (f *fakeBUCOAuthClient) VerifyIDToken(
	_ context.Context,
	token string,
	_ string,
	_ string,
	_ []byte,
	_ time.Time,
) (bucIDTokenClaims, error) {
	f.verifiedToken = token
	return f.claims, f.verifyErr
}

func TestEnterpriseIdentityStartBindingStoresOnlyHashedStateAndNonce(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	store := &fakeEnterpriseIdentityStore{}
	serviceUnderTest := newTestEnterpriseIdentityService(t, store, &fakeBUCOAuthClient{}, &fakeEnterpriseAuthX{}, &fakeEnterpriseIdem{}, &fakeEnterpriseAnchor{}, now)
	result, err := serviceUnderTest.StartBinding(context.Background(), StartEnterpriseIdentityBindingInput{
		WorkspaceID:  util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentID:      util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		ActorUserID:  util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		EmployeeID:   "12345",
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
	if authorizeURL.Query().Get("agent_id") != "buc-agent-1" ||
		authorizeURL.Query().Get("authorize_app") != "a1,mw" {
		t.Fatalf("authorize query = %v", authorizeURL.Query())
	}
}

func TestEnterpriseIdentityBindingRequiresRevokeBeforeChangingEmployee(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	workspaceID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	agentID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	store := &fakeEnterpriseIdentityStore{
		current: db.AgentEnterpriseIdentity{
			ID:          util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
			WorkspaceID: workspaceID,
			AgentID:     agentID,
			RawEmpID:    "12345",
			Status:      "active",
		},
	}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		&fakeBUCOAuthClient{},
		&fakeEnterpriseAuthX{},
		&fakeEnterpriseIdem{},
		&fakeEnterpriseAnchor{},
		now,
	)

	_, err := serviceUnderTest.StartBinding(context.Background(), StartEnterpriseIdentityBindingInput{
		WorkspaceID:  workspaceID,
		AgentID:      agentID,
		ActorUserID:  util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		EmployeeID:   "67890",
		RedirectPath: "/agents/222/settings",
	})
	if !errors.Is(err, ErrEnterpriseIdentityEmployeeConflict) {
		t.Fatalf("StartBinding error = %v", err)
	}
	if len(store.createAttempt.StateHash) != 0 {
		t.Fatal("OAuth attempt was created for a conflicting employee binding")
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
			AgentType:    "agent",
			AipAgentType: "assistant",
			Framework: &idemapi.AgentIdentityProfileFramework{
				Name: "multica",
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

func TestEnterpriseIdentityCompleteBindingPersistsOnlyEncryptedAuthXRefresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	workspaceID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	agentID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	actorID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	state := "oauth-state"
	nonce := "oauth-nonce"
	bucIDToken := "signed-buc-id-token"
	store := &fakeEnterpriseIdentityStore{
		attempt: db.AgentEnterpriseIdentityAttempt{
			WorkspaceID:       workspaceID,
			AgentID:           agentID,
			ActorUserID:       actorID,
			RequestedRawEmpID: "12345",
			StateHash:         sha256Bytes(state),
			NonceHash:         sha256Bytes(nonce),
			RedirectPath:      "/agents/222/settings",
		},
		agent: db.Agent{
			ID:          agentID,
			WorkspaceID: workspaceID,
			Name:        "A1 Explorer",
			Model:       pgtype.Text{String: "qwen3", Valid: true},
		},
		getCurrentErr: pgx.ErrNoRows,
	}
	buc := &fakeBUCOAuthClient{
		tokens: BUCIdentityTokens{
			AccessToken:  "buc-access",
			RefreshToken: "buc-refresh",
			IDToken:      bucIDToken,
		},
		claims: bucIDTokenClaims{
			EmployeeID: "12345",
			AgentID:    "buc-agent-1",
			Nonce:      nonce,
			Name:       "测试员工",
		},
	}
	authX := &fakeEnterpriseAuthX{issueResult: EnterpriseOIDCToken{
		IDToken:          "authx-id",
		RefreshToken:     "authx-refresh",
		ExpiresAt:        now.Add(time.Hour),
		RefreshExpiresAt: now.Add(24 * time.Hour),
	}}
	idem := &fakeEnterpriseIdem{ait: "ait-1"}
	anchor := &fakeEnterpriseAnchor{}
	serviceUnderTest := newTestEnterpriseIdentityService(t, store, buc, authX, idem, anchor, now)

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
	if anchor.createdEmployee != "12345" ||
		anchor.createdTokens.RefreshToken != "buc-refresh" ||
		anchor.createdTokens.IDToken != bucIDToken {
		t.Fatalf("anchor input = %#v", anchor)
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
	if store.upsert.BucAnchorSandboxID.String != "anchor-1" ||
		store.upsert.AgentSpiffeID == "" ||
		store.upsert.AipID != "aip-1" {
		t.Fatalf("persisted identity = %#v", store.upsert)
	}
}

func TestEnterpriseIdentityResolveRotatesRefreshAndIssuesTaskAIT(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte("refresh-old"))
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
			BucAnchorSandboxID:         pgtype.Text{String: "anchor-1", Valid: true},
			AuthxRefreshTokenEncrypted: sealed,
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
	anchor := &fakeEnterpriseAnchor{}
	serviceUnderTest := newTestEnterpriseIdentityService(t, store, &fakeBUCOAuthClient{}, authX, idem, anchor, now)
	serviceUnderTest.Secrets = box

	resolved, err := serviceUnderTest.ResolveASBTaskIdentity(
		context.Background(),
		store.current.WorkspaceID,
		store.current.AgentID,
	)
	if err != nil {
		t.Fatalf("ResolveASBTaskIdentity: %v", err)
	}
	if authX.renewedFrom != "refresh-old" ||
		idem.issuedOIDC != "authx-id-new" ||
		resolved.AgentIdentityToken != "ait-task" ||
		resolved.AnchorSandboxID != "anchor-1" ||
		len(resolved.Fingerprint) != 64 {
		t.Fatalf("resolved = %#v", resolved)
	}
	rotated, err := box.Open(store.cas.AuthxRefreshTokenEncrypted)
	if err != nil {
		t.Fatal(err)
	}
	if string(rotated) != "refresh-new" || anchor.availableID != "anchor-1" {
		t.Fatalf("rotation = %q anchor=%q", rotated, anchor.availableID)
	}
}

func TestEnterpriseIdentityMaintenanceRenewsAnchorAndRotatesRefresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte("refresh-old"))
	if err != nil {
		t.Fatal(err)
	}
	identity := db.AgentEnterpriseIdentity{
		ID:                         util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		WorkspaceID:                util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentID:                    util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		RawEmpID:                   "12345",
		BucAnchorSandboxID:         pgtype.Text{String: "anchor-1", Valid: true},
		AuthxRefreshTokenEncrypted: sealed,
		AuthxRefreshExpiresAt:      pgtype.Timestamptz{Time: now.Add(10 * time.Minute), Valid: true},
		TokenVersion:               2,
		Status:                     "active",
	}
	store := &fakeEnterpriseIdentityStore{
		current:     identity,
		maintenance: []db.AgentEnterpriseIdentity{identity},
	}
	authX := &fakeEnterpriseAuthX{renewResult: EnterpriseOIDCToken{
		IDToken:          "authx-id-new",
		RefreshToken:     "refresh-new",
		ExpiresAt:        now.Add(time.Hour),
		RefreshExpiresAt: now.Add(24 * time.Hour),
	}}
	anchor := &fakeEnterpriseAnchor{}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		&fakeBUCOAuthClient{},
		authX,
		&fakeEnterpriseIdem{},
		anchor,
		now,
	)
	serviceUnderTest.Secrets = box

	if err := serviceUnderTest.maintainActiveIdentities(context.Background()); err != nil {
		t.Fatalf("maintainActiveIdentities: %v", err)
	}
	if anchor.availableID != "anchor-1" || authX.renewedFrom != "refresh-old" {
		t.Fatalf("anchor=%q renewed_from=%q", anchor.availableID, authX.renewedFrom)
	}
	rotated, err := box.Open(store.cas.AuthxRefreshTokenEncrypted)
	if err != nil {
		t.Fatal(err)
	}
	if string(rotated) != "refresh-new" {
		t.Fatalf("rotated refresh token = %q", rotated)
	}
	if !store.maintenanceArgs.RotateBefore.Time.Equal(now.Add(defaultEnterpriseRefreshBefore)) ||
		!store.maintenanceArgs.AnchorRenewBefore.Time.Equal(now.Add(-defaultEnterpriseAnchorRenewInterval)) ||
		store.maintenanceArgs.BatchSize != defaultEnterpriseMaintenanceBatch {
		t.Fatalf("maintenance query = %#v", store.maintenanceArgs)
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
		ID:                 util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		WorkspaceID:        workspaceID,
		AgentID:            agentID,
		RawEmpID:           "12345",
		BucAgentID:         "buc-agent-1",
		AgentSpiffeID:      "spiffe://agents.example/ns/multica/agents/222",
		AipID:              "aip-1",
		BucAnchorSandboxID: pgtype.Text{String: "anchor-1", Valid: true},
		Status:             "active",
	}
	reboundIdentity := identity
	reboundIdentity.BucAnchorSandboxID = pgtype.Text{String: "anchor-2", Valid: true}
	if enterpriseIdentityFingerprint(identity) == enterpriseIdentityFingerprint(reboundIdentity) {
		t.Fatal("identity fingerprint did not change with the binding anchor")
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
	anchor := &fakeEnterpriseAnchor{}
	serviceUnderTest := newTestEnterpriseIdentityService(
		t,
		store,
		&fakeBUCOAuthClient{},
		&fakeEnterpriseAuthX{},
		&fakeEnterpriseIdem{},
		anchor,
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
	if got := strings.Join(anchor.deletedIDs, ","); got != "task-sandbox-1,anchor-1" {
		t.Fatalf("deleted sandboxes = %q", got)
	}
	if store.current.Status != "revoked" {
		t.Fatalf("identity status = %q", store.current.Status)
	}
}

func TestHTTPBUCOAuthClientVerifyIDTokenRejectsForgedSignatureAndEmployeeMismatch(t *testing.T) {
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
	validToken := signedTestBUCHMACToken(t, now, "nonce-1", []byte("test-only-key"))
	if _, err := client.VerifyIDToken(
		context.Background(),
		validToken,
		"12345",
		"buc-agent-1",
		sha256Bytes("nonce-1"),
		now,
	); err != nil {
		t.Fatalf("verify valid HMAC token: %v", err)
	}
	if _, err := client.VerifyIDToken(
		context.Background(),
		validToken,
		"99999",
		"buc-agent-1",
		sha256Bytes("nonce-1"),
		now,
	); err == nil {
		t.Fatal("expected employee mismatch to fail")
	}
	forgedToken := signedTestBUCHMACToken(t, now, "nonce-1", []byte("attacker-key"))
	if _, err := client.VerifyIDToken(
		context.Background(),
		forgedToken,
		"12345",
		"buc-agent-1",
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
		"12345",
		"buc-agent-1",
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
	anchor EnterpriseIdentityAnchor,
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
			BUCAuthorizeApps:    []string{"a1", "mw"},
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
		anchor,
		box,
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
			Audience:  jwt.ClaimStrings{"buc-client-1"},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
		EmployeeID: "12345",
		AgentID:    "buc-agent-1",
		Nonce:      nonce,
		Name:       "测试员工",
	}
}

func signedTestBUCHMACToken(t *testing.T, now time.Time, nonce string, key []byte) string {
	t.Helper()
	token, err := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		testBUCIDTokenClaims(now, defaultBUCIssuer, nonce),
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
