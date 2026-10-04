package handler

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

// employeeTaskWakeExtension adds a wake kind's own input, tools and
// completion to the generic task wake, without a second worker.
type employeeTaskWakeExtension interface {
	extendInput(ctx context.Context, w *EmployeeSceneWorker, job employeeentry.Job, input *employeeSavedInput) error
	// executeTool runs inside the job's journaled tool transaction; handled
	// is false for tools it does not own.
	executeTool(ctx context.Context, tx pgx.Tx, w *EmployeeSceneWorker, job employeeentry.Job, call employeeloop.ToolCall) (employeeloop.ToolResult, bool, error)
	// completeTx runs inside the job's completion transaction, after the
	// generic completion enqueued its scene notice (if any).
	completeTx(ctx context.Context, tx pgx.Tx, w *EmployeeSceneWorker, job employeeentry.Job, saved employeeSavedOutcome, reply string) error
	// afterComplete runs after the completion committed.
	afterComplete(ctx context.Context, w *EmployeeSceneWorker, job employeeentry.Job)
}

func employeeTaskWakeExtensionFor(kind string) employeeTaskWakeExtension {
	if kind == employeeentry.TaskWakeExecutionProgress {
		return employeeProgressExtension{}
	}
	if kind == employeeentry.TaskWakeRoutineDecision {
		return employeeRoutineDecisionExtension{}
	}
	return nil
}
