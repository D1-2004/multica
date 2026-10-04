package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A question's historical source never restores foreground participation.
func employeeHumanQuiet(ctx context.Context, database employeeQueryer, scope employeeentry.Scope) (bool, error) {
	p, err := employeeReadParticipation(ctx, database, scope)
	return p.Mode == "quiet", err
}

// This fences only the response wake's new foreground acknowledgement. Existing
// execution notices and round-end questions retain their background authority.
func (h *Handler) beforeEmployeeHumanResponseSend(ctx context.Context, in dingtalkresponse.ActionInput) (bool, error) {
	database, ok := employeeEntryDB(h)
	if !ok {
		return false, errors.New("human response authority unavailable")
	}
	var jobID string
	err := database.QueryRow(ctx, `SELECT source_id FROM employee_host_notice WHERE action_id=$1 AND source_kind='human_response'`, in.ActionID).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	tx, err := database.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer tx.Rollback(ctx)
	scope := employeeentry.Scope{WorkspaceID: in.WorkspaceID, AgentID: in.AgentID, TenantOrgID: in.DWSOrgID, SceneID: in.SceneID}
	if err = humanquestion.LockScope(ctx, tx, scope); err != nil {
		return true, err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_host_notice n JOIN employee_scene_job j ON j.id::text=n.source_id WHERE n.action_id=$1 AND n.source_kind='human_response' AND j.id=$2::uuid AND j.kind='human_response' AND j.state='completed' AND n.workspace_id=$3::uuid AND n.agent_id=$4::uuid AND n.tenant_org_id=$5 AND n.scene_id=$6::uuid AND j.workspace_id=n.workspace_id AND j.agent_id=n.agent_id AND j.tenant_org_id=n.tenant_org_id AND j.scene_id=n.scene_id)`, in.ActionID, jobID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID).Scan(&valid)
	if err != nil {
		return true, err
	}
	if !valid || in.SceneNoticeID != jobID {
		return true, &dingtalkresponse.SuppressSendError{Reason: "human_response_notice_scope_mismatch"}
	}
	if _, err = employeeSceneFence(ctx, &Handler{Queries: db.New(tx)}, employeeentry.Job{Scope: scope}); err != nil {
		return true, err
	}
	quiet, err := employeeHumanQuiet(ctx, tx, scope)
	if err != nil {
		return true, err
	}
	if quiet {
		return true, &dingtalkresponse.SuppressSendError{Reason: "scene_participation_quiet"}
	}
	return true, tx.Commit(ctx)
}
