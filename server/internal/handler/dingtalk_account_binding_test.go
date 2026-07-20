package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type fakeDingTalkAccountBindingService struct {
	completeCalls  int
	completeParams agentmessagerouter.CompleteBindingParams
	completeResult agentmessagerouter.CompleteBindingResult
	unbindResult   agentmessagerouter.PublicDingTalkAccountBinding
}

func (f *fakeDingTalkAccountBindingService) Begin(context.Context, agentmessagerouter.BeginParams) (agentmessagerouter.BeginResult, error) {
	return agentmessagerouter.BeginResult{}, nil
}

func (f *fakeDingTalkAccountBindingService) List(context.Context, pgtype.UUID) ([]agentmessagerouter.PublicDingTalkAccountBinding, error) {
	return nil, nil
}

func (f *fakeDingTalkAccountBindingService) CompleteBinding(_ context.Context, params agentmessagerouter.CompleteBindingParams) (agentmessagerouter.CompleteBindingResult, error) {
	f.completeCalls++
	f.completeParams = params
	return f.completeResult, nil
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
		DingTalkAccountBindings:      service,
		DingTalkAccountBindingOrigin: "https://dbase.example.internal",
	}
	body := validDingTalkBindingCallbackBody("message", "success", "source-1")
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
			req = withURLParams(req, "bindingId", "11111111-1111-1111-1111-111111111111")
			w := httptest.NewRecorder()
			h.CompleteDingTalkAccountBindingCallback(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d want %d body=%s", w.Code, tt.wantStatus, w.Body.String())
			}
			assertDingTalkAccountBindingErrorCode(t, w, tt.wantCode)
		})
	}
	if service.completeCalls != 0 {
		t.Fatalf("callback service calls = %d", service.completeCalls)
	}
}

func TestDingTalkAccountCallbackRejectsOversizedBody(t *testing.T) {
	service := &fakeDingTalkAccountBindingService{}
	h := &Handler{
		DingTalkAccountBindings:      service,
		DingTalkAccountBindingOrigin: "https://dbase.example.internal",
	}
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/integrations/dingtalk/account-bindings/11111111-1111-1111-1111-111111111111/callback",
		strings.NewReader(`{"binding_mode":"message","status":"completed","identity_binding":{"status":"failed","error":{"code":"identity_lookup_failed","message":"`+strings.Repeat("x", maxDingTalkAccountCallbackBodyBytes)+`","retryable":false}},"message_binding":{"status":"skipped"}}`),
	)
	req.Header.Set("Origin", "https://dbase.example.internal")
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
	req = withURLParams(req, "bindingId", "11111111-1111-1111-1111-111111111111")
	w := httptest.NewRecorder()
	h.CompleteDingTalkAccountBindingCallback(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	assertDingTalkAccountBindingErrorCode(t, w, "invalid_binding_result")
	if service.completeCalls != 0 {
		t.Fatalf("callback service calls = %d", service.completeCalls)
	}
}

func TestDingTalkAccountCallbackPublishesActivatedEvent(t *testing.T) {
	service := &fakeDingTalkAccountBindingService{
		completeResult: agentmessagerouter.CompleteBindingResult{
			Status: agentmessagerouter.DingTalkBindingCompletionStatus,
			Binding: agentmessagerouter.PublicDingTalkAccountBinding{
				ID:           "11111111-1111-1111-1111-111111111111",
				WorkspaceID:  "22222222-2222-2222-2222-222222222222",
				AgentID:      "33333333-3333-3333-3333-333333333333",
				MessageRoute: agentmessagerouter.PublicDingTalkBindingOutcome{Status: "active"},
			},
		},
	}
	bus := events.New()
	var published events.Event
	bus.Subscribe(protocol.EventDingTalkAccountBindingActivated, func(event events.Event) {
		published = event
	})
	h := &Handler{
		DingTalkAccountBindings:      service,
		DingTalkAccountBindingOrigin: "https://dbase.example.internal",
		Bus:                          bus,
	}
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/integrations/dingtalk/account-bindings/11111111-1111-1111-1111-111111111111/callback",
		strings.NewReader(validDingTalkBindingCallbackBody("message", "success", "source-1")),
	)
	req.Header.Set("Origin", "https://dbase.example.internal")
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
	req = withURLParams(req, "bindingId", "11111111-1111-1111-1111-111111111111")
	w := httptest.NewRecorder()

	h.CompleteDingTalkAccountBindingCallback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if published.Type != protocol.EventDingTalkAccountBindingActivated ||
		published.WorkspaceID != service.completeResult.Binding.WorkspaceID ||
		published.ActorType != "system" {
		t.Fatalf("published event = %#v", published)
	}
}

