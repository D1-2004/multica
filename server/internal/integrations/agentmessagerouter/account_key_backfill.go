package agentmessagerouter

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type AccountKeyBackfillOutcome string

const (
	AccountKeyBackfillWouldUpdate       AccountKeyBackfillOutcome = "would_update"
	AccountKeyBackfillUpdated           AccountKeyBackfillOutcome = "updated"
	AccountKeyBackfillAlreadyComplete   AccountKeyBackfillOutcome = "already_complete"
	AccountKeyBackfillSourceNotFound    AccountKeyBackfillOutcome = "source_not_found"
	AccountKeyBackfillMissingTenant     AccountKeyBackfillOutcome = "missing_tenant"
	AccountKeyBackfillInvalidLocal      AccountKeyBackfillOutcome = "invalid_local"
	AccountKeyBackfillRouterUnavailable AccountKeyBackfillOutcome = "router_unavailable"
	AccountKeyBackfillCASConflict       AccountKeyBackfillOutcome = "cas_conflict"
)

var accountKeyBackfillOutcomes = []AccountKeyBackfillOutcome{
	AccountKeyBackfillWouldUpdate,
	AccountKeyBackfillUpdated,
	AccountKeyBackfillAlreadyComplete,
	AccountKeyBackfillSourceNotFound,
	AccountKeyBackfillMissingTenant,
	AccountKeyBackfillInvalidLocal,
	AccountKeyBackfillRouterUnavailable,
	AccountKeyBackfillCASConflict,
}

type AccountKeyBackfillRecord struct {
	InstallationFingerprint string                    `json:"installation_fingerprint"`
	SourceFingerprint       string                    `json:"source_fingerprint,omitempty"`
	AccountFingerprint      string                    `json:"account_fingerprint,omitempty"`
	Outcome                 AccountKeyBackfillOutcome `json:"outcome"`
	Reason                  string                    `json:"reason"`
}

type AccountKeyBackfillReport struct {
	Apply   bool                              `json:"apply"`
	Total   int                               `json:"total"`
	Counts  map[AccountKeyBackfillOutcome]int `json:"counts"`
	Records []AccountKeyBackfillRecord        `json:"records"`
}

type AccountKeyBackfillStore interface {
	ListDingTalkAccountBindingAccountKeyBackfillRows(context.Context) ([]db.ChannelInstallation, error)
	BackfillDingTalkAccountRouterAccountKey(context.Context, db.BackfillDingTalkAccountRouterAccountKeyParams) (db.ChannelInstallation, error)
}

type AccountKeyBackfillRouter interface {
	GetDigitalEmployeeSourceIdentities(context.Context, []string) (DigitalEmployeeSourceIdentityResult, error)
}

type AccountKeyBackfillRunner struct {
	store  AccountKeyBackfillStore
	router AccountKeyBackfillRouter
}

func NewAccountKeyBackfillRunner(store AccountKeyBackfillStore, router AccountKeyBackfillRouter) (*AccountKeyBackfillRunner, error) {
	if store == nil || router == nil {
		return nil, errors.New("dingtalk account key backfill is not configured")
	}
	return &AccountKeyBackfillRunner{store: store, router: router}, nil
}

type accountKeyBackfillCandidate struct {
	row    db.ChannelInstallation
	config DingTalkAccountConfig
	index  int
}

