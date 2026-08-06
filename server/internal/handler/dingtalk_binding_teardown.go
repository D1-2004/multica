package handler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type DingTalkBindingTeardownRouter interface {
	UnbindDigitalEmployeeBinding(
		context.Context,
		agentmessagerouter.DigitalEmployeeBindingKey,
	) (agentmessagerouter.DigitalEmployeeBindingUnbindResult, error)
	GetDigitalEmployeeSourceIdentities(
		context.Context,
		[]string,
	) (agentmessagerouter.DigitalEmployeeSourceIdentityResult, error)
}

type dingTalkBindingTeardownStore interface {
	LockAgents(context.Context, []pgtype.UUID) ([]db.Agent, error)
	GetBindingForUpdate(context.Context, pgtype.UUID) (db.ChannelInstallation, error)
	BackfillBindingAccountKey(
		context.Context,
		db.ChannelInstallation,
		agentmessagerouter.DigitalEmployeeSourceIdentity,
	) (db.ChannelInstallation, error)
	DeleteBindingProjection(
		context.Context,
		db.ChannelInstallation,
		agentmessagerouter.DingTalkAccountConfig,
	) error
}

type dbDingTalkBindingTeardownStore struct {
	queries *db.Queries
}

func (s dbDingTalkBindingTeardownStore) LockAgents(
	ctx context.Context,
	agentIDs []pgtype.UUID,
) ([]db.Agent, error) {
	return s.queries.LockAgentsForDingTalkBindingTeardown(ctx, agentIDs)
}

func (s dbDingTalkBindingTeardownStore) GetBindingForUpdate(
	ctx context.Context,
	agentID pgtype.UUID,
) (db.ChannelInstallation, error) {
	return s.queries.GetDingTalkAccountBindingByAgentForUpdate(ctx, agentID)
}

func (s dbDingTalkBindingTeardownStore) BackfillBindingAccountKey(
	ctx context.Context,
	row db.ChannelInstallation,
	identity agentmessagerouter.DigitalEmployeeSourceIdentity,
) (db.ChannelInstallation, error) {
	config, err := agentmessagerouter.ParseDingTalkAccountConfig(row.Config)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	return s.queries.BackfillDingTalkAccountRouterAccountKey(ctx, db.BackfillDingTalkAccountRouterAccountKeyParams{
		RouterPlatform: identity.Platform, RouterTenantID: identity.TenantID, RouterAccountID: identity.AccountID,
		ID: row.ID, WorkspaceID: row.WorkspaceID, AgentID: row.AgentID,
		ExpectedStatus: row.Status, ExpectedRouterSourceID: config.RouterSourceID,
		ExpectedConfig: append([]byte(nil), row.Config...),
	})
}

func (s dbDingTalkBindingTeardownStore) DeleteBindingProjection(
	ctx context.Context,
	row db.ChannelInstallation,
	config agentmessagerouter.DingTalkAccountConfig,
) error {
	deleted, err := s.queries.DeleteDingTalkAccountBindingProjectionForTeardown(
		ctx,
		db.DeleteDingTalkAccountBindingProjectionForTeardownParams{
			ID: row.ID, WorkspaceID: row.WorkspaceID, AgentID: row.AgentID,
			ExpectedRouterPlatform:  config.RouterPlatform,
			ExpectedRouterTenantID:  config.RouterTenantID,
			ExpectedRouterAccountID: config.RouterAccountID,
		},
	)
	if err != nil {
		return err
	}
	if deleted.ID != row.ID || deleted.WorkspaceID != row.WorkspaceID || deleted.AgentID != row.AgentID {
		return errors.New("dingtalk binding teardown projection changed")
	}
	return nil
}

func teardownDingTalkBindings(
	ctx context.Context,
	store dingTalkBindingTeardownStore,
	router DingTalkBindingTeardownRouter,
	agentIDs []pgtype.UUID,
) error {
	_, err := teardownDingTalkBindingsLocked(ctx, store, router, agentIDs, "")
	return err
}

