package agentmessagerouter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	DingTalkBindingTaskStatusSuccess = "success"
	DingTalkBindingTaskStatusFailed  = "failed"
	DingTalkBindingTaskStatusSkipped = "skipped"
	DingTalkBindingCompletionStatus  = "completed"
	maxBindingTaskErrorCodeBytes     = 64
	maxBindingTaskErrorMessageRunes  = 256
)

type BindingTaskError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type IdentityBindingResult struct {
	Status                  string            `json:"status"`
	AccountUID              string            `json:"account_uid"`
	AccountOrgID            string            `json:"account_org_id"`
	AccountOrganizationName string            `json:"account_organization_name"`
	AccountDisplayName      string            `json:"account_display_name"`
	AccountAvatarURL        string            `json:"account_avatar_url"`
	Error                   *BindingTaskError `json:"error"`
}

type BindingSubscriptionResult struct {
	Domain   string `json:"domain"`
	SourceID string `json:"source_id"`
	Status   string `json:"status"`
}

type MessageBindingResult struct {
	Status        string                         `json:"status"`
	MessageScope  string                         `json:"message_scope"`
	Conversations []DingTalkConversationSnapshot `json:"conversations"`
	SourceID      string                         `json:"source_id"`
	Subscriptions []BindingSubscriptionResult    `json:"subscriptions"`
	Error         *BindingTaskError              `json:"error"`
}

type CompleteBindingParams struct {
	BindingID     pgtype.UUID
	BindingMode   BindingMode
	CallbackToken string
	Status        string
	Identity      IdentityBindingResult
	Message       MessageBindingResult
}

type BindingTaskAcknowledgement struct {
	Status string `json:"status"`
}

type CompleteBindingResult struct {
	Status          string                       `json:"status"`
	IdentityBinding BindingTaskAcknowledgement   `json:"identity_binding"`
	MessageBinding  BindingTaskAcknowledgement   `json:"message_binding"`
	Binding         PublicDingTalkAccountBinding `json:"binding"`
}

