package inboundcoord

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const coordinatorIssueFollowUpContextKey = "coordinator_issue_follow_up"
const coordinatorIssueTriggerContextKey = "coordinator_issue_trigger"

// CoordinatorTraceIDContextKey is the task.context key that points an Issue
// task back at the coordinator turn that created it. The task trace exporter
// (service.TaskContextCoordinatorTraceKey) reads the same key.
const CoordinatorTraceIDContextKey = "coordinator_trace_id"

// WithCoordinatorTraceID stamps the coordinator trace id on a task context.
// It returns raw unchanged when the id is empty or the context cannot be
// decoded, so tracing never blocks Issue creation.
func WithCoordinatorTraceID(raw []byte, traceID string) []byte {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" || len(raw) == 0 {
		return raw
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return raw
	}
	encoded, err := json.Marshal(traceID)
	if err != nil {
		return raw
	}
	payload[CoordinatorTraceIDContextKey] = encoded
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
