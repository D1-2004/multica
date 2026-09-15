package dshschedule

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

const EvidenceKind = string(attribution.EvidenceDSHSchedule)

type ExecutionReader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}
type Execution struct {
	dshhost.SessionScope
	Receipt
}

// LoadExecution reconstructs the complete ordered prompt from its committed
// occurrences. Missing or reordered rows cannot silently change the task input.
// The launch and claim boundaries both use this database-derived native scope.
func LoadExecution(ctx context.Context, db ExecutionReader, key dshhost.Key, taskID uuid.UUID) (Execution, error) {
	var e Execution
	if db == nil || key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || taskID == uuid.Nil {
		return e, ErrInvalid
	}
	e.SessionScope.Key = key
	e.TaskID = taskID
	rows, err := db.Query(ctx, `SELECT o.session_id,o.schedule_id,o.occurrence_at,o.request_id,
 s.owner_member_id,s.source_task_id,s.prompt,s.first_due_at,s.every_seconds,m.scope_kind,m.scope_id,o.batch_ordinal,b.request_id
 FROM dsh_schedule_occurrence o
 JOIN dsh_schedule s ON s.workspace_id=o.workspace_id AND s.agent_id=o.agent_id AND s.session_id=o.session_id AND s.schedule_id=o.schedule_id
 JOIN dsh_task_binding b ON b.workspace_id=o.workspace_id AND b.agent_id=o.agent_id AND b.task_id=o.task_id AND b.session_id=o.session_id
 JOIN dsh_employee_session m ON m.workspace_id=o.workspace_id AND m.agent_id=o.agent_id AND m.session_id=o.session_id
 WHERE o.workspace_id=$1 AND o.agent_id=$2 AND o.task_id=$3 ORDER BY o.batch_ordinal`, key.WorkspaceID, key.AgentID, taskID)
	if err != nil {
		return Execution{}, err
	}
	defer rows.Close()
	var reminders []Due
	var requestID uuid.UUID
	for rows.Next() {
		due := Due{Record: Record{Key: Key{WorkspaceID: key.WorkspaceID, AgentID: key.AgentID}}}
		var kind string
		var scope, request uuid.UUID
		var ordinal int
		if err := rows.Scan(&due.SessionID, &due.ScheduleID, &due.At, &due.RequestID, &due.OwnerMemberID, &due.SourceTaskID, &due.Prompt, &due.FirstDue, &due.EverySeconds, &kind, &scope, &ordinal, &request); err != nil {
			return Execution{}, err
		}
		if ordinal != len(reminders) || scope == uuid.Nil || (kind != "chat" && kind != "issue" && kind != "task") {
			return Execution{}, ErrInvalid
		}
		if len(reminders) == 0 {
			e.Kind, e.ID, requestID = kind, scope, request
		} else if e.Kind != kind || e.ID != scope || requestID != request {
			return Execution{}, ErrInvalid
		}
		reminders = append(reminders, due)
	}
	if err := rows.Err(); err != nil {
		return Execution{}, err
	}
	if len(reminders) == 0 {
		return Execution{}, pgx.ErrNoRows
	}
	batch, err := NewBatch(reminders)
	if err != nil || batch.RequestID != requestID {
		return Execution{}, ErrInvalid
	}
	e.Batch = batch
	return e, nil
}