func (s *Service) CompleteBinding(ctx context.Context, params CompleteBindingParams) (result CompleteBindingResult, err error) {
	var businessMetrics *obsmetrics.BusinessMetrics
	if s != nil {
		businessMetrics = s.metrics
	}
	defer func() {
		businessMetrics.RecordDingTalkAccountCallback(dingTalkAccountOperationOutcome(err))
	}()
	if s == nil || s.store == nil || s.identityStore == nil || s.router == nil {
		return CompleteBindingResult{}, ErrNotConfigured
	}
	if !params.BindingID.Valid || !params.BindingMode.Valid() {
		return CompleteBindingResult{}, ErrNotFound
	}
	messageScope, conversations, err := validateCompleteBindingParams(params)
	if err != nil {
		return CompleteBindingResult{}, ErrInvalidResult
	}

	var binding PublicDingTalkAccountBinding
	if params.BindingMode == BindingModeIdentity {
		if params.Identity.Status == DingTalkBindingTaskStatusSuccess {
			binding, err = s.completeCallback(ctx, CallbackParams{
				BindingID:       params.BindingID,
				BindingMode:     params.BindingMode,
				CallbackToken:   params.CallbackToken,
				IdentityBinding: params.Identity,
				MessageBinding:  params.Message,
			}, false)
			if err != nil {
				return CompleteBindingResult{}, err
			}
		} else {
			attempt, lookupErr := s.identityStore.GetAgentDingTalkIdentityAttempt(ctx, params.BindingID)
			if lookupErr != nil {
				if errors.Is(lookupErr, pgx.ErrNoRows) {
					return CompleteBindingResult{}, ErrNotFound
				}
				return CompleteBindingResult{}, fmt.Errorf("get dingtalk identity attempt: %w", lookupErr)
			}
			if !VerifyCallbackToken(params.CallbackToken, attempt.CallbackTokenHash) ||
				!s.now().Before(attempt.ExpiresAt.Time) {
				return CompleteBindingResult{}, ErrCallbackExpired
			}
			binding = PublicDingTalkAccountBinding{
				ID:           util.UUIDToString(attempt.AgentID),
				WorkspaceID:  util.UUIDToString(attempt.WorkspaceID),
				AgentID:      util.UUIDToString(attempt.AgentID),
				DWSIdentity:  PublicDingTalkBindingOutcome{Status: DingTalkBindingStatusFailed},
				MessageRoute: PublicDingTalkBindingOutcome{Status: "unbound"},
			}
		}
		return completeBindingAcknowledgement(params, binding), nil
	}

	if params.Message.Status == DingTalkBindingTaskStatusSuccess {
		binding, err = s.completeCallback(ctx, CallbackParams{
			BindingID:       params.BindingID,
			BindingMode:     params.BindingMode,
			CallbackToken:   params.CallbackToken,
			IdentityBinding: params.Identity,
			MessageBinding:  params.Message,
		}, false)
		if err != nil {
			return CompleteBindingResult{}, err
		}
	} else {
		row, lookupErr := s.store.GetDingTalkAccountBinding(ctx, params.BindingID)
		if lookupErr != nil {
			if errors.Is(lookupErr, pgx.ErrNoRows) {
				return CompleteBindingResult{}, ErrNotFound
			}
			return CompleteBindingResult{}, fmt.Errorf("get dingtalk account binding: %w", lookupErr)
		}
		config, configErr := ParseDingTalkAccountConfig(row.Config)
		if configErr != nil {
			return CompleteBindingResult{}, fmt.Errorf("%w: stored binding config", ErrInvalidResult)
		}
		if !VerifyCallbackToken(params.CallbackToken, config.CallbackTokenHash) ||
			!s.now().Before(config.CallbackExpiresAt) {
			return CompleteBindingResult{}, ErrCallbackExpired
		}
		config.DWSIdentityStatus = ""
		if params.Identity.Status == DingTalkBindingTaskStatusFailed {
			config.DWSIdentityStatus = DingTalkBindingStatusFailed
		}
		config.MessageRouteStatus = params.Message.Status
		config.MessageScope = messageScope
		config.Conversations = conversations
		rawConfig, marshalErr := config.Marshal()
		if marshalErr != nil {
			return CompleteBindingResult{}, fmt.Errorf("%w: terminal binding config", ErrInvalidResult)
		}
		completed, completeErr := s.store.CompleteDingTalkAccountBindingResult(
			ctx,
			db.CompleteDingTalkAccountBindingResultParams{
				Config:                    rawConfig,
				Status:                    "pending",
				ID:                        row.ID,
				WorkspaceID:               row.WorkspaceID,
				AgentID:                   row.AgentID,
				ExpectedCallbackTokenHash: config.CallbackTokenHash,
			},
		)
		if completeErr != nil {
			if errors.Is(completeErr, pgx.ErrNoRows) {
				return CompleteBindingResult{}, ErrBindingConflict
			}
			return CompleteBindingResult{}, fmt.Errorf("complete dingtalk binding result: %w", completeErr)
		}
		binding, err = s.publicBinding(ctx, completed)
		if err != nil {
			return CompleteBindingResult{}, err
		}
	}

	return completeBindingAcknowledgement(params, binding), nil
}

func completeBindingAcknowledgement(params CompleteBindingParams, binding PublicDingTalkAccountBinding) CompleteBindingResult {
	return CompleteBindingResult{
		Status:          DingTalkBindingCompletionStatus,
		IdentityBinding: BindingTaskAcknowledgement{Status: params.Identity.Status},
		MessageBinding:  BindingTaskAcknowledgement{Status: params.Message.Status},
		Binding:         binding,
	}
}

