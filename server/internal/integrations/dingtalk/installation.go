package dingtalk

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// InstallationParams is the input shape RegistrationService assembles
// after a successful device-flow scan-to-install. The credentials are
// supplied as plaintext — encryption happens inside
// InstallationService.Upsert via the supplied *secretbox.Box, so callers
// never see (and therefore cannot leak) the ciphertext that lands in
// the DB.
type InstallationParams struct {
	WorkspaceID     pgtype.UUID
	AgentID         pgtype.UUID
	ClientID        string
	ClientSecret    string // plaintext; encrypted at the service boundary
	InstallerUserID pgtype.UUID
	// AllowUnbound persists the "serve unbound senders as the installer"
	// mode on the installation config (see dingtalkInstallConfig).
	AllowUnbound bool
}

var (
	// ErrAppOwnedByAnotherWorkspace is returned when the scanned DingTalk
	// app (its client_id) is already installed in a DIFFERENT Multica
	// workspace — it would collide with the (channel_type, app_id) routing
	// index. Reusing it here requires disconnecting it there first.
	ErrAppOwnedByAnotherWorkspace = errors.New("dingtalk: this DingTalk app is already connected to another Multica workspace")

	// ErrAgentAlreadyConnected is returned when re-pointing the app at a
	// new agent would collide with a DIFFERENT DingTalk installation that
	// agent already holds. Disconnect the agent's existing bot first.
	ErrAgentAlreadyConnected = errors.New("dingtalk: the selected agent already has a different DingTalk bot installed")

	ErrRouterUnavailable = errors.New("dingtalk: agent message router is unavailable")

	ErrGatewayCallbackCutoverUnavailable = errors.New("dingtalk: gateway callback cutover wiring is unavailable")
)

type RobotSubscriptionRouter interface {
	RegisterRobot(context.Context, agentmessagerouter.RobotRegistration) (agentmessagerouter.Subscription, error)
	DeleteSubscription(context.Context, string) error
}

type DispatchEndpointProvider interface {
	Ensure(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID) (agentmessagerouter.DispatchEndpoint, error)
}

type LegacyIngressQuiescer interface {
	// QuiesceLegacyIngress is idempotent and returns only after the existing
	// Stream receiver can no longer consume this installation.
	QuiesceLegacyIngress(context.Context, pgtype.UUID) error
}

type GatewayCallbackConfigurator interface {
	// ConfigureGatewayCallback is idempotent and returns only after DingTalk
	// has accepted the Gateway callback for this robot.
	ConfigureGatewayCallback(context.Context, Installation) error
}

type InstallationCutoverConfig struct {
	LegacyIngress LegacyIngressQuiescer
	Callback      GatewayCallbackConfigurator
	StateChanged  func()
}

// pgUniqueViolation is the Postgres SQLSTATE for a unique-constraint violation.
const pgUniqueViolation = "23505"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

// installQueries is the slice of generated queries the Upsert flow needs.
// WithTx returns the same interface bound to a transaction so the
// move-agent path (upsert + chat-session retire) commits atomically — and
// so tests can inject a fake without a real DB (the slack.InstallService
// adapter pattern).
type installQueries interface {
	WithTx(tx pgx.Tx) installQueries
	GetChannelInstallationByAppID(ctx context.Context, arg db.GetChannelInstallationByAppIDParams) (db.ChannelInstallation, error)
	UpsertChannelInstallation(ctx context.Context, arg db.UpsertChannelInstallationParams) (db.ChannelInstallation, error)
	UpsertChannelInstallationByAppID(ctx context.Context, arg db.UpsertChannelInstallationByAppIDParams) (db.ChannelInstallation, error)
	DeleteChannelChatSessionBindingsByInstallation(ctx context.Context, arg db.DeleteChannelChatSessionBindingsByInstallationParams) error
	ReclaimDeadChannelInstallationByAppID(ctx context.Context, arg db.ReclaimDeadChannelInstallationByAppIDParams) (pgtype.UUID, error)
	ReclaimRevokedChannelInstallationByAgent(ctx context.Context, arg db.ReclaimRevokedChannelInstallationByAgentParams) (pgtype.UUID, error)
	GetDingTalkInstallationForRouterReconciliation(context.Context, pgtype.UUID) (db.ChannelInstallation, error)
	ListDingTalkInstallationsForRouterReconciliation(context.Context) ([]db.ChannelInstallation, error)
	UpdateDingTalkInstallationRouterState(context.Context, installationRouterStateParams) (db.ChannelInstallation, error)
}

