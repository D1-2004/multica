package inboundcoord

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const coordinatorIssueFollowUpContextKey = "coordinator_issue_follow_up"
const coordinatorIssueTriggerContextKey = "coordinator_issue_trigger"

// CoordinatorTraceIDContextKey is the task.context key that points an Issue
// task back at the coordinator turn that created it. The task trace exporter
// (service.TaskContextCoordinatorTraceKey) reads the same key.
const CoordinatorTraceIDContextKey = "coordinator_trace_id"

// CoordinatorTraceTagsContextKey carries the turn's Langfuse trace tags so a
// task joining the turn's trace repeats them; Langfuse keeps the tag set of
// whichever span it processes last, so every producer must agree.
const CoordinatorTraceTagsContextKey = "coordinator_trace_tags"

// StampCoordinatorTrace makes a task started by a coordinator turn share that
// turn's Langfuse trace: it installs the turn's trace id as the task's chat
// trace when the context carries none (channel names the inbound surface),
// and stamps coordinator_trace_id plus coordinator_trace_tags. Any failure
// returns raw unchanged so tracing never blocks task creation.
func StampCoordinatorTrace(raw []byte, decision Decision, channel string, startedAt time.Time) []byte {
	return WithCoordinatorTraceTags(WithCoordinatorTrace(raw, decision.TraceID, channel, startedAt), decision.TraceTags)
}

// WithCoordinatorTrace installs the coordinator trace id as the task's chat
// trace when none is present and stamps coordinator_trace_id.
func WithCoordinatorTrace(raw []byte, traceID, channel string, startedAt time.Time) []byte {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return raw
	}
	if _, present, err := chattrace.Parse(raw); err == nil && !present {
		if trace, err := chattrace.From(traceID, channel, startedAt.UnixMilli()); err == nil {
			if merged, err := chattrace.Merge(raw, trace); err == nil {
				raw = merged
			}
		}
	}
	return WithCoordinatorTraceID(raw, traceID)
}

// WithCoordinatorTraceTags stamps the turn's trace tags on a task context.
func WithCoordinatorTraceTags(raw []byte, tags []string) []byte {
	cleaned := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			cleaned = append(cleaned, tag)
		}
	}
	if len(cleaned) == 0 {
		return raw
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return raw
	}
	return withContextKey(raw, CoordinatorTraceTagsContextKey, encoded)
}

// WithCoordinatorTraceID stamps the coordinator trace id on a task context.
// It returns raw unchanged when the id is empty or the context cannot be
// decoded, so tracing never blocks Issue creation.
func WithCoordinatorTraceID(raw []byte, traceID string) []byte {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return raw
	}
	encoded, err := json.Marshal(traceID)
	if err != nil {
		return raw
	}
	return withContextKey(raw, CoordinatorTraceIDContextKey, encoded)
}

// withContextKey sets one key on a JSON object context, treating an empty or
// null context as {}. An undecodable context is returned unchanged.
func withContextKey(raw []byte, key string, value json.RawMessage) []byte {
	payload := map[string]json.RawMessage{}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
			return raw
		}
	}
	payload[key] = value
	merged, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return merged
}

type CoordinatorIssueTrigger string

const (
	CoordinatorIssueTriggerCreate  CoordinatorIssueTrigger = "new_issue"
	CoordinatorIssueTriggerComment CoordinatorIssueTrigger = "issue_comment"
)

// IndependentIssueTaskContext turns a coordinator-created Issue task into an
// independent relay task. The short loop owns the Router completion; the Issue
// task keeps the DingTalk identity and scene but cannot reuse that callback.
func IndependentIssueTaskContext(raw []byte, trigger CoordinatorIssueTrigger) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("coordinator issue dispatch context is required")
	}
	if trigger != CoordinatorIssueTriggerCreate && trigger != CoordinatorIssueTriggerComment {
		return nil, errors.New("coordinator issue trigger is invalid")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode coordinator issue dispatch context: %w", err)
	}
	surface, err := json.Marshal(map[string]string{"type": protocol.DispatchSurfaceTypeIssue})
	if err != nil {
		return nil, fmt.Errorf("encode coordinator issue surface: %w", err)
	}
	payload[protocol.DispatchSurfaceJSONKey] = surface
	payload[coordinatorIssueFollowUpContextKey] = json.RawMessage("true")
	triggerJSON, err := json.Marshal(trigger)
	if err != nil {
		return nil, fmt.Errorf("encode coordinator issue trigger: %w", err)
	}
	payload[coordinatorIssueTriggerContextKey] = triggerJSON
	delete(payload, "completion_callback")
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode coordinator issue dispatch context: %w", err)
	}
	return encoded, nil
}
