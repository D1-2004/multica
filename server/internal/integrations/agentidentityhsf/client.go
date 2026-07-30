package agentidentityhsf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	dapr "github.com/dapr/go-sdk/client"
)

const (
	defaultBindingName  = "hsf.consumer"
	defaultDaprGRPCPort = "50001"
	defaultTimeout      = 10 * time.Second
	maxTTLSeconds       = 900

	serviceInterface = "com.dingtalk.ailab.agentidentity.client.AgentIdentityContextService"
	serviceVersion   = "1.0.0"
	serviceGroup     = "HSF"

	createMethodName    = "createAgentIdentityContext"
	createParameterType = "com.dingtalk.ailab.agentidentity.client.request.CreateAgentIdentityContextRequest"
	extendMethodName    = "extendAgentIdentityContext"
	extendParameterType = "com.dingtalk.ailab.agentidentity.client.request.ExtendAgentIdentityContextRequest"

	createRequestClass = "com.dingtalk.ailab.agentidentity.client.request.CreateAgentIdentityContextRequest"
	extendRequestClass = "com.dingtalk.ailab.agentidentity.client.request.ExtendAgentIdentityContextRequest"
	contextClass       = "com.dingtalk.ailab.agentidentity.client.model.AgentRuntimeContext"
	identityClass      = "com.dingtalk.ailab.agentidentity.client.model.AgentIdentitySpec"
)

// CreateContextRequest identifies the identities represented by a short-lived
// Agent Identity context. The caller is responsible for keeping the returned
// token private and passing it only to the runtime that will redeem it.
type CreateContextRequest struct {
	RequestID          string
	TaskID             string
	AgentID            string
	RuntimeType        string
	RuntimeID          string
	Reason             string
	Source             map[string]string
	UID                string
	OrgID              string
	GithubConnectionID string
	TTLSeconds         int
}

type CreateContextResult struct {
	ContextID    string
	ContextToken string
	ExpiresAt    int64
}

type ExtendContextRequest struct {
	ContextToken       string
	TaskID             string
	AgentID            string
	RuntimeType        string
	RuntimeID          string
	Reason             string
	Source             map[string]string
	GithubConnectionID string
}

type ExtendContextResult struct {
	ContextID string
	ExpiresAt int64
}

// ValidationError reports a malformed request without sending anything to HSF.
type ValidationError struct {
	Field string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid Agent Identity field: %s", e.Field)
}

// ServiceError is an application-level error returned by Agent Identity.
// ErrorMessage is deliberately not retained because this path must never
// surface values echoed by a downstream service.
type ServiceError struct {
	Code string
}

func (e *ServiceError) Error() string {
	return fmt.Sprintf("Agent Identity HSF request failed: %s", e.Code)
}

type bindingInvoker interface {
	InvokeBinding(context.Context, *dapr.InvokeBindingRequest) (*dapr.BindingEvent, error)
	Close()
}

type dialBinding func(context.Context, string) (bindingInvoker, error)

type Client struct {
	address     string
	bindingName string
	appName     string
	timeout     time.Duration
	dial        dialBinding
}

func NewClient() *Client {
	return &Client{
		address:     daprAddressFromEnv(),
		bindingName: defaultBindingName,
		appName:     strings.TrimSpace(os.Getenv("APP_NAME")),
		timeout:     defaultTimeout,
		dial: func(ctx context.Context, address string) (bindingInvoker, error) {
			return dapr.NewClientWithAddressContext(ctx, address)
		},
	}
}

