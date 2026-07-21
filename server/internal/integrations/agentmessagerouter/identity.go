package agentmessagerouter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func normalizeIdentityBindingResult(result IdentityBindingResult) (IdentityBindingResult, error) {
	result.Status = strings.TrimSpace(result.Status)
	result.AccountUID = strings.TrimSpace(result.AccountUID)
	result.AccountOrgID = strings.TrimSpace(result.AccountOrgID)
	result.AccountOrganizationName = strings.TrimSpace(result.AccountOrganizationName)
	result.AccountDisplayName = strings.TrimSpace(result.AccountDisplayName)
	result.AccountAvatarURL = strings.TrimSpace(result.AccountAvatarURL)
	if result.Status != DingTalkBindingTaskStatusSuccess || result.Error != nil ||
		!isDecimalIdentifier(result.AccountUID) || !isDecimalIdentifier(result.AccountOrgID) ||
		utf8.RuneCountInString(result.AccountOrganizationName) > maxOrganizationNameRunes ||
		utf8.RuneCountInString(result.AccountDisplayName) > maxAccountNameRunes ||
		!validAccountAvatarURL(result.AccountAvatarURL) {
		return IdentityBindingResult{}, ErrInvalidResult
	}
	return result, nil
}

func (s *Service) completeIdentityBinding(
	ctx context.Context,
	attemptID pgtype.UUID,
	callbackToken string,
	identity IdentityBindingResult,
) (PublicDingTalkAccountBinding, error) {
	if s == nil || s.identityStore == nil || s.store == nil {
		return PublicDingTalkAccountBinding{}, ErrNotConfigured
	}
	if !attemptID.Valid {
		return PublicDingTalkAccountBinding{}, ErrNotFound
	}
	attempt, err := s.identityStore.GetAgentDingTalkIdentityAttempt(ctx, attemptID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicDingTalkAccountBinding{}, ErrNotFound
		}
		return PublicDingTalkAccountBinding{}, fmt.Errorf("get dingtalk identity attempt: %w", err)
	}
	if !VerifyCallbackToken(callbackToken, attempt.CallbackTokenHash) ||
		!s.now().Before(attempt.ExpiresAt.Time) {
		return PublicDingTalkAccountBinding{}, ErrCallbackExpired
	}
	if attempt.UsedAt.Valid {
		if !attemptMatchesIdentityCallback(attempt, identity.AccountUID, identity.AccountOrgID) {
			return PublicDingTalkAccountBinding{}, ErrBindingConflict
		}
		return s.publicBindingForAgent(ctx, attempt.WorkspaceID, attempt.AgentID)
	}

	_, err = s.identityStore.CompleteAgentDingTalkIdentityAttempt(ctx, db.CompleteAgentDingTalkIdentityAttemptParams{
		DwsUid:             pgtype.Text{String: identity.AccountUID, Valid: true},
		OrgID:              pgtype.Text{String: identity.AccountOrgID, Valid: true},
		OrganizationName:   identity.AccountOrganizationName,
		AttemptID:          attempt.ID,
		CallbackTokenHash:  attempt.CallbackTokenHash,
		AccountDisplayName: identity.AccountDisplayName,
		AccountAvatarUrl:   identity.AccountAvatarURL,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current, lookupErr := s.identityStore.GetAgentDingTalkIdentityAttempt(ctx, attemptID)
			if lookupErr == nil && current.UsedAt.Valid &&
				attemptMatchesIdentityCallback(current, identity.AccountUID, identity.AccountOrgID) {
				return s.publicBindingForAgent(ctx, current.WorkspaceID, current.AgentID)
			}
			return PublicDingTalkAccountBinding{}, ErrBindingConflict
		}
		return PublicDingTalkAccountBinding{}, fmt.Errorf("complete dingtalk identity attempt: %w", err)
	}
	return s.publicBindingForAgent(ctx, attempt.WorkspaceID, attempt.AgentID)
}

func (s *Service) publicBindingForAgent(ctx context.Context, workspaceID, agentID pgtype.UUID) (PublicDingTalkAccountBinding, error) {
	row, err := s.store.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if err == nil {
		return s.publicBinding(ctx, row)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PublicDingTalkAccountBinding{}, fmt.Errorf("get dingtalk account binding: %w", err)
	}
	identity, identityErr := s.identityStore.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if identityErr != nil {
		if errors.Is(identityErr, pgx.ErrNoRows) {
			return PublicDingTalkAccountBinding{}, ErrNotFound
		}
		return PublicDingTalkAccountBinding{}, fmt.Errorf("get dingtalk identity: %w", identityErr)
	}
	return publicIdentityBinding(identity), nil
}

func attemptMatchesIdentityCallback(attempt db.AgentDingtalkIdentityAttempt, uid, orgID string) bool {
	return attempt.CompletedUid.Valid && attempt.CompletedUid.String == uid &&
		attempt.CompletedOrgID.Valid && attempt.CompletedOrgID.String == orgID
}

func isDecimalIdentifier(value string) bool {
	if value == "" || value == "0" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
