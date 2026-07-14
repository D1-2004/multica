package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type fakeDingTalkAccountBindingService struct {
	callbackCalls  int
	callbackResult agentmessagerouter.PublicDingTalkAccountBinding
	unbindResult   agentmessagerouter.PublicDingTalkAccountBinding
}

func (f *fakeDingTalkAccountBindingService) Begin(context.Context, agentmessagerouter.BeginParams) (agentmessagerouter.BeginResult, error) {
	return agentmessagerouter.BeginResult{}, nil
}

func (f *fakeDingTalkAccountBindingService) List(context.Context, pgtype.UUID) ([]agentmessagerouter.PublicDingTalkAccountBinding, error) {
	return nil, nil
}

func (f *fakeDingTalkAccountBindingService) CompleteCallback(context.Context, agentmessagerouter.CallbackParams) (agentmessagerouter.PublicDingTalkAccountBinding, error) {
	f.callbackCalls++
	return f.callbackResult, nil
}

func (f *fakeDingTalkAccountBindingService) Unbind(context.Context, agentmessagerouter.UnbindParams) (agentmessagerouter.PublicDingTalkAccountBinding, error) {
	return f.unbindResult, nil
}

func TestListDingTalkAccountBindingsReportsUnconfiguredWithoutSecrets(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/workspace-1/dingtalk/account-bindings", nil)
	w := httptest.NewRecorder()
	h.ListDingTalkAccountBindings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "{\"bindings\":[],\"configured\":false}\n" {
		t.Fatalf("body = %s", got)
	}
}

func TestDingTalkAccountCallbackRequiresExactOriginAndBearer(t *testing.T) {
	service := &fakeDingTalkAccountBindingService{}
	h := &Handler{
		DingTalkAccountBindings:     service,
		DingTalkAccountBindingOrigin: "https://dbase.example.internal",
	}
	body := `{"source_id":"source-1","account_display_name":"Zhang San"}`
	tests := []struct {
		name          string
		origin        string
		authorization string
		wantStatus    int
		wantCode      string
	}{
		{name: "missing origin", authorization: "Bearer callback-token", wantStatus: http.StatusForbidden, wantCode: "callback_origin_forbidden"},
		{name: "wrong origin", origin: "https://evil.example", authorization: "Bearer callback-token", wantStatus: http.StatusForbidden, wantCode: "callback_origin_forbidden"},
		{name: "missing authorization", origin: "https://dbase.example.internal", wantStatus: http.StatusUnauthorized, wantCode: "callback_auth_required"},
		{name: "malformed authorization", origin: "https://dbase.example.internal", authorization: "callback-token", wantStatus: http.StatusUnauthorized, wantCode: "callback_auth_required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/integrations/dingtalk/account-bindings/11111111-1111-1111-1111-111111111111/callback", strings.NewReader(body))
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			req = withURLParams(req, "installationId", "11111111-1111-1111-1111-111111111111")
			w := httptest.NewRecorder()
			h.CompleteDingTalkAccountBindingCallback(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d want %d body=%s", w.Code, tt.wantStatus, w.Body.String())
			}
			assertDingTalkAccountBindingErrorCode(t, w, tt.wantCode)
		})
	}
	if service.callbackCalls != 0 {
		t.Fatalf("callback service calls = %d", service.callbackCalls)
	}
}

func TestDingTalkAccountCallbackRejectsOversizedBody(t *testing.T) {
	service := &fakeDingTalkAccountBindingService{}
	h := &Handler{
		DingTalkAccountBindings:     service,
		DingTalkAccountBindingOrigin: "https://dbase.example.internal",
	}
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/integrations/dingtalk/account-bindings/11111111-1111-1111-1111-111111111111/callback",
		strings.NewReader(`{"source_id":"source-1","account_display_name":"`+strings.Repeat("x", maxDingTalkAccountCallbackBodyBytes)+`"}`),
	)
	req.Header.Set("Origin", "https://dbase.example.internal")
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
	req = withURLParams(req, "installationId", "11111111-1111-1111-1111-111111111111")
	w := httptest.NewRecorder()
	h.CompleteDingTalkAccountBindingCallback(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	assertDingTalkAccountBindingErrorCode(t, w, "invalid_binding_result")
	if service.callbackCalls != 0 {
		t.Fatalf("callback service calls = %d", service.callbackCalls)
	}
}

