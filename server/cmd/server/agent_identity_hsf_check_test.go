package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
)

type fakeAgentIdentityContextCreator struct {
	request agentidentityhsf.CreateContextRequest
	result  agentidentityhsf.CreateContextResult
	err     error
}

func (f *fakeAgentIdentityContextCreator) CreateContext(_ context.Context, request agentidentityhsf.CreateContextRequest) (agentidentityhsf.CreateContextResult, error) {
	f.request = request
	return f.result, f.err
}

func callAgentIdentityHSFCheck(t *testing.T, handler http.HandlerFunc, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/internal/agent-identity/hsf-check", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestAgentIdentityHSFCheckRequiresBearerToken(t *testing.T) {
	handler := agentIdentityHSFCheckHandler("operator-token", &fakeAgentIdentityContextCreator{})
	recorder := callAgentIdentityHSFCheck(t, handler, "", `{}`)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestAgentIdentityHSFCheckDiscardsContextToken(t *testing.T) {
	fake := &fakeAgentIdentityContextCreator{result: agentidentityhsf.CreateContextResult{
		ContextID:    "ctx_123",
		ContextToken: "context-token-must-not-leak",
		ExpiresAt:    1783665600000,
	}}
	handler := agentIdentityHSFCheckHandler("operator-token", fake)
	recorder := callAgentIdentityHSFCheck(t, handler, "operator-token", `{
		"uid":"24710833",
		"org_id":"439446171",
		"request_id":"diagnostic-request"
	}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "context-token-must-not-leak") || strings.Contains(recorder.Body.String(), "context_token\"") {
		t.Fatalf("context token leaked in response: %s", recorder.Body.String())
	}
	var response agentIdentityHSFCheckResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Success || !response.ContextTokenReceived || response.ContextID != "ctx_123" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if fake.request.UID != "24710833" || fake.request.OrgID != "439446171" || fake.request.TTLSeconds != 900 {
		t.Fatalf("unexpected HSF request: %#v", fake.request)
	}
	if fake.request.TaskID != "diagnostic-request" || fake.request.RuntimeType != "AONE_SERVICE" {
		t.Fatalf("unexpected runtime context: %#v", fake.request)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestAgentIdentityHSFCheckMapsValidationError(t *testing.T) {
	fake := &fakeAgentIdentityContextCreator{err: &agentidentityhsf.ValidationError{Field: "uid"}}
	handler := agentIdentityHSFCheckHandler("operator-token", fake)
	recorder := callAgentIdentityHSFCheck(t, handler, "operator-token", `{"uid":"bad","org_id":"439446171"}`)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"field":"uid"`) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAgentIdentityHSFCheckMapsServiceError(t *testing.T) {
	fake := &fakeAgentIdentityContextCreator{err: &agentidentityhsf.ServiceError{Code: "DWS_CLIENT_ID_NOT_CONFIGURED"}}
	handler := agentIdentityHSFCheckHandler("operator-token", fake)
	recorder := callAgentIdentityHSFCheck(t, handler, "operator-token", `{"uid":"24710833","org_id":"439446171"}`)

	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), `"error_code":"DWS_CLIENT_ID_NOT_CONFIGURED"`) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAgentIdentityHSFCheckHidesTransportError(t *testing.T) {
	fake := &fakeAgentIdentityContextCreator{err: errors.New("transport failed with context-token-must-not-leak")}
	handler := agentIdentityHSFCheckHandler("operator-token", fake)
	recorder := callAgentIdentityHSFCheck(t, handler, "operator-token", `{"uid":"24710833","org_id":"439446171"}`)

	if recorder.Code != http.StatusBadGateway || strings.Contains(recorder.Body.String(), "context-token-must-not-leak") {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
