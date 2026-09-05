package scenememory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"

	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Langfuse instrumentation for the memory loop. One claimed flush is one
// trace; the DWS history read is a retriever observation and every merge
// round is a generation. The scene key (the DingTalk conversation id) is the
// Langfuse session so a conversation's coordinator turns and memory flushes
// sit side by side, and the coordinator trace that triggered the flush is
// kept as coord_trace_id for cross-lookup.
const (
	flushTraceName       = "scene_memory_flush"
	flushTraceTag        = "scene_memory"
	flushTraceTextBudget = 4000
)

type flushOutcome struct {
	EventCount     int
	CaughtUp       bool
	Replace        bool
	MemoryRevision int64
	MemoryText     string
	CursorAt       time.Time
}

func (f *MemoryFlusher) startFlushTrace(ctx context.Context, row db.SceneMemory, started time.Time) *langfuse.Trace {
	if f == nil || f.Langfuse == nil {
		return nil
	}
	agentName := ""
	if f.Agents != nil && row.AgentID.Valid {
		if agent, err := f.Agents.GetAgent(ctx, row.AgentID); err == nil {
			agentName = strings.TrimSpace(agent.Name)
		}
	}
	trace := f.Langfuse.StartTrace(ctx, flushTraceOptions(row, agentName, started))
	trace.Index(flushIndexKeys(row))
	return trace
}

// flushIndexKeys are the ids a reader may hold when looking for this flush.
// flushIndexKeys are the flush's ids that nothing else makes searchable: the
// session is the scene key and the tags carry the agent and the workspace,
// so only the row id and the triggering turn (coord_trace_id, plus the job id
// when it differs) are indexed.
func flushIndexKeys(row db.SceneMemory) map[string]string {
	coord := strings.TrimSpace(row.LastTriggerCoordTraceID)
	keys := map[string]string{
		"scene_memory_id": util.UUIDToString(row.ID),
		"coord_trace_id":  coord,
	}
	if job := util.UUIDToString(row.LastTriggerJobID); job != "" && job != coord {
		keys["job_id"] = job
	}
	return keys
}

func flushTraceOptions(row db.SceneMemory, agentName string, started time.Time) langfuse.TraceOptions {
	metadata := map[string]any{
		"loop":                flushTraceName,
		"agent_name":          agentName,
		"scene_memory_id":     util.UUIDToString(row.ID),
		"scene_key":           strings.TrimSpace(row.SceneKey),
		"conversation_id":     strings.TrimSpace(row.SceneKey),
		"scene_kind":          strings.TrimSpace(row.SceneKind),
		"conversation_kind":   strings.TrimSpace(row.SceneKind),
		"scene_title":         clipRunes(strings.TrimSpace(row.SceneTitle), 80),
		"conversation_name":   clipRunes(strings.TrimSpace(row.SceneTitle), 80),
		"platform":            strings.TrimSpace(row.Platform),
		"org_id":              strings.TrimSpace(row.OrgID),
		"dws_org_id":          strings.TrimSpace(row.OrgID),
		"workspace_id":        util.UUIDToString(row.WorkspaceID),
		"agent_id":            util.UUIDToString(row.AgentID),
		"memory_revision":     row.MemoryRevision,
		"dirty_revision":      row.DirtyRevision,
		"flushed_revision":    row.FlushedRevision,
		"attempt":             int64(row.AttemptCount),
		"coord_trace_id":      strings.TrimSpace(row.LastTriggerCoordTraceID),
		"trigger_job_id":      util.UUIDToString(row.LastTriggerJobID),
		"trigger_evidence_id": strings.TrimSpace(row.LastTriggerEvidenceID),
		"model":               flushModel,
	}
	tags := []string{flushTraceTag}
	for _, tag := range []string{
		langfuse.Tag("kind", row.SceneKind),
		langfuse.Tag("agent", util.UUIDToString(row.AgentID)),
		langfuse.Tag("workspace", util.UUIDToString(row.WorkspaceID)),
	} {
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	input := map[string]any{
		"memory_revision":       row.MemoryRevision,
		"current_memory":        clipRunes(strings.TrimSpace(row.MemoryText), flushTraceTextBudget),
		"lease_target_revision": row.LeaseTargetDirtyRevision.Int64,
	}
	if row.SourceCursorAt.Valid {
		input["source_cursor_at"] = row.SourceCursorAt.Time.UTC().Format(time.RFC3339)
	}
	if row.LeaseTargetThroughAt.Valid {
		input["lease_target_through_at"] = row.LeaseTargetThroughAt.Time.UTC().Format(time.RFC3339)
	}
	return langfuse.TraceOptions{
		Name:      flushTraceName,
		Type:      langfuse.TypeChain,
		SessionID: strings.TrimSpace(row.SceneKey),
		Tags:      tags,
		Metadata:  metadata,
		Input:     input,
		StartTime: started,
	}
}

func finishFlushTrace(t *langfuse.Trace, outcome *flushOutcome, err error) {
	if t == nil {
		return
	}
	status := "committed"
	switch {
	case err != nil:
		status = "error:" + FlushErrorCode(err)
	case !outcome.CaughtUp:
		status = "partial"
	}
	t.AddMetadata(map[string]any{
		"status":              status,
		"event_count":         outcome.EventCount,
		"caught_up":           outcome.CaughtUp,
		"replace":             outcome.Replace,
		"new_memory_revision": outcome.MemoryRevision,
		"error_code":          errorCodeOrEmpty(err),
	})
	output := map[string]any{
		"status":              status,
		"event_count":         outcome.EventCount,
		"caught_up":           outcome.CaughtUp,
		"replace":             outcome.Replace,
		"new_memory_revision": outcome.MemoryRevision,
	}
	if outcome.Replace {
		output["memory_text"] = clipRunes(strings.TrimSpace(outcome.MemoryText), flushTraceTextBudget)
	}
	if !outcome.CursorAt.IsZero() {
		output["cursor_at"] = outcome.CursorAt.UTC().Format(time.RFC3339)
	}
	end := langfuse.EndOptions{Output: output, Err: err}
	if err != nil && !TerminalFlushCode(FlushErrorCode(err)) {
		// Retryable failures (history not visible yet, transient DWS errors)
		// are expected on the way to a committed flush.
		end.Err = nil
		end.Level = langfuse.LevelWarning
		end.StatusMessage = err.Error()
	}
	t.End(end)
}

func errorCodeOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return FlushErrorCode(err)
}

