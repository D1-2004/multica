package dingtalk

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RegistrationSessionStatus is the discriminated state a `begin`
// session lives in. The HTTP status endpoint serializes the underlying
// string verbatim so the frontend can pattern-match without parsing
// prose. Values mirror the lark install flow: pending | success | error.
type RegistrationSessionStatus string

const (
	// RegistrationStatusPending means the QR has been minted and the
	// background goroutine is still polling DingTalk.
	RegistrationStatusPending RegistrationSessionStatus = "pending"

	// RegistrationStatusSuccess means the device flow returned
	// credentials AND the installation row committed. `installation_id`
	// is populated.
	RegistrationStatusSuccess RegistrationSessionStatus = "success"

	// RegistrationStatusError means the session reached a terminal
	// failure. `error_reason` is set to a stable code so the frontend
	// can render the right copy without parsing `error_message`.
	RegistrationStatusError RegistrationSessionStatus = "error"
)

// Reason codes the service stores on a failed session. Stable strings
// so the frontend can switch on them without parsing prose.
const (
	RegistrationReasonExpired                = "expired"
	RegistrationReasonInstallFailed          = "install_failed"
	RegistrationReasonProtocol               = "dingtalk_protocol_error"
	RegistrationReasonCredentialsCheckFailed = "credentials_check_failed"
	RegistrationReasonInstallationConflict   = "installation_conflict"
	RegistrationReasonInternalError          = "internal_error"
	RegistrationReasonSuperseded             = "superseded"
)

// AppCredentialVerifier validates a freshly minted (client_id,
// client_secret) pair before the service commits it. The production
// implementation exchanges the pair for an app access token — the
// cheapest call that proves the created app is alive. Nil disables the
// check (tests, offline deployments).
type AppCredentialVerifier interface {
	VerifyAppCredentials(ctx context.Context, clientID, clientSecret string) error
}

type HTTPCallbackEndpoint struct {
	EndpointID  string
	DispatchURL string
}

type HTTPCallbackRouter interface {
	PrepareEndpoint(ctx context.Context, workspaceID, agentID, actorUserID pgtype.UUID) (HTTPCallbackEndpoint, error)
	Register(ctx context.Context, endpoint HTTPCallbackEndpoint, agentID pgtype.UUID, robotCode string) (sourceID string, err error)
}

// RegistrationServiceConfig configures the service.
type RegistrationServiceConfig struct {
	// SessionTTL caps how long a successful or errored session stays in
	// the in-process cache before GC. Default 30 minutes — long enough
	// for the frontend to fetch the final status after the dialog
	// closes, short enough that abandoned sessions do not pin memory
	// forever.
	SessionTTL time.Duration

	// Now is overridable for deterministic expiry-bound tests.
	Now func() time.Time

	// Logger is used for protocol-level warnings. Nil uses slog.Default().
	Logger *slog.Logger

	// sessionStore overrides the DB-backed store. In-package tests inject
	// the in-memory implementation; production leaves this nil so the
	// constructor wires the DB store, without which a multi-replica
	// deployment 404s every cross-pod status poll.
	sessionStore sessionStore
}

