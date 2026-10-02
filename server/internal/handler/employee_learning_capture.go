package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeelearning"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// ReconcileEmployeeLearnings is an independent background consumer. An ordinary
// runtime outcome is only an inferred private candidate, never verified evidence.
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
	memoryScope := employeememory.Scope{WorkspaceID: workspace, AgentID: agent, TenantOrgID: c.Scope.TenantOrgID, Scene: c.Scope.Scene, Kind: employeememory.ScopePrivate, PrincipalID: c.RequesterRef}
	queue, err := queries.GetAgentTask(ctx, parseUUID(c.QueueTaskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return skip("missing_execution")
	}
	if err != nil {
		return employeelearning.Result{}, err
	}
	execution, valid := service.ParseDirectTaskContext(queue)
	if !valid || execution.EmployeeTaskID != c.TaskID || execution.WorkspaceID != c.Scope.WorkspaceID || queue.AgentID != agent {
		return skip("execution_scope_mismatch")
	}
	terminal := map[string]string{"succeeded": "completed", "failed": "failed", "cancelled": "cancelled"}[string(c.State)]
	if queue.Status != terminal {
		return skip("execution_state_mismatch")
	}
	var goal, result string
	if err = tx.QueryRow(ctx, `SELECT t.definition->>'goal',r.result FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE t.workspace_id=$1 AND t.agent_id=$2 AND t.id=$3::uuid AND r.id=$4::uuid`, workspace, agent, c.TaskID, c.RunID).Scan(&goal, &result); err != nil {
		return employeelearning.Result{}, err
	}
	result = redact.Text(strings.TrimSpace(result))
	if json.Valid([]byte(result)) {
		result = "Structured output is retained in the execution record; no textual claim is inferred."
	}
	if result == "" {
		return skip("no_result_content")
	}
	insight := fmt.Sprintf("Unverified execution candidate. Task %s reported %s. Goal: %s. Reported result excerpt: %s", c.TaskID, c.State, employeeLearningExcerpt(redact.Text(goal), 600), employeeLearningExcerpt(result, 2200))
	record, err := h.EmployeeMemory.RecordTx(ctx, tx, memoryScope, employeememory.LearningRecord{Type: employeememory.LearningTypeOperational, Key: "run-" + c.RunID, Insight: insight, Confidence: 3, Source: employeememory.LearningSourceInferred}, employeememory.TrustedEvidence{SourceID: "employee-run:" + c.RunID, EvidenceID: "agent_task_queue:" + c.QueueTaskID, ActorID: "system:employee-learning", OccurredAt: *c.FinishedAt})
	if err != nil {
		if errors.Is(err, employeememory.ErrPreResetEvidence) {
			return skip("pre_reset_outcome")
		}
		if errors.Is(err, employeememory.ErrInvalidLearning) || errors.Is(err, employeememory.ErrInvalidScope) || errors.Is(err, employeememory.ErrUntrustedCorrection) {
			return skip("rejected_candidate")
		}
		return employeelearning.Result{}, err
	}
	return employeelearning.Result{LearningID: record.ID}, nil
}
func employeeLearningExcerpt(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := text[:limit]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + " [excerpt truncated]"
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
