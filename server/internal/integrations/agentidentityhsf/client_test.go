package agentidentityhsf

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	dapr "github.com/dapr/go-sdk/client"
)

type fakeBindingInvoker struct {
	request *dapr.InvokeBindingRequest
	event   *dapr.BindingEvent
	err     error
	closed  bool
}

func (f *fakeBindingInvoker) InvokeBinding(_ context.Context, request *dapr.InvokeBindingRequest) (*dapr.BindingEvent, error) {
	f.request = request
	return f.event, f.err
}

func (f *fakeBindingInvoker) Close() {
	f.closed = true
}

func newTestClient(invoker *fakeBindingInvoker) *Client {
	return &Client{
		address:     "127.0.0.1:50001",
		bindingName: defaultBindingName,
		appName:     "dt-fde-multica",
		timeout:     defaultTimeout,
		dial: func(context.Context, string) (bindingInvoker, error) {
			return invoker, nil
		},
	}
}

func validCreateContextRequest() CreateContextRequest {
	return CreateContextRequest{
		RequestID:   "request-1",
		TaskID:      "task-1",
		AgentID:     "agent-1",
		RuntimeType: "AONE_SERVICE",
		RuntimeID:   "runtime-1",
		Reason:      "diagnostic",
		Source:      map[string]string{"app": "dt-fde-multica"},
		UID:         "24710833",
		OrgID:       "439446171",
		TTLSeconds:  900,
	}
}

func TestCreateContextInvokesAgentIdentityHSF(t *testing.T) {
	invoker := &fakeBindingInvoker{event: &dapr.BindingEvent{Data: []byte(`{
		"success":true,
		"contextId":"ctx_123",
		"contextToken":"secret-context-token",
		"expiresAt":1783665600000
	}`)}}
	client := newTestClient(invoker)

	result, err := client.CreateContext(context.Background(), validCreateContextRequest())
	if err != nil {
		t.Fatalf("CreateContext: %v", err)
	}
	if result.ContextID != "ctx_123" || result.ContextToken != "secret-context-token" || result.ExpiresAt != 1783665600000 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !invoker.closed {
		t.Fatal("Dapr client was not closed")
	}
	if invoker.request == nil {
		t.Fatal("Dapr binding was not invoked")
	}
	if invoker.request.Name != "hsf.consumer" || invoker.request.Operation != "invoke" {
		t.Fatalf("unexpected binding target: %s/%s", invoker.request.Name, invoker.request.Operation)
	}

	wantMetadata := map[string]string{
		"rpc-interface-name":         serviceInterface,
		"rpc-version":                "1.0.0",
		"rpc-group":                  "HSF",
		"rpc-method-name":            "createAgentIdentityContext",
		"rpc-method-parameter-types": parameterType,
		"serialization-type":         "application/json",
		"rpc-generic":                "true",
		"rpc-timeout":                "10000",
		"appName":                    "dt-fde-multica",
	}
	for key, want := range wantMetadata {
		if got := invoker.request.Metadata[key]; got != want {
			t.Errorf("metadata[%q] = %q, want %q", key, got, want)
		}
	}

	var payload []map[string]any
	if err := json.Unmarshal(invoker.request.Data, &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("payload length = %d, want 1", len(payload))
	}
	request := payload[0]
	if request["class"] != requestClass || request["requestId"] != "request-1" || request["ttlSeconds"] != float64(900) {
		t.Fatalf("unexpected request payload: %#v", request)
	}
	contextPayload, ok := request["context"].(map[string]any)
	if !ok || contextPayload["class"] != contextClass || contextPayload["taskId"] != "task-1" {
		t.Fatalf("unexpected runtime context: %#v", request["context"])
	}
	identities, ok := request["identities"].([]any)
	if !ok || len(identities) != 1 {
		t.Fatalf("unexpected identities: %#v", request["identities"])
	}
	identity, ok := identities[0].(map[string]any)
	if !ok || identity["class"] != identityClass || identity["key"] != "dws" || identity["type"] != "DWS_UID" {
		t.Fatalf("unexpected identity: %#v", identities[0])
	}
	attributes, ok := identity["attributes"].(map[string]any)
	if !ok || attributes["uid"] != "24710833" || attributes["orgId"] != "439446171" {
		t.Fatalf("unexpected identity attributes: %#v", identity["attributes"])
	}
	credentials, ok := identity["credentials"].([]any)
	if !ok || len(credentials) != 1 || credentials[0] != "DWS_AUTH_CODE" {
		t.Fatalf("unexpected credentials: %#v", identity["credentials"])
	}
}

