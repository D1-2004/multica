package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestInstallConfigPersistsRouterAssociationAndCutoverWithoutDeliverySecret(t *testing.T) {
	raw, err := encodeInstallConfig(Installation{
		ClientID:                 "robot-code-1",
		AppSecretEncrypted:       []byte("ciphertext"),
		RouterSourceID:           "source-1",
		RouterAgentID:            "agent-1",
		RouterRegistrationStatus: RouterRegistrationActive,
		IngressCutoverState:      IngressLegacyStream,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "delivery_secret") || strings.Contains(string(raw), "Bearer ") {
		t.Fatalf("delivery secret leaked into installation config: %s", raw)
	}
	row := db.ChannelInstallation{
		Config: raw,
		Status: "active",
	}
	got, err := installationFromRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if got.RouterSourceID != "source-1" || got.RouterAgentID != "agent-1" ||
		got.RouterRegistrationStatus != RouterRegistrationActive ||
		got.IngressCutoverState != IngressLegacyStream {
		t.Fatalf("installation = %#v", got)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != 6 {
		t.Fatalf("unexpected config fields: %#v", object)
	}
}

// These tests cover the Upsert branching — fresh install, same-agent
// refresh, agent switch (the DingTalk device flow re-issues the SAME
// client_id on a re-scan, so switching agents must MOVE the row), and
// the conflict fences — through the installQueries seam, without a DB.

// fakeInstallQueries records calls and returns scripted rows/errors.
type fakeInstallQueries struct {
	prev    db.ChannelInstallation
	prevErr error
	current db.ChannelInstallation

	upsertErr  error
	byAppIDErr error
	deleteErr  error
	stateErrAt int
	stateErr   error

	upsertCalls         []db.UpsertChannelInstallationParams
	byAppIDCalls        []db.UpsertChannelInstallationByAppIDParams
	deleteCalls         []db.DeleteChannelChatSessionBindingsByInstallationParams
	reclaimAppIDCalls   []db.ReclaimDeadChannelInstallationByAppIDParams
	reclaimByAgentCalls []db.ReclaimRevokedChannelInstallationByAgentParams
	stateCalls          []installationRouterStateParams
}

func (f *fakeInstallQueries) WithTx(pgx.Tx) installQueries { return f }

// ReclaimDeadChannelInstallationByAppID mimics the SQL predicate: a REVOKED
// prev row held by any (workspace, agent) pair OTHER than the caller's own is
// removed, so the subsequent lookup misses. Live/own rows are spared. Orphan
// reclaim (deleted workspace/agent) is not modeled — the fake has no
// existence tables.
func (f *fakeInstallQueries) ReclaimDeadChannelInstallationByAppID(_ context.Context, arg db.ReclaimDeadChannelInstallationByAppIDParams) (pgtype.UUID, error) {
	f.reclaimAppIDCalls = append(f.reclaimAppIDCalls, arg)
	if f.prevErr == nil && f.prev.Status == "revoked" &&
		!(uuidEqual(f.prev.WorkspaceID, arg.WorkspaceID) && uuidEqual(f.prev.AgentID, arg.AgentID)) {
		removed := f.prev.ID
		f.prev = db.ChannelInstallation{}
		f.prevErr = pgx.ErrNoRows
		return removed, nil
	}
	return pgtype.UUID{}, pgx.ErrNoRows
}

func (f *fakeInstallQueries) ReclaimRevokedChannelInstallationByAgent(_ context.Context, arg db.ReclaimRevokedChannelInstallationByAgentParams) (pgtype.UUID, error) {
	f.reclaimByAgentCalls = append(f.reclaimByAgentCalls, arg)
	return pgtype.UUID{}, pgx.ErrNoRows
}

func (f *fakeInstallQueries) GetChannelInstallationByAppID(context.Context, db.GetChannelInstallationByAppIDParams) (db.ChannelInstallation, error) {
	if f.prevErr != nil {
		return db.ChannelInstallation{}, f.prevErr
	}
	return f.prev, nil
}

func (f *fakeInstallQueries) UpsertChannelInstallation(_ context.Context, arg db.UpsertChannelInstallationParams) (db.ChannelInstallation, error) {
	f.upsertCalls = append(f.upsertCalls, arg)
	if f.upsertErr != nil {
		return db.ChannelInstallation{}, f.upsertErr
	}
	f.current = db.ChannelInstallation{
		ID:              uuidForInstallTest("99999999-9999-9999-9999-999999999999"),
		WorkspaceID:     arg.WorkspaceID,
		AgentID:         arg.AgentID,
		ChannelType:     arg.ChannelType,
		Config:          arg.Config,
		InstallerUserID: arg.InstallerUserID,
		Status:          "active",
	}
	return f.current, nil
}

func (f *fakeInstallQueries) UpsertChannelInstallationByAppID(_ context.Context, arg db.UpsertChannelInstallationByAppIDParams) (db.ChannelInstallation, error) {
	f.byAppIDCalls = append(f.byAppIDCalls, arg)
	if f.byAppIDErr != nil {
		return db.ChannelInstallation{}, f.byAppIDErr
	}
	// The conflict-update moves the EXISTING row: keep prev's id.
	f.current = db.ChannelInstallation{
		ID:              f.prev.ID,
		WorkspaceID:     arg.WorkspaceID,
		AgentID:         arg.AgentID,
		ChannelType:     arg.ChannelType,
		Config:          arg.Config,
		InstallerUserID: arg.InstallerUserID,
		Status:          "active",
	}
	return f.current, nil
}

func (f *fakeInstallQueries) DeleteChannelChatSessionBindingsByInstallation(_ context.Context, arg db.DeleteChannelChatSessionBindingsByInstallationParams) error {
	f.deleteCalls = append(f.deleteCalls, arg)
	return f.deleteErr
}

func (f *fakeInstallQueries) GetDingTalkInstallationForRouterReconciliation(context.Context, pgtype.UUID) (db.ChannelInstallation, error) {
	if !f.current.ID.Valid {
		if f.prevErr != nil {
			return db.ChannelInstallation{}, f.prevErr
		}
		return f.prev, nil
	}
	return f.current, nil
}

func (f *fakeInstallQueries) ListDingTalkInstallationsForRouterReconciliation(context.Context) ([]db.ChannelInstallation, error) {
	if f.current.ID.Valid && f.current.Status == "pending" {
		return []db.ChannelInstallation{f.current}, nil
	}
	return nil, nil
}

func (f *fakeInstallQueries) UpdateDingTalkInstallationRouterState(_ context.Context, arg installationRouterStateParams) (db.ChannelInstallation, error) {
	f.stateCalls = append(f.stateCalls, arg)
	if f.stateErr != nil && len(f.stateCalls) == f.stateErrAt {
		return db.ChannelInstallation{}, f.stateErr
	}
	row := f.current
	if !row.ID.Valid {
		row = f.prev
	}
	if !row.ID.Valid || row.ID != arg.ID || row.AgentID != arg.AgentID {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	current, err := installationFromRow(row)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	if row.Status != arg.ExpectedStatus ||
		string(current.RouterRegistrationStatus) != arg.ExpectedRouterRegistrationStatus ||
		string(current.IngressCutoverState) != arg.ExpectedIngressCutoverState {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	row.Config = append([]byte(nil), arg.Config...)
	row.Status = string(arg.Status)
	f.current = row
	f.prev = row
	f.prevErr = nil
	return row, nil
}

type fakeRobotRouter struct {
	registerErr error
	deleteErr   error
	onRegister  func()
	registered  []agentmessagerouter.RobotRegistration
	deleted     []string
}

func (f *fakeRobotRouter) RegisterRobot(_ context.Context, registration agentmessagerouter.RobotRegistration) (agentmessagerouter.Subscription, error) {
	f.registered = append(f.registered, registration)
	if f.onRegister != nil {
		f.onRegister()
	}
	if f.registerErr != nil {
		return agentmessagerouter.Subscription{}, f.registerErr
	}
	return agentmessagerouter.Subscription{
		SourceID:    "source-1",
		AgentID:     registration.AgentID,
		DispatchURL: registration.DispatchURL,
		Status:      "active",
	}, nil
}

func (f *fakeRobotRouter) DeleteSubscription(_ context.Context, sourceID string) error {
	f.deleted = append(f.deleted, sourceID)
	return f.deleteErr
}

type fakeRobotEndpointProvider struct {
	calls []pgtype.UUID
}

func (f *fakeRobotEndpointProvider) Ensure(_ context.Context, workspaceID, agentID, actorUserID pgtype.UUID) (agentmessagerouter.DispatchEndpoint, error) {
	f.calls = append(f.calls, agentID)
	return agentmessagerouter.DispatchEndpoint{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		ActorUserID: actorUserID,
		EndpointID:  "v1_EREREREREREREREREREREQ",
		DispatchURL: "https://multica.example/api/webhooks/agent-dispatch/v1_EREREREREREREREREREREQ",
	}, nil
}

type fakeLegacyIngressQuiescer struct {
	calls int
	err   error
}

func (f *fakeLegacyIngressQuiescer) QuiesceLegacyIngress(context.Context, pgtype.UUID) error {
	f.calls++
	return f.err
}

type fakeGatewayCallbackConfigurator struct {
	calls int
	err   error
}

func (f *fakeGatewayCallbackConfigurator) ConfigureGatewayCallback(context.Context, Installation) error {
	f.calls++
	return f.err
}

// fakeTx is a no-op pgx.Tx: embedding the interface satisfies it, and the
// only methods Upsert touches are Commit and Rollback.
type fakeTx struct {
	pgx.Tx
	committed bool
}

func (t *fakeTx) Commit(context.Context) error   { t.committed = true; return nil }
func (t *fakeTx) Rollback(context.Context) error { return nil }

type fakeTxStarter struct{ tx *fakeTx }

func (f *fakeTxStarter) Begin(context.Context) (pgx.Tx, error) { return f.tx, nil }

func uuidForInstallTest(s string) pgtype.UUID {
	u, err := util.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

var (
	instTestWorkspace = uuidForInstallTest("11111111-1111-1111-1111-111111111111")
	instTestAgentA    = uuidForInstallTest("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	instTestAgentB    = uuidForInstallTest("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	instTestInstaller = uuidForInstallTest("77777777-7777-7777-7777-777777777777")
	instTestOtherWs   = uuidForInstallTest("22222222-2222-2222-2222-222222222222")
	instTestRowID     = uuidForInstallTest("33333333-3333-3333-3333-333333333333")
)

func newUpsertServiceForTest(t *testing.T, q *fakeInstallQueries) (*InstallationService, *fakeTx) {
	return newUpsertServiceWithRouterForTest(t, q, &fakeRobotRouter{})
}

func newUpsertServiceWithRouterForTest(t *testing.T, q *fakeInstallQueries, router *fakeRobotRouter) (*InstallationService, *fakeTx) {
	return newUpsertServiceWithCutoverForTest(t, q, router, InstallationCutoverConfig{})
}

func newUpsertServiceWithCutoverForTest(
	t *testing.T,
	q *fakeInstallQueries,
	router *fakeRobotRouter,
	cutover InstallationCutoverConfig,
) (*InstallationService, *fakeTx) {
	t.Helper()
	box, err := secretbox.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	tx := &fakeTx{}
	svc, err := newInstallationService(q, &fakeTxStarter{tx: tx}, nil, box, router, &fakeRobotEndpointProvider{}, cutover)
	if err != nil {
		t.Fatalf("newInstallationService: %v", err)
	}
	return svc, tx
}

func installParamsForTest(agent pgtype.UUID) InstallationParams {
	return InstallationParams{
		WorkspaceID:     instTestWorkspace,
		AgentID:         agent,
		ClientID:        "ding-client-x",
		ClientSecret:    "s3cret",
		InstallerUserID: instTestInstaller,
	}
}

// prevRowForTest is the agent-A row holding client_id "ding-client-x".
func prevRowForTest(workspaceID pgtype.UUID) db.ChannelInstallation {
	return db.ChannelInstallation{
		ID:          instTestRowID,
		WorkspaceID: workspaceID,
		AgentID:     instTestAgentA,
		ChannelType: channelTypeDingTalk,
		Config:      []byte(`{"app_id":"ding-client-x"}`),
		Status:      "active",
	}
}

func registeredPrevRowForTest(t *testing.T, agentID pgtype.UUID) db.ChannelInstallation {
	t.Helper()
	config, err := encodeInstallConfig(Installation{
		ClientID:                 "ding-client-x",
		AppSecretEncrypted:       []byte("ciphertext"),
		RouterSourceID:           "source-old",
		RouterAgentID:            uuidString(agentID),
		RouterRegistrationStatus: RouterRegistrationActive,
		IngressCutoverState:      IngressLegacyStream,
	})
	if err != nil {
		t.Fatal(err)
	}
	row := prevRowForTest(instTestWorkspace)
	row.AgentID = agentID
	row.Config = config
	row.InstallerUserID = instTestInstaller
	return row
}

func TestUpsertRouterFailureLeavesPendingAndReconcileActivates(t *testing.T) {
	q := &fakeInstallQueries{prevErr: pgx.ErrNoRows}
	router := &fakeRobotRouter{registerErr: errors.New("router unavailable")}
	svc, _ := newUpsertServiceWithRouterForTest(t, q, router)

	_, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentA))
	if !errors.Is(err, ErrRouterUnavailable) {
		t.Fatalf("Upsert error = %v, want ErrRouterUnavailable", err)
	}
	pending, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" || pending.RouterRegistrationStatus != RouterRegistrationPending ||
		pending.IngressCutoverState != IngressLegacyStream {
		t.Fatalf("pending installation = %#v", pending)
	}

	router.registerErr = nil
	count, err := svc.ReconcilePending(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("ReconcilePending = %d, %v", count, err)
	}
	active, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != "active" || active.RouterSourceID != "source-1" ||
		active.RouterRegistrationStatus != RouterRegistrationActive {
		t.Fatalf("active installation = %#v", active)
	}
}

func TestRouterSuccessWithLocalStateFailureRemainsReplayable(t *testing.T) {
	stateErr := errors.New("database unavailable after Router registration")
	q := &fakeInstallQueries{prevErr: pgx.ErrNoRows, stateErrAt: 2, stateErr: stateErr}
	router := &fakeRobotRouter{}
	svc, _ := newUpsertServiceWithRouterForTest(t, q, router)

	_, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentA))
	if !errors.Is(err, stateErr) {
		t.Fatalf("Upsert error = %v, want state persistence failure", err)
	}
	pending, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" || pending.RouterRegistrationStatus != RouterRegistrationPending {
		t.Fatalf("installation was silently activated: %#v", pending)
	}

	q.stateErr = nil
	count, err := svc.ReconcilePending(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("ReconcilePending = %d, %v", count, err)
	}
	if len(router.registered) != 2 {
		t.Fatalf("Router registration calls = %d, want idempotent replay", len(router.registered))
	}
	active, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != "active" || active.RouterRegistrationStatus != RouterRegistrationActive {
		t.Fatalf("reconciled installation = %#v", active)
	}
}

func TestStaleRouterRegistrationIsCompensatedInsteadOfOverwritingRevoke(t *testing.T) {
	q := &fakeInstallQueries{prevErr: pgx.ErrNoRows, stateErrAt: 2, stateErr: pgx.ErrNoRows}
	router := &fakeRobotRouter{}
	router.onRegister = func() {
		current, err := installationFromRow(q.current)
		if err != nil {
			t.Fatal(err)
		}
		current.RouterRegistrationStatus = RouterRegistrationRevokePending
		config, err := encodeInstallConfig(current)
		if err != nil {
			t.Fatal(err)
		}
		q.current.Config = config
	}
	svc, _ := newUpsertServiceWithRouterForTest(t, q, router)

	if _, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentA)); err == nil {
		t.Fatal("expected stale registration state transition to fail")
	}
	current, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if current.RouterRegistrationStatus != RouterRegistrationRevokePending || current.Status != "pending" {
		t.Fatalf("stale registration overwrote revoke: %#v", current)
	}
	if len(router.deleted) != 1 || router.deleted[0] != "source-1" {
		t.Fatalf("stale Router source was not compensated: %#v", router.deleted)
	}
}

