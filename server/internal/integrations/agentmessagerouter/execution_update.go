package agentmessagerouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
)

var executionUpdateCallbackPattern = regexp.MustCompile(`^/api/v1/dispatch-tasks/([A-Za-z0-9_-]{1,128})/execution-update$`)

type ExecutionUpdateExtension struct {
	IssueID         string `json:"issueId"`
	IssueIdentifier string `json:"issueIdentifier"`
	TargetTaskID    string `json:"targetTaskId"`
	TargetAgentID   string `json:"targetAgentId"`
}

type ExecutionUpdateRequest struct {
	RequestID      string                   `json:"requestId"`
	AgentID        string                   `json:"agentId"`
	ExternalTaskID string                   `json:"externalTaskId"`
	UpdateType     string                   `json:"updateType"`
	OccurredAt     int64                    `json:"occurredAt"`
	ResultMessage  string                   `json:"resultMessage,omitempty"`
	Extension      ExecutionUpdateExtension `json:"extension"`
}

func (c *Client) SubmitExecutionUpdate(
	ctx context.Context,
	callbackPath string,
	update ExecutionUpdateRequest,
) (*DWSDelivery, error) {
	callbackMatch := executionUpdateCallbackPattern.FindStringSubmatch(callbackPath)
	if len(callbackMatch) != 2 {
		return nil, errors.New("agent message router execution update callback path is invalid")
	}
	body, err := json.Marshal(update)
	if err != nil {
		return nil, errors.New("encode agent message router execution update")
	}
	response, err := c.do(ctx, http.MethodPost, callbackPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &ExecutionResultDeliveryError{status: response.StatusCode}
	}
	responseBody, err := readRouterResponseBody(response.Body)
	if err != nil {
		return nil, &ExecutionResultDeliveryError{status: response.StatusCode, code: "invalid_response"}
	}
	var acknowledgement struct {
		Success *bool  `json:"success"`
		Code    string `json:"code"`
		Data    *struct {
			DWSDelivery    *DWSDelivery `json:"dwsDelivery"`
			DispatchTaskID string       `json:"dispatchTaskId"`
			RequestID      string       `json:"requestId"`
			UpdateType     string       `json:"updateType"`
		} `json:"data"`
	}
	if len(bytes.TrimSpace(responseBody)) == 0 ||
		json.Unmarshal(responseBody, &acknowledgement) != nil ||
		acknowledgement.Success == nil {
		return nil, &ExecutionResultDeliveryError{
			status: response.StatusCode,
			code:   "invalid_response",
		}
	}
	if !*acknowledgement.Success {
		return nil, &ExecutionResultDeliveryError{
			status: response.StatusCode,
			code:   safeRouterErrorCode(acknowledgement.Code),
		}
	}
	if acknowledgement.Code != "success" ||
		acknowledgement.Data == nil ||
		acknowledgement.Data.DispatchTaskID != callbackMatch[1] ||
		acknowledgement.Data.RequestID != update.RequestID ||
		acknowledgement.Data.UpdateType != update.UpdateType {
		return nil, &ExecutionResultDeliveryError{
			status: response.StatusCode,
			code:   "protocol_mismatch",
		}
	}
	return acknowledgement.Data.DWSDelivery, nil
}
