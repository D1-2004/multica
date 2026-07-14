package agentmessagerouter

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const canonicalCallbackToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type fakeBindingStore struct {
	row          db.ChannelInstallation
	beginErr     error
	getErr       error
	activateErr  error
	activateHook func(*fakeBindingStore)
	revokeErr    error
	listErr      error
	cleanupErr   error
	beginConfig  []byte
	revokeArg    db.RevokeDingTalkAccountBindingParams
	cleanupCalls int
	activated    bool
	revoked      bool
}

func (f *fakeBindingStore) BeginDingTalkAccountBinding(_ context.Context, arg db.BeginDingTalkAccountBindingParams) (db.ChannelInstallation, error) {
	if f.beginErr != nil {
		return db.ChannelInstallation{}, f.beginErr
	}
	f.beginConfig = append([]byte(nil), arg.Config...)
	if f.row.ID.Valid {
		old, err := ParseDingTalkAccountConfig(f.row.Config)
		if err != nil {
			return db.ChannelInstallation{}, err
		}
		pending, err := ParseDingTalkAccountConfig(arg.Config)
		if err != nil {
			return db.ChannelInstallation{}, err
		}
		pending.DispatchEndpointID = old.DispatchEndpointID
		pending.DispatchKeyID = old.DispatchKeyID
		pending.DispatchURL = old.DispatchURL
		arg.Config, err = pending.Marshal()
		if err != nil {
			return db.ChannelInstallation{}, err
		}
		f.row.Config = arg.Config
		f.row.Status = "pending"
		f.row.InstallerUserID = arg.InstallerUserID
		return f.row, nil
	}
	f.row = bindingRow(
		uuidStringForTest(arg.WorkspaceID),
		uuidStringForTest(arg.AgentID),
		"11111111-1111-1111-1111-111111111111",
		"pending",
		arg.Config,
	)
	f.row.InstallerUserID = arg.InstallerUserID
	return f.row, nil
}