func (r *AccountKeyBackfillRunner) Run(ctx context.Context, apply bool) (AccountKeyBackfillReport, error) {
	if r == nil || r.store == nil || r.router == nil {
		return AccountKeyBackfillReport{}, errors.New("dingtalk account key backfill is not configured")
	}
	rows, err := r.store.ListDingTalkAccountBindingAccountKeyBackfillRows(ctx)
	if err != nil {
		return AccountKeyBackfillReport{}, errors.New("list dingtalk account key backfill rows")
	}
	report := newAccountKeyBackfillReport(apply, len(rows))
	candidates := make([]accountKeyBackfillCandidate, 0, len(rows))
	sourceIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		record := AccountKeyBackfillRecord{InstallationFingerprint: accountKeyBackfillFingerprint("installation", row.ID.String())}
		config, parseErr := ParseDingTalkAccountConfig(row.Config)
		if parseErr != nil || !row.ID.Valid || !row.WorkspaceID.Valid || !row.AgentID.Valid ||
			row.ChannelType != ChannelTypeDingTalkAccount || row.Status != "active" {
			report.append(record, AccountKeyBackfillInvalidLocal, "local_row_invalid")
			continue
		}
		if config.RouterSourceID != "" {
			record.SourceFingerprint = accountKeyBackfillFingerprint("source", config.RouterSourceID)
		}
		completeFields := 0
		if config.RouterPlatform != "" {
			completeFields++
		}
		if config.RouterTenantID != "" {
			completeFields++
		}
		if config.RouterAccountID != "" {
			completeFields++
		}
		if completeFields == 3 {
			if !validSourceID(config.RouterSourceID) {
				report.append(record, AccountKeyBackfillInvalidLocal, "complete_account_key_without_source")
				continue
			}
			record.AccountFingerprint = accountKeyBackfillFingerprint(
				"account", config.RouterPlatform+"\x00"+config.RouterTenantID+"\x00"+config.RouterAccountID,
			)
			report.append(record, AccountKeyBackfillAlreadyComplete, "account_key_present")
			continue
		}
		if completeFields != 0 || !validSourceID(config.RouterSourceID) {
			report.append(record, AccountKeyBackfillInvalidLocal, "local_account_key_or_source_invalid")
			continue
		}
		report.Records = append(report.Records, record)
		candidates = append(candidates, accountKeyBackfillCandidate{row: row, config: config, index: len(report.Records) - 1})
		sourceIDs = append(sourceIDs, config.RouterSourceID)
	}
	if len(candidates) == 0 {
		return report, nil
	}
	resolved, err := r.router.GetDigitalEmployeeSourceIdentities(ctx, sourceIDs)
	if err != nil {
		for _, candidate := range candidates {
			report.finish(candidate.index, AccountKeyBackfillRouterUnavailable, "router_unavailable")
		}
		return report, nil
	}
	identities := make(map[string]DigitalEmployeeSourceIdentity, len(resolved.Sources))
	duplicate := make(map[string]bool)
	for _, identity := range resolved.Sources {
		if _, exists := identities[identity.SourceID]; exists {
			duplicate[identity.SourceID] = true
		}
		identities[identity.SourceID] = identity
	}
	missing := make(map[string]struct{}, len(resolved.MissingSourceIDs))
	for _, sourceID := range resolved.MissingSourceIDs {
		missing[sourceID] = struct{}{}
	}
	for _, candidate := range candidates {
		identity, found := identities[candidate.config.RouterSourceID]
		if _, explicitlyMissing := missing[candidate.config.RouterSourceID]; explicitlyMissing || !found {
			report.finish(candidate.index, AccountKeyBackfillSourceNotFound, "source_not_found")
			continue
		}
		if duplicate[candidate.config.RouterSourceID] || identity.SourceID != candidate.config.RouterSourceID ||
			identity.Platform != "dingtalk" || identity.SourceType != "digital_employee" || identity.Domain != "channel" {
			report.finish(candidate.index, AccountKeyBackfillInvalidLocal, "source_identity_mismatch")
			continue
		}
		if !validRouterIdentifier(identity.TenantID) || !validRouterIdentifier(identity.AccountID) {
			report.finish(candidate.index, AccountKeyBackfillMissingTenant, "trusted_account_identity_missing")
			continue
		}
		report.Records[candidate.index].AccountFingerprint = accountKeyBackfillFingerprint(
			"account", identity.Platform+"\x00"+identity.TenantID+"\x00"+identity.AccountID,
		)
		if !apply {
			report.finish(candidate.index, AccountKeyBackfillWouldUpdate, "source_identity_verified")
			continue
		}
		updated, updateErr := r.store.BackfillDingTalkAccountRouterAccountKey(ctx, db.BackfillDingTalkAccountRouterAccountKeyParams{
			RouterPlatform: identity.Platform, RouterTenantID: identity.TenantID, RouterAccountID: identity.AccountID,
			ID: candidate.row.ID, WorkspaceID: candidate.row.WorkspaceID, AgentID: candidate.row.AgentID,
			ExpectedStatus: candidate.row.Status, ExpectedRouterSourceID: candidate.config.RouterSourceID,
			ExpectedConfig: append([]byte(nil), candidate.row.Config...),
		})
		if updateErr != nil {
			if errors.Is(updateErr, pgx.ErrNoRows) {
				report.finish(candidate.index, AccountKeyBackfillCASConflict, "row_changed")
				continue
			}
			return AccountKeyBackfillReport{}, errors.New("dingtalk account key backfill storage failed")
		}
		updatedConfig, parseErr := ParseDingTalkAccountConfig(updated.Config)
		if parseErr != nil || updated.ID != candidate.row.ID || updated.WorkspaceID != candidate.row.WorkspaceID ||
			updated.AgentID != candidate.row.AgentID || updated.Status != candidate.row.Status ||
			updatedConfig.RouterSourceID != candidate.config.RouterSourceID || updatedConfig.RouterPlatform != identity.Platform ||
			updatedConfig.RouterTenantID != identity.TenantID || updatedConfig.RouterAccountID != identity.AccountID {
			report.finish(candidate.index, AccountKeyBackfillCASConflict, "cas_result_invalid")
			continue
		}
		report.finish(candidate.index, AccountKeyBackfillUpdated, "source_identity_verified")
	}
	return report, nil
}

func newAccountKeyBackfillReport(apply bool, total int) AccountKeyBackfillReport {
	report := AccountKeyBackfillReport{
		Apply: apply, Total: total, Counts: make(map[AccountKeyBackfillOutcome]int, len(accountKeyBackfillOutcomes)),
		Records: make([]AccountKeyBackfillRecord, 0, total),
	}
	for _, outcome := range accountKeyBackfillOutcomes {
		report.Counts[outcome] = 0
	}
	return report
}

func (r *AccountKeyBackfillReport) append(record AccountKeyBackfillRecord, outcome AccountKeyBackfillOutcome, reason string) {
	record.Outcome = outcome
	record.Reason = reason
	r.Records = append(r.Records, record)
	r.Counts[outcome]++
}

func (r *AccountKeyBackfillReport) finish(index int, outcome AccountKeyBackfillOutcome, reason string) {
	r.Records[index].Outcome = outcome
	r.Records[index].Reason = reason
	r.Counts[outcome]++
}

func accountKeyBackfillFingerprint(domain, value string) string {
	digest := sha256.Sum256([]byte("dingtalk-account-key-backfill:v1:" + domain + ":" + value))
	return fmt.Sprintf("sha256:%x", digest)
}