func (c RegistrationServiceConfig) withDefaults() RegistrationServiceConfig {
	if c.SessionTTL == 0 {
		c.SessionTTL = 30 * time.Minute
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// RegistrationService owns the device-flow install lifecycle. It is the
// one place that:
//
//  1. opens a new device-flow session against DingTalk (Begin),
//  2. tracks the session's polling state in-process,
//  3. runs the background polling goroutine,
//  4. on success, optionally verifies the credentials, then writes the
//     dingtalk channel_installation row.
//
// Unlike the lark flow there is no installer identity binding: the
// DingTalk poll success payload does not say who scanned, so binding a
// DingTalk account to a Multica user happens later through the
// channel binding-token flow once the inbound transport lands.
//
// Session state is persisted (migration 181) rather than kept in
// process: this deployment runs several replicas, and a status poll may
// be served by any of them, not just the one that opened the session.
type RegistrationService struct {
	cfg                RegistrationServiceConfig
	client             *RegistrationClient
	installs           *InstallationService
	verifier           AppCredentialVerifier
	authQueries        authQueriesAdapter
	httpCallbackRouter HTTPCallbackRouter

	// bus is optional. When wired (SetEventBus), a successful install
	// publishes dingtalk_installation:created the moment the row
	// commits, so every workspace client refreshes its connection badge
	// without waiting for a browser to poll the status endpoint. Nil is
	// valid — install still works, it just won't push the WS frame.
	bus *events.Bus

	// store holds the observable session state. It is DB-backed in
	// production: the browser's status polls are load-balanced across
	// replicas, so a session that lived only in the memory of the pod
	// that served /install/begin 404s on every poll that lands
	// elsewhere. The device code and the polling goroutine still live
	// only on the originating pod — see registration_store.go.
	store sessionStore
}

// authQueriesAdapter is the minimal lookup surface the service needs
// before kicking off a session: agent ↔ workspace ownership validation.
// Kept as an interface so tests can drop in a stub instead of a real
// *db.Queries + Postgres fixture.
type authQueriesAdapter interface {
	GetAgentInWorkspace(ctx context.Context, params db.GetAgentInWorkspaceParams) (db.Agent, error)
}

// NewRegistrationService wires the device-flow client and the DB write
// path. verifier may be nil (credential check skipped); every other
// dependency missing surfaces as a constructor error so a silent
// half-init at startup cannot leave the install button returning 500s
// at runtime.
func NewRegistrationService(
	cfg RegistrationServiceConfig,
	client *RegistrationClient,
	installs *InstallationService,
	queries *db.Queries,
	verifier AppCredentialVerifier,
) (*RegistrationService, error) {
	if client == nil {
		return nil, errors.New("dingtalk registration: RegistrationClient is required")
	}
	if installs == nil {
		return nil, errors.New("dingtalk registration: InstallationService is required")
	}
	if queries == nil {
		return nil, errors.New("dingtalk registration: queries is required")
	}
	resolved := cfg.withDefaults()
	store := resolved.sessionStore
	if store == nil {
		store = &dbSessionStore{q: queries}
	}
	return &RegistrationService{
		cfg:         resolved,
		client:      client,
		installs:    installs,
		verifier:    verifier,
		authQueries: queries,
		store:       store,
	}, nil
}

// SetEventBus wires the optional event bus AFTER construction so the
// constructor-validation cases stay untouched and the bus remains
// nil-safe.
func (s *RegistrationService) SetEventBus(bus *events.Bus) {
	s.bus = bus
}

func (s *RegistrationService) SetHTTPCallbackRouter(router HTTPCallbackRouter) {
	s.httpCallbackRouter = router
}

func (s *RegistrationService) RetryHTTPCallbackRouter(
	ctx context.Context,
	workspaceID, installationID pgtype.UUID,
) (Installation, error) {
	inst, err := s.installs.GetInWorkspace(ctx, installationID, workspaceID)
	if err != nil {
		return Installation{}, err
	}
	if inst.Status != string(InstallationActive) || inst.TransportMode != TransportModeHTTPCallback ||
		strings.TrimSpace(inst.RobotCode) == "" {
		return Installation{}, errors.New("dingtalk HTTP callback installation is not retryable")
	}
	if s.httpCallbackRouter == nil {
		return Installation{}, errors.New("agent message router is not configured")
	}
	endpoint, err := s.httpCallbackRouter.PrepareEndpoint(ctx, inst.WorkspaceID, inst.AgentID, inst.InstallerUserID)
	if err != nil || (inst.DispatchEndpointID != "" && endpoint.EndpointID != inst.DispatchEndpointID) {
		if err == nil {
			err = errors.New("dispatch endpoint changed")
		}
		updated, _ := s.installs.UpdateRouterState(ctx, inst, "", "failed", err.Error())
		return updated, err
	}
	inst.DispatchEndpointID = endpoint.EndpointID
	sourceID, err := s.httpCallbackRouter.Register(ctx, endpoint, inst.AgentID, inst.RobotCode)
	if err != nil {
		updated, _ := s.installs.UpdateRouterState(ctx, inst, "", "failed", err.Error())
		return updated, err
	}
	return s.installs.UpdateRouterState(ctx, inst, sourceID, "registered", "")
}

// publishInstalled emits dingtalk_installation:created on the optional
// bus. Both install and revoke broadcast to the whole workspace via the
// SubscribeAll fanout; the frontend invalidates the dingtalk
// installations query on the dingtalk_installation prefix. Nil-safe.
func (s *RegistrationService) publishInstalled(workspaceID, installationID pgtype.UUID) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{
		Type:        protocol.EventDingTalkInstallationCreated,
		WorkspaceID: uuidString(workspaceID),
		ActorType:   "system",
		Payload:     map[string]any{"installation_id": uuidString(installationID)},
	})
}