func (f *fakeBindingStore) GetDingTalkAccountBinding(_ context.Context, _ pgtype.UUID) (db.ChannelInstallation, error) {
	if f.getErr != nil {
		return db.ChannelInstallation{}, f.getErr
	}
	if !f.row.ID.Valid {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	return f.row, nil
}

func (f *fakeBindingStore) GetDingTalkAccountBindingByAgent(_ context.Context, _ db.GetDingTalkAccountBindingByAgentParams) (db.ChannelInstallation, error) {
	if f.getErr != nil {
		return db.ChannelInstallation{}, f.getErr
	}
	if !f.row.ID.Valid {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	return f.row, nil
}

func (f *fakeBindingStore) GetDingTalkAccountBindingInWorkspace(_ context.Context, _ db.GetDingTalkAccountBindingInWorkspaceParams) (db.ChannelInstallation, error) {
	if f.getErr != nil {
		return db.ChannelInstallation{}, f.getErr
	}
	if !f.row.ID.Valid {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	return f.row, nil
}

func (f *fakeBindingStore) ListDingTalkAccountBindings(_ context.Context, _ pgtype.UUID) ([]db.ChannelInstallation, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if !f.row.ID.Valid {
		return nil, nil
	}
	return []db.ChannelInstallation{f.row}, nil
}

func (f *fakeBindingStore) ClearExpiredDingTalkAccountCallbackCredentials(_ context.Context, arg db.ClearExpiredDingTalkAccountCallbackCredentialsParams) error {
	f.cleanupCalls++
	if f.cleanupErr != nil {
		return f.cleanupErr
	}
	if !f.row.ID.Valid || !arg.ExpiredBefore.Valid {
		return nil
	}
	config, err := ParseDingTalkAccountConfig(f.row.Config)
	if err != nil {
		return err
	}
	if config.CallbackExpiresAt.IsZero() || config.CallbackExpiresAt.After(arg.ExpiredBefore.Time) {
		return nil
	}
	config.CallbackTokenHash = ""
	config.CallbackExpiresAt = time.Time{}
	f.row.Config, err = config.Marshal()
	return err
}

func (f *fakeBindingStore) ActivateDingTalkAccountBinding(_ context.Context, arg db.ActivateDingTalkAccountBindingParams) (db.ChannelInstallation, error) {
	if f.activateHook != nil {
		f.activateHook(f)
	}
	if f.activateErr != nil {
		return db.ChannelInstallation{}, f.activateErr
	}
	f.row.Config = append([]byte(nil), arg.Config...)
	f.row.Status = "active"
	f.activated = true
	return f.row, nil
}

func (f *fakeBindingStore) RevokeDingTalkAccountBinding(_ context.Context, arg db.RevokeDingTalkAccountBindingParams) (db.ChannelInstallation, error) {
	if f.revokeErr != nil {
		return db.ChannelInstallation{}, f.revokeErr
	}
	f.revokeArg = arg
	f.row.Status = "revoked"
	f.revoked = true
	return f.row, nil
}

type fakeBindingRouter struct {
	issued       BindingToken
	issueErr     error
	subscription Subscription
	getErr       error
	deleteErr    error
	issueAgent   string
	issueURL     string
	deleted      []string
}

func (f *fakeBindingRouter) IssueBindingToken(_ context.Context, agentID, dispatchURL string) (BindingToken, error) {
	f.issueAgent = agentID
	f.issueURL = dispatchURL
	return f.issued, f.issueErr
}

func (f *fakeBindingRouter) GetSubscription(_ context.Context, sourceID string) (Subscription, error) {
	if f.subscription.SourceID == "" {
		f.subscription.SourceID = sourceID
	}
	return f.subscription, f.getErr
}

func (f *fakeBindingRouter) DeleteSubscription(_ context.Context, sourceID string) error {
	f.deleted = append(f.deleted, sourceID)
	return f.deleteErr
}

func TestBeginDingTalkAccountBindingReusesEndpointAndDoesNotPersistRouterToken(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	workspaceID := uuidForTest(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	agentID := uuidForTest(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	initiatorID := uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc")
	oldEndpoint := "v1_EREREREREREREREREREREQ"
	oldConfig := NewPendingDingTalkAccountConfig(
		oldEndpoint,
		"https://multica.example/api/webhooks/agent-dispatch/"+oldEndpoint,
		HashCallbackToken(canonicalCallbackToken),
		now.Add(time.Minute),
	)
	oldRaw, err := oldConfig.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeBindingStore{row: bindingRow(
		uuidStringForTest(workspaceID),
		uuidStringForTest(agentID),
		"11111111-1111-1111-1111-111111111111",
		"pending",
		oldRaw,
	)}
	router := &fakeBindingRouter{issued: BindingToken{
		BindingToken: "bat_v1.router-secret-must-not-be-persisted",
		ExpiresAt:    now.Add(5 * time.Minute),
	}}
	service := newBindingServiceForTest(t, store, router, now)

	result, err := service.Begin(context.Background(), BeginParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		InitiatorID: initiatorID,
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if result.InstallationID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("installation id = %q", result.InstallationID)
	}
	if result.ExpiresAt != router.issued.ExpiresAt {
		t.Fatalf("expires at = %v, want %v", result.ExpiresAt, router.issued.ExpiresAt)
	}
	parsedQR, err := url.Parse(result.QRCodeURL)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(parsedQR.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragment) != 4 || fragment.Get("bindingToken") != router.issued.BindingToken ||
		fragment.Get("callbackToken") == "" || fragment.Get("callbackUrl") == "" ||
		fragment.Get("expiresAt") != strconv.FormatInt(router.issued.ExpiresAt.Unix(), 10) {
		t.Fatalf("unexpected QR fragment: %#v", fragment)
	}
	if fragment.Get("agentId") != "" || fragment.Get("dispatchUrl") != "" {
		t.Fatalf("QR leaked routing fields: %#v", fragment)
	}
	persisted := string(store.row.Config)
	if strings.Contains(persisted, router.issued.BindingToken) || strings.Contains(string(store.beginConfig), router.issued.BindingToken) {
		t.Fatal("Router binding token was persisted")
	}
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	if config.DispatchEndpointID != oldEndpoint {
		t.Fatalf("endpoint = %q, want reused %q", config.DispatchEndpointID, oldEndpoint)
	}
	if !VerifyCallbackToken(fragment.Get("callbackToken"), config.CallbackTokenHash) {
		t.Fatal("persisted callback hash does not match returned token")
	}
	if router.issueAgent != uuidStringForTest(agentID) || router.issueURL != config.DispatchURL {
		t.Fatalf("Router issue request = agent %q url %q", router.issueAgent, router.issueURL)
	}
}

func TestBeginDingTalkAccountBindingRejectsActive(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	config := NewPendingDingTalkAccountConfig(
		"v1_EREREREREREREREREREREQ",
		"https://multica.example/api/webhooks/agent-dispatch/v1_EREREREREREREREREREREQ",
		HashCallbackToken(canonicalCallbackToken),
		now.Add(time.Minute),
	)
	raw, err := config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeBindingStore{
		row: bindingRow(
			"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			"11111111-1111-1111-1111-111111111111",
			"active",
			raw,
		),
		beginErr: pgx.ErrNoRows,
	}
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	_, err = service.Begin(context.Background(), BeginParams{
		WorkspaceID: store.row.WorkspaceID,
		AgentID:     store.row.AgentID,
		InitiatorID: uuidForTest(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"),
	})
	if !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("Begin() error = %v, want ErrAlreadyActive", err)
	}
	if router.issueAgent != "" {
		t.Fatal("active binding must not issue a Router token")
	}
}

func TestCompleteCallbackVerifiesSubscriptionAndActivates(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	callbackToken := canonicalCallbackToken
	store := pendingBindingStore(t, now, callbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-1",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	binding, err := service.CompleteCallback(context.Background(), CallbackParams{
		InstallationID:   store.row.ID,
		CallbackToken:    callbackToken,
		SourceID:         "source-1",
		AccountDisplayName: "Zhang San",
		AccountAvatarURL: "https://example.com/avatar.png",
	})
	if err != nil {
		t.Fatalf("CompleteCallback() error = %v", err)
	}
	if !store.activated || binding.Status != "active" || binding.AccountDisplayName != "Zhang San" {
		t.Fatalf("binding = %#v, activated=%v", binding, store.activated)
	}
	activeConfig, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	if activeConfig.RouterSourceID != "source-1" || activeConfig.BoundAt == nil {
		t.Fatalf("active config = %#v", activeConfig)
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{callbackToken, config.CallbackTokenHash, config.DispatchEndpointID, "source-1"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public binding leaked %q: %s", secret, encoded)
		}
	}
}

func TestCompleteCallbackMismatchDeletesRouterSubscriptionWithoutActivating(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-1",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "inactive",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	_, err = service.CompleteCallback(context.Background(), CallbackParams{
		InstallationID: store.row.ID,
		CallbackToken:  canonicalCallbackToken,
		SourceID:       "source-1",
	})
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("CompleteCallback() error = %v, want ErrBindingConflict", err)
	}
	if store.activated {
		t.Fatal("mismatched Router subscription was activated")
	}
	if len(router.deleted) != 1 || router.deleted[0] != "source-1" {
		t.Fatalf("compensation deletes = %#v", router.deleted)
	}
}

func TestCompleteCallbackCompensatesVerifiedSourceWhenActivationLosesPendingCAS(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	store.activateErr = pgx.ErrNoRows
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-1",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	_, err = service.CompleteCallback(context.Background(), CallbackParams{
		InstallationID: store.row.ID,
		CallbackToken:  canonicalCallbackToken,
		SourceID:       "source-1",
	})
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("CompleteCallback() error = %v, want ErrBindingConflict", err)
	}
	if len(router.deleted) != 1 || router.deleted[0] != "source-1" {
		t.Fatalf("verified stale source compensation = %#v", router.deleted)
	}
}

