package agentmessagerouter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	defaultCallbackTTL       = 10 * time.Minute
	maxAccountNameRunes      = 128
	maxOrganizationNameRunes = 256
	maxAvatarURLBytes        = 2048
	maxSourceIDBytes         = 64
)

var (
	ErrNotConfigured     = errors.New("dingtalk account binding is not configured")
	ErrAlreadyActive     = errors.New("dingtalk account binding is already active")
	ErrNotFound          = errors.New("dingtalk account binding not found")
	ErrCallbackExpired   = errors.New("dingtalk account binding callback expired")
	ErrBindingConflict   = errors.New("dingtalk account binding conflict")
	ErrRouterUnavailable = errors.New("agent message router is unavailable")
	ErrInvalidResult     = errors.New("dingtalk account binding result is invalid")
)

// Store is the generated-query seam used by the binding lifecycle. *db.Queries
// satisfies it directly; tests use an in-memory fake.
type Store interface {
	BeginDingTalkAccountBinding(context.Context, db.BeginDingTalkAccountBindingParams) (db.ChannelInstallation, error)
	GetDingTalkAccountBinding(context.Context, pgtype.UUID) (db.ChannelInstallation, error)
	GetDingTalkAccountBindingByAgent(context.Context, db.GetDingTalkAccountBindingByAgentParams) (db.ChannelInstallation, error)
	GetDingTalkAccountBindingInWorkspace(context.Context, db.GetDingTalkAccountBindingInWorkspaceParams) (db.ChannelInstallation, error)
	ListDingTalkAccountBindings(context.Context, pgtype.UUID) ([]db.ChannelInstallation, error)
	ClearExpiredDingTalkAccountCallbackCredentials(context.Context, db.ClearExpiredDingTalkAccountCallbackCredentialsParams) error
	ActivateDingTalkAccountBinding(context.Context, db.ActivateDingTalkAccountBindingParams) (db.ChannelInstallation, error)
	ActivateDingTalkAccountBindingWithIdentity(context.Context, db.ActivateDingTalkAccountBindingWithIdentityParams) (db.ActivateDingTalkAccountBindingWithIdentityRow, error)
	CompleteDingTalkAccountBindingResult(context.Context, db.CompleteDingTalkAccountBindingResultParams) (db.ChannelInstallation, error)
	RevokeDingTalkAccountBinding(context.Context, db.RevokeDingTalkAccountBindingParams) (db.ChannelInstallation, error)
}

type IdentityStore interface {
	BeginAgentDingTalkIdentityAttempt(context.Context, db.BeginAgentDingTalkIdentityAttemptParams) (db.AgentDingtalkIdentityAttempt, error)
	GetAgentDingTalkIdentityAttempt(context.Context, pgtype.UUID) (db.AgentDingtalkIdentityAttempt, error)
	GetAgentDingTalkIdentityAttemptByAgent(context.Context, db.GetAgentDingTalkIdentityAttemptByAgentParams) (db.AgentDingtalkIdentityAttempt, error)
	CompleteAgentDingTalkIdentityAttempt(context.Context, db.CompleteAgentDingTalkIdentityAttemptParams) (db.CompleteAgentDingTalkIdentityAttemptRow, error)
	GetAgentDingTalkIdentity(context.Context, db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error)
	ListAgentDingTalkIdentities(context.Context, pgtype.UUID) ([]db.AgentDingtalkIdentity, error)
	DeleteAgentDingTalkIdentity(context.Context, db.DeleteAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error)
	DeleteAgentDingTalkIdentityAttempts(context.Context, db.DeleteAgentDingTalkIdentityAttemptsParams) error
}

// Router is the existing subscription contract used to issue a QR credential,
// verify the DBase callback, and proxy unbind. *Client satisfies it.
type Router interface {
	IssueBindingToken(ctx context.Context, agentID, dispatchURL string) (BindingToken, error)
	GetSubscription(ctx context.Context, sourceID string) (Subscription, error)
	DeleteSubscription(ctx context.Context, sourceID string) error
}