func validateCompleteBindingParams(params CompleteBindingParams) (string, []DingTalkConversationSnapshot, error) {
	if params.Status != DingTalkBindingCompletionStatus || !params.BindingMode.Valid() {
		return "", nil, ErrInvalidResult
	}
	switch params.Identity.Status {
	case DingTalkBindingTaskStatusSuccess:
		if params.Identity.Error != nil || !isDecimalIdentifier(strings.TrimSpace(params.Identity.AccountUID)) ||
			!isDecimalIdentifier(strings.TrimSpace(params.Identity.AccountOrgID)) ||
			utf8.RuneCountInString(strings.TrimSpace(params.Identity.AccountOrganizationName)) > maxOrganizationNameRunes ||
			utf8.RuneCountInString(strings.TrimSpace(params.Identity.AccountDisplayName)) > maxAccountNameRunes ||
			!validAccountAvatarURL(strings.TrimSpace(params.Identity.AccountAvatarURL)) {
			return "", nil, ErrInvalidResult
		}
	case DingTalkBindingTaskStatusFailed:
		if !emptyIdentityResult(params.Identity) || !validBindingTaskError(params.Identity.Error) {
			return "", nil, ErrInvalidResult
		}
	default:
		return "", nil, ErrInvalidResult
	}
	if params.BindingMode == BindingModeIdentity && params.Message.Status != DingTalkBindingTaskStatusSkipped {
		return "", nil, ErrInvalidResult
	}
	if params.BindingMode == BindingModeMessage {
		if params.Identity.Status == DingTalkBindingTaskStatusSuccess && params.Message.Status == DingTalkBindingTaskStatusSkipped {
			return "", nil, ErrInvalidResult
		}
		if params.Identity.Status == DingTalkBindingTaskStatusFailed && params.Message.Status != DingTalkBindingTaskStatusSkipped {
			return "", nil, ErrInvalidResult
		}
	}

	switch params.Message.Status {
	case DingTalkBindingTaskStatusSuccess:
		if params.Identity.Status != DingTalkBindingTaskStatusSuccess || params.Message.Error != nil ||
			!validSourceID(params.Message.SourceID) || !validBindingSubscriptions(params.Message.Subscriptions) {
			return "", nil, ErrInvalidResult
		}
		return normalizeDingTalkConversationBinding(params.Message.MessageScope, params.Message.Conversations)
	case DingTalkBindingTaskStatusSkipped:
		if params.Message.Error != nil || params.Message.SourceID != "" || params.Message.MessageScope != "" ||
			len(params.Message.Conversations) != 0 || len(params.Message.Subscriptions) != 0 {
			return "", nil, ErrInvalidResult
		}
		return DingTalkMessageScopeDirectOnly, nil, nil
	case DingTalkBindingTaskStatusFailed:
		if params.Message.SourceID != "" || len(params.Message.Subscriptions) != 0 ||
			!validBindingTaskError(params.Message.Error) {
			return "", nil, ErrInvalidResult
		}
		if params.Message.MessageScope == "" {
			if len(params.Message.Conversations) != 0 {
				return "", nil, ErrInvalidResult
			}
			return DingTalkMessageScopeDirectOnly, nil, nil
		}
		return normalizeDingTalkConversationBinding(params.Message.MessageScope, params.Message.Conversations)
	default:
		return "", nil, ErrInvalidResult
	}
}

func emptyIdentityResult(result IdentityBindingResult) bool {
	return result.AccountUID == "" && result.AccountOrgID == "" &&
		result.AccountOrganizationName == "" && result.AccountDisplayName == "" &&
		result.AccountAvatarURL == ""
}

func validBindingTaskError(taskError *BindingTaskError) bool {
	if taskError == nil || len(taskError.Code) == 0 || len(taskError.Code) > maxBindingTaskErrorCodeBytes ||
		utf8.RuneCountInString(taskError.Message) == 0 ||
		utf8.RuneCountInString(taskError.Message) > maxBindingTaskErrorMessageRunes {
		return false
	}
	for index, r := range taskError.Code {
		if index == 0 {
			if r < 'a' || r > 'z' {
				return false
			}
			continue
		}
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return utf8.ValidString(taskError.Message) &&
		strings.IndexFunc(taskError.Message, unicode.IsControl) < 0
}

func validSourceID(sourceID string) bool {
	trimmed := strings.TrimSpace(sourceID)
	return trimmed != "" && sourceID == trimmed && len(sourceID) <= maxSourceIDBytes
}

func validBindingSubscriptions(subscriptions []BindingSubscriptionResult) bool {
	for _, subscription := range subscriptions {
		if (subscription.Domain != "channel" && subscription.Domain != "calendar") ||
			subscription.Status != "active" || !validSourceID(subscription.SourceID) {
			return false
		}
	}
	return true
}