func teardownDingTalkBindingsLocked(
	ctx context.Context,
	store dingTalkBindingTeardownStore,
	router DingTalkBindingTeardownRouter,
	agentIDs []pgtype.UUID,
	publicBaseURL string,
) ([]db.Agent, error) {
	if store == nil {
		return nil, errors.New("dingtalk binding teardown store is not configured")
	}
	normalized, err := normalizeDingTalkBindingTeardownAgentIDs(agentIDs)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return []db.Agent{}, nil
	}
	locked, err := store.LockAgents(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("lock agents for dingtalk binding teardown: %w", err)
	}
	if !sameLockedDingTalkBindingAgents(normalized, locked) {
		return nil, errors.New("dingtalk binding teardown agent set changed")
	}

	for _, agentID := range normalized {
		row, lookupErr := store.GetBindingForUpdate(ctx, agentID)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			continue
		}
		if lookupErr != nil {
			return nil, fmt.Errorf("lock dingtalk binding projection: %w", lookupErr)
		}
		if row.AgentID != agentID || row.ChannelType != agentmessagerouter.ChannelTypeDingTalkAccount {
			return nil, errors.New("dingtalk binding teardown projection changed")
		}
		switch row.Status {
		case "revoked":
			continue
		case "active":
		case "pending":
			return nil, errors.New("dingtalk binding is still pending")
		default:
			return nil, errors.New("dingtalk binding status is invalid")
		}
		if router == nil {
			return nil, errors.New("dingtalk binding Router is not configured")
		}

		config, parseErr := agentmessagerouter.ParseDingTalkAccountConfig(row.Config)
		if parseErr != nil {
			return nil, errors.New("dingtalk binding account key is invalid")
		}
		if !completeDingTalkBindingAccountKey(config) {
			identity, resolveErr := resolveLegacyDingTalkBindingAccountKey(ctx, router, row, config, publicBaseURL)
			if resolveErr != nil {
				return nil, resolveErr
			}
			updated, updateErr := store.BackfillBindingAccountKey(ctx, row, identity)
			if updateErr != nil {
				return nil, fmt.Errorf("backfill dingtalk binding account key: %w", updateErr)
			}
			updatedConfig, updatedParseErr := agentmessagerouter.ParseDingTalkAccountConfig(updated.Config)
			if updatedParseErr != nil || updated.ID != row.ID || updated.WorkspaceID != row.WorkspaceID ||
				updated.AgentID != row.AgentID || updated.Status != row.Status ||
				updatedConfig.RouterSourceID != config.RouterSourceID || !completeDingTalkBindingAccountKey(updatedConfig) ||
				updatedConfig.RouterPlatform != identity.Platform || updatedConfig.RouterTenantID != identity.TenantID ||
				updatedConfig.RouterAccountID != identity.AccountID {
				return nil, errors.New("dingtalk binding account key changed during enrichment")
			}
			row = updated
			config = updatedConfig
		}

		result, unbindErr := router.UnbindDigitalEmployeeBinding(ctx, agentmessagerouter.DigitalEmployeeBindingKey{
			AgentID: agentID.String(), Platform: config.RouterPlatform,
			TenantID: config.RouterTenantID, AccountID: config.RouterAccountID,
		})
		if unbindErr != nil {
			return nil, fmt.Errorf("conditionally unbind dingtalk account: %w", unbindErr)
		}
		if result.Status != "unbound" && result.Status != "ownership_changed" {
			return nil, errors.New("conditional dingtalk account unbind was not safe")
		}
		if deleteErr := store.DeleteBindingProjection(ctx, row, config); deleteErr != nil {
			return nil, fmt.Errorf("delete dingtalk binding projection: %w", deleteErr)
		}
	}
	return locked, nil
}