func TestCreateContextReturnsSafeServiceError(t *testing.T) {
	invoker := &fakeBindingInvoker{event: &dapr.BindingEvent{Data: []byte(`{
		"success":false,
		"errorCode":"DWS_CLIENT_ID_NOT_CONFIGURED",
		"errorMessage":"do-not-expose-this-value"
	}`)}}
	client := newTestClient(invoker)

	_, err := client.CreateContext(context.Background(), validCreateContextRequest())
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) {
		t.Fatalf("error = %v, want ServiceError", err)
	}
	if serviceErr.Code != "DWS_CLIENT_ID_NOT_CONFIGURED" {
		t.Fatalf("error code = %q", serviceErr.Code)
	}
	if strings.Contains(err.Error(), "do-not-expose") {
		t.Fatalf("service message leaked through error: %v", err)
	}
}

func TestCreateContextRejectsInvalidRequestBeforeDial(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*CreateContextRequest)
		field string
	}{
		{name: "missing request id", alter: func(r *CreateContextRequest) { r.RequestID = "" }, field: "request_id"},
		{name: "non numeric uid", alter: func(r *CreateContextRequest) { r.UID = "uid-1" }, field: "uid"},
		{name: "non numeric org", alter: func(r *CreateContextRequest) { r.OrgID = "org-1" }, field: "org_id"},
		{name: "ttl too long", alter: func(r *CreateContextRequest) { r.TTLSeconds = 901 }, field: "ttl_seconds"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dialed := false
			client := &Client{
				timeout: defaultTimeout,
				dial: func(context.Context, string) (bindingInvoker, error) {
					dialed = true
					return nil, errors.New("unexpected dial")
				},
			}
			request := validCreateContextRequest()
			test.alter(&request)

			_, err := client.CreateContext(context.Background(), request)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || validationErr.Field != test.field {
				t.Fatalf("error = %#v, want ValidationError(%s)", err, test.field)
			}
			if dialed {
				t.Fatal("invalid request reached Dapr dialer")
			}
		})
	}
}

func TestCreateContextRejectsIncompleteSuccess(t *testing.T) {
	invoker := &fakeBindingInvoker{event: &dapr.BindingEvent{Data: []byte(`{
		"success":true,
		"contextId":"ctx_123",
		"contextToken":"",
		"expiresAt":1783665600000
	}`)}}
	client := newTestClient(invoker)

	_, err := client.CreateContext(context.Background(), validCreateContextRequest())
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("error = %v, want incomplete response", err)
	}
}

func TestDaprAddressFromEnv(t *testing.T) {
	t.Setenv("DAPR_GRPC_ENDPOINT", "dns:///daprd:50001")
	t.Setenv("DAPR_GRPC_PORT", "50002")
	if got := daprAddressFromEnv(); got != "dns:///daprd:50001" {
		t.Fatalf("endpoint address = %q", got)
	}

	t.Setenv("DAPR_GRPC_ENDPOINT", "")
	if got := daprAddressFromEnv(); got != "127.0.0.1:50002" {
		t.Fatalf("port address = %q", got)
	}
}

func TestCreateContextHonorsTimeout(t *testing.T) {
	invoker := &fakeBindingInvoker{err: context.DeadlineExceeded}
	client := newTestClient(invoker)
	client.timeout = time.Millisecond

	_, err := client.CreateContext(context.Background(), validCreateContextRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}