func TestUpsertSwitchAgentRevokesPreviousRouterBindingBeforeRegistration(t *testing.T) {
	q := &fakeInstallQueries{prev: registeredPrevRowForTest(t, instTestAgentA)}
	router := &fakeRobotRouter{}
	svc, _ := newUpsertServiceWithRouterForTest(t, q, router)

	inst, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentB))
	if err != nil {
		t.Fatal(err)
	}
	if len(router.deleted) != 1 || router.deleted[0] != "source-old" {
		t.Fatalf("deleted sources = %#v", router.deleted)
	}
	if len(router.registered) != 1 || router.registered[0].AgentID != uuidString(instTestAgentB) {
		t.Fatalf("registrations = %#v", router.registered)
	}
	if inst.RouterAgentID != uuidString(instTestAgentB) || inst.RouterSourceID != "source-1" || inst.Status != "active" {
		t.Fatalf("moved installation = %#v", inst)
	}
}

func TestRevokeRouterFailureStaysPendingAndReconciles(t *testing.T) {
	q := &fakeInstallQueries{current: registeredPrevRowForTest(t, instTestAgentA)}
	router := &fakeRobotRouter{deleteErr: errors.New("router unavailable")}
	svc, _ := newUpsertServiceWithRouterForTest(t, q, router)

	if err := svc.Revoke(context.Background(), q.current.ID); !errors.Is(err, ErrRouterUnavailable) {
		t.Fatalf("Revoke error = %v, want ErrRouterUnavailable", err)
	}
	pending, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" || pending.RouterRegistrationStatus != RouterRegistrationRevokePending {
		t.Fatalf("pending revoke = %#v", pending)
	}

	router.deleteErr = nil
	count, err := svc.ReconcilePending(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("ReconcilePending = %d, %v", count, err)
	}
	revoked, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Status != "revoked" || revoked.RouterRegistrationStatus != RouterRegistrationInactive {
		t.Fatalf("revoked installation = %#v", revoked)
	}
}

