package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Langfuse export for agent tasks (the Daemon side of the loop).
//
// The daemon itself never talks to Langfuse: it already reports messages,
// usage, and completion to the server, and the FC runtime proxy relays every
// model request/response pair to /api/daemon/tasks/{id}/llm-traces. The
// server turns those into one Langfuse trace per task:
//
//   - the trace id is the task's chat trace (the task UUID for non-chat
//     tasks), so a task id pasted into Langfuse opens the run;
//   - the root observation id is deterministic (TaskLangfuseRootSpanID), so
//     the relay can parent generations under it before the task finishes;
//   - the root is emitted once, after the terminal status commits, with the
//     persisted transcript (tool calls, assistant text) as child observations
//     and the aggregated token usage.
//
// Local daemons therefore get task-level traces without any image rebuild;
// only the raw model bodies depend on the runtime image's llm_trace_v1 proxy.
const (
	taskTraceName        = "agent_task"
	taskTraceTag         = "agent_task"
	taskTraceMessageCap  = 300
	taskTraceTextBudget  = 4000
	taskTraceEmitTimeout = 10 * time.Second
	// TaskContextCoordinatorTraceKey is the task.context key the inbound
	// coordinator stamps on the Issue task it creates, so the task trace can
	// point back at the coordinator turn (coord_trace_id).
	TaskContextCoordinatorTraceKey = "coordinator_trace_id"
)

// TaskLangfuseRootSpanID is the deterministic root observation id of a task
// trace, shared by the completion hook and the sandbox LLM relay.
func TaskLangfuseRootSpanID(taskID string) string {
	return langfuse.DeterministicSpanID("task:" + strings.TrimSpace(taskID))
}

// taskTraceContext is the lookup-key subset of task.context.
type taskTraceContext struct {
	ConversationID     string
	ConversationTitle  string
	ConversationKind   string
	SenderName         string
	PersonID           string
	DWSUID             string
	DWSOrgID           string
	CoordinatorTraceID string
	Channel            string
	DispatchSource     string
	IssueTrigger       string
	SurfaceType        string
}

func parseTaskTraceContext(raw []byte) taskTraceContext {
	var payload map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
		return taskTraceContext{}
	}
	out := taskTraceContext{
		CoordinatorTraceID: rawString(payload[TaskContextCoordinatorTraceKey]),
		IssueTrigger:       rawString(payload["coordinator_issue_trigger"]),
	}
	if trace, present, err := chattrace.Parse(raw); err == nil && present {
		out.Channel = trace.Channel
	}
	var source struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(payload["dispatch_source"], &source) == nil {
		out.DispatchSource = strings.TrimSpace(source.Type)
	}
	var surface struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(payload["dispatch_surface"], &surface) == nil {
		out.SurfaceType = strings.TrimSpace(surface.Type)
	}
	var identity struct {
		DWS struct {
			UID   string `json:"uid"`
			OrgID string `json:"org_id"`
		} `json:"dws"`
	}
	if json.Unmarshal(payload["external_identity"], &identity) == nil {
		out.DWSUID = strings.TrimSpace(identity.DWS.UID)
		out.DWSOrgID = strings.TrimSpace(identity.DWS.OrgID)
	}
	var event struct {
		Conversation map[string]any `json:"conversation"`
		Sender       map[string]any `json:"sender"`
	}
	if json.Unmarshal(payload["dispatch_event_data"], &event) == nil {
		out.ConversationID = firstContextString(event.Conversation, "openConversationId", "open_conversation_id", "id")
		out.ConversationTitle = firstContextString(event.Conversation, "title", "conversationTitle", "conversation_title", "name")
		out.ConversationKind = firstContextString(event.Conversation, "type")
		out.SenderName = firstContextString(event.Sender, "displayName", "display_name", "name", "nick")
		out.PersonID = firstContextString(event.Sender, "staffId", "staff_id", "openDingTalkId", "senderOpenDingTalkId", "uid")
	}
	return out
}