func TestCompleteCallbackCompensatesLosingSourceWhenAnotherCallbackActivates(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	store.activateErr = pgx.ErrNoRows
	store.activateHook = func(store *fakeBindingStore) {
		winning := config
		winning.RouterSourceID = "source-winner"
		winning.BoundAt = &now
		store.row.Config, err = winning.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		store.row.Status = "active"
	}
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-loser",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	_, err = service.CompleteCallback(context.Background(), CallbackParams{
		InstallationID: store.row.ID,
		CallbackToken:  canonicalCallbackToken,
		SourceID:       "source-loser",
	})

	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("CompleteCallback() error = %v, want ErrBindingConflict", err)
	}
	if len(router.deleted) != 1 || router.deleted[0] != "source-loser" {
		t.Fatalf("losing source compensation = %#v", router.deleted)
	}
}

func TestCompleteCallbackIsIdempotentAndRejectsDifferentSource(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	boundAt := now.Add(-time.Second)
	config.RouterSourceID = "source-1"
	config.BoundAt = &boundAt
	store.row.Config, err = config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store.row.Status = "active"
	router := &fakeBindingRouter{subscription: Subscription{
		SourceID:    "source-1",
		AgentID:     uuidStringForTest(store.row.AgentID),
		DispatchURL: config.DispatchURL,
		Status:      "active",
	}}
	service := newBindingServiceForTest(t, store, router, now)

	if _, err := service.CompleteCallback(context.Background(), CallbackParams{
		InstallationID: store.row.ID,
		CallbackToken:  canonicalCallbackToken,
		SourceID:       "source-1",
	}); err != nil {
		t.Fatalf("idempotent callback error = %v", err)
	}
	if store.activated {
		t.Fatal("idempotent callback must not rewrite active row")
	}

	_, err = service.CompleteCallback(context.Background(), CallbackParams{
		InstallationID: store.row.ID,
		CallbackToken:  canonicalCallbackToken,
		SourceID:       "source-2",
	})
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("different-source callback error = %v", err)
	}
	if len(router.deleted) != 0 {
		t.Fatalf("different source must not delete an unverified Router subscription: %#v", router.deleted)
	}
}