func TestRevokeTreatsMissingRouterSubscriptionAsIdempotentSuccess(t *testing.T) {
	q := &fakeInstallQueries{current: registeredPrevRowForTest(t, instTestAgentA)}
	router := &fakeRobotRouter{deleteErr: agentmessagerouter.ErrSubscriptionNotFound}
	svc, _ := newUpsertServiceWithRouterForTest(t, q, router)

	if err := svc.Revoke(context.Background(), q.current.ID); err != nil {
		t.Fatalf("Revoke missing subscription: %v", err)
	}
	revoked, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Status != "revoked" || revoked.RouterRegistrationStatus != RouterRegistrationInactive {
		t.Fatalf("revoked installation = %#v", revoked)
	}
}

func TestRevokePendingWithoutKnownSourceDiscoversThenRevokesRouterSource(t *testing.T) {
	config, err := encodeInstallConfig(Installation{
		ClientID:                 "ding-client-x",
		AppSecretEncrypted:       []byte("ciphertext"),
		RouterRegistrationStatus: RouterRegistrationRevokePending,
		IngressCutoverState:      IngressLegacyStream,
	})
	if err != nil {
		t.Fatal(err)
	}
	row := prevRowForTest(instTestWorkspace)
	row.Config = config
	row.Status = "pending"
	row.InstallerUserID = instTestInstaller
	q := &fakeInstallQueries{current: row}
	router := &fakeRobotRouter{}
	svc, _ := newUpsertServiceWithRouterForTest(t, q, router)

	count, err := svc.ReconcilePending(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("ReconcilePending = %d, %v", count, err)
	}
	if len(router.registered) != 1 || len(router.deleted) != 1 || router.deleted[0] != "source-1" {
		t.Fatalf("Router discovery/revoke = registered %#v deleted %#v", router.registered, router.deleted)
	}
	revoked, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Status != "revoked" || revoked.RouterRegistrationStatus != RouterRegistrationInactive {
		t.Fatalf("revoked installation = %#v", revoked)
	}
}