func daprAddressFromEnv() string {
	if endpoint := strings.TrimSpace(os.Getenv("DAPR_GRPC_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	port := strings.TrimSpace(os.Getenv("DAPR_GRPC_PORT"))
	if port == "" {
		port = defaultDaprGRPCPort
	}
	return net.JoinHostPort("127.0.0.1", port)
}

func (c *Client) CreateContext(ctx context.Context, req CreateContextRequest) (CreateContextResult, error) {
	req, err := validateCreateRequest(req)
	if err != nil {
		return CreateContextResult{}, err
	}

	identities := identitiesForRequest(req.UID, req.OrgID, req.GithubConnectionID)
	payload := []createContextPayload{{
		RequestID:  req.RequestID,
		Identities: identities,
		Context:    runtimeContextForRequest(req.TaskID, req.AgentID, req.RuntimeType, req.RuntimeID, req.Reason, req.Source),
		Class:      createRequestClass,
		TTLSeconds: req.TTLSeconds,
	}}
	data, err := json.Marshal(payload)
	if err != nil {
		return CreateContextResult{}, errors.New("encode Agent Identity HSF request")
	}

	event, err := c.invoke(ctx, createMethodName, createParameterType, data)
	if err != nil {
		return CreateContextResult{}, err
	}
	if event == nil {
		return CreateContextResult{}, errors.New("Agent Identity HSF returned no response")
	}

	var response createContextResponse
	if err := json.Unmarshal(event.Data, &response); err != nil {
		return CreateContextResult{}, errors.New("decode Agent Identity HSF response")
	}
	if !response.Success {
		return CreateContextResult{}, &ServiceError{Code: safeErrorCode(response.ErrorCode)}
	}
	if strings.TrimSpace(response.ContextID) == "" || strings.TrimSpace(response.ContextToken) == "" || response.ExpiresAt <= 0 {
		return CreateContextResult{}, errors.New("Agent Identity HSF returned an incomplete response")
	}

	return CreateContextResult{
		ContextID:    response.ContextID,
		ContextToken: response.ContextToken,
		ExpiresAt:    response.ExpiresAt,
	}, nil
}

func (c *Client) ExtendContext(ctx context.Context, req ExtendContextRequest) (ExtendContextResult, error) {
	req, err := validateExtendRequest(req)
	if err != nil {
		return ExtendContextResult{}, err
	}

	payload := []extendContextPayload{{
		ContextToken: req.ContextToken,
		Identities:   identitiesForRequest("", "", req.GithubConnectionID),
		Context:      runtimeContextForRequest(req.TaskID, req.AgentID, req.RuntimeType, req.RuntimeID, req.Reason, req.Source),
		Class:        extendRequestClass,
	}}
	data, err := json.Marshal(payload)
	if err != nil {
		return ExtendContextResult{}, errors.New("encode Agent Identity HSF extend request")
	}

	event, err := c.invoke(ctx, extendMethodName, extendParameterType, data)
	if err != nil {
		return ExtendContextResult{}, err
	}
	if event == nil {
		return ExtendContextResult{}, errors.New("Agent Identity HSF returned no response")
	}

	var response extendContextResponse
	if err := json.Unmarshal(event.Data, &response); err != nil {
		return ExtendContextResult{}, errors.New("decode Agent Identity HSF extend response")
	}
	if !response.Success {
		return ExtendContextResult{}, &ServiceError{Code: safeErrorCode(response.ErrorCode)}
	}
	if strings.TrimSpace(response.ContextID) == "" || response.ExpiresAt <= 0 {
		return ExtendContextResult{}, errors.New("Agent Identity HSF returned an incomplete extend response")
	}
	return ExtendContextResult{
		ContextID: response.ContextID,
		ExpiresAt: response.ExpiresAt,
	}, nil
}

func (c *Client) invoke(ctx context.Context, methodName string, parameterType string, data []byte) (*dapr.BindingEvent, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	invoker, err := c.dial(timeoutCtx, c.address)
	if err != nil {
		return nil, fmt.Errorf("connect to Dapr sidecar: %w", err)
	}
	defer invoker.Close()

	metadata := map[string]string{
		"rpc-interface-name":         serviceInterface,
		"rpc-version":                serviceVersion,
		"rpc-group":                  serviceGroup,
		"rpc-method-name":            methodName,
		"rpc-method-parameter-types": parameterType,
		"serialization-type":         "application/json",
		"rpc-generic":                "true",
		"rpc-timeout":                strconv.FormatInt(c.timeout.Milliseconds(), 10),
	}
	if c.appName != "" {
		metadata["appName"] = c.appName
	}

	event, err := invoker.InvokeBinding(timeoutCtx, &dapr.InvokeBindingRequest{
		Name:      c.bindingName,
		Operation: "invoke",
		Data:      data,
		Metadata:  metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("invoke Agent Identity HSF binding: %w", err)
	}
	return event, nil
}

func validateCreateRequest(req CreateContextRequest) (CreateContextRequest, error) {
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.RuntimeType = strings.TrimSpace(req.RuntimeType)
	req.RuntimeID = strings.TrimSpace(req.RuntimeID)
	req.Reason = strings.TrimSpace(req.Reason)
	req.UID = strings.TrimSpace(req.UID)
	req.OrgID = strings.TrimSpace(req.OrgID)
	req.GithubConnectionID = strings.TrimSpace(req.GithubConnectionID)

	for _, required := range []struct {
		field string
		value string
	}{
		{field: "request_id", value: req.RequestID},
		{field: "task_id", value: req.TaskID},
		{field: "agent_id", value: req.AgentID},
	} {
		if required.value == "" {
			return CreateContextRequest{}, &ValidationError{Field: required.field}
		}
	}
	if err := validateIdentities(req.UID, req.OrgID, req.GithubConnectionID); err != nil {
		return CreateContextRequest{}, err
	}
	if req.TTLSeconds < 1 || req.TTLSeconds > maxTTLSeconds {
		return CreateContextRequest{}, &ValidationError{Field: "ttl_seconds"}
	}
	return req, nil
}

func validateExtendRequest(req ExtendContextRequest) (ExtendContextRequest, error) {
	req.ContextToken = strings.TrimSpace(req.ContextToken)
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.RuntimeType = strings.TrimSpace(req.RuntimeType)
	req.RuntimeID = strings.TrimSpace(req.RuntimeID)
	req.Reason = strings.TrimSpace(req.Reason)
	req.GithubConnectionID = strings.TrimSpace(req.GithubConnectionID)
	for _, required := range []struct {
		field string
		value string
	}{
		{field: "context_token", value: req.ContextToken},
		{field: "task_id", value: req.TaskID},
		{field: "agent_id", value: req.AgentID},
		{field: "github_connection_id", value: req.GithubConnectionID},
	} {
		if required.value == "" {
			return ExtendContextRequest{}, &ValidationError{Field: required.field}
		}
	}
	return req, nil
}

func validateIdentities(uid, orgID, githubConnectionID string) error {
	hasDWS := uid != "" || orgID != ""
	hasGithub := githubConnectionID != ""
	if !hasDWS && !hasGithub {
		return &ValidationError{Field: "identities"}
	}
	if hasDWS {
		if !isDecimalIdentifier(uid) {
			return &ValidationError{Field: "uid"}
		}
		if !isDecimalIdentifier(orgID) {
			return &ValidationError{Field: "org_id"}
		}
	}
	return nil
}

func identitiesForRequest(uid, orgID, githubConnectionID string) []identityPayload {
	identities := make([]identityPayload, 0, 2)
	if uid != "" || orgID != "" {
		identities = append(identities, identityPayload{
			Credentials: []string{"DWS_AUTH_CODE"},
			Attributes: map[string]string{
				"uid":   uid,
				"orgId": orgID,
			},
			Type:  "DWS_UID",
			Class: identityClass,
			Key:   "dws",
		})
	}
	if githubConnectionID != "" {
		identities = append(identities, identityPayload{
			Credentials: []string{"GITHUB_ACCESS_TOKEN"},
			Attributes: map[string]string{
				"connectionId": githubConnectionID,
			},
			Type:  "GITHUB_USER",
			Class: identityClass,
			Key:   "github",
		})
	}
	return identities
}

func runtimeContextForRequest(taskID, agentID, runtimeType, runtimeID, reason string, source map[string]string) runtimeContextPayload {
	return runtimeContextPayload{
		Reason:      reason,
		AgentID:     agentID,
		Source:      cloneStringMap(source),
		RuntimeType: runtimeType,
		Class:       contextClass,
		TaskID:      taskID,
		RuntimeID:   runtimeID,
	}
}

func isDecimalIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func safeErrorCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "UNKNOWN"
	}
	for _, r := range code {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return "UNKNOWN"
		}
	}
	return code
}