func normalizeDingTalkBindingTeardownAgentIDs(agentIDs []pgtype.UUID) ([]pgtype.UUID, error) {
	unique := make(map[pgtype.UUID]struct{}, len(agentIDs))
	for _, agentID := range agentIDs {
		if !agentID.Valid {
			return nil, errors.New("dingtalk binding teardown agent id is invalid")
		}
		unique[agentID] = struct{}{}
	}
	normalized := make([]pgtype.UUID, 0, len(unique))
	for agentID := range unique {
		normalized = append(normalized, agentID)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].String() < normalized[j].String() })
	return normalized, nil
}

func sameLockedDingTalkBindingAgents(agentIDs []pgtype.UUID, locked []db.Agent) bool {
	if len(agentIDs) != len(locked) {
		return false
	}
	for index, agentID := range agentIDs {
		if locked[index].ID != agentID {
			return false
		}
	}
	return true
}

func sameDingTalkBindingAgentSet(agentIDs []pgtype.UUID, agents []db.Agent) bool {
	if len(agentIDs) != len(agents) {
		return false
	}
	expected := make(map[pgtype.UUID]struct{}, len(agentIDs))
	for _, agentID := range agentIDs {
		expected[agentID] = struct{}{}
	}
	for _, agent := range agents {
		if _, ok := expected[agent.ID]; !ok {
			return false
		}
		delete(expected, agent.ID)
	}
	return len(expected) == 0
}

func completeDingTalkBindingAccountKey(config agentmessagerouter.DingTalkAccountConfig) bool {
	return config.RouterPlatform == "dingtalk" &&
		validDingTalkBindingRouterIdentifier(config.RouterTenantID) &&
		validDingTalkBindingRouterIdentifier(config.RouterAccountID)
}

func resolveLegacyDingTalkBindingAccountKey(
	ctx context.Context,
	router DingTalkBindingTeardownRouter,
	row db.ChannelInstallation,
	config agentmessagerouter.DingTalkAccountConfig,
	publicBaseURL string,
) (agentmessagerouter.DigitalEmployeeSourceIdentity, error) {
	sourceID := config.RouterSourceID
	if !validDingTalkBindingRouterIdentifier(sourceID) {
		return agentmessagerouter.DigitalEmployeeSourceIdentity{}, errors.New("legacy dingtalk binding source is invalid")
	}
	result, err := router.GetDigitalEmployeeSourceIdentities(ctx, []string{sourceID})
	if err != nil {
		return agentmessagerouter.DigitalEmployeeSourceIdentity{}, fmt.Errorf("resolve legacy dingtalk binding account key: %w", err)
	}
	for _, missing := range result.MissingSourceIDs {
		if missing == sourceID {
			return agentmessagerouter.DigitalEmployeeSourceIdentity{}, errors.New("legacy dingtalk binding source was not found")
		}
	}
	if len(result.Sources) != 1 {
		return agentmessagerouter.DigitalEmployeeSourceIdentity{}, errors.New("legacy dingtalk binding source is ambiguous")
	}
	identity := result.Sources[0]
	if identity.SourceID != sourceID || identity.Platform != "dingtalk" ||
		identity.SourceType != "digital_employee" || identity.Domain != "channel" ||
		!validDingTalkBindingRouterIdentifier(identity.TenantID) ||
		!validDingTalkBindingRouterIdentifier(identity.AccountID) {
		return agentmessagerouter.DigitalEmployeeSourceIdentity{}, errors.New("legacy dingtalk binding source identity is inconsistent")
	}
	return identity, nil
}

func validDingTalkBindingRouterIdentifier(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 256 &&
		utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func (h *Handler) teardownDingTalkBindings(
	ctx context.Context,
	queries *db.Queries,
	agentIDs []pgtype.UUID,
) ([]db.Agent, error) {
	if h == nil || queries == nil {
		return nil, errors.New("dingtalk binding teardown is not configured")
	}
	return teardownDingTalkBindingsLocked(
		ctx,
		dbDingTalkBindingTeardownStore{queries: queries},
		h.DingTalkBindingTeardownRouter,
		agentIDs,
		h.currentConfig().PublicURL,
	)
}