// registrationSession is the in-memory state for one in-flight install.
type registrationSession struct {
	id            string
	workspaceID   pgtype.UUID
	agentID       pgtype.UUID
	initiatorID   pgtype.UUID
	allowUnbound  bool
	transportMode TransportMode
	generation    int64

	deviceCode string
	qrCodeURL  string
	interval   time.Duration
	expiresAt  time.Time

	mu             sync.Mutex
	status         RegistrationSessionStatus
	installationID pgtype.UUID
	errorReason    string
	errorMessage   string
	gcAfter        time.Time
}

func (s *registrationSession) snapshot() RegistrationSessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RegistrationSessionState{
		ID:             s.id,
		Status:         s.status,
		InstallationID: s.installationID,
		ErrorReason:    s.errorReason,
		ErrorMessage:   s.errorMessage,
	}
}

func (s *registrationSession) markSuccess(installationID pgtype.UUID, gcAfter time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = RegistrationStatusSuccess
	s.installationID = installationID
	s.gcAfter = gcAfter
}

func (s *registrationSession) markError(reason, msg string, gcAfter time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Idempotent: if a parallel goroutine already terminated the
	// session, don't clobber the first reason — the user already saw it.
	if s.status != RegistrationStatusPending {
		return
	}
	s.status = RegistrationStatusError
	s.errorReason = reason
	s.errorMessage = msg
	s.gcAfter = gcAfter
}

// RegistrationSessionState is the read-only snapshot the handler
// serializes to the frontend.
type RegistrationSessionState struct {
	ID                 string
	Status             RegistrationSessionStatus
	InstallationID     pgtype.UUID
	RegistrationStatus string
	ErrorReason        string
	ErrorMessage       string
}

// BeginInstallParams is the trusted input from the handler — the
// workspace, agent, and initiating user have already been authenticated
// and authorized at the router (admin role on the workspace; agent
// belongs to the workspace).
type BeginInstallParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	InitiatorID pgtype.UUID
	// AllowUnbound requests the "serve unbound senders as the installer"
	// mode; persisted on the installation when the scan completes.
	AllowUnbound bool
	// TransportMode defaults to STREAM for backward compatibility.
	TransportMode TransportMode
}

// BeginInstallResult is the public payload the handler echoes to the
// frontend. The session_id is the opaque handle the frontend uses to
// poll status; we deliberately do NOT echo the device_code (DingTalk
// would honor a poll from anywhere if it leaked).
type BeginInstallResult struct {
	SessionID           string
	QRCodeURL           string
	ExpiresInSeconds    int
	PollIntervalSeconds int
}

type RegistrationCapabilities struct {
	HTTPCallbackAvailable bool
	HTTPCallbackReason    string
}

func (s *RegistrationService) Capabilities() RegistrationCapabilities {
	result := RegistrationCapabilities{}
	if err := validateOutgoingURL(s.client.cfg.OutgoingURL); err != nil {
		result.HTTPCallbackReason = "callback_url_not_configured"
		return result
	}
	if s.httpCallbackRouter == nil {
		result.HTTPCallbackReason = "agent_message_router_not_configured"
		return result
	}
	result.HTTPCallbackAvailable = true
	return result
}

