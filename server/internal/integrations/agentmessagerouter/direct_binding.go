package agentmessagerouter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type DirectBindingParams struct {
	Agent             BeginAgent
	InitiatorID       pgtype.UUID
	TenantID          string
	DigitalEmployeeID string
	SurfaceType       string
	MessageScope      string
	EnabledDomains    []string
	Conversations     []DingTalkConversationSnapshot
}

type DirectDigitalEmployeeBinding struct {
	WorkspaceID         string                         `json:"workspace_id"`
	AgentID             string                         `json:"agent_id"`
	TenantID            string                         `json:"tenant_id,omitempty"`
	DigitalEmployeeID   string                         `json:"digital_employee_id,omitempty"`
	Status              string                         `json:"status"`
	RouterBindingStatus string                         `json:"router_binding_status"`
	RouterBindingOwner  string                         `json:"router_binding_owner,omitempty"`
	SurfaceType         string                         `json:"surface_type,omitempty"`
	MessageScope        string                         `json:"message_scope,omitempty"`
	EnabledDomains      []string                       `json:"enabled_domains,omitempty"`
	Conversations       []DingTalkConversationSnapshot `json:"conversations,omitempty"`
	BoundAt             *time.Time                     `json:"bound_at,omitempty"`
	UpdatedAt           *time.Time                     `json:"updated_at,omitempty"`
	RetryStatus         string                         `json:"retry_status"`
}

type directSubscriptionCreator interface {
	CreateHTTPCallbackSubscription(context.Context, CreateSubscriptionParams) (Subscription, error)
}

