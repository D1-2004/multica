package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	defaultAgentDispatchMessagePageSize = 100
	maxAgentDispatchMessagePageSize     = 200
)

type AgentDispatchTaskSummaryResponse struct {
	TaskID              string             `json:"task_id"`
	Status              string             `json:"status"`
	CreatedAt           string             `json:"created_at"`
	DispatchedAt        *string            `json:"dispatched_at"`
	StartedAt           *string            `json:"started_at"`
	CompletedAt         *string            `json:"completed_at"`
	DurationMS          *int64             `json:"duration_ms"`
	Provider            *string            `json:"provider"`
	Model               *string            `json:"model"`
	InputTokens         *int64             `json:"input_tokens"`
	OutputTokens        *int64             `json:"output_tokens"`
	CacheReadTokens     *int64             `json:"cache_read_tokens"`
	CacheWriteTokens    *int64             `json:"cache_write_tokens"`
	MessageCount        int32              `json:"message_count"`
	ToolCallCount       int32              `json:"tool_call_count"`
	UsageDetails        []TaskUsagePayload `json:"usage_details"`
	TranscriptAvailable bool               `json:"transcript_available"`
}

type AgentDispatchTaskMessagesResponse struct {
	Items      []protocol.TaskMessagePayload `json:"items"`
	NextCursor *string                       `json:"next_cursor"`
}

func (h *Handler) GetAgentDispatchTaskSummary(w http.ResponseWriter, r *http.Request) {
	task, ok := h.requireAgentDispatchTaskAccess(w, r)
	if !ok {
		return
	}

	usageRows, err := h.Queries.GetTaskUsage(r.Context(), task.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get task usage")
		return
	}
	messageSummary, err := h.Queries.GetTaskMessageSummary(r.Context(), task.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get task message summary")
		return
	}

	response := AgentDispatchTaskSummaryResponse{
		TaskID:              uuidToString(task.ID),
		Status:              task.Status,
		CreatedAt:           agentDispatchObservabilityTimestamp(task.CreatedAt),
		DispatchedAt:        agentDispatchObservabilityTimestampPtr(task.DispatchedAt),
		StartedAt:           agentDispatchObservabilityTimestampPtr(task.StartedAt),
		CompletedAt:         agentDispatchObservabilityTimestampPtr(task.CompletedAt),
		MessageCount:        messageSummary.MessageCount,
		ToolCallCount:       messageSummary.ToolCallCount,
		UsageDetails:        make([]TaskUsagePayload, 0, len(usageRows)),
		TranscriptAvailable: true,
	}
	if task.StartedAt.Valid && task.CompletedAt.Valid && !task.CompletedAt.Time.Before(task.StartedAt.Time) {
		durationMS := task.CompletedAt.Time.Sub(task.StartedAt.Time).Milliseconds()
		response.DurationMS = &durationMS
	}
	if len(usageRows) > 0 {
		var inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64
		for _, usage := range usageRows {
			inputTokens += usage.InputTokens
			outputTokens += usage.OutputTokens
			cacheReadTokens += usage.CacheReadTokens
			cacheWriteTokens += usage.CacheWriteTokens
			response.UsageDetails = append(response.UsageDetails, TaskUsagePayload{
				Provider:         usage.Provider,
				Model:            usage.Model,
				InputTokens:      usage.InputTokens,
				OutputTokens:     usage.OutputTokens,
				CacheReadTokens:  usage.CacheReadTokens,
				CacheWriteTokens: usage.CacheWriteTokens,
			})
		}
		response.InputTokens = &inputTokens
		response.OutputTokens = &outputTokens
		response.CacheReadTokens = &cacheReadTokens
		response.CacheWriteTokens = &cacheWriteTokens
		if len(usageRows) == 1 {
			provider := usageRows[0].Provider
			model := usageRows[0].Model
			response.Provider = &provider
			response.Model = &model
		}
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) ListAgentDispatchTaskMessages(w http.ResponseWriter, r *http.Request) {
	task, ok := h.requireAgentDispatchTaskAccess(w, r)
	if !ok {
		return
	}

	since := int64(-1)
	if rawSince := strings.TrimSpace(r.URL.Query().Get("since")); rawSince != "" {
		parsed, err := strconv.ParseInt(rawSince, 10, 32)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid since parameter")
			return
		}
		since = parsed
	}
	limit := defaultAgentDispatchMessagePageSize
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > maxAgentDispatchMessagePageSize {
			writeError(w, http.StatusBadRequest, "invalid limit parameter")
			return
		}
		limit = parsed
	}

	messages, err := h.Queries.ListTaskMessagesPage(r.Context(), db.ListTaskMessagesPageParams{
		TaskID: task.ID,
		Seq:    int32(since),
		Limit:  int32(limit + 1),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list task messages")
		return
	}

	var nextCursor *string
	if len(messages) > limit {
		messages = messages[:limit]
		cursor := strconv.Itoa(int(messages[len(messages)-1].Seq))
		nextCursor = &cursor
	}
	items := make([]protocol.TaskMessagePayload, len(messages))
	for index, message := range messages {
		items[index] = taskMessageToPayload(
			message,
			uuidToString(task.ID),
			uuidToString(task.IssueID),
		)
	}

	writeJSON(w, http.StatusOK, AgentDispatchTaskMessagesResponse{
		Items:      items,
		NextCursor: nextCursor,
	})
}

func (h *Handler) requireAgentDispatchTaskAccess(
	w http.ResponseWriter,
	r *http.Request,
) (db.AgentTaskQueue, bool) {
	dispatchContext, ok := h.resolveAgentDispatchContext(w, r)
	if !ok {
		return db.AgentTaskQueue{}, false
	}
	taskID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(chi.URLParam(r, "taskId")), "taskId")
	if !ok {
		return db.AgentTaskQueue{}, false
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          taskID,
		WorkspaceID: dispatchContext.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "task not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to resolve task")
		}
		return db.AgentTaskQueue{}, false
	}
	if task.AgentID != dispatchContext.AgentID {
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, false
	}
	return task, true
}

func agentDispatchObservabilityTimestamp(value pgtype.Timestamptz) string {
	if !value.Valid {
		return ""
	}
	return value.Time.UTC().Format(time.RFC3339Nano)
}

func agentDispatchObservabilityTimestampPtr(value pgtype.Timestamptz) *string {
	if !value.Valid {
		return nil
	}
	formatted := agentDispatchObservabilityTimestamp(value)
	return &formatted
}
