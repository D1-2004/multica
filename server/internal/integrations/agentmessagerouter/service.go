package agentmessagerouter

import (
	"context"
	"encoding/json"
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
	CompleteDingTalkAccountBindingResult(context.Context, db.CompleteDingTalkAccountBindingResultParams) (db.ChannelInstallation, error)
	UpdateDingTalkAccountBindingSurface(context.Context, db.UpdateDingTalkAccountBindingSurfaceParams) (db.ChannelInstallation, error)
	RevokeDingTalkAccountBinding(context.Context, db.RevokeDingTalkAccountBindingParams) (db.ChannelInstallation, error)
}

type robotEndpointStore interface {
	GetActiveDingTalkBotInstallationByAgent(context.Context, db.GetActiveDingTalkBotInstallationByAgentParams) (db.ChannelInstallation, error)
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
	IssueBindingToken(ctx context.Context, descriptor AgentDescriptor) (BindingToken, error)
	GetSubscription(ctx context.Context, sourceID string) (Subscription, error)
	UpdateSubscriptionSurface(ctx context.Context, sourceID, agentID, surfaceType string) (Subscription, error)
	DeleteSubscription(ctx context.Context, sourceID string) error
	DeleteDigitalEmployeeSubscriptions(ctx context.Context, agentID string) error
}

type DispatchEndpoint struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	ActorUserID pgtype.UUID
	EndpointID  string
	DispatchURL string
}

type DispatchEndpointStore interface {
	EnsureAgentDispatchEndpoint(context.Context, DispatchEndpoint) (DispatchEndpoint, error)
	GetAgentDispatchEndpoint(context.Context, pgtype.UUID) (DispatchEndpoint, error)
}

type DispatchEndpointServiceConfig struct {
	PublicBaseURL string
	Keyring       *DispatchKeyring
	Random        io.Reader
}

type DispatchEndpointService struct {
	store        DispatchEndpointStore
	publicOrigin *url.URL
	keyring      *DispatchKeyring
	random       io.Reader
}

type DBDispatchEndpointStore struct {
	queries *db.Queries
}

func NewDBDispatchEndpointStore(queries *db.Queries) *DBDispatchEndpointStore {
	return &DBDispatchEndpointStore{queries: queries}
}

func (s *DBDispatchEndpointStore) EnsureAgentDispatchEndpoint(
	ctx context.Context,
	candidate DispatchEndpoint,
) (DispatchEndpoint, error) {
	if s == nil || s.queries == nil {
		return DispatchEndpoint{}, errors.New("agent dispatch endpoint store is not configured")
	}
	row, err := s.queries.EnsureAgentDispatchEndpoint(ctx, db.EnsureAgentDispatchEndpointParams{
		WorkspaceID: candidate.WorkspaceID,
		AgentID:     candidate.AgentID,
		ActorUserID: candidate.ActorUserID,
		EndpointID:  candidate.EndpointID,
		DispatchUrl: candidate.DispatchURL,
	})
	if err != nil {
		return DispatchEndpoint{}, err
	}
	return dispatchEndpointFromRow(row), nil
}

func (s *DBDispatchEndpointStore) GetAgentDispatchEndpoint(
	ctx context.Context,
	agentID pgtype.UUID,
) (DispatchEndpoint, error) {
	if s == nil || s.queries == nil {
		return DispatchEndpoint{}, errors.New("agent dispatch endpoint store is not configured")
	}
	row, err := s.queries.GetAgentDispatchEndpoint(ctx, agentID)
	if err != nil {
		return DispatchEndpoint{}, err
	}
	return dispatchEndpointFromRow(row), nil
}

func dispatchEndpointFromRow(row db.AgentDispatchEndpoint) DispatchEndpoint {
	return DispatchEndpoint{
		WorkspaceID: row.WorkspaceID,
		AgentID:     row.AgentID,
		ActorUserID: row.ActorUserID,
		EndpointID:  row.EndpointID,
		DispatchURL: row.DispatchUrl,
	}
}