func TestGatewayCallbackCutoverFailsClosedWithoutExternalWiring(t *testing.T) {
	q := &fakeInstallQueries{current: registeredPrevRowForTest(t, instTestAgentA)}
	svc, _ := newUpsertServiceWithRouterForTest(t, q, &fakeRobotRouter{})

	err := svc.CutoverToGatewayCallback(context.Background(), q.current.ID)
	if !errors.Is(err, ErrGatewayCallbackCutoverUnavailable) {
		t.Fatalf("CutoverToGatewayCallback error = %v", err)
	}
	current, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "active" || current.IngressCutoverState != IngressLegacyStream {
		t.Fatalf("cutover changed installation without external wiring: %#v", current)
	}
}

func TestGatewayCallbackCutoverActivatesOnlyAfterBothExternalSteps(t *testing.T) {
	q := &fakeInstallQueries{current: registeredPrevRowForTest(t, instTestAgentA)}
	quiescer := &fakeLegacyIngressQuiescer{}
	callback := &fakeGatewayCallbackConfigurator{err: errors.New("platform callback unavailable")}
	svc, _ := newUpsertServiceWithCutoverForTest(t, q, &fakeRobotRouter{}, InstallationCutoverConfig{
		LegacyIngress: quiescer,
		Callback:      callback,
	})

	if err := svc.CutoverToGatewayCallback(context.Background(), q.current.ID); err == nil {
		t.Fatal("expected callback configuration failure")
	}
	pending, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" || pending.IngressCutoverState != IngressCallbackPending ||
		quiescer.calls != 1 || callback.calls != 1 {
		t.Fatalf("failed cutover = %#v, quiesce=%d callback=%d", pending, quiescer.calls, callback.calls)
	}

	callback.err = nil
	count, err := svc.ReconcilePending(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("ReconcilePending = %d, %v", count, err)
	}
	active, err := installationFromRow(q.current)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != "active" || active.IngressCutoverState != IngressGatewayCallback {
		t.Fatalf("completed cutover = %#v", active)
	}
	beforeQuiesce, beforeCallback := quiescer.calls, callback.calls
	if err := svc.CutoverToGatewayCallback(context.Background(), q.current.ID); err != nil {
		t.Fatalf("duplicate cutover: %v", err)
	}
	if quiescer.calls != beforeQuiesce || callback.calls != beforeCallback {
		t.Fatalf("duplicate cutover repeated external work: quiesce=%d callback=%d", quiescer.calls, callback.calls)
	}
}

