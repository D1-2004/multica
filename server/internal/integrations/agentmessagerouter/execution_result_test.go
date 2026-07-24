package agentmessagerouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSubmitExecutionResultUsesTrustedBaseAndCredential(t *testing.T) {
	var body ExecutionResultRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/dispatch-tasks/router-task-1/execution-result" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer service-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]string{
				"dispatchTaskId":    "router-task-1",
				"executionStatus":   body.ExecutionStatus,
				"executionReportId": "report-1",
			},
		})
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	request := ExecutionResultRequest{
		RequestID:          "multica-terminal:root-task",
		AgentID:            "agent-1",
		ExternalTaskID:     "root-task",
		ExternalRunID:      "leaf-task",
		ExternalSessionID:  "session-1",
		ExecutionStatus:    "completed",
		ResultMessage:      "最后回复",
		ExecutionResult:    map[string]any{"failureReason": ""},
	}
	if err := client.SubmitExecutionResult(context.Background(), "/api/v1/dispatch-tasks/router-task-1/execution-result", request); err != nil {
		t.Fatal(err)
	}
	if body.RequestID != request.RequestID || body.ResultMessage != request.ResultMessage ||
		body.ExternalTaskID != request.ExternalTaskID {
		t.Fatalf("body = %#v", body)
	}
}

func TestClientSubmitExecutionResultRejectsInvalidSuccessEnvelopeAsRetryable(t *testing.T) {
	for _, responseBody := range []string{
		"",
		`not-json`,
		`{"success":true,"code":"success","data":null}`,
		`{"success":true,"code":"success","data":{"dispatchTaskId":"other","executionStatus":"completed","executionReportId":"report-1"}}`,
		`{"success":true,"code":"success","data":{"dispatchTaskId":"task","executionStatus":"failed","executionReportId":"report-1"}}`,
		`{"success":true,"code":"success","data":{"dispatchTaskId":"task","executionStatus":"completed","executionReportId":""}}`,
	} {
		t.Run(responseBody, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(responseBody))
			}))
			defer server.Close()
			client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
			if err != nil {
				t.Fatal(err)
			}
			err = client.SubmitExecutionResult(
				context.Background(),
				"/api/v1/dispatch-tasks/task/execution-result",
				ExecutionResultRequest{ExecutionStatus: "completed"},
			)
			var deliveryErr *ExecutionResultDeliveryError
			if !errors.As(err, &deliveryErr) || !deliveryErr.Retryable() {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestClientSubmitExecutionResultDoesNotFollowExternalRedirect(t *testing.T) {
	externalRequests := 0
	external := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		externalRequests++
	}))
	defer external.Close()

	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", external.URL+"/leak")
				w.WriteHeader(status)
			}))
			defer router.Close()
			client, err := NewClient(ClientConfig{
				BaseURL:           router.URL,
				ServiceCredential: "service-secret",
				HTTPClient:        &http.Client{},
			})
			if err != nil {
				t.Fatal(err)
			}
			err = client.SubmitExecutionResult(
				context.Background(),
				"/api/v1/dispatch-tasks/task/execution-result",
				ExecutionResultRequest{ExecutionStatus: "completed", ResultMessage: "private reply"},
			)
			var deliveryErr *ExecutionResultDeliveryError
			if !errors.As(err, &deliveryErr) || deliveryErr.Retryable() {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if externalRequests != 0 {
		t.Fatalf("external redirect received %d requests", externalRequests)
	}
}

func TestClientSubmitExecutionResultRejectsUntrustedCallbackPath(t *testing.T) {
	client, err := NewClient(ClientConfig{BaseURL: "https://router.internal", ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	for _, callback := range []string{
		"https://evil.example/api/v1/dispatch-tasks/task/execution-result",
		"/api/v1/dispatch-tasks/task/execution-result?token=secret",
		"/api/v1/dispatch-tasks/task/other",
	} {
		if err := client.SubmitExecutionResult(context.Background(), callback, ExecutionResultRequest{}); err == nil {
			t.Fatalf("callback %q accepted", callback)
		}
	}
}

func TestClientSubmitExecutionResultClassifiesRetryableStatus(t *testing.T) {
	for _, tt := range []struct {
		status    int
		retryable bool
	}{
		{status: http.StatusRequestTimeout, retryable: true},
		{status: http.StatusTooManyRequests, retryable: true},
		{status: http.StatusBadGateway, retryable: true},
		{status: http.StatusBadRequest, retryable: false},
	} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer server.Close()
			client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
			if err != nil {
				t.Fatal(err)
			}
			err = client.SubmitExecutionResult(
				context.Background(),
				"/api/v1/dispatch-tasks/task/execution-result",
				ExecutionResultRequest{},
			)
			var deliveryErr *ExecutionResultDeliveryError
			if !errors.As(err, &deliveryErr) {
				t.Fatalf("error = %v", err)
			}
			if deliveryErr.Retryable() != tt.retryable {
				t.Fatalf("Retryable() = %v", deliveryErr.Retryable())
			}
		})
	}
}

func TestClientSubmitExecutionResultClassifiesHTTP200BusinessError(t *testing.T) {
	for _, tt := range []struct {
		code      string
		retryable bool
	}{
		{code: "internal_error", retryable: true},
		{code: "bad_request", retryable: false},
	} {
		t.Run(tt.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":false,"code":"` + tt.code + `","message":"delivery rejected"}`))
			}))
			defer server.Close()
			client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
			if err != nil {
				t.Fatal(err)
			}
			err = client.SubmitExecutionResult(
				context.Background(),
				"/api/v1/dispatch-tasks/task/execution-result",
				ExecutionResultRequest{},
			)
			var deliveryErr *ExecutionResultDeliveryError
			if !errors.As(err, &deliveryErr) {
				t.Fatalf("error = %v", err)
			}
			if deliveryErr.Retryable() != tt.retryable {
				t.Fatalf("Retryable() = %v, want %v", deliveryErr.Retryable(), tt.retryable)
			}
		})
	}
}
