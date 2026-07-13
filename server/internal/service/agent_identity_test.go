package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPAgentIdentityRedeemerRedeemsDWSAuthCode(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != agentIdentityRedeemPath {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer context-token" {
			t.Fatalf("authorization = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["identityKey"] != "dws" || body["credentialType"] != "DWS_AUTH_CODE" {
			t.Fatalf("request body = %#v", body)
		}
		writeJSONResponse(t, w, http.StatusOK, map[string]any{
			"ok":         true,
			"identity":   map[string]any{"key": "dws", "uid": "user-123"},
			"credential": map[string]any{"type": "DWS_AUTH_CODE", "authCode": "auth-code-secret"},
		})
	}))
	defer srv.Close()

	credential, err := NewHTTPAgentIdentityRedeemer(srv.URL, time.Second).RedeemDWSAuthCode(context.Background(), "context-token")
	if err != nil {
		t.Fatalf("RedeemDWSAuthCode: %v", err)
	}
	if credential.UID != "user-123" || credential.AuthCode != "auth-code-secret" {
		t.Fatalf("credential = %#v", credential)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestHTTPAgentIdentityRedeemerDoesNotRetryOrLeakSecrets(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSONResponse(t, w, http.StatusForbidden, map[string]any{
			"ok":           false,
			"errorCode":    "IDENTITY_MISMATCH",
			"errorMessage": "context-token-secret auth-code-secret",
		})
	}))
	defer srv.Close()

	_, err := NewHTTPAgentIdentityRedeemer(srv.URL, time.Second).RedeemDWSAuthCode(context.Background(), "context-token-secret")
	if err == nil {
		t.Fatal("RedeemDWSAuthCode must fail")
	}
	var redeemErr *AgentIdentityRedeemError
	if !errors.As(err, &redeemErr) || redeemErr.StatusCode != http.StatusForbidden || redeemErr.Code != "IDENTITY_MISMATCH" {
		t.Fatalf("error = %#v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want exactly one request", calls)
	}
	if strings.Contains(err.Error(), "context-token-secret") || strings.Contains(err.Error(), "auth-code-secret") {
		t.Fatalf("error leaked a secret: %v", err)
	}
}

func TestHTTPAgentIdentityRedeemerRejectsIdentityMismatchWithoutLeakingAuthCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, map[string]any{
			"ok":         true,
			"identity":   map[string]any{"key": "other", "uid": "user-123"},
			"credential": map[string]any{"type": "DWS_AUTH_CODE", "authCode": "auth-code-secret"},
		})
	}))
	defer srv.Close()

	_, err := NewHTTPAgentIdentityRedeemer(srv.URL, time.Second).RedeemDWSAuthCode(context.Background(), "context-token-secret")
	if err == nil || !strings.Contains(err.Error(), "unexpected identity key") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "context-token-secret") || strings.Contains(err.Error(), "auth-code-secret") {
		t.Fatalf("error leaked a secret: %v", err)
	}
}

func writeJSONResponse(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
