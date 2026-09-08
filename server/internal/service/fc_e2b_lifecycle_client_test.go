package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFCE2BLifecycleClientRenewsAndReadsActualExpiry(t *testing.T) {
	end := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	renewed := time.Now().Add(55 * time.Minute).UTC().Truncate(time.Second)
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "test-key" {
			t.Error("missing FC authentication")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /sandboxes/sbx-test":
			_ = json.NewEncoder(w).Encode(map[string]any{"sandboxID": "sbx-test", "state": "running", "endAt": end})
		case "POST /sandboxes/sbx-test/timeout":
			var body struct { Timeout int `json:"timeout"` }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Timeout != 3600 {
				t.Errorf("timeout must be seconds: %+v %v", body, err)
			}
			posts++
			end = renewed
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := newFCE2BSandboxClient(FCE2BConfig{APIURL: server.URL, APIKey: "test-key"})
	info, err := client.ensureTTL(context.Background(), "sbx-test", 10*time.Minute, time.Hour)
	if err != nil || !info.EndAt.Equal(renewed) || posts != 1 {
		t.Fatalf("renewal did not preserve provider expiry: %+v posts=%d err=%v", info, posts, err)
	}
	if _, err := client.ensureTTL(context.Background(), "sbx-test", 10*time.Minute, time.Hour); err != nil || posts != 1 {
		t.Fatal("adequate remaining lifetime should skip renewal", err)
	}
}

func TestFCE2BLifecycleClientRejectsUnknownStateAndRedactsErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"secret":"never-log-this"}`))
			}))
			defer server.Close()
			client := newFCE2BSandboxClient(FCE2BConfig{APIURL: server.URL, APIKey: "test-key"})
			_, err := client.ensureTTL(context.Background(), "sbx-test", time.Minute, time.Hour)
			if err == nil || strings.Contains(err.Error(), "never-log-this") || strings.Contains(err.Error(), "test-key") {
				t.Fatalf("expected sanitized error, got %v", err)
			}
		})
	}
}
