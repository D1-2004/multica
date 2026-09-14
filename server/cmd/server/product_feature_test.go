package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProductFeaturePublishRequiresBearerToken(t *testing.T) {
	called := false
	handler := internalProductFeaturePublishHandler("operator-token", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	})

	request := httptest.NewRequest(http.MethodPost, "/api/internal/features/releases", nil)
	recorder := httptest.NewRecorder()
	handler(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("publish handler was called without a bearer token")
	}
}

func TestProductFeaturePublishAcceptsMatchingBearerToken(t *testing.T) {
	called := false
	handler := internalProductFeaturePublishHandler("operator-token", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	})

	request := httptest.NewRequest(http.MethodPost, "/api/internal/features/releases", nil)
	request.Header.Set("Authorization", "Bearer operator-token")
	recorder := httptest.NewRecorder()
	handler(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if !called {
		t.Fatal("publish handler was not called with a matching bearer token")
	}
}
