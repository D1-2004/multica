package dingtalk

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// These tests cover the Upsert branching — fresh install, same-agent
// refresh, agent switch (the DingTalk device flow re-issues the SAME
// client_id on a re-scan, so switching agents must MOVE the row), and
// the conflict fences — through the installQueries seam, without a DB.

// fakeInstallQueries records calls and returns scripted rows/errors.
type fakeInstallQueries struct {
	prev    db.ChannelInstallation
	prevErr error

	upsertErr  error
	byAppIDErr error
	deleteErr  error

	upsertCalls         []db.UpsertChannelInstallationParams
	byAppIDCalls        []db.UpsertChannelInstallationByAppIDParams
	deleteCalls         []db.DeleteChannelChatSessionBindingsByInstallationParams
	reclaimAppIDCalls   []db.ReclaimDeadChannelInstallationByAppIDParams
	reclaimByAgentCalls []db.ReclaimRevokedChannelInstallationByAgentParams
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
	return db.ChannelInstallation{
		ID:              uuidForInstallTest("99999999-9999-9999-9999-999999999999"),
		WorkspaceID:     arg.WorkspaceID,
		AgentID:         arg.AgentID,
		ChannelType:     arg.ChannelType,
		Config:          arg.Config,
		InstallerUserID: arg.InstallerUserID,
		Status:          "active",
	}, nil
}

func (f *fakeInstallQueries) UpsertChannelInstallationByAppID(_ context.Context, arg db.UpsertChannelInstallationByAppIDParams) (db.ChannelInstallation, error) {
	f.byAppIDCalls = append(f.byAppIDCalls, arg)
	if f.byAppIDErr != nil {
		return db.ChannelInstallation{}, f.byAppIDErr
	}
	// The conflict-update moves the EXISTING row: keep prev's id.
	return db.ChannelInstallation{
		ID:              f.prev.ID,
		WorkspaceID:     arg.WorkspaceID,
		AgentID:         arg.AgentID,
		ChannelType:     arg.ChannelType,
		Config:          arg.Config,
		InstallerUserID: arg.InstallerUserID,
		Status:          "active",
	}, nil
}

func (f *fakeInstallQueries) DeleteChannelChatSessionBindingsByInstallation(_ context.Context, arg db.DeleteChannelChatSessionBindingsByInstallationParams) error {
	f.deleteCalls = append(f.deleteCalls, arg)
	return f.deleteErr
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
	t.Helper()
	box, err := secretbox.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	tx := &fakeTx{}
	svc, err := newInstallationService(q, &fakeTxStarter{tx: tx}, nil, box)
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
		RobotCode:       "robot-code-x",
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