func rawString(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func firstContextString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// TaskLangfuseTraceOptions builds the trace-level attributes (id, user,
// session, tags, lookup metadata) shared by every producer of a task trace.
func TaskLangfuseTraceOptions(task db.AgentTaskQueue, agent *db.Agent, runtime *db.AgentRuntime) langfuse.TraceOptions {
	taskID := util.UUIDToString(task.ID)
	traceID := ""
	if trace, err := chattrace.ForTask(task.Context, taskID, task.CreatedAt.Time); err == nil {
		traceID = trace.TraceID
	}
	tc := parseTaskTraceContext(task.Context)
	metadata := map[string]any{
		"loop":                taskTraceName,
		"task_id":             taskID,
		"trace_id":            traceID,
		"issue_id":            util.UUIDToString(task.IssueID),
		"agent_id":            util.UUIDToString(task.AgentID),
		"workspace_id":        "",
		"runtime_id":          util.UUIDToString(task.RuntimeID),
		"chat_session_id":     util.UUIDToString(task.ChatSessionID),
		"parent_task_id":      util.UUIDToString(task.ParentTaskID),
		"autopilot_run_id":    util.UUIDToString(task.AutopilotRunID),
		"squad_id":            util.UUIDToString(task.SquadID),
		"trigger_comment_id":  util.UUIDToString(task.TriggerCommentID),
		"initiator_user_id":   util.UUIDToString(task.InitiatorUserID),
		"originator_user_id":  util.UUIDToString(task.OriginatorUserID),
		"attempt":             int64(task.Attempt),
		"status":              strings.TrimSpace(task.Status),
		"session_id":          strings.TrimSpace(task.SessionID.String),
		"conversation_id":     tc.ConversationID,
		"conversation_name":   tc.ConversationTitle,
		"conversation_kind":   tc.ConversationKind,
		"sender_name":         tc.SenderName,
		"person_id":           tc.PersonID,
		"dws_uid":             tc.DWSUID,
		"dws_org_id":          tc.DWSOrgID,
		"coord_trace_id":      tc.CoordinatorTraceID,
		"channel":             tc.Channel,
		"dispatch_source":     tc.DispatchSource,
		"dispatch_surface":    tc.SurfaceType,
		"coordinator_trigger": tc.IssueTrigger,
	}
	tags := []string{taskTraceTag}
	if agent != nil {
		metadata["agent_name"] = strings.TrimSpace(agent.Name)
		metadata["workspace_id"] = util.UUIDToString(agent.WorkspaceID)
	}
	if runtime != nil {
		metadata["runtime_name"] = strings.TrimSpace(runtime.Name)
		metadata["runtime_mode"] = strings.TrimSpace(runtime.RuntimeMode)
		metadata["provider"] = strings.TrimSpace(runtime.Provider)
		metadata["daemon_id"] = strings.TrimSpace(runtime.DaemonID.String)
		if mode := strings.TrimSpace(runtime.RuntimeMode); mode != "" {
			tags = append(tags, "runtime:"+mode)
		}
		if provider := strings.TrimSpace(runtime.Provider); provider != "" {
			tags = append(tags, "provider:"+provider)
		}
	}
	if tc.Channel != "" {
		tags = append(tags, "channel:"+tc.Channel)
	}
	if tc.DispatchSource != "" {
		tags = append(tags, "source:"+tc.DispatchSource)
	}
	userID := tc.PersonID
	if userID == "" {
		userID = tc.DWSUID
	}
	if userID == "" {
		userID = util.UUIDToString(task.OriginatorUserID)
	}
	if userID == "" {
		userID = util.UUIDToString(task.InitiatorUserID)
	}
	sessionID := tc.ConversationID
	if sessionID == "" {
		sessionID = util.UUIDToString(task.ChatSessionID)
	}
	return langfuse.TraceOptions{
		TraceID:    traceID,
		RootSpanID: TaskLangfuseRootSpanID(taskID),
		Name:       taskTraceName,
		Type:       langfuse.TypeAgent,
		UserID:     userID,
		SessionID:  sessionID,
		Tags:       tags,
		Metadata:   metadata,
	}
}

// observeTaskTerminal exports the task trace after a terminal status commits.
// It runs detached from the request so the daemon's completion callback never
// waits on Langfuse or on the transcript read.
func (s *TaskService) observeTaskTerminal(ctx context.Context, task db.AgentTaskQueue) {
	if s == nil || s.Langfuse == nil || s.Queries == nil {
		return
	}
	go s.emitTaskTrace(context.WithoutCancel(ctx), task)
}

func (s *TaskService) emitTaskTrace(ctx context.Context, task db.AgentTaskQueue) {
	ctx, cancel := context.WithTimeout(ctx, taskTraceEmitTimeout)
	defer cancel()
	taskID := util.UUIDToString(task.ID)
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Warn("langfuse task trace panicked", "event", "langfuse_task_trace_panic", "task_id", taskID, "panic", recovered)
		}
	}()

	var agent *db.Agent
	if row, err := s.Queries.GetAgent(ctx, task.AgentID); err == nil {
		agent = &row
	}
	var runtime *db.AgentRuntime
	if task.RuntimeID.Valid {
		if row, err := s.Queries.GetAgentRuntime(ctx, task.RuntimeID); err == nil {
			runtime = &row
		}
	}
	opts := TaskLangfuseTraceOptions(task, agent, runtime)
	opts.StartTime = taskTraceStart(task)
	opts.Input = taskTraceInput(ctx, s.Queries, task)
	if opts.Metadata == nil {
		opts.Metadata = map[string]any{}
	}
	opts.Metadata["failure_reason"] = strings.TrimSpace(task.FailureReason.String)

	trace := s.Langfuse.StartTrace(ctx, opts)
	if trace == nil {
		return
	}
	endTime := time.Now()
	if task.CompletedAt.Valid {
		endTime = task.CompletedAt.Time
	}
	messages, err := s.Queries.ListTaskMessages(ctx, task.ID)
	if err != nil {
		slog.Warn("langfuse task trace: list messages failed", "task_id", taskID, "error", err)
	}
	emitTaskMessageObservations(trace, messages, endTime)

	usage, usageMeta := taskTraceUsage(ctx, s.Queries, task)
	if len(usageMeta) > 0 {
		trace.AddMetadata(usageMeta)
	}
	status := strings.TrimSpace(task.Status)
	trace.AddTags("status:" + status)
	output := map[string]any{
		"status":         status,
		"failure_reason": strings.TrimSpace(task.FailureReason.String),
		"message_count":  len(messages),
	}
	if result := taskTraceResult(task.Result); result != nil {
		output["result"] = result
	}
	if errText := strings.TrimSpace(task.Error.String); errText != "" {
		output["error"] = langfuseClip(errText, taskTraceTextBudget)
	}
	end := langfuse.EndOptions{EndTime: endTime, Output: output, Usage: usage}
	switch status {
	case "failed":
		message := strings.TrimSpace(task.Error.String)
		if message == "" {
			message = "task failed"
		}
		end.Err = errors.New(langfuseClip(message, 2000))
	case "canceled", "cancelled":
		end.Level = langfuse.LevelWarning
		end.StatusMessage = "task canceled"
	}
	trace.End(end)
}

