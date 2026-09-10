package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/langfuse"
)

const scenePrefetchTimeout = 2 * time.Second

// The current-scene lookup is already mandatory before any work plan. Perform
// that mechanical read in Host so the first model request can make a decision.
// Failure remains an unavailable read, not an empty result or authorization.
func (c *Coordinator) prefetchSceneRecall(ctx context.Context, turn *Turn, sequence *int) (functionCall, string, error) {
	arguments, _ := json.Marshal(map[string]any{"conversation_id": turn.ConversationID, "since": "48h", "limit": coordinationRecallDefault})
	call := functionCall{ID: "host-scene-prefetch", Name: toolAssocRecall, Arguments: string(arguments)}
	started := time.Now()
	lt := langfuse.TraceFromContext(ctx)
	var observation *langfuse.Observation
	if lt != nil {
		observation = lt.StartObservation(langfuse.ObservationOptions{Type: langfuse.TypeTool, Name: toolAssocRecall,
			Input: toolPayload(call.Arguments), Metadata: map[string]any{"origin": "host_prefetch", "tool_call_id": call.ID, "timeout_ms": scenePrefetchTimeout.Milliseconds()}})
	}
	readCtx, cancel := context.WithTimeout(ctx, scenePrefetchTimeout)
	result, err := c.callTool(readCtx, *turn, call.Name, call.Arguments)
	cancel()
	if err == nil {
		var envelope map[string]json.RawMessage
		if decodeErr := json.Unmarshal([]byte(result), &envelope); decodeErr != nil {
			err = fmt.Errorf("invalid scene recall envelope: %w", decodeErr)
		} else {
			for _, field := range []string{"status", "conversation_id"} {
				raw, present := envelope[field]
				if !present {
					continue
				}
				var value string
				if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
					err = fmt.Errorf("invalid scene recall %s", field)
					break
				}
				if field == "status" && value != "loaded" && value != "empty" {
					err = fmt.Errorf("scene recall is unavailable: %s", value)
					break
				}
				if field == "conversation_id" && value != turn.ConversationID {
					err = fmt.Errorf("scene recall returned a different conversation")
					break
				}
			}
		}
	}

	result, err = rememberCoordinationRead(turn, sequence, call.Name, call.Arguments, result, err)
	var envelope struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal([]byte(result), &envelope)
	status := envelope.Status
	if err != nil {
		status = "unavailable"
	}
	elapsed := time.Since(started).Milliseconds()
	if lt != nil {
		lt.AddMetadata(map[string]any{"scene_prefetch_status": status, "scene_prefetch_elapsed_ms": elapsed})
	}
	traceToolEnd(observation, result, err, "host_prefetch")
	slog.Info("inbound coordinator scene prefetch", append(coordinatorLogIndex(*turn),
		"event", "inbound_coordinator_scene_prefetch", "origin", "host_prefetch", "status", status,
		"elapsed_ms", elapsed, "read_snapshot_count", len(turn.CoordinationReads), "timeout_ms", scenePrefetchTimeout.Milliseconds(), "arguments", call.Arguments, "result", clipRunes(result, llmLogToolBudget))...)
	return call, result, err
}

// Observed group chatter must establish relevance before unrelated old work
// enters its first decision. The same loop can still recall on demand and
// retains the mandatory recall-before-work gate; no classifier call is added.
func shouldPrefetchSceneRecall(turn Turn) bool {
	if (turn.Loop != "" && turn.Loop != LoopInbound) || turn.ConversationID == "" {
		return false
	}
	return !(turn.ProactiveConversation && !turn.Addressed && strings.EqualFold(turn.ChatType, "group"))
}
