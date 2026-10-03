package handler

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeelearning"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ReconcileEmployeeLearnings is an independent background consumer of terminal
// Employee Runs. Ordinary Run outcomes are no longer recorded as inferred
// private candidates: "every event writes a learning" only produced noise
// (GawkBot task_distill.go: distill verified outcomes only). Each Run still
// gets its durable consumption receipt, so the consumer never rescans it;
// verified outcomes are distilled by internal/employeeverification.
func (h *Handler) ReconcileEmployeeLearnings(ctx context.Context, limit int) (int, error) {
	if h == nil || h.EmployeeMemory == nil {
		return 0, nil
	}
	database, ok := employeeEntryDB(h)
	if !ok {
		return 0, employeelearning.ErrInvalid
	}
	return employeelearning.NewStore(database).Process(ctx, limit, h.captureEmployeeRunCandidate)
}
func (h *Handler) captureEmployeeRunCandidate(ctx context.Context, tx pgx.Tx, c employeelearning.Candidate) (employeelearning.Result, error) {
	skip := func(reason string) (employeelearning.Result, error) {
		return employeelearning.Result{SkipReason: reason}, nil
	}
	queries := db.New(tx)
	workspace, agent := parseUUID(c.Scope.WorkspaceID), parseUUID(c.Scope.AgentID)
	row, err := queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agent, WorkspaceID: workspace})
	if errors.Is(err, pgx.ErrNoRows) || row.ArchivedAt.Valid {
		return skip("agent_archived")
	}
	if err != nil {
		return employeelearning.Result{}, err
	}
	if _, err = fencedScene(ctx, queries, &c.Scope.Scene, scene.Owner{WorkspaceID: workspace, AgentID: agent}, c.Scope.TenantOrgID); err != nil {
		if errors.Is(err, scene.ErrNotFound) || errors.Is(err, scene.ErrStaleTenant) || errors.Is(err, scene.ErrUnresolved) {
			return skip("stale_tenant")
		}
		return employeelearning.Result{}, err
	}
	queue, err := queries.GetAgentTask(ctx, parseUUID(c.QueueTaskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return skip("missing_execution")
	}
	if err != nil {
		return employeelearning.Result{}, err
	}
	execution, valid := service.ParseDirectTaskContext(queue)
	// Automation is not a requester-private human learning source.
	if (valid && execution.AutomationOrigin != nil) || service.IsAutomationRequesterRef(c.RequesterRef) {
		return skip("automation_origin")
	}
	if !valid || execution.EmployeeTaskID != c.TaskID || execution.WorkspaceID != c.Scope.WorkspaceID || queue.AgentID != agent {
		return skip("execution_scope_mismatch")
	}
	terminal := map[string]string{"succeeded": "completed", "failed": "failed", "cancelled": "cancelled"}[string(c.State)]
	if queue.Status != terminal {
		return skip("execution_state_mismatch")
	}
	var result string
	if err = tx.QueryRow(ctx, `SELECT r.result FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE t.workspace_id=$1 AND t.agent_id=$2 AND t.id=$3::uuid AND r.id=$4::uuid`, workspace, agent, c.TaskID, c.RunID).Scan(&result); err != nil {
		return employeelearning.Result{}, err
	}
	if strings.TrimSpace(result) == "" {
		return skip("no_result_content")
	}
	return skip("candidate_retired")
}
func employeeUniqueRequester(messages []employeeSourceMessage) (string, bool) {
	requester := ""
	for _, message := range messages {
		if message.RequesterRef == "" {
			return "", false
		}
		if requester != "" && requester != message.RequesterRef {
			return "", false
		}
		requester = message.RequesterRef
	}
	return requester, requester != ""
}