func TestDingTalkAccountCallbackForwardsMessageScopeAndConversations(t *testing.T) {
	service := &fakeDingTalkAccountBindingService{
		completeResult: agentmessagerouter.CompleteBindingResult{
			Status:          agentmessagerouter.DingTalkBindingCompletionStatus,
			IdentityBinding: agentmessagerouter.BindingTaskAcknowledgement{Status: agentmessagerouter.DingTalkBindingTaskStatusSkipped},
			MessageBinding:  agentmessagerouter.BindingTaskAcknowledgement{Status: agentmessagerouter.DingTalkBindingTaskStatusSuccess},
			Binding: agentmessagerouter.PublicDingTalkAccountBinding{
				ID:          "11111111-1111-1111-1111-111111111111",
				WorkspaceID: "22222222-2222-2222-2222-222222222222",
				AgentID:     "33333333-3333-3333-3333-333333333333",
			},
		},
	}
	h := &Handler{
		DingTalkAccountBindings:      service,
		DingTalkAccountBindingOrigin: "https://dbase.example.internal",
	}
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/integrations/dingtalk/account-bindings/11111111-1111-1111-1111-111111111111/callback",
		strings.NewReader(`{
			"binding_mode":"message",
			"status":"completed",
			"identity_binding":{
				"status":"skipped",
				"account_uid":null,
				"account_org_id":null,
				"account_organization_name":null,
				"account_display_name":null,
				"account_avatar_url":null,
				"error":null
			},
			"message_binding":{
				"status":"success",
				"source_id":"source-1",
				"message_scope":"custom",
				"conversations":[
					{"cid":"cid-alpha","name":"Project Alpha","avatar_media_id":"@media-alpha","avatar_url":"https://example.com/alpha.png"},
					{"cid":"cid-beta","name":"Project Beta"}
				],
				"subscriptions":[{"domain":"channel","source_id":"source-1","status":"active"}],
				"error":null
			}
		}`),
	)
	req.Header.Set("Origin", "https://dbase.example.internal")
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
	req = withURLParams(req, "bindingId", "11111111-1111-1111-1111-111111111111")
	w := httptest.NewRecorder()

	h.CompleteDingTalkAccountBindingCallback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	encoded, err := json.Marshal(service.completeParams)
	if err != nil {
		t.Fatal(err)
	}
	var forwarded map[string]any
	if err := json.Unmarshal(encoded, &forwarded); err != nil {
		t.Fatal(err)
	}
	identity, ok := forwarded["Identity"].(map[string]any)
	if !ok || identity["status"] != "skipped" || identity["account_uid"] != "" {
		t.Fatalf("forwarded identity = %#v params=%s", forwarded["Identity"], encoded)
	}
	if forwarded["BindingMode"] != string(agentmessagerouter.BindingModeMessage) {
		t.Fatalf("forwarded binding mode = %#v params=%s", forwarded["BindingMode"], encoded)
	}
	message, ok := forwarded["Message"].(map[string]any)
	if !ok || message["message_scope"] != "custom" {
		t.Fatalf("forwarded message = %#v params=%s", forwarded["Message"], encoded)
	}
	conversations, ok := message["conversations"].([]any)
	if !ok || len(conversations) != 2 {
		t.Fatalf("forwarded conversations = %#v params=%s", message["conversations"], encoded)
	}
	var response struct {
		Status          string `json:"status"`
		IdentityBinding struct {
			Status string `json:"status"`
		} `json:"identity_binding"`
		MessageBinding struct {
			Status string `json:"status"`
		} `json:"message_binding"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode normalized response: %v body=%s", err, w.Body.String())
	}
	if response.Status != "completed" || response.IdentityBinding.Status != "skipped" || response.MessageBinding.Status != "success" {
		t.Fatalf("normalized response = %#v body=%s", response, w.Body.String())
	}
}

func TestDingTalkAccountCallbackAcceptsFailedIdentityAndSkippedMessageAsTerminalResult(t *testing.T) {
	service := &fakeDingTalkAccountBindingService{
		completeResult: agentmessagerouter.CompleteBindingResult{
			Status:          agentmessagerouter.DingTalkBindingCompletionStatus,
			IdentityBinding: agentmessagerouter.BindingTaskAcknowledgement{Status: agentmessagerouter.DingTalkBindingTaskStatusFailed},
			MessageBinding:  agentmessagerouter.BindingTaskAcknowledgement{Status: agentmessagerouter.DingTalkBindingTaskStatusSkipped},
			Binding: agentmessagerouter.PublicDingTalkAccountBinding{
				ID:          "11111111-1111-1111-1111-111111111111",
				WorkspaceID: "22222222-2222-2222-2222-222222222222",
				AgentID:     "33333333-3333-3333-3333-333333333333",
			},
		},
	}
	h := &Handler{
		DingTalkAccountBindings:      service,
		DingTalkAccountBindingOrigin: "https://dbase.example.internal",
	}
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/integrations/dingtalk/account-bindings/11111111-1111-1111-1111-111111111111/callback",
		strings.NewReader(`{
			"binding_mode":"identity",
			"status":"completed",
			"identity_binding":{
				"status":"failed",
				"account_uid":null,
				"account_org_id":null,
				"account_organization_name":null,
				"account_display_name":null,
				"account_avatar_url":null,
				"error":{"code":"identity_lookup_failed","message":"identity unavailable","retryable":true}
			},
			"message_binding":{
				"status":"skipped",
				"message_scope":null,
				"conversations":[],
				"source_id":null,
				"subscriptions":[],
				"error":null
			}
		}`),
	)
	req.Header.Set("Origin", "https://dbase.example.internal")
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
	req = withURLParams(req, "bindingId", "11111111-1111-1111-1111-111111111111")
	w := httptest.NewRecorder()

	h.CompleteDingTalkAccountBindingCallback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"completed"`) ||
		!strings.Contains(w.Body.String(), `"status":"failed"`) ||
		!strings.Contains(w.Body.String(), `"status":"skipped"`) {
		t.Fatalf("normalized terminal response = %s", w.Body.String())
	}
}

