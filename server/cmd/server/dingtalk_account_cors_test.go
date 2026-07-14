package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDingTalkAccountCallbackCORSUsesNarrowPolicy(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := dingTalkAccountCallbackCORSMiddleware(
		[]string{"https://app.multica.example"},
		"https://dbase.example.internal",
	)(next)

	req := httptest.NewRequest(http.MethodOptions, "/api/integrations/dingtalk/account-bindings/01900000-0000-7000-8000-000000000000/callback", nil)
	req.Header.Set("Origin", "https://dbase.example.internal")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://dbase.example.internal" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("callback must not allow credentials, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "POST" {
		t.Fatalf("Access-Control-Allow-Methods = %q", got)
	}
	if got := w.Header().Get("Vary"); got == "" {
		t.Fatal("callback CORS response must vary by Origin")
	}
}

func TestDingTalkAccountCallbackCORSRejectsOtherOrigins(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := dingTalkAccountCallbackCORSMiddleware(
		[]string{"https://app.multica.example"},
		"https://dbase.example.internal",
	)(next)

	req := httptest.NewRequest(http.MethodOptions, "/api/integrations/dingtalk/account-bindings/01900000-0000-7000-8000-000000000000/callback", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected Access-Control-Allow-Origin = %q", got)
	}
}

func TestDingTalkAccountCallbackCORSLeavesRegularAPIPolicyIntact(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := dingTalkAccountCallbackCORSMiddleware(
		[]string{"https://app.multica.example"},
		"https://dbase.example.internal",
	)(next)

	req := httptest.NewRequest(http.MethodOptions, "/api/workspaces", nil)
	req.Header.Set("Origin", "https://app.multica.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.multica.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("regular API credentials policy changed: %q", got)
	}
}

func TestDBaseBindingURLMustMatchCallbackOrigin(t *testing.T) {
	if !dBaseBindingURLMatchesOrigin(
		"https://dbase.example.internal/dingtalk/account-bind",
		"https://dbase.example.internal",
	) {
		t.Fatal("same DBase origin should match")
	}
	for _, bindingURL := range []string{
		"https://evil.example/dingtalk/account-bind",
		"http://dbase.example.internal/dingtalk/account-bind",
		"not-a-url",
	} {
		if dBaseBindingURLMatchesOrigin(bindingURL, "https://dbase.example.internal") {
			t.Fatalf("binding URL %q unexpectedly matched", bindingURL)
		}
	}
}