func TestDingTalkAccountCallbackPublishesActivatedEvent(t *testing.T) {
	service := &fakeDingTalkAccountBindingService{
		callbackResult: agentmessagerouter.PublicDingTalkAccountBinding{
			ID:          "11111111-1111-1111-1111-111111111111",
			WorkspaceID: "22222222-2222-2222-2222-222222222222",
			AgentID:     "33333333-3333-3333-3333-333333333333",
			Status:      "active",
		},
	}
	bus := events.New()
	var published events.Event
	bus.Subscribe(protocol.EventDingTalkAccountBindingActivated, func(event events.Event) {
		published = event
	})
	h := &Handler{
		DingTalkAccountBindings:     service,
		DingTalkAccountBindingOrigin: "https://dbase.example.internal",
		Bus:                         bus,
	}
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/integrations/dingtalk/account-bindings/11111111-1111-1111-1111-111111111111/callback",
		strings.NewReader(`{"source_id":"source-1","account_display_name":"Zhang San"}`),
	)
	req.Header.Set("Origin", "https://dbase.example.internal")
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
	req = withURLParams(req, "installationId", "11111111-1111-1111-1111-111111111111")
	w := httptest.NewRecorder()

	h.CompleteDingTalkAccountBindingCallback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if published.Type != protocol.EventDingTalkAccountBindingActivated ||
		published.WorkspaceID != service.callbackResult.WorkspaceID ||
		published.ActorType != "system" {
		t.Fatalf("published event = %#v", published)
	}
}

func TestUnbindDingTalkAccountPublishesRevokedEvent(t *testing.T) {
	service := &fakeDingTalkAccountBindingService{
		unbindResult: agentmessagerouter.PublicDingTalkAccountBinding{
			ID:          "11111111-1111-1111-1111-111111111111",
			WorkspaceID: "22222222-2222-2222-2222-222222222222",
			AgentID:     "33333333-3333-3333-3333-333333333333",
			Status:      "revoked",
		},
	}
	bus := events.New()
	var published events.Event
	bus.Subscribe(protocol.EventDingTalkAccountBindingRevoked, func(event events.Event) {
		published = event
	})
	h := &Handler{DingTalkAccountBindings: service, Bus: bus}
	req := httptest.NewRequest(
		http.MethodDelete,
		"/api/workspaces/22222222-2222-2222-2222-222222222222/dingtalk/account-bindings/11111111-1111-1111-1111-111111111111",
		nil,
	)
	req.Header.Set("X-User-ID", "44444444-4444-4444-4444-444444444444")
	req = withURLParams(
		req,
		"id", "22222222-2222-2222-2222-222222222222",
		"installationId", "11111111-1111-1111-1111-111111111111",
	)
	w := httptest.NewRecorder()

	h.UnbindDingTalkAccountBinding(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if published.Type != protocol.EventDingTalkAccountBindingRevoked ||
		published.WorkspaceID != service.unbindResult.WorkspaceID ||
		published.ActorType != "user" ||
		published.ActorID != "44444444-4444-4444-4444-444444444444" {
		t.Fatalf("published event = %#v", published)
	}
}

func TestDingTalkAccountBindingServiceErrorsHaveStableCodes(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{err: agentmessagerouter.ErrInvalidResult, wantStatus: http.StatusBadRequest, wantCode: "invalid_binding_result"},
		{err: agentmessagerouter.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "binding_attempt_not_found"},
		{err: agentmessagerouter.ErrBindingConflict, wantStatus: http.StatusConflict, wantCode: "binding_result_conflict"},
		{err: agentmessagerouter.ErrCallbackExpired, wantStatus: http.StatusGone, wantCode: "binding_callback_expired"},
		{err: agentmessagerouter.ErrRouterUnavailable, wantStatus: http.StatusBadGateway, wantCode: "subscription_verify_failed"},
		{err: errors.New("unexpected"), wantStatus: http.StatusInternalServerError, wantCode: "binding_internal_error"},
	}
	for _, tt := range tests {
		t.Run(tt.wantCode, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeDingTalkAccountBindingError(w, tt.err)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d want %d body=%s", w.Code, tt.wantStatus, w.Body.String())
			}
			assertDingTalkAccountBindingErrorCode(t, w, tt.wantCode)
		})
	}
}

func TestNormalizeDingTalkAccountBindingOrigin(t *testing.T) {
	tests := map[string]string{
		"https://DBASE.Example.Internal:443/": "https://dbase.example.internal",
		"https://dbase.example.internal:8443": "https://dbase.example.internal:8443",
	}
	for input, want := range tests {
		got, err := NormalizeDingTalkAccountBindingOrigin(input)
		if err != nil {
			t.Fatalf("NormalizeDingTalkAccountBindingOrigin(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeDingTalkAccountBindingOrigin(%q) = %q want %q", input, got, want)
		}
	}
	for _, input := range []string{"http://dbase.example.internal", "https://user@dbase.example.internal", "https://dbase.example.internal/path"} {
		if _, err := NormalizeDingTalkAccountBindingOrigin(input); err == nil {
			t.Fatalf("expected invalid DBase origin %q", input)
		}
	}
}

func assertDingTalkAccountBindingErrorCode(t *testing.T, recorder *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v body=%s", err, recorder.Body.String())
	}
	if body.Code != want {
		t.Fatalf("code = %q want %q body=%s", body.Code, want, recorder.Body.String())
	}
}