func traceHistoryRead(t *langfuse.Trace, row db.SceneMemory) *langfuse.Observation {
	if t == nil {
		return nil
	}
	input := map[string]any{"scene_key": strings.TrimSpace(row.SceneKey)}
	if row.SourceCursorAt.Valid {
		input["since"] = row.SourceCursorAt.Time.UTC().Format(time.RFC3339)
	}
	if row.LeaseTargetThroughAt.Valid {
		input["until"] = row.LeaseTargetThroughAt.Time.UTC().Format(time.RFC3339)
	}
	return t.StartObservation(langfuse.ObservationOptions{
		Type:  langfuse.TypeRetriever,
		Name:  "dws_history_range",
		Input: input,
	})
}

func endHistoryRead(obs *langfuse.Observation, events []HistoryEvent, err error) {
	if obs == nil {
		return
	}
	end := langfuse.EndOptions{Err: err}
	if err == nil {
		end.Output = map[string]any{"event_count": len(events)}
		if len(events) > 0 {
			end.Output.(map[string]any)["oldest"] = events[0].OccurredAt.UTC().Format(time.RFC3339)
			end.Output.(map[string]any)["newest"] = events[len(events)-1].OccurredAt.UTC().Format(time.RFC3339)
		}
	}
	obs.End(end)
}

func traceFlushGeneration(t *langfuse.Trace, round int, messages []openai.ChatCompletionMessageParamUnion) *langfuse.Observation {
	if t == nil {
		return nil
	}
	return t.StartObservation(langfuse.ObservationOptions{
		Type:            langfuse.TypeGeneration,
		Name:            fmt.Sprintf("memory_flush.round.%d", round+1),
		Model:           flushModel,
		ModelParameters: map[string]any{"tools": []string{"memory_flush_commit"}},
		Input:           messages,
		Metadata:        map[string]any{"round": round + 1},
	})
}

func endFlushGeneration(gen *langfuse.Observation, completion *openai.ChatCompletion, err error) {
	if gen == nil {
		return
	}
	end := langfuse.EndOptions{Err: err}
	if completion != nil {
		usage := &langfuse.Usage{
			Input:     completion.Usage.PromptTokens,
			Output:    completion.Usage.CompletionTokens,
			Total:     completion.Usage.TotalTokens,
			CacheRead: completion.Usage.PromptTokensDetails.CachedTokens,
		}
		if usage.Input > 0 || usage.Output > 0 || usage.Total > 0 {
			end.Usage = usage
		}
		if len(completion.Choices) > 0 {
			choice := completion.Choices[0]
			if raw := strings.TrimSpace(choice.Message.RawJSON()); raw != "" {
				end.Output = json.RawMessage(raw)
			} else {
				end.Output = choice.Message
			}
			end.Metadata = map[string]any{"finish_reason": choice.FinishReason}
		}
	}
	gen.End(end)
}

// traceFlushCommit records the memory_flush_commit tool call the server
// accepted or rejected in one round.
func traceFlushCommit(t *langfuse.Trace, round int, callID, arguments string, accepted bool, reason string) {
	if t == nil {
		return
	}
	var input any = clipRunes(strings.TrimSpace(arguments), flushTraceTextBudget*2)
	if json.Valid([]byte(arguments)) {
		input = json.RawMessage(arguments)
	}
	end := langfuse.EndOptions{Output: map[string]any{"accepted": accepted, "reason": reason}}
	if !accepted {
		end.Level = langfuse.LevelWarning
		end.StatusMessage = reason
	}
	t.Event(langfuse.ObservationOptions{
		Type:     langfuse.TypeTool,
		Name:     "memory_flush_commit",
		Input:    input,
		Metadata: map[string]any{"round": round + 1, "tool_call_id": callID},
	}, end)
}

func traceFlushNudge(t *langfuse.Trace, round int, content string) {
	if t == nil {
		return
	}
	t.Event(langfuse.ObservationOptions{
		Type:     langfuse.TypeEvent,
		Name:     "memory_flush.nudge",
		Input:    clipRunes(strings.TrimSpace(content), flushTraceTextBudget),
		Metadata: map[string]any{"round": round + 1},
	}, langfuse.EndOptions{
		Level:         langfuse.LevelWarning,
		StatusMessage: "assistant answered without memory_flush_commit",
	})
}
