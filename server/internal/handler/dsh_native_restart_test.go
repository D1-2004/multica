package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeRestartRequiresSavedChangesAndManagedRuntime(t *testing.T) {
	const prefix = "/api/dsh-native/ui/test/"
	for _, route := range []string{"dsh-market/restart", "dsh-market/api/v1/restart"} {
		t.Run(route, func(t *testing.T) {
			forwarded := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded++
				if r.URL.Path != "/"+route {
					t.Errorf("wrong restart route %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusAccepted)
			}))
			defer upstream.Close()
			for _, failure := range []string{"unsupported Runtime", "snapshot unavailable", "save failed"} {
				r := httptest.NewRequest(http.MethodPost, prefix+route, nil)
				r.Header.Set("Origin", "https://pre.test")
				w := httptest.NewRecorder()
				if !serveDSHNativeRestart(w, r, upstream.URL, "https://pre.test", prefix, func() error { return errors.New(failure) }) || w.Code != http.StatusConflict || forwarded != 0 {
					t.Fatalf("restart escaped failed preparation: %s code=%d forwarded=%d", failure, w.Code, forwarded)
				}
			}
			r := httptest.NewRequest(http.MethodPost, prefix+route, nil)
			r.Header.Set("Origin", "https://pre.test")
			w := httptest.NewRecorder()
			serveDSHNativeRestart(w, r, upstream.URL, "https://pre.test", prefix, func() error { return nil })
			if w.Code != http.StatusAccepted || forwarded != 1 {
				t.Fatalf("prepared restart not forwarded: %d / %d", w.Code, forwarded)
			}
		})
	}
}

func TestNativeRestartRejectsInvalidRequestsBeforePreparation(t *testing.T) {
	const prefix = "/api/dsh-native/ui/test/"
	for _, tc := range []struct{ method, path, origin, upgrade string }{
		{"GET", "dsh-market/restart", "https://pre.test", ""},
		{"POST", "dsh-market/restart?force=true", "https://pre.test", ""},
		{"POST", "dsh-market/restart", "https://other.test", ""},
		{"POST", "dsh-market/restart", "https://pre.test", "websocket"},
	} {
		r := httptest.NewRequest(tc.method, prefix+tc.path, nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Upgrade", tc.upgrade)
		w := httptest.NewRecorder()
		serveDSHNativeRestart(w, r, "http://invalid", "https://pre.test", prefix, func() error { t.Fatal("invalid request reached preparation"); return nil })
		if w.Code != http.StatusForbidden {
			t.Fatalf("invalid request returned %d", w.Code)
		}
	}
	r := httptest.NewRequest("GET", prefix+"dsh-market/status", nil)
	if serveDSHNativeRestart(httptest.NewRecorder(), r, "", "", prefix, func() error { t.Fatal("read reached preparation"); return nil }) {
		t.Fatal("ordinary read intercepted")
	}
}