func TestDingTalkAccountCallbackHasNoConversationCountLimit(t *testing.T) {
	service := &fakeDingTalkAccountBindingService{}
	h := &Handler{
		DingTalkAccountBindings:      service,
		DingTalkAccountBindingOrigin: "https://dbase.example.internal",
	}
	conversations := make([]map[string]string, 400)
	for i := range conversations {
		conversations[i] = map[string]string{
			"cid":  "cid-" + strconv.Itoa(i),
			"name": "Conversation " + strconv.Itoa(i),
		}
	}
	body, err := json.Marshal(map[string]any{
		"binding_mode": "message",
		"status":       "completed",
		"identity_binding": map[string]any{
			"status": "skipped",
		},
		"message_binding": map[string]any{
			"status":        "success",
			"source_id":     "source-1",
			"message_scope": "all",
			"conversations": conversations,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 16<<10 {
		t.Fatalf("test body = %d bytes, want larger than legacy limit", len(body))
	}
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/integrations/dingtalk/account-bindings/11111111-1111-1111-1111-111111111111/callback",
		strings.NewReader(string(body)),
	)
	req.Header.Set("Origin", "https://dbase.example.internal")
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
	req = withURLParams(req, "bindingId", "11111111-1111-1111-1111-111111111111")
	w := httptest.NewRecorder()

	h.CompleteDingTalkAccountBindingCallback(w, req)

	if w.Code != http.StatusOK || service.completeCalls != 1 {
		t.Fatalf("status = %d callback calls = %d body=%s", w.Code, service.completeCalls, w.Body.String())
	}
}

func TestUnbindDingTalkAccountPublishesRevokedEvent(t *testing.T) {
	service := &fakeDingTalkAccountBindingService{
		unbindResult: agentmessagerouter.PublicDingTalkAccountBinding{
			ID:           "11111111-1111-1111-1111-111111111111",
			WorkspaceID:  "22222222-2222-2222-2222-222222222222",
			AgentID:      "33333333-3333-3333-3333-333333333333",
			DWSIdentity:  agentmessagerouter.PublicDingTalkBindingOutcome{Status: "unbound"},
			MessageRoute: agentmessagerouter.PublicDingTalkBindingOutcome{Status: "revoked"},
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
		"/api/workspaces/22222222-2222-2222-2222-222222222222/dingtalk/account-bindings/33333333-3333-3333-3333-333333333333?binding_mode=message",
		nil,
	)
	req.Header.Set("X-User-ID", "44444444-4444-4444-4444-444444444444")
	req = withURLParams(
		req,
		"id", "22222222-2222-2222-2222-222222222222",
		"agentId", "33333333-3333-3333-3333-333333333333",
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

func validDingTalkBindingCallbackBody(bindingMode, messageStatus, sourceID string) string {
	identity := `{"status":"skipped","account_uid":"","account_org_id":"","account_organization_name":"","account_display_name":"","account_avatar_url":"","error":null}`
	if bindingMode == "identity" {
		identity = `{"status":"success","account_uid":"24710833","account_org_id":"439446171","account_organization_name":"Alibaba Group","account_display_name":"Xu Mo","account_avatar_url":"https://example.com/avatar.png","error":null}`
	}
	message := `{"status":"skipped","message_scope":"","conversations":[],"source_id":"","subscriptions":[],"error":null}`
	if messageStatus == "success" {
		message = `{"status":"success","message_scope":"direct_only","conversations":[],"source_id":"` + sourceID + `","subscriptions":[{"domain":"channel","source_id":"` + sourceID + `","status":"active"}],"error":null}`
	}
	return `{"binding_mode":"` + bindingMode + `","status":"completed","identity_binding":` + identity + `,"message_binding":` + message + `}`
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
