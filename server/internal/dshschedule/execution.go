package dshschedule

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const EvidenceKind = string(attribution.EvidenceDSHSchedule)

type RowReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Execution struct {
	dshhost.SessionScope
	Receipt
}

// LoadExecution reconstructs task input from the committed occurrence and
// immutable registration, never a caller-supplied context/prompt. Both launcher
// and daemon use it, including unscoped follow-ups whose new task ID is different
// from the task that originally created the native Session.
func LoadExecution(ctx context.Context, db RowReader, key dshhost.Key, taskID uuid.UUID) (Execution, error) {
	var e Execution
	if db == nil || key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || taskID == uuid.Nil {
		return e, ErrInvalid
	}
	e.SessionScope.Key = key
	e.Receipt.Due.Record.Key = Key{WorkspaceID: key.WorkspaceID, AgentID: key.AgentID}
	e.TaskID = taskID
	err := db.QueryRow(ctx, `SELECT o.session_id,o.schedule_id,o.occurrence_at,o.request_id,
 s.owner_member_id,s.source_task_id,s.prompt,s.first_due_at,s.every_seconds,m.scope_kind,m.scope_id
 FROM dsh_schedule_occurrence o
 JOIN dsh_schedule s ON s.workspace_id=o.workspace_id AND s.agent_id=o.agent_id AND s.session_id=o.session_id AND s.schedule_id=o.schedule_id
 JOIN dsh_task_binding b ON b.workspace_id=o.workspace_id AND b.agent_id=o.agent_id AND b.task_id=o.task_id AND b.session_id=o.session_id AND b.request_id=o.request_id
 JOIN dsh_employee_session m ON m.workspace_id=o.workspace_id AND m.agent_id=o.agent_id AND m.session_id=o.session_id
 WHERE o.workspace_id=$1 AND o.agent_id=$2 AND o.task_id=$3`, key.WorkspaceID, key.AgentID, taskID).Scan(
		&e.Receipt.SessionID, &e.ScheduleID, &e.At, &e.RequestID, &e.OwnerMemberID, &e.SourceTaskID, &e.Prompt, &e.FirstDue, &e.EverySeconds, &e.Kind, &e.ID)
	if err != nil {
		return Execution{}, err
	}
	planned, err := Plan(e.Record, e.At, e.At)
	if err != nil || planned.RequestID != e.RequestID || e.ID == uuid.Nil || (e.Kind != "chat" && e.Kind != "issue" && e.Kind != "task") {
		return Execution{}, ErrInvalid
	}
	return e, nil
}

func (e Execution) NativePrompt() *protocol.DSHNativePrompt {
	text := e.Due.Framing()
	return &protocol.DSHNativePrompt{SessionID: e.Receipt.SessionID, RequestID: e.RequestID.String(), Mode: "queue",
		Content: []protocol.DSHNativePromptPart{{Type: "text", Text: &text}}}
}
