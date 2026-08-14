package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type TaskExecutionUsageSummary struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

type TaskExecutionCurrentSandboxSummary struct {
	ID        string  `json:"id"`
	ScopeType string  `json:"scope_type"`
	ScopeID   string  `json:"scope_id"`
	Status    string  `json:"status"`
	ExpiresAt *string `json:"expires_at"`
}

type TaskExecutionRuntimeSummary struct {
	ID             string                              `json:"id"`
	Name           string                              `json:"name"`
	Mode           string                              `json:"mode"`
	Provider       string                              `json:"provider"`
	Status         string                              `json:"status"`
	Metadata       json.RawMessage                     `json:"metadata,omitempty"`
	CurrentSandbox *TaskExecutionCurrentSandboxSummary `json:"current_sandbox,omitempty"`
}

type TaskExecutionSummary struct {
	TaskID                string                       `json:"task_id"`
	Status                string                       `json:"status"`
	CreatedAt             string                       `json:"created_at"`
	DispatchedAt          *string                      `json:"dispatched_at"`
	StartedAt             *string                      `json:"started_at"`
	CompletedAt           *string                      `json:"completed_at"`
	FirstEffectiveReplyAt *string                      `json:"first_effective_reply_at"`
	DurationMS            *int64                       `json:"duration_ms"`
	Provider              *string                      `json:"provider"`
	Model                 *string                      `json:"model"`
	InputTokens           *int64                       `json:"input_tokens"`
	OutputTokens          *int64                       `json:"output_tokens"`
	CacheReadTokens       *int64                       `json:"cache_read_tokens"`
	CacheWriteTokens      *int64                       `json:"cache_write_tokens"`
	MessageCount          int32                        `json:"message_count"`
	ToolCallCount         int32                        `json:"tool_call_count"`
	UsageDetails          []TaskExecutionUsageSummary  `json:"usage_details"`
	TranscriptAvailable   bool                         `json:"transcript_available"`
	Runtime               *TaskExecutionRuntimeSummary `json:"runtime,omitempty"`
}

func BuildTaskExecutionSummary(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
) (TaskExecutionSummary, error) {
	usageRows, err := queries.GetTaskUsage(ctx, task.ID)
	if err != nil {
		return TaskExecutionSummary{}, err
	}
	messageSummary, err := queries.GetTaskMessageSummary(ctx, task.ID)
	if err != nil {
		return TaskExecutionSummary{}, err
	}
	runtimeSummary, err := buildTaskExecutionRuntimeSummary(ctx, queries, task)
	if err != nil {
		return TaskExecutionSummary{}, err
	}

	summary := TaskExecutionSummary{
		TaskID:                util.UUIDToString(task.ID),
		Status:                task.Status,
		CreatedAt:             taskExecutionTimestamp(task.CreatedAt),
		DispatchedAt:          taskExecutionTimestampPtr(task.DispatchedAt),
		StartedAt:             taskExecutionTimestampPtr(task.StartedAt),
		CompletedAt:           taskExecutionTimestampPtr(task.CompletedAt),
		FirstEffectiveReplyAt: taskExecutionTimestampPtr(messageSummary.FirstEffectiveReplyAt),
		MessageCount:          messageSummary.MessageCount,
		ToolCallCount:         messageSummary.ToolCallCount,
		UsageDetails:          make([]TaskExecutionUsageSummary, 0, len(usageRows)),
		TranscriptAvailable:   true,
		Runtime:               runtimeSummary,
	}
	if task.StartedAt.Valid && task.CompletedAt.Valid && !task.CompletedAt.Time.Before(task.StartedAt.Time) {
		durationMS := task.CompletedAt.Time.Sub(task.StartedAt.Time).Milliseconds()
		summary.DurationMS = &durationMS
	}
	if len(usageRows) > 0 {
		var inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64
		for _, usage := range usageRows {
			inputTokens += usage.InputTokens
			outputTokens += usage.OutputTokens
			cacheReadTokens += usage.CacheReadTokens
			cacheWriteTokens += usage.CacheWriteTokens
			summary.UsageDetails = append(summary.UsageDetails, TaskExecutionUsageSummary{
				Provider:         usage.Provider,
				Model:            usage.Model,
				InputTokens:      usage.InputTokens,
				OutputTokens:     usage.OutputTokens,
				CacheReadTokens:  usage.CacheReadTokens,
				CacheWriteTokens: usage.CacheWriteTokens,
			})
		}
		summary.InputTokens = &inputTokens
		summary.OutputTokens = &outputTokens
		summary.CacheReadTokens = &cacheReadTokens
		summary.CacheWriteTokens = &cacheWriteTokens
		if len(usageRows) == 1 {
			provider := usageRows[0].Provider
			model := usageRows[0].Model
			summary.Provider = &provider
			summary.Model = &model
		}
	}
	return summary, nil
}

func buildTaskExecutionRuntimeSummary(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
) (*TaskExecutionRuntimeSummary, error) {
	if !task.RuntimeID.Valid {
		return nil, nil
	}
	runtime, err := queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	summary := &TaskExecutionRuntimeSummary{
		ID:       util.UUIDToString(runtime.ID),
		Name:     runtime.Name,
		Mode:     runtime.RuntimeMode,
		Provider: runtime.Provider,
		Status:   runtime.Status,
	}
	if !IsFCE2BRuntime(runtime) {
		return summary, nil
	}
	summary.Metadata = json.RawMessage(runtime.Metadata)
	var metadata struct {
		Template string `json:"template"`
	}
	if json.Unmarshal(runtime.Metadata, &metadata) != nil || strings.TrimSpace(metadata.Template) == "" {
		return summary, nil
	}
	scopeType, scopeID, ok := taskExecutionSandboxScope(task)
	if !ok {
		return summary, nil
	}
	session, err := queries.GetActiveFCE2BSandboxSession(ctx, db.GetActiveFCE2BSandboxSessionParams{
		RuntimeID: task.RuntimeID,
		ScopeType: scopeType,
		ScopeID:   scopeID,
		Template:  strings.TrimSpace(metadata.Template),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return summary, nil
		}
		return nil, err
	}
	summary.CurrentSandbox = &TaskExecutionCurrentSandboxSummary{
		ID:        session.SandboxID,
		ScopeType: session.ScopeType,
		ScopeID:   util.UUIDToString(session.ScopeID),
		Status:    session.Status,
		ExpiresAt: taskExecutionTimestampPtr(session.ExpiresAt),
	}
	return summary, nil
}

func taskExecutionSandboxScope(task db.AgentTaskQueue) (string, pgtype.UUID, bool) {
	if task.ChatSessionID.Valid {
		return "chat", task.ChatSessionID, true
	}
	if task.IssueID.Valid {
		return "issue", task.IssueID, true
	}
	return "", pgtype.UUID{}, false
}

func taskExecutionTimestamp(value pgtype.Timestamptz) string {
	if !value.Valid {
		return ""
	}
	return value.Time.UTC().Format(time.RFC3339Nano)
}

func taskExecutionTimestampPtr(value pgtype.Timestamptz) *string {
	if !value.Valid {
		return nil
	}
	formatted := taskExecutionTimestamp(value)
	return &formatted
}