func taskTraceStart(task db.AgentTaskQueue) time.Time {
	switch {
	case task.StartedAt.Valid:
		return task.StartedAt.Time
	case task.DispatchedAt.Valid:
		return task.DispatchedAt.Time
	case task.CreatedAt.Valid:
		return task.CreatedAt.Time
	}
	return time.Now()
}

func taskTraceInput(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) map[string]any {
	input := map[string]any{
		"trigger_summary": langfuseClip(strings.TrimSpace(task.TriggerSummary.String), taskTraceTextBudget),
		"handoff_note":    langfuseClip(strings.TrimSpace(task.HandoffNote.String), taskTraceTextBudget),
		"attempt":         task.Attempt,
	}
	if task.IssueID.Valid && queries != nil {
		if issue, err := queries.GetIssue(ctx, task.IssueID); err == nil {
			input["issue_title"] = langfuseClip(strings.TrimSpace(issue.Title), 200)
			input["issue_number"] = issue.Number
			input["issue_description"] = langfuseClip(strings.TrimSpace(issue.Description.String), taskTraceTextBudget)
		}
	}
	return input
}

func taskTraceResult(result []byte) any {
	trimmed := strings.TrimSpace(string(result))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if json.Valid([]byte(trimmed)) && len(trimmed) <= taskTraceTextBudget*4 {
		return json.RawMessage(trimmed)
	}
	return langfuseClip(trimmed, taskTraceTextBudget*4)
}