func (s *Service) BindDigitalEmployee(ctx context.Context, params DirectBindingParams) (DirectDigitalEmployeeBinding, error) {
	if !params.Agent.Workspace.ID.Valid || !params.Agent.ID.Valid || !params.InitiatorID.Valid {
		return DirectDigitalEmployeeBinding{}, ErrNotFound
	}
	if s == nil || s.store == nil || s.router == nil || s.endpoints == nil || s.random == nil {
		return DirectDigitalEmployeeBinding{}, ErrNotConfigured
	}
	creator, ok := s.router.(directSubscriptionCreator)
	if !ok {
		return DirectDigitalEmployeeBinding{}, ErrNotConfigured
	}

	params.TenantID = strings.TrimSpace(params.TenantID)
	params.DigitalEmployeeID = strings.TrimSpace(params.DigitalEmployeeID)
	params.SurfaceType = strings.TrimSpace(params.SurfaceType)
	if params.SurfaceType == "" {
		params.SurfaceType = DingTalkSurfaceAuto
	}
	if !validRouterIdentifier(params.TenantID) || !validRouterIdentifier(params.DigitalEmployeeID) ||
		!validDingTalkSurfaceType(params.SurfaceType) {
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}
	messageScope, conversations, err := normalizeDingTalkConversationBinding(params.MessageScope, params.Conversations)
	if err != nil {
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}
	domains := params.EnabledDomains
	if len(domains) == 0 {
		domains = []string{"channel"}
	}
	domains, err = normalizeBindingDomains(domains)
	if err != nil || !containsBindingDomain(domains, "channel") {
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}

	existing, err := s.store.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{
		WorkspaceID: params.Agent.Workspace.ID,
		AgentID:     params.Agent.ID,
	})
	if err == nil && existing.Status == "active" {
		config, parseErr := ParseDingTalkAccountConfig(existing.Config)
		if parseErr != nil {
			return DirectDigitalEmployeeBinding{}, ErrInvalidResult
		}
		if config.RouterPlatform == "dingtalk" && config.RouterTenantID == params.TenantID &&
			config.RouterAccountID == params.DigitalEmployeeID {
			return s.GetDigitalEmployeeBinding(ctx, params.Agent.Workspace.ID, params.Agent.ID)
		}
		return DirectDigitalEmployeeBinding{}, ErrAlreadyActive
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DirectDigitalEmployeeBinding{}, fmt.Errorf("get dingtalk account binding: %w", err)
	}

	agentName, err := normalizeBindingDisplayName("agent name", params.Agent.Name)
	if err != nil {
		return DirectDigitalEmployeeBinding{}, fmt.Errorf("%w: agent descriptor", ErrInvalidResult)
	}
	workspaceName, err := normalizeBindingDisplayName("workspace name", params.Agent.Workspace.Name)
	if err != nil {
		return DirectDigitalEmployeeBinding{}, fmt.Errorf("%w: workspace descriptor", ErrInvalidResult)
	}
	endpoint, err := s.endpoints.Ensure(ctx, params.Agent.Workspace.ID, params.Agent.ID, params.InitiatorID)
	if err != nil {
		return DirectDigitalEmployeeBinding{}, fmt.Errorf("%w: endpoint resolution failed", ErrNotConfigured)
	}
	dispatchPath, err := dispatchPathForEndpointID(endpoint.EndpointID)
	if err != nil {
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}
	_, callbackHash, err := GenerateCallbackToken(s.random)
	if err != nil {
		return DirectDigitalEmployeeBinding{}, fmt.Errorf("%w: local credential generation failed", ErrNotConfigured)
	}
	pendingConfig := NewPendingDingTalkAccountConfig(
		endpoint.EndpointID,
		dispatchPath,
		callbackHash,
		s.now().UTC().Add(s.callbackTTL),
	)
	pendingConfig.RouterPlatform = "dingtalk"
	pendingConfig.RouterTenantID = params.TenantID
	pendingConfig.RouterAccountID = params.DigitalEmployeeID
	pendingConfig.SurfaceType = params.SurfaceType
	pendingConfig.MessageScope = messageScope
	pendingConfig.EnabledDomains = domains
	pendingConfig.Conversations = conversations
	pendingRaw, err := pendingConfig.Marshal()
	if err != nil {
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}
	row, err := s.store.BeginDingTalkAccountBinding(ctx, db.BeginDingTalkAccountBindingParams{
		WorkspaceID:     params.Agent.Workspace.ID,
		AgentID:         params.Agent.ID,
		Config:          pendingRaw,
		InstallerUserID: params.InitiatorID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DirectDigitalEmployeeBinding{}, ErrBindingConflict
		}
		return DirectDigitalEmployeeBinding{}, fmt.Errorf("begin dingtalk account binding: %w", err)
	}
	if row.Status != "pending" || row.ChannelType != ChannelTypeDingTalkAccount ||
		row.WorkspaceID != params.Agent.Workspace.ID || row.AgentID != params.Agent.ID {
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}
	config, err := ParseDingTalkAccountConfig(row.Config)
	if err != nil || config.RouterPlatform != "dingtalk" || config.RouterTenantID != params.TenantID ||
		config.RouterAccountID != params.DigitalEmployeeID {
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}

	bindingKey := DigitalEmployeeBindingKey{
		AgentID:         util.UUIDToString(params.Agent.ID),
		Platform:        "dingtalk",
		TenantID:        params.TenantID,
		AccountID:       params.DigitalEmployeeID,
		ExpectedDomains: append([]string(nil), domains...),
	}
	precheck, err := s.checkDirectDigitalEmployeeBinding(ctx, bindingKey)
	if err != nil {
		return DirectDigitalEmployeeBinding{}, err
	}
	if precheck.Status == "bound_to_other_agent" || precheck.Status == "inconsistent" {
		return DirectDigitalEmployeeBinding{}, ErrBindingConflict
	}
	if precheck.Status != "unbound" && precheck.Status != "valid" {
		return DirectDigitalEmployeeBinding{}, ErrRouterUnavailable
	}

	descriptor, err := normalizeAgentDescriptor(AgentDescriptor{
		AgentID: bindingKey.AgentID,
		Name:    agentName,
		Workspace: WorkspaceDescriptor{
			ID:   util.UUIDToString(params.Agent.Workspace.ID),
			Name: workspaceName,
		},
		DispatchPath: dispatchPath,
	})
	if err != nil {
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}
	issued, err := s.router.IssueBindingToken(ctx, descriptor)
	if err != nil || strings.TrimSpace(issued.BindingToken) == "" || !issued.ExpiresAt.After(s.now()) {
		return DirectDigitalEmployeeBinding{}, ErrRouterUnavailable
	}
	subscription, createErr := creator.CreateHTTPCallbackSubscription(ctx, CreateSubscriptionParams{
		TenantID:           bindingKey.TenantID,
		AccountID:          bindingKey.AccountID,
		AgentID:            bindingKey.AgentID,
		DispatchURL:        endpoint.DispatchURL,
		BindingToken:       issued.BindingToken,
		SubscriptionConfig: map[string]any{"upstreamMode": "HTTP_CALLBACK"},
		EnabledDomains:     append([]string(nil), domains...),
		Surface:            SubscriptionSurface{Type: params.SurfaceType},
		Outbound:           SubscriptionOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ReplaceExisting:    false,
	})
	if createErr != nil {
		if precheck.Status == "unbound" {
			if check, checkErr := s.checkDirectDigitalEmployeeBinding(ctx, bindingKey); checkErr == nil && check.Status == "valid" {
				if !s.directBindingAttemptIsCurrent(ctx, row, config.CallbackTokenHash, bindingKey) {
					return DirectDigitalEmployeeBinding{}, ErrBindingConflict
				}
				if compensationErr := s.compensateDirectDigitalEmployeeBinding(ctx, bindingKey); compensationErr != nil {
					return DirectDigitalEmployeeBinding{}, ErrRouterUnavailable
				}
			}
		}
		var routerErr *RouterAPIError
		if errors.As(createErr, &routerErr) &&
			(routerErr.Code == "source_already_bound" || routerErr.Code == "agent_already_bound") {
			return DirectDigitalEmployeeBinding{}, ErrBindingConflict
		}
		return DirectDigitalEmployeeBinding{}, ErrRouterUnavailable
	}
	if subscription.Status != "active" || subscription.AgentID != bindingKey.AgentID ||
		!isTrimmedNonEmpty(subscription.SourceID) ||
		!s.subscriptionDispatchTargetMatches(subscription.DispatchURL, config.DispatchEndpointID) ||
		subscription.Surface != (SubscriptionSurface{Type: params.SurfaceType}) ||
		subscription.Outbound != (SubscriptionOutbound{Mode: "dws", ReplyTo: "latest_message"}) {
		if precheck.Status == "unbound" {
			if !s.directBindingAttemptIsCurrent(ctx, row, config.CallbackTokenHash, bindingKey) {
				return DirectDigitalEmployeeBinding{}, ErrBindingConflict
			}
			if compensationErr := s.compensateDirectDigitalEmployeeBinding(ctx, bindingKey); compensationErr != nil {
				return DirectDigitalEmployeeBinding{}, ErrRouterUnavailable
			}
		}
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}
	result, activateErr := s.activateDirectDigitalEmployeeBinding(ctx, row, config, subscription.SourceID)
	if activateErr == nil {
		return result, nil
	}
	if precheck.Status == "unbound" {
		if !s.directBindingAttemptIsCurrent(ctx, row, config.CallbackTokenHash, bindingKey) {
			return DirectDigitalEmployeeBinding{}, activateErr
		}
		if compensationErr := s.compensateDirectDigitalEmployeeBinding(ctx, bindingKey); compensationErr != nil {
			return DirectDigitalEmployeeBinding{}, ErrRouterUnavailable
		}
	}
	return DirectDigitalEmployeeBinding{}, activateErr
}