// BeginInstall opens a fresh device-flow session and kicks off the
// background polling goroutine. The returned payload feeds the QR-code
// dialog on the frontend; the polling goroutine runs until success,
// terminal failure, or device_code expiry.
func (s *RegistrationService) BeginInstall(ctx context.Context, p BeginInstallParams) (BeginInstallResult, error) {
	if !p.WorkspaceID.Valid || !p.AgentID.Valid || !p.InitiatorID.Valid {
		return BeginInstallResult{}, errors.New("dingtalk registration: workspace, agent, and initiator are required")
	}
	// Agent ownership pre-check — without this, a workspace admin could
	// open an install session against another workspace's agent by
	// guessing the UUID. The handler does the same check; doing it here
	// too keeps the service self-defending. (Unlike Lark's flow there
	// is no name pre-fill: the DingTalk protocol carries no app-name
	// field, so the agent row is only consulted for ownership.)
	if _, err := s.authQueries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          p.AgentID,
		WorkspaceID: p.WorkspaceID,
	}); err != nil {
		return BeginInstallResult{}, fmt.Errorf("dingtalk registration: agent not in workspace: %w", err)
	}

	requestedMode := p.TransportMode
	if strings.TrimSpace(string(requestedMode)) == "" {
		requestedMode = TransportModeStream
	}
	mode, err := normalizeTransportMode(requestedMode)
	if err != nil {
		return BeginInstallResult{}, err
	}
	if mode == TransportModeHTTPCallback && !s.Capabilities().HTTPCallbackAvailable {
		return BeginInstallResult{}, &RegistrationError{
			Code: "http_callback_unavailable", Description: "HTTP callback transport is not fully configured",
		}
	}
	begin, err := s.client.BeginWithTransport(ctx, mode)
	if err != nil {
		return BeginInstallResult{}, fmt.Errorf("dingtalk registration: begin: %w", err)
	}

	now := s.cfg.Now()
	sessionID, err := randomSessionID()
	if err != nil {
		return BeginInstallResult{}, fmt.Errorf("dingtalk registration: mint session id: %w", err)
	}
	sess := &registrationSession{
		id:            sessionID,
		workspaceID:   p.WorkspaceID,
		agentID:       p.AgentID,
		initiatorID:   p.InitiatorID,
		allowUnbound:  p.AllowUnbound,
		transportMode: mode,
		deviceCode:    begin.DeviceCode,
		qrCodeURL:     begin.QRCodeURL,
		interval:      begin.Interval,
		expiresAt:     now.Add(begin.ExpiresIn),
		status:        RegistrationStatusPending,
	}
	generation, err := s.store.Create(ctx, sessionRecord{
		ID:            sess.id,
		WorkspaceID:   sess.workspaceID,
		Status:        RegistrationStatusPending,
		ExpiresAt:     sess.expiresAt,
		TransportMode: sess.transportMode,
		AllowUnbound:  sess.allowUnbound,
	}, sess.agentID)
	if err != nil {
		return BeginInstallResult{}, fmt.Errorf("dingtalk registration: persist session: %w", err)
	}
	sess.generation = generation

	// The polling goroutine outlives the request context, so we cannot
	// reuse ctx here. Its own context is sized to the (capped) device
	// code expiry — see registrationMaxPollWindow.
	go s.runPolling(sess)

	return BeginInstallResult{
		SessionID:           sessionID,
		QRCodeURL:           begin.QRCodeURL,
		ExpiresInSeconds:    int(begin.ExpiresIn / time.Second),
		PollIntervalSeconds: int(begin.Interval / time.Second),
	}, nil
}

// GetSession returns the current state of an in-flight or recently-
// finished session. The workspace UUID is required so a session
// initiated by one workspace cannot be polled by another.
//
// ErrRegistrationSessionNotFound is returned for unknown / swept
// sessions; the frontend treats it the same as an error reason of
// "session_lost" — prompt the user to restart the install.
//
// The read goes to the store, not to the polling goroutine's memory, so
// any replica can answer a poll for a session another replica opened.
func (s *RegistrationService) GetSession(ctx context.Context, workspaceID pgtype.UUID, sessionID string) (RegistrationSessionState, error) {
	if strings.TrimSpace(sessionID) == "" {
		return RegistrationSessionState{}, ErrRegistrationSessionNotFound
	}
	if err := s.store.Sweep(ctx, s.cfg.Now()); err != nil {
		s.cfg.Logger.Warn("dingtalk registration: sweep failed", "err", err)
	}
	rec, err := s.store.Get(ctx, sessionID)
	if err != nil {
		return RegistrationSessionState{}, err
	}
	if !uuidEqual(rec.WorkspaceID, workspaceID) {
		// Treat as not found — leaking "exists but wrong workspace"
		// would let an attacker enumerate session ids across workspaces.
		return RegistrationSessionState{}, ErrRegistrationSessionNotFound
	}
	// A row still pending past the device-code expiry means the poller is
	// gone: either the deadline passed before it could record the outcome,
	// or the replica driving it was rolled. Report it as expired so the
	// dialog offers a rescan instead of spinning forever.
	if rec.Status == RegistrationStatusPending && !rec.ExpiresAt.IsZero() && rec.ExpiresAt.Before(s.cfg.Now()) {
		return RegistrationSessionState{
			ID:           rec.ID,
			Status:       RegistrationStatusError,
			ErrorReason:  RegistrationReasonExpired,
			ErrorMessage: "QR expired before authorization",
		}, nil
	}
	state := RegistrationSessionState{
		ID:             rec.ID,
		Status:         rec.Status,
		InstallationID: rec.InstallationID,
		ErrorReason:    rec.ErrorReason,
		ErrorMessage:   rec.ErrorMessage,
	}
	if rec.Status == RegistrationStatusSuccess && rec.InstallationID.Valid &&
		s.installs != nil && s.installs.queries != nil {
		inst, err := s.installs.GetInWorkspace(ctx, rec.InstallationID, workspaceID)
		if err != nil {
			s.cfg.Logger.Warn("dingtalk registration: load completed installation status failed",
				"session_id", rec.ID, "installation_id", uuidString(rec.InstallationID), "err", err)
			return RegistrationSessionState{}, fmt.Errorf("load completed installation: %w", err)
		}
		state.RegistrationStatus = inst.RegistrationStatus
	}
	return state, nil
}