func taskTraceUsage(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (*langfuse.Usage, map[string]any) {
	if queries == nil {
		return nil, nil
	}
	rows, err := queries.GetTaskUsage(ctx, task.ID)
	if err != nil || len(rows) == 0 {
		return nil, nil
	}
	usage := &langfuse.Usage{}
	providers := make([]string, 0, len(rows))
	models := make([]string, 0, len(rows))
	for _, row := range rows {
		usage.Input += row.InputTokens
		usage.Output += row.OutputTokens
		usage.CacheRead += row.CacheReadTokens
		usage.CacheWrite += row.CacheWriteTokens
		if provider := strings.TrimSpace(row.Provider); provider != "" {
			providers = appendUnique(providers, provider)
		}
		if model := strings.TrimSpace(row.Model); model != "" {
			models = appendUnique(models, model)
		}
	}
	meta := map[string]any{
		"input_tokens":       usage.Input,
		"output_tokens":      usage.Output,
		"cache_read_tokens":  usage.CacheRead,
		"cache_write_tokens": usage.CacheWrite,
	}
	if len(models) > 0 {
		meta["model"] = strings.Join(models, ",")
	}
	if len(providers) > 0 {
		meta["usage_provider"] = strings.Join(providers, ",")
	}
	return usage, meta
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// emitTaskMessageObservations replays the persisted transcript as child
// observations: tool_use rows open a tool observation that the matching
// tool_result closes; assistant text and thinking become events.
func emitTaskMessageObservations(trace *langfuse.Trace, messages []db.TaskMessage, taskEnd time.Time) {
	if trace == nil {
		return
	}
	if len(messages) > taskTraceMessageCap {
		trace.Event(langfuse.ObservationOptions{
			Type: langfuse.TypeEvent, Name: "transcript_truncated",
			Metadata: map[string]any{"message_count": len(messages), "kept": taskTraceMessageCap},
		}, langfuse.EndOptions{Level: langfuse.LevelWarning, StatusMessage: "transcript longer than the Langfuse export cap"})
		messages = messages[:taskTraceMessageCap]
	}
	type openTool struct {
		obs  *langfuse.Observation
		tool string
	}
	var open []openTool
	closeTool := func(index int, output string, at time.Time) {
		entry := open[index]
		open = append(open[:index], open[index+1:]...)
		entry.obs.End(langfuse.EndOptions{EndTime: at, Output: messagePayload(output)})
	}
	for _, msg := range messages {
		at := taskEnd
		if msg.CreatedAt.Valid {
			at = msg.CreatedAt.Time
		}
		tool := strings.TrimSpace(msg.Tool.String)
		switch strings.TrimSpace(msg.Type) {
		case "tool_use", "tool-use":
			var input any
			if len(msg.Input) > 0 && json.Valid(msg.Input) {
				input = json.RawMessage(msg.Input)
			} else if len(msg.Input) > 0 {
				input = langfuseClip(string(msg.Input), taskTraceTextBudget)
			}
			name := tool
			if name == "" {
				name = "tool"
			}
			obs := trace.StartObservation(langfuse.ObservationOptions{
				Type:      langfuse.TypeTool,
				Name:      name,
				StartTime: at,
				Input:     input,
				Metadata:  map[string]any{"seq": int64(msg.Seq)},
			})
			if output := strings.TrimSpace(msg.Output.String); output != "" {
				obs.End(langfuse.EndOptions{EndTime: at, Output: messagePayload(output)})
				continue
			}
			open = append(open, openTool{obs: obs, tool: tool})
		case "tool_result", "tool-result":
			matched := false
			for i := len(open) - 1; i >= 0; i-- {
				if tool == "" || open[i].tool == tool {
					closeTool(i, msg.Output.String+msg.Content.String, at)
					matched = true
					break
				}
			}
			if !matched {
				trace.Event(langfuse.ObservationOptions{
					Type: langfuse.TypeTool, Name: nonEmpty(tool, "tool_result"), StartTime: at,
					Metadata: map[string]any{"seq": int64(msg.Seq)},
				}, langfuse.EndOptions{Output: messagePayload(msg.Output.String + msg.Content.String)})
			}
		case "text":
			trace.Event(langfuse.ObservationOptions{
				Type: langfuse.TypeEvent, Name: "assistant_text", StartTime: at,
				Metadata: map[string]any{"seq": int64(msg.Seq)},
			}, langfuse.EndOptions{Output: langfuseClip(strings.TrimSpace(msg.Content.String), taskTraceTextBudget)})
		case "thinking":
			trace.Event(langfuse.ObservationOptions{
				Type: langfuse.TypeEvent, Name: "thinking", StartTime: at,
				Metadata: map[string]any{"seq": int64(msg.Seq)},
			}, langfuse.EndOptions{Output: langfuseClip(strings.TrimSpace(msg.Content.String), taskTraceTextBudget), Level: langfuse.LevelDebug})
		case "status", "":
			// Lifecycle chatter; the root observation already carries status.
		default:
			trace.Event(langfuse.ObservationOptions{
				Type: langfuse.TypeEvent, Name: strings.TrimSpace(msg.Type), StartTime: at,
				Metadata: map[string]any{"seq": int64(msg.Seq), "tool": tool},
			}, langfuse.EndOptions{Output: langfuseClip(strings.TrimSpace(msg.Content.String+msg.Output.String), taskTraceTextBudget)})
		}
	}
	for len(open) > 0 {
		closeTool(len(open)-1, "", taskEnd)
	}
}

func messagePayload(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if json.Valid([]byte(raw)) && len(raw) <= taskTraceTextBudget*4 {
		return json.RawMessage(raw)
	}
	return langfuseClip(raw, taskTraceTextBudget*4)
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

// langfuseClip bounds text by rune count on a rune boundary.
func langfuseClip(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
