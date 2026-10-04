package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

// The Host side of scene routine occurrences that run as EmployeeTask Direct
// executions (service/employee_routine_task.go). The routine outbox stays
// the only sender; this file provides the silent admission hook,
// delivery-target check, replica gate and recovery of a
// terminal execution whose AutopilotRun was never settled.

var _ service.EmployeeRoutineHost = (*Handler)(nil)

// EmployeeRoutineReady gates the new queue-context shape: every live replica
// must advertise the EmployeeLoop marker whose reader understands a Direct
// execution that carries a real autopilot_run_id and an automation origin.
func (h *Handler) EmployeeRoutineReady(ctx context.Context) error {
	if h == nil || h.DingTalkResponses == nil || h.TxStarter == nil {
		return errors.New("routine notices are unavailable")
	}
	return h.employeeNoticeReplicasReady(ctx)
}

// CheckRoutineDeliveryTx validates, with database reads only, that the
// routine's notices can be addressed in its scene as the agent's identity.
func (h *Handler) CheckRoutineDeliveryTx(ctx context.Context, tx pgx.Tx, routine contextcap.Routine) error {
	if source := routine.Source; source != nil {
		if err := source.ValidateBinding(routine.WorkspaceID, routine.AgentID, routine.TenantOrgID, routine.SceneID); err != nil {
			return fmt.Errorf("%w: source scope changed", service.ErrRoutineDeliveryTarget)
		}
		// Stored materials are provenance, not an enduring grant. Revalidate the
		// source's exact scope and requester before compiling them into a new Run.
		if source.EmployeeTaskID != "" {
			var available bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_task t
 JOIN employee_task_run r ON r.task_id=t.id AND r.workspace_id=t.workspace_id AND r.agent_id=t.agent_id AND r.tenant_org_id=t.tenant_org_id
 WHERE t.id=$1::uuid AND t.workspace_id=$2::uuid AND t.agent_id=$3::uuid AND t.tenant_org_id=$4 AND t.scene_id=$5::uuid
 AND t.scope_kind='scene' AND t.requester_ref=$6 AND t.state <> 'cancelled' AND r.id=$7::uuid AND r.queue_task_id=$8::uuid)`,
				source.EmployeeTaskID, routine.WorkspaceID, routine.AgentID, routine.TenantOrgID, routine.SceneID,
				source.RequesterRef, source.EmployeeRunID, source.QueueTaskID).Scan(&available)
			if err != nil {
				return err
			}
			if !available {
				return fmt.Errorf("%w: original task material is no longer available in this scene", service.ErrRoutineDeliveryTarget)
			}
		}
	}
	in, err := h.routineNoticeInput(ctx, h.Queries.WithTx(tx), routine, "check")
	if err != nil {
		return classifyRoutineDeliveryError(err)
	}
	if in.DWSUID == "" || in.DWSOrgID == "" || in.ConversationID == "" || (!in.IsGroup && in.SenderOpenDingTalkID == "") {
		return fmt.Errorf("%w: the scene's conversation or recipient is unknown", service.ErrRoutineDeliveryTarget)
	}
	return nil
}

func classifyRoutineDeliveryError(err error) error {
	var refusal *sceneRoutineError
	if errors.As(err, &refusal) || errors.Is(err, scene.ErrNotFound) || errors.Is(err, scene.ErrStaleTenant) {
		return fmt.Errorf("%w: %v", service.ErrRoutineDeliveryTarget, err)
	}
	return err
}

// EnqueueRoutineStartNoticeTx deliberately has no chat effect for Employee
// routine work. Admission and progress remain observable in run history;
// the routine outbox delivers the terminal business result exactly once.
func (h *Handler) EnqueueRoutineStartNoticeTx(ctx context.Context, tx pgx.Tx, notice service.RoutineStartNotice) error {
	return nil
}

func (h *Handler) NotifyRoutineNotices() {
	if h != nil && h.DingTalkResponses != nil {
		h.DingTalkResponses.Notify()
	}
}

// ReconcileEmployeeRoutineRuns settles routine-origin executions whose queue
// row reached a terminal status while their AutopilotRun still says running,
// for example after a lost task event, and routine decisions whose wake job
// ended without settling them. SyncRunFromTask settles the run and posts the
// end notice only when it does not exist yet.
func (h *Handler) ReconcileEmployeeRoutineRuns(ctx context.Context, limit int) (int, error) {
	if h == nil || h.DB == nil || h.AutopilotService == nil || limit < 1 || limit > 1000 {
		return 0, nil
	}
	rows, err := h.DB.Query(ctx, `SELECT o.queue_task_id FROM autopilot_run ar
 JOIN (SELECT autopilot_run_id,autopilot_id,queue_task_id FROM employee_routine_occurrence WHERE state='accepted'
       UNION ALL
       SELECT autopilot_run_id,autopilot_id,queue_task_id FROM employee_webhook_occurrence WHERE state='accepted') o
   ON o.autopilot_run_id=ar.id AND o.autopilot_id=ar.autopilot_id
 JOIN agent_task_queue q ON q.id=o.queue_task_id AND q.autopilot_run_id=ar.id
 WHERE ar.status='running' AND q.status IN ('completed','failed','cancelled')
 ORDER BY q.completed_at NULLS FIRST, q.id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	var ids []pgtype.UUID
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	// Decisions whose wake ended without one (held, or completed by a binary
	// without the decision extension) settle as failed so the routine is not
	// overlapped forever.
	decided, err := h.AutopilotService.ReconcileRoutineDecisions(ctx, limit)
	if err != nil {
		return decided, err
	}
	settled := decided
	for _, id := range ids {
		task, err := h.Queries.GetAgentTask(ctx, id)
		if err != nil {
			continue
		}
		h.AutopilotService.SyncRunFromTask(ctx, task)
		settled++
		slog.InfoContext(ctx, "employee routine run settled by reconciliation", "queue_task_id", util.UUIDToString(id),
			"autopilot_run_id", util.UUIDToString(task.AutopilotRunID), "status", task.Status)
	}
	return settled, nil
}
