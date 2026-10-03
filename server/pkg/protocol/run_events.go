package protocol

import "time"

// TaskEventSource is observation-only metadata. Identity and authorization are
// supplied by the host, never by an adapter or model-generated text.
type TaskEventSource struct {
	SessionID  string    `json:"session_id,omitempty"`
	TurnID     string    `json:"turn_id,omitempty"`
	MessageID  string    `json:"message_id,omitempty"`
	CallID     string    `json:"call_id,omitempty"`
	Phase      string    `json:"phase,omitempty"`
	Status     string    `json:"status,omitempty"`
	Level      string    `json:"level,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

// TaskEventContext is stored beside the existing transcript body, not a second
// copy of it. TaskID below is the Employee task; QueueTaskID is the execution.
type TaskEventContext struct {
	Version      int             `json:"version"`
	WorkspaceID  string          `json:"workspace_id"`
	AgentID      string          `json:"agent_id"`
	SceneID      string          `json:"scene_id,omitempty"`
	TaskID       string          `json:"employee_task_id,omitempty"`
	RunID        string          `json:"employee_run_id,omitempty"`
	GoalRevision int64           `json:"goal_revision,omitempty"`
	RuntimeID    string          `json:"runtime_id"`
	Provider     string          `json:"provider,omitempty"`
	Source       TaskEventSource `json:"source"`
}

// TaskRunEvent is the fact-only view consumed by the Employee Loop. Diagnostic
// bodies and tool input/output deliberately have no fields in this contract.
type TaskRunEvent struct {
	TaskEventContext
	EventID       string `json:"event_id"`
	QueueTaskID   string `json:"queue_task_id"`
	Seq           int    `json:"seq"`
	Kind          string `json:"kind"`
	Role          string `json:"role"`
	Reportability string `json:"reportability"`
	Content       string `json:"content,omitempty"`
	Tool          string `json:"tool,omitempty"`
	CreatedAt     string `json:"created_at"`
}

type TaskRunEventPage struct {
	Events  []TaskRunEvent `json:"events"`
	NextSeq int            `json:"next_seq"`
	HasMore bool           `json:"has_more"`
}