type installationRouterStateParams = db.UpdateDingTalkInstallationRouterStateParams

// dbInstallQueries adapts *db.Queries to installQueries — the generated
// WithTx returns *db.Queries, so we wrap it to return the interface.
type dbInstallQueries struct{ *db.Queries }

func (q dbInstallQueries) WithTx(tx pgx.Tx) installQueries {
	return dbInstallQueries{q.Queries.WithTx(tx)}
}

// InstallationService creates, refreshes and revokes per-agent DingTalk
// installations. It owns the at-rest encryption of the client_secret so
// no caller can accidentally insert a row with plaintext credentials —
// the only path to writing a dingtalk channel_installation goes through
// here. Mirrors lark.InstallationService.
type InstallationService struct {
	queries   *ChannelStore
	q         installQueries
	tx        engine.TxStarter
	box       *secretbox.Box
	router    RobotSubscriptionRouter
	endpoints DispatchEndpointProvider
	cutover   InstallationCutoverConfig
}

// NewInstallationService binds the service to a queries handle, a tx
// starter (*pgxpool.Pool) and a secretbox keyed for at-rest encryption.
// The box MUST be non-nil; we refuse to fall back to plaintext storage
// even in test or dev configurations.
func NewInstallationService(
	queries *db.Queries,
	tx engine.TxStarter,
	box *secretbox.Box,
	router RobotSubscriptionRouter,
	endpoints DispatchEndpointProvider,
	cutover InstallationCutoverConfig,
) (*InstallationService, error) {
	if queries == nil {
		return nil, errors.New("dingtalk: InstallationService requires queries")
	}
	return newInstallationService(dbInstallQueries{queries}, tx, NewChannelStore(queries), box, router, endpoints, cutover)
}

// newInstallationService is the testable core: it takes the
// installQueries interface so tests can inject a fake (with a fake
// TxStarter) without a real DB.
func newInstallationService(
	q installQueries,
	tx engine.TxStarter,
	store *ChannelStore,
	box *secretbox.Box,
	router RobotSubscriptionRouter,
	endpoints DispatchEndpointProvider,
	cutover InstallationCutoverConfig,
) (*InstallationService, error) {
	if box == nil {
		return nil, errors.New("dingtalk: InstallationService requires a non-nil secretbox.Box")
	}
	if q == nil {
		return nil, errors.New("dingtalk: InstallationService requires queries")
	}
	if tx == nil {
		return nil, errors.New("dingtalk: InstallationService requires a tx starter")
	}
	if router == nil {
		return nil, errors.New("dingtalk: InstallationService requires a Router client")
	}
	if endpoints == nil {
		return nil, errors.New("dingtalk: InstallationService requires dispatch endpoints")
	}
	return &InstallationService{
		queries:   store,
		q:         q,
		tx:        tx,
		box:       box,
		router:    router,
		endpoints: endpoints,
		cutover:   cutover,
	}, nil
}

