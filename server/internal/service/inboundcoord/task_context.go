package inboundcoord

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const coordinatorIssueFollowUpContextKey = "coordinator_issue_follow_up"
const coordinatorIssueTriggerContextKey = "coordinator_issue_trigger"

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
	if err := ensureCoordinatorIssueDispatchEnvelope(payload); err != nil {
		return nil, err
	}
	delete(payload, "completion_callback")
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode coordinator issue dispatch context: %w", err)
	}
	return encoded, nil
}

// ensureCoordinatorIssueDispatchEnvelope fills the claim-time dispatch
// keys a Stream robot callback omits. Digital-employee Router already
// carries them; overwriting would drop that identity. Without them
// dispatchInstructionAppliesTo rejects the context and the sandbox never
// receives the same send-then-finish follow-up as digital employees.
func ensureCoordinatorIssueDispatchEnvelope(payload map[string]json.RawMessage) error {
	if _, ok := payload["dispatch_source"]; !ok {
		sourceType := "robot"
		raw, err := json.Marshal(map[string]string{"platform": "dingtalk", "type": sourceType})
		if err != nil {
			return fmt.Errorf("encode coordinator issue dispatch source: %w", err)
		}
		payload["dispatch_source"] = raw
	}
	if _, ok := payload["dispatch_domain"]; !ok {
		payload["dispatch_domain"] = json.RawMessage(`"channel"`)
	}
	if _, ok := payload["dispatch_type"]; !ok {
		payload["dispatch_type"] = json.RawMessage(`"message.created"`)
	}
	if _, ok := payload["dispatch_outbound"]; !ok {
		raw, err := json.Marshal(map[string]string{
			"mode":    protocol.DispatchOutboundModeDWS,
			"replyTo": protocol.DispatchReplyToLatestMessage,
		})
		if err != nil {
			return fmt.Errorf("encode coordinator issue dispatch outbound: %w", err)
		}
		payload[protocol.DispatchOutboundJSONKey] = raw
	}
	return nil
}
