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

const maxCorpIDBytes = 256

func (s *Service) CompleteIdentityCallback(ctx context.Context, params IdentityCallbackParams) (PublicDingTalkAccountBinding, error) {
	if s == nil || s.identityStore == nil || s.orgEmployees == nil || s.store == nil {
		return PublicDingTalkAccountBinding{}, ErrNotConfigured
	}
	if !params.AttemptID.Valid {
		return PublicDingTalkAccountBinding{}, ErrNotFound
	}
	openID := strings.TrimSpace(params.AccountOpenID)
	orgID := strings.TrimSpace(params.AccountOrgID)
	corpID := strings.TrimSpace(params.AccountCorpID)
	displayName := strings.TrimSpace(params.AccountDisplayName)
	avatarURL := strings.TrimSpace(params.AccountAvatarURL)
	if !isDecimalIdentifier(openID) || !isDecimalIdentifier(orgID) ||
		corpID == "" || corpID != params.AccountCorpID || len(corpID) > maxCorpIDBytes ||
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
		if !attemptMatchesIdentityCallback(attempt, openID, orgID, corpID) {
			return PublicDingTalkAccountBinding{}, ErrBindingConflict
		}
		return s.publicBindingForAgent(ctx, attempt.WorkspaceID, attempt.AgentID)
	}

	employee, err := s.orgEmployees.GetEmployeeByStaffID(ctx, orgID, openID)
	if err != nil {
		return PublicDingTalkAccountBinding{}, fmt.Errorf("%w: %v", ErrIdentityUnavailable, err)
	}
	if employee.OrgID != orgID || employee.StaffID != openID || !isDecimalIdentifier(employee.UID) {
		return PublicDingTalkAccountBinding{}, ErrIdentityMismatch
	}
	_, err = s.identityStore.CompleteAgentDingTalkIdentityAttempt(ctx, db.CompleteAgentDingTalkIdentityAttemptParams{
		AccountOpenID:      pgtype.Text{String: openID, Valid: true},
		OrgID:              pgtype.Text{String: orgID, Valid: true},
		AccountCorpID:      pgtype.Text{String: corpID, Valid: true},
		AttemptID:          attempt.ID,
		CallbackTokenHash:  attempt.CallbackTokenHash,
		DwsUid:             employee.UID,
		AccountDisplayName: displayName,
		AccountAvatarUrl:   avatarURL,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current, lookupErr := s.identityStore.GetAgentDingTalkIdentityAttempt(ctx, params.AttemptID)
			if lookupErr == nil && current.UsedAt.Valid &&
				attemptMatchesIdentityCallback(current, openID, orgID, corpID) {
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

func attemptMatchesIdentityCallback(attempt db.AgentDingtalkIdentityAttempt, openID, orgID, corpID string) bool {
	return attempt.CompletedOpenID.Valid && attempt.CompletedOpenID.String == openID &&
		attempt.CompletedOrgID.Valid && attempt.CompletedOrgID.String == orgID &&
		attempt.CompletedCorpID.Valid && attempt.CompletedCorpID.String == corpID
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
