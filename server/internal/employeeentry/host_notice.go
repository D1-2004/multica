package employeeentry

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Host notice sources. Each kind's writer owns its source_id format.
const (
	HostNoticeTaskWake   = "task_wake"
	HostNoticeInvitation = "invitation"
	HostNoticeWatchdog   = "watchdog"
	// HostNoticeTaskPlan is a deterministic plan note (paused, budget spent).
	HostNoticeTaskPlan = "task_plan"
)

// HostNotice links a Host-initiated scene message to the dialogue it belongs
// to: the same scene and the principal whose recent conversation may show it.
// Only a delivered action with a provider message id becomes history.
type HostNotice struct {
	ActionID    string
	Scope       Scope
	PrincipalID string
	SourceKind  string
	SourceID    string
	// OriginReceiptID is the admitted message the notice derives from, if any.
	// Withdrawn memory evidence from that receipt conservatively hides it.
	OriginReceiptID string
}

// RecordHostNotice must run in the transaction that enqueues the response
// action, so a committed send always has its history fact and vice versa.
// Recording the same action again with other facts is ErrConflict.
func RecordHostNotice(ctx context.Context, db DB, n HostNotice) error {
	switch n.SourceKind {
	case HostNoticeTaskWake, HostNoticeInvitation, HostNoticeWatchdog, HostNoticeTaskPlan:
	default:
		return ErrInvalid
	}
	if db == nil || n.ActionID == "" || len(n.ActionID) > 256 || strings.TrimSpace(n.ActionID) != n.ActionID || !validScope(n.Scope) || !validID(n.PrincipalID) || !wakeRef(n.SourceID) || (n.OriginReceiptID != "" && !validID(n.OriginReceiptID)) {
		return ErrInvalid
	}
	args := append(scopeArgs(n.Scope), n.ActionID, n.PrincipalID, n.SourceKind, n.SourceID, n.OriginReceiptID)
	if _, err := db.Exec(ctx, `INSERT INTO employee_host_notice(workspace_id,agent_id,tenant_org_id,scene_id,action_id,principal_id,source_kind,source_id,origin_receipt_id) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6::uuid,$7,$8,NULLIF($9,'')::uuid) ON CONFLICT DO NOTHING`, args...); err != nil {
		return err
	}
	var same bool
	err := db.QueryRow(ctx, `SELECT workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND principal_id=$6::uuid AND source_kind=$7 AND source_id=$8 AND origin_receipt_id IS NOT DISTINCT FROM NULLIF($9,'')::uuid FROM employee_host_notice WHERE action_id=$5`, args...).Scan(&same)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if !same {
		return ErrConflict
	}
	return nil
}