// Upsert creates a new installation or refreshes an existing one.
//
// DingTalk's scan-to-create flow ("一键创建") re-authorizes the org's
// EXISTING app on a re-scan, returning the same client_id the first
// install minted. The app — not the (workspace, agent) pair — is the
// natural identity, so when the incoming client_id already belongs to
// another agent in the same workspace the row MOVES to the new agent
// (contrast Slack, where a re-used app id means a paste mistake and is
// refused). User bindings survive the move (they hang off the
// installation id); chat-session bindings are retired because each
// chat_session is permanently tied to the agent it was created under.
func (s *InstallationService) Upsert(ctx context.Context, p InstallationParams) (Installation, error) {
	if err := validateInstallationParams(p); err != nil {
		return Installation{}, err
	}
	sealed, err := s.box.Seal([]byte(p.ClientSecret))
	if err != nil {
		return Installation{}, fmt.Errorf("encrypt client_secret: %w", err)
	}

	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return Installation{}, fmt.Errorf("begin install tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	// Free the (dingtalk, client_id) routing slot from any DEAD prior owner —
	// a revoked placeholder held by another agent or workspace, or an orphan
	// whose owning workspace/agent was deleted (#4810) — before the lookup, so
	// a bot that was disconnected can be rebound instead of tripping a unique
	// violation on its own ghost row. The caller's OWN revoked row is spared:
	// the agent-keyed upsert below reactivates it in place, preserving its
	// installation id and user bindings. Mirrors slack.persistInstall and
	// lark.ChannelStore.ReclaimDeadInstallationByAppID.
	if _, err := qtx.ReclaimDeadChannelInstallationByAppID(ctx, db.ReclaimDeadChannelInstallationByAppIDParams{
		ChannelType: channelTypeDingTalk,
		AppID:       p.ClientID,
		WorkspaceID: p.WorkspaceID,
		AgentID:     p.AgentID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// pgx.ErrNoRows just means nothing was dead — a no-op, not a failure.
		return Installation{}, fmt.Errorf("reclaim dead dingtalk installation: %w", err)
	}

	// Who holds this client_id today? Decides which upsert shape applies
	// and fences the cross-workspace case with a clear error instead of a
	// raw unique violation.
	prev, err := qtx.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
		ChannelType: channelTypeDingTalk,
		AppID:       p.ClientID,
	})
	prevFound := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Installation{}, fmt.Errorf("lookup installation by client_id: %w", err)
	}
	if prevFound && !uuidEqual(prev.WorkspaceID, p.WorkspaceID) {
		return Installation{}, ErrAppOwnedByAnotherWorkspace
	}
	desired := Installation{
		WorkspaceID:              p.WorkspaceID,
		AgentID:                  p.AgentID,
		ClientID:                 p.ClientID,
		AppSecretEncrypted:       sealed,
		InstallerUserID:          p.InstallerUserID,
		AllowUnbound:             p.AllowUnbound,
		RouterRegistrationStatus: RouterRegistrationPending,
		IngressCutoverState:      IngressLegacyStream,
	}
	if prevFound {
		previous, parseErr := installationFromRow(prev)
		if parseErr != nil {
			return Installation{}, fmt.Errorf("decode previous dingtalk installation: %w", parseErr)
		}
		desired.RouterSourceID = previous.RouterSourceID
		desired.RouterAgentID = previous.RouterAgentID
		if previous.IngressCutoverState != "" {
			desired.IngressCutoverState = previous.IngressCutoverState
		}
	}
	cfg, err := encodeInstallConfig(desired)
	if err != nil {
		return Installation{}, err
	}

	var row db.ChannelInstallation
	if prevFound && !uuidEqual(prev.AgentID, p.AgentID) {
		// The TARGET agent may still hold a revoked leftover from an earlier
		// disconnect. It pins the (workspace_id, agent_id, channel_type)
		// unique slot and would abort the move below with a spurious
		// "already connected" even though its occupant is dead — clear it
		// (and its dependents) first. An ACTIVE row is left alone: that is
		// the genuine ErrAgentAlreadyConnected conflict.
		if _, err := qtx.ReclaimRevokedChannelInstallationByAgent(ctx, db.ReclaimRevokedChannelInstallationByAgentParams{
			WorkspaceID: p.WorkspaceID,
			AgentID:     p.AgentID,
			ChannelType: channelTypeDingTalk,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Installation{}, fmt.Errorf("reclaim target agent's revoked installation: %w", err)
		}

		// Agent switch: conflict on the (channel_type, app_id) routing
		// index moves the existing row to the new agent and reactivates
		// it. The query's workspace fence turns a cross-workspace race
		// into zero rows.
		row, err = qtx.UpsertChannelInstallationByAppID(ctx, db.UpsertChannelInstallationByAppIDParams{
			WorkspaceID:     p.WorkspaceID,
			AgentID:         p.AgentID,
			ChannelType:     channelTypeDingTalk,
			Config:          cfg,
			InstallerUserID: p.InstallerUserID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Installation{}, ErrAppOwnedByAnotherWorkspace
			}
			if isUniqueViolation(err) {
				// Moving would collide with the target agent's existing
				// (workspace_id, agent_id, channel_type) row.
				return Installation{}, ErrAgentAlreadyConnected
			}
			return Installation{}, fmt.Errorf("move installation to agent: %w", err)
		}
		// Existing chat sessions are pinned to the OLD agent; retire
		// their bindings so the next inbound message opens a fresh
		// session under the new one. chat_session rows stay for history.
		if err := qtx.DeleteChannelChatSessionBindingsByInstallation(ctx, db.DeleteChannelChatSessionBindingsByInstallationParams{
			InstallationID: row.ID,
			ChannelType:    channelTypeDingTalk,
		}); err != nil {
			return Installation{}, fmt.Errorf("retire chat session bindings: %w", err)
		}
	} else {
		// Fresh install, or a re-scan for the same agent (including the
		// recovery path where the org-side app was deleted and the scan
		// minted a NEW client_id: the (workspace, agent, channel) conflict
		// key replaces that agent's config in place).
		row, err = qtx.UpsertChannelInstallation(ctx, db.UpsertChannelInstallationParams{
			WorkspaceID:     p.WorkspaceID,
			AgentID:         p.AgentID,
			ChannelType:     channelTypeDingTalk,
			Config:          cfg,
			InstallerUserID: p.InstallerUserID,
		})
		if err != nil {
			if isUniqueViolation(err) {
				// Lost a race on the (channel_type, app_id) routing index.
				return Installation{}, ErrAppOwnedByAnotherWorkspace
			}
			return Installation{}, fmt.Errorf("upsert installation: %w", err)
		}
	}

	row, err = qtx.UpdateDingTalkInstallationRouterState(ctx, installationRouterStateParams{
		Config:                           cfg,
		Status:                           string(InstallationPending),
		ID:                               row.ID,
		WorkspaceID:                      row.WorkspaceID,
		AgentID:                          row.AgentID,
		ClientID:                         p.ClientID,
		ExpectedStatus:                   string(InstallationActive),
		ExpectedRouterRegistrationStatus: string(desired.RouterRegistrationStatus),
		ExpectedIngressCutoverState:       string(desired.IngressCutoverState),
	})
	if err != nil {
		return Installation{}, fmt.Errorf("stage dingtalk Router registration: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Installation{}, fmt.Errorf("commit install tx: %w", err)
	}
	s.notifyStateChanged()
	return s.reconcileRow(ctx, row)
}

func (s *InstallationService) Revoke(ctx context.Context, id pgtype.UUID) error {
	row, err := s.q.GetDingTalkInstallationForRouterReconciliation(ctx, id)
	if err != nil {
		return err
	}
	inst, err := installationFromRow(row)
	if err != nil {
		return err
	}
	if inst.Status == string(InstallationRevoked) {
		return nil
	}
	if inst.RouterRegistrationStatus != RouterRegistrationRevokePending {
		expectedRouterStatus := inst.RouterRegistrationStatus
		expectedIngressState := inst.IngressCutoverState
		inst.RouterRegistrationStatus = RouterRegistrationRevokePending
		staged, transitionErr := s.updateState(
			ctx,
			inst,
			InstallationStatus(inst.Status),
			expectedRouterStatus,
			expectedIngressState,
			InstallationPending,
		)
		if transitionErr != nil {
			return transitionErr
		}
		inst = staged
		s.notifyStateChanged()
	}
	_, err = s.reconcileInstallation(ctx, inst)
	return err
}

func (s *InstallationService) ReconcilePending(ctx context.Context) (int, error) {
	if s == nil || s.q == nil || s.router == nil || s.endpoints == nil {
		return 0, errors.New("dingtalk: InstallationService is not configured")
	}
	rows, err := s.q.ListDingTalkInstallationsForRouterReconciliation(ctx)
	if err != nil {
		return 0, fmt.Errorf("list dingtalk Router reconciliation work: %w", err)
	}
	completed := 0
	var reconciliationErrors []error
	for _, row := range rows {
		inst, parseErr := installationFromRow(row)
		if parseErr != nil {
			reconciliationErrors = append(reconciliationErrors,
				fmt.Errorf("reconcile dingtalk installation %s: %w", uuidString(row.ID), parseErr))
			continue
		}
		if _, reconcileErr := s.reconcileInstallation(ctx, inst); reconcileErr != nil {
			reconciliationErrors = append(reconciliationErrors,
				fmt.Errorf("reconcile dingtalk installation %s: %w", uuidString(row.ID), reconcileErr))
			continue
		}
		completed++
	}
	return completed, errors.Join(reconciliationErrors...)
}

func (s *InstallationService) CutoverToGatewayCallback(ctx context.Context, id pgtype.UUID) error {
	if s == nil || s.cutover.LegacyIngress == nil || s.cutover.Callback == nil {
		return ErrGatewayCallbackCutoverUnavailable
	}
	row, err := s.q.GetDingTalkInstallationForRouterReconciliation(ctx, id)
	if err != nil {
		return err
	}
	inst, err := installationFromRow(row)
	if err != nil {
		return err
	}
	if inst.Status == string(InstallationActive) && inst.IngressCutoverState == IngressGatewayCallback {
		return nil
	}
	if inst.RouterRegistrationStatus != RouterRegistrationActive ||
		strings.TrimSpace(inst.RouterSourceID) == "" {
		return errors.New("dingtalk: Router registration must be active before callback cutover")
	}
	if inst.IngressCutoverState == IngressLegacyStream && inst.Status == string(InstallationActive) {
		expectedRouterStatus := inst.RouterRegistrationStatus
		expectedIngressState := inst.IngressCutoverState
		inst.IngressCutoverState = IngressCallbackPending
		inst, err = s.updateState(
			ctx,
			inst,
			InstallationActive,
			expectedRouterStatus,
			expectedIngressState,
			InstallationPending,
		)
		if err != nil {
			return err
		}
		s.notifyStateChanged()
	}
	if inst.Status != string(InstallationPending) || inst.IngressCutoverState != IngressCallbackPending {
		return errors.New("dingtalk: callback cutover state is invalid")
	}
	_, err = s.continueGatewayCallbackCutover(ctx, inst)
	return err
}

func (s *InstallationService) reconcileRow(ctx context.Context, row db.ChannelInstallation) (Installation, error) {
	inst, err := installationFromRow(row)
	if err != nil {
		return Installation{}, err
	}
	return s.reconcileInstallation(ctx, inst)
}

func (s *InstallationService) reconcileInstallation(ctx context.Context, inst Installation) (Installation, error) {
	switch {
	case inst.RouterRegistrationStatus == RouterRegistrationPending:
		return s.registerRouterSource(ctx, inst)
	case inst.RouterRegistrationStatus == RouterRegistrationRevokePending:
		return s.revokeRouterSource(ctx, inst)
	case inst.RouterRegistrationStatus == RouterRegistrationActive &&
		inst.IngressCutoverState == IngressCallbackPending:
		return s.continueGatewayCallbackCutover(ctx, inst)
	case inst.Status == string(InstallationActive) &&
		inst.RouterRegistrationStatus == RouterRegistrationActive:
		return inst, nil
	case inst.Status == string(InstallationRevoked) &&
		inst.RouterRegistrationStatus == RouterRegistrationInactive:
		return inst, nil
	default:
		return Installation{}, errors.New("dingtalk: installation reconciliation state is invalid")
	}
}

func (s *InstallationService) registerRouterSource(ctx context.Context, inst Installation) (Installation, error) {
	if inst.Status != string(InstallationPending) {
		return Installation{}, errors.New("dingtalk: Router registration requires pending installation")
	}
	if (inst.RouterSourceID == "") != (inst.RouterAgentID == "") {
		return Installation{}, errors.New("dingtalk: previous Router association is incomplete")
	}
	expectedRouterStatus := inst.RouterRegistrationStatus
	expectedIngressState := inst.IngressCutoverState
	endpoint, err := s.endpoints.Ensure(ctx, inst.WorkspaceID, inst.AgentID, inst.InstallerUserID)
	if err != nil {
		return Installation{}, fmt.Errorf("%w: dispatch endpoint: %v", ErrRouterUnavailable, err)
	}
	desiredAgentID := uuidString(inst.AgentID)
	if inst.RouterSourceID != "" && inst.RouterAgentID != desiredAgentID {
		if err := s.router.DeleteSubscription(ctx, inst.RouterSourceID); err != nil &&
			!errors.Is(err, agentmessagerouter.ErrSubscriptionNotFound) {
			return Installation{}, fmt.Errorf("%w: revoke previous source: %v", ErrRouterUnavailable, err)
		}
	}
	subscription, err := s.router.RegisterRobot(ctx, agentmessagerouter.RobotRegistration{
		RobotCode:   inst.ClientID,
		AgentID:     desiredAgentID,
		DispatchURL: endpoint.DispatchURL,
	})
	if err != nil {
		return Installation{}, fmt.Errorf("%w: register robot source: %v", ErrRouterUnavailable, err)
	}
	if strings.TrimSpace(subscription.SourceID) == "" || subscription.SourceID != strings.TrimSpace(subscription.SourceID) ||
		subscription.AgentID != desiredAgentID || subscription.DispatchURL != endpoint.DispatchURL ||
		subscription.Status != "active" {
		return Installation{}, fmt.Errorf("%w: registration response is invalid", ErrRouterUnavailable)
	}
	inst.RouterSourceID = subscription.SourceID
	inst.RouterAgentID = desiredAgentID
	inst.RouterRegistrationStatus = RouterRegistrationActive
	if inst.IngressCutoverState == "" {
		inst.IngressCutoverState = IngressLegacyStream
	}
	active, err := s.updateState(
		ctx,
		inst,
		InstallationPending,
		expectedRouterStatus,
		expectedIngressState,
		InstallationActive,
	)
	if err != nil {
		cleanupErr := s.router.DeleteSubscription(ctx, subscription.SourceID)
		if cleanupErr != nil && !errors.Is(cleanupErr, agentmessagerouter.ErrSubscriptionNotFound) {
			return Installation{}, errors.Join(
				err,
				fmt.Errorf("%w: compensate stale robot registration: %v", ErrRouterUnavailable, cleanupErr),
			)
		}
		return Installation{}, err
	}
	s.notifyStateChanged()
	return active, nil
}

func (s *InstallationService) revokeRouterSource(ctx context.Context, inst Installation) (Installation, error) {
	if inst.Status != string(InstallationPending) {
		return Installation{}, errors.New("dingtalk: Router revoke requires pending installation")
	}
	if (inst.RouterSourceID == "") != (inst.RouterAgentID == "") {
		return Installation{}, errors.New("dingtalk: Router association is incomplete")
	}
	expectedRouterStatus := inst.RouterRegistrationStatus
	expectedIngressState := inst.IngressCutoverState
	if inst.RouterSourceID == "" {
		endpoint, err := s.endpoints.Ensure(ctx, inst.WorkspaceID, inst.AgentID, inst.InstallerUserID)
		if err != nil {
			return Installation{}, fmt.Errorf("%w: dispatch endpoint for revoke: %v", ErrRouterUnavailable, err)
		}
		desiredAgentID := uuidString(inst.AgentID)
		subscription, err := s.router.RegisterRobot(ctx, agentmessagerouter.RobotRegistration{
			RobotCode:   inst.ClientID,
			AgentID:     desiredAgentID,
			DispatchURL: endpoint.DispatchURL,
		})
		if err != nil {
			return Installation{}, fmt.Errorf("%w: resolve robot source for revoke: %v", ErrRouterUnavailable, err)
		}
		if strings.TrimSpace(subscription.SourceID) == "" ||
			subscription.SourceID != strings.TrimSpace(subscription.SourceID) ||
			subscription.AgentID != desiredAgentID || subscription.DispatchURL != endpoint.DispatchURL ||
			subscription.Status != "active" {
			return Installation{}, fmt.Errorf("%w: revoke source resolution response is invalid", ErrRouterUnavailable)
		}
		inst.RouterSourceID = subscription.SourceID
		inst.RouterAgentID = desiredAgentID
		persisted, persistErr := s.updateState(
			ctx,
			inst,
			InstallationPending,
			expectedRouterStatus,
			expectedIngressState,
			InstallationPending,
		)
		if persistErr != nil {
			cleanupErr := s.router.DeleteSubscription(ctx, subscription.SourceID)
			if cleanupErr != nil && !errors.Is(cleanupErr, agentmessagerouter.ErrSubscriptionNotFound) {
				return Installation{}, errors.Join(
					persistErr,
					fmt.Errorf("%w: compensate revoke source resolution: %v", ErrRouterUnavailable, cleanupErr),
				)
			}
			return Installation{}, persistErr
		}
		inst = persisted
		expectedRouterStatus = inst.RouterRegistrationStatus
		expectedIngressState = inst.IngressCutoverState
	}
	if inst.RouterSourceID != "" {
		if err := s.router.DeleteSubscription(ctx, inst.RouterSourceID); err != nil &&
			!errors.Is(err, agentmessagerouter.ErrSubscriptionNotFound) {
			return Installation{}, fmt.Errorf("%w: revoke robot source: %v", ErrRouterUnavailable, err)
		}
	}
	inst.RouterRegistrationStatus = RouterRegistrationInactive
	inst.IngressCutoverState = IngressLegacyStream
	revoked, err := s.updateState(
		ctx,
		inst,
		InstallationPending,
		expectedRouterStatus,
		expectedIngressState,
		InstallationRevoked,
	)
	if err != nil {
		return Installation{}, err
	}
	s.notifyStateChanged()
	return revoked, nil
}

func (s *InstallationService) continueGatewayCallbackCutover(ctx context.Context, inst Installation) (Installation, error) {
	if s.cutover.LegacyIngress == nil || s.cutover.Callback == nil {
		return Installation{}, ErrGatewayCallbackCutoverUnavailable
	}
	if inst.Status != string(InstallationPending) || inst.IngressCutoverState != IngressCallbackPending ||
		inst.RouterRegistrationStatus != RouterRegistrationActive {
		return Installation{}, errors.New("dingtalk: callback cutover state is invalid")
	}
	expectedRouterStatus := inst.RouterRegistrationStatus
	expectedIngressState := inst.IngressCutoverState
	if err := s.cutover.LegacyIngress.QuiesceLegacyIngress(ctx, inst.ID); err != nil {
		return Installation{}, fmt.Errorf("dingtalk: quiesce legacy Stream ingress: %w", err)
	}
	if err := s.cutover.Callback.ConfigureGatewayCallback(ctx, inst); err != nil {
		return Installation{}, fmt.Errorf("dingtalk: configure Gateway callback: %w", err)
	}
	inst.IngressCutoverState = IngressGatewayCallback
	active, err := s.updateState(
		ctx,
		inst,
		InstallationPending,
		expectedRouterStatus,
		expectedIngressState,
		InstallationActive,
	)
	if err != nil {
		return Installation{}, err
	}
	s.notifyStateChanged()
	return active, nil
}

func (s *InstallationService) updateState(
	ctx context.Context,
	inst Installation,
	expectedStatus InstallationStatus,
	expectedRouterStatus RouterRegistrationStatus,
	expectedIngressState IngressCutoverState,
	nextStatus InstallationStatus,
) (Installation, error) {
	config, err := encodeInstallConfig(inst)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.UpdateDingTalkInstallationRouterState(ctx, installationRouterStateParams{
		Config:                           config,
		Status:                           string(nextStatus),
		ID:                               inst.ID,
		WorkspaceID:                      inst.WorkspaceID,
		AgentID:                          inst.AgentID,
		ClientID:                         inst.ClientID,
		ExpectedStatus:                   string(expectedStatus),
		ExpectedRouterRegistrationStatus: string(expectedRouterStatus),
		ExpectedIngressCutoverState:       string(expectedIngressState),
	})
	if err == nil {
		return installationFromRow(row)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Installation{}, err
	}
	currentRow, loadErr := s.q.GetDingTalkInstallationForRouterReconciliation(ctx, inst.ID)
	if loadErr != nil {
		return Installation{}, err
	}
	current, loadErr := installationFromRow(currentRow)
	if loadErr != nil {
		return Installation{}, loadErr
	}
	if current.Status == string(nextStatus) && current.AgentID == inst.AgentID &&
		current.ClientID == inst.ClientID && current.RouterSourceID == inst.RouterSourceID &&
		current.RouterAgentID == inst.RouterAgentID &&
		current.RouterRegistrationStatus == inst.RouterRegistrationStatus &&
		current.IngressCutoverState == inst.IngressCutoverState {
		return current, nil
	}
	return Installation{}, err
}

func (s *InstallationService) notifyStateChanged() {
	if s != nil && s.cutover.StateChanged != nil {
		s.cutover.StateChanged()
	}
}

// DecryptClientSecret returns the plaintext client_secret for the
// supplied installation row. Reserved for the future inbound transport
// (DingTalk Stream Mode) that must authenticate on behalf of an
// installation; the plaintext value must never round-trip through an
// HTTP response.
func (s *InstallationService) DecryptClientSecret(inst Installation) (string, error) {
	plain, err := s.box.Open(inst.AppSecretEncrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt client_secret: %w", err)
	}
	return string(plain), nil
}

// GetInWorkspace is the workspace-scoped lookup helper, so a forged
// installation_id from a different workspace returns NotFound instead
// of leaking existence.
func (s *InstallationService) GetInWorkspace(ctx context.Context, id, workspaceID pgtype.UUID) (Installation, error) {
	row, err := s.queries.GetDingTalkInstallationInWorkspace(ctx, id, workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Installation{}, ErrInstallationNotFound
		}
		return Installation{}, err
	}
	return row, nil
}

// ListByWorkspace returns every installation rooted at the workspace,
// active and revoked, oldest first.
func (s *InstallationService) ListByWorkspace(ctx context.Context, workspaceID pgtype.UUID) ([]Installation, error) {
	return s.queries.ListDingTalkInstallationsByWorkspace(ctx, workspaceID)
}

// ErrInstallationNotFound surfaces "no row matches in this workspace" —
// used by the HTTP layer to return 404. Distinct from a plain
// pgx.ErrNoRows so handlers do not need to import pgx.
var ErrInstallationNotFound = errors.New("dingtalk installation not found")

func validateInstallationParams(p InstallationParams) error {
	switch {
	case !p.WorkspaceID.Valid:
		return errors.New("workspace_id is required")
	case !p.AgentID.Valid:
		return errors.New("agent_id is required")
	case !p.InstallerUserID.Valid:
		return errors.New("installer_user_id is required")
	case p.ClientID == "":
		return errors.New("client_id is required")
	case p.ClientSecret == "":
		return errors.New("client_secret is required")
	}
	return nil
}