func NewDispatchEndpointService(store DispatchEndpointStore, config DispatchEndpointServiceConfig) (*DispatchEndpointService, error) {
	if store == nil || config.Keyring == nil || config.Random == nil {
		return nil, errors.New("agent dispatch endpoint service is not configured")
	}
	publicOrigin, err := canonicalHTTPSOrigin(config.PublicBaseURL)
	if err != nil {
		return nil, fmt.Errorf("agent dispatch endpoint service is not configured: %w", err)
	}
	return &DispatchEndpointService{
		store:        store,
		publicOrigin: publicOrigin,
		keyring:      config.Keyring,
		random:       config.Random,
	}, nil
}

func (s *DispatchEndpointService) Ensure(
	ctx context.Context,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	actorUserID pgtype.UUID,
) (DispatchEndpoint, error) {
	if s == nil || s.store == nil || s.publicOrigin == nil || s.keyring == nil || s.random == nil {
		return DispatchEndpoint{}, errors.New("agent dispatch endpoint service is not configured")
	}
	if !workspaceID.Valid || !agentID.Valid || !actorUserID.Valid {
		return DispatchEndpoint{}, errors.New("agent dispatch endpoint ownership is invalid")
	}
	endpointID, err := s.keyring.GenerateEndpointID(s.random)
	if err != nil {
		return DispatchEndpoint{}, err
	}
	dispatchPath, err := dispatchPathForEndpointID(endpointID)
	if err != nil {
		return DispatchEndpoint{}, err
	}
	// dispatch_url is a legacy column name. New endpoint mappings persist only
	// the environment-neutral path; runtime callers receive a URL rebuilt below.
	result, err := s.store.EnsureAgentDispatchEndpoint(ctx, DispatchEndpoint{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		ActorUserID: actorUserID,
		EndpointID:  endpointID,
		DispatchURL: dispatchPath,
	})
	if err != nil {
		return DispatchEndpoint{}, fmt.Errorf("ensure agent dispatch endpoint: %w", err)
	}
	return normalizeDispatchEndpoint(result, workspaceID, agentID, s.publicOrigin)
}

func (s *DispatchEndpointService) Get(ctx context.Context, agentID pgtype.UUID) (DispatchEndpoint, error) {
	if s == nil || s.store == nil || !agentID.Valid {
		return DispatchEndpoint{}, errors.New("agent dispatch endpoint service is not configured")
	}
	result, err := s.store.GetAgentDispatchEndpoint(ctx, agentID)
	if err != nil {
		return DispatchEndpoint{}, err
	}
	return normalizeDispatchEndpoint(result, result.WorkspaceID, agentID, s.publicOrigin)
}

func normalizeDispatchEndpoint(
	endpoint DispatchEndpoint,
	workspaceID, agentID pgtype.UUID,
	publicOrigin *url.URL,
) (DispatchEndpoint, error) {
	if endpoint.WorkspaceID != workspaceID || endpoint.AgentID != agentID ||
		!endpoint.ActorUserID.Valid || !isTrimmedNonEmpty(endpoint.EndpointID) || publicOrigin == nil {
		return DispatchEndpoint{}, errors.New("agent dispatch endpoint response is invalid")
	}
	// Historical rows may contain a pre-release or production origin. The
	// endpoint mapping is identified by endpoint_id, so never trust that stored
	// origin when constructing a callback for the current runtime.
	dispatchURL, err := BuildDispatchURL(publicOrigin.String(), endpoint.EndpointID)
	if err != nil {
		return DispatchEndpoint{}, errors.New("agent dispatch endpoint response is invalid")
	}
	endpoint.DispatchURL = dispatchURL
	return endpoint, nil
}

type ServiceConfig struct {
	PublicBaseURL   string
	DBaseBindingURL string
	CallbackTTL     time.Duration
	Keyring         *DispatchKeyring
	Random          io.Reader
	Now             func() time.Time
	IdentityStore   IdentityStore
	Endpoints       *DispatchEndpointService
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
	endpoints       *DispatchEndpointService
	metrics         *obsmetrics.BusinessMetrics
}

type BeginWorkspace struct {
	ID   pgtype.UUID
	Name string
}