type ServiceConfig struct {
	PublicBaseURL   string
	DBaseBindingURL string
	CallbackTTL     time.Duration
	Keyring         *DispatchKeyring
	Random          io.Reader
	Now             func() time.Time
	IdentityStore   IdentityStore
	Metrics         *obsmetrics.BusinessMetrics
}

type Service struct {
	store           Store
	router          Router
	publicOrigin    *url.URL
	dbaseBindingURL *url.URL
	callbackTTL     time.Duration
	keyring         *DispatchKeyring
	random          io.Reader
	now             func() time.Time
	identityStore   IdentityStore
	metrics         *obsmetrics.BusinessMetrics
}

type BeginParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	InitiatorID pgtype.UUID
	BindingMode BindingMode
}

type BeginResult struct {
	BindingID string    `json:"binding_id"`
	QRCodeURL string    `json:"qr_code_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type BindingMode string

const (
	BindingModeMessage  BindingMode = "message"
	BindingModeIdentity BindingMode = "identity"
)

func (m BindingMode) Valid() bool {
	return m == BindingModeMessage || m == BindingModeIdentity
}

type CallbackParams struct {
	BindingID       pgtype.UUID
	BindingMode     BindingMode
	CallbackToken   string
	IdentityBinding IdentityBindingResult
	MessageBinding  MessageBindingResult
}

type UnbindParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	BindingMode BindingMode
}

func NewService(store Store, router Router, config ServiceConfig) (*Service, error) {
	if store == nil || router == nil || config.Keyring == nil || config.Random == nil ||
		config.IdentityStore == nil {
		return nil, ErrNotConfigured
	}
	publicOrigin, err := canonicalHTTPSOrigin(config.PublicBaseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid public origin", ErrNotConfigured)
	}
	dbaseBindingURL, err := parseDBaseBindingURL(config.DBaseBindingURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid DBase binding URL", ErrNotConfigured)
	}
	callbackTTL := config.CallbackTTL
	if callbackTTL <= 0 {
		callbackTTL = defaultCallbackTTL
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:           store,
		router:          router,
		publicOrigin:    publicOrigin,
		dbaseBindingURL: dbaseBindingURL,
		callbackTTL:     callbackTTL,
		keyring:         config.Keyring,
		random:          config.Random,
		now:             now,
		identityStore:   config.IdentityStore,
		metrics:         config.Metrics,
	}, nil
}

func (s *Service) Begin(ctx context.Context, params BeginParams) (result BeginResult, err error) {
	var businessMetrics *obsmetrics.BusinessMetrics
	if s != nil {
		businessMetrics = s.metrics
	}
	defer func() {
		businessMetrics.RecordDingTalkAccountBegin(dingTalkAccountOperationOutcome(err))
	}()
	if !params.WorkspaceID.Valid || !params.AgentID.Valid || !params.InitiatorID.Valid ||
		!params.BindingMode.Valid() {
		return BeginResult{}, ErrNotFound
	}
	if s == nil || s.store == nil || s.router == nil || s.identityStore == nil || s.keyring == nil || s.random == nil {
		return BeginResult{}, ErrNotConfigured
	}
	endpointID, err := s.keyring.GenerateEndpointID(s.random)
	if err != nil {
		return BeginResult{}, fmt.Errorf("%w: endpoint generation failed", ErrNotConfigured)
	}
	dispatchURL, err := BuildDispatchURL(s.publicOrigin.String(), endpointID)
	if err != nil {
		return BeginResult{}, fmt.Errorf("%w: dispatch URL generation failed", ErrNotConfigured)
	}
	callbackToken, callbackHash, err := GenerateCallbackToken(s.random)
	if err != nil {
		return BeginResult{}, fmt.Errorf("%w: callback credential generation failed", ErrNotConfigured)
	}
	callbackExpiresAt := s.now().UTC().Add(s.callbackTTL)
	bindingID := pgtype.UUID{}
	storedDispatchURL := dispatchURL
	if params.BindingMode == BindingModeMessage {
		pendingConfig, marshalErr := NewPendingDingTalkAccountConfig(
			endpointID,
			dispatchURL,
			callbackHash,
			callbackExpiresAt,
		).Marshal()
		if marshalErr != nil {
			return BeginResult{}, fmt.Errorf("%w: pending config", ErrInvalidResult)
		}
		row, beginErr := s.store.BeginDingTalkAccountBinding(ctx, db.BeginDingTalkAccountBindingParams{
			WorkspaceID:     params.WorkspaceID,
			AgentID:         params.AgentID,
			Config:          pendingConfig,
			InstallerUserID: params.InitiatorID,
		})
		if beginErr != nil {
			if !errors.Is(beginErr, pgx.ErrNoRows) {
				return BeginResult{}, fmt.Errorf("begin dingtalk account binding: %w", beginErr)
			}
			existing, lookupErr := s.store.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{
				WorkspaceID: params.WorkspaceID,
				AgentID:     params.AgentID,
			})
			if lookupErr == nil && existing.Status == "active" {
				return BeginResult{}, ErrAlreadyActive
			}
			if errors.Is(lookupErr, pgx.ErrNoRows) {
				return BeginResult{}, ErrNotFound
			}
			if lookupErr != nil {
				return BeginResult{}, fmt.Errorf("lookup dingtalk account binding: %w", lookupErr)
			}
			return BeginResult{}, ErrBindingConflict
		}
		if row.Status != "pending" || row.ChannelType != ChannelTypeDingTalkAccount ||
			row.WorkspaceID != params.WorkspaceID || row.AgentID != params.AgentID {
			return BeginResult{}, ErrInvalidResult
		}
		storedConfig, parseErr := ParseDingTalkAccountConfig(row.Config)
		if parseErr != nil {
			return BeginResult{}, fmt.Errorf("%w: stored pending config", ErrInvalidResult)
		}
		bindingID = row.ID
		storedDispatchURL = storedConfig.DispatchURL
	} else {
		existingRoute, lookupErr := s.store.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{
			WorkspaceID: params.WorkspaceID,
			AgentID:     params.AgentID,
		})
		if lookupErr == nil && existingRoute.Status != "revoked" {
			return BeginResult{}, ErrAlreadyActive
		}
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return BeginResult{}, fmt.Errorf("lookup dingtalk account binding: %w", lookupErr)
		}
		_, identityErr := s.identityStore.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
			WorkspaceID: params.WorkspaceID,
			AgentID:     params.AgentID,
		})
		if identityErr == nil {
			return BeginResult{}, ErrAlreadyActive
		}
		if !errors.Is(identityErr, pgx.ErrNoRows) {
			return BeginResult{}, fmt.Errorf("lookup dingtalk identity: %w", identityErr)
		}
	}

	issued, err := s.router.IssueBindingToken(ctx, util.UUIDToString(params.AgentID), storedDispatchURL)
	if err != nil {
		return BeginResult{}, ErrRouterUnavailable
	}
	if strings.TrimSpace(issued.BindingToken) == "" || !issued.ExpiresAt.After(s.now()) {
		return BeginResult{}, ErrInvalidResult
	}
	expiresAt := issued.ExpiresAt.UTC()
	if callbackExpiresAt.Before(expiresAt) {
		expiresAt = callbackExpiresAt
	}
	if params.BindingMode == BindingModeIdentity {
		if err := s.identityStore.DeleteAgentDingTalkIdentityAttempts(ctx, db.DeleteAgentDingTalkIdentityAttemptsParams{
			WorkspaceID: params.WorkspaceID,
			AgentID:     params.AgentID,
		}); err != nil {
			return BeginResult{}, fmt.Errorf("prepare dingtalk identity attempt: %w", err)
		}
		identityAttempt, attemptErr := s.identityStore.BeginAgentDingTalkIdentityAttempt(ctx, db.BeginAgentDingTalkIdentityAttemptParams{
			WorkspaceID:       params.WorkspaceID,
			AgentID:           params.AgentID,
			InitiatorUserID:   params.InitiatorID,
			CallbackTokenHash: callbackHash,
			ExpiresAt:         pgtype.Timestamptz{Time: expiresAt, Valid: true},
		})
		if attemptErr != nil {
			if errors.Is(attemptErr, pgx.ErrNoRows) {
				return BeginResult{}, ErrNotFound
			}
			return BeginResult{}, fmt.Errorf("begin dingtalk identity attempt: %w", attemptErr)
		}
		bindingID = identityAttempt.ID
	}
	callbackURL := *s.publicOrigin
	callbackURL.Path = "/api/integrations/dingtalk/account-bindings/" + util.UUIDToString(bindingID) + "/callback"
	fragment := url.Values{
		"bindingMode":  {string(params.BindingMode)},
		"bindingToken":  {issued.BindingToken},
		"callbackUrl":   {callbackURL.String()},
		"callbackToken": {callbackToken},
		"expiresAt":     {strconv.FormatInt(expiresAt.Unix(), 10)},
		"agentId":       {util.UUIDToString(params.AgentID)},
		"dispatchUrl":   {storedDispatchURL},
	}
	qrCodeURL := s.dbaseBindingURL.String() + "#" + fragment.Encode()
	return BeginResult{
		BindingID: util.UUIDToString(bindingID),
		QRCodeURL: qrCodeURL,
		ExpiresAt: expiresAt,
	}, nil
}

func (s *Service) List(ctx context.Context, workspaceID pgtype.UUID) ([]PublicDingTalkAccountBinding, error) {
	if s == nil || s.store == nil {
		return nil, ErrNotConfigured
	}
	if !workspaceID.Valid {
		return nil, ErrNotFound
	}
	if err := s.store.ClearExpiredDingTalkAccountCallbackCredentials(ctx, db.ClearExpiredDingTalkAccountCallbackCredentialsParams{
		WorkspaceID: workspaceID,
		ExpiredBefore: pgtype.Timestamptz{
			Time:  s.now().UTC(),
			Valid: true,
		},
	}); err != nil {
		return nil, fmt.Errorf("clear expired dingtalk account callback credentials: %w", err)
	}
	rows, err := s.store.ListDingTalkAccountBindings(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list dingtalk account bindings: %w", err)
	}
	identities, err := s.identityStore.ListAgentDingTalkIdentities(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list dingtalk identities: %w", err)
	}
	bindings := make([]PublicDingTalkAccountBinding, 0, len(rows)+len(identities))
	seenAgents := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		binding, err := s.publicBinding(ctx, row)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
		seenAgents[binding.AgentID] = struct{}{}
	}
	for _, identity := range identities {
		agentID := util.UUIDToString(identity.AgentID)
		if _, seen := seenAgents[agentID]; seen {
			continue
		}
		bindings = append(bindings, publicIdentityBinding(identity))
	}
	return bindings, nil
}

func (s *Service) CompleteCallback(ctx context.Context, params CallbackParams) (binding PublicDingTalkAccountBinding, err error) {
	return s.completeCallback(ctx, params, true)
}

func (s *Service) completeCallback(ctx context.Context, params CallbackParams, recordMetric bool) (binding PublicDingTalkAccountBinding, err error) {
	var businessMetrics *obsmetrics.BusinessMetrics
	if recordMetric && s != nil {
		businessMetrics = s.metrics
	}
	defer func() {
		if recordMetric {
			businessMetrics.RecordDingTalkAccountCallback(dingTalkAccountOperationOutcome(err))
		}
	}()
	if s == nil || s.store == nil || s.router == nil || s.identityStore == nil {
		return PublicDingTalkAccountBinding{}, ErrNotConfigured
	}
	if !params.BindingID.Valid || !params.BindingMode.Valid() {
		return PublicDingTalkAccountBinding{}, ErrNotFound
	}
	identity, identityErr := normalizeIdentityBindingResult(params.IdentityBinding)
	if identityErr != nil {
		return PublicDingTalkAccountBinding{}, ErrInvalidResult
	}
	messageScope, conversations, validationErr := validateCompleteBindingParams(CompleteBindingParams{
		BindingID:      params.BindingID,
		BindingMode:    params.BindingMode,
		CallbackToken:  params.CallbackToken,
		Status:         DingTalkBindingCompletionStatus,
		Identity:       identity,
		Message:        params.MessageBinding,
	})
	if validationErr != nil {
		return PublicDingTalkAccountBinding{}, ErrInvalidResult
	}
	if params.BindingMode == BindingModeIdentity {
		if params.MessageBinding.Status != DingTalkBindingTaskStatusSkipped {
			return PublicDingTalkAccountBinding{}, ErrInvalidResult
		}
		return s.completeIdentityBinding(ctx, params.BindingID, params.CallbackToken, identity)
	}
	if params.MessageBinding.Status != DingTalkBindingTaskStatusSuccess {
		return PublicDingTalkAccountBinding{}, ErrInvalidResult
	}
	sourceID := strings.TrimSpace(params.MessageBinding.SourceID)
	row, err := s.store.GetDingTalkAccountBinding(ctx, params.BindingID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicDingTalkAccountBinding{}, ErrNotFound
		}
		return PublicDingTalkAccountBinding{}, fmt.Errorf("get dingtalk account binding: %w", err)
	}
	config, err := ParseDingTalkAccountConfig(row.Config)
	if err != nil {
		return PublicDingTalkAccountBinding{}, fmt.Errorf("%w: stored binding config", ErrInvalidResult)
	}
	if !VerifyCallbackToken(params.CallbackToken, config.CallbackTokenHash) ||
		!s.now().Before(config.CallbackExpiresAt) {
		return PublicDingTalkAccountBinding{}, ErrCallbackExpired
	}
	if row.Status == "active" {
		if config.RouterSourceID != sourceID {
			return PublicDingTalkAccountBinding{}, ErrBindingConflict
		}
		storedIdentity, identityLookupErr := s.identityStore.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
			WorkspaceID: row.WorkspaceID,
			AgentID:     row.AgentID,
		})
		if identityLookupErr != nil || storedIdentity.DwsUid != identity.AccountUID ||
			storedIdentity.OrgID != identity.AccountOrgID {
			return PublicDingTalkAccountBinding{}, ErrBindingConflict
		}
		if err := s.verifyActiveSubscription(ctx, sourceID, row, config); err != nil {
			return PublicDingTalkAccountBinding{}, err
		}
		return s.publicBinding(ctx, row)
	}
	if row.Status != "pending" {
		return PublicDingTalkAccountBinding{}, ErrBindingConflict
	}
	subscription, err := s.verifySubscription(ctx, sourceID, row, config)
	if err != nil {
		// Only compensate a source whose GET response proves it belongs to this
		// exact agent and dispatch URL. Never DELETE an unverified body sourceId.
		if errors.Is(err, ErrBindingConflict) && subscription.SourceID == sourceID &&
			subscription.AgentID == util.UUIDToString(row.AgentID) &&
			subscription.DispatchURL == config.DispatchURL {
			if err := s.router.DeleteSubscription(ctx, subscription.SourceID); err != nil {
				return PublicDingTalkAccountBinding{}, ErrRouterUnavailable
			}
		}
		return PublicDingTalkAccountBinding{}, err
	}
	boundAt := s.now().UTC()
	config.RouterSourceID = sourceID
	config.AccountDisplayName = identity.AccountDisplayName
	config.AccountAvatarURL = identity.AccountAvatarURL
	config.DWSIdentityStatus = ""
	config.MessageRouteStatus = ""
	config.MessageScope = messageScope
	config.Conversations = conversations
	config.BoundAt = &boundAt
	activeConfig, err := config.Marshal()
	if err != nil {
		return PublicDingTalkAccountBinding{}, fmt.Errorf("%w: active binding config", ErrInvalidResult)
	}
	activated, err := s.store.ActivateDingTalkAccountBindingWithIdentity(ctx, db.ActivateDingTalkAccountBindingWithIdentityParams{
		Config:                    activeConfig,
		ID:                        row.ID,
		WorkspaceID:               row.WorkspaceID,
		AgentID:                   row.AgentID,
		ExpectedCallbackTokenHash: config.CallbackTokenHash,
		DwsUid:                    identity.AccountUID,
		OrgID:                     identity.AccountOrgID,
		OrganizationName:          identity.AccountOrganizationName,
		AccountDisplayName:        identity.AccountDisplayName,
		AccountAvatarUrl:          identity.AccountAvatarURL,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current, lookupErr := s.store.GetDingTalkAccountBinding(ctx, params.BindingID)
			if lookupErr == nil && current.Status == "active" {
				currentConfig, parseErr := ParseDingTalkAccountConfig(current.Config)
				if parseErr == nil && currentConfig.RouterSourceID == sourceID &&
					VerifyCallbackToken(params.CallbackToken, currentConfig.CallbackTokenHash) &&
					s.now().Before(currentConfig.CallbackExpiresAt) {
					return s.publicBinding(ctx, current)
				}
			}
			// The callback source was already verified against this exact agent and
			// dispatch URL. If the local CAS lost to a newer pending attempt, an
			// unbind, or another source becoming active, remove only this losing
			// source. Never delete the source stored by the winning active row.
			if lookupErr == nil {
				if deleteErr := s.router.DeleteSubscription(ctx, sourceID); deleteErr != nil {
					return PublicDingTalkAccountBinding{}, ErrRouterUnavailable
				}
			}
			return PublicDingTalkAccountBinding{}, ErrBindingConflict
		}
		return PublicDingTalkAccountBinding{}, fmt.Errorf("activate dingtalk account binding: %w", err)
	}
	return s.publicBinding(ctx, channelInstallationFromActivated(activated))
}

func channelInstallationFromActivated(row db.ActivateDingTalkAccountBindingWithIdentityRow) db.ChannelInstallation {
	return db.ChannelInstallation{
		ID:               row.ID,
		WorkspaceID:      row.WorkspaceID,
		AgentID:          row.AgentID,
		ChannelType:      row.ChannelType,
		Config:           row.Config,
		Status:           row.Status,
		WsLeaseToken:     row.WsLeaseToken,
		WsLeaseExpiresAt: row.WsLeaseExpiresAt,
		InstallerUserID:  row.InstallerUserID,
		InstalledAt:      row.InstalledAt,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func (s *Service) Unbind(ctx context.Context, params UnbindParams) (binding PublicDingTalkAccountBinding, err error) {
	var businessMetrics *obsmetrics.BusinessMetrics
	if s != nil {
		businessMetrics = s.metrics
	}
	defer func() {
		businessMetrics.RecordDingTalkAccountUnbind(dingTalkAccountOperationOutcome(err))
	}()
	if s == nil || s.store == nil || s.router == nil || s.identityStore == nil {
		return PublicDingTalkAccountBinding{}, ErrNotConfigured
	}
	if !params.WorkspaceID.Valid || !params.AgentID.Valid || !params.BindingMode.Valid() {
		return PublicDingTalkAccountBinding{}, ErrNotFound
	}
	if params.BindingMode == BindingModeIdentity {
		route, routeErr := s.store.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{
			WorkspaceID: params.WorkspaceID,
			AgentID:     params.AgentID,
		})
		if routeErr == nil && route.Status == "active" {
			return PublicDingTalkAccountBinding{}, ErrBindingConflict
		}
		if routeErr != nil && !errors.Is(routeErr, pgx.ErrNoRows) {
			return PublicDingTalkAccountBinding{}, fmt.Errorf("get dingtalk account binding: %w", routeErr)
		}
		_, deleteErr := s.identityStore.DeleteAgentDingTalkIdentity(ctx, db.DeleteAgentDingTalkIdentityParams{
			WorkspaceID: params.WorkspaceID,
			AgentID:     params.AgentID,
		})
		if deleteErr != nil {
			if errors.Is(deleteErr, pgx.ErrNoRows) {
				return PublicDingTalkAccountBinding{}, ErrNotFound
			}
			return PublicDingTalkAccountBinding{}, fmt.Errorf("delete dingtalk identity: %w", deleteErr)
		}
		if routeErr == nil {
			return s.publicBinding(ctx, route)
		}
		return PublicDingTalkAccountBinding{
			ID:           util.UUIDToString(params.AgentID),
			WorkspaceID:  util.UUIDToString(params.WorkspaceID),
			AgentID:      util.UUIDToString(params.AgentID),
			DWSIdentity:  PublicDingTalkBindingOutcome{Status: "unbound"},
			MessageRoute: PublicDingTalkBindingOutcome{Status: "unbound"},
		}, nil
	}

	row, err := s.store.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{
		WorkspaceID: params.WorkspaceID,
		AgentID:     params.AgentID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicDingTalkAccountBinding{}, ErrNotFound
		}
		return PublicDingTalkAccountBinding{}, fmt.Errorf("get dingtalk account binding: %w", err)
	}
	config, err := ParseDingTalkAccountConfig(row.Config)
	if err != nil {
		return PublicDingTalkAccountBinding{}, fmt.Errorf("%w: stored binding config", ErrInvalidResult)
	}
	if config.RouterSourceID != "" {
		// Remote delete stays fail-closed EXCEPT for the router's explicit
		// "subscription_not_found" business error: the subscription is
		// already gone, which is exactly the state this delete wants. This
		// is the multi-replica recovery path — a pod that crashed between
		// the router delete and the local revoke, or a concurrent unbind on
		// another replica, leaves the remote side deleted while the local
		// row is still active; without this tolerance every retry 502s
		// forever and the binding can never be unbound (re-begin then 409s
		// on the active row). Transport failures and other router errors
		// still abort before the local transition, so a live subscription
		// is never orphaned. Revoke itself is idempotent (status IN
		// pending/active/revoked), so double unbinds converge.
		if err := s.router.DeleteSubscription(ctx, config.RouterSourceID); err != nil &&
			!errors.Is(err, ErrSubscriptionNotFound) {
			return PublicDingTalkAccountBinding{}, ErrRouterUnavailable
		}
	} else if row.Status == "active" && config.MessageRouteStatus != DingTalkBindingStatusSkipped {
		return PublicDingTalkAccountBinding{}, ErrInvalidResult
	}
	revoked, err := s.store.RevokeDingTalkAccountBinding(ctx, db.RevokeDingTalkAccountBindingParams{
		ID:          row.ID,
		WorkspaceID: row.WorkspaceID,
		AgentID:     row.AgentID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicDingTalkAccountBinding{}, ErrNotFound
		}
		return PublicDingTalkAccountBinding{}, fmt.Errorf("revoke dingtalk account binding: %w", err)
	}
	return s.publicBinding(ctx, revoked)
}

func (s *Service) verifyActiveSubscription(ctx context.Context, sourceID string, row db.ChannelInstallation, config DingTalkAccountConfig) error {
	_, err := s.verifySubscription(ctx, sourceID, row, config)
	return err
}

func (s *Service) verifySubscription(ctx context.Context, sourceID string, row db.ChannelInstallation, config DingTalkAccountConfig) (Subscription, error) {
	subscription, err := s.router.GetSubscription(ctx, sourceID)
	if err != nil {
		if errors.Is(err, ErrSubscriptionNotFound) {
			s.metrics.RecordDingTalkAccountSubscriptionVerify("not_found")
		} else {
			s.metrics.RecordDingTalkAccountSubscriptionVerify("router_unavailable")
		}
		return subscription, mapSubscriptionLookupError(err)
	}
	outcome := subscriptionVerificationOutcome(subscription, sourceID, row, config)
	s.metrics.RecordDingTalkAccountSubscriptionVerify(outcome)
	if outcome != "success" {
		return subscription, ErrBindingConflict
	}
	return subscription, nil
}

func mapSubscriptionLookupError(err error) error {
	if errors.Is(err, ErrSubscriptionNotFound) {
		return ErrBindingConflict
	}
	return ErrRouterUnavailable
}

func subscriptionMatches(subscription Subscription, sourceID string, row db.ChannelInstallation, config DingTalkAccountConfig) bool {
	return subscriptionVerificationOutcome(subscription, sourceID, row, config) == "success"
}

func subscriptionVerificationOutcome(subscription Subscription, sourceID string, row db.ChannelInstallation, config DingTalkAccountConfig) string {
	if subscription.SourceID != sourceID {
		return "source_mismatch"
	}
	if subscription.AgentID != util.UUIDToString(row.AgentID) {
		return "agent_mismatch"
	}
	if subscription.DispatchURL != config.DispatchURL {
		return "dispatch_url_mismatch"
	}
	if subscription.Status != "active" {
		return "inactive"
	}
	return "success"
}

func dingTalkAccountOperationOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrNotConfigured):
		return "not_configured"
	case errors.Is(err, ErrAlreadyActive):
		return "already_active"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrCallbackExpired):
		return "expired"
	case errors.Is(err, ErrBindingConflict):
		return "conflict"
	case errors.Is(err, ErrRouterUnavailable):
		return "router_unavailable"
	case errors.Is(err, ErrInvalidResult):
		return "invalid_result"
	default:
		return "storage_error"
	}
}

func (s *Service) publicBinding(ctx context.Context, row db.ChannelInstallation) (PublicDingTalkAccountBinding, error) {
	if row.ChannelType != ChannelTypeDingTalkAccount {
		return PublicDingTalkAccountBinding{}, ErrInvalidResult
	}
	config, err := ParseDingTalkAccountConfig(row.Config)
	if err != nil {
		return PublicDingTalkAccountBinding{}, fmt.Errorf("%w: stored binding config", ErrInvalidResult)
	}
	dwsIdentity := PublicDingTalkBindingOutcome{Status: "unbound"}
	identity, identityErr := s.identityStore.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
		WorkspaceID: row.WorkspaceID,
		AgentID:     row.AgentID,
	})
	if identityErr == nil {
		boundAt := identity.BoundAt.Time
		source := string(BindingModeIdentity)
		if row.Status == "active" {
			source = string(BindingModeMessage)
		}
		dwsIdentity = PublicDingTalkBindingOutcome{
			Status:             "active",
			Source:             source,
			OrganizationName:   identity.OrganizationName,
			AccountDisplayName: identity.AccountDisplayName,
			AccountAvatarURL:   identity.AccountAvatarUrl,
			BoundAt:            &boundAt,
		}
	} else if !errors.Is(identityErr, pgx.ErrNoRows) {
		return PublicDingTalkAccountBinding{}, fmt.Errorf("load dingtalk identity: %w", identityErr)
	} else if config.DWSIdentityStatus != "" {
		dwsIdentity.Status = config.DWSIdentityStatus
	}
	return config.PublicBinding(
		util.UUIDToString(row.WorkspaceID),
		util.UUIDToString(row.AgentID),
		row.Status,
		dwsIdentity,
	), nil
}

func publicIdentityBinding(identity db.AgentDingtalkIdentity) PublicDingTalkAccountBinding {
	boundAt := identity.BoundAt.Time
	return PublicDingTalkAccountBinding{
		ID:          util.UUIDToString(identity.AgentID),
		WorkspaceID: util.UUIDToString(identity.WorkspaceID),
		AgentID:     util.UUIDToString(identity.AgentID),
		DWSIdentity: PublicDingTalkBindingOutcome{
			Status:             "active",
			Source:             string(BindingModeIdentity),
			OrganizationName:   identity.OrganizationName,
			AccountDisplayName: identity.AccountDisplayName,
			AccountAvatarURL:   identity.AccountAvatarUrl,
			BoundAt:            &boundAt,
		},
		MessageRoute: PublicDingTalkBindingOutcome{Status: "unbound"},
	}
}

func parseDBaseBindingURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery ||
		parsed.Opaque != "" {
		return nil, errors.New("DBase binding URL must be an absolute HTTPS URL without query or fragment")
	}
	return parsed, nil
}

func validAccountAvatarURL(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > maxAvatarURLBytes {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.Hostname() != "" &&
		parsed.User == nil && parsed.Fragment == "" && parsed.Opaque == ""
}
