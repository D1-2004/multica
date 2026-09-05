package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

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
	// TaskContextCoordinatorTraceTagsKey carries that turn's trace tags.
	TaskContextCoordinatorTraceTagsKey = "coordinator_trace_tags"
	// coordinatorTraceName is the trace name of a coordinator turn; a task
	// joining that trace repeats it (inboundcoord owns the constant, but
	// service cannot import inboundcoord without a cycle in tests).
	coordinatorTraceName = "inbound_coordinator"
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
	// CoordinatorTraceTags are the tags of the coordinator turn whose trace
	// this task joins; the task repeats them so the trace keeps one tag set.
	CoordinatorTraceTags []string
	Channel              string
	DispatchSource       string
	IssueTrigger         string
	SurfaceType          string
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
	if raw, ok := payload[TaskContextCoordinatorTraceTagsKey]; ok {
		var tags []string
		if json.Unmarshal(raw, &tags) == nil {
			for _, tag := range tags {
				if tag = strings.TrimSpace(tag); tag != "" {
					out.CoordinatorTraceTags = append(out.CoordinatorTraceTags, tag)
				}
			}
		}
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
	workspaceID := ""
	if agent != nil {
		metadata["agent_name"] = strings.TrimSpace(agent.Name)
		workspaceID = util.UUIDToString(agent.WorkspaceID)
		metadata["workspace_id"] = workspaceID
	}
	if runtime != nil {
		metadata["runtime_name"] = strings.TrimSpace(runtime.Name)
		metadata["runtime_mode"] = strings.TrimSpace(runtime.RuntimeMode)
		metadata["provider"] = strings.TrimSpace(runtime.Provider)
		metadata["daemon_id"] = strings.TrimSpace(runtime.DaemonID.String)
		tags = append(tags, langfuse.Tag("runtime", runtime.RuntimeMode), langfuse.Tag("provider", runtime.Provider))
	}
	tags = append(tags, langfuse.Tag("channel", tc.Channel), langfuse.Tag("source", tc.DispatchSource))
	userID := taskTraceUserID(task, tc)
	sessionID := tc.ConversationID
	if sessionID == "" {
		sessionID = util.UUIDToString(task.ChatSessionID)
	}
	// Ids the Langfuse API can only filter through tags on this deployment.
	tags = append(tags,
		langfuse.Tag("agent", util.UUIDToString(task.AgentID)),
		langfuse.Tag("workspace", workspaceID),
		langfuse.Tag("user", userID),
		langfuse.Tag("task", taskID),
		langfuse.Tag("issue", util.UUIDToString(task.IssueID)),
	)
	cleaned := tags[:0]
	for _, tag := range tags {
		if tag != "" {
			cleaned = append(cleaned, tag)
		}
	}
	tags = cleaned
	// A task started by a coordinator turn joins that turn's trace. Langfuse
	// resolves a trace's name and tags from whichever span it processes last,
	// so the task repeats the turn's name and tags instead of its own; the
	// root observation is still named agent_task, the task's runtime and
	// provider stay available as metadata, and the task/issue ids remain
	// reachable through the idx.* events (TaskIndexKeys).
	traceName := ""
	if tc.CoordinatorTraceID != "" {
		traceName = coordinatorTraceName
		tags = tc.CoordinatorTraceTags
		if len(tags) == 0 {
			tags = []string{coordinatorTraceName}
		}
		// Trace metadata merges per key, last writer wins, so the task must
		// not repeat the turn's conversation-level values (its DingTalk
		// conversation type "single" would overwrite kind "p2p", its loop
		// would overwrite inbound_coordinator). Langfuse also folds a root
		// observation's metadata into the trace, so the keys are dropped
		// rather than moved onto the agent_task root; the turn already
		// carries them.
		metadata = withoutCoordinatorOwnedMetadata(metadata)
	}
	return langfuse.TraceOptions{
		TraceID:    traceID,
		RootSpanID: TaskLangfuseRootSpanID(taskID),
		Name:       taskTraceName,
		TraceName:  traceName,
		Type:       langfuse.TypeAgent,
		UserID:     userID,
		SessionID:  sessionID,
		Tags:       tags,
		Metadata:   metadata,
	}
}