func (s *Service) GetDigitalEmployeeBinding(
	ctx context.Context,
	workspaceID, agentID pgtype.UUID,
) (DirectDigitalEmployeeBinding, error) {
	if s == nil || s.store == nil || s.router == nil {
		return DirectDigitalEmployeeBinding{}, ErrNotConfigured
	}
	if !workspaceID.Valid || !agentID.Valid {
		return DirectDigitalEmployeeBinding{}, ErrNotFound
	}
	row, err := s.store.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return directUnboundDigitalEmployeeBinding(workspaceID, agentID), nil
	}
	if err != nil {
		return DirectDigitalEmployeeBinding{}, fmt.Errorf("get dingtalk account binding: %w", err)
	}
	config, err := ParseDingTalkAccountConfig(row.Config)
	if err != nil {
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}
	result := directDigitalEmployeeBindingFromRow(row, config)
	if row.Status == "revoked" {
		result.Status = "unbound"
		result.RouterBindingStatus = "unbound"
		result.RetryStatus = "not_required"
		return result, nil
	}
	if !completeDigitalEmployeeAccountKey(config) {
		result.RouterBindingStatus = "inconsistent"
		result.RetryStatus = "manual_review"
		return result, nil
	}
	check, checkErr := s.checkDirectDigitalEmployeeBinding(ctx, DigitalEmployeeBindingKey{
		AgentID:         util.UUIDToString(row.AgentID),
		Platform:        config.RouterPlatform,
		TenantID:        config.RouterTenantID,
		AccountID:       config.RouterAccountID,
		ExpectedDomains: config.bindingDomains(),
	})
	if checkErr != nil {
		result.RouterBindingStatus = "router_unavailable"
		result.RetryStatus = "retryable"
		return result, nil
	}
	result.RouterBindingStatus = check.Status
	if check.Status == "valid" {
		result.RouterBindingOwner = util.UUIDToString(row.AgentID)
	} else if check.Status == "bound_to_other_agent" {
		result.RouterBindingOwner = check.CurrentAgentID
	}
	if row.Status == "active" && check.Status == "valid" {
		result.RetryStatus = "not_required"
	} else if check.Status == "unbound" {
		result.RetryStatus = "retryable"
	} else {
		result.RetryStatus = "manual_review"
	}
	return result, nil
}

