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

func (s *Service) CompleteIdentityCallback(ctx context.Context, params IdentityCallbackParams) (PublicDingTalkAccountBinding, error) {
	if s == nil || s.identityStore == nil || s.store == nil {
		return PublicDingTalkAccountBinding{}, ErrNotConfigured
	}
	if !params.AttemptID.Valid {
		return PublicDingTalkAccountBinding{}, ErrNotFound
	}
	uid := strings.TrimSpace(params.AccountUID)
	orgID := strings.TrimSpace(params.AccountOrgID)
	organizationName := strings.TrimSpace(params.AccountOrganizationName)
	displayName := strings.TrimSpace(params.AccountDisplayName)
	avatarURL := strings.TrimSpace(params.AccountAvatarURL)
	if !isDecimalIdentifier(uid) || !isDecimalIdentifier(orgID) ||
		utf8.RuneCountInString(organizationName) > maxOrganizationNameRunes ||
		utf8.RuneCountInString(displayName) > maxAccountNameRunes || !validAccountAvatarURL(avatarURL) {
		return PublicDingTalkAccountBinding{}, ErrInvalidResult
	}

	attempt, err := s.identityStore.GetAgentDingTalkIdentityAttempt(ctx, params.AttemptID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicDingTalkAccountBinding{}, ErrNotFound
		}
		return PublicDingTalkAccountBinding{}, fmt.Errorf("get dingtalk identity attempt: %w", err)
	}
	if !VerifyCallbackToken(params.CallbackToken, attempt.CallbackTokenHash) ||
		!s.now().Before(attempt.ExpiresAt.Time) {
		return PublicDingTalkAccountBinding{}, ErrCallbackExpired
	}
	if attempt.UsedAt.Valid {
		if !attemptMatchesIdentityCallback(attempt, uid, orgID) {
			return PublicDingTalkAccountBinding{}, ErrBindingConflict
		}
		return s.publicBindingForAgent(ctx, attempt.WorkspaceID, attempt.AgentID)
	}

	_, err = s.identityStore.CompleteAgentDingTalkIdentityAttempt(ctx, db.CompleteAgentDingTalkIdentityAttemptParams{
		DwsUid:             pgtype.Text{String: uid, Valid: true},
		OrgID:              pgtype.Text{String: orgID, Valid: true},
		OrganizationName:   organizationName,
		AttemptID:          attempt.ID,
		CallbackTokenHash:  attempt.CallbackTokenHash,
		AccountDisplayName: displayName,
		AccountAvatarUrl:   avatarURL,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current, lookupErr := s.identityStore.GetAgentDingTalkIdentityAttempt(ctx, params.AttemptID)
			if lookupErr == nil && current.UsedAt.Valid &&
				attemptMatchesIdentityCallback(current, uid, orgID) {
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
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicDingTalkAccountBinding{}, ErrNotFound
		}
		return PublicDingTalkAccountBinding{}, fmt.Errorf("get dingtalk account binding: %w", err)
	}
	return s.publicBinding(ctx, row)
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