func TestUpsertFreshInstallUsesAgentKey(t *testing.T) {
	q := &fakeInstallQueries{prevErr: pgx.ErrNoRows}
	svc, tx := newUpsertServiceForTest(t, q)

	inst, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentA))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(q.upsertCalls) != 1 || len(q.byAppIDCalls) != 0 {
		t.Errorf("want 1 agent-keyed upsert and 0 app-keyed, got %d/%d", len(q.upsertCalls), len(q.byAppIDCalls))
	}
	if len(q.deleteCalls) != 0 {
		t.Errorf("fresh install must not retire chat sessions, got %d calls", len(q.deleteCalls))
	}
	if !tx.committed {
		t.Error("tx not committed")
	}
	if inst.ClientID != "ding-client-x" {
		t.Errorf("ClientID = %q, want ding-client-x", inst.ClientID)
	}
}

func TestUpsertSameAgentRefreshDoesNotRetireSessions(t *testing.T) {
	q := &fakeInstallQueries{prev: prevRowForTest(instTestWorkspace)}
	svc, tx := newUpsertServiceForTest(t, q)

	if _, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentA)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(q.upsertCalls) != 1 || len(q.byAppIDCalls) != 0 {
		t.Errorf("want agent-keyed refresh, got %d/%d upsert/byAppID calls", len(q.upsertCalls), len(q.byAppIDCalls))
	}
	if len(q.deleteCalls) != 0 {
		t.Errorf("same-agent refresh must not retire chat sessions, got %d calls", len(q.deleteCalls))
	}
	if !tx.committed {
		t.Error("tx not committed")
	}
}