// runPolling is the background loop: wait → poll → branch on result.
func (s *RegistrationService) runPolling(sess *registrationSession) {
	ctx, cancel := context.WithDeadline(context.Background(), sess.expiresAt)
	defer cancel()

	interval := sess.interval
	if interval <= 0 {
		interval = time.Duration(registrationDefaultPollSeconds) * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			s.cfg.Logger.Info("dingtalk registration: session expired",
				"session_id", sess.id,
				"workspace_id", uuidString(sess.workspaceID))
			s.recordError(sess, RegistrationReasonExpired, "QR expired before authorization")
			return
		case <-time.After(interval):
		}

		res, err := s.client.Poll(ctx, sess.deviceCode)
		if err != nil {
			var re *RegistrationError
			if errors.As(err, &re) {
				s.cfg.Logger.Warn("dingtalk registration: protocol error",
					"session_id", sess.id, "code", re.Code, "desc", re.Description)
				s.recordError(sess, RegistrationReasonProtocol, re.Error())
				return
			}
			// Transient transport error (DNS, network) — log and try
			// again on the next tick rather than killing the session.
			s.cfg.Logger.Warn("dingtalk registration: transport error, will retry",
				"session_id", sess.id, "err", err)
			continue
		}

		switch {
		case res.ClientID != "" && res.ClientSecret != "":
			pollMode, modeErr := transportModeFromPoll(res.Mode)
			if modeErr != nil || pollMode != sess.transportMode {
				s.recordError(sess, RegistrationReasonProtocol, "registration result transport does not match requested transport")
				return
			}
			s.finishSuccess(ctx, sess, res)
			return
		case res.Err != nil:
			reason := RegistrationReasonProtocol
			switch res.Err.Code {
			case "expired":
				reason = RegistrationReasonExpired
			case "fail":
				// FAIL covers user-denied and DingTalk-side create
				// failures alike; fail_reason (prose) rides along in
				// error_message for the diagnostic tooltip.
				reason = RegistrationReasonInstallFailed
			}
			s.cfg.Logger.Info("dingtalk registration: terminal error",
				"session_id", sess.id, "code", res.Err.Code, "desc", res.Err.Description)
			s.recordError(sess, reason, res.Err.Error())
			return
		default:
			// WAITING — keep the interval, loop.
		}
	}
}