func (s *Service) activateDirectDigitalEmployeeBinding(
	ctx context.Context,
	row db.ChannelInstallation,
	config DingTalkAccountConfig,
	sourceID string,
) (DirectDigitalEmployeeBinding, error) {
	expectedCallbackHash := config.CallbackTokenHash
	boundAt := s.now().UTC()
	config.RouterSourceID = strings.TrimSpace(sourceID)
	config.MessageRouteStatus = ""
	config.MessageRouteError = nil
	config.CallbackTokenHash = ""
	config.CallbackExpiresAt = time.Time{}
	config.BoundAt = &boundAt
	activeRaw, err := config.Marshal()
	if err != nil {
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}
	activated, err := s.store.ActivateDingTalkAccountBinding(ctx, db.ActivateDingTalkAccountBindingParams{
		Config:                    activeRaw,
		ID:                        row.ID,
		WorkspaceID:               row.WorkspaceID,
		AgentID:                   row.AgentID,
		ExpectedCallbackTokenHash: expectedCallbackHash,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DirectDigitalEmployeeBinding{}, ErrBindingConflict
		}
		return DirectDigitalEmployeeBinding{}, fmt.Errorf("activate dingtalk account binding: %w", err)
	}
	activeConfig, err := ParseDingTalkAccountConfig(activated.Config)
	if err != nil {
		return DirectDigitalEmployeeBinding{}, ErrInvalidResult
	}
	if s.responsePolicyNotifier != nil {
		s.responsePolicyNotifier.NotifyResponsePolicyChanged()
	}
	result := directDigitalEmployeeBindingFromRow(activated, activeConfig)
	result.RouterBindingStatus = "valid"
	result.RouterBindingOwner = util.UUIDToString(activated.AgentID)
	result.RetryStatus = "not_required"
	return result, nil
}

func (s *Service) checkDirectDigitalEmployeeBinding(
	ctx context.Context,
	binding DigitalEmployeeBindingKey,
) (DigitalEmployeeBindingCheck, error) {
	checks, err := s.router.CheckDigitalEmployeeBindings(ctx, []DigitalEmployeeBindingKey{binding})
	if err != nil || len(checks) != 1 {
		return DigitalEmployeeBindingCheck{}, ErrRouterUnavailable
	}
	check := checks[0]
	if check.AgentID != binding.AgentID || check.Platform != binding.Platform ||
		check.TenantID != binding.TenantID || check.AccountID != binding.AccountID ||
		!validDigitalEmployeeBindingCheck(check) {
		return DigitalEmployeeBindingCheck{}, ErrRouterUnavailable
	}
	return check, nil
}

func (s *Service) compensateDirectDigitalEmployeeBinding(ctx context.Context, binding DigitalEmployeeBindingKey) error {
	result, err := s.router.UnbindDigitalEmployeeBinding(ctx, DigitalEmployeeBindingKey{
		AgentID: binding.AgentID, Platform: binding.Platform, TenantID: binding.TenantID, AccountID: binding.AccountID,
	})
	if err != nil || (result.Status != "unbound" && result.Status != "ownership_changed") {
		return ErrRouterUnavailable
	}
	return nil
}

func (s *Service) directBindingAttemptIsCurrent(
	ctx context.Context,
	row db.ChannelInstallation,
	callbackHash string,
	binding DigitalEmployeeBindingKey,
) bool {
	current, err := s.store.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{
		WorkspaceID: row.WorkspaceID,
		AgentID:     row.AgentID,
	})
	if err != nil || current.ID != row.ID || current.WorkspaceID != row.WorkspaceID ||
		current.AgentID != row.AgentID || current.Status != "pending" {
		return false
	}
	config, err := ParseDingTalkAccountConfig(current.Config)
	return err == nil && callbackHash != "" && config.CallbackTokenHash == callbackHash &&
		config.RouterPlatform == binding.Platform && config.RouterTenantID == binding.TenantID &&
		config.RouterAccountID == binding.AccountID
}

func directDigitalEmployeeBindingFromRow(row db.ChannelInstallation, config DingTalkAccountConfig) DirectDigitalEmployeeBinding {
	result := DirectDigitalEmployeeBinding{
		WorkspaceID:       util.UUIDToString(row.WorkspaceID),
		AgentID:           util.UUIDToString(row.AgentID),
		TenantID:          config.RouterTenantID,
		DigitalEmployeeID: config.RouterAccountID,
		Status:            row.Status,
		SurfaceType:       config.SurfaceType,
		MessageScope:      config.MessageScope,
		EnabledDomains:    append([]string(nil), config.EnabledDomains...),
		Conversations:     append([]DingTalkConversationSnapshot(nil), config.Conversations...),
		BoundAt:           config.BoundAt,
		RetryStatus:       "retryable",
	}
	if row.UpdatedAt.Valid {
		updatedAt := row.UpdatedAt.Time
		result.UpdatedAt = &updatedAt
	}
	return result
}

func directUnboundDigitalEmployeeBinding(workspaceID, agentID pgtype.UUID) DirectDigitalEmployeeBinding {
	return DirectDigitalEmployeeBinding{
		WorkspaceID: util.UUIDToString(workspaceID), AgentID: util.UUIDToString(agentID),
		Status: "unbound", RouterBindingStatus: "unbound", RetryStatus: "not_required",
	}
}