func TestCompleteCallbackRejectsExpiredTokenBeforeRouterLookup(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now.Add(-20*time.Minute), canonicalCallbackToken)
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	_, err := service.CompleteCallback(context.Background(), CallbackParams{
		InstallationID: store.row.ID,
		CallbackToken:  canonicalCallbackToken,
		SourceID:       "source-1",
	})
	if !errors.Is(err, ErrCallbackExpired) {
		t.Fatalf("CompleteCallback() error = %v, want ErrCallbackExpired", err)
	}
	if router.subscription.SourceID != "" {
		t.Fatal("expired callback should not query Router")
	}
}

func TestCompleteCallbackMapsMissingRouterSubscriptionToConflict(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	router := &fakeBindingRouter{getErr: ErrSubscriptionNotFound}
	service := newBindingServiceForTest(t, store, router, now)

	_, err := service.CompleteCallback(context.Background(), CallbackParams{
		InstallationID: store.row.ID,
		CallbackToken:  canonicalCallbackToken,
		SourceID:       "source-1",
	})
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("CompleteCallback() error = %v, want ErrBindingConflict", err)
	}
}

func TestCompleteCallbackRejectsOversizedSourceID(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	_, err := service.CompleteCallback(context.Background(), CallbackParams{
		InstallationID: store.row.ID,
		CallbackToken:  canonicalCallbackToken,
		SourceID:       strings.Repeat("s", 65),
	})
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("CompleteCallback() error = %v, want ErrInvalidResult", err)
	}
}

