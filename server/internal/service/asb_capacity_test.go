package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestWaitForASBCapacityReleaseObservesSandboxAndQuota(t *testing.T) {
	t.Parallel()

	var getCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/sandbox-reclaimed":
			call := getCalls.Add(1)
			if call == 1 {
				_, _ = io.WriteString(response, `{"id":"sandbox-reclaimed","status":{"state":"Running"},"createdAt":"2026-08-03T05:00:00Z"}`)
				return
			}
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"code":"NOT_FOUND","message":"sandbox not found"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/quotas":
			usage := 5
			if getCalls.Load() > 1 {
				usage = 4
			}
			_, _ = io.WriteString(response, `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":5,"usage":`+strconv.Itoa(usage)+`}]`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client := newTestASBClient(t, server)

	if err := waitForASBCapacityRelease(
		context.Background(),
		client,
		"sandbox-reclaimed",
	); err != nil {
		t.Fatalf("waitForASBCapacityRelease: %v", err)
	}
	if getCalls.Load() < 2 {
		t.Fatalf("get calls = %d, want at least 2", getCalls.Load())
	}
}

func TestGetLiveASBSandboxTreatsNotFoundAsMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/missing-sandbox" {
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"code":"NOT_FOUND","message":"sandbox not found"}`)
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	client := newTestASBClient(t, server)

	sandbox, exists, err := getLiveASBSandbox(
		context.Background(),
		client,
		"missing-sandbox",
	)
	if err != nil {
		t.Fatalf("getLiveASBSandbox: %v", err)
	}
	if exists || sandbox != nil {
		t.Fatalf("sandbox = %#v, exists = %t", sandbox, exists)
	}
}

func TestIsActiveASBTaskStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"queued", "dispatched", "running", "waiting_local_directory", "deferred"} {
		if !isActiveASBTaskStatus(status) {
			t.Fatalf("status %q should fence its sandbox from reclaim", status)
		}
	}
	for _, status := range []string{"completed", "failed", "cancelled"} {
		if isActiveASBTaskStatus(status) {
			t.Fatalf("status %q should allow an untracked sandbox reclaim", status)
		}
	}
}
