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
	methodName       = "createAgentIdentityContext"
	parameterType    = "com.dingtalk.ailab.agentidentity.client.request.CreateAgentIdentityContextRequest"

	requestClass  = "com.dingtalk.ailab.agentidentity.client.request.CreateAgentIdentityContextRequest"
	contextClass  = "com.dingtalk.ailab.agentidentity.client.model.AgentRuntimeContext"
	identityClass = "com.dingtalk.ailab.agentidentity.client.model.AgentIdentitySpec"
)

// CreateContextRequest identifies the DWS user represented by a short-lived
// Agent Identity context. The caller is responsible for keeping the returned
// token private and passing it only to the runtime that will redeem it.
type CreateContextRequest struct {
	RequestID   string
	TaskID      string
	AgentID     string
	RuntimeType string
	RuntimeID   string
	Reason      string
	Source      map[string]string
	UID         string
	OrgID       string
	TTLSeconds  int
}

type CreateContextResult struct {
	ContextID    string
	ContextToken string
	ExpiresAt    int64
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
	req, err := validateRequest(req)
	if err != nil {
		return CreateContextResult{}, err
	}

	payload := []createContextPayload{{
		RequestID: req.RequestID,
		Identities: []identityPayload{{
			Credentials: []string{"DWS_AUTH_CODE"},
			Attributes: map[string]string{
				"uid":   req.UID,
				"orgId": req.OrgID,
			},
			Type:  "DWS_UID",
			Class: identityClass,
			Key:   "dws",
		}},
		Context: runtimeContextPayload{
			Reason:      req.Reason,
			AgentID:     req.AgentID,
			Source:      cloneStringMap(req.Source),
			RuntimeType: req.RuntimeType,
			Class:       contextClass,
			TaskID:      req.TaskID,
			RuntimeID:   req.RuntimeID,
		},
		Class:      requestClass,
		TTLSeconds: req.TTLSeconds,
	}}
	data, err := json.Marshal(payload)
	if err != nil {
		return CreateContextResult{}, errors.New("encode Agent Identity HSF request")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	invoker, err := c.dial(timeoutCtx, c.address)
	if err != nil {
		return CreateContextResult{}, fmt.Errorf("connect to Dapr sidecar: %w", err)
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
		return CreateContextResult{}, fmt.Errorf("invoke Agent Identity HSF binding: %w", err)
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

func validateRequest(req CreateContextRequest) (CreateContextRequest, error) {
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.RuntimeType = strings.TrimSpace(req.RuntimeType)
	req.RuntimeID = strings.TrimSpace(req.RuntimeID)
	req.Reason = strings.TrimSpace(req.Reason)
	req.UID = strings.TrimSpace(req.UID)
	req.OrgID = strings.TrimSpace(req.OrgID)

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
	if !isDecimalIdentifier(req.UID) {
		return CreateContextRequest{}, &ValidationError{Field: "uid"}
	}
	if !isDecimalIdentifier(req.OrgID) {
		return CreateContextRequest{}, &ValidationError{Field: "org_id"}
	}
	if req.TTLSeconds < 1 || req.TTLSeconds > maxTTLSeconds {
		return CreateContextRequest{}, &ValidationError{Field: "ttl_seconds"}
	}
	return req, nil
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