func TestUpsertSwitchAgentMovesRowAndRetiresSessions(t *testing.T) {
	q := &fakeInstallQueries{prev: prevRowForTest(instTestWorkspace)}
	svc, tx := newUpsertServiceForTest(t, q)

	inst, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentB))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(q.byAppIDCalls) != 1 || len(q.upsertCalls) != 0 {
		t.Fatalf("want 1 app-keyed upsert and 0 agent-keyed, got %d/%d", len(q.byAppIDCalls), len(q.upsertCalls))
	}
	if got := q.byAppIDCalls[0].AgentID; !uuidEqual(got, instTestAgentB) {
		t.Errorf("moved to agent %v, want agent B", got)
	}
	if len(q.deleteCalls) != 1 {
		t.Fatalf("want 1 chat-session retire, got %d", len(q.deleteCalls))
	}
	if !uuidEqual(q.deleteCalls[0].InstallationID, instTestRowID) {
		t.Errorf("retired bindings for %v, want the moved row %v", q.deleteCalls[0].InstallationID, instTestRowID)
	}
	if q.deleteCalls[0].ChannelType != channelTypeDingTalk {
		t.Errorf("retire channel_type = %q, want %q", q.deleteCalls[0].ChannelType, channelTypeDingTalk)
	}
	if !tx.committed {
		t.Error("tx not committed")
	}
	if !uuidEqual(inst.AgentID, instTestAgentB) {
		t.Errorf("returned AgentID = %v, want agent B", inst.AgentID)
	}
	if !uuidEqual(inst.ID, instTestRowID) {
		t.Errorf("returned ID = %v, want the moved row %v (user bindings must survive)", inst.ID, instTestRowID)
	}
}