type BeginAgent struct {
	ID        pgtype.UUID
	Name      string
	Workspace BeginWorkspace
}

type BeginParams struct {
	Agent       BeginAgent
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

type UpdateSurfaceParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	SurfaceType string
}

func NewService(store Store, router Router, config ServiceConfig) (*Service, error) {
	if store == nil || router == nil || config.Keyring == nil || config.Random == nil ||
		config.IdentityStore == nil || config.Endpoints == nil {
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
		endpoints:       config.Endpoints,
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
	if !params.Agent.Workspace.ID.Valid || !params.Agent.ID.Valid || !params.InitiatorID.Valid ||
		!params.BindingMode.Valid() {
		return BeginResult{}, ErrNotFound
	}
	if s == nil || s.store == nil || s.router == nil || s.identityStore == nil || s.keyring == nil ||
		s.random == nil || s.endpoints == nil {
		return BeginResult{}, ErrNotConfigured
	}
	agentName, err := normalizeBindingDisplayName("agent name", params.Agent.Name)
	if err != nil {
		return BeginResult{}, fmt.Errorf("%w: begin agent descriptor: %v", ErrInvalidResult, err)
	}
	workspaceName, err := normalizeBindingDisplayName("workspace name", params.Agent.Workspace.Name)
	if err != nil {
		return BeginResult{}, fmt.Errorf("%w: begin agent descriptor: %v", ErrInvalidResult, err)
	}
	endpoint, err := s.endpoints.Ensure(ctx, params.Agent.Workspace.ID, params.Agent.ID, params.InitiatorID)
	if err != nil {
		return BeginResult{}, fmt.Errorf("%w: endpoint resolution failed", ErrNotConfigured)
	}
	endpointID := endpoint.EndpointID
	dispatchPath, err := dispatchPathForEndpointID(endpointID)
	if err != nil {
		return BeginResult{}, fmt.Errorf("%w: dispatch path", ErrInvalidResult)
	}
	descriptor, err := normalizeAgentDescriptor(AgentDescriptor{
		AgentID: util.UUIDToString(params.Agent.ID),
		Name:    agentName,
		Workspace: WorkspaceDescriptor{
			ID:   util.UUIDToString(params.Agent.Workspace.ID),
			Name: workspaceName,
		},
		DispatchPath: dispatchPath,
	})
	if err != nil {
		return BeginResult{}, fmt.Errorf("%w: begin agent descriptor: %v", ErrInvalidResult, err)
	}
	callbackToken, callbackHash, err := GenerateCallbackToken(s.random)
	if err != nil {
		return BeginResult{}, fmt.Errorf("%w: callback credential generation failed", ErrNotConfigured)
	}
	callbackExpiresAt := s.now().UTC().Add(s.callbackTTL)
	bindingID := pgtype.UUID{}
	if params.BindingMode == BindingModeMessage {
		pendingConfig, marshalErr := NewPendingDingTalkAccountConfig(
			endpointID,
			dispatchPath,
			callbackHash,
			callbackExpiresAt,
		).Marshal()
		if marshalErr != nil {
			return BeginResult{}, fmt.Errorf("%w: pending config", ErrInvalidResult)
		}
		row, beginErr := s.store.BeginDingTalkAccountBinding(ctx, db.BeginDingTalkAccountBindingParams{
			WorkspaceID:     params.Agent.Workspace.ID,
			AgentID:         params.Agent.ID,
			Config:          pendingConfig,
			InstallerUserID: params.InitiatorID,
		})
		if beginErr != nil {
			if !errors.Is(beginErr, pgx.ErrNoRows) {
				return BeginResult{}, fmt.Errorf("begin dingtalk account binding: %w", beginErr)
			}
			existing, lookupErr := s.store.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{
				WorkspaceID: params.Agent.Workspace.ID,
				AgentID:     params.Agent.ID,
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
			row.WorkspaceID != params.Agent.Workspace.ID || row.AgentID != params.Agent.ID {
			return BeginResult{}, ErrInvalidResult
		}
		storedConfig, parseErr := ParseDingTalkAccountConfig(row.Config)
		if parseErr != nil {
			return BeginResult{}, fmt.Errorf("%w: stored pending config", ErrInvalidResult)
		}
		if storedConfig.DispatchEndpointID != endpointID {
			return BeginResult{}, fmt.Errorf("%w: stored endpoint mismatch", ErrInvalidResult)
		}
		bindingID = row.ID
	} else {
		_, identityErr := s.identityStore.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
			WorkspaceID: params.Agent.Workspace.ID,
			AgentID:     params.Agent.ID,
		})
		if identityErr == nil {
			return BeginResult{}, ErrAlreadyActive
		}
		if !errors.Is(identityErr, pgx.ErrNoRows) {
			return BeginResult{}, fmt.Errorf("lookup dingtalk identity: %w", identityErr)
		}
	}

	issued, err := s.router.IssueBindingToken(ctx, descriptor)
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
			WorkspaceID: params.Agent.Workspace.ID,
			AgentID:     params.Agent.ID,
		}); err != nil {
			return BeginResult{}, fmt.Errorf("prepare dingtalk identity attempt: %w", err)
		}
		identityAttempt, attemptErr := s.identityStore.BeginAgentDingTalkIdentityAttempt(ctx, db.BeginAgentDingTalkIdentityAttemptParams{
			WorkspaceID:       params.Agent.Workspace.ID,
			AgentID:           params.Agent.ID,
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
		"bindingMode":   {string(params.BindingMode)},
		"bindingToken":  {issued.BindingToken},
		"callbackUrl":   {callbackURL.String()},
		"callbackToken": {callbackToken},
		"expiresAt":     {strconv.FormatInt(expiresAt.Unix(), 10)},
		"agentId":       {descriptor.AgentID},
		"agentName":     {descriptor.Name},
		"workspaceId":   {descriptor.Workspace.ID},
		"workspaceName": {descriptor.Workspace.Name},
		"dispatchPath":  {descriptor.DispatchPath},
	}
	qrCodeURL := s.dbaseBindingURL.String() + "#" + fragment.Encode()
	return BeginResult{
		BindingID: util.UUIDToString(bindingID),
		QRCodeURL: qrCodeURL,
		ExpiresAt: expiresAt,
	}, nil
}

