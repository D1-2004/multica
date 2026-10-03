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
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/util"
)

// The Host side of scene routine occurrences that run as EmployeeTask Direct
// executions (service/employee_routine_task.go). The routine's own start and
// end notices stay the only sender; this file adds the in-transaction start
// notice, the delivery-target check, the replica gate and the recovery of a
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

// EnqueueRoutineStartNoticeTx records the start notice of an accepted
// occurrence in the admission transaction. It is idempotent on the run id.
func (h *Handler) EnqueueRoutineStartNoticeTx(ctx context.Context, tx pgx.Tx, notice service.RoutineStartNotice) error {
	if h.DingTalkResponses == nil {
		return errors.New("routine notices are unavailable")
	}
	in, err := h.routineNoticeInput(ctx, h.Queries.WithTx(tx), notice.Routine, routineStartText(notice.Title, notice.Run, notice.Timezone))
	if err != nil {
		return classifyRoutineDeliveryError(err)
	}
	_, err = h.DingTalkResponses.EnqueueRoutineNotice(ctx, tx, in, util.UUIDToString(notice.Run.ID), dingtalkresponse.RoutineNoticeStart)
	return err
}

// NotifyRoutineNotices wakes the response outbox after a commit.
func (h *Handler) NotifyRoutineNotices() {
	if h != nil && h.DingTalkResponses != nil {
		h.DingTalkResponses.Notify()
	}
}

// ReconcileEmployeeRoutineRuns settles routine-origin executions whose queue
// row reached a terminal status while their AutopilotRun still says running,
// for example after a lost task event. SyncRunFromTask settles the run and
// posts the end notice only when it does not exist yet.
func (h *Handler) ReconcileEmployeeRoutineRuns(ctx context.Context, limit int) (int, error) {
	if h == nil || h.DB == nil || h.AutopilotService == nil || limit < 1 || limit > 1000 {
		return 0, nil
	}
	rows, err := h.DB.Query(ctx, `SELECT o.queue_task_id FROM autopilot_run ar
 JOIN employee_routine_occurrence o ON o.autopilot_run_id=ar.id AND o.autopilot_id=ar.autopilot_id AND o.state='accepted'
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
	settled := 0
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
