package a2aintegration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

type testPort struct {
	getTaskCalls int
	principal    Principal
}

type keepAlivePort struct {
	testPort
	release <-chan struct{}
}

type completedTaskPort struct {
	testPort
}

type panickingPort struct {
	testPort
}

func completedTask(taskID a2a.TaskID) *a2a.Task {
	return &a2a.Task{
		ID:        taskID,
		ContextID: "ctx_completed",
		Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted},
		Artifacts: []*a2a.Artifact{{
			ID:    "art_completed",
			Name:  "result",
			Parts: a2a.ContentParts{a2a.NewTextPart("completed output")},
		}},
	}
}

func (port *completedTaskPort) GetTask(_ context.Context, request *a2a.GetTaskRequest) (*a2a.Task, error) {
	return completedTask(request.ID), nil
}

func (port *completedTaskPort) SendStreamingMessage(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		task := completedTask("task_completed")
		if !yield(task, nil) {
			return
		}
		yield(&a2a.TaskArtifactUpdateEvent{
			TaskID:    task.ID,
			ContextID: task.ContextID,
			Artifact:  task.Artifacts[0],
			LastChunk: true,
		}, nil)
	}
}

func (port *completedTaskPort) SubscribeToTask(context.Context, *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(completedTask("task_completed"), nil)
	}
}

func (port *panickingPort) GetTask(context.Context, *a2a.GetTaskRequest) (*a2a.Task, error) {
	panic("completed task projection")
}

func (port *panickingPort) SendStreamingMessage(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return func(func(a2a.Event, error) bool) {
		panic("completed streaming projection")
	}
}

func (port *panickingPort) SubscribeToTask(context.Context, *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return func(func(a2a.Event, error) bool) {
		panic("completed subscription projection")
	}
}

func (port *keepAlivePort) SendStreamingMessage(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		select {
		case <-ctx.Done():
		case <-port.release:
		}
	}
}

func (port *keepAlivePort) SubscribeToTask(context.Context, *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {}
}

func (port *testPort) SendMessage(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	return nil, errors.New("unexpected SendMessage call")
}

func (port *testPort) GetTask(ctx context.Context, request *a2a.GetTaskRequest) (*a2a.Task, error) {
	port.getTaskCalls++
	port.principal, _ = PrincipalFromContext(ctx)
	return &a2a.Task{
		ID:        request.ID,
		ContextID: "ctx_1",
		Status:    a2a.TaskStatus{State: a2a.TaskStateWorking},
	}, nil
}

func TestJSONRPCVersionGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		version    string
		wantCode   int
		wantCalled bool
	}{
		{name: "missing defaults to v0.3", wantCode: -32009},
		{name: "query parameter does not select a version", path: "/api/a2a/agents/agent_1/v1?A2A-Version=1.0", wantCode: -32009},
		{name: "unsupported version", version: "0.3", wantCode: -32009},
		{name: "version whitespace is rejected", version: " 1.0 ", wantCode: -32009},
		{name: "v1", version: "1.0", wantCalled: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			port := &testPort{}
			handler := NewJSONRPCHandler(port)
			path := test.path
			if path == "" {
				path = "/api/a2a/agents/agent_1/v1"
			}
			request := httptest.NewRequest(
				http.MethodPost,
				path,
				strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"task_1"}}`),
			)
			if test.version != "" {
				request.Header.Set(a2a.SvcParamVersion, test.version)
			}
			request = request.WithContext(WithPrincipal(request.Context(), Principal{
				EndpointID:      "endpoint_1",
				ClientID:        "client_1",
				Scopes:          []string{"read"},
				EndpointEnabled: true,
			}))

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			var payload struct {
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("json.Unmarshal(response) error = %v; body = %s", err, response.Body.String())
			}

			if test.wantCode != 0 {
				if payload.Error == nil || payload.Error.Code != test.wantCode {
					t.Fatalf("JSON-RPC error = %+v, want code %d; body = %s", payload.Error, test.wantCode, response.Body.String())
				}
			} else if payload.Error != nil {
				t.Fatalf("JSON-RPC error = %+v, want success; body = %s", payload.Error, response.Body.String())
			}

			if got := port.getTaskCalls > 0; got != test.wantCalled {
				t.Fatalf("Port.GetTask called = %v, want %v", got, test.wantCalled)
			}
			if test.wantCalled && port.principal.ClientID != "client_1" {
				t.Fatalf("Port principal = %+v, want injected client", port.principal)
			}
		})
	}
}

func TestJSONRPCVersionGuardRejectsDuplicateVersionHeaders(t *testing.T) {
	t.Parallel()

	port := &testPort{}
	handler := NewJSONRPCHandler(port)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/a2a/agents/agent_1/v1",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"task_1"}}`),
	)
	request.Header.Add(a2a.SvcParamVersion, "1.0")
	request.Header.Add(a2a.SvcParamVersion, "1.0")
	request = request.WithContext(WithPrincipal(request.Context(), Principal{
		EndpointID: "endpoint_1", ClientID: "client_1", Scopes: []string{"read"}, EndpointEnabled: true,
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var payload struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error == nil || payload.Error.Code != -32009 || port.getTaskCalls != 0 {
		t.Fatalf("duplicate version response = %s, calls=%d", response.Body.String(), port.getTaskCalls)
	}
}

func TestJSONRPCProtocolErrorsRemainSDKNative(t *testing.T) {
	t.Parallel()

	handler := NewJSONRPCHandler(&testPort{})
	tests := []struct {
		name     string
		body     string
		wantCode int
		wantID   string
	}{
		{name: "malformed JSON", body: `{`, wantCode: -32700, wantID: "null"},
		{name: "unknown method", body: `{"jsonrpc":"2.0","id":"request-7","method":"FutureMutation","params":{}}`, wantCode: -32601, wantID: `"request-7"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, "/v1", strings.NewReader(test.body))
			request.Header.Set(a2a.SvcParamVersion, string(a2a.Version))
			request = request.WithContext(WithPrincipal(request.Context(), Principal{
				Scopes:          []string{"send", "read", "list", "cancel"},
				EndpointEnabled: true,
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			var payload struct {
				ID    json.RawMessage `json:"id"`
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
			}
			if payload.Error == nil || payload.Error.Code != test.wantCode {
				t.Fatalf("error = %+v, want code %d; body=%s", payload.Error, test.wantCode, response.Body.String())
			}
			if string(payload.ID) != test.wantID {
				t.Fatalf("id = %s, want %s", payload.ID, test.wantID)
			}
		})
	}
}

func TestJSONRPCStreamingSendsKeepAlive(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	handler := newJSONRPCHandler(&keepAlivePort{release: release}, 10*time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithPrincipal(r.Context(), Principal{
			Scopes:          []string{"send"},
			EndpointEnabled: true,
		})
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer server.Close()

	requestCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		server.URL,
		strings.NewReader(`{"jsonrpc":"2.0","id":"keep-alive","method":"SendStreamingMessage","params":{"message":{"messageId":"message-1","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(a2a.SvcParamVersion, string(a2a.Version))

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open streaming response: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("streaming status = %d, want 200", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("streaming content type = %q, want text/event-stream", contentType)
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read keep-alive: %v", err)
	}
	if line != ": keep-alive\n" {
		t.Fatalf("keep-alive line = %q", line)
	}
	close(release)
}

func TestJSONRPCSerializesCompletedTaskAndTextArtifact(t *testing.T) {
	t.Parallel()

	handler := NewJSONRPCHandler(&completedTaskPort{})
	tests := []struct {
		name string
		body string
	}{
		{
			name: "GetTask",
			body: `{"jsonrpc":"2.0","id":"get-completed","method":"GetTask","params":{"id":"task_completed"}}`,
		},
		{
			name: "SendStreamingMessage",
			body: `{"jsonrpc":"2.0","id":"stream-completed","method":"SendStreamingMessage","params":{"message":{"messageId":"message-completed","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1", strings.NewReader(test.body))
			request.Header.Set(a2a.SvcParamVersion, string(a2a.Version))
			request = request.WithContext(WithPrincipal(request.Context(), Principal{
				Scopes:          []string{"send", "read"},
				EndpointEnabled: true,
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			body := response.Body.String()
			if !strings.Contains(body, `"text":"completed output"`) {
				t.Fatalf("response is missing text artifact: %s", body)
			}
		})
	}
}

func TestJSONRPCConvertsTransportPanicsToProtocolErrors(t *testing.T) {
	t.Parallel()

	handler := NewJSONRPCHandler(&panickingPort{})
	tests := []struct {
		name        string
		body        string
		privateText string
	}{
		{
			name:        "GetTask",
			body:        `{"jsonrpc":"2.0","id":"panic-get","method":"GetTask","params":{"id":"task_completed"}}`,
			privateText: "completed task projection",
		},
		{
			name:        "SendStreamingMessage",
			body:        `{"jsonrpc":"2.0","id":"panic-stream","method":"SendStreamingMessage","params":{"message":{"messageId":"message-panic","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`,
			privateText: "completed streaming projection",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1", strings.NewReader(test.body))
			request.Header.Set(a2a.SvcParamVersion, string(a2a.Version))
			request = request.WithContext(WithPrincipal(request.Context(), Principal{
				Scopes:          []string{"send", "read"},
				EndpointEnabled: true,
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if body := response.Body.String(); !strings.Contains(body, `"code":-32603`) ||
				!strings.Contains(body, "A2A transport internal error") || strings.Contains(body, test.privateText) {
				t.Fatalf("panic response is not a JSON-RPC internal error: %s", body)
			}
		})
	}
}

func TestJSONRPCScopeAuthorizationRunsAtSDKMethodBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		body            string
		scopes          []string
		endpointEnabled bool
	}{
		{
			name:            "read call without read scope",
			body:            `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"task_1"}}`,
			scopes:          []string{"send"},
			endpointEnabled: true,
		},
		{
			name:            "disabled endpoint cannot send",
			body:            `{"jsonrpc":"2.0","id":2,"method":"SendMessage","params":{"message":{"messageId":"message_1","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`,
			scopes:          []string{"send"},
			endpointEnabled: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			port := &testPort{}
			handler := NewJSONRPCHandler(port)
			request := httptest.NewRequest(http.MethodPost, "/v1", strings.NewReader(test.body))
			request.Header.Set(a2a.SvcParamVersion, string(a2a.Version))
			request = request.WithContext(WithPrincipal(request.Context(), Principal{
				Scopes:          test.scopes,
				EndpointEnabled: test.endpointEnabled,
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			var payload struct {
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
			}
			if payload.Error == nil || payload.Error.Code != -31403 {
				t.Fatalf("error = %+v, want unauthorized; body=%s", payload.Error, response.Body.String())
			}
			if port.getTaskCalls != 0 {
				t.Fatalf("unauthorized request reached application port")
			}
		})
	}
}

func TestMethodScopeFailsClosedForUnknownSDKMethod(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"SendMessage":          "send",
		"SendStreamingMessage": "send",
		"GetTask":              "read",
		"SubscribeToTask":      "read",
		"ListTasks":            "list",
		"CancelTask":           "cancel",
		"GetTaskPushConfig":    "read",
		"GetExtendedAgentCard": "read",
		"CreateTaskPushConfig": "send",
		"DeleteTaskPushConfig": "send",
		"ListTaskPushConfigs":  "list",
		"FutureMutation":       "",
	}
	for method, want := range tests {
		if got := methodScope(method); got != want {
			t.Errorf("methodScope(%q) = %q, want %q", method, got, want)
		}
	}
}

func TestRequestHandlerUnsupportedOperations(t *testing.T) {
	t.Parallel()

	handler := &requestHandler{port: &testPort{}}
	ctx := context.Background()

	if _, err := handler.ListTasks(ctx, &a2a.ListTasksRequest{}); !errors.Is(err, a2a.ErrUnsupportedOperation) {
		t.Fatalf("ListTasks() error = %v, want ErrUnsupportedOperation", err)
	}
	if _, err := handler.CancelTask(ctx, &a2a.CancelTaskRequest{}); !errors.Is(err, a2a.ErrUnsupportedOperation) {
		t.Fatalf("CancelTask() error = %v, want ErrUnsupportedOperation", err)
	}
	if _, err := handler.GetTaskPushConfig(ctx, &a2a.GetTaskPushConfigRequest{}); !errors.Is(err, a2a.ErrPushNotificationNotSupported) {
		t.Fatalf("GetTaskPushConfig() error = %v, want ErrPushNotificationNotSupported", err)
	}
	if _, err := handler.GetExtendedAgentCard(ctx, &a2a.GetExtendedAgentCardRequest{}); !errors.Is(err, a2a.ErrUnsupportedOperation) {
		t.Fatalf("GetExtendedAgentCard() error = %v, want ErrUnsupportedOperation", err)
	}

	for _, events := range []func() error{
		func() error {
			for _, err := range handler.SendStreamingMessage(ctx, &a2a.SendMessageRequest{}) {
				return err
			}
			return nil
		},
		func() error {
			for _, err := range handler.SubscribeToTask(ctx, &a2a.SubscribeToTaskRequest{}) {
				return err
			}
			return nil
		},
	} {
		if err := events(); !errors.Is(err, a2a.ErrUnsupportedOperation) {
			t.Fatalf("streaming operation error = %v, want ErrUnsupportedOperation", err)
		}
	}
}