func (s *Service) dispatchEndpointForAgent(
	ctx context.Context,
	workspaceID, agentID pgtype.UUID,
) (string, string, error) {
	if store, ok := s.store.(robotEndpointStore); ok {
		row, err := store.GetActiveDingTalkBotInstallationByAgent(ctx, db.GetActiveDingTalkBotInstallationByAgentParams{
			WorkspaceID: workspaceID,
			AgentID:     agentID,
		})
		if err == nil {
			var cfg struct {
				EndpointID string `json:"dispatch_endpoint_id"`
			}
			if json.Unmarshal(row.Config, &cfg) != nil {
				return "", "", errors.New("invalid robot dispatch endpoint")
			}
			if strings.TrimSpace(cfg.EndpointID) != "" {
				dispatchURL, err := BuildDispatchURL(s.publicOrigin.String(), cfg.EndpointID)
				return cfg.EndpointID, dispatchURL, err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", err
		}
	}
	endpointID, err := s.keyring.GenerateEndpointID(s.random)
	if err != nil {
		return "", "", err
	}
	dispatchURL, err := BuildDispatchURL(s.publicOrigin.String(), endpointID)
	return endpointID, dispatchURL, err
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
		config, parseErr := ParseDingTalkAccountConfig(row.Config)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: stored binding config", ErrInvalidResult)
		}
		if row.Status == "active" && config.SurfaceType == "" && config.RouterSourceID != "" {
			subscription, verifyErr := s.verifySubscription(ctx, config.RouterSourceID, row, config)
			if verifyErr != nil {
				return nil, verifyErr
			}
			config.SurfaceType = subscription.Surface.Type
			row.Config, parseErr = config.Marshal()
			if parseErr != nil {
				return nil, fmt.Errorf("%w: current binding surface", ErrInvalidResult)
			}
		}
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
	identity := params.IdentityBinding
	if params.BindingMode == BindingModeIdentity {
		var identityErr error
		identity, identityErr = normalizeIdentityBindingResult(identity)
		if identityErr != nil {
			return PublicDingTalkAccountBinding{}, ErrInvalidResult
		}
	}
	messageScope, conversations, validationErr := validateCompleteBindingParams(CompleteBindingParams{
		BindingID:     params.BindingID,
		BindingMode:   params.BindingMode,
		CallbackToken: params.CallbackToken,
		Status:        DingTalkBindingCompletionStatus,
		Identity:      identity,
		Message:       params.MessageBinding,
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
		// exact agent and dispatch endpoint. Never DELETE an unverified body sourceId.
		if errors.Is(err, ErrBindingConflict) && subscription.SourceID == sourceID &&
			subscription.AgentID == util.UUIDToString(row.AgentID) &&
			s.subscriptionDispatchTargetMatches(subscription.DispatchURL, config.DispatchEndpointID) {
			if err := s.router.DeleteSubscription(ctx, subscription.SourceID); err != nil {
				return PublicDingTalkAccountBinding{}, ErrRouterUnavailable
			}
		}
		return PublicDingTalkAccountBinding{}, err
	}
	boundAt := s.now().UTC()
	config.RouterSourceID = sourceID
	config.AccountDisplayName = strings.TrimSpace(params.MessageBinding.AccountDisplayName)
	config.AccountAvatarURL = strings.TrimSpace(params.MessageBinding.AccountAvatarURL)
	config.SurfaceType = subscription.Surface.Type
	config.MessageRouteStatus = ""
	config.MessageRouteError = nil
	config.MessageScope = messageScope
	config.CalendarStartEnabled = hasActiveCalendarSubscription(params.MessageBinding.Subscriptions)
	config.Conversations = conversations
	config.BoundAt = &boundAt
	activeConfig, err := config.Marshal()
	if err != nil {
		return PublicDingTalkAccountBinding{}, fmt.Errorf("%w: active binding config", ErrInvalidResult)
	}
	activated, err := s.store.ActivateDingTalkAccountBinding(ctx, db.ActivateDingTalkAccountBindingParams{
		Config:                    activeConfig,
		ID:                        row.ID,
		WorkspaceID:               row.WorkspaceID,
		AgentID:                   row.AgentID,
		ExpectedCallbackTokenHash: config.CallbackTokenHash,
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
			// dispatch endpoint. If the local CAS lost to a newer pending attempt, an
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
	return s.publicBinding(ctx, activated)
}

func (s *Service) UpdateSurface(ctx context.Context, params UpdateSurfaceParams) (PublicDingTalkAccountBinding, error) {
	if s == nil || s.store == nil || s.router == nil || s.identityStore == nil {
		return PublicDingTalkAccountBinding{}, ErrNotConfigured
	}
	if !params.WorkspaceID.Valid || !params.AgentID.Valid {
		return PublicDingTalkAccountBinding{}, ErrNotFound
	}
	params.SurfaceType = strings.TrimSpace(params.SurfaceType)
	if !validDingTalkSurfaceType(params.SurfaceType) {
		return PublicDingTalkAccountBinding{}, ErrInvalidResult
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
	if row.Status != "active" {
		return PublicDingTalkAccountBinding{}, ErrBindingConflict
	}
	config, err := ParseDingTalkAccountConfig(row.Config)
	if err != nil || strings.TrimSpace(config.RouterSourceID) == "" {
		return PublicDingTalkAccountBinding{}, ErrInvalidResult
	}
	current, err := s.verifySubscription(ctx, config.RouterSourceID, row, config)
	if err != nil {
		return PublicDingTalkAccountBinding{}, err
	}
	updated, err := s.router.UpdateSubscriptionSurface(
		ctx,
		config.RouterSourceID,
		util.UUIDToString(row.AgentID),
		params.SurfaceType,
	)
	if err != nil {
		return PublicDingTalkAccountBinding{}, ErrRouterUnavailable
	}
	if s.subscriptionVerificationOutcome(updated, config.RouterSourceID, row, config) != "success" ||
		updated.Surface.Type != params.SurfaceType || updated.Outbound != current.Outbound {
		return PublicDingTalkAccountBinding{}, ErrBindingConflict
	}
	stored, err := s.store.UpdateDingTalkAccountBindingSurface(ctx, db.UpdateDingTalkAccountBindingSurfaceParams{
		WorkspaceID:            row.WorkspaceID,
		AgentID:                row.AgentID,
		SurfaceType:            params.SurfaceType,
		ExpectedRouterSourceID: config.RouterSourceID,
	})
	if err != nil {
		if validDingTalkSurfaceType(current.Surface.Type) {
			if _, restoreErr := s.router.UpdateSubscriptionSurface(
				ctx,
				config.RouterSourceID,
				util.UUIDToString(row.AgentID),
				current.Surface.Type,
			); restoreErr != nil {
				return PublicDingTalkAccountBinding{}, ErrRouterUnavailable
			}
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicDingTalkAccountBinding{}, ErrBindingConflict
		}
		return PublicDingTalkAccountBinding{}, fmt.Errorf("update dingtalk account binding surface: %w", err)
	}
	return s.publicBinding(ctx, stored)
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
		route, routeErr := s.store.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{
			WorkspaceID: params.WorkspaceID,
			AgentID:     params.AgentID,
		})
		if routeErr == nil {
			return s.publicBinding(ctx, route)
		}
		if !errors.Is(routeErr, pgx.ErrNoRows) {
			return PublicDingTalkAccountBinding{}, fmt.Errorf("get dingtalk account binding: %w", routeErr)
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
	if _, err := ParseDingTalkAccountConfig(row.Config); err != nil {
		return PublicDingTalkAccountBinding{}, fmt.Errorf("%w: stored binding config", ErrInvalidResult)
	}
	// A calendar-enabled account owns multiple Router sources. Resolve them
	// from the stable agent ID, rather than the mutable local source ID, so a
	// retry also heals calendar sources orphaned by the legacy single-source
	// unbind path. Router returns an empty success when nothing remains.
	if err := s.router.DeleteDigitalEmployeeSubscriptions(
		ctx,
		util.UUIDToString(row.AgentID),
	); err != nil {
		return PublicDingTalkAccountBinding{}, ErrRouterUnavailable
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
	outcome := s.subscriptionVerificationOutcome(subscription, sourceID, row, config)
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

func (s *Service) subscriptionVerificationOutcome(subscription Subscription, sourceID string, row db.ChannelInstallation, config DingTalkAccountConfig) string {
	if subscription.SourceID != sourceID {
		return "source_mismatch"
	}
	if subscription.AgentID != util.UUIDToString(row.AgentID) {
		return "agent_mismatch"
	}
	if !s.subscriptionDispatchTargetMatches(subscription.DispatchURL, config.DispatchEndpointID) {
		return "dispatch_url_mismatch"
	}
	if subscription.Status != "active" {
		return "inactive"
	}
	return "success"
}

func (s *Service) subscriptionDispatchTargetMatches(dispatchTarget, endpointID string) bool {
	expectedPath, err := dispatchPathForEndpointID(endpointID)
	if err != nil {
		return false
	}
	if dispatchTarget == expectedPath {
		return true
	}
	if s == nil || s.publicOrigin == nil {
		return false
	}
	expectedURL, err := BuildDispatchURL(s.publicOrigin.String(), endpointID)
	return err == nil && dispatchTarget == expectedURL
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
		dwsIdentity = PublicDingTalkBindingOutcome{
			Status:             "active",
			Source:             string(BindingModeIdentity),
			OrganizationName:   identity.OrganizationName,
			AccountDisplayName: identity.AccountDisplayName,
			AccountAvatarURL:   identity.AccountAvatarUrl,
			BoundAt:            &boundAt,
		}
	} else if !errors.Is(identityErr, pgx.ErrNoRows) {
		return PublicDingTalkAccountBinding{}, fmt.Errorf("load dingtalk identity: %w", identityErr)
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