func TestUpsertRefusesAppOwnedByAnotherWorkspace(t *testing.T) {
	q := &fakeInstallQueries{prev: prevRowForTest(instTestOtherWs)}
	svc, tx := newUpsertServiceForTest(t, q)

	_, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentB))
	if !errors.Is(err, ErrAppOwnedByAnotherWorkspace) {
		t.Fatalf("want ErrAppOwnedByAnotherWorkspace, got %v", err)
	}
	if len(q.upsertCalls)+len(q.byAppIDCalls)+len(q.deleteCalls) != 0 {
		t.Error("no writes may happen when the app belongs to another workspace")
	}
	if tx.committed {
		t.Error("tx must not commit on refusal")
	}
}

func TestUpsertSwitchAgentWorkspaceFenceRace(t *testing.T) {
	// The pre-check saw our workspace, but the fenced upsert updated zero
	// rows (another workspace claimed the app in between): same refusal.
	q := &fakeInstallQueries{prev: prevRowForTest(instTestWorkspace), byAppIDErr: pgx.ErrNoRows}
	svc, _ := newUpsertServiceForTest(t, q)

	_, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentB))
	if !errors.Is(err, ErrAppOwnedByAnotherWorkspace) {
		t.Fatalf("want ErrAppOwnedByAnotherWorkspace, got %v", err)
	}
	if len(q.deleteCalls) != 0 {
		t.Error("must not retire chat sessions when the move failed")
	}
}

func TestUpsertSwitchAgentTargetOccupied(t *testing.T) {
	// Agent B already holds a DIFFERENT dingtalk installation: the move
	// trips the (workspace_id, agent_id, channel_type) unique constraint.
	q := &fakeInstallQueries{
		prev:       prevRowForTest(instTestWorkspace),
		byAppIDErr: &pgconn.PgError{Code: pgUniqueViolation},
	}
	svc, _ := newUpsertServiceForTest(t, q)

	_, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentB))
	if !errors.Is(err, ErrAgentAlreadyConnected) {
		t.Fatalf("want ErrAgentAlreadyConnected, got %v", err)
	}
	if len(q.deleteCalls) != 0 {
		t.Error("must not retire chat sessions when the move failed")
	}
}

// revokedRowForTest is prevRowForTest flipped to 'revoked' — the leftover a
// disconnect deliberately keeps (audit) that used to pin both unique slots.
func revokedRowForTest(workspaceID pgtype.UUID) db.ChannelInstallation {
	row := prevRowForTest(workspaceID)
	row.Status = "revoked"
	return row
}

func TestUpsertRebindsRevokedBotToNewAgent(t *testing.T) {
	// THE reported bug: disconnect the bot from agent A, then bind the SAME
	// bot to agent B. The revoked row must be reclaimed so the install lands
	// as a FRESH agent-keyed upsert instead of tripping "already connected".
	q := &fakeInstallQueries{prev: revokedRowForTest(instTestWorkspace)}
	svc, tx := newUpsertServiceForTest(t, q)

	inst, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentB))
	if err != nil {
		t.Fatalf("Upsert after disconnect: %v", err)
	}
	if len(q.reclaimAppIDCalls) != 1 {
		t.Fatalf("want 1 app-id reclaim, got %d", len(q.reclaimAppIDCalls))
	}
	if got := q.reclaimAppIDCalls[0]; got.ChannelType != channelTypeDingTalk || got.AppID != "ding-client-x" {
		t.Errorf("reclaim keyed by (%q, %q), want (dingtalk, ding-client-x)", got.ChannelType, got.AppID)
	}
	// The ghost row is gone, so this is a fresh install, not a move.
	if len(q.upsertCalls) != 1 || len(q.byAppIDCalls) != 0 {
		t.Errorf("want fresh agent-keyed upsert after reclaim, got %d/%d upsert/byAppID", len(q.upsertCalls), len(q.byAppIDCalls))
	}
	if len(q.deleteCalls) != 0 {
		t.Errorf("no chat sessions to retire on a fresh install, got %d calls", len(q.deleteCalls))
	}
	if !tx.committed {
		t.Error("tx not committed")
	}
	if !uuidEqual(inst.AgentID, instTestAgentB) {
		t.Errorf("returned AgentID = %v, want agent B", inst.AgentID)
	}
}