type createContextPayload struct {
	Identities []identityPayload     `json:"identities"`
	RequestID  string                `json:"requestId"`
	Context    runtimeContextPayload `json:"context"`
	Class      string                `json:"class"`
	TTLSeconds int                   `json:"ttlSeconds"`
}

type extendContextPayload struct {
	ContextToken string                `json:"contextToken"`
	Identities   []identityPayload     `json:"identities"`
	Context      runtimeContextPayload `json:"context,omitempty"`
	Class        string                `json:"class"`
}

type identityPayload struct {
	Credentials []string          `json:"credentials"`
	Attributes  map[string]string `json:"attributes"`
	Type        string            `json:"type"`
	Class       string            `json:"class"`
	Key         string            `json:"key"`
}

type runtimeContextPayload struct {
	Reason      string            `json:"reason,omitempty"`
	AgentID     string            `json:"agentId"`
	Source      map[string]string `json:"source,omitempty"`
	RuntimeType string            `json:"runtimeType,omitempty"`
	Class       string            `json:"class"`
	TaskID      string            `json:"taskId"`
	RuntimeID   string            `json:"runtimeId,omitempty"`
}

type createContextResponse struct {
	Success      bool   `json:"success"`
	ContextID    string `json:"contextId"`
	ContextToken string `json:"contextToken"`
	ExpiresAt    int64  `json:"expiresAt"`
	ErrorCode    string `json:"errorCode"`
}

type extendContextResponse struct {
	Success   bool   `json:"success"`
	ContextID string `json:"contextId"`
	ExpiresAt int64  `json:"expiresAt"`
	ErrorCode string `json:"errorCode"`
}