// finishSuccess runs the post-poll finalization: optional credential
// verification, then the installation upsert. A single row write —
// no transaction needed (contrast lark, which pairs the install with
// an installer binding; DingTalk's poll payload has no installer
// identity to bind).
func (s *RegistrationService) finishSuccess(ctx context.Context, sess *registrationSession, res *RegistrationPollResult) {
	current, err := s.store.IsCurrent(ctx, sess.workspaceID, sess.agentID, sess.generation)
	if err != nil {
		s.recordError(sess, RegistrationReasonInternalError, "failed to verify registration generation")
		return
	}
	if !current {
		s.recordError(sess, RegistrationReasonSuperseded, "a newer registration session replaced this QR code")
		return
	}
	// APPROVING credentials are issued once while DingTalk still rejects robot
	// messaging. Persist them immediately instead of running an availability
	// probe that is expected to fail before approval.
	if s.verifier != nil && !res.ApprovalPending {
		if err := s.verifier.VerifyAppCredentials(ctx, res.ClientID, res.ClientSecret); err != nil {
			s.cfg.Logger.Warn("dingtalk registration: credentials check failed",
				"session_id", sess.id, "err", err)
			s.recordError(sess, RegistrationReasonCredentialsCheckFailed, err.Error())
			return
		}
	}

	endpoint := HTTPCallbackEndpoint{}
	routerStatus := ""
	routerLastError := ""
	if sess.transportMode == TransportModeHTTPCallback {
		routerStatus = "pending"
		if s.httpCallbackRouter == nil {
			routerStatus = "failed"
			routerLastError = "agent message router is not configured"
		} else if prepared, err := s.httpCallbackRouter.PrepareEndpoint(ctx, sess.workspaceID, sess.agentID, sess.initiatorID); err != nil {
			routerStatus = "failed"
			routerLastError = err.Error()
		} else {
			endpoint = prepared
		}
	}

	inst, err := s.installs.Upsert(ctx, InstallationParams{
		WorkspaceID:        sess.workspaceID,
		AgentID:            sess.agentID,
		ClientID:           res.ClientID,
		ClientSecret:       res.ClientSecret,
		InstallerUserID:    sess.initiatorID,
		AllowUnbound:       sess.allowUnbound,
		TransportMode:      sess.transportMode,
		RobotCode:          res.RobotCode,
		DispatchEndpointID: endpoint.EndpointID,
		RouterStatus:       routerStatus,
		RouterLastError:    routerLastError,
		RegistrationStatus: func() string {
			if res.ApprovalPending {
				return registrationStatusApproving
			}
			return ""
		}(),
	})
	if err != nil {
		s.cfg.Logger.Warn("dingtalk registration: upsert installation",
			"session_id", sess.id, "err", err)
		s.recordError(sess, RegistrationReasonInstallationConflict, err.Error())
		return
	}

	// Persist the HTTP installation and endpoint before creating the Router
	// source. A Router delivery can then authenticate as soon as registration
	// becomes active; a Router failure leaves a retryable installation instead
	// of forcing the user to scan again.
	if sess.transportMode == TransportModeHTTPCallback && routerStatus == "pending" {
		sourceID, registerErr := s.httpCallbackRouter.Register(ctx, endpoint, sess.agentID, res.RobotCode)
		if registerErr != nil {
			routerStatus = "failed"
			routerLastError = registerErr.Error()
		} else {
			routerStatus = "registered"
		}
		updated, updateErr := s.installs.UpdateRouterState(ctx, inst, sourceID, routerStatus, routerLastError)
		if updateErr != nil {
			s.cfg.Logger.Error("dingtalk registration: persist router state failed",
				"session_id", sess.id, "installation_id", uuidString(inst.ID), "err", updateErr)
		} else {
			inst = updated
		}
	}

	s.recordSuccess(sess, inst.ID)
	// Publish at the commit point so the connection badge updates on
	// every workspace client without a page refresh — not only on the
	// tab that happens to poll the status endpoint to success.
	s.publishInstalled(sess.workspaceID, inst.ID)
	s.cfg.Logger.Info("dingtalk registration: install complete",
		"session_id", sess.id,
		"workspace_id", uuidString(sess.workspaceID),
		"agent_id", uuidString(sess.agentID),
		"installation_id", uuidString(inst.ID),
		"transport_mode", inst.TransportMode,
		"connection_managed", inst.ConnectionManaged,
		"registration_status", inst.RegistrationStatus)
}

// recordSuccess / recordError commit a terminal outcome. They update the
// driving goroutine's own copy and — the part that matters for a
// multi-replica deployment — persist it, so a poll served by any other
// replica sees the outcome instead of an unknown session.
func (s *RegistrationService) recordSuccess(sess *registrationSession, installationID pgtype.UUID) {
	gcAfter := s.gcDeadline()
	sess.markSuccess(installationID, gcAfter)
	if err := s.store.FinishSuccess(context.Background(), sess.id, installationID, gcAfter); err != nil {
		s.cfg.Logger.Error("dingtalk registration: persist success failed; poll will report the session as expired",
			"session_id", sess.id, "err", err)
	}
}

func (s *RegistrationService) recordError(sess *registrationSession, reason, message string) {
	gcAfter := s.gcDeadline()
	sess.markError(reason, message, gcAfter)
	if err := s.store.FinishError(context.Background(), sess.id, reason, message, gcAfter); err != nil {
		s.cfg.Logger.Error("dingtalk registration: persist error failed",
			"session_id", sess.id, "reason", reason, "err", err)
	}
}

func (s *RegistrationService) gcDeadline() time.Time {
	return s.cfg.Now().Add(s.cfg.SessionTTL)
}

// ErrRegistrationSessionNotFound is what the service returns for
// unknown / GC'd sessions. The handler maps it to 404.
var ErrRegistrationSessionNotFound = errors.New("dingtalk registration: session not found")

func randomSessionID() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func uuidEqual(a, b pgtype.UUID) bool {
	if !a.Valid || !b.Valid {
		return false
	}
	return a.Bytes == b.Bytes
}

func uuidString(u pgtype.UUID) string { return util.UUIDToString(u) }

// truncate caps s at n bytes for log/error tails.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