func TestUpsertRebindsRevokedBotAcrossWorkspaces(t *testing.T) {
	// A bot disconnected in workspace X must be rebindable by workspace Y —
	// holding the app credentials proves control. The revoked ghost row used
	// to surface as ErrAppOwnedByAnotherWorkspace forever.
	q := &fakeInstallQueries{prev: revokedRowForTest(instTestOtherWs)}
	svc, tx := newUpsertServiceForTest(t, q)

	if _, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentB)); err != nil {
		t.Fatalf("Upsert across workspaces after disconnect: %v", err)
	}
	if len(q.upsertCalls) != 1 || len(q.byAppIDCalls) != 0 {
		t.Errorf("want fresh agent-keyed upsert after reclaim, got %d/%d upsert/byAppID", len(q.upsertCalls), len(q.byAppIDCalls))
	}
	if !tx.committed {
		t.Error("tx not committed")
	}
}

func TestUpsertSameAgentReinstallSparesOwnRevokedRow(t *testing.T) {
	// Re-installing the SAME agent's disconnected bot must NOT reclaim its
	// row: the agent-keyed upsert reactivates it in place, preserving the
	// installation id and every user binding hanging off it.
	q := &fakeInstallQueries{prev: revokedRowForTest(instTestWorkspace)}
	svc, _ := newUpsertServiceForTest(t, q)

	if _, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentA)); err != nil {
		t.Fatalf("same-agent reinstall: %v", err)
	}
	if q.prevErr != nil {
		t.Error("own revoked row must be spared by the reclaim, but it was removed")
	}
	if len(q.upsertCalls) != 1 || len(q.byAppIDCalls) != 0 {
		t.Errorf("want in-place agent-keyed reactivation, got %d/%d upsert/byAppID", len(q.upsertCalls), len(q.byAppIDCalls))
	}
	if len(q.reclaimByAgentCalls) != 0 {
		t.Errorf("no move happened, target-agent reclaim must not run, got %d calls", len(q.reclaimByAgentCalls))
	}
}

func TestUpsertSwitchAgentClearsTargetRevokedLeftover(t *testing.T) {
	// Moving a LIVE bot to agent B whose own bot was disconnected earlier:
	// B's revoked leftover pins the (workspace, agent, channel) slot and
	// must be cleared before the move, or the upsert aborts with a spurious
	// ErrAgentAlreadyConnected.
	q := &fakeInstallQueries{prev: prevRowForTest(instTestWorkspace)}
	svc, tx := newUpsertServiceForTest(t, q)

	if _, err := svc.Upsert(context.Background(), installParamsForTest(instTestAgentB)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(q.reclaimByAgentCalls) != 1 {
		t.Fatalf("want 1 target-agent reclaim before the move, got %d", len(q.reclaimByAgentCalls))
	}
	got := q.reclaimByAgentCalls[0]
	if !uuidEqual(got.WorkspaceID, instTestWorkspace) || !uuidEqual(got.AgentID, instTestAgentB) || got.ChannelType != channelTypeDingTalk {
		t.Errorf("reclaim keyed by (%v, %v, %q), want (workspace, agent B, dingtalk)", got.WorkspaceID, got.AgentID, got.ChannelType)
	}
	if len(q.byAppIDCalls) != 1 {
		t.Fatalf("want the move to proceed after the reclaim, got %d byAppID calls", len(q.byAppIDCalls))
	}
	if !tx.committed {
		t.Error("tx not committed")
	}
}