func TestUnbindDeletesRouterBeforeRevokingAndListIsPublic(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	store := pendingBindingStore(t, now, canonicalCallbackToken)
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	boundAt := now
	config.RouterSourceID = "source-1"
	config.BoundAt = &boundAt
	config.AccountDisplayName = "Zhang San"
	store.row.Config, err = config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store.row.Status = "active"
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	binding, err := service.Unbind(context.Background(), UnbindParams{
		WorkspaceID:    store.row.WorkspaceID,
		InstallationID: store.row.ID,
	})
	if err != nil {
		t.Fatalf("Unbind() error = %v", err)
	}
	if len(router.deleted) != 1 || router.deleted[0] != "source-1" || !store.revoked || binding.Status != "revoked" {
		t.Fatalf("delete=%#v revoked=%v binding=%#v", router.deleted, store.revoked, binding)
	}
	if store.revokeArg.AgentID != store.row.AgentID {
		t.Fatalf("revoke agent = %v, want %v", store.revokeArg.AgentID, store.row.AgentID)
	}

	listed, err := service.List(context.Background(), store.row.WorkspaceID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 || listed[0].Status != "revoked" {
		t.Fatalf("listed = %#v", listed)
	}
	encoded, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{config.RouterSourceID, config.CallbackTokenHash, config.DispatchEndpointID} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("list leaked %q: %s", secret, encoded)
		}
	}
}

func TestListClearsExpiredCallbackCredentialBeforeReturningBindings(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 20, 0, 0, time.UTC)
	store := pendingBindingStore(t, now.Add(-20*time.Minute), canonicalCallbackToken)
	router := &fakeBindingRouter{}
	service := newBindingServiceForTest(t, store, router, now)

	listed, err := service.List(context.Background(), store.row.WorkspaceID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed = %#v", listed)
	}
	if store.cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", store.cleanupCalls)
	}
	config, err := ParseDingTalkAccountConfig(store.row.Config)
	if err != nil {
		t.Fatal(err)
	}
	if config.CallbackTokenHash != "" || !config.CallbackExpiresAt.IsZero() {
		t.Fatalf("expired callback credential was retained: %#v", config)
	}
}

func newBindingServiceForTest(t *testing.T, store Store, router Router, now time.Time) *Service {
	t.Helper()
	keyring, err := ParseDispatchKeyring("v1:ERERERERERERERERERERERERERERERERERERERERERE", "v1")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, router, ServiceConfig{
		PublicBaseURL:  "https://multica.example",
		DBaseBindingURL: "https://dbase.example/bind",
		CallbackTTL:    10 * time.Minute,
		Keyring:        keyring,
		Random:         rand.Reader,
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func pendingBindingStore(t *testing.T, now time.Time, callbackToken string) *fakeBindingStore {
	t.Helper()
	endpoint := "v1_EREREREREREREREREREREQ"
	config := NewPendingDingTalkAccountConfig(
		endpoint,
		"https://multica.example/api/webhooks/agent-dispatch/"+endpoint,
		HashCallbackToken(callbackToken),
		now.Add(10*time.Minute),
	)
	raw, err := config.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return &fakeBindingStore{row: bindingRow(
		"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"11111111-1111-1111-1111-111111111111",
		"pending",
		raw,
	)}
}

func bindingRow(workspaceID, agentID, installationID, status string, config []byte) db.ChannelInstallation {
	return db.ChannelInstallation{
		ID:          mustUUIDForTest(installationID),
		WorkspaceID: mustUUIDForTest(workspaceID),
		AgentID:     mustUUIDForTest(agentID),
		ChannelType: ChannelTypeDingTalkAccount,
		Config:      append([]byte(nil), config...),
		Status:      status,
	}
}

func uuidForTest(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	value := mustUUIDForTest(raw)
	if !value.Valid {
		t.Fatalf("invalid uuid %q", raw)
	}
	return value
}

func mustUUIDForTest(raw string) pgtype.UUID {
	var value pgtype.UUID
	_ = value.Scan(raw)
	return value
}

func uuidStringForTest(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}