// coordinatorOwnedMetadata are the trace-level keys a coordinator turn writes
// (inboundcoord.coordinatorTraceOptions); a task joining the turn's trace
// must not send them on any of its spans.
var coordinatorOwnedMetadata = map[string]bool{
	"loop": true, "conversation_id": true, "conversation_name": true, "conversation_kind": true,
	"sender_name": true, "person_id": true, "dws_uid": true, "dws_org_id": true,
	"agent_id": true, "agent_name": true, "workspace_id": true, "chat_session_id": true,
}

func withoutCoordinatorOwnedMetadata(metadata map[string]any) map[string]any {
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if !coordinatorOwnedMetadata[key] {
			out[key] = value
		}
	}
	return out
}

// taskTraceUserID is the user a task's trace is filed under: the DingTalk
// person, then the DWS uid, then the originating and initiating Multica users.
func taskTraceUserID(task db.AgentTaskQueue, tc taskTraceContext) string {
	for _, candidate := range []string{tc.PersonID, tc.DWSUID, util.UUIDToString(task.OriginatorUserID), util.UUIDToString(task.InitiatorUserID)} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

// TaskIndexKeys are the ids a reader may hold when looking for a task's
// trace that no tag, session or trace id already covers; each becomes an
// "idx.<key>.<value>" event under the task root's "index" node. The sandbox
// relay and the completion hook both emit them (deterministic ids, so they
// upsert). A task-owned trace carries agent, workspace, user, task and issue
// as tags and the conversation (or the chat session) as its session. A task
// that joined a coordinator turn's trace repeats the turn's tags, so its task
// and issue ids go into the index, while the conversation, agent and user
// were indexed by the turn.
func TaskIndexKeys(task db.AgentTaskQueue) map[string]string {
	tc := parseTaskTraceContext(task.Context)
	keys := map[string]string{
		"runtime_id":         util.UUIDToString(task.RuntimeID),
		"parent_task_id":     util.UUIDToString(task.ParentTaskID),
		"autopilot_run_id":   util.UUIDToString(task.AutopilotRunID),
		"trigger_comment_id": util.UUIDToString(task.TriggerCommentID),
		"session_id":         strings.TrimSpace(task.SessionID.String),
	}
	if tc.CoordinatorTraceID != "" {
		keys["task_id"] = util.UUIDToString(task.ID)
		keys["issue_id"] = util.UUIDToString(task.IssueID)
		return keys
	}
	if tc.ConversationID != "" {
		keys["chat_session_id"] = util.UUIDToString(task.ChatSessionID)
	}
	tagged := taskTraceUserID(task, tc)
	for key, value := range map[string]string{
		"person_id":          tc.PersonID,
		"dws_uid":            tc.DWSUID,
		"originator_user_id": util.UUIDToString(task.OriginatorUserID),
		"initiator_user_id":  util.UUIDToString(task.InitiatorUserID),
	} {
		if value != "" && value != tagged {
			keys[key] = value
		}
	}
	return keys
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
	trace.Index(TaskIndexKeys(task))
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

// coalesceTaskMessages merges runs of consecutive streamed chunks of the same
// kind (thinking, text) into one message so a transcript reads as a few
// paragraphs instead of hundreds of token-sized events. Tool rows are kept.
func coalesceTaskMessages(messages []db.TaskMessage) []db.TaskMessage {
	out := make([]db.TaskMessage, 0, len(messages))
	for _, msg := range messages {
		kind := strings.TrimSpace(msg.Type)
		if (kind == "text" || kind == "thinking") && len(out) > 0 {
			last := &out[len(out)-1]
			if strings.TrimSpace(last.Type) == kind {
				last.Content = pgtype.Text{String: last.Content.String + msg.Content.String, Valid: true}
				continue
			}
		}
		out = append(out, msg)
	}
	return out
}

// emitTaskMessageObservations replays the persisted transcript as child
// observations: tool_use rows open a tool observation that the matching
// tool_result closes; assistant text and thinking become events, one per
// contiguous run of streamed chunks.
func emitTaskMessageObservations(trace *langfuse.Trace, messages []db.TaskMessage, taskEnd time.Time) {
	if trace == nil {
		return
	}
	messages = coalesceTaskMessages(messages)
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
