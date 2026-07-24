package agentmessagerouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var executionResultCallbackPattern = regexp.MustCompile(`^/api/v1/dispatch-tasks/([A-Za-z0-9_-]{1,128})/execution-result$`)

type ExecutionResultRequest struct {
	RequestID         string         `json:"requestId"`
	AgentID           string         `json:"agentId"`
	ExternalTaskID    string         `json:"externalTaskId"`
	ExternalRunID     string         `json:"externalRunId,omitempty"`
	ExternalSessionID string         `json:"externalSessionId,omitempty"`
	ExecutionStatus   string         `json:"executionStatus"`
	ResultMessage     string         `json:"resultMessage"`
	ExecutionResult   map[string]any `json:"executionResult,omitempty"`
	RawPayload        map[string]any `json:"rawPayload,omitempty"`
}

type ExecutionResultDeliveryError struct {
	status int
	code   string
}

func (e *ExecutionResultDeliveryError) Error() string {
	if e.code != "" {
		return fmt.Sprintf("agent message router execution result failed with status %d (%s)", e.status, e.code)
	}
	return fmt.Sprintf("agent message router execution result failed with status %d", e.status)
}

func (e *ExecutionResultDeliveryError) Retryable() bool {
	if e == nil {
		return false
	}
	if e.status == http.StatusRequestTimeout ||
		e.status == http.StatusTooManyRequests ||
		e.status >= http.StatusInternalServerError {
		return true
	}
	switch e.code {
	case "internal_error", "service_unavailable", "too_many_requests", "timeout",
		"invalid_response", "protocol_mismatch":
		return true
	default:
		return false
	}
}

func (c *Client) SubmitExecutionResult(
	ctx context.Context,
	callbackPath string,
	result ExecutionResultRequest,
) error {
	callbackMatch := executionResultCallbackPattern.FindStringSubmatch(callbackPath)
	if len(callbackMatch) != 2 {
		return errors.New("agent message router execution result callback path is invalid")
	}
	body, err := json.Marshal(result)
	if err != nil {
		return errors.New("encode agent message router execution result")
	}
	response, err := c.do(ctx, http.MethodPost, callbackPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return &ExecutionResultDeliveryError{status: response.StatusCode}
	}
	responseBody, err := readRouterResponseBody(response.Body)
	if err != nil {
		return &ExecutionResultDeliveryError{status: response.StatusCode, code: "invalid_response"}
	}
	var acknowledgement struct {
		Success *bool  `json:"success"`
		Code    string `json:"code"`
		Data    *struct {
			DispatchTaskID    string `json:"dispatchTaskId"`
			ExecutionStatus   string `json:"executionStatus"`
			ExecutionReportID string `json:"executionReportId"`
		} `json:"data"`
	}
	if len(bytes.TrimSpace(responseBody)) == 0 ||
		json.Unmarshal(responseBody, &acknowledgement) != nil ||
		acknowledgement.Success == nil {
		return &ExecutionResultDeliveryError{
			status: response.StatusCode,
			code:   "invalid_response",
		}
	}
	if !*acknowledgement.Success {
		return &ExecutionResultDeliveryError{
			status: response.StatusCode,
			code:   safeRouterErrorCode(acknowledgement.Code),
		}
	}
	if acknowledgement.Code != "success" ||
		acknowledgement.Data == nil ||
		acknowledgement.Data.DispatchTaskID != callbackMatch[1] ||
		acknowledgement.Data.ExecutionStatus != result.ExecutionStatus ||
		strings.TrimSpace(acknowledgement.Data.ExecutionReportID) == "" {
		return &ExecutionResultDeliveryError{
			status: response.StatusCode,
			code:   "protocol_mismatch",
		}
	}
	return nil
}
