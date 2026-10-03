package handler

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Stop is a domain fact, not a queue flag or a model's final text. A committed
// stop closes the accepted input boundary; any older unsent result is obsolete.
// Both new notice admission and the final outbox send fence call this query.
func employeeNoticeStopped(ctx context.Context, tx pgx.Tx, b employeeNoticeBinding) (bool, error) {
	var stopped bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_task_entry e JOIN employee_task_run current ON current.task_id=e.task_id AND current.workspace_id=e.workspace_id AND current.agent_id=e.agent_id AND current.tenant_org_id=e.tenant_org_id
 LEFT JOIN employee_task_run target ON target.id=e.run_id AND target.task_id=e.task_id AND target.workspace_id=e.workspace_id AND target.agent_id=e.agent_id AND target.tenant_org_id=e.tenant_org_id
 WHERE e.workspace_id=$1::uuid AND e.agent_id=$2::uuid AND e.tenant_org_id=$3 AND e.task_id=$4::uuid AND current.id=$5::uuid AND e.kind='input' AND e.payload->>'operation'='stop' AND e.actor_ref=$6 AND e.seq>=current.input_seq
 AND ((e.run_id IS NULL AND COALESCE(e.payload->>'run_id','')='' AND COALESCE(e.payload->>'queue_task_id','')='') OR (target.id::text=e.payload->>'run_id' AND target.queue_task_id::text=e.payload->>'queue_task_id')))`, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.TaskID, b.RunID, b.Requester).Scan(&stopped)
	return stopped, err
}
