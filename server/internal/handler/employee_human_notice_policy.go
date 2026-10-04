package handler

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service"
)

func (h *Handler) employeeHumanNoticeCommitment(ctx context.Context, tx pgx.Tx, b employeeNoticeBinding, principal string) (employeetask.CompletionNoticePolicy, *service.DirectTaskNoticeOrigin, bool, error) {
	var meta struct {
		ResponseID string `json:"employee_human_response_id"`
	}
	if json.Unmarshal(b.Queue.Context, &meta) != nil || meta.ResponseID == "" {
		return employeetask.CompletionNoticePolicy{}, nil, false, nil
	}
	job := employeeentry.Job{ID: b.JobID}
	err := tx.QueryRow(ctx, `SELECT kind,workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,principal_id::text,items,state FROM employee_scene_job WHERE id=$1::uuid`, b.JobID).Scan(&job.Kind, &job.Scope.WorkspaceID, &job.Scope.AgentID, &job.Scope.TenantOrgID, &job.Scope.SceneID, &job.PrincipalID, &job.Items, &job.State)
	if err != nil {
		return employeetask.CompletionNoticePolicy{}, nil, true, err
	}
	binding, err := readEmployeeHumanBinding(ctx, tx, job)
	if err != nil || binding.Response.ID != meta.ResponseID || principal != job.PrincipalID || job.State != "completed" || binding.Question.SourceRef != b.SourceRef {
		return employeetask.CompletionNoticePolicy{}, nil, true, holdEmployeeNotice("human_response_notice_binding_mismatch")
	}
	proof := employeeExecutionBinding{Scope: job.Scope, SourceRef: b.SourceRef, Requester: b.Requester, Queue: b.Queue}
	proof.TaskID, proof.RunID, proof.QueueTaskID, proof.JobID, proof.GoalRevision = b.TaskID, b.RunID, b.QueueID, b.JobID, b.RunGoalRevision
	reason, err := employeeHumanExecutionProof(ctx, tx, &proof, job)
	if err != nil {
		return employeetask.CompletionNoticePolicy{}, nil, true, err
	}
	if reason != "" {
		return employeetask.CompletionNoticePolicy{}, nil, true, holdEmployeeNotice(reason)
	}
	policy, origin, err := service.InheritDirectTaskNoticePolicy(b.Queue)
	if err != nil {
		return policy, nil, true, err
	}
	if origin != nil {
		if _, err = h.employeeInheritedNoticeSource(ctx, tx, b.Scope, b.TaskID, b.Requester, principal, policy, *origin); err != nil {
			return policy, nil, true, err
		}
	}
	return policy, origin, true, nil
}
