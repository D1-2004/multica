package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/multica-ai/multica/server/internal/employeetask"
)

const employeeCompletionNoticeContextKey = "employee_completion_notice_policy"

func employeeCompletionNoticePolicy(args map[string]any, source employeeSourceMessage) (employeetask.CompletionNoticePolicy, error) {
	policy := employeetask.CompletionNoticePolicy{Mode: employeetask.CompletionNoticeAlways}
	value, ok := args["completion_notice_policy"]
	if !ok {
		return policy, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return policy, err
	}
	var request struct {
		Mode             employeetask.CompletionNoticeMode `json:"mode"`
		RequireDelivery  string                            `json:"require_delivery"`
		InstructionQuote string                            `json:"instruction_quote"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Mode == "" {
		return policy, errors.New("invalid completion_notice_policy")
	}
	policy.Mode, policy.RequireDelivery, policy.InstructionQuote = request.Mode, request.RequireDelivery, request.InstructionQuote
	if policy.Mode == employeetask.CompletionNoticeIfNotDelivered {
		policy.SourceRef = source.SourceRef
	}
	return validateEmployeeCompletionNoticePolicy(policy, source)
}
func validateEmployeeCompletionNoticePolicy(policy employeetask.CompletionNoticePolicy, source employeeSourceMessage) (employeetask.CompletionNoticePolicy, error) {
	policy, err := employeetask.NormalizeCompletionNoticePolicy(policy, source.SourceRef)
	if err != nil {
		return policy, err
	}
	if policy.Mode == employeetask.CompletionNoticeIfNotDelivered {
		// Only the selected speaker's actual request is evidence. History, quoted
		// material, model prompts and messages by another speaker cannot opt out.
		if source.Message.ReferencedMessage != nil || source.Message.Reaction != nil || !strings.Contains(source.Message.Text, policy.InstructionQuote) {
			return policy, errors.New("completion notice exemption quote must occur in the selected source message")
		}
	}
	return policy, nil
}
